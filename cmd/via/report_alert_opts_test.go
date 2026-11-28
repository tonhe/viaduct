package main

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/tui"
)

// Report mode's debug log records the alerts the TUI would have raised with
// the same thresholds. The thresholds reach the recorder through the options
// each report-mode function is given, not through package state (viaduct#170):
// two runs in one process with different thresholds each use their own.
func TestReportMode_AlertThresholdsArePassedExplicitly(t *testing.T) {
	// The scripted replies take 1ms. A latency threshold below that is
	// crossed in every round, and two rounds raise the alert.
	low := reportOpts{noASN: true, alertLatency: 500 * time.Microsecond, alertRounds: 2}
	high := reportOpts{noASN: true, alertLatency: time.Second, alertRounds: 2}
	off := low
	off.noAlert = true

	const rounds = 4
	alertLines := func(t *testing.T, opts reportOpts, multi bool, outputFmt string) []string {
		t.Helper()
		installScript(t, strayScript(rounds))
		sink := &lineSink{}
		cfg := scriptCfg("icmp", rounds, sink)
		target := net.ParseIP(scriptTarget)
		var err error
		captureStdout(t, func() {
			if multi {
				specs := []tui.TargetSpec{
					{Name: "a.example", IP: target, Cfg: cfg, ProtocolName: "icmp"},
					{Name: "b.example", IP: target, Cfg: cfg, ProtocolName: "icmp"},
				}
				err = runMultiReport(specs, "icmp", opts, outputFmt)
				return
			}
			err = runReport("target.example", target, cfg, "icmp", opts, outputFmt)
		})
		if err != nil {
			t.Fatalf("report mode: %v", err)
		}
		sink.mu.Lock()
		defer sink.mu.Unlock()
		var out []string
		for _, l := range sink.lines {
			if strings.Contains(l, "alert raised") || strings.Contains(l, "alert cleared") {
				out = append(out, l)
			}
		}
		return out
	}

	for _, tc := range []struct {
		name      string
		multi     bool
		outputFmt string
		targets   int
	}{
		{"one target, text", false, "", 1},
		{"one target, json", false, "json", 1},
		{"two targets, text", true, "", 2},
		{"two targets, json", true, "json", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := alertLines(t, low, tc.multi, tc.outputFmt)
			if len(got) != tc.targets {
				t.Fatalf("threshold below the reply time: %d alert lines, want %d:\n%s", len(got), tc.targets, strings.Join(got, "\n"))
			}
			for _, l := range got {
				if !strings.Contains(l, "round 2: alert raised: ") {
					t.Errorf("alert line %q, want it raised in round 2, after alertRounds bad rounds", l)
				}
			}
			// The next run in this process has its own thresholds.
			if got := alertLines(t, high, tc.multi, tc.outputFmt); len(got) != 0 {
				t.Errorf("threshold above the reply time: alert lines %q, want none", got)
			}
			if got := alertLines(t, off, tc.multi, tc.outputFmt); len(got) != 0 {
				t.Errorf("alerting off: alert lines %q, want none", got)
			}
			if got := alertLines(t, reportOpts{noASN: true}, tc.multi, tc.outputFmt); len(got) != 0 {
				t.Errorf("no thresholds: alert lines %q, want none", got)
			}
		})
	}
}
