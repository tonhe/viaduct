package main

import (
	"runtime/debug"
	"testing"
)

// buildInfo returns a BuildInfo with the given main-module version and VCS
// settings, as debug.ReadBuildInfo reports them.
func buildInfo(moduleVersion string, settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{
		Main:     debug.Module{Path: "github.com/tonhe/viaduct", Version: moduleVersion},
		Settings: settings,
	}
}

func TestResolveVersion(t *testing.T) {
	rev := debug.BuildSetting{Key: "vcs.revision", Value: "8d1ab98c0ffee1234567890abcdef1234567890a"}
	shortRev := debug.BuildSetting{Key: "vcs.revision", Value: "8d1ab98"}
	dirty := debug.BuildSetting{Key: "vcs.modified", Value: "true"}
	clean := debug.BuildSetting{Key: "vcs.modified", Value: "false"}

	cases := []struct {
		name     string
		injected string
		info     *debug.BuildInfo
		want     string
	}{
		// Release builds: -ldflags -X main.version=<tag> wins over everything.
		{"injected wins over module version", "0.3.0", buildInfo("v0.2.1", rev, dirty), "0.3.0"},
		{"injected wins with no build info", "0.3.0", nil, "0.3.0"},
		{"injected kept verbatim", "v0.3.0-rc1", buildInfo("(devel)"), "v0.3.0-rc1"},

		// go install module@version: the module version, without the v.
		{"dev + module version", "dev", buildInfo("v0.2.1"), "0.2.1"},
		{"dev + module version ignores vcs", "dev", buildInfo("v0.2.1", rev, dirty), "0.2.1"},
		{"dev + pseudo-version", "dev", buildInfo("v0.2.2-0.20261124101500-8d1ab98c0ffe+dirty"), "0.2.2-0.20261124101500-8d1ab98c0ffe+dirty"},
		{"empty injected behaves like dev", "", buildInfo("v0.2.1"), "0.2.1"},

		// Plain build in a checkout: (devel) plus whatever VCS stamp exists.
		{"dev + (devel), no vcs", "dev", buildInfo("(devel)"), "dev"},
		{"dev + empty module version, no vcs", "dev", buildInfo(""), "dev"},
		{"dev + (devel) + clean revision", "dev", buildInfo("(devel)", rev, clean), "dev+8d1ab98c0ffe"},
		{"dev + (devel) + dirty revision", "dev", buildInfo("(devel)", rev, dirty), "dev+8d1ab98c0ffe.dirty"},
		{"dev + (devel) + revision, no modified flag", "dev", buildInfo("(devel)", rev), "dev+8d1ab98c0ffe"},
		{"dev + short revision kept whole", "dev", buildInfo("(devel)", shortRev, dirty), "dev+8d1ab98.dirty"},
		{"dev + dirty flag without revision", "dev", buildInfo("(devel)", dirty), "dev"},

		// No build info at all (binary built without module support).
		{"dev + no build info", "dev", nil, "dev"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.injected, tc.info); got != tc.want {
				t.Errorf("resolveVersion(%q, ...) = %q, want %q", tc.injected, got, tc.want)
			}
		})
	}
}

func TestDisplayVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"0.3.0", "v0.3.0"},
		{"0.2.2-0.20261124101500-8d1ab98c0ffe+dirty", "v0.2.2-0.20261124101500-8d1ab98c0ffe+dirty"},
		{"v0.3.0", "v0.3.0"},
		{"dev", "dev"},
		{"dev+8d1ab98c0ffe.dirty", "dev+8d1ab98c0ffe.dirty"},
		{"", ""},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := displayVersion(tc.in); got != tc.want {
				t.Errorf("displayVersion(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestVersionDefault pins the default: a build that injects nothing must not
// claim a release number. Test binaries carry no module version or VCS stamp,
// so the fallback leaves it at "dev".
func TestVersionDefault(t *testing.T) {
	if version != "dev" {
		t.Errorf("version = %q in a test build, want \"dev\"", version)
	}
}
