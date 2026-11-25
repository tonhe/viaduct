package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Summary panel and frame layout (viaduct#82).

func TestSummary_HeadersAlignWithValues(t *testing.T) {
	m := multiTargetModel(120, 30, 3, 5)
	lines := strings.Split(stripANSI(m.renderSummary()), "\n")
	header, row := lines[0], lines[1]

	// AVG is right-aligned: the header's last letter sits over the value's
	// last character ("ms").
	// Columns are counted in characters: the focus marker is one cell but
	// several bytes.
	col := func(s, sub string) int { return len([]rune(s[:strings.Index(s, sub)])) }
	avgEnd := col(header, "AVG") + len("AVG")
	msEnd := col(row, "ms") + len("ms")
	if avgEnd != msEnd {
		t.Errorf("AVG header ends at column %d, its value at %d\n%s\n%s", avgEnd, msEnd, header, row)
	}
	// WRST-HOP is left-aligned: header and value start in the same column.
	// The value is the worst hop's address, or "—".
	hopStart := strings.Index(header, "WRST-HOP")
	rowRunes := []rune(row)
	headerRunes := []rune(header)
	hopStartRunes := len([]rune(header[:hopStart]))
	if hopStartRunes >= len(rowRunes) || rowRunes[hopStartRunes] == ' ' || rowRunes[hopStartRunes-1] != ' ' {
		t.Errorf("WRST-HOP value does not start under its header (column %d)\n%s\n%s", hopStartRunes, string(headerRunes), string(rowRunes))
	}
}

func TestSummary_NoDataRowShowsDashes(t *testing.T) {
	m := multiTargetModel(120, 30, 2, 3)
	lines := strings.Split(stripANSI(m.renderSummary()), "\n")
	idle := lines[2] // second target: nothing traced yet
	if strings.Contains(idle, "0.0%") || strings.Contains(idle, "0.0ms") {
		t.Errorf("a target with no data shows zero figures, want dashes: %q", idle)
	}
}

func TestFrame_EveryLineFitsAndTheFrameFitsTheTerminal(t *testing.T) {
	longErr := errors.New("lookup bad.example on 192.0.2.53:53: no such host, and a rather long explanation follows")
	cases := []struct {
		name string
		m    func(w, h int) Model
		h    int
	}{
		{"summary, 3 targets", func(w, h int) Model { return multiTargetModel(w, h, 3, 12) }, 30},
		{"summary with an alert", func(w, h int) Model {
			m := multiTargetModel(w, h, 3, 12)
			ae := NewAlertEngine(1, 0, 1)
			ae.Update(55.7, 0)
			ae.SetAffectedHop(3, "AS64500 EXAMPLE-NETWORK-OPERATOR")
			m.sessions[0].alertEngine = ae
			return m
		}, 30},
		{"focused target failed to start", func(w, h int) Model {
			m := multiTargetModel(w, h, 3, 12)
			m.sessions[1].startErr = longErr
			m.focusedIdx = 1
			return m
		}, 30},
		{"16 targets, short terminal", func(w, h int) Model { return multiTargetModel(w, h, 16, 10) }, 20},
		{"column view, very short terminal", func(w, h int) Model {
			m := multiTargetModel(w, h, 3, 12)
			m.columnView = true
			return m
		}, 6},
		{"single target, short terminal", func(w, h int) Model { return multiTargetModel(w, h, 1, 25) }, 8},
	}
	for _, tc := range cases {
		for _, w := range []int{80, 120} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, w), func(t *testing.T) {
				assertFits(t, tc.name, tc.m(w, tc.h).View(), w, tc.h)
			})
		}
	}
}

func TestFrame_KeepsTheStatusBarWhenClipped(t *testing.T) {
	m := multiTargetModel(120, 20, 16, 10)
	lines := strings.Split(strings.TrimRight(stripANSI(m.View()), "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "q:quit") {
		t.Errorf("the last line of a clipped frame is not the status bar: %q", last)
	}
}

// Column view has no scrolling. When a path is deeper than the panes can
// show, the view says so; it used to cut the path with no sign of it.
func TestColumnView_SaysWhenAPathIsCut(t *testing.T) {
	m := multiTargetModel(120, 16, 2, 20)
	m.columnView = true
	out := stripANSI(m.View())
	if !strings.Contains(out, "of 20") {
		t.Errorf("column view cuts a 20-hop path at height 16 without saying so:\n%s", out)
	}
	assertFits(t, "column view, cut path", m.View(), 120, 16)

	// A path that fits has no such note.
	short := multiTargetModel(120, 30, 2, 5)
	short.columnView = true
	if strings.Contains(stripANSI(short.View()), "hops 1") {
		t.Error("column view shows a cut note for a path that fits")
	}
}

func TestFinalSummary_CoversEveryTarget(t *testing.T) {
	m := multiTargetModel(120, 30, 3, 4)
	m.sessions[2].startErr = errors.New("resolve t02.example: no such host")
	out := m.FinalSummary()
	for _, want := range []string{"t00.example", "t01.example", "t02.example", "no such host"} {
		if !strings.Contains(out, want) {
			t.Errorf("final summary does not mention %q:\n%s", want, out)
		}
	}
}

func TestFinalSummary_SingleTargetUnchanged(t *testing.T) {
	m := multiTargetModel(120, 30, 1, 4)
	out := m.FinalSummary()
	if strings.Count(out, "via — ") != 1 {
		t.Errorf("single-target final summary should have one header:\n%s", out)
	}
}
