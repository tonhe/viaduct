package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

func ttlSample(ms int) hop.SparkSample {
	return hop.SparkSample{RTT: time.Duration(ms) * time.Millisecond, Kind: hop.SparkTTL}
}

func TestRenderSparklineBasic(t *testing.T) {
	data := []hop.SparkSample{ttlSample(10), ttlSample(20), ttlSample(15)}
	result := renderSparkline(data, 14)
	if !strings.Contains(result, "█") {
		t.Fatalf("expected block chars in output, got %q", result)
	}
}

func TestRenderSparklineEmpty(t *testing.T) {
	result := renderSparkline(nil, 14)
	trimmed := strings.TrimSpace(result)
	if trimmed != "" {
		t.Fatalf("expected all spaces for empty data, got %q", result)
	}
}

func TestRenderSparklineAllSame(t *testing.T) {
	data := make([]hop.SparkSample, 14)
	for i := range data {
		data[i] = ttlSample(10)
	}
	result := renderSparkline(data, 14)
	if !strings.Contains(result, "█") {
		t.Fatalf("expected block chars for uniform data, got %q", result)
	}
}

func TestRenderSparklineLoss(t *testing.T) {
	data := []hop.SparkSample{ttlSample(10), {Kind: hop.SparkDrop}, ttlSample(12)}
	result := renderSparkline(data, 14)
	if !strings.Contains(result, "·") {
		t.Fatalf("expected · for drop, got %q", result)
	}
}

func TestRenderSparklineGradient(t *testing.T) {
	data := []hop.SparkSample{ttlSample(1), ttlSample(100)}
	result := renderSparkline(data, 14)
	blockCount := strings.Count(result, "█")
	if blockCount != 2 {
		t.Fatalf("expected 2 block chars, got %d in %q", blockCount, result)
	}
}

// TestRenderSparklineEcho: echo-derived cells render as ▓ (distinct from the
// █ TTL block), so a rate-limited hop carried by echo is visible as texture.
func TestRenderSparklineEcho(t *testing.T) {
	data := []hop.SparkSample{
		ttlSample(10),
		{RTT: 11 * time.Millisecond, Kind: hop.SparkEcho},
	}
	result := renderSparkline(data, 14)
	if !strings.Contains(result, "▓") {
		t.Fatalf("expected ▓ for echo cell, got %q", result)
	}
	if strings.Count(result, "█") != 1 {
		t.Fatalf("expected exactly 1 █ (the TTL cell), got %q", result)
	}
}

// TestRenderSparklineHeld: a Held cell (rate-limited, reachable, unsampled this
// round) renders as ░ — not a drop dot, so it doesn't read as packet loss.
func TestRenderSparklineHeld(t *testing.T) {
	data := []hop.SparkSample{ttlSample(10), {Kind: hop.SparkHeld}, ttlSample(12)}
	result := renderSparkline(data, 14)
	if !strings.Contains(result, "░") {
		t.Fatalf("expected ░ for held cell, got %q", result)
	}
	if strings.Contains(result, "·") {
		t.Fatalf("held cell must not render as a drop dot, got %q", result)
	}
}
