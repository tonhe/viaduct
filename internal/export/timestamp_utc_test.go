package export

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/tonhe/viaduct/internal/hop"
)

// A report's timestamp is in UTC whatever the host's time zone, so that every
// timestamp in a multi-target export is in the same zone as the envelope's
// (viaduct#108 item 9).
func TestBuildReport_TimestampIsUTC(t *testing.T) {
	if !inNonUTCZone(t) {
		return
	}

	before := time.Now()
	r := BuildReport("v", "target.example", net.ParseIP("192.0.2.1"), "udp", hop.NewTable(30), nil, nil, 0)
	after := time.Now()

	if r.Timestamp.Location() != time.UTC {
		t.Errorf("timestamp zone = %v, want UTC", r.Timestamp.Location())
	}
	if r.Timestamp.Before(before) || r.Timestamp.After(after) {
		t.Errorf("timestamp %v is not the time the report was built (%v to %v)", r.Timestamp, before, after)
	}

	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(doc.Timestamp, "Z") {
		t.Errorf("JSON timestamp = %q, want it to end in Z", doc.Timestamp)
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
