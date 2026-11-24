// Package hop tracks per-hop results, RTT stats, and path state.
package hop

import (
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

	mu sync.RWMutex
}

// NewHop creates a hop for the given TTL.
func NewHop(ttl int) *Hop {
	return &Hop{TTL: ttl}
}

// AddSample records an RTT measurement from the given IP with a flow identifier.
func (h *Hop) AddSample(ip net.IP, rtt time.Duration, flowID int, kind ProbeKind) {
	h.mu.Lock()
	defer h.mu.Unlock()

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
	h.roundsSent++
	if probes > 0 {
		h.probesSent += probes
	}
}

// MarkRoundEnd closes a probe round: probes sent so far and replies received
// so far become the figures loss is computed from, then stability tracking
// advances for all nodes.
func (h *Hop) MarkRoundEnd() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closedSent = h.probesSent
	h.closedRecvd = h.probesRecvd
	for _, n := range h.Nodes {
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

// HeadlineLoss is the loss figure every view shows for this hop, and whether
// it is echo-derived. When the hop's primary node is judged rate-limited, the
// TTL probe loss is an artifact of the router throttling its replies, so the
// loss of direct echoes to that node is reported instead (shown as "~N%†").
// Otherwise it is LossPercent.
func (h *Hop) HeadlineLoss() (loss float64, rateLimited bool) {
	if pn := h.PrimaryNode(); pn != nil && pn.RateLimitState() == RateLimitYes {
		echo := pn.EchoStats()
		return echo.ProbeLossPercent(), true
	}
	return h.LossPercent(), false
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

// Reset clears all nodes and zeroes counters, preserving TTL.
func (h *Hop) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
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
