package hop

import (
	"net"
	"testing"
	"time"
)

// The debug log reports a hop's TTL probes by type and its echo totals. Both
// are closed-round figures, like ProbeCounts.

func TestHop_ProbeCountsByType(t *testing.T) {
	ip := net.ParseIP("192.0.2.1")
	h := NewHop(3)

	// Round 1: one ICMP TTL probe and two flows; the ICMP probe and one flow
	// are answered.
	h.MarkRoundSentByType(1, 2)
	h.AddSample(ip, time.Millisecond, 0, KindICMPTTL)
	h.AddSample(ip, time.Millisecond, 0, KindPrimary)

	if is, ir, ps, pr := h.ProbeCountsByType(); is != 0 || ir != 0 || ps != 0 || pr != 0 {
		t.Errorf("open round: got icmp %d/%d primary %d/%d, want all 0: the round is not closed", is, ir, ps, pr)
	}
	h.MarkRoundEnd()
	if is, ir, ps, pr := h.ProbeCountsByType(); is != 1 || ir != 1 || ps != 2 || pr != 1 {
		t.Errorf("after round 1: got icmp %d/%d primary %d/%d, want 1/1 and 2/1", is, ir, ps, pr)
	}

	// Round 2: nothing answered. An echo answer is not a TTL-probe reply.
	h.MarkRoundSentByType(1, 2)
	h.AddSample(ip, time.Millisecond, 0, KindEcho)
	h.MarkRoundEnd()
	is, ir, ps, pr := h.ProbeCountsByType()
	if is != 2 || ir != 1 || ps != 4 || pr != 1 {
		t.Errorf("after round 2: got icmp %d/%d primary %d/%d, want 2/1 and 4/1", is, ir, ps, pr)
	}
	if sent, recvd := h.ProbeCounts(); sent != is+ps || recvd != ir+pr {
		t.Errorf("types sum to %d/%d, ProbeCounts is %d/%d", is+ps, ir+pr, sent, recvd)
	}

	h.Reset()
	if is, ir, ps, pr := h.ProbeCountsByType(); is != 0 || ir != 0 || ps != 0 || pr != 0 {
		t.Errorf("after Reset: got icmp %d/%d primary %d/%d, want all 0", is, ir, ps, pr)
	}
}

// A hop that never answers still has its probes counted by type.
func TestHop_ProbeCountsByType_SilentHop(t *testing.T) {
	h := NewHop(2)
	for i := 0; i < 3; i++ {
		h.MarkRoundSentByType(1, 4)
		h.MarkRoundEnd()
	}
	if is, ir, ps, pr := h.ProbeCountsByType(); is != 3 || ir != 0 || ps != 12 || pr != 0 {
		t.Errorf("got icmp %d/%d primary %d/%d, want 3/0 and 12/0", is, ir, ps, pr)
	}
}

func TestHop_EchoCounts(t *testing.T) {
	answering, silent := net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.2")
	h := NewHop(4)
	h.MarkRoundSentByType(1, 0)
	h.AddSample(answering, time.Millisecond, 0, KindICMPTTL)
	h.AddSample(silent, time.Millisecond, 1, KindICMPTTL)
	h.MarkRoundEnd()

	if s, r, cs, cr := h.EchoCounts(); s != 0 || r != 0 || cs != 0 || cr != 0 {
		t.Errorf("no echoes yet: got %d/%d counted %d/%d, want all 0", s, r, cs, cr)
	}

	// 25 echoes to each node: one node answers all, the other ignores ping.
	nodes := h.GetNodes()
	for i := 0; i < 25; i++ {
		for _, n := range nodes {
			n.MarkEchoSent()
		}
		h.AddSample(answering, time.Millisecond, 0, KindEcho)
	}
	s, r, cs, cr := h.EchoCounts()
	if s != 50 || r != 25 {
		t.Errorf("raw echo counts = %d sent, %d received; want 50, 25", s, r)
	}
	// The node that ignores ping is left out of the totals the verdict uses.
	if cs != 25 || cr != 25 {
		t.Errorf("counted echo totals = %d sent, %d received; want 25, 25", cs, cr)
	}
}

func TestLossKindAndRateLimitState_String(t *testing.T) {
	for _, tc := range []struct {
		got, want string
	}{
		{LossMeasured.String(), "measured"},
		{LossRateLimited.String(), "rate_limited"},
		{LossProvisional.String(), "provisional"},
		{RateLimitYes.String(), "yes"},
		{RateLimitNo.String(), "no"},
		{RateLimitUndetermined.String(), "undetermined"},
	} {
		if tc.got != tc.want {
			t.Errorf("String() = %q, want %q", tc.got, tc.want)
		}
	}
}
