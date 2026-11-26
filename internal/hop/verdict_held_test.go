package hop

import (
	"net"
	"testing"
)

// The exports say when a hop is holding an earlier verdict and carry the
// TTL-probe counts that verdict was made from (viaduct#200).

func TestHop_VerdictHeldAndVerdictProbeCounts(t *testing.T) {
	a, b := net.ParseIP(silentNodeA), net.ParseIP(silentNodeB)

	// check compares the two accessors, and the snapshot, with what is wanted.
	check := func(t *testing.T, when string, h *Hop, held bool, sent, received int) {
		t.Helper()
		if got := h.VerdictHeld(); got != held {
			t.Errorf("%s: VerdictHeld = %v, want %v", when, got, held)
		}
		if s, r := h.VerdictProbeCounts(); s != sent || r != received {
			t.Errorf("%s: VerdictProbeCounts = %d, %d; want %d, %d", when, s, r, sent, received)
		}
		snap := h.LossSnapshot()
		if snap.VerdictHeld != held || snap.VerdictProbesSent != sent || snap.VerdictProbesReceived != received {
			t.Errorf("%s: snapshot held, probes = %v, %d, %d; want %v, %d, %d",
				when, snap.VerdictHeld, snap.VerdictProbesSent, snap.VerdictProbesReceived, held, sent, received)
		}
	}

	t.Run("new hop", func(t *testing.T) {
		check(t, "new hop", NewHop(6), false, 0, 0)
	})

	t.Run("no verdict yet: the current counts", func(t *testing.T) {
		h := NewHop(6)
		for round := 0; round < 10; round++ {
			runECMPRound(h, round, func(ip string) bool { return ip == silentNodeA })
		}
		if h.RateLimitState() != RateLimitUndetermined {
			t.Fatalf("fixture: verdict = %v after 10 rounds, want undetermined", h.RateLimitState())
		}
		check(t, "10 rounds", h, false, 70, 10)
	})

	t.Run("verdict from enough echoes: the current counts", func(t *testing.T) {
		h := NewHop(6)
		for round := 0; round < 40; round++ {
			runECMPRound(h, round, func(ip string) bool { return ip == silentNodeA })
		}
		if h.RateLimitState() != RateLimitYes {
			t.Fatalf("fixture: verdict = %v, want yes", h.RateLimitState())
		}
		sent, received := h.ProbeCounts()
		if sent != 280 || received != 40 {
			t.Fatalf("fixture: ProbeCounts = %d, %d; want 280, 40", sent, received)
		}
		check(t, "40 rounds", h, false, 280, 40)
	})

	t.Run("held, then a new verdict, then Reset", func(t *testing.T) {
		h := NewHop(6)
		// B has the hop to itself and ignores 25 echoes: measured. Then A
		// appears and answers echo: B leaves the totals, which start again
		// at 1, and the hop holds its verdict.
		for round := 0; round < 25; round++ {
			h.MarkRoundSentProbes(7)
			h.AddSample(b, 0, 0, KindICMPTTL)
			for _, n := range h.GetNodes() {
				n.MarkEchoSent()
			}
			h.MarkRoundEnd()
		}
		if h.RateLimitState() != RateLimitNo {
			t.Fatalf("fixture: verdict = %v, want no", h.RateLimitState())
		}
		check(t, "before the totals fall", h, false, 175, 25)

		// In the rounds that follow every TTL probe is answered, so the
		// current counts move away from the held ones.
		round := func() {
			h.MarkRoundSentProbes(7)
			for i := 0; i < 7; i++ {
				h.AddSample(a, 0, 0, KindICMPTTL)
			}
			for _, n := range h.GetNodes() {
				n.MarkEchoSent()
			}
			h.AddSample(a, 0, 0, KindEcho)
			h.MarkRoundEnd()
		}
		for i := 0; i < 19; i++ {
			round()
			if s, r := h.ProbeCounts(); s != 175+7*(i+1) || r != 25+7*(i+1) {
				t.Fatalf("fixture: round %d: ProbeCounts = %d, %d", i, s, r)
			}
			if h.RateLimitState() != RateLimitNo {
				t.Errorf("round %d: verdict = %v, want the held no", i, h.RateLimitState())
			}
			// The verdict was last taken before A's first echo answer, on
			// the counts of the last closed round.
			check(t, "held", h, true, 175, 25)
		}
		round()
		check(t, "with 20 new samples", h, false, 315, 165)

		h.Reset()
		check(t, "after Reset", h, false, 0, 0)
	})

	t.Run("Reset while held", func(t *testing.T) {
		h := NewHop(6)
		for round := 0; round < 25; round++ {
			h.MarkRoundSentProbes(7)
			h.AddSample(b, 0, 0, KindICMPTTL)
			for _, n := range h.GetNodes() {
				n.MarkEchoSent()
			}
			h.MarkRoundEnd()
		}
		h.MarkRoundSentProbes(7)
		h.AddSample(a, 0, 0, KindICMPTTL)
		for _, n := range h.GetNodes() {
			n.MarkEchoSent()
		}
		h.AddSample(a, 0, 0, KindEcho)
		h.MarkRoundEnd()
		check(t, "held", h, true, 175, 25)
		h.Reset()
		check(t, "after Reset", h, false, 0, 0)
	})
}
