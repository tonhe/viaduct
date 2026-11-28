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

// SparkKind classifies what a sparkline cell represents in a given round. The
// ladder encodes how much the tracer knows about the hop that round, so the
// sparkline's drop criterion matches the echo-aware loss column: a cell is only
// a drop when no tier reached the hop.
type SparkKind uint8

const (
	SparkDrop SparkKind = iota // a probe was sent, nothing answered: genuine loss
	SparkTTL                   // TTL/UDP reply: direct latency measurement
	SparkEcho                  // TTL silent, echo confirms reachability: echo latency
	SparkHeld                  // known rate-limited, not echoed this round: reachable, no reading
)

// SparkSample is one sparkline cell: a latency reading (zero for Held/Drop) and
// the kind that determines how it renders.
type SparkSample struct {
	RTT  time.Duration
	Kind SparkKind
}

// ProbeKind selects which ProbeStats slot a sample updates. Kept local to
// this package (no dependency on internal/probe); the TUI maps
// probe.ProbeType to ProbeKind.
type ProbeKind int

const (
	KindICMPTTL ProbeKind = iota
	KindPrimary
	KindEcho
)

// PathNode tracks per-IP statistics for ECMP multipath discovery.
// It records RTT samples, flow IDs, loss, and path stability over time.
type PathNode struct {
	IP       net.IP
	Hostname string

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

	sparkBuf   [sparklineSize]time.Duration // ring buffer of RTT samples (0 = no reading)
	sparkKind  [sparklineSize]SparkKind     // parallel buffer: how each cell renders
	sparkIdx   int                          // next write index
	sparkCount int                          // entries filled (up to sparklineSize)

	echoSentThisRound bool // an echo probe was dispatched to this node this round
	isTarget          bool // this node is the trace's destination

	hopVerdict RateLimitState // the owning hop's verdict as of the last closed round; Undetermined until one closes

	recentAvgs     [trendWindowSize]float64 // per-round avg RTTs in ms
	recentAvgIdx   int
	recentAvgCount int
	roundRTTSum    time.Duration // sum of RTTs received this round
	roundRTTCount  int           // number of RTTs received this round

	asnNum int    // Autonomous System Number (0 = unknown)
	asnOrg string // AS organization name

	icmpTTL  ProbeStats
	primary  ProbeStats
	icmpEcho ProbeStats

	// echoIgnored is the number of echoes this node ignored before its first
	// answer, when it ignored echoSilentAfter or more and was left out of its
	// hop's echo totals for it. See echoTotals and joinEchoTotals.
	echoIgnored int

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
// The kind parameter routes the sample to the appropriate per-probe-type slot.
// Echo samples update only the icmpEcho slot and do not affect aggregate RTT/loss.
func (pn *PathNode) AddSample(rtt time.Duration, flowID int, kind ProbeKind) {
	pn.mu.Lock()
	defer pn.mu.Unlock()

	switch kind {
	case KindICMPTTL:
		pn.icmpTTL.addReply(rtt)
	case KindPrimary:
		pn.primary.addReply(rtt)
	case KindEcho:
		pn.icmpEcho.addReply(rtt)
		return // echo is ground truth, not a latency sample: no aggregates
	}

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

// writeSparkLocked appends one sample to the sparkline ring buffer.
// Caller must hold pn.mu.
func (pn *PathNode) writeSparkLocked(s SparkSample) {
	pn.sparkBuf[pn.sparkIdx] = s.RTT
	pn.sparkKind[pn.sparkIdx] = s.Kind
	pn.sparkIdx = (pn.sparkIdx + 1) % sparklineSize
	if pn.sparkCount < sparklineSize {
		pn.sparkCount++
	}
}

// SparklineData returns the sparkline ring buffer contents in chronological order.
func (pn *PathNode) SparklineData() []SparkSample {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	if pn.sparkCount == 0 {
		return nil
	}
	result := make([]SparkSample, pn.sparkCount)
	start := 0
	if pn.sparkCount == sparklineSize {
		start = pn.sparkIdx // oldest entry when buffer is full
	}
	for i := 0; i < pn.sparkCount; i++ {
		idx := (start + i) % sparklineSize
		result[i] = SparkSample{RTT: pn.sparkBuf[idx], Kind: pn.sparkKind[idx]}
	}
	return result
}

// RecordRoundAvg records one sparkline entry per round and feeds latency-trend
// tracking. The sample kind reconciles the sparkline with the echo-based loss
// column: a drop is written only when no tier reached the hop this round, so a
// dot means the same thing in both places. Precedence: a direct TTL/UDP reading
// wins; else echo latency (reachable, rate-limited); else a Held marker when the
// hop is known rate-limited but wasn't echoed this round (reachable, no reading);
// else a genuine Drop.
func (pn *PathNode) RecordRoundAvg() {
	pn.mu.Lock()
	defer pn.mu.Unlock()

	switch {
	case pn.roundRTTCount > 0:
		avgMs := float64(pn.roundRTTSum) / float64(pn.roundRTTCount) / float64(time.Millisecond)
		pn.recentAvgs[pn.recentAvgIdx] = avgMs
		pn.recentAvgIdx = (pn.recentAvgIdx + 1) % trendWindowSize
		if pn.recentAvgCount < trendWindowSize {
			pn.recentAvgCount++
		}
		avgRTT := pn.roundRTTSum / time.Duration(pn.roundRTTCount)
		pn.writeSparkLocked(SparkSample{RTT: avgRTT, Kind: SparkTTL})

	case pn.icmpEcho.respondedThisRound:
		pn.writeSparkLocked(SparkSample{RTT: pn.icmpEcho.LastRTT, Kind: SparkEcho})

	case pn.sparkRateLimitedLocked() && !pn.echoSentThisRound:
		pn.writeSparkLocked(SparkSample{Kind: SparkHeld})

	default:
		pn.writeSparkLocked(SparkSample{Kind: SparkDrop})
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

// MarkEchoSent records one echo probe dispatched toward this node. The
// per-round flag lets RecordRoundAvg tell "rate-limited, not sampled this round"
// (Held) apart from "echo sent and lost" (Drop). Reset in MarkProbeRoundEnd,
// which runs after RecordRoundAvg reads it.
func (pn *PathNode) MarkEchoSent() {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	pn.icmpEcho.markSent()
	pn.echoSentThisRound = true
}

// MarkProbeRoundEnd closes the per-probe-type round window.
func (pn *PathNode) MarkProbeRoundEnd() {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	pn.icmpTTL.markRoundEnd()
	pn.primary.markRoundEnd()
	pn.icmpEcho.markRoundEnd()
	pn.echoSentThisRound = false
}

// markProbesSent records the TTL-limited probes of each type sent to this
// node's hop in the round being closed.
func (pn *PathNode) markProbesSent(icmpTTL, primary int) {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	pn.icmpTTL.ProbesSent += icmpTTL
	pn.primary.ProbesSent += primary
}

// ICMPTTLStats returns a snapshot of the ICMP TTL exceeded per-probe stats.
func (pn *PathNode) ICMPTTLStats() ProbeStats {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.icmpTTL
}

// PrimaryStats returns a snapshot of the primary probe per-probe stats.
func (pn *PathNode) PrimaryStats() ProbeStats {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.primary
}

// EchoStats returns a snapshot of the ICMP echo per-probe stats.
func (pn *PathNode) EchoStats() ProbeStats {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.icmpEcho
}

// echoSilentAfter is the number of echoes a node may leave unanswered from
// the start before it is taken to ignore ping.
const echoSilentAfter = 20

// echoTotals returns the echoes sent to and answered by this node that count
// toward its hop's echo totals (Hop.RateLimitState, Hop.Loss). A node that
// has answered none of its first echoSilentAfter echoes ignores ping: its
// unanswered echoes are its own filter and say nothing about the path, so it
// contributes nothing. If it answers later, it counts from that answer on
// (joinEchoTotals). EchoStats keeps the raw counters.
func (pn *PathNode) echoTotals() (sent, received int) {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	if pn.icmpEcho.Received == 0 && pn.icmpEcho.Sent >= echoSilentAfter {
		return 0, 0
	}
	sent, received = pn.icmpEcho.Sent-pn.echoIgnored, pn.icmpEcho.Received
	if sent < received {
		// More answers than echoes counted: some of the echoes taken as
		// ignored were answered late. Every answer counts with its echo.
		sent = received
	}
	return sent, received
}

// joinEchoTotals is called by the owning hop just before it records an echo
// answer from this node, when the node was being left out of the hop's echo
// totals: another node of the hop answers echo. If this is the node's first
// answer after it ignored echoSilentAfter echoes or more, the echoes before
// the one answered stay out of the totals for good. A node on a hop where no
// node answered echo was never left out, and keeps every echo it ignored.
func (pn *PathNode) joinEchoTotals() {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	if pn.icmpEcho.Received == 0 && pn.icmpEcho.Sent > echoSilentAfter {
		pn.echoIgnored = pn.icmpEcho.Sent - 1
	}
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

// RateLimitState classifies whether a hop's TTL-probe loss is an artifact of
// ICMP rate limiting, using echo probes as ground truth. See
// Hop.RateLimitState.
type RateLimitState int

const (
	RateLimitUndetermined RateLimitState = iota // echo samples below guard
	RateLimitNo
	RateLimitYes
)

const (
	// rateLimitMinEchoSamples is the fewest echo probes needed for a verdict.
	rateLimitMinEchoSamples = 20
	// rateLimitZ is the z-score the difference between ICMP TTL probe loss
	// and echo loss must reach: 2.58 is p < 0.01, two-sided.
	rateLimitZ = 2.58
)

// MarkTarget records that this node is the trace's target, answering at this
// TTL. The target can be marked at several TTLs; which of them is the
// destination's hop is Table.TargetTTL's to say. On that hop the mark picks
// the node whose counts the verdict is made from (Hop.RateLimitState).
func (pn *PathNode) MarkTarget() {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	pn.isTarget = true
}

// rateLimitVerdict compares TTL-probe loss with echo loss: Yes when TTL
// probes are lost significantly more often than echoes, Undetermined below
// rateLimitMinEchoSamples echoes. "Significantly" is a two-proportion z-test
// at rateLimitZ, so the verdict accounts for how many probes of each kind
// there are; a fixed margin flags ordinary lossy links whenever the two
// samples happen to differ. The only caller is Hop.RateLimitState: the
// verdict is the hop's, and a node has none of its own.
func rateLimitVerdict(ttlSent, ttlLost, echoSent, echoLost int) RateLimitState {
	if echoSent < rateLimitMinEchoSamples {
		return RateLimitUndetermined
	}
	if ttlSent == 0 {
		return RateLimitNo
	}
	if ttlLost < 0 {
		ttlLost = 0
	}
	if echoLost < 0 {
		echoLost = 0
	}
	n1, n2 := float64(ttlSent), float64(echoSent)
	lost1, lost2 := float64(ttlLost), float64(echoLost)
	p1, p2 := lost1/n1, lost2/n2
	pooled := (lost1 + lost2) / (n1 + n2)
	variance := pooled * (1 - pooled) * (1/n1 + 1/n2)
	if variance <= 0 {
		return RateLimitNo // both lossless or both dead: nothing to tell apart
	}
	if (p1-p2)/math.Sqrt(variance) >= rateLimitZ {
		return RateLimitYes
	}
	return RateLimitNo
}

// IsTarget reports whether this node has been marked as the destination.
func (pn *PathNode) IsTarget() bool {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	return pn.isTarget
}

// setHopVerdict records the owning hop's rate-limit verdict, taken when a
// round closes. The sparkline uses it to tell "reachable, not read this
// round" from a drop; the hop verdict is the authoritative one because a
// node cannot see the TTL probes other nodes at its hop answered.
func (pn *PathNode) setHopVerdict(v RateLimitState) {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	pn.hopVerdict = v
}

// sparkRateLimitedLocked reports whether this node should be treated as
// rate-limited when writing a sparkline cell. It is the owning hop's verdict
// for every node, the destination included. A node whose hop has not closed
// a round yet has no verdict and is not rate-limited.
func (pn *PathNode) sparkRateLimitedLocked() bool {
	return pn.hopVerdict == RateLimitYes
}

// Reset clears all stats but preserves the IP address.
func (pn *PathNode) Reset() {
	pn.mu.Lock()
	defer pn.mu.Unlock()

	pn.Hostname = ""
	pn.asnNum = 0
	pn.asnOrg = ""
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
	pn.sparkKind = [sparklineSize]SparkKind{}
	pn.sparkIdx = 0
	pn.sparkCount = 0
	pn.echoSentThisRound = false
	pn.isTarget = false
	pn.hopVerdict = RateLimitUndetermined
	pn.recentAvgs = [trendWindowSize]float64{}
	pn.recentAvgIdx = 0
	pn.recentAvgCount = 0
	pn.roundRTTSum = 0
	pn.roundRTTCount = 0
	pn.icmpTTL = ProbeStats{}
	pn.primary = ProbeStats{}
	pn.icmpEcho = ProbeStats{}
	pn.echoIgnored = 0
	pn.flowIDs = make(map[int]struct{})
	pn.stability = [stabilityWindowSize]bool{}
	pn.stabIdx = 0
	pn.stabCount = 0
}
