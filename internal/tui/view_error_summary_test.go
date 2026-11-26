package tui

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

func TestGolden_ErrorView(t *testing.T) {
	setTestTheme(t)
	setHostOS(t, "linux")
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
	m.sessions[0].table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 0, hop.KindPrimary)
	m.sessions[0].table.GetOrCreate(2).AddSample(net.ParseIP("198.51.100.5"), 10*time.Millisecond, 0, hop.KindPrimary)
	m.sessions[0].table.GetOrCreate(3).AddSample(net.ParseIP("203.0.113.10"), 20*time.Millisecond, 0, hop.KindPrimary)
	m.sessions[0].table.GetOrCreate(4).AddSample(net.ParseIP("203.0.113.20"), 25*time.Millisecond, 0, hop.KindPrimary)
	m.sessions[0].targetHit = true
	m.sessions[0].maxTTLHit = 4
	m.sessions[0].probeCount = 12

	got := m.FinalSummary()
	path := filepath.Join("testdata", "view", "final_summary.golden")
	goldenCompare(t, path, got)
}

// TestFinalSummary_ErroredSession verifies that FinalSummary does not panic
// when the focused session has a non-nil startErr (IP never resolved) and that
// the output mentions both the target name and the error.
func TestFinalSummary_ErroredSession(t *testing.T) {
	specs := []TargetSpec{
		{
			Name: "bad.example",
			// IP is intentionally nil — simulates a failed DNS resolution.
			Err: fmt.Errorf("resolve bad.example: no such host"),
		},
	}
	m := NewMulti(specs, "v0.0.0-test", true, 5.0, 0, 3, true, nil)

	// Must not panic.
	var got string
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("FinalSummary panicked on errored session: %v", r)
			}
		}()
		got = m.FinalSummary()
	}()

	if !strings.Contains(got, "bad.example") {
		t.Errorf("FinalSummary output missing target name; got: %q", got)
	}
	if !strings.Contains(got, "error") {
		t.Errorf("FinalSummary output missing 'error'; got: %q", got)
	}
}
