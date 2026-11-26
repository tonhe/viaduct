// Package tui implements the terminal UI using bubbletea.
package tui

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tonhe/viaduct/internal/asn"
	"github.com/tonhe/viaduct/internal/config"
	"github.com/tonhe/viaduct/internal/export"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/probe"
	"github.com/tonhe/viaduct/internal/resolve"
	"github.com/tonhe/viaduct/internal/theme"
)

// Styles — theme-aware functions so colors update when theme changes.
// Individual styles set only Foreground. Background is applied per-line via
// lineBg() / barBg() which patch ANSI resets to re-assert background color,
// ensuring the entire line has a uniform background with no gaps.

func headerStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base05)
}
func dimStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base04)
}
func ipStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base0D)
}
func hostStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base05)
}
func lossStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base08)
}
func okStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base0B)
}
func statusStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base04)
}
func starStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base03)
}
func treeStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base03)
}
func flowStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base0D)
}
func stabStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base03)
}
func rateLimitStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base03)
}
func asnStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base03)
}
func asnBoundaryStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base0A).Bold(true)
}
func amberStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base09)
}
func trendDegStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base08)
}
func trendImpStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base0B)
}
func alertStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base08).Bold(true)
}

// lossStyleFor picks the loss-cell style based on the loss percent: green for
// 0%, red otherwise. Rate-limited cells bypass this and use rateLimitStyle.
func lossStyleFor(loss float64) lipgloss.Style {
	if loss == 0 {
		return okStyle()
	}
	return lossStyle()
}

// lossCell renders a hop's loss figure for the table's 8-column Loss% cell:
// a measurement in green or red, echo loss on a rate-limited hop as "~N%†",
// and loss whose cause is not yet known as "N%?", dimmed, so it is not read
// as a measurement of the path.
func lossCell(h *hop.Hop) string {
	loss, kind := h.Loss()
	plain := padCell(hop.FormatLoss(loss, kind), 8)
	switch kind {
	case hop.LossRateLimited:
		return rateLimitStyle().Render(plain)
	case hop.LossProvisional:
		return dimStyle().Render(plain)
	default:
		return lossStyleFor(loss).Render(plain)
	}
}

// padCell left-aligns plain (unstyled) text to width columns. Styling must be
// applied AFTER padding: fmt pads by byte length, so styling first garbles
// real terminals while passing color-stripped tests.
func padCell(plain string, width int) string {
	if w := lipgloss.Width(plain); w < width {
		return plain + strings.Repeat(" ", width-w)
	}
	return plain
}

// hexToANSI converts a lipgloss.Color hex string to a 24-bit ANSI escape.
// mode 38 = foreground, mode 48 = background.
func hexToANSI(c lipgloss.Color, mode int) string {
	hex := string(c)
	if len(hex) > 0 && hex[0] == '#' {
		hex = hex[1:]
	}
	if len(hex) != 6 {
		return ""
	}
	r := hexByte(hex[0:2])
	g := hexByte(hex[2:4])
	b := hexByte(hex[4:6])
	return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", mode, r, g, b)
}

func hexByte(s string) uint8 {
	var v uint8
	for _, c := range s {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint8(c - '0')
		case c >= 'a' && c <= 'f':
			v |= uint8(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			v |= uint8(c - 'A' + 10)
		}
	}
	return v
}

const ansiReset = "\x1b[0m"

// paintLine takes a line (possibly containing ANSI styled fragments) and paints
// the given foreground + background colors across the entire line. It works by:
//  1. Prefixing with fg+bg escapes so unstyled text inherits them
//  2. Patching every \x1b[0m reset to re-assert fg+bg after it
//  3. Padding to the target width with colored spaces
//  4. Ending with a final reset
func paintLine(line string, width int, fgColor, bgColor lipgloss.Color) string {
	fgSeq := hexToANSI(fgColor, 38)
	bgSeq := hexToANSI(bgColor, 48)
	combo := fgSeq + bgSeq
	if combo == "" {
		return line
	}

	// Patch resets: every time a styled fragment resets, re-assert fg+bg
	patched := strings.ReplaceAll(line, ansiReset, ansiReset+combo)

	// Prefix with fg+bg
	result := combo + patched

	// Pad to target width
	visualW := lipgloss.Width(line)
	if visualW < width {
		result += strings.Repeat(" ", width-visualW)
	}

	// Final reset
	result += ansiReset
	return result
}

// lineBg wraps a line to exactly the given width with the theme's Base05
// foreground and Base00 background, ensuring no gaps between styled fragments.
func lineBg(line string, width int) string {
	return paintLine(line, width, theme.Current.Base05, theme.Current.Base00)
}

// barBg wraps a line to exactly the given width with the theme's Base04
// foreground and Base01 (lighter) background, used for header and status bars.
func barBg(line string, width int) string {
	return paintLine(line, width, theme.Current.Base04, theme.Current.Base01)
}

// dnsSubmitter is the narrow interface used by onHopUpdate to queue IPs for
// reverse-DNS resolution. The production implementation is *resolve.Resolver;
// tests may inject a recording fake.
type dnsSubmitter interface {
	Submit(ip net.IP) bool
}

// Model is the bubbletea model for the TUI.
type Model struct {
	// sessions holds one TargetSession per trace target. Single-target mode
	// always has exactly one session at index 0. The slice is fixed at startup;
	// indices in target-indexed messages stay valid for the Model's lifetime.
	sessions   []*TargetSession
	focusedIdx int

	// Global (non-per-target) fields.
	version      string
	resolver     *resolve.Resolver // read-only lookups only; submissions go through dnsSub
	dnsSub       dnsSubmitter      // used by onHopUpdate; defaults to resolver
	enricher     *asn.Enricher
	startTime    time.Time
	err          error
	width        int
	height       int
	paused       bool
	dnsEnabled   bool
	viewMode     ViewMode
	columnView   bool // true = side-by-side column layout (multi-target only)
	scrollOffset int  // number of lines scrolled from bottom (0 = bottom)
	autoScroll   bool // true = follow latest data
	config       *config.Config // reference to persisted config
	settings     *SettingsModel // nil when settings closed
	showSettings bool
	exportStatusMsg  string
	exportStatusTime time.Time
	nowFunc          func() time.Time // for test determinism; nil defaults to time.Now
}

// focused returns the currently focused session. Model always has at least one session.
func (m Model) focused() *TargetSession { return m.sessions[m.focusedIdx] }

// now returns the current time, using the injected nowFunc if set,
// otherwise time.Now.
func (m Model) now() time.Time {
	if m.nowFunc != nil {
		return m.nowFunc()
	}
	return time.Now()
}

// New creates a new TUI model.
func New(target string, targetIP net.IP, cfg probe.Config, version string, protocolName string, noASN bool, alertLoss float64, alertLatency time.Duration, alertRounds int, noAlert bool, appCfg *config.Config) Model {
	s := newSession(target, targetIP, cfg, protocolName, alertLoss, alertLatency, alertRounds, noAlert)
	res := resolve.New(4)
	return Model{
		sessions:  []*TargetSession{s},
		version:   version,
		resolver:  res,
		dnsSub:    res,
		enricher: func() *asn.Enricher {
			if noASN {
				return nil
			}
			return asn.New(4)
		}(),
		startTime:  time.Now(),
		dnsEnabled: !cfg.NoDNS,
		autoScroll: true,
		config:     appCfg,
	}
}

// Resolver returns the resolver so main.go can submit IPs for resolution.
func (m *Model) Resolver() *resolve.Resolver {
	return m.resolver
}

// Table returns the hop table for the primary (first) session.
func (m *Model) Table() *hop.Table {
	return m.sessions[0].table
}

// SetTracer sets the probe tracer on the primary (first) session so the TUI
// can pause/unpause it.
func (m *Model) SetTracer(t *probe.Tracer) {
	m.sessions[0].tracer = t
}

// SessionTable returns the hop table for session i.
func (m *Model) SessionTable(i int) *hop.Table {
	return m.sessions[i].table
}

// SetSessionTracer sets the probe tracer on session i and wires the echo sink.
func (m *Model) SetSessionTracer(i int, t *probe.Tracer) {
	m.sessions[i].tracer = t
	m.sessions[i].echoSink = t
}

// Enricher returns the ASN enricher so main.go can start it and submit IPs.
func (m Model) Enricher() *asn.Enricher { return m.enricher }

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

// Init returns the initial command (tick timer only).
func (m Model) Init() tea.Cmd {
	return tickCmd()
}

// Update handles messages by dispatching to per-message-type sub-handlers.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SettingsSavedMsg:
		return m.onSettingsSaved(msg)
	case SettingsCancelMsg:
		return m.onSettingsCancel(msg)
	case tea.KeyMsg:
		return m.onKey(msg)
	case tea.WindowSizeMsg:
		return m.onWindowSize(msg)
	case HopUpdateMsg:
		return m.onHopUpdate(msg)
	case HostnameMsg:
		return m.onHostname(msg)
	case ASNMsg:
		return m.onASN(msg)
	case RoundEndMsg:
		return m.onRoundEnd(msg)
	case EchoSentMsg:
		return m.onEchoSent(msg)
	case TickMsg:
		return m.onTick(msg)
	case ProtocolSwitchMsg:
		return m.onProtocolSwitch(msg)
	case ExportDoneMsg:
		return m.onExportDone(msg)
	case ProbeErrorMsg:
		return m.onProbeError(msg)
	}
	return m, nil
}

// View renders the TUI: the frame for the current view, never taller than the
// terminal.
func (m Model) View() string {
	return clipFrame(m.renderView(), m.height)
}

// clipFrame cuts a frame to at most height lines. The last line is kept, since
// it is the status bar; the lines above it are cut from the bottom. A frame
// taller than the terminal scrolls the header off the top, which is what
// happened when the summary panel of a 16-target session left no room on a
// short terminal. A height of zero or less means the size is not known yet.
func clipFrame(frame string, height int) string {
	if height <= 0 {
		return frame
	}
	// A frame may end with a newline; that is not an extra line.
	body := strings.TrimSuffix(frame, "\n")
	lines := strings.Split(body, "\n")
	if len(lines) <= height {
		return frame
	}
	kept := append(lines[:height-1:height-1], lines[len(lines)-1])
	return strings.Join(kept, "\n") + frame[len(body):]
}

// renderView renders the frame for the current view at whatever height it
// comes to; View clips it.
func (m Model) renderView() string {
	if m.err != nil {
		return m.errorView()
	}

	// Probe Detail view uses a custom full-page renderer. In column view the
	// panes are the view: they are not replaced by one target's probe table.
	inColumnView := len(m.sessions) > 1 && m.columnView
	if m.viewMode == ViewProbeDetail && !inColumnView {
		return m.viewProbeDetail()
	}

	s := m.focused()
	var b strings.Builder

	// Header bar
	// The panes of column view do not change with the view mode, so the
	// header names a view only outside it.
	viewName := ""
	if m.viewMode != ViewDefault && !inColumnView {
		viewName = m.viewMode.String()
	}
	b.WriteString(barBg(headerStyle().Render(m.headerLine(s, viewName)), m.width))
	b.WriteString("\n")

	// Separator
	b.WriteString(barBg(dimStyle().Render(separatorLine(m.width)), m.width))
	b.WriteString("\n")

	// Column comparison view: side-by-side layout when more than one session
	// is active and the user has toggled column view with 'c'.
	if len(m.sessions) > 1 && m.columnView {
		colBlock := m.renderColumns()
		b.WriteString(colBlock)
		// Fill remaining space above the status bar.
		// usedCol = 2 lines already written (title + separator) + lines in colBlock
		colLines := strings.Count(colBlock, "\n")
		usedCol := 2 + colLines
		emptyLine := lineBg("", m.width)
		if m.height > 0 && usedCol+1 < m.height { // +1 for status bar
			for i := usedCol + 1; i < m.height; i++ {
				b.WriteString(emptyLine)
				b.WriteString("\n")
			}
		}
		// Status bar consistent with summary branch
		elapsed := m.now().Sub(m.startTime).Truncate(time.Second)
		dnsLabel := "on"
		if !m.dnsEnabled {
			dnsLabel = "off"
		}
		// Column view: surface the focus/mark/select keys on the status bar.
		// space marks the focused target; when any are marked, only marked panes
		// show — so mark doubles as pane selection.
		status := m.statusLine(statusParts{
			trace:   m.traceStatus(m.focused()),
			elapsed: fmt.Sprintf("Elapsed: %s", elapsed),
			dns:     "DNS: " + dnsLabel,
			keys:    columnKeyHints(),
			keyGlue: "  ",
		})
		b.WriteString(barBg(statusStyle().Render(status), m.width))
		if m.showSettings && m.settings != nil {
			return m.settings.View()
		}
		return b.String()
	}

	// Multi-target summary panel: render above the detail pane when more than
	// one session is active and column view is not in use.
	multiTarget := len(m.sessions) > 1 && !m.columnView
	if multiTarget {
		b.WriteString(m.renderSummary())
	}

	// Column headers (adaptive to terminal width)
	layout := m.getLayout()
	colHeader := m.renderColumnHeader(layout)
	b.WriteString(lineBg(dimStyle().Render(colHeader), m.width))
	b.WriteString("\n")

	// Determine how many hop lines we can show.
	// Fixed lines consumed: 3 header lines (title + separator + col header) + 1 status bar.
	// In multi-target mode subtract the summary panel (header + one row per session).
	fixedLines := m.fixedLineCount()
	availableRows := m.hopRowsAvailable()

	// hopLines holds all rendered hop rows before viewport clipping.
	// displayLines is the subset actually written to the builder.
	var hopLines []string
	var displayLines []string

	// When the focused session failed to start, render an error detail pane
	// in place of the hop table and skip all hop-rendering logic.
	if s.startErr != nil {
		b.WriteString(m.renderFocusedError(s, availableRows))
		// displayLines stays nil; displayCount below will be 0 because
		// renderFocusedError already filled availableRows lines.
		displayLines = nil
	} else {
		// Hop rows — iterate by TTL to show gap hops as *
		hops := s.table.Snapshot()
		hopMap := make(map[int]*hop.Hop, len(hops))
		for _, h := range hops {
			hopMap[h.TTL] = h
		}
		hopMaxTTL := s.table.MaxTTLSeen()
		if s.maxTTLHit > 0 && s.maxTTLHit < hopMaxTTL {
			hopMaxTTL = s.maxTTLHit
		}

		// Compute deltas for Health/Latency views
		deltas := computeDeltas(hopMap, hopMaxTTL)
		maxDeltaTTL := largestDeltaTTL(deltas)

		// Compute per-divergent-hop node caps based on available vertical space.
		// Baseline: 1 line per TTL. Surplus rows are distributed top-down to
		// divergent hops, expanding each by its extra nodes until space runs out.
		type divInfo struct {
			ttl      int
			maxNodes int
			total    int // total nodes at this TTL
		}
		var divHops []divInfo
		baseline := hopMaxTTL // 1 line per TTL
		for ttl := 1; ttl <= hopMaxTTL; ttl++ {
			if h, ok := hopMap[ttl]; ok && h.IsDivergent() {
				nodes := h.GetNodes()
				divHops = append(divHops, divInfo{ttl: ttl, maxNodes: 1, total: len(nodes)})
			}
		}
		surplus := availableRows - baseline
		if surplus < 0 {
			surplus = 0
		}
		// Distribute surplus rows top-down
		for i := range divHops {
			extra := divHops[i].total - 1 // nodes beyond the primary
			if extra > surplus {
				extra = surplus
			}
			divHops[i].maxNodes += extra
			surplus -= extra
			if surplus <= 0 {
				break
			}
		}
		// Build a TTL→maxNodes lookup
		divCap := make(map[int]int, len(divHops))
		for _, d := range divHops {
			divCap[d.ttl] = d.maxNodes
		}

		// Build all hop lines
		for ttl := 1; ttl <= hopMaxTTL; ttl++ {
			h, ok := hopMap[ttl]
			if !ok || h.GetIP() == nil {
				// The marker sits in the IP column, like an address.
				star := starStyle().Render("*")
				hopLines = append(hopLines, fmt.Sprintf("%s %s%s", dimStyle().Render(fmt.Sprintf("%-3d", ttl)), layout.treeGap(), star))
				continue
			}
			if h.IsDivergent() {
				cap := divCap[ttl]
				if cap < 1 {
					cap = 1
				}
				hopLines = append(hopLines, m.renderDivergentHop(h, deltas, maxDeltaTTL, cap)...)
			} else {
				hopLines = append(hopLines, m.renderHop(h, deltas, maxDeltaTTL))
			}
		}

		displayLines = hopLines
		if availableRows > 0 && len(hopLines) > availableRows {
			if m.autoScroll {
				// Auto-scroll: show the last N lines
				displayLines = hopLines[len(hopLines)-availableRows:]
			} else {
				// Manual scroll: offset from bottom
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
	}

	// Alert bar (if degraded). In multi-target mode, prefix with the target
	// name so the operator knows which trace is degraded.
	alertLine := ""
	if s.alertEngine != nil {
		alertLine = s.alertEngine.Message()
		if alertLine != "" && multiTarget {
			alertLine = s.target + ": " + alertLine
		}
	}

	// Fill remaining space with themed background.
	// usedLines accounts for: 3 header lines, summary panel (if any),
	// 1 col-header line (already in fixedLines), displayed hop lines,
	// optional alert bar, 1 status bar.
	emptyLine := lineBg("", m.width)
	displayCount := len(displayLines)
	if s.startErr != nil {
		// renderFocusedError already wrote availableRows lines.
		displayCount = availableRows
	}
	extraLines := 0
	if alertLine != "" {
		extraLines = 1
	}
	usedLines := fixedLines + displayCount + extraLines
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

	// Status bar
	elapsed := m.now().Sub(m.startTime).Truncate(time.Second)

	dnsLabel := "on"
	if !m.dnsEnabled {
		dnsLabel = "off"
	}
	flowLabel := ""
	if s.probeCfg.NumPaths > 1 {
		flowLabel = fmt.Sprintf("%d flows", s.probeCfg.NumPaths)
	}
	// Setup notice (e.g. an IPv4 fallback): stays for the whole trace.
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
		flows:   flowLabel,
		notice:  notice,
		keys:    m.viewKeyHints(),
		scroll:  scrollHint,
		targets: m.targetKeyHints(),
	})
	b.WriteString(barBg(statusStyle().Render(status), m.width))

	// Settings overlay — rendered on top of everything else.
	if m.showSettings && m.settings != nil {
		return m.settings.View()
	}

	return b.String()
}

// columnLayout determines which columns are visible and their widths based on terminal width.
type columnLayout struct {
	ipWidth       int
	showTree      bool // ECMP tree connector column (4 chars)
	treeOptional  bool // no hop is divergent, so the tree column is empty and may be dropped to fit
	showHostname  bool
	hostnameWidth int
	showRnd       bool
	showBest      bool
	showWrst      bool
	showStDev     bool
	showLast      bool
	showASN       bool
	asnWidth      int
	asnFull       bool // show full "AS1234 (ORG)" instead of just "AS1234"
	showDelta     bool
	showGMean     bool
	showJttr      bool
	showJavg      bool
	showSpark     bool
	showTrend     bool
}

// asnColumnWidth scans visible hops and returns the width needed for the
// ASN column. Short mode returns width for the longest "AS<number>" string;
// full mode returns width for "AS<number> (<org>)". Minimum is 7 ("AS" + 5 digits).
func (m Model) asnColumnWidth(full bool) int {
	min := 7 // "AS65535"
	if m.enricher == nil {
		return min
	}
	s := m.focused()
	maxLen := 0
	for _, h := range s.table.Snapshot() {
		ip := h.GetIP()
		if ip == nil {
			continue
		}
		info, ok := m.enricher.Lookup(ip)
		if !ok || info.Number == 0 {
			continue
		}
		var label string
		if full {
			label = asn.FormatASN(info.Number, info.Org)
		} else {
			label = asn.FormatASNShort(info.Number)
		}
		if len(label) > maxLen {
			maxLen = len(label)
		}
	}
	if maxLen < min {
		return min
	}
	return maxLen
}

func (m Model) getLayout() columnLayout {
	w := m.width
	if w < 40 {
		w = 40
	}

	s := m.focused()
	// Compute IP column width based on actual hops (handles IPv6 addresses).
	hops := s.table.Snapshot()
	ipWidth := computeIPColWidth(hops)
	layout := columnLayout{ipWidth: ipWidth}

	// The tree column carries the connectors of hops with more than one node.
	// It is a column of the layout, present in the header and in every row,
	// so a connector never moves the columns after it. It is shown whenever a
	// hop has more than one node, whatever the flow count: one flow still
	// meets several nodes at a hop when routers balance per packet, which is
	// what an ICMP trace sees. With several flows and no such hop yet it is
	// blank, and can go when the terminal is narrow.
	divergent := false
	for _, h := range hops {
		if h.IsDivergent() {
			divergent = true
			break
		}
	}
	if divergent || s.probeCfg.NumPaths > 1 {
		layout.showTree = true
		layout.treeOptional = !divergent
	}

	// Budget-based layout: start with fixed columns (#, IP, Loss%, Avg, Spark),
	// then add optional columns in priority order only if there's remaining room.
	// This guarantees columns never overflow the terminal width.
	//
	// Column widths (from renderColumnHeader / renderHopRow):
	//   # = 3, tree = 4, IP = ipWidth, Hostname = hostnameWidth,
	//   ASN = asnWidth, Loss% = 8, Rnd = 5, Avg/Best/Wrst/StDev/Last/Delta/GMean/Jttr/Javg = 7,
	//   Spark = 14, Trend = trendWidth (11)
	// Parts are joined with " " (1 char per gap), so total separators = numParts - 1.
	//
	// Fixed parts: #(3) + IP(ipWidth) + Loss%(8) + Avg(7) + Spark(14) = 5 parts
	// Separators between 5 parts = 4
	base := 3 + layout.ipWidth + 8 + 7 + 14 + 4
	if layout.showTree {
		base += 4 + 1 // tree column + its separator
	}
	layout.showSpark = true

	budget := w - base
	if budget < 0 {
		budget = 0
	}

	// tryAdd attempts to allocate 'cost' chars from the budget.
	// Cost should include +1 for the separator that joins.Join adds.
	// Returns true if there was room.
	tryAdd := func(cost int) bool {
		if budget >= cost {
			budget -= cost
			return true
		}
		return false
	}

	hasASN := m.enricher != nil
	asnShortW := m.asnColumnWidth(false) // sized to longest visible AS number
	asnFullW := m.asnColumnWidth(true)   // sized to longest "AS1234 (ORG)"

	// Each view defines its priority order. Columns are added greedily
	// until the budget runs out. New columns cost width+1 (for the join
	// separator). Growing an existing column costs just the delta.

	switch m.viewMode {
	case ViewHealth:
		// Priority: Hostname, Delta, Trend, ASN(short), host grow, host grow, ASN(full)
		if tryAdd(15 + 1) {
			layout.showHostname = true
			layout.hostnameWidth = 15
		}
		if tryAdd(7 + 1) {
			layout.showDelta = true
		}
		if tryAdd(trendWidth + 1) {
			layout.showTrend = true
		}
		if hasASN && tryAdd(asnShortW+1) {
			layout.showASN = true
			layout.asnWidth = asnShortW
		}
		if layout.showHostname && tryAdd(10) {
			layout.hostnameWidth += 10
		}
		if layout.showHostname && tryAdd(10) {
			layout.hostnameWidth += 10
		}
		if layout.showASN && asnFullW > asnShortW && tryAdd(asnFullW-asnShortW) {
			layout.asnFull = true
			layout.asnWidth = asnFullW
		}

	case ViewLatency:
		// Priority: Hostname, Delta, Best, Wrst, ASN(short), Last, GMean, host grow, host grow, ASN(full)
		if tryAdd(15 + 1) {
			layout.showHostname = true
			layout.hostnameWidth = 15
		}
		if tryAdd(7 + 1) {
			layout.showDelta = true
		}
		if tryAdd(7 + 1) {
			layout.showBest = true
		}
		if tryAdd(7 + 1) {
			layout.showWrst = true
		}
		if hasASN && tryAdd(asnShortW+1) {
			layout.showASN = true
			layout.asnWidth = asnShortW
		}
		if tryAdd(7 + 1) {
			layout.showLast = true
		}
		if tryAdd(7 + 1) {
			layout.showGMean = true
		}
		if layout.showHostname && tryAdd(10) {
			layout.hostnameWidth += 10
		}
		if layout.showHostname && tryAdd(10) {
			layout.hostnameWidth += 10
		}
		if layout.showASN && asnFullW > asnShortW && tryAdd(asnFullW-asnShortW) {
			layout.asnFull = true
			layout.asnWidth = asnFullW
		}

	case ViewVariability:
		// Priority: Hostname, StDev, Jttr, Javg, Trend, ASN(short), host grow, host grow, ASN(full)
		if tryAdd(15 + 1) {
			layout.showHostname = true
			layout.hostnameWidth = 15
		}
		if tryAdd(7 + 1) {
			layout.showStDev = true
		}
		if tryAdd(7 + 1) {
			layout.showJttr = true
		}
		if tryAdd(7 + 1) {
			layout.showJavg = true
		}
		if tryAdd(trendWidth + 1) {
			layout.showTrend = true
		}
		if hasASN && tryAdd(asnShortW+1) {
			layout.showASN = true
			layout.asnWidth = asnShortW
		}
		if layout.showHostname && tryAdd(10) {
			layout.hostnameWidth += 10
		}
		if layout.showHostname && tryAdd(10) {
			layout.hostnameWidth += 10
		}
		if layout.showASN && asnFullW > asnShortW && tryAdd(asnFullW-asnShortW) {
			layout.asnFull = true
			layout.asnWidth = asnFullW
		}

	default: // ViewDefault
		// Priority: Hostname, Rnd, Last, ASN(short), Best, Wrst, StDev, host grow, host grow, ASN(full)
		if tryAdd(15 + 1) {
			layout.showHostname = true
			layout.hostnameWidth = 15
		}
		if tryAdd(5 + 1) {
			layout.showRnd = true
		}
		if tryAdd(7 + 1) {
			layout.showLast = true
		}
		if hasASN && tryAdd(asnShortW+1) {
			layout.showASN = true
			layout.asnWidth = asnShortW
		}
		if tryAdd(7 + 1) {
			layout.showBest = true
		}
		if tryAdd(7 + 1) {
			layout.showWrst = true
		}
		if tryAdd(7 + 1) {
			layout.showStDev = true
		}
		if layout.showHostname && tryAdd(10) {
			layout.hostnameWidth += 10
		}
		if layout.showHostname && tryAdd(10) {
			layout.hostnameWidth += 10
		}
		if layout.showHostname && tryAdd(10) {
			layout.hostnameWidth += 10
		}
		if layout.showASN && asnFullW > asnShortW && tryAdd(asnFullW-asnShortW) {
			layout.asnFull = true
			layout.asnWidth = asnFullW
		}
	}

	// Safety valve: compute the exact rendered width and shrink if over budget.
	// This catches any accounting drift between tryAdd costs and actual column widths.
	layout.clampToWidth(w)

	return layout
}

// treeGap is the blank a row without columns of its own (a hop that did not
// answer, the "more paths" note) puts between the hop number and the IP
// column: the tree column and its separator, or nothing when the layout has
// no tree column.
func (l columnLayout) treeGap() string {
	if l.showTree {
		return "     " // 4 cells + 1 separator
	}
	return ""
}

// trendWidth is the width of the Trend column: "▲ degrading" and
// "▼ improving", its longest cells, are 11 cells.
const trendWidth = 11

// computeWidth returns the exact display width this layout will render,
// matching the logic in renderColumnHeader (parts joined with " ").
func (l *columnLayout) computeWidth() int {
	w := 3 + l.ipWidth + 8 + 7 // # + IP + Loss% + Avg (always present)
	parts := 4                  // 4 fixed parts
	if l.showTree {
		w += 4
		parts++
	}
	if l.showHostname {
		w += l.hostnameWidth
		parts++
	}
	if l.showASN {
		w += l.asnWidth
		parts++
	}
	if l.showRnd {
		w += 5
		parts++
	}
	if l.showBest {
		w += 7
		parts++
	}
	if l.showWrst {
		w += 7
		parts++
	}
	if l.showStDev {
		w += 7
		parts++
	}
	if l.showLast {
		w += 7
		parts++
	}
	if l.showDelta {
		w += 7
		parts++
	}
	if l.showGMean {
		w += 7
		parts++
	}
	if l.showJttr {
		w += 7
		parts++
	}
	if l.showJavg {
		w += 7
		parts++
	}
	if l.showSpark {
		w += 14
		parts++
	}
	if l.showTrend {
		w += trendWidth
		parts++
	}
	w += parts - 1 // separators from strings.Join(" ")
	return w
}

// clampToWidth drops optional columns (right to left) until the layout fits.
func (l *columnLayout) clampToWidth(maxWidth int) {
	// Drop order: least important first. Each iteration removes the widest
	// dispensable column. We loop until it fits or only fixed columns remain.
	for l.computeWidth() > maxWidth {
		// Try shrinking hostname first (cheap, no column removal)
		if l.showHostname && l.hostnameWidth > 10 {
			excess := l.computeWidth() - maxWidth
			shrink := excess
			if shrink > l.hostnameWidth-10 {
				shrink = l.hostnameWidth - 10
			}
			l.hostnameWidth -= shrink
			continue
		}
		// Try shrinking ASN (back to short format first, then minimum width)
		if l.showASN && l.asnWidth > 7 {
			l.asnFull = false
			excess := l.computeWidth() - maxWidth
			shrink := excess
			if shrink > l.asnWidth-7 {
				shrink = l.asnWidth - 7
			}
			l.asnWidth -= shrink
			continue
		}
		// Drop columns in reverse priority
		if l.showTrend {
			l.showTrend = false
			continue
		}
		if l.showJavg {
			l.showJavg = false
			continue
		}
		if l.showJttr {
			l.showJttr = false
			continue
		}
		if l.showGMean {
			l.showGMean = false
			continue
		}
		if l.showDelta {
			l.showDelta = false
			continue
		}
		if l.showStDev {
			l.showStDev = false
			continue
		}
		if l.showWrst {
			l.showWrst = false
			continue
		}
		if l.showBest {
			l.showBest = false
			continue
		}
		if l.showLast {
			l.showLast = false
			continue
		}
		if l.showRnd {
			l.showRnd = false
			continue
		}
		if l.showASN {
			l.showASN = false
			l.asnWidth = 0
			continue
		}
		if l.showHostname {
			l.showHostname = false
			l.hostnameWidth = 0
			continue
		}
		if l.showSpark {
			l.showSpark = false
			continue
		}
		// Only the fixed columns remain (#, tree, IP, Loss%, Avg). With a
		// 39-character IPv6 address they need 65 columns. Give up the blank
		// tree column first, then shorten the address column; addresses that
		// no longer fit are cut in the middle so both ends stay visible.
		if l.showTree && l.treeOptional {
			l.showTree = false
			continue
		}
		if l.ipWidth > minIPColWidth {
			shrink := l.computeWidth() - maxWidth
			if shrink > l.ipWidth-minIPColWidth {
				shrink = l.ipWidth - minIPColWidth
			}
			l.ipWidth -= shrink
			continue
		}
		break // nothing left to give
	}
}

// minIPColWidth is the narrowest the IP column gets: any IPv4 address fits.
const minIPColWidth = 15

// renderColumnHeader builds the column header line based on the current layout.
func (m Model) renderColumnHeader(layout columnLayout) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("%-3s", "#"))
	if layout.showTree {
		parts = append(parts, "    ") // 4 chars for tree connector column
	}
	parts = append(parts, fmt.Sprintf("%-*s", layout.ipWidth, "IP"))
	if layout.showHostname {
		parts = append(parts, fmt.Sprintf("%-*s", layout.hostnameWidth, "Hostname"))
	}
	if layout.showASN {
		parts = append(parts, fmt.Sprintf("%-*s", layout.asnWidth, "ASN"))
	}
	parts = append(parts, fmt.Sprintf("%-8s", "Loss%"))
	if layout.showRnd {
		parts = append(parts, fmt.Sprintf("%-5s", "Rnd"))
	}
	parts = append(parts, fmt.Sprintf("%-7s", "Avg"))
	if layout.showBest {
		parts = append(parts, fmt.Sprintf("%-7s", "Best"))
	}
	if layout.showWrst {
		parts = append(parts, fmt.Sprintf("%-7s", "Wrst"))
	}
	if layout.showStDev {
		parts = append(parts, fmt.Sprintf("%-7s", "StDev"))
	}
	if layout.showLast {
		parts = append(parts, fmt.Sprintf("%-7s", "Last"))
	}
	if layout.showDelta {
		parts = append(parts, fmt.Sprintf("%-7s", "Delta"))
	}
	if layout.showGMean {
		parts = append(parts, fmt.Sprintf("%-7s", "GMean"))
	}
	if layout.showJttr {
		parts = append(parts, fmt.Sprintf("%-7s", "Jttr"))
	}
	if layout.showJavg {
		parts = append(parts, fmt.Sprintf("%-7s", "Javg"))
	}
	if layout.showSpark {
		parts = append(parts, fmt.Sprintf("%-14s", "Spark"))
	}
	if layout.showTrend {
		parts = append(parts, fmt.Sprintf("%-*s", trendWidth, "Trend"))
	}
	return strings.Join(parts, " ")
}

// computeDeltas returns hop-to-hop latency delta for each TTL.
func computeDeltas(hopMap map[int]*hop.Hop, maxTTL int) []time.Duration {
	deltas := make([]time.Duration, maxTTL+1)
	var prevAvg time.Duration
	hasPrev := false
	for i := 1; i <= maxTTL; i++ {
		h, ok := hopMap[i]
		if !ok || h == nil {
			continue
		}
		pn := h.PrimaryNode()
		if pn == nil || pn.GetReceived() == 0 {
			continue
		}
		avg := pn.AvgRTT()
		if !hasPrev {
			deltas[i] = avg
			prevAvg = avg
			hasPrev = true
		} else {
			deltas[i] = avg - prevAvg
			prevAvg = avg
		}
	}
	return deltas
}

// largestDeltaTTL returns the TTL with the largest positive delta.
func largestDeltaTTL(deltas []time.Duration) int {
	maxD := time.Duration(0)
	maxTTL := 0
	for i := 1; i < len(deltas); i++ {
		if deltas[i] > maxD {
			maxD = deltas[i]
			maxTTL = i
		}
	}
	return maxTTL
}

func (m Model) renderHop(h *hop.Hop, deltas []time.Duration, maxDeltaTTL int) string {
	layout := m.getLayout()
	ttlStr := fmt.Sprintf("%-3d", h.TTL)

	// If no IP, this is a non-responding hop
	ip := h.GetIP()
	if ip == nil {
		star := starStyle().Render("*")
		return fmt.Sprintf("%s %s", dimStyle().Render(ttlStr), star)
	}

	var parts []string
	parts = append(parts, dimStyle().Render(ttlStr))
	if layout.showTree {
		parts = append(parts, "    ") // empty tree connector column
	}
	parts = append(parts, ipStyle().Render(fmt.Sprintf("%-*s", layout.ipWidth, truncateAddr(ip.String(), layout.ipWidth))))

	// Hostname
	if layout.showHostname {
		hostname := h.GetHostname()
		if hostname == "" {
			if name, ok := m.resolver.Lookup(ip); ok && name != "" {
				hostname = name
			}
		}
		if len(hostname) > layout.hostnameWidth {
			hostname = hostname[:layout.hostnameWidth]
		}
		if hostname == "" {
			hostname = "..."
		}
		parts = append(parts, hostStyle().Render(fmt.Sprintf("%-*s", layout.hostnameWidth, hostname)))
	}

	if layout.showASN {
		parts = append(parts, m.renderASNCell(ip, m.prevASNForTTL(h.TTL), layout.asnWidth, layout.asnFull))
	}

	pn := h.PrimaryNode()

	parts = append(parts, lossCell(h))

	// RTT stats are hop-level, taken from the TTL-limited probes. Echo
	// replies are kept in the node's EchoStats and do not feed them.
	if layout.showRnd {
		parts = append(parts, fmt.Sprintf("%-5d", h.Rounds()))
	}
	parts = append(parts, fmt.Sprintf("%-7s", formatDuration(h.AvgRTT())))
	if layout.showBest {
		parts = append(parts, fmt.Sprintf("%-7s", formatDuration(h.GetMinRTT())))
	}
	if layout.showWrst {
		parts = append(parts, fmt.Sprintf("%-7s", formatDuration(h.GetMaxRTT())))
	}
	if layout.showStDev {
		stdev := h.StDev()
		if stdev == 0 {
			parts = append(parts, fmt.Sprintf("%-7s", "-"))
		} else {
			parts = append(parts, fmt.Sprintf("%-7s", fmt.Sprintf("%.1f", stdev)))
		}
	}
	if layout.showLast {
		parts = append(parts, fmt.Sprintf("%-7s", formatDuration(h.GetLastRTT())))
	}

	// New columns from M4 (reuse the primary node computed above for the loss cell).

	if layout.showDelta {
		if h.TTL < len(deltas) && deltas[h.TTL] != 0 {
			d := deltas[h.TTL]
			deltaStr := fmt.Sprintf("%-7s", formatDelta(d))
			if h.TTL == maxDeltaTTL {
				parts = append(parts, amberStyle().Render(deltaStr))
			} else {
				parts = append(parts, deltaStr)
			}
		} else {
			parts = append(parts, fmt.Sprintf("%-7s", "-"))
		}
	}
	if layout.showGMean {
		if pn != nil {
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(pn.GeoMean())))
		} else {
			parts = append(parts, fmt.Sprintf("%-7s", "-"))
		}
	}
	if layout.showJttr {
		if pn != nil {
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(pn.Jitter())))
		} else {
			parts = append(parts, fmt.Sprintf("%-7s", "-"))
		}
	}
	if layout.showJavg {
		if pn != nil {
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(pn.JitterMean())))
		} else {
			parts = append(parts, fmt.Sprintf("%-7s", "-"))
		}
	}
	if layout.showSpark {
		if pn != nil {
			parts = append(parts, renderSparkline(pn.SparklineData(), 14))
		} else {
			parts = append(parts, renderSparkline(nil, 14))
		}
	}
	if layout.showTrend {
		if pn != nil {
			trend := pn.Trend()
			switch trend {
			case "degrading":
				parts = append(parts, trendDegStyle().Render(fmt.Sprintf("%-*s", trendWidth, "▲ "+trend)))
			case "improving":
				parts = append(parts, trendImpStyle().Render(fmt.Sprintf("%-*s", trendWidth, "▼ "+trend)))
			default:
				parts = append(parts, dimStyle().Render(fmt.Sprintf("%-*s", trendWidth, "— stable")))
			}
		} else {
			parts = append(parts, fmt.Sprintf("%-*s", trendWidth, "-"))
		}
	}

	return strings.Join(parts, " ")
}

func (m Model) renderDivergentHop(h *hop.Hop, deltas []time.Duration, maxDeltaTTL int, maxNodes int) []string {
	layout := m.getLayout()
	nodes := h.GetNodes()

	// Sort by received count descending (primary path first)
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].GetReceived() > nodes[j].GetReceived()
	})

	displayNodes := nodes
	overflow := 0
	if maxNodes > 0 && len(nodes) > maxNodes {
		displayNodes = nodes[:maxNodes]
		overflow = len(nodes) - maxNodes
	}

	var lines []string
	for i, node := range displayNodes {
		ip := node.GetIP()
		if ip == nil {
			continue
		}

		// TTL number: only on the first line, in flowStyle (blue)
		ttlStr := "   "
		if i == 0 {
			ttlStr = flowStyle().Render(fmt.Sprintf("%-3d", h.TTL))
		}

		// Tree connector as its own column (4 display chars)
		isLast := i == len(displayNodes)-1 && overflow == 0
		connector := "├── "
		if isLast {
			connector = "└── "
		}

		var parts []string
		parts = append(parts, ttlStr)
		parts = append(parts, treeStyle().Render(connector))
		parts = append(parts, ipStyle().Render(fmt.Sprintf("%-*s", layout.ipWidth, truncateAddr(ip.String(), layout.ipWidth))))

		// Hostname
		if layout.showHostname {
			hostname := node.GetHostname()
			if hostname == "" {
				if name, ok := m.resolver.Lookup(ip); ok && name != "" {
					hostname = name
				}
			}
			if len(hostname) > layout.hostnameWidth {
				hostname = hostname[:layout.hostnameWidth]
			}
			if hostname == "" {
				hostname = "..."
			}
			parts = append(parts, hostStyle().Render(fmt.Sprintf("%-*s", layout.hostnameWidth, hostname)))
		}

		if layout.showASN {
			if i == 0 {
				parts = append(parts, m.renderASNCell(ip, m.prevASNForTTL(h.TTL), layout.asnWidth, layout.asnFull))
			} else {
				parts = append(parts, fmt.Sprintf("%-*s", layout.asnWidth, ""))
			}
		}

		// Loss cell (first node only): the hop's figure.
		if i == 0 {
			parts = append(parts, lossCell(h))
		} else {
			parts = append(parts, dimStyle().Render(fmt.Sprintf("%-8s", "-")))
		}

		// Sent count and RTT stats — hop/node-level only; echo tier is folded
		// into PathNode stats and needs no separate branch.
		if layout.showRnd {
			if i == 0 {
				parts = append(parts, fmt.Sprintf("%-5d", h.Rounds()))
			} else {
				parts = append(parts, fmt.Sprintf("%-5s", "-"))
			}
		}
		parts = append(parts, fmt.Sprintf("%-7s", formatDuration(node.AvgRTT())))
		if layout.showBest {
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(node.GetMinRTT())))
		}
		if layout.showWrst {
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(node.GetMaxRTT())))
		}
		if layout.showStDev {
			stdev := node.StDev()
			if stdev == 0 {
				parts = append(parts, fmt.Sprintf("%-7s", "-"))
			} else {
				parts = append(parts, fmt.Sprintf("%-7s", fmt.Sprintf("%.1f", stdev)))
			}
		}
		if layout.showLast {
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(node.GetLastRTT())))
		}

		// New columns from M4
		if layout.showDelta {
			if i == 0 && h.TTL < len(deltas) && deltas[h.TTL] != 0 {
				d := deltas[h.TTL]
				deltaStr := fmt.Sprintf("%-7s", formatDelta(d))
				if h.TTL == maxDeltaTTL {
					parts = append(parts, amberStyle().Render(deltaStr))
				} else {
					parts = append(parts, deltaStr)
				}
			} else {
				parts = append(parts, fmt.Sprintf("%-7s", "-"))
			}
		}
		if layout.showGMean {
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(node.GeoMean())))
		}
		if layout.showJttr {
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(node.Jitter())))
		}
		if layout.showJavg {
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(node.JitterMean())))
		}
		if layout.showSpark {
			parts = append(parts, renderSparkline(node.SparklineData(), 14))
		}
		if layout.showTrend {
			if i == 0 {
				trend := node.Trend()
				switch trend {
				case "degrading":
					parts = append(parts, trendDegStyle().Render(fmt.Sprintf("%-*s", trendWidth, "▲ "+trend)))
				case "improving":
					parts = append(parts, trendImpStyle().Render(fmt.Sprintf("%-*s", trendWidth, "▼ "+trend)))
				default:
					parts = append(parts, dimStyle().Render(fmt.Sprintf("%-*s", trendWidth, "— stable")))
				}
			} else {
				parts = append(parts, fmt.Sprintf("%-*s", trendWidth, ""))
			}
		}

		lines = append(lines, strings.Join(parts, " "))
	}

	if overflow > 0 {
		// The note starts in the IP column, under the addresses it stands for.
		lines = append(lines, fmt.Sprintf("    %s%s", layout.treeGap(),
			stabStyle().Render(fmt.Sprintf("... +%d more paths", overflow))))
	}

	return lines
}

// prevASNForTTL returns the ASN of the previous visible hop (skipping unknown).
func (m Model) prevASNForTTL(ttl int) int {
	if m.enricher == nil {
		return 0
	}
	s := m.focused()
	hops := s.table.Snapshot()
	hopMap := make(map[int]*hop.Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}
	for prev := ttl - 1; prev >= 1; prev-- {
		h, ok := hopMap[prev]
		if !ok {
			continue
		}
		ip := h.GetIP()
		if ip == nil {
			continue
		}
		if info, ok := m.enricher.Lookup(ip); ok && info.Number != 0 {
			return info.Number
		}
	}
	return 0
}

// renderASNCell formats the ASN column with boundary highlighting.
// When full is false, only the AS number is shown (e.g. "AS13335").
// When full is true, the org name is included (e.g. "AS13335 (CLOUDFLARENET)").
func (m Model) renderASNCell(ip net.IP, prevASN int, width int, full bool) string {
	if m.enricher == nil || ip == nil {
		return fmt.Sprintf("%-*s", width, "")
	}
	info, ok := m.enricher.Lookup(ip)
	if !ok {
		return dimStyle().Render(fmt.Sprintf("%-*s", width, "..."))
	}
	var label string
	if full {
		label = asn.FormatASN(info.Number, info.Org)
	} else {
		label = asn.FormatASNShort(info.Number)
	}
	if label == "" {
		return fmt.Sprintf("%-*s", width, "")
	}
	if len(label) > width {
		label = label[:width]
	}
	formatted := fmt.Sprintf("%-*s", width, label)
	if info.Number != 0 && info.Number != prevASN {
		return asnBoundaryStyle().Render(formatted)
	}
	return asnStyle().Render(formatted)
}

func formatDelta(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	prefix := "+"
	if d < 0 {
		prefix = "-"
		d = -d
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%s%.0fµs", prefix, float64(d.Microseconds()))
	}
	return fmt.Sprintf("%s%.1f", prefix, float64(d.Microseconds())/1000.0)
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.0fµs", float64(d.Microseconds()))
	}
	return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000.0)
}

// computeIPColWidth returns the max IP-string length across all hops,
// clamped to [15, 39]. v4-only traces stay compact; v6 widens as needed.
func computeIPColWidth(hops []*hop.Hop) int {
	w := 15
	for _, h := range hops {
		for _, n := range h.Nodes {
			if l := len(n.IP.String()); l > w {
				w = l
			}
		}
	}
	if w > 39 {
		w = 39
	}
	return w
}

// FinalSummary returns the plain-text summary printed to stdout after the TUI
// exits: one table per target, in session order, separated by a blank line. A
// target that failed to start gets its error line.
func (m Model) FinalSummary() string {
	parts := make([]string, 0, len(m.sessions))
	for _, s := range m.sessions {
		parts = append(parts, m.finalSummaryFor(s))
	}
	return strings.Join(parts, "\n")
}

// finalSummaryFor returns the summary table for one target.
func (m Model) finalSummaryFor(s *TargetSession) string {
	var b strings.Builder

	// Guard: if the session failed to resolve (errored spec), emit a short error
	// line and return early. Avoids nil-dereference on s.targetIP.String().
	if s.startErr != nil {
		b.WriteString(fmt.Sprintf("via — %s — error: %v\n", s.target, s.startErr))
		return b.String()
	}

	multipath := s.probeCfg.NumPaths > 1

	// Header
	protoLabel := strings.ToUpper(s.protocolName)
	if s.protocolName == "auto" {
		protoLabel = "Auto → " + strings.ToUpper(s.probeCfg.Protocol.Name())
	}
	if s.protocolName != "icmp" && s.probeCfg.NumPaths > 1 {
		protoLabel += "/ECMP"
	}
	targetIPStr := "(unresolved)"
	if s.targetIP != nil {
		targetIPStr = s.targetIP.String()
	}
	if multipath {
		b.WriteString(fmt.Sprintf("via — %s (%s) — %s — %d flows\n", s.target, targetIPStr, protoLabel, s.probeCfg.NumPaths))
	} else {
		b.WriteString(fmt.Sprintf("via — %s (%s) — %s\n", s.target, targetIPStr, protoLabel))
	}

	// Determine max TTL to display
	maxTTL := s.table.MaxTTLSeen()
	if s.maxTTLHit > 0 && s.maxTTLHit < maxTTL {
		maxTTL = s.maxTTLHit
	}

	// Hop rows
	hops := s.table.Snapshot()
	hopMap := make(map[int]*hop.Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}

	// Dynamic IP column width: widens for IPv6, stays compact for IPv4-only traces.
	ipColWidth := computeIPColWidth(hops)
	ipFmt := fmt.Sprintf("%%-%ds", ipColWidth)

	// Column headers
	if multipath {
		b.WriteString(fmt.Sprintf("%-3s "+ipFmt+" %-22s %-20s %-8s %-5s %-7s %-7s %-7s %-7s %-7s %-8s %-5s\n",
			"#", "IP", "Hostname", "ASN", "Loss%", "Rnd", "Avg", "Best", "Wrst", "StDev", "Last", "Flows", "Stab"))
	} else {
		b.WriteString(fmt.Sprintf("%-3s "+ipFmt+" %-22s %-20s %-8s %-5s %-7s %-7s %-7s %-7s %-7s\n",
			"#", "IP", "Hostname", "ASN", "Loss%", "Rnd", "Avg", "Best", "Wrst", "StDev", "Last"))
	}

	prevASN := 0

	for ttl := 1; ttl <= maxTTL; ttl++ {
		h, ok := hopMap[ttl]
		if !ok || h.GetIP() == nil {
			b.WriteString(fmt.Sprintf("%-3d *\n", ttl))
			continue
		}

		// One figure for the hop, the same one the live table shows.
		hopLossStr := hop.FormatLoss(h.Loss())

		if multipath && h.IsDivergent() {
			nodes := h.GetNodes()
			sort.Slice(nodes, func(i, j int) bool {
				return nodes[i].GetReceived() > nodes[j].GetReceived()
			})
			sent := h.Rounds()
			nodeIdx := 0
			for _, node := range nodes {
				ip := node.GetIP()
				if ip == nil {
					continue
				}
				hostname := node.GetHostname()
				if hostname == "" {
					if name, found := m.resolver.Lookup(ip); found && name != "" {
						hostname = name
					}
				}
				if len(hostname) > 20 {
					hostname = hostname[:20]
				}

				asnLabel := ""
				currentASN := 0
				if m.enricher != nil {
					if info, ok := m.enricher.Lookup(ip); ok {
						currentASN = info.Number
						asnLabel = asn.FormatASN(info.Number, info.Org)
					}
				}
				if len(asnLabel) > 20 {
					asnLabel = asnLabel[:20]
				}

				lossStr := "-"
				sntStr := "-"
				if nodeIdx == 0 {
					lossStr = hopLossStr
					sntStr = fmt.Sprintf("%d", sent)
				}

				avg := float64(node.AvgRTT().Microseconds()) / 1000.0
				minRTT := float64(node.GetMinRTT().Microseconds()) / 1000.0
				maxRTT := float64(node.GetMaxRTT().Microseconds()) / 1000.0
				stdev := node.StDev()
				last := float64(node.GetLastRTT().Microseconds()) / 1000.0
				flows := hop.FormatFlowIDs(node.GetFlowIDs())
				stab := fmt.Sprintf("%.0f%%", node.StabilityPercent())

				b.WriteString(fmt.Sprintf("%-3d "+ipFmt+" %-22s %-20s %-8s %-5s %-7s %-7s %-7s %-7s %-7s %-8s %-5s\n",
					ttl,
					ip.String(),
					hostname,
					asnLabel,
					lossStr,
					sntStr,
					fmt.Sprintf("%.1f", avg),
					fmt.Sprintf("%.1f", minRTT),
					fmt.Sprintf("%.1f", maxRTT),
					fmt.Sprintf("%.1f", stdev),
					fmt.Sprintf("%.1f", last),
					flows,
					stab,
				))
				if currentASN != 0 {
					prevASN = currentASN
				}
				nodeIdx++
			}
		} else {
			ip := h.GetIP()
			hostname := h.GetHostname()
			if hostname == "" {
				if name, found := m.resolver.Lookup(ip); found && name != "" {
					hostname = name
				}
			}
			if len(hostname) > 20 {
				hostname = hostname[:20]
			}

			asnLabel := ""
			currentASN := 0
			if m.enricher != nil {
				if info, ok := m.enricher.Lookup(ip); ok {
					currentASN = info.Number
					asnLabel = asn.FormatASN(info.Number, info.Org)
				}
			}
			if len(asnLabel) > 20 {
				asnLabel = asnLabel[:20]
			}

			lossStr := hopLossStr
			sent := h.Rounds()
			avg := float64(h.AvgRTT().Microseconds()) / 1000.0
			minRTT := float64(h.GetMinRTT().Microseconds()) / 1000.0
			maxRTT := float64(h.GetMaxRTT().Microseconds()) / 1000.0
			stdev := h.StDev()
			last := float64(h.GetLastRTT().Microseconds()) / 1000.0

			if multipath {
				nodes := h.GetNodes()
				flows := "-"
				stab := "-"
				if len(nodes) == 1 {
					flows = hop.FormatFlowIDs(nodes[0].GetFlowIDs())
					stab = fmt.Sprintf("%.0f%%", nodes[0].StabilityPercent())
				}
				b.WriteString(fmt.Sprintf("%-3d "+ipFmt+" %-22s %-20s %-8s %-5d %-7s %-7s %-7s %-7s %-7s %-8s %-5s\n",
					ttl,
					ip.String(),
					hostname,
					asnLabel,
					lossStr,
					sent,
					fmt.Sprintf("%.1f", avg),
					fmt.Sprintf("%.1f", minRTT),
					fmt.Sprintf("%.1f", maxRTT),
					fmt.Sprintf("%.1f", stdev),
					fmt.Sprintf("%.1f", last),
					flows,
					stab,
				))
			} else {
				b.WriteString(fmt.Sprintf("%-3d "+ipFmt+" %-22s %-20s %-8s %-5d %-7s %-7s %-7s %-7s %-7s\n",
					ttl,
					ip.String(),
					hostname,
					asnLabel,
					lossStr,
					sent,
					fmt.Sprintf("%.1f", avg),
					fmt.Sprintf("%.1f", minRTT),
					fmt.Sprintf("%.1f", maxRTT),
					fmt.Sprintf("%.1f", stdev),
					fmt.Sprintf("%.1f", last),
				))
			}
			if currentASN != 0 {
				prevASN = currentASN
			}
		}
	}
	_ = prevASN // FinalSummary is plain text, no boundary highlighting

	return b.String()
}

// maxScrollOffset returns the maximum valid scroll offset for the current state.
// fixedLineCount is the number of lines the default view spends on everything
// but hop rows: title, separator, column header and status bar, plus the
// summary panel when several targets are traced.
func (m Model) fixedLineCount() int {
	n := 4
	if len(m.sessions) > 1 && !m.columnView {
		n += m.summaryLineCount()
	}
	return n
}

// hopRowsAvailable is the number of hop rows the default view can show. View
// and the scroll limit both use it: when they disagreed (the scroll limit
// ignored the summary panel) the first hops of a long path could not be
// scrolled to.
func (m Model) hopRowsAvailable() int {
	if fixed := m.fixedLineCount(); m.height > fixed {
		return m.height - fixed
	}
	return 0
}

func (m Model) maxScrollOffset() int {
	totalLines := m.countHopLines()
	availableRows := m.hopRowsAvailable()
	maxOff := totalLines - availableRows
	if maxOff < 0 {
		maxOff = 0
	}
	return maxOff
}

// countHopLines returns the total number of display lines for all hops.
func (m Model) countHopLines() int {
	s := m.focused()
	hops := s.table.Snapshot()
	hopMap := make(map[int]*hop.Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}
	maxTTL := s.table.MaxTTLSeen()
	if s.maxTTLHit > 0 && s.maxTTLHit < maxTTL {
		maxTTL = s.maxTTLHit
	}
	count := 0
	for ttl := 1; ttl <= maxTTL; ttl++ {
		h, ok := hopMap[ttl]
		if !ok || h.GetIP() == nil {
			count++
			continue
		}
		if h.IsDivergent() {
			count += len(h.GetNodes())
		} else {
			count++
		}
	}
	return count
}

// hostOS is the operating system the error view's advice is written for. A
// variable so tests can pin it: the golden files are made for "linux" and
// must come out the same on every platform the tests run on.
var hostOS = runtime.GOOS

func (m Model) errorView() string {
	lines := []string{
		lossStyle().Render("Error: " + m.err.Error()),
		"",
		"This tool requires raw socket permissions.",
		"",
	}
	if hostOS == "linux" {
		// File capabilities (setcap) exist on Linux only.
		lines = append(lines,
			"Options:",
			"  1. Run with sudo:  sudo via <target>",
			"  2. Set capability: sudo setcap cap_net_raw+ep $(which via)",
		)
	} else {
		lines = append(lines, "Run with sudo:  sudo via <target>")
	}
	lines = append(lines, "", "Press q to quit.")
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(lineBg(line, m.width))
		b.WriteString("\n")
	}
	// Fill remaining height
	emptyLine := lineBg("", m.width)
	remaining := m.height - len(lines)
	for i := 0; i < remaining; i++ {
		b.WriteString(emptyLine)
		b.WriteString("\n")
	}
	return b.String()
}

// exportSessionSnapshot holds a data-race-safe snapshot of a single session's
// scalar fields. The snapshot is captured synchronously in exportCmd before the
// closure is returned; the closure then reads only these locals, never the live
// *TargetSession pointer.
type exportSessionSnapshot struct {
	target       string
	targetIP     net.IP
	protocolName string
	probeCount   int
	maxTTLHit    int
	table        *hop.Table
}

func (m Model) exportCmd() tea.Cmd {
	// Snapshot all scalar fields from every session before the closure is
	// returned. The closure runs asynchronously as a tea.Cmd goroutine while
	// the update loop may concurrently mutate these fields — capturing live
	// *TargetSession pointers and reading through them inside the goroutine
	// is a data race. m.version, m.resolver, m.enricher are on the Model
	// value copy and are safe as-is.
	if len(m.sessions) > 1 {
		// Multi-target path: build a MultiReport from all non-errored sessions.
		snaps := make([]exportSessionSnapshot, 0, len(m.sessions))
		for _, s := range m.sessions {
			if s.startErr != nil {
				continue
			}
			snaps = append(snaps, exportSessionSnapshot{
				target:       s.target,
				targetIP:     s.targetIP,
				protocolName: s.protocolName,
				probeCount:   s.probeCount,
				maxTTLHit:    s.maxTTLHit,
				table:        s.table,
			})
		}
		if len(snaps) == 0 {
			return func() tea.Msg {
				return ExportDoneMsg{Err: fmt.Errorf("no active sessions to export")}
			}
		}
		return func() tea.Msg {
			now := time.Now()
			mr := export.MultiReport{
				Version:   m.version,
				Timestamp: now,
				Targets:   make([]export.Report, 0, len(snaps)),
			}
			for _, snap := range snaps {
				maxTTL := snap.table.MaxTTLSeen()
				if snap.maxTTLHit > 0 && snap.maxTTLHit < maxTTL {
					maxTTL = snap.maxTTLHit
				}
				mr.Targets = append(mr.Targets, export.BuildReport(
					m.version, snap.target, snap.targetIP,
					snap.protocolName, snap.table,
					m.resolver, m.enricher, maxTTL,
				))
			}

			format := "json"
			if m.config != nil && m.config.ExportFormat != "" {
				format = m.config.ExportFormat
			}

			ts := now.Format("20060102-1504")
			filename := fmt.Sprintf("via-multi-%s.%s", ts, format)

			f, err := os.Create(filename)
			if err != nil {
				return ExportDoneMsg{Err: err}
			}
			defer f.Close()

			switch format {
			case "json":
				err = export.WriteJSONMulti(f, mr)
			case "csv":
				err = export.WriteCSVMulti(f, mr)
			case "dot":
				err = export.WriteDOTMulti(f, mr)
			}
			return ExportDoneMsg{Path: filename, Err: err}
		}
	}

	// Single-target path: unchanged from original implementation.
	s := m.focused()
	target := s.target
	targetIP := s.targetIP
	protocolName := s.protocolName
	maxTTLHit := s.maxTTLHit
	table := s.table
	return func() tea.Msg {
		maxTTL := table.MaxTTLSeen()
		if maxTTLHit > 0 && maxTTLHit < maxTTL {
			maxTTL = maxTTLHit
		}
		report := export.BuildReport(m.version, target, targetIP,
			protocolName, table, m.resolver, m.enricher, maxTTL)

		format := "json"
		if m.config != nil && m.config.ExportFormat != "" {
			format = m.config.ExportFormat
		}

		ts := time.Now().Format("20060102-1504")
		filename := fmt.Sprintf("via-%s-%s.%s", target, ts, format)

		f, err := os.Create(filename)
		if err != nil {
			return ExportDoneMsg{Err: err}
		}
		defer f.Close()

		switch format {
		case "json":
			err = export.WriteJSON(f, report)
		case "csv":
			err = export.WriteCSV(f, report)
		case "dot":
			err = export.WriteDOT(f, report)
		}
		return ExportDoneMsg{Path: filename, Err: err}
	}
}
