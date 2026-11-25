package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// Column widths for the probe detail view.
const (
	pdWidthTTL   = 3
	pdWidthIP    = 17 // v4 default; widens for v6 via computeIPColWidth
	pdWidthProbe = 11 // ICMP-TTL, primary, Echo columns
	pdWidthRnd   = 5
)

// probeCell formats "recvd (loss%)" for one ProbeStats. The plain text is
// width-padded first, then styled by loss severity (reuse ok/loss styles).
// This is the correct pad-then-style order; never fmt %-Ns a styled string.
func probeCell(recvd int, lossPct float64, width int) string {
	plain := fmt.Sprintf("%d (%.0f%%)", recvd, lossPct)
	return lossStyleFor(lossPct).Render(padCell(plain, width))
}

// echoCell renders the Echo probe column. When sent < 5 (below the min-sample
// guard), it shows the running count as "N/5" in dim style to indicate the
// measurement is still accumulating — undetermined != not-rate-limited.
func echoCell(s hop.ProbeStats, width int) string {
	if s.Sent < 5 {
		plain := fmt.Sprintf("%d/5", s.Sent)
		return dimStyle().Render(padCell(plain, width))
	}
	return probeCell(s.Received, s.ProbeLossPercent(), width)
}

// renderProbeDetailHeader returns the column-header line for the probe detail view.
func (m Model) renderProbeDetailHeader(ipWidth int, primaryColName string) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("%-*s", pdWidthTTL, "#"))
	parts = append(parts, fmt.Sprintf("%-*s", ipWidth, "IP"))
	parts = append(parts, fmt.Sprintf("%-*s", pdWidthProbe, "ICMP-TTL"))
	parts = append(parts, fmt.Sprintf("%-*s", pdWidthProbe, primaryColName))
	parts = append(parts, fmt.Sprintf("%-*s", pdWidthProbe, "Echo"))
	parts = append(parts, fmt.Sprintf("%-*s", pdWidthRnd, "Rnd"))
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

// renderProbeDetailHopRow renders a single node row for the probe detail view.
// ttl is the hop TTL, pn is the path node. isICMPProto indicates that the
// session uses ICMP as its primary protocol (so the primary column is N/A).
func (m Model) renderProbeDetailHopRow(ttl int, pn *hop.PathNode, ipWidth int, isICMPProto bool) string {
	ttlStr := dimStyle().Render(fmt.Sprintf("%-*d", pdWidthTTL, ttl))

	ip := pn.GetIP()
	ipStr := ""
	if ip != nil {
		ipStr = ip.String()
	}
	ipC := ipStyle().Render(padCell(ipStr, ipWidth))

	icmpStats := pn.ICMPTTLStats()
	primaryStats := pn.PrimaryStats()
	echoStats := pn.EchoStats()

	icmpC := probeCell(icmpStats.RecvRounds, icmpStats.RoundLossPercent(), pdWidthProbe)

	var primaryC string
	if isICMPProto {
		// When probing via ICMP, the "primary" slot maps to the same ICMP-TTL
		// kind; render a dash to avoid double-counting.
		primaryC = dimStyle().Render(padCell("—", pdWidthProbe))
	} else {
		primaryC = probeCell(primaryStats.RecvRounds, primaryStats.RoundLossPercent(), pdWidthProbe)
	}

	echoC := echoCell(echoStats, pdWidthProbe)

	return strings.Join([]string{ttlStr, ipC, icmpC, primaryC, echoC}, " ")
}

// renderProbeDetailView returns all lines for the probe detail body (col-header
// at index 0, then one line per node). Lines are unstyled-background; the caller
// wraps them with lineBg.
func (m Model) renderProbeDetailView() []string {
	s := m.focused()
	isICMPProto := s.protocolName == "icmp"
	primaryName := primaryColHeader(s.protocolName)

	hops := s.table.Snapshot()
	ipWidth := computeIPColWidth(hops)
	if ipWidth < pdWidthIP {
		ipWidth = pdWidthIP
	}

	var lines []string

	// Col-header line (index 0).
	header := m.renderProbeDetailHeader(ipWidth, primaryName)
	lines = append(lines, dimStyle().Render(header))

	// Hop rows.
	hopMap := make(map[int]*hop.Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}
	hopMaxTTL := s.table.MaxTTLSeen()
	if s.maxTTLHit > 0 && s.maxTTLHit < hopMaxTTL {
		hopMaxTTL = s.maxTTLHit
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

		rndStr := fmt.Sprintf("%-*d", pdWidthRnd, h.Rounds())

		if h.IsDivergent() {
			for _, pn := range h.GetNodes() {
				partial := m.renderProbeDetailHopRow(ttl, pn, ipWidth, isICMPProto)
				lines = append(lines, partial+" "+rndStr)
			}
		} else {
			pn := h.PrimaryNode()
			if pn == nil {
				continue
			}
			partial := m.renderProbeDetailHopRow(ttl, pn, ipWidth, isICMPProto)
			lines = append(lines, partial+" "+rndStr)
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

	// fixedLines: 2 (header + sep) + 1 (col-header) + 1 (status bar).
	fixedLines := 4
	if multiTarget {
		fixedLines += m.summaryLineCount()
	}
	availableRows := 0
	if m.height > fixedLines {
		availableRows = m.height - fixedLines
	}

	// Render probe detail rows; [0] is the col-header.
	allLines := m.renderProbeDetailView()

	// Write col-header (always present at index 0).
	b.WriteString(lineBg(allLines[0], m.width))
	b.WriteString("\n")

	// Hop content rows with scroll handling.
	hopLines := allLines[1:]
	displayLines := hopLines
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

	// Fill remaining vertical space with themed background.
	emptyLine := lineBg("", m.width)
	usedLines := fixedLines + len(displayLines)
	if m.height > 0 && usedLines < m.height {
		for i := 0; i < m.height-usedLines; i++ {
			b.WriteString(emptyLine)
			b.WriteString("\n")
		}
	}

	// Status bar.
	maxTTL := s.table.MaxTTLSeen()
	elapsed := m.now().Sub(m.startTime).Truncate(time.Second)
	traceStatus := fmt.Sprintf("Tracing... %d hops", maxTTL)
	if m.paused {
		traceStatus = "PAUSED"
	} else if s.targetHit {
		traceStatus = fmt.Sprintf("Tracing... %d/%d hops", maxTTL, s.maxTTLHit)
	}
	dnsLabel := "on"
	if !m.dnsEnabled {
		dnsLabel = "off"
	}
	scrollHint := ""
	if !m.autoScroll && len(hopLines) > availableRows {
		scrollHint = " [scrolled] G:bottom g:top"
	}
	multiHint := ""
	if len(m.sessions) > 1 {
		multiHint = "    ↑/↓:target space:mark c:columns"
	}
	footerNote := "echo cadence: ceil(pairs/budget) rounds"
	keys := "j/k:scroll p:pause r:reset [d] " + m.viewMode.Next().String()
	status := m.statusLine(
		fmt.Sprintf("%s    Elapsed: %s    DNS: %s    %s q:quit%s    %s%s",
			traceStatus, elapsed, dnsLabel, keys, scrollHint, footerNote, multiHint),
		statusParts{
			trace:   traceStatus,
			elapsed: fmt.Sprintf("Elapsed: %s", elapsed),
			dns:     "DNS: " + dnsLabel,
			keys:    keys,
			scroll:  strings.TrimSpace(scrollHint),
			multi:   strings.TrimSpace(multiHint),
			footer:  footerNote,
		})
	b.WriteString(barBg(statusStyle().Render(status), m.width))

	// Settings overlay — rendered on top of everything else.
	if m.showSettings && m.settings != nil {
		return m.settings.View()
	}

	return b.String()
}
