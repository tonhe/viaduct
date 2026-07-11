package resolve

// Tests for Resolver: cache correctness, deduplication, concurrent safety,
// context cancellation, and post-cancellation behavior.
//
// DNS seam: lookupFunc field on Resolver (see resolve.go).
// Tests inject a fake lookup so no real network I/O occurs.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestResolver builds a Resolver whose DNS lookup is replaced by fn.
// Extracted seam: lookupFunc field on Resolver (see resolve.go).
func newTestResolver(workers int, fn func(context.Context, net.IP) (string, error)) *Resolver {
	return &Resolver{
		workers:    workers,
		reqCh:      make(chan net.IP, 256),
		lookupFunc: fn,
	}
}

// startResolver runs r.Run in a background goroutine and returns a cancel func.
func startResolver(t *testing.T, r *Resolver, results chan<- Result) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	go func() {
		r.Run(ctx, results)
	}()
	return cancel
}

// waitResult blocks until one Result arrives or the deadline expires.
func waitResult(t *testing.T, results <-chan Result, d time.Duration) Result {
	t.Helper()
	select {
	case r := <-results:
		return r
	case <-time.After(d):
		t.Fatal("timed out waiting for resolve result")
		return Result{}
	}
}

// doc IP helpers — all from documentation address space (RFC 5737).
func docIP(suffix int) net.IP {
	return net.ParseIP(fmt.Sprintf("192.0.2.%d", suffix))
}

// ---------------------------------------------------------------------------
// Existing tests (preserved)
// ---------------------------------------------------------------------------

func TestCacheHitMiss(t *testing.T) {
	r := New(4)

	// Miss
	if _, ok := r.Lookup(net.ParseIP("192.0.2.1")); ok {
		t.Fatal("expected cache miss")
	}

	// Populate cache directly
	r.cache.Store("192.0.2.1", "host.example.com")

	// Hit
	name, ok := r.Lookup(net.ParseIP("192.0.2.1"))
	if !ok {
		t.Fatal("expected cache hit")
	}
	if name != "host.example.com" {
		t.Fatalf("expected host.example.com, got %s", name)
	}
}

func TestDeduplication(t *testing.T) {
	r := New(4)
	ip := net.ParseIP("192.0.2.2")

	submitted1 := r.Submit(ip)
	submitted2 := r.Submit(ip)

	if !submitted1 {
		t.Fatal("first submit should return true")
	}
	if submitted2 {
		t.Fatal("second submit should return false (dedup)")
	}
}

// ---------------------------------------------------------------------------
// Cache correctness
// ---------------------------------------------------------------------------

func TestCacheHit_FullRoundTrip(t *testing.T) {
	wantHostname := "host.example.com"
	ip := docIP(10)

	r := newTestResolver(1, func(_ context.Context, _ net.IP) (string, error) {
		return wantHostname, nil
	})

	results := make(chan Result, 1)
	cancel := startResolver(t, r, results)
	defer cancel()

	r.Submit(ip)
	res := waitResult(t, results, 2*time.Second)

	if res.Hostname != wantHostname {
		t.Errorf("hostname = %q, want %q", res.Hostname, wantHostname)
	}

	// Cache hit: Lookup returns the value without querying again
	got, ok := r.Lookup(ip)
	if !ok {
		t.Fatal("Lookup after resolution: expected cache hit")
	}
	if got != wantHostname {
		t.Errorf("cached hostname = %q, want %q", got, wantHostname)
	}
}

func TestCacheMiss_BeforeResolution(t *testing.T) {
	r := newTestResolver(1, func(_ context.Context, _ net.IP) (string, error) {
		time.Sleep(50 * time.Millisecond) // hold the worker
		return "host.example.com", nil
	})

	results := make(chan Result, 1)
	cancel := startResolver(t, r, results)
	defer cancel()

	ip := docIP(20)
	r.Submit(ip)

	// Lookup immediately — worker hasn't resolved yet
	_, ok := r.Lookup(ip)
	if ok {
		t.Error("Lookup before resolution: expected cache miss, got hit")
	}
}

func TestNegativeCache_FailedLookupCached(t *testing.T) {
	// NXDOMAIN-like: lookup returns error. The empty string should be cached
	// so subsequent Submits are no-ops.
	var callCount int32

	r := newTestResolver(1, func(_ context.Context, _ net.IP) (string, error) {
		atomic.AddInt32(&callCount, 1)
		return "", errors.New("no such host")
	})

	results := make(chan Result, 4)
	cancel := startResolver(t, r, results)
	defer cancel()

	ip := docIP(30)
	r.Submit(ip)

	// Wait for the worker to process and cache the failure
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := r.cache.Load(ip.String()); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	cached, cacheHit := r.cache.Load(ip.String())
	if !cacheHit {
		t.Fatal("negative result should be cached after failed lookup")
	}
	if cached.(string) != "" {
		t.Errorf("cached value for failed lookup should be empty string, got %q", cached)
	}

	// Second Submit should be rejected (already cached)
	if r.Submit(ip) {
		t.Error("Submit after negative cache: should return false (already cached)")
	}

	// Lookup count should be exactly 1
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("lookupFunc called %d times, want 1 (negative cache not working)", got)
	}

	// No result should have been sent (empty hostname is not forwarded)
	select {
	case res := <-results:
		t.Errorf("unexpected result for failed lookup: %+v", res)
	default:
	}
}

func TestCacheHit_SecondSubmitIsNoop(t *testing.T) {
	var callCount int32

	r := newTestResolver(1, func(_ context.Context, _ net.IP) (string, error) {
		atomic.AddInt32(&callCount, 1)
		return "host.example.com", nil
	})

	results := make(chan Result, 2)
	cancel := startResolver(t, r, results)
	defer cancel()

	ip := docIP(40)
	r.Submit(ip)
	waitResult(t, results, 2*time.Second)

	// Now cached: second Submit should return false and not trigger lookup
	if r.Submit(ip) {
		t.Error("second Submit after cache hit should return false")
	}

	// Give worker time to potentially process a second lookup (it shouldn't)
	time.Sleep(50 * time.Millisecond)

	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("lookupFunc called %d times, want exactly 1 (cache hit not preventing re-query)", got)
	}
}

// ---------------------------------------------------------------------------
// Trailing-dot stripping
// ---------------------------------------------------------------------------

func TestTrailingDotStripped(t *testing.T) {
	ip := docIP(50)

	r := newTestResolver(1, func(_ context.Context, _ net.IP) (string, error) {
		return "host.example.com.", nil // trailing dot as DNS returns
	})

	results := make(chan Result, 1)
	cancel := startResolver(t, r, results)
	defer cancel()

	r.Submit(ip)
	res := waitResult(t, results, 2*time.Second)

	if res.Hostname != "host.example.com" {
		t.Errorf("hostname = %q, want %q (trailing dot not stripped)", res.Hostname, "host.example.com")
	}
}

// ---------------------------------------------------------------------------
// Concurrent submits for same IP — deduplication
// ---------------------------------------------------------------------------

func TestConcurrentSubmits_SameIP_OneLookup(t *testing.T) {
	var callCount int32
	ip := docIP(60)

	r := newTestResolver(4, func(_ context.Context, _ net.IP) (string, error) {
		atomic.AddInt32(&callCount, 1)
		time.Sleep(20 * time.Millisecond) // hold worker to let concurrent submits pile up
		return "host.example.com", nil
	})

	results := make(chan Result, 8)
	cancel := startResolver(t, r, results)
	defer cancel()

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			r.Submit(ip)
		}()
	}
	wg.Wait()

	// Wait for resolution
	waitResult(t, results, 2*time.Second)

	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("lookupFunc called %d times, want exactly 1 (dedup failed)", got)
	}
}

// ---------------------------------------------------------------------------
// Concurrent submits for different IPs
// ---------------------------------------------------------------------------

func TestConcurrentSubmits_DifferentIPs_AllResolved(t *testing.T) {
	const numIPs = 50

	r := newTestResolver(8, func(_ context.Context, ip net.IP) (string, error) {
		return "host-" + ip.String() + ".example.com", nil
	})

	results := make(chan Result, numIPs)
	cancel := startResolver(t, r, results)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(numIPs)
	for i := 0; i < numIPs; i++ {
		i := i
		go func() {
			defer wg.Done()
			r.Submit(net.ParseIP(fmt.Sprintf("198.51.100.%d", i)))
		}()
	}
	wg.Wait()

	// Collect all results within deadline
	received := make(map[string]bool)
	deadline := time.After(3 * time.Second)
	for len(received) < numIPs {
		select {
		case res := <-results:
			received[res.IP.String()] = true
		case <-deadline:
			t.Errorf("only received %d/%d results before deadline", len(received), numIPs)
			return
		}
	}

	if len(received) != numIPs {
		t.Errorf("received %d results, want %d", len(received), numIPs)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation (Stop equivalent)
// ---------------------------------------------------------------------------

func TestContextCancel_RunReturns(t *testing.T) {
	r := newTestResolver(2, func(_ context.Context, _ net.IP) (string, error) {
		time.Sleep(100 * time.Millisecond)
		return "host.example.com", nil
	})

	results := make(chan Result, 1)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		r.Run(ctx, results)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Run returned cleanly after cancel
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestContextCancel_SubmitDoesNotBlock(t *testing.T) {
	// After context is cancelled, Submit should not block or panic.
	// The channel is still open, so Submit into a non-full channel succeeds;
	// what matters is it never hangs indefinitely.
	r := newTestResolver(1, func(_ context.Context, _ net.IP) (string, error) {
		return "host.example.com", nil
	})

	results := make(chan Result, 4)
	ctx, cancel := context.WithCancel(context.Background())
	go r.Run(ctx, results)
	cancel()

	// Give Run a moment to stop workers
	time.Sleep(10 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		r.Submit(docIP(70))
		close(done)
	}()

	select {
	case <-done:
		// Submit returned without blocking
	case <-time.After(1 * time.Second):
		t.Fatal("Submit blocked after context cancellation")
	}
}

func TestContextCancel_LookupStillWorks(t *testing.T) {
	// After cancellation, the cache should still be readable.
	ip := docIP(80)

	r := newTestResolver(1, func(_ context.Context, _ net.IP) (string, error) {
		return "cached.example.com", nil
	})

	results := make(chan Result, 1)
	cancel := startResolver(t, r, results)

	r.Submit(ip)
	waitResult(t, results, 2*time.Second)

	cancel() // stop workers

	// Lookup on cached entry must still work
	got, ok := r.Lookup(ip)
	if !ok {
		t.Fatal("Lookup after cancel: expected cache hit")
	}
	if got != "cached.example.com" {
		t.Errorf("Lookup after cancel: got %q, want %q", got, "cached.example.com")
	}
}

func TestDoubleCancel_Idempotent(t *testing.T) {
	// context.CancelFunc is safe to call multiple times; ensure Run does not panic.
	r := newTestResolver(2, func(_ context.Context, _ net.IP) (string, error) {
		return "host.example.com", nil
	})

	results := make(chan Result, 1)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		r.Run(ctx, results)
		close(done)
	}()

	cancel()
	cancel() // second cancel must not panic

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after double cancel")
	}
}

// ---------------------------------------------------------------------------
// Worker pool correctness
// ---------------------------------------------------------------------------

func TestWorkerPool_RightNumberSpawned(t *testing.T) {
	// Verify Run spawns exactly `workers` goroutines by counting how many
	// concurrent lookups occur simultaneously.
	const workers = 3
	const ips = 10

	var concurrent int32
	var peak int32

	r := newTestResolver(workers, func(_ context.Context, _ net.IP) (string, error) {
		cur := atomic.AddInt32(&concurrent, 1)
		// Track peak concurrency
		for {
			p := atomic.LoadInt32(&peak)
			if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond) // hold slot
		atomic.AddInt32(&concurrent, -1)
		return "host.example.com", nil
	})

	results := make(chan Result, ips)
	cancel := startResolver(t, r, results)
	defer cancel()

	for i := 0; i < ips; i++ {
		r.Submit(docIP(100 + i))
	}

	// Wait for all results
	for i := 0; i < ips; i++ {
		waitResult(t, results, 5*time.Second)
	}

	if p := atomic.LoadInt32(&peak); p > int32(workers) {
		t.Errorf("peak concurrent lookups = %d, exceeds worker count %d", p, workers)
	}
}

func TestWorkerPool_StopsOnContextCancel(t *testing.T) {
	// Verify all workers stop and Run returns when context is cancelled,
	// even when there are pending items in the queue.
	blocked := make(chan struct{}) // closed to unblock workers if needed

	r := newTestResolver(4, func(ctx context.Context, _ net.IP) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-blocked:
			return "host.example.com", nil
		}
	})

	results := make(chan Result, 4)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		r.Run(ctx, results)
		close(done)
	}()

	// Submit a few IPs to keep workers busy
	for i := 0; i < 4; i++ {
		r.Submit(docIP(200 + i))
	}

	// Cancel while workers are blocked in lookup
	cancel()

	select {
	case <-done:
		// All workers stopped
	case <-time.After(3 * time.Second):
		close(blocked) // unblock to avoid goroutine leak in test
		t.Fatal("Run did not return after context cancellation with blocked workers")
	}
}

// ---------------------------------------------------------------------------
// Submit channel full — drop behavior
// ---------------------------------------------------------------------------

func TestSubmit_ChannelFull_DoesNotBlock(t *testing.T) {
	// Fill the channel, then verify Submit returns false without blocking.
	r := newTestResolver(0, func(_ context.Context, _ net.IP) (string, error) {
		return "host.example.com", nil
	})
	// No workers running — channel will fill up

	// Fill the channel (capacity 256)
	filled := 0
	for i := 0; i < 256; i++ {
		ip := net.ParseIP(fmt.Sprintf("198.51.100.%d", i%256))
		// Use unique keys by manipulating cache to avoid dedup interfering
		key := fmt.Sprintf("10.%d.%d.1", i/256, i%256)
		ip = net.ParseIP(key)
		if ip == nil {
			continue
		}
		if r.Submit(ip) {
			filled++
		}
	}

	// Now the channel should be full; next submit must not block
	done := make(chan bool)
	go func() {
		// This IP hasn't been submitted yet; channel is full → should drop
		newIP := net.ParseIP("10.99.99.99")
		result := r.Submit(newIP)
		done <- result
	}()

	select {
	case dropped := <-done:
		// dropped == false means it was dropped (channel full)
		// dropped == true means it was queued (channel had space; that's fine too)
		_ = dropped
	case <-time.After(1 * time.Second):
		t.Fatal("Submit blocked when channel was full")
	}
}

// TestSubmit_ChannelFull_ReturnsFalse verifies that when the request channel is full,
// Submit returns false (not true). This kills the surviving mutant: "return false → return true"
// in the channel-full branch of Submit().
func TestSubmit_ChannelFull_ReturnsFalse(t *testing.T) {
	// Use a tiny channel capacity to make it easy to fill.
	r := &Resolver{
		workers:    0,
		reqCh:      make(chan net.IP, 1), // capacity 1 — fills after a single submit
		lookupFunc: func(_ context.Context, _ net.IP) (string, error) { return "h", nil },
	}

	// First submit fills the channel.
	ip1 := net.ParseIP("192.0.2.1")
	if !r.Submit(ip1) {
		t.Fatal("first submit on empty channel should return true")
	}

	// Second submit (different IP, channel full) must return false.
	ip2 := net.ParseIP("192.0.2.2")
	got := r.Submit(ip2)
	if got {
		t.Fatal("Submit with full channel should return false; got true (mutant survived)")
	}

	// The dropped IP must not remain in pending after the failed submit.
	if _, pending := r.pending.Load(ip2.String()); pending {
		t.Fatal("dropped IP should have been removed from pending map")
	}
}

// ---------------------------------------------------------------------------
// Result channel — full results channel does not deadlock
// ---------------------------------------------------------------------------

func TestResultChannel_Full_NoDeadlock(t *testing.T) {
	// results channel is unbuffered; if no one reads, worker should respect
	// ctx.Done() and not deadlock.
	ip := docIP(90)

	r := newTestResolver(1, func(_ context.Context, _ net.IP) (string, error) {
		return "host.example.com", nil
	})

	results := make(chan Result) // intentionally unbuffered
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.Run(ctx, results)
		close(done)
	}()

	r.Submit(ip)

	// Do NOT read from results — worker must exit via ctx.Done()
	select {
	case <-done:
		// Run returned cleanly after timeout
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: Run did not return when results channel was full and context cancelled")
	}
}

// ---------------------------------------------------------------------------
// Race detector: concurrent Submit + Lookup
// ---------------------------------------------------------------------------

func TestRace_ConcurrentSubmitAndLookup(t *testing.T) {
	r := newTestResolver(4, func(_ context.Context, ip net.IP) (string, error) {
		return "host-" + ip.String() + ".example.com", nil
	})

	results := make(chan Result, 64)
	cancel := startResolver(t, r, results)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			r.Submit(docIP(i))
		}()
		go func() {
			defer wg.Done()
			r.Lookup(docIP(i))
		}()
	}
	wg.Wait()
}
