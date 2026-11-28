package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// isolateConfig points the config file lookup at an empty directory, so a
// test of runTrace reads the built-in defaults and not the developer's file.
func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("USERPROFILE", dir)
}

// A flag error is reported before the capability check, so an unprivileged
// run is not sent through sudo first (viaduct#108 item 4). The test passes
// with or without raw-socket privilege: with it the capability check passes
// and sudo is never wanted; without it, sudo must still not have been run.
func TestRunTrace_FlagErrorsComeBeforeTheSudoReExec(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"--no-paths with another --paths", []string{"--no-paths", "--paths", "3", "192.0.2.1"}, "--no-paths and --paths 3 contradict each other"},
		{"unknown output format", []string{"-o", "yaml", "192.0.2.1"}, `unknown export format "yaml"`},
		{"unknown protocol", []string{"-P", "sctp", "192.0.2.1"}, "unknown protocol: sctp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateConfig(t)
			sudoRuns := fakeReExec(t, 1000, "linux", func() error { return errors.New("sudo: not run in tests") })

			cmd := newRootCmd()
			cmd.SetArgs(tc.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()

			if *sudoRuns != 0 {
				t.Errorf("sudo was run %d time(s) before the flag error was reported", *sudoRuns)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestCheckFlags(t *testing.T) {
	cases := []struct {
		name           string
		args           []string
		configProtocol string
		want           string // "" = no error
	}{
		{"defaults", nil, "udp", ""},
		{"--no-paths alone", []string{"--no-paths"}, "udp", ""},
		{"--no-paths with --paths 1 agree", []string{"--no-paths", "--paths", "1"}, "udp", ""},
		{"--no-paths with --paths 3", []string{"--no-paths", "--paths", "3"}, "udp", "contradict"},
		{"--paths out of range is not an error here", []string{"--paths", "0"}, "udp", ""},
		{"known format", []string{"-o", "csv"}, "udp", ""},
		{"format alias", []string{"-o", "graphviz"}, "udp", ""},
		{"unknown format", []string{"-o", "xml"}, "udp", "unknown export format"},
		{"every protocol", []string{"-P", "tcp"}, "udp", ""},
		{"unknown protocol on the command line", []string{"-P", "sctp"}, "udp", "unknown protocol: sctp"},
		{"the config's protocol is checked when -P is absent", nil, "sctp", "unknown protocol: sctp"},
		{"-P overrides a bad config protocol", []string{"-P", "icmp"}, "sctp", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newRootCmd()
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatalf("ParseFlags(%v): %v", tc.args, err)
			}
			err := checkFlags(cmd, tc.configProtocol)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("checkFlags = %v, want no error", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("checkFlags = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// A flow count below 1 is clamped with a warning, as one above the maximum
// is (viaduct#108 item 6).
func TestPathsClampWarning(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{1, ""},
		{6, ""},
		{maxNumPaths, ""},
		{0, "Warning: --paths 0 is below 1, using 1 (single path)"},
		{-4, "Warning: --paths -4 is below 1, using 1 (single path)"},
		{maxNumPaths + 1, "Warning: --paths 33 exceeds maximum 32, clamping"},
	}
	for _, tc := range cases {
		if got := pathsClampWarning(tc.n); got != tc.want {
			t.Errorf("pathsClampWarning(%d) = %q, want %q", tc.n, got, tc.want)
		}
		// Every count that is clamped has a warning, and no other does.
		if _, clamped := clampNumPaths(tc.n); clamped != (tc.want != "") {
			t.Errorf("clampNumPaths(%d) clamped = %v, but the warning is %q", tc.n, clamped, tc.want)
		}
	}
}

// The --no-paths help names its conflict with --paths (viaduct#108 item 7).
func TestNoPathsHelpNamesTheConflict(t *testing.T) {
	usage := newRootCmd().Flags().Lookup("no-paths").Usage
	if !strings.Contains(usage, "--paths 1") || !strings.Contains(usage, "error with any other --paths value") {
		t.Errorf("--no-paths help = %q, want it to say it is --paths 1 and an error with any other --paths value", usage)
	}
}
