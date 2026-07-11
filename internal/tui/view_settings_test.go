package tui

import (
	"path/filepath"
	"testing"
)

func TestGolden_Settings(t *testing.T) {
	setTestTheme(t)

	cases := []struct {
		name  string
		state settingsState
	}{
		{"main", settingsMain},
		{"theme", settingsTheme},
		{"probe", settingsProbe},
		{"display", settingsDisplay},
		{"alert", settingsAlert},
		{"confirm_discard", settingsConfirmDiscard},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(withSize(120, 30), withSettingsOpen(tc.state))
			got := m.View()
			path := filepath.Join("testdata", "view", "settings_"+tc.name+".golden")
			goldenCompare(t, path, got)
		})
	}
}
