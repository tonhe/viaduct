package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
)

// Seams of the sudo re-exec, replaced in tests.
var (
	// runSudo runs this program again under sudo with the same arguments and
	// waits for it.
	runSudo = reExecWithSudo
	geteuid = os.Geteuid
	// hostOS decides which advice the raw-socket error gives.
	hostOS = runtime.GOOS
)

// childExitError says the copy of via run under sudo exited with a non-zero
// status. The parent exits with the same status and prints nothing: whatever
// went wrong has been reported already, by the copy or by sudo.
type childExitError struct{ code int }

func (e *childExitError) Error() string {
	return fmt.Sprintf("re-executed under sudo: exit status %d", e.code)
}

// exitStatus is the process exit status for the error the root command
// returned: the re-executed copy's status when it failed, 1 for any other
// error.
func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	var child *childExitError
	if errors.As(err, &child) && child.code > 0 {
		return child.code
	}
	return 1
}

// rawSocketHint is the advice printed under a raw-socket permission error.
// File capabilities (setcap) exist on Linux only.
func rawSocketHint(goos string) string {
	if goos == "linux" {
		return "Run with sudo or set capability:\n  sudo setcap cap_net_raw+ep $(which via)"
	}
	return "Run with sudo:\n  sudo via <target>"
}

// rawSocketDenied decides what happens after the raw-socket check failed with
// sockErr.
//
// A process that is not root runs itself again under sudo. Then:
//
//   - the copy exited 0: nil, and the parent exits 0;
//   - sudo exited non-zero: a *childExitError carrying that status, and cobra
//     is told not to print it. sudo exits with the status of the command it
//     ran, and with 1 when it refuses (wrong password, prompt cancelled, user
//     not in sudoers). The two cannot be told apart by status, and in both the
//     failure is already on the terminal, from the copy or from sudo;
//   - sudo could not be run (not installed, not executable): the socket error
//     with the hint.
//
// Root gets the socket error with the hint; sudo would not change anything.
func rawSocketDenied(cmd *cobra.Command, sockErr error) error {
	if geteuid() != 0 {
		sudoErr := runSudo()
		if sudoErr == nil {
			return nil
		}
		var exitErr *exec.ExitError
		if errors.As(sudoErr, &exitErr) {
			cmd.SilenceErrors = true
			return &childExitError{code: exitErr.ExitCode()}
		}
	}
	return fmt.Errorf("raw socket permission denied: %w\n\n%s", sockErr, rawSocketHint(hostOS))
}

// icmpPathsWarning returns the warning for a --paths value that ICMP mode
// cannot honour, or "" when there is nothing to say. Only a --paths given on
// the command line is warned about: the value from the config file, or the
// built-in default, is not something the user asked for on this run.
func icmpPathsWarning(protocolName string, numPaths int, pathsChanged bool) string {
	if protocolName != "icmp" || !pathsChanged || numPaths <= 1 {
		return ""
	}
	return "Warning: ICMP mode does not support multipath discovery, ignoring --paths"
}

func reExecWithSudo() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command("sudo", append([]string{exe}, os.Args[1:]...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
