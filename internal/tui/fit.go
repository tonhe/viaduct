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
//
// Segments are a barSeparator apart. Two neighbours in the same group (other
// than group 0) are joined by the second one's glue instead: key hints sit a
// space apart, and stay that way whichever of them are left.
type barSegment struct {
	text  string
	drop  int
	group int
	glue  string
}

// barSeparator sits between the segments of a bar.
const barSeparator = "    "

// fitBar joins segs, and while the line is wider than width leaves out
// droppable segments, highest drop number first. If the segments that are
// never dropped are still too wide, the line is cut with an ellipsis. Empty
// segments are skipped. A width of zero or less means the terminal size is
// not known yet, and nothing is left out.
func fitBar(width int, segs []barSegment) string {
	kept := make([]barSegment, 0, len(segs))
	for _, s := range segs {
		if s.text != "" {
			kept = append(kept, s)
		}
	}
	join := func() string {
		var b strings.Builder
		for i, s := range kept {
			switch {
			case i == 0:
			case s.group != 0 && s.group == kept[i-1].group:
				b.WriteString(s.glue)
			default:
				b.WriteString(barSeparator)
			}
			b.WriteString(s.text)
		}
		return b.String()
	}
	if width <= 0 {
		return join()
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
	segs := []barSegment{{text: version, drop: 3}, {}, {text: proto, drop: 1}, {text: probes, drop: 4}}
	if view != "" {
		segs = append(segs, barSegment{text: view, drop: 2})
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

// The order in which a status bar that does not fit gives up its segments
// (viaduct#104). This block is the whole order: the last name goes first, the
// first name goes last, and dropNever stays. The trace status (the hop count,
// or PAUSED) and q:quit are never dropped.
//
// The view note goes first, then DNS, flows and elapsed, which are shown
// elsewhere or matter less. The key hints go after those, one at a time and
// the least used first, because a new user needs them to operate the tool.
// The scroll state and a notice outlast the hints: they say something the
// keys do not.
const (
	dropNever = iota

	dropNotice      // setup notice, protocol switch or export result
	dropScrollState // "[scrolled] G:bottom g:top"

	dropHintTarget  // ↑/↓:target, tab/↑↓:focus
	dropHintView    // [d] <next view>
	dropHintColumns // c:columns, c:summary
	dropHintScroll  // j/k:scroll
	dropHintMark    // space:mark
	dropHintPause   // p:pause
	dropHintExport  // e:export
	dropHintReset   // r:reset

	dropElapsed
	dropFlows
	dropDNS
	dropFooter // view-specific note
)

// keyHint is one key hint on the status bar and its place in the drop order.
type keyHint struct {
	text string
	drop int
}

// viewKeyHints are the hints every single-pane view shows before q:quit.
func (m Model) viewKeyHints() []keyHint {
	return []keyHint{
		{"j/k:scroll", dropHintScroll},
		{"p:pause", dropHintPause},
		{"r:reset", dropHintReset},
		{"[d] " + m.viewMode.Next().String(), dropHintView},
	}
}

// targetKeyHints are the extra hints shown with more than one target.
func (m Model) targetKeyHints() []keyHint {
	if len(m.sessions) <= 1 {
		return nil
	}
	return []keyHint{
		{"↑/↓:target", dropHintTarget},
		{"space:mark", dropHintMark},
		{"c:columns", dropHintColumns},
	}
}

// columnKeyHints are the hints of column view.
func columnKeyHints() []keyHint {
	return []keyHint{
		{"tab/↑↓:focus", dropHintTarget},
		{"space:mark", dropHintMark},
		{"c:summary", dropHintColumns},
		{"e:export", dropHintExport},
		{"p:pause", dropHintPause},
		{"r:reset", dropHintReset},
	}
}

// statusParts are the pieces of a status bar, in the order they are shown.
// Empty ones are skipped.
type statusParts struct {
	trace   string    // "Tracing... N hops" or "PAUSED": always shown
	elapsed string    // "Elapsed: 12s"
	dns     string    // "DNS: on"
	flows   string    // "6 flows"
	notice  string    // setup notice, protocol switch or export result
	keys    []keyHint // key hints, followed by q:quit
	keyGlue string    // what joins the key hints and q:quit; one space if empty
	scroll  string    // "[scrolled] G:bottom g:top"
	footer  string    // view-specific note
	targets []keyHint // multi-target key hints
}

// statusLine is the status bar. When it does not fit the terminal it leaves
// segments out in the order of the drop constants above, and keeps the trace
// status and q:quit.
func (m Model) statusLine(p statusParts) string {
	const keyGroup, targetGroup = 1, 2
	glue := p.keyGlue
	if glue == "" {
		glue = " "
	}
	segs := []barSegment{
		{text: p.trace},
		{text: p.elapsed, drop: dropElapsed},
		{text: p.dns, drop: dropDNS},
		{text: p.flows, drop: dropFlows},
		{text: p.notice, drop: dropNotice},
	}
	for _, k := range p.keys {
		segs = append(segs, barSegment{text: k.text, drop: k.drop, group: keyGroup, glue: glue})
	}
	segs = append(segs,
		barSegment{text: "q:quit", group: keyGroup, glue: glue},
		barSegment{text: p.scroll, drop: dropScrollState, group: keyGroup, glue: " "},
		barSegment{text: p.footer, drop: dropFooter},
	)
	for _, k := range p.targets {
		segs = append(segs, barSegment{text: k.text, drop: k.drop, group: targetGroup, glue: " "})
	}
	return fitBar(m.width, segs)
}

// traceStatus is the first segment of the status bar: PAUSED, or the hop
// count. The count is the destination's distance once it has answered and the
// highest TTL seen until then; the table also holds entries beyond the
// destination, which are not part of the path (viaduct#105).
func (m Model) traceStatus(s *TargetSession) string {
	if m.paused {
		return "PAUSED"
	}
	return "Tracing... " + strconv.Itoa(summaryHops(s)) + " hops"
}

// separatorLine is the rule under the header: the terminal's width, or 60
// before the size is known.
func separatorLine(width int) string {
	if width <= 0 {
		width = 60
	}
	return strings.Repeat("─", width)
}
