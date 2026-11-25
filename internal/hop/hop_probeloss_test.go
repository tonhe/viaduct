package hop

import (
	"math"
	"net"
	"testing"
	"time"
)

// Hop loss is per probe: TTL-limited probes that got no reply over probes
// sent, counted over completed rounds only. These tests pin that definition.

var probeLossIP = net.ParseIP("192.0.2.10")

func approx(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// reply records n non-echo replies on h.
func reply(h *Hop, n int) {
	for i := 0; i < n; i++ {
		h.AddSample(probeLossIP, time.Millisecond, i, KindPrimary)
	}
}

func TestHopLoss_PartialLossWithinRoundIsCounted(t *testing.T) {
	// One round of 7 probes (1 ICMP TTL + 6 flows), 5 answered. Round-based
	// accounting called this 0% because something answered.
	h := NewHop(3)
	h.MarkRoundSentProbes(7)
	reply(h, 5)
	h.MarkRoundEnd()

	if got, want := h.LossPercent(), 2.0/7.0*100; !approx(got, want) {
		t.Errorf("LossPercent = %.2f, want %.2f (2 of 7 probes lost)", got, want)
	}
}

func TestHopLoss_AccumulatesAcrossRounds(t *testing.T) {
	h := NewHop(3)
	for _, answered := range []int{7, 5, 6, 0} {
		h.MarkRoundSentProbes(7)
		reply(h, answered)
		h.MarkRoundEnd()
	}
	// 28 sent, 18 answered.
	if got, want := h.LossPercent(), 10.0/28.0*100; !approx(got, want) {
		t.Errorf("LossPercent = %.2f, want %.2f", got, want)
	}
}

func TestHopLoss_FirstRoundInFlightIsNotLoss(t *testing.T) {
	// Probes are out, no reply processed yet, round not closed. The live view
	// used to show 100% here.
	h := NewHop(1)
	h.MarkRoundSentProbes(1)
	if got := h.LossPercent(); got != 0 {
		t.Errorf("LossPercent with round 1 in flight = %.2f, want 0", got)
	}
	if got := h.Rounds(); got != 1 {
		t.Errorf("Rounds = %d, want 1", got)
	}
}

func TestHopLoss_InFlightRoundExcludedUntilItCloses(t *testing.T) {
	h := NewHop(2)
	h.MarkRoundSentProbes(7)
	reply(h, 7)
	h.MarkRoundEnd()

	// Round 2 is sent; nothing back yet.
	h.MarkRoundSentProbes(7)
	if got := h.LossPercent(); got != 0 {
		t.Errorf("LossPercent with round 2 just sent = %.2f, want 0 (was 50 under round-based accounting)", got)
	}
	// Some replies arrive; the round is still open.
	reply(h, 3)
	if got := h.LossPercent(); got != 0 {
		t.Errorf("LossPercent mid-round = %.2f, want 0", got)
	}
	// The round closes with 4 of its 7 probes unanswered.
	h.MarkRoundEnd()
	if got, want := h.LossPercent(), 4.0/14.0*100; !approx(got, want) {
		t.Errorf("LossPercent after round 2 closed = %.2f, want %.2f", got, want)
	}
}

func TestHopLoss_LateReplyIsCreditedWhenNextRoundCloses(t *testing.T) {
	h := NewHop(4)
	h.MarkRoundSentProbes(1)
	h.MarkRoundEnd() // reply not in yet
	if got := h.LossPercent(); got != 100 {
		t.Fatalf("LossPercent after unanswered round = %.2f, want 100", got)
	}

	h.MarkRoundSentProbes(1)
	reply(h, 2) // round 1's late reply and round 2's reply
	h.MarkRoundEnd()
	if got := h.LossPercent(); got != 0 {
		t.Errorf("LossPercent after the late reply arrived = %.2f, want 0", got)
	}
}

func TestHopLoss_EchoRepliesDoNotCount(t *testing.T) {
	// Echo-tier replies are ground truth for rate-limit detection; they answer
	// no TTL probe and must not hide TTL probe loss.
	h := NewHop(3)
	h.MarkRoundSentProbes(7)
	for i := 0; i < 7; i++ {
		h.AddSample(probeLossIP, time.Millisecond, 0, KindEcho)
	}
	h.MarkRoundEnd()
	if got := h.LossPercent(); got != 100 {
		t.Errorf("LossPercent with only echo replies = %.2f, want 100", got)
	}
}

func TestHopLoss_NeverNegative(t *testing.T) {
	// More replies than probes (a duplicated reply) must not produce negative loss.
	h := NewHop(1)
	h.MarkRoundSentProbes(1)
	reply(h, 3)
	h.MarkRoundEnd()
	if got := h.LossPercent(); got != 0 {
		t.Errorf("LossPercent with duplicate replies = %.2f, want 0", got)
	}
}

func TestHopLoss_MarkRoundSentCountsOneProbe(t *testing.T) {
	h := NewHop(1)
	h.MarkRoundSent()
	h.MarkRoundEnd()
	h.MarkRoundSent()
	reply(h, 1)
	h.MarkRoundEnd()
	if got := h.LossPercent(); got != 50 {
		t.Errorf("LossPercent = %.2f, want 50", got)
	}
	if got := h.Rounds(); got != 2 {
		t.Errorf("Rounds = %d, want 2", got)
	}
}

func TestHopLoss_ResetClearsProbeCounts(t *testing.T) {
	h := NewHop(1)
	h.MarkRoundSentProbes(7)
	h.MarkRoundEnd()
	h.Reset()
	if got := h.LossPercent(); got != 0 {
		t.Errorf("LossPercent after Reset = %.2f, want 0", got)
	}
	h.MarkRoundSentProbes(2)
	reply(h, 1)
	h.MarkRoundEnd()
	if got := h.LossPercent(); got != 50 {
		t.Errorf("LossPercent after Reset and one round = %.2f, want 50", got)
	}
}

func TestTableMarkRoundSent_CarriesProbeCount(t *testing.T) {
	tbl := NewTable(30)
	tbl.MarkRoundSent(3, 7)
	h := tbl.GetOrCreate(3)
	reply(h, 6)
	h.MarkRoundEnd()
	if got, want := h.LossPercent(), 1.0/7.0*100; !approx(got, want) {
		t.Errorf("LossPercent = %.2f, want %.2f", got, want)
	}
}

func TestHopProbeCounts_ReportClosedRoundsOnly(t *testing.T) {
	// ProbeCounts are the exact figures LossPercent is computed from, exposed
	// so exports (and the network lab) can check them against real packets.
	h := NewHop(3)
	if sent, recvd := h.ProbeCounts(); sent != 0 || recvd != 0 {
		t.Fatalf("new hop ProbeCounts = %d, %d, want 0, 0", sent, recvd)
	}
	h.MarkRoundSentProbes(7)
	reply(h, 5)
	h.MarkRoundEnd()
	h.MarkRoundSentProbes(7) // open round: not included
	reply(h, 2)
	if sent, recvd := h.ProbeCounts(); sent != 7 || recvd != 5 {
		t.Errorf("ProbeCounts with round 2 open = %d, %d, want 7, 5", sent, recvd)
	}
	h.MarkRoundEnd()
	if sent, recvd := h.ProbeCounts(); sent != 14 || recvd != 7 {
		t.Errorf("ProbeCounts after round 2 closed = %d, %d, want 14, 7", sent, recvd)
	}
}
