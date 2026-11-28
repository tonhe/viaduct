package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tonhe/viaduct/internal/config"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/probe"
)

// sessionAt returns the session at idx, or nil if idx is out of range.
// Handlers must check for nil and return early to avoid an index-out-of-bounds
// panic when a stale or malformed message arrives with a bad TargetIdx.
func (m Model) sessionAt(idx int) *TargetSession {
	if idx < 0 || idx >= len(m.sessions) {
		return nil
	}
	return m.sessions[idx]
}

// onSettingsSaved handles SettingsSavedMsg: closes the settings overlay and persists config.
func (m Model) onSettingsSaved(msg SettingsSavedMsg) (Model, tea.Cmd) {
	m.showSettings = false
	m.settings = nil
	m.viewMode = viewModeAfterSave(m.viewMode, m.config.DisplayMode, msg.Config.DisplayMode)
	*m.config = *msg.Config
	_ = config.Save(m.config)
	return m, nil
}

// onSettingsCancel handles SettingsCancelMsg: closes the settings overlay without saving.
func (m Model) onSettingsCancel(_ SettingsCancelMsg) (Model, tea.Cmd) {
	m.showSettings = false
	m.settings = nil
	return m, nil
}

// onWindowSize handles tea.WindowSizeMsg: records the new terminal dimensions.
func (m Model) onWindowSize(msg tea.WindowSizeMsg) (Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	return m, nil
}

// onHopUpdate handles HopUpdateMsg: records a probe result and submits the IP for DNS.
func (m Model) onHopUpdate(msg HopUpdateMsg) (Model, tea.Cmd) {
	s := m.sessionAt(msg.TargetIdx)
	if s == nil {
		return m, nil
	}
	r := msg.Result
	h := s.table.GetOrCreate(r.TTL)
	kind := hop.KindPrimary
	switch r.ProbeType {
	case probe.ProbeICMPTTL:
		kind = hop.KindICMPTTL
	case probe.ProbeICMPEcho:
		kind = hop.KindEcho
	}
	if !h.AddSample(r.IP, r.RTT, r.FlowID, kind) {
		// A reply to a probe sent before a reset: not counted.
		return m, nil
	}
	s.probeCount++
	s.table.NoteReply(r.TTL, r.IP, r.FlowID, kind, r.IsTarget)
	if r.IsTarget {
		s.targetHit = true
		// The destination's TTL is the table's to say (hop.Table.TargetTTL);
		// maxTTLHit is the session's copy, which the views read.
		s.maxTTLHit = s.table.TargetTTL()
	}
	// Submit IP for reverse DNS (skip for echo: the IP is already known/resolved)
	if m.dnsEnabled && kind != hop.KindEcho && m.dnsSub != nil {
		m.dnsSub.Submit(r.IP)
	}
	return m, nil
}

// onHostname handles HostnameMsg: sets the resolved hostname on matching path nodes
// across all sessions (a shared first-hop IP may appear in every table).
func (m Model) onHostname(msg HostnameMsg) (Model, tea.Cmd) {
	for _, s := range m.sessions {
		for _, h := range s.table.Snapshot() {
			for _, node := range h.GetNodes() {
				if ip := node.GetIP(); ip != nil && ip.Equal(msg.IP) {
					node.SetHostname(msg.Hostname)
				}
			}
		}
	}
	return m, nil
}

// onASN handles ASNMsg: sets the ASN info on matching path nodes across all sessions.
func (m Model) onASN(msg ASNMsg) (Model, tea.Cmd) {
	if m.enricher != nil {
		for _, s := range m.sessions {
			for _, h := range s.table.Snapshot() {
				for _, node := range h.GetNodes() {
					if ip := node.GetIP(); ip != nil && ip.Equal(msg.IP) {
						node.SetASN(msg.Number, msg.Org)
					}
				}
			}
		}
	}
	return m, nil
}

// onEchoSent attributes an echo send to the exact {IP, TTL} node.
// Uses Get to avoid fabricating a hop row for an unknown TTL.
func (m Model) onEchoSent(msg EchoSentMsg) (Model, tea.Cmd) {
	s := m.sessionAt(msg.TargetIdx)
	if s == nil {
		return m, nil
	}
	h := s.table.Get(msg.TTL)
	if h == nil {
		return m, nil
	}
	for _, node := range h.GetNodes() {
		if ip := node.GetIP(); ip != nil && ip.Equal(msg.IP) {
			node.MarkEchoSent()
			break
		}
	}
	return m, nil
}

// onRoundEnd handles RoundEndMsg: marks round-end on all hops and updates the alert engine.
func (m Model) onRoundEnd(msg RoundEndMsg) (Model, tea.Cmd) {
	s := m.sessionAt(msg.TargetIdx)
	if s == nil {
		return m, nil
	}
	// The TTLs probes were sent to in the round being closed.
	sentTo := CloseRound(s.table)
	// The round may have moved the destination (hop.Table.TargetTTL).
	if ttl := s.table.TargetTTL(); ttl > 0 {
		s.maxTTLHit = ttl
	}
	// Collect {IP, TTL} pairs and submit to the echo sink for next-round echo probing.
	if s.echoSink != nil {
		// The nodes of the path, up to and including the destination's hop
		// (see hop.Table.EchoNodes for why later hops are left out).
		var pairs []probe.EchoTarget
		for _, n := range s.table.EchoNodes() {
			pairs = append(pairs, probe.EchoTarget{IP: n.IP, TTL: n.TTL})
		}
		s.echoSink.SubmitEchoTargets(pairs)
	}
	// Update alert engine with destination metrics
	s.alertEngine.ObserveRound(s.table.Snapshot(), sentTo, alertMaxTTL(s.table, s.maxTTLHit))
	s.debug.RoundClosed(s.table, sentTo)
	return m, nil
}

// onTick handles TickMsg: schedules the next tick.
func (m Model) onTick(_ TickMsg) (Model, tea.Cmd) {
	return m, tickCmd()
}

// onProtocolSwitch handles ProtocolSwitchMsg: records a transient status message.
func (m Model) onProtocolSwitch(msg ProtocolSwitchMsg) (Model, tea.Cmd) {
	s := m.sessionAt(msg.TargetIdx)
	if s == nil {
		return m, nil
	}
	s.switchStatusMsg = fmt.Sprintf("No UDP responses, switching to %s...", strings.ToUpper(msg.NewProtocol))
	s.switchStatusTime = time.Now()
	return m, nil
}

// onExportDone handles ExportDoneMsg: records the export status message.
func (m Model) onExportDone(msg ExportDoneMsg) (Model, tea.Cmd) {
	if msg.Err != nil {
		m.exportStatusMsg = fmt.Sprintf("Export failed: %v", msg.Err)
	} else {
		m.exportStatusMsg = fmt.Sprintf("Exported to %s", msg.Path)
	}
	m.exportStatusTime = time.Now()
	return m, nil
}

// onProbeError handles ProbeErrorMsg. A run error belongs to one target: with
// several targets that target's row shows the error and the others keep
// tracing, and the session ends only when no target is left. With a single
// target the error ends the session.
func (m Model) onProbeError(msg ProbeErrorMsg) (Model, tea.Cmd) {
	if len(m.sessions) <= 1 {
		m.err = msg.Err
		return m, tea.Quit
	}
	s := m.sessionAt(msg.TargetIdx)
	if s == nil {
		return m, nil
	}
	s.startErr = msg.Err
	for _, other := range m.sessions {
		if other.startErr == nil {
			return m, nil
		}
	}
	m.err = msg.Err
	return m, tea.Quit
}
