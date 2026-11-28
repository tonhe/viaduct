package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// statusBar returns the last line of the rendered view, without color or
// trailing padding.
func statusBar(m Model) string {
	lines := strings.Split(strings.TrimRight(stripANSI(m.View()), "\n"), "\n")
	return strings.TrimRight(lines[len(lines)-1], " ")
}

// staleTailModel is a single-target model whose destination is at TTL 3 and
// whose table also holds round one's entries at TTL 4 to 6, as every real
// trace of a reachable target does.
func staleTailModel(view ViewMode) Model {
	m := newTestModel(withSize(160, 30), withView(view))
	stale := staleTailSession()
	m.sessions[0].target = stale.target
	m.sessions[0].targetIP = stale.targetIP
	m.sessions[0].table = stale.table
	m.sessions[0].maxTTLHit = stale.maxTTLHit
	m.sessions[0].targetHit = true
	return m
}

// The status bar's hop count is the destination's distance once it has
// answered, not the highest TTL that ever replied next to it (viaduct#105).
func TestStatusBar_HopCountIsTheDestinationDistance(t *testing.T) {
	for _, view := range []ViewMode{ViewDefault, ViewProbeDetail} {
		t.Run(view.String(), func(t *testing.T) {
			m := staleTailModel(view)
			if got := m.sessions[0].table.MaxTTLSeen(); got != 6 {
				t.Fatalf("fixture: highest TTL seen = %d, want 6", got)
			}
			status := statusBar(m)
			if !strings.HasPrefix(status, "Tracing... 3 hops    ") {
				t.Errorf("status bar = %q, want it to start with %q", status, "Tracing... 3 hops")
			}
			if strings.Contains(status, "6/3") || strings.Contains(status, "/3 hops") {
				t.Errorf("status bar still shows a second hop number: %q", status)
			}
		})
	}
}

// Until the destination answers, the count is the highest TTL seen.
func TestStatusBar_HopCountBeforeTheDestinationAnswers(t *testing.T) {
	for _, view := range []ViewMode{ViewDefault, ViewProbeDetail} {
		t.Run(view.String(), func(t *testing.T) {
			m := newTestModel(withSize(160, 30), withView(view), withHops(
				hopSample(1, "192.0.2.1", time.Millisecond),
				hopSample(4, "192.0.2.4", 4*time.Millisecond),
			))
			if status := statusBar(m); !strings.HasPrefix(status, "Tracing... 4 hops    ") {
				t.Errorf("status bar = %q, want it to start with %q", status, "Tracing... 4 hops")
			}
		})
	}
}

// What the status bar shows at each width (viaduct#104). When it does not fit
// it gives up DNS, then flows, then elapsed, then the key hints one at a
// time, least used first. The hop count and q:quit are always there.
func TestStatusBar_SegmentsByWidth(t *testing.T) {
	single := func(w int) Model { return multiTargetModel(w, 30, 1, 12) }
	multi := func(w int) Model { return multiTargetModel(w, 30, 3, 12) }
	columns := func(w int) Model {
		m := multiTargetModel(w, 30, 3, 12)
		m.columnView = true
		return m
	}
	detail := func(w int) Model {
		m := multiTargetModel(w, 30, 1, 12)
		m.viewMode = ViewProbeDetail
		return m
	}
	for _, tc := range []struct {
		name  string
		model func(w int) Model
		width int
		want  string
	}{
		{"single", single, 100, "Tracing... 12 hops    Elapsed: 0s    j/k:scroll e:export p:pause r:reset [d] Health q:quit"},
		{"single", single, 80, "Tracing... 12 hops    j/k:scroll e:export p:pause r:reset [d] Health q:quit"},
		{"single", single, 72, "Tracing... 12 hops    j/k:scroll e:export p:pause [d] Health q:quit"},
		{"single", single, 60, "Tracing... 12 hops    j/k:scroll p:pause [d] Health q:quit"},

		{"multi", multi, 100, "Tracing... 12 hops    j/k:scroll p:pause [d] Health q:quit    ↑/↓:target space:mark c:columns"},
		{"multi", multi, 80, "Tracing... 12 hops    j/k:scroll [d] Health q:quit    ↑/↓:target c:columns"},
		{"multi", multi, 72, "Tracing... 12 hops    [d] Health q:quit    ↑/↓:target c:columns"},
		{"multi", multi, 60, "Tracing... 12 hops    [d] Health q:quit    ↑/↓:target"},

		{"column view", columns, 100, "Tracing... 12 hops    tab/↑↓:focus  space:mark  c:summary  e:export  p:pause  r:reset  q:quit"},
		{"column view", columns, 80, "Tracing... 12 hops    tab/↑↓:focus  space:mark  c:summary  p:pause  q:quit"},
		{"column view", columns, 72, "Tracing... 12 hops    tab/↑↓:focus  space:mark  c:summary  q:quit"},
		{"column view", columns, 60, "Tracing... 12 hops    tab/↑↓:focus  c:summary  q:quit"},

		{"Probe Detail", detail, 100, "Tracing... 12 hops    Elapsed: 0s    j/k:scroll e:export p:pause r:reset [d] Default q:quit"},
		{"Probe Detail", detail, 80, "Tracing... 12 hops    j/k:scroll e:export p:pause r:reset [d] Default q:quit"},
		{"Probe Detail", detail, 72, "Tracing... 12 hops    j/k:scroll e:export p:pause [d] Default q:quit"},
		{"Probe Detail", detail, 60, "Tracing... 12 hops    j/k:scroll p:pause [d] Default q:quit"},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.name, tc.width), func(t *testing.T) {
			got := statusBar(tc.model(tc.width))
			if got != tc.want {
				t.Errorf("status bar at %d columns:\n got %q\nwant %q", tc.width, got, tc.want)
			}
			if w := lipgloss.Width(got); w > tc.width {
				t.Errorf("status bar is %d columns wide at width %d", w, tc.width)
			}
			for _, always := range []string{"Tracing... 12 hops", "q:quit"} {
				if !strings.Contains(got, always) {
					t.Errorf("status bar lost %q: %q", always, got)
				}
			}
		})
	}
}

// Every view hints e:export, and every view shows the export result while it
// is fresh, column view included (viaduct#144). The result outlasts every key
// hint: the hop count, the result and q:quit are the last things left.
func TestStatusBar_ExportResultInEveryView(t *testing.T) {
	const result = "Exported to via-export.json"
	exported := func(m Model) Model {
		m.exportStatusMsg = result
		m.exportStatusTime = m.now()
		return m
	}
	single := func(w int) Model { return exported(multiTargetModel(w, 30, 1, 12)) }
	columns := func(w int) Model {
		m := multiTargetModel(w, 30, 3, 12)
		m.columnView = true
		return exported(m)
	}
	detail := func(w int) Model {
		m := multiTargetModel(w, 30, 1, 12)
		m.viewMode = ViewProbeDetail
		return exported(m)
	}
	for _, tc := range []struct {
		name  string
		model func(w int) Model
		width int
		want  string
	}{
		{"single", single, 100, "Tracing... 12 hops    " + result + "    j/k:scroll e:export p:pause [d] Health q:quit"},
		{"single", single, 80, "Tracing... 12 hops    " + result + "    [d] Health q:quit"},
		{"single", single, 72, "Tracing... 12 hops    " + result + "    [d] Health q:quit"},
		{"single", single, 60, "Tracing... 12 hops    " + result + "    q:quit"},

		{"column view", columns, 100, "Tracing... 12 hops    " + result + "    tab/↑↓:focus  space:mark  c:summary  q:quit"},
		{"column view", columns, 80, "Tracing... 12 hops    " + result + "    tab/↑↓:focus  q:quit"},
		{"column view", columns, 72, "Tracing... 12 hops    " + result + "    q:quit"},
		{"column view", columns, 60, "Tracing... 12 hops    " + result + "    q:quit"},

		{"Probe Detail", detail, 100, "Tracing... 12 hops    " + result + "    j/k:scroll e:export p:pause [d] Default q:quit"},
		{"Probe Detail", detail, 80, "Tracing... 12 hops    " + result + "    [d] Default q:quit"},
		{"Probe Detail", detail, 72, "Tracing... 12 hops    " + result + "    [d] Default q:quit"},
		{"Probe Detail", detail, 60, "Tracing... 12 hops    " + result + "    q:quit"},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.name, tc.width), func(t *testing.T) {
			got := statusBar(tc.model(tc.width))
			if got != tc.want {
				t.Errorf("status bar at %d columns:\n got %q\nwant %q", tc.width, got, tc.want)
			}
			if w := lipgloss.Width(got); w > tc.width {
				t.Errorf("status bar is %d columns wide at width %d", w, tc.width)
			}
		})
	}
}

// The export result is shown for five seconds; after that column view shows
// no notice at all, and the single-pane views fall back to the setup notice.
func TestStatusBar_ExportResultExpires(t *testing.T) {
	for _, columnView := range []bool{false, true} {
		m := multiTargetModel(200, 30, 3, 12)
		m.columnView = columnView
		m.exportStatusMsg = "Export failed: permission denied"
		m.exportStatusTime = m.now().Add(-6 * time.Second)
		if got := statusBar(m); strings.Contains(got, "Export failed") {
			t.Errorf("column view %v: a six-second-old export result is still shown: %q", columnView, got)
		}
		m.exportStatusTime = m.now()
		if got := statusBar(m); !strings.Contains(got, "Export failed: permission denied") {
			t.Errorf("column view %v: a fresh export result is not shown: %q", columnView, got)
		}
	}
}

// A persistent notice and the scrolled marker outlast the key hints
// (viaduct#144). The order is DNS, flows, elapsed, the key hints, the
// scrolled marker, then the notice; the hop count and q:quit stay. The
// 40-column notice keeps its place at 80 columns, and goes only where it
// cannot fit next to them.
func TestStatusBar_NoticeAndScrollMarkerByWidth(t *testing.T) {
	const notice = "no IPv6 route to 2001:db8::1; using IPv4"
	for _, tc := range []struct {
		width int
		want  string
	}{
		{100, "Tracing... 25 hops    " + notice + "    q:quit [scrolled] G:bottom g:top"},
		{80, "Tracing... 25 hops    " + notice + "    q:quit"},
		{72, "Tracing... 25 hops    " + notice + "    q:quit"},
		{60, "Tracing... 25 hops    q:quit"},
	} {
		t.Run(fmt.Sprint(tc.width), func(t *testing.T) {
			m := multiTargetModel(tc.width, 12, 1, 25)
			m.autoScroll = false
			m.scrollOffset = 2
			m.sessions[0].notice = notice
			got := statusBar(m)
			if got != tc.want {
				t.Errorf("status bar at %d columns:\n got %q\nwant %q", tc.width, got, tc.want)
			}
			if w := lipgloss.Width(got); w > tc.width {
				t.Errorf("status bar is %d columns wide at width %d", w, tc.width)
			}
		})
	}
}

// The hop count and q:quit survive at every width they fit in at all, in
// every view, paused or not.
func TestStatusBar_HopCountAndQuitAlwaysShown(t *testing.T) {
	for _, tc := range barCases() {
		for w := 30; w <= 140; w++ {
			status := statusBar(tc.m(w))
			if !strings.HasPrefix(status, "Tracing... ") || !strings.Contains(status, " hops") || !strings.Contains(status, "q:quit") {
				t.Errorf("%s at %d columns: status bar %q lost the hop count or q:quit", tc.name, w, status)
			}
			if got := lipgloss.Width(status); got > w {
				t.Errorf("%s: status bar is %d columns wide at width %d", tc.name, got, w)
			}
		}
	}
}

// The whole drop order, read off one status bar holding every kind of
// segment as the terminal narrows a column at a time.
func TestStatusBar_DropOrder(t *testing.T) {
	m := multiTargetModel(0, 12, 3, 25)
	m.viewMode = ViewProbeDetail
	m.autoScroll = false
	m.scrollOffset = 2
	m.sessions[0].notice = "no IPv6 route to 2001:db8::1; using IPv4"
	m.width = 400
	full := statusBar(m)

	// First to go at the top, last at the bottom.
	order := []string{
		"echo cadence",
		"DNS: ",
		"Elapsed: ",
		"r:reset",
		"e:export",
		"p:pause",
		"space:mark",
		"j/k:scroll",
		"c:columns",
		"[d] ",
		"↑/↓:target",
		"[scrolled]",
		"no IPv6 route",
	}
	for _, seg := range order {
		if !strings.Contains(full, seg) {
			t.Fatalf("fixture: the full status bar has no %q: %q", seg, full)
		}
	}
	gone := map[string]int{} // segment -> widest terminal that lacks it
	for w := lipgloss.Width(full); w >= 28; w-- {
		m.width = w
		status := statusBar(m)
		for _, seg := range order {
			if _, done := gone[seg]; !done && !strings.Contains(status, seg) {
				gone[seg] = w
			}
		}
		for _, always := range []string{"Tracing... 25 hops", "q:quit"} {
			if !strings.Contains(status, always) {
				t.Fatalf("at %d columns the status bar lost %q: %q", w, always, status)
			}
		}
	}
	for i, seg := range order {
		w, ok := gone[seg]
		if !ok {
			t.Errorf("%q was never dropped", seg)
			continue
		}
		if i > 0 && w > gone[order[i-1]] {
			t.Errorf("%q went at %d columns, before %q (%d): wrong order", seg, w, order[i-1], gone[order[i-1]])
		}
	}
}

// Flows sit between DNS and elapsed in the order. Probe Detail has no flow
// count, so this is read off the default view.
func TestStatusBar_FlowsGoAfterDNSAndBeforeElapsed(t *testing.T) {
	m := multiTargetModel(400, 30, 1, 12)
	full := lipgloss.Width(statusBar(m))
	var dnsAt, flowsAt, elapsedAt int
	for w := full; w >= 30; w-- {
		m.width = w
		status := statusBar(m)
		if dnsAt == 0 && !strings.Contains(status, "DNS: ") {
			dnsAt = w
		}
		if flowsAt == 0 && !strings.Contains(status, "6 flows") {
			flowsAt = w
		}
		if elapsedAt == 0 && !strings.Contains(status, "Elapsed: ") {
			elapsedAt = w
		}
	}
	if !(dnsAt > flowsAt && flowsAt > elapsedAt && elapsedAt > 0) {
		t.Errorf("dropped at widths DNS %d, flows %d, elapsed %d; want DNS first, then flows, then elapsed", dnsAt, flowsAt, elapsedAt)
	}
}

// A paused trace shows PAUSED in place of the hop count and still fits.
func TestStatusBar_Paused(t *testing.T) {
	m := multiTargetModel(72, 30, 1, 12)
	m.paused = true
	if got, want := statusBar(m), "PAUSED    j/k:scroll e:export p:pause r:reset [d] Health q:quit"; got != want {
		t.Errorf("status bar = %q, want %q", got, want)
	}
}

// A hint is glued to its neighbours only while they are in the same group:
// whichever are left, hints stay a space apart and other segments a full
// separator apart.
func TestFitBar_GlueOnlyWithinAGroup(t *testing.T) {
	segs := []barSegment{
		{text: "state"},
		{text: ""},
		{text: "aaaa", drop: 3, group: 1, glue: " "},
		{text: "bbbb", drop: 2, group: 1, glue: " "},
		{text: "quit", group: 1, glue: " "},
		{text: "cccc", drop: 1, group: 2, glue: " "},
	}
	for _, tc := range []struct {
		width int
		want  string
	}{
		{0, "state    aaaa bbbb quit    cccc"},
		{31, "state    aaaa bbbb quit    cccc"},
		{30, "state    bbbb quit    cccc"},
		{26, "state    bbbb quit    cccc"},
		{25, "state    quit    cccc"},
		{21, "state    quit    cccc"},
		{20, "state    quit"},
		{13, "state    quit"},
		{12, "state    qu…"},
		{4, "sta…"},
	} {
		if got := fitBar(tc.width, segs); got != tc.want {
			t.Errorf("fitBar(%d) = %q, want %q", tc.width, got, tc.want)
		}
	}
}
