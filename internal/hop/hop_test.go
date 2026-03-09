package hop

import (
	"math"
	"net"
	"testing"
	"time"
)

func TestNewHop(t *testing.T) {
	h := NewHop(3)
	if h.TTL != 3 {
		t.Fatalf("expected TTL 3, got %d", h.TTL)
	}
	if h.GetSent() != 0 {
		t.Fatal("new hop should have zero sent")
	}
}

func TestHopAddSample(t *testing.T) {
	h := NewHop(1)
	ip := net.ParseIP("10.0.0.1")

	h.AddSample(ip, 10*time.Millisecond, 0)
	h.AddSample(ip, 20*time.Millisecond, 0)
	h.AddSample(ip, 15*time.Millisecond, 0)

	if h.GetIP().String() != "10.0.0.1" {
		t.Fatalf("expected IP 10.0.0.1, got %s", h.GetIP())
	}
	if h.AvgRTT() != 15*time.Millisecond {
		t.Fatalf("expected avg 15ms, got %v", h.AvgRTT())
	}
	if h.GetMinRTT() != 10*time.Millisecond {
		t.Fatalf("expected min 10ms, got %v", h.GetMinRTT())
	}
	if h.GetMaxRTT() != 20*time.Millisecond {
		t.Fatalf("expected max 20ms, got %v", h.GetMaxRTT())
	}
	if h.GetLastRTT() != 15*time.Millisecond {
		t.Fatalf("expected last 15ms, got %v", h.GetLastRTT())
	}
	pn := h.PrimaryNode()
	if pn.GetReceived() != 3 {
		t.Fatalf("expected 3 received, got %d", pn.GetReceived())
	}
}

func TestHopLossPercent(t *testing.T) {
	h := NewHop(1)
	ip := net.ParseIP("10.0.0.1")
	// Simulate 10 sent, 7 received
	for i := 0; i < 10; i++ {
		h.IncrementSent()
	}
	for i := 0; i < 7; i++ {
		h.AddSample(ip, 10*time.Millisecond, 0)
	}
	loss := h.LossPercent()
	if loss != 30.0 {
		t.Fatalf("expected 30%% loss, got %.1f%%", loss)
	}
}

func TestHopLossPercentZeroSent(t *testing.T) {
	h := NewHop(1)
	if h.LossPercent() != 0 {
		t.Fatal("zero sent should yield 0% loss")
	}
}

func TestHopSetHostname(t *testing.T) {
	h := NewHop(1)
	ip := net.ParseIP("10.0.0.1")
	h.AddSample(ip, 10*time.Millisecond, 0)
	h.SetHostname("router.local")
	if h.GetHostname() != "router.local" {
		t.Fatalf("expected router.local, got %s", h.GetHostname())
	}
}

func TestTableGetOrCreate(t *testing.T) {
	tbl := NewTable(30)
	h1 := tbl.GetOrCreate(5)
	h2 := tbl.GetOrCreate(5)
	if h1 != h2 {
		t.Fatal("expected same hop pointer for same TTL")
	}
	if h1.TTL != 5 {
		t.Fatalf("expected TTL 5, got %d", h1.TTL)
	}
}

func TestTableSnapshot(t *testing.T) {
	tbl := NewTable(30)
	h := tbl.GetOrCreate(1)
	h.AddSample(net.ParseIP("1.1.1.1"), 5*time.Millisecond, 0)
	h.IncrementSent()

	snap := tbl.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 hop in snapshot, got %d", len(snap))
	}
	if snap[0].TTL != 1 {
		t.Fatalf("expected TTL 1, got %d", snap[0].TTL)
	}
}

func TestTableMaxTTLReached(t *testing.T) {
	tbl := NewTable(30)
	tbl.GetOrCreate(1)
	tbl.GetOrCreate(5)
	if tbl.MaxTTLSeen() != 5 {
		t.Fatalf("expected max TTL 5, got %d", tbl.MaxTTLSeen())
	}
}

func TestHopStDev(t *testing.T) {
	h := NewHop(1)
	ip := net.ParseIP("10.0.0.1")

	// Samples: 10, 20, 30 ms => mean=20, variance=100/3, stdev≈5.77
	h.AddSample(ip, 10*time.Millisecond, 0)
	h.AddSample(ip, 20*time.Millisecond, 0)
	h.AddSample(ip, 30*time.Millisecond, 0)

	stdev := h.StDev()
	expected := math.Sqrt(float64(200) / 3.0) // population stdev in ms
	if math.Abs(stdev-expected) > 0.01 {
		t.Fatalf("expected stdev ~%.2f ms, got %.2f ms", expected, stdev)
	}
}

func TestHopStDevSingleSample(t *testing.T) {
	h := NewHop(1)
	h.AddSample(net.ParseIP("10.0.0.1"), 10*time.Millisecond, 0)
	if h.StDev() != 0 {
		t.Fatalf("single sample stdev should be 0, got %f", h.StDev())
	}
}

func TestHopStDevZeroSamples(t *testing.T) {
	h := NewHop(1)
	if h.StDev() != 0 {
		t.Fatal("zero samples stdev should be 0")
	}
}

func TestHopReset(t *testing.T) {
	h := NewHop(3)
	ip := net.ParseIP("10.0.0.1")
	h.AddSample(ip, 10*time.Millisecond, 0)
	h.AddSample(ip, 20*time.Millisecond, 0)
	for i := 0; i < 5; i++ {
		h.IncrementSent()
	}

	h.Reset()

	if h.TTL != 3 {
		t.Fatal("Reset should preserve TTL")
	}
	if h.GetIP() != nil {
		t.Fatal("Reset should clear IP")
	}
	if h.GetSent() != 0 {
		t.Fatal("Reset should zero sent")
	}
	if h.GetMinRTT() != 0 || h.GetMaxRTT() != 0 || h.GetLastRTT() != 0 {
		t.Fatal("Reset should zero RTTs")
	}
	if h.StDev() != 0 {
		t.Fatal("Reset should zero StDev")
	}
	if h.AvgRTT() != 0 {
		t.Fatal("Reset should zero AvgRTT")
	}
}

// --- Multi-IP / ECMP tests ---

func TestHopMultipleIPs(t *testing.T) {
	h := NewHop(4)
	ip1 := net.ParseIP("10.0.0.1")
	ip2 := net.ParseIP("10.0.0.2")
	h.AddSample(ip1, 10*time.Millisecond, 0)
	h.AddSample(ip2, 15*time.Millisecond, 1)
	h.AddSample(ip1, 12*time.Millisecond, 0)
	if !h.IsDivergent() {
		t.Fatal("expected divergent")
	}
	nodes := h.GetNodes()
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
}

func TestHopSingleIP(t *testing.T) {
	h := NewHop(3)
	ip := net.ParseIP("10.0.0.1")
	h.AddSample(ip, 10*time.Millisecond, 0)
	h.AddSample(ip, 12*time.Millisecond, 1)
	if h.IsDivergent() {
		t.Fatal("should not be divergent with 1 IP")
	}
}

func TestHopPrimaryNode(t *testing.T) {
	h := NewHop(4)
	ip1 := net.ParseIP("10.0.0.1")
	ip2 := net.ParseIP("10.0.0.2")
	h.AddSample(ip1, 10*time.Millisecond, 0)
	h.AddSample(ip1, 12*time.Millisecond, 0)
	h.AddSample(ip2, 15*time.Millisecond, 1)
	primary := h.PrimaryNode()
	if !primary.IP.Equal(ip1) {
		t.Fatalf("expected primary 10.0.0.1, got %s", primary.IP)
	}
}

func TestHopMarkRoundEnd(t *testing.T) {
	h := NewHop(1)
	ip := net.ParseIP("10.0.0.1")
	h.AddSample(ip, 10*time.Millisecond, 0)

	// MarkRoundEnd should not panic and should advance stability on all nodes
	h.MarkRoundEnd()
	nodes := h.GetNodes()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
}

func TestHopGetNodesIsCopy(t *testing.T) {
	h := NewHop(1)
	ip := net.ParseIP("10.0.0.1")
	h.AddSample(ip, 10*time.Millisecond, 0)

	nodes := h.GetNodes()
	nodes[0] = nil // mutate copy
	// Original should be unaffected
	if h.PrimaryNode() == nil {
		t.Fatal("GetNodes should return a copy, not a reference to internal slice")
	}
}

func TestHopLossPercentMultiNode(t *testing.T) {
	h := NewHop(1)
	ip1 := net.ParseIP("10.0.0.1")
	ip2 := net.ParseIP("10.0.0.2")
	// 10 probes sent total, 4 received by ip1, 3 received by ip2 = 7 total received => 30% loss
	for i := 0; i < 10; i++ {
		h.IncrementSent()
	}
	for i := 0; i < 4; i++ {
		h.AddSample(ip1, 10*time.Millisecond, 0)
	}
	for i := 0; i < 3; i++ {
		h.AddSample(ip2, 15*time.Millisecond, 1)
	}
	loss := h.LossPercent()
	if loss != 30.0 {
		t.Fatalf("expected 30%% loss, got %.1f%%", loss)
	}
}
