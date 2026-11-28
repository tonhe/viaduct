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

	check := func(t *testing.T, when string) {
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
		if got := h.Rounds(); snap.Rounds != got {
			t.Errorf("%s: snapshot Rounds = %d, Rounds = %d", when, snap.Rounds, got)
		}
	}

	check(t, "new hop")

	// Two closed rounds of 7 probes, 5 then 3 answered.
	for _, answered := range []int{5, 3} {
		h.MarkRoundSentProbes(7)
		for i := 0; i < answered; i++ {
			h.AddSample(ip, time.Millisecond, i, KindPrimary)
		}
		h.MarkRoundEnd()
	}
	check(t, "two closed rounds")
	snap := h.LossSnapshot()
	if snap.ProbesSent != 14 || snap.ProbesReceived != 8 || snap.Rounds != 2 {
		t.Errorf("snapshot probes, rounds = %d, %d, %d; want 14, 8, 2",
			snap.ProbesSent, snap.ProbesReceived, snap.Rounds)
	}
	if want := 6.0 / 14.0 * 100; snap.LossPct != want {
		t.Errorf("snapshot LossPct = %v, want %v", snap.LossPct, want)
	}

	// An open round is not in the probe counts or the loss.
	h.MarkRoundSentProbes(7)
	h.AddSample(ip, time.Millisecond, 0, KindPrimary)
	check(t, "open round")
	if snap := h.LossSnapshot(); snap.ProbesSent != 14 || snap.ProbesReceived != 8 {
		t.Errorf("open round: snapshot probes = %d, %d; want 14, 8", snap.ProbesSent, snap.ProbesReceived)
	}
}
