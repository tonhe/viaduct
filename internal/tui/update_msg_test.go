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
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/probe"
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

		if m2.sessions[0].probeCount != 1 {
			t.Errorf("probeCount = %d, want 1", m2.sessions[0].probeCount)
		}

		snap := m2.sessions[0].table.Snapshot()
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

		if !m2.sessions[0].targetHit {
			t.Error("targetHit not set when IsTarget=true")
		}
		if m2.sessions[0].maxTTLHit != 5 {
			t.Errorf("maxTTLHit = %d, want 5", m2.sessions[0].maxTTLHit)
		}
	})

	t.Run("IsTarget marks the node as the destination", func(t *testing.T) {
		m := newTestModel()
		ip := net.ParseIP("192.0.2.5")
		m2, _ := m.onHopUpdate(HopUpdateMsg{Result: probe.Result{
			TTL: 5, IP: ip, RTT: time.Millisecond, IsTarget: true, ProbeType: probe.ProbeICMPTTL,
		}})

		pn := m2.sessions[0].table.GetOrCreate(5).PrimaryNode()
		if pn == nil {
			t.Fatal("no node at TTL 5")
		}
		if !pn.IsTarget() {
			t.Error("destination node is not marked as the target")
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

		if m2.sessions[0].targetHit {
			t.Error("targetHit should not be set when IsTarget=false")
		}
		if m2.sessions[0].maxTTLHit != 0 {
			t.Errorf("maxTTLHit = %d, want 0", m2.sessions[0].maxTTLHit)
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

		if m.sessions[0].maxTTLHit != 5 {
			t.Errorf("maxTTLHit = %d, want 5 (lower TTL wins)", m.sessions[0].maxTTLHit)
		}
	})

	t.Run("DNS submitted when dnsEnabled and resolver present", func(t *testing.T) {
		rec := &recordingSubmitter{}
		m := newTestModel()
		m.dnsEnabled = true
		m.dnsSub = rec

		ip := net.ParseIP("192.0.2.1")
		msg := HopUpdateMsg{Result: probe.Result{
			TTL: 1,
			IP:  ip,
			RTT: 5 * time.Millisecond,
		}}
		m2, cmd := m.onHopUpdate(msg)

		if m2.sessions[0].probeCount != 1 {
			t.Errorf("probeCount = %d, want 1", m2.sessions[0].probeCount)
		}
		if len(rec.submitted) != 1 || !rec.submitted[0].Equal(ip) {
			t.Errorf("Submit called with %v, want [%s]", rec.submitted, ip)
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

		snap := m2.sessions[0].table.Snapshot()
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

		snap := m2.sessions[0].table.Snapshot()
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

		snap := m2.sessions[0].table.Snapshot()
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

		snap := m2.sessions[0].table.Snapshot()
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
// TestOnRoundEnd
// ---------------------------------------------------------------------------

func TestOnRoundEnd(t *testing.T) {
	t.Run("marks round-end on all hops without alert engine", func(t *testing.T) {
		m := newTestModel(withHops(
			hopSample(1, "192.0.2.1", 5*time.Millisecond),
			hopSample(2, "192.0.2.2", 10*time.Millisecond),
		))
		// No alertEngine — should not panic

		// Simulate the tracer calling MarkRoundSent for each TTL first.
		for _, h := range m.sessions[0].table.Snapshot() {
			h.MarkRoundSent()
		}

		m2, cmd := m.onRoundEnd(RoundEndMsg{})

		snap := m2.sessions[0].table.Snapshot()
		if len(snap) != 2 {
			t.Fatalf("snapshot len = %d, want 2", len(snap))
		}
		// MarkRoundSent increments roundsSent; Rounds() returns that count.
		for _, h := range snap {
			if h.Rounds() != 1 {
				t.Errorf("TTL %d Rounds() = %d, want 1 after one MarkRoundSent+MarkRoundEnd", h.TTL, h.Rounds())
			}
		}
		// After MarkRoundEnd with one received sample, loss should be 0%.
		for _, h := range snap {
			if h.LossPercent() != 0 {
				t.Errorf("TTL %d LossPercent() = %.1f%%, want 0 (response arrived this round)", h.TTL, h.LossPercent())
			}
		}
		cmdIsNil(t, cmd)
	})

	t.Run("runs alert engine update when alert engine is present", func(t *testing.T) {
		m := newTestModel(
			withHops(hopSample(1, "192.0.2.1", 5*time.Millisecond)),
			withAlertEngine(5.0, 100*time.Millisecond, 3),
		)
		m.sessions[0].targetHit = true
		m.sessions[0].maxTTLHit = 1

		m2, cmd := m.onRoundEnd(RoundEndMsg{})

		// Engine was called; it should still be in Healthy state (single round, no loss)
		if m2.sessions[0].alertEngine == nil {
			t.Fatal("alertEngine should not be nil")
		}
		if m2.sessions[0].alertEngine.State() != AlertHealthy {
			t.Errorf("alert state = %v, want AlertHealthy", m2.sessions[0].alertEngine.State())
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

		if !strings.Contains(m2.sessions[0].switchStatusMsg, "ICMP") {
			t.Errorf("switchStatusMsg = %q, want it to contain ICMP", m2.sessions[0].switchStatusMsg)
		}
		if m2.sessions[0].switchStatusTime.Before(before) {
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
// TestUpdate_RoutesByTargetIdx / TestUpdate_TargetIdxZeroDefault
// ---------------------------------------------------------------------------

func TestUpdate_RoutesByTargetIdx(t *testing.T) {
	m := newTestModel()
	// second session, distinct target
	cfg := m.sessions[0].probeCfg
	m.sessions = append(m.sessions, newSession("second.example", net.ParseIP("198.51.100.2"), cfg, "udp", 5.0, 0, 3, true))

	res := probe.Result{TTL: 1, IP: net.ParseIP("203.0.113.9"), RTT: 5 * time.Millisecond}
	next, _ := m.Update(HopUpdateMsg{TargetIdx: 1, Result: res})
	m2 := next.(Model)

	if got := len(m2.sessions[0].table.Snapshot()); got != 0 {
		t.Fatalf("session 0 table has %d hops, want 0", got)
	}
	if got := len(m2.sessions[1].table.Snapshot()); got != 1 {
		t.Fatalf("session 1 table has %d hops, want 1", got)
	}
}

func TestUpdate_TargetIdxZeroDefault(t *testing.T) {
	m := newTestModel()
	res := probe.Result{TTL: 1, IP: net.ParseIP("203.0.113.9"), RTT: 5 * time.Millisecond}
	next, _ := m.Update(HopUpdateMsg{Result: res}) // no TargetIdx set (defaults to 0)
	m2 := next.(Model)
	if got := len(m2.sessions[0].table.Snapshot()); got != 1 {
		t.Fatalf("session 0 table has %d hops, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// TestUpdate_HostnameASNFanOut
// ---------------------------------------------------------------------------

func TestUpdate_HostnameASNFanOut(t *testing.T) {
	sharedIP := net.ParseIP("203.0.113.50")

	// Build two sessions each with the shared IP at TTL 1.
	m := newTestModel()
	m.sessions[0].table.GetOrCreate(1).AddSample(sharedIP, 5*time.Millisecond, 0, hop.KindPrimary)
	cfg := m.sessions[0].probeCfg
	sess1 := newSession("second.example", net.ParseIP("198.51.100.2"), cfg, "udp", 5.0, 0, 3, true)
	sess1.table.GetOrCreate(1).AddSample(sharedIP, 8*time.Millisecond, 0, hop.KindPrimary)
	m.sessions = append(m.sessions, sess1)

	t.Run("hostname fans out to all sessions", func(t *testing.T) {
		m2, _ := m.onHostname(HostnameMsg{IP: sharedIP, Hostname: "shared.example.com"})

		for i, s := range m2.sessions {
			snap := s.table.Snapshot()
			if len(snap) == 0 {
				t.Fatalf("session %d snapshot is empty", i)
			}
			nodes := snap[0].GetNodes()
			if len(nodes) == 0 {
				t.Fatalf("session %d has no nodes", i)
			}
			if got := nodes[0].GetHostname(); got != "shared.example.com" {
				t.Errorf("session %d hostname = %q, want shared.example.com", i, got)
			}
		}
	})

	t.Run("ASN fans out to all sessions", func(t *testing.T) {
		// Attach a zero-worker enricher so the nil-guard in onASN passes.
		m3 := m
		m3.enricher = asn.New(0)

		m4, _ := m3.onASN(ASNMsg{IP: sharedIP, Number: 64500, Org: "FanOut-Net"})

		for i, s := range m4.sessions {
			snap := s.table.Snapshot()
			if len(snap) == 0 {
				t.Fatalf("session %d snapshot is empty", i)
			}
			nodes := snap[0].GetNodes()
			if len(nodes) == 0 {
				t.Fatalf("session %d has no nodes", i)
			}
			num, org := nodes[0].GetASN()
			if num != 64500 {
				t.Errorf("session %d ASN number = %d, want 64500", i, num)
			}
			if org != "FanOut-Net" {
				t.Errorf("session %d ASN org = %q, want FanOut-Net", i, org)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// TestOnHopUpdate_OutOfRangeTargetIdx
// ---------------------------------------------------------------------------

// TestOnHopUpdate_OutOfRangeTargetIdx verifies that out-of-range TargetIdx
// values are dropped without panicking and without modifying the model.
func TestOnHopUpdate_OutOfRangeTargetIdx(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		targetIdx int
	}{
		{name: "negative", targetIdx: -1},
		{name: "equal_to_len", targetIdx: 1}, // model has exactly 1 session (index 0)
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestModel() // exactly one session at index 0

			msg := HopUpdateMsg{
				TargetIdx: tc.targetIdx,
				Result:    probe.Result{TTL: 1, IP: net.ParseIP("192.0.2.1"), RTT: 5 * time.Millisecond},
			}

			// Must not panic.
			m2, cmd := m.onHopUpdate(msg)

			// Model must be unchanged: session 0 probeCount stays 0.
			if m2.sessions[0].probeCount != 0 {
				t.Errorf("probeCount = %d, want 0 (model should be unchanged)", m2.sessions[0].probeCount)
			}
			if len(m2.sessions[0].table.Snapshot()) != 0 {
				t.Error("hop table should remain empty")
			}
			cmdIsNil(t, cmd)
		})
	}
}

// ---------------------------------------------------------------------------
// TestOnHopUpdateRoutesProbeKind
// ---------------------------------------------------------------------------

// TestOnHopUpdateRoutesProbeKind verifies that HopUpdateMsg.Result.ProbeType
// routes samples to the correct stats slot:
//   - ProbeICMPEcho  → node.EchoStats().Received (not aggregates)
//   - ProbeUDPTTL    → node.PrimaryStats().Received
//   - ProbeICMPTTL   → node.ICMPTTLStats().Received
func TestOnHopUpdateRoutesProbeKind(t *testing.T) {
	t.Run("ProbeICMPEcho lands in EchoStats only", func(t *testing.T) {
		m := newTestModel()
		msg := HopUpdateMsg{Result: probe.Result{
			TTL:       2,
			IP:        net.ParseIP("192.0.2.2"),
			RTT:       10 * time.Millisecond,
			ProbeType: probe.ProbeICMPEcho,
		}}
		m2, _ := m.onHopUpdate(msg)
		snap := m2.sessions[0].table.Snapshot()
		if len(snap) != 1 {
			t.Fatalf("snapshot len = %d, want 1", len(snap))
		}
		nodes := snap[0].GetNodes()
		if len(nodes) != 1 {
			t.Fatalf("nodes len = %d, want 1", len(nodes))
		}
		n := nodes[0]
		if got := n.EchoStats().Received; got != 1 {
			t.Errorf("EchoStats.Received = %d, want 1", got)
		}
		if got := n.PrimaryStats().Received; got != 0 {
			t.Errorf("PrimaryStats.Received = %d, want 0 (echo must not land in primary)", got)
		}
		if got := n.ICMPTTLStats().Received; got != 0 {
			t.Errorf("ICMPTTLStats.Received = %d, want 0 (echo must not land in icmpttl)", got)
		}
	})

	t.Run("ProbeUDPTTL lands in PrimaryStats", func(t *testing.T) {
		m := newTestModel()
		msg := HopUpdateMsg{Result: probe.Result{
			TTL:       3,
			IP:        net.ParseIP("192.0.2.3"),
			RTT:       15 * time.Millisecond,
			ProbeType: probe.ProbeUDPTTL,
		}}
		m2, _ := m.onHopUpdate(msg)
		snap := m2.sessions[0].table.Snapshot()
		nodes := snap[0].GetNodes()
		n := nodes[0]
		if got := n.PrimaryStats().Received; got != 1 {
			t.Errorf("PrimaryStats.Received = %d, want 1", got)
		}
		if got := n.ICMPTTLStats().Received; got != 0 {
			t.Errorf("ICMPTTLStats.Received = %d, want 0", got)
		}
		if got := n.EchoStats().Received; got != 0 {
			t.Errorf("EchoStats.Received = %d, want 0", got)
		}
	})

	t.Run("ProbeICMPTTL lands in ICMPTTLStats", func(t *testing.T) {
		m := newTestModel()
		msg := HopUpdateMsg{Result: probe.Result{
			TTL:       4,
			IP:        net.ParseIP("192.0.2.4"),
			RTT:       20 * time.Millisecond,
			ProbeType: probe.ProbeICMPTTL,
		}}
		m2, _ := m.onHopUpdate(msg)
		snap := m2.sessions[0].table.Snapshot()
		nodes := snap[0].GetNodes()
		n := nodes[0]
		if got := n.ICMPTTLStats().Received; got != 1 {
			t.Errorf("ICMPTTLStats.Received = %d, want 1", got)
		}
		if got := n.PrimaryStats().Received; got != 0 {
			t.Errorf("PrimaryStats.Received = %d, want 0", got)
		}
		if got := n.EchoStats().Received; got != 0 {
			t.Errorf("EchoStats.Received = %d, want 0", got)
		}
	})

	t.Run("ProbeICMPEcho does not submit DNS", func(t *testing.T) {
		rec := &recordingSubmitter{}
		m := newTestModel()
		m.dnsEnabled = true
		m.dnsSub = rec

		// Echo probe: Submit must NOT be called.
		echoMsg := HopUpdateMsg{Result: probe.Result{
			TTL:       5,
			IP:        net.ParseIP("192.0.2.5"),
			RTT:       5 * time.Millisecond,
			ProbeType: probe.ProbeICMPEcho,
		}}
		m.onHopUpdate(echoMsg)

		if len(rec.submitted) != 0 {
			t.Errorf("echo probe: Submit called %d time(s), want 0; IPs: %v", len(rec.submitted), rec.submitted)
		}

		// Sanity: a non-echo probe on the same model DOES submit.
		udpMsg := HopUpdateMsg{Result: probe.Result{
			TTL:       5,
			IP:        net.ParseIP("192.0.2.5"),
			RTT:       5 * time.Millisecond,
			ProbeType: probe.ProbeUDPTTL,
		}}
		m.onHopUpdate(udpMsg)

		if len(rec.submitted) != 1 {
			t.Errorf("udp probe: Submit called %d time(s), want 1; IPs: %v", len(rec.submitted), rec.submitted)
		}
	})
}

// ---------------------------------------------------------------------------
// TestOnEchoSent
// ---------------------------------------------------------------------------

// recordingSubmitter records every IP passed to Submit, satisfying dnsSubmitter.
type recordingSubmitter struct {
	submitted []net.IP
}

func (r *recordingSubmitter) Submit(ip net.IP) bool {
	r.submitted = append(r.submitted, ip)
	return true
}

// fakeEchoSink records SubmitEchoTargets calls for testing.
type fakeEchoSink struct {
	submitted []probe.EchoTarget
}

func (f *fakeEchoSink) SubmitEchoTargets(targets []probe.EchoTarget) {
	f.submitted = append(f.submitted, targets...)
}

// Hops beyond the destination are not echo targets: the destination is
// recorded there only because round one probes every TTL.
func TestOnRoundEnd_NoEchoTargetsBeyondTheDestination(t *testing.T) {
	m := newTestModel()
	sink := &fakeEchoSink{}
	m.sessions[0].echoSink = sink
	tbl := m.sessions[0].table
	dst := net.ParseIP("198.51.100.1")
	tbl.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), time.Millisecond, 0, hop.KindPrimary)
	for ttl := 2; ttl <= 6; ttl++ {
		h := tbl.GetOrCreate(ttl)
		h.AddSample(dst, time.Millisecond, 0, hop.KindICMPTTL)
		h.MarkTarget(dst)
	}

	m.onRoundEnd(RoundEndMsg{TargetIdx: 0})

	if len(sink.submitted) != 2 {
		t.Fatalf("submitted %d echo targets, want 2 (the router and the destination once): %v", len(sink.submitted), sink.submitted)
	}
	for _, tgt := range sink.submitted {
		if tgt.TTL > 2 {
			t.Errorf("echo target %v at TTL %d is beyond the destination at TTL 2", tgt.IP, tgt.TTL)
		}
	}
}

// TestOnEchoSent verifies EchoSentMsg attribution to per-node EchoStats.Sent.
func TestOnEchoSent(t *testing.T) {
	t.Run("known IP and TTL increments EchoStats.Sent on matching node", func(t *testing.T) {
		ip := net.ParseIP("192.0.2.10")
		m := newTestModel(withHops(hopSample(3, "192.0.2.10", 10*time.Millisecond)))

		msg := EchoSentMsg{TargetIdx: 0, IP: ip, TTL: 3}
		m2, cmd := m.onEchoSent(msg)

		snap := m2.sessions[0].table.Snapshot()
		if len(snap) == 0 {
			t.Fatal("snapshot is empty")
		}
		nodes := snap[0].GetNodes()
		if len(nodes) == 0 {
			t.Fatal("no nodes")
		}
		if got := nodes[0].EchoStats().Sent; got != 1 {
			t.Errorf("EchoStats.Sent = %d, want 1", got)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("unknown IP at known TTL is a no-op (no node created)", func(t *testing.T) {
		ip := net.ParseIP("192.0.2.99")
		m := newTestModel(withHops(hopSample(3, "192.0.2.10", 10*time.Millisecond)))

		msg := EchoSentMsg{TargetIdx: 0, IP: ip, TTL: 3}
		m2, cmd := m.onEchoSent(msg)

		snap := m2.sessions[0].table.Snapshot()
		nodes := snap[0].GetNodes()
		if len(nodes) != 1 {
			t.Fatalf("nodes len = %d, want 1 (no node must be fabricated)", len(nodes))
		}
		if got := nodes[0].EchoStats().Sent; got != 0 {
			t.Errorf("EchoStats.Sent = %d, want 0 (wrong IP must not be incremented)", got)
		}
		cmdIsNil(t, cmd)
	})

	t.Run("unknown TTL is a no-op (no new hop row)", func(t *testing.T) {
		ip := net.ParseIP("192.0.2.10")
		m := newTestModel(withHops(hopSample(3, "192.0.2.10", 10*time.Millisecond)))

		msg := EchoSentMsg{TargetIdx: 0, IP: ip, TTL: 99}
		m2, _ := m.onEchoSent(msg)

		snap := m2.sessions[0].table.Snapshot()
		// Table should only have the TTL 3 hop; TTL 99 must not appear.
		for _, h := range snap {
			if h.TTL == 99 {
				t.Error("a new hop row was fabricated for unknown TTL 99")
			}
		}
	})

	t.Run("bad TargetIdx is a no-op", func(t *testing.T) {
		m := newTestModel(withHops(hopSample(3, "192.0.2.10", 10*time.Millisecond)))
		msg := EchoSentMsg{TargetIdx: 99, IP: net.ParseIP("192.0.2.10"), TTL: 3}
		m2, cmd := m.onEchoSent(msg)
		// Must not panic and session 0 must be unchanged.
		snap := m2.sessions[0].table.Snapshot()
		nodes := snap[0].GetNodes()
		if got := nodes[0].EchoStats().Sent; got != 0 {
			t.Errorf("EchoStats.Sent = %d, want 0", got)
		}
		cmdIsNil(t, cmd)
	})
}

// ---------------------------------------------------------------------------
// TestOnRoundEndSubmitsEchoTargets
// ---------------------------------------------------------------------------

// TestOnRoundEndSubmitsEchoTargets verifies that onRoundEnd collects all
// {IP, TTL} pairs from the table and passes them to the echoSink.
func TestOnRoundEndSubmitsEchoTargets(t *testing.T) {
	ipA := net.ParseIP("192.0.2.1")
	ipB := net.ParseIP("192.0.2.2")
	ipC := net.ParseIP("192.0.2.3")

	// Build table: TTL 1→A, TTL 2→B+C, TTL 3→A (same IP, different TTL)
	m := newTestModel()
	sink := &fakeEchoSink{}
	m.sessions[0].echoSink = sink

	m.sessions[0].table.GetOrCreate(1).AddSample(ipA, 5*time.Millisecond, 0, hop.KindPrimary)
	m.sessions[0].table.GetOrCreate(2).AddSample(ipB, 10*time.Millisecond, 0, hop.KindPrimary)
	m.sessions[0].table.GetOrCreate(2).AddSample(ipC, 10*time.Millisecond, 1, hop.KindPrimary)
	m.sessions[0].table.GetOrCreate(3).AddSample(ipA, 15*time.Millisecond, 0, hop.KindPrimary)

	m2, cmd := m.onRoundEnd(RoundEndMsg{TargetIdx: 0})
	_ = m2
	cmdIsNil(t, cmd)

	got := sink.submitted
	// Expect exactly 4 pairs (not deduped by IP).
	if len(got) != 4 {
		t.Fatalf("SubmitEchoTargets called with %d pairs, want 4: %v", len(got), got)
	}

	// Build a set of received pairs for order-independent comparison.
	type pair struct {
		ip  string
		ttl int
	}
	want := map[pair]int{
		{ipA.String(), 1}: 1,
		{ipB.String(), 2}: 1,
		{ipC.String(), 2}: 1,
		{ipA.String(), 3}: 1,
	}
	for _, tgt := range got {
		k := pair{tgt.IP.String(), tgt.TTL}
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected pair: {%s, %d}", tgt.IP, tgt.TTL)
		}
		want[k]--
	}
	for k, cnt := range want {
		if cnt != 0 {
			t.Errorf("missing pair: {%s, %d}", k.ip, k.ttl)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers local to this file
// ---------------------------------------------------------------------------

// windowSizeMsg constructs a tea.WindowSizeMsg with the given dimensions.
func windowSizeMsg(w, h int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: w, Height: h}
}
