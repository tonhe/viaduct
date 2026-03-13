package tui

import (
	"net"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

func TestViewModeCount(t *testing.T) {
	if ViewCount != 4 {
		t.Fatalf("expected 4 view modes, got %d", ViewCount)
	}
}

func TestViewModeCycle(t *testing.T) {
	v := ViewDefault
	v = v.Next()
	if v != ViewHealth {
		t.Fatalf("expected Health after Default, got %v", v)
	}
	v = v.Next()
	if v != ViewLatency {
		t.Fatalf("expected Latency after Health, got %v", v)
	}
	v = v.Next()
	if v != ViewVariability {
		t.Fatalf("expected Variability after Latency, got %v", v)
	}
	v = v.Next()
	if v != ViewDefault {
		t.Fatalf("expected Default after Variability, got %v", v)
	}
}

func TestViewModeNames(t *testing.T) {
	if ViewDefault.String() != "Default" {
		t.Fatalf("expected Default, got %s", ViewDefault.String())
	}
	if ViewHealth.String() != "Health" {
		t.Fatalf("expected Health, got %s", ViewHealth.String())
	}
}

func TestViewColumns(t *testing.T) {
	cols := ViewHealth.Columns()
	hasDelta := false
	hasTrend := false
	for _, c := range cols {
		if c == "Delta" {
			hasDelta = true
		}
		if c == "Trend" {
			hasTrend = true
		}
	}
	if !hasDelta || !hasTrend {
		t.Fatalf("Health view missing Delta or Trend: %v", cols)
	}
}

func TestComputeDeltasBasic(t *testing.T) {
	table := hop.NewTable(4)
	table.GetOrCreate(1).AddSample(net.ParseIP("10.0.0.1"), 1*time.Millisecond, 0)
	table.GetOrCreate(2).AddSample(net.ParseIP("10.0.0.2"), 5*time.Millisecond, 0)
	table.GetOrCreate(3).AddSample(net.ParseIP("10.0.0.3"), 18*time.Millisecond, 0)

	hops := table.Snapshot()
	hopMap := make(map[int]*hop.Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}
	deltas := computeDeltas(hopMap, 3)

	if deltas[1] != 1*time.Millisecond {
		t.Fatalf("hop 1 delta: expected 1ms, got %v", deltas[1])
	}
	if deltas[2] != 4*time.Millisecond {
		t.Fatalf("hop 2 delta: expected 4ms, got %v", deltas[2])
	}
	if deltas[3] != 13*time.Millisecond {
		t.Fatalf("hop 3 delta: expected 13ms, got %v", deltas[3])
	}
}

func TestComputeDeltasSkipsTimedOut(t *testing.T) {
	table := hop.NewTable(4)
	table.GetOrCreate(1).AddSample(net.ParseIP("10.0.0.1"), 5*time.Millisecond, 0)
	table.GetOrCreate(2) // timed out
	table.GetOrCreate(3).AddSample(net.ParseIP("10.0.0.3"), 20*time.Millisecond, 0)

	hops := table.Snapshot()
	hopMap := make(map[int]*hop.Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}
	deltas := computeDeltas(hopMap, 3)

	if deltas[3] != 15*time.Millisecond {
		t.Fatalf("hop 3 delta: expected 15ms (skipping hop 2), got %v", deltas[3])
	}
}
