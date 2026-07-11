package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tonhe/viaduct/internal/config"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/ping"
)

// onSettingsSaved handles SettingsSavedMsg: closes the settings overlay and persists config.
func (m Model) onSettingsSaved(msg SettingsSavedMsg) (Model, tea.Cmd) {
	m.showSettings = false
	m.settings = nil
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
	r := msg.Result
	h := m.table.GetOrCreate(r.TTL)
	h.AddSample(r.IP, r.RTT, r.FlowID)
	m.probeCount++
	if r.IsTarget {
		m.targetHit = true
		if m.maxTTLHit == 0 || r.TTL < m.maxTTLHit {
			m.maxTTLHit = r.TTL
		}
	}
	// Submit IP for reverse DNS
	if m.dnsEnabled {
		m.resolver.Submit(r.IP)
	}
	return m, nil
}

// onHostname handles HostnameMsg: sets the resolved hostname on matching path nodes.
func (m Model) onHostname(msg HostnameMsg) (Model, tea.Cmd) {
	// Find PathNode(s) with matching IP and set hostname
	for _, h := range m.table.Snapshot() {
		for _, node := range h.GetNodes() {
			if ip := node.GetIP(); ip != nil && ip.Equal(msg.IP) {
				node.SetHostname(msg.Hostname)
			}
		}
	}
	return m, nil
}

// onASN handles ASNMsg: sets the ASN info on matching path nodes.
func (m Model) onASN(msg ASNMsg) (Model, tea.Cmd) {
	if m.enricher != nil {
		for _, h := range m.table.Snapshot() {
			for _, node := range h.GetNodes() {
				if ip := node.GetIP(); ip != nil && ip.Equal(msg.IP) {
					node.SetASN(msg.Number, msg.Org)
				}
			}
		}
	}
	return m, nil
}

// onPingUpdate handles PingUpdateMsg: records a ping reply or loss in per-IP stats.
func (m Model) onPingUpdate(msg PingUpdateMsg) (Model, tea.Cmd) {
	if m.pingStats == nil {
		return m, nil
	}
	key := msg.IP.String()
	stat, ok := m.pingStats[key]
	if !ok {
		stat = &ping.Stat{}
		m.pingStats[key] = stat
	}
	if msg.Lost {
		stat.MarkSent()
	} else {
		stat.AddReply(msg.RTT)
	}
	return m, nil
}

// onRoundEnd handles RoundEndMsg: marks round-end on all hops and updates the alert engine.
func (m Model) onRoundEnd(_ RoundEndMsg) (Model, tea.Cmd) {
	for _, h := range m.table.Snapshot() {
		h.MarkRoundEnd()
	}
	// Update alert engine with destination metrics
	if m.alertEngine != nil {
		hops := m.table.Snapshot()
		hopMap := make(map[int]*hop.Hop, len(hops))
		for _, h := range hops {
			hopMap[h.TTL] = h
		}
		maxTTL := m.table.MaxTTLSeen()
		if m.maxTTLHit > 0 && m.maxTTLHit < maxTTL {
			maxTTL = m.maxTTLHit
		}
		// Find destination hop
		var destLoss float64
		var destLatency time.Duration
		for i := maxTTL; i >= 1; i-- {
			h, ok := hopMap[i]
			if !ok || h == nil {
				continue
			}
			pn := h.PrimaryNode()
			if pn != nil && pn.GetReceived() > 0 {
				destLoss = pn.LossPercent()
				destLatency = pn.AvgRTT()
				break
			}
		}
		bell := m.alertEngine.Update(destLoss, destLatency)
		if bell && m.alertEngine.State() == AlertDegraded {
			deltas := computeDeltas(hopMap, maxTTL)
			m.alertEngine.FindAffectedHop(hopMap, maxTTL, deltas)
		}
		_ = bell // bell sound handled by terminal if needed
	}
	return m, nil
}

// onTick handles TickMsg: schedules the next tick.
func (m Model) onTick(_ TickMsg) (Model, tea.Cmd) {
	return m, tickCmd()
}

// onProtocolSwitch handles ProtocolSwitchMsg: records a transient status message.
func (m Model) onProtocolSwitch(msg ProtocolSwitchMsg) (Model, tea.Cmd) {
	m.switchStatusMsg = fmt.Sprintf("No UDP responses, switching to %s...", strings.ToUpper(msg.NewProtocol))
	m.switchStatusTime = time.Now()
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

// onProbeError handles ProbeErrorMsg: stores the error and quits the TUI.
func (m Model) onProbeError(msg ProbeErrorMsg) (Model, tea.Cmd) {
	m.err = msg.Err
	return m, tea.Quit
}
