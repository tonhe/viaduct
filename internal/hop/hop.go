// Package hop tracks per-hop results, RTT stats, and path state.
package hop

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/tonhe/viaduct/internal/dest"
)

// Hop holds the state for a single TTL hop.
// It contains one or more PathNodes, each representing a distinct IP seen at this TTL.
type Hop struct {
	TTL       int
	Nodes     []*PathNode
	roundsSent int // rounds in which the tracer probed this TTL (includes the open round)

	// Per-probe loss accounting. The running counters move as probes go out
	// and replies come in; the closed counters are a snapshot taken when a
	// round closes, so loss never includes probes whose replies may still be
	// on the way.
	probesSent   int // TTL-limited probes sent to this TTL
	probesRecvd  int // non-echo replies received
	closedSent   int // probesSent as of the last closed round
	closedRecvd  int // probesRecvd as of the last closed round
	closedRounds int // roundsSent as of the last closed round

	// The ICMP TTL probes' share of the counters above, kept so the debug log
	// can give a hop's probes by type (ProbeCountsByType). The rest are
	// primary (UDP or TCP) probes.
	icmpSent        int
	icmpRecvd       int
	closedICMPSent  int
	closedICMPRecvd int

	// TTL-limited probes of each type sent in the open round, as the tracer
	// reported them. Moved to the nodes when the round closes.
	openICMP    int
	openPrimary int

	// draining is set by Reset when this hop's round was open at the reset.
	// Replies still on their way answer probes whose sends Reset discarded,
	// so they are dropped until that round closes or a new round is sent.
	draining bool

	// held is the hop's last rate-limit verdict as a hop of the path, and
	// heldTarget its last verdict judged on the target alone (see
	// verdictBasis). Once a hop has had a verdict it keeps one: while its echo
	// totals are below the samples a verdict needs, the record stands in (see
	// holdVerdictLocked). Both are kept, so the hop has one to hold whichever
	// way it is judged when it gains or loses the destination.
	held       verdictRecord
	heldTarget verdictRecord

	// destination is set by Table.EndRound on the destination's hop (see
	// Table.TargetTTL). That hop is judged on the target alone.
	destination bool

	mu sync.RWMutex
}

// NewHop creates a hop for the given TTL.
func NewHop(ttl int) *Hop {
	return &Hop{TTL: ttl}
}

// AddSample records an RTT measurement from the given IP with a flow
// identifier. It reports whether the sample was recorded: a reply that
// arrives while the round open at a Reset drains is dropped (see Reset).
func (h *Hop) AddSample(ip net.IP, rtt time.Duration, flowID int, kind ProbeKind) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.draining {
		return false
	}

	// Find or create PathNode for this IP
	var node *PathNode
	for _, n := range h.Nodes {
		if n.IP.Equal(ip) {
			node = n
			break
		}
	}
	if node == nil {
		node = NewPathNode(ip)
		h.Nodes = append(h.Nodes, node)
	}
	if kind != KindEcho {
		h.probesRecvd++
		if kind == KindICMPTTL {
			h.icmpRecvd++
		}
	} else {
		h.beforeEchoAnswerLocked(node)
	}
	node.AddSample(rtt, flowID, kind)
	return true
}

// IsDivergent returns true if more than one IP has been seen at this TTL.
func (h *Hop) IsDivergent() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.Nodes) > 1
}

// PrimaryNode returns the PathNode with the most received samples.
// Returns nil if there are no nodes.
func (h *Hop) PrimaryNode() *PathNode {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.primaryNodeLocked()
}

func (h *Hop) primaryNodeLocked() *PathNode {
	if len(h.Nodes) == 0 {
		return nil
	}
	best := h.Nodes[0]
	bestRecv := best.GetReceived()
	for _, n := range h.Nodes[1:] {
		recv := n.GetReceived()
		if recv > bestRecv {
			best = n
			bestRecv = recv
		}
	}
	return best
}

// GetNodes returns a copy of the nodes slice.
func (h *Hop) GetNodes() []*PathNode {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*PathNode, len(h.Nodes))
	copy(out, h.Nodes)
	return out
}

// MarkRoundSent records a round in which one TTL-limited probe was sent to
// this TTL. Shorthand for MarkRoundSentProbes(1).
func (h *Hop) MarkRoundSent() { h.MarkRoundSentProbes(1) }

// MarkRoundSentProbes records that the tracer sent probes TTL-limited probes
// to this TTL this round, given as a total: the first is taken to be the ICMP
// TTL probe and the rest primary flows. A caller that knows the split uses
// MarkRoundSentByType.
func (h *Hop) MarkRoundSentProbes(probes int) {
	if probes <= 0 {
		h.MarkRoundSentByType(0, 0)
		return
	}
	h.MarkRoundSentByType(1, probes-1)
}

// MarkRoundSentByType records the TTL-limited probes the tracer actually
// wrote for this TTL this round: icmpTTL ICMP TTL probes and primary UDP or
// TCP flow probes. Called once per hop per round (probe.RoundTypeMarker via
// Table).
//
// A probe that did not leave (the write failed, timed out or was skipped) is
// not in these counts, so it is neither sent nor lost. When nothing left for
// this TTL, the round is not a round for this hop: no probes, no Rnd, and no
// sparkline cell or stability slot when it closes.
func (h *Hop) MarkRoundSentByType(icmpTTL, primary int) {
	if icmpTTL < 0 {
		icmpTTL = 0
	}
	if primary < 0 {
		primary = 0
	}
	if icmpTTL+primary == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// A new round's probes are out: replies from here on may answer them.
	h.draining = false
	h.roundsSent++
	h.probesSent += icmpTTL + primary
	h.icmpSent += icmpTTL
	h.openICMP += icmpTTL
	h.openPrimary += primary
}

// RoundOpen reports whether probes were sent to this hop in a round that has
// not been closed yet: whether the next MarkRoundEnd closes a round for it.
func (h *Hop) RoundOpen() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.roundsSent > h.closedRounds
}

// MarkRoundEnd closes a probe round: probes sent so far and replies received
// so far become the figures loss is computed from, then stability tracking
// advances for all nodes. A round in which nothing was sent to this hop
// (the tracer was paused, no send to it succeeded, or the hop lies beyond the
// destination) updates the figures but is not a round for the nodes: it adds
// no probes, sparkline cell or stability slot.
func (h *Hop) MarkRoundEnd() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.draining = false
	h.closedSent = h.probesSent
	h.closedRecvd = h.probesRecvd
	h.closedICMPSent = h.icmpSent
	h.closedICMPRecvd = h.icmpRecvd
	if h.roundsSent == h.closedRounds {
		return
	}
	h.closedRounds = h.roundsSent
	// Probes of each type sent to this hop in the round being closed, as the
	// tracer reported them.
	icmpSent, primarySent := h.openICMP, h.openPrimary
	h.openICMP, h.openPrimary = 0, 0
	h.holdVerdictLocked()
	verdict := h.rateLimitStateLocked()
	for _, n := range h.Nodes {
		n.markProbesSent(icmpSent, primarySent)
		n.setHopVerdict(verdict)
		n.RecordRoundAvg()
		n.AdvanceRound()
		n.MarkProbeRoundEnd()
	}
}

// --- Backward-compatible delegating methods ---

// GetIP returns the primary node's IP address (thread-safe).
func (h *Hop) GetIP() net.IP {
	h.mu.RLock()
	defer h.mu.RUnlock()
	pn := h.primaryNodeLocked()
	if pn == nil {
		return nil
	}
	return pn.GetIP()
}

// GetHostname returns the primary node's hostname (thread-safe).
func (h *Hop) GetHostname() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	pn := h.primaryNodeLocked()
	if pn == nil {
		return ""
	}
	return pn.GetHostname()
}

// SetHostname sets the hostname on the primary node.
func (h *Hop) SetHostname(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pn := h.primaryNodeLocked()
	if pn != nil {
		pn.SetHostname(name)
	}
}

// AvgRTT returns the primary node's average RTT.
func (h *Hop) AvgRTT() time.Duration {
	h.mu.RLock()
	defer h.mu.RUnlock()
	pn := h.primaryNodeLocked()
	if pn == nil {
		return 0
	}
	return pn.AvgRTT()
}

// StDev returns the primary node's RTT standard deviation in milliseconds.
func (h *Hop) StDev() float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	pn := h.primaryNodeLocked()
	if pn == nil {
		return 0
	}
	return pn.StDev()
}

// LossPercent returns per-probe loss: TTL-limited probes that got no reply
// over probes sent, across completed rounds. The open round is excluded, so a
// live view does not show probes still awaiting their replies as lost. A
// reply that arrives after its round closed is credited when the next round
// closes.
func (h *Hop) LossPercent() float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lossPercentLocked()
}

func (h *Hop) lossPercentLocked() float64 {
	if h.closedSent == 0 {
		return 0
	}
	lost := h.closedSent - h.closedRecvd
	if lost < 0 {
		lost = 0 // duplicated replies
	}
	return float64(lost) / float64(h.closedSent) * 100
}

// MarkTarget records that the node with the given address at this TTL is the
// trace's destination (see PathNode.MarkTarget).
func (h *Hop) MarkTarget(ip net.IP) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, n := range h.Nodes {
		if n.IP.Equal(ip) {
			n.MarkTarget()
		}
	}
}

// RateLimitState is the hop's rate-limit verdict: whether the loss of
// TTL-limited probes at this hop is the routers declining to answer them
// rather than packets being lost.
//
// It compares every TTL-limited probe sent to this hop distance, of every
// type, with every direct echo sent to the hop's nodes. The comparison has to
// be made for the hop as a whole: a TTL-limited probe is addressed to a
// distance, not to a node, so one node cannot tell which probes it "lost". A
// hop that answers echoes but not its TTL probes is rate-limiting or filtering
// them. A hop that loses both at the same rate has real loss.
//
// The destination's hop is the exception (see verdictBasis): it is judged on
// the target alone, so a router that answers at the same TTL does not dilute
// the target's loss.
func (h *Hop) RateLimitState() RateLimitState {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.rateLimitStateLocked()
}

func (h *Hop) rateLimitStateLocked() RateLimitState {
	// The destination is tested like any other hop: it throttles the Port
	// Unreachable errors it sends in answer to UDP probes just as a router
	// throttles Time Exceeded. Real loss on the way to it costs echoes too,
	// and is not flagged. Only the counts differ (verdictBasis).
	b := h.basisLocked()
	verdict := b.verdict()
	if verdict == RateLimitUndetermined {
		// Too few echo samples. A hop that has had a verdict keeps it until
		// its echo totals are enough for a new one.
		return b.held.verdict
	}
	return verdict
}

// verdictRecord is a rate-limit verdict as it was taken, with the echo loss
// that went with it and the counts both were made from.
type verdictRecord struct {
	verdict     RateLimitState // Undetermined until the first verdict
	echoLoss    float64
	echoSent    int
	echoRecvd   int
	probesSent  int
	probesRecvd int
}

// verdictBasis is what a hop's verdict and headline loss are made from: the
// TTL-limited probes sent and answered, as of the last closed round, the
// echoes sent and answered, and the record of the last verdict taken on the
// same basis.
type verdictBasis struct {
	probesSent, probesRecvd int
	echoSent, echoRecvd     int
	held                    *verdictRecord
	// target is true when the counts are the target's alone (targetBasisLocked).
	target bool
}

func (b verdictBasis) verdict() RateLimitState {
	return rateLimitVerdict(b.probesSent, b.probesSent-b.probesRecvd, b.echoSent, b.echoSent-b.echoRecvd)
}

// isHeld reports whether the verdict made on this basis is a held one: there
// has been a verdict, and the echo totals are now below the samples a new one
// needs.
func (b verdictBasis) isHeld() bool {
	return b.echoSent < rateLimitMinEchoSamples && b.held.verdict != RateLimitUndetermined
}

// basisLocked returns the counts the hop is judged on. Every hop but the
// destination's is judged as a hop of the path (hopBasisLocked). The
// destination's hop is judged on the target alone (targetBasisLocked) once a
// node there is marked as the target: at the destination's TTL a router can
// answer the probes of a type whose path is longer (in UDP mode, the ICMP TTL
// probe), and its echoes and replies would dilute the target's loss.
func (h *Hop) basisLocked() verdictBasis {
	if h.destination {
		if b, ok := h.targetBasisLocked(); ok {
			return b
		}
	}
	return h.hopBasisLocked()
}

// hopBasisLocked is the basis of a hop of the path: every TTL-limited probe
// sent to this TTL, of every type, against the hop's echo totals
// (echoTotalsLocked).
func (h *Hop) hopBasisLocked() verdictBasis {
	b := verdictBasis{probesSent: h.closedSent, probesRecvd: h.closedRecvd, held: &h.held}
	b.echoSent, b.echoRecvd = h.echoTotalsLocked()
	return b
}

// targetBasisLocked is the basis of the target alone: only the echoes sent to
// the node marked as the target, every one of them (there is no other node
// whose answers could set the target's silence against), and only the
// TTL-limited probes of the types the target has answered at this TTL. A
// probe type the target never answers here is answered, or lost, by someone
// else. ok is false when no node of the hop is marked as the target. If the
// target has answered only echoes here, every probe type counts.
func (h *Hop) targetBasisLocked() (b verdictBasis, ok bool) {
	icmp, primary := false, false
	for _, n := range h.Nodes {
		if !n.IsTarget() {
			continue
		}
		ok = true
		e := n.EchoStats()
		b.echoSent += e.Sent
		b.echoRecvd += e.Received
		icmp = icmp || n.ICMPTTLStats().Received > 0
		primary = primary || n.PrimaryStats().Received > 0
	}
	if !ok {
		return verdictBasis{}, false
	}
	if !icmp && !primary {
		icmp, primary = true, true
	}
	if icmp {
		b.probesSent += h.closedICMPSent
		b.probesRecvd += h.closedICMPRecvd
	}
	if primary {
		b.probesSent += h.closedSent - h.closedICMPSent
		b.probesRecvd += h.closedRecvd - h.closedICMPRecvd
	}
	b.held = &h.heldTarget
	b.target = true
	return b, true
}

// setDestination records whether this hop is the destination's (see
// Table.EndRound).
func (h *Hop) setDestination(dest bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.destination = dest
}

// holdVerdictLocked records the hop's verdict, and the echo loss that goes
// with it, while the hop has enough echo samples for one. The echo totals can
// fall back below that number: a node that ignores ping drops out of them
// when it has ignored echoSilentAfter echoes, or when another node of the hop
// first answers one. The record is then what the hop shows until the new
// totals are enough. It is taken when a round closes and before every echo
// answer, the event that changes the totals. Callers hold the write lock.
//
// The record is kept for both ways a hop can be judged (see basisLocked), so
// a hop that gains or loses the destination has its own last verdict to hold.
func (h *Hop) holdVerdictLocked() {
	hold(h.hopBasisLocked())
	if b, ok := h.targetBasisLocked(); ok {
		hold(b)
	}
}

// hold records b's verdict in b.held, if the counts are enough for one.
func hold(b verdictBasis) {
	verdict := b.verdict()
	if verdict == RateLimitUndetermined {
		return
	}
	*b.held = verdictRecord{
		verdict:     verdict,
		echoLoss:    echoLossPercent(b.echoSent, b.echoRecvd),
		echoSent:    b.echoSent,
		echoRecvd:   b.echoRecvd,
		probesSent:  b.probesSent,
		probesRecvd: b.probesRecvd,
	}
}

// verdictHeldLocked reports whether the verdict the hop shows is a held one:
// it has had a verdict, and its echo totals are now below the samples a new
// one needs.
func (h *Hop) verdictHeldLocked() bool {
	return h.basisLocked().isHeld()
}

// verdictProbeCountsLocked returns the TTL-probe counts the hop's verdict was
// made from: the held ones while the verdict is held, else the current ones.
func (h *Hop) verdictProbeCountsLocked() (sent, received int) {
	b := h.basisLocked()
	if b.isHeld() {
		return b.held.probesSent, b.held.probesRecvd
	}
	return b.probesSent, b.probesRecvd
}

// verdictEchoTotalsLocked returns the echo totals the verdict was made from:
// the held ones while the verdict is held, else the current ones.
func (h *Hop) verdictEchoTotalsLocked() (sent, received int) {
	b := h.basisLocked()
	if b.isHeld() {
		return b.held.echoSent, b.held.echoRecvd
	}
	return b.echoSent, b.echoRecvd
}

// VerdictHeld reports whether the hop is holding an earlier rate-limit
// verdict: its echo totals fell below the samples a verdict needs after it had
// one, and the new totals are not yet enough. The verdict it shows was then
// made from VerdictEchoTotals and VerdictProbeCounts, not from the current
// counts.
func (h *Hop) VerdictHeld() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.verdictHeldLocked()
}

// VerdictProbeCounts returns the TTL-limited probes sent to this TTL, and the
// replies received, that the hop's rate-limit verdict was made from. While
// the hop holds an earlier verdict (see VerdictHeld) they are the counts of
// the moment that verdict was taken; otherwise they equal ProbeCounts, except
// on the destination's hop, where they are the probes of the types the target
// has answered (see basisLocked). With VerdictEchoTotals they are everything
// the verdict was computed from.
func (h *Hop) VerdictProbeCounts() (sent, received int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.verdictProbeCountsLocked()
}

// beforeEchoAnswerLocked runs before an echo answer from node is recorded.
// It holds the verdict as it stands, because the answer can take echoes out
// of the totals, and tells a node that was being left out (another node of
// the hop answers echo) that it now joins them.
func (h *Hop) beforeEchoAnswerLocked(node *PathNode) {
	h.holdVerdictLocked()
	for _, n := range h.Nodes {
		if n != node && n.EchoStats().Received > 0 {
			node.joinEchoTotals()
			return
		}
	}
}

// echoLossPercent is the share of sent echoes that were not answered.
func echoLossPercent(sent, received int) float64 {
	lost := sent - received
	if lost < 0 {
		lost = 0
	}
	return float64(lost) / float64(sent) * 100
}

// echoTotalsLocked sums the echo probes sent to, and answered by, this hop's
// nodes. When at least one node answers echo, a node that ignores ping is left
// out (PathNode.echoTotals): the answering nodes measure the path and the
// silent one only its own filter. When no node has answered an echo there is
// nothing to set the silence against, and every echo counts as lost.
func (h *Hop) echoTotalsLocked() (sent, received int) {
	answered := false
	for _, n := range h.Nodes {
		s := n.EchoStats()
		sent += s.Sent
		received += s.Received
		answered = answered || s.Received > 0
	}
	if !answered {
		return sent, received
	}
	sent, received = 0, 0
	for _, n := range h.Nodes {
		s, r := n.echoTotals()
		sent += s
		received += r
	}
	return sent, received
}

// HeadlineLoss is the loss figure every view shows for this hop, and whether
// it is echo-derived. When the hop is judged rate-limited, its TTL probe loss
// is an artifact of routers not answering, so the loss of direct echoes to
// the hop's nodes is reported instead (shown as "~N%†"). Otherwise it is
// LossPercent, except on the destination's hop, where both are the target's
// alone (see basisLocked).
func (h *Hop) HeadlineLoss() (loss float64, rateLimited bool) {
	loss, kind := h.Loss()
	return loss, kind == LossRateLimited
}

// LossKind says what a hop's loss figure is.
type LossKind int

const (
	// LossMeasured is TTL-probe loss with nothing in doubt: either there is
	// none, or there are enough echo samples to say it is not rate limiting.
	LossMeasured LossKind = iota
	// LossRateLimited is echo loss, reported because the hop's TTL-probe loss
	// is judged to be rate limiting.
	LossRateLimited
	// LossProvisional is TTL-probe loss on a hop that does not yet have
	// enough echo samples for a verdict. It may be rate limiting; it is not
	// yet a measurement of the path.
	LossProvisional
)

// Loss is the loss figure every view shows for this hop, and what kind of
// figure it is. FormatLoss renders the pair.
func (h *Hop) Loss() (pct float64, kind LossKind) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lossLocked()
}

func (h *Hop) lossLocked() (pct float64, kind LossKind) {
	b := h.basisLocked()
	verdict := h.rateLimitStateLocked()
	if verdict == RateLimitYes {
		if b.echoSent < rateLimitMinEchoSamples {
			// The verdict is a held one: so is its echo loss.
			return b.held.echoLoss, LossRateLimited
		}
		return echoLossPercent(b.echoSent, b.echoRecvd), LossRateLimited
	}
	if b.probesSent == 0 {
		return 0, LossMeasured
	}
	lost := b.probesSent - b.probesRecvd
	if lost <= 0 {
		return 0, LossMeasured
	}
	pct = float64(lost) / float64(b.probesSent) * 100
	if verdict == RateLimitUndetermined && len(h.Nodes) > 0 {
		return pct, LossProvisional
	}
	return pct, LossMeasured
}

// SettledLoss is the loss figure for decisions that must not act on a number
// still in doubt, such as alerting: Loss, with provisional loss counted as
// none.
func (h *Hop) SettledLoss() float64 {
	pct, kind := h.Loss()
	if kind == LossProvisional {
		return 0
	}
	return pct
}

// FormatLoss renders a loss figure the same way in every view: "12.3%" for a
// measurement, "~0%†" for echo loss on a rate-limited hop, "85.7%?" while the
// verdict is not yet possible.
func FormatLoss(pct float64, kind LossKind) string {
	switch kind {
	case LossRateLimited:
		return fmt.Sprintf("~%.0f%%†", pct)
	case LossProvisional:
		return fmt.Sprintf("%.1f%%?", pct)
	default:
		return fmt.Sprintf("%.1f%%", pct)
	}
}

// ProbeCounts returns the TTL-limited probes sent to this TTL and the replies
// received, as of the last closed round. These are the figures LossPercent is
// computed from. received can exceed sent if a reply was duplicated.
func (h *Hop) ProbeCounts() (sent, received int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.closedSent, h.closedRecvd
}

// ProbeCountsByType splits ProbeCounts by probe type: the ICMP TTL probes
// sent to this TTL and their replies, and the same for the primary (UDP or
// TCP) probes, as of the last closed round.
func (h *Hop) ProbeCountsByType() (icmpSent, icmpRecvd, primarySent, primaryRecvd int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.closedICMPSent, h.closedICMPRecvd,
		h.closedSent - h.closedICMPSent, h.closedRecvd - h.closedICMPRecvd
}

// EchoCounts returns the direct echoes sent to this hop's nodes and the
// answers received, and the part of both that counts toward the hop's
// rate-limit verdict and echo loss: a node that ignores ping is left out of
// the counted totals (see echoTotalsLocked), and on the destination's hop
// only the target's echoes count (see basisLocked).
func (h *Hop) EchoCounts() (sent, received, countedSent, countedReceived int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, n := range h.Nodes {
		s := n.EchoStats()
		sent += s.Sent
		received += s.Received
	}
	b := h.basisLocked()
	return sent, received, b.echoSent, b.echoRecvd
}

// VerdictEchoTotals returns the echo totals the hop's rate-limit verdict and
// headline loss were made from: echoes sent to the hop's nodes and answers
// received, without the nodes left out for ignoring ping (see EchoIgnored).
// While the hop holds an earlier verdict because those totals fell below the
// samples a verdict needs, they are the totals the held verdict was made
// from, so a rate-limited headline is always the loss of the totals returned.
// On the destination's hop they are the echoes sent to the target and its
// answers (see basisLocked).
func (h *Hop) VerdictEchoTotals() (sent, received int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.verdictEchoTotalsLocked()
}

// EchoIgnored reports whether the node with the given address is left out of
// the hop's echo totals: it has answered none of its first echoSilentAfter
// echoes while another node of the hop answers echo. A node that starts
// answering is no longer left out; the echoes it ignored before stay out of
// the totals. On the destination's hop no node is: the target's echoes are
// all counted, and the other nodes' echoes are not the target's.
func (h *Hop) EchoIgnored(ip net.IP) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.basisLocked().target {
		return false
	}
	ignores, othersAnswer := false, false
	for _, n := range h.Nodes {
		s := n.EchoStats()
		if n.IP.Equal(ip) {
			ignores = s.Received == 0 && s.Sent >= echoSilentAfter
		} else if s.Received > 0 {
			othersAnswer = true
		}
	}
	return ignores && othersAnswer
}

// LossSnapshot is a hop's loss figures read together under one lock, so they
// agree with each other even while probes are being recorded.
type LossSnapshot struct {
	Loss float64
	Kind LossKind
	// SettledLoss is SettledLoss: Loss, with provisional loss counted as none.
	SettledLoss    float64
	RateLimit      RateLimitState
	EchoSent       int // the totals the verdict and headline were made from
	EchoReceived   int
	EchoIgnoredIPs map[string]bool // keyed by net.IP.String()
	// VerdictHeld is true while the hop holds an earlier verdict, and the
	// VerdictProbes counts are the TTL-probe counts the verdict was made from
	// (see VerdictHeld and VerdictProbeCounts).
	VerdictHeld           bool
	VerdictProbesSent     int
	VerdictProbesReceived int
	// ProbesSent and ProbesReceived are ProbeCounts and LossPct is
	// LossPercent, read with the figures above. Rounds is the rounds those
	// counts cover: the closed ones. Unlike Hop.Rounds it leaves out a round
	// still waiting for replies, as the probe counts do.
	ProbesSent     int
	ProbesReceived int
	LossPct        float64
	Rounds         int
}

// LossSnapshot returns the hop's headline loss, its kind, its settled loss
// (see SettledLoss), its rate-limit verdict, the echo totals they were made from (see VerdictEchoTotals), the
// nodes left out of those totals (see EchoIgnored), whether the verdict is a
// held one and the TTL-probe counts it was made from (see VerdictHeld and
// VerdictProbeCounts), and its probe counts, per-probe loss and rounds (see
// ProbeCounts and LossPercent; Rounds is the closed rounds), all from one
// state.
func (h *Hop) LossSnapshot() LossSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var snap LossSnapshot
	snap.ProbesSent, snap.ProbesReceived = h.closedSent, h.closedRecvd
	snap.LossPct = h.lossPercentLocked()
	snap.Rounds = h.closedRounds
	snap.Loss, snap.Kind = h.lossLocked()
	if snap.Kind != LossProvisional {
		snap.SettledLoss = snap.Loss
	}
	snap.RateLimit = h.rateLimitStateLocked()
	snap.EchoSent, snap.EchoReceived = h.verdictEchoTotalsLocked()
	snap.VerdictHeld = h.verdictHeldLocked()
	snap.VerdictProbesSent, snap.VerdictProbesReceived = h.verdictProbeCountsLocked()
	snap.EchoIgnoredIPs = make(map[string]bool)
	if h.basisLocked().target {
		return snap // no node is left out of the target's totals (EchoIgnored)
	}
	for i, n := range h.Nodes {
		if n.IP == nil {
			continue
		}
		s := n.EchoStats()
		if s.Received != 0 || s.Sent < echoSilentAfter {
			continue
		}
		for j, o := range h.Nodes {
			if i != j && o.EchoStats().Received > 0 {
				snap.EchoIgnoredIPs[n.IP.String()] = true
				break
			}
		}
	}
	return snap
}

// String names the kind as the JSON export does (loss_kind).
func (k LossKind) String() string {
	switch k {
	case LossRateLimited:
		return "rate_limited"
	case LossProvisional:
		return "provisional"
	default:
		return "measured"
	}
}

// String names the verdict as the JSON export does (rate_limited).
func (s RateLimitState) String() string {
	switch s {
	case RateLimitYes:
		return "yes"
	case RateLimitNo:
		return "no"
	default:
		return "undetermined"
	}
}

// GetMinRTT returns the primary node's minimum RTT.
func (h *Hop) GetMinRTT() time.Duration {
	h.mu.RLock()
	defer h.mu.RUnlock()
	pn := h.primaryNodeLocked()
	if pn == nil {
		return 0
	}
	return pn.GetMinRTT()
}

// GetMaxRTT returns the primary node's maximum RTT.
func (h *Hop) GetMaxRTT() time.Duration {
	h.mu.RLock()
	defer h.mu.RUnlock()
	pn := h.primaryNodeLocked()
	if pn == nil {
		return 0
	}
	return pn.GetMaxRTT()
}

// GetLastRTT returns the primary node's last RTT.
func (h *Hop) GetLastRTT() time.Duration {
	h.mu.RLock()
	defer h.mu.RUnlock()
	pn := h.primaryNodeLocked()
	if pn == nil {
		return 0
	}
	return pn.GetLastRTT()
}

// Rounds returns the number of probe rounds sent to this hop (thread-safe).
// This is what the user-facing Rnd column displays.
func (h *Hop) Rounds() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.roundsSent
}

// Reset clears all nodes and zeroes counters, preserving TTL. If a round
// was sent to this hop and has not closed, its replies may still be in
// flight; they would be credited against the zeroed sent count and hide later
// loss, so they are dropped until the round closes. The open round is not
// counted at all: neither as sent nor as lost.
func (h *Hop) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.draining = h.roundsSent > h.closedRounds
	h.closedRounds = 0
	h.Nodes = nil
	h.roundsSent = 0
	h.probesSent = 0
	h.probesRecvd = 0
	h.closedSent = 0
	h.closedRecvd = 0
	h.icmpSent, h.icmpRecvd = 0, 0
	h.closedICMPSent, h.closedICMPRecvd = 0, 0
	h.openICMP = 0
	h.openPrimary = 0
	h.held = verdictRecord{}
	h.heldTarget = verdictRecord{}
}

// Table is a thread-safe collection of hops indexed by TTL.
type Table struct {
	maxHops int
	hops    map[int]*Hop
	mu      sync.RWMutex

	// dest follows which TTL is the destination's (destination.go).
	dest dest.Tracker
}

// NewTable creates a hop table with the given max hop limit.
func NewTable(maxHops int) *Table {
	return &Table{
		maxHops: maxHops,
		hops:    make(map[int]*Hop),
	}
}

// Get returns the hop at ttl, or nil if none exists. Unlike GetOrCreate it
// never creates a row, so lookups for unseen TTLs stay invisible.
func (t *Table) Get(ttl int) *Hop {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.hops[ttl]
}

// GetOrCreate returns the hop for the given TTL, creating it if needed.
func (t *Table) GetOrCreate(ttl int) *Hop {
	t.mu.Lock()
	defer t.mu.Unlock()

	if h, ok := t.hops[ttl]; ok {
		return h
	}
	h := NewHop(ttl)
	t.hops[ttl] = h
	return h
}

// MarkRoundSent records that a round of probes TTL-limited probes was sent to
// the given TTL. Implements probe.RoundMarker.
func (t *Table) MarkRoundSent(ttl int, probes int) {
	t.GetOrCreate(ttl).MarkRoundSentProbes(probes)
}

// MarkRoundSentByType records the TTL-limited probes of each type the tracer
// wrote for the given TTL this round. Implements probe.RoundTypeMarker. A TTL
// for which nothing left gets no hop row: nothing was sent to it.
func (t *Table) MarkRoundSentByType(ttl int, icmpTTL int, primary int) {
	if icmpTTL <= 0 && primary <= 0 {
		return
	}
	t.GetOrCreate(ttl).MarkRoundSentByType(icmpTTL, primary)
}

// Snapshot returns a sorted slice of hops from TTL 1 to MaxTTLSeen.
func (t *Table) Snapshot() []*Hop {
	t.mu.RLock()
	defer t.mu.RUnlock()

	max := t.maxTTLSeenLocked()
	result := make([]*Hop, 0, max)
	for ttl := 1; ttl <= max; ttl++ {
		if h, ok := t.hops[ttl]; ok {
			result = append(result, h)
		}
	}
	return result
}

// EchoNode is a node to send direct echoes to, and the hop it belongs to.
type EchoNode struct {
	IP  net.IP
	TTL int
}

// EchoNodes lists the nodes of the path: every known node at every hop up to
// and including the destination's. Hops beyond the destination are left out.
// They exist because the first round probes every TTL before the
// destination's distance is known, and the destination answers each probe
// that reaches it; sending echoes to those entries would multiply the echo
// traffic at the destination by the number of unused TTLs.
func (t *Table) EchoNodes() []EchoNode {
	target := t.TargetTTL()
	var out []EchoNode
	for _, h := range t.Snapshot() {
		if target > 0 && h.TTL > target {
			break
		}
		for _, n := range h.GetNodes() {
			if ip := n.GetIP(); ip != nil {
				out = append(out, EchoNode{IP: ip, TTL: h.TTL})
			}
		}
	}
	return out
}

// MaxTTLSeen returns the highest TTL that has been recorded.
func (t *Table) MaxTTLSeen() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.maxTTLSeenLocked()
}

// ResetAll resets stats on all hops. The destination's TTL is kept: a reset
// clears what was measured, not where the target is, and the tracer keeps
// probing up to it.
func (t *Table) ResetAll() {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, h := range t.hops {
		h.Reset()
	}
}

func (t *Table) maxTTLSeenLocked() int {
	max := 0
	for ttl := range t.hops {
		if ttl > max {
			max = ttl
		}
	}
	return max
}
