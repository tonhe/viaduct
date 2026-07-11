package tui

import (
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestGolden_ErrorView(t *testing.T) {
	setTestTheme(t)
	m := newTestModel(withSize(100, 24))
	m.err = errors.New("raw socket permission denied: operation not permitted")
	got := m.View()
	path := filepath.Join("testdata", "view", "error_view.golden")
	goldenCompare(t, path, got)
}

func TestGolden_FinalSummary(t *testing.T) {
	setTestTheme(t)
	m := newTestModel(
		withSize(120, 24),
		withView(ViewDefault),
	)
	// Seed a realistic trace
	m.table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 0)
	m.table.GetOrCreate(2).AddSample(net.ParseIP("198.51.100.5"), 10*time.Millisecond, 0)
	m.table.GetOrCreate(3).AddSample(net.ParseIP("203.0.113.10"), 20*time.Millisecond, 0)
	m.table.GetOrCreate(4).AddSample(net.ParseIP("203.0.113.20"), 25*time.Millisecond, 0)
	m.targetHit = true
	m.maxTTLHit = 4
	m.probeCount = 12

	got := m.FinalSummary()
	path := filepath.Join("testdata", "view", "final_summary.golden")
	goldenCompare(t, path, got)
}
