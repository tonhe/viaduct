package tui

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/tonhe/viaduct/internal/probe"
)

// e2eOpts are shared options for teatest scenarios.
// Wraps newTestModel to add teatest-specific settings.

// startE2E boots a teatest test with the given seeded Model.
// Returns the test model handle. Callers drive it with Send/Type/WaitFor
// and terminate with Quit.
func startE2E(t *testing.T, opts ...testOpt) *teatest.TestModel {
	t.Helper()
	setTestTheme(t)
	m := newTestModel(opts...)
	// teatest requires the model as tea.Model (interface); Model implements it.
	return teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(m.width, m.height),
	)
}

// TestE2E_ColdStart boots the model with no messages and quits.
// Verifies the initial frame renders without data and matches the golden file.
func TestE2E_ColdStart(t *testing.T) {
	tm := startE2E(t, withSize(120, 24))

	// Trigger a render by sending a WindowSizeMsg, then quit.
	tm.Send(tea.WindowSizeMsg{Width: 120, Height: 24})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	finalModel := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second))
	m := finalModel.(Model)

	got := m.View()
	path := filepath.Join("testdata", "e2e", "cold_start.golden")
	goldenCompare(t, path, got)
}

// TestE2E_HappyPath feeds 5 hop updates (last IsTarget=true) and 3 round-end
// messages, then quits. Asserts targetHit, probeCount, and golden frame.
func TestE2E_HappyPath(t *testing.T) {
	tm := startE2E(t, withSize(120, 24))

	// Feed 5 hop updates; the 5th marks the target.
	for i := 1; i <= 5; i++ {
		result := probe.Result{
			TTL:      i,
			IP:       net.ParseIP(fmt.Sprintf("192.0.2.%d", i)),
			RTT:      time.Duration(i*10) * time.Millisecond,
			IsTarget: i == 5,
			FlowID:   0,
		}
		tm.Send(HopUpdateMsg{Result: result})
	}

	// 3 round-end messages.
	for i := 0; i < 3; i++ {
		tm.Send(RoundEndMsg{})
	}

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	finalModel := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second))
	m := finalModel.(Model)

	if !m.targetHit {
		t.Error("targetHit not set after IsTarget hop")
	}
	if m.probeCount != 5 {
		t.Errorf("probeCount = %d, want 5", m.probeCount)
	}

	got := m.View()
	path := filepath.Join("testdata", "e2e", "happy_path.golden")
	goldenCompare(t, path, got)
}

// TestE2E_FailurePath sends one hop update then a ProbeErrorMsg, which causes
// the model to auto-quit via tea.Quit. Verifies err is set and golden frame.
func TestE2E_FailurePath(t *testing.T) {
	tm := startE2E(t, withSize(120, 24))

	tm.Send(HopUpdateMsg{Result: probe.Result{
		TTL: 1,
		IP:  net.ParseIP("192.0.2.1"),
		RTT: 5 * time.Millisecond,
	}})

	tm.Send(ProbeErrorMsg{Err: errors.New("network unreachable")})

	// The model auto-quits because ProbeErrorMsg returns tea.Quit.
	finalModel := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second))
	m := finalModel.(Model)

	if m.err == nil {
		t.Error("m.err not set after ProbeErrorMsg")
	}

	got := m.View()
	path := filepath.Join("testdata", "e2e", "failure_path.golden")
	goldenCompare(t, path, got)
}

// TestE2E_IPv6Target boots with an IPv6 config, feeds v6 hop updates and a
// RoundEndMsg, then quits. Verifies IPv6 addresses render correctly in the
// dynamic-width IP column and that targetHit is set.
func TestE2E_IPv6Target(t *testing.T) {
	tm := startE2E(t, withSize(160, 24), withIPVersion(6))

	// Send v6 hops.
	v6Addrs := []string{
		"2001:db8:1::1",
		"2001:db8:2::abcd",
		"2001:db8:3::1:5",
		"2001:db8:cafe::1",
	}
	for i, addr := range v6Addrs {
		tm.Send(HopUpdateMsg{Result: probe.Result{
			TTL:      i + 1,
			IP:       net.ParseIP(addr),
			RTT:      time.Duration((i+1)*5) * time.Millisecond,
			IsTarget: i == len(v6Addrs)-1,
			FlowID:   0,
		}})
	}

	tm.Send(RoundEndMsg{})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	finalModel := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second))
	m := finalModel.(Model)

	if m.probeCfg.IPVersion != 6 {
		t.Errorf("IPVersion = %d, want 6", m.probeCfg.IPVersion)
	}
	if !m.targetHit {
		t.Error("targetHit not set")
	}

	got := m.View()
	path := filepath.Join("testdata", "e2e", "ipv6_target.golden")
	goldenCompare(t, path, got)
}

// TestE2E_CleanQuit sends some hop updates then quits mid-round (no RoundEndMsg).
// Verifies the model exits cleanly within the timeout and that accumulated hop
// state is preserved on the final model.
func TestE2E_CleanQuit(t *testing.T) {
	tm := startE2E(t, withSize(120, 24))

	// Send some hops.
	for i := 1; i <= 3; i++ {
		tm.Send(HopUpdateMsg{Result: probe.Result{
			TTL: i, IP: net.ParseIP(fmt.Sprintf("192.0.2.%d", i)),
			RTT: time.Duration(i*10) * time.Millisecond,
		}})
	}

	// Quit mid-round (no RoundEndMsg sent).
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	finalModel := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second))
	m := finalModel.(Model)

	// The model itself doesn't spawn goroutines from Init/Update, so there's
	// nothing to leak here. The check is just that FinalModel returns within
	// the timeout — if quit didn't propagate, teatest would panic.

	// Verify the model still has the hops (state wasn't wiped on quit).
	snap := m.table.Snapshot()
	if len(snap) != 3 {
		t.Errorf("table has %d hops after quit, want 3", len(snap))
	}

	got := m.View()
	path := filepath.Join("testdata", "e2e", "clean_quit.golden")
	goldenCompare(t, path, got)
}

// TestE2E_Framework is the done-signal for T13: boot + immediate quit smoke test.
func TestE2E_Framework(t *testing.T) {
	tm := startE2E(t, withSize(100, 24))

	// Send a KeyMsg for 'q' to trigger quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	// Wait for the model to finish. teatest.WaitForFinish with a timeout.
	finalModel := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second))

	// Sanity: it should have exited cleanly and be a *Model or Model.
	if finalModel == nil {
		t.Fatal("nil final model")
	}
}
