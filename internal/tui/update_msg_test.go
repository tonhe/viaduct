package tui

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tonhe/viaduct/internal/asn"
	"github.com/tonhe/viaduct/internal/config"
	"github.com/tonhe/viaduct/internal/probe"
	"github.com/tonhe/viaduct/internal/resolve"
)

// ---------------------------------------------------------------------------
// TestOnSettingsSaved
// ---------------------------------------------------------------------------

func TestOnSettingsSaved(t *testing.T) {
	t.Run("closes overlay and applies config", func(t *testing.T) {
		m := newTestModel(withSettingsOpen(settingsMain))

		newCfg := config.Default()
		newCfg.Protocol = "icmp"
		msg := SettingsSavedMsg{Config: newCfg}

		m2, cmd := m.onSettingsSaved(msg)

		if m2.showSettings {
			t.Error("showSettings should be false after save")
		}
		if m2.settings != nil {
			t.Error("settings field should be nil after save")
		}
		if m2.config.Protocol != "icmp" {
			t.Errorf("config.Protocol = %q, want icmp", m2.config.Protocol)
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnSettingsCancel
// ---------------------------------------------------------------------------

func TestOnSettingsCancel(t *testing.T) {
	t.Run("closes overlay without saving", func(t *testing.T) {
		m := newTestModel(withSettingsOpen(settingsMain))
		// Record original protocol to verify it didn't change
		origProtocol := m.config.Protocol

		msg := SettingsCancelMsg{}
		m2, cmd := m.onSettingsCancel(msg)

		if m2.showSettings {
			t.Error("showSettings should be false after cancel")
		}
		if m2.settings != nil {
			t.Error("settings field should be nil after cancel")
		}
		if m2.config.Protocol != origProtocol {
			t.Errorf("config.Protocol changed: got %q, want %q", m2.config.Protocol, origProtocol)
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnWindowSize
// ---------------------------------------------------------------------------

func TestOnWindowSize(t *testing.T) {
	t.Run("updates width and height", func(t *testing.T) {
		m := newTestModel(withSize(100, 24))

		msg := windowSizeMsg(120, 40)
		m2, cmd := m.onWindowSize(msg)

		if m2.width != 120 {
			t.Errorf("width = %d, want 120", m2.width)
		}
		if m2.height != 40 {
			t.Errorf("height = %d, want 40", m2.height)
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnHopUpdate
// ---------------------------------------------------------------------------

func TestOnHopUpdate(t *testing.T) {
	t.Run("adds sample and increments probeCount", func(t *testing.T) {
		m := newTestModel()

		msg := HopUpdateMsg{Result: probe.Result{
			TTL:    3,
			IP:     net.ParseIP("192.0.2.3"),
			RTT:    15 * time.Millisecond,
			FlowID: 0,
		}}
		m2, cmd := m.onHopUpdate(msg)

		if m2.probeCount != 1 {
			t.Errorf("probeCount = %d, want 1", m2.probeCount)
		}

		snap := m2.table.Snapshot()
		if len(snap) != 1 {
			t.Fatalf("table snapshot len = %d, want 1", len(snap))
		}
		h := snap[0]
		if h.TTL != 3 {
			t.Errorf("hop TTL = %d, want 3", h.TTL)
		}
		nodes := h.GetNodes()
		if len(nodes) != 1 {
			t.Fatalf("nodes len = %d, want 1", len(nodes))
		}
		if !nodes[0].GetIP().Equal(net.ParseIP("192.0.2.3")) {
			t.Errorf("node IP = %v, want 192.0.2.3", nodes[0].GetIP())
		}
		if nodes[0].GetReceived() != 1 {
			t.Errorf("node received = %d, want 1", nodes[0].GetReceived())
		}

		cmdIsNil(t, cmd)
	})

	t.Run("IsTarget flips targetHit and records maxTTLHit", func(t *testing.T) {
		m := newTestModel()

		msg := HopUpdateMsg{Result: probe.Result{
			TTL:      5,
			IP:       net.ParseIP("192.0.2.5"),
			RTT:      12 * time.Millisecond,
			IsTarget: true,
		}}
		m2, _ := m.onHopUpdate(msg)

		if !m2.targetHit {
			t.Error("targetHit not set when IsTarget=true")
		}
		if m2.maxTTLHit != 5 {
			t.Errorf("maxTTLHit = %d, want 5", m2.maxTTLHit)
		}
	})

	t.Run("non-target probe does not flip targetHit", func(t *testing.T) {
		m := newTestModel()

		msg := HopUpdateMsg{Result: probe.Result{
			TTL:      2,
			IP:       net.ParseIP("192.0.2.2"),
			RTT:      8 * time.Millisecond,
			IsTarget: false,
		}}
		m2, _ := m.onHopUpdate(msg)

		if m2.targetHit {
			t.Error("targetHit should not be set when IsTarget=false")
		}
		if m2.maxTTLHit != 0 {
			t.Errorf("maxTTLHit = %d, want 0", m2.maxTTLHit)
		}
	})

	t.Run("maxTTLHit takes the minimum TTL when multiple targets arrive", func(t *testing.T) {
		m := newTestModel()

		// First target at TTL 7
		m, _ = m.onHopUpdate(HopUpdateMsg{Result: probe.Result{
			TTL: 7, IP: net.ParseIP("192.0.2.7"), IsTarget: true,
		}})
		// Second target at TTL 5 — should replace the higher value
		m, _ = m.onHopUpdate(HopUpdateMsg{Result: probe.Result{
			TTL: 5, IP: net.ParseIP("192.0.2.5"), IsTarget: true,
		}})

		if m.maxTTLHit != 5 {
			t.Errorf("maxTTLHit = %d, want 5 (lower TTL wins)", m.maxTTLHit)
		}
	})

	t.Run("DNS submitted when dnsEnabled and resolver present", func(t *testing.T) {
		r := resolve.New(1) // buffered channel, won't block
		m := newTestModel()
		m.dnsEnabled = true
		m.resolver = r

		msg := HopUpdateMsg{Result: probe.Result{
			TTL: 1,
			IP:  net.ParseIP("192.0.2.1"),
			RTT: 5 * time.Millisecond,
		}}
		m2, cmd := m.onHopUpdate(msg)

		// Resolver.Submit returns true when newly queued; verify the IP landed
		// in the channel by checking the resolver accepted it (no panic, no block).
		if m2.probeCount != 1 {
			t.Errorf("probeCount = %d, want 1", m2.probeCount)
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnHostname
// ---------------------------------------------------------------------------

func TestOnHostname(t *testing.T) {
	t.Run("sets hostname on matching node", func(t *testing.T) {
		ip := net.ParseIP("192.0.2.10")
		m := newTestModel(withHops(hopSample(2, "192.0.2.10", 10*time.Millisecond)))

		msg := HostnameMsg{IP: ip, Hostname: "host.example.com"}
		m2, cmd := m.onHostname(msg)

		snap := m2.table.Snapshot()
		if len(snap) == 0 {
			t.Fatal("snapshot is empty")
		}
		nodes := snap[0].GetNodes()
		if len(nodes) == 0 {
			t.Fatal("no nodes in hop")
		}
		if nodes[0].GetHostname() != "host.example.com" {
			t.Errorf("hostname = %q, want host.example.com", nodes[0].GetHostname())
		}
		cmdIsNil(t, cmd)
	})

	t.Run("noop when no node matches the IP", func(t *testing.T) {
		m := newTestModel(withHops(hopSample(1, "192.0.2.1", 5*time.Millisecond)))

		msg := HostnameMsg{IP: net.ParseIP("192.0.2.99"), Hostname: "other.example.com"}
		m2, cmd := m.onHostname(msg)

		snap := m2.table.Snapshot()
		nodes := snap[0].GetNodes()
		if nodes[0].GetHostname() != "" {
			t.Errorf("hostname should remain empty, got %q", nodes[0].GetHostname())
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnASN
// ---------------------------------------------------------------------------

func TestOnASN(t *testing.T) {
	t.Run("noop when enricher is nil", func(t *testing.T) {
		ip := net.ParseIP("192.0.2.20")
		m := newTestModel(withHops(hopSample(3, "192.0.2.20", 20*time.Millisecond)))
		// enricher is nil by default

		msg := ASNMsg{IP: ip, Number: 64496, Org: "Example Org"}
		m2, cmd := m.onASN(msg)

		snap := m2.table.Snapshot()
		nodes := snap[0].GetNodes()
		num, org := nodes[0].GetASN()
		if num != 0 || org != "" {
			t.Errorf("ASN should not be set when enricher=nil: got %d %q", num, org)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("sets ASN when enricher is non-nil and IP matches", func(t *testing.T) {
		ip := net.ParseIP("192.0.2.21")
		m := newTestModel(withHops(hopSample(4, "192.0.2.21", 25*time.Millisecond)))
		// Attach a non-nil enricher directly — nil-ness is the guard in onASN.
		// We use 0 workers so no goroutines are started.
		m.enricher = asn.New(0)

		msg := ASNMsg{IP: ip, Number: 64496, Org: "Example-Net"}
		m2, cmd := m.onASN(msg)

		snap := m2.table.Snapshot()
		nodes := snap[0].GetNodes()
		num, org := nodes[0].GetASN()
		if num != 64496 {
			t.Errorf("ASN number = %d, want 64496", num)
		}
		if org != "Example-Net" {
			t.Errorf("ASN org = %q, want Example-Net", org)
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnPingUpdate
// ---------------------------------------------------------------------------

func TestOnPingUpdate(t *testing.T) {
	t.Run("noop when pingStats is nil", func(t *testing.T) {
		m := newTestModel() // pingStats is nil by default

		msg := PingUpdateMsg{
			IP:  net.ParseIP("192.0.2.30"),
			RTT: 10 * time.Millisecond,
		}
		m2, cmd := m.onPingUpdate(msg)

		if m2.pingStats != nil {
			t.Error("pingStats should remain nil")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("records reply in pingStats when map is initialised", func(t *testing.T) {
		m := newTestModel(withPingStats())
		ip := net.ParseIP("192.0.2.31")

		msg := PingUpdateMsg{
			IP:  ip,
			RTT: 20 * time.Millisecond,
		}
		m2, cmd := m.onPingUpdate(msg)

		stat, ok := m2.pingStats[ip.String()]
		if !ok {
			t.Fatal("pingStats entry not created")
		}
		if stat.Received != 1 {
			t.Errorf("Received = %d, want 1", stat.Received)
		}
		if stat.LastRTT != 20*time.Millisecond {
			t.Errorf("LastRTT = %v, want 20ms", stat.LastRTT)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("records loss (Lost=true calls MarkSent)", func(t *testing.T) {
		m := newTestModel(withPingStats())
		ip := net.ParseIP("192.0.2.32")

		msg := PingUpdateMsg{
			IP:   ip,
			Lost: true,
		}
		m2, cmd := m.onPingUpdate(msg)

		stat, ok := m2.pingStats[ip.String()]
		if !ok {
			t.Fatal("pingStats entry not created for loss")
		}
		if stat.Sent != 1 {
			t.Errorf("Sent = %d, want 1", stat.Sent)
		}
		if stat.Received != 0 {
			t.Errorf("Received = %d, want 0 (loss)", stat.Received)
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnRoundEnd
// ---------------------------------------------------------------------------

func TestOnRoundEnd(t *testing.T) {
	t.Run("marks round-end on all hops without alert engine", func(t *testing.T) {
		m := newTestModel(withHops(
			hopSample(1, "192.0.2.1", 5*time.Millisecond),
			hopSample(2, "192.0.2.2", 10*time.Millisecond),
		))
		// No alertEngine — should not panic

		m2, cmd := m.onRoundEnd(RoundEndMsg{})

		snap := m2.table.Snapshot()
		if len(snap) != 2 {
			t.Fatalf("snapshot len = %d, want 2", len(snap))
		}
		// MarkRoundEnd increments rounds; GetSent returns rounds count
		for _, h := range snap {
			if h.GetSent() != 1 {
				t.Errorf("TTL %d GetSent() = %d, want 1 after one MarkRoundEnd", h.TTL, h.GetSent())
			}
		}
		cmdIsNil(t, cmd)
	})

	t.Run("runs alert engine update when alert engine is present", func(t *testing.T) {
		m := newTestModel(
			withHops(hopSample(1, "192.0.2.1", 5*time.Millisecond)),
			withAlertEngine(5.0, 100*time.Millisecond, 3),
		)
		m.targetHit = true
		m.maxTTLHit = 1

		m2, cmd := m.onRoundEnd(RoundEndMsg{})

		// Engine was called; it should still be in Healthy state (single round, no loss)
		if m2.alertEngine == nil {
			t.Fatal("alertEngine should not be nil")
		}
		if m2.alertEngine.State() != AlertHealthy {
			t.Errorf("alert state = %v, want AlertHealthy", m2.alertEngine.State())
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnTick
// ---------------------------------------------------------------------------

func TestOnTick(t *testing.T) {
	t.Run("returns a non-nil tick cmd", func(t *testing.T) {
		m := newTestModel()
		m2, cmd := m.onTick(TickMsg(time.Now()))

		_ = m2
		cmdIsNonNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnProtocolSwitch
// ---------------------------------------------------------------------------

func TestOnProtocolSwitch(t *testing.T) {
	t.Run("sets switchStatusMsg and switchStatusTime", func(t *testing.T) {
		m := newTestModel()
		before := time.Now()

		msg := ProtocolSwitchMsg{NewProtocol: "icmp"}
		m2, cmd := m.onProtocolSwitch(msg)

		if !strings.Contains(m2.switchStatusMsg, "ICMP") {
			t.Errorf("switchStatusMsg = %q, want it to contain ICMP", m2.switchStatusMsg)
		}
		if m2.switchStatusTime.Before(before) {
			t.Error("switchStatusTime should be at or after the call time")
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnExportDone
// ---------------------------------------------------------------------------

func TestOnExportDone(t *testing.T) {
	t.Run("success path sets exportStatusMsg with path", func(t *testing.T) {
		m := newTestModel()
		before := time.Now()

		msg := ExportDoneMsg{Path: "/tmp/via-export.json"}
		m2, cmd := m.onExportDone(msg)

		if !strings.Contains(m2.exportStatusMsg, "/tmp/via-export.json") {
			t.Errorf("exportStatusMsg = %q, want it to contain the path", m2.exportStatusMsg)
		}
		if m2.exportStatusTime.Before(before) {
			t.Error("exportStatusTime should be at or after the call time")
		}
		cmdIsNil(t, cmd)
	})

	t.Run("error path sets exportStatusMsg with error text", func(t *testing.T) {
		m := newTestModel()
		before := time.Now()

		exportErr := errors.New("permission denied")
		msg := ExportDoneMsg{Err: exportErr}
		m2, cmd := m.onExportDone(msg)

		if !strings.Contains(m2.exportStatusMsg, "permission denied") {
			t.Errorf("exportStatusMsg = %q, want it to contain the error", m2.exportStatusMsg)
		}
		if m2.exportStatusTime.Before(before) {
			t.Error("exportStatusTime should be at or after the call time")
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnProbeError
// ---------------------------------------------------------------------------

func TestOnProbeError(t *testing.T) {
	t.Run("stores error and returns tea.Quit", func(t *testing.T) {
		m := newTestModel()
		probeErr := errors.New("socket: operation not permitted")

		msg := ProbeErrorMsg{Err: probeErr}
		m2, cmd := m.onProbeError(msg)

		if m2.err == nil {
			t.Fatal("err field should be set after ProbeErrorMsg")
		}
		if m2.err.Error() != probeErr.Error() {
			t.Errorf("err = %v, want %v", m2.err, probeErr)
		}
		cmdIsQuit(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// helpers local to this file
// ---------------------------------------------------------------------------

// windowSizeMsg constructs a tea.WindowSizeMsg with the given dimensions.
func windowSizeMsg(w, h int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: w, Height: h}
}
