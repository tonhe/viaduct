package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
		if m.tracer != nil {
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
		m.probeCount = 5
		m2, _ := m.onMainKey(keyPress("r"))
		if m2.probeCount != 0 {
			t.Errorf("probeCount = %d, want 0 after reset", m2.probeCount)
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
		snap := m2.table.Snapshot()
		// Hops are retained in the table after reset
		if len(snap) != 2 {
			t.Errorf("table snapshot len = %d, want 2 (hops remain after reset)", len(snap))
		}
		// But their nodes and stats should be cleared
		for _, h := range snap {
			if len(h.GetNodes()) != 0 {
				t.Errorf("TTL %d: GetNodes() = %d, want 0 after reset", h.TTL, len(h.GetNodes()))
			}
			if h.GetSent() != 0 {
				t.Errorf("TTL %d: GetSent() = %d, want 0 after reset", h.TTL, h.GetSent())
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

	t.Run("clears pingStats entries", func(t *testing.T) {
		m := newTestModel(withPingStats())
		// seed a stat entry manually
		m.pingStats["192.0.2.1"] = nil
		m2, _ := m.onMainKey(keyPress("r"))
		if len(m2.pingStats) != 0 {
			t.Errorf("pingStats len = %d, want 0 after reset", len(m2.pingStats))
		}
	})

	t.Run("resets alert engine when present", func(t *testing.T) {
		m := newTestModel(withAlertEngine(5.0, 100*time.Millisecond, 3))
		// drive the engine into a non-healthy state if possible, then reset
		if m.alertEngine == nil {
			t.Fatal("precondition: alertEngine should be set")
		}
		m2, _ := m.onMainKey(keyPress("r"))
		// After reset, engine should be healthy (freshly reset)
		if m2.alertEngine.State() != AlertHealthy {
			t.Errorf("alert state = %v, want AlertHealthy after reset", m2.alertEngine.State())
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

// ---------------------------------------------------------------------------
// TestOnMainKey_Noop
// ---------------------------------------------------------------------------

func TestOnMainKey_Noop(t *testing.T) {
	t.Run("unknown key leaves model unchanged and returns nil cmd", func(t *testing.T) {
		m := newTestModel()
		origView := m.viewMode
		origScroll := m.scrollOffset
		origPaused := m.paused
		origProbeCount := m.probeCount

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
		if m2.probeCount != origProbeCount {
			t.Errorf("probeCount changed: got %d, want %d", m2.probeCount, origProbeCount)
		}
		cmdIsNil(t, cmd)
	})
}
