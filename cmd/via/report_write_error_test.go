package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// closedStdout swaps os.Stdout for a file that is already closed, so every
// write to it fails, and puts the real one back when the test ends.
func closedStdout(t *testing.T) {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "report"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = f
	t.Cleanup(func() { os.Stdout = orig })
}

// Single-target report mode fails when the export cannot be written, in each
// format: `via -r -o csv target > file` on a full disk used to exit 0 with an
// empty or cut-short file (viaduct#284).
func TestRunReport_FailsWhenTheExportCannotBeWritten(t *testing.T) {
	const rounds = 2
	for _, format := range []string{"json", "csv", "dot"} {
		t.Run(format, func(t *testing.T) {
			installScript(t, strayScript(rounds))
			closedStdout(t)

			err := runReport("target.example", net.ParseIP(scriptTarget), scriptCfg("icmp", rounds, nil), "icmp", true, format)
			if err == nil {
				t.Fatalf("runReport returned nil though the %s report could not be written", format)
			}
			if !errors.Is(err, os.ErrClosed) {
				t.Errorf("error = %v, want it to wrap the write error (%v)", err, os.ErrClosed)
			}
			if want := "writing the " + format + " report"; !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to say %q", err, want)
			}
		})
	}
}

// The same holds through runMultiReport with one target, which is the path
// the command takes: its error is what main turns into a non-zero exit.
func TestRunMultiReport_OneTargetFailsWhenTheExportCannotBeWritten(t *testing.T) {
	for _, format := range []string{"json", "csv", "dot"} {
		t.Run(format, func(t *testing.T) {
			installStubTracer(t, buildV4Results(), nil)
			closedStdout(t)

			specs := mixedSpecs()[:1]
			if specs[0].Err != nil {
				t.Fatalf("the first spec of mixedSpecs is not a healthy one: %v", specs[0].Err)
			}
			if err := runMultiReport(specs, "icmp", true, format); !errors.Is(err, os.ErrClosed) {
				t.Errorf("runMultiReport = %v, want an error wrapping %v", err, os.ErrClosed)
			}
		})
	}
}

// A report that is written is still a success, and the bytes are a whole
// report in the format asked for.
func TestRunReport_WritesTheExportAndSucceeds(t *testing.T) {
	const rounds = 2
	starts := map[string]string{"json": "{", "csv": "ttl,", "dot": "digraph"}
	for _, format := range []string{"json", "csv", "dot"} {
		t.Run(format, func(t *testing.T) {
			installScript(t, strayScript(rounds))
			var runErr error
			out := captureStdout(t, func() {
				runErr = runReport("target.example", net.ParseIP(scriptTarget), scriptCfg("icmp", rounds, nil), "icmp", true, format)
			})
			if runErr != nil {
				t.Fatalf("runReport: %v", runErr)
			}
			if !bytes.HasPrefix([]byte(out), []byte(starts[format])) {
				t.Errorf("%s output starts %q, want it to start with %q", format, firstLine(out), starts[format])
			}
		})
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
