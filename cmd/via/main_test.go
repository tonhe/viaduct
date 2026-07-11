package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/export"
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
			proto, err := buildProtocol(tc.protocolName, tc.dstPort, &n)

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

// ── Integration tests for runReport ──────────────────────────────────────────
//
// These tests inject a stubTracer instead of a real probe.Tracer, exercising
// the full runReport wiring (config propagation, hop table assembly, export)
// without requiring raw sockets.

// stubTracer implements tracerIface with deterministic, pre-canned results.
type stubTracer struct {
	// capturedCfg records the Config passed at construction (set externally).
	capturedCfg probe.Config

	results     []probe.Result
	onSent      probe.SentCounter
	onRoundEnd  func()
}

func (s *stubTracer) SetOnSent(c probe.SentCounter)  { s.onSent = c }
func (s *stubTracer) SetOnRoundEnd(f func())          { s.onRoundEnd = f }
func (s *stubTracer) Discover(_ context.Context, _ chan<- probe.Result) {}

// Run emits all pre-canned results then returns nil, simulating a completed trace.
func (s *stubTracer) Run(_ context.Context, out chan<- probe.Result) error {
	for _, r := range s.results {
		out <- r
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
		MaxHops:   30,
		FirstTTL:  1,
		Timeout:   100 * time.Millisecond,
		RoundDelay: 0,
		MaxRounds: 1,
		NumPaths:  1,
		IPVersion: 4,
		Protocol:  probe.NewICMPProtocol(),
	}

	t.Run("printReport_stdout", func(t *testing.T) {
		installStubTracer(t, buildV4Results(), nil)
		out := captureStdout(t, func() {
			if err := runReport(target, targetIP, cfg, "icmp", true, true, ""); err != nil {
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
			if err := runReport(target, targetIP, cfg, "icmp", true, true, "json"); err != nil {
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
			if err := runReport(target, targetIP, cfg, "icmp", true, true, "csv"); err != nil {
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
			if err := runReport(target, targetIP, cfg, "icmp", true, true, "dot"); err != nil {
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
		MaxHops:   30,
		FirstTTL:  1,
		Timeout:   100 * time.Millisecond,
		RoundDelay: 0,
		MaxRounds: 1,
		NumPaths:  1,
		IPVersion: 6,
		Protocol:  probe.NewICMPProtocol(),
	}

	out := captureStdout(t, func() {
		if err := runReport(target, targetIP, cfg, "icmp", true, true, ""); err != nil {
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
