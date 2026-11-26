package hop

import (
	"net"
	"testing"
)

// The exports carry the echo totals the hop's verdict and headline used, and
// say which nodes are left out of them (viaduct#147).

// LossSnapshot reads from one state what the separate accessors read one by one.
func TestHop_LossSnapshotMatchesAccessors(t *testing.T) {
	a, b := net.ParseIP(silentNodeA), net.ParseIP(silentNodeB)
	h := NewHop(6)
	for round := 0; round < 40; round++ {
		runECMPRound(h, round, func(ip string) bool { return ip == silentNodeA })
	}
	snap := h.LossSnapshot()
	pct, kind := h.Loss()
	if snap.Loss != pct || snap.Kind != kind {
		t.Errorf("snapshot loss = %v %v, Loss = %v %v", snap.Loss, snap.Kind, pct, kind)
	}
	if snap.RateLimit != h.RateLimitState() {
		t.Errorf("snapshot verdict = %v, RateLimitState = %v", snap.RateLimit, h.RateLimitState())
	}
	if s, r := h.VerdictEchoTotals(); snap.EchoSent != s || snap.EchoReceived != r {
		t.Errorf("snapshot echo totals = %d, %d; VerdictEchoTotals = %d, %d", snap.EchoSent, snap.EchoReceived, s, r)
	}
	if snap.EchoIgnoredIPs[a.String()] != h.EchoIgnored(a) || snap.EchoIgnoredIPs[b.String()] != h.EchoIgnored(b) {
		t.Errorf("snapshot ignored = %v, want a=%v b=%v", snap.EchoIgnoredIPs, h.EchoIgnored(a), h.EchoIgnored(b))
	}
	if !snap.EchoIgnoredIPs[b.String()] {
		t.Error("fixture: the silent node is not marked ignored")
	}
}

func TestHop_VerdictEchoTotalsAndEchoIgnored(t *testing.T) {
	a, b := net.ParseIP(silentNodeA), net.ParseIP(silentNodeB)

	t.Run("no echoes", func(t *testing.T) {
		h := NewHop(6)
		if s, r := h.VerdictEchoTotals(); s != 0 || r != 0 {
			t.Errorf("VerdictEchoTotals = %d, %d; want 0, 0", s, r)
		}
		if h.EchoIgnored(a) {
			t.Error("EchoIgnored is true for an address the hop does not have")
		}
	})

	t.Run("node that ignores echo is left out", func(t *testing.T) {
		h := NewHop(6)
		for round := 0; round < 40; round++ {
			runECMPRound(h, round, func(ip string) bool { return ip == silentNodeA })
		}
		// A: 40 echoes, all answered. B appeared in round 1: 39, none answered.
		if s, r := h.VerdictEchoTotals(); s != 40 || r != 40 {
			t.Errorf("VerdictEchoTotals = %d, %d; want 40, 40: the silent node is left out", s, r)
		}
		if h.EchoIgnored(a) {
			t.Error("EchoIgnored(answering node) = true, want false")
		}
		if !h.EchoIgnored(b) {
			t.Error("EchoIgnored(silent node) = false, want true")
		}
	})

	t.Run("no node answers echo: nothing is left out", func(t *testing.T) {
		h := NewHop(6)
		for round := 0; round < 40; round++ {
			runECMPRound(h, round, func(string) bool { return false })
		}
		if s, r := h.VerdictEchoTotals(); s != 79 || r != 0 {
			t.Errorf("VerdictEchoTotals = %d, %d; want 79, 0", s, r)
		}
		if h.EchoIgnored(a) || h.EchoIgnored(b) {
			t.Error("EchoIgnored = true on a hop where no node answers echo")
		}
	})

	t.Run("node that starts answering joins", func(t *testing.T) {
		h := NewHop(6)
		for round := 0; round < 26; round++ {
			runECMPRound(h, round, func(ip string) bool { return ip == silentNodeA })
		}
		if !h.EchoIgnored(b) {
			t.Fatal("fixture: B is not left out after ignoring 25 echoes")
		}
		for i := 0; i < 35; i++ {
			lose := i%5 == 4
			runECMPRound(h, 26+i, func(ip string) bool { return ip == silentNodeA || !lose })
		}
		// A: 61 of 61. B: 35 counted, 28 answered; its first 25 stay out.
		if s, r := h.VerdictEchoTotals(); s != 96 || r != 89 {
			t.Errorf("VerdictEchoTotals = %d, %d; want 96, 89", s, r)
		}
		if h.EchoIgnored(b) {
			t.Error("EchoIgnored(B) = true after it started answering")
		}
	})

	t.Run("held verdict keeps the totals it was made from", func(t *testing.T) {
		h := NewHop(6)
		// B has the hop to itself and ignores 25 echoes. Then A appears and
		// answers echo: B leaves the totals and the new ones start at 1.
		for round := 0; round < 25; round++ {
			h.MarkRoundSentProbes(7)
			h.AddSample(b, 0, 0, KindICMPTTL)
			for _, n := range h.GetNodes() {
				n.MarkEchoSent()
			}
			h.MarkRoundEnd()
		}
		if s, r := h.VerdictEchoTotals(); s != 25 || r != 0 {
			t.Fatalf("fixture: VerdictEchoTotals = %d, %d; want 25, 0", s, r)
		}
		round := func() {
			h.MarkRoundSentProbes(7)
			h.AddSample(a, 0, 0, KindICMPTTL)
			for _, n := range h.GetNodes() {
				n.MarkEchoSent()
			}
			h.AddSample(a, 0, 0, KindEcho)
			h.MarkRoundEnd()
		}
		for i := 0; i < 19; i++ {
			round()
			if !h.EchoIgnored(b) {
				t.Fatalf("round %d: EchoIgnored(B) = false, want true", i)
			}
			if _, _, cs, _ := h.EchoCounts(); cs >= 20 {
				t.Fatalf("fixture: round %d: counted echoes = %d, want fewer than 20", i, cs)
			}
			// The verdict is the held one. It was last taken just before A's
			// first answer: B's 26 unanswered echoes and A's one.
			if s, r := h.VerdictEchoTotals(); s != 27 || r != 0 {
				t.Errorf("round %d: VerdictEchoTotals = %d, %d; want the held 27, 0", i, s, r)
			}
		}
		round()
		if s, r := h.VerdictEchoTotals(); s != 20 || r != 20 {
			t.Errorf("with 20 new samples: VerdictEchoTotals = %d, %d; want 20, 20", s, r)
		}
		h.Reset()
		if s, r := h.VerdictEchoTotals(); s != 0 || r != 0 {
			t.Errorf("after Reset: VerdictEchoTotals = %d, %d; want 0, 0", s, r)
		}
	})
}
