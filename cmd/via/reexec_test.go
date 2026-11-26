package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tonhe/viaduct/internal/probe"
)

// TestHelperExit is not a test. Run as a child process with VIA_TEST_EXIT set,
// it exits with that status, which gives the tests a real *exec.ExitError.
func TestHelperExit(t *testing.T) {
	v := os.Getenv("VIA_TEST_EXIT")
	if v == "" {
		t.Skip("helper process only")
	}
	code, err := strconv.Atoi(v)
	if err != nil {
		os.Exit(99)
	}
	os.Exit(code)
}

// childExit runs this test binary as a child that exits with code and returns
// the error exec gives for it: nil for 0, an *exec.ExitError otherwise.
func childExit(t *testing.T, code int) error {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestHelperExit$")
	c.Env = append(os.Environ(), "VIA_TEST_EXIT="+strconv.Itoa(code))
	return c.Run()
}

// fakeReExec replaces the seams of the re-exec for one test.
func fakeReExec(t *testing.T, euid int, goos string, sudo func() error) *int {
	t.Helper()
	calls := 0
	prevSudo, prevEuid, prevOS := runSudo, geteuid, hostOS
	runSudo = func() error { calls++; return sudo() }
	geteuid = func() int { return euid }
	hostOS = goos
	t.Cleanup(func() { runSudo, geteuid, hostOS = prevSudo, prevEuid, prevOS })
	return &calls
}

// executeDenied runs rawSocketDenied inside a cobra command, the way runTrace
// does, and returns what cobra printed, the error Execute returned, and the
// exit status main would use for it.
func executeDenied(t *testing.T, sockErr error) (stderr string, err error, status int) {
	t.Helper()
	var buf bytes.Buffer
	root := &cobra.Command{
		Use:          "via",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rawSocketDenied(cmd, sockErr)
		},
	}
	root.SetArgs(nil)
	root.SetOut(&buf)
	root.SetErr(&buf)
	err = root.Execute()
	return buf.String(), err, exitStatus(err)
}

func TestRawSocketDenied_ReExecOutcome(t *testing.T) {
	sockErr := errors.New("listen ip4:icmp 0.0.0.0: socket: operation not permitted")

	tests := []struct {
		name       string
		euid       int
		sudo       func(t *testing.T) error
		wantCalls  int
		wantStatus int
		wantDenied bool // the parent prints its own "permission denied" error
	}{
		{
			name:       "child exits 0",
			euid:       1000,
			sudo:       func(t *testing.T) error { return childExit(t, 0) },
			wantCalls:  1,
			wantStatus: 0,
		},
		{
			name:       "child exits 3",
			euid:       1000,
			sudo:       func(t *testing.T) error { return childExit(t, 3) },
			wantCalls:  1,
			wantStatus: 3,
		},
		{
			name:       "child exits 1, as does sudo when it refuses",
			euid:       1000,
			sudo:       func(t *testing.T) error { return childExit(t, 1) },
			wantCalls:  1,
			wantStatus: 1,
		},
		{
			name: "sudo not found",
			euid: 1000,
			sudo: func(*testing.T) error {
				return &exec.Error{Name: "sudo", Err: exec.ErrNotFound}
			},
			wantCalls:  1,
			wantStatus: 1,
			wantDenied: true,
		},
		{
			name:       "sudo cannot be started",
			euid:       1000,
			sudo:       func(*testing.T) error { return errors.New("fork/exec /usr/bin/sudo: permission denied") },
			wantCalls:  1,
			wantStatus: 1,
			wantDenied: true,
		},
		{
			name:       "already root: no re-exec",
			euid:       0,
			sudo:       func(t *testing.T) error { t.Error("sudo run as root"); return nil },
			wantCalls:  0,
			wantStatus: 1,
			wantDenied: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := fakeReExec(t, tc.euid, "linux", func() error { return tc.sudo(t) })

			stderr, err, status := executeDenied(t, sockErr)

			if *calls != tc.wantCalls {
				t.Errorf("sudo runs = %d, want %d", *calls, tc.wantCalls)
			}
			if status != tc.wantStatus {
				t.Errorf("exit status = %d, want %d (err: %v)", status, tc.wantStatus, err)
			}
			if tc.wantDenied {
				if !strings.Contains(stderr, "raw socket permission denied") || !strings.Contains(stderr, sockErr.Error()) {
					t.Errorf("parent did not report the socket error; printed %q", stderr)
				}
				return
			}
			if stderr != "" {
				t.Errorf("parent printed %q after sudo ran the child; want nothing", stderr)
			}
		})
	}
}

func TestExitStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"no error", nil, 0},
		{"ordinary error", errors.New("resolve host.example: no such host"), 1},
		{"child exit 3", &childExitError{code: 3}, 3},
		{"child exit 2, wrapped", errors.Join(errors.New("x"), &childExitError{code: 2}), 2},
		{"child killed by a signal", &childExitError{code: -1}, 1},
		{"child exit 0 reported as an error", &childExitError{code: 0}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitStatus(tc.err); got != tc.want {
				t.Errorf("exitStatus = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRawSocketHint_SetcapOnLinuxOnly(t *testing.T) {
	tests := []struct {
		goos       string
		wantSetcap bool
	}{
		{"linux", true},
		{"darwin", false},
		{"windows", false},
		{"freebsd", false},
	}
	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			hint := rawSocketHint(tc.goos)
			if got := strings.Contains(hint, "setcap"); got != tc.wantSetcap {
				t.Errorf("hint for %s mentions setcap = %v, want %v\n%s", tc.goos, got, tc.wantSetcap, hint)
			}
			if !strings.Contains(hint, "sudo") {
				t.Errorf("hint for %s does not mention sudo:\n%s", tc.goos, hint)
			}

			// The same through the error the CLI prints.
			fakeReExec(t, 0, tc.goos, func() error { return nil })
			stderr, _, _ := executeDenied(t, errors.New("socket: operation not permitted"))
			if got := strings.Contains(stderr, "setcap"); got != tc.wantSetcap {
				t.Errorf("CLI error on %s mentions setcap = %v, want %v\n%s", tc.goos, got, tc.wantSetcap, stderr)
			}
		})
	}
}

func TestICMPPathsWarning(t *testing.T) {
	tests := []struct {
		name         string
		protocol     string
		numPaths     int
		pathsChanged bool
		wantWarning  bool
	}{
		{"icmp, --paths 6 given", "icmp", 6, true, true},
		{"icmp, paths from config or the default", "icmp", 6, false, false},
		{"icmp, --paths 1 given", "icmp", 1, true, false},
		{"udp, --paths 6 given", "udp", 6, true, false},
		{"tcp, --paths 6 given", "tcp", 6, true, false},
		{"auto, --paths 6 given", "auto", 6, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := icmpPathsWarning(tc.protocol, tc.numPaths, tc.pathsChanged)
			if (got != "") != tc.wantWarning {
				t.Errorf("warning = %q, want one: %v", got, tc.wantWarning)
			}
			if tc.wantWarning && !strings.Contains(got, "--paths") {
				t.Errorf("warning %q does not name --paths", got)
			}
		})
	}
}

// buildProtocol still turns multipath off for ICMP; it no longer prints.
func TestBuildProtocol_ICMPSinglePathSilently(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w
	numPaths := 6
	_, buildErr := buildProtocol("icmp", 0, &numPaths, probe.Config{NumPaths: numPaths})
	os.Stderr = prev
	w.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	if buildErr != nil {
		t.Fatalf("buildProtocol: %v", buildErr)
	}
	if numPaths != 1 {
		t.Errorf("numPaths = %d, want 1", numPaths)
	}
	if out.Len() != 0 {
		t.Errorf("buildProtocol printed %q, want nothing", out.String())
	}
}
