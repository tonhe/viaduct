package hop

import (
	"math"
	"net"
	"testing"
	"time"
)

func TestPathNodeAddSample(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0)
	pn.AddSample(20*time.Millisecond, 1)

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
	pn.AddSample(10*time.Millisecond, 0)
	pn.AddSample(15*time.Millisecond, 2)
	pn.AddSample(12*time.Millisecond, 0)

	flows := pn.GetFlowIDs()
	if len(flows) != 2 {
		t.Fatalf("expected 2 unique flows, got %d", len(flows))
	}
}

func TestPathNodeStDev(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0)
	pn.AddSample(20*time.Millisecond, 0)
	pn.AddSample(30*time.Millisecond, 0)

	stdev := pn.StDev()
	expected := math.Sqrt(float64(200) / 3.0)
	if math.Abs(stdev-expected) > 0.01 {
		t.Fatalf("expected stdev ~%.2f, got %.2f", expected, stdev)
	}
}

func TestPathNodeLossPercent(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.IncrementSent()
	pn.IncrementSent()
	pn.IncrementSent()
	pn.AddSample(10*time.Millisecond, 0)

	loss := pn.LossPercent()
	if math.Abs(loss-66.666) > 0.1 {
		t.Fatalf("expected ~66.7%% loss, got %.1f%%", loss)
	}
}

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
	pn.AddSample(10*time.Millisecond, 0)
	pn.AddSample(100*time.Millisecond, 0)
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
	pn.AddSample(100*time.Microsecond, 0) // 0.1ms
	pn.AddSample(200*time.Microsecond, 0) // 0.2ms
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
	pn.AddSample(10*time.Millisecond, 0)
	pn.AddSample(15*time.Millisecond, 0)
	// Jitter = |15 - 10| = 5ms
	j := pn.Jitter()
	if j != 5*time.Millisecond {
		t.Fatalf("expected jitter 5ms, got %v", j)
	}
}

func TestPathNodeJitterFirstSample(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0)
	// No previous RTT, jitter should be 0
	if pn.Jitter() != 0 {
		t.Fatalf("expected 0 jitter on first sample, got %v", pn.Jitter())
	}
}

func TestPathNodeJitterMean(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0)
	pn.AddSample(20*time.Millisecond, 0) // jitter: 10ms
	pn.AddSample(15*time.Millisecond, 0) // jitter: 5ms
	pn.AddSample(25*time.Millisecond, 0) // jitter: 10ms
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
	pn.AddSample(10*time.Millisecond, 0)
	pn.RecordRoundAvg()
	// Round 2: one probe
	pn.AddSample(20*time.Millisecond, 0)
	pn.RecordRoundAvg()
	// Round 3: one probe
	pn.AddSample(15*time.Millisecond, 0)
	pn.RecordRoundAvg()

	data := pn.SparklineData()
	if len(data) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(data))
	}
	if data[0] != 10*time.Millisecond || data[1] != 20*time.Millisecond || data[2] != 15*time.Millisecond {
		t.Fatalf("unexpected data: %v", data)
	}
}

func TestPathNodeSparklineRingOverflow(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	// Fill beyond capacity (28) — one probe per round
	for i := 0; i < 35; i++ {
		pn.AddSample(time.Duration(i)*time.Millisecond, 0)
		pn.RecordRoundAvg()
	}
	data := pn.SparklineData()
	if len(data) != 28 {
		t.Fatalf("expected 28 samples (capped), got %d", len(data))
	}
	// Oldest should be sample 7 (35-28=7)
	if data[0] != 7*time.Millisecond {
		t.Fatalf("expected oldest=7ms, got %v", data[0])
	}
}

func TestPathNodeSparklineLostProbe(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	// Round 1: reply
	pn.AddSample(10*time.Millisecond, 0)
	pn.RecordRoundAvg()
	// Round 2: no replies — RecordRoundAvg records loss
	pn.RecordRoundAvg()
	// Round 3: reply
	pn.AddSample(12*time.Millisecond, 0)
	pn.RecordRoundAvg()

	data := pn.SparklineData()
	if len(data) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(data))
	}
	if data[1] != 0 { // 0 = loss/timeout sentinel
		t.Fatalf("expected 0 for loss, got %v", data[1])
	}
}

func TestPathNodeRecordRoundAvgRecordsLossWhenNoProbes(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	// Round 1: reply received
	pn.AddSample(10*time.Millisecond, 0)
	pn.RecordRoundAvg()

	// Round 2: no probes received — RecordRoundAvg records sparkline loss
	pn.RecordRoundAvg()

	data := pn.SparklineData()
	if len(data) != 2 {
		t.Fatalf("expected 2 entries (1 sample + 1 loss), got %d", len(data))
	}
	if data[1] != 0 {
		t.Fatalf("expected 0 (loss sentinel) for no-reply round, got %v", data[1])
	}
}

func TestPathNodeTrendStable(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	for i := 0; i < 10; i++ {
		pn.AddSample(10*time.Millisecond, 0)
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
		pn.AddSample(rtt, 0)
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
		pn.AddSample(rtt, 0)
		pn.RecordRoundAvg()
	}
	if pn.Trend() != "improving" {
		t.Fatalf("expected improving, got %s", pn.Trend())
	}
}

func TestPathNodeTrendInsufficientData(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0)
	pn.RecordRoundAvg()
	if pn.Trend() != "stable" {
		t.Fatalf("expected stable with 1 round, got %s", pn.Trend())
	}
}

func TestPathNodeReset(t *testing.T) {
	pn := NewPathNode(net.ParseIP("10.0.0.1"))
	pn.AddSample(10*time.Millisecond, 0)
	pn.IncrementSent()

	pn.Reset()
	if pn.GetReceived() != 0 {
		t.Fatal("Reset should zero received")
	}
	if pn.GetSent() != 0 {
		t.Fatal("Reset should zero sent")
	}
}
