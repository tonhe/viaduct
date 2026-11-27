package tui

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tonhe/viaduct/internal/config"
	"github.com/tonhe/viaduct/internal/probe"
)

// The view each display_mode value opens.
var wantViewForDisplayMode = map[string]ViewMode{
	"default":     ViewDefault,
	"health":      ViewHealth,
	"latency":     ViewLatency,
	"variability": ViewVariability,
	"probes":      ViewProbeDetail,
}

func TestViewModeForDisplayMode(t *testing.T) {
	for mode, want := range wantViewForDisplayMode {
		if got := viewModeForDisplayMode(mode); got != want {
			t.Errorf("viewModeForDisplayMode(%q) = %v, want %v", mode, got, want)
		}
	}
	// A value the setting does not offer opens the default view.
	for _, mode := range []string{"", "Probes", "probe", "nonsense"} {
		if got := viewModeForDisplayMode(mode); got != ViewDefault {
			t.Errorf("viewModeForDisplayMode(%q) = %v, want the default view", mode, got)
		}
	}
}

// The setting and the views stay in step: every value the settings menu offers
// has a view of its own, and every view can be chosen by a value. A view or a
// value added without the other fails here.
func TestDisplayModes_OneForEveryView(t *testing.T) {
	seen := map[ViewMode]string{}
	for _, mode := range displayModeOptions {
		if _, ok := wantViewForDisplayMode[mode]; !ok {
			t.Errorf("the settings menu offers display mode %q, which this test has no view for", mode)
		}
		v := viewModeForDisplayMode(mode)
		if other, dup := seen[v]; dup {
			t.Errorf("display modes %q and %q both open the %v view", other, mode, v)
		}
		seen[v] = mode
	}
	for v := ViewMode(0); v < ViewCount; v++ {
		if _, ok := seen[v]; !ok {
			t.Errorf("no display mode opens the %v view", v)
		}
	}
	// config.Validate accepts every value the menu offers.
	for _, mode := range displayModeOptions {
		c := config.Default()
		c.DisplayMode = mode
		config.Validate(c)
		if c.DisplayMode != mode {
			t.Errorf("config.Validate changed display_mode %q to %q", mode, c.DisplayMode)
		}
	}
}

// newWithDisplayMode builds a one-target model with the given display_mode in
// its config. (cmd/via calls NewMulti, which builds on New; see
// TestNewMulti_OpensTheConfiguredView.)
func newWithDisplayMode(mode string) Model {
	appCfg := config.Default()
	appCfg.DisplayMode = mode
	cfg := probe.DefaultConfig()
	cfg.IPVersion = 4
	return New("target.example", net.ParseIP("192.0.2.1"), cfg, "0.0.0-test", "udp", true, 5, 0, 3, true, appCfg)
}

// The acceptance test of viaduct#269: via opens in the view its config names.
// Before, every value opened the default view.
func TestNew_OpensTheConfiguredView(t *testing.T) {
	setTestTheme(t)
	for mode, want := range wantViewForDisplayMode {
		t.Run(mode, func(t *testing.T) {
			m := newWithDisplayMode(mode)
			if m.viewMode != want {
				t.Fatalf("display_mode = %q: the model opens in the %v view, want %v", mode, m.viewMode, want)
			}
			// What is drawn is that view: its name is in the header for
			// every view but the default one, and the [d] hint names the
			// view after it.
			m.width, m.height = 140, 24
			view := stripANSI(m.View())
			lines := strings.Split(view, "\n")
			if want != ViewDefault && !strings.Contains(lines[0], want.String()) {
				t.Errorf("display_mode = %q: the header does not name the %v view: %q", mode, want, lines[0])
			}
			if hint := "[d] " + want.Next().String(); !strings.Contains(view, hint) {
				t.Errorf("display_mode = %q: the status bar does not offer %q", mode, hint)
			}
		})
	}
}

// cmd/via builds its model with NewMulti, for one target or several. The
// configured view opens there too, whatever the number of targets and whether
// or not one of them failed to start.
func TestNewMulti_OpensTheConfiguredView(t *testing.T) {
	cfg := probe.DefaultConfig()
	cfg.IPVersion = 4
	one := []TargetSpec{{Name: "a.example", IP: net.ParseIP("192.0.2.1"), Cfg: cfg, ProtocolName: "udp"}}
	three := append(append([]TargetSpec(nil), one...),
		TargetSpec{Name: "b.example", IP: net.ParseIP("192.0.2.2"), Cfg: cfg, ProtocolName: "udp"},
		TargetSpec{Name: "bad.example", Cfg: cfg, ProtocolName: "udp", Err: errors.New("cannot resolve host")},
	)
	for mode, want := range wantViewForDisplayMode {
		for _, specs := range [][]TargetSpec{one, three} {
			appCfg := config.Default()
			appCfg.DisplayMode = mode
			m := NewMulti(specs, "0.0.0-test", true, 5, 0, 3, true, appCfg)
			if m.viewMode != want {
				t.Errorf("display_mode = %q, %d targets: opens in the %v view, want %v", mode, len(specs), m.viewMode, want)
			}
		}
	}
}

// A config without a usable display_mode, or no config at all, opens the
// default view.
func TestNew_DefaultViewWithoutAConfiguredOne(t *testing.T) {
	if m := newWithDisplayMode("nonsense"); m.viewMode != ViewDefault {
		t.Errorf("unknown display_mode: opens in %v, want the default view", m.viewMode)
	}
	cfg := probe.DefaultConfig()
	cfg.IPVersion = 4
	m := New("target.example", net.ParseIP("192.0.2.1"), cfg, "0.0.0-test", "udp", true, 5, 0, 3, true, nil)
	if m.viewMode != ViewDefault {
		t.Errorf("nil config: opens in %v, want the default view", m.viewMode)
	}
}

// isolateConfigDir points every variable the config path is built from at a
// temporary directory, so saving settings in a test writes there. It returns
// the path the config file is saved to.
func isolateConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("USERPROFILE", dir)
	return filepath.Join(dir, "via", "config.toml")
}

// pressKeys sends each key to the model as the program would, and delivers the
// message of any command a key returns (saving settings is one).
func pressKeys(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		next, cmd := m.Update(keyPress(k))
		m = next.(Model)
		if cmd == nil {
			continue
		}
		if msg := cmd(); msg != nil {
			next, _ = m.Update(msg)
			m = next.(Model)
		}
	}
	return m
}

// Changing Display Mode in the settings menu and saving switches the running
// view to the one chosen, and writes the choice to the config file.
func TestSettingsSave_ChangedDisplayModeSwitchesTheView(t *testing.T) {
	setTestTheme(t)
	path := isolateConfigDir(t)

	m := newWithDisplayMode("default")
	m.width, m.height = 140, 40
	if m.viewMode != ViewDefault {
		t.Fatalf("fixture: starts in %v", m.viewMode)
	}

	// Esc opens settings; the third entry is Display & Enrichment; Enter on
	// its first row moves Display Mode on by one; Ctrl-S saves.
	steps := []struct {
		keys     []string
		wantMode string
		wantView ViewMode
	}{
		{[]string{"esc", "j", "j", "enter", "enter", "ctrl+s"}, "health", ViewHealth},
		{[]string{"esc", "j", "j", "enter", "enter", "enter", "enter", "ctrl+s"}, "probes", ViewProbeDetail},
		{[]string{"esc", "j", "j", "enter", "enter", "ctrl+s"}, "default", ViewDefault},
	}
	for _, step := range steps {
		m = pressKeys(t, m, step.keys...)
		if m.showSettings {
			t.Fatalf("after saving %q the settings overlay is still open", step.wantMode)
		}
		if m.config.DisplayMode != step.wantMode {
			t.Fatalf("saved display_mode = %q, want %q", m.config.DisplayMode, step.wantMode)
		}
		if m.viewMode != step.wantView {
			t.Errorf("after saving display_mode %q the running view is %v, want %v", step.wantMode, m.viewMode, step.wantView)
		}
		saved, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading the saved config at %s: %v", path, err)
		}
		if want := `display_mode = "` + step.wantMode + `"`; !strings.Contains(string(saved), want) {
			t.Errorf("the saved config does not hold %s:\n%s", want, saved)
		}
	}
}

// Saving settings with Display Mode as it was leaves the running view alone:
// a view picked with d is not taken away by saving, say, a theme.
func TestSettingsSave_UnchangedDisplayModeKeepsTheRunningView(t *testing.T) {
	setTestTheme(t)
	isolateConfigDir(t)

	m := newWithDisplayMode("health")
	m.width, m.height = 140, 40
	m = pressKeys(t, m, "d") // Health -> Latency, by key: the setting still says health
	if m.viewMode != ViewLatency || m.config.DisplayMode != "health" {
		t.Fatalf("fixture: view %v with display_mode %q, want Latency with health", m.viewMode, m.config.DisplayMode)
	}

	// Open settings, toggle DNS lookups in Display & Enrichment, save.
	m = pressKeys(t, m, "esc", "j", "j", "enter", "j", "enter", "ctrl+s")
	if m.showSettings {
		t.Fatal("the settings overlay is still open after saving")
	}
	if m.config.DisplayMode != "health" {
		t.Fatalf("display_mode = %q after a save that did not touch it, want health", m.config.DisplayMode)
	}
	if m.viewMode != ViewLatency {
		t.Errorf("the running view is %v after a save that did not change Display Mode, want Latency kept", m.viewMode)
	}

	// Leaving the menu after changing Display Mode, and choosing Discard (the
	// second option) when asked, changes nothing.
	m = pressKeys(t, m, "esc", "j", "j", "enter", "enter", "esc", "esc", "j", "enter")
	if m.showSettings {
		t.Fatal("the settings overlay is still open after discarding")
	}
	if m.viewMode != ViewLatency || m.config.DisplayMode != "health" {
		t.Errorf("after discarding a change: view %v, display_mode %q; want Latency and health", m.viewMode, m.config.DisplayMode)
	}
}

// The d key moves through the views and does not change the setting.
func TestViewKey_DoesNotChangeTheSetting(t *testing.T) {
	setTestTheme(t)
	m := newWithDisplayMode("probes")
	m.width, m.height = 140, 40
	for i := 0; i < int(ViewCount); i++ {
		m = pressKeys(t, m, "d")
		if m.config.DisplayMode != "probes" {
			t.Fatalf("after %d presses of d display_mode = %q, want probes", i+1, m.config.DisplayMode)
		}
	}
	if m.viewMode != ViewProbeDetail {
		t.Errorf("after one press per view the view is %v, want back at Probe Detail", m.viewMode)
	}
}
