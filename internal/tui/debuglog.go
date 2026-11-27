package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// snapshotEvery is the number of rounds between per-hop snapshots in the
// debug log.
const snapshotEvery = 10

// RoundDebug writes one target's round records to the debug log
// (--debug-log): a line for every completed round, a per-hop snapshot every
// snapshotEvery rounds and at exit, and a line when an alert is raised or
// cleared. Everything it writes is read from the hop table after the round
// has been closed, so the figures are the ones the views show.
//
// A periodic snapshot is taken when its round closes and written when the
// next round closes. If the run ends first, the final snapshot is written in
// its place, so a run that ends on a snapshot round has one set of hop lines
// for that round, labeled final.
//
// A nil *RoundDebug records nothing: that is the state without --debug-log.
type RoundDebug struct {
	logf    func(format string, args ...any)
	target  string
	primary string       // the primary probe type's name; "" when the primary is ICMP
	engine  *AlertEngine // nil when alerting is off
	// drive is set in report mode, where no TUI feeds the alert engine: the
	// recorder feeds it each round itself.
	drive bool

	now    func() time.Time
	last   time.Time // when the previous round closed
	rounds int
	alert  string // the text of the alert that is raised, "" when none is
	dest   int    // the destination's TTL when the previous round closed, 0 while unknown
	prev   map[int]roundCounts
	// held is the periodic snapshot of the last round closed, not yet
	// written: the next round's close writes it, Final drops it.
	held []string
}

// roundCounts are a hop's cumulative counters when a round closed.
type roundCounts struct {
	icmpSent, icmpRecvd, primarySent, primaryRecvd, echoSent, echoRecvd int
}

func (c roundCounts) minus(p roundCounts) roundCounts {
	return roundCounts{
		c.icmpSent - p.icmpSent, c.icmpRecvd - p.icmpRecvd,
		c.primarySent - p.primarySent, c.primaryRecvd - p.primaryRecvd,
		c.echoSent - p.echoSent, c.echoRecvd - p.echoRecvd,
	}
}

func (c roundCounts) anyNegative() bool {
	return c.icmpSent < 0 || c.icmpRecvd < 0 || c.primarySent < 0 || c.primaryRecvd < 0 || c.echoSent < 0 || c.echoRecvd < 0
}

// NewRoundDebug returns the recorder for one target of the TUI. logf is the
// debug log's Printf; with a nil logf there is no log and the result is nil.
// target tags every line, as the probe engine's own lines are tagged.
// protocol is the primary protocol's name. engine is the session's alert
// engine, which the TUI updates; nil when alerting is off.
func NewRoundDebug(logf func(format string, args ...any), target, protocol string, engine *AlertEngine) *RoundDebug {
	if logf == nil {
		return nil
	}
	d := &RoundDebug{logf: logf, target: target, engine: engine, now: time.Now, prev: make(map[int]roundCounts)}
	switch protocol {
	case "icmp":
	case "tcp":
		d.primary = "tcp-ttl"
	default:
		d.primary = "udp-ttl"
	}
	d.last = d.now()
	return d
}

// NewReportDebug returns the recorder for one target of report mode. Report
// mode shows no alerts, so the recorder runs an alert engine of its own with
// the given thresholds and logs the alerts the TUI would have shown.
func NewReportDebug(logf func(format string, args ...any), target, protocol string, alertLoss float64, alertLatency time.Duration, alertRounds int, noAlert bool) *RoundDebug {
	var engine *AlertEngine
	if !noAlert {
		engine = NewAlertEngine(alertLoss, alertLatency, alertRounds)
	}
	d := NewRoundDebug(logf, target, protocol, engine)
	if d != nil {
		d.drive = true
	}
	return d
}

// CloseRound closes the round on every hop of table and on the table itself,
// which settles the destination's TTL, and returns the TTLs probes were sent
// to in that round.
func CloseRound(table *hop.Table) map[int]bool {
	hops := table.Snapshot()
	sentTo := make(map[int]bool, len(hops))
	for _, h := range hops {
		if h.RoundOpen() {
			sentTo[h.TTL] = true
		}
		h.MarkRoundEnd()
	}
	table.EndRound()
	return sentTo
}

// alertMaxTTL is the last TTL alerting looks at: the destination's, or the
// highest seen while the destination has not answered.
func alertMaxTTL(table *hop.Table, targetTTL int) int {
	maxTTL := table.MaxTTLSeen()
	if targetTTL > 0 && targetTTL < maxTTL {
		maxTTL = targetTTL
	}
	return maxTTL
}

// RoundClosed records a round that has just been closed on table (CloseRound,
// whose result is sentTo). In the TUI the session's alert engine has already
// seen the round.
func (d *RoundDebug) RoundClosed(table *hop.Table, sentTo map[int]bool) {
	if d == nil {
		return
	}
	// The run went on past the last snapshot round: its snapshot is not the
	// final one, so it is written now.
	d.write(d.held)
	d.held = nil

	d.rounds++
	now := d.now()
	took := now.Sub(d.last)
	d.last = now

	hops := table.Snapshot()
	target := table.TargetTTL()
	var sum roundCounts
	// Hops probed and hops that answered nothing are counted up to the
	// destination. Hops beyond it are probed in round one, and again while
	// the tracer looks further out for a destination that did not answer;
	// they are counted apart, so that a silent TTL nobody is shown does not
	// read as a silent hop of the path.
	probed, silent, beyond := 0, 0, 0
	for _, h := range hops {
		var c roundCounts
		c.icmpSent, c.icmpRecvd, c.primarySent, c.primaryRecvd = h.ProbeCountsByType()
		c.echoSent, c.echoRecvd, _, _ = h.EchoCounts()
		delta := c.minus(d.prev[h.TTL])
		if delta.anyNegative() {
			// The counters were reset since the last round: they hold this
			// round alone.
			delta = c
		}
		d.prev[h.TTL] = c
		sum.icmpSent += delta.icmpSent
		sum.icmpRecvd += delta.icmpRecvd
		sum.primarySent += delta.primarySent
		sum.primaryRecvd += delta.primaryRecvd
		sum.echoSent += delta.echoSent
		sum.echoRecvd += delta.echoRecvd
		switch {
		case delta.icmpSent+delta.primarySent == 0:
		case target > 0 && h.TTL > target:
			beyond++
		default:
			probed++
			if delta.icmpRecvd+delta.primaryRecvd == 0 {
				silent++
			}
		}
	}
	byType := func(icmp, primary, echo int) string {
		s := fmt.Sprintf("icmp-ttl=%d", icmp)
		if d.primary != "" {
			s += fmt.Sprintf(" %s=%d", d.primary, primary)
		}
		return s + fmt.Sprintf(" icmp-echo=%d", echo)
	}
	line := fmt.Sprintf("round %d: took %v; sent %s; replies %s; no reply from %d of %d hops probed",
		d.rounds, took.Round(time.Millisecond),
		byType(sum.icmpSent, sum.primarySent, sum.echoSent),
		byType(sum.icmpRecvd, sum.primaryRecvd, sum.echoRecvd),
		silent, probed)
	if beyond > 0 {
		line += fmt.Sprintf("; %d TTLs probed beyond the destination", beyond)
	}
	d.printf("%s", line)

	if d.dest != 0 && target != 0 && target != d.dest {
		d.printf("round %d: destination moved from ttl=%d to ttl=%d", d.rounds, d.dest, target)
	}
	if target != 0 {
		d.dest = target
	}

	if d.drive {
		d.engine.ObserveRound(hops, sentTo, alertMaxTTL(table, target))
	}
	d.logAlert()

	if d.rounds%snapshotEvery == 0 {
		d.held = d.snapshot("snapshot", table)
	}
}

// logAlert writes a line when the alert engine's state has changed since the
// last round.
func (d *RoundDebug) logAlert() {
	msg := ""
	if d.engine != nil {
		msg = strings.TrimPrefix(d.engine.Message(), "! ")
	}
	switch {
	case msg != "" && d.alert == "":
		d.printf("round %d: alert raised: %s", d.rounds, msg)
	case msg == "" && d.alert != "":
		d.printf("round %d: alert cleared: %s", d.rounds, d.alert)
	}
	d.alert = msg
}

// Final writes the snapshot taken when the run ends. It replaces a periodic
// snapshot of the last round that has not been written yet: the final set has
// that round's figures with any reply that arrived since.
func (d *RoundDebug) Final(table *hop.Table) {
	if d == nil {
		return
	}
	d.held = nil
	d.write(d.snapshot("final", table))
}

// write writes hop lines to the log.
func (d *RoundDebug) write(lines []string) {
	for _, l := range lines {
		d.printf("%s", l)
	}
}

// snapshot returns one line per hop in the table. A hop beyond the
// destination's TTL is marked beyond=yes: the views leave those hops out, and
// the log lists them because they show a destination TTL that is wrong or has
// moved. Counts are sent/lost over completed rounds.
func (d *RoundDebug) snapshot(label string, table *hop.Table) []string {
	var lines []string
	target := table.TargetTTL()
	for _, h := range table.Snapshot() {
		var nodes []string
		for _, n := range h.GetNodes() {
			if ip := n.GetIP(); ip != nil {
				nodes = append(nodes, ip.String())
			}
		}
		if len(nodes) == 0 {
			nodes = []string{"-"}
		}
		pct, kind := h.Loss()
		icmpSent, icmpRecvd, primarySent, primaryRecvd := h.ProbeCountsByType()
		echoSent, echoRecvd, countedSent, countedRecvd := h.EchoCounts()
		ttlProbes := fmt.Sprintf("icmp-ttl=%d/%d", icmpSent, lost(icmpSent, icmpRecvd))
		if d.primary != "" {
			ttlProbes += fmt.Sprintf(" %s=%d/%d", d.primary, primarySent, lost(primarySent, primaryRecvd))
		}
		line := fmt.Sprintf("%s round=%d ttl=%d nodes=%s loss=%s kind=%s %s echo=%d/%d echo-counted=%d/%d rate-limited=%s",
			label, d.rounds, h.TTL, strings.Join(nodes, ","), hop.FormatLoss(pct, kind), kind, ttlProbes,
			echoSent, lost(echoSent, echoRecvd), countedSent, lost(countedSent, countedRecvd), h.RateLimitState())
		if target > 0 && h.TTL > target {
			line += " beyond=yes"
		}
		lines = append(lines, line)
	}
	return lines
}

// lost is sent minus received, never negative: a duplicated reply can make
// received exceed sent.
func lost(sent, received int) int {
	if received > sent {
		return 0
	}
	return sent - received
}

func (d *RoundDebug) printf(format string, args ...any) {
	d.logf("[%s] %s", d.target, fmt.Sprintf(format, args...))
}

// SetDebugLog turns on the debug log's round records (--debug-log) for every
// target that is being traced. logf is the log's Printf; nil leaves them off.
func (m *Model) SetDebugLog(logf func(format string, args ...any)) {
	for _, s := range m.sessions {
		if s.startErr != nil || s.targetIP == nil {
			continue
		}
		s.debug = NewRoundDebug(logf, s.targetIP.String(), s.protocolName, s.alertEngine)
	}
}

// DebugFinal writes every target's final snapshot to the debug log. Call it
// once, when the TUI has stopped.
func (m Model) DebugFinal() {
	for _, s := range m.sessions {
		s.debug.Final(s.table)
	}
}
