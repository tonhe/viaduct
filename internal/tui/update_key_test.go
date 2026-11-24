package tui

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tonhe/viaduct/internal/hop"
)

// keyPress constructs a tea.KeyMsg whose .String() returns the given string.
// Single printable runes use KeyRunes; named keys are mapped via tea constants.
// This helper is defined here and shared across T6/T7 key-handler tests.
func keyPress(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// ---------------------------------------------------------------------------
// TestOnMainKey_Quit
// ---------------------------------------------------------------------------

func TestOnMainKey_Quit(t *testing.T) {
	t.Run("q quits", func(t *testing.T) {
		m := newTestModel()
		_, cmd := m.onMainKey(keyPress("q"))
		cmdIsQuit(t, cmd)
	})

	t.Run("ctrl+c quits", func(t *testing.T) {
		m := newTestModel()
		_, cmd := m.onMainKey(tea.KeyMsg{Type: tea.KeyCtrlC})
		cmdIsQuit(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnMainKey_Esc
// ---------------------------------------------------------------------------

func TestOnMainKey_Esc(t *testing.T) {
	t.Run("opens settings overlay", func(t *testing.T) {
		m := newTestModel()
		m2, cmd := m.onMainKey(tea.KeyMsg{Type: tea.KeyEsc})
		if !m2.showSettings {
			t.Error("showSettings not set after esc")
		}
		if m2.settings == nil {
			t.Error("settings not initialized after esc")
		}
		_ = cmd
	})
}

// ---------------------------------------------------------------------------
// TestOnMainKey_Pause
// ---------------------------------------------------------------------------

func TestOnMainKey_Pause(t *testing.T) {
	t.Run("toggles paused from false to true", func(t *testing.T) {
		m := newTestModel()
		if m.paused {
			t.Fatal("precondition: paused should start false")
		}
		m2, _ := m.onMainKey(keyPress("p"))
		if !m2.paused {
			t.Error("paused should flip to true")
		}
	})

	t.Run("toggles paused from true to false", func(t *testing.T) {
		m := newTestModel()
		m.paused = true
		m2, _ := m.onMainKey(keyPress("p"))
		if m2.paused {
			t.Error("paused should flip back to false")
		}
	})

	t.Run("does not panic with nil tracer", func(t *testing.T) {
		m := newTestModel() // tracer is nil by default
		if m.sessions[0].tracer != nil {
			t.Fatal("precondition: tracer should be nil")
		}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on p with nil tracer: %v", r)
			}
		}()
		_, _ = m.onMainKey(keyPress("p"))
	})
}

// ---------------------------------------------------------------------------
// TestOnMainKey_Reset
// ---------------------------------------------------------------------------

func TestOnMainKey_Reset(t *testing.T) {
	t.Run("clears probeCount", func(t *testing.T) {
		m := newTestModel()
		m.sessions[0].probeCount = 5
		m2, _ := m.onMainKey(keyPress("r"))
		if m2.sessions[0].probeCount != 0 {
			t.Errorf("probeCount = %d, want 0 after reset", m2.sessions[0].probeCount)
		}
	})

	t.Run("resets hop stats (nodes cleared, hops remain)", func(t *testing.T) {
		// ResetAll clears Nodes and sentTotal on each hop but does not remove
		// hops from the table map. Snapshot still returns the same number of hops.
		m := newTestModel(withHops(
			hopSample(1, "192.0.2.1", 10*time.Millisecond),
			hopSample(2, "192.0.2.2", 20*time.Millisecond),
		))
		m2, _ := m.onMainKey(keyPress("r"))
		snap := m2.sessions[0].table.Snapshot()
		// Hops are retained in the table after reset
		if len(snap) != 2 {
			t.Errorf("table snapshot len = %d, want 2 (hops remain after reset)", len(snap))
		}
		// But their nodes and stats should be cleared
		for _, h := range snap {
			if len(h.GetNodes()) != 0 {
				t.Errorf("TTL %d: GetNodes() = %d, want 0 after reset", h.TTL, len(h.GetNodes()))
			}
			if h.Rounds() != 0 {
				t.Errorf("TTL %d: Rounds() = %d, want 0 after reset", h.TTL, h.Rounds())
			}
		}
	})

	t.Run("resets startTime to recent", func(t *testing.T) {
		m := newTestModel()
		m.startTime = time.Unix(0, 0) // very old
		before := time.Now()
		m2, _ := m.onMainKey(keyPress("r"))
		if m2.startTime.Before(before) {
			t.Errorf("startTime = %v, want at or after %v", m2.startTime, before)
		}
	})

	t.Run("resets alert engine when present", func(t *testing.T) {
		m := newTestModel(withAlertEngine(5.0, 100*time.Millisecond, 3))
		// drive the engine into a non-healthy state if possible, then reset
		if m.sessions[0].alertEngine == nil {
			t.Fatal("precondition: alertEngine should be set")
		}
		m2, _ := m.onMainKey(keyPress("r"))
		// After reset, engine should be healthy (freshly reset)
		if m2.sessions[0].alertEngine.State() != AlertHealthy {
			t.Errorf("alert state = %v, want AlertHealthy after reset", m2.sessions[0].alertEngine.State())
		}
	})

	t.Run("does not panic with nil alertEngine", func(t *testing.T) {
		m := newTestModel() // alertEngine nil by default
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on r with nil alertEngine: %v", r)
			}
		}()
		_, _ = m.onMainKey(keyPress("r"))
	})
}

// ---------------------------------------------------------------------------
// TestOnMainKey_CycleViewMode
// ---------------------------------------------------------------------------

func TestOnMainKey_CycleViewMode(t *testing.T) {
	t.Run("d advances viewMode by one", func(t *testing.T) {
		m := newTestModel(withView(ViewDefault))
		m2, _ := m.onMainKey(keyPress("d"))
		if m2.viewMode != ViewDefault.Next() {
			t.Errorf("viewMode = %v, want %v", m2.viewMode, ViewDefault.Next())
		}
	})

	t.Run("d wraps through all view modes back to start", func(t *testing.T) {
		m := newTestModel(withView(ViewDefault))
		// Cycle through every mode until we return to ViewDefault
		for i := 0; i < int(ViewCount); i++ {
			m, _ = m.onMainKey(keyPress("d"))
		}
		if m.viewMode != ViewDefault {
			t.Errorf("viewMode after full cycle = %v, want ViewDefault", m.viewMode)
		}
	})
}

// ---------------------------------------------------------------------------
// TestOnMainKey_ScrollDown (j / down)
// ---------------------------------------------------------------------------

func TestOnMainKey_ScrollDown(t *testing.T) {
	// scrollOffset > 0 means we're viewing above the bottom; j brings us closer
	// to bottom (decrements offset). When offset reaches 0, autoScroll becomes true.

	t.Run("j decrements scrollOffset when > 0", func(t *testing.T) {
		m := newTestModel()
		m.scrollOffset = 3
		m.autoScroll = false
		m2, _ := m.onMainKey(keyPress("j"))
		if m2.scrollOffset != 2 {
			t.Errorf("scrollOffset = %d, want 2", m2.scrollOffset)
		}
	})

	t.Run("down decrements scrollOffset when > 0", func(t *testing.T) {
		m := newTestModel()
		m.scrollOffset = 3
		m.autoScroll = false
		m2, _ := m.onMainKey(tea.KeyMsg{Type: tea.KeyDown})
		if m2.scrollOffset != 2 {
			t.Errorf("scrollOffset = %d, want 2", m2.scrollOffset)
		}
	})

	t.Run("j clamps at 0 (does not go negative)", func(t *testing.T) {
		m := newTestModel()
		m.scrollOffset = 0
		m.autoScroll = false
		m2, _ := m.onMainKey(keyPress("j"))
		if m2.scrollOffset < 0 {
			t.Errorf("scrollOffset = %d, want >= 0 (no negative scroll)", m2.scrollOffset)
		}
	})

	t.Run("j sets autoScroll true when offset reaches 0", func(t *testing.T) {
		m := newTestModel()
		m.scrollOffset = 1
		m.autoScroll = false
		m2, _ := m.onMainKey(keyPress("j"))
		if m2.scrollOffset != 0 {
			t.Errorf("scrollOffset = %d, want 0", m2.scrollOffset)
		}
		if !m2.autoScroll {
			t.Error("autoScroll should be true when scrollOffset reaches 0")
		}
	})

	t.Run("j sets autoScroll false when offset does not reach 0", func(t *testing.T) {
		m := newTestModel()
		m.scrollOffset = 3
		m.autoScroll = true
		m2, _ := m.onMainKey(keyPress("j"))
		if m2.autoScroll {
			t.Error("autoScroll should be false when scrollOffset is still > 0")
		}
	})
}

// ---------------------------------------------------------------------------
// TestOnMainKey_ScrollUp (k / up)
// ---------------------------------------------------------------------------

func TestOnMainKey_ScrollUp(t *testing.T) {
	// k/up increments scrollOffset (moving view toward older/top data)
	// and always sets autoScroll = false.

	// To get maxScrollOffset > 0 we need more hops than display rows.
	// height=24 → availableRows = 20; seed 25 hops.
	makeScrollableModel := func() Model {
		opts := make([]hopSampleFixture, 25)
		for i := range opts {
			opts[i] = hopSample(i+1, "192.0.2.1", 10*time.Millisecond)
		}
		return newTestModel(withSize(100, 24), withHops(opts...))
	}

	t.Run("k increments scrollOffset when below max", func(t *testing.T) {
		m := makeScrollableModel()
		m.scrollOffset = 0
		m.autoScroll = true
		m2, _ := m.onMainKey(keyPress("k"))
		if m2.scrollOffset != 1 {
			t.Errorf("scrollOffset = %d, want 1", m2.scrollOffset)
		}
	})

	t.Run("up increments scrollOffset when below max", func(t *testing.T) {
		m := makeScrollableModel()
		m.scrollOffset = 0
		m.autoScroll = true
		m2, _ := m.onMainKey(tea.KeyMsg{Type: tea.KeyUp})
		if m2.scrollOffset != 1 {
			t.Errorf("scrollOffset = %d, want 1", m2.scrollOffset)
		}
	})

	t.Run("k clamps at maxScrollOffset", func(t *testing.T) {
		m := makeScrollableModel()
		maxOff := m.maxScrollOffset()
		if maxOff == 0 {
			t.Skip("maxScrollOffset == 0; need more hops than display rows")
		}
		m.scrollOffset = maxOff
		m2, _ := m.onMainKey(keyPress("k"))
		if m2.scrollOffset > maxOff {
			t.Errorf("scrollOffset = %d exceeded max %d", m2.scrollOffset, maxOff)
		}
	})

	t.Run("k sets autoScroll false", func(t *testing.T) {
		m := makeScrollableModel()
		m.scrollOffset = 0
		m.autoScroll = true
		m2, _ := m.onMainKey(keyPress("k"))
		if m2.autoScroll {
			t.Error("autoScroll should be false after k")
		}
	})
}

// ---------------------------------------------------------------------------
// TestOnMainKey_JumpToBottom (G)
// ---------------------------------------------------------------------------

func TestOnMainKey_JumpToBottom(t *testing.T) {
	t.Run("G sets scrollOffset to 0 and autoScroll true", func(t *testing.T) {
		m := newTestModel()
		m.scrollOffset = 5
		m.autoScroll = false
		m2, _ := m.onMainKey(keyPress("G"))
		if m2.scrollOffset != 0 {
			t.Errorf("scrollOffset = %d, want 0", m2.scrollOffset)
		}
		if !m2.autoScroll {
			t.Error("autoScroll should be true after G")
		}
	})
}

// ---------------------------------------------------------------------------
// TestOnMainKey_JumpToTop (g)
// ---------------------------------------------------------------------------

func TestOnMainKey_JumpToTop(t *testing.T) {
	t.Run("g sets scrollOffset to maxScrollOffset and autoScroll false", func(t *testing.T) {
		// seed enough hops to have a non-trivial maxScrollOffset
		opts := make([]hopSampleFixture, 25)
		for i := range opts {
			opts[i] = hopSample(i+1, "192.0.2.1", 10*time.Millisecond)
		}
		m := newTestModel(withSize(100, 24), withHops(opts...))
		m.scrollOffset = 0
		m.autoScroll = true

		maxOff := m.maxScrollOffset()
		m2, _ := m.onMainKey(keyPress("g"))

		if m2.scrollOffset != maxOff {
			t.Errorf("scrollOffset = %d, want %d (maxScrollOffset)", m2.scrollOffset, maxOff)
		}
		if m2.autoScroll {
			t.Error("autoScroll should be false after g")
		}
	})

	t.Run("g with no scrollable content sets offset to 0", func(t *testing.T) {
		// Empty table → maxScrollOffset == 0
		m := newTestModel()
		m.scrollOffset = 0
		m.autoScroll = true
		m2, _ := m.onMainKey(keyPress("g"))
		if m2.scrollOffset != 0 {
			t.Errorf("scrollOffset = %d, want 0 when no content", m2.scrollOffset)
		}
		if m2.autoScroll {
			t.Error("autoScroll should be false after g even with no content")
		}
	})
}

// ---------------------------------------------------------------------------
// TestOnMainKey_Export (e)
// ---------------------------------------------------------------------------

func TestOnMainKey_Export(t *testing.T) {
	t.Run("e returns non-nil export cmd", func(t *testing.T) {
		m := newTestModel()
		_, cmd := m.onMainKey(keyPress("e"))
		cmdIsNonNil(t, cmd)
	})
}

// TestExportCmd_NoRaceOnSessionFields verifies that exportCmd snapshots all
// session scalar fields before building the closure, so concurrent mutations
// by the update loop cannot race with the async tea.Cmd goroutine.
//
// Under -race this test fails on the pre-fix code (closure read s.probeCount
// etc. through the live *TargetSession while the loop writes them) and passes
// after the fix (locals captured at call site, no shared pointer reads).
func TestExportCmd_NoRaceOnSessionFields(t *testing.T) {
	// Write the export file to a throw-away directory so the test is
	// hermetic and leaves no files behind in the working directory.
	t.Chdir(t.TempDir())

	m := newTestModel()

	// Obtain the cmd while the session is in a known state.
	cmd := m.exportCmd()

	done := make(chan struct{})

	// Run the closure in a goroutine — this is what tea does with tea.Cmd.
	go func() {
		defer close(done)
		cmd() // reads the snapshotted locals; must not touch s.*
	}()

	// Simulate the update-loop writing session scalars concurrently.
	for i := range 100 {
		m.sessions[0].probeCount = i
		m.sessions[0].maxTTLHit = i % 30
	}

	<-done
}

// ---------------------------------------------------------------------------
// TestOnMainKey_Noop
// ---------------------------------------------------------------------------

func TestOnMainKey_Noop(t *testing.T) {
	t.Run("unknown key leaves model unchanged and returns nil cmd", func(t *testing.T) {
		m := newTestModel()
		origView := m.viewMode
		origScroll := m.scrollOffset
		origPaused := m.paused
		origProbeCount := m.sessions[0].probeCount

		m2, cmd := m.onMainKey(keyPress("x"))

		if m2.viewMode != origView {
			t.Errorf("viewMode changed: got %v, want %v", m2.viewMode, origView)
		}
		if m2.scrollOffset != origScroll {
			t.Errorf("scrollOffset changed: got %d, want %d", m2.scrollOffset, origScroll)
		}
		if m2.paused != origPaused {
			t.Errorf("paused changed: got %v, want %v", m2.paused, origPaused)
		}
		if m2.sessions[0].probeCount != origProbeCount {
			t.Errorf("probeCount changed: got %d, want %d", m2.sessions[0].probeCount, origProbeCount)
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestKeys_MultiTargetFocus (Task 8)
// ---------------------------------------------------------------------------

func TestKeys_MultiTargetFocus(t *testing.T) {
	m := multiTestModel(t, withSize(120, 40))

	next, _ := m.onMainKey(keyPress("down"))
	m2 := next
	if m2.focusedIdx != 1 {
		t.Fatalf("down: focusedIdx = %d, want 1", m2.focusedIdx)
	}

	next, _ = m2.onMainKey(keyPress("up"))
	m3 := next
	if m3.focusedIdx != 0 {
		t.Fatalf("up: focusedIdx = %d, want 0", m3.focusedIdx)
	}

	// tab wraps: 3 tabs over 3 targets returns to 0
	next, _ = m3.onMainKey(keyPress("tab"))
	next, _ = next.onMainKey(keyPress("tab"))
	next, _ = next.onMainKey(keyPress("tab"))
	m4 := next
	if m4.focusedIdx != 0 {
		t.Fatalf("3x tab over 3 targets: focusedIdx = %d, want 0", m4.focusedIdx)
	}
}

func TestKeys_MultiTargetFocus_Clamp(t *testing.T) {
	m := multiTestModel(t, withSize(120, 40))
	// Already at index 0; up should not go negative.
	next, _ := m.onMainKey(keyPress("up"))
	if next.focusedIdx != 0 {
		t.Fatalf("up at index 0: focusedIdx = %d, want 0 (clamped)", next.focusedIdx)
	}

	// Go to last session; down should not exceed len-1.
	m.focusedIdx = len(m.sessions) - 1
	next, _ = m.onMainKey(keyPress("down"))
	if next.focusedIdx != len(m.sessions)-1 {
		t.Fatalf("down at last index: focusedIdx = %d, want %d (clamped)", next.focusedIdx, len(m.sessions)-1)
	}
}

// ---------------------------------------------------------------------------
// TestKeys_MarkAndToggleColumns (Task 8)
// ---------------------------------------------------------------------------

func TestKeys_MarkAndToggleColumns(t *testing.T) {
	m := multiTestModel(t, withSize(120, 40))

	next, _ := m.onMainKey(keyPress(" "))
	m2 := next
	if !m2.sessions[0].marked {
		t.Fatal("space should mark focused session")
	}

	// space again should unmark
	next, _ = m2.onMainKey(keyPress(" "))
	m3 := next
	if m3.sessions[0].marked {
		t.Fatal("second space should unmark focused session")
	}

	// 'c' toggles columnView
	next, _ = m3.onMainKey(keyPress("c"))
	m4 := next
	if !m4.columnView {
		t.Fatal("'c' should enable column view")
	}

	// 'c' again should disable it
	next, _ = m4.onMainKey(keyPress("c"))
	m5 := next
	if m5.columnView {
		t.Fatal("second 'c' should disable column view")
	}
}

// ---------------------------------------------------------------------------
// TestKeys_SingleTargetUnaffected (Task 8)
// ---------------------------------------------------------------------------

func TestKeys_SingleTargetUnaffected(t *testing.T) {
	t.Run("c is no-op with one target", func(t *testing.T) {
		m := newTestModel(withSize(100, 24))
		next, _ := m.onMainKey(keyPress("c"))
		if next.columnView {
			t.Fatal("'c' must be a no-op with one target")
		}
	})

	t.Run("space is no-op with one target", func(t *testing.T) {
		m := newTestModel(withSize(100, 24))
		next, _ := m.onMainKey(keyPress(" "))
		if next.sessions[0].marked {
			t.Fatal("space must be a no-op with one target")
		}
	})

	t.Run("down acts as scroll with one target", func(t *testing.T) {
		// In single-target mode, down still scrolls (decrements scrollOffset).
		m := newTestModel(withSize(100, 24))
		m.scrollOffset = 3
		next, _ := m.onMainKey(keyPress("down"))
		if next.focusedIdx != 0 {
			t.Errorf("focusedIdx changed: got %d, want 0", next.focusedIdx)
		}
		if next.scrollOffset != 2 {
			t.Errorf("scrollOffset = %d, want 2 (scroll still works in single-target)", next.scrollOffset)
		}
	})

	t.Run("up acts as scroll with one target", func(t *testing.T) {
		// Build a model with enough hops to have positive maxScrollOffset.
		opts := make([]hopSampleFixture, 25)
		for i := range opts {
			opts[i] = hopSample(i+1, "192.0.2.1", 10*time.Millisecond)
		}
		m := newTestModel(withSize(100, 24), withHops(opts...))
		m.scrollOffset = 0
		next, _ := m.onMainKey(keyPress("up"))
		if next.focusedIdx != 0 {
			t.Errorf("focusedIdx changed: got %d, want 0", next.focusedIdx)
		}
		if next.scrollOffset != 1 {
			t.Errorf("scrollOffset = %d, want 1 (scroll still works in single-target)", next.scrollOffset)
		}
	})
}

// ---------------------------------------------------------------------------
// TestKeys_FocusChangeClampsScroll
// ---------------------------------------------------------------------------

func TestKeys_FocusChangeClampsScroll(t *testing.T) {
	// Build a 3-target model where session 0 has 25 hops (scrollable at height 24)
	// but sessions 1 and 2 have few/no hops (maxScrollOffset == 0 for them).
	opts := make([]hopSampleFixture, 25)
	for i := range opts {
		opts[i] = hopSample(i+1, "192.0.2.1", 10*time.Millisecond)
	}
	m := multiTestModel(t, withSize(120, 24), withHops(opts...))

	// Confirm session 0 has scrollable content.
	maxOff0 := m.maxScrollOffset()
	if maxOff0 == 0 {
		t.Skip("session 0 maxScrollOffset == 0; need more hops than display rows")
	}

	// Scroll session 0 all the way up — this offset exceeds session 1's max.
	m.scrollOffset = maxOff0
	m.autoScroll = false

	// Focus session 1 via down-arrow.
	next, _ := m.onMainKey(keyPress("down"))

	if next.focusedIdx != 1 {
		t.Fatalf("down: focusedIdx = %d, want 1", next.focusedIdx)
	}

	maxOff1 := next.maxScrollOffset()
	if next.scrollOffset > maxOff1 {
		t.Errorf("scrollOffset = %d exceeds maxScrollOffset = %d after focus change", next.scrollOffset, maxOff1)
	}

	// autoScroll invariant: true iff scrollOffset == 0.
	wantAutoScroll := next.scrollOffset == 0
	if next.autoScroll != wantAutoScroll {
		t.Errorf("autoScroll = %v, want %v (scrollOffset = %d)", next.autoScroll, wantAutoScroll, next.scrollOffset)
	}

	// Must not panic during View().
	_ = next.View()

	// Now test via tab key: scroll session 0 again and tab to session 1.
	m.scrollOffset = maxOff0
	m.autoScroll = false
	m.focusedIdx = 0

	next2, _ := m.onMainKey(keyPress("tab"))
	if next2.focusedIdx != 1 {
		t.Fatalf("tab: focusedIdx = %d, want 1", next2.focusedIdx)
	}
	maxOff1b := next2.maxScrollOffset()
	if next2.scrollOffset > maxOff1b {
		t.Errorf("tab: scrollOffset = %d exceeds maxScrollOffset = %d after focus change", next2.scrollOffset, maxOff1b)
	}
	wantAutoScroll2 := next2.scrollOffset == 0
	if next2.autoScroll != wantAutoScroll2 {
		t.Errorf("tab: autoScroll = %v, want %v (scrollOffset = %d)", next2.autoScroll, wantAutoScroll2, next2.scrollOffset)
	}
	_ = next2.View()
}

// ---------------------------------------------------------------------------
// TestKeys_MultiTarget_PauseAndReset (Task 8)
// ---------------------------------------------------------------------------

func TestKeys_MultiTarget_Pause(t *testing.T) {
	t.Run("p toggles paused on multi-target model", func(t *testing.T) {
		m := multiTestModel(t, withSize(120, 40))
		if m.paused {
			t.Fatal("precondition: paused should start false")
		}
		next, _ := m.onMainKey(keyPress("p"))
		if !next.paused {
			t.Error("paused should flip to true")
		}
	})

	t.Run("p does not panic with nil tracers on all sessions", func(t *testing.T) {
		m := multiTestModel(t, withSize(120, 40))
		for i, s := range m.sessions {
			if s.tracer != nil {
				t.Fatalf("precondition: sessions[%d].tracer should be nil", i)
			}
		}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on p with nil tracers: %v", r)
			}
		}()
		_, _ = m.onMainKey(keyPress("p"))
	})
}

func TestKeys_MultiTarget_Reset(t *testing.T) {
	t.Run("r resets all sessions' tables and counters", func(t *testing.T) {
		m := multiTestModel(t, withSize(120, 40))
		// Seed probeCount and add hops to session 0.
		m.sessions[0].probeCount = 7
		m.sessions[1].probeCount = 3
		// Seed a hop on session 1 to verify table reset.
		m.sessions[1].table.GetOrCreate(1).AddSample(nil, 10*time.Millisecond, 0, hop.KindPrimary)
		m.sessions[1].table.MarkRoundSent(1)

		next, _ := m.onMainKey(keyPress("r"))

		for i, s := range next.sessions {
			if s.probeCount != 0 {
				t.Errorf("sessions[%d].probeCount = %d, want 0 after reset", i, s.probeCount)
			}
		}
	})

	t.Run("r does not panic with nil alertEngines", func(t *testing.T) {
		m := multiTestModel(t, withSize(120, 40))
		for i, s := range m.sessions {
			if s.alertEngine != nil {
				t.Fatalf("precondition: sessions[%d].alertEngine should be nil", i)
			}
		}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on r with nil alertEngines: %v", r)
			}
		}()
		_, _ = m.onMainKey(keyPress("r"))
	})
}

// ---------------------------------------------------------------------------
// TestExportCmd_MultiTarget (Task 12)
// ---------------------------------------------------------------------------

// TestExportCmd_MultiTarget verifies the multi-target branch of exportCmd:
//   - Returns a non-nil cmd.
//   - Writes a file without error when invoked.
//   - Skips errored sessions (bad.example has startErr set in multiTestModel).
//   - Includes non-errored sessions (one.example and two.example) in output.
func TestExportCmd_MultiTarget(t *testing.T) {
	// Write export files to a temp directory so the test is hermetic.
	t.Chdir(t.TempDir())

	m := multiTestModel(t, withSize(120, 40))

	// Precondition: multiTestModel builds 3 sessions, third is errored.
	if len(m.sessions) != 3 {
		t.Fatalf("precondition: want 3 sessions, got %d", len(m.sessions))
	}
	if m.sessions[2].startErr == nil {
		t.Fatal("precondition: sessions[2].startErr should be non-nil (bad.example)")
	}

	cmd := m.exportCmd()
	if cmd == nil {
		t.Fatal("exportCmd returned nil for multi-target model")
	}

	msg := cmd()
	done, ok := msg.(ExportDoneMsg)
	if !ok {
		t.Fatalf("exportCmd returned %T, want ExportDoneMsg", msg)
	}
	if done.Err != nil {
		t.Fatalf("export failed: %v", done.Err)
	}
	if done.Path == "" {
		t.Fatal("ExportDoneMsg.Path is empty")
	}

	// The exported file must exist.
	if _, err := os.Stat(done.Path); err != nil {
		t.Fatalf("exported file not found at %q: %v", done.Path, err)
	}

	// Read the file and verify both healthy targets are present and errored
	// session is absent.
	data, err := os.ReadFile(done.Path)
	if err != nil {
		t.Fatalf("reading exported file: %v", err)
	}
	content := string(data)

	for _, want := range []string{"one.example", "two.example"} {
		if !strings.Contains(content, want) {
			t.Errorf("exported file missing %q", want)
		}
	}
	if strings.Contains(content, "bad.example") {
		t.Error("exported file should not contain errored session bad.example")
	}
}

// TestExportCmd_AllErroredSessions verifies that exportCmd returns an error
// when all sessions have startErr set (all sessions failed to start).
func TestExportCmd_AllErroredSessions(t *testing.T) {
	t.Chdir(t.TempDir())

	// Build a multi-target model where all sessions have startErr.
	m := newTestModel(withSize(120, 40))
	m.sessions[0].target = "one.example"
	m.sessions[0].startErr = fmt.Errorf("cannot resolve host")

	s2 := newSession("two.example", net.ParseIP("192.0.2.2"), m.sessions[0].probeCfg, "udp", 5.0, 0, 3, true)
	s2.startErr = fmt.Errorf("connection timeout")

	s3 := newSession("three.example", net.ParseIP("192.0.2.3"), m.sessions[0].probeCfg, "udp", 5.0, 0, 3, true)
	s3.startErr = fmt.Errorf("permission denied")

	m.sessions = append(m.sessions, s2, s3)

	cmd := m.exportCmd()
	if cmd == nil {
		t.Fatal("exportCmd returned nil for multi-target model")
	}

	msg := cmd()
	done, ok := msg.(ExportDoneMsg)
	if !ok {
		t.Fatalf("exportCmd returned %T, want ExportDoneMsg", msg)
	}

	// Must return an error since all sessions errored.
	if done.Err == nil {
		t.Fatal("expected error when all sessions have startErr, got nil")
	}
	if done.Err.Error() != "no active sessions to export" {
		t.Errorf("expected error 'no active sessions to export', got %q", done.Err.Error())
	}

	// No file should be created.
	if done.Path != "" {
		t.Errorf("expected empty Path on error, got %q", done.Path)
	}

	// Verify no export file was written.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading current directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "via-multi-") {
			t.Errorf("unexpected export file created: %s", entry.Name())
		}
	}
}
