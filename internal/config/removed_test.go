package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// captureWarnings points the package's warning output at a buffer and clears
// the once-per-run state, restoring both when the test ends.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := warnOut
	warnOut = &buf
	resetRemovedKeyWarnings()
	t.Cleanup(func() {
		warnOut = prev
		resetRemovedKeyWarnings()
	})
	return &buf
}

// writeConfig writes body to the config path under a temp XDG_CONFIG_HOME and
// returns that path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	cfgPath := setXDGTempDir(t)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(body), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

const removedKeyMessage = "PingSupplement was removed in v0.3.0 and is ignored"

// TestLoad_RemovedKeyWarning covers issue #66: a config that still carries the
// removed ping_supplement key loads without error, warns once, and keeps every
// other setting. A config without the key produces no warning.
func TestLoad_RemovedKeyWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG override not applicable on Windows")
	}

	rest := "theme = \"dracula\"\nmax_hops = 20\nasn_lookups = false\nexport_format = \"json\"\n"

	cases := []struct {
		name      string
		body      string
		wantWarns int
	}{
		{"key absent", rest, 0},
		{"empty file", "", 0},
		{"key false", "ping_supplement = false\n" + rest, 1},
		{"key true", rest + "ping_supplement = true\n", 1},
		{"key with wrong type", "ping_supplement = \"yes\"\n" + rest, 1},
		{"key in other case", "Ping_Supplement = false\n" + rest, 1},
		{"struct field spelling", "PingSupplement = false\n" + rest, 1},
		{"only in a comment", "# ping_supplement = false\n" + rest, 0},
		{"only inside a value", "theme = \"ping_supplement\"\n", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureWarnings(t)
			writeConfig(t, tc.body)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}

			out := buf.String()
			if got := strings.Count(out, removedKeyMessage); got != tc.wantWarns {
				t.Errorf("warning count = %d, want %d; output %q", got, tc.wantWarns, out)
			}
			if tc.wantWarns == 0 && out != "" {
				t.Errorf("expected no output, got %q", out)
			}
			if tc.wantWarns > 0 && !strings.HasSuffix(out, "\n") {
				t.Errorf("warning is not newline-terminated: %q", out)
			}

			if !strings.Contains(tc.body, "max_hops") {
				return
			}
			if cfg.Theme != "dracula" {
				t.Errorf("Theme = %q, want dracula", cfg.Theme)
			}
			if cfg.MaxHops != 20 {
				t.Errorf("MaxHops = %d, want 20", cfg.MaxHops)
			}
			if cfg.ASNLookups {
				t.Error("ASNLookups = true, want false")
			}
			if cfg.ExportFormat != "json" {
				t.Errorf("ExportFormat = %q, want json", cfg.ExportFormat)
			}
			// A field the file does not set keeps its default.
			if cfg.Paths != Default().Paths {
				t.Errorf("Paths = %d, want default %d", cfg.Paths, Default().Paths)
			}
		})
	}
}

// TestLoad_RemovedKeyWarnsOncePerRun loads the same config repeatedly and
// expects a single warning for the whole run.
func TestLoad_RemovedKeyWarnsOncePerRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG override not applicable on Windows")
	}

	buf := captureWarnings(t)
	writeConfig(t, "ping_supplement = false\ntheme = \"dracula\"\n")

	for i := 0; i < 3; i++ {
		if _, err := Load(); err != nil {
			t.Fatalf("Load() #%d error: %v", i+1, err)
		}
	}

	if got := strings.Count(buf.String(), removedKeyMessage); got != 1 {
		t.Errorf("warning count after 3 loads = %d, want 1; output %q", got, buf.String())
	}
}

// TestLoad_MalformedWithRemovedKey checks that a broken file is still reported
// as a parse error and not masked by the removed-key handling.
func TestLoad_MalformedWithRemovedKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG override not applicable on Windows")
	}

	captureWarnings(t)
	writeConfig(t, "ping_supplement = false\ntheme = [unterminated\n")

	if _, err := Load(); err == nil {
		t.Fatal("Load() of malformed TOML returned nil error")
	}
}

// TestSave_DropsRemovedKey loads a config that carries the removed key, saves
// it, and checks the key is gone from the file while the settings survive.
func TestSave_DropsRemovedKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG override not applicable on Windows")
	}

	captureWarnings(t)
	cfgPath := writeConfig(t, "theme = \"dracula\"\nping_supplement = false\nmax_hops = 20\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(strings.ToLower(string(data)), "supplement") {
		t.Errorf("saved config still mentions the removed key:\n%s", data)
	}

	buf := captureWarnings(t)
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() after Save() error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("reload of the saved config warned: %q", buf.String())
	}
	if got.Theme != "dracula" || got.MaxHops != 20 {
		t.Errorf("after save: Theme=%q MaxHops=%d, want dracula/20", got.Theme, got.MaxHops)
	}
}
