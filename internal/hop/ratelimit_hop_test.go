package hop

import (
	"net"
	"testing"
	"time"
)

// The rate-limit verdict is a property of the hop, not of one node: every
// TTL-limited probe sent to the hop distance, of every type, against every
// echo sent to the hop's nodes. A live trace showed why a per-node verdict
// built from the one ICMP TTL probe per round is wrong:
//
//   - a router that answers the ICMP probe but none of the six UDP probes
//     showed 86% loss and was not flagged, because its ICMP loss was zero
//   - at an ECMP hop the ICMP probe is answered by one node per round, so the
//     other nodes looked as if they were losing it and were flagged, at hops
//     with no loss at all

// round runs one round on h: sent TTL-limited probes, replies given as node
// IPs with their probe kind, then one echo to every known node, answered when
// echoOK is true.
type hopReply struct {
	ip   string
	kind ProbeKind
}

func runRound(h *Hop, sent int, replies []hopReply, echoOK bool) {
	h.MarkRoundSentProbes(sent)
	for i, r := range replies {
		h.AddSample(net.ParseIP(r.ip), time.Millisecond, i, r.kind)
	}
	for _, n := range h.GetNodes() {
		n.MarkEchoSent()
		if echoOK {
			h.AddSample(n.GetIP(), time.Millisecond, 0, KindEcho)
		}
	}
	h.MarkRoundEnd()
}

func TestHopRateLimit_ECMPNodesAreNotFlagged(t *testing.T) {
	nodes := []string{"192.0.2.11", "192.0.2.12", "192.0.2.13", "192.0.2.14"}
	h := NewHop(8)
	for round := 0; round < 40; round++ {
		// 7 probes, all answered: the ICMP probe by one node, the six UDP
		// flows spread over the four.
		replies := []hopReply{{nodes[round%4], KindICMPTTL}}
		for f := 0; f < 6; f++ {
			replies = append(replies, hopReply{nodes[f%4], KindPrimary})
		}
		runRound(h, 7, replies, true)
	}
	if got := h.LossPercent(); got != 0 {
		t.Fatalf("fixture: hop loss = %.1f, want 0", got)
	}
	if got := h.RateLimitState(); got != RateLimitNo {
		t.Errorf("hop RateLimitState = %v, want No: nothing is lost here", got)
	}
	if loss, limited := h.HeadlineLoss(); limited || loss != 0 {
		t.Errorf("HeadlineLoss = %.1f, %v; want 0, false", loss, limited)
	}
}

func TestHopRateLimit_AnswersICMPButNotPrimaryProbes(t *testing.T) {
	// The router answers the ICMP TTL probe and every echo, and never answers
	// the UDP probes: 1 of 7 TTL probes answered per round.
	h := NewHop(7)
	for round := 0; round < 40; round++ {
		runRound(h, 7, []hopReply{{"192.0.2.7", KindICMPTTL}}, true)
	}
	if got := h.LossPercent(); !approx(got, 6.0/7.0*100) {
		t.Fatalf("fixture: TTL probe loss = %.2f, want 85.71", got)
	}
	if got := h.RateLimitState(); got != RateLimitYes {
		t.Errorf("hop RateLimitState = %v, want Yes: it answers echoes but not most TTL probes", got)
	}
	if loss, limited := h.HeadlineLoss(); !limited || loss != 0 {
		t.Errorf("HeadlineLoss = %.1f, %v; want 0 (echo loss), true", loss, limited)
	}
}

func TestHopRateLimit_RealLossOnEveryProbeType(t *testing.T) {
	// One round in five loses everything: TTL probes and echoes alike.
	h := NewHop(3)
	for round := 0; round < 60; round++ {
		if round%5 == 4 {
			runRound(h, 7, nil, false)
			continue
		}
		replies := []hopReply{{"192.0.2.3", KindICMPTTL}}
		for f := 0; f < 6; f++ {
			replies = append(replies, hopReply{"192.0.2.3", KindPrimary})
		}
		runRound(h, 7, replies, true)
	}
	if got := h.RateLimitState(); got != RateLimitNo {
		t.Errorf("hop RateLimitState = %v, want No: echoes are lost at the same rate", got)
	}
	if loss, limited := h.HeadlineLoss(); limited || !approx(loss, 20) {
		t.Errorf("HeadlineLoss = %.1f, %v; want 20, false", loss, limited)
	}
}

func TestHopRateLimit_UndeterminedUntilEnoughEchoes(t *testing.T) {
	h := NewHop(7)
	for round := 0; round < 19; round++ {
		runRound(h, 7, []hopReply{{"192.0.2.7", KindICMPTTL}}, true)
	}
	// The node appears in round 1, so 18 echoes have been sent: below 20.
	if got := h.RateLimitState(); got != RateLimitUndetermined {
		t.Errorf("hop RateLimitState = %v, want Undetermined below 20 echo samples", got)
	}
	if _, limited := h.HeadlineLoss(); limited {
		t.Error("HeadlineLoss reports echo loss before there are enough echo samples")
	}
}

// The destination is judged like any other hop (viaduct#71). A host throttles
// the ICMP Port Unreachable errors it sends in answer to UDP probes just as a
// router throttles Time Exceeded; live, an IPv6 destination that answered
// every echo showed 21% loss this way.
func TestHopRateLimit_DestinationThrottlingItsErrors(t *testing.T) {
	const dst = "198.51.100.1"
	h := NewHop(9)
	h.MarkTarget(net.ParseIP(dst))
	for round := 0; round < 45; round++ {
		// The ICMP probe and four of the six UDP probes are answered.
		replies := []hopReply{{dst, KindICMPTTL}}
		for f := 0; f < 4; f++ {
			replies = append(replies, hopReply{dst, KindPrimary})
		}
		runRound(h, 7, replies, true)
	}
	if got := h.LossPercent(); !approx(got, 2.0/7.0*100) {
		t.Fatalf("fixture: TTL probe loss = %.2f, want 28.57", got)
	}
	if got := h.RateLimitState(); got != RateLimitYes {
		t.Errorf("destination RateLimitState = %v, want Yes: every echo is answered", got)
	}
	if loss, limited := h.HeadlineLoss(); !limited || loss != 0 {
		t.Errorf("HeadlineLoss = %.1f, %v; want 0 (echo loss), true", loss, limited)
	}
}

func TestHopRateLimit_DestinationWithRealLoss(t *testing.T) {
	const dst = "198.51.100.1"
	h := NewHop(9)
	h.MarkTarget(net.ParseIP(dst))
	for round := 0; round < 60; round++ {
		if round%5 == 4 {
			runRound(h, 7, nil, false) // everything lost, echoes too
			continue
		}
		replies := []hopReply{{dst, KindICMPTTL}}
		for f := 0; f < 6; f++ {
			replies = append(replies, hopReply{dst, KindPrimary})
		}
		runRound(h, 7, replies, true)
	}
	if loss, limited := h.HeadlineLoss(); limited || !approx(loss, 20) {
		t.Errorf("HeadlineLoss = %.1f, %v; want 20, false: real loss at the destination is reported", loss, limited)
	}
}

// Until there are enough echo samples for a verdict, a hop's TTL-probe loss
// may be rate limiting or may be real. It is reported as provisional so that
// no view presents it as a measurement.
func TestHopLoss_ProvisionalUntilTheVerdictIsPossible(t *testing.T) {
	h := NewHop(7)
	for round := 0; round < 12; round++ {
		runRound(h, 7, []hopReply{{"192.0.2.7", KindICMPTTL}}, true)
	}
	loss, kind := h.Loss()
	if kind != LossProvisional || !approx(loss, 6.0/7.0*100) {
		t.Errorf("after 12 rounds Loss = %.2f, %v; want 85.71, LossProvisional", loss, kind)
	}
	for round := 0; round < 30; round++ {
		runRound(h, 7, []hopReply{{"192.0.2.7", KindICMPTTL}}, true)
	}
	if loss, kind := h.Loss(); kind != LossRateLimited || loss != 0 {
		t.Errorf("after 42 rounds Loss = %.2f, %v; want 0, LossRateLimited", loss, kind)
	}
}

func TestHopLoss_NoLossIsNeverProvisional(t *testing.T) {
	h := NewHop(2)
	for round := 0; round < 5; round++ {
		replies := []hopReply{{"192.0.2.2", KindICMPTTL}}
		for f := 0; f < 6; f++ {
			replies = append(replies, hopReply{"192.0.2.2", KindPrimary})
		}
		runRound(h, 7, replies, true)
	}
	if loss, kind := h.Loss(); kind != LossMeasured || loss != 0 {
		t.Errorf("Loss = %.2f, %v; want 0, LossMeasured: there is nothing to be unsure about", loss, kind)
	}
}

func TestHopLoss_SettledRealLossIsMeasured(t *testing.T) {
	h := NewHop(3)
	for round := 0; round < 60; round++ {
		if round%5 == 4 {
			runRound(h, 7, nil, false)
			continue
		}
		replies := []hopReply{{"192.0.2.3", KindICMPTTL}}
		for f := 0; f < 6; f++ {
			replies = append(replies, hopReply{"192.0.2.3", KindPrimary})
		}
		runRound(h, 7, replies, true)
	}
	if loss, kind := h.Loss(); kind != LossMeasured || !approx(loss, 20) {
		t.Errorf("Loss = %.2f, %v; want 20, LossMeasured", loss, kind)
	}
}

func TestFormatLoss(t *testing.T) {
	for _, tc := range []struct {
		pct  float64
		kind LossKind
		want string
	}{
		{0, LossMeasured, "0.0%"},
		{12.34, LossMeasured, "12.3%"},
		{0.4, LossRateLimited, "~0%†"},
		{85.71, LossProvisional, "85.7%?"},
	} {
		if got := FormatLoss(tc.pct, tc.kind); got != tc.want {
			t.Errorf("FormatLoss(%v, %v) = %q, want %q", tc.pct, tc.kind, got, tc.want)
		}
	}
}

// The sparkline's "held" cell (reachable, no reading this round) follows the
// hop verdict too.
func TestHopRateLimit_VerdictReachesTheSparkline(t *testing.T) {
	h := NewHop(7)
	for round := 0; round < 40; round++ {
		runRound(h, 7, []hopReply{{"192.0.2.7", KindPrimary}}, true)
	}
	// A round with no TTL reply and no echo sent: reachable but unread.
	h.MarkRoundSentProbes(7)
	h.MarkRoundEnd()
	samples := h.PrimaryNode().SparklineData()
	if last := samples[len(samples)-1]; last.Kind != SparkHeld {
		t.Errorf("last sparkline cell = %v, want SparkHeld for a rate-limited hop with no echo this round", last.Kind)
	}
}

// The destination's sparkline follows the hop verdict like any other hop's
// (viaduct#79): a rate-limited destination that is not echoed in a round is
// reachable but unread, not dropped.
func TestHopRateLimit_VerdictReachesTheDestinationSparkline(t *testing.T) {
	const dst = "198.51.100.1"
	h := NewHop(9)
	for round := 0; round < 45; round++ {
		runRound(h, 7, []hopReply{{dst, KindPrimary}}, true)
		h.MarkTarget(net.ParseIP(dst))
	}
	if got := h.RateLimitState(); got != RateLimitYes {
		t.Fatalf("fixture: destination RateLimitState = %v, want Yes", got)
	}
	// A round with no TTL reply and no echo sent.
	h.MarkRoundSentProbes(7)
	h.MarkRoundEnd()
	samples := h.PrimaryNode().SparklineData()
	if last := samples[len(samples)-1]; last.Kind != SparkHeld {
		t.Errorf("last sparkline cell = %v, want SparkHeld for a rate-limited destination with no echo this round", last.Kind)
	}
}

// A node whose hop has not yet closed a round has no verdict, so it is not
// rate-limited: a silent round is a drop, whatever the node's own counters say.
func TestPathNode_NoHopVerdictIsNotRateLimited(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.7"))
	for i := 0; i < 40; i++ {
		pn.MarkProbeRoundEnd() // an ICMP TTL probe sent, never answered
		pn.MarkEchoSent()
		pn.AddSample(time.Millisecond, 0, KindEcho)
	}
	pn.MarkProbeRoundEnd() // clear the echo-sent mark: no echo this round
	pn.RecordRoundAvg()
	samples := pn.SparklineData()
	if last := samples[len(samples)-1]; last.Kind != SparkDrop {
		t.Errorf("last sparkline cell = %v, want SparkDrop: only the hop's verdict makes a cell held", last.Kind)
	}
}
