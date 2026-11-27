package tui

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/tonhe/viaduct/internal/asn"
)

// Hop-table cells are cut and padded in display cells, and a round count too
// wide for its column is shortened, so no cell moves the columns after it.

func TestTruncateCells(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"gw.example.net", 20, "gw.example.net"},
		{"gw.example.net", 14, "gw.example.net"},
		{"gw.example.net", 5, "gw.ex"},
		{"gw.example.net", 0, ""},
		{"", 3, ""},
		// Two bytes, one cell each: a byte cut would stop at 3 cells or
		// split the "ö".
		{"cöre.pöp.example.net", 6, "cöre.p"},
		{"äöü", 2, "äö"},
		// Two cells each: a wide character that would straddle the limit is
		// dropped, so the result is a cell short.
		{"東京ア", 4, "東京"},
		{"東京ア", 5, "東京"},
		{"a東京", 2, "a"},
	}
	for _, c := range cases {
		got := truncateCells(c.in, c.n)
		if got != c.want {
			t.Errorf("truncateCells(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncateCells(%q, %d) = %q is not valid UTF-8", c.in, c.n, got)
		}
		if w := lipgloss.Width(got); w > c.n && c.n >= 0 {
			t.Errorf("truncateCells(%q, %d) is %d cells wide", c.in, c.n, w)
		}
	}
}

func TestFormatRounds(t *testing.T) {
	cases := []struct {
		n, width int
		want     string
	}{
		{0, 5, "0"},
		{6, 5, "6"},
		{99999, 5, "99999"},
		{100000, 5, "100k"},
		{123456, 5, "123k"},
		{9999999, 5, "9999k"},
		{10000000, 5, "10M"},
		{12345678, 5, "12M"},
		{99999999999, 5, "99G"},
		{1234, 3, "1k"},
		{123, 3, "123"},
	}
	for _, c := range cases {
		got := formatRounds(c.n, c.width)
		if got != c.want {
			t.Errorf("formatRounds(%d, %d) = %q, want %q", c.n, c.width, got, c.want)
		}
		if len(got) > c.width {
			t.Errorf("formatRounds(%d, %d) = %q is wider than %d", c.n, c.width, got, c.width)
		}
	}
}

// TestRenderASNCell_CutsByCells: an ASN label longer than its column is cut
// in display cells, and the cell is exactly the column wide.
func TestRenderASNCell_CutsByCells(t *testing.T) {
	ip := net.ParseIP("192.0.2.1")
	cases := []struct {
		org   string
		width int
		want  string
	}{
		{"EXAMPLE-NET", 12, "AS64496 (EXA"},
		{"Zürich", 11, "AS64496 (Zü"},
		{"Zürich", 10, "AS64496 (Z"},
		{"Zürich Transit", 30, "AS64496 (Zürich Transit)"},
		{"日本ネット", 12, "AS64496 (日"},
		{"日本ネット", 13, "AS64496 (日本"},
	}
	for _, c := range cases {
		m := newTestModel()
		m.enricher = asn.New(0)
		m.asnLookup = func(net.IP) (asn.Info, bool) { return asn.Info{Number: 64496, Org: c.org}, true }
		cell := stripANSI(m.renderASNCell(ip, 0, c.width, true))
		if w := lipgloss.Width(cell); w != c.width {
			t.Errorf("org %q, width %d: cell %q is %d cells wide", c.org, c.width, cell, w)
		}
		if got := strings.TrimRight(cell, " "); got != c.want {
			t.Errorf("org %q, width %d: cell holds %q, want %q", c.org, c.width, got, c.want)
		}
	}
}

// TestHopTable_WideCharacterNames: a hostname and an ASN label made of wide
// characters are cut and padded in cells, so the row is the terminal's width
// and the Loss% column starts under its header on every row.
func TestHopTable_WideCharacterNames(t *testing.T) {
	setTestTheme(t)
	forceColor(t)

	const wideHost = "東京コア01.例え.ネットワーク.example.jp"
	for _, view := range alignViews {
		if view == ViewProbeDetail {
			continue // no hostname or ASN column
		}
		for _, w := range []int{100, 160, 200} {
			t.Run(fmt.Sprintf("%s/%d", view, w), func(t *testing.T) {
				m := newTestModel(withSize(w, 30), withView(view), withAlignHops(4, false, 1))
				m.enricher = asn.New(0)
				m.asnLookup = func(net.IP) (asn.Info, bool) {
					return asn.Info{Number: 64496, Org: "日本ネットワーク株式会社"}, true
				}
				for _, ttl := range []int{1, 2} {
					for _, n := range m.sessions[0].table.GetOrCreate(ttl).GetNodes() {
						n.SetHostname(wideHost)
					}
				}

				out := m.View()
				var header string
				var rows []string
				for _, raw := range strings.Split(out, "\n") {
					if got := lipgloss.Width(raw); got != w {
						t.Errorf("line is %d cells wide, terminal is %d: %q", got, w, stripANSI(raw))
					}
					line := stripANSI(raw)
					if strings.HasPrefix(line, "#") && strings.Contains(line, " IP ") {
						header = line
						continue
					}
					if header != "" && strings.Contains(line, "東京") {
						rows = append(rows, line)
					}
				}
				if header == "" || len(rows) != 2 {
					t.Fatalf("want a header and 2 rows with the wide hostname, got %q and %d rows:\n%s", header, len(rows), stripANSI(out))
				}
				if !strings.Contains(header, " ASN ") {
					t.Fatalf("no ASN column at width %d: %q", w, header)
				}
				lossAt := lipgloss.Width(header[:strings.Index(header, "Loss%")])
				for _, row := range rows {
					if !strings.Contains(row, "AS64496") {
						t.Errorf("row has no ASN label: %q", row)
					}
					i := strings.Index(row, "0.0%")
					if i < 0 {
						t.Fatalf("no loss figure in row %q", row)
					}
					if at := lipgloss.Width(row[:i]); at != lossAt {
						t.Errorf("loss figure starts at cell %d, Loss%% column at cell %d\n  header: %q\n  row:    %q", at, lossAt, header, row)
					}
				}
			})
		}
	}
}

// TestRowAlignment_WideRoundCount: a round count with more digits than the
// Rnd column is shown in a short form and moves no column.
func TestRowAlignment_WideRoundCount(t *testing.T) {
	setTestTheme(t)
	forceColor(t)

	const rounds = 123456
	m := newTestModel(withAlignHops(4, true, 1))
	for _, h := range m.sessions[0].table.Snapshot() {
		for r := h.Rounds(); r < rounds; r++ {
			h.MarkRoundSent()
			h.MarkRoundEnd()
		}
	}
	if got := m.sessions[0].table.GetOrCreate(1).Rounds(); got != rounds {
		t.Fatalf("hop 1 has %d rounds, want %d", got, rounds)
	}

	for _, view := range alignViews {
		for _, w := range alignWidths {
			t.Run(fmt.Sprintf("%s/%d", view, w), func(t *testing.T) {
				mm := m
				withSize(w, alignTall)(&mm)
				withView(view)(&mm)
				out := mm.View()
				tbl := alignTableOf(t, out, w)
				assertTableAligned(t, tbl)
				plain := stripANSI(out)
				if strings.Contains(string(tbl.header), " Rnd") {
					if !strings.Contains(plain, " 123k ") {
						t.Errorf("Rnd column shown but no row has the short form 123k")
					}
				}
				// Probe Detail's count columns ("0/123456") carry the
				// full figure by design; elsewhere it would be the Rnd cell.
				if view != ViewProbeDetail && strings.Contains(plain, "123456") {
					t.Error("full round count 123456 is in the table")
				}
				if t.Failed() {
					t.Logf("rendered view:\n%s", plain)
				}
			})
		}
	}
}
