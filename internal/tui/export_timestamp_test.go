package tui

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

// The TUI's multi-target JSON export carries UTC timestamps throughout, as
// report mode does (viaduct#108 item 9).
func TestExportCmd_MultiTargetTimestampsAreUTC(t *testing.T) {
	if !inNonUTCZone(t) {
		return
	}
	t.Chdir(t.TempDir())

	m := newTestModel(hopsFull())
	m.sessions = append(m.sessions, newTestModel(hopsFull()).sessions[0])

	done, ok := m.exportCmd()().(ExportDoneMsg)
	if !ok || done.Err != nil {
		t.Fatalf("export: msg = %+v, want an ExportDoneMsg without an error", done)
	}
	raw, err := os.ReadFile(done.Path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Timestamp string `json:"timestamp"`
		Targets   []struct {
			Timestamp string `json:"timestamp"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the export: %v\n%s", err, raw)
	}
	if !strings.HasSuffix(doc.Timestamp, "Z") {
		t.Errorf("envelope timestamp = %q, want it to end in Z", doc.Timestamp)
	}
	if len(doc.Targets) != 2 {
		t.Fatalf("%d targets exported, want 2", len(doc.Targets))
	}
	for i, tg := range doc.Targets {
		if !strings.HasSuffix(tg.Timestamp, "Z") {
			t.Errorf("target %d timestamp = %q, want it to end in Z", i, tg.Timestamp)
		}
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
