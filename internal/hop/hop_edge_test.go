package hop

import (
	"math"
	"net"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Welford boundary conditions
// ---------------------------------------------------------------------------

// TestWelfordZeroSamples: StDev on a fresh PathNode must be 0, not NaN.
func TestWelfordZeroSamples(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	stdev := pn.StDev()
	if math.IsNaN(stdev) || stdev != 0 {
		t.Fatalf("expected StDev==0 for zero samples, got %f", stdev)
	}
	if math.IsNaN(float64(pn.AvgRTT())) {
		t.Fatal("AvgRTT should not be NaN for zero samples")
	}
	if pn.AvgRTT() != 0 {
		t.Fatalf("expected AvgRTT==0, got %v", pn.AvgRTT())
	}
}

// TestWelfordOneSample: mean equals the value, stdev must be 0 (not NaN).
func TestWelfordOneSample(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	pn.AddSample(42*time.Millisecond, 0)

	if pn.AvgRTT() != 42*time.Millisecond {
		t.Fatalf("expected mean 42ms, got %v", pn.AvgRTT())
	}
	stdev := pn.StDev()
	if math.IsNaN(stdev) || stdev != 0 {
		t.Fatalf("expected StDev==0 for one sample, got %f", stdev)
	}
}

// TestWelfordTwoIdenticalSamples: same value twice → stdev 0.
func TestWelfordTwoIdenticalSamples(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	pn.AddSample(20*time.Millisecond, 0)
	pn.AddSample(20*time.Millisecond, 1)

	if pn.AvgRTT() != 20*time.Millisecond {
		t.Fatalf("expected mean 20ms, got %v", pn.AvgRTT())
	}
	stdev := pn.StDev()
	if math.IsNaN(stdev) || stdev > 0.0001 {
		t.Fatalf("expected StDev≈0 for two identical samples, got %f", stdev)
	}
}

// TestWelford1000Samples: numerical stability — stdev for 1..1000 ms uniform.
// Population stdev of 1,2,...,N is sqrt((N^2-1)/12).  For N=1000 that is ~288.675.
func TestWelford1000Samples(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	for i := 1; i <= 1000; i++ {
		pn.AddSample(time.Duration(i)*time.Millisecond, 0)
	}
	stdev := pn.StDev()
	// Population stdev of {1..N}: sqrt((N²-1)/12)
	n := 1000.0
	expected := math.Sqrt((n*n - 1) / 12.0)
	if math.Abs(stdev-expected) > 0.5 {
		t.Fatalf("1000-sample stdev: expected ~%.3f, got %.3f", expected, stdev)
	}
}

// TestPathNodeResetAfterSamples: Reset wipes Welford state, post-reset stdev==0.
func TestPathNodeResetAfterSamples(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	for i := 0; i < 20; i++ {
		pn.AddSample(time.Duration(i)*time.Millisecond, 0)
	}
	pn.Reset()

	if pn.StDev() != 0 {
		t.Fatalf("expected StDev==0 after Reset, got %f", pn.StDev())
	}
	if pn.GeoMean() != 0 {
		t.Fatalf("expected GeoMean==0 after Reset, got %v", pn.GeoMean())
	}
	if pn.AvgRTT() != 0 {
		t.Fatalf("expected AvgRTT==0 after Reset, got %v", pn.AvgRTT())
	}
}

// ---------------------------------------------------------------------------
// Geometric mean edge cases
// ---------------------------------------------------------------------------

// TestGeoMeanSingleValue: geometric mean of one value equals that value.
func TestGeoMeanSingleValue(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	pn.AddSample(50*time.Millisecond, 0)
	gm := pn.GeoMean()
	gmMs := float64(gm) / float64(time.Millisecond)
	if math.Abs(gmMs-50.0) > 0.1 {
		t.Fatalf("expected GeoMean≈50ms, got %.2fms", gmMs)
	}
}

// TestGeoMeanVerySmallValues: 0.001ms floor prevents log(0) / underflow.
func TestGeoMeanVerySmallValues(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	// 1 µs = 0.001 ms (exactly at the floor)
	pn.AddSample(1*time.Microsecond, 0)
	pn.AddSample(1*time.Microsecond, 0)
	gm := pn.GeoMean()
	if gm <= 0 {
		t.Fatalf("expected positive GeoMean for sub-ms values, got %v", gm)
	}
	if math.IsNaN(float64(gm)) || math.IsInf(float64(gm), 0) {
		t.Fatalf("GeoMean must not be NaN or Inf for tiny values, got %v", gm)
	}
}

// ---------------------------------------------------------------------------
// PathNode LossPercent with zero sent
// ---------------------------------------------------------------------------

// TestPathNodeLossPercentZeroSent: no divide-by-zero when sent==0.
func TestPathNodeLossPercentZeroSent(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	if pn.LossPercent() != 0 {
		t.Fatalf("expected 0%% loss when sent==0, got %f", pn.LossPercent())
	}
}

// TestPathNodeLossPercentAllTimeout: 100% loss when all probes lost.
func TestPathNodeLossPercentAllTimeout(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	for i := 0; i < 5; i++ {
		pn.IncrementSent()
	}
	// No AddSample calls — all lost.
	loss := pn.LossPercent()
	if loss != 100.0 {
		t.Fatalf("expected 100%% loss, got %.1f%%", loss)
	}
}

// TestPathNodeLossPercentAlternating: interleaved loss/success.
func TestPathNodeLossPercentAlternating(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	for i := 0; i < 10; i++ {
		pn.IncrementSent()
	}
	for i := 0; i < 5; i++ {
		pn.AddSample(10*time.Millisecond, 0)
	}
	loss := pn.LossPercent()
	if loss != 50.0 {
		t.Fatalf("expected 50%% loss, got %.1f%%", loss)
	}
}

// ---------------------------------------------------------------------------
// StabilityPercent zero count branch
// ---------------------------------------------------------------------------

// TestStabilityPercentZeroCount: fresh node returns 0, not NaN.
func TestStabilityPercentZeroCount(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	sp := pn.StabilityPercent()
	if sp != 0 {
		t.Fatalf("expected 0%% stability for fresh node, got %.1f%%", sp)
	}
}

// ---------------------------------------------------------------------------
// PathNode.AvgRTT zero-samples branch
// ---------------------------------------------------------------------------

// TestPathNodeAvgRTTZeroSamples: AvgRTT must return 0 when received==0.
func TestPathNodeAvgRTTZeroSamples(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	if pn.AvgRTT() != 0 {
		t.Fatalf("expected AvgRTT==0 for zero samples, got %v", pn.AvgRTT())
	}
}

// ---------------------------------------------------------------------------
// SparklineData full-ring-buffer ordering
// ---------------------------------------------------------------------------

// TestSparklineDataFullBufferOrder: when buffer is full the oldest entry is
// returned first (start = sparkIdx, not 0).
func TestSparklineDataFullBufferOrder(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	// Fill exactly 28 rounds (sparklineSize).
	for i := 1; i <= sparklineSize; i++ {
		pn.AddSample(time.Duration(i)*time.Millisecond, 0)
		pn.RecordRoundAvg()
	}
	data := pn.SparklineData()
	if len(data) != sparklineSize {
		t.Fatalf("expected %d entries, got %d", sparklineSize, len(data))
	}
	// Chronologically oldest is sample 1 (1ms).
	if data[0] != 1*time.Millisecond {
		t.Fatalf("expected oldest=1ms, got %v", data[0])
	}
	// Chronologically newest is sample 28.
	if data[sparklineSize-1] != time.Duration(sparklineSize)*time.Millisecond {
		t.Fatalf("expected newest=%dms, got %v", sparklineSize, data[sparklineSize-1])
	}
}

// ---------------------------------------------------------------------------
// Trend: threshold elevation branch (avgMs > 50ms triggers pct path)
// ---------------------------------------------------------------------------

// TestTrendThresholdElevated: when avg RTT > 50ms the threshold switches to
// 1% of avgMs. With high and slowly-rising latency the slope stays sub-threshold
// and the trend reads "stable" despite the elevated absolute latency.
func TestTrendThresholdElevated(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	// 10 rounds at 200ms with imperceptible linear drift (+0.1ms/round).
	// Slope ≈ 0.1; threshold = 200*0.01 = 2.0 — so slope < threshold → "stable".
	for i := 0; i < 10; i++ {
		rtt := time.Duration(200000+i*100) * time.Microsecond // 200.0 to 200.9 ms
		pn.AddSample(rtt, 0)
		pn.RecordRoundAvg()
	}
	trend := pn.Trend()
	if trend != "stable" {
		t.Fatalf("expected stable with tiny slope at high RTT, got %q", trend)
	}
}

// ---------------------------------------------------------------------------
// Hop-level nil-primary-node delegation
// ---------------------------------------------------------------------------

// TestHopGetHostnameNoNodes: GetHostname on a hop with no nodes returns "".
func TestHopGetHostnameNoNodes(t *testing.T) {
	h := NewHop(1)
	if h.GetHostname() != "" {
		t.Fatalf("expected empty hostname for hop with no nodes, got %q", h.GetHostname())
	}
}

// TestHopSetHostnameNoNodes: SetHostname on empty hop must not panic.
func TestHopSetHostnameNoNodes(t *testing.T) {
	h := NewHop(1)
	h.SetHostname("should-not-crash.local") // no nodes — must be a no-op
}

// TestHopPrimaryNodeNil: PrimaryNode returns nil when there are no nodes.
func TestHopPrimaryNodeNil(t *testing.T) {
	h := NewHop(1)
	if h.PrimaryNode() != nil {
		t.Fatal("expected nil PrimaryNode for empty hop")
	}
}

// ---------------------------------------------------------------------------
// Table.IncrementSent and Table.ResetAll
// ---------------------------------------------------------------------------

// TestTableIncrementSent: Table.IncrementSent creates hop + increments sentTotal.
func TestTableIncrementSent(t *testing.T) {
	tbl := NewTable(10)
	tbl.IncrementSent(3)
	tbl.IncrementSent(3)
	h := tbl.GetOrCreate(3)
	// sentTotal is private; verify via LossPercent (2 sent, 0 received → 100%).
	loss := h.LossPercent()
	if loss != 100.0 {
		t.Fatalf("expected 100%% loss after 2 IncrementSent with no samples, got %.1f%%", loss)
	}
}

// TestTableResetAll: ResetAll clears samples on every hop but keeps structure.
func TestTableResetAll(t *testing.T) {
	tbl := NewTable(10)

	h1 := tbl.GetOrCreate(1)
	h1.AddSample(net.ParseIP("192.0.2.1"), 10*time.Millisecond, 0)
	h1.IncrementSent()

	h2 := tbl.GetOrCreate(2)
	h2.AddSample(net.ParseIP("192.0.2.2"), 20*time.Millisecond, 0)
	h2.IncrementSent()

	tbl.ResetAll()

	// After reset all hops should report zero RTT and cleared nodes.
	snap := tbl.Snapshot()
	// Hops still exist in the table (structure preserved).
	if len(snap) != 2 {
		t.Fatalf("expected 2 hops after ResetAll, got %d", len(snap))
	}
	for _, h := range snap {
		if h.GetIP() != nil {
			t.Fatalf("TTL %d: expected nil IP after ResetAll, got %s", h.TTL, h.GetIP())
		}
		if h.AvgRTT() != 0 {
			t.Fatalf("TTL %d: expected AvgRTT==0 after ResetAll, got %v", h.TTL, h.AvgRTT())
		}
	}
}

// ---------------------------------------------------------------------------
// FormatFlowIDs — all branches
// ---------------------------------------------------------------------------

// TestHopPrimaryNodeSecondIPWins: when the second IP has more received samples
// it becomes primary — exercises the recv>bestRecv body in primaryNodeLocked.
func TestHopPrimaryNodeSecondIPWins(t *testing.T) {
	h := NewHop(5)
	ip1 := net.ParseIP("192.0.2.1")
	ip2 := net.ParseIP("192.0.2.2")
	// ip1 gets 1 sample, ip2 gets 3 — ip2 should be primary.
	h.AddSample(ip1, 10*time.Millisecond, 0)
	h.AddSample(ip2, 20*time.Millisecond, 1)
	h.AddSample(ip2, 21*time.Millisecond, 1)
	h.AddSample(ip2, 22*time.Millisecond, 1)
	pn := h.PrimaryNode()
	if pn == nil {
		t.Fatal("expected non-nil primary node")
	}
	if !pn.IP.Equal(ip2) {
		t.Fatalf("expected primary to be ip2 (192.0.2.2), got %s", pn.IP)
	}
}

// TestSparklineDataEmpty: SparklineData on a fresh node returns nil (sparkCount==0).
func TestSparklineDataEmpty(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.1"))
	data := pn.SparklineData()
	if data != nil {
		t.Fatalf("expected nil SparklineData for fresh node, got %v", data)
	}
}

// ---------------------------------------------------------------------------
// FormatFlowIDs — all branches
// ---------------------------------------------------------------------------

// TestFormatFlowIDsEmpty: empty slice returns "-".
func TestFormatFlowIDsEmpty(t *testing.T) {
	got := FormatFlowIDs(nil)
	if got != "-" {
		t.Fatalf("expected \"-\", got %q", got)
	}
}

// TestFormatFlowIDsSingle: single element formatted 1-indexed.
func TestFormatFlowIDsSingle(t *testing.T) {
	got := FormatFlowIDs([]int{0})
	if got != "1" {
		t.Fatalf("expected \"1\", got %q", got)
	}
}

// TestFormatFlowIDsTwoConsecutive: 2 consecutive IDs → "1,2" (not range; len<=2).
func TestFormatFlowIDsTwoConsecutive(t *testing.T) {
	got := FormatFlowIDs([]int{0, 1})
	if got != "1,2" {
		t.Fatalf("expected \"1,2\", got %q", got)
	}
}

// TestFormatFlowIDsThreeConsecutive: 3+ consecutive IDs → compact range "1-3".
func TestFormatFlowIDsThreeConsecutive(t *testing.T) {
	got := FormatFlowIDs([]int{0, 1, 2})
	if got != "1-3" {
		t.Fatalf("expected \"1-3\", got %q", got)
	}
}

// TestFormatFlowIDsNonConsecutive: non-consecutive IDs → comma list.
func TestFormatFlowIDsNonConsecutive(t *testing.T) {
	got := FormatFlowIDs([]int{0, 2, 4})
	if got != "1,3,5" {
		t.Fatalf("expected \"1,3,5\", got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Rate-limit heuristic edge cases
// ---------------------------------------------------------------------------

// TestDetectRateLimited_HopWithNoIP: a hop in the table with nil IP should
// be skipped without panic (covers the !ok || GetIP()==nil continue path).
func TestDetectRateLimited_HopWithNoIP(t *testing.T) {
	tbl := NewTable(3)

	// TTL 1: normal hop with replies
	h1 := tbl.GetOrCreate(1)
	for i := 0; i < 10; i++ {
		h1.AddSample(net.ParseIP("192.0.2.1"), time.Millisecond, 0)
		h1.IncrementSent()
	}

	// TTL 2: hop exists but has no IP (no AddSample called, only IncrementSent).
	h2 := tbl.GetOrCreate(2)
	for i := 0; i < 10; i++ {
		h2.IncrementSent()
	}

	// TTL 3: normal hop
	h3 := tbl.GetOrCreate(3)
	for i := 0; i < 10; i++ {
		h3.AddSample(net.ParseIP("192.0.2.3"), time.Millisecond, 0)
		h3.IncrementSent()
	}

	// Must not panic; TTL 2 has no IP so it should be skipped.
	rl := DetectRateLimited(tbl.Snapshot(), 3)
	if rl[2] {
		t.Fatal("hop with no IP should not be flagged as rate-limited")
	}
}

// TestDetectRateLimited_HighLossDownstream: if downstream hops ALSO have high
// loss it is a real network issue, not rate-limiting.
func TestDetectRateLimited_HighLossDownstream(t *testing.T) {
	tbl := NewTable(3)

	// TTL 1: 0% loss
	h1 := tbl.GetOrCreate(1)
	for i := 0; i < 10; i++ {
		h1.AddSample(net.ParseIP("192.0.2.1"), time.Millisecond, 0)
		h1.IncrementSent()
	}

	// TTL 2: 50% loss
	h2 := tbl.GetOrCreate(2)
	for i := 0; i < 5; i++ {
		h2.AddSample(net.ParseIP("192.0.2.2"), time.Millisecond, 0)
	}
	for i := 0; i < 10; i++ {
		h2.IncrementSent()
	}

	// TTL 3 (maxTTL): also 50% loss — NOT rate-limiting at TTL 2.
	h3 := tbl.GetOrCreate(3)
	for i := 0; i < 5; i++ {
		h3.AddSample(net.ParseIP("192.0.2.3"), time.Millisecond, 0)
	}
	for i := 0; i < 10; i++ {
		h3.IncrementSent()
	}

	rl := DetectRateLimited(tbl.Snapshot(), 3)
	if rl[2] {
		t.Fatal("hop 2 should NOT be rate-limited when downstream loss is equally high")
	}
}

// TestDetectRateLimited_LowLossHop: a hop with ≤1% loss must never be flagged.
func TestDetectRateLimited_LowLossHop(t *testing.T) {
	tbl := NewTable(2)

	h1 := tbl.GetOrCreate(1)
	for i := 0; i < 100; i++ {
		h1.AddSample(net.ParseIP("192.0.2.1"), time.Millisecond, 0)
		h1.IncrementSent()
	}
	// 1 lost out of 100 = exactly 1% — at the boundary (threshold is >1.0)
	h1.IncrementSent()

	h2 := tbl.GetOrCreate(2)
	for i := 0; i < 100; i++ {
		h2.AddSample(net.ParseIP("192.0.2.2"), time.Millisecond, 0)
		h2.IncrementSent()
	}

	rl := DetectRateLimited(tbl.Snapshot(), 2)
	if rl[1] {
		t.Fatal("hop with exactly 1% loss should not be rate-limited (threshold >1.0)")
	}
}

// TestDetectRateLimited_BoundaryDifferential: the detection requires downstream
// loss to be < hopLoss-5.  At exactly hopLoss-5 it should NOT fire.
func TestDetectRateLimited_BoundaryDifferential(t *testing.T) {
	tbl := NewTable(2)

	// TTL 1: 50% loss
	h1 := tbl.GetOrCreate(1)
	for i := 0; i < 5; i++ {
		h1.AddSample(net.ParseIP("192.0.2.1"), time.Millisecond, 0)
	}
	for i := 0; i < 10; i++ {
		h1.IncrementSent()
	}

	// TTL 2 (maxTTL, downstream): exactly 45% loss → loss == hopLoss - 5 (not < hopLoss-5).
	h2 := tbl.GetOrCreate(2)
	for i := 0; i < 55; i++ {
		h2.AddSample(net.ParseIP("192.0.2.2"), time.Millisecond, 0)
	}
	for i := 0; i < 100; i++ {
		h2.IncrementSent()
	}

	rl := DetectRateLimited(tbl.Snapshot(), 2)
	// minDownstream after processing TTL 2 (maxTTL) = 45.
	// For TTL 1: loss=50, condition: minDownstream(45) < loss-5.0(45) → 45 < 45 is false.
	if rl[1] {
		t.Fatal("hop 1 should NOT be rate-limited when downstream loss is exactly hopLoss-5")
	}
}
