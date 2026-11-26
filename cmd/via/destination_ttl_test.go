package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/export"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/probe"
)

// Report mode and the destination's TTL when the target answers at more than
// one (viaduct#234): the text report, the JSON export and the debug log must
// name the same destination hop.

const scriptTarget = "192.0.2.1"

// scriptedProbe is one TTL-limited probe of a round: its TTL, type and flow,
// and the address that answered it ("" when nothing did).
type scriptedProbe struct {
	ttl  int
	pt   probe.ProbeType
	flow int
	from string
}

// scriptedTracer stands in for probe.Tracer: it plays rounds of probes into
// the round marker and the results channel as the tracer does, and closes
// each round once the collector has applied its results.
type scriptedTracer struct {
	rounds     [][]scriptedProbe
	onRound    probe.RoundMarker
	onRoundEnd func()
}

func (s *scriptedTracer) SetOnRoundSent(r probe.RoundMarker)   { s.onRound = r }
func (s *scriptedTracer) SetOnRoundEnd(f func())               { s.onRoundEnd = f }
func (s *scriptedTracer) SetOnEchoSent(func(net.IP, int))      {}
func (s *scriptedTracer) SubmitEchoTargets([]probe.EchoTarget) {}

func (s *scriptedTracer) Run(_ context.Context, out chan<- probe.Result) error {
	tbl := s.onRound.(*hop.Table)
	answered := map[int]int{} // TTL -> replies delivered so far
	for _, round := range s.rounds {
		sent := map[int][2]int{}
		for _, p := range round {
			c := sent[p.ttl]
			if p.pt == probe.ProbeICMPTTL {
				c[0]++
			} else {
				c[1]++
			}
			sent[p.ttl] = c
		}
		for ttl, c := range sent {
			s.onRound.(probe.RoundTypeMarker).MarkRoundSentByType(ttl, c[0], c[1])
		}
		for _, p := range round {
			if p.from == "" {
				continue
			}
			answered[p.ttl]++
			out <- probe.Result{TTL: p.ttl, IP: net.ParseIP(p.from), RTT: time.Millisecond,
				IsTarget: p.from == scriptTarget, FlowID: p.flow, ProbeType: p.pt}
		}
		// The collector applies results on its own goroutine: wait for it.
		deadline := time.Now().Add(5 * time.Second)
		for !applied(tbl, answered) {
			if time.Now().After(deadline) {
				panic("scriptedTracer: the collector did not apply the round's results")
			}
			time.Sleep(time.Millisecond)
		}
		s.onRoundEnd()
	}
	return nil
}

func applied(tbl *hop.Table, answered map[int]int) bool {
	for ttl, want := range answered {
		h := tbl.Get(ttl)
		if h == nil {
			return false
		}
		got := 0
		for _, n := range h.GetNodes() {
			got += n.GetReceived()
		}
		if got != want {
			return false
		}
	}
	return true
}

func installScript(t *testing.T, rounds [][]scriptedProbe) {
	t.Helper()
	orig := newTracer
	newTracer = func(net.IP, probe.Config) tracerIface { return &scriptedTracer{rounds: rounds} }
	t.Cleanup(func() { newTracer = orig })
}

type lineSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *lineSink) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func scriptRouter(ttl int) string { return fmt.Sprintf("198.51.100.%d", ttl) }

// strayScript: ICMP mode, routers at TTL 1 to 8, the target at TTL 9. In
// round one the probe sent with TTL 6 reaches the target, not the router.
// Round one probes TTL 1 to 12; the routers that answer at TTL 7 and 8 place
// the destination beyond them, so the tracer probes up to TTL 9 from round
// two on, as probe.Tracer does.
func strayScript(rounds int) [][]scriptedProbe {
	var out [][]scriptedProbe
	for r := 0; r < rounds; r++ {
		upTo := 9
		if r == 0 {
			upTo = 12
		}
		var round []scriptedProbe
		for ttl := 1; ttl <= upTo; ttl++ {
			p := scriptedProbe{ttl: ttl, pt: probe.ProbeICMPTTL, from: scriptRouter(ttl)}
			if ttl >= 9 || (r == 0 && ttl == 6) {
				p.from = scriptTarget
			}
			round = append(round, p)
		}
		out = append(out, round)
	}
	return out
}

// unequalScript: the ICMP TTL probe and three UDP flows. The target answers
// flow 2 at TTL 3 and every probe at TTL 4; a router answers the others at
// TTL 3.
func unequalScript(rounds int) [][]scriptedProbe {
	var out [][]scriptedProbe
	for r := 0; r < rounds; r++ {
		upTo := 4
		if r == 0 {
			upTo = 6
		}
		var round []scriptedProbe
		for ttl := 1; ttl <= upTo; ttl++ {
			for i := -1; i < 3; i++ {
				p := scriptedProbe{ttl: ttl, pt: probe.ProbeUDPTTL, flow: i, from: scriptRouter(ttl)}
				if i < 0 {
					p.pt, p.flow = probe.ProbeICMPTTL, 0
				}
				if ttl >= 4 || (ttl == 3 && i == 2) {
					p.from = scriptTarget
				}
				round = append(round, p)
			}
		}
		out = append(out, round)
	}
	return out
}

func scriptCfg(protocol string, rounds int, sink *lineSink) probe.Config {
	cfg := probe.Config{MaxHops: 30, FirstTTL: 1, Timeout: 100 * time.Millisecond, MaxRounds: rounds, NumPaths: 3, IPVersion: 4, NoDNS: true}
	if protocol == "icmp" {
		cfg.Protocol = probe.NewICMPProtocol(cfg)
	} else {
		cfg.Protocol = probe.NewUDPProtocol(33434)
	}
	if sink != nil {
		cfg.Debugf = sink.logf
	}
	return cfg
}

// debugDestination returns the destination's TTL as the debug log's final
// snapshot gives it: the highest TTL that is not marked beyond=yes.
func debugDestination(t *testing.T, lines []string) int {
	t.Helper()
	destTTL := 0
	for _, line := range lines {
		i := strings.Index(line, "] final round=")
		if i < 0 {
			continue
		}
		var round, ttl int
		if _, err := fmt.Sscanf(line[i:], "] final round=%d ttl=%d", &round, &ttl); err != nil {
			t.Fatalf("debug log line %q: %v", line, err)
		}
		if !strings.HasSuffix(line, " beyond=yes") && ttl > destTTL {
			destTTL = ttl
		}
	}
	return destTTL
}

// textRows returns the hop rows of the text report: TTL -> the row.
func textRows(out string) map[int]string {
	rows := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		var ttl int
		if _, err := fmt.Sscanf(line, "%d ", &ttl); err == nil && ttl > 0 {
			if _, dup := rows[ttl]; !dup {
				rows[ttl] = line
			}
		}
	}
	return rows
}

// A short run, so that the report is right from the first round on: a run
// that ended before the destination had been corrected would report the
// stray's TTL.
func TestRunReport_StrayReplyDoesNotPinTheDestination(t *testing.T) {
	const rounds = 3
	for _, format := range []string{"json", ""} {
		t.Run("format="+format, func(t *testing.T) {
			installScript(t, strayScript(rounds))
			sink := &lineSink{}
			var runErr error
			out := captureStdout(t, func() {
				runErr = runReport("target.example", net.ParseIP(scriptTarget), scriptCfg("icmp", rounds, sink), "icmp", true, format)
			})
			if runErr != nil {
				t.Fatalf("runReport: %v", runErr)
			}
			if got := debugDestination(t, sink.lines); got != 9 {
				t.Errorf("debug log: destination at TTL %d, want 9", got)
			}
			for _, l := range sink.lines {
				if strings.Contains(l, "destination moved") {
					t.Errorf("debug log: the destination moved, want it at TTL 9 from round one: %q", l)
				}
			}

			if format == "" {
				rows := textRows(out)
				if len(rows) != 9 {
					t.Fatalf("text report has %d hop rows, want 9:\n%s", len(rows), out)
				}
				if !strings.Contains(rows[9], scriptTarget+" ") || !strings.Contains(rows[9], " 0.0% ") {
					t.Errorf("text report: destination row = %q, want the target with 0.0%% loss", rows[9])
				}
				return
			}
			var report export.Report
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatalf("JSON: %v\n%s", err, out)
			}
			if len(report.Hops) != 9 {
				t.Fatalf("JSON: %d hops, want 9 (the destination at TTL 9)", len(report.Hops))
			}
			last := report.Hops[8]
			if len(last.Nodes) != 1 || last.Nodes[0].IP != scriptTarget {
				t.Fatalf("JSON: last hop's nodes = %+v, want the target alone", last.Nodes)
			}
			// TTL 9 was probed in every round and answered every time.
			if want := rounds; last.ProbesSent != want || last.ProbesReceived != want || last.Loss != 0 || last.HeadlineLoss != 0 {
				t.Errorf("JSON: destination sent/received %d/%d, loss %.1f, headline %.1f; want %d/%d and no loss",
					last.ProbesSent, last.ProbesReceived, last.Loss, last.HeadlineLoss, want, want)
			}
			if n := last.Nodes[0]; n.RateLimited == "yes" || last.LossKind == "rate_limited" {
				t.Errorf("JSON: destination rate_limited %q, loss_kind %q; want not rate-limited", n.RateLimited, last.LossKind)
			}
			// TTL 6 is an ordinary hop that lists the target beside its router.
			h6 := report.Hops[5]
			listed := false
			for _, n := range h6.Nodes {
				listed = listed || n.IP == scriptTarget
			}
			if h6.ProbesSent != rounds || h6.ProbesReceived != rounds || len(h6.Nodes) != 2 || !listed {
				t.Errorf("JSON: TTL 6 sent/received = %d/%d with nodes %+v, want %d/%d, the router and the target", h6.ProbesSent, h6.ProbesReceived, h6.Nodes, rounds, rounds)
			}
		})
	}
}

// A reply lost in round one is not a path change. ICMP mode, one flow, a
// stable path with the target at TTL 10; round one probes every TTL and the
// reply to the probe sent with TTL 11 is lost. A three-round report has ten
// rows and ends on the target with no loss.
func TestRunReport_LostReplyInRoundOneDoesNotMoveTheDestination(t *testing.T) {
	const rounds = 3
	var script [][]scriptedProbe
	for r := 0; r < rounds; r++ {
		upTo := 10
		if r == 0 {
			upTo = 30
		}
		var round []scriptedProbe
		for ttl := 1; ttl <= upTo; ttl++ {
			p := scriptedProbe{ttl: ttl, pt: probe.ProbeICMPTTL, from: scriptRouter(ttl)}
			if ttl >= 10 {
				p.from = scriptTarget
			}
			if ttl == 11 {
				p.from = ""
			}
			round = append(round, p)
		}
		script = append(script, round)
	}
	for _, format := range []string{"json", ""} {
		t.Run("format="+format, func(t *testing.T) {
			installScript(t, script)
			sink := &lineSink{}
			var runErr error
			out := captureStdout(t, func() {
				runErr = runReport("target.example", net.ParseIP(scriptTarget), scriptCfg("icmp", rounds, sink), "icmp", true, format)
			})
			if runErr != nil {
				t.Fatalf("runReport: %v", runErr)
			}
			if got := debugDestination(t, sink.lines); got != 10 {
				t.Errorf("debug log: destination at TTL %d, want 10", got)
			}
			if format == "" {
				rows := textRows(out)
				if len(rows) != 10 {
					t.Fatalf("text report has %d hop rows, want 10:\n%s", len(rows), out)
				}
				if !strings.Contains(rows[10], scriptTarget+" ") || !strings.Contains(rows[10], " 0.0% ") {
					t.Errorf("text report: destination row = %q, want the target with 0.0%% loss", rows[10])
				}
				return
			}
			var report export.Report
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatalf("JSON: %v\n%s", err, out)
			}
			if len(report.Hops) != 10 {
				t.Fatalf("JSON: %d hops, want 10 (the destination at TTL 10)", len(report.Hops))
			}
			last := report.Hops[9]
			if last.ProbesSent != rounds || last.ProbesReceived != rounds || last.HeadlineLoss != 0 {
				t.Errorf("JSON: destination sent/received %d/%d, headline %.1f; want %d/%d and no loss",
					last.ProbesSent, last.ProbesReceived, last.HeadlineLoss, rounds, rounds)
			}
		})
	}
}

func TestRunReport_UnequalPathLengths(t *testing.T) {
	const rounds = 6
	for _, format := range []string{"json", ""} {
		t.Run("format="+format, func(t *testing.T) {
			installScript(t, unequalScript(rounds))
			sink := &lineSink{}
			var runErr error
			out := captureStdout(t, func() {
				runErr = runReport("target.example", net.ParseIP(scriptTarget), scriptCfg("udp", rounds, sink), "udp", true, format)
			})
			if runErr != nil {
				t.Fatalf("runReport: %v", runErr)
			}
			if got := debugDestination(t, sink.lines); got != 4 {
				t.Errorf("debug log: destination at TTL %d, want 4", got)
			}

			if format == "" {
				rows := textRows(out)
				if len(rows) != 4 {
					t.Fatalf("text report has %d hop rows, want 4:\n%s", len(rows), out)
				}
				if !strings.Contains(rows[4], scriptTarget+" ") || !strings.Contains(rows[4], " 0.0% ") {
					t.Errorf("text report: destination row = %q, want the target with 0.0%% loss", rows[4])
				}
				if !strings.Contains(rows[3], scriptRouter(3)+" ") {
					t.Errorf("text report: TTL 3 row = %q, want the router as its headline", rows[3])
				}
				return
			}
			var report export.Report
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatalf("JSON: %v\n%s", err, out)
			}
			if len(report.Hops) != 4 {
				t.Fatalf("JSON: %d hops, want 4 (the destination at TTL 4)", len(report.Hops))
			}
			last := report.Hops[3]
			if len(last.Nodes) != 1 || last.Nodes[0].IP != scriptTarget || last.ProbesSent != 4*rounds || last.ProbesReceived != 4*rounds {
				t.Errorf("JSON: destination hop = %+v, want the target alone with %d of %d probes answered", last, 4*rounds, 4*rounds)
			}
			h3 := report.Hops[2]
			ips := map[string]bool{}
			for _, n := range h3.Nodes {
				ips[n.IP] = true
			}
			if len(h3.Nodes) != 2 || h3.Nodes[0].IP != scriptRouter(3) || !ips[scriptTarget] {
				t.Errorf("JSON: TTL 3 nodes = %+v, want the router first and the target listed", h3.Nodes)
			}
		})
	}
}

func TestPathMaxTTL(t *testing.T) {
	table := hop.NewTable(30)
	if got := pathMaxTTL(table); got != 0 {
		t.Errorf("empty table: %d, want 0", got)
	}
	router, target := net.ParseIP("198.51.100.1"), net.ParseIP(scriptTarget)
	for ttl := 1; ttl <= 5; ttl++ {
		table.MarkRoundSentByType(ttl, 1, 0)
	}
	recordResult(table, probe.Result{TTL: 1, IP: router, ProbeType: probe.ProbeICMPTTL})
	if got := pathMaxTTL(table); got != 5 {
		t.Errorf("target not answered: %d, want 5 (the highest TTL seen)", got)
	}
	for ttl := 2; ttl <= 5; ttl++ {
		recordResult(table, probe.Result{TTL: ttl, IP: target, ProbeType: probe.ProbeICMPTTL, IsTarget: true})
	}
	if got := pathMaxTTL(table); got != 2 {
		t.Errorf("target answered at TTL 2 to 5: %d, want 2", got)
	}
}
