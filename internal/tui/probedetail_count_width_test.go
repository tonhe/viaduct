package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/tonhe/viaduct/internal/hop"
)

// A count cell is never wider than its column. A figure that does not fit is
// shown with each number in thousands or millions, rounded down, as the Rnd
// column does (viaduct#247).
func TestCountCell_NeverWiderThanItsColumn(t *testing.T) {
	setTestTheme(t)
	cases := []struct {
		received, sent int
		want           string
	}{
		{0, 0, "—"},
		{7, 9, "7/9"},
		{9999, 9999, "9999/9999"},     // 9 cells: the widest figure shown in full
		{0, 1234567, "0/1234567"},     // 9 cells, in full
		{9999, 10000, "9999/10k"},     // 10 cells in full: shortened
		{12345, 123456, "12k/123k"},   // the case in the issue
		{123456, 123456, "123k/123k"}, // every round answered
		{999999, 1000000, "999k/1M"},
		{12345678, 12345678, "12M/12M"},
		{0, 12345678, "0/12M"},
	}
	for _, tc := range cases {
		for _, dim := range []bool{false, true} {
			cell := stripANSI(countCell(tc.received, tc.sent, dim))
			if w := lipgloss.Width(cell); w != pdWidthCount {
				t.Errorf("countCell(%d, %d, dim=%v) is %d cells wide, want %d: %q", tc.received, tc.sent, dim, w, pdWidthCount, cell)
			}
			if got := strings.TrimRight(cell, " "); got != tc.want {
				t.Errorf("countCell(%d, %d, dim=%v) = %q, want %q", tc.received, tc.sent, dim, got, tc.want)
			}
		}
	}
}

// Probe Detail stays aligned when a hop has answered more rounds than a count
// cell can show in full: the columns after ICMP-TTL start under their headers.
func TestRowAlignment_ProbeDetailWideCounts(t *testing.T) {
	setTestTheme(t)
	forceColor(t)

	const rounds = 123456
	m := newTestModel(withAlignHops(4, false, 1))
	h := m.sessions[0].table.GetOrCreate(1)
	ip := h.GetIP()
	if ip == nil {
		t.Fatal("fixture: hop 1 has no address")
	}
	for r := h.Rounds(); r < rounds; r++ {
		h.MarkRoundSent()
		h.AddSample(ip, time.Millisecond, 0, hop.KindICMPTTL)
		h.MarkRoundEnd()
	}
	st := h.PrimaryNode().ICMPTTLStats()
	if full := fmt.Sprintf("%d/%d", st.Received, st.ProbesSent); len(full) <= pdWidthCount {
		t.Fatalf("fixture: hop 1's ICMP-TTL count %q fits its column; the test needs one that does not", full)
	}

	for _, w := range alignWidths {
		t.Run(fmt.Sprintf("%d", w), func(t *testing.T) {
			mm := m
			withSize(w, alignTall)(&mm)
			withView(ViewProbeDetail)(&mm)
			out := mm.View()
			assertTableAligned(t, alignTableOf(t, out, w))
			if t.Failed() {
				t.Logf("rendered view:\n%s", stripANSI(out))
			}
		})
	}
}
