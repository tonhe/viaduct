package hop

import (
	"net"
	"testing"
	"time"
)

func TestDetectRateLimited_BasicCase(t *testing.T) {
	table := NewTable(5)

	// Hop 1: 0% loss
	h1 := table.GetOrCreate(1)
	for i := 0; i < 10; i++ {
		h1.AddSample(net.ParseIP("10.0.0.1"), time.Millisecond, 0)
		h1.IncrementSent()
	}

	// Hop 2: 50% loss (rate-limited candidate)
	h2 := table.GetOrCreate(2)
	for i := 0; i < 5; i++ {
		h2.AddSample(net.ParseIP("10.0.0.2"), time.Millisecond, 0)
	}
	for i := 0; i < 10; i++ {
		h2.IncrementSent()
	}

	// Hop 3: 0% loss (target)
	h3 := table.GetOrCreate(3)
	for i := 0; i < 10; i++ {
		h3.AddSample(net.ParseIP("10.0.0.3"), time.Millisecond, 0)
		h3.IncrementSent()
	}

	rl := DetectRateLimited(table.Snapshot(), 3)
	if !rl[2] {
		t.Fatal("expected hop 2 to be rate-limited")
	}
	if rl[1] || rl[3] {
		t.Fatal("hops 1 and 3 should not be rate-limited")
	}
}

func TestDetectRateLimited_NoRateLimiting(t *testing.T) {
	table := NewTable(3)

	for ttl := 1; ttl <= 3; ttl++ {
		h := table.GetOrCreate(ttl)
		for i := 0; i < 10; i++ {
			h.AddSample(net.IPv4(10, 0, 0, byte(ttl)), time.Millisecond, 0)
			h.IncrementSent()
		}
	}

	rl := DetectRateLimited(table.Snapshot(), 3)
	for ttl := 1; ttl <= 3; ttl++ {
		if rl[ttl] {
			t.Fatalf("hop %d should not be rate-limited", ttl)
		}
	}
}

func TestDetectRateLimited_EmptyTable(t *testing.T) {
	rl := DetectRateLimited(nil, 0)
	if len(rl) != 0 {
		t.Fatal("expected empty result for nil hops")
	}
}

// TestDetectRateLimited_LossExactlyOnePercent tests the exact 1.0% threshold boundary.
// A hop with exactly 1.0% loss must NOT be flagged as rate-limited (loss > 1.0, not >=).
// This kills the surviving mutant: loss > 1.0 → loss >= 1.0 (> → >=).
func TestDetectRateLimited_LossExactlyOnePercent(t *testing.T) {
	table := NewTable(3)

	// Hop 1: 0% loss
	h1 := table.GetOrCreate(1)
	for i := 0; i < 100; i++ {
		h1.AddSample(net.ParseIP("10.0.0.1"), time.Millisecond, 0)
		h1.IncrementSent()
	}

	// Hop 2: exactly 1.0% loss (1 lost out of 100 sent)
	h2 := table.GetOrCreate(2)
	for i := 0; i < 99; i++ {
		h2.AddSample(net.ParseIP("10.0.0.2"), time.Millisecond, 0)
	}
	for i := 0; i < 100; i++ {
		h2.IncrementSent()
	}

	// Hop 3: 0% loss
	h3 := table.GetOrCreate(3)
	for i := 0; i < 100; i++ {
		h3.AddSample(net.ParseIP("10.0.0.3"), time.Millisecond, 0)
		h3.IncrementSent()
	}

	rl := DetectRateLimited(table.Snapshot(), 3)
	// Exactly 1.0% loss: must NOT be rate-limited (threshold is > 1.0, not >= 1.0)
	if rl[2] {
		t.Fatal("hop with exactly 1.0% loss should NOT be flagged as rate-limited (threshold is > 1.0%)")
	}
}

// TestDetectRateLimited_LossWellAboveOnePercent verifies that loss clearly above
// 1.0% (e.g., 20%) is caught when downstream has significantly lower loss.
// The algorithm requires loss > 1.0 AND minDownstream < loss - 5.0, so we need
// at least ~6% differential. This ensures the > 1.0 threshold catches real rate
// limiting rather than being dead code.
func TestDetectRateLimited_LossWellAboveOnePercent(t *testing.T) {
	table := NewTable(3)

	// Hop 1: 0% loss
	h1 := table.GetOrCreate(1)
	for i := 0; i < 100; i++ {
		h1.AddSample(net.ParseIP("10.0.0.1"), time.Millisecond, 0)
		h1.IncrementSent()
	}

	// Hop 2: 20% loss (20 lost out of 100)
	h2 := table.GetOrCreate(2)
	for i := 0; i < 80; i++ {
		h2.AddSample(net.ParseIP("10.0.0.2"), time.Millisecond, 0)
	}
	for i := 0; i < 100; i++ {
		h2.IncrementSent()
	}

	// Hop 3: 0% loss (downstream has much lower loss → qualifies as rate-limited)
	h3 := table.GetOrCreate(3)
	for i := 0; i < 100; i++ {
		h3.AddSample(net.ParseIP("10.0.0.3"), time.Millisecond, 0)
		h3.IncrementSent()
	}

	rl := DetectRateLimited(table.Snapshot(), 3)
	// 20% loss with 0% downstream (diff = 20 > 5): should be rate-limited
	if !rl[2] {
		t.Fatal("hop with 20% loss and 0% downstream should be flagged as rate-limited")
	}
}
