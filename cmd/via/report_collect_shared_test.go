package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/tonhe/viaduct/internal/probe"
	"github.com/tonhe/viaduct/internal/tui"
)

// Single-target report mode and multi-target export both collect a trace
// through collectReport (viaduct#108 item 13). These tests hold the two
// callers to the same result for the same trace, and to their own way of
// reporting a tracer that fails.

// failingTracer is a tracer whose run ends in an error.
type failingTracer struct {
	stubTracer
	err error
}

func (f *failingTracer) Run(context.Context, chan<- probe.Result) error { return f.err }

func installFailingTracer(t *testing.T, err error) {
	t.Helper()
	orig := newTracer
	newTracer = func(net.IP, probe.Config) tracerIface { return &failingTracer{err: err} }
	t.Cleanup(func() { newTracer = orig })
}

// exportedHops decodes the "hops" of a single-target JSON report.
func exportedHops(t *testing.T, raw json.RawMessage) []any {
	t.Helper()
	var doc struct {
		Hops []any `json:"hops"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing a report: %v\n%s", err, raw)
	}
	if len(doc.Hops) == 0 {
		t.Fatalf("fixture: the report has no hops:\n%s", raw)
	}
	return doc.Hops
}

func TestReportCallers_SameTraceSameHops(t *testing.T) {
	const rounds = 3
	target := net.ParseIP(scriptTarget)

	installScript(t, strayScript(rounds))
	single := captureStdout(t, func() {
		if err := runReport("target.example", target, scriptCfg("icmp", rounds, nil), "icmp", reportOpts{noASN: true}, "json"); err != nil {
			t.Errorf("runReport: %v", err)
		}
	})

	installScript(t, strayScript(rounds))
	// Two targets: with one, runMultiReport hands over to the single-target path.
	specs := []tui.TargetSpec{
		{Name: "target.example", IP: target, Cfg: scriptCfg("icmp", rounds, nil), ProtocolName: "icmp"},
		{Name: "other.example", IP: target, Cfg: scriptCfg("icmp", rounds, nil), ProtocolName: "icmp"},
	}
	multi := captureStdout(t, func() {
		if err := runMultiReport(specs, "icmp", reportOpts{noASN: true}, "json"); err != nil {
			t.Errorf("runMultiReport: %v", err)
		}
	})
	var envelope struct {
		Targets []json.RawMessage `json:"targets"`
	}
	if err := json.Unmarshal([]byte(multi), &envelope); err != nil {
		t.Fatalf("parsing the multi-target output: %v\n%s", err, multi)
	}
	if len(envelope.Targets) != 2 {
		t.Fatalf("%d targets in the multi-target output, want 2", len(envelope.Targets))
	}

	want := exportedHops(t, json.RawMessage(single))
	for i, tg := range envelope.Targets {
		if got := exportedHops(t, tg); !reflect.DeepEqual(got, want) {
			t.Errorf("target %d: the two report paths disagree on the same trace:\nsingle: %s\nmulti:  %s", i, single, tg)
		}
	}
}

func TestReportCallers_TracerError(t *testing.T) {
	boom := errors.New("socket: operation not permitted")
	target := net.ParseIP("192.0.2.4").To4()
	cfg := reportDebugCfg(nil)

	// Single target: the run fails, the error names the tracer and wraps the cause.
	installFailingTracer(t, boom)
	var err error
	tableReturned := false
	out := captureStdout(t, func() {
		tbl, e := runReportInner("192.0.2.4", target, cfg, "icmp", reportOpts{noASN: true}, "json")
		tableReturned, err = tbl != nil, e
	})
	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "tracer error: ") {
		t.Errorf("runReportInner error = %v, want it to begin \"tracer error: \" and wrap the tracer's error", err)
	}
	if tableReturned {
		t.Error("runReportInner returned a table with a failed tracer")
	}
	if out != "" {
		t.Errorf("a failed trace wrote a report: %q", out)
	}

	// Multi target: the target is recorded as failed with the tracer's own text.
	installFailingTracer(t, boom)
	specs := []tui.TargetSpec{
		{Name: "192.0.2.4", IP: target, Cfg: cfg, ProtocolName: "icmp"},
		{Name: "198.51.100.4", IP: net.ParseIP("198.51.100.4").To4(), Cfg: cfg, ProtocolName: "icmp"},
	}
	var multiErr error
	multi := captureStdout(t, func() { multiErr = runMultiReport(specs, "icmp", reportOpts{noASN: true}, "json") })
	if multiErr == nil {
		t.Error("runMultiReport returned no error with every target failed")
	}
	var doc struct {
		Targets []struct {
			Error string `json:"error"`
		} `json:"targets"`
	}
	if jerr := json.Unmarshal([]byte(multi), &doc); jerr != nil {
		t.Fatalf("parsing the multi-target output: %v\n%s", jerr, multi)
	}
	if len(doc.Targets) != 2 {
		t.Fatalf("%d targets in the multi-target output, want 2", len(doc.Targets))
	}
	for i, tg := range doc.Targets {
		if tg.Error != boom.Error() {
			t.Errorf("target %d error = %q, want the tracer's own text %q", i, tg.Error, boom.Error())
		}
	}
}
