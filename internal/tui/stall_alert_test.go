package tui

import (
	"net"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/probe"
)

// A round whose sends stalled raises no loss alert (viaduct#115). The tracer
// reports what it wrote; a round in which nothing left for the destination
// adds no probes, no loss, and is not a round the alert engine counts.

const stallTTLs = 3 // the destination answers at TTL 3

func stallNodeIP(ttl int) net.IP { return net.IPv4(192, 0, 2, byte(ttl)) }

// stallSim drives one session as its tracer would: probes marked sent on the
// table, replies and echo sends as messages, the round closed by RoundEndMsg.
type stallSim struct{ m Model }

// round is a round in which every TTL's probes left: one ICMP TTL probe and
// two flows, of which answered are answered, and one echo to each node.
func (s *stallSim) round(answered int, echoAnswered bool) {
	for ttl := 1; ttl <= stallTTLs; ttl++ {
		s.m.sessions[0].table.MarkRoundSentByType(ttl, 1, 2)
		for i := 0; i < answered; i++ {
			pt := probe.ProbeUDPTTL
			if i == 0 {
				pt = probe.ProbeICMPTTL
			}
			s.m, _ = s.m.onHopUpdate(HopUpdateMsg{Result: probe.Result{
				TTL: ttl, IP: stallNodeIP(ttl), RTT: time.Millisecond, FlowID: i,
				ProbeType: pt, IsTarget: ttl == stallTTLs,
			}})
		}
	}
	for ttl := 1; ttl <= stallTTLs; ttl++ {
		s.m, _ = s.m.onEchoSent(EchoSentMsg{IP: stallNodeIP(ttl), TTL: ttl})
		if echoAnswered {
			s.m, _ = s.m.onHopUpdate(HopUpdateMsg{Result: probe.Result{
				TTL: ttl, IP: stallNodeIP(ttl), RTT: time.Millisecond, ProbeType: probe.ProbeICMPEcho,
			}})
		}
	}
	s.m, _ = s.m.onRoundEnd(RoundEndMsg{})
}

// stalledRound is a round in which no send succeeded: the tracer reports
// nothing written for every TTL, sends no echo, and ends the round.
func (s *stallSim) stalledRound() {
	for ttl := 1; ttl <= stallTTLs; ttl++ {
		s.m.sessions[0].table.MarkRoundSentByType(ttl, 0, 0)
	}
	s.m, _ = s.m.onRoundEnd(RoundEndMsg{})
}

func (s *stallSim) alert() AlertState { return s.m.sessions[0].alertEngine.State() }

func TestStalledRounds_HealthyPathRaisesNoAlert(t *testing.T) {
	s := &stallSim{m: newTestModel(withAlertEngine(5, 0, 3))}
	for i := 0; i < 25; i++ {
		s.round(3, true)
	}
	dest := s.m.sessions[0].table.Get(stallTTLs)
	sentBefore, recvdBefore := dest.ProbeCounts()
	rndBefore := dest.Rounds()

	// The socket stops accepting writes for 20 rounds.
	for i := 0; i < 20; i++ {
		s.stalledRound()
		if s.alert() != AlertHealthy {
			t.Fatalf("stalled round %d raised an alert: %q", i+1, s.m.sessions[0].alertEngine.Message())
		}
	}

	for ttl := 1; ttl <= stallTTLs; ttl++ {
		h := s.m.sessions[0].table.Get(ttl)
		if pct, kind := h.Loss(); pct != 0 {
			t.Errorf("TTL %d: loss = %.1f%% (kind %v) after stalled rounds, want 0: probes that did not leave are not lost", ttl, pct, kind)
		}
	}
	if sent, recvd := dest.ProbeCounts(); sent != sentBefore || recvd != recvdBefore {
		t.Errorf("destination probes = %d sent, %d received; want the %d, %d from before the stall", sent, recvd, sentBefore, recvdBefore)
	}
	if got := dest.Rounds(); got != rndBefore {
		t.Errorf("destination Rnd = %d, want %d: no round was sent to it", got, rndBefore)
	}
}

// With real loss already counting toward an alert, rounds that sent nothing
// are not further evidence: they neither complete the count nor reset it.
func TestRoundsWithNothingSent_DoNotAdvanceTheAlert(t *testing.T) {
	for _, tc := range []struct {
		name string
		idle func(*stallSim)
	}{
		{"sends stalled", func(s *stallSim) { s.stalledRound() }},
		{"paused", func(s *stallSim) { s.m, _ = s.m.onRoundEnd(RoundEndMsg{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Alert above 35% loss for 3 rounds.
			s := &stallSim{m: newTestModel(withAlertEngine(35, 0, 3))}
			// 20 rounds losing a third of both the TTL probes and the echoes:
			// real loss (not rate limiting), under the threshold.
			for i := 0; i < 20; i++ {
				s.round(2, i%3 != 0)
			}
			if s.alert() != AlertHealthy {
				t.Fatalf("fixture: alert raised during warm-up: %q", s.m.sessions[0].alertEngine.Message())
			}
			// Two rounds with everything lost take the destination over the
			// threshold: two bad rounds, one short of an alert.
			s.round(0, false)
			s.round(0, false)
			dest := s.m.sessions[0].table.Get(stallTTLs)
			if got := dest.SettledLoss(); got <= 35 {
				t.Fatalf("fixture: destination loss = %.1f%%, want over 35", got)
			}
			if s.alert() != AlertHealthy {
				t.Fatal("fixture: alert raised after two bad rounds, want three")
			}

			for i := 0; i < 10; i++ {
				tc.idle(s)
				if s.alert() != AlertHealthy {
					t.Fatalf("round %d, in which nothing was sent, raised the alert: %q", i+1, s.m.sessions[0].alertEngine.Message())
				}
			}

			// The third round of evidence raises it.
			s.round(0, false)
			if s.alert() != AlertDegraded {
				t.Error("no alert after a third round of real loss: rounds with nothing sent must not reset the count")
			}
		})
	}
}
