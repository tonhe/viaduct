package hop

import (
	"net"
	"testing"
	"time"
)

// Pause and reset must not corrupt probe counts (lab scenario 11, #62).

var pauseResetIP = net.ParseIP("192.0.2.20")

func TestHopReset_InFlightRepliesAreDropped(t *testing.T) {
	// The review of #72: a round of 7 probes is out when the user resets, and
	// its 7 replies arrive afterwards. They used to be credited against the
	// zeroed sent count (sent=0 recvd=7), so two later rounds losing 7 of 14
	// read as 14 of 14 answered.
	h := NewHop(2)
	h.MarkRoundSentProbes(7)
	h.MarkRoundEnd()
	h.MarkRoundSentProbes(7)
	h.Reset()
	for i := 0; i < 7; i++ {
		if h.AddSample(pauseResetIP, time.Millisecond, i, KindPrimary) {
			t.Fatalf("reply %d to a probe sent before the reset was recorded", i)
		}
	}
	if h.AddSample(pauseResetIP, time.Millisecond, 0, KindEcho) {
		t.Fatal("echo reply in flight at the reset was recorded")
	}
	h.MarkRoundEnd()

	if sent, recvd := h.ProbeCounts(); sent != 0 || recvd != 0 {
		t.Errorf("after the in-flight round closed: ProbeCounts = %d, %d, want 0, 0", sent, recvd)
	}
	if got := h.Rounds(); got != 0 {
		t.Errorf("Rounds = %d, want 0 (the in-flight round is not counted)", got)
	}
	if got := len(h.GetNodes()); got != 0 {
		t.Errorf("nodes = %d, want 0", got)
	}
	if got := h.LossPercent(); got != 0 {
		t.Errorf("LossPercent = %.2f, want 0 (the in-flight round is not loss)", got)
	}

	for _, answered := range []int{4, 3} {
		h.MarkRoundSentProbes(7)
		reply(h, answered)
		h.MarkRoundEnd()
	}
	if sent, recvd := h.ProbeCounts(); sent != 14 || recvd != 7 {
		t.Errorf("ProbeCounts = %d, %d, want 14, 7", sent, recvd)
	}
	if got := h.LossPercent(); got != 50 {
		t.Errorf("LossPercent = %.2f, want 50", got)
	}
	if got := h.Rounds(); got != 2 {
		t.Errorf("Rounds = %d, want 2", got)
	}
}

func TestHopReset_BetweenRoundsDropsNothing(t *testing.T) {
	// No round open at the reset: nothing is in flight, so the next reply is
	// the next round's and counts.
	h := NewHop(2)
	h.MarkRoundSentProbes(3)
	reply(h, 3)
	h.MarkRoundEnd()
	h.Reset()
	h.MarkRoundSentProbes(3)
	reply(h, 2)
	h.MarkRoundEnd()
	if sent, recvd := h.ProbeCounts(); sent != 3 || recvd != 2 {
		t.Errorf("ProbeCounts = %d, %d, want 3, 2", sent, recvd)
	}
	if got := h.Rounds(); got != 1 {
		t.Errorf("Rounds = %d, want 1", got)
	}
}

func TestHopReset_NewRoundEndsDrain(t *testing.T) {
	// The round end reaches the hop through the UI, the next round's sends do
	// not; if the next round is marked first, its replies must count.
	h := NewHop(2)
	h.MarkRoundSentProbes(3)
	h.Reset()
	h.MarkRoundSentProbes(3)
	if !h.AddSample(pauseResetIP, time.Millisecond, 0, KindPrimary) {
		t.Fatal("reply after a new round was sent was dropped")
	}
	h.MarkRoundEnd()
	if sent, recvd := h.ProbeCounts(); sent != 3 || recvd != 1 {
		t.Errorf("ProbeCounts = %d, %d, want 3, 1", sent, recvd)
	}
}

func TestTableResetAll_DrainsOpenRoundOnly(t *testing.T) {
	tbl := NewTable(30)
	tbl.MarkRoundSent(1, 3) // round open at TTL 1
	tbl.GetOrCreate(2)      // TTL 2 not yet probed this round
	tbl.ResetAll()
	if tbl.GetOrCreate(1).AddSample(pauseResetIP, time.Millisecond, 0, KindPrimary) {
		t.Error("TTL 1: reply in flight at the reset was recorded")
	}
	tbl.MarkRoundSent(2, 3)
	if !tbl.GetOrCreate(2).AddSample(pauseResetIP, time.Millisecond, 0, KindPrimary) {
		t.Error("TTL 2: reply to a probe sent after the reset was dropped")
	}
}

// runRounds drives h through rounds: a negative entry is a round closed with
// nothing sent to the hop (the tracer was paused), otherwise the number of
// the 3 probes sent that were answered. Each answered round's first reply is
// an ICMP TTL reply, the rest primary.
func runRounds(h *Hop, rounds []int) {
	for _, answered := range rounds {
		if answered >= 0 {
			h.MarkRoundSentProbes(3)
			for i := 0; i < answered; i++ {
				kind := KindPrimary
				if i == 0 {
					kind = KindICMPTTL
				}
				h.AddSample(pauseResetIP, time.Duration(answered+1)*time.Millisecond, i, kind)
			}
		}
		h.MarkRoundEnd()
	}
}

func TestHopMarkRoundEnd_PausedRoundsLeaveCountsUnchanged(t *testing.T) {
	paused := NewHop(2)
	runRounds(paused, []int{3, 2, -1, -1, -1, 3, 0, 1})
	fresh := NewHop(2)
	runRounds(fresh, []int{3, 2, 3, 0, 1})

	ps, pr := paused.ProbeCounts()
	fs, fr := fresh.ProbeCounts()
	if ps != fs || pr != fr {
		t.Errorf("ProbeCounts = %d, %d, want %d, %d", ps, pr, fs, fr)
	}
	if paused.Rounds() != fresh.Rounds() {
		t.Errorf("Rounds = %d, want %d", paused.Rounds(), fresh.Rounds())
	}
	if paused.LossPercent() != fresh.LossPercent() {
		t.Errorf("LossPercent = %.2f, want %.2f", paused.LossPercent(), fresh.LossPercent())
	}

	pn, fn := paused.PrimaryNode(), fresh.PrimaryNode()
	if pn == nil || fn == nil {
		t.Fatal("no primary node")
	}
	for _, c := range []struct {
		name      string
		got, want ProbeStats
	}{
		{"ICMPTTLStats", pn.ICMPTTLStats(), fn.ICMPTTLStats()},
		{"PrimaryStats", pn.PrimaryStats(), fn.PrimaryStats()},
		{"EchoStats", pn.EchoStats(), fn.EchoStats()},
	} {
		if c.got.Sent != c.want.Sent || c.got.Received != c.want.Received || c.got.ProbesSent != c.want.ProbesSent {
			t.Errorf("%s = %+v, want %+v", c.name, c.got, c.want)
		}
	}
	if got, want := pn.StabilityPercent(), fn.StabilityPercent(); got != want {
		t.Errorf("StabilityPercent = %.2f, want %.2f", got, want)
	}
	if got, want := pn.SparklineData(), fn.SparklineData(); len(got) != len(want) {
		t.Errorf("sparkline cells = %d, want %d", len(got), len(want))
	}
}

func TestHopMarkRoundEnd_LateReplyInPausedRoundIsCredited(t *testing.T) {
	// A reply that arrives after its round closed is credited when the next
	// round closes, paused or not.
	h := NewHop(2)
	h.MarkRoundSentProbes(2)
	reply(h, 1)
	h.MarkRoundEnd()
	reply(h, 1)
	h.MarkRoundEnd() // paused: nothing sent
	if sent, recvd := h.ProbeCounts(); sent != 2 || recvd != 2 {
		t.Errorf("ProbeCounts = %d, %d, want 2, 2", sent, recvd)
	}
}
