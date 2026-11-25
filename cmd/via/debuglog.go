package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/tonhe/viaduct/internal/probe"
)

// debugLog is the sink for the probe engine's debug lines (--debug-log): send
// failures, once per address, and the probes of each round that did not
// leave. It goes to a file because the TUI owns the terminal. One log is
// shared by every target's tracer, so writes are serialized.
type debugLog struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

func newDebugLog(w io.Writer) *debugLog {
	return &debugLog{w: w, now: time.Now}
}

// Printf writes one timestamped line. It has the signature of
// probe.Config.Debugf.
func (d *debugLog) Printf(format string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fmt.Fprintf(d.w, "%s %s\n", d.now().Format("2006-01-02T15:04:05.000Z07:00"), fmt.Sprintf(format, args...))
}

// openDebugLog opens path for appending, creating it if needed, and returns
// the log and a function that closes the file.
func openDebugLog(path string) (*debugLog, func(), error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("debug log: %w", err)
	}
	return newDebugLog(f), func() { f.Close() }, nil
}

// applyDebugLog wires --debug-log into the probe config every target is
// traced with. With no path it does nothing. The returned function closes
// the log.
func applyDebugLog(cfg *probe.Config, path string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	d, closeLog, err := openDebugLog(path)
	if err != nil {
		return nil, err
	}
	cfg.Debugf = d.Printf
	return closeLog, nil
}
