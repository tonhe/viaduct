// Package tui implements the terminal UI using bubbletea.
package tui

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tonhe/viaduct/internal/asn"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/ping"
	"github.com/tonhe/viaduct/internal/probe"
	"github.com/tonhe/viaduct/internal/resolve"
)

// Styles
var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	ipStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
	hostStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("251"))
	lossStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	starStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	treeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))  // muted for tree connectors
	flowStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("33"))   // blue for divergence TTL number
	stabStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))  // dim for stability badge
	rateLimitStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("243")) // dim gray for ICMP rate-limited loss
	asnStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	asnBoundaryStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("179")) // muted gold for AS boundary
	pingBadgeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("44")) // cyan
	amberStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("178")) // amber for largest delta
	trendDegStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // red for degrading
	trendImpStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // green for improving
	alertStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true) // red bold for alert
)

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
}

// New creates a new TUI model.
func New(target string, targetIP net.IP, cfg probe.Config, version string, protocolName string, noASN bool, noPing bool, alertLoss float64, alertLatency time.Duration, alertRounds int, noAlert bool) Model {
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

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "p":
			m.paused = !m.paused
			if m.tracer != nil {
				if m.paused {
					atomic.StoreInt32(&m.tracer.Paused, 1)
				} else {
					atomic.StoreInt32(&m.tracer.Paused, 0)
				}
			}
		case "r":
			m.table.ResetAll()
			m.probeCount = 0
			m.startTime = time.Now()
			if m.alertEngine != nil {
				m.alertEngine.Reset()
			}
		case "n":
			m.dnsEnabled = !m.dnsEnabled
		case "d":
			m.viewMode = m.viewMode.Next()
		case "j", "down":
			if m.scrollOffset > 0 {
				m.scrollOffset--
			}
			m.autoScroll = m.scrollOffset == 0
		case "k", "up":
			maxOff := m.maxScrollOffset()
			if m.scrollOffset < maxOff {
				m.scrollOffset++
			}
			m.autoScroll = false
		case "G":
			m.scrollOffset = 0
			m.autoScroll = true
		case "g":
			m.autoScroll = false
			m.scrollOffset = m.maxScrollOffset()
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case HopUpdateMsg:
		r := msg.Result
		h := m.table.GetOrCreate(r.TTL)
		h.AddSample(r.IP, r.RTT, r.FlowID)
		m.probeCount++
		if r.IsTarget {
			m.targetHit = true
			if m.maxTTLHit == 0 || r.TTL < m.maxTTLHit {
				m.maxTTLHit = r.TTL
			}
		}
		// Submit IP for reverse DNS
		if m.dnsEnabled {
			m.resolver.Submit(r.IP)
		}

	case HostnameMsg:
		// Find PathNode(s) with matching IP and set hostname
		for _, h := range m.table.Snapshot() {
			for _, node := range h.GetNodes() {
				if ip := node.GetIP(); ip != nil && ip.Equal(msg.IP) {
					node.SetHostname(msg.Hostname)
				}
			}
		}

	case ASNMsg:
		if m.enricher != nil {
			for _, h := range m.table.Snapshot() {
				for _, node := range h.GetNodes() {
					if ip := node.GetIP(); ip != nil && ip.Equal(msg.IP) {
						node.SetASN(msg.Number, msg.Org)
					}
				}
			}
		}

	case PingUpdateMsg:
		if m.pingStats == nil {
			return m, nil
		}
		key := msg.IP.String()
		stat, ok := m.pingStats[key]
		if !ok {
			stat = &ping.Stat{}
			m.pingStats[key] = stat
		}
		if msg.Lost {
			stat.MarkSent()
		} else {
			stat.AddReply(msg.RTT)
		}
		return m, nil

	case RoundEndMsg:
		for _, h := range m.table.Snapshot() {
			h.MarkRoundEnd()
		}
		// Update alert engine with destination metrics
		if m.alertEngine != nil {
			hops := m.table.Snapshot()
			hopMap := make(map[int]*hop.Hop, len(hops))
			for _, h := range hops {
				hopMap[h.TTL] = h
			}
			maxTTL := m.table.MaxTTLSeen()
			if m.maxTTLHit > 0 && m.maxTTLHit < maxTTL {
				maxTTL = m.maxTTLHit
			}
			// Find destination hop
			var destLoss float64
			var destLatency time.Duration
			for i := maxTTL; i >= 1; i-- {
				h, ok := hopMap[i]
				if !ok || h == nil {
					continue
				}
				pn := h.PrimaryNode()
				if pn != nil && pn.GetReceived() > 0 {
					destLoss = pn.LossPercent()
					destLatency = pn.AvgRTT()
					break
				}
			}
			bell := m.alertEngine.Update(destLoss, destLatency)
			if bell && m.alertEngine.State() == AlertDegraded {
				deltas := computeDeltas(hopMap, maxTTL)
				m.alertEngine.FindAffectedHop(hopMap, maxTTL, deltas)
			}
			_ = bell // bell sound handled by terminal if needed
		}

	case TickMsg:
		return m, tickCmd()

	case ProtocolSwitchMsg:
		m.switchStatusMsg = fmt.Sprintf("No UDP responses, switching to %s...", strings.ToUpper(msg.NewProtocol))
		m.switchStatusTime = time.Now()

	case ProbeErrorMsg:
		m.err = msg.Err
		return m, tea.Quit
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
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")

	// Separator
	b.WriteString(dimStyle.Render(strings.Repeat("─", max(m.width, 60))))
	b.WriteString("\n")

	// Column headers (adaptive to terminal width)
	layout := m.getLayout()
	colHeader := m.renderColumnHeader(layout)
	b.WriteString(dimStyle.Render(colHeader))
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

	// Build all hop lines first
	var hopLines []string
	for ttl := 1; ttl <= maxTTL; ttl++ {
		h, ok := hopMap[ttl]
		if !ok || h.GetIP() == nil {
			star := starStyle.Render("*")
			treeGap := ""
			if m.probeCfg.NumPaths > 1 {
				treeGap = "     " // 4 chars + 1 space separator
			}
			hopLines = append(hopLines, fmt.Sprintf("%s %s%s", dimStyle.Render(fmt.Sprintf("%-4d", ttl)), treeGap, star))
			continue
		}
		if h.IsDivergent() {
			hopLines = append(hopLines, m.renderDivergentHop(h, rateLimited[ttl], deltas, maxDeltaTTL)...)
		} else {
			hopLines = append(hopLines, m.renderHop(h, rateLimited[ttl], deltas, maxDeltaTTL))
		}
	}

	// Determine how many hop lines we can show
	availableRows := 0
	if m.height > 4 {
		availableRows = m.height - 4 // 3 header lines + 1 status line
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
		b.WriteString(line)
		b.WriteString("\n")
	}

	// Alert bar (if degraded)
	alertLine := ""
	if m.alertEngine != nil {
		alertLine = m.alertEngine.Message()
	}

	// Fill remaining space
	displayCount := len(displayLines)
	extraLines := 0
	if alertLine != "" {
		extraLines = 1
	}
	usedLines := 3 + displayCount + extraLines + 1
	if m.height > 0 && usedLines < m.height {
		for i := 0; i < m.height-usedLines; i++ {
			b.WriteString("\n")
		}
	}

	if alertLine != "" {
		b.WriteString(alertStyle.Render(alertLine))
		b.WriteString("\n")
	}

	// Status bar
	maxTTL = m.table.MaxTTLSeen()
	elapsed := time.Since(m.startTime).Truncate(time.Second)
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
	if m.switchStatusMsg != "" && time.Since(m.switchStatusTime) < 5*time.Second {
		switchHint = "    " + m.switchStatusMsg
	}
	scrollHint := ""
	if !m.autoScroll && len(hopLines) > availableRows {
		scrollHint = " [scrolled] G:bottom g:top"
	}
	status := fmt.Sprintf("%s    Elapsed: %s    DNS: %s%s%s    j/k:scroll p:pause r:reset n:dns [d] %s q:quit%s",
		traceStatus, elapsed, dnsLabel, flowLabel, switchHint, m.viewMode.Next().String(), scrollHint)
	b.WriteString(statusStyle.Render(status))

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
	showStab      bool // for ECMP divergent hops
	showASN       bool
	asnWidth      int
	showDelta     bool
	showGMean     bool
	showJttr      bool
	showJavg      bool
	showSpark     bool
	showTrend     bool
}

func (m Model) getLayout() columnLayout {
	w := m.width
	if w < 40 {
		w = 40
	}

	layout := columnLayout{ipWidth: 18}

	if m.probeCfg.NumPaths > 1 {
		layout.showTree = true
	}

	// Budget-based layout: start with fixed columns (#, IP, Loss%, Avg, Spark),
	// then add optional columns in priority order only if there's remaining room.
	// This guarantees columns never overflow the terminal width.
	//
	// Column widths (from renderColumnHeader / renderHopRow):
	//   # = 4, tree = 4, IP = ipWidth, Hostname = hostnameWidth+2,
	//   ASN = asnWidth, Loss% = 8, Snt = 5, Avg/Best/Wrst/StDev/Last/Delta/GMean/Jttr/Javg = 8,
	//   Spark = 14, Trend = 9, Stab = 6
	// Parts are joined with " " (1 char per gap), so total separators = numParts - 1.
	//
	// Fixed parts: #(4) + IP(18) + Loss%(8) + Avg(8) + Spark(14) = 5 parts
	// Separators between 5 parts = 4
	base := 4 + layout.ipWidth + 8 + 8 + 14 + 4
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

	// Each view defines its priority order. Columns are added greedily
	// until the budget runs out. New columns cost width+1 (for the join
	// separator). Growing an existing column costs just the delta.

	switch m.viewMode {
	case ViewHealth:
		// Priority: Hostname, Delta, Trend, short ASN, ASN→20, host grow, ASN→25, host grow
		if tryAdd(15 + 2 + 1) { // hostnameWidth + 2 padding + separator
			layout.showHostname = true
			layout.hostnameWidth = 15
		}
		if tryAdd(8 + 1) {
			layout.showDelta = true
		}
		if tryAdd(9 + 1) {
			layout.showTrend = true
		}
		if hasASN && tryAdd(12+1) {
			layout.showASN = true
			layout.asnWidth = 12
		}
		if layout.showASN && tryAdd(8) {
			layout.asnWidth = 20
		}
		if layout.showHostname && tryAdd(7) {
			layout.hostnameWidth += 7
		}
		if layout.showASN && tryAdd(5) {
			layout.asnWidth = 25
		}
		if layout.showHostname && tryAdd(8) {
			layout.hostnameWidth += 8
		}

	case ViewLatency:
		// Priority: Hostname, Delta, Best, Wrst, short ASN, Last, GMean, ASN→20, host grow, ASN→25
		if tryAdd(15 + 2 + 1) {
			layout.showHostname = true
			layout.hostnameWidth = 15
		}
		if tryAdd(8 + 1) {
			layout.showDelta = true
		}
		if tryAdd(8 + 1) {
			layout.showBest = true
		}
		if tryAdd(8 + 1) {
			layout.showWrst = true
		}
		if hasASN && tryAdd(12+1) {
			layout.showASN = true
			layout.asnWidth = 12
		}
		if tryAdd(8 + 1) {
			layout.showLast = true
		}
		if tryAdd(8 + 1) {
			layout.showGMean = true
		}
		if layout.showASN && tryAdd(8) {
			layout.asnWidth = 20
		}
		if layout.showHostname && tryAdd(7) {
			layout.hostnameWidth += 7
		}
		if layout.showASN && tryAdd(5) {
			layout.asnWidth = 25
		}
		if layout.showHostname && tryAdd(8) {
			layout.hostnameWidth += 8
		}

	case ViewVariability:
		// Priority: Hostname, StDev, Jttr, Javg, Trend, short ASN, ASN→20, host grow, ASN→25
		if tryAdd(15 + 2 + 1) {
			layout.showHostname = true
			layout.hostnameWidth = 15
		}
		if tryAdd(8 + 1) {
			layout.showStDev = true
		}
		if tryAdd(8 + 1) {
			layout.showJttr = true
		}
		if tryAdd(8 + 1) {
			layout.showJavg = true
		}
		if tryAdd(9 + 1) {
			layout.showTrend = true
		}
		if hasASN && tryAdd(12+1) {
			layout.showASN = true
			layout.asnWidth = 12
		}
		if layout.showASN && tryAdd(8) {
			layout.asnWidth = 20
		}
		if layout.showHostname && tryAdd(7) {
			layout.hostnameWidth += 7
		}
		if layout.showASN && tryAdd(5) {
			layout.asnWidth = 25
		}
		if layout.showHostname && tryAdd(8) {
			layout.hostnameWidth += 8
		}

	default: // ViewDefault
		// Priority: Hostname, Snt, Last, short ASN, Best, Wrst, StDev, ASN→20, host grow, Stab, ASN→25, host grow
		if tryAdd(15 + 2 + 1) {
			layout.showHostname = true
			layout.hostnameWidth = 15
		}
		if tryAdd(5 + 1) {
			layout.showSnt = true
		}
		if tryAdd(8 + 1) {
			layout.showLast = true
		}
		if hasASN && tryAdd(12+1) {
			layout.showASN = true
			layout.asnWidth = 12
		}
		if tryAdd(8 + 1) {
			layout.showBest = true
		}
		if tryAdd(8 + 1) {
			layout.showWrst = true
		}
		if tryAdd(8 + 1) {
			layout.showStDev = true
		}
		if layout.showASN && tryAdd(8) {
			layout.asnWidth = 20
		}
		if layout.showHostname && tryAdd(7) {
			layout.hostnameWidth += 7
		}
		if tryAdd(6 + 1) {
			layout.showStab = true
		}
		if layout.showASN && tryAdd(5) {
			layout.asnWidth = 25
		}
		if layout.showHostname && tryAdd(8) {
			layout.hostnameWidth += 8
		}
		if layout.showHostname && tryAdd(10) {
			layout.hostnameWidth += 10
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
	w := 4 + l.ipWidth + 8 + 8 // # + IP + Loss% + Avg (always present)
	parts := 4                  // 4 fixed parts
	if l.showTree {
		w += 4
		parts++
	}
	if l.showHostname {
		w += l.hostnameWidth + 2
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
		w += 8
		parts++
	}
	if l.showWrst {
		w += 8
		parts++
	}
	if l.showStDev {
		w += 8
		parts++
	}
	if l.showLast {
		w += 8
		parts++
	}
	if l.showDelta {
		w += 8
		parts++
	}
	if l.showGMean {
		w += 8
		parts++
	}
	if l.showJttr {
		w += 8
		parts++
	}
	if l.showJavg {
		w += 8
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
	if l.showStab {
		w += 6
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
		// Try shrinking ASN
		if l.showASN && l.asnWidth > 12 {
			excess := l.computeWidth() - maxWidth
			shrink := excess
			if shrink > l.asnWidth-12 {
				shrink = l.asnWidth - 12
			}
			l.asnWidth -= shrink
			continue
		}
		// Drop columns in reverse priority
		if l.showStab {
			l.showStab = false
			continue
		}
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
	parts = append(parts, fmt.Sprintf("%-4s", "#"))
	if layout.showTree {
		parts = append(parts, "    ") // 4 chars for tree connector column
	}
	parts = append(parts, fmt.Sprintf("%-*s", layout.ipWidth, "IP"))
	if layout.showHostname {
		parts = append(parts, fmt.Sprintf("%-*s", layout.hostnameWidth+2, "Hostname"))
	}
	if layout.showASN {
		parts = append(parts, fmt.Sprintf("%-*s", layout.asnWidth, "ASN"))
	}
	parts = append(parts, fmt.Sprintf("%-8s", "Loss%"))
	if layout.showSnt {
		parts = append(parts, fmt.Sprintf("%-5s", "Snt"))
	}
	parts = append(parts, fmt.Sprintf("%-8s", "Avg"))
	if layout.showBest {
		parts = append(parts, fmt.Sprintf("%-8s", "Best"))
	}
	if layout.showWrst {
		parts = append(parts, fmt.Sprintf("%-8s", "Wrst"))
	}
	if layout.showStDev {
		parts = append(parts, fmt.Sprintf("%-8s", "StDev"))
	}
	if layout.showLast {
		parts = append(parts, fmt.Sprintf("%-8s", "Last"))
	}
	if layout.showDelta {
		parts = append(parts, fmt.Sprintf("%-8s", "Delta"))
	}
	if layout.showGMean {
		parts = append(parts, fmt.Sprintf("%-8s", "GMean"))
	}
	if layout.showJttr {
		parts = append(parts, fmt.Sprintf("%-8s", "Jttr"))
	}
	if layout.showJavg {
		parts = append(parts, fmt.Sprintf("%-8s", "Javg"))
	}
	if layout.showSpark {
		parts = append(parts, fmt.Sprintf("%-14s", "Spark"))
	}
	if layout.showTrend {
		parts = append(parts, fmt.Sprintf("%-9s", "Trend"))
	}
	if layout.showStab {
		parts = append(parts, fmt.Sprintf("%-6s", "Stab"))
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
	ttlStr := fmt.Sprintf("%-4d", h.TTL)

	// If no IP, this is a non-responding hop
	ip := h.GetIP()
	if ip == nil {
		star := starStyle.Render("*")
		return fmt.Sprintf("%s %s", dimStyle.Render(ttlStr), star)
	}

	var parts []string
	parts = append(parts, dimStyle.Render(ttlStr))
	if layout.showTree {
		parts = append(parts, "    ") // empty tree connector column
	}
	parts = append(parts, ipStyle.Render(fmt.Sprintf("%-*s", layout.ipWidth, ip.String())))

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
		parts = append(parts, hostStyle.Render(fmt.Sprintf("%-*s", layout.hostnameWidth+2, hostname)))
	}

	if layout.showASN {
		parts = append(parts, m.renderASNCell(ip, m.prevASNForTTL(h.TTL), layout.asnWidth))
	}

	// Check for ping supplement data
	var ps *ping.Stat
	if isRateLimited && m.pingStats != nil {
		ps = m.pingStats[ip.String()]
	}

	// Loss — ping indicator "†" fits within the 8-char column
	if ps != nil && ps.Sent > 0 {
		loss := ps.LossPercent()
		lossStr := fmt.Sprintf("%.1f%%", loss)
		padded := fmt.Sprintf("%-6s", lossStr)
		if loss == 0 {
			parts = append(parts, okStyle.Render(padded)+pingBadgeStyle.Render("† "))
		} else {
			parts = append(parts, lossStyle.Render(padded)+pingBadgeStyle.Render("† "))
		}
	} else {
		loss := h.LossPercent()
		lossStr := fmt.Sprintf("%-8s", fmt.Sprintf("%.1f%%", loss))
		if loss == 0 {
			parts = append(parts, okStyle.Render(fmt.Sprintf("%-8s", "0.0%")))
		} else if isRateLimited {
			parts = append(parts, rateLimitStyle.Render(fmt.Sprintf("%-8s", fmt.Sprintf("~%.0f%%", loss))))
		} else {
			parts = append(parts, lossStyle.Render(lossStr))
		}
	}

	// RTT stats — use ping stats when available
	if ps != nil && ps.Received > 0 {
		if layout.showSnt {
			parts = append(parts, fmt.Sprintf("%-5d", ps.Sent))
		}
		parts = append(parts, fmt.Sprintf("%-8s", formatDuration(ps.AvgRTT())))
		if layout.showBest {
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(ps.MinRTT)))
		}
		if layout.showWrst {
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(ps.MaxRTT)))
		}
		if layout.showStDev {
			stdev := ps.StDev()
			if stdev == 0 {
				parts = append(parts, fmt.Sprintf("%-8s", "-"))
			} else {
				parts = append(parts, fmt.Sprintf("%-8s", fmt.Sprintf("%.1f", stdev)))
			}
		}
		if layout.showLast {
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(ps.LastRTT)))
		}
	} else {
		if layout.showSnt {
			parts = append(parts, fmt.Sprintf("%-5d", h.GetSent()))
		}
		parts = append(parts, fmt.Sprintf("%-8s", formatDuration(h.AvgRTT())))
		if layout.showBest {
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(h.GetMinRTT())))
		}
		if layout.showWrst {
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(h.GetMaxRTT())))
		}
		if layout.showStDev {
			stdev := h.StDev()
			if stdev == 0 {
				parts = append(parts, fmt.Sprintf("%-8s", "-"))
			} else {
				parts = append(parts, fmt.Sprintf("%-8s", fmt.Sprintf("%.1f", stdev)))
			}
		}
		if layout.showLast {
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(h.GetLastRTT())))
		}
	}

	// New columns from M4
	pn := h.PrimaryNode()

	if layout.showDelta {
		if h.TTL < len(deltas) && deltas[h.TTL] != 0 {
			d := deltas[h.TTL]
			deltaStr := fmt.Sprintf("%-8s", formatDelta(d))
			if h.TTL == maxDeltaTTL {
				parts = append(parts, amberStyle.Render(deltaStr))
			} else {
				parts = append(parts, deltaStr)
			}
		} else {
			parts = append(parts, fmt.Sprintf("%-8s", "-"))
		}
	}
	if layout.showGMean {
		if pn != nil {
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(pn.GeoMean())))
		} else {
			parts = append(parts, fmt.Sprintf("%-8s", "-"))
		}
	}
	if layout.showJttr {
		if pn != nil {
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(pn.Jitter())))
		} else {
			parts = append(parts, fmt.Sprintf("%-8s", "-"))
		}
	}
	if layout.showJavg {
		if pn != nil {
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(pn.JitterMean())))
		} else {
			parts = append(parts, fmt.Sprintf("%-8s", "-"))
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
				parts = append(parts, trendDegStyle.Render(fmt.Sprintf("%-9s", "▲ "+trend)))
			case "improving":
				parts = append(parts, trendImpStyle.Render(fmt.Sprintf("%-9s", "▼ "+trend)))
			default:
				parts = append(parts, dimStyle.Render(fmt.Sprintf("%-9s", "— stable")))
			}
		} else {
			parts = append(parts, fmt.Sprintf("%-9s", "-"))
		}
	}

	return strings.Join(parts, " ")
}

func (m Model) renderDivergentHop(h *hop.Hop, isRateLimited bool, deltas []time.Duration, maxDeltaTTL int) []string {
	layout := m.getLayout()
	nodes := h.GetNodes()

	// Sort by received count descending (primary path first)
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].GetReceived() > nodes[j].GetReceived()
	})

	// Cap at 5 visible nodes
	overflow := 0
	displayNodes := nodes
	if len(nodes) > 5 {
		displayNodes = nodes[:4]
		overflow = len(nodes) - 4
	}

	var lines []string
	for i, node := range displayNodes {
		ip := node.GetIP()
		if ip == nil {
			continue
		}

		// TTL number: only on the first line, in flowStyle (blue)
		ttlStr := "    "
		if i == 0 {
			ttlStr = flowStyle.Render(fmt.Sprintf("%-4d", h.TTL))
		}

		// Tree connector as its own column (4 display chars)
		isLast := i == len(displayNodes)-1 && overflow == 0
		connector := "├── "
		if isLast {
			connector = "└── "
		}

		var parts []string
		parts = append(parts, ttlStr)
		parts = append(parts, treeStyle.Render(connector))
		parts = append(parts, ipStyle.Render(fmt.Sprintf("%-*s", layout.ipWidth, ip.String())))

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
			parts = append(parts, hostStyle.Render(fmt.Sprintf("%-*s", layout.hostnameWidth+2, hostname)))
		}

		if layout.showASN {
			if i == 0 {
				parts = append(parts, m.renderASNCell(ip, m.prevASNForTTL(h.TTL), layout.asnWidth))
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
				lossStr := fmt.Sprintf("%.1f%%", loss)
				padded := fmt.Sprintf("%-6s", lossStr)
				if loss == 0 {
					parts = append(parts, okStyle.Render(padded)+pingBadgeStyle.Render("† "))
				} else {
					parts = append(parts, lossStyle.Render(padded)+pingBadgeStyle.Render("† "))
				}
			} else {
				loss := h.LossPercent()
				if loss == 0 {
					parts = append(parts, okStyle.Render(fmt.Sprintf("%-8s", "0.0%")))
				} else if isRateLimited {
					parts = append(parts, rateLimitStyle.Render(fmt.Sprintf("%-8s", fmt.Sprintf("~%.0f%%", loss))))
				} else {
					parts = append(parts, lossStyle.Render(fmt.Sprintf("%-8s", fmt.Sprintf("%.1f%%", loss))))
				}
			}
		} else {
			parts = append(parts, dimStyle.Render(fmt.Sprintf("%-8s", "-")))
		}

		// Sent count and RTT stats
		if i == 0 && ps != nil && ps.Received > 0 {
			if layout.showSnt {
				parts = append(parts, fmt.Sprintf("%-5d", ps.Sent))
			}
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(ps.AvgRTT())))
			if layout.showBest {
				parts = append(parts, fmt.Sprintf("%-8s", formatDuration(ps.MinRTT)))
			}
			if layout.showWrst {
				parts = append(parts, fmt.Sprintf("%-8s", formatDuration(ps.MaxRTT)))
			}
			if layout.showStDev {
				stdev := ps.StDev()
				if stdev == 0 {
					parts = append(parts, fmt.Sprintf("%-8s", "-"))
				} else {
					parts = append(parts, fmt.Sprintf("%-8s", fmt.Sprintf("%.1f", stdev)))
				}
			}
			if layout.showLast {
				parts = append(parts, fmt.Sprintf("%-8s", formatDuration(ps.LastRTT)))
			}
		} else {
			if layout.showSnt {
				if i == 0 {
					parts = append(parts, fmt.Sprintf("%-5d", h.GetSent()))
				} else {
					parts = append(parts, fmt.Sprintf("%-5s", "-"))
				}
			}
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(node.AvgRTT())))
			if layout.showBest {
				parts = append(parts, fmt.Sprintf("%-8s", formatDuration(node.GetMinRTT())))
			}
			if layout.showWrst {
				parts = append(parts, fmt.Sprintf("%-8s", formatDuration(node.GetMaxRTT())))
			}
			if layout.showStDev {
				stdev := node.StDev()
				if stdev == 0 {
					parts = append(parts, fmt.Sprintf("%-8s", "-"))
				} else {
					parts = append(parts, fmt.Sprintf("%-8s", fmt.Sprintf("%.1f", stdev)))
				}
			}
			if layout.showLast {
				parts = append(parts, fmt.Sprintf("%-8s", formatDuration(node.GetLastRTT())))
			}
		}

		// New columns from M4
		if layout.showDelta {
			if i == 0 && h.TTL < len(deltas) && deltas[h.TTL] != 0 {
				d := deltas[h.TTL]
				deltaStr := fmt.Sprintf("%-8s", formatDelta(d))
				if h.TTL == maxDeltaTTL {
					parts = append(parts, amberStyle.Render(deltaStr))
				} else {
					parts = append(parts, deltaStr)
				}
			} else {
				parts = append(parts, fmt.Sprintf("%-8s", "-"))
			}
		}
		if layout.showGMean {
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(node.GeoMean())))
		}
		if layout.showJttr {
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(node.Jitter())))
		}
		if layout.showJavg {
			parts = append(parts, fmt.Sprintf("%-8s", formatDuration(node.JitterMean())))
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
					parts = append(parts, trendDegStyle.Render(fmt.Sprintf("%-9s", "▲ "+trend)))
				case "improving":
					parts = append(parts, trendImpStyle.Render(fmt.Sprintf("%-9s", "▼ "+trend)))
				default:
					parts = append(parts, dimStyle.Render(fmt.Sprintf("%-9s", "— stable")))
				}
			} else {
				parts = append(parts, fmt.Sprintf("%-9s", ""))
			}
		}

		// Stability badge
		if layout.showStab {
			stabPct := node.StabilityPercent()
			parts = append(parts, stabStyle.Render(fmt.Sprintf("[%.0f%%]", stabPct)))
		}

		lines = append(lines, strings.Join(parts, " "))
	}

	// Overflow line
	if overflow > 0 {
		lines = append(lines, fmt.Sprintf("     %s",
			stabStyle.Render(fmt.Sprintf("... +%d more paths", overflow))))
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
func (m Model) renderASNCell(ip net.IP, prevASN int, width int) string {
	if m.enricher == nil || ip == nil {
		return fmt.Sprintf("%-*s", width, "")
	}
	info, ok := m.enricher.Lookup(ip)
	if !ok {
		return dimStyle.Render(fmt.Sprintf("%-*s", width, "..."))
	}
	label := asn.FormatASN(info.Number, info.Org)
	if label == "" {
		return fmt.Sprintf("%-*s", width, "")
	}
	if len(label) > width {
		label = label[:width]
	}
	formatted := fmt.Sprintf("%-*s", width, label)
	if info.Number != 0 && info.Number != prevASN {
		return asnBoundaryStyle.Render(formatted)
	}
	return asnStyle.Render(formatted)
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
	return fmt.Sprintf("%s%.1fms", prefix, float64(d.Microseconds())/1000.0)
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.0fµs", float64(d.Microseconds()))
	}
	return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000.0)
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

	// Column headers
	if multipath {
		b.WriteString(fmt.Sprintf("%-4s %-18s %-22s %-20s %-8s %-5s %-8s %-8s %-8s %-8s %-8s %-8s %-5s\n",
			"#", "IP", "Hostname", "ASN", "Loss%", "Snt", "Avg", "Best", "Wrst", "StDev", "Last", "Flows", "Stab"))
	} else {
		b.WriteString(fmt.Sprintf("%-4s %-18s %-22s %-20s %-8s %-5s %-8s %-8s %-8s %-8s %-8s\n",
			"#", "IP", "Hostname", "ASN", "Loss%", "Snt", "Avg", "Best", "Wrst", "StDev", "Last"))
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

	rateLimited := hop.DetectRateLimited(hops, maxTTL)
	prevASN := 0

	for ttl := 1; ttl <= maxTTL; ttl++ {
		h, ok := hopMap[ttl]
		if !ok || h.GetIP() == nil {
			b.WriteString(fmt.Sprintf("%-4d *\n", ttl))
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

				b.WriteString(fmt.Sprintf("%-4d %-18s %-22s %-20s %-8s %-5s %-8s %-8s %-8s %-8s %-8s %-8s %-5s\n",
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
				b.WriteString(fmt.Sprintf("%-4d %-18s %-22s %-20s %-8s %-5d %-8s %-8s %-8s %-8s %-8s %-8s %-5s\n",
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
				b.WriteString(fmt.Sprintf("%-4d %-18s %-22s %-20s %-8s %-5d %-8s %-8s %-8s %-8s %-8s\n",
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
			n := len(h.GetNodes())
			if n > 5 {
				count += 5 // 4 nodes + overflow line
			} else {
				count += n
			}
		} else {
			count++
		}
	}
	return count
}

func (m Model) errorView() string {
	var b strings.Builder
	b.WriteString(lossStyle.Render("Error: " + m.err.Error()))
	b.WriteString("\n\n")
	b.WriteString("This tool requires raw socket permissions.\n\n")
	b.WriteString("Options:\n")
	b.WriteString("  1. Run with sudo:  sudo via <target>\n")
	b.WriteString("  2. Set capability: sudo setcap cap_net_raw+ep $(which via)\n")
	b.WriteString("\nPress q to quit.\n")
	return b.String()
}
