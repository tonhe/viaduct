package tui

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/tonhe/viaduct/internal/hop"
)

// forceColor makes lipgloss emit ANSI even without a TTY, so byte-length
// padding bugs become visible to width assertions. It saves and restores the
// renderer's color profile via t.Cleanup so state does not leak to other tests.
// Do NOT call t.Parallel() in tests that use forceColor — the renderer state is
// process-global and parallel runs would race.
func forceColor(t *testing.T) {
	t.Helper()
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })
}

// TestDefaultViewStyledCellWidths builds a model with one rate-limited hop and
// one normal hop, forces color output, renders the default view, and asserts
// that every content row has the same visual width — proving the ~N%† cell
// occupies exactly 8 columns, equal to a plain "N.N%" cell.
//
// The test also asserts that ANSI escapes are actually present in the rendered
// output so a silently-non-colored run cannot pass vacuously.
func TestDefaultViewStyledCellWidths(t *testing.T) {
	setTestTheme(t)
	forceColor(t)

	// Build a model with two hops:
	//   TTL 1: normal hop (will show plain "0.0%" loss)
	//   TTL 3: rate-limited hop (will show "~0%†" loss)
	// withRateLimitedHop requires the hop to already exist (from hopsFull).
	m := newTestModel(withSize(120, 24), hopsFull(), withRateLimitedHop(3))
	out := m.View()

	// Sanity: assert that ANSI escapes are actually present so a non-colored
	// run cannot vacuously pass these width assertions.
	if !strings.Contains(out, "\x1b[") {
		t.Fatal("rendered output contains no ANSI escapes — forceColor did not work; width assertions would be meaningless")
	}

	// Split into lines and collect the hop-content lines (those that contain
	// recognizable IP addresses from the fixture).
	lines := strings.Split(out, "\n")

	// Identify hop rows by looking for lines containing known fixture IPs.
	hopIPs := []string{"192.0.2.1", "198.51.100.5", "203.0.113.10", "203.0.113.20"}
	var hopRows []string
	for _, line := range lines {
		for _, ip := range hopIPs {
			if strings.Contains(line, ip) {
				hopRows = append(hopRows, line)
				break
			}
		}
	}
	if len(hopRows) == 0 {
		t.Fatalf("no hop rows found in view output:\n%s", stripANSI(out))
	}

	// Every hop row must have the same visual width (the model width after
	// lineBg pads to 120 columns). Collect widths and report any divergence.
	var widths []int
	for _, row := range hopRows {
		widths = append(widths, lipgloss.Width(row))
	}
	for i := 1; i < len(widths); i++ {
		if widths[i] != widths[0] {
			t.Errorf("hop row %d has visual width %d, but row 0 has %d — widths must be equal", i, widths[i], widths[0])
			t.Logf("row 0 (plain loss): %q", stripANSI(hopRows[0]))
			t.Logf("row %d (rate-limited†): %q", i, stripANSI(hopRows[i]))
		}
	}

	// Specifically identify the rate-limited row (TTL 3, IP 203.0.113.10) and
	// a plain row (TTL 1, IP 192.0.2.1) and assert their widths are equal.
	var rateLimitedRow, normalRow string
	for _, row := range hopRows {
		if strings.Contains(row, "203.0.113.10") {
			rateLimitedRow = row
		}
		if strings.Contains(row, "192.0.2.1") {
			normalRow = row
		}
	}
	if rateLimitedRow == "" {
		t.Fatal("rate-limited row (203.0.113.10) not found in hop rows")
	}
	if normalRow == "" {
		t.Fatal("normal row (192.0.2.1) not found in hop rows")
	}

	rlWidth := lipgloss.Width(rateLimitedRow)
	normWidth := lipgloss.Width(normalRow)
	if rlWidth != normWidth {
		t.Errorf("rate-limited row (†) visual width = %d, normal row width = %d — must be equal", rlWidth, normWidth)
		t.Logf("rate-limited stripped: %q", stripANSI(rateLimitedRow))
		t.Logf("normal stripped:       %q", stripANSI(normalRow))
	}

	// Confirm the rate-limited row actually contains the dagger (test is not trivially vacuous).
	if !strings.Contains(rateLimitedRow, "†") {
		t.Errorf("rate-limited row does not contain '†' — fixture may not have triggered RateLimitYes")
	}
}

// TestPadCellStyledAfterPad verifies two complementary facts:
//
//  1. padCell("~0%†", 8) followed by style.Render() produces a string whose
//     visual width (lipgloss.Width) equals 8. This is the correct pad-then-style
//     order that padCell enforces.
//
//  2. Doing it in the WRONG order — style.Render() first, then fmt.Sprintf
//     with %-8s — produces a DIFFERENT visual width, proving the test can catch
//     the byte-length padding bug class that pad-then-style guards against.
//
// This is a forced-color test because without ANSI escapes the styled string
// equals the plain string, and %-8s would happen to work correctly (the byte-
// length and display-width agree when there are no control sequences).
func TestPadCellStyledAfterPad(t *testing.T) {
	forceColor(t)
	// Ensure a non-trivial style is applied (non-Ascii profile required).
	setTestTheme(t)

	style := rateLimitStyle()

	// Case 1: correct order — pad first (using display width), then style.
	padded := padCell("~0%†", 8) // 4-col content → 4 spaces appended → 8-col padded plain
	correct := style.Render(padded)

	// Sanity: ANSI must be present for the test to be meaningful.
	if !strings.Contains(correct, "\x1b[") {
		t.Fatal("styled output contains no ANSI escapes — forceColor did not take effect; test would be vacuous")
	}

	correctWidth := lipgloss.Width(correct)
	if correctWidth != 8 {
		t.Errorf("pad-then-style: lipgloss.Width = %d, want 8; got %q", correctWidth, correct)
	}

	// Case 2: wrong order — style first, then fmt %-8s byte-pads the ANSI-wrapped string.
	// This is the bug class we're guarding against.
	styled := style.Render("~0%†") // ANSI-wrapped, byte-length >> display-length
	bugged := fmt.Sprintf("%-8s", styled)

	buggedWidth := lipgloss.Width(bugged)
	// The bug produces a DIFFERENT (incorrect) visual width — fmt counts bytes
	// in the ANSI sequence as if they were display columns, so it under-pads
	// (or doesn't pad at all). The correct width is 8; the bugged version should
	// differ, demonstrating the failure mode the test can detect.
	if buggedWidth == correctWidth {
		// Equal widths mean the ANSI profile was stripped mid-test: the
		// demonstration is vacuous and cannot catch the bug class.
		t.Errorf("bug-class demonstration failed: style-then-fmt width (%d) equals pad-then-style width (%d); ANSI may not be present", buggedWidth, correctWidth)
	} else {
		// Expected: bugged width != 8 (fmt %-8s sees a long byte sequence
		// and thinks no padding is needed).
		t.Logf("pad-then-style width = %d (correct); style-then-fmt width = %d (demonstrates bug class)", correctWidth, buggedWidth)
	}
}

// TestColumnPaneLineWidths forces color and asserts every line a pane emits is
// exactly paneWidth wide, including a star (missing-hop) row and both a long and
// short IP. A collapsed cell from byte-length padding (the columns.go star-cell
// bug class) would make its line short; forced color makes that detectable.
func TestColumnPaneLineWidths(t *testing.T) {
	setTestTheme(t)
	forceColor(t)

	m := multiTestModel(t, withSize(120, 40))
	s := m.sessions[0]

	h1 := s.table.GetOrCreate(1)
	h1.MarkRoundSent()
	h1.AddSample(net.ParseIP("203.0.113.100"), 5*time.Millisecond, 0, hop.KindPrimary)
	h1.MarkRoundEnd()

	// TTL 2 sent but never answered -> star row.
	h2 := s.table.GetOrCreate(2)
	h2.MarkRoundSent()
	h2.MarkRoundEnd()

	h3 := s.table.GetOrCreate(3)
	h3.MarkRoundSent()
	h3.AddSample(net.ParseIP("203.0.113.1"), 6*time.Millisecond, 0, hop.KindPrimary)
	h3.MarkRoundEnd()

	const paneWidth = 40
	for _, focused := range []bool{false, true} {
		lines := m.renderPane(s, paneWidth, 3, focused)
		anyANSI := false
		for _, line := range lines {
			if strings.Contains(line, "\x1b[") {
				anyANSI = true
			}
			if w := lipgloss.Width(line); w != paneWidth {
				t.Errorf("focused=%v: pane line width = %d, want %d: %q", focused, w, paneWidth, stripANSI(line))
			}
		}
		if !anyANSI {
			t.Fatalf("focused=%v: no ANSI escapes — forceColor did not take effect; width assertions would be vacuous", focused)
		}
	}
}
