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
	// Same widths as the row format in renderSummaryRow: the AVG value is
	// "%8.1fms", ten cells.
	line := fmt.Sprintf("  %-24s %5s %7s %10s  %-16s %s",
		"TARGET", "HOPS", "LOSS", "AVG", "WRST-HOP", "ALERT")
	return barBg(dimStyle().Render(m.fitLine(line)), m.width) + "\n"
}

// fitLine cuts a plain line to the terminal's width. Before the size is known
// the line is left as it is.
func (m Model) fitLine(line string) string {
	if m.width <= 0 {
		return line
	}
	return cutToWidth(line, m.width)
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
		return m.styleSummaryRow(i, m.fitLine(row)) + "\n"
	}

	hops := summaryHops(s)
	loss, lossKind, avg := destStats(s)
	alertMsg := summaryAlert(s)
	// Before any hop has answered there is nothing to report: dashes, not
	// zero figures that read as a measurement.
	lossCol, avgCol := "-", "-"
	if hops > 0 {
		lossCol = hop.FormatLoss(loss, lossKind)
		avgCol = fmt.Sprintf("%.1fms", float64(avg)/float64(time.Millisecond))
	}
	row := fmt.Sprintf("%s%-24s %5d %7s %10s  %-16s %s",
		prefix, truncateSummary(name, 24), hops, lossCol, avgCol,
		truncateSummary(worstHop(s), 16),
		alertMsg)
	return m.styleSummaryRow(i, m.fitLine(row)) + "\n"
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
	ttl := worstHopTTL(s)
	if ttl == 0 {
		return "—"
	}
	if h := s.table.Get(ttl); h != nil {
		if ip := h.GetIP(); ip != nil {
			return ip.String()
		}
	}
	return "—"
}

// worstHopTTL returns the TTL worstHop reports, or 0 before any data.
func worstHopTTL(s *TargetSession) int {
	snap := pathHops(s)
	if len(snap) == 0 {
		return 0
	}
	hopMap := make(map[int]*hop.Hop, len(snap))
	maxTTL := 0
	for _, h := range snap {
		hopMap[h.TTL] = h
		if h.TTL > maxTTL {
			maxTTL = h.TTL
		}
	}
	return largestDeltaTTL(computeDeltas(hopMap, maxTTL))
}

// pathHops returns the session's hops up to and including the destination's.
// The table also holds entries beyond it: round one probes every TTL before
// the destination's distance is known, the destination answers each probe
// that reaches it, and those entries never update again. Nothing shown for
// the path may be read from them.
func pathHops(s *TargetSession) []*hop.Hop {
	snap := s.table.Snapshot()
	limit := s.maxTTLHit
	if limit == 0 {
		limit = s.table.TargetTTL()
	}
	if limit == 0 {
		return snap
	}
	out := snap[:0:0]
	for _, h := range snap {
		if h.TTL <= limit {
			out = append(out, h)
		}
	}
	return out
}

// summaryHops is the HOPS figure for a session: the destination's distance
// once it has answered, the highest TTL seen until then.
func summaryHops(s *TargetSession) int {
	n := 0
	for _, h := range pathHops(s) {
		if h.TTL > n {
			n = h.TTL
		}
	}
	return n
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
	snap := pathHops(s)
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
	msg := lossStyle().Render(m.fitLine(s.target + ": " + s.startErr.Error() + " — target is not being traced"))
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

