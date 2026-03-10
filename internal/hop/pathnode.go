package hop

import (
	"math"
	"net"
	"sort"
	"sync"
	"time"
)

const stabilityWindowSize = 50

// PathNode tracks per-IP statistics for ECMP multipath discovery.
// It records RTT samples, flow IDs, loss, and path stability over time.
type PathNode struct {
	IP       net.IP
	Hostname string

	sent     int
	received int
	minRTT   time.Duration
	maxRTT   time.Duration
	lastRTT  time.Duration
	totalRTT time.Duration
	mean     float64 // running mean in ms (Welford's)
	m2       float64 // running sum of squared differences in ms² (Welford's)

	asnNum int    // Autonomous System Number (0 = unknown)
	asnOrg string // AS organization name

	flowIDs   map[int]struct{}
	stability [stabilityWindowSize]bool
	stabIdx   int
	stabCount int

	mu sync.Mutex
}

// NewPathNode creates a PathNode for the given IP address.
func NewPathNode(ip net.IP) *PathNode {
	return &PathNode{
		IP:      ip,
		flowIDs: make(map[int]struct{}),
	}
}

// AddSample records an RTT measurement and the associated flow ID.
func (pn *PathNode) AddSample(rtt time.Duration, flowID int) {
	pn.mu.Lock()
	defer pn.mu.Unlock()

	pn.received++
	pn.lastRTT = rtt
	pn.totalRTT += rtt

	if pn.received == 1 || rtt < pn.minRTT {
		pn.minRTT = rtt
	}
	if rtt > pn.maxRTT {
		pn.maxRTT = rtt
	}

	// Welford's online algorithm for variance
	ms := float64(rtt.Microseconds()) / 1000.0
	delta := ms - pn.mean
	pn.mean += delta / float64(pn.received)
	delta2 := ms - pn.mean
	pn.m2 += delta * delta2

	pn.flowIDs[flowID] = struct{}{}

	// Mark this node as seen in the current stability round
	pn.stability[pn.stabIdx] = true
}

// IncrementSent increments the sent probe count.
func (pn *PathNode) IncrementSent() {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	pn.sent++
}

// AvgRTT returns the average round-trip time.
func (pn *PathNode) AvgRTT() time.Duration {
	pn.mu.Lock()
	defer pn.mu.Unlock()

	if pn.received == 0 {
		return 0
	}
	return pn.totalRTT / time.Duration(pn.received)
}

// StDev returns the population standard deviation of RTT in milliseconds.
func (pn *PathNode) StDev() float64 {
	pn.mu.Lock()
	defer pn.mu.Unlock()

	if pn.received < 2 {
		return 0
	}
	return math.Sqrt(pn.m2 / float64(pn.received))
}

// LossPercent returns the packet loss percentage.
func (pn *PathNode) LossPercent() float64 {
	pn.mu.Lock()
	defer pn.mu.Unlock()

	if pn.sent == 0 {
		return 0
	}
	return float64(pn.sent-pn.received) / float64(pn.sent) * 100
}

// MarkSeen marks the current stability slot as seen (true).
func (pn *PathNode) MarkSeen() {
	pn.mu.Lock()
	defer pn.mu.Unlock()

	pn.stability[pn.stabIdx] = true
}

// AdvanceRound moves the stability ring buffer forward and pre-clears the next slot.
func (pn *PathNode) AdvanceRound() {
	pn.mu.Lock()
	defer pn.mu.Unlock()

	pn.stabIdx = (pn.stabIdx + 1) % stabilityWindowSize
	if pn.stabCount < stabilityWindowSize {
		pn.stabCount++
	}
	pn.stability[pn.stabIdx] = false
}

// StabilityPercent returns the percentage of recent rounds where this IP was seen.
func (pn *PathNode) StabilityPercent() float64 {
	pn.mu.Lock()
	defer pn.mu.Unlock()

	if pn.stabCount == 0 {
		return 0
	}

	seen := 0
	for i := 0; i < pn.stabCount; i++ {
		if pn.stability[i] {
			seen++
		}
	}
	return float64(seen) / float64(pn.stabCount) * 100
}

// GetFlowIDs returns a sorted slice of unique flow IDs seen by this node.
func (pn *PathNode) GetFlowIDs() []int {
	pn.mu.Lock()
	defer pn.mu.Unlock()

	ids := make([]int, 0, len(pn.flowIDs))
	for id := range pn.flowIDs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// GetIP returns the node's IP address.
func (pn *PathNode) GetIP() net.IP {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.IP
}

// GetHostname returns the node's hostname.
func (pn *PathNode) GetHostname() string {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.Hostname
}

// SetHostname sets the reverse DNS hostname.
func (pn *PathNode) SetHostname(name string) {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	pn.Hostname = name
}

// GetASN returns the Autonomous System Number and organization name.
func (pn *PathNode) GetASN() (int, string) {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.asnNum, pn.asnOrg
}

// SetASN sets the Autonomous System Number and organization name.
func (pn *PathNode) SetASN(number int, org string) {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	pn.asnNum = number
	pn.asnOrg = org
}

// GetSent returns the sent count.
func (pn *PathNode) GetSent() int {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.sent
}

// GetReceived returns the received count.
func (pn *PathNode) GetReceived() int {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.received
}

// GetMinRTT returns the minimum RTT.
func (pn *PathNode) GetMinRTT() time.Duration {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.minRTT
}

// GetMaxRTT returns the maximum RTT.
func (pn *PathNode) GetMaxRTT() time.Duration {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.maxRTT
}

// GetLastRTT returns the last RTT.
func (pn *PathNode) GetLastRTT() time.Duration {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.lastRTT
}

// Reset clears all stats but preserves the IP address.
func (pn *PathNode) Reset() {
	pn.mu.Lock()
	defer pn.mu.Unlock()

	pn.Hostname = ""
	pn.asnNum = 0
	pn.asnOrg = ""
	pn.sent = 0
	pn.received = 0
	pn.minRTT = 0
	pn.maxRTT = 0
	pn.lastRTT = 0
	pn.totalRTT = 0
	pn.mean = 0
	pn.m2 = 0
	pn.flowIDs = make(map[int]struct{})
	pn.stability = [stabilityWindowSize]bool{}
	pn.stabIdx = 0
	pn.stabCount = 0
}
