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
	if h.Rounds() != 0 {
		t.Fatal("new hop should have zero rounds")
	}
}

func TestHopAddSample(t *testing.T) {
	h := NewHop(1)
	ip := net.ParseIP("10.0.0.1")

	h.AddSample(ip, 10*time.Millisecond, 0, KindPrimary)
	h.AddSample(ip, 20*time.Millisecond, 0, KindPrimary)
	h.AddSample(ip, 15*time.Millisecond, 0, KindPrimary)

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
	// 10 rounds sent; 7 had at least one response => 30% loss
	for i := 0; i < 7; i++ {
		h.MarkRoundSent()
		h.AddSample(ip, 10*time.Millisecond, 0, KindPrimary)
		h.MarkRoundEnd()
	}
	for i := 0; i < 3; i++ {
		h.MarkRoundSent()
		h.MarkRoundEnd()
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
	h.AddSample(ip, 10*time.Millisecond, 0, KindPrimary)
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
	h.AddSample(net.ParseIP("1.1.1.1"), 5*time.Millisecond, 0, KindPrimary)
	tbl.MarkRoundSent(1)

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
	h.AddSample(ip, 10*time.Millisecond, 0, KindPrimary)
	h.AddSample(ip, 20*time.Millisecond, 0, KindPrimary)
	h.AddSample(ip, 30*time.Millisecond, 0, KindPrimary)

	stdev := h.StDev()
	expected := math.Sqrt(float64(200) / 3.0) // population stdev in ms
	if math.Abs(stdev-expected) > 0.01 {
		t.Fatalf("expected stdev ~%.2f ms, got %.2f ms", expected, stdev)
	}
}

func TestHopStDevSingleSample(t *testing.T) {
	h := NewHop(1)
	h.AddSample(net.ParseIP("10.0.0.1"), 10*time.Millisecond, 0, KindPrimary)
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
	h.MarkRoundSent()
	h.AddSample(ip, 10*time.Millisecond, 0, KindPrimary)
	h.AddSample(ip, 20*time.Millisecond, 0, KindPrimary)
	h.MarkRoundEnd()
	for i := 0; i < 4; i++ {
		h.MarkRoundSent()
		h.MarkRoundEnd()
	}

	h.Reset()

	if h.TTL != 3 {
		t.Fatal("Reset should preserve TTL")
	}
	if h.GetIP() != nil {
		t.Fatal("Reset should clear IP")
	}
	if h.Rounds() != 0 {
		t.Fatal("Reset should zero rounds")
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
	h.AddSample(ip1, 10*time.Millisecond, 0, KindPrimary)
	h.AddSample(ip2, 15*time.Millisecond, 1, KindPrimary)
	h.AddSample(ip1, 12*time.Millisecond, 0, KindPrimary)
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
	h.AddSample(ip, 10*time.Millisecond, 0, KindPrimary)
	h.AddSample(ip, 12*time.Millisecond, 1, KindPrimary)
	if h.IsDivergent() {
		t.Fatal("should not be divergent with 1 IP")
	}
}

func TestHopPrimaryNode(t *testing.T) {
	h := NewHop(4)
	ip1 := net.ParseIP("10.0.0.1")
	ip2 := net.ParseIP("10.0.0.2")
	h.AddSample(ip1, 10*time.Millisecond, 0, KindPrimary)
	h.AddSample(ip1, 12*time.Millisecond, 0, KindPrimary)
	h.AddSample(ip2, 15*time.Millisecond, 1, KindPrimary)
	primary := h.PrimaryNode()
	if !primary.IP.Equal(ip1) {
		t.Fatalf("expected primary 10.0.0.1, got %s", primary.IP)
	}
}

func TestHopMarkRoundEnd(t *testing.T) {
	h := NewHop(1)
	ip := net.ParseIP("10.0.0.1")
	h.AddSample(ip, 10*time.Millisecond, 0, KindPrimary)

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
	h.AddSample(ip, 10*time.Millisecond, 0, KindPrimary)

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
	// 10 rounds sent; 7 rounds had at least one response => 30% loss
	for i := 0; i < 7; i++ {
		h.MarkRoundSent()
		h.AddSample(ip1, 10*time.Millisecond, 0, KindPrimary)
		h.AddSample(ip2, 15*time.Millisecond, 1, KindPrimary)
		h.MarkRoundEnd()
	}
	for i := 0; i < 3; i++ {
		h.MarkRoundSent()
		h.MarkRoundEnd()
	}
	loss := h.LossPercent()
	if loss != 30.0 {
		t.Fatalf("expected 30%% loss, got %.1f%%", loss)
	}
}

func TestHopRoundBasedLoss(t *testing.T) {
	h := NewHop(3)
	ip := net.ParseIP("198.51.100.1")
	// Round 1: 6 ECMP flows all answer — counts as ONE received round.
	h.MarkRoundSent()
	for f := 0; f < 6; f++ {
		h.AddSample(ip, 10*time.Millisecond, f, KindPrimary)
	}
	h.MarkRoundEnd()
	// Round 2: silence.
	h.MarkRoundSent()
	h.MarkRoundEnd()
	if got := h.LossPercent(); got != 50.0 {
		t.Errorf("LossPercent = %v, want 50 (round-based, ECMP-agnostic)", got)
	}
	if h.Rounds() != 2 {
		t.Errorf("Rounds = %d, want 2", h.Rounds())
	}
}

func TestHopRoundBasedLossV6(t *testing.T) {
	h := NewHop(3)
	ip := net.ParseIP("2001:db8::1")
	// Round 1: 6 ECMP flows all answer — counts as ONE received round.
	h.MarkRoundSent()
	for f := 0; f < 6; f++ {
		h.AddSample(ip, 10*time.Millisecond, f, KindPrimary)
	}
	h.MarkRoundEnd()
	// Round 2: silence.
	h.MarkRoundSent()
	h.MarkRoundEnd()
	if got := h.LossPercent(); got != 50.0 {
		t.Errorf("LossPercent = %v, want 50 (round-based, ECMP-agnostic)", got)
	}
	if h.Rounds() != 2 {
		t.Errorf("Rounds = %d, want 2", h.Rounds())
	}
}

func TestTableGet(t *testing.T) {
	tbl := NewTable(30)

	// Get on unseen TTL returns nil and does not create a row.
	if got := tbl.Get(5); got != nil {
		t.Fatalf("Get on unseen TTL should return nil, got %v", got)
	}
	if snap := tbl.Snapshot(); len(snap) != 0 {
		t.Fatalf("Get miss must not create a row; snapshot len = %d", len(snap))
	}

	// After GetOrCreate the same TTL is found.
	h := tbl.GetOrCreate(5)
	if got := tbl.Get(5); got != h {
		t.Fatalf("Get after GetOrCreate should return same pointer; got %p want %p", got, h)
	}

	// A different unseen TTL still returns nil.
	if got := tbl.Get(99); got != nil {
		t.Fatalf("Get on different unseen TTL should return nil, got %v", got)
	}
	// Snapshot still has only the one row we created.
	if snap := tbl.Snapshot(); len(snap) != 1 {
		t.Fatalf("snapshot should have 1 row, got %d", len(snap))
	}
}

func TestHopEchoDoesNotFeedHopLoss(t *testing.T) {
	h := NewHop(3)
	ip := net.ParseIP("198.51.100.1")
	h.MarkRoundSent()
	h.AddSample(ip, 10*time.Millisecond, 0, KindEcho) // only an echo arrived
	h.MarkRoundEnd()
	if got := h.LossPercent(); got != 100.0 {
		t.Errorf("LossPercent = %v, want 100 (echo must not count as hop response)", got)
	}
}
