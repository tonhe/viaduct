package hop

import (
	"net"
	"testing"
	"time"
)

// A probe that did not leave is not sent and not lost (viaduct#115). The
// tracer reports what it wrote for each TTL, by type; nothing here assumes a
// round's probes went out.

var unsentIP = net.ParseIP("192.0.2.20")

// answeredRound is a complete round at h: one ICMP TTL probe and two primary
// probes sent, all answered by unsentIP.
func answeredRound(h *Hop) {
	h.MarkRoundSentByType(1, 2)
	h.AddSample(unsentIP, time.Millisecond, 0, KindICMPTTL)
	h.AddSample(unsentIP, time.Millisecond, 0, KindPrimary)
	h.AddSample(unsentIP, time.Millisecond, 1, KindPrimary)
	h.MarkRoundEnd()
}

func TestHopUnsent_RoundWithNothingSentIsNotARound(t *testing.T) {
	for _, mark := range []struct {
		name string
		call func(*Hop)
	}{
		{"MarkRoundSentByType(0, 0)", func(h *Hop) { h.MarkRoundSentByType(0, 0) }},
		{"MarkRoundSentProbes(0)", func(h *Hop) { h.MarkRoundSentProbes(0) }},
	} {
		t.Run(mark.name, func(t *testing.T) {
			h := NewHop(3)
			answeredRound(h)
			node := h.GetNodes()[0]
			sparkBefore := len(node.SparklineData())
			stabBefore := node.StabilityPercent()

			// Three rounds in which every send to this hop failed or was skipped.
			for i := 0; i < 3; i++ {
				mark.call(h)
				h.MarkRoundEnd()
			}

			if got := h.Rounds(); got != 1 {
				t.Errorf("Rounds = %d, want 1: a round with nothing sent is not a round", got)
			}
			if sent, recvd := h.ProbeCounts(); sent != 3 || recvd != 3 {
				t.Errorf("ProbeCounts = %d sent, %d received; want 3, 3", sent, recvd)
			}
			if got := h.LossPercent(); got != 0 {
				t.Errorf("LossPercent = %.1f, want 0: probes that did not leave are not lost", got)
			}
			if pct, kind := h.Loss(); pct != 0 || kind != LossMeasured {
				t.Errorf("Loss = %.1f, kind %v; want 0, measured", pct, kind)
			}
			if got := h.SettledLoss(); got != 0 {
				t.Errorf("SettledLoss = %.1f, want 0: nothing here for alerting to act on", got)
			}
			if got := node.ICMPTTLStats().ProbesSent; got != 1 {
				t.Errorf("node ICMP TTL probes sent = %d, want 1", got)
			}
			if got := node.PrimaryStats().ProbesSent; got != 2 {
				t.Errorf("node primary probes sent = %d, want 2", got)
			}
			if got := len(node.SparklineData()); got != sparkBefore {
				t.Errorf("sparkline has %d cells, want %d: no cell, least of all a drop, for a round that sent nothing", got, sparkBefore)
			}
			if got := node.StabilityPercent(); got != stabBefore {
				t.Errorf("stability = %.0f%%, want %.0f%%", got, stabBefore)
			}
		})
	}
}

func TestHopUnsent_PerTypeCountsAreWhatWasSent(t *testing.T) {
	// Round 1: everything sent. Round 2: the ICMP socket took nothing, the two
	// flows went out. Round 3: only the ICMP probe went out.
	h := NewHop(4)
	answeredRound(h)

	h.MarkRoundSentByType(0, 2)
	h.AddSample(unsentIP, time.Millisecond, 0, KindPrimary)
	h.AddSample(unsentIP, time.Millisecond, 1, KindPrimary)
	h.MarkRoundEnd()

	h.MarkRoundSentByType(1, 0)
	h.AddSample(unsentIP, time.Millisecond, 0, KindICMPTTL)
	h.MarkRoundEnd()

	node := h.GetNodes()[0]
	icmp, primary := node.ICMPTTLStats(), node.PrimaryStats()
	if icmp.ProbesSent != 2 || icmp.Received != 2 {
		t.Errorf("ICMP TTL: %d sent, %d received; want 2, 2", icmp.ProbesSent, icmp.Received)
	}
	if primary.ProbesSent != 4 || primary.Received != 4 {
		t.Errorf("primary: %d sent, %d received; want 4, 4", primary.ProbesSent, primary.Received)
	}
	if sent, recvd := h.ProbeCounts(); sent != 6 || recvd != 6 {
		t.Errorf("ProbeCounts = %d, %d; want 6, 6", sent, recvd)
	}
	if got := h.LossPercent(); got != 0 {
		t.Errorf("LossPercent = %.1f, want 0", got)
	}
	if got := h.Rounds(); got != 3 {
		t.Errorf("Rounds = %d, want 3", got)
	}
}

func TestHopUnsent_PartlySentRoundCountsOnlyWhatLeft(t *testing.T) {
	// Seven probes were due; three left and two of those were answered. Loss
	// is one of three, not five of seven.
	h := NewHop(2)
	h.MarkRoundSentByType(1, 2)
	h.AddSample(unsentIP, time.Millisecond, 0, KindICMPTTL)
	h.AddSample(unsentIP, time.Millisecond, 0, KindPrimary)
	h.MarkRoundEnd()

	if sent, recvd := h.ProbeCounts(); sent != 3 || recvd != 2 {
		t.Errorf("ProbeCounts = %d, %d; want 3, 2", sent, recvd)
	}
	if got, want := h.LossPercent(), 1.0/3.0*100; !approx(got, want) {
		t.Errorf("LossPercent = %.2f, want %.2f", got, want)
	}
}

func TestHopUnsent_ProbesTotalKeepsTheAssumedSplit(t *testing.T) {
	// MarkRoundSentProbes carries a total only: the first probe is taken to be
	// the ICMP TTL probe and the rest primary, as before.
	h := NewHop(1)
	h.MarkRoundSentProbes(7)
	h.AddSample(unsentIP, time.Millisecond, 0, KindICMPTTL)
	h.MarkRoundEnd()
	node := h.GetNodes()[0]
	if got := node.ICMPTTLStats().ProbesSent; got != 1 {
		t.Errorf("ICMP TTL probes sent = %d, want 1", got)
	}
	if got := node.PrimaryStats().ProbesSent; got != 6 {
		t.Errorf("primary probes sent = %d, want 6", got)
	}
}

func TestHopUnsent_TwoRoundsSentBeforeOneCloses(t *testing.T) {
	// The TUI closes a round on a message, so the next round's first probes
	// can be marked before it does. Both rounds' probes keep their types.
	h := NewHop(1)
	h.AddSample(unsentIP, time.Millisecond, 0, KindICMPTTL) // node known
	h.MarkRoundSentByType(1, 2)
	h.MarkRoundSentByType(0, 2)
	h.MarkRoundEnd()
	node := h.GetNodes()[0]
	if got := node.ICMPTTLStats().ProbesSent; got != 1 {
		t.Errorf("ICMP TTL probes sent = %d, want 1", got)
	}
	if got := node.PrimaryStats().ProbesSent; got != 4 {
		t.Errorf("primary probes sent = %d, want 4", got)
	}
}

func TestHopUnsent_ResetClearsTheOpenRoundSplit(t *testing.T) {
	h := NewHop(1)
	h.MarkRoundSentByType(1, 2)
	h.Reset()
	h.MarkRoundSentByType(0, 2)
	h.AddSample(unsentIP, time.Millisecond, 0, KindPrimary)
	h.MarkRoundEnd()
	node := h.GetNodes()[0]
	if got := node.ICMPTTLStats().ProbesSent; got != 0 {
		t.Errorf("ICMP TTL probes sent after Reset = %d, want 0", got)
	}
	if got := node.PrimaryStats().ProbesSent; got != 2 {
		t.Errorf("primary probes sent after Reset = %d, want 2", got)
	}
}

func TestHopUnsent_NegativeCountsAreIgnored(t *testing.T) {
	h := NewHop(1)
	h.MarkRoundSentByType(-1, -5)
	h.MarkRoundEnd()
	if got := h.Rounds(); got != 0 {
		t.Errorf("Rounds = %d, want 0", got)
	}
	if sent, _ := h.ProbeCounts(); sent != 0 {
		t.Errorf("probes sent = %d, want 0", sent)
	}
}

func TestHopRoundOpen(t *testing.T) {
	h := NewHop(1)
	if h.RoundOpen() {
		t.Error("a new hop has an open round")
	}
	h.MarkRoundSentByType(0, 0)
	if h.RoundOpen() {
		t.Error("a round in which nothing left is open")
	}
	h.MarkRoundSentByType(1, 0)
	if !h.RoundOpen() {
		t.Error("no open round after a probe was sent")
	}
	h.MarkRoundEnd()
	if h.RoundOpen() {
		t.Error("the round is still open after MarkRoundEnd")
	}
	h.MarkRoundSentProbes(3)
	h.Reset()
	if h.RoundOpen() {
		t.Error("the round is still open after Reset")
	}
}

func TestTableMarkRoundSentByType(t *testing.T) {
	tbl := NewTable(30)
	tbl.MarkRoundSentByType(5, 0, 3)
	h := tbl.Get(5)
	if h == nil {
		t.Fatal("no hop created for TTL 5")
	}
	h.AddSample(unsentIP, time.Millisecond, 0, KindPrimary)
	h.MarkRoundEnd()
	node := h.GetNodes()[0]
	if icmp, primary := node.ICMPTTLStats().ProbesSent, node.PrimaryStats().ProbesSent; icmp != 0 || primary != 3 {
		t.Errorf("sent by type = %d ICMP, %d primary; want 0, 3", icmp, primary)
	}

	// A TTL none of whose probes left gets no row: nothing was sent to it.
	tbl.MarkRoundSentByType(9, 0, 0)
	if tbl.Get(9) != nil {
		t.Error("a hop row was created for a TTL nothing was sent to")
	}
}

// A hop that only ever had unsent rounds after a link-local node appeared has
// no echo samples: its loss is provisional, never echo loss.
func TestHopUnsent_NodeNeverEchoedStaysProvisional(t *testing.T) {
	h := NewHop(1)
	ll := net.ParseIP("fe80::1")
	for i := 0; i < 30; i++ {
		h.MarkRoundSentByType(1, 0)
		if i%2 == 0 {
			h.AddSample(ll, time.Millisecond, 0, KindICMPTTL)
		}
		h.MarkRoundEnd()
	}
	pct, kind := h.Loss()
	if kind != LossProvisional {
		t.Errorf("loss kind = %v, want provisional: the node was never echoed", kind)
	}
	if pct != 50 {
		t.Errorf("loss = %.1f, want 50", pct)
	}
	if got := FormatLoss(pct, kind); got != "50.0%?" {
		t.Errorf("FormatLoss = %q, want \"50.0%%?\"", got)
	}
	if got := h.SettledLoss(); got != 0 {
		t.Errorf("SettledLoss = %.1f, want 0", got)
	}
}
