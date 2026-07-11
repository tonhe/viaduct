// Package tui implements the terminal UI using bubbletea.
package tui

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tonhe/viaduct/internal/asn"
	"github.com/tonhe/viaduct/internal/config"
	"github.com/tonhe/viaduct/internal/export"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/ping"
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
func pingBadgeStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base0C)
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

// Model is the bubbletea model for the TUI.
type Model struct {
	target     string
	targetIP   net.IP
	version    string
	table      *hop.Table
	resolver   *resolve.Resolver
	enricher   *asn.Enricher
	probeCfg   probe.Config
	probeCount int
	startTime  time.Time
	err        error
	width      int
	height     int
	targetHit    bool
	maxTTLHit    int
	paused       bool
	dnsEnabled   bool
	viewMode     ViewMode
	tracer           *probe.Tracer
	scrollOffset     int       // number of lines scrolled from bottom (0 = bottom)
	autoScroll       bool      // true = follow latest data
	protocolName     string    // "icmp", "udp", "tcp", "auto"
	switchStatusMsg  string    // transient status for auto mode switch
	switchStatusTime time.Time // when switch status was set
	pingStats        map[string]*ping.Stat
	alertEngine      *AlertEngine
	config           *config.Config  // reference to persisted config
	settings         *SettingsModel  // nil when settings closed
	showSettings     bool
	exportStatusMsg  string
	exportStatusTime time.Time
	nowFunc          func() time.Time // for test determinism; nil defaults to time.Now
}

// now returns the current time, using the injected nowFunc if set,
// otherwise time.Now.
func (m Model) now() time.Time {
	if m.nowFunc != nil {
		return m.nowFunc()
	}
	return time.Now()
}

// New creates a new TUI model.
func New(target string, targetIP net.IP, cfg probe.Config, version string, protocolName string, noASN bool, noPing bool, alertLoss float64, alertLatency time.Duration, alertRounds int, noAlert bool, appCfg *config.Config) Model {
	var ae *AlertEngine
	if !noAlert {
		ae = NewAlertEngine(alertLoss, alertLatency, alertRounds)
	}
	return Model{
		target:       target,
		targetIP:     targetIP,
		version:      version,
		table:        hop.NewTable(cfg.MaxHops),
		resolver: resolve.New(4),
		enricher: func() *asn.Enricher {
			if noASN {
				return nil
			}
			return asn.New(4)
		}(),
		probeCfg: cfg,
		startTime:    time.Now(),
		dnsEnabled:   !cfg.NoDNS,
		autoScroll:   true,
		protocolName: protocolName,
		pingStats: func() map[string]*ping.Stat {
			if noPing {
				return nil
			}
			return make(map[string]*ping.Stat)
		}(),
		alertEngine: ae,
		config:      appCfg,
	}
}

// Resolver returns the resolver so main.go can submit IPs for resolution.
func (m *Model) Resolver() *resolve.Resolver {
	return m.resolver
}

// Table returns the hop table.
func (m *Model) Table() *hop.Table {
	return m.table
}

// SetTracer sets the probe tracer so the TUI can pause/unpause it.
func (m *Model) SetTracer(t *probe.Tracer) {
	m.tracer = t
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
	case PingUpdateMsg:
		return m.onPingUpdate(msg)
	case RoundEndMsg:
		return m.onRoundEnd(msg)
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

// View renders the TUI.
func (m Model) View() string {
	if m.err != nil {
		return m.errorView()
	}

	var b strings.Builder

	// Header bar
	protoLabel := strings.ToUpper(m.protocolName)
	if m.protocolName == "auto" {
		protoLabel = "Auto → " + strings.ToUpper(m.probeCfg.Protocol.Name())
	}
	if m.protocolName != "icmp" && m.probeCfg.NumPaths > 1 {
		protoLabel += "/ECMP"
	}
	header := fmt.Sprintf("via %s   Target: %s (%s)    Proto: %s    Probes: %d",
		m.version, m.target, m.targetIP.String(), protoLabel, m.probeCount)
	if m.viewMode != ViewDefault {
		header += fmt.Sprintf("    View: %s", m.viewMode.String())
	}
	b.WriteString(barBg(headerStyle().Render(header), m.width))
	b.WriteString("\n")

	// Separator
	b.WriteString(barBg(dimStyle().Render(strings.Repeat("─", max(m.width, 60))), m.width))
	b.WriteString("\n")

	// Column headers (adaptive to terminal width)
	layout := m.getLayout()
	colHeader := m.renderColumnHeader(layout)
	b.WriteString(lineBg(dimStyle().Render(colHeader), m.width))
	b.WriteString("\n")

	// Hop rows — iterate by TTL to show gap hops as *
	hops := m.table.Snapshot()
	hopMap := make(map[int]*hop.Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}
	maxTTL := m.table.MaxTTLSeen()
	if m.maxTTLHit > 0 && m.maxTTLHit < maxTTL {
		maxTTL = m.maxTTLHit
	}

	rateLimited := hop.DetectRateLimited(hops, maxTTL)

	// Compute deltas for Health/Latency views
	deltas := computeDeltas(hopMap, maxTTL)
	maxDeltaTTL := largestDeltaTTL(deltas)

	// Determine how many hop lines we can show
	availableRows := 0
	if m.height > 4 {
		availableRows = m.height - 4 // 3 header lines + 1 status line
	}

	// Compute per-divergent-hop node caps based on available vertical space.
	// Baseline: 1 line per TTL. Surplus rows are distributed top-down to
	// divergent hops, expanding each by its extra nodes until space runs out.
	type divInfo struct {
		ttl      int
		maxNodes int
		total    int // total nodes at this TTL
	}
	var divHops []divInfo
	baseline := maxTTL // 1 line per TTL
	for ttl := 1; ttl <= maxTTL; ttl++ {
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
	var hopLines []string
	for ttl := 1; ttl <= maxTTL; ttl++ {
		h, ok := hopMap[ttl]
		if !ok || h.GetIP() == nil {
			star := starStyle().Render("*")
			treeGap := ""
			if m.probeCfg.NumPaths > 1 {
				treeGap = "     " // 4 chars + 1 space separator
			}
			hopLines = append(hopLines, fmt.Sprintf("%s %s%s", dimStyle().Render(fmt.Sprintf("%-3d", ttl)), treeGap, star))
			continue
		}
		if h.IsDivergent() {
			cap := divCap[ttl]
			if cap < 1 {
				cap = 1
			}
			hopLines = append(hopLines, m.renderDivergentHop(h, rateLimited[ttl], deltas, maxDeltaTTL, cap)...)
		} else {
			hopLines = append(hopLines, m.renderHop(h, rateLimited[ttl], deltas, maxDeltaTTL))
		}
	}

	displayLines := hopLines
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

	// Alert bar (if degraded)
	alertLine := ""
	if m.alertEngine != nil {
		alertLine = m.alertEngine.Message()
	}

	// Fill remaining space with themed background
	emptyLine := lineBg("", m.width)
	displayCount := len(displayLines)
	extraLines := 0
	if alertLine != "" {
		extraLines = 1
	}
	usedLines := 3 + displayCount + extraLines + 1
	if m.height > 0 && usedLines < m.height {
		for i := 0; i < m.height-usedLines; i++ {
			b.WriteString(emptyLine)
			b.WriteString("\n")
		}
	}

	if alertLine != "" {
		b.WriteString(lineBg(alertStyle().Render(alertLine), m.width))
		b.WriteString("\n")
	}

	// Status bar
	maxTTL = m.table.MaxTTLSeen()
	elapsed := m.now().Sub(m.startTime).Truncate(time.Second)
	var traceStatus string
	if m.paused {
		traceStatus = "PAUSED"
	} else if m.targetHit {
		traceStatus = fmt.Sprintf("Tracing... %d/%d hops", maxTTL, m.maxTTLHit)
	} else {
		traceStatus = fmt.Sprintf("Tracing... %d hops", maxTTL)
	}

	dnsLabel := "on"
	if !m.dnsEnabled {
		dnsLabel = "off"
	}
	flowLabel := ""
	if m.probeCfg.NumPaths > 1 {
		flowLabel = fmt.Sprintf("    %d flows", m.probeCfg.NumPaths)
	}
	switchHint := ""
	if m.switchStatusMsg != "" && m.now().Sub(m.switchStatusTime) < 5*time.Second {
		switchHint = "    " + m.switchStatusMsg
	}
	if m.exportStatusMsg != "" && m.now().Sub(m.exportStatusTime) < 5*time.Second {
		switchHint = "    " + m.exportStatusMsg
	}
	scrollHint := ""
	if !m.autoScroll && len(hopLines) > availableRows {
		scrollHint = " [scrolled] G:bottom g:top"
	}
	status := fmt.Sprintf("%s    Elapsed: %s    DNS: %s%s%s    j/k:scroll p:pause r:reset [d] %s q:quit%s",
		traceStatus, elapsed, dnsLabel, flowLabel, switchHint, m.viewMode.Next().String(), scrollHint)
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
	showHostname  bool
	hostnameWidth int
	showSnt       bool
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
	maxLen := 0
	for _, h := range m.table.Snapshot() {
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

	// Compute IP column width based on actual hops (handles IPv6 addresses).
	hops := m.table.Snapshot()
	ipWidth := computeIPColWidth(hops)
	layout := columnLayout{ipWidth: ipWidth}

	if m.probeCfg.NumPaths > 1 {
		layout.showTree = true
	}

	// Budget-based layout: start with fixed columns (#, IP, Loss%, Avg, Spark),
	// then add optional columns in priority order only if there's remaining room.
	// This guarantees columns never overflow the terminal width.
	//
	// Column widths (from renderColumnHeader / renderHopRow):
	//   # = 3, tree = 4, IP = ipWidth, Hostname = hostnameWidth,
	//   ASN = asnWidth, Loss% = 8, Snt = 5, Avg/Best/Wrst/StDev/Last/Delta/GMean/Jttr/Javg = 7,
	//   Spark = 14, Trend = 9
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
		if tryAdd(9 + 1) {
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
		if tryAdd(9 + 1) {
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
		// Priority: Hostname, Snt, Last, ASN(short), Best, Wrst, StDev, host grow, host grow, ASN(full)
		if tryAdd(15 + 1) {
			layout.showHostname = true
			layout.hostnameWidth = 15
		}
		if tryAdd(5 + 1) {
			layout.showSnt = true
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
	if l.showSnt {
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
		w += 9
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
		if l.showSnt {
			l.showSnt = false
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
		break // only fixed columns remain
	}
}

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
	if layout.showSnt {
		parts = append(parts, fmt.Sprintf("%-5s", "Snt"))
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
		parts = append(parts, fmt.Sprintf("%-9s", "Trend"))
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

func (m Model) renderHop(h *hop.Hop, isRateLimited bool, deltas []time.Duration, maxDeltaTTL int) string {
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
	parts = append(parts, ipStyle().Render(fmt.Sprintf("%-*s", layout.ipWidth, ip.String())))

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

	// Check for ping supplement data
	var ps *ping.Stat
	if isRateLimited && m.pingStats != nil {
		ps = m.pingStats[ip.String()]
	}

	// Loss%
	if ps != nil && ps.Sent > 0 {
		loss := ps.LossPercent()
		if loss == 0 {
			parts = append(parts, okStyle().Render(fmt.Sprintf("%-8s", "0.0%")))
		} else {
			parts = append(parts, lossStyle().Render(fmt.Sprintf("%-8s", fmt.Sprintf("%.1f%%", loss))))
		}
	} else {
		loss := h.LossPercent()
		if loss == 0 {
			parts = append(parts, okStyle().Render(fmt.Sprintf("%-8s", "0.0%")))
		} else if isRateLimited {
			parts = append(parts, rateLimitStyle().Render(fmt.Sprintf("%-8s", fmt.Sprintf("~%.0f%%", loss))))
		} else {
			parts = append(parts, lossStyle().Render(fmt.Sprintf("%-8s", fmt.Sprintf("%.1f%%", loss))))
		}
	}

	// RTT stats — use ping stats when available; "†" on Snt indicates ping supplement
	if ps != nil && ps.Received > 0 {
		if layout.showSnt {
			sntNum := fmt.Sprintf("%d", ps.Sent)
			pad := 5 - len(sntNum) - 1
			if pad < 0 {
				pad = 0
			}
			parts = append(parts, sntNum+pingBadgeStyle().Render("†")+strings.Repeat(" ", pad))
		}
		parts = append(parts, fmt.Sprintf("%-7s", formatDuration(ps.AvgRTT())))
		if layout.showBest {
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(ps.MinRTT)))
		}
		if layout.showWrst {
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(ps.MaxRTT)))
		}
		if layout.showStDev {
			stdev := ps.StDev()
			if stdev == 0 {
				parts = append(parts, fmt.Sprintf("%-7s", "-"))
			} else {
				parts = append(parts, fmt.Sprintf("%-7s", fmt.Sprintf("%.1f", stdev)))
			}
		}
		if layout.showLast {
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(ps.LastRTT)))
		}
	} else {
		if layout.showSnt {
			parts = append(parts, fmt.Sprintf("%-5d", h.GetSent()))
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
	}

	// New columns from M4
	pn := h.PrimaryNode()

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
		if ps != nil && ps.Received > 0 {
			parts = append(parts, renderSparkline(ps.SparklineData(), 14))
		} else if pn != nil {
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
				parts = append(parts, trendDegStyle().Render(fmt.Sprintf("%-9s", "▲ "+trend)))
			case "improving":
				parts = append(parts, trendImpStyle().Render(fmt.Sprintf("%-9s", "▼ "+trend)))
			default:
				parts = append(parts, dimStyle().Render(fmt.Sprintf("%-9s", "— stable")))
			}
		} else {
			parts = append(parts, fmt.Sprintf("%-9s", "-"))
		}
	}

	return strings.Join(parts, " ")
}

func (m Model) renderDivergentHop(h *hop.Hop, isRateLimited bool, deltas []time.Duration, maxDeltaTTL int, maxNodes int) []string {
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
		parts = append(parts, ipStyle().Render(fmt.Sprintf("%-*s", layout.ipWidth, ip.String())))

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

		// Check for ping supplement data (first node only)
		var ps *ping.Stat
		if i == 0 && isRateLimited && m.pingStats != nil {
			ps = m.pingStats[ip.String()]
		}

		// Loss: show hop-level aggregate on first node, "-" on subsequent
		if i == 0 {
			if ps != nil && ps.Sent > 0 {
				loss := ps.LossPercent()
				if loss == 0 {
					parts = append(parts, okStyle().Render(fmt.Sprintf("%-8s", "0.0%")))
				} else {
					parts = append(parts, lossStyle().Render(fmt.Sprintf("%-8s", fmt.Sprintf("%.1f%%", loss))))
				}
			} else {
				loss := h.LossPercent()
				if loss == 0 {
					parts = append(parts, okStyle().Render(fmt.Sprintf("%-8s", "0.0%")))
				} else if isRateLimited {
					parts = append(parts, rateLimitStyle().Render(fmt.Sprintf("%-8s", fmt.Sprintf("~%.0f%%", loss))))
				} else {
					parts = append(parts, lossStyle().Render(fmt.Sprintf("%-8s", fmt.Sprintf("%.1f%%", loss))))
				}
			}
		} else {
			parts = append(parts, dimStyle().Render(fmt.Sprintf("%-8s", "-")))
		}

		// Sent count and RTT stats; "†" on Snt indicates ping supplement
		if i == 0 && ps != nil && ps.Received > 0 {
			if layout.showSnt {
				sntNum := fmt.Sprintf("%d", ps.Sent)
				pad := 5 - len(sntNum) - 1
				if pad < 0 {
					pad = 0
				}
				parts = append(parts, sntNum+pingBadgeStyle().Render("†")+strings.Repeat(" ", pad))
			}
			parts = append(parts, fmt.Sprintf("%-7s", formatDuration(ps.AvgRTT())))
			if layout.showBest {
				parts = append(parts, fmt.Sprintf("%-7s", formatDuration(ps.MinRTT)))
			}
			if layout.showWrst {
				parts = append(parts, fmt.Sprintf("%-7s", formatDuration(ps.MaxRTT)))
			}
			if layout.showStDev {
				stdev := ps.StDev()
				if stdev == 0 {
					parts = append(parts, fmt.Sprintf("%-7s", "-"))
				} else {
					parts = append(parts, fmt.Sprintf("%-7s", fmt.Sprintf("%.1f", stdev)))
				}
			}
			if layout.showLast {
				parts = append(parts, fmt.Sprintf("%-7s", formatDuration(ps.LastRTT)))
			}
		} else {
			if layout.showSnt {
				if i == 0 {
					parts = append(parts, fmt.Sprintf("%-5d", h.GetSent()))
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
			if ps != nil && ps.Received > 0 {
				parts = append(parts, renderSparkline(ps.SparklineData(), 14))
			} else {
				parts = append(parts, renderSparkline(node.SparklineData(), 14))
			}
		}
		if layout.showTrend {
			if i == 0 {
				trend := node.Trend()
				switch trend {
				case "degrading":
					parts = append(parts, trendDegStyle().Render(fmt.Sprintf("%-9s", "▲ "+trend)))
				case "improving":
					parts = append(parts, trendImpStyle().Render(fmt.Sprintf("%-9s", "▼ "+trend)))
				default:
					parts = append(parts, dimStyle().Render(fmt.Sprintf("%-9s", "— stable")))
				}
			} else {
				parts = append(parts, fmt.Sprintf("%-9s", ""))
			}
		}

		lines = append(lines, strings.Join(parts, " "))
	}

	if overflow > 0 {
		lines = append(lines, fmt.Sprintf("     %s",
			stabStyle().Render(fmt.Sprintf("... +%d more paths", overflow))))
	}

	return lines
}

// prevASNForTTL returns the ASN of the previous visible hop (skipping unknown).
func (m Model) prevASNForTTL(ttl int) int {
	if m.enricher == nil {
		return 0
	}
	hops := m.table.Snapshot()
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

// FinalSummary returns a plain-text summary table for printing to stdout after the TUI exits.
func (m Model) FinalSummary() string {
	var b strings.Builder
	multipath := m.probeCfg.NumPaths > 1

	// Header
	protoLabel := strings.ToUpper(m.protocolName)
	if m.protocolName == "auto" {
		protoLabel = "Auto → " + strings.ToUpper(m.probeCfg.Protocol.Name())
	}
	if m.protocolName != "icmp" && m.probeCfg.NumPaths > 1 {
		protoLabel += "/ECMP"
	}
	if multipath {
		b.WriteString(fmt.Sprintf("via — %s (%s) — %s — %d flows\n", m.target, m.targetIP.String(), protoLabel, m.probeCfg.NumPaths))
	} else {
		b.WriteString(fmt.Sprintf("via — %s (%s) — %s\n", m.target, m.targetIP.String(), protoLabel))
	}

	// Determine max TTL to display
	maxTTL := m.table.MaxTTLSeen()
	if m.maxTTLHit > 0 && m.maxTTLHit < maxTTL {
		maxTTL = m.maxTTLHit
	}

	// Hop rows
	hops := m.table.Snapshot()
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
			"#", "IP", "Hostname", "ASN", "Loss%", "Snt", "Avg", "Best", "Wrst", "StDev", "Last", "Flows", "Stab"))
	} else {
		b.WriteString(fmt.Sprintf("%-3s "+ipFmt+" %-22s %-20s %-8s %-5s %-7s %-7s %-7s %-7s %-7s\n",
			"#", "IP", "Hostname", "ASN", "Loss%", "Snt", "Avg", "Best", "Wrst", "StDev", "Last"))
	}

	rateLimited := hop.DetectRateLimited(hops, maxTTL)
	prevASN := 0

	for ttl := 1; ttl <= maxTTL; ttl++ {
		h, ok := hopMap[ttl]
		if !ok || h.GetIP() == nil {
			b.WriteString(fmt.Sprintf("%-3d *\n", ttl))
			continue
		}

		if multipath && h.IsDivergent() {
			nodes := h.GetNodes()
			sort.Slice(nodes, func(i, j int) bool {
				return nodes[i].GetReceived() > nodes[j].GetReceived()
			})
			hopLoss := h.LossPercent()
			sent := h.GetSent()
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
					if ps, ok := m.pingStats[ip.String()]; ok && rateLimited[ttl] && ps.Sent > 0 {
						lossStr = fmt.Sprintf("%.1f%%†", ps.LossPercent())
						sntStr = fmt.Sprintf("%d", ps.Sent)
					} else if rateLimited[ttl] {
						lossStr = fmt.Sprintf("~%.0f%%", hopLoss)
						sntStr = fmt.Sprintf("%d", sent)
					} else {
						lossStr = fmt.Sprintf("%.1f%%", hopLoss)
						sntStr = fmt.Sprintf("%d", sent)
					}
				}

				var avg, minRTT, maxRTT, stdev, last float64
				if nodeIdx == 0 {
					if ps, ok := m.pingStats[ip.String()]; ok && rateLimited[ttl] && ps.Received > 0 {
						avg = float64(ps.AvgRTT().Microseconds()) / 1000.0
						minRTT = float64(ps.MinRTT.Microseconds()) / 1000.0
						maxRTT = float64(ps.MaxRTT.Microseconds()) / 1000.0
						stdev = ps.StDev()
						last = float64(ps.LastRTT.Microseconds()) / 1000.0
					} else {
						avg = float64(node.AvgRTT().Microseconds()) / 1000.0
						minRTT = float64(node.GetMinRTT().Microseconds()) / 1000.0
						maxRTT = float64(node.GetMaxRTT().Microseconds()) / 1000.0
						stdev = node.StDev()
						last = float64(node.GetLastRTT().Microseconds()) / 1000.0
					}
				} else {
					avg = float64(node.AvgRTT().Microseconds()) / 1000.0
					minRTT = float64(node.GetMinRTT().Microseconds()) / 1000.0
					maxRTT = float64(node.GetMaxRTT().Microseconds()) / 1000.0
					stdev = node.StDev()
					last = float64(node.GetLastRTT().Microseconds()) / 1000.0
				}
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

			var lossStr string
			var avg, minRTT, maxRTT, stdev, last float64
			var sent int
			if ps, ok := m.pingStats[ip.String()]; ok && rateLimited[ttl] && ps.Sent > 0 {
				lossStr = fmt.Sprintf("%.1f%%†", ps.LossPercent())
				sent = ps.Sent
				if ps.Received > 0 {
					avg = float64(ps.AvgRTT().Microseconds()) / 1000.0
					minRTT = float64(ps.MinRTT.Microseconds()) / 1000.0
					maxRTT = float64(ps.MaxRTT.Microseconds()) / 1000.0
					stdev = ps.StDev()
					last = float64(ps.LastRTT.Microseconds()) / 1000.0
				}
			} else if rateLimited[ttl] {
				lossStr = fmt.Sprintf("~%.0f%%", h.LossPercent())
				sent = h.GetSent()
				avg = float64(h.AvgRTT().Microseconds()) / 1000.0
				minRTT = float64(h.GetMinRTT().Microseconds()) / 1000.0
				maxRTT = float64(h.GetMaxRTT().Microseconds()) / 1000.0
				stdev = h.StDev()
				last = float64(h.GetLastRTT().Microseconds()) / 1000.0
			} else {
				lossStr = fmt.Sprintf("%.1f%%", h.LossPercent())
				sent = h.GetSent()
				avg = float64(h.AvgRTT().Microseconds()) / 1000.0
				minRTT = float64(h.GetMinRTT().Microseconds()) / 1000.0
				maxRTT = float64(h.GetMaxRTT().Microseconds()) / 1000.0
				stdev = h.StDev()
				last = float64(h.GetLastRTT().Microseconds()) / 1000.0
			}

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
func (m Model) maxScrollOffset() int {
	totalLines := m.countHopLines()
	availableRows := 0
	if m.height > 4 {
		availableRows = m.height - 4
	}
	maxOff := totalLines - availableRows
	if maxOff < 0 {
		maxOff = 0
	}
	return maxOff
}

// countHopLines returns the total number of display lines for all hops.
func (m Model) countHopLines() int {
	hops := m.table.Snapshot()
	hopMap := make(map[int]*hop.Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}
	maxTTL := m.table.MaxTTLSeen()
	if m.maxTTLHit > 0 && m.maxTTLHit < maxTTL {
		maxTTL = m.maxTTLHit
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

func (m Model) errorView() string {
	lines := []string{
		lossStyle().Render("Error: " + m.err.Error()),
		"",
		"This tool requires raw socket permissions.",
		"",
		"Options:",
		"  1. Run with sudo:  sudo via <target>",
		"  2. Set capability: sudo setcap cap_net_raw+ep $(which via)",
		"",
		"Press q to quit.",
	}
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

func (m Model) exportCmd() tea.Cmd {
	return func() tea.Msg {
		maxTTL := m.table.MaxTTLSeen()
		if m.maxTTLHit > 0 && m.maxTTLHit < maxTTL {
			maxTTL = m.maxTTLHit
		}
		report := export.BuildReport(m.version, m.target, m.targetIP,
			m.protocolName, m.probeCount, m.table, m.resolver, m.enricher, maxTTL)

		format := "json"
		if m.config != nil && m.config.ExportFormat != "" {
			format = m.config.ExportFormat
		}

		ts := time.Now().Format("20060102-1504")
		filename := fmt.Sprintf("via-%s-%s.%s", m.target, ts, format)

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
