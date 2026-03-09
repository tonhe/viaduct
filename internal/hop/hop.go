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
	sentTotal int // total probes sent to this TTL (all flows)

	mu sync.RWMutex
}

// NewHop creates a hop for the given TTL.
func NewHop(ttl int) *Hop {
	return &Hop{TTL: ttl}
}

// AddSample records an RTT measurement from the given IP with a flow identifier.
func (h *Hop) AddSample(ip net.IP, rtt time.Duration, flowID int) {
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
	node.AddSample(rtt, flowID)
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

// MarkRoundEnd advances stability tracking for all nodes.
func (h *Hop) MarkRoundEnd() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, n := range h.Nodes {
		n.AdvanceRound()
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

// LossPercent returns the packet loss percentage across all nodes.
// Loss = (sentTotal - totalReceivedAllNodes) / sentTotal * 100
func (h *Hop) LossPercent() float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.sentTotal == 0 {
		return 0
	}
	totalReceived := 0
	for _, n := range h.Nodes {
		totalReceived += n.GetReceived()
	}
	return float64(h.sentTotal-totalReceived) / float64(h.sentTotal) * 100
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

// GetSent returns the total sent count for this hop (thread-safe).
func (h *Hop) GetSent() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sentTotal
}

// IncrementSent increments the total sent probe count.
func (h *Hop) IncrementSent() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sentTotal++
}

// Reset clears all nodes and zeroes sentTotal, preserving TTL.
func (h *Hop) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Nodes = nil
	h.sentTotal = 0
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

// IncrementSent increments the sent count for a given TTL.
// Implements probe.SentCounter.
func (t *Table) IncrementSent(ttl int) {
	t.GetOrCreate(ttl).IncrementSent()
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
