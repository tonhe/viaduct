package config

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLoad throws arbitrary bytes at the TOML loader.
// It must never panic; malformed TOML should return an error or fall back to defaults.
func FuzzLoad(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("theme = \"solarized-dark\"\n"))
	f.Add([]byte("garbage = [[[["))
	f.Add([]byte("theme = 123")) // wrong type
	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on %q: %v", data, r)
			}
		}()
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		path := filepath.Join(tmpDir, "via", "config.toml")
		_ = os.MkdirAll(filepath.Dir(path), 0755)
		_ = os.WriteFile(path, data, 0644)
		_, _ = Load()
	})
}
