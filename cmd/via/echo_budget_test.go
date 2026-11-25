package main

import (
	"fmt"
	"testing"

	"github.com/tonhe/viaduct/internal/tui"
)

// Report mode traces targets one after another, so each gets the full echo
// budget; the TUI runs them at once and divides it, with a floor (viaduct#84).
func TestApplyEchoBudget(t *testing.T) {
	for _, tc := range []struct {
		name       string
		targets    int
		reportMode bool
		want       int
	}{
		{name: "TUI, one target: full budget", targets: 1, want: 64},
		{name: "TUI, 4 targets: divided", targets: 4, want: 16},
		{name: "TUI, 16 targets: divided", targets: 16, want: 4},
		{name: "TUI, 32 targets: floor of 4", targets: 32, want: 4},
		{name: "report, one target: full budget", targets: 1, reportMode: true, want: 64},
		{name: "report, 4 targets: undivided", targets: 4, reportMode: true, want: 64},
		{name: "report, 16 targets: undivided", targets: 16, reportMode: true, want: 64},
		{name: "report, 32 targets: undivided", targets: 32, reportMode: true, want: 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			specs := make([]tui.TargetSpec, tc.targets)
			applyEchoBudget(specs, tc.reportMode)
			for i, sp := range specs {
				if sp.Cfg.EchoBudget != tc.want {
					t.Errorf("spec %d: EchoBudget = %d, want %d", i, sp.Cfg.EchoBudget, tc.want)
				}
			}
		})
	}

	t.Run("failed targets do not count and get no budget", func(t *testing.T) {
		specs := make([]tui.TargetSpec, 5)
		specs[1].Err = fmt.Errorf("resolve failed")
		applyEchoBudget(specs, false)
		for i, sp := range specs {
			want := 16
			if i == 1 {
				want = 0
			}
			if sp.Cfg.EchoBudget != want {
				t.Errorf("spec %d: EchoBudget = %d, want %d", i, sp.Cfg.EchoBudget, want)
			}
		}
	})
}
