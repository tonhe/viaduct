package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// Probe Detail shows, for each hop, what each kind of probe got back.
//
// Loss belongs to the hop, not to each node: an unanswered probe does not say
// which node it went to, so a per-node "loss" would mostly measure how
// traffic is split. So:
//
//   - the hop's first line carries the hop's loss, the same figure as the
//     main table (including ~N%† and N%?)
//   - every node line carries its share of the hop's replies, labelled
//     "% of replies"
//   - the per-type cells are counts, received/sent, as in the JSON
//
// Nothing labelled "Loss" shows a per-node figure.

// Column widths for the probe detail view.
const (
	pdWidthTTL   = 3
	pdWidthIP    = 17            // v4 default; widens for v6 via computeIPColWidth
	pdWidthLoss  = widthLossCell // same cell as the main table's Loss% column
	pdWidthShare = 12            // "% of replies"
	pdWidthCount = 9             // "received/sent" for one probe type
	pdWidthRnd   = 5

	pdShareHeader = "% of replies"
)

// pdLayout says which Probe Detail columns are shown and how wide the address
// column is. The hop number, address, loss and share are always shown; the
// count columns and Rnd are dropped, least important first, on a terminal too
// narrow for them, and then the address is shortened. Last, the loss column
// gives up its spare cell: that is what fits the four columns into the
// 40-column minimum layout.
type pdLayout struct {
	ipw         int
	lossw       int
	showICMP    bool
	showPrimary bool
	showEcho    bool
	showRnd     bool
}

func (l pdLayout) width() int {
	w := pdWidthTTL + 1 + l.ipw + 1 + l.lossw + 1 + pdWidthShare
	for _, c := range []struct {
		show bool
		w    int
	}{{l.showICMP, pdWidthCount}, {l.showPrimary, pdWidthCount}, {l.showEcho, pdWidthCount}, {l.showRnd, pdWidthRnd}} {
		if c.show {
			w += 1 + c.w
		}
	}
	return w
}

// computePDLayout fits the Probe Detail columns into width. ipNeeded is the
// widest address to show. hasPrimary is false when the session sends no
// primary-protocol probes (ICMP mode): the primary column is then left out.
// A width of zero or less means the terminal size is not known yet:
// everything is shown.
func computePDLayout(width, ipNeeded int, hasPrimary bool) pdLayout {
	if ipNeeded < pdWidthIP {
		ipNeeded = pdWidthIP
	}
	l := pdLayout{ipw: ipNeeded, lossw: pdWidthLoss, showICMP: true, showPrimary: hasPrimary, showEcho: true, showRnd: true}
	if width <= 0 {
		return l
	}
	for _, drop := range []*bool{&l.showRnd, &l.showEcho, &l.showPrimary, &l.showICMP} {
		if l.width() <= width {
			return l
		}
		*drop = false
	}
	if over := l.width() - width; over > 0 {
		l.ipw -= over
		if l.ipw < minIPColWidth {
			l.ipw = minIPColWidth
		}
	}
	if l.width() > width {
		l.lossw = minWidthLossCell
	}
	return l
}

// pdWidthCountNum is the room for one number of a count cell that does not
// fit in full: two of them and the slash fill the column.
const pdWidthCountNum = (pdWidthCount - 1) / 2

// countCell renders "received/sent" for one probe type, or a dash when none
// of that type were sent. dim marks a figure that is still accumulating.
//
// The cell is never wider than its column. A figure too long to show in full
// has each number shortened to thousands or millions, rounded down, as the
// Rnd column is: 12345/123456 is shown as 12k/123k.
func countCell(received, sent int, dim bool) string {
	if sent == 0 {
		return dimStyle().Render(padCell("—", pdWidthCount))
	}
	text := fmt.Sprintf("%d/%d", received, sent)
	if len(text) > pdWidthCount {
		text = formatRounds(received, pdWidthCountNum) + "/" + formatRounds(sent, pdWidthCountNum)
	}
	plain := padCell(text, pdWidthCount)
	if dim {
		return dimStyle().Render(plain)
	}
	return plain
}

// shareCell renders a node's share of its hop's replies to TTL-limited
// probes. It is a share, not a loss figure: at an ECMP hop it says how the
// replies are split between the nodes.
func shareCell(nodeReplies, hopReplies int) string {
	if hopReplies == 0 {
		return dimStyle().Render(padCell("-", pdWidthShare))
	}
	return padCell(fmt.Sprintf("%.0f%%", float64(nodeReplies)/float64(hopReplies)*100), pdWidthShare)
}

// renderProbeDetailHeader returns the column-header line for the probe detail view.
func (m Model) renderProbeDetailHeader(l pdLayout, primaryColName string) string {
	parts := []string{
		fmt.Sprintf("%-*s", pdWidthTTL, "#"),
		fmt.Sprintf("%-*s", l.ipw, "IP"),
		fmt.Sprintf("%-*s", l.lossw, "Loss%"),
		fmt.Sprintf("%-*s", pdWidthShare, pdShareHeader),
	}
	if l.showICMP {
		parts = append(parts, fmt.Sprintf("%-*s", pdWidthCount, "ICMP-TTL"))
	}
	if l.showPrimary {
		parts = append(parts, fmt.Sprintf("%-*s", pdWidthCount, primaryColName))
	}
	if l.showEcho {
		parts = append(parts, fmt.Sprintf("%-*s", pdWidthCount, "Echo"))
	}
	if l.showRnd {
		parts = append(parts, fmt.Sprintf("%-*s", pdWidthRnd, "Rnd"))
	}
	return strings.Join(parts, " ")
}

// primaryColHeader returns the protocol-appropriate column name for the primary
// (UDP/TCP TTL) probe column.
func primaryColHeader(protocolName string) string {
	switch protocolName {
	case "tcp":
		return "TCP-TTL"
	default:
		return "UDP-TTL"
	}
}

// renderProbeDetailHopRow renders one node's row. first marks the hop's first
// line, which carries the hop's loss; the other node lines of an ECMP hop
// show a dash there. hopReplies is the hop's total of TTL-probe replies, for
// the share.
func (m Model) renderProbeDetailHopRow(h *hop.Hop, pn *hop.PathNode, l pdLayout, first bool, hopReplies int) string {
	ipStr := ""
	if ip := pn.GetIP(); ip != nil {
		ipStr = truncateAddr(ip.String(), l.ipw)
	}
	icmpStats, primaryStats, echoStats := pn.ICMPTTLStats(), pn.PrimaryStats(), pn.EchoStats()

	lossC := dimStyle().Render(padCell("-", l.lossw))
	if first {
		lossC = lossCellWidth(h, l.lossw)
	}
	parts := []string{
		dimStyle().Render(fmt.Sprintf("%-*d", pdWidthTTL, h.TTL)),
		ipStyle().Render(padCell(ipStr, l.ipw)),
		lossC,
		shareCell(icmpStats.Received+primaryStats.Received, hopReplies),
	}
	if l.showICMP {
		parts = append(parts, countCell(icmpStats.Received, icmpStats.ProbesSent, false))
	}
	if l.showPrimary {
		parts = append(parts, countCell(primaryStats.Received, primaryStats.ProbesSent, false))
	}
	if l.showEcho {
		// Dimmed until the hop has enough echo samples for a verdict.
		parts = append(parts, countCell(echoStats.Received, echoStats.Sent, h.RateLimitState() == hop.RateLimitUndetermined))
	}
	if l.showRnd {
		parts = append(parts, fmt.Sprintf("%-*s", pdWidthRnd, formatRounds(h.Rounds(), pdWidthRnd)))
	}
	return strings.Join(parts, " ")
}

// renderProbeDetailView returns all lines for the probe detail body (col-header
// at index 0, then one line per node). Lines are unstyled-background; the caller
// wraps them with lineBg.
func (m Model) renderProbeDetailView() []string {
	s := m.focused()
	// In ICMP mode no primary-protocol probes are sent, so there is no
	// primary column: the ICMP-TTL column covers the probes sent.
	hasPrimary := s.protocolName != "icmp"

	hops := pathHops(s)
	l := computePDLayout(m.width, computeIPColWidth(hops), hasPrimary)

	lines := []string{dimStyle().Render(m.renderProbeDetailHeader(l, primaryColHeader(s.protocolName)))}

	hopMap := make(map[int]*hop.Hop, len(hops))
	hopMaxTTL := 0
	for _, h := range hops {
		hopMap[h.TTL] = h
		if h.TTL > hopMaxTTL {
			hopMaxTTL = h.TTL
		}
	}

	for ttl := 1; ttl <= hopMaxTTL; ttl++ {
		h, ok := hopMap[ttl]
		if !ok || h.GetIP() == nil {
			// Non-responding hop.
			star := starStyle().Render("*")
			ttlStr := dimStyle().Render(fmt.Sprintf("%-*d", pdWidthTTL, ttl))
			lines = append(lines, ttlStr+" "+star)
			continue
		}

		nodes := []*hop.PathNode{h.PrimaryNode()}
		if h.IsDivergent() {
			nodes = h.GetNodes()
		}
		hopReplies := 0
		for _, pn := range h.GetNodes() {
			hopReplies += pn.ICMPTTLStats().Received + pn.PrimaryStats().Received
		}
		for i, pn := range nodes {
			if pn == nil {
				continue
			}
			lines = append(lines, m.renderProbeDetailHopRow(h, pn, l, i == 0, hopReplies))
		}
	}

	return lines
}

// viewProbeDetail is the top-level entry point called from View() when
// m.viewMode == ViewProbeDetail. It produces the complete TUI string.
func (m Model) viewProbeDetail() string {
	s := m.focused()
	var b strings.Builder

	// Header bar.
	b.WriteString(barBg(headerStyle().Render(m.headerLine(s, ViewProbeDetail.String())), m.width))
	b.WriteString("\n")

	// Separator.
	b.WriteString(barBg(dimStyle().Render(separatorLine(m.width)), m.width))
	b.WriteString("\n")

	// Multi-target summary panel.
	multiTarget := len(m.sessions) > 1 && !m.columnView
	if multiTarget {
		b.WriteString(m.renderSummary())
	}

	fixedLines := m.fixedLineCount()
	availableRows := m.hopRowsAvailable()

	// Render probe detail rows; [0] is the col-header.
	allLines := m.renderProbeDetailView()

	// Write col-header (always present at index 0).
	b.WriteString(lineBg(allLines[0], m.width))
	b.WriteString("\n")

	hopLines := allLines[1:]
	var displayLines []string
	displayCount := 0
	if s.startErr != nil {
		// A target that failed to start shows its error, as in the default view.
		b.WriteString(m.renderFocusedError(s, availableRows))
		displayCount = availableRows
		hopLines = nil
	} else {
		// Hop content rows with scroll handling.
		displayLines = hopLines
		if availableRows > 0 && len(hopLines) > availableRows {
			if m.autoScroll {
				displayLines = hopLines[len(hopLines)-availableRows:]
			} else {
				offset := m.scrollOffset
				maxOffset := len(hopLines) - availableRows
				if maxOffset < 0 {
					maxOffset = 0
				}
				if offset > maxOffset {
					offset = maxOffset
				}
				end := len(hopLines) - offset
				start := end - availableRows
				if start < 0 {
					start = 0
				}
				displayLines = hopLines[start:end]
			}
		}
		for _, line := range displayLines {
			b.WriteString(lineBg(line, m.width))
			b.WriteString("\n")
		}
		displayCount = len(displayLines)
	}

	// Alert line, as in the default view.
	alertLine := ""
	if s.alertEngine != nil {
		alertLine = s.alertEngine.Message()
		if alertLine != "" && multiTarget {
			alertLine = s.target + ": " + alertLine
		}
	}

	// Fill remaining vertical space with themed background.
	emptyLine := lineBg("", m.width)
	usedLines := fixedLines + displayCount
	if alertLine != "" {
		usedLines++
	}
	if m.height > 0 && usedLines < m.height {
		for i := 0; i < m.height-usedLines; i++ {
			b.WriteString(emptyLine)
			b.WriteString("\n")
		}
	}
	if alertLine != "" {
		b.WriteString(lineBg(alertStyle().Render(m.fitLine(alertLine)), m.width))
		b.WriteString("\n")
	}

	// Status bar.
	elapsed := m.now().Sub(m.startTime).Truncate(time.Second)
	dnsLabel := "on"
	if !m.dnsEnabled {
		dnsLabel = "off"
	}
	// A setup notice, a protocol switch or an export result, as in the
	// default view.
	notice := s.notice
	if s.switchStatusMsg != "" && m.now().Sub(s.switchStatusTime) < 5*time.Second {
		notice = s.switchStatusMsg
	}
	if m.exportStatusMsg != "" && m.now().Sub(m.exportStatusTime) < 5*time.Second {
		notice = m.exportStatusMsg
	}
	scrollHint := ""
	if !m.autoScroll && len(hopLines) > availableRows {
		scrollHint = "[scrolled] G:bottom g:top"
	}
	status := m.statusLine(statusParts{
		trace:   m.traceStatus(s),
		elapsed: fmt.Sprintf("Elapsed: %s", elapsed),
		dns:     "DNS: " + dnsLabel,
		notice:  notice,
		keys:    m.viewKeyHints(),
		scroll:  scrollHint,
		footer:  "echo cadence: ceil(pairs/budget) rounds",
		targets: m.targetKeyHints(),
	})
	b.WriteString(barBg(statusStyle().Render(status), m.width))

	// Settings overlay — rendered on top of everything else.
	if m.showSettings && m.settings != nil {
		return m.settings.View()
	}

	return b.String()
}
