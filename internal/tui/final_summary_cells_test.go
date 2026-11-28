package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// The exit summary cuts a hostname and an ASN label by display cells and
// keeps the round count inside its column (viaduct#248). It used to cut both
// names by bytes, which can split a character, and print the count unbounded.
func TestFinalSummary_NonASCIINamesAndWideRoundCount(t *testing.T) {
	const (
		rounds = 123456
		// Two-byte characters placed so that a cut at byte 20 falls inside one.
		host = "cöre01.pöp-éast.trånsit.exämple.net"
	)
	for _, family := range []int{4, 6} {
		for _, numPaths := range []int{1, 6} {
			t.Run(fmt.Sprintf("IPv%d/paths=%d", family, numPaths), func(t *testing.T) {
				m := newTestModel(withIPVersion(family), withAlignHops(family, true, numPaths), withASNLabels(family))
				for _, h := range m.sessions[0].table.Snapshot() {
					for _, pn := range h.GetNodes() {
						pn.SetHostname(host)
					}
					for r := h.Rounds(); r < rounds; r++ {
						h.MarkRoundSent()
						h.MarkRoundEnd()
					}
				}

				out := m.FinalSummary()
				if !utf8.ValidString(out) {
					t.Errorf("the summary is not valid UTF-8:\n%q", out)
				}
				lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
				if len(lines) < 7 {
					t.Fatalf("summary has %d lines:\n%s", len(lines), out)
				}
				header := alignCells(t, lines[1])
				starts := map[int]string{}
				for _, c := range alignHeaderCols(header) {
					starts[c.start] = c.name
				}
				hostCol, asnCol, rndCol := -1, -1, -1
				for start, name := range starts {
					switch name {
					case "Hostname":
						hostCol = start
					case "ASN":
						asnCol = start
					case "Rnd":
						rndCol = start
					}
				}
				if hostCol < 0 || asnCol < 0 || rndCol < 0 {
					t.Fatalf("header has no Hostname, ASN or Rnd column: %q", string(header))
				}

				sawHost, sawOrg, sawShort := false, false, false
				for _, line := range lines[2:] {
					if strings.HasSuffix(strings.TrimSpace(line), "*") {
						continue // a silent hop: number and star only
					}
					cells := alignCells(t, line)
					// The line is as wide as the header at most: no cell ran over.
					if lw, hw := lipgloss.Width(line), lipgloss.Width(lines[1]); lw > hw {
						t.Errorf("row is %d cells wide, header is %d\n  header: %q\n  row:    %q", lw, hw, lines[1], line)
						continue
					}
					// Hostname: the first 20 cells of the name, whole characters only.
					if got := strings.TrimRight(string(cells[hostCol:asnCol]), " "); got != "" {
						sawHost = true
						if want := "cöre01.pöp-éast.trån"; got != want {
							t.Errorf("hostname cell = %q, want %q", got, want)
						}
					}
					// ASN: at most 20 cells, and the separator before Loss% is a space.
					asnEnd := asnCol + 20
					if cells[asnEnd] != ' ' {
						t.Errorf("cell %d after the ASN column holds %q, want a space\n  row: %q", asnEnd, string(cells[asnEnd]), line)
					}
					// The label with the non-ASCII organisation name is cut to
					// exactly 20 cells, whole characters only.
					if label := string(cells[asnCol:asnEnd]); strings.HasPrefix(label, "AS64497") {
						sawOrg = true
						if want := "AS64497 (Rés€au Züri"; label != want {
							t.Errorf("ASN cell = %q, want %q", label, want)
						}
					}
					// Rnd: the short form, inside 5 cells, on a hop's first line.
					rnd := strings.TrimRight(string(cells[rndCol:rndCol+5]), " ")
					if rnd == "123k" {
						sawShort = true
					} else if rnd != "-" {
						t.Errorf("Rnd cell = %q, want 123k or a dash\n  row: %q", rnd, line)
					}
					if cells[rndCol+5] != ' ' {
						t.Errorf("cell after the Rnd column holds %q, want a space\n  row: %q", string(cells[rndCol+5]), line)
					}
				}
				if !sawHost {
					t.Error("no row shows a hostname")
				}
				if !sawOrg {
					t.Error("no row shows the non-ASCII organisation name")
				}
				if !sawShort {
					t.Error("no row shows the short round count 123k")
				}
				if t.Failed() {
					t.Logf("summary:\n%s", out)
				}
			})
		}
	}
}
