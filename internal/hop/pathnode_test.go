package hop

import (
	"math"
	"net"
	"testing"
	"time"
)

func TestPathNodeAddSample(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)
	pn.AddSample(20*time.Millisecond, 1, KindPrimary)

	if pn.GetReceived() != 2 {
		t.Fatalf("expected 2 received, got %d", pn.GetReceived())
	}
	if pn.AvgRTT() != 15*time.Millisecond {
		t.Fatalf("expected avg 15ms, got %v", pn.AvgRTT())
	}
	if pn.GetMinRTT() != 10*time.Millisecond {
		t.Fatalf("expected min 10ms, got %v", pn.GetMinRTT())
	}
	if pn.GetMaxRTT() != 20*time.Millisecond {
		t.Fatalf("expected max 20ms, got %v", pn.GetMaxRTT())
	}
}

func TestPathNodeFlowIDs(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)
	pn.AddSample(15*time.Millisecond, 2, KindPrimary)
	pn.AddSample(12*time.Millisecond, 0, KindPrimary)

	flows := pn.GetFlowIDs()
	if len(flows) != 2 {
		t.Fatalf("expected 2 unique flows, got %d", len(flows))
	}
}

func TestPathNodeStDev(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)
	pn.AddSample(20*time.Millisecond, 0, KindPrimary)
	pn.AddSample(30*time.Millisecond, 0, KindPrimary)

	stdev := pn.StDev()
	expected := math.Sqrt(float64(200) / 3.0)
	if math.Abs(stdev-expected) > 0.01 {
		t.Fatalf("expected stdev ~%.2f, got %.2f", expected, stdev)
	}
}

// PathNode no longer tracks sent/loss directly; hop-level per-probe loss
// is tested in hop_test.go (TestHopRoundBasedLoss, TestHopLossPercent).

func TestPathNodeStability(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))

	for i := 0; i < 10; i++ {
		pn.MarkSeen()
		pn.AdvanceRound()
	}
	for i := 0; i < 5; i++ {
		pn.AdvanceRound()
	}

	stab := pn.StabilityPercent()
	expected := float64(10) / float64(15) * 100
	if math.Abs(stab-expected) > 0.1 {
		t.Fatalf("expected stability ~%.1f%%, got %.1f%%", expected, stab)
	}
}

func TestPathNodeASN(t *testing.T) {
	pn := &PathNode{IP: net.ParseIP("1.1.1.1")}
	asn, org := pn.GetASN()
	if asn != 0 || org != "" {
		t.Fatalf("expected zero ASN, got %d %q", asn, org)
	}
	pn.SetASN(13335, "CLOUDFLARENET")
	asn, org = pn.GetASN()
	if asn != 13335 {
		t.Fatalf("expected ASN 13335, got %d", asn)
	}
	if org != "CLOUDFLARENET" {
		t.Fatalf("expected org 'CLOUDFLARENET', got %q", org)
	}
}

func TestPathNodeResetClearsASN(t *testing.T) {
	pn := &PathNode{IP: net.ParseIP("1.1.1.1")}
	pn.SetASN(13335, "CLOUDFLARENET")
	pn.Reset()
	asn, org := pn.GetASN()
	if asn != 0 || org != "" {
		t.Fatalf("expected zero ASN after reset, got %d %q", asn, org)
	}
}

func TestPathNodeGeoMean(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)
	pn.AddSample(100*time.Millisecond, 0, KindPrimary)
	// Geometric mean of 10 and 100 = sqrt(10*100) = ~31.6ms
	gm := pn.GeoMean()
	gmMs := float64(gm) / float64(time.Millisecond)
	if gmMs < 30.0 || gmMs > 33.0 {
		t.Fatalf("expected GeoMean ~31.6ms, got %.1fms", gmMs)
	}
}

func TestPathNodeGeoMeanSubMillisecond(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	// Sub-millisecond RTTs should not panic (floor at 0.001ms)
	pn.AddSample(100*time.Microsecond, 0, KindPrimary) // 0.1ms
	pn.AddSample(200*time.Microsecond, 0, KindPrimary) // 0.2ms
	gm := pn.GeoMean()
	if gm <= 0 {
		t.Fatalf("expected positive GeoMean, got %v", gm)
	}
}

func TestPathNodeGeoMeanNoSamples(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	if pn.GeoMean() != 0 {
		t.Fatalf("expected 0 for no samples, got %v", pn.GeoMean())
	}
}

func TestPathNodeJitter(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)
	pn.AddSample(15*time.Millisecond, 0, KindPrimary)
	// Jitter = |15 - 10| = 5ms
	j := pn.Jitter()
	if j != 5*time.Millisecond {
		t.Fatalf("expected jitter 5ms, got %v", j)
	}
}

func TestPathNodeJitterFirstSample(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)
	// No previous RTT, jitter should be 0
	if pn.Jitter() != 0 {
		t.Fatalf("expected 0 jitter on first sample, got %v", pn.Jitter())
	}
}

func TestPathNodeJitterMean(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)
	pn.AddSample(20*time.Millisecond, 0, KindPrimary) // jitter: 10ms
	pn.AddSample(15*time.Millisecond, 0, KindPrimary) // jitter: 5ms
	pn.AddSample(25*time.Millisecond, 0, KindPrimary) // jitter: 10ms
	// Mean jitter = (10+5+10)/3 = 8.33ms
	jm := pn.JitterMean()
	jmMs := float64(jm) / float64(time.Millisecond)
	if jmMs < 7.5 || jmMs > 9.0 {
		t.Fatalf("expected JitterMean ~8.3ms, got %.1fms", jmMs)
	}
}

func TestPathNodeSparklineData(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	// Round 1: one probe
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)
	pn.RecordRoundAvg()
	// Round 2: one probe
	pn.AddSample(20*time.Millisecond, 0, KindPrimary)
	pn.RecordRoundAvg()
	// Round 3: one probe
	pn.AddSample(15*time.Millisecond, 0, KindPrimary)
	pn.RecordRoundAvg()

	data := pn.SparklineData()
	if len(data) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(data))
	}
	if data[0].RTT != 10*time.Millisecond || data[1].RTT != 20*time.Millisecond || data[2].RTT != 15*time.Millisecond {
		t.Fatalf("unexpected data: %v", data)
	}
	for i, s := range data {
		if s.Kind != SparkTTL {
			t.Fatalf("sample %d: kind = %v, want SparkTTL", i, s.Kind)
		}
	}
}

func TestPathNodeSparklineRingOverflow(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	// Fill beyond capacity (28) — one probe per round
	for i := 0; i < 35; i++ {
		pn.AddSample(time.Duration(i)*time.Millisecond, 0, KindPrimary)
		pn.RecordRoundAvg()
	}
	data := pn.SparklineData()
	if len(data) != 28 {
		t.Fatalf("expected 28 samples (capped), got %d", len(data))
	}
	// Oldest should be sample 7 (35-28=7)
	if data[0].RTT != 7*time.Millisecond {
		t.Fatalf("expected oldest=7ms, got %v", data[0].RTT)
	}
}

func TestPathNodeSparklineLostProbe(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	// Round 1: reply
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)
	pn.RecordRoundAvg()
	// Round 2: no replies — RecordRoundAvg records loss
	pn.RecordRoundAvg()
	// Round 3: reply
	pn.AddSample(12*time.Millisecond, 0, KindPrimary)
	pn.RecordRoundAvg()

	data := pn.SparklineData()
	if len(data) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(data))
	}
	if data[1].RTT != 0 || data[1].Kind != SparkDrop {
		t.Fatalf("expected drop (RTT 0, SparkDrop) for lost round, got %+v", data[1])
	}
}

func TestPathNodeRecordRoundAvgRecordsLossWhenNoProbes(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	// Round 1: reply received
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)
	pn.RecordRoundAvg()

	// Round 2: no probes received — RecordRoundAvg records sparkline loss
	pn.RecordRoundAvg()

	data := pn.SparklineData()
	if len(data) != 2 {
		t.Fatalf("expected 2 entries (1 sample + 1 loss), got %d", len(data))
	}
	if data[1].RTT != 0 || data[1].Kind != SparkDrop {
		t.Fatalf("expected drop (RTT 0, SparkDrop) for no-reply round, got %+v", data[1])
	}
}

// driveRateLimited runs n rounds where the TTL tier is silent but echo replies,
// leaving the node in RateLimitYes with the given echo RTT. Mirrors the
// production round order (RecordRoundAvg → MarkProbeRoundEnd).
func driveRateLimited(pn *PathNode, n int, echoRTT time.Duration) {
	for i := 0; i < n; i++ {
		pn.MarkEchoSent()
		pn.AddSample(echoRTT, 0, KindEcho) // echo confirms reachability
		pn.RecordRoundAvg()                // TTL silent this round
		pn.MarkProbeRoundEnd()
	}
}

// TestRecordRoundAvg_EchoFillsGap: when the TTL tier is silent but echo replied,
// the sparkline cell is SparkEcho carrying the echo RTT — not a drop. This is
// what keeps a rate-limited hop's sparkline consistent with its ~0%† loss cell.
func TestRecordRoundAvg_EchoFillsGap(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	driveRateLimited(pn, rateLimitMinEchoSamples+1, 7*time.Millisecond)

	if got := pn.RateLimitState(); got != RateLimitYes {
		t.Fatalf("precondition: RateLimitState = %v, want RateLimitYes", got)
	}
	data := pn.SparklineData()
	for i, s := range data {
		if s.Kind != SparkEcho {
			t.Fatalf("cell %d: kind = %v, want SparkEcho", i, s.Kind)
		}
		if s.RTT != 7*time.Millisecond {
			t.Fatalf("cell %d: RTT = %v, want 7ms (echo latency)", i, s.RTT)
		}
	}
}

// TestRecordRoundAvg_HeldWhenRateLimitedAndUnsampled: on a rate-limited hop, a
// round with no TTL reply and no echo sent is Held (reachable, no reading) — not
// a drop, which would falsely read as packet loss between sparse echoes.
func TestRecordRoundAvg_HeldWhenRateLimitedAndUnsampled(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	driveRateLimited(pn, rateLimitMinEchoSamples+1, 7*time.Millisecond)

	// Round with neither a TTL reply nor an echo sent.
	pn.RecordRoundAvg()
	pn.MarkProbeRoundEnd()

	data := pn.SparklineData()
	last := data[len(data)-1]
	if last.Kind != SparkHeld {
		t.Fatalf("unsampled rate-limited round: kind = %v, want SparkHeld", last.Kind)
	}
}

// TestRecordRoundAvg_DropWhenEchoSentButLost: on a rate-limited hop, if an echo
// WAS sent this round and got no reply, that is a genuine drop — the Held state
// must not mask a real loss.
func TestRecordRoundAvg_DropWhenEchoSentButLost(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	driveRateLimited(pn, rateLimitMinEchoSamples+1, 7*time.Millisecond)

	// Echo sent this round, but no echo reply and no TTL reply.
	pn.MarkEchoSent()
	pn.RecordRoundAvg()
	pn.MarkProbeRoundEnd()

	data := pn.SparklineData()
	last := data[len(data)-1]
	if last.Kind != SparkDrop {
		t.Fatalf("echo sent but lost: kind = %v, want SparkDrop", last.Kind)
	}
}

// TestRecordRoundAvg_TTLWinsOverEcho: when both tiers reply, the direct TTL
// measurement takes precedence over echo.
func TestRecordRoundAvg_TTLWinsOverEcho(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.MarkEchoSent()
	pn.AddSample(5*time.Millisecond, 0, KindEcho)
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)
	pn.RecordRoundAvg()

	data := pn.SparklineData()
	if data[0].Kind != SparkTTL || data[0].RTT != 10*time.Millisecond {
		t.Fatalf("both tiers replied: got %+v, want SparkTTL @ 10ms", data[0])
	}
}

func TestPathNodeTrendStable(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	for i := 0; i < 10; i++ {
		pn.AddSample(10*time.Millisecond, 0, KindPrimary)
		pn.RecordRoundAvg()
	}
	if pn.Trend() != "stable" {
		t.Fatalf("expected stable, got %s", pn.Trend())
	}
}

func TestPathNodeTrendDegrading(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	for i := 0; i < 10; i++ {
		rtt := time.Duration(10+i*2) * time.Millisecond
		pn.AddSample(rtt, 0, KindPrimary)
		pn.RecordRoundAvg()
	}
	if pn.Trend() != "degrading" {
		t.Fatalf("expected degrading, got %s", pn.Trend())
	}
}

func TestPathNodeTrendImproving(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	for i := 0; i < 10; i++ {
		rtt := time.Duration(30-i*2) * time.Millisecond
		pn.AddSample(rtt, 0, KindPrimary)
		pn.RecordRoundAvg()
	}
	if pn.Trend() != "improving" {
		t.Fatalf("expected improving, got %s", pn.Trend())
	}
}

func TestPathNodeTrendInsufficientData(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)
	pn.RecordRoundAvg()
	if pn.Trend() != "stable" {
		t.Fatalf("expected stable with 1 round, got %s", pn.Trend())
	}
}

func TestPathNodeReset(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0, KindPrimary)

	pn.Reset()
	if pn.GetReceived() != 0 {
		t.Fatal("Reset should zero received")
	}
}

func TestPathNodeProbeKindRouting(t *testing.T) {
	pn := NewPathNode(net.ParseIP("198.51.100.7"))
	pn.AddSample(10*time.Millisecond, 0, KindICMPTTL)
	pn.AddSample(20*time.Millisecond, 1, KindPrimary)
	pn.AddSample(30*time.Millisecond, 0, KindEcho)

	icmp, prim, echo := pn.ICMPTTLStats(), pn.PrimaryStats(), pn.EchoStats()
	if icmp.Received != 1 || prim.Received != 1 || echo.Received != 1 {
		t.Fatalf("routing: icmp=%d prim=%d echo=%d, want 1/1/1",
			icmp.Received, prim.Received, echo.Received)
	}
	// Echo must NOT feed aggregate RTT stats.
	if pn.GetReceived() != 2 {
		t.Errorf("aggregate received = %d, want 2 (echo excluded)", pn.GetReceived())
	}
	if pn.GetMaxRTT() != 20*time.Millisecond {
		t.Errorf("aggregate MaxRTT = %v, want 20ms (echo excluded)", pn.GetMaxRTT())
	}
}

func TestPathNodeProbeKindRoutingV6(t *testing.T) {
	pn := NewPathNode(net.ParseIP("2001:db8::7"))
	pn.AddSample(10*time.Millisecond, 0, KindICMPTTL)
	pn.AddSample(30*time.Millisecond, 0, KindEcho)
	if pn.ICMPTTLStats().Received != 1 || pn.EchoStats().Received != 1 {
		t.Fatal("v6 routing broken")
	}
	if pn.GetReceived() != 1 {
		t.Errorf("v6 aggregate received = %d, want 1", pn.GetReceived())
	}
}

func TestPathNodeMarkEchoSent(t *testing.T) {
	pn := NewPathNode(net.ParseIP("198.51.100.7"))
	pn.MarkEchoSent()
	pn.MarkEchoSent()
	pn.AddSample(5*time.Millisecond, 0, KindEcho)
	echo := pn.EchoStats()
	if echo.Sent != 2 || echo.Received != 1 {
		t.Errorf("echo sent/recv = %d/%d, want 2/1", echo.Sent, echo.Received)
	}
	if got := echo.ProbeLossPercent(); got != 50.0 {
		t.Errorf("echo loss = %v, want 50", got)
	}
}

func TestPathNodeProbeStatsRoundEnd(t *testing.T) {
	pn := NewPathNode(net.ParseIP("198.51.100.7"))
	// Round 1: icmp replies; round 2: silence.
	pn.AddSample(10*time.Millisecond, 0, KindICMPTTL)
	pn.MarkProbeRoundEnd()
	pn.MarkProbeRoundEnd()
	icmp := pn.ICMPTTLStats()
	if icmp.Sent != 2 || icmp.RecvRounds != 1 {
		t.Errorf("icmpTTL sent/recvRounds = %d/%d, want 2/1", icmp.Sent, icmp.RecvRounds)
	}
	if got := icmp.RoundLossPercent(); got != 50.0 {
		t.Errorf("icmpTTL round loss = %v, want 50", got)
	}
	// Echo Sent must be untouched by round ends.
	if pn.EchoStats().Sent != 0 {
		t.Error("echo Sent moved on round end")
	}
}
