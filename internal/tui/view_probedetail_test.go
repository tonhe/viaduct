package tui

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/tonhe/viaduct/internal/hop"
)

// ---------------------------------------------------------------------------
// TestViewModeCycleIncludesProbeDetail
// ---------------------------------------------------------------------------

// TestViewModeCycleIncludesProbeDetail verifies that ViewProbeDetail is in the
// cycle between ViewVariability and ViewDefault, and that its String() returns
// "Probe Detail".
func TestViewModeCycleIncludesProbeDetail(t *testing.T) {
	if ViewVariability.Next() != ViewProbeDetail {
		t.Errorf("ViewVariability.Next() = %v, want ViewProbeDetail", ViewVariability.Next())
	}
	if ViewProbeDetail.Next() != ViewDefault {
		t.Errorf("ViewProbeDetail.Next() = %v, want ViewDefault", ViewProbeDetail.Next())
	}
	if ViewProbeDetail.String() != "Probe Detail" {
		t.Errorf("ViewProbeDetail.String() = %q, want \"Probe Detail\"", ViewProbeDetail.String())
	}
}

// ---------------------------------------------------------------------------
// Probe detail fixture helpers
// ---------------------------------------------------------------------------

// withProbeDetailHops seeds a 2-hop model for probe detail testing. Replies
// are recorded through the hop, as the collector does, so the hop's loss and
// the node's counts come from the same events. Each round sends two probes:
// the ICMP TTL probe and one UDP probe.
//
// Hop 1 (192.0.2.1), 10 rounds:
//   - rounds 1 to 8: both answered; round 9: ICMP only; round 10: neither
//   - ICMP-TTL 9 of 10, UDP 8 of 10, hop loss 3 of 20 = 15%
//   - Echo: 6 sent, 6 answered (fewer than 20, so the loss is provisional)
//
// Hop 2 (192.0.2.2), 1 round: UDP answered, ICMP not; 3 echoes sent, none
// answered yet.
func withProbeDetailHops() testOpt {
	return func(m *Model) {
		ip1 := net.ParseIP("192.0.2.1")
		ip2 := net.ParseIP("192.0.2.2")
		h1 := m.sessions[0].table.GetOrCreate(1)
		h2 := m.sessions[0].table.GetOrCreate(2)

		for round := 1; round <= 10; round++ {
			h1.MarkRoundSentProbes(2)
			if round <= 9 {
				h1.AddSample(ip1, 5*time.Millisecond, 0, hop.KindICMPTTL)
			}
			if round <= 8 {
				h1.AddSample(ip1, 5*time.Millisecond, 1, hop.KindPrimary)
			}
			h1.MarkRoundEnd()
		}
		pn1 := h1.PrimaryNode()
		for i := 0; i < 6; i++ {
			pn1.MarkEchoSent()
			h1.AddSample(ip1, 3*time.Millisecond, 0, hop.KindEcho)
		}

		h2.MarkRoundSentProbes(2)
		h2.AddSample(ip2, 10*time.Millisecond, 1, hop.KindPrimary)
		h2.MarkRoundEnd()
		pn2 := h2.PrimaryNode()
		for i := 0; i < 3; i++ {
			pn2.MarkEchoSent()
		}
	}
}


// ---------------------------------------------------------------------------
// TestProbeDetailRender
// ---------------------------------------------------------------------------

// TestProbeDetailRender builds a 2-hop model, renders the Probe Detail view,
// and compares against the golden file. It also asserts structural properties
// about the rendered output.
func TestProbeDetailRender(t *testing.T) {
	setTestTheme(t)

	m := newTestModel(withSize(120, 24), withView(ViewProbeDetail), withProbeDetailHops())
	got := m.View()
	stripped := stripANSI(got)

	// Assert columns are present in the header.
	if !strings.Contains(stripped, "ICMP-TTL") {
		t.Errorf("probe detail header missing ICMP-TTL column; got:\n%s", stripped)
	}
	if !strings.Contains(stripped, "UDP-TTL") {
		t.Errorf("probe detail header missing UDP-TTL column (protocol %q); got:\n%s",
			m.sessions[0].protocolName, stripped)
	}
	if !strings.Contains(stripped, "Echo") {
		t.Errorf("probe detail header missing Echo column; got:\n%s", stripped)
	}
	if !strings.Contains(stripped, "Rnd") {
		t.Errorf("probe detail header missing Rnd column; got:\n%s", stripped)
	}

	// The hop's loss and each node's share have their own columns.
	for _, col := range []string{"Loss%", "% of replies"} {
		if !strings.Contains(stripped, col) {
			t.Errorf("probe detail header missing %q column; got:\n%s", col, stripped)
		}
	}

	// Hop 1 probe cells are counts, received/sent, per probe type: ICMP-TTL
	// 9 of 10, UDP 9 of 10 (8 in rounds plus the fixture's first reply), echo
	// 6 of 6. No cell carries a per-node loss percentage.
	for _, cell := range []string{"9/10", "6/6"} {
		if !strings.Contains(stripped, cell) {
			t.Errorf("probe detail missing hop 1 cell %q; got:\n%s", cell, stripped)
		}
	}
	if strings.Contains(stripped, "%)") {
		t.Errorf("probe detail still shows a per-node loss percentage; got:\n%s", stripped)
	}

	// Hop 2 echo column shows its running count: 3 sent, none answered yet.
	if !strings.Contains(stripped, "0/3") {
		t.Errorf("hop 2 Echo cell missing its 0/3 count; got:\n%s", stripped)
	}

	// Rnd column: hop1 must show h.Rounds() = 10. Space-delimited so the
	// assertion cannot match the "10" inside the "(10%)" loss cell.
	if !strings.Contains(stripped, " 10 ") {
		t.Errorf("Rnd column missing hop1 round count '10'; got:\n%s", stripped)
	}

	path := filepath.Join("testdata", "view", "probedetail_single.golden")
	goldenCompare(t, path, got)
}

// ---------------------------------------------------------------------------
// TestProbeDetailRenderMulti
// ---------------------------------------------------------------------------

// TestProbeDetailRenderMulti verifies that the probe detail view renders the
// focused session correctly in a multi-target model.
func TestProbeDetailRenderMulti(t *testing.T) {
	setTestTheme(t)

	m := multiTestModel(t, withSize(120, 40), withView(ViewProbeDetail), withProbeDetailHops())
	// Focus stays on sessions[0] which has the detail hops seeded.
	got := m.View()
	stripped := stripANSI(got)

	// The focused session's probe detail must appear.
	if !strings.Contains(stripped, "ICMP-TTL") {
		t.Errorf("multi probe detail missing ICMP-TTL header; got:\n%s", stripped)
	}

	path := filepath.Join("testdata", "view", "probedetail_multi.golden")
	goldenCompare(t, path, got)
}

// ---------------------------------------------------------------------------
// TestProbeDetailStyledCellWidths
// ---------------------------------------------------------------------------

// TestProbeDetailStyledCellWidths verifies that every styled cell in the
// probe detail hop rows has the correct visual width — no byte-length padding
// of styled strings. Uses forceColor to ensure ANSI is present.
func TestProbeDetailStyledCellWidths(t *testing.T) {
	setTestTheme(t)
	forceColor(t)

	m := newTestModel(withSize(120, 24), withView(ViewProbeDetail), withProbeDetailHops())
	out := m.View()

	// Sanity: ANSI escapes must be present for width assertions to be meaningful.
	if !strings.Contains(out, "\x1b[") {
		t.Fatal("rendered output contains no ANSI escapes — forceColor did not work; width assertions would be meaningless")
	}

	lines := strings.Split(out, "\n")

	// Identify hop rows (contain known fixture IPs).
	hopIPs := []string{"192.0.2.1", "192.0.2.2"}
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
		t.Fatalf("no hop rows found in probe detail output:\n%s", stripANSI(out))
	}

	// All hop rows must share the same visual width (model width after lineBg pads).
	var widths []int
	for _, row := range hopRows {
		widths = append(widths, lipgloss.Width(row))
	}
	for i := 1; i < len(widths); i++ {
		if widths[i] != widths[0] {
			t.Errorf("hop row %d has visual width %d, but row 0 has %d — widths must be equal",
				i, widths[i], widths[0])
			t.Logf("row 0: %q", stripANSI(hopRows[0]))
			t.Logf("row %d: %q", i, stripANSI(hopRows[i]))
		}
	}
}

// ---------------------------------------------------------------------------
// TestE2E_DKeyCycleProbeDetail
// ---------------------------------------------------------------------------

// TestE2E_DKeyCycleProbeDetail presses "d" five times and asserts that the
// rendered view passes through "Probe Detail" and returns to Default.
//
// After 4 presses the view mode is ViewProbeDetail; the test waits for a
// rendered frame containing "View: Probe Detail" (the frame header — unique to
// this mode; the status-bar next-mode hint in other views says only
// "Probe Detail") before pressing the 5th "d" to wrap back to Default.
func TestE2E_DKeyCycleProbeDetail(t *testing.T) {
	tm, _ := startE2E(t, withSize(120, 24))

	for i := 0; i < 4; i++ {
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	}

	// Assert an actual rendered frame shows the Probe Detail view header.
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return strings.Contains(string(bts), "View: Probe Detail")
	}, teatest.WithDuration(2*time.Second))

	// 5th press wraps the cycle back to Default.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	finalModel := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second))
	m := finalModel.(Model)

	if m.viewMode != ViewDefault {
		t.Errorf("viewMode after 5 d-presses = %v, want ViewDefault", m.viewMode)
	}
}

