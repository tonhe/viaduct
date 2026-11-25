package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The header line and the status bar are built from segments, so that on a
// terminal too narrow for the whole line the least important parts go first,
// the way the hop table drops columns. A line that fits is rendered exactly
// as it always was.

// barSegment is one part of a header or status line. drop is its place in the
// queue to be left out when the line does not fit: 0 is never dropped, and
// among the rest the highest number goes first.
type barSegment struct {
	text string
	drop int
}

// barSeparator sits between the segments of a shortened bar.
const barSeparator = "    "

// fitBar returns full when it fits in width. Otherwise it joins segs, leaving
// out droppable segments, highest drop number first, until the line fits. If
// the segments that are never dropped are still too wide, the line is cut
// with an ellipsis. Empty segments are skipped. A width of zero or less means
// the terminal size is not known yet, and full is returned.
func fitBar(width int, full string, segs []barSegment) string {
	if width <= 0 || lipgloss.Width(full) <= width {
		return full
	}
	kept := make([]barSegment, 0, len(segs))
	for _, s := range segs {
		if s.text != "" {
			kept = append(kept, s)
		}
	}
	join := func() string {
		parts := make([]string, len(kept))
		for i, s := range kept {
			parts[i] = s.text
		}
		return strings.Join(parts, barSeparator)
	}
	for lipgloss.Width(join()) > width {
		worst := -1
		for i, s := range kept {
			if s.drop > 0 && (worst < 0 || s.drop > kept[worst].drop) {
				worst = i
			}
		}
		if worst < 0 {
			break
		}
		kept = append(kept[:worst], kept[worst+1:]...)
	}
	return cutToWidth(join(), width)
}

// cutToWidth shortens s to at most width display cells, ending in an
// ellipsis when anything was removed.
func cutToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > width {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// targetSegment renders "Target: name (address)" in at most width cells. The
// name is shortened first, then the address is shortened in the middle, so
// both ends of an IPv6 address stay visible.
func targetSegment(name, addr string, width int) string {
	const label = "Target: "
	full := label + name + " (" + addr + ")"
	if width <= 0 || lipgloss.Width(full) <= width {
		return full
	}
	room := width - lipgloss.Width(label) - len(" ()")
	if room < 2 {
		return cutToWidth(full, width)
	}
	// Give the name what the address leaves, but at least 8 cells (or half
	// the room when even that is too much).
	nameRoom := room - lipgloss.Width(addr)
	if minName := min(8, room/2); nameRoom < minName {
		nameRoom = minName
	}
	shortName := truncateSummary(name, nameRoom)
	shortAddr := truncateAddr(addr, room-lipgloss.Width(shortName))
	return cutToWidth(label+shortName+" ("+shortAddr+")", width)
}

// minTargetWidth is the least room the header gives the target before it
// starts dropping other segments: the label, 8 cells of name, the brackets
// and a 15-cell address.
const minTargetWidth = len("Target: ") + 8 + len(" ()") + 15

// headerLine is the title line: version, target, protocol, probe count and,
// outside the default view, the view's name. When it does not fit, the target
// is shortened first (name, then address); if that is not enough the probe
// count, the version, the view name and the protocol are left out, in that
// order. The target is always shown.
func (m Model) headerLine(s *TargetSession, viewName string) string {
	protoLabel := strings.ToUpper(s.protocolName)
	if s.protocolName == "auto" {
		protoLabel = "Auto → " + strings.ToUpper(s.probeCfg.Protocol.Name())
	}
	if s.protocolName != "icmp" && s.probeCfg.NumPaths > 1 {
		protoLabel += "/ECMP"
	}
	addr := "unresolved"
	if s.targetIP != nil {
		addr = s.targetIP.String()
	}
	version := "via " + m.version
	proto := "Proto: " + protoLabel
	probes := "Probes: " + strconv.Itoa(s.probeCount)
	view := ""
	if viewName != "" {
		view = "View: " + viewName
	}

	full := version + "   " + targetSegment(s.target, addr, 0) + barSeparator + proto + barSeparator + probes
	if view != "" {
		full += barSeparator + view
	}
	if m.width <= 0 || lipgloss.Width(full) <= m.width {
		return full
	}

	// The line in order. The target has drop 0 and an empty text: it is
	// rendered last, into whatever room the others leave.
	segs := []barSegment{{version, 3}, {"", 0}, {proto, 1}, {probes, 4}}
	if view != "" {
		segs = append(segs, barSegment{view, 2})
	}
	for {
		used := 0
		for _, seg := range segs {
			if seg.drop > 0 {
				used += lipgloss.Width(seg.text) + len(barSeparator)
			}
		}
		room := m.width - used
		// Enough for the target as it is, or for its shortest useful form.
		need := min(minTargetWidth, lipgloss.Width(targetSegment(s.target, addr, 0)))
		if room >= need || len(segs) == 1 {
			parts := make([]string, len(segs))
			for i, seg := range segs {
				parts[i] = seg.text
				if seg.drop == 0 {
					parts[i] = targetSegment(s.target, addr, room)
				}
			}
			line := strings.Join(parts, barSeparator)
			// The version keeps its three-space gap, as in the full line.
			line = strings.Replace(line, version+barSeparator, version+"   ", 1)
			return cutToWidth(line, m.width)
		}
		// Leave out the segment with the highest drop number.
		worst := -1
		for i, seg := range segs {
			if seg.drop > 0 && (worst < 0 || seg.drop > segs[worst].drop) {
				worst = i
			}
		}
		segs = append(segs[:worst], segs[worst+1:]...)
	}
}

// statusParts are the pieces of a status bar. Empty ones are skipped.
type statusParts struct {
	trace   string // "Tracing... N hops" or "PAUSED": always shown
	elapsed string // "Elapsed: 12s"
	dns     string // "DNS: on"
	flows   string // "6 flows"
	notice  string // setup notice, protocol switch or export result
	keys    string // key hints other than q:quit
	scroll  string // "[scrolled] G:bottom g:top"
	multi   string // multi-target key hints
	footer  string // view-specific note
}

// statusLine returns full when it fits. Otherwise it keeps the trace status
// and q:quit and leaves out, in this order: the view note, the multi-target
// hints, the key hints, the scroll hint, DNS, flows, elapsed, and last of all
// the notice, which says something the keys do not.
func (m Model) statusLine(full string, p statusParts) string {
	line := fitBar(m.width, full, []barSegment{
		{p.trace, 0},
		{p.elapsed, 3},
		{p.dns, 5},
		{p.flows, 4},
		{p.notice, 2},
		{p.keys, 7},
		{"q:quit", 0},
		{p.scroll, 6},
		{p.footer, 9},
		{p.multi, 8},
	})
	// While the key hints are shown, q:quit sits with them, as in the full line.
	if p.keys != "" {
		line = strings.Replace(line, p.keys+barSeparator+"q:quit", p.keys+" q:quit", 1)
	}
	return line
}

// separatorLine is the rule under the header: the terminal's width, or 60
// before the size is known.
func separatorLine(width int) string {
	if width <= 0 {
		width = 60
	}
	return strings.Repeat("─", width)
}
