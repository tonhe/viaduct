package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// In multi-target JSON every timestamp is UTC: the envelope's, a traced
// target's and a failed target's (viaduct#108 item 9). They used to be UTC
// for the envelope and local time for each target.
func TestRunMultiReport_JSONTimestampsAreAllUTC(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("test", -(7*3600 + 30*60))
	t.Cleanup(func() { time.Local = saved })

	installStubTracer(t, buildV4Results(), nil)
	out := captureStdout(t, func() { runMultiReport(mixedSpecs(), "icmp", true, "json") })

	var doc struct {
		Timestamp string `json:"timestamp"`
		Targets   []struct {
			Target    string `json:"target"`
			Timestamp string `json:"timestamp"`
			Error     string `json:"error"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not the multi-target JSON: %v\n%s", err, out)
	}
	if !strings.HasSuffix(doc.Timestamp, "Z") {
		t.Errorf("envelope timestamp = %q, want it to end in Z", doc.Timestamp)
	}
	if len(doc.Targets) != 2 {
		t.Fatalf("%d targets in the output, want 2", len(doc.Targets))
	}
	sawFailed := false
	for _, tg := range doc.Targets {
		if tg.Error != "" {
			sawFailed = true
		}
		if !strings.HasSuffix(tg.Timestamp, "Z") {
			t.Errorf("target %s (error %q): timestamp = %q, want it to end in Z", tg.Target, tg.Error, tg.Timestamp)
		}
	}
	if !sawFailed {
		t.Error("fixture: no failed target in the output")
	}
}
