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
