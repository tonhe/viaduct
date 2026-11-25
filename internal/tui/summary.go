package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/theme"
)

// summaryLineCount returns the number of lines the summary panel will occupy
// (header + one row per session).
func (m Model) summaryLineCount() int {
	return 1 + len(m.sessions)
}

// renderSummary renders the multi-target summary panel: one header line, then
// one row per session showing target name, hop count, loss%, avg RTT, worst
// hop, and alert state.
func (m Model) renderSummary() string {
	var b strings.Builder
	b.WriteString(m.summaryHeader())
	for i, s := range m.sessions {
		b.WriteString(m.renderSummaryRow(i, s))
	}
	return b.String()
}

// summaryHeader renders the column header line for the summary panel.
func (m Model) summaryHeader() string {
	line := fmt.Sprintf("  %-24s %5s %7s %9s  %-16s %s",
		"TARGET", "HOPS", "LOSS", "AVG", "WRST-HOP", "ALERT")
	return barBg(dimStyle().Render(line), m.width) + "\n"
}

// renderSummaryRow renders one row for session i. Focused row uses the
// selection background (Base02); all others use the normal line background.
func (m Model) renderSummaryRow(i int, s *TargetSession) string {
	prefix := "  "
	if i == m.focusedIdx {
		prefix = "▶ "
	}

	name := s.target
	if s.marked {
		name = "*" + name
	}

	if s.startErr != nil {
		row := fmt.Sprintf("%s%-24s  error: %s", prefix, truncateSummary(name, 24), s.startErr.Error())
		return m.styleSummaryRow(i, row) + "\n"
	}

	hops := s.table.MaxTTLSeen()
	loss, lossKind, avg := destStats(s)
	alertMsg := summaryAlert(s)
	row := fmt.Sprintf("%s%-24s %5d %7s %8.1fms  %-16s %s",
		prefix, truncateSummary(name, 24), hops, hop.FormatLoss(loss, lossKind),
		float64(avg)/float64(time.Millisecond),
		truncateSummary(worstHop(s), 16),
		alertMsg)
	return m.styleSummaryRow(i, row) + "\n"
}

// styleSummaryRow paints row with the selection background when it is the
// focused session, otherwise the standard line background.
func (m Model) styleSummaryRow(i int, row string) string {
	if i == m.focusedIdx {
		return paintLine(row, m.width, theme.Current.Base06, theme.Current.Base02)
	}
	return lineBg(row, m.width)
}

// worstHop returns the IP string of the hop with the largest positive
// hop-to-hop avg-RTT delta — the same "largest contributor" the detail table
// highlights in amber. Returns "—" before any data.
//
// Uses computeDeltas + largestDeltaTTL so that gap handling (carrying prevAvg
// across non-responding hops) matches the detail pane exactly.
func worstHop(s *TargetSession) string {
	snap := s.table.Snapshot()
	if len(snap) == 0 {
		return "—"
	}
	hopMap := make(map[int]*hop.Hop, len(snap))
	maxTTL := 0
	for _, h := range snap {
		hopMap[h.TTL] = h
		if h.TTL > maxTTL {
			maxTTL = h.TTL
		}
	}
	deltas := computeDeltas(hopMap, maxTTL)
	ttl := largestDeltaTTL(deltas)
	if ttl == 0 {
		return "—"
	}
	h, ok := hopMap[ttl]
	if !ok {
		return "—"
	}
	if ip := h.GetIP(); ip != nil {
		return ip.String()
	}
	return "—"
}

// truncateSummary returns s truncated to at most n display cells (one cell per
// rune for the BMP characters used in target names and IPs). When s is longer
// than n cells, it returns the first (n-1) runes followed by '…' so the total
// is n cells. When n <= 1 it returns at most n runes (no ellipsis). The result
// is always valid UTF-8.
func truncateSummary(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		if n <= 0 {
			return ""
		}
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
}

// destStats returns the loss figure (see hop.Hop.Loss) and avg RTT of the last
// responding hop in s's table, or zeros when no hop has received any data.
// Walking in reverse avoids reporting zeros for a trailing non-responding gap
// hop as the destination.
func destStats(s *TargetSession) (float64, hop.LossKind, time.Duration) {
	snap := s.table.Snapshot()
	for i := len(snap) - 1; i >= 0; i-- {
		h := snap[i]
		pn := h.PrimaryNode()
		if pn != nil && pn.GetReceived() > 0 {
			loss, kind := h.Loss()
			return loss, kind, h.AvgRTT()
		}
	}
	return 0, hop.LossMeasured, 0
}

// summaryAlert returns the alert label for a session row: the alert message
// when degraded, "—" otherwise. Nil alert engine is treated as healthy.
func summaryAlert(s *TargetSession) string {
	if s.alertEngine != nil && s.alertEngine.State() == AlertDegraded {
		return s.alertEngine.Message()
	}
	return "—"
}

// renderFocusedError renders the detail pane for a focused session that
// failed to start (startErr != nil). Fills all available rows with the
// error message and empty background lines.
func (m Model) renderFocusedError(s *TargetSession, availableRows int) string {
	msg := lossStyle().Render(s.target + ": " + s.startErr.Error() + " — target is not being traced")
	var b strings.Builder
	b.WriteString(lineBg(msg, m.width))
	b.WriteString("\n")
	emptyLine := lineBg("", m.width)
	for i := 1; i < availableRows; i++ {
		b.WriteString(emptyLine)
		b.WriteString("\n")
	}
	return b.String()
}

