package probe

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// requireRawSocketsEnv names the environment variable that makes the
// real-socket tests mandatory. Those tests need root (or CAP_NET_RAW) and a
// loopback of both families, and skip on a host that lacks one of them, so
// that an unprivileged `go test ./...` passes. With the variable set to 1 the
// same conditions are failures: CI runs the tests as root with it set
// (`make test-root`, Linux and macOS), and a runner that cannot run one of
// them fails the job where it would otherwise report a skip as a pass.
const requireRawSocketsEnv = "VIA_REQUIRE_RAW_SOCKETS"

// skipUnlessRequired skips a real-socket test the host cannot run (no raw
// socket, no loopback of the family, a socket option the platform lacks), or
// fails it when requireRawSocketsEnv is 1.
func skipUnlessRequired(t testing.TB, format string, args ...any) {
	t.Helper()
	reason := fmt.Sprintf(format, args...)
	if os.Getenv(requireRawSocketsEnv) == "1" {
		t.Fatalf("%s; %s=1, so this test must run and may not skip", reason, requireRawSocketsEnv)
		return
	}
	t.Skip(reason)
}

// recordingTB stands in for the test being skipped or failed.
type recordingTB struct {
	testing.TB
	skipped []string
	failed  []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Skip(args ...any) { r.skipped = append(r.skipped, fmt.Sprint(args...)) }

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.failed = append(r.failed, fmt.Sprintf(format, args...))
}

func TestSkipUnlessRequired(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    string
		wantFail bool
	}{
		{"unset: skip", "", false},
		{"0: skip", "0", false},
		{"1: fail", "1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(requireRawSocketsEnv, tc.value)
			rec := &recordingTB{}
			skipUnlessRequired(rec, "no raw %s socket here", "ICMP")

			if tc.wantFail {
				if len(rec.skipped) != 0 || len(rec.failed) != 1 {
					t.Fatalf("skipped %q, failed %q; want one failure and no skip", rec.skipped, rec.failed)
				}
				for _, want := range []string{"no raw ICMP socket here", "VIA_REQUIRE_RAW_SOCKETS=1"} {
					if !strings.Contains(rec.failed[0], want) {
						t.Errorf("failure %q does not contain %q", rec.failed[0], want)
					}
				}
				return
			}
			if len(rec.failed) != 0 || len(rec.skipped) != 1 {
				t.Fatalf("skipped %q, failed %q; want one skip and no failure", rec.skipped, rec.failed)
			}
			if rec.skipped[0] != "no raw ICMP socket here" {
				t.Errorf("skip reason = %q", rec.skipped[0])
			}
		})
	}
}
