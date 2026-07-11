package tui

import (
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tonhe/viaduct/internal/config"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/ping"
	"github.com/tonhe/viaduct/internal/probe"
	"github.com/tonhe/viaduct/internal/resolve"
)

// testOpt configures a testModel via functional options.
type testOpt func(*Model)

// newTestModel returns a Model seeded with sensible defaults for tests.
// Callers pass option funcs (withXxx) to override specific fields.
//
// Defaults:
//   - target: "example.com"
//   - targetIP: 192.0.2.1
//   - version: "0.0.0-test"
//   - table: empty hop.Table (maxHops 30)
//   - probeCfg: probe.DefaultConfig() with IPVersion=4
//   - startTime: time.Unix(0, 0)
//   - width, height: 100, 24
//   - config: config.Default()
//   - viewMode: ViewDefault
//   - protocolName: "udp"
//   - autoScroll: true
//   - resolver: resolve.New(4) with empty cache (no goroutines started)
//   - nowFunc: returns time.Unix(0, 0) for deterministic elapsed output
//   - All other fields zero-value / nil
//
// This function is the ONLY seam through which tests construct Models,
// so field additions to Model only require updating this function.
func newTestModel(opts ...testOpt) Model {
	cfg := probe.DefaultConfig()
	cfg.IPVersion = 4

	m := Model{
		target:       "example.com",
		targetIP:     net.ParseIP("192.0.2.1"),
		version:      "0.0.0-test",
		table:        hop.NewTable(cfg.MaxHops),
		probeCfg:     cfg,
		startTime:    time.Unix(0, 0),
		width:        100,
		height:       24,
		config:       config.Default(),
		viewMode:     ViewDefault,
		protocolName: "udp",
		autoScroll:   true,
		resolver:     resolve.New(4),
		nowFunc:      func() time.Time { return time.Unix(0, 0) },
	}

	for _, opt := range opts {
		opt(&m)
	}
	return m
}

// withNow injects a fixed clock for deterministic test output.
// The Model's now() method will return t on every call.
func withNow(t time.Time) testOpt {
	return func(m *Model) { m.nowFunc = func() time.Time { return t } }
}

// --- Option funcs ---

// withHops seeds the hop table with the given samples.
// Each sample carries a TTL, IP string, and RTT; flowID is always 0.
func withHops(samples ...hopSampleFixture) testOpt {
	return func(m *Model) {
		for _, s := range samples {
			ip := net.ParseIP(s.ip)
			m.table.GetOrCreate(s.ttl).AddSample(ip, s.rtt, 0)
		}
	}
}

// withSize sets terminal dimensions.
func withSize(w, h int) testOpt {
	return func(m *Model) {
		m.width = w
		m.height = h
	}
}

// withView sets the current ViewMode.
func withView(v ViewMode) testOpt {
	return func(m *Model) { m.viewMode = v }
}

// withIPVersion sets the probe IPVersion (4 or 6) and adjusts targetIP
// to match the family. IPv6 default targetIP is 2001:db8::1.
// The target hostname remains "example.com" in both cases (documentation domain).
func withIPVersion(v int) testOpt {
	return func(m *Model) {
		m.probeCfg.IPVersion = v
		if v == 6 {
			m.targetIP = net.ParseIP("2001:db8::1")
		}
	}
}

// withSettingsOpen opens the settings overlay in a specific state.
// The SettingsModel is constructed from the model's current config.
func withSettingsOpen(state settingsState) testOpt {
	return func(m *Model) {
		s := NewSettings(m.config, m.width, m.height)
		s.state = state
		m.settings = &s
		m.showSettings = true
	}
}

// withAlertEngine attaches an alert engine with given thresholds.
func withAlertEngine(lossPct float64, latency time.Duration, rounds int) testOpt {
	return func(m *Model) {
		m.alertEngine = NewAlertEngine(lossPct, latency, rounds)
	}
}

// withConfig replaces the model's config reference.
func withConfig(cfg *config.Config) testOpt {
	return func(m *Model) { m.config = cfg }
}

// withPingStats initialises the pingStats map so ping-related code paths
// don't treat it as a disabled/nil map.
func withPingStats() testOpt {
	return func(m *Model) {
		if m.pingStats == nil {
			m.pingStats = make(map[string]*ping.Stat) //nolint:staticcheck
		}
	}
}

// withEnricher is intentionally a no-op: leaving enricher nil exercises the
// "no ASN" code path, which is the correct default for unit tests that don't
// want live lookups. Callers that need the "ASN configured" branch should
// construct and attach an enricher directly via the Model fields (same package).
func withEnricher() testOpt {
	return func(m *Model) {
		// nil enricher is the safe default — code must be nil-safe on this path.
	}
}

// --- Sample fixture ---

// hopSampleFixture is a compact representation of a single hop measurement.
type hopSampleFixture struct {
	ttl int
	ip  string // IPv4 or IPv6 string; parsed by withHops
	rtt time.Duration
}

// hopSample is shorthand for constructing a hopSampleFixture.
func hopSample(ttl int, ip string, rtt time.Duration) hopSampleFixture {
	return hopSampleFixture{ttl: ttl, ip: ip, rtt: rtt}
}

// --- Command matchers ---

// cmdIsNil asserts the returned tea.Cmd is nil.
func cmdIsNil(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd != nil {
		t.Errorf("expected nil cmd, got %T", cmd)
	}
}

// cmdIsQuit asserts the returned tea.Cmd, when invoked, returns tea.QuitMsg.
func cmdIsQuit(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected quit cmd, got nil")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", msg)
	}
}

// cmdIsNonNil asserts the returned tea.Cmd is non-nil (for cases where we
// just want to know a Cmd was issued, not what it does).
func cmdIsNonNil(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Error("expected non-nil cmd, got nil")
	}
}

// --- String helpers (used by golden tests too) ---

// ansiRE matches ANSI escape sequences.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[mKHJABCDsu]`)

// stripANSI removes ANSI escape sequences from a string.
func stripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

// normalizeLE replaces CRLF and lone CR with LF.
func normalizeLE(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// --- Smoke test ---

// TestHelpers_Compile verifies the helpers build and can construct a Model.
// This is the "framework smoke test" done-signal from PLAN.md T4.
func TestHelpers_Compile(t *testing.T) {
	// Default model
	m := newTestModel()
	if m.target != "example.com" {
		t.Errorf("default target = %q, want example.com", m.target)
	}
	if m.width != 100 || m.height != 24 {
		t.Errorf("default size = %dx%d, want 100x24", m.width, m.height)
	}
	if m.probeCfg.IPVersion != 4 {
		t.Errorf("default IPVersion = %d, want 4", m.probeCfg.IPVersion)
	}
	if m.viewMode != ViewDefault {
		t.Errorf("default viewMode = %v, want ViewDefault", m.viewMode)
	}
	if !m.autoScroll {
		t.Error("default autoScroll should be true")
	}
	if m.config == nil {
		t.Error("default config should not be nil")
	}
	if m.table == nil {
		t.Error("default table should not be nil")
	}

	// Exercise each option
	m2 := newTestModel(
		withSize(160, 48),
		withView(ViewHealth),
		withIPVersion(6),
		withPingStats(),
	)
	if m2.width != 160 || m2.height != 48 {
		t.Error("withSize did not apply")
	}
	if m2.viewMode != ViewHealth {
		t.Error("withView did not apply")
	}
	if m2.probeCfg.IPVersion != 6 {
		t.Error("withIPVersion did not apply")
	}
	if m2.pingStats == nil {
		t.Error("withPingStats did not apply")
	}
	wantIP := net.ParseIP("2001:db8::1")
	if !m2.targetIP.Equal(wantIP) {
		t.Errorf("withIPVersion(6) targetIP = %v, want %v", m2.targetIP, wantIP)
	}

	// hop fixture
	m3 := newTestModel(withHops(
		hopSample(1, "192.0.2.1", 10*time.Millisecond),
		hopSample(2, "192.0.2.2", 20*time.Millisecond),
	))
	snap := m3.table.Snapshot()
	if len(snap) != 2 {
		t.Errorf("table snapshot len = %d, want 2", len(snap))
	}
	if snap[0].TTL != 1 {
		t.Errorf("snap[0].TTL = %d, want 1", snap[0].TTL)
	}
	if snap[1].TTL != 2 {
		t.Errorf("snap[1].TTL = %d, want 2", snap[1].TTL)
	}

	// alert engine option
	m4 := newTestModel(withAlertEngine(5.0, 100*time.Millisecond, 3))
	if m4.alertEngine == nil {
		t.Error("withAlertEngine did not attach alertEngine")
	}

	// settings open option
	m5 := newTestModel(withSettingsOpen(settingsMain))
	if !m5.showSettings {
		t.Error("withSettingsOpen did not set showSettings")
	}
	if m5.settings == nil {
		t.Error("withSettingsOpen did not attach settings")
	}

	// config override
	custom := config.Default()
	custom.Protocol = "icmp"
	m6 := newTestModel(withConfig(custom))
	if m6.config.Protocol != "icmp" {
		t.Errorf("withConfig: Protocol = %q, want icmp", m6.config.Protocol)
	}

	// cmd matchers
	cmdIsNil(t, nil)
	cmdIsNonNil(t, tea.Quit)
	cmdIsQuit(t, tea.Quit)

	// stripANSI smoke check
	stripped := stripANSI("\x1b[31mhello\x1b[0m")
	if stripped != "hello" {
		t.Errorf("stripANSI = %q, want %q", stripped, "hello")
	}

	// normalizeLE smoke check
	if normalizeLE("a\r\nb\rc") != "a\nb\nc" {
		t.Error("normalizeLE did not fully normalize")
	}
}
