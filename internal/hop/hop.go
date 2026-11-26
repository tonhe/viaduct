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

	// heldVerdict is the hop's last rate-limit verdict, and heldEchoLoss the
	// echo loss that went with it. Once a hop has had a verdict it keeps one:
	// while its echo totals are below the samples a verdict needs, these stand
	// in (see holdVerdictLocked). Undetermined until the first verdict.
	// heldEchoSent and heldEchoRecvd are the echo totals both were made from,
	// and heldProbesSent and heldProbesRecvd the TTL-probe counts.
	heldVerdict     RateLimitState
	heldEchoLoss    float64
	heldEchoSent    int
	heldEchoRecvd   int
	heldProbesSent  int
	heldProbesRecvd int

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
// The destination is judged like any other hop.
func (h *Hop) RateLimitState() RateLimitState {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.rateLimitStateLocked()
}

func (h *Hop) rateLimitStateLocked() RateLimitState {
	// The destination is judged like any other hop: it throttles the Port
	// Unreachable errors it sends in answer to UDP probes just as a router
	// throttles Time Exceeded. Real loss on the way to it costs echoes too,
	// and is not flagged.
	echoSent, echoRecvd := h.echoTotalsLocked()
	verdict := rateLimitVerdict(h.closedSent, h.closedSent-h.closedRecvd, echoSent, echoSent-echoRecvd)
	if verdict == RateLimitUndetermined {
		// Too few echo samples. A hop that has had a verdict keeps it until
		// its echo totals are enough for a new one.
		return h.heldVerdict
	}
	return verdict
}

// holdVerdictLocked records the hop's verdict, and the echo loss that goes
// with it, while the hop has enough echo samples for one. The echo totals can
// fall back below that number: a node that ignores ping drops out of them
// when it has ignored echoSilentAfter echoes, or when another node of the hop
// first answers one. The record is then what the hop shows until the new
// totals are enough. It is taken when a round closes and before every echo
// answer, the event that changes the totals. Callers hold the write lock.
func (h *Hop) holdVerdictLocked() {
	sent, received := h.echoTotalsLocked()
	verdict := rateLimitVerdict(h.closedSent, h.closedSent-h.closedRecvd, sent, sent-received)
	if verdict == RateLimitUndetermined {
		return
	}
	h.heldVerdict = verdict
	h.heldEchoLoss = echoLossPercent(sent, received)
	h.heldEchoSent, h.heldEchoRecvd = sent, received
	h.heldProbesSent, h.heldProbesRecvd = h.closedSent, h.closedRecvd
}

// verdictHeldLocked reports whether the verdict the hop shows is a held one:
// it has had a verdict, and its echo totals are now below the samples a new
// one needs.
func (h *Hop) verdictHeldLocked() bool {
	sent, _ := h.echoTotalsLocked()
	return sent < rateLimitMinEchoSamples && h.heldVerdict != RateLimitUndetermined
}

// verdictProbeCountsLocked returns the TTL-probe counts the hop's verdict was
// made from: the held ones while the verdict is held, else the current ones.
func (h *Hop) verdictProbeCountsLocked() (sent, received int) {
	if h.verdictHeldLocked() {
		return h.heldProbesSent, h.heldProbesRecvd
	}
	return h.closedSent, h.closedRecvd
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
// the moment that verdict was taken; otherwise they equal ProbeCounts. With
// VerdictEchoTotals they are everything the verdict was computed from.
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
// LossPercent.
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
	verdict := h.rateLimitStateLocked()
	if verdict == RateLimitYes {
		sent, received := h.echoTotalsLocked()
		if sent < rateLimitMinEchoSamples {
			// The verdict is a held one: so is its echo loss.
			return h.heldEchoLoss, LossRateLimited
		}
		return echoLossPercent(sent, received), LossRateLimited
	}
	if h.closedSent == 0 {
		return 0, LossMeasured
	}
	lost := h.closedSent - h.closedRecvd
	if lost <= 0 {
		return 0, LossMeasured
	}
	pct = float64(lost) / float64(h.closedSent) * 100
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
// the counted totals (see echoTotalsLocked).
func (h *Hop) EchoCounts() (sent, received, countedSent, countedReceived int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, n := range h.Nodes {
		s := n.EchoStats()
		sent += s.Sent
		received += s.Received
	}
	countedSent, countedReceived = h.echoTotalsLocked()
	return sent, received, countedSent, countedReceived
}

// VerdictEchoTotals returns the echo totals the hop's rate-limit verdict and
// headline loss were made from: echoes sent to the hop's nodes and answers
// received, without the nodes left out for ignoring ping (see EchoIgnored).
// While the hop holds an earlier verdict because those totals fell below the
// samples a verdict needs, they are the totals the held verdict was made
// from, so a rate-limited headline is always the loss of the totals returned.
func (h *Hop) VerdictEchoTotals() (sent, received int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	sent, received = h.echoTotalsLocked()
	if sent < rateLimitMinEchoSamples && h.heldVerdict != RateLimitUndetermined {
		return h.heldEchoSent, h.heldEchoRecvd
	}
	return sent, received
}

// EchoIgnored reports whether the node with the given address is left out of
// the hop's echo totals: it has answered none of its first echoSilentAfter
// echoes while another node of the hop answers echo. A node that starts
// answering is no longer left out; the echoes it ignored before stay out of
// the totals.
func (h *Hop) EchoIgnored(ip net.IP) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
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
	Loss           float64
	Kind           LossKind
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
}

// LossSnapshot returns the hop's headline loss, its kind, its rate-limit
// verdict, the echo totals they were made from (see VerdictEchoTotals), the
// nodes left out of those totals (see EchoIgnored), whether the verdict is a
// held one and the TTL-probe counts it was made from (see VerdictHeld and
// VerdictProbeCounts), all from one state.
func (h *Hop) LossSnapshot() LossSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var snap LossSnapshot
	snap.Loss, snap.Kind = h.lossLocked()
	snap.RateLimit = h.rateLimitStateLocked()
	snap.EchoSent, snap.EchoReceived = h.echoTotalsLocked()
	if snap.EchoSent < rateLimitMinEchoSamples && h.heldVerdict != RateLimitUndetermined {
		snap.EchoSent, snap.EchoReceived = h.heldEchoSent, h.heldEchoRecvd
	}
	snap.VerdictHeld = h.verdictHeldLocked()
	snap.VerdictProbesSent, snap.VerdictProbesReceived = h.verdictProbeCountsLocked()
	snap.EchoIgnoredIPs = make(map[string]bool)
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
	h.heldVerdict = RateLimitUndetermined
	h.heldEchoLoss = 0
	h.heldEchoSent, h.heldEchoRecvd = 0, 0
	h.heldProbesSent, h.heldProbesRecvd = 0, 0
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
