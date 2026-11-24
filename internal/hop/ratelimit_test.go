package hop

import (
	"net"
	"testing"
	"time"
)

// A node is judged rate-limited when its ICMP TTL probe loss is significantly
// higher than its direct echo loss: a two-proportion z-test at z >= 2.58
// (p < 0.01), with at least 20 echo samples. The destination is never
// flagged. (viaduct#53)

// mkNode builds a node with the given ICMP TTL probes (sent, answered) and
// echo probes (sent, answered).
func mkNode(ttlSent, ttlRecv, echoSent, echoRecv int) *PathNode {
	pn := NewPathNode(net.ParseIP("198.51.100.9"))
	for i := 0; i < ttlSent; i++ {
		if i < ttlRecv {
			pn.AddSample(time.Millisecond, 0, KindICMPTTL)
		}
		pn.MarkProbeRoundEnd()
	}
	for i := 0; i < echoSent; i++ {
		pn.MarkEchoSent()
	}
	for i := 0; i < echoRecv; i++ {
		pn.AddSample(time.Millisecond, 0, KindEcho)
	}
	return pn
}

func TestRateLimitState(t *testing.T) {
	tests := []struct {
		name               string
		ttlSent, ttlRecv   int
		echoSent, echoRecv int
		want               RateLimitState
	}{
		// Too few echo samples: no verdict, however large the gap.
		{"no echo data", 100, 20, 0, 0, RateLimitUndetermined},
		{"19 echo samples, huge gap", 100, 20, 19, 19, RateLimitUndetermined},

		// Real loss: both probe types lose about the same.
		{"equal 20% loss, n=20", 20, 16, 20, 16, RateLimitNo},
		{"equal 20% loss, n=100", 100, 80, 100, 80, RateLimitNo},
		{"equal 20% loss, n=1000", 1000, 800, 1000, 800, RateLimitNo},
		// The lab case that used to be flagged: 20% true loss on both sides,
		// measured as 25% and 15% in one run of 100. z = 1.77.
		{"sampling noise, 25% vs 15%, n=100", 100, 75, 100, 85, RateLimitNo},
		// A 6-point gap at n=100 was enough for the old fixed margin. z = 1.13.
		{"small gap, 20% vs 14%, n=100", 100, 80, 100, 86, RateLimitNo},
		{"no loss at all", 100, 100, 100, 100, RateLimitNo},
		{"total loss on both", 50, 0, 50, 0, RateLimitNo},
		{"echo loss higher than ttl loss", 100, 90, 100, 60, RateLimitNo},

		// Rate limiting: TTL probes go unanswered, echoes are answered.
		{"80% ttl loss, echo clean, n=40", 40, 8, 39, 39, RateLimitYes},
		{"15% ttl loss, echo clean, n=200", 200, 170, 200, 200, RateLimitYes},
		{"all ttl lost, echo clean, exactly 20 echoes", 20, 0, 20, 20, RateLimitYes},
		// Just over the threshold: 30% vs 12% at n=100 gives z = 3.13.
		{"30% vs 12%, n=100", 100, 70, 100, 88, RateLimitYes},
		// Just under: 30% vs 15% at n=100 gives z = 2.54.
		{"30% vs 15%, n=100", 100, 70, 100, 85, RateLimitNo},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pn := mkNode(tc.ttlSent, tc.ttlRecv, tc.echoSent, tc.echoRecv)
			if got := pn.RateLimitState(); got != tc.want {
				t.Errorf("RateLimitState = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRateLimitState_DestinationIsNeverFlagged(t *testing.T) {
	// For the destination, the echo probe and the ICMP TTL probe are the same
	// kind of packet on the same path, so a difference between them is noise.
	pn := mkNode(100, 20, 100, 100) // would be a clear Yes for a router
	if got := pn.RateLimitState(); got != RateLimitYes {
		t.Fatalf("precondition: state = %v, want Yes before marking the target", got)
	}
	pn.MarkTarget()
	if got := pn.RateLimitState(); got != RateLimitNo {
		t.Errorf("RateLimitState for the destination = %v, want No", got)
	}
}

func TestRateLimitState_ResetClearsTargetMark(t *testing.T) {
	pn := mkNode(100, 20, 100, 100)
	pn.MarkTarget()
	pn.Reset()
	for i := 0; i < 100; i++ {
		if i < 20 {
			pn.AddSample(time.Millisecond, 0, KindICMPTTL)
		}
		pn.MarkProbeRoundEnd()
		pn.MarkEchoSent()
		pn.AddSample(time.Millisecond, 0, KindEcho)
	}
	if got := pn.RateLimitState(); got != RateLimitYes {
		t.Errorf("state after Reset = %v, want Yes (target mark must not survive Reset)", got)
	}
}

func TestHopMarkTarget_MarksOnlyThatNode(t *testing.T) {
	h := NewHop(4)
	target := net.ParseIP("198.51.100.1")
	other := net.ParseIP("192.0.2.10")
	for i := 0; i < 100; i++ {
		h.MarkRoundSent()
		if i < 20 {
			h.AddSample(target, time.Millisecond, 0, KindICMPTTL)
			h.AddSample(other, time.Millisecond, 0, KindICMPTTL)
		}
		for _, n := range h.GetNodes() {
			n.MarkEchoSent()
		}
		h.AddSample(target, time.Millisecond, 0, KindEcho)
		h.AddSample(other, time.Millisecond, 0, KindEcho)
		h.MarkRoundEnd()
	}
	h.MarkTarget(target)
	for _, n := range h.GetNodes() {
		want := RateLimitYes
		if n.GetIP().Equal(target) {
			want = RateLimitNo
		}
		if got := n.RateLimitState(); got != want {
			t.Errorf("node %s: state = %v, want %v", n.GetIP(), got, want)
		}
	}
}

// HeadlineLoss is the one loss figure every view shows for a hop: echo-derived
// loss when the hop is judged rate-limited, per-probe TTL loss otherwise.
func TestHopHeadlineLoss(t *testing.T) {
	ip := net.ParseIP("192.0.2.2")

	t.Run("rate-limited hop shows echo loss", func(t *testing.T) {
		h := NewHop(1)
		for i := 0; i < 40; i++ {
			h.MarkRoundSent()
			if i < 8 {
				h.AddSample(ip, time.Millisecond, 0, KindICMPTTL)
			}
			if i > 0 {
				for _, n := range h.GetNodes() {
					n.MarkEchoSent()
				}
				h.AddSample(ip, time.Millisecond, 0, KindEcho)
			}
			h.MarkRoundEnd()
		}
		loss, limited := h.HeadlineLoss()
		if !limited || loss != 0 {
			t.Errorf("HeadlineLoss = %.1f, %v; want 0 (echo loss), true", loss, limited)
		}
		if got := h.LossPercent(); got != 80 {
			t.Errorf("LossPercent = %.1f, want 80 (TTL probe loss is still available)", got)
		}
	})

	t.Run("lossy hop shows probe loss", func(t *testing.T) {
		h := NewHop(1)
		for i := 0; i < 40; i++ {
			h.MarkRoundSent()
			answered := i%5 != 0 // 20% loss on both probe types
			if answered {
				h.AddSample(ip, time.Millisecond, 0, KindICMPTTL)
			}
			if i > 0 {
				for _, n := range h.GetNodes() {
					n.MarkEchoSent()
				}
				if answered {
					h.AddSample(ip, time.Millisecond, 0, KindEcho)
				}
			}
			h.MarkRoundEnd()
		}
		loss, limited := h.HeadlineLoss()
		if limited || loss != 20 {
			t.Errorf("HeadlineLoss = %.1f, %v; want 20, false", loss, limited)
		}
	})

	t.Run("hop with no nodes", func(t *testing.T) {
		h := NewHop(1)
		h.MarkRoundSent()
		h.MarkRoundEnd()
		loss, limited := h.HeadlineLoss()
		if limited || loss != 100 {
			t.Errorf("HeadlineLoss = %.1f, %v; want 100, false", loss, limited)
		}
	})
}
