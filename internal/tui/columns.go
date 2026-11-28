package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/tonhe/viaduct/internal/hop"
)

// minPaneWidth is the narrowest a target pane may be before the view stops
// adding more panes. Fit = terminalWidth / minPaneWidth; with fewer targets
// than fit, each pane simply gets wider (terminalWidth / numTargets), which is
// what keeps 2–3 targets from looking smushed. maxPanes caps the count so very
// wide terminals don't shatter into many thin panes.
const (
	minPaneWidth = 34
	maxPanes     = 6
)

// paneBorder is a set of box-drawing glyphs. Only the light set is used: heavy
// box-drawing (┏━┓┃) renders at inconsistent widths in many terminal fonts, and
// a top border is a long run of horizontals, so any per-glyph drift accumulates
// and pushes the next pane's label off its column. Focus is shown by border
// color and a bold label instead of a heavier glyph.
type paneBorder struct {
	tl, tr, bl, br, h, v string
}

func lightPaneBorder() paneBorder {
	return paneBorder{tl: "╭", tr: "╮", bl: "╰", br: "╯", h: "─", v: "│"}
}

// paneBorderStyle returns the border color: an accent for the focused pane, dim
// otherwise.
func paneBorderStyle(focused bool) lipgloss.Style {
	if focused {
		return accentStyle()
	}
	return dimStyle()
}

// columnTargets picks which session indices the column view shows, in order.
// If any session is marked (and not errored), only marked sessions are shown.
// Otherwise as many non-errored sessions as fit the terminal width (up to
// maxPanes) are shown. The focused session is always included so its
// accent-colored border is visible. Errored sessions are always excluded.
func (m Model) columnTargets() []int {
	// n panes need n*minPaneWidth + (n-1) gap columns, so fit = (width+1)/(minPaneWidth+1).
	fit := (m.width + 1) / (minPaneWidth + 1)
	if fit < 1 {
		fit = 1
	}
	if fit > maxPanes {
		fit = maxPanes
	}

	var marked, healthy []int
	for i, s := range m.sessions {
		if s.startErr != nil {
			continue
		}
		healthy = append(healthy, i)
		if s.marked {
			marked = append(marked, i)
		}
	}

	pool := marked
	if len(pool) == 0 {
		pool = healthy
	}
	if len(pool) <= fit {
		return pool
	}

	// More targets than fit: keep the first `fit`, but guarantee the focused
	// session is among them (swap it into the last slot if it was cut).
	shown := append([]int(nil), pool[:fit]...)
	focusVisible := false
	for _, i := range shown {
		if i == m.focusedIdx {
			focusVisible = true
			break
		}
	}
	if !focusVisible {
		for _, i := range pool {
			if i == m.focusedIdx {
				shown[len(shown)-1] = i
				break
			}
		}
	}
	return shown
}

// visibleCount returns the number of non-errored sessions.
func (m Model) visibleCount() int {
	n := 0
	for _, s := range m.sessions {
		if s.startErr == nil {
			n++
		}
	}
	return n
}

// paneLayout holds the inner column widths for one pane, chosen to fill the
// available inner width. Columns are added by priority: Avg, Loss, a wider IP,
// then Hostname.
type paneLayout struct {
	ipw      int
	hostw    int // 0 = hidden
	showAvg  bool
	showLoss bool
}

const (
	paneNumW  = 2
	paneAvgW  = 6
	paneLossW = 7 // the longest figure hop.FormatLoss returns ("100.0%?")
	paneIPMin = 9
	paneIPMax = 15
)

// computePaneLayout distributes `inner` (the width between the border glyphs)
// across the pane's columns. Frame overhead: a leading and trailing space plus
// the num column and the separator before IP.
//
// needIP is the length of the longest address the pane has to show. The IP
// column grows to paneIPMax, enough for any IPv4 address, or to needIP when an
// address is longer (IPv6 runs to 39 characters), before the hostname column
// gets any room: a hostname next to an address cut to a prefix is no help.
func computePaneLayout(inner, needIP int) paneLayout {
	ipMax := maxInt(paneIPMax, needIP)
	avail := inner - (1 + paneNumW + 1 + 1) // lead + num + sep + trailing
	lay := paneLayout{ipw: paneIPMin}
	if avail <= paneIPMin {
		lay.ipw = maxInt(3, avail)
		return lay
	}
	remaining := avail - paneIPMin
	if remaining >= 1+paneAvgW {
		lay.showAvg = true
		remaining -= 1 + paneAvgW
	}
	if remaining >= 1+paneLossW {
		lay.showLoss = true
		remaining -= 1 + paneLossW
	}
	if grow := minInt(remaining, ipMax-paneIPMin); grow > 0 {
		lay.ipw += grow
		remaining -= grow
	}
	if remaining >= 1+8 { // separator + a usable hostname
		lay.hostw = remaining - 1
	}
	return lay
}

// renderColumns renders the side-by-side comparison view: one bordered pane per
// selected target, panes dividing the terminal width equally. The focused pane
// is shown by an accent-colored border and a bold label; every pane uses the
// light border. A "+N more" line appears when targets are cut off.
func (m Model) renderColumns() string {
	idxs := m.columnTargets()
	if len(idxs) == 0 {
		return ""
	}
	hidden := m.visibleCount() - len(idxs)

	// A one-column gap separates adjacent panes so their borders never abut
	// (touching borders render as a doubled/ambiguous seam that misaligns the
	// following pane in some terminals). Total gap width is n-1.
	n := len(idxs)
	paneWidth := (m.width - (n - 1)) / n
	if paneWidth < minPaneWidth {
		// Terminal too narrow for even one min-width pane at this count: show
		// just the focused pane full width.
		paneWidth = m.width
		n = 1
		idxs = []int{m.focusedIdx}
	}

	// Vertical budget: total height minus the page header (2 lines: title +
	// separator), the status bar (1), the pane's own top border + column header
	// + bottom border (3), and the "+N more" line when present.
	overhead := 2 + 1 + 3
	if hidden > 0 {
		overhead++
	}
	availRows := 0
	if m.height > overhead {
		availRows = m.height - overhead
	}

	maxTTL := 0
	for _, i := range idxs {
		s := m.sessions[i]
		d := s.table.MaxTTLSeen()
		if s.maxTTLHit > 0 && s.maxTTLHit < d {
			d = s.maxTTLHit
		}
		if d > maxTTL {
			maxTTL = d
		}
	}
	// Column view does not scroll. When the deepest path does not fit, one
	// row is given to a note saying how much is shown, so a cut path is not
	// mistaken for a short one.
	cutNote := ""
	if deepest := maxTTL; availRows > 0 && deepest > availRows {
		maxTTL = availRows - 1
		if maxTTL < 1 {
			maxTTL = 1
		}
		cutNote = fmt.Sprintf("  hops 1–%d of %d shown — c for the full table", maxTTL, deepest)
	}
	if maxTTL < 1 {
		maxTTL = 1
	}

	// Build each pane as a slice of paneWidth-wide lines.
	panes := make([][]string, n)
	for p, i := range idxs {
		panes[p] = m.renderPane(m.sessions[i], paneWidth, maxTTL, i == m.focusedIdx)
	}

	height := maxTTL + 3 // top border + header + rows + bottom border
	var b strings.Builder
	for line := 0; line < height; line++ {
		var sb strings.Builder
		for p := range panes {
			if p > 0 {
				sb.WriteString(" ") // gap between panes
			}
			sb.WriteString(panes[p][line])
		}
		b.WriteString(lineBg(sb.String(), m.width))
		b.WriteString("\n")
	}
	if cutNote != "" {
		b.WriteString(lineBg(dimStyle().Render(m.fitLine(cutNote)), m.width))
		b.WriteString("\n")
	}
	if hidden > 0 {
		more := dimStyle().Render(m.fitLine(fmt.Sprintf("  +%d more — space to mark which show", hidden)))
		b.WriteString(lineBg(more, m.width))
		b.WriteString("\n")
	}
	return b.String()
}

// renderPane builds one bordered target pane as paneWidth-wide lines:
// top border with label, column header, one row per TTL, bottom border.
func (m Model) renderPane(s *TargetSession, paneWidth, maxTTL int, focused bool) []string {
	bd := lightPaneBorder()
	bstyle := paneBorderStyle(focused)
	v := bstyle.Render(bd.v)
	inner := paneWidth - 2

	hops := make(map[int]*hop.Hop)
	for _, h := range s.table.Snapshot() {
		hops[h.TTL] = h
	}
	// This session only fills rows up to where its own destination first
	// answered. The shared grid may be taller (a deeper target sets maxTTL), but
	// the extra rows stay blank rather than repeating this target's destination,
	// which answers at every TTL past its own.
	sessionMax := s.table.MaxTTLSeen()
	if s.maxTTLHit > 0 && s.maxTTLHit < sessionMax {
		sessionMax = s.maxTTLHit
	}

	// The longest address this pane shows decides how wide the IP column may grow.
	needIP := 0
	for ttl, h := range hops {
		if ttl > sessionMax {
			continue
		}
		if ip := h.GetIP(); ip != nil {
			needIP = maxInt(needIP, len(ip.String()))
		}
	}
	lay := computePaneLayout(inner, needIP)

	lines := make([]string, 0, maxTTL+3)
	lines = append(lines, m.paneTopBorder(s, bd, bstyle, paneWidth, focused))
	lines = append(lines, v+m.paneHeaderContent(lay, inner)+v)
	for ttl := 1; ttl <= maxTTL; ttl++ {
		var content string
		if ttl <= sessionMax {
			content = m.paneRowContent(lay, inner, ttl, hops[ttl])
		} else {
			content = padCell("", inner) // past this target's depth: blank row
		}
		lines = append(lines, v+content+v)
	}

	bottom := bstyle.Render(bd.bl + strings.Repeat(bd.h, inner) + bd.br)
	lines = append(lines, bottom)
	return lines
}

// paneTopBorder builds "╭ label ─────╮" at exactly paneWidth wide. The label
// sits immediately after the corner and one space, so it lines up with the
// content column below (which also begins one space in past the border), and the
// dash run resumes after it: corner + " " + label + " " + h*fill + corner.
func (m Model) paneTopBorder(s *TargetSession, bd paneBorder, bstyle lipgloss.Style, paneWidth int, focused bool) string {
	label := s.target
	if s.marked {
		label = "*" + label
	}
	maxLabel := paneWidth - 5 // corner + 2 spaces + >=1 dash + corner
	if maxLabel < 1 {
		maxLabel = 1
	}
	label = truncateSummary(label, maxLabel)
	fill := paneWidth - 4 - lipgloss.Width(label)
	if fill < 1 {
		fill = 1
	}
	labelStyle := headerStyle()
	if focused {
		labelStyle = accentStyle().Bold(true)
	}
	return bstyle.Render(bd.tl+" ") + labelStyle.Render(label) + bstyle.Render(" "+strings.Repeat(bd.h, fill)+bd.tr)
}

// paneHeaderContent builds the column-header row content (between borders).
func (m Model) paneHeaderContent(lay paneLayout, inner int) string {
	fields := []string{dimStyle().Render(fmt.Sprintf("%*s", paneNumW, "#"))}
	fields = append(fields, dimStyle().Render(padCell("IP", lay.ipw)))
	if lay.hostw > 0 {
		fields = append(fields, dimStyle().Render(padCell("Host", lay.hostw)))
	}
	if lay.showAvg {
		fields = append(fields, dimStyle().Render(fmt.Sprintf("%*s", paneAvgW, "Avg")))
	}
	if lay.showLoss {
		fields = append(fields, dimStyle().Render(fmt.Sprintf("%*s", paneLossW, "Loss")))
	}
	return padCell(" "+strings.Join(fields, " "), inner)
}

// paneRowContent builds one TTL row's content (between borders). A missing hop
// renders a star in the IP column; all cells are padded before styling.
func (m Model) paneRowContent(lay paneLayout, inner, ttl int, h *hop.Hop) string {
	num := dimStyle().Render(fmt.Sprintf("%*d", paneNumW, ttl))

	var ipCell, hostCell, avgCell, lossCell string
	if h == nil || h.GetIP() == nil {
		ipCell = starStyle().Render(padCell("*", lay.ipw))
		if lay.hostw > 0 {
			hostCell = padCell("", lay.hostw)
		}
		if lay.showAvg {
			avgCell = fmt.Sprintf("%*s", paneAvgW, "")
		}
		if lay.showLoss {
			lossCell = fmt.Sprintf("%*s", paneLossW, "")
		}
	} else {
		ipCell = ipStyle().Render(padCell(truncateAddr(h.GetIP().String(), lay.ipw), lay.ipw))
		if lay.hostw > 0 {
			name := h.GetHostname()
			hostCell = hostStyle().Render(padCell(truncateSummary(name, lay.hostw), lay.hostw))
		}
		if lay.showAvg {
			avgCell = fmt.Sprintf("%*s", paneAvgW, formatDuration(h.AvgRTT()))
		}
		if lay.showLoss {
			// The hop's figure, written as the detail table writes it
			// (hop.FormatLoss), right-aligned in the pane's cell.
			loss, kind := h.Loss()
			style := lossStyleFor(loss)
			switch kind {
			case hop.LossRateLimited:
				style = rateLimitStyle()
			case hop.LossProvisional:
				style = dimStyle()
			}
			lossStr := hop.FormatLoss(loss, kind)
			lossCell = style.Render(strings.Repeat(" ", maxInt(0, paneLossW-lipgloss.Width(lossStr))) + lossStr)
		}
	}

	fields := []string{num, ipCell}
	if lay.hostw > 0 {
		fields = append(fields, hostCell)
	}
	if lay.showAvg {
		fields = append(fields, avgCell)
	}
	if lay.showLoss {
		fields = append(fields, lossCell)
	}
	return padCell(" "+strings.Join(fields, " "), inner)
}

// truncateCells cuts s to at most n display cells. It never cuts inside a
// character, so a wide character that would straddle the limit is dropped and
// the result can be a cell short of n; pad with padCell, which counts cells.
func truncateCells(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	w := 0
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > n {
			return s[:i]
		}
		w += rw
	}
	return s
}

// formatRounds renders a round count in at most width cells. A count with
// more digits than that is shown in a short form, rounded down: 123456 in 5
// cells is "123k", 12345678 is "12M".
func formatRounds(n, width int) string {
	s := strconv.Itoa(n)
	if len(s) <= width {
		return s
	}
	div := 1
	for _, suffix := range []string{"k", "M", "G", "T", "P", "E"} {
		div *= 1000
		if short := strconv.Itoa(n/div) + suffix; len(short) <= width {
			return short
		}
	}
	if width < 0 {
		width = 0
	}
	return s[:width]
}

// truncateAddr shortens an address to n columns by cutting out its middle:
// head + "…" + tail. Addresses that share a prefix (hops in one network, or
// any two IPv6 addresses in the same /64) differ at the end, so a prefix-only
// cut would make them look identical.
func truncateAddr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "…"
	}
	head := n / 2
	tail := n - 1 - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
