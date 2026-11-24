package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/export"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/probe"
)

// Documentation address space per RFC 5737 / RFC 3849.
var (
	docV4 = net.ParseIP("192.0.2.1").To4() // TEST-NET-1, RFC 5737
	docV6 = net.ParseIP("2001:db8::1")     // 2001:db8::/32, RFC 3849
)

// TestSelectFamily exercises the full decision matrix of selectFamily.
func TestSelectFamily(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		forceV4     bool
		forceV6     bool
		v4          net.IP
		v6          net.IP
		hasV4       bool
		hasV6       bool
		wantVersion int
		wantIP      net.IP
		wantErrSub  string // non-empty means we expect an error containing this substring
	}{
		// ── Forced v4 ────────────────────────────────────────────────────────
		{
			name:        "forced_v4_has_v4_ip_and_transport",
			forceV4:     true,
			v4:          docV4,
			hasV4:       true,
			wantVersion: 4,
			wantIP:      docV4,
		},
		{
			name:       "forced_v4_no_v4_ip",
			forceV4:    true,
			v4:         nil,
			hasV4:      true,
			wantErrSub: "no IPv4 (A) record",
		},
		{
			name:       "forced_v4_has_v4_ip_no_transport",
			forceV4:    true,
			v4:         docV4,
			hasV4:      false,
			wantErrSub: "no IPv4 transport",
		},
		// ── Forced v6 ────────────────────────────────────────────────────────
		{
			name:        "forced_v6_has_v6_ip_and_transport",
			forceV6:     true,
			v6:          docV6,
			hasV6:       true,
			wantVersion: 6,
			wantIP:      docV6,
		},
		{
			name:       "forced_v6_no_v6_ip",
			forceV6:    true,
			v6:         nil,
			hasV6:      true,
			wantErrSub: "no IPv6 (AAAA) record",
		},
		{
			name:       "forced_v6_has_v6_ip_no_transport",
			forceV6:    true,
			v6:         docV6,
			hasV6:      false,
			wantErrSub: "no IPv6 transport",
		},
		// ── Auto-select ──────────────────────────────────────────────────────
		{
			name:        "auto_both_ips_both_transports_prefers_v6",
			v4:          docV4,
			v6:          docV6,
			hasV4:       true,
			hasV6:       true,
			wantVersion: 6,
			wantIP:      docV6,
		},
		{
			name:        "auto_v4_only_has_v4_transport",
			v4:          docV4,
			hasV4:       true,
			wantVersion: 4,
			wantIP:      docV4,
		},
		{
			name: "auto_v4_only_no_v4_transport_best_effort_fallthrough",
			// No transport but v4 address exists — best-effort, returns v4 so
			// the downstream raw-socket open can trigger sudo re-exec.
			v4:          docV4,
			hasV4:       false,
			wantVersion: 4,
			wantIP:      docV4,
		},
		{
			name:        "auto_v6_only_has_v6_transport",
			v6:          docV6,
			hasV6:       true,
			wantVersion: 6,
			wantIP:      docV6,
		},
		{
			name: "auto_v6_only_no_v6_transport_best_effort_fallthrough",
			// v6 address exists but no v6 transport; v4 is nil → falls through
			// to the final "v6 != nil" guard and returns v6 best-effort.
			v6:          docV6,
			hasV6:       false,
			wantVersion: 6,
			wantIP:      docV6,
		},
		{
			name:        "auto_both_ips_v6_transport_only_prefers_v6",
			v4:          docV4,
			v6:          docV6,
			hasV4:       false,
			hasV6:       true,
			wantVersion: 6,
			wantIP:      docV6,
		},
		{
			name:        "auto_both_ips_v4_transport_only_falls_back_to_v4",
			v4:          docV4,
			v6:          docV6,
			hasV4:       true,
			hasV6:       false,
			wantVersion: 4,
			wantIP:      docV4,
		},
		{
			name:       "auto_no_ips_at_all",
			wantErrSub: "no usable address",
		},
	}

	for _, tc := range tests {
		tc := tc // capture for parallel sub-test
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotVersion, gotIP, err := selectFamily(tc.forceV4, tc.forceV6, tc.v4, tc.v6, tc.hasV4, tc.hasV6)

			if tc.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (version=%d ip=%v)", tc.wantErrSub, gotVersion, gotIP)
				}
				if !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("expected error containing %q, got %q", tc.wantErrSub, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotVersion != tc.wantVersion {
				t.Errorf("version: got %d, want %d", gotVersion, tc.wantVersion)
			}
			if !gotIP.Equal(tc.wantIP) {
				t.Errorf("IP: got %v, want %v", gotIP, tc.wantIP)
			}
		})
	}
}

// TestBuildProtocol exercises the protocol factory for valid and invalid inputs.
func TestBuildProtocol(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		protocolName string
		dstPort      int
		numPaths     int
		wantErr      bool
		wantErrSub   string
		wantPaths    int // expected numPaths after call (0 = don't check)
	}{
		{
			name:         "udp_default_port",
			protocolName: "udp",
			dstPort:      0,
			numPaths:     1,
		},
		{
			name:         "udp_custom_port",
			protocolName: "udp",
			dstPort:      12345,
			numPaths:     1,
		},
		{
			name:         "tcp_default_port",
			protocolName: "tcp",
			dstPort:      0,
			numPaths:     1,
		},
		{
			name:         "tcp_custom_port",
			protocolName: "tcp",
			dstPort:      80,
			numPaths:     1,
		},
		{
			name:         "icmp_single_path",
			protocolName: "icmp",
			dstPort:      0,
			numPaths:     1,
		},
		{
			name:         "icmp_multipath_resets_to_one",
			protocolName: "icmp",
			dstPort:      0,
			numPaths:     6,
			wantPaths:    1,
		},
		{
			name:         "auto_default_port",
			protocolName: "auto",
			dstPort:      0,
			numPaths:     1,
		},
		{
			name:         "unknown_protocol",
			protocolName: "quic",
			dstPort:      0,
			numPaths:     1,
			wantErr:      true,
			wantErrSub:   "unknown protocol",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := tc.numPaths
			cfg := probe.Config{NumPaths: n}
			proto, err := buildProtocol(tc.protocolName, tc.dstPort, &n, cfg)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrSub)
				}
				if tc.wantErrSub != "" && !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("expected error containing %q, got %q", tc.wantErrSub, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if proto == nil {
				t.Fatal("buildProtocol returned nil protocol without error")
			}
			if tc.wantPaths != 0 && n != tc.wantPaths {
				t.Errorf("numPaths: got %d, want %d", n, tc.wantPaths)
			}
		})
	}
}

// TestICMPNumPathsSync ensures that ICMP mode's numPaths clamp (1) reaches probeCfg.NumPaths.
func TestICMPNumPathsSync(t *testing.T) {
	t.Parallel()

	// Simulate the main.go flow: build probeCfg with numPaths > 1,
	// call buildProtocol with ICMP, then verify probeCfg is updated.
	numPaths := 6
	probeCfg := probe.Config{
		NumPaths: numPaths,
	}

	proto, err := buildProtocol("icmp", 0, &numPaths, probeCfg)
	if err != nil {
		t.Fatalf("buildProtocol failed: %v", err)
	}
	if proto == nil {
		t.Fatal("buildProtocol returned nil protocol")
	}

	// After buildProtocol clamps numPaths in ICMP mode, sync it back to cfg.
	probeCfg.NumPaths = numPaths

	if probeCfg.NumPaths != 1 {
		t.Errorf("probeCfg.NumPaths: got %d, want 1", probeCfg.NumPaths)
	}
}

// ── Integration tests for runReport ──────────────────────────────────────────
//
// These tests inject a stubTracer instead of a real probe.Tracer, exercising
// the full runReport wiring (config propagation, hop table assembly, export)
// without requiring raw sockets.

// stubTracer implements tracerIface with deterministic, pre-canned results.
type stubTracer struct {
	// capturedCfg records the Config passed at construction (set externally).
	capturedCfg probe.Config

	results        []probe.Result
	onRound        probe.RoundMarker
	onRoundEnd     func()
	onEchoSent     func(ip net.IP, ttl int)
	echoSubmitted  [][]probe.EchoTarget // one entry per SubmitEchoTargets call
}

func (s *stubTracer) SetOnRoundSent(r probe.RoundMarker) { s.onRound = r }
func (s *stubTracer) SetOnRoundEnd(f func())             { s.onRoundEnd = f }
func (s *stubTracer) SetOnEchoSent(f func(ip net.IP, ttl int)) {
	s.onEchoSent = f
}
func (s *stubTracer) SubmitEchoTargets(targets []probe.EchoTarget) {
	// Snapshot so later mutations by the caller don't rewrite history.
	cp := append([]probe.EchoTarget(nil), targets...)
	s.echoSubmitted = append(s.echoSubmitted, cp)
}

// Run emits all pre-canned results then returns nil, simulating a completed trace.
// It waits for both the channel to drain AND for the last result to appear in
// the table (observable via onRound, which is the hop.Table itself) before
// firing OnRoundEnd.  This guarantees the collector goroutine has fully
// processed every item — len(out)==0 proves receipt, not completion of AddSample.
// The deadline path panics so a deadlock fails loudly rather than proceeding
// silently with incomplete state.
func (s *stubTracer) Run(_ context.Context, out chan<- probe.Result) error {
	// Identify the last result so we can confirm it reached the table.
	var lastResult *probe.Result
	for i := range s.results {
		out <- s.results[i]
		lastResult = &s.results[i]
	}
	if lastResult != nil && s.onRound != nil {
		// Wait until both: channel is drained AND the last probe's hop exists
		// in the table with at least one sample received.  The onRound field
		// holds the *hop.Table (set via SetOnRoundSent); we use it as a
		// hop.Table to check visibility of the last result.
		tbl, ok := s.onRound.(*hop.Table)
		if ok {
			deadline := time.Now().Add(2 * time.Second)
			for {
				if time.Now().After(deadline) {
					panic("stubTracer.Run: deadline exceeded waiting for collector to process last result — collector goroutine likely blocked")
				}
				time.Sleep(time.Millisecond)
				if len(out) > 0 {
					continue
				}
				// Channel empty; confirm the last hop is visible in the table.
				h := tbl.Get(lastResult.TTL)
				if h != nil && h.GetIP() != nil {
					break
				}
			}
		} else {
			// Fallback: onRound is not a *hop.Table; drain channel only.
			deadline := time.Now().Add(2 * time.Second)
			for len(out) > 0 {
				if time.Now().After(deadline) {
					panic("stubTracer.Run: deadline exceeded waiting for channel drain")
				}
				time.Sleep(time.Millisecond)
			}
		}
	}
	// Fire OnRoundEnd once so round-end accounting runs.
	if s.onRoundEnd != nil {
		s.onRoundEnd()
	}
	return nil
}

// captureStdout swaps os.Stdout for a pipe, calls fn, then restores stdout and
// returns everything written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

// installStubTracer replaces newTracer for the duration of the test and returns
// the stub so tests can inspect captured state.
func installStubTracer(t *testing.T, results []probe.Result, cfg *probe.Config) *stubTracer {
	t.Helper()
	stub := &stubTracer{results: results}
	orig := newTracer
	newTracer = func(target net.IP, c probe.Config) tracerIface {
		if cfg != nil {
			*cfg = c
		}
		stub.capturedCfg = c
		return stub
	}
	t.Cleanup(func() { newTracer = orig })
	return stub
}

// buildV4Results builds a 4-hop IPv4 result set. The last hop has IsTarget=true.
func buildV4Results() []probe.Result {
	rtt := 10 * time.Millisecond
	return []probe.Result{
		{TTL: 1, IP: net.ParseIP("192.0.2.1"), RTT: rtt, IsTarget: false, FlowID: 0},
		{TTL: 2, IP: net.ParseIP("192.0.2.2"), RTT: rtt * 2, IsTarget: false, FlowID: 0},
		{TTL: 3, IP: net.ParseIP("192.0.2.3"), RTT: rtt * 3, IsTarget: false, FlowID: 0},
		{TTL: 4, IP: net.ParseIP("192.0.2.4"), RTT: rtt * 4, IsTarget: true, FlowID: 0},
	}
}

// buildV6Results builds a 4-hop IPv6 result set. The last hop has IsTarget=true.
func buildV6Results() []probe.Result {
	rtt := 10 * time.Millisecond
	return []probe.Result{
		{TTL: 1, IP: net.ParseIP("2001:db8::1"), RTT: rtt, IsTarget: false, FlowID: 0},
		{TTL: 2, IP: net.ParseIP("2001:db8::2"), RTT: rtt * 2, IsTarget: false, FlowID: 0},
		{TTL: 3, IP: net.ParseIP("2001:db8::3"), RTT: rtt * 3, IsTarget: false, FlowID: 0},
		{TTL: 4, IP: net.ParseIP("2001:db8::4"), RTT: rtt * 4, IsTarget: true, FlowID: 0},
	}
}

// TestRunReport_HappyPath tests a 4-hop IPv4 report with all three export formats.
func TestRunReport_HappyPath(t *testing.T) {
	targetIP := net.ParseIP("192.0.2.4").To4()
	target := "192.0.2.4"

	cfg := probe.Config{
		MaxHops:    30,
		FirstTTL:   1,
		Timeout:    100 * time.Millisecond,
		RoundDelay: 0,
		MaxRounds:  1,
		NumPaths:   1,
		IPVersion:  4,
	}
	cfg.Protocol = probe.NewICMPProtocol(cfg)

	t.Run("printReport_stdout", func(t *testing.T) {
		installStubTracer(t, buildV4Results(), nil)
		out := captureStdout(t, func() {
			if err := runReport(target, targetIP, cfg, "icmp", true, ""); err != nil {
				t.Fatalf("runReport returned error: %v", err)
			}
		})
		if !strings.Contains(out, target) {
			t.Errorf("stdout does not contain target IP %q; got:\n%s", target, out)
		}
		// Count data rows: lines that start with a hop number (digit at position 0).
		hopLines := 0
		for _, line := range strings.Split(out, "\n") {
			trimmed := strings.TrimSpace(line)
			if len(trimmed) > 0 && trimmed[0] >= '1' && trimmed[0] <= '9' {
				hopLines++
			}
		}
		if hopLines != 4 {
			t.Errorf("expected 4 hop lines in output, got %d:\n%s", hopLines, out)
		}
	})

	t.Run("json_export", func(t *testing.T) {
		installStubTracer(t, buildV4Results(), nil)
		out := captureStdout(t, func() {
			if err := runReport(target, targetIP, cfg, "icmp", true, "json"); err != nil {
				t.Fatalf("runReport returned error: %v", err)
			}
		})

		var report export.Report
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatalf("JSON output is not valid: %v\nOutput:\n%s", err, out)
		}
		if report.TargetIP != target {
			t.Errorf("JSON target_ip: got %q, want %q", report.TargetIP, target)
		}
		if len(report.Hops) != 4 {
			t.Errorf("JSON hops: got %d, want 4", len(report.Hops))
		}
	})

	t.Run("csv_export", func(t *testing.T) {
		installStubTracer(t, buildV4Results(), nil)
		out := captureStdout(t, func() {
			if err := runReport(target, targetIP, cfg, "icmp", true, "csv"); err != nil {
				t.Fatalf("runReport returned error: %v", err)
			}
		})

		r := csv.NewReader(strings.NewReader(out))
		rows, err := r.ReadAll()
		if err != nil {
			t.Fatalf("CSV output is not valid: %v\nOutput:\n%s", err, out)
		}
		// header + 4 data rows
		if len(rows) != 5 {
			t.Errorf("CSV rows: got %d, want 5 (header + 4 hops)", len(rows))
		}
		// Verify header
		if rows[0][0] != "ttl" {
			t.Errorf("CSV first header column: got %q, want %q", rows[0][0], "ttl")
		}
	})

	t.Run("dot_export", func(t *testing.T) {
		installStubTracer(t, buildV4Results(), nil)
		out := captureStdout(t, func() {
			if err := runReport(target, targetIP, cfg, "icmp", true, "dot"); err != nil {
				t.Fatalf("runReport returned error: %v", err)
			}
		})

		if !strings.HasPrefix(strings.TrimSpace(out), "digraph") {
			t.Errorf("DOT output does not start with 'digraph'; got:\n%s", out)
		}
		if !strings.Contains(out, "}") {
			t.Errorf("DOT output missing closing brace")
		}
	})
}

// TestRunReport_IPv6 tests a 4-hop IPv6 report, verifying that IPVersion=6
// propagates into the tracer config and that v6 addresses appear in the output.
func TestRunReport_IPv6(t *testing.T) {
	targetIP := net.ParseIP("2001:db8::4")
	target := "2001:db8::4"

	var capturedCfg probe.Config
	installStubTracer(t, buildV6Results(), &capturedCfg)

	cfg := probe.Config{
		MaxHops:    30,
		FirstTTL:   1,
		Timeout:    100 * time.Millisecond,
		RoundDelay: 0,
		MaxRounds:  1,
		NumPaths:   1,
		IPVersion:  6,
	}
	cfg.Protocol = probe.NewICMPProtocol(cfg)

	out := captureStdout(t, func() {
		if err := runReport(target, targetIP, cfg, "icmp", true, ""); err != nil {
			t.Fatalf("runReport returned error: %v", err)
		}
	})

	// IPVersion=6 must have reached the stub.
	if capturedCfg.IPVersion != 6 {
		t.Errorf("cfg.IPVersion not propagated: got %d, want 6", capturedCfg.IPVersion)
	}

	// Output must contain IPv6 addresses.
	if !strings.Contains(out, "2001:db8::") {
		t.Errorf("output does not contain IPv6 addresses; got:\n%s", out)
	}

	// Exactly 4 hop data rows should appear (lines starting with a digit).
	hopLines := 0
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 0 && trimmed[0] >= '1' && trimmed[0] <= '9' {
			hopLines++
		}
	}
	if hopLines != 4 {
		t.Errorf("expected 4 hop data rows, got %d:\n%s", hopLines, out)
	}
}

// baseProbeConfig returns a probe.Config that mirrors what main.go builds before
// per-target adjustment. Used by resolveTargetSpecs tests so they share the same
// base config as the real call site.
func baseProbeConfig() probe.Config {
	cfg := probe.Config{
		MaxHops:     30,
		FirstTTL:    1,
		Timeout:     time.Second,
		ProbeDelay:  10 * time.Millisecond,
		RoundDelay:  time.Second,
		PayloadSize: 64,
		MaxRounds:   0,
		NoDNS:       false,
		ReportMode:  false,
		NumPaths:    6,
		IPVersion: 4,
	}
	cfg.Protocol, _ = buildProtocol("udp", 0, &cfg.NumPaths, cfg)
	return cfg
}

func TestResolveTargetSpecs_MixedSuccess(t *testing.T) {
	// resolveTargetSpecs is pure except for DNS; inject the lookup.
	lookup := func(host string) ([]net.IP, error) {
		switch host {
		case "v4.example":
			return []net.IP{net.ParseIP("192.0.2.10")}, nil
		case "bad.example":
			return nil, fmt.Errorf("no such host")
		}
		return nil, fmt.Errorf("unexpected host %q", host)
	}
	initPortAllocator(2)
	specs := resolveTargetSpecs([]string{"v4.example", "bad.example"}, false, false, true, true, baseProbeConfig(), "udp", lookup)
	if len(specs) != 2 {
		t.Fatalf("specs = %d, want 2", len(specs))
	}
	if specs[0].Err != nil {
		t.Fatalf("v4.example errored: %v", specs[0].Err)
	}
	wantBase, _, _ := sessionPorts(0)
	if specs[0].Cfg.BasePort != wantBase {
		t.Fatalf("session 0 BasePort = %d, want %d", specs[0].Cfg.BasePort, wantBase)
	}
	if specs[1].Err == nil {
		t.Fatal("bad.example should carry Err")
	}
}

func TestResolveTargetSpecs_PortBlocks(t *testing.T) {
	lookup := func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("192.0.2.10")}, nil
	}
	initPortAllocator(2)
	specs := resolveTargetSpecs([]string{"a.example", "b.example"}, false, false, true, true, baseProbeConfig(), "udp", lookup)
	wantBase0, wantID0, _ := sessionPorts(0)
	wantBase1, wantID1, _ := sessionPorts(1)
	if specs[1].Cfg.BasePort != wantBase1 {
		t.Fatalf("session 1 BasePort = %d, want %d", specs[1].Cfg.BasePort, wantBase1)
	}
	if specs[1].Cfg.ICMPID != wantID1 {
		t.Fatalf("session 1 ICMPID = %d, want %d", specs[1].Cfg.ICMPID, wantID1)
	}
	// Verify stride relationship.
	if specs[1].Cfg.BasePort != specs[0].Cfg.BasePort+basePortStride {
		t.Fatalf("session 1 port = %d, want session 0 port %d + stride %d",
			specs[1].Cfg.BasePort, specs[0].Cfg.BasePort, basePortStride)
	}
	// Suppress unused-variable warning for wantBase0/wantID0.
	_ = wantBase0
	_ = wantID0
}

// TestResolveTargetSpecs_EmptyAnswer verifies that a lookup returning (nil, nil)
// — a successful query with zero IP addresses — results in Err != nil for that
// target while other targets in the same call still resolve normally.
func TestResolveTargetSpecs_EmptyAnswer(t *testing.T) {
	// empty.example returns no IPs and no error (NXDOMAIN with zero records).
	// ok.example resolves normally.
	lookup := func(host string) ([]net.IP, error) {
		switch host {
		case "ok.example":
			return []net.IP{net.ParseIP("198.51.100.1")}, nil
		case "empty.example":
			return nil, nil // successful query, zero addresses
		}
		return nil, fmt.Errorf("unexpected host %q", host)
	}

	specs := resolveTargetSpecs(
		[]string{"ok.example", "empty.example"},
		false, false, true, true,
		baseProbeConfig(), "udp", lookup,
	)

	if len(specs) != 2 {
		t.Fatalf("specs = %d, want 2", len(specs))
	}
	if specs[0].Err != nil {
		t.Fatalf("ok.example should not have errored, got: %v", specs[0].Err)
	}
	if specs[1].Err == nil {
		t.Fatal("empty.example should carry Err for zero-IP answer, got nil")
	}
}

// ── printReport rate-limit dagger regression ──────────────────────────────
//
// Verifies that printReport renders the tilde+dagger loss form when the
// primary node's RateLimitState() == RateLimitYes and plain "%.1f%%" when
// the state is Undetermined. The rendering must come from the hop table
// alone — no ping supplement path exists any more.

// seedHopRateLimit populates an already-created hop with per-kind samples
// that drive its primary node's RateLimitState. Mirrors the recipe in
// internal/hop/ratelimit_test.go's mkNode helper.
func seedHopRateLimit(t *testing.T, tbl *hop.Table, ttl int, ip net.IP, ttlRounds, ttlRecv, echoSent, echoRecv int) *hop.Hop {
	t.Helper()
	h := tbl.GetOrCreate(ttl)
	// One primary sample so PrimaryNode() and GetIP() are non-nil; the
	// printer skips hops with no primary node.
	h.AddSample(ip, time.Millisecond, 0, hop.KindPrimary)
	pn := h.PrimaryNode()
	if pn == nil {
		t.Fatalf("PrimaryNode nil after seeding sample at ttl %d", ttl)
	}
	for i := 0; i < ttlRounds; i++ {
		if i < ttlRecv {
			pn.AddSample(time.Millisecond, 0, hop.KindICMPTTL)
		}
		pn.MarkProbeRoundEnd()
	}
	for i := 0; i < echoSent; i++ {
		pn.MarkEchoSent()
	}
	for i := 0; i < echoRecv; i++ {
		pn.AddSample(time.Millisecond, 0, hop.KindEcho)
	}
	h.MarkRoundSent()
	return h
}

func TestPrintReport_RateLimitDagger(t *testing.T) {
	target := "192.0.2.4"
	targetIP := net.ParseIP("192.0.2.4").To4()

	tbl := hop.NewTable(30)
	// TTL 1: 30% TTL loss, 20% echo loss, 5 echo samples → RateLimitYes.
	seedHopRateLimit(t, tbl, 1, net.ParseIP("192.0.2.1"), 10, 7, 5, 4)
	// TTL 2: no echo data → RateLimitUndetermined; must render plain loss.
	seedHopRateLimit(t, tbl, 2, net.ParseIP("192.0.2.2"), 10, 5, 0, 0)

	out := captureStdout(t, func() {
		printReport(target, targetIP, tbl, nil, nil, 0, 1, "icmp")
	})

	// Locate the two data rows (leading digit).
	var row1, row2 string
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] < '1' || trimmed[0] > '9' {
			continue
		}
		if strings.HasPrefix(trimmed, "1 ") {
			row1 = trimmed
		} else if strings.HasPrefix(trimmed, "2 ") {
			row2 = trimmed
		}
	}
	if row1 == "" {
		t.Fatalf("no row for TTL 1 in output:\n%s", out)
	}
	if row2 == "" {
		t.Fatalf("no row for TTL 2 in output:\n%s", out)
	}

	// RateLimitYes: must include the dagger (†) and tilde (~).
	if !strings.Contains(row1, "†") {
		t.Errorf("TTL 1 (RateLimitYes) row missing dagger; got: %q", row1)
	}
	if !strings.Contains(row1, "~") {
		t.Errorf("TTL 1 (RateLimitYes) row missing tilde; got: %q", row1)
	}
	// RateLimitUndetermined: must render plain loss with no dagger/tilde.
	if strings.Contains(row2, "†") {
		t.Errorf("TTL 2 (Undetermined) row should not carry dagger; got: %q", row2)
	}
	if strings.Contains(row2, "~") {
		t.Errorf("TTL 2 (Undetermined) row should not carry tilde; got: %q", row2)
	}
}

// TestRunReport_EchoWiring verifies runReport hooks the echo tier to the
// tracer: at round-end it submits every {node IP, TTL} pair to
// SubmitEchoTargets, and echo-sent callbacks land on MarkEchoSent for the
// matching PathNode.
func TestRunReport_EchoWiring(t *testing.T) {
	targetIP := net.ParseIP("192.0.2.4").To4()
	target := "192.0.2.4"

	cfg := probe.Config{
		MaxHops:    30,
		FirstTTL:   1,
		Timeout:    100 * time.Millisecond,
		RoundDelay: 0,
		MaxRounds:  1,
		NumPaths:   1,
		IPVersion:  4,
	}
	cfg.Protocol = probe.NewICMPProtocol(cfg)

	stub := installStubTracer(t, buildV4Results(), nil)

	// runReportInner returns the table directly — no test-only hook needed.
	var capturedTable *hop.Table
	_ = captureStdout(t, func() {
		tbl, err := runReportInner(target, targetIP, cfg, "icmp", true, "")
		if err != nil {
			t.Fatalf("runReportInner returned error: %v", err)
		}
		capturedTable = tbl
	})

	// The stub fires OnRoundEnd exactly once, so we should see exactly one
	// SubmitEchoTargets call carrying all four hop nodes as pairs.
	if len(stub.echoSubmitted) != 1 {
		t.Fatalf("SubmitEchoTargets called %d times, want 1", len(stub.echoSubmitted))
	}
	got := stub.echoSubmitted[0]
	if len(got) != 4 {
		t.Fatalf("SubmitEchoTargets pair count = %d, want 4; pairs: %v", len(got), got)
	}
	// Each hop TTL 1..4 with its unique IP must appear exactly once.
	seen := make(map[int]string, 4)
	for _, p := range got {
		if prev, dup := seen[p.TTL]; dup {
			t.Errorf("duplicate TTL %d: %s and %s", p.TTL, prev, p.IP.String())
		}
		seen[p.TTL] = p.IP.String()
	}
	for ttl := 1; ttl <= 4; ttl++ {
		if _, ok := seen[ttl]; !ok {
			t.Errorf("missing TTL %d in submitted echo targets", ttl)
		}
	}

	// Simulate echo sends from the tracer for known {IP, TTL} pairs and
	// assert that EchoStats.Sent is incremented on the exact PathNode.
	// capturedTable is the same table runReportInner assembled; the
	// onEchoSent closure closes over it, so we can exercise the callback
	// directly via the stub.
	if capturedTable == nil {
		t.Fatal("runReportInner returned nil table")
	}

	// TTL 1, IP 192.0.2.1 — fire onEchoSent, expect Sent=1 on that node.
	knownIP := net.ParseIP("192.0.2.1")
	stub.onEchoSent(knownIP, 1)

	h := capturedTable.Get(1)
	if h == nil {
		t.Fatal("hop at TTL 1 not found in captured table")
	}
	var found bool
	for _, node := range h.GetNodes() {
		if nip := node.GetIP(); nip != nil && nip.Equal(knownIP) {
			if node.EchoStats().Sent != 1 {
				t.Errorf("TTL 1 node EchoStats.Sent = %d, want 1", node.EchoStats().Sent)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("TTL 1 node with IP 192.0.2.1 not found after echo-sent callback")
	}

	// Unknown IP — must be a no-op (no node exists at TTL 1 for this address).
	unknownIP := net.ParseIP("192.0.2.99")
	stub.onEchoSent(unknownIP, 1) // should not panic or mutate anything

	// Unknown TTL — must be a no-op (no hop exists at TTL 99).
	stub.onEchoSent(knownIP, 99) // should not panic or mutate anything
}
