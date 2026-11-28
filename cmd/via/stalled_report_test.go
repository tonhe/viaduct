package main

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/export"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/probe"
)

// Report mode with a socket that stops accepting writes (viaduct#115): the
// rounds in which nothing left are not sent, not lost, and not an error.

// stalledTracer stands in for a tracer whose sends all fail after the first
// round. It reports to the round marker what probe.Tracer reports: the probes
// written for each TTL, by type, which is nothing once the socket has stalled.
type stalledTracer struct {
	results    []probe.Result
	stalled    int // rounds after the first, in each of which nothing leaves
	onRound    probe.RoundMarker
	onRoundEnd func()
	byType     bool // the round marker took the per-type report
}

func (s *stalledTracer) SetOnRoundSent(r probe.RoundMarker)   { s.onRound = r }
func (s *stalledTracer) SetOnRoundEnd(f func())               { s.onRoundEnd = f }
func (s *stalledTracer) SetOnEchoSent(func(net.IP, int))      {}
func (s *stalledTracer) SubmitEchoTargets([]probe.EchoTarget) {}

func (s *stalledTracer) mark(ttl, icmpTTL, primary int) {
	if m, ok := s.onRound.(probe.RoundTypeMarker); ok {
		s.byType = true
		m.MarkRoundSentByType(ttl, icmpTTL, primary)
		return
	}
	s.onRound.MarkRoundSent(ttl, icmpTTL+primary)
}

func (s *stalledTracer) Run(_ context.Context, out chan<- probe.Result) error {
	// Round 1: one ICMP TTL probe and two flows to each TTL, all answered.
	for _, r := range s.results {
		if r.ProbeType == probe.ProbeICMPTTL {
			s.mark(r.TTL, 1, 2)
		}
		out <- r
	}
	// The collector applies results on its own goroutine; the round is closed
	// once the last one is in the table.
	tbl := s.onRound.(*hop.Table)
	last := s.results[len(s.results)-1]
	deadline := time.Now().Add(2 * time.Second)
	for {
		if h := tbl.Get(last.TTL); h != nil && len(out) == 0 {
			if n := h.PrimaryNode(); n != nil && n.GetReceived() == 3 {
				break
			}
		}
		if time.Now().After(deadline) {
			panic("stalledTracer: the collector did not apply the first round's results")
		}
		time.Sleep(time.Millisecond)
	}
	s.onRoundEnd()

	// The socket stalls: every send of every later round fails or is skipped.
	for i := 0; i < s.stalled; i++ {
		for ttl := 1; ttl <= last.TTL; ttl++ {
			s.mark(ttl, 0, 0)
		}
		s.onRoundEnd()
	}
	return nil
}

func stalledResults(hops int) []probe.Result {
	var out []probe.Result
	for ttl := 1; ttl <= hops; ttl++ {
		ip := net.IPv4(192, 0, 2, byte(ttl))
		for i, pt := range []probe.ProbeType{probe.ProbeICMPTTL, probe.ProbeUDPTTL, probe.ProbeUDPTTL} {
			out = append(out, probe.Result{TTL: ttl, IP: ip, RTT: time.Millisecond, FlowID: i, ProbeType: pt, IsTarget: ttl == hops})
		}
	}
	return out
}

func TestRunReport_StalledRoundsAreNotSentNotLostNotAnError(t *testing.T) {
	const hops = 3
	stub := &stalledTracer{results: stalledResults(hops), stalled: 9}
	orig := newTracer
	newTracer = func(net.IP, probe.Config) tracerIface { return stub }
	t.Cleanup(func() { newTracer = orig })

	cfg := probe.Config{MaxHops: 30, FirstTTL: 1, Timeout: 100 * time.Millisecond, MaxRounds: 10, NumPaths: 2, IPVersion: 4, NoDNS: true}
	cfg.Protocol = probe.NewUDPProtocol(33434)
	target := net.IPv4(192, 0, 2, hops)

	var runErr error
	out := captureStdout(t, func() {
		runErr = runReport("192.0.2.3", target, cfg, "udp", reportOpts{noASN: true}, "json")
	})
	// Report mode decides its exit status on errors only; a stall is not one.
	if runErr != nil {
		t.Fatalf("runReport: %v, want no error for rounds whose sends stalled", runErr)
	}
	if !stub.byType {
		t.Fatal("report mode's round marker does not take the tracer's per-type report (probe.RoundTypeMarker)")
	}

	var report export.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("JSON: %v\n%s", err, out)
	}
	if len(report.Hops) != hops {
		t.Fatalf("%d hops, want %d", len(report.Hops), hops)
	}
	for _, h := range report.Hops {
		if h.ProbesSent != 3 || h.Sent != 3 || h.ProbesReceived != 3 {
			t.Errorf("TTL %d: probes_sent, sent, probes_received = %d, %d, %d; want 3, 3, 3: the 27 probes of the stalled rounds did not leave",
				h.TTL, h.ProbesSent, h.Sent, h.ProbesReceived)
		}
		if h.Loss != 0 || h.HeadlineLoss != 0 || h.LossKind != "measured" {
			t.Errorf("TTL %d: loss_pct %v, headline_loss_pct %v, loss_kind %q; want 0, 0, measured", h.TTL, h.Loss, h.HeadlineLoss, h.LossKind)
		}
		if h.Rounds != 1 {
			t.Errorf("TTL %d: rounds = %d, want 1", h.TTL, h.Rounds)
		}
		n := h.Nodes[0]
		if n.Probes.ICMPTTL.Sent != 1 || n.Probes.Primary.Sent != 2 {
			t.Errorf("TTL %d: icmp_ttl sent %d, primary sent %d; want 1, 2", h.TTL, n.Probes.ICMPTTL.Sent, n.Probes.Primary.Sent)
		}
	}
	if report.Probes != 3*hops {
		t.Errorf("probes = %d, want %d", report.Probes, 3*hops)
	}
}

// The text report shows the same: no loss for rounds that sent nothing.
func TestRunReport_StalledRounds_TextReportShowsNoLoss(t *testing.T) {
	const hops = 3
	stub := &stalledTracer{results: stalledResults(hops), stalled: 9}
	orig := newTracer
	newTracer = func(net.IP, probe.Config) tracerIface { return stub }
	t.Cleanup(func() { newTracer = orig })

	cfg := probe.Config{MaxHops: 30, FirstTTL: 1, Timeout: 100 * time.Millisecond, MaxRounds: 10, NumPaths: 2, IPVersion: 4, NoDNS: true}
	cfg.Protocol = probe.NewUDPProtocol(33434)

	out := captureStdout(t, func() {
		if err := runReport("192.0.2.3", net.IPv4(192, 0, 2, hops), cfg, "udp", reportOpts{noASN: true}, ""); err != nil {
			t.Errorf("runReport: %v", err)
		}
	})
	rows := 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.HasPrefix(f[1], "192.0.2.") {
			continue
		}
		rows++
		if !strings.Contains(line, " 0.0% ") {
			t.Errorf("hop row shows loss for rounds that were never sent: %q", line)
		}
	}
	if rows != hops {
		t.Errorf("%d hop rows, want %d:\n%s", rows, hops, out)
	}
}
