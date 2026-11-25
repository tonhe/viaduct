// Package hop tracks per-hop results, RTT stats, and path state.
package hop

import (
	"fmt"
	"net"
	"sync"
	"time"
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

	// draining is set by Reset when this hop's round was open at the reset.
	// Replies still on their way answer probes whose sends Reset discarded,
	// so they are dropped until that round closes or a new round is sent.
	draining bool

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
// to this TTL this round (ICMP TTL plus every primary flow). Called once per
// hop per round (probe.RoundMarker via Table).
func (h *Hop) MarkRoundSentProbes(probes int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// A new round's probes are out: replies from here on may answer them.
	h.draining = false
	h.roundsSent++
	if probes > 0 {
		h.probesSent += probes
	}
}

// MarkRoundEnd closes a probe round: probes sent so far and replies received
// so far become the figures loss is computed from, then stability tracking
// advances for all nodes. A round in which nothing was sent to this hop
// (the tracer was paused, or the hop lies beyond the destination) updates the
// figures but is not a round for the nodes: it adds no probes, sparkline cell
// or stability slot.
func (h *Hop) MarkRoundEnd() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.draining = false
	if h.roundsSent == h.closedRounds {
		h.closedSent = h.probesSent
		h.closedRecvd = h.probesRecvd
		return
	}
	h.closedRounds = h.roundsSent
	// Probes sent to this hop in the round being closed. A round is one ICMP
	// TTL probe followed by the UDP or TCP flows, so the first is the ICMP
	// probe and the rest are primary probes.
	inRound := h.probesSent - h.closedSent
	icmpSent, primarySent := 0, 0
	if inRound > 0 {
		icmpSent, primarySent = 1, inRound-1
	}
	h.closedSent = h.probesSent
	h.closedRecvd = h.probesRecvd
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
	return rateLimitVerdict(h.closedSent, h.closedSent-h.closedRecvd, echoSent, echoSent-echoRecvd)
}

// echoTotalsLocked sums the echo probes sent to, and answered by, this hop's
// nodes. A node that ignores ping is left out (PathNode.echoTotals); if every
// node is, the hop has no echo samples and its verdict stays Undetermined.
func (h *Hop) echoTotalsLocked() (sent, received int) {
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
	verdict := h.rateLimitStateLocked()
	if verdict == RateLimitYes {
		sent, received := h.echoTotalsLocked()
		lost := sent - received
		if lost < 0 {
			lost = 0
		}
		return float64(lost) / float64(sent) * 100, LossRateLimited
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
}

// Table is a thread-safe collection of hops indexed by TTL.
type Table struct {
	maxHops int
	hops    map[int]*Hop
	mu      sync.RWMutex
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

// TargetTTL returns the destination's distance: the lowest TTL at which a
// node marked as the target has answered, or 0 while it has not.
func (t *Table) TargetTTL() int {
	for _, h := range t.Snapshot() {
		for _, n := range h.GetNodes() {
			if n.IsTarget() {
				return h.TTL
			}
		}
	}
	return 0
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

// ResetAll resets stats on all hops.
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
