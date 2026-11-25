package main

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/probe"
	"github.com/tonhe/viaduct/internal/tui"
)

// Report mode writes the debug log's round records: a line per round and a
// final per-hop snapshot, for each target, through both report paths.

type roundLogSink struct{ lines []string }

func (s *roundLogSink) logf(format string, args ...any) {
	s.lines = append(s.lines, fmt.Sprintf(format, args...))
}

func (s *roundLogSink) count(substr string) int {
	n := 0
	for _, l := range s.lines {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

func reportDebugCfg(sink *roundLogSink) probe.Config {
	cfg := probe.Config{
		MaxHops:    30,
		FirstTTL:   1,
		Timeout:    100 * time.Millisecond,
		MaxRounds:  1,
		NumPaths:   1,
		IPVersion:  4,
		ReportMode: true,
		NoDNS:      true,
	}
	cfg.Protocol = probe.NewICMPProtocol(cfg)
	if sink != nil {
		cfg.Debugf = sink.logf
	}
	return cfg
}

func TestReportMode_DebugLogRoundRecords(t *testing.T) {
	for _, tc := range []struct {
		name      string
		outputFmt string // "" is the text report, "json" the export path
	}{
		{"text report", ""},
		{"json export", "json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installStubTracer(t, buildV4Results(), nil)
			sink := &roundLogSink{}
			cfg := reportDebugCfg(sink)
			specs := []tui.TargetSpec{
				{Name: "192.0.2.4", IP: net.ParseIP("192.0.2.4").To4(), Cfg: cfg, ProtocolName: "icmp"},
				{Name: "198.51.100.4", IP: net.ParseIP("198.51.100.4").To4(), Cfg: cfg, ProtocolName: "icmp"},
			}
			captureStdout(t, func() {
				if err := runMultiReport(specs, "icmp", true, tc.outputFmt); err != nil {
					t.Errorf("runMultiReport: %v", err)
				}
			})
			for _, want := range []string{
				"[192.0.2.4] round 1: took ",
				"[198.51.100.4] round 1: took ",
				"[192.0.2.4] final round=1 ttl=4 nodes=192.0.2.4 loss=0.0% kind=measured icmp-ttl=0/0 echo=0/0 echo-counted=0/0 rate-limited=undetermined",
				"[198.51.100.4] final round=1 ttl=4 nodes=192.0.2.4 loss=0.0% kind=measured icmp-ttl=0/0 echo=0/0 echo-counted=0/0 rate-limited=undetermined",
			} {
				if got := sink.count(want); got != 1 {
					t.Errorf("%d lines containing %q, want 1:\n%s", got, want, strings.Join(sink.lines, "\n"))
				}
			}
			// One final line per hop, per target.
			if got := sink.count(" final "); got != 8 {
				t.Errorf("%d final lines, want 8", got)
			}
		})
	}
}

// Without --debug-log there is no recorder.
func TestNewReportDebug_OffWithoutDebugLog(t *testing.T) {
	if d := newReportDebug(net.ParseIP("192.0.2.4"), reportDebugCfg(nil)); d != nil {
		t.Errorf("recorder without a debug log = %v, want nil", d)
	}
	// A config with no protocol set must not panic.
	sink := &roundLogSink{}
	if d := newReportDebug(net.ParseIP("192.0.2.4"), probe.Config{Debugf: sink.logf}); d == nil {
		t.Error("no recorder with a debug log")
	}
}
