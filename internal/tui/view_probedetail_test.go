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

// withProbeDetailHops seeds a 2-hop model for probe detail testing.
//
// Hop 1 (192.0.2.1):
//   - ICMP-TTL: 10 rounds, 1 lost (RecvRounds=9) → 10% round loss
//   - Primary (UDP): 10 rounds, 2 lost (RecvRounds=8) → 20% round loss
//   - Echo: 6 sent, 6 received (0%) — above guard of 5
//
// Hop 2 (192.0.2.2):
//   - Echo: 3 sent only (below guard of 5)
func withProbeDetailHops() testOpt {
	return func(m *Model) {
		ip1 := net.ParseIP("192.0.2.1")
		ip2 := net.ParseIP("192.0.2.2")

		h1 := m.sessions[0].table.GetOrCreate(1)
		h2 := m.sessions[0].table.GetOrCreate(2)

		// Seed hop1 with initial RTT samples to create the PathNode.
		h1.AddSample(ip1, 5*time.Millisecond, 0, hop.KindICMPTTL)
		h1.AddSample(ip1, 5*time.Millisecond, 0, hop.KindPrimary)
		// Seed hop2 with a Primary sample to create its PathNode.
		h2.AddSample(ip2, 10*time.Millisecond, 0, hop.KindPrimary)

		pn1 := h1.PrimaryNode()
		pn2 := h2.PrimaryNode()

		// --- Hop1: drive 10 rounds of per-probe-type stats ---
		//
		// MarkRoundEnd calls MarkProbeRoundEnd on nodes which:
		//   - icmpTTL.markRoundEnd(true)  → icmpTTL.Sent++, plus RecvRounds++ if responded
		//   - primary.markRoundEnd(true)  → primary.Sent++, plus RecvRounds++ if responded
		//   - icmpEcho.markRoundEnd(false) → no Sent++, just RecvRounds++ if responded
		//
		// To get icmpTTL=10% loss (1/10) and primary=20% loss (2/10):
		//   Rounds 1-9: both ICMP-TTL and Primary respond.
		//   Round 9: Primary does NOT respond.
		//   Round 10: neither responds.

		// Rounds 1-8: both respond.
		for i := 0; i < 8; i++ {
			h1.MarkRoundSent()
			if pn1 != nil {
				pn1.AddSample(5*time.Millisecond, 0, hop.KindICMPTTL)
				pn1.AddSample(5*time.Millisecond, 0, hop.KindPrimary)
			}
			h1.MarkRoundEnd()
		}

		// Round 9: ICMP-TTL responds, Primary does NOT respond.
		h1.MarkRoundSent()
		if pn1 != nil {
			pn1.AddSample(5*time.Millisecond, 0, hop.KindICMPTTL)
		}
		h1.MarkRoundEnd()

		// Round 10: neither responds.
		h1.MarkRoundSent()
		h1.MarkRoundEnd()

		// Drive echo: 6 sent, 6 received (ProbeLossPercent = 0%).
		// Must call MarkEchoSent before AddSample(KindEcho) to ensure Sent==Received.
		if pn1 != nil {
			for i := 0; i < 6; i++ {
				pn1.MarkEchoSent()
				pn1.AddSample(3*time.Millisecond, 0, hop.KindEcho)
			}
		}

		// --- Hop2: 1 round, 3 echo sends only (below guard of 5) ---
		h2.MarkRoundSent()
		h2.MarkRoundEnd()
		if pn2 != nil {
			for i := 0; i < 3; i++ {
				pn2.MarkEchoSent()
			}
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

	// Hop 1 probe cells must render "recvd (loss%)" for each probe type:
	// ICMP-TTL 9 recv rounds of 10 (10%), primary 8 of 10 (20%), echo 6/6 (0%).
	for _, cell := range []string{"9 (10%)", "8 (20%)", "6 (0%)"} {
		if !strings.Contains(stripped, cell) {
			t.Errorf("probe detail missing hop 1 cell %q; got:\n%s", cell, stripped)
		}
	}

	// Hop 2 echo column must show running count "3/5" (below guard).
	if !strings.Contains(stripped, "3/5") {
		t.Errorf("hop 2 Echo cell missing '3/5' guard display; got:\n%s", stripped)
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

// ---------------------------------------------------------------------------
// probeCell / echoCell unit tests
// ---------------------------------------------------------------------------

// TestProbeCell verifies the probeCell helper formats and pads correctly.
func TestProbeCell(t *testing.T) {
	cases := []struct {
		recvd   int
		lossPct float64
		width   int
		wantStr string
	}{
		{8, 20.0, 11, "8 (20%)    "},
		{10, 0.0, 11, "10 (0%)    "},
		{5, 50.0, 11, "5 (50%)    "},
	}
	for _, tc := range cases {
		stripped := stripANSI(probeCell(tc.recvd, tc.lossPct, tc.width))
		if stripped != tc.wantStr {
			t.Errorf("probeCell(%d, %.1f, %d) = %q, want %q", tc.recvd, tc.lossPct, tc.width, stripped, tc.wantStr)
		}
	}
}

// TestEchoCell verifies the echoCell helper shows the running count when below guard.
func TestEchoCell(t *testing.T) {
	// Below guard: sent < 5.
	below := hop.ProbeStats{Sent: 3, Received: 0}
	stripped := stripANSI(echoCell(below, 11))
	if !strings.Contains(stripped, "3/5") {
		t.Errorf("echoCell below guard = %q, want '3/5'", stripped)
	}

	// At guard: sent == 5, received all (0% loss).
	atGuard := hop.ProbeStats{Sent: 5, Received: 5}
	stripped2 := stripANSI(echoCell(atGuard, 11))
	if !strings.Contains(stripped2, "5 (0%)") {
		t.Errorf("echoCell at guard = %q, want '5 (0%%)'", stripped2)
	}
}
