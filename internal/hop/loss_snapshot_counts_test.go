package hop

import (
	"net"
	"testing"
	"time"
)

// LossSnapshot carries the probe counts, per-probe loss and rounds with the
// verdict figures, so an export reads a hop once (viaduct#227).
func TestHop_LossSnapshotCarriesProbeCounts(t *testing.T) {
	ip := net.ParseIP("192.0.2.7")
	h := NewHop(3)

	// closed is the number of rounds closed so far: the rounds the probe
	// counts cover.
	check := func(t *testing.T, when string, closed int) {
		t.Helper()
		snap := h.LossSnapshot()
		sent, recvd := h.ProbeCounts()
		if snap.ProbesSent != sent || snap.ProbesReceived != recvd {
			t.Errorf("%s: snapshot probes = %d, %d; ProbeCounts = %d, %d",
				when, snap.ProbesSent, snap.ProbesReceived, sent, recvd)
		}
		if got := h.LossPercent(); snap.LossPct != got {
			t.Errorf("%s: snapshot LossPct = %v, LossPercent = %v", when, snap.LossPct, got)
		}
		if snap.Rounds != closed {
			t.Errorf("%s: snapshot Rounds = %d, want the %d closed rounds", when, snap.Rounds, closed)
		}
		if snap.ProbesSent != 7*snap.Rounds {
			t.Errorf("%s: snapshot ProbesSent = %d over %d rounds of 7 probes", when, snap.ProbesSent, snap.Rounds)
		}
	}

	check(t, "new hop", 0)

	// Two closed rounds of 7 probes, 5 then 3 answered.
	for _, answered := range []int{5, 3} {
		h.MarkRoundSentProbes(7)
		for i := 0; i < answered; i++ {
			h.AddSample(ip, time.Millisecond, i, KindPrimary)
		}
		h.MarkRoundEnd()
	}
	check(t, "two closed rounds", 2)
	if got := h.Rounds(); got != 2 {
		t.Errorf("two closed rounds: Rounds() = %d, want 2", got)
	}
	snap := h.LossSnapshot()
	if snap.ProbesSent != 14 || snap.ProbesReceived != 8 || snap.Rounds != 2 {
		t.Errorf("snapshot probes, rounds = %d, %d, %d; want 14, 8, 2",
			snap.ProbesSent, snap.ProbesReceived, snap.Rounds)
	}
	if want := 6.0 / 14.0 * 100; snap.LossPct != want {
		t.Errorf("snapshot LossPct = %v, want %v", snap.LossPct, want)
	}

	// An open round is not in the probe counts, the loss or the snapshot's
	// rounds (viaduct#262). Hop.Rounds counts it: the Rnd column shows the
	// round under way.
	h.MarkRoundSentProbes(7)
	h.AddSample(ip, time.Millisecond, 0, KindPrimary)
	check(t, "open round", 2)
	if got := h.Rounds(); got != 3 {
		t.Errorf("open round: Rounds() = %d, want 3: it counts the open round", got)
	}
	if snap := h.LossSnapshot(); snap.ProbesSent != 14 || snap.ProbesReceived != 8 {
		t.Errorf("open round: snapshot probes = %d, %d; want 14, 8", snap.ProbesSent, snap.ProbesReceived)
	}

	// Closing it brings it in.
	h.MarkRoundEnd()
	check(t, "third round closed", 3)

	// After a reset the snapshot starts over.
	h.Reset()
	check(t, "after Reset", 0)
}
