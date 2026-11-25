package tui

import (
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// onKey handles tea.KeyMsg: delegates to the settings overlay when it is open,
// otherwise dispatches to onMainKey for the main-view key bindings.
func (m Model) onKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	// When settings overlay is open, delegate ALL key events to it.
	if m.showSettings && m.settings != nil {
		newSettings, cmd := m.settings.Update(msg)
		m.settings = &newSettings
		return m, cmd
	}
	return m.onMainKey(msg)
}

// onMainKey handles key events when the main view is active (settings closed).
func (m Model) onMainKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	// Column view does not scroll. The scroll keys must not move the hidden
	// hop table, or it comes back scrolled when column view is left.
	if len(m.sessions) > 1 && m.columnView {
		switch msg.String() {
		case "j", "k", "g", "G":
			return m, nil
		}
	}
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		ns := NewSettings(m.config, m.width, m.height)
		m.settings = &ns
		m.showSettings = true
	case "p":
		m.paused = !m.paused
		paused := int32(0)
		if m.paused {
			paused = 1
		}
		for _, s := range m.sessions {
			if s.tracer != nil {
				atomic.StoreInt32(&s.tracer.Paused, paused)
			}
		}
	case "r":
		m.startTime = time.Now()
		for _, s := range m.sessions {
			s.table.ResetAll()
			s.probeCount = 0
			if s.alertEngine != nil {
				s.alertEngine.Reset()
			}
		}
	case "d":
		m.viewMode = m.viewMode.Next()
	case "j":
		if m.scrollOffset > 0 {
			m.scrollOffset--
		}
		m.autoScroll = m.scrollOffset == 0
	case "down":
		if len(m.sessions) > 1 {
			if m.focusedIdx < len(m.sessions)-1 {
				m.focusedIdx++
			}
			m.clampScrollToFocus()
		} else {
			if m.scrollOffset > 0 {
				m.scrollOffset--
			}
			m.autoScroll = m.scrollOffset == 0
		}
	case "k":
		maxOff := m.maxScrollOffset()
		if m.scrollOffset < maxOff {
			m.scrollOffset++
		}
		m.autoScroll = false
	case "up":
		if len(m.sessions) > 1 {
			if m.focusedIdx > 0 {
				m.focusedIdx--
			}
			m.clampScrollToFocus()
		} else {
			maxOff := m.maxScrollOffset()
			if m.scrollOffset < maxOff {
				m.scrollOffset++
			}
			m.autoScroll = false
		}
	case "tab":
		if len(m.sessions) > 1 {
			m.focusedIdx = (m.focusedIdx + 1) % len(m.sessions)
			m.clampScrollToFocus()
		}
	case " ":
		if len(m.sessions) > 1 {
			m.focused().marked = !m.focused().marked
		}
	case "c":
		if len(m.sessions) > 1 {
			m.columnView = !m.columnView
		}
	case "G":
		m.scrollOffset = 0
		m.autoScroll = true
	case "g":
		m.autoScroll = false
		m.scrollOffset = m.maxScrollOffset()
	case "e":
		return m, m.exportCmd()
	}
	return m, nil
}

// clampScrollToFocus clamps m.scrollOffset to the maximum valid offset for the
// newly focused session and keeps autoScroll consistent (true iff offset is 0).
// Call this after any change to m.focusedIdx.
func (m *Model) clampScrollToFocus() {
	maxOff := m.maxScrollOffset()
	if m.scrollOffset > maxOff {
		m.scrollOffset = maxOff
	}
	m.autoScroll = m.scrollOffset == 0
}
