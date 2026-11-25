package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/probe"
)

func TestDebugLog_PrintfWritesOneTimestampedLine(t *testing.T) {
	var buf bytes.Buffer
	d := newDebugLog(&buf)
	d.now = func() time.Time { return time.Date(2026, 11, 25, 4, 38, 44, 123e6, time.UTC) }
	d.Printf("[%s] send failed: %v", "2001:db8::1", "i/o timeout")
	want := "2026-11-25T04:38:44.123Z [2001:db8::1] send failed: i/o timeout\n"
	if got := buf.String(); got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
}

// lockedBuffer is a bytes.Buffer that reports interleaved writes: the log
// must make each line one Write.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func TestDebugLog_ConcurrentTracersDoNotInterleave(t *testing.T) {
	var out lockedBuffer
	d := newDebugLog(&out)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				d.Printf("[target-%d] round %d: probes not sent", g, i)
			}
		}(g)
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(out.buf.String(), "\n"), "\n")
	if len(lines) != 400 {
		t.Fatalf("%d lines, want 400", len(lines))
	}
	for _, l := range lines {
		if !strings.HasSuffix(l, ": probes not sent") || strings.Count(l, "[target-") != 1 {
			t.Fatalf("interleaved or malformed line: %q", l)
		}
	}
}

func TestOpenDebugLog_AppendsAndCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "via-debug.log")
	for _, msg := range []string{"first run", "second run"} {
		d, closeLog, err := openDebugLog(path)
		if err != nil {
			t.Fatalf("openDebugLog: %v", err)
		}
		d.Printf("%s", msg)
		closeLog()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " first run") || !strings.HasSuffix(lines[1], " second run") {
		t.Errorf("log = %q, want the two runs' lines, the second appended", string(data))
	}
}

func TestOpenDebugLog_UnwritablePathIsAnError(t *testing.T) {
	_, _, err := openDebugLog(filepath.Join(t.TempDir(), "no-such-dir", "via-debug.log"))
	if err == nil {
		t.Fatal("no error for a debug log in a directory that does not exist")
	}
	if !strings.Contains(err.Error(), "debug log") {
		t.Errorf("error = %q, want it to say it is about the debug log", err)
	}
}

// The --debug-log flag reaches every target's probe config.
func TestApplyDebugLog_SetsDebugfOnTheBaseConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "via-debug.log")
	var cfg probe.Config
	closeLog, err := applyDebugLog(&cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Debugf == nil {
		t.Fatal("Debugf not set")
	}
	cfg.Debugf("[%s] round %d", "192.0.2.1", 3)
	closeLog()
	data, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(data), " [192.0.2.1] round 3\n") {
		t.Errorf("log = %q, want the line written through Debugf", string(data))
	}

	// No flag: no log, nothing to close, no error.
	var plain probe.Config
	closeLog, err = applyDebugLog(&plain, "")
	if err != nil || plain.Debugf != nil {
		t.Errorf("without --debug-log: err = %v, Debugf set = %v; want nil, false", err, plain.Debugf != nil)
	}
	closeLog()

	if _, err := applyDebugLog(&plain, filepath.Join(t.TempDir(), "missing", "x.log")); err == nil {
		t.Error("no error for an unwritable --debug-log path")
	}
}
