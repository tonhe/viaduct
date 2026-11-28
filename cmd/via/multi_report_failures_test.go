package main

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/tonhe/viaduct/internal/export"
	"github.com/tonhe/viaduct/internal/probe"
	"github.com/tonhe/viaduct/internal/tui"
)

// Multi-target report mode with a target that failed. The run exits non-zero
// (runMultiReport returns an error), the targets that worked are still
// reported, and structured output keeps the failed target with its error.

func mixedSpecs() []tui.TargetSpec {
	cfg := probe.Config{MaxHops: 4, FirstTTL: 1, MaxRounds: 1, NumPaths: 1, IPVersion: 4}
	cfg.Protocol = probe.NewICMPProtocol(cfg)
	return []tui.TargetSpec{
		{Name: "good.example", IP: net.ParseIP("192.0.2.4"), Cfg: cfg, ProtocolName: "icmp"},
		{Name: "bad.example", Err: errors.New("resolve bad.example: no such host"), ProtocolName: "icmp"},
	}
}

func TestRunMultiReport_JSONKeepsFailedTargetsAndFails(t *testing.T) {
	installStubTracer(t, buildV4Results(), nil)
	var runErr error
	out := captureStdout(t, func() { runErr = runMultiReport(mixedSpecs(), "icmp", reportOpts{noASN: true}, "json") })

	if runErr == nil {
		t.Error("runMultiReport returned nil with a failed target; the command must exit non-zero")
	} else if !strings.Contains(runErr.Error(), "1 of 2") {
		t.Errorf("error = %q, want it to say 1 of 2 targets failed", runErr)
	}

	var mr export.MultiReport
	if err := json.Unmarshal([]byte(out), &mr); err != nil {
		t.Fatalf("output is not the multi-target JSON: %v\n%s", err, out)
	}
	if len(mr.Targets) != 2 {
		t.Fatalf("targets = %d, want 2: the failed target stays in the output", len(mr.Targets))
	}
	good, bad := mr.Targets[0], mr.Targets[1]
	if good.Target != "good.example" || good.Error != "" || len(good.Hops) == 0 {
		t.Errorf("good target = %q error %q hops %d; want its trace and no error", good.Target, good.Error, len(good.Hops))
	}
	if bad.Target != "bad.example" || !strings.Contains(bad.Error, "no such host") || len(bad.Hops) != 0 {
		t.Errorf("bad target = %q error %q hops %d; want the name, the error and no hops", bad.Target, bad.Error, len(bad.Hops))
	}
}

func TestRunMultiReport_AllFailedIsAnEmptyListNotNull(t *testing.T) {
	specs := mixedSpecs()[1:]
	specs = append(specs, tui.TargetSpec{Name: "worse.example", Err: errors.New("resolve worse.example: no such host")})
	var runErr error
	out := captureStdout(t, func() { runErr = runMultiReport(specs, "icmp", reportOpts{noASN: true}, "json") })
	if runErr == nil {
		t.Error("runMultiReport returned nil when every target failed")
	}
	if strings.Contains(out, `"targets": null`) {
		t.Errorf("targets is null; want the failed targets listed:\n%s", out)
	}
	var mr export.MultiReport
	if err := json.Unmarshal([]byte(out), &mr); err != nil || len(mr.Targets) != 2 {
		t.Errorf("targets = %d (err %v), want both failed targets", len(mr.Targets), err)
	}
}

func TestRunMultiReport_TextModeFailsAndStillReportsTheRest(t *testing.T) {
	installStubTracer(t, buildV4Results(), nil)
	var runErr error
	out := captureStdout(t, func() { runErr = runMultiReport(mixedSpecs(), "icmp", reportOpts{noASN: true}, "") })
	if runErr == nil {
		t.Error("runMultiReport returned nil with a failed target in text mode")
	}
	if !strings.Contains(out, "good.example") || !strings.Contains(out, "bad.example") {
		t.Errorf("text output should have a section for both targets:\n%s", out)
	}
}

func TestRunMultiReport_AllHealthySucceeds(t *testing.T) {
	installStubTracer(t, buildV4Results(), nil)
	specs := mixedSpecs()[:1]
	specs = append(specs, specs[0])
	specs[1].Name = "also-good.example"
	var runErr error
	captureStdout(t, func() { runErr = runMultiReport(specs, "icmp", reportOpts{noASN: true}, "json") })
	if runErr != nil {
		t.Errorf("runMultiReport = %v with two healthy targets, want nil", runErr)
	}
}
