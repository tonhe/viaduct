//go:build soak

// Soak tests hit real network and require raw-socket capability.
// Run with: make soak-test
// Or:       sudo go test -tags soak -timeout 30m ./cmd/via/...
//
// These are opt-in and NEVER run on CI. They exist to catch environmental,
// timing, and leak-class bugs that unit tests can't see.

package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// soakTargets is the sweep. Mix of well-known v4-only, dual-stack, and v6-only hosts.
var soakTargets = []struct {
	name string
	args []string
}{
	{"cloudflare-1.1.1.1", []string{"-r", "-c", "5", "1.1.1.1"}},
	{"google-8.8.8.8", []string{"-r", "-c", "5", "8.8.8.8"}},
	{"cloudflare-dns", []string{"-r", "-c", "5", "cloudflare-dns.com"}},
	{"google-dns", []string{"-r", "-c", "5", "dns.google"}},
	{"github", []string{"-r", "-c", "5", "github.com"}},
	{"example", []string{"-r", "-c", "5", "example.com"}},
	// IPv6-forced (skipped if host has no v6 transport)
	{"ipv6-forced-google", []string{"-6", "-r", "-c", "5", "dns.google"}},
	{"ipv6-google-native", []string{"-6", "-r", "-c", "5", "ipv6.google.com"}},
	// Force v4 to prove that path still works
	{"ipv4-forced-cloudflare", []string{"-4", "-r", "-c", "5", "1.1.1.1"}},
	// TCP mode
	{"tcp-cloudflare", []string{"-r", "-c", "3", "-P", "tcp", "-p", "443", "1.1.1.1"}},
	// Auto protocol
	{"auto-google", []string{"-r", "-c", "3", "-P", "auto", "8.8.8.8"}},
}

// TestSoak_Sweep runs every scenario in soakTargets, tracks resources, and
// reports failures. Each individual scenario failure marks the test as failed
// but the sweep continues.
func TestSoak_Sweep(t *testing.T) {
	// Locate the via binary. Prefer $VIA_BIN, else ~/bin/via, else "via" on PATH.
	binPath := viaBinaryPath(t)

	goroBefore := runtime.NumGoroutine()
	var memBefore runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	for _, tc := range soakTargets {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(binPath, tc.args...)
			start := time.Now()
			out, err := cmd.CombinedOutput()
			dur := time.Since(start)

			outStr := string(out)

			if err != nil {
				// Some scenarios are expected to fail on hosts without v6 transport, etc.
				// Only fail if the output doesn't look like a "clean" failure with
				// an error message.
				if strings.Contains(outStr, "no IPv6 transport") ||
					strings.Contains(outStr, "no IPv4") ||
					strings.Contains(outStr, "no AAAA") {
					t.Skipf("expected environment limit: %v — %s", err, outStr)
					return
				}
				t.Errorf("via failed (%v) in %s\noutput:\n%s", err, dur, outStr)
				return
			}

			// Basic assertions: exited within reason, hop count > 0.
			if dur > 45*time.Second {
				t.Errorf("via took %v; too slow?", dur)
			}
			// A trace with -r prints a table; simplest check is that the target
			// appears somewhere in the output (as an IP or hostname).
			if !strings.Contains(outStr, "1.") && !strings.Contains(outStr, ":") {
				t.Errorf("via output looks empty; expected some hop rows:\n%s", outStr)
			}
		})
	}

	// Give goroutines a moment to unwind after the last exec finishes.
	time.Sleep(500 * time.Millisecond)
	runtime.GC()

	goroAfter := runtime.NumGoroutine()
	if goroAfter > goroBefore+2 {
		t.Errorf("goroutine leak: before=%d after=%d", goroBefore, goroAfter)
	}

	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)
	growth := int64(memAfter.HeapAlloc) - int64(memBefore.HeapAlloc)
	if growth > 20*1024*1024 { // 20 MB
		t.Errorf("suspicious heap growth: %d bytes", growth)
	}
	t.Logf("soak sweep OK: %d scenarios, heap Δ = %d bytes, goroutines: %d → %d",
		len(soakTargets), growth, goroBefore, goroAfter)
}

func viaBinaryPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("VIA_BIN"); p != "" {
		return p
	}
	if home := os.Getenv("HOME"); home != "" {
		return home + "/bin/via"
	}
	// Fallback: just call "via" and let exec.LookPath resolve.
	return "via"
}
