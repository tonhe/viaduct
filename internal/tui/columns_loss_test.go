package tui

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/tonhe/viaduct/internal/hop"
)

// paneLossHop builds a one-node hop from closed rounds of 10 probes each.
// lost of the probes in all go unanswered. echoSent echoes are sent to the
// node and echoAnswered of them answered.
func paneLossHop(t *testing.T, rounds, lost, echoSent, echoAnswered int) *hop.Hop {
	t.Helper()
	ip := net.ParseIP("192.0.2.7")
	h := hop.NewHop(3)
	for r := 0; r < rounds; r++ {
		h.MarkRoundSentProbes(10)
		for i := 0; i < 10; i++ {
			if r*10+i < lost {
				continue
			}
			h.AddSample(ip, time.Millisecond, i, hop.KindPrimary)
		}
		h.MarkRoundEnd()
	}
	for i := 0; i < echoSent; i++ {
		h.GetNodes()[0].MarkEchoSent()
		if i < echoAnswered {
			h.AddSample(ip, time.Millisecond, 0, hop.KindEcho)
		}
	}
	return h
}

// A pane writes a hop's loss as the table does, through hop.FormatLoss
// (viaduct#268, #111 item 5): the same figure, the dagger and the question
// mark included.
func TestColumnView_LossCellIsFormatLoss(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    *hop.Hop
		kind hop.LossKind
		want string
	}{
		{"0.4% measured", paneLossHop(t, 25, 1, 20, 0), hop.LossMeasured, "0.4%"},
		{"rate-limited", paneLossHop(t, 10, 60, 40, 40), hop.LossRateLimited, "~0%†"},
		{"provisional", paneLossHop(t, 10, 60, 5, 5), hop.LossProvisional, "60.0%?"},
		{"99% measured", paneLossHop(t, 10, 99, 20, 0), hop.LossMeasured, "99.0%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loss, kind := tc.h.Loss()
			if kind != tc.kind {
				t.Fatalf("fixture: loss kind = %v, want %v", kind, tc.kind)
			}
			want := hop.FormatLoss(loss, kind)
			if tc.want != "" && want != tc.want {
				t.Fatalf("fixture: FormatLoss = %q, want %q", want, tc.want)
			}
			if table := strings.TrimSpace(stripANSI(lossCell(tc.h))); table != want {
				t.Fatalf("fixture: the table's loss cell = %q, want %q", table, want)
			}

			// At the minimum pane width and at a wide one.
			for _, paneWidth := range []int{minPaneWidth, 80} {
				inner := paneWidth - 2
				lay := computePaneLayout(inner, 0)
				if !lay.showLoss {
					t.Fatalf("pane width %d: the loss column is not shown: %+v", paneWidth, lay)
				}
				m := Model{}
				row := stripANSI(m.paneRowContent(lay, inner, 3, tc.h))
				header := stripANSI(m.paneHeaderContent(lay, inner))
				if got := lipgloss.Width(row); got != inner {
					t.Errorf("pane width %d: row is %d cells, want %d: %q", paneWidth, got, inner, row)
				}
				if got := lipgloss.Width(header); got != inner {
					t.Errorf("pane width %d: header is %d cells, want %d: %q", paneWidth, got, inner, header)
				}
				if !strings.HasSuffix(row, " "+want+" ") {
					t.Errorf("pane width %d: row = %q, want it to end with the table's figure %q", paneWidth, row, want)
				}
				if !strings.HasSuffix(header, "Loss ") {
					t.Errorf("pane width %d: header = %q, want Loss right-aligned over the figures", paneWidth, header)
				}
			}
		})
	}
}

// The pane's # header sits over the hop numbers, which are right-aligned
// (viaduct#268, #111 item 2).
func TestColumnView_NumberHeaderIsRightAligned(t *testing.T) {
	inner := minPaneWidth - 2
	lay := computePaneLayout(inner, 0)
	m := Model{}
	header := stripANSI(m.paneHeaderContent(lay, inner))
	if !strings.HasPrefix(header, "  # IP") {
		t.Errorf("header = %q, want it to start with %q", header, "  # IP")
	}
	for _, ttl := range []int{1, 12} {
		row := stripANSI(m.paneRowContent(lay, inner, ttl, nil))
		// The last digit of the hop number is under the #.
		if strings.Index(header, "#") != paneNumW || row[paneNumW] < '0' || row[paneNumW] > '9' || row[paneNumW+1] != ' ' {
			t.Errorf("hop %d: # is not over the number's last digit:\n%q\n%q", ttl, header, row)
		}
	}
}

// The pane's loss cell holds the longest figure hop.FormatLoss returns.
func TestColumnView_LossCellHoldsTheLongestFigure(t *testing.T) {
	for _, kind := range []hop.LossKind{hop.LossMeasured, hop.LossRateLimited, hop.LossProvisional} {
		if s := hop.FormatLoss(100, kind); lipgloss.Width(s) > paneLossW {
			t.Errorf("%q is wider than the pane's loss cell (%d)", s, paneLossW)
		}
	}
}
