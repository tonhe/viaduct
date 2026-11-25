package probe

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// Send failures and the fallback echo ID (viaduct#86).
//
//   - A probe whose send fails (SetTTL or WriteTo) leaves no record in the
//     probe map. The final round waits while records are outstanding, so a
//     record for a probe that never left makes the run wait a full timeout.
//   - The fallback EchoID is chosen in NewTracer, before any goroutine starts.

// ttlErrSender is a probeSender whose SetTTL always fails.
type ttlErrSender struct{}

func (ttlErrSender) SetTTL(int) error                        { return errors.New("set ttl error injected") }
func (ttlErrSender) WriteTo(b []byte, _ net.Addr) (int, error) { return len(b), nil }

func TestRun_FailedSendLeavesNoProbeRecord(t *testing.T) {
	target := net.ParseIP("192.0.2.1")
	source := net.ParseIP("198.51.100.7")

	protocols := []struct {
		name  string
		proto func(Config) ProbeProtocol
	}{
		{"icmp", func(c Config) ProbeProtocol { return NewICMPProtocol(c) }},
		{"udp", func(Config) ProbeProtocol { return NewUDPProtocol(33434) }},
		{"tcp", func(Config) ProbeProtocol { return NewTCPProtocol(443) }},
	}
	senders := []struct {
		name string
		snd  probeSender
	}{
		{"WriteTo fails", &errSender{}},
		{"SetTTL fails", ttlErrSender{}},
	}

	for _, p := range protocols {
		for _, s := range senders {
			t.Run(p.name+"/"+s.name, func(t *testing.T) {
				cfg := DefaultConfig()
				cfg.IPVersion = 4
				cfg.SourceIP = source
				cfg.TargetIP = target
				cfg.Protocol = p.proto(cfg)
				cfg.MaxHops = 3
				cfg.NumPaths = 2
				cfg.ProbeDelay = 0
				cfg.RoundDelay = 5 * time.Millisecond
				cfg.MaxRounds = 1
				// Long enough that a final round waiting for it is unmistakable,
				// and that the sweep (twice the timeout) cannot hide a record.
				cfg.Timeout = 3 * time.Second

				tr := NewTracer(target, cfg)
				snd := s.snd
				tr.newSender = func(context.Context, ProbeProtocol, chan<- Result) (probeSender, func(), error) {
					return snd, nil, nil
				}
				var rm sentCounter
				tr.SetOnRoundSent(&rm)

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				start := time.Now()
				if err := tr.run(ctx, nil, make(chan Result, 64)); err != nil {
					t.Fatalf("run: %v", err)
				}
				elapsed := time.Since(start)

				tr.pm.mu.Lock()
				left := len(tr.pm.items)
				tr.pm.mu.Unlock()
				if left != 0 {
					t.Errorf("probe map holds %d records for probes that were never sent, want 0", left)
				}
				if elapsed > time.Second {
					t.Errorf("run took %v: the final round waited for probes that were never sent (timeout %v)", elapsed, cfg.Timeout)
				}
				if rm.probes != 0 {
					t.Errorf("round marker counted %d probes sent, want 0", rm.probes)
				}
			})
		}
	}
}

// sentCounter totals the probes reported to the round marker.
type sentCounter struct{ probes int }

func (c *sentCounter) MarkRoundSent(_ int, probes int) { c.probes += probes }

// A successful send still leaves its record: the removal is for failures only.
func TestRun_SuccessfulSendKeepsProbeRecord(t *testing.T) {
	target := net.ParseIP("192.0.2.1")
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.TargetIP = target
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.MaxHops = 2
	cfg.NumPaths = 2
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 1
	cfg.Timeout = 50 * time.Millisecond

	// 2 TTLs x (1 ICMP TTL probe + 2 UDP flows) = 6 probes, all written.
	snd := &fakeSender{}
	tr := newRunTracer(snd, cfg, target)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.run(ctx, nil, make(chan Result, 64)); err != nil {
		t.Fatalf("run: %v", err)
	}

	snd.mu.Lock()
	written := len(snd.written)
	snd.mu.Unlock()
	tr.pm.mu.Lock()
	left := len(tr.pm.items)
	tr.pm.mu.Unlock()
	if written != 6 {
		t.Fatalf("sender recorded %d writes, want 6", written)
	}
	if left != written {
		t.Errorf("probe map holds %d records, want %d: one per probe that was written", left, written)
	}
}

func TestNewTracer_ChoosesFallbackEchoID(t *testing.T) {
	target := net.ParseIP("192.0.2.1")

	cfg := DefaultConfig()
	cfg.EchoID = 0
	for i := 0; i < 50; i++ {
		tr := NewTracer(target, cfg)
		if id := tr.cfg.EchoID; id < 1 || id > 65534 {
			t.Fatalf("EchoID = %d right after NewTracer, want 1..65534", id)
		}
	}

	cfg.EchoID = 0x4321
	if got := NewTracer(target, cfg).cfg.EchoID; got != 0x4321 {
		t.Errorf("EchoID = %#x, want the configured 0x4321", got)
	}
}

// Run starts the listener and then the send loop. With EchoID 0 the send loop
// used to write the fallback into t.cfg while the listener was already reading
// it. This test runs the two the way Run does, under -race in `make test-all`,
// and requires that the send loop leaves the ID NewTracer chose untouched.
func TestRun_EchoIDZero_NoWriteAfterListenerStarts(t *testing.T) {
	target := net.ParseIP("192.0.2.1")
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.TargetIP = target
	cfg.EchoID = 0
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.MaxHops = 1
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 20 * time.Millisecond
	cfg.MaxRounds = 1
	cfg.Timeout = 50 * time.Millisecond

	tr := newRunTracer(&fakeSender{}, cfg, target)
	want := tr.cfg.EchoID

	// A Time Exceeded for a probe the send loop will not reuse the key of: the
	// listener reads t.cfg to identify it.
	id := tr.cfg.Protocol.(*ICMPProtocol).sessionID
	const seq = 999
	tr.pm.addTo(probeKey{SrcPort: id, DstPort: seq}, 1, 0, time.Now(), ProbeICMPTTL, target)
	conn := newFakeConn()
	conn.push(marshalTimeExceeded(buildICMPInnerHeader(net.ParseIP("198.51.100.7"), target, id, seq)), fakeAddr{"203.0.113.1"})

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan Result, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tr.listen(ctx, conn, results)
	}()

	if err := tr.run(ctx, nil, results); err != nil {
		t.Fatalf("run: %v", err)
	}
	select {
	case <-results:
	case <-time.After(2 * time.Second):
		t.Error("the listener produced no result for the queued Time Exceeded")
	}
	cancel()
	<-done

	if got := tr.cfg.EchoID; got == 0 || got != want {
		t.Errorf("EchoID = %d after the run, want the %d chosen by NewTracer", got, want)
	}
}
