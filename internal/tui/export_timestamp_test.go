package tui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// The TUI's multi-target JSON export carries UTC timestamps throughout, as
// report mode does (viaduct#108 item 9).
func TestExportCmd_MultiTargetTimestampsAreUTC(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("test", 9*3600)
	t.Cleanup(func() { time.Local = saved })
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
