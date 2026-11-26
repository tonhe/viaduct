package tui

import (
	"errors"
	"strings"
	"testing"
)

// setHostOS pins the operating system the error view is rendered for, so its
// output (and the golden files made from it) is the same on every platform the
// tests run on.
func setHostOS(t *testing.T, goos string) {
	t.Helper()
	prev := hostOS
	hostOS = goos
	t.Cleanup(func() { hostOS = prev })
}

func TestErrorView_SetcapOnLinuxOnly(t *testing.T) {
	tests := []struct {
		goos       string
		wantSetcap bool
	}{
		{"linux", true},
		{"darwin", false},
		{"windows", false},
	}
	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			setTestTheme(t)
			setHostOS(t, tc.goos)
			m := newTestModel(withSize(100, 24))
			m.err = errors.New("raw socket permission denied: operation not permitted")

			got := m.View()

			if has := strings.Contains(got, "setcap"); has != tc.wantSetcap {
				t.Errorf("error view on %s mentions setcap = %v, want %v", tc.goos, has, tc.wantSetcap)
			}
			if !strings.Contains(got, "sudo via <target>") {
				t.Errorf("error view on %s does not offer sudo", tc.goos)
			}
			if lines := strings.Count(got, "\n"); lines != 24 {
				t.Errorf("error view on %s is %d lines, want 24", tc.goos, lines)
			}
		})
	}
}
