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
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		s := NewSettings(m.config, m.width, m.height)
		m.settings = &s
		m.showSettings = true
	case "p":
		m.paused = !m.paused
		if m.tracer != nil {
			if m.paused {
				atomic.StoreInt32(&m.tracer.Paused, 1)
			} else {
				atomic.StoreInt32(&m.tracer.Paused, 0)
			}
		}
	case "r":
		m.table.ResetAll()
		m.probeCount = 0
		m.startTime = time.Now()
		if m.alertEngine != nil {
			m.alertEngine.Reset()
		}
		for k := range m.pingStats {
			delete(m.pingStats, k)
		}
	case "d":
		m.viewMode = m.viewMode.Next()
	case "j", "down":
		if m.scrollOffset > 0 {
			m.scrollOffset--
		}
		m.autoScroll = m.scrollOffset == 0
	case "k", "up":
		maxOff := m.maxScrollOffset()
		if m.scrollOffset < maxOff {
			m.scrollOffset++
		}
		m.autoScroll = false
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
