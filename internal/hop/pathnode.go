package hop

import (
	"math"
	"net"
	"sort"
	"sync"
	"time"
)

const stabilityWindowSize = 50
const sparklineSize = 28
const trendWindowSize = 10

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
	mean      float64 // running mean in ms (Welford's)
	m2        float64 // running sum of squared differences in ms² (Welford's)
	sumLogRTT   float64       // running sum of ln(rtt_ms) for geometric mean
	hasPrevRTT  bool          // whether prevRTT is valid
	prevRTT     time.Duration // previous probe's RTT
	jitter      time.Duration // current jitter |rtt - prevRTT|
	jitterMean  float64       // running jitter mean in ms
	jitterCount int           // number of jitter samples

	sparkBuf   [sparklineSize]time.Duration // ring buffer of RTT samples (0 = loss)
	sparkIdx   int                          // next write index
	sparkCount int                          // entries filled (up to sparklineSize)

	recentAvgs     [trendWindowSize]float64 // per-round avg RTTs in ms
	recentAvgIdx   int
	recentAvgCount int
	roundRTTSum    time.Duration // sum of RTTs received this round
	roundRTTCount  int           // number of RTTs received this round

	asnNum int    // Autonomous System Number (0 = unknown)
	asnOrg string // AS organization name

	flowIDs   map[int]struct{}
	stability [stabilityWindowSize]bool
	stabIdx   int
	stabCount int

	mu sync.Mutex
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
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

	// Geometric mean: accumulate ln(rtt_ms)
	rttMs := float64(rtt) / float64(time.Millisecond)
	if rttMs < 0.001 {
		rttMs = 0.001
	}
	pn.sumLogRTT += math.Log(rttMs)

	// Jitter: |current - previous|
	if pn.hasPrevRTT {
		pn.jitter = absDuration(rtt - pn.prevRTT)
		pn.jitterCount++
		jitterMs := float64(pn.jitter) / float64(time.Millisecond)
		pn.jitterMean += (jitterMs - pn.jitterMean) / float64(pn.jitterCount)
	}
	pn.prevRTT = rtt
	pn.hasPrevRTT = true

	// Per-round tracking for trend detection and sparkline
	pn.roundRTTSum += rtt
	pn.roundRTTCount++

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

// GeoMean returns the geometric mean of RTT samples.
func (pn *PathNode) GeoMean() time.Duration {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	if pn.received == 0 {
		return 0
	}
	ms := math.Exp(pn.sumLogRTT / float64(pn.received))
	return time.Duration(ms * float64(time.Millisecond))
}

// recordLossLocked writes a loss sentinel to the sparkline buffer.
// Caller must hold pn.mu.
func (pn *PathNode) recordLossLocked() {
	pn.sparkBuf[pn.sparkIdx] = 0 // 0 = timeout sentinel
	pn.sparkIdx = (pn.sparkIdx + 1) % sparklineSize
	if pn.sparkCount < sparklineSize {
		pn.sparkCount++
	}
}

// SparklineData returns the sparkline ring buffer contents in chronological order.
func (pn *PathNode) SparklineData() []time.Duration {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	if pn.sparkCount == 0 {
		return nil
	}
	result := make([]time.Duration, pn.sparkCount)
	start := 0
	if pn.sparkCount == sparklineSize {
		start = pn.sparkIdx // oldest entry when buffer is full
	}
	for i := 0; i < pn.sparkCount; i++ {
		result[i] = pn.sparkBuf[(start+i)%sparklineSize]
	}
	return result
}

// RecordRoundAvg records the per-round average RTT for trend detection
// and sparkline. One sparkline entry per round (the round average),
// not per probe.
func (pn *PathNode) RecordRoundAvg() {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	if pn.roundRTTCount == 0 {
		// No probes received this round — record loss in sparkline
		pn.recordLossLocked()
		return
	}
	avgMs := float64(pn.roundRTTSum) / float64(pn.roundRTTCount) / float64(time.Millisecond)
	pn.recentAvgs[pn.recentAvgIdx] = avgMs
	pn.recentAvgIdx = (pn.recentAvgIdx + 1) % trendWindowSize
	if pn.recentAvgCount < trendWindowSize {
		pn.recentAvgCount++
	}

	// Sparkline: record round average
	avgRTT := pn.roundRTTSum / time.Duration(pn.roundRTTCount)
	pn.sparkBuf[pn.sparkIdx] = avgRTT
	pn.sparkIdx = (pn.sparkIdx + 1) % sparklineSize
	if pn.sparkCount < sparklineSize {
		pn.sparkCount++
	}

	pn.roundRTTSum = 0
	pn.roundRTTCount = 0
}

// Trend returns the latency trend: "stable", "degrading", or "improving".
func (pn *PathNode) Trend() string {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	if pn.recentAvgCount < 3 {
		return "stable"
	}
	n := pn.recentAvgCount
	start := 0
	if n == trendWindowSize {
		start = pn.recentAvgIdx
	}
	var sumX, sumY, sumXY, sumX2 float64
	for i := 0; i < n; i++ {
		x := float64(i)
		y := pn.recentAvgs[(start+i)%trendWindowSize]
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}
	fn := float64(n)
	slope := (fn*sumXY - sumX*sumY) / (fn*sumX2 - sumX*sumX)

	avgMs := sumY / fn
	threshold := 0.5
	if pct := avgMs * 0.01; pct > threshold {
		threshold = pct
	}

	if slope > threshold {
		return "degrading"
	}
	if slope < -threshold {
		return "improving"
	}
	return "stable"
}

// Jitter returns the current jitter (|rtt - prevRTT|).
func (pn *PathNode) Jitter() time.Duration {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.jitter
}

// JitterMean returns the running mean jitter.
func (pn *PathNode) JitterMean() time.Duration {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return time.Duration(pn.jitterMean * float64(time.Millisecond))
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
	pn.sumLogRTT = 0
	pn.hasPrevRTT = false
	pn.prevRTT = 0
	pn.jitter = 0
	pn.jitterMean = 0
	pn.jitterCount = 0
	pn.sparkBuf = [sparklineSize]time.Duration{}
	pn.sparkIdx = 0
	pn.sparkCount = 0
	pn.recentAvgs = [trendWindowSize]float64{}
	pn.recentAvgIdx = 0
	pn.recentAvgCount = 0
	pn.roundRTTSum = 0
	pn.roundRTTCount = 0
	pn.flowIDs = make(map[int]struct{})
	pn.stability = [stabilityWindowSize]bool{}
	pn.stabIdx = 0
	pn.stabCount = 0
}
