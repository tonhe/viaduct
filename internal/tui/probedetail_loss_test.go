package tui

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// Probe Detail, per the Owner decision on viaduct#80:
//
//   - the hop line shows per-probe loss, the same figure as the main table,
//     including ~N%† and N%?
//   - each node line shows its share of the hop's replies, labelled as a
//     share, never as loss
//   - nothing labelled "Loss" shows a per-node figure
//
// The per-type cells are counts (received/sent), as in the JSON.

// pdRound runs one round on h: sent TTL-limited probes, the given replies
// (first of each node's is the ICMP probe when icmpFrom names that node),
// then one echo to every known node, answered.
func pdRound(h *hop.Hop, sent int, icmpFrom string, primary map[string]int) {
	h.MarkRoundSentProbes(sent)
	if icmpFrom != "" {
		h.AddSample(net.ParseIP(icmpFrom), 10*time.Millisecond, 0, hop.KindICMPTTL)
	}
	flow := 0
	for ip, n := range primary {
		for i := 0; i < n; i++ {
			h.AddSample(net.ParseIP(ip), 10*time.Millisecond, flow, hop.KindPrimary)
			flow++
		}
	}
	for _, n := range h.GetNodes() {
		n.MarkEchoSent()
		h.AddSample(n.GetIP(), 10*time.Millisecond, 0, hop.KindEcho)
	}
	h.MarkRoundEnd()
}

func pdModel(w, h int) Model {
	m := newTestModel(withSize(w, h))
	m.viewMode = ViewProbeDetail
	return m
}

// pdRows returns the Probe Detail body rows (after the column header) as
// plain text.
func pdRows(m Model) (header string, rows []string) {
	lines := m.renderProbeDetailView()
	for i, l := range lines {
		l = strings.TrimRight(stripANSI(l), " ")
		if i == 0 {
			header = l
			continue
		}
		rows = append(rows, l)
	}
	return header, rows
}

func TestProbeDetail_HopLineShowsTheHopsLossFigure(t *testing.T) {
	m := pdModel(120, 30)
	tbl := m.sessions[0].table

	// Hop 1: answers the ICMP probe, never the six UDP probes, every echo.
	// 45 rounds: judged rate-limited, ~0%†.
	limited := tbl.GetOrCreate(1)
	for r := 0; r < 45; r++ {
		pdRound(limited, 7, "192.0.2.1", nil)
	}
	// Hop 2: the same behaviour after 12 rounds: not yet known, 85.7%?.
	early := tbl.GetOrCreate(2)
	for r := 0; r < 12; r++ {
		pdRound(early, 7, "192.0.2.2", nil)
	}
	// Hop 3: the destination answers ICMP and 1 of 6 UDP probes; echoes are
	// lost at the same rate... here every echo is answered for 12 rounds, so
	// the figure is the raw per-probe loss, provisional: 71.4%?.
	dst := tbl.GetOrCreate(3)
	for r := 0; r < 12; r++ {
		pdRound(dst, 7, "198.51.100.1", map[string]int{"198.51.100.1": 1})
	}

	header, rows := pdRows(m)
	if !strings.Contains(header, "Loss%") {
		t.Fatalf("header has no Loss%% column: %q", header)
	}
	for i, want := range []string{"~0%†", "85.7%?", "71.4%?"} {
		if !strings.Contains(rows[i], want) {
			t.Errorf("hop %d line = %q, want the hop's figure %s as in the main table", i+1, rows[i], want)
		}
		if got := hop.FormatLoss(tbl.Get(i + 1).Loss()); got != want {
			t.Errorf("fixture: hop %d main-table figure = %s, want %s", i+1, got, want)
		}
	}
}

func TestProbeDetail_NodeLinesShowAShareOfRepliesNotLoss(t *testing.T) {
	m := pdModel(120, 30)
	h := m.sessions[0].table.GetOrCreate(1)
	// ECMP hop, nothing lost: node A answers the ICMP probe and 2 UDP flows,
	// node B answers 4 UDP flows... wait for both nodes to exist before
	// counting shares: 40 rounds, A 3 replies a round, B 4.
	for r := 0; r < 40; r++ {
		pdRound(h, 7, "192.0.2.11", map[string]int{"192.0.2.11": 2, "192.0.2.12": 4})
	}
	if h.LossPercent() != 0 {
		t.Fatalf("fixture: hop loss = %.1f, want 0", h.LossPercent())
	}

	header, rows := pdRows(m)
	if !strings.Contains(header, "% of replies") {
		t.Fatalf("header does not label the share column %q: %q", "% of replies", header)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows for a two-node hop, want 2:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	var a, b string
	for _, r := range rows {
		if strings.Contains(r, "192.0.2.11") {
			a = r
		}
		if strings.Contains(r, "192.0.2.12") {
			b = r
		}
	}
	if !strings.Contains(a, "43%") || !strings.Contains(b, "57%") {
		t.Errorf("shares: A %q should show 43%% (120 of 280 replies), B %q 57%% (160 of 280)", a, b)
	}
	// The hop's loss appears once, on the hop's first line; the other node
	// line has no loss figure.
	lossCol := strings.Index(header, "Loss%")
	figures := 0
	for _, r := range rows {
		cell := strings.TrimSpace(r[lossCol : lossCol+8])
		if cell != "-" {
			figures++
			if cell != "0.0%" {
				t.Errorf("hop line loss cell = %q, want 0.0%%", cell)
			}
		}
	}
	if figures != 1 {
		t.Errorf("%d node lines carry a loss figure, want exactly 1 (the hop's)", figures)
	}
	// No cell pairs a per-node count with a percentage in brackets, the old
	// "recvd (loss%)" form.
	for _, r := range rows {
		if strings.Contains(r, "%)") {
			t.Errorf("row still shows a per-node loss percentage: %q", r)
		}
	}
}

func TestProbeDetail_TypeCellsAreCounts(t *testing.T) {
	m := pdModel(120, 30)
	h := m.sessions[0].table.GetOrCreate(1)
	for r := 0; r < 10; r++ {
		pdRound(h, 7, "192.0.2.1", map[string]int{"192.0.2.1": 3})
	}
	_, rows := pdRows(m)
	// ICMP TTL: 10 of 10. UDP: 30 of 60. Echo: 9 of 9 (echoes start once
	// the node is known, so one fewer than the rounds).
	for _, want := range []string{"10/10", "30/60"} {
		if !strings.Contains(rows[0], want) {
			t.Errorf("row %q does not show the count %s", rows[0], want)
		}
	}
}

// In ICMP mode no primary-protocol probes are sent, so Probe Detail has no
// UDP-TTL or TCP-TTL column (it used to show a UDP-TTL column of dashes).
// UDP and TCP modes keep their primary column.
func TestProbeDetail_PrimaryColumnByProtocol(t *testing.T) {
	for _, tc := range []struct {
		proto      string
		wantCol    string // "" means no primary column
		wantHeader string
	}{
		{"icmp", "", "#   IP                Loss%    % of replies ICMP-TTL  Echo      Rnd"},
		{"udp", "UDP-TTL", "#   IP                Loss%    % of replies ICMP-TTL  UDP-TTL   Echo      Rnd"},
		{"tcp", "TCP-TTL", "#   IP                Loss%    % of replies ICMP-TTL  TCP-TTL   Echo      Rnd"},
	} {
		t.Run(tc.proto, func(t *testing.T) {
			m := pdModel(120, 30)
			m.sessions[0].protocolName = tc.proto
			h := m.sessions[0].table.GetOrCreate(1)
			for r := 0; r < 10; r++ {
				if tc.wantCol == "" {
					pdRound(h, 1, "192.0.2.1", nil)
				} else {
					pdRound(h, 2, "192.0.2.1", map[string]int{"192.0.2.1": 1})
				}
			}
			header, rows := pdRows(m)
			if header != tc.wantHeader {
				t.Errorf("header =\n%q, want\n%q", header, tc.wantHeader)
			}
			for _, col := range []string{"UDP-TTL", "TCP-TTL"} {
				if col != tc.wantCol && strings.Contains(header, col) {
					t.Errorf("%s mode header has a %s column: %q", tc.proto, col, header)
				}
			}
			if tc.wantCol == "" {
				// ICMP 10/10, echo 10/10, and no dash cell for a primary column.
				if strings.Count(rows[0], "10/10") != 2 || strings.Contains(rows[0], "—") {
					t.Errorf("ICMP mode row = %q, want ICMP and Echo counts and no primary cell", rows[0])
				}
			} else if strings.Count(rows[0], "10/10") != 3 {
				t.Errorf("%s mode row = %q, want ICMP, primary and Echo counts of 10/10", tc.proto, rows[0])
			}
			for _, w := range []int{60, 80, 120} {
				m.width = w
				assertFits(t, tc.proto+" Probe Detail", m.View(), w, 30)
			}
		})
	}
}

// Without a primary column the remaining columns drop in the same order:
// Rnd, Echo, ICMP-TTL.
func TestComputePDLayout_NoPrimaryKeepsDropOrder(t *testing.T) {
	full := computePDLayout(0, 17, false)
	if full.showPrimary || !full.showICMP || !full.showEcho || !full.showRnd {
		t.Fatalf("unknown width without primary: %+v", full)
	}
	base := pdWidthTTL + 1 + 17 + 1 + pdWidthLoss + 1 + pdWidthShare
	for _, tc := range []struct {
		width           int
		icmp, echo, rnd bool
	}{
		{base + 2*(1+pdWidthCount) + 1 + pdWidthRnd, true, true, true},
		{base + 2*(1+pdWidthCount), true, true, false},
		{base + 1 + pdWidthCount, true, false, false},
		{base, false, false, false},
	} {
		l := computePDLayout(tc.width, 17, false)
		if l.showPrimary || l.showICMP != tc.icmp || l.showEcho != tc.echo || l.showRnd != tc.rnd {
			t.Errorf("width %d: %+v, want icmp=%v echo=%v rnd=%v", tc.width, l, tc.icmp, tc.echo, tc.rnd)
		}
		if l.width() > tc.width {
			t.Errorf("width %d: layout is %d wide", tc.width, l.width())
		}
	}
}

func TestProbeDetail_ShowsStartErrorAlertAndExportStatus(t *testing.T) {
	// A focused target that failed to start shows its error, not an empty table.
	m := pdModel(120, 30)
	m.sessions[0].startErr = errors.New("resolve example.com: no such host")
	if out := stripANSI(m.View()); !strings.Contains(out, "no such host") {
		t.Errorf("Probe Detail does not show the focused target's start error:\n%s", out)
	}

	// Alert line and export status, as in the default view.
	m = pdModel(120, 30)
	h := m.sessions[0].table.GetOrCreate(1)
	pdRound(h, 7, "192.0.2.1", nil)
	ae := NewAlertEngine(1, 0, 1)
	ae.Update(55.7, 0)
	m.sessions[0].alertEngine = ae
	m.exportStatusMsg = "Exported: via-export.json"
	m.exportStatusTime = m.now()
	out := stripANSI(m.View())
	if !strings.Contains(out, "Sustained loss") {
		t.Errorf("Probe Detail does not show the alert line:\n%s", out)
	}
	if !strings.Contains(out, "Exported: via-export.json") {
		t.Errorf("Probe Detail does not show the export status:\n%s", out)
	}
}

func TestProbeDetail_RowsFitTheTerminal(t *testing.T) {
	for _, w := range []int{40, 60, 80, 120} {
		m := pdModel(w, 30)
		m.sessions[0].target = "a-rather-long-hostname.subdomain.example.net"
		m.sessions[0].targetIP = net.ParseIP("2001:db8:1234:5678:9abc:def0:1234:5678")
		for ttl := 1; ttl <= 4; ttl++ {
			h := m.sessions[0].table.GetOrCreate(ttl)
			for r := 0; r < 25; r++ {
				pdRound(h, 7, "2001:db8:1234:5678:9abc:def0:1234:567"+string(rune('0'+ttl)), map[string]int{"2001:db8:1234:5678:9abc:def0:1234:567" + string(rune('0'+ttl)): 6})
			}
		}
		assertFits(t, "Probe Detail", m.View(), w, 30)
	}
}

func TestCountCell(t *testing.T) {
	for _, tc := range []struct {
		received, sent int
		want           string
	}{
		{30, 60, "30/60    "},
		{10, 10, "10/10    "},
		{0, 0, "—        "},
	} {
		if got := stripANSI(countCell(tc.received, tc.sent, false)); got != tc.want {
			t.Errorf("countCell(%d, %d) = %q, want %q", tc.received, tc.sent, got, tc.want)
		}
	}
}

func TestShareCell(t *testing.T) {
	if got := strings.TrimSpace(stripANSI(shareCell(120, 280))); got != "43%" {
		t.Errorf("shareCell(120, 280) = %q, want 43%%", got)
	}
	if got := strings.TrimSpace(stripANSI(shareCell(0, 0))); got != "-" {
		t.Errorf("shareCell with no replies = %q, want a dash", got)
	}
}

func TestComputePDLayout_DropsCountColumnsBeforeTheAddress(t *testing.T) {
	wide := computePDLayout(120, 39, true)
	if !wide.showICMP || !wide.showPrimary || !wide.showEcho || !wide.showRnd || wide.ipw != 39 {
		t.Errorf("at 120 columns everything fits: %+v", wide)
	}
	for _, w := range []int{40, 60, 80} {
		l := computePDLayout(w, 39, true)
		if l.width() > w && l.ipw > minIPColWidth {
			t.Errorf("layout at width %d is %d wide: %+v", w, l.width(), l)
		}
	}
	if l := computePDLayout(80, 15, true); !l.showICMP || !l.showPrimary || !l.showEcho {
		t.Errorf("an IPv4 path at 80 columns keeps the three count columns: %+v", l)
	}
}

// In column view the panes are the view. Cycling to Probe Detail with d must
// not replace them with one target's table.
func TestProbeDetail_DoesNotReplaceColumnView(t *testing.T) {
	m := multiTargetModel(120, 30, 3, 5)
	m.columnView = true
	m.viewMode = ViewProbeDetail
	out := stripANSI(m.View())
	if !strings.Contains(out, "t01.example") || !strings.Contains(out, "c:summary") {
		t.Errorf("column view was replaced when the view mode is Probe Detail:\n%s", out)
	}
	if strings.Contains(out, "% of replies") {
		t.Errorf("Probe Detail table is shown in column view:\n%s", out)
	}
}

// The four columns that are always shown fit the 40-column minimum layout:
// once the address is at its minimum, the loss column gives up its spare
// cell, and only then.
func TestComputePDLayout_FitsTheMinimumWidth(t *testing.T) {
	for _, hasPrimary := range []bool{true, false} {
		l := computePDLayout(40, 39, hasPrimary)
		if l.width() != 40 || l.ipw != minIPColWidth || l.lossw != minWidthLossCell {
			t.Errorf("at 40 columns (primary=%v): width %d, %+v", hasPrimary, l.width(), l)
		}
		if l := computePDLayout(41, 39, hasPrimary); l.width() != 41 || l.lossw != pdWidthLoss {
			t.Errorf("at 41 columns (primary=%v) the loss column keeps its width: width %d, %+v", hasPrimary, l.width(), l)
		}
	}
	if l := computePDLayout(0, 39, true); l.lossw != pdWidthLoss {
		t.Errorf("unknown terminal width: %+v", l)
	}
	// The narrowed cell still holds the longest loss figure.
	for _, kind := range []hop.LossKind{hop.LossMeasured, hop.LossRateLimited, hop.LossProvisional} {
		if s := hop.FormatLoss(100, kind); len([]rune(s)) > minWidthLossCell {
			t.Errorf("%q is wider than the narrowed loss cell (%d)", s, minWidthLossCell)
		}
	}
}
