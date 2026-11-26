package tui

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/tonhe/viaduct/internal/asn"
	"github.com/tonhe/viaduct/internal/hop"
)

// Row alignment invariant for every view that draws a hop table.
//
// Every column starts at the same cell in every row and under its header, for
// every kind of row: a single-node hop, the first node of a multi-node (ECMP)
// hop, its later nodes, a hop that did not answer, and the destination. A row
// that needs room for something extra (the ECMP tree connector) takes it from
// the width of a column's content. It never moves the columns after it.
//
// The tests below render the same hop mix in IPv4 and IPv6, with and without
// multi-node hops, with one flow and with several, with and without the ASN
// column, in every view mode and at several terminal widths, and check the
// invariant in display cells on the rendered text.

// alignNode is one responding address at a hop.
type alignNode struct {
	ip      string
	host    string // "" = no reverse DNS yet
	replies int    // rounds answered, out of alignRounds; 0 = answers echo only
}

// alignHop is one TTL. No nodes means the hop never answered.
type alignHop struct {
	ttl   int
	nodes []alignNode
}

const alignRounds = 6

// alignSparkWidth is the width of the Spark column in the hop table.
const alignSparkWidth = 14

// alignLongHost is longer than the hostname column ever gets.
const alignLongHost = "ae-303-1215.core01.pop-east.transit.example.net"

// alignAddrs holds the fixture addresses per family, from the documentation
// ranges (RFC 5737, RFC 3849). The IPv6 ones include addresses of 38
// characters, the longest the documentation prefix can spell.
var alignAddrs = map[int][]string{
	4: {
		"192.0.2.1",
		"198.51.100.254",
		"203.0.113.101", "203.0.113.102", "203.0.113.103",
		"198.51.100.17", "198.51.100.18",
		"203.0.113.200",
	},
	6: {
		"2001:db8::1",
		"2001:db8:1111:2222:3333:4444:5555:6661",
		"2001:db8:aaaa:bbbb:cccc:dddd:eeee:fff1", "2001:db8:aaaa:bbbb:cccc:dddd:eeee:fff2", "2001:db8:aaaa:bbbb::3",
		"2001:db8:1111:2222:3333:4444:5555:7771", "2001:db8:1111:2222:3333:4444:5555:7772",
		"2001:db8:ffff:eeee:dddd:cccc:bbbb:aaaa",
	},
}

// alignHops builds the hop mix for a family: single-node hops, a hop that
// never answers, the destination, and, when ecmp is set, a three-node and a
// two-node hop. Without ecmp those two hops have one node each.
func alignHops(family int, ecmp bool) []alignHop {
	a := alignAddrs[family]
	hops := []alignHop{
		{ttl: 1, nodes: []alignNode{{a[0], "gw.example.net", 6}}},
		{ttl: 2, nodes: []alignNode{{a[1], alignLongHost, 6}}},
		{ttl: 3, nodes: []alignNode{{a[2], alignLongHost, 6}, {a[3], "", 4}, {a[4], "b.example.net", 2}}},
		{ttl: 4},
		{ttl: 5, nodes: []alignNode{{a[5], "", 5}, {a[6], alignLongHost, 3}}},
		// The destination answers echo only: its TTL-probe loss is 100% and
		// provisional, "100.0%?", the widest the Loss% cell gets.
		{ttl: 6, nodes: []alignNode{{a[7], "dst.example.net", 0}}},
	}
	if !ecmp {
		for i := range hops {
			if len(hops[i].nodes) > 1 {
				hops[i].nodes = hops[i].nodes[:1]
			}
		}
	}
	return hops
}

// seedAlignHops loads hops into session s as alignRounds closed rounds of
// numPaths probes each, and marks the last hop as the destination.
func seedAlignHops(s *TargetSession, hops []alignHop, numPaths int) {
	s.probeCfg.NumPaths = numPaths
	for _, fh := range hops {
		h := s.table.GetOrCreate(fh.ttl)
		for r := 0; r < alignRounds; r++ {
			h.MarkRoundSentProbes(numPaths)
			for n, node := range fh.nodes {
				if r < node.replies {
					rtt := time.Duration(fh.ttl*7+n*3+r) * time.Millisecond
					h.AddSample(net.ParseIP(node.ip), rtt, n, hop.KindPrimary)
				}
			}
			h.MarkRoundEnd()
		}
		for _, node := range fh.nodes {
			if node.replies == 0 {
				h.AddSample(net.ParseIP(node.ip), time.Duration(fh.ttl*7)*time.Millisecond, 0, hop.KindEcho)
			}
		}
		for _, pn := range h.GetNodes() {
			for _, node := range fh.nodes {
				if pn.GetIP().Equal(net.ParseIP(node.ip)) {
					pn.SetHostname(node.host)
				}
			}
		}
	}
	last := hops[len(hops)-1]
	s.table.GetOrCreate(last.ttl).MarkTarget(net.ParseIP(last.nodes[0].ip))
	s.maxTTLHit = last.ttl
}

// withAlignHops seeds the focused session with the hop mix.
func withAlignHops(family int, ecmp bool, numPaths int) testOpt {
	return func(m *Model) {
		seedAlignHops(m.sessions[0], alignHops(family, ecmp), numPaths)
	}
}

// withASNColumn attaches an enricher, which is what turns the ASN column on.
// Its cache is empty, so the cells show the pending marker.
func withASNColumn() testOpt {
	return func(m *Model) { m.enricher = asn.New(0) }
}

// alignCol is a column as the header line defines it: its label and the cell
// its label starts at.
type alignCol struct {
	name  string
	start int
}

// alignHeaderCols reads the columns off a header line. Labels are separated
// by spaces; the one label with spaces in it is kept whole.
func alignHeaderCols(header []rune) []alignCol {
	text := strings.Replace(string(header), pdShareHeader, strings.ReplaceAll(pdShareHeader, " ", "_"), 1)
	rs := []rune(text)
	var cols []alignCol
	for i := 0; i < len(rs); i++ {
		if rs[i] == ' ' {
			continue
		}
		j := i
		for j < len(rs) && rs[j] != ' ' {
			j++
		}
		cols = append(cols, alignCol{name: string(rs[i:j]), start: i})
		i = j
	}
	return cols
}

// alignCells returns a stripped line as one rune per display cell. It fails
// the test when a rune is not one cell wide, since positions would then not
// be cells.
func alignCells(t *testing.T, stripped string) []rune {
	t.Helper()
	rs := []rune(stripped)
	if w := lipgloss.Width(stripped); w != len(rs) {
		t.Fatalf("line has %d runes but is %d cells wide; positions are not cells: %q", len(rs), w, stripped)
	}
	return rs
}

func isTreeGlyph(r rune) bool { return r == '├' || r == '└' || r == '─' }

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
}

// alignTable is a hop table cut out of a rendered view.
type alignTable struct {
	header []rune
	rows   [][]rune
}

// alignTableOf finds the hop table in a full-screen view: the header line
// (the last line that starts with "#" and has an IP column; the multi-target
// summary panel above it has a header of its own), then every line down to
// the first blank filler line or the status bar, which is the last line.
// Every line of the view must be exactly width cells: a row that runs past
// the right edge fails here.
func alignTableOf(t *testing.T, view string, width int) alignTable {
	t.Helper()
	lines := strings.Split(view, "\n")
	headerAt := -1
	for i, raw := range lines {
		if w := lipgloss.Width(raw); w != width {
			t.Errorf("line is %d cells wide, terminal is %d: %q", w, width, stripANSI(raw))
		}
		if s := stripANSI(raw); strings.HasPrefix(s, "#") && strings.Contains(s, " IP ") {
			headerAt = i
		}
	}
	if headerAt < 0 {
		t.Fatalf("no table header found in view:\n%s", stripANSI(view))
	}
	tbl := alignTable{header: alignCells(t, stripANSI(lines[headerAt]))}
	for _, raw := range lines[headerAt+1 : len(lines)-1] {
		line := stripANSI(raw)
		if strings.TrimSpace(line) == "" {
			break
		}
		tbl.rows = append(tbl.rows, alignCells(t, line))
	}
	return tbl
}

// alignRowCounts is how many rows of each kind a table check saw.
type alignRowCounts struct {
	data, tree, star, note int
}

// firstCell returns the cell of the first non-blank rune in row.
func firstCell(row []rune, from int) int {
	for i := from; i < len(row); i++ {
		if row[i] != ' ' {
			return i
		}
	}
	return -1
}

// assertTableAligned checks every row of tbl against the header's columns and
// returns how many rows of each kind it saw.
func assertTableAligned(t *testing.T, tbl alignTable) alignRowCounts {
	t.Helper()
	cols := alignHeaderCols(tbl.header)
	ipStart := -1
	for _, c := range cols {
		if c.name == "IP" {
			ipStart = c.start
		}
	}
	if len(cols) < 3 || cols[0].name != "#" || ipStart < 0 {
		t.Fatalf("unexpected header: %q", string(tbl.header))
	}

	var counts alignRowCounts
	fail := func(row []rune, format string, args ...interface{}) {
		t.Helper()
		t.Errorf("%s\n  header: %q\n  row:    %q", fmt.Sprintf(format, args...), string(tbl.header), string(row))
	}

	for _, row := range tbl.rows {
		text := strings.TrimSpace(string(row))
		if strings.HasPrefix(text, "...") {
			// "+N more paths" under a cut ECMP hop: it has no columns of its
			// own and starts in the IP column.
			counts.note++
			if at := firstCell(row, 0); at != ipStart {
				fail(row, "more-paths note starts at cell %d, the IP column starts at cell %d", at, ipStart)
			}
			continue
		}
		if fields := strings.Fields(text); len(fields) == 2 && fields[1] == "*" {
			counts.star++
			if at := firstCell(row, len(fields[0])); at != ipStart {
				fail(row, "no-reply marker is at cell %d, the IP column starts at cell %d", at, ipStart)
			}
			continue
		}
		counts.data++
		rowFailed := false
		fail := func(row []rune, format string, args ...interface{}) {
			t.Helper()
			if !rowFailed { // one report per row: the first column that is off
				fail(row, format, args...)
			}
			rowFailed = true
		}

		hasTree := false
		for i, r := range row {
			if !isTreeGlyph(r) {
				continue
			}
			hasTree = true
			if i < len("#  ")+1 || i >= ipStart-1 {
				fail(row, "tree connector at cell %d is outside the gap between # and IP (IP starts at cell %d)", i, ipStart)
				break
			}
		}
		if hasTree {
			counts.tree++
		}

		for i, c := range cols {
			end := len(row)
			if i+1 < len(cols) {
				end = cols[i+1].start
			}
			if c.start >= len(row) {
				fail(row, "row ends before column %s (cell %d)", c.name, c.start)
				break
			}
			if c.start > 0 && row[c.start-1] != ' ' {
				fail(row, "column %s: cell %d before it holds %q, want the column separator", c.name, c.start-1, row[c.start-1])
			}
			cell := make([]rune, end-c.start)
			copy(cell, row[c.start:end])
			if c.name == "#" {
				for k, r := range cell {
					if isTreeGlyph(r) {
						cell[k] = ' '
					}
				}
			}
			content := strings.TrimRight(string(cell), " ")
			if c.name == "Spark" {
				// The sparkline is drawn newest-right inside its 14 cells,
				// so it is held to the column's span, not to its first cell.
				if len([]rune(content)) > alignSparkWidth {
					fail(row, "column Spark: content runs %d cells, past the column's %d", len([]rune(content)), alignSparkWidth)
				}
				continue
			}
			switch {
			case content == "":
				if c.name == "IP" || c.name == "Hostname" || c.name == "Avg" {
					fail(row, "column %s is empty", c.name)
				}
			case strings.HasPrefix(content, " "):
				fail(row, "column %s: content %q does not start at the column's first cell (%d)", c.name, content, c.start)
			case c.name == "IP":
				if !isHexDigit([]rune(content)[0]) || strings.Contains(content, " ") {
					fail(row, "column IP: cell %d holds %q, want an address starting there", c.start, content)
				}
			case c.name == "Hostname":
				if strings.Contains(content, " ") {
					fail(row, "column Hostname: %q holds text from a neighbouring column", content)
				}
			}
		}
	}
	return counts
}

// alignWidths are the terminal widths checked. 60 is below what the fixed
// columns need with a long IPv6 address, so the address itself is shortened.
var alignWidths = []int{60, 80, 100, 120, 160, 200}

var alignViews = []ViewMode{ViewDefault, ViewHealth, ViewLatency, ViewVariability, ViewProbeDetail}

// Terminal heights: alignTall shows every node of every hop; alignShort has
// no room for the second ECMP hop's nodes, so it is cut to one node and a
// "+1 more paths" note.
const (
	alignTall  = 40
	alignShort = 12
)

// TestRowAlignment_HopTable is the invariant for the single-target hop table
// in every view mode.
func TestRowAlignment_HopTable(t *testing.T) {
	setTestTheme(t)
	forceColor(t)

	type variant struct {
		family, numPaths int
		ecmp, withASN    bool
		height           int
	}
	var variants []variant
	for _, family := range []int{4, 6} {
		for _, ecmp := range []bool{true, false} {
			// One flow is what an ICMP trace has; six is the default.
			for _, numPaths := range []int{1, 6} {
				for _, withASN := range []bool{false, true} {
					for _, height := range []int{alignTall, alignShort} {
						variants = append(variants, variant{family, numPaths, ecmp, withASN, height})
					}
				}
			}
		}
	}

	for _, v := range variants {
		for _, view := range alignViews {
			for _, w := range alignWidths {
				name := fmt.Sprintf("IPv%d/ecmp=%v/paths=%d/asn=%v/h=%d/%s/%d", v.family, v.ecmp, v.numPaths, v.withASN, v.height, view, w)
				t.Run(name, func(t *testing.T) {
					opts := []testOpt{withSize(w, v.height), withIPVersion(v.family), withView(view), withAlignHops(v.family, v.ecmp, v.numPaths)}
					if v.withASN {
						opts = append(opts, withASNColumn())
					}
					m := newTestModel(opts...)
					out := m.View()
					if !strings.Contains(out, "\x1b[") {
						t.Fatal("no ANSI in the rendered view: styled-cell padding would go unchecked")
					}
					counts := assertTableAligned(t, alignTableOf(t, out, w))

					// The fixture must have produced each row kind, or the
					// checks above passed on nothing.
					tall := v.height == alignTall
					wantData := 5
					if v.ecmp && tall {
						wantData = 8
					}
					if counts.data < wantData {
						t.Errorf("%d address rows checked, want at least %d", counts.data, wantData)
					}
					if counts.star != 1 {
						t.Errorf("%d no-reply rows checked, want 1", counts.star)
					}
					if !strings.Contains(stripANSI(out), "100.0%?") {
						t.Error("no row with the widest loss cell, 100.0%?")
					}
					if v.ecmp && view != ViewProbeDetail {
						if tall && counts.tree != 5 {
							t.Errorf("%d rows with a tree connector, want 5", counts.tree)
						}
						if !tall && (counts.note != 1 || counts.tree != 4) {
							t.Errorf("%d more-paths notes and %d rows with a tree connector, want 1 and 4", counts.note, counts.tree)
						}
					}
					if t.Failed() {
						t.Logf("rendered view:\n%s", stripANSI(out))
					}
				})
			}
		}
	}
}

// TestRowAlignment_MultiTargetDetail is the same invariant for the hop table
// under the multi-target summary panel.
func TestRowAlignment_MultiTargetDetail(t *testing.T) {
	setTestTheme(t)
	forceColor(t)

	for _, family := range []int{4, 6} {
		for _, view := range alignViews {
			for _, w := range alignWidths {
				t.Run(fmt.Sprintf("IPv%d/%s/%d", family, view, w), func(t *testing.T) {
					m := multiTestModel(t, withSize(w, 40), withIPVersion(family), withView(view), withAlignHops(family, true, 1))
					out := m.View()
					counts := assertTableAligned(t, alignTableOf(t, out, w))
					if counts.data != 8 || counts.star != 1 {
						t.Errorf("checked %d address rows and %d no-reply rows, want 8 and 1", counts.data, counts.star)
					}
					if t.Failed() {
						t.Logf("rendered view:\n%s", stripANSI(out))
					}
				})
			}
		}
	}
}

// TestRowAlignment_FinalSummary checks the plain-text table printed when the
// TUI exits: every field of every row starts where a header label starts.
func TestRowAlignment_FinalSummary(t *testing.T) {
	for _, family := range []int{4, 6} {
		for _, numPaths := range []int{1, 6} {
			t.Run(fmt.Sprintf("IPv%d/paths=%d", family, numPaths), func(t *testing.T) {
				m := newTestModel(withIPVersion(family), withAlignHops(family, true, numPaths))
				lines := strings.Split(strings.TrimRight(m.FinalSummary(), "\n"), "\n")
				if len(lines) < 7 {
					t.Fatalf("summary has %d lines:\n%s", len(lines), m.FinalSummary())
				}
				header := alignCells(t, lines[1])
				starts := map[int]string{}
				for _, c := range alignHeaderCols(header) {
					starts[c.start] = c.name
				}
				for _, line := range lines[2:] {
					for _, f := range alignHeaderCols(alignCells(t, line)) {
						if _, ok := starts[f.start]; !ok {
							t.Errorf("field %q starts at cell %d, where no column starts\n  header: %q\n  row:    %q", f.name, f.start, string(header), line)
						}
					}
				}
			})
		}
	}
}

// TestRowAlignment_ColumnView checks the panes of the multi-target column
// view. A pane's left-aligned columns (IP, Host) start under their header in
// every row; its right-aligned ones (Avg, Loss) end under it.
func TestRowAlignment_ColumnView(t *testing.T) {
	setTestTheme(t)
	forceColor(t)

	for _, family := range []int{4, 6} {
		for _, w := range alignWidths {
			t.Run(fmt.Sprintf("IPv%d/%d", family, w), func(t *testing.T) {
				m := multiTestModel(t, withSize(w, 40), withIPVersion(family), withAlignHops(family, true, 1))
				seedAlignHops(m.sessions[1], alignHops(family, false), 1)
				m.columnView = true
				out := m.View()

				// Cut the view into panes: the text between each pair of
				// vertical border glyphs, line by line.
				var panes [][][]rune
				for _, raw := range strings.Split(out, "\n") {
					if got := lipgloss.Width(raw); got != w {
						t.Errorf("line is %d cells wide, terminal is %d: %q", got, w, stripANSI(raw))
					}
					line := alignCells(t, stripANSI(raw))
					var bars []int
					for i, r := range line {
						if r == '│' {
							bars = append(bars, i)
						}
					}
					if len(bars) == 0 {
						continue
					}
					if len(bars)%2 != 0 {
						t.Fatalf("odd number of pane borders: %q", string(line))
					}
					if panes == nil {
						panes = make([][][]rune, len(bars)/2)
					}
					if len(bars)/2 != len(panes) {
						t.Fatalf("pane count changes between lines: %q", string(line))
					}
					for p := range panes {
						panes[p] = append(panes[p], line[bars[2*p]+1:bars[2*p+1]])
					}
				}
				// Below two minimum pane widths only the focused pane is shown.
				wantPanes := 2
				if w < 2*minPaneWidth+1 {
					wantPanes = 1
				}
				if len(panes) < wantPanes {
					t.Fatalf("want at least %d panes, got %d:\n%s", wantPanes, len(panes), stripANSI(out))
				}

				rowsChecked := 0
				for p, lines := range panes {
					cols := alignHeaderCols(lines[0])
					for _, row := range lines[1:] {
						if strings.TrimSpace(string(row)) == "" {
							continue // past this target's depth
						}
						if len(row) != len(lines[0]) {
							t.Errorf("pane %d: row is %d cells wide, header is %d: %q", p, len(row), len(lines[0]), string(row))
							continue
						}
						rowsChecked++
						for _, c := range cols {
							end := c.start + len([]rune(c.name))
							switch c.name {
							case "IP", "Host":
								if row[c.start-1] != ' ' {
									t.Errorf("pane %d: column %s: cell %d before it is not blank\n  header: %q\n  row:    %q", p, c.name, c.start-1, string(lines[0]), string(row))
								}
								if c.name == "IP" && row[c.start] == ' ' {
									t.Errorf("pane %d: column IP does not start at cell %d\n  header: %q\n  row:    %q", p, c.start, string(lines[0]), string(row))
								}
							case "Avg", "Loss":
								if end < len(row) && row[end] != ' ' {
									t.Errorf("pane %d: column %s runs past its last cell %d\n  header: %q\n  row:    %q", p, c.name, end-1, string(lines[0]), string(row))
								}
								star := strings.Contains(string(row), "*")
								if !star && row[end-1] == ' ' {
									t.Errorf("pane %d: column %s does not end at cell %d\n  header: %q\n  row:    %q", p, c.name, end-1, string(lines[0]), string(row))
								}
							}
						}
					}
				}
				if rowsChecked < 6*wantPanes {
					t.Errorf("only %d pane rows checked, want at least %d", rowsChecked, 6*wantPanes)
				}
				if t.Failed() {
					t.Logf("rendered view:\n%s", stripANSI(out))
				}
			})
		}
	}
}
