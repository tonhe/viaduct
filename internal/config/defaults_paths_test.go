package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// wantDefaults is the default of every Config field, keyed by the field's
// name. TestDefault_EachField requires an entry for each field, so a field
// added to Config without a stated default fails there.
var wantDefaults = map[string]any{
	"Theme":        "solarized-dark",
	"Protocol":     "udp",
	"MaxHops":      30,
	"Interval":     "1s",
	"Paths":        6,
	"DisplayMode":  "default",
	"DNSLookups":   true,
	"ASNLookups":   true,
	"AlertEnabled": true,
	"AlertLoss":    5.0,
	"AlertLatency": "0s",
	"AlertRounds":  3,
	"ExportFormat": "", // unset: the export key then writes JSON
	"IPFamily":     "auto",
}

// Each default has its own subtest, named for the field, so a changed default
// fails that subtest and no other here.
func TestDefault_EachField(t *testing.T) {
	d := reflect.ValueOf(*Default())
	typ := d.Type()

	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		seen[name] = true
		t.Run(name, func(t *testing.T) {
			want, ok := wantDefaults[name]
			if !ok {
				t.Fatalf("Config.%s has no entry in wantDefaults: state its default there", name)
			}
			got := d.Field(i).Interface()
			// DeepEqual compares the type too: 30 is not 30.0.
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Default().%s = %#v, want %#v", name, got, want)
			}
		})
	}
	for name := range wantDefaults {
		if !seen[name] {
			t.Errorf("wantDefaults names %s, which is not a field of Config", name)
		}
	}
}

// Validate leaves the defaults as they are: every default is a value Validate
// accepts.
func TestDefault_SurvivesValidate(t *testing.T) {
	c := Default()
	Validate(c)
	if !reflect.DeepEqual(c, Default()) {
		t.Errorf("Validate changed the defaults:\n got  %+v\n want %+v", *c, *Default())
	}
}

// setPathEnv sets every variable configDir reads on any platform, so a path
// test never depends on the environment it runs in. An empty value is set, not
// left as it was.
func setPathEnv(t *testing.T, xdg, home, appdata, userprofile string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", appdata)
	t.Setenv("USERPROFILE", userprofile)
}

// On Unix the directory is $XDG_CONFIG_HOME/via, or ~/.config/via without it.
// The Windows variables are set too and must make no difference.
func TestConfigDir_Unix_ExplicitEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix paths; see TestConfigDir_Windows_ExplicitEnv")
	}
	xdg, home, other := t.TempDir(), t.TempDir(), t.TempDir()

	cases := []struct {
		name                            string
		xdg, home, appdata, userprofile string
		want                            string
	}{
		{"XDG_CONFIG_HOME set", xdg, home, "", "", filepath.Join(xdg, "via")},
		{"XDG_CONFIG_HOME set, HOME empty", xdg, "", "", "", filepath.Join(xdg, "via")},
		{"XDG_CONFIG_HOME empty: under HOME", "", home, "", "", filepath.Join(home, ".config", "via")},
		{"both empty: under the working directory", "", "", "", "", filepath.Join(".", ".config", "via")},
		{"Windows variables are not read: XDG", xdg, home, other, other, filepath.Join(xdg, "via")},
		{"Windows variables are not read: HOME", "", home, other, other, filepath.Join(home, ".config", "via")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setPathEnv(t, tc.xdg, tc.home, tc.appdata, tc.userprofile)
			if got := configDir(); got != tc.want {
				t.Errorf("configDir() = %q, want %q", got, tc.want)
			}
			if got, want := configPath(), filepath.Join(tc.want, "config.toml"); got != want {
				t.Errorf("configPath() = %q, want %q", got, want)
			}
		})
	}
}

// On Windows the directory is %APPDATA%\via, or
// %USERPROFILE%\AppData\Roaming\via without APPDATA. The Unix variables are
// set too and must make no difference.
func TestConfigDir_Windows_ExplicitEnv(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows paths; configDir chooses by runtime.GOOS")
	}
	appdata, profile, other := t.TempDir(), t.TempDir(), t.TempDir()

	cases := []struct {
		name                            string
		xdg, home, appdata, userprofile string
		want                            string
	}{
		{"APPDATA set", "", "", appdata, profile, filepath.Join(appdata, "via")},
		{"APPDATA set, USERPROFILE empty", "", "", appdata, "", filepath.Join(appdata, "via")},
		{"APPDATA empty: under USERPROFILE", "", "", "", profile, filepath.Join(profile, "AppData", "Roaming", "via")},
		{"both empty: a relative path", "", "", "", "", filepath.Join("AppData", "Roaming", "via")},
		{"Unix variables are not read: APPDATA", other, other, appdata, profile, filepath.Join(appdata, "via")},
		{"Unix variables are not read: USERPROFILE", other, other, "", profile, filepath.Join(profile, "AppData", "Roaming", "via")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setPathEnv(t, tc.xdg, tc.home, tc.appdata, tc.userprofile)
			if got := configDir(); got != tc.want {
				t.Errorf("configDir() = %q, want %q", got, tc.want)
			}
			if got, want := configPath(), filepath.Join(tc.want, "config.toml"); got != want {
				t.Errorf("configPath() = %q, want %q", got, want)
			}
		})
	}
}

// Save and Load use the directory configDir names, on either platform: the
// file is written under the directory the environment points at, and read back
// from it.
func TestSaveLoad_UseTheConfiguredDirectory(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	var want string
	if runtime.GOOS == "windows" {
		setPathEnv(t, elsewhere, elsewhere, root, elsewhere)
		want = filepath.Join(root, "via", "config.toml")
	} else {
		// No XDG_CONFIG_HOME: the fallback under HOME.
		setPathEnv(t, "", root, elsewhere, elsewhere)
		want = filepath.Join(root, ".config", "via", "config.toml")
	}

	c := Default()
	c.MaxHops = 17
	if err := Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("Save did not write %s: %v", want, err)
	}
	if entries, err := os.ReadDir(elsewhere); err != nil || len(entries) != 0 {
		t.Errorf("Save wrote under a directory the platform does not use: %v (err %v)", entries, err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.MaxHops != 17 {
		t.Errorf("Load().MaxHops = %d, want 17 from %s", got.MaxHops, want)
	}
}
