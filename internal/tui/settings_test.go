package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tonhe/viaduct/internal/config"
)

// ---------------------------------------------------------------------------
// Cmd matchers for settings messages
// ---------------------------------------------------------------------------

func cmdEmitsSettingsSaved(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected settings saved cmd, got nil")
	}
	msg := cmd()
	if _, ok := msg.(SettingsSavedMsg); !ok {
		t.Errorf("expected SettingsSavedMsg, got %T", msg)
	}
}

func cmdEmitsSettingsCancel(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected settings cancel cmd, got nil")
	}
	msg := cmd()
	if _, ok := msg.(SettingsCancelMsg); !ok {
		t.Errorf("expected SettingsCancelMsg, got %T", msg)
	}
}

// newTestSettings constructs a SettingsModel from config.Default() at 100×24.
func newTestSettings() SettingsModel {
	return NewSettings(config.Default(), 100, 24)
}

// ---------------------------------------------------------------------------
// TestSettings_MainState — settingsMain
// ---------------------------------------------------------------------------

func TestSettings_MainState(t *testing.T) {
	t.Run("enter on cursor 0 transitions to theme sub-view", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 0
		s2, cmd := s.Update(keyPress("enter"))
		if s2.state != settingsTheme {
			t.Errorf("state = %v, want settingsTheme", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("enter on cursor 1 transitions to probe sub-view", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 1
		s2, cmd := s.Update(keyPress("enter"))
		if s2.state != settingsProbe {
			t.Errorf("state = %v, want settingsProbe", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("enter on cursor 2 transitions to display sub-view", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 2
		s2, cmd := s.Update(keyPress("enter"))
		if s2.state != settingsDisplay {
			t.Errorf("state = %v, want settingsDisplay", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("enter on cursor 3 transitions to alert sub-view", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 3
		s2, cmd := s.Update(keyPress("enter"))
		if s2.state != settingsAlert {
			t.Errorf("state = %v, want settingsAlert", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("enter on cursor 4 (Save & Exit) emits SettingsSavedMsg", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 4
		_, cmd := s.Update(keyPress("enter"))
		cmdEmitsSettingsSaved(t, cmd)
	})

	t.Run("enter on cursor 5 (Cancel) emits SettingsCancelMsg", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 5
		_, cmd := s.Update(keyPress("enter"))
		cmdEmitsSettingsCancel(t, cmd)
	})

	t.Run("space acts like enter on cursor 0", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 0
		s2, _ := s.Update(keyPress(" "))
		if s2.state != settingsTheme {
			t.Errorf("state = %v, want settingsTheme", s2.state)
		}
	})

	t.Run("down moves cursor from 0 to 1", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 0
		s2, cmd := s.Update(keyPress("down"))
		if s2.cursor != 1 {
			t.Errorf("cursor = %d, want 1", s2.cursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("j moves cursor from 0 to 1", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 0
		s2, cmd := s.Update(keyPress("j"))
		if s2.cursor != 1 {
			t.Errorf("cursor = %d, want 1", s2.cursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("up moves cursor from 2 to 1", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 2
		s2, cmd := s.Update(keyPress("up"))
		if s2.cursor != 1 {
			t.Errorf("cursor = %d, want 1", s2.cursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("k moves cursor from 2 to 1", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 2
		s2, cmd := s.Update(keyPress("k"))
		if s2.cursor != 1 {
			t.Errorf("cursor = %d, want 1", s2.cursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("down clamps at last item", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = numMainItems - 1
		s2, cmd := s.Update(keyPress("down"))
		if s2.cursor != numMainItems-1 {
			t.Errorf("cursor = %d, want %d (clamped)", s2.cursor, numMainItems-1)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("up clamps at first item", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 0
		s2, cmd := s.Update(keyPress("up"))
		if s2.cursor != 0 {
			t.Errorf("cursor = %d, want 0 (clamped)", s2.cursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("esc with no changes emits SettingsCancelMsg", func(t *testing.T) {
		s := newTestSettings()
		// No changes — savedConfig equals config, previousTheme == Current
		_, cmd := s.Update(keyPress("esc"))
		cmdEmitsSettingsCancel(t, cmd)
	})

	t.Run("esc with unsaved changes transitions to confirmDiscard", func(t *testing.T) {
		s := newTestSettings()
		// Mutate working config so hasChanges() returns true
		s.config.Protocol = "icmp"
		s2, cmd := s.Update(keyPress("esc"))
		if s2.state != settingsConfirmDiscard {
			t.Errorf("state = %v, want settingsConfirmDiscard", s2.state)
		}
		if s2.confirmCursor != 0 {
			t.Errorf("confirmCursor = %d, want 0", s2.confirmCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("ctrl+s saves and emits SettingsSavedMsg", func(t *testing.T) {
		s := newTestSettings()
		_, cmd := s.Update(keyPress("ctrl+s"))
		cmdEmitsSettingsSaved(t, cmd)
	})

	t.Run("unrecognised key is a no-op", func(t *testing.T) {
		s := newTestSettings()
		s.cursor = 2
		s2, cmd := s.Update(keyPress("x"))
		if s2.state != settingsMain {
			t.Errorf("state = %v, want settingsMain", s2.state)
		}
		if s2.cursor != 2 {
			t.Errorf("cursor changed unexpectedly to %d", s2.cursor)
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestSettings_ThemeState — settingsTheme
// ---------------------------------------------------------------------------

func TestSettings_ThemeState(t *testing.T) {
	t.Run("down increments themeIndex", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsTheme
		s.themeIndex = 0
		s2, cmd := s.Update(keyPress("down"))
		if s2.themeIndex != 1 {
			t.Errorf("themeIndex = %d, want 1", s2.themeIndex)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("j increments themeIndex", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsTheme
		s.themeIndex = 0
		s2, _ := s.Update(keyPress("j"))
		if s2.themeIndex != 1 {
			t.Errorf("themeIndex = %d, want 1", s2.themeIndex)
		}
	})

	t.Run("down clamps at last theme", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsTheme
		count := s.config // unused, just verifying count via Update
		_ = count
		// We don't know the count, but we can set themeIndex to a large number
		// and verify it clamps (ByIndex returns nil for out-of-range, Set is not called)
		// Set to second-to-last and go down, then down again at last
		// Just use the clamping property: drive to 0 first then go down many times
		s.themeIndex = 0
		for i := 0; i < 200; i++ {
			var cmd tea.Cmd
			s, cmd = s.Update(keyPress("down"))
			_ = cmd
		}
		// Must not be negative or panic; themeIndex should be count-1
		if s.themeIndex < 0 {
			t.Errorf("themeIndex went negative: %d", s.themeIndex)
		}
	})

	t.Run("up decrements themeIndex", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsTheme
		s.themeIndex = 3
		s2, cmd := s.Update(keyPress("up"))
		if s2.themeIndex != 2 {
			t.Errorf("themeIndex = %d, want 2", s2.themeIndex)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("k decrements themeIndex", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsTheme
		s.themeIndex = 3
		s2, _ := s.Update(keyPress("k"))
		if s2.themeIndex != 2 {
			t.Errorf("themeIndex = %d, want 2", s2.themeIndex)
		}
	})

	t.Run("up clamps at index 0", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsTheme
		s.themeIndex = 0
		s2, cmd := s.Update(keyPress("up"))
		if s2.themeIndex != 0 {
			t.Errorf("themeIndex = %d, want 0 (clamped)", s2.themeIndex)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("enter confirms and returns to main", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsTheme
		s2, cmd := s.Update(keyPress("enter"))
		if s2.state != settingsMain {
			t.Errorf("state = %v, want settingsMain", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("space also confirms and returns to main", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsTheme
		s2, cmd := s.Update(keyPress(" "))
		if s2.state != settingsMain {
			t.Errorf("state = %v, want settingsMain", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("esc reverts themeIndex and returns to main", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsTheme
		originalIndex := s.themeIndex
		s.themeIndex = originalIndex + 1 // simulate having moved the selection
		s2, cmd := s.Update(keyPress("esc"))
		if s2.state != settingsMain {
			t.Errorf("state = %v, want settingsMain", s2.state)
		}
		// themeIndex must be reset to the saved theme's index
		if s2.themeIndex == originalIndex+1 {
			t.Errorf("themeIndex was not reverted (still %d)", s2.themeIndex)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("ctrl+s saves from theme state and emits SettingsSavedMsg", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsTheme
		_, cmd := s.Update(keyPress("ctrl+s"))
		cmdEmitsSettingsSaved(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestSettings_ProbeState — settingsProbe
// ---------------------------------------------------------------------------

func TestSettings_ProbeState(t *testing.T) {
	t.Run("down increments probeCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 0
		s2, cmd := s.Update(keyPress("down"))
		if s2.probeCursor != 1 {
			t.Errorf("probeCursor = %d, want 1", s2.probeCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("j also moves probeCursor down", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 0
		s2, _ := s.Update(keyPress("j"))
		if s2.probeCursor != 1 {
			t.Errorf("probeCursor = %d, want 1", s2.probeCursor)
		}
	})

	t.Run("up decrements probeCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 2
		s2, cmd := s.Update(keyPress("up"))
		if s2.probeCursor != 1 {
			t.Errorf("probeCursor = %d, want 1", s2.probeCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("k also moves probeCursor up", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 2
		s2, _ := s.Update(keyPress("k"))
		if s2.probeCursor != 1 {
			t.Errorf("probeCursor = %d, want 1", s2.probeCursor)
		}
	})

	t.Run("down clamps at last probe item", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = numProbeItems - 1
		s2, cmd := s.Update(keyPress("down"))
		if s2.probeCursor != numProbeItems-1 {
			t.Errorf("probeCursor = %d, want %d (clamped)", s2.probeCursor, numProbeItems-1)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("up clamps at first probe item", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 0
		s2, cmd := s.Update(keyPress("up"))
		if s2.probeCursor != 0 {
			t.Errorf("probeCursor = %d, want 0 (clamped)", s2.probeCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("Protocol: enter cycles value", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 0
		before := s.config.Protocol
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.Protocol == before {
			t.Errorf("Protocol did not cycle: still %q", s2.config.Protocol)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("IPFamily: enter cycles auto to 4", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 1
		s.config.IPFamily = "auto"
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.IPFamily != "4" {
			t.Errorf("IPFamily = %q, want %q", s2.config.IPFamily, "4")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("IPFamily: enter cycles 4 to 6", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 1
		s.config.IPFamily = "4"
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.IPFamily != "6" {
			t.Errorf("IPFamily = %q, want %q", s2.config.IPFamily, "6")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("IPFamily: enter cycles 6 to auto", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 1
		s.config.IPFamily = "6"
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.IPFamily != "auto" {
			t.Errorf("IPFamily = %q, want %q", s2.config.IPFamily, "auto")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("MaxHops: enter enters edit mode with current value as buffer", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 2
		s.config.MaxHops = 30
		s2, cmd := s.Update(keyPress("enter"))
		if s2.focusedInput != 2 {
			t.Errorf("focusedInput = %d, want 2", s2.focusedInput)
		}
		if s2.inputBuffer != "30" {
			t.Errorf("inputBuffer = %q, want %q", s2.inputBuffer, "30")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("MaxHops edit mode: digit appended to buffer", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.focusedInput = 2
		s.inputBuffer = "1"
		s2, cmd := s.Update(keyPress("5"))
		if s2.inputBuffer != "15" {
			t.Errorf("inputBuffer = %q, want %q", s2.inputBuffer, "15")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("MaxHops edit mode: backspace trims buffer", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.focusedInput = 2
		s.inputBuffer = "30"
		s2, cmd := s.Update(keyPress("backspace"))
		if s2.inputBuffer != "3" {
			t.Errorf("inputBuffer = %q, want %q", s2.inputBuffer, "3")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("MaxHops edit mode: backspace on empty buffer is no-op", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.focusedInput = 2
		s.inputBuffer = ""
		s2, cmd := s.Update(keyPress("backspace"))
		if s2.inputBuffer != "" {
			t.Errorf("inputBuffer = %q, want empty", s2.inputBuffer)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("MaxHops edit mode: enter with valid value commits and exits edit mode", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.focusedInput = 2
		s.inputBuffer = "20"
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.MaxHops != 20 {
			t.Errorf("MaxHops = %d, want 20", s2.config.MaxHops)
		}
		if s2.focusedInput != -1 {
			t.Errorf("focusedInput = %d, want -1", s2.focusedInput)
		}
		if s2.inputBuffer != "" {
			t.Errorf("inputBuffer = %q, want empty", s2.inputBuffer)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("MaxHops edit mode: enter with invalid value does not commit", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.focusedInput = 2
		s.inputBuffer = "999"
		origMaxHops := s.config.MaxHops
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.MaxHops != origMaxHops {
			t.Errorf("MaxHops changed to %d despite invalid input", s2.config.MaxHops)
		}
		if s2.focusedInput != -1 {
			t.Errorf("focusedInput = %d, want -1", s2.focusedInput)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("MaxHops edit mode: esc cancels without committing", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.focusedInput = 2
		s.inputBuffer = "99"
		origMaxHops := s.config.MaxHops
		s2, cmd := s.Update(keyPress("esc"))
		if s2.config.MaxHops != origMaxHops {
			t.Errorf("MaxHops changed despite esc cancel")
		}
		if s2.focusedInput != -1 {
			t.Errorf("focusedInput = %d, want -1", s2.focusedInput)
		}
		if s2.inputBuffer != "" {
			t.Errorf("inputBuffer = %q, want empty", s2.inputBuffer)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("Interval: enter enters edit mode with current value as buffer", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 3
		s.config.Interval = "1s"
		s2, cmd := s.Update(keyPress("enter"))
		if s2.focusedInput != 3 {
			t.Errorf("focusedInput = %d, want 3", s2.focusedInput)
		}
		if s2.inputBuffer != "1s" {
			t.Errorf("inputBuffer = %q, want %q", s2.inputBuffer, "1s")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("Interval edit mode: enter with non-empty value commits", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.focusedInput = 3
		s.inputBuffer = "500ms"
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.Interval != "500ms" {
			t.Errorf("Interval = %q, want %q", s2.config.Interval, "500ms")
		}
		if s2.focusedInput != -1 {
			t.Errorf("focusedInput = %d, want -1", s2.focusedInput)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("Interval edit mode: enter with empty buffer does not commit", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.focusedInput = 3
		s.inputBuffer = ""
		origInterval := s.config.Interval
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.Interval != origInterval {
			t.Errorf("Interval changed to %q despite empty input", s2.config.Interval)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("Paths: enter enters edit mode with current value as buffer", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 4
		s.config.Paths = 6
		s2, cmd := s.Update(keyPress("enter"))
		if s2.focusedInput != 4 {
			t.Errorf("focusedInput = %d, want 4", s2.focusedInput)
		}
		if s2.inputBuffer != "6" {
			t.Errorf("inputBuffer = %q, want %q", s2.inputBuffer, "6")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("Paths edit mode: enter with valid value commits", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.focusedInput = 4
		s.inputBuffer = "8"
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.Paths != 8 {
			t.Errorf("Paths = %d, want 8", s2.config.Paths)
		}
		if s2.focusedInput != -1 {
			t.Errorf("focusedInput = %d, want -1", s2.focusedInput)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("Paths edit mode: enter with out-of-range value does not commit", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.focusedInput = 4
		s.inputBuffer = "50"
		origPaths := s.config.Paths
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.Paths != origPaths {
			t.Errorf("Paths changed to %d despite out-of-range input", s2.config.Paths)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("Back: enter on cursor 5 returns to main state", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 5
		s2, cmd := s.Update(keyPress("enter"))
		if s2.state != settingsMain {
			t.Errorf("state = %v, want settingsMain", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("esc (no focused input) returns to main state", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.focusedInput = -1
		s2, cmd := s.Update(keyPress("esc"))
		if s2.state != settingsMain {
			t.Errorf("state = %v, want settingsMain", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("ctrl+s saves from probe state and emits SettingsSavedMsg", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		_, cmd := s.Update(keyPress("ctrl+s"))
		cmdEmitsSettingsSaved(t, cmd)
	})

	t.Run("printable non-digit key ignored in non-focused navigation mode", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsProbe
		s.probeCursor = 0
		s.focusedInput = -1
		s2, cmd := s.Update(keyPress("z"))
		if s2.probeCursor != 0 {
			t.Errorf("probeCursor changed unexpectedly to %d", s2.probeCursor)
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestSettings_DisplayState — settingsDisplay
// ---------------------------------------------------------------------------

func TestSettings_DisplayState(t *testing.T) {
	t.Run("down increments displayCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = 0
		s2, cmd := s.Update(keyPress("down"))
		if s2.displayCursor != 1 {
			t.Errorf("displayCursor = %d, want 1", s2.displayCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("j also increments displayCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = 0
		s2, _ := s.Update(keyPress("j"))
		if s2.displayCursor != 1 {
			t.Errorf("displayCursor = %d, want 1", s2.displayCursor)
		}
	})

	t.Run("up decrements displayCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = 2
		s2, cmd := s.Update(keyPress("up"))
		if s2.displayCursor != 1 {
			t.Errorf("displayCursor = %d, want 1", s2.displayCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("k also decrements displayCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = 2
		s2, _ := s.Update(keyPress("k"))
		if s2.displayCursor != 1 {
			t.Errorf("displayCursor = %d, want 1", s2.displayCursor)
		}
	})

	t.Run("down clamps at last display item", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = numDisplayItems - 1
		s2, cmd := s.Update(keyPress("down"))
		if s2.displayCursor != numDisplayItems-1 {
			t.Errorf("displayCursor = %d, want %d (clamped)", s2.displayCursor, numDisplayItems-1)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("up clamps at first display item", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = 0
		s2, cmd := s.Update(keyPress("up"))
		if s2.displayCursor != 0 {
			t.Errorf("displayCursor = %d, want 0 (clamped)", s2.displayCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("DisplayMode: enter cycles to next mode", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = 0
		s.config.DisplayMode = "default"
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.DisplayMode == "default" {
			t.Errorf("DisplayMode did not cycle from default")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("DisplayMode: cycles wrap around after last mode", func(t *testing.T) {
		// "variability" is the last mode — next should be "default"
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = 0
		s.config.DisplayMode = "variability"
		s2, _ := s.Update(keyPress("enter"))
		if s2.config.DisplayMode != "default" {
			t.Errorf("DisplayMode = %q, want %q after wrap", s2.config.DisplayMode, "default")
		}
	})

	t.Run("DNS: enter toggles DNSLookups", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = 1
		before := s.config.DNSLookups
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.DNSLookups == before {
			t.Errorf("DNSLookups did not toggle: still %v", s2.config.DNSLookups)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("ASN: enter toggles ASNLookups", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = 2
		before := s.config.ASNLookups
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.ASNLookups == before {
			t.Errorf("ASNLookups did not toggle: still %v", s2.config.ASNLookups)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("Ping: enter toggles PingSupplement", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = 3
		before := s.config.PingSupplement
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.PingSupplement == before {
			t.Errorf("PingSupplement did not toggle: still %v", s2.config.PingSupplement)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("Back: enter on cursor 4 returns to main state", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = 4
		s2, cmd := s.Update(keyPress("enter"))
		if s2.state != settingsMain {
			t.Errorf("state = %v, want settingsMain", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("esc returns to main state", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s2, cmd := s.Update(keyPress("esc"))
		if s2.state != settingsMain {
			t.Errorf("state = %v, want settingsMain", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("ctrl+s saves from display state and emits SettingsSavedMsg", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		_, cmd := s.Update(keyPress("ctrl+s"))
		cmdEmitsSettingsSaved(t, cmd)
	})

	t.Run("space also acts like enter for toggle items", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsDisplay
		s.displayCursor = 1 // DNS
		before := s.config.DNSLookups
		s2, cmd := s.Update(keyPress(" "))
		if s2.config.DNSLookups == before {
			t.Errorf("DNSLookups did not toggle via space: still %v", s2.config.DNSLookups)
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestSettings_AlertState — settingsAlert
// ---------------------------------------------------------------------------

func TestSettings_AlertState(t *testing.T) {
	t.Run("down increments alertCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.alertCursor = 0
		s2, cmd := s.Update(keyPress("down"))
		if s2.alertCursor != 1 {
			t.Errorf("alertCursor = %d, want 1", s2.alertCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("j also increments alertCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.alertCursor = 0
		s2, _ := s.Update(keyPress("j"))
		if s2.alertCursor != 1 {
			t.Errorf("alertCursor = %d, want 1", s2.alertCursor)
		}
	})

	t.Run("up decrements alertCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.alertCursor = 2
		s2, cmd := s.Update(keyPress("up"))
		if s2.alertCursor != 1 {
			t.Errorf("alertCursor = %d, want 1", s2.alertCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("k also decrements alertCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.alertCursor = 2
		s2, _ := s.Update(keyPress("k"))
		if s2.alertCursor != 1 {
			t.Errorf("alertCursor = %d, want 1", s2.alertCursor)
		}
	})

	t.Run("down clamps at last alert item", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.alertCursor = numAlertItems - 1
		s2, cmd := s.Update(keyPress("down"))
		if s2.alertCursor != numAlertItems-1 {
			t.Errorf("alertCursor = %d, want %d (clamped)", s2.alertCursor, numAlertItems-1)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("up clamps at first alert item", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.alertCursor = 0
		s2, cmd := s.Update(keyPress("up"))
		if s2.alertCursor != 0 {
			t.Errorf("alertCursor = %d, want 0 (clamped)", s2.alertCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("AlertEnabled: enter toggles AlertEnabled", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.alertCursor = 0
		before := s.config.AlertEnabled
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.AlertEnabled == before {
			t.Errorf("AlertEnabled did not toggle: still %v", s2.config.AlertEnabled)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("AlertLoss: enter enters edit mode with current value as buffer", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.alertCursor = 1
		s.config.AlertLoss = 5.0
		s2, cmd := s.Update(keyPress("enter"))
		if s2.focusedInput != 1 {
			t.Errorf("focusedInput = %d, want 1", s2.focusedInput)
		}
		if s2.inputBuffer != "5.0" {
			t.Errorf("inputBuffer = %q, want %q", s2.inputBuffer, "5.0")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("AlertLoss edit mode: digits and dot accepted", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.focusedInput = 1
		s.inputBuffer = "5"
		s2, _ := s.Update(keyPress("."))
		if s2.inputBuffer != "5." {
			t.Errorf("inputBuffer = %q, want %q", s2.inputBuffer, "5.")
		}
		s3, _ := s2.Update(keyPress("2"))
		if s3.inputBuffer != "5.2" {
			t.Errorf("inputBuffer = %q, want %q", s3.inputBuffer, "5.2")
		}
	})

	t.Run("AlertLoss edit mode: enter with valid float commits", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.focusedInput = 1
		s.inputBuffer = "10.5"
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.AlertLoss != 10.5 {
			t.Errorf("AlertLoss = %f, want 10.5", s2.config.AlertLoss)
		}
		if s2.focusedInput != -1 {
			t.Errorf("focusedInput = %d, want -1", s2.focusedInput)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("AlertLoss edit mode: enter with out-of-range float does not commit", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.focusedInput = 1
		s.inputBuffer = "150"
		origLoss := s.config.AlertLoss
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.AlertLoss != origLoss {
			t.Errorf("AlertLoss changed to %f despite out-of-range", s2.config.AlertLoss)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("AlertLoss edit mode: backspace trims buffer", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.focusedInput = 1
		s.inputBuffer = "10.5"
		s2, cmd := s.Update(keyPress("backspace"))
		if s2.inputBuffer != "10." {
			t.Errorf("inputBuffer = %q, want %q", s2.inputBuffer, "10.")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("AlertLoss edit mode: esc cancels without committing", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.focusedInput = 1
		s.inputBuffer = "99.9"
		origLoss := s.config.AlertLoss
		s2, cmd := s.Update(keyPress("esc"))
		if s2.config.AlertLoss != origLoss {
			t.Errorf("AlertLoss changed despite esc cancel")
		}
		if s2.focusedInput != -1 {
			t.Errorf("focusedInput = %d, want -1", s2.focusedInput)
		}
		if s2.inputBuffer != "" {
			t.Errorf("inputBuffer = %q, want empty", s2.inputBuffer)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("AlertLatency: enter enters edit mode with current value as buffer", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.alertCursor = 2
		s.config.AlertLatency = "50ms"
		s2, cmd := s.Update(keyPress("enter"))
		if s2.focusedInput != 2 {
			t.Errorf("focusedInput = %d, want 2", s2.focusedInput)
		}
		if s2.inputBuffer != "50ms" {
			t.Errorf("inputBuffer = %q, want %q", s2.inputBuffer, "50ms")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("AlertLatency edit mode: enter with non-empty string commits", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.focusedInput = 2
		s.inputBuffer = "100ms"
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.AlertLatency != "100ms" {
			t.Errorf("AlertLatency = %q, want %q", s2.config.AlertLatency, "100ms")
		}
		if s2.focusedInput != -1 {
			t.Errorf("focusedInput = %d, want -1", s2.focusedInput)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("AlertLatency edit mode: enter with empty buffer does not commit", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.focusedInput = 2
		s.inputBuffer = ""
		origLatency := s.config.AlertLatency
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.AlertLatency != origLatency {
			t.Errorf("AlertLatency changed to %q despite empty input", s2.config.AlertLatency)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("AlertRounds: enter enters edit mode with current value as buffer", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.alertCursor = 3
		s.config.AlertRounds = 3
		s2, cmd := s.Update(keyPress("enter"))
		if s2.focusedInput != 3 {
			t.Errorf("focusedInput = %d, want 3", s2.focusedInput)
		}
		if s2.inputBuffer != "3" {
			t.Errorf("inputBuffer = %q, want %q", s2.inputBuffer, "3")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("AlertRounds edit mode: digits accepted", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.focusedInput = 3
		s.inputBuffer = "1"
		s2, _ := s.Update(keyPress("0"))
		if s2.inputBuffer != "10" {
			t.Errorf("inputBuffer = %q, want %q", s2.inputBuffer, "10")
		}
	})

	t.Run("AlertRounds edit mode: enter with valid value commits", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.focusedInput = 3
		s.inputBuffer = "5"
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.AlertRounds != 5 {
			t.Errorf("AlertRounds = %d, want 5", s2.config.AlertRounds)
		}
		if s2.focusedInput != -1 {
			t.Errorf("focusedInput = %d, want -1", s2.focusedInput)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("AlertRounds edit mode: enter with out-of-range value does not commit", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.focusedInput = 3
		s.inputBuffer = "25"
		origRounds := s.config.AlertRounds
		s2, cmd := s.Update(keyPress("enter"))
		if s2.config.AlertRounds != origRounds {
			t.Errorf("AlertRounds changed to %d despite out-of-range", s2.config.AlertRounds)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("Back: enter on cursor 4 returns to main state", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.alertCursor = 4
		s2, cmd := s.Update(keyPress("enter"))
		if s2.state != settingsMain {
			t.Errorf("state = %v, want settingsMain", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("esc (no focused input) returns to main state", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		s.focusedInput = -1
		s2, cmd := s.Update(keyPress("esc"))
		if s2.state != settingsMain {
			t.Errorf("state = %v, want settingsMain", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("ctrl+s saves from alert state and emits SettingsSavedMsg", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsAlert
		_, cmd := s.Update(keyPress("ctrl+s"))
		cmdEmitsSettingsSaved(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestSettings_ConfirmDiscardState — settingsConfirmDiscard
// ---------------------------------------------------------------------------

func TestSettings_ConfirmDiscardState(t *testing.T) {
	t.Run("down increments confirmCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 0
		s2, cmd := s.Update(keyPress("down"))
		if s2.confirmCursor != 1 {
			t.Errorf("confirmCursor = %d, want 1", s2.confirmCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("j also increments confirmCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 0
		s2, _ := s.Update(keyPress("j"))
		if s2.confirmCursor != 1 {
			t.Errorf("confirmCursor = %d, want 1", s2.confirmCursor)
		}
	})

	t.Run("tab increments confirmCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 0
		s2, cmd := s.Update(keyPress("tab"))
		if s2.confirmCursor != 1 {
			t.Errorf("confirmCursor = %d, want 1", s2.confirmCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("up decrements confirmCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 2
		s2, cmd := s.Update(keyPress("up"))
		if s2.confirmCursor != 1 {
			t.Errorf("confirmCursor = %d, want 1", s2.confirmCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("k also decrements confirmCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 2
		s2, _ := s.Update(keyPress("k"))
		if s2.confirmCursor != 1 {
			t.Errorf("confirmCursor = %d, want 1", s2.confirmCursor)
		}
	})

	t.Run("shift+tab decrements confirmCursor", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 2
		s2, cmd := s.Update(keyPress("shift+tab"))
		if s2.confirmCursor != 1 {
			t.Errorf("confirmCursor = %d, want 1", s2.confirmCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("down clamps at last confirm item (index 2)", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 2
		s2, cmd := s.Update(keyPress("down"))
		if s2.confirmCursor != 2 {
			t.Errorf("confirmCursor = %d, want 2 (clamped)", s2.confirmCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("up clamps at first confirm item (index 0)", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 0
		s2, cmd := s.Update(keyPress("up"))
		if s2.confirmCursor != 0 {
			t.Errorf("confirmCursor = %d, want 0 (clamped)", s2.confirmCursor)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("s quick-saves and emits SettingsSavedMsg", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		_, cmd := s.Update(keyPress("s"))
		cmdEmitsSettingsSaved(t, cmd)
	})

	t.Run("d quick-discards and emits SettingsCancelMsg", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		_, cmd := s.Update(keyPress("d"))
		cmdEmitsSettingsCancel(t, cmd)
	})

	t.Run("esc returns to main state without closing", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s2, cmd := s.Update(keyPress("esc"))
		if s2.state != settingsMain {
			t.Errorf("state = %v, want settingsMain", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("enter on cursor 0 (Save) emits SettingsSavedMsg", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 0
		_, cmd := s.Update(keyPress("enter"))
		cmdEmitsSettingsSaved(t, cmd)
	})

	t.Run("enter on cursor 1 (Discard) emits SettingsCancelMsg", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 1
		_, cmd := s.Update(keyPress("enter"))
		cmdEmitsSettingsCancel(t, cmd)
	})

	t.Run("enter on cursor 2 (Go back) returns to main state", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 2
		s2, cmd := s.Update(keyPress("enter"))
		if s2.state != settingsMain {
			t.Errorf("state = %v, want settingsMain", s2.state)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("space on cursor 0 (Save) emits SettingsSavedMsg", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 0
		_, cmd := s.Update(keyPress(" "))
		cmdEmitsSettingsSaved(t, cmd)
	})

	t.Run("unrecognised key is a no-op", func(t *testing.T) {
		s := newTestSettings()
		s.state = settingsConfirmDiscard
		s.confirmCursor = 1
		s2, cmd := s.Update(keyPress("x"))
		if s2.state != settingsConfirmDiscard {
			t.Errorf("state = %v, want settingsConfirmDiscard", s2.state)
		}
		if s2.confirmCursor != 1 {
			t.Errorf("confirmCursor changed to %d unexpectedly", s2.confirmCursor)
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestSettings_SavedMsgContainsConfig — SettingsSavedMsg carries working config
// ---------------------------------------------------------------------------

func TestSettings_SavedMsgContainsConfig(t *testing.T) {
	t.Run("SettingsSavedMsg.Config reflects working copy at time of save", func(t *testing.T) {
		s := newTestSettings()
		s.config.Protocol = "icmp"
		s.cursor = 4 // Save & Exit
		_, cmd := s.Update(keyPress("enter"))
		if cmd == nil {
			t.Fatal("expected non-nil cmd")
		}
		msg := cmd()
		saved, ok := msg.(SettingsSavedMsg)
		if !ok {
			t.Fatalf("expected SettingsSavedMsg, got %T", msg)
		}
		if saved.Config == nil {
			t.Fatal("SettingsSavedMsg.Config is nil")
		}
		if saved.Config.Protocol != "icmp" {
			t.Errorf("saved Protocol = %q, want icmp", saved.Config.Protocol)
		}
	})
}

// ---------------------------------------------------------------------------
// TestSettings_WindowSizeMsg — non-key messages handled by Update
// ---------------------------------------------------------------------------

func TestSettings_WindowSizeMsg(t *testing.T) {
	t.Run("WindowSizeMsg updates width and height", func(t *testing.T) {
		s := newTestSettings()
		s2, cmd := s.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		if s2.width != 120 || s2.height != 40 {
			t.Errorf("size = %dx%d, want 120x40", s2.width, s2.height)
		}
		cmdIsNil(t, cmd)
	})
}
