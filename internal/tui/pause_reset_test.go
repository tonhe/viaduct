package tui

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/probe"
)

// Lab scenario 11 (#62): pausing and resetting must not corrupt probe counts.
// The tracers are simulated at the seams they use in production: each marks
// a TTL sent on its session's table directly (probe.RoundMarker), and its
// replies, echo sends and round ends reach the model as messages. Pause and
// reset are key presses on the model.
//
// The simulated tracer sends no echoes while paused. The real one still does
// (see the found issue on #62); these tests cover the TTL-limited counts,
// Rnd, and the per-node stats of rounds that were sent.

const (
	prTTLs         = 3
	prProbesPerTTL = 3 // one ICMP TTL probe and two primary flows
)

func prNodeIP(ttl int) net.IP { return net.ParseIP(fmt.Sprintf("192.0.2.%d", ttl)) }

// prAnswered is how many of a TTL's probes are answered in a round: a fixed
// pattern with some loss, so the counts are not trivially all-answered.
func prAnswered(round, ttl int) int {
	return prProbesPerTTL - (round+ttl)%3/2 - (round*ttl)%5/4
}

// prSim drives a model's sessions as their tracers would.
type prSim struct {
	m       Model
	targets []int
}

// sendTTL is what a tracer does for one TTL: nothing while paused,
// otherwise probes out and marked sent.
func (s *prSim) sendTTL(idx, ttl int) bool {
	if s.m.paused {
		return false
	}
	s.m.sessions[idx].table.MarkRoundSent(ttl, prProbesPerTTL)
	return true
}

func (s *prSim) replies(idx, round, ttl int) {
	for i := 0; i < prAnswered(round, ttl); i++ {
		pt := probe.ProbeUDPTTL
		if i == 0 {
			pt = probe.ProbeICMPTTL
		}
		s.m, _ = s.m.onHopUpdate(HopUpdateMsg{TargetIdx: idx, Result: probe.Result{
			TTL: ttl, IP: prNodeIP(ttl), RTT: time.Duration(ttl) * time.Millisecond,
			FlowID: i, ProbeType: pt,
		}})
	}
}

// echoes sends one echo to each TTL's node, answered except at TTL 2 on odd
// rounds.
func (s *prSim) echoes(idx, round int) {
	if s.m.paused {
		return
	}
	for ttl := 1; ttl <= prTTLs; ttl++ {
		s.m, _ = s.m.onEchoSent(EchoSentMsg{TargetIdx: idx, IP: prNodeIP(ttl), TTL: ttl})
		if ttl == 2 && round%2 == 1 {
			continue
		}
		s.m, _ = s.m.onHopUpdate(HopUpdateMsg{TargetIdx: idx, Result: probe.Result{
			TTL: ttl, IP: prNodeIP(ttl), RTT: time.Millisecond, ProbeType: probe.ProbeICMPEcho,
		}})
	}
}

func (s *prSim) key(k string) {
	s.m, _ = s.m.onMainKey(keyPress(k))
}

func (s *prSim) closeRound() {
	for _, idx := range s.targets {
		s.m, _ = s.m.onRoundEnd(RoundEndMsg{TargetIdx: idx})
	}
}

// round runs one full probe round on every target (or, while paused, a
// round in which nothing is sent).
func (s *prSim) round(n int) {
	for ttl := 1; ttl <= prTTLs; ttl++ {
		for _, idx := range s.targets {
			if s.sendTTL(idx, ttl) {
				s.replies(idx, n, ttl)
			}
		}
	}
	for _, idx := range s.targets {
		s.echoes(idx, n)
	}
	s.closeRound()
}

// partialRound sends TTLs 1 to upTo on every target, then presses p if
// pause is set, then closes the round. No echoes go out: with pause set the
// tracer is paused by the time they would; without it the run is the
// reference for one that was.
func (s *prSim) partialRound(n, upTo int, pause bool) {
	for ttl := 1; ttl <= upTo; ttl++ {
		for _, idx := range s.targets {
			if s.sendTTL(idx, ttl) {
				s.replies(idx, n, ttl)
			}
		}
	}
	if pause {
		s.key("p")
	}
	s.closeRound()
}

// resetRound sends every TTL on every target, presses r while all of the
// round's replies and echoes are in flight, then lets them arrive and closes
// the round.
func (s *prSim) resetRound(n int) {
	for ttl := 1; ttl <= prTTLs; ttl++ {
		for _, idx := range s.targets {
			s.sendTTL(idx, ttl)
		}
	}
	s.key("r")
	for _, idx := range s.targets {
		for ttl := 1; ttl <= prTTLs; ttl++ {
			s.replies(idx, n, ttl)
		}
		s.echoes(idx, n)
	}
	s.closeRound()
}

type prHopCounts struct {
	sent, recvd, rounds int
	icmp, primary, echo hop.ProbeStats
}

func prCounts(s *TargetSession) []prHopCounts {
	var out []prHopCounts
	for ttl := 1; ttl <= prTTLs; ttl++ {
		h := s.table.Get(ttl)
		if h == nil {
			out = append(out, prHopCounts{})
			continue
		}
		c := prHopCounts{rounds: h.Rounds()}
		c.sent, c.recvd = h.ProbeCounts()
		if n := h.PrimaryNode(); n != nil {
			c.icmp, c.primary, c.echo = n.ICMPTTLStats(), n.PrimaryStats(), n.EchoStats()
		}
		out = append(out, c)
	}
	return out
}

func prSame(a, b hop.ProbeStats) bool {
	return a.Sent == b.Sent && a.Received == b.Received &&
		a.RecvRounds == b.RecvRounds && a.ProbesSent == b.ProbesSent
}

// prCompare checks every target's counts in got against the reference run.
func prCompare(t *testing.T, got, want *prSim) {
	t.Helper()
	for _, idx := range got.targets {
		gs, ws := got.m.sessions[idx], want.m.sessions[idx]
		if gs.probeCount != ws.probeCount {
			t.Errorf("target %d: probeCount = %d, want %d", idx, gs.probeCount, ws.probeCount)
		}
		g, w := prCounts(gs), prCounts(ws)
		for i := range g {
			ttl := i + 1
			if w[i].sent == 0 {
				t.Errorf("target %d TTL %d: reference run sent nothing", idx, ttl)
			}
			if g[i].sent != w[i].sent || g[i].recvd != w[i].recvd || g[i].rounds != w[i].rounds {
				t.Errorf("target %d TTL %d: sent/recvd/Rnd = %d/%d/%d, want %d/%d/%d",
					idx, ttl, g[i].sent, g[i].recvd, g[i].rounds, w[i].sent, w[i].recvd, w[i].rounds)
			}
			for _, c := range []struct {
				name      string
				got, want hop.ProbeStats
			}{
				{"ICMP TTL", g[i].icmp, w[i].icmp},
				{"primary", g[i].primary, w[i].primary},
				{"echo", g[i].echo, w[i].echo},
			} {
				if !prSame(c.got, c.want) {
					t.Errorf("target %d TTL %d %s stats = %+v, want %+v", idx, ttl, c.name, c.got, c.want)
				}
			}
		}
	}
}

// prCases are the single- and multi-target models. Session 2 of
// multiTestModel failed to start; it is pressed through the keys but sends
// nothing.
var prCases = []struct {
	name   string
	newSim func(t *testing.T) *prSim
}{
	{"single target", func(t *testing.T) *prSim {
		return &prSim{m: newTestModel(), targets: []int{0}}
	}},
	{"multi target", func(t *testing.T) *prSim {
		return &prSim{m: multiTestModel(t), targets: []int{0, 1}}
	}},
}

func TestPauseResume_CountsEqualFreshRun(t *testing.T) {
	for _, tc := range prCases {
		t.Run(tc.name, func(t *testing.T) {
			// p pressed after TTL 2 of round 2, three rounds closed while
			// paused, p pressed again, two more rounds.
			paused := tc.newSim(t)
			paused.round(0)
			paused.round(1)
			paused.partialRound(2, 2, true)
			if !paused.m.paused {
				t.Fatal("not paused after p")
			}
			for i := 0; i < 3; i++ {
				paused.round(3)
			}
			paused.key("p")
			if paused.m.paused {
				t.Fatal("still paused after the second p")
			}
			paused.round(3)
			paused.round(4)

			// The same rounds with no pause.
			fresh := tc.newSim(t)
			fresh.round(0)
			fresh.round(1)
			fresh.partialRound(2, 2, false)
			fresh.round(3)
			fresh.round(4)

			prCompare(t, paused, fresh)
		})
	}
}

func TestReset_MidRunCountsRestartFromZero(t *testing.T) {
	for _, tc := range prCases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.newSim(t)
			s.round(0)
			s.round(1)
			s.resetRound(2)

			for _, idx := range s.targets {
				sess := s.m.sessions[idx]
				if sess.probeCount != 0 {
					t.Errorf("target %d: probeCount = %d after the in-flight round, want 0", idx, sess.probeCount)
				}
				for ttl := 1; ttl <= prTTLs; ttl++ {
					h := sess.table.Get(ttl)
					if h == nil {
						t.Fatalf("target %d TTL %d: hop row gone after reset", idx, ttl)
					}
					sent, recvd := h.ProbeCounts()
					if sent != 0 || recvd != 0 || h.Rounds() != 0 {
						t.Errorf("target %d TTL %d: sent/recvd/Rnd = %d/%d/%d after the in-flight round, want 0/0/0",
							idx, ttl, sent, recvd, h.Rounds())
					}
					if pct, _ := h.Loss(); pct != 0 {
						t.Errorf("target %d TTL %d: loss = %.1f%%, want 0 (the in-flight round is not loss)", idx, ttl, pct)
					}
				}
			}

			s.round(3)
			s.round(4)

			fresh := tc.newSim(t)
			fresh.round(3)
			fresh.round(4)

			prCompare(t, s, fresh)
		})
	}
}
