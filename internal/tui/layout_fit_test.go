package tui

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Layout rules for multi-target sessions and narrow terminals:
//
//   - every hop can be reached by scrolling, whatever the summary panel takes
//     (viaduct#81)
//   - no rendered line is wider than the terminal, and the frame is never
//     taller than it (viaduct#68, #82)

// multiTargetModel returns a model of the given size with n targets; the
// first has a path of hops hops and is focused.
func multiTargetModel(w, h, n, hops int) Model {
	var fixtures []hopSampleFixture
	for ttl := 1; ttl <= hops; ttl++ {
		fixtures = append(fixtures, hopSample(ttl, fmt.Sprintf("192.0.2.%d", ttl), time.Duration(ttl)*time.Millisecond))
	}
	m := newTestModel(withSize(w, h), withHops(fixtures...))
	m.sessions[0].target = "t00.example"
	for i := 1; i < n; i++ {
		m.sessions = append(m.sessions, newSession(
			fmt.Sprintf("t%02d.example", i), net.ParseIP(fmt.Sprintf("198.51.100.%d", i)),
			m.sessions[0].probeCfg, "udp", 5.0, 0, 3, true))
	}
	return m
}

// hopRowsShown returns the TTLs of the hop rows in a rendered default view,
// in order: lines of the hop table start with the hop number.
func hopRowsShown(view string) []int {
	var ttls []int
	inTable := false
	for _, line := range strings.Split(stripANSI(view), "\n") {
		if strings.HasPrefix(line, "#") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		var ttl int
		if _, err := fmt.Sscanf(line, "%d ", &ttl); err == nil && ttl > 0 {
			ttls = append(ttls, ttl)
		}
	}
	return ttls
}

func TestScroll_TopHopsReachableWithSummaryPanel(t *testing.T) {
	for _, tc := range []struct {
		name            string
		w, h, n, hops   int
	}{
		{"3 targets, height 24, 30 hops", 120, 24, 3, 30},
		{"16 targets, height 40, 25 hops", 120, 40, 16, 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := multiTargetModel(tc.w, tc.h, tc.n, tc.hops)
			// "g": scroll to the top.
			m.autoScroll = false
			m.scrollOffset = m.maxScrollOffset()

			rows := hopRowsShown(m.View())
			if len(rows) == 0 || rows[0] != 1 {
				t.Fatalf("after scrolling to the top the first hop row is %v, want hop 1 (rows shown: %v)", first(rows), rows)
			}
			// And back to the bottom: the last hop is shown.
			m.scrollOffset = 0
			rows = hopRowsShown(m.View())
			if len(rows) == 0 || rows[len(rows)-1] != tc.hops {
				t.Errorf("at the bottom the last hop row is %v, want hop %d", rows, tc.hops)
			}
		})
	}
}

func first(xs []int) interface{} {
	if len(xs) == 0 {
		return "none"
	}
	return xs[0]
}

// assertFits fails for every line of view wider than w, and if the view has
// more lines than h.
func assertFits(t *testing.T, name, view string, w, h int) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if h > 0 && len(lines) > h {
		t.Errorf("%s: frame is %d lines, terminal height is %d", name, len(lines), h)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got > w {
			t.Errorf("%s: line %d is %d columns wide, terminal is %d: %q", name, i+1, got, w, stripANSI(line))
		}
	}
}
