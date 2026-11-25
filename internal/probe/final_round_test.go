package probe

import (
	"context"
	"testing"
	"time"
)

// The final round must not end while its probes can still be answered. With
// an interval shorter than the round-trip time (200 ms interval, 300 ms
// replies) the run used to stop before the last round's replies arrived, and
// they were counted as lost. The echo tier's per-round delay hid this until
// the echo flood at the destination was removed.

func TestProbeMapPending_CountsUnansweredProbesYoungerThanTheTimeout(t *testing.T) {
	pm := newProbeMap()
	now := time.Now()
	fresh := probeKey{SrcPort: 1, DstPort: 1}
	answered := probeKey{SrcPort: 1, DstPort: 2}
	expired := probeKey{SrcPort: 1, DstPort: 3}
	pm.add(fresh, 3, 0, now, ProbeICMPTTL)
	pm.add(answered, 3, 0, now, ProbeICMPTTL)
	pm.add(expired, 3, 0, now.Add(-2*time.Second), ProbeICMPTTL)
	pm.match(answered)

	if got := pm.pending(time.Second); got != 1 {
		t.Errorf("pending = %d, want 1: one probe is unanswered and still within the timeout", got)
	}
	pm.match(fresh)
	if got := pm.pending(time.Second); got != 0 {
		t.Errorf("pending = %d, want 0 once it is answered", got)
	}
}

func TestAwaitFinalReplies_ReturnsWhenTheLastProbeIsAnswered(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Timeout = 2 * time.Second
	tr := NewTracer(nil, cfg)
	key := probeKey{SrcPort: 1, DstPort: 1}
	tr.pm.add(key, 3, 0, time.Now(), ProbeICMPTTL)

	go func() {
		time.Sleep(60 * time.Millisecond)
		tr.pm.match(key)
	}()
	start := time.Now()
	tr.awaitFinalReplies(context.Background())
	if waited := time.Since(start); waited < 50*time.Millisecond || waited > time.Second {
		t.Errorf("waited %v, want about 60ms: until the reply, not the full timeout and not zero", waited)
	}
}

func TestAwaitFinalReplies_GivesUpAtTheTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Timeout = 80 * time.Millisecond
	tr := NewTracer(nil, cfg)
	tr.pm.add(probeKey{SrcPort: 1, DstPort: 1}, 3, 0, time.Now(), ProbeICMPTTL)

	start := time.Now()
	tr.awaitFinalReplies(context.Background())
	if waited := time.Since(start); waited < 70*time.Millisecond || waited > 500*time.Millisecond {
		t.Errorf("waited %v, want about 80ms (the timeout) for a probe that is never answered", waited)
	}
}

func TestAwaitFinalReplies_StopsOnCancel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Timeout = 5 * time.Second
	tr := NewTracer(nil, cfg)
	tr.pm.add(probeKey{SrcPort: 1, DstPort: 1}, 3, 0, time.Now(), ProbeICMPTTL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	tr.awaitFinalReplies(ctx)
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Errorf("waited %v after cancel, want to return at once", waited)
	}
}
