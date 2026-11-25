package main

import (
	"runtime/debug"
	"strings"
)

// devVersion is the value of version in a build that injected nothing.
const devVersion = "dev"

// vcsRevisionLen is how many characters of the VCS revision a dev build shows.
const vcsRevisionLen = 12

// The linker applies -X main.version before any init runs, so an injected
// version is already in place here and resolveVersion leaves it alone.
func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		info = nil
	}
	version = resolveVersion(version, info)
}

// resolveVersion returns the version via reports. injected is the value of the
// version variable as the linker left it; info is the result of
// debug.ReadBuildInfo, or nil when the binary carries none.
//
// A version injected at build time (-ldflags "-X main.version=...") is
// returned unchanged. Otherwise the build describes itself:
//
//   - the main module's version without its leading "v", when the toolchain
//     recorded one (go install module@version, or a VCS-stamped build);
//   - "dev+<revision>" with a ".dirty" suffix for a modified tree, when only a
//     VCS revision is recorded;
//   - "dev" when nothing is recorded.
func resolveVersion(injected string, info *debug.BuildInfo) string {
	if injected != "" && injected != devVersion {
		return injected
	}
	if info == nil {
		return devVersion
	}

	if mv := info.Main.Version; mv != "" && mv != "(devel)" {
		return strings.TrimPrefix(mv, "v")
	}

	var revision string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return devVersion
	}
	if len(revision) > vcsRevisionLen {
		revision = revision[:vcsRevisionLen]
	}
	v := devVersion + "+" + revision
	if modified {
		v += ".dirty"
	}
	return v
}

// displayVersion formats a version for the TUI header and exports: a release
// number gets a leading "v" ("0.3.0" becomes "v0.3.0"); anything else, such as
// "dev+8d1ab98c0ffe", is shown as it is.
func displayVersion(v string) string {
	if v != "" && v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return v
}
