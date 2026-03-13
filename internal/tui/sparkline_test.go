package tui

import (
	"strings"
	"testing"
	"time"
)

func TestRenderSparklineBasic(t *testing.T) {
	data := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		15 * time.Millisecond,
	}
	result := renderSparkline(data, 14)
	// Should contain block characters
	if !strings.Contains(result, "█") {
		t.Fatalf("expected block chars in output, got %q", result)
	}
}

func TestRenderSparklineEmpty(t *testing.T) {
	result := renderSparkline(nil, 14)
	// Should be all spaces
	trimmed := strings.TrimSpace(result)
	if trimmed != "" {
		t.Fatalf("expected all spaces for empty data, got %q", result)
	}
}

func TestRenderSparklineAllSame(t *testing.T) {
	data := make([]time.Duration, 14)
	for i := range data {
		data[i] = 10 * time.Millisecond
	}
	result := renderSparkline(data, 14)
	// All same values should produce blocks (no spaces except ANSI codes)
	if !strings.Contains(result, "█") {
		t.Fatalf("expected block chars for uniform data, got %q", result)
	}
}

func TestRenderSparklineLoss(t *testing.T) {
	data := []time.Duration{10 * time.Millisecond, 0, 12 * time.Millisecond}
	result := renderSparkline(data, 14)
	// Should contain the loss marker (·)
	if !strings.Contains(result, "·") {
		t.Fatalf("expected · for loss, got %q", result)
	}
}

func TestRenderSparklineGradient(t *testing.T) {
	// Low and high values should produce different colors
	data := []time.Duration{
		1 * time.Millisecond,
		100 * time.Millisecond,
	}
	result := renderSparkline(data, 14)
	// Both should render as blocks
	blockCount := strings.Count(result, "█")
	if blockCount != 2 {
		t.Fatalf("expected 2 block chars, got %d in %q", blockCount, result)
	}
}
