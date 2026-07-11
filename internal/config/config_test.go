package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDefault_AllFields verifies every field in Default() has a non-zero /
// non-empty value where one is expected, and that IPFamily is "auto".
func TestDefault_AllFields(t *testing.T) {
	d := Default()
	if d == nil {
		t.Fatal("Default() returned nil")
	}

	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"Theme", d.Theme, "solarized-dark"},
		{"Protocol", d.Protocol, "udp"},
		{"MaxHops", d.MaxHops, 30},
		{"Interval", d.Interval, "1s"},
		{"Paths", d.Paths, 6},
		{"DisplayMode", d.DisplayMode, "default"},
		{"AlertLoss", d.AlertLoss, 5.0},
		{"AlertLatency", d.AlertLatency, "0s"},
		{"AlertRounds", d.AlertRounds, 3},
		{"IPFamily", d.IPFamily, "auto"},
	}

	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			switch got := c.got.(type) {
			case string:
				if got != c.want.(string) {
					t.Errorf("got %q, want %q", got, c.want)
				}
			case int:
				if got != c.want.(int) {
					t.Errorf("got %d, want %d", got, c.want)
				}
			case float64:
				if got != c.want.(float64) {
					t.Errorf("got %f, want %f", got, c.want)
				}
			}
		})
	}

	// Bool fields that should be true by default
	if !d.DNSLookups {
		t.Error("DNSLookups should default to true")
	}
	if !d.ASNLookups {
		t.Error("ASNLookups should default to true")
	}
	if !d.PingSupplement {
		t.Error("PingSupplement should default to true")
	}
	if !d.AlertEnabled {
		t.Error("AlertEnabled should default to true")
	}
}

// TestDefault_IPFamilyIsAuto specifically checks the IPFamily default (regression
// target for the IPv6 feature work).
func TestDefault_IPFamilyIsAuto(t *testing.T) {
	d := Default()
	if d.IPFamily != "auto" {
		t.Errorf("IPFamily default = %q, want %q", d.IPFamily, "auto")
	}
}

// TestDefault_ExportFormatEmpty verifies ExportFormat defaults to empty string
// (it has no config-file default; it's set from CLI flags).
func TestDefault_ExportFormatEmpty(t *testing.T) {
	d := Default()
	if d.ExportFormat != "" {
		t.Errorf("ExportFormat default = %q, want empty string", d.ExportFormat)
	}
}

// setXDGTempDir redirects XDG_CONFIG_HOME into a temp dir and returns the
// expected config path inside it.
func setXDGTempDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	return filepath.Join(tmp, "via", "config.toml")
}

// TestSaveLoad_RoundTrip writes a non-default Config then reads it back and
// checks every field survives the round-trip.
func TestSaveLoad_RoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG override not applicable on Windows")
	}

	setXDGTempDir(t)

	want := &Config{
		Theme:          "dracula",
		Protocol:       "icmp",
		MaxHops:        20,
		Interval:       "500ms",
		Paths:          4,
		DisplayMode:    "health",
		DNSLookups:     false,
		ASNLookups:     false,
		PingSupplement: false,
		AlertEnabled:   false,
		AlertLoss:      12.5,
		AlertLatency:   "150ms",
		AlertRounds:    7,
		ExportFormat:   "", // Save does not write ExportFormat
		IPFamily:       "4",
	}

	if err := Save(want); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	fields := []struct {
		name     string
		got, exp interface{}
	}{
		{"Theme", got.Theme, want.Theme},
		{"Protocol", got.Protocol, want.Protocol},
		{"MaxHops", got.MaxHops, want.MaxHops},
		{"Interval", got.Interval, want.Interval},
		{"Paths", got.Paths, want.Paths},
		{"DisplayMode", got.DisplayMode, want.DisplayMode},
		{"DNSLookups", got.DNSLookups, want.DNSLookups},
		{"ASNLookups", got.ASNLookups, want.ASNLookups},
		{"PingSupplement", got.PingSupplement, want.PingSupplement},
		{"AlertEnabled", got.AlertEnabled, want.AlertEnabled},
		{"AlertLoss", got.AlertLoss, want.AlertLoss},
		{"AlertLatency", got.AlertLatency, want.AlertLatency},
		{"AlertRounds", got.AlertRounds, want.AlertRounds},
		{"IPFamily", got.IPFamily, want.IPFamily},
	}

	for _, f := range fields {
		t.Run(f.name, func(t *testing.T) {
			if f.got != f.exp {
				t.Errorf("got %v, want %v", f.got, f.exp)
			}
		})
	}
}

// TestSaveLoad_IPFamily6 specifically round-trips IPFamily="6" to guard against
// regression in the IPv6 feature.
func TestSaveLoad_IPFamily6(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG override not applicable on Windows")
	}

	setXDGTempDir(t)

	cfg := Default()
	cfg.IPFamily = "6"

	if err := Save(cfg); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if got.IPFamily != "6" {
		t.Errorf("IPFamily round-trip: got %q, want %q", got.IPFamily, "6")
	}
}

// TestLoad_MissingFile returns Default() with no error when no config file exists.
func TestLoad_MissingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG override not applicable on Windows")
	}

	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	// Deliberately do NOT create the file.

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	d := Default()
	if got.Theme != d.Theme || got.IPFamily != d.IPFamily || got.MaxHops != d.MaxHops {
		t.Errorf("Load() with missing file should return Default(); got %+v", got)
	}
}

// TestLoad_EmptyFile returns Default() when the file is empty (toml.Unmarshal
// on empty bytes is a no-op).
func TestLoad_EmptyFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG override not applicable on Windows")
	}

	cfgPath := setXDGTempDir(t)

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte{}, 0644); err != nil {
		t.Fatalf("write empty file: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() on empty file returned error: %v", err)
	}

	d := Default()
	if got.Theme != d.Theme || got.IPFamily != d.IPFamily {
		t.Errorf("Empty file should yield defaults; got %+v", got)
	}
}

// TestLoad_MalformedTOML returns an error for bad TOML, not a panic or default.
func TestLoad_MalformedTOML(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG override not applicable on Windows")
	}

	cfgPath := setXDGTempDir(t)

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("theme = [[[not valid toml"), 0644); err != nil {
		t.Fatalf("write bad file: %v", err)
	}

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with malformed TOML should return an error, got nil")
	}
	if !strings.Contains(err.Error(), "config:") {
		t.Errorf("error should be prefixed with 'config:'; got: %v", err)
	}
}

// TestLoad_PartialConfig verifies that missing fields inherit their defaults.
func TestLoad_PartialConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG override not applicable on Windows")
	}

	cfgPath := setXDGTempDir(t)

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Only set theme; everything else should come from Default().
	partial := []byte(`theme = "catppuccin-mocha"`)
	if err := os.WriteFile(cfgPath, partial, 0644); err != nil {
		t.Fatalf("write partial file: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	d := Default()

	if got.Theme != "catppuccin-mocha" {
		t.Errorf("Theme: got %q, want %q", got.Theme, "catppuccin-mocha")
	}
	// All other fields should be defaults.
	if got.Protocol != d.Protocol {
		t.Errorf("Protocol: got %q, want default %q", got.Protocol, d.Protocol)
	}
	if got.MaxHops != d.MaxHops {
		t.Errorf("MaxHops: got %d, want default %d", got.MaxHops, d.MaxHops)
	}
	if got.IPFamily != d.IPFamily {
		t.Errorf("IPFamily: got %q, want default %q", got.IPFamily, d.IPFamily)
	}
	if got.AlertLoss != d.AlertLoss {
		t.Errorf("AlertLoss: got %f, want default %f", got.AlertLoss, d.AlertLoss)
	}
	if got.AlertRounds != d.AlertRounds {
		t.Errorf("AlertRounds: got %d, want default %d", got.AlertRounds, d.AlertRounds)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Validate tests
// ──────────────────────────────────────────────────────────────────────────────

// TestValidate_ValidDefaults verifies that a Default() config passes through
// Validate unchanged.
func TestValidate_ValidDefaults(t *testing.T) {
	c := Default()
	Validate(c)

	d := Default()
	if c.Theme != d.Theme {
		t.Errorf("Theme mutated: %q", c.Theme)
	}
	if c.Protocol != d.Protocol {
		t.Errorf("Protocol mutated: %q", c.Protocol)
	}
	if c.IPFamily != d.IPFamily {
		t.Errorf("IPFamily mutated: %q", c.IPFamily)
	}
}

// TestValidate_IPFamily_EmptyString treats empty string as "auto".
func TestValidate_IPFamily_EmptyString(t *testing.T) {
	c := Default()
	c.IPFamily = ""
	Validate(c)
	if c.IPFamily != "auto" {
		t.Errorf("empty IPFamily should become 'auto', got %q", c.IPFamily)
	}
}

// TestValidate_IPFamily_ValidValues checks that "auto", "4", "6" are accepted
// without modification.
func TestValidate_IPFamily_ValidValues(t *testing.T) {
	for _, v := range []string{"auto", "4", "6"} {
		t.Run(v, func(t *testing.T) {
			c := Default()
			c.IPFamily = v
			Validate(c)
			if c.IPFamily != v {
				t.Errorf("valid IPFamily %q was changed to %q", v, c.IPFamily)
			}
		})
	}
}

// TestValidate_IPFamily_Invalid clamps invalid values to "auto".
// Task 19 behaviour: invalid ip_family → "auto" with a stderr warning.
func TestValidate_IPFamily_Invalid(t *testing.T) {
	for _, bad := range []string{"7", "both", "ipv4", "IPv6", "5", "0"} {
		t.Run(bad, func(t *testing.T) {
			c := Default()
			c.IPFamily = bad
			Validate(c)
			if c.IPFamily != "auto" {
				t.Errorf("invalid IPFamily %q should be clamped to 'auto', got %q", bad, c.IPFamily)
			}
		})
	}
}

// TestValidate_Theme_Empty resets empty theme to default.
func TestValidate_Theme_Empty(t *testing.T) {
	c := Default()
	c.Theme = ""
	Validate(c)
	if c.Theme == "" {
		t.Errorf("empty Theme should be set to default, got empty string")
	}
}

// TestValidate_Protocol_Unknown resets unknown protocol to default.
func TestValidate_Protocol_Unknown(t *testing.T) {
	c := Default()
	c.Protocol = "ftp"
	Validate(c)
	if c.Protocol != "udp" {
		t.Errorf("unknown Protocol should become 'udp', got %q", c.Protocol)
	}
}

// TestValidate_Protocol_ValidValues checks all documented protocol values.
func TestValidate_Protocol_ValidValues(t *testing.T) {
	for _, v := range []string{"udp", "icmp", "tcp", "auto"} {
		t.Run(v, func(t *testing.T) {
			c := Default()
			c.Protocol = v
			Validate(c)
			if c.Protocol != v {
				t.Errorf("valid Protocol %q was changed to %q", v, c.Protocol)
			}
		})
	}
}

// TestValidate_MaxHops_Clamp verifies clamping at boundaries and beyond.
func TestValidate_MaxHops_Clamp(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 1},
		{-5, 1},
		{1, 1},
		{30, 30},
		{64, 64},
		{65, 64},
		{100, 64},
	}
	for _, tc := range cases {
		c := Default()
		c.MaxHops = tc.in
		Validate(c)
		if c.MaxHops != tc.want {
			t.Errorf("MaxHops(%d) → %d, want %d", tc.in, c.MaxHops, tc.want)
		}
	}
}

// TestValidate_Paths_Clamp verifies clamping at boundaries and beyond.
func TestValidate_Paths_Clamp(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 1},
		{-1, 1},
		{1, 1},
		{6, 6},
		{32, 32},
		{33, 32},
	}
	for _, tc := range cases {
		c := Default()
		c.Paths = tc.in
		Validate(c)
		if c.Paths != tc.want {
			t.Errorf("Paths(%d) → %d, want %d", tc.in, c.Paths, tc.want)
		}
	}
}

// TestValidate_AlertLoss_Clamp verifies clamping between 0 and 100.
func TestValidate_AlertLoss_Clamp(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{-1.0, 0.0},
		{0.0, 0.0},
		{5.0, 5.0},
		{100.0, 100.0},
		{101.0, 100.0},
		{-0.1, 0.0},
	}
	for _, tc := range cases {
		c := Default()
		c.AlertLoss = tc.in
		Validate(c)
		if c.AlertLoss != tc.want {
			t.Errorf("AlertLoss(%f) → %f, want %f", tc.in, c.AlertLoss, tc.want)
		}
	}
}

// TestValidate_AlertRounds_Clamp verifies clamping between 1 and 20.
func TestValidate_AlertRounds_Clamp(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 1},
		{-1, 1},
		{1, 1},
		{3, 3},
		{20, 20},
		{21, 20},
	}
	for _, tc := range cases {
		c := Default()
		c.AlertRounds = tc.in
		Validate(c)
		if c.AlertRounds != tc.want {
			t.Errorf("AlertRounds(%d) → %d, want %d", tc.in, c.AlertRounds, tc.want)
		}
	}
}

// TestValidate_Interval_BadDuration resets an unparseable interval to default.
func TestValidate_Interval_BadDuration(t *testing.T) {
	c := Default()
	c.Interval = "not-a-duration"
	Validate(c)
	if c.Interval != "1s" {
		t.Errorf("bad Interval should reset to '1s', got %q", c.Interval)
	}
}

// TestValidate_AlertLatency_BadDuration resets an unparseable alert_latency.
func TestValidate_AlertLatency_BadDuration(t *testing.T) {
	c := Default()
	c.AlertLatency = "bad"
	Validate(c)
	if c.AlertLatency != "0s" {
		t.Errorf("bad AlertLatency should reset to '0s', got %q", c.AlertLatency)
	}
}

// TestValidate_DisplayMode_Unknown resets unknown display_mode to "default".
func TestValidate_DisplayMode_Unknown(t *testing.T) {
	c := Default()
	c.DisplayMode = "rainbow"
	Validate(c)
	if c.DisplayMode != "default" {
		t.Errorf("unknown DisplayMode should become 'default', got %q", c.DisplayMode)
	}
}

// TestValidate_DisplayMode_ValidValues checks all known display_mode values.
func TestValidate_DisplayMode_ValidValues(t *testing.T) {
	for _, v := range []string{"default", "health", "latency", "variability"} {
		t.Run(v, func(t *testing.T) {
			c := Default()
			c.DisplayMode = v
			Validate(c)
			if c.DisplayMode != v {
				t.Errorf("valid DisplayMode %q was changed to %q", v, c.DisplayMode)
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Platform path tests
// ──────────────────────────────────────────────────────────────────────────────

// TestConfigDir_XDG verifies that XDG_CONFIG_HOME is respected on Unix.
func TestConfigDir_XDG(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG not used on Windows")
	}

	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	got := configDir()
	want := filepath.Join(tmp, "via")
	if got != want {
		t.Errorf("configDir() = %q, want %q", got, want)
	}
}

// TestConfigDir_XDGEmpty falls back to ~/.config/via when XDG_CONFIG_HOME
// is unset.
func TestConfigDir_XDGEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG not used on Windows")
	}

	t.Setenv("XDG_CONFIG_HOME", "")

	got := configDir()
	// Must end with /.config/via or ./config/via (when HOME is also empty)
	if !strings.HasSuffix(got, filepath.Join(".config", "via")) {
		t.Errorf("configDir() with no XDG = %q, expected suffix '.config/via'", got)
	}
}

// TestConfigPath_XDG verifies configPath() appends "config.toml".
func TestConfigPath_XDG(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG not used on Windows")
	}

	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	got := configPath()
	want := filepath.Join(tmp, "via", "config.toml")
	if got != want {
		t.Errorf("configPath() = %q, want %q", got, want)
	}
}

// TestConfigDir_Windows_APPDATA verifies Windows path when APPDATA is set.
// Only runs on Windows; on Linux we validate the code compiles correctly.
func TestConfigDir_Windows_APPDATA(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}

	tmp := t.TempDir()
	t.Setenv("APPDATA", tmp)

	got := configDir()
	want := filepath.Join(tmp, "via")
	if got != want {
		t.Errorf("configDir() on Windows = %q, want %q", got, want)
	}
}
