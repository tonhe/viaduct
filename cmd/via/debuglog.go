package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/tonhe/viaduct/internal/probe"
	"github.com/tonhe/viaduct/internal/tui"
)

// debugLog is the sink for the debug lines (--debug-log). The probe engine
// writes send failures, once per address, and the probes of each round that
// did not leave. The round recorder (tui.RoundDebug) writes a line per
// completed round, a per-hop snapshot every 10 rounds and at exit, and alerts
// raised and cleared. It goes to a file because the TUI owns the terminal.
// One log is shared by every target, so writes are serialized.
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

// reportAlert holds the alert thresholds report mode's debug-log recorder
// uses: the ones the TUI would alert on. run sets it from the flags and the
// config file. Report mode itself shows no alerts.
var reportAlert struct {
	loss    float64
	latency time.Duration
	rounds  int
	off     bool
}

// newReportDebug returns the debug log's round recorder for one report-mode
// target, or nil without --debug-log.
func newReportDebug(targetIP net.IP, cfg probe.Config) *tui.RoundDebug {
	protocol := ""
	if cfg.Protocol != nil {
		protocol = cfg.Protocol.Name()
	}
	return tui.NewReportDebug(cfg.Debugf, targetIP.String(), protocol,
		reportAlert.loss, reportAlert.latency, reportAlert.rounds, reportAlert.off)
}
