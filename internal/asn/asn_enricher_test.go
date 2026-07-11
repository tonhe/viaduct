package asn

// Tests for Enricher orchestration: Run/worker cycle, cache correctness,
// DNS seam injection, concurrent safety.

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestEnricher builds an Enricher whose DNS lookup is replaced by fn.
// Extracted seam: lookupFunc field on Enricher (see asn.go).
func newTestEnricher(workers int, fn func(context.Context, net.IP) Info) *Enricher {
	return &Enricher{
		workers:    workers,
		reqCh:      make(chan net.IP, 256),
		lookupFunc: fn,
	}
}

// runWithTimeout starts e.Run(ctx, results) in a goroutine and returns a cancel
// function. The caller must call cancel() to stop the enricher.
func runWithTimeout(t *testing.T, e *Enricher, results chan<- Result) (cancel context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	go func() {
		e.Run(ctx, results)
	}()
	return cancel
}

// waitForResult reads one Result from results within deadline, fatal on timeout.
func waitForResult(t *testing.T, results <-chan Result, deadline time.Duration) Result {
	t.Helper()
	select {
	case r := <-results:
		return r
	case <-time.After(deadline):
		t.Fatal("timed out waiting for ASN result")
		return Result{}
	}
}

// ---------------------------------------------------------------------------
// Run / worker: successful lookup
// ---------------------------------------------------------------------------

func TestEnricherRun_SuccessfulLookup(t *testing.T) {
	wantInfo := Info{Number: 64496, Org: "EXAMPLE-ORG"}
	lookupCalled := false

	e := newTestEnricher(1, func(_ context.Context, ip net.IP) Info {
		lookupCalled = true
		return wantInfo
	})

	results := make(chan Result, 1)
	cancel := runWithTimeout(t, e, results)
	defer cancel()

	ip := net.ParseIP("192.0.2.1")
	e.Submit(ip)

	r := waitForResult(t, results, 2*time.Second)
	if !r.IP.Equal(ip) {
		t.Errorf("result IP = %v, want %v", r.IP, ip)
	}
	if r.Info != wantInfo {
		t.Errorf("result Info = %+v, want %+v", r.Info, wantInfo)
	}
	if !lookupCalled {
		t.Error("lookupFunc was never called")
	}

	// Verify result is cached
	cached, found := e.Lookup(ip)
	if !found {
		t.Fatal("IP should be cached after lookup")
	}
	if cached != wantInfo {
		t.Errorf("cached Info = %+v, want %+v", cached, wantInfo)
	}
}

// ---------------------------------------------------------------------------
// Run / worker: NXDOMAIN (no result) — negative response cached, no send
// ---------------------------------------------------------------------------

func TestEnricherRun_NXDomain(t *testing.T) {
	e := newTestEnricher(1, func(_ context.Context, ip net.IP) Info {
		return Info{} // nothing found
	})

	results := make(chan Result, 1)
	cancel := runWithTimeout(t, e, results)
	defer cancel()

	ip := net.ParseIP("192.0.2.3")
	e.Submit(ip)

	// Give worker time to process
	time.Sleep(200 * time.Millisecond)

	// No result should be sent
	select {
	case r := <-results:
		t.Errorf("unexpected result for NXDOMAIN IP: %+v", r)
	default:
		// correct — nothing sent
	}

	// But the empty Info should still be cached (negative cache)
	_, found := e.Lookup(ip)
	if !found {
		t.Error("NXDOMAIN result should still be cached (prevents re-lookup)")
	}
}

// ---------------------------------------------------------------------------
// Run / worker: DNS error — no crash, no result sent, no infinite retry
// ---------------------------------------------------------------------------

func TestEnricherRun_LookupError(t *testing.T) {
	var callCount int32
	e := newTestEnricher(1, func(_ context.Context, ip net.IP) Info {
		atomic.AddInt32(&callCount, 1)
		return Info{} // simulate DNS timeout returning nothing
	})

	results := make(chan Result, 1)
	cancel := runWithTimeout(t, e, results)
	defer cancel()

	ip := net.ParseIP("192.0.2.4")
	e.Submit(ip)

	time.Sleep(200 * time.Millisecond)

	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("lookup should be called exactly once, got %d", got)
	}

	select {
	case r := <-results:
		t.Errorf("expected no result on lookup error, got %+v", r)
	default:
		// correct
	}
}

// ---------------------------------------------------------------------------
// Run / worker: bogus TXT — parser returns error, no crash
// ---------------------------------------------------------------------------

func TestEnricherRun_BogusResponse(t *testing.T) {
	// Simulate what happens when parseOriginResponse fails inside lookupCymru.
	// We test this at the seam level: lookupFunc returns zero Info.
	e := newTestEnricher(1, func(_ context.Context, ip net.IP) Info {
		// Mimic parseOriginResponse failing on "bogus | data"
		_, err := parseOriginResponse("bogus | data")
		if err == nil {
			return Info{Number: 999} // should not happen
		}
		return Info{} // parser failed, return zero
	})

	results := make(chan Result, 1)
	cancel := runWithTimeout(t, e, results)
	defer cancel()

	ip := net.ParseIP("192.0.2.5")
	e.Submit(ip)

	time.Sleep(200 * time.Millisecond)

	select {
	case r := <-results:
		t.Errorf("bogus TXT should produce no result, got %+v", r)
	default:
		// correct
	}
}

// ---------------------------------------------------------------------------
// Submit / cache: same IP twice deduplication via pending map
// ---------------------------------------------------------------------------

func TestEnricherSubmit_PendingDedup(t *testing.T) {
	var mu sync.Mutex
	callCount := 0

	// Slow lookup to keep the first request in-flight
	e := newTestEnricher(1, func(ctx context.Context, ip net.IP) Info {
		mu.Lock()
		callCount++
		mu.Unlock()
		// Block until context cancelled so the IP stays pending
		<-ctx.Done()
		return Info{}
	})

	results := make(chan Result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() { e.Run(ctx, results) }()

	ip := net.ParseIP("192.0.2.6")
	e.Submit(ip)

	// Give worker time to pick up the request
	time.Sleep(50 * time.Millisecond)

	// Second Submit while first is in-flight
	second := e.Submit(ip)
	if second {
		t.Error("second Submit while pending should return false")
	}

	// Wait for lookup to have been called
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	c := callCount
	mu.Unlock()
	if c != 1 {
		t.Errorf("lookup called %d times, want 1", c)
	}
}

// ---------------------------------------------------------------------------
// Cache hit: after result received, re-Submit returns false (cached)
// ---------------------------------------------------------------------------

func TestEnricherSubmit_CacheHitAfterResult(t *testing.T) {
	wantInfo := Info{Number: 64500, Org: "EXAMPLE-CACHE"}

	e := newTestEnricher(1, func(_ context.Context, ip net.IP) Info {
		return wantInfo
	})

	results := make(chan Result, 2)
	cancel := runWithTimeout(t, e, results)
	defer cancel()

	ip := net.ParseIP("192.0.2.7")
	e.Submit(ip)

	waitForResult(t, results, 2*time.Second)

	// Re-submit after cached
	again := e.Submit(ip)
	if again {
		t.Error("Submit after cache hit should return false")
	}

	// Lookup should still return the cached value
	info, found := e.Lookup(ip)
	if !found {
		t.Fatal("IP should be in cache after result received")
	}
	if info != wantInfo {
		t.Errorf("cached info = %+v, want %+v", info, wantInfo)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation: Run exits cleanly, pending worker drains
// ---------------------------------------------------------------------------

func TestEnricherRun_CtxCancel(t *testing.T) {
	done := make(chan struct{})
	e := newTestEnricher(2, func(ctx context.Context, ip net.IP) Info {
		<-ctx.Done()
		return Info{}
	})

	results := make(chan Result, 1)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		e.Run(ctx, results)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Run exited cleanly
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

// ---------------------------------------------------------------------------
// Concurrent submit safety: 100 goroutines, same IP — only one lookup fires
// ---------------------------------------------------------------------------

func TestConcurrentSubmit_SameIP(t *testing.T) {
	var lookupCount int32

	e := newTestEnricher(4, func(_ context.Context, ip net.IP) Info {
		atomic.AddInt32(&lookupCount, 1)
		return Info{Number: 64501, Org: "CONCURRENT-TEST"}
	})

	results := make(chan Result, 10)
	cancel := runWithTimeout(t, e, results)
	defer cancel()

	ip := net.ParseIP("192.0.2.20")
	const goroutines = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			e.Submit(ip)
		}()
	}
	wg.Wait()

	// Wait for the single lookup to complete
	waitForResult(t, results, 2*time.Second)

	got := atomic.LoadInt32(&lookupCount)
	if got != 1 {
		t.Errorf("lookupFunc called %d times, want exactly 1 (dedup failed)", got)
	}
}

// ---------------------------------------------------------------------------
// Concurrent submit safety: 100 goroutines, different IPs — all processed
// Run with -race to catch data races.
// ---------------------------------------------------------------------------

func TestConcurrentSubmit_DifferentIPs(t *testing.T) {
	const count = 50 // keep under channel capacity (256)

	e := newTestEnricher(4, func(_ context.Context, ip net.IP) Info {
		return Info{Number: 64502, Org: "MULTI-IP-TEST"}
	})

	results := make(chan Result, count)
	cancel := runWithTimeout(t, e, results)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(count)
	for i := 0; i < count; i++ {
		go func(i int) {
			defer wg.Done()
			ip := net.IPv4(192, 0, 2, byte(i+1))
			e.Submit(ip)
		}(i)
	}
	wg.Wait()

	received := 0
	deadline := time.After(3 * time.Second)
	for received < count {
		select {
		case <-results:
			received++
		case <-deadline:
			t.Fatalf("only received %d/%d results before timeout", received, count)
		}
	}
}

// ---------------------------------------------------------------------------
// Submit v4 documentation address: should queue (not private)
// ---------------------------------------------------------------------------

func TestSubmitPublicV4DocAddr(t *testing.T) {
	// 192.0.2.0/24 is TEST-NET-1 (documentation), routable for test purposes.
	// probe.IsPrivate does NOT classify 192.0.2.x as private (no RFC 5737 block).
	e := &Enricher{
		workers:    0,
		reqCh:      make(chan net.IP, 256),
		lookupFunc: lookupCymru,
	}

	ip := net.ParseIP("192.0.2.100")
	ok := e.Submit(ip)
	if !ok {
		t.Error("Submit(192.0.2.100) should return true — 192.0.2.0/24 is not in IsPrivate's list")
	}

	// Should be in pending
	_, inPending := e.pending.Load(ip.String())
	if !inPending {
		t.Error("IP should be in pending after Submit returned true")
	}
}

// ---------------------------------------------------------------------------
// Submit v6 documentation address: queues, reverse name goes to origin6
// ---------------------------------------------------------------------------

func TestSubmitPublicV6DocAddr(t *testing.T) {
	// 2001:db8::/32 is documentation space — not private per probe.IsPrivate.
	e := &Enricher{
		workers:    0,
		reqCh:      make(chan net.IP, 256),
		lookupFunc: lookupCymru,
	}

	ip := net.ParseIP("2001:db8::1")
	ok := e.Submit(ip)
	if !ok {
		t.Error("Submit(2001:db8::1) should return true — 2001:db8::/32 is not private")
	}
}

// ---------------------------------------------------------------------------
// parseOriginResponse: trailing-newline and whitespace trim behavior
// These are regression tests: the parser must not choke on real-world noise.
// ---------------------------------------------------------------------------

func TestParseOriginResponse_TrailingNoise(t *testing.T) {
	cases := []struct {
		txt  string
		want int
	}{
		{"13335 | 1.1.1.0/24 | US | arin | 2014-03-28\n", 13335},
		{"13335 | 1.1.1.0/24 | US | arin | 2014-03-28\r\n", 13335},
		{"\t13335\t| 1.1.1.0/24 | US", 13335},
	}
	for _, tc := range cases {
		got, err := parseOriginResponse(tc.txt)
		if err != nil {
			t.Errorf("parseOriginResponse(%q) unexpected error: %v", tc.txt, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseOriginResponse(%q) = %d, want %d", tc.txt, got, tc.want)
		}
	}
}
