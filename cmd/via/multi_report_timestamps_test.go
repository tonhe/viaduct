package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

// In multi-target JSON every timestamp is UTC: the envelope's, a traced
// target's and a failed target's (viaduct#108 item 9). They used to be UTC
// for the envelope and local time for each target.
func TestRunMultiReport_JSONTimestampsAreAllUTC(t *testing.T) {
	if !inNonUTCZone(t) {
		return
	}

	installStubTracer(t, buildV4Results(), nil)
	out := captureStdout(t, func() { runMultiReport(mixedSpecs(), "icmp", reportOpts{noASN: true}, "json") })

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

// inNonUTCZone runs the calling test again in a child process whose time zone
// is not UTC, and reports whether this process is that child. The parent
// returns false after the child has passed or failed. The zone is set through
// the TZ variable of a new process because time.Local is process-wide: a test
// that assigns to it races with every goroutine that reads the clock.
// Windows takes its zone from the system and ignores TZ, so there the child
// runs in the host's zone.
func inNonUTCZone(t *testing.T) bool {
	t.Helper()
	const marker = "VIA_TEST_ZONE_CHILD"
	if os.Getenv(marker) != "" {
		if _, offset := time.Now().Zone(); offset == 0 && runtime.GOOS != "windows" {
			t.Fatal("fixture: the child process is in UTC, want a zone with an offset")
		}
		return true
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), "TZ=Asia/Kolkata", marker+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the test failed in a non-UTC zone: %v\n%s", err, out)
	}
	return false
}
