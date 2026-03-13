package ping

import (
	"math"
	"time"
)

const sparklineSize = 28

// Stat tracks ping statistics for a single IP.
type Stat struct {
	Sent     int
	Received int
	MinRTT   time.Duration
	MaxRTT   time.Duration
	LastRTT  time.Duration
	totalRTT time.Duration
	mean     float64 // Welford's mean in ms
	m2       float64 // Welford's M2

	sumLogRTT   float64       // running sum of ln(rtt_ms) for geometric mean
	hasPrevRTT  bool          // whether prevRTT is valid
	prevRTT     time.Duration // previous probe's RTT
	jitterVal   time.Duration // current jitter |rtt - prevRTT|
	jitterMean  float64       // running jitter mean in ms
	jitterCount int           // number of jitter samples

	sparkBuf   [sparklineSize]time.Duration // ring buffer of RTT samples (0 = loss)
	sparkIdx   int                          // next write index
	sparkCount int                          // entries filled (up to sparklineSize)
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// MarkSent records that a ping was sent (may or may not get a reply).
func (s *Stat) MarkSent() {
	s.Sent++
}

// AddReply records a successful ping reply.
func (s *Stat) AddReply(rtt time.Duration) {
	s.Received++
	s.LastRTT = rtt
	s.totalRTT += rtt

	if s.Received == 1 || rtt < s.MinRTT {
		s.MinRTT = rtt
	}
	if rtt > s.MaxRTT {
		s.MaxRTT = rtt
	}

	// Welford's online algorithm
	ms := float64(rtt.Microseconds()) / 1000.0
	delta := ms - s.mean
	s.mean += delta / float64(s.Received)
	delta2 := ms - s.mean
	s.m2 += delta * delta2

	// Geometric mean: accumulate ln(rtt_ms)
	rttMs := float64(rtt) / float64(time.Millisecond)
	if rttMs < 0.001 {
		rttMs = 0.001
	}
	s.sumLogRTT += math.Log(rttMs)

	// Jitter: |current - previous|
	if s.hasPrevRTT {
		s.jitterVal = absDuration(rtt - s.prevRTT)
		s.jitterCount++
		jitterMs := float64(s.jitterVal) / float64(time.Millisecond)
		s.jitterMean += (jitterMs - s.jitterMean) / float64(s.jitterCount)
	}
	s.prevRTT = rtt
	s.hasPrevRTT = true

	// Sparkline ring buffer
	s.sparkBuf[s.sparkIdx] = rtt
	s.sparkIdx = (s.sparkIdx + 1) % sparklineSize
	if s.sparkCount < sparklineSize {
		s.sparkCount++
	}
}

// LossPercent returns packet loss as a percentage.
func (s *Stat) LossPercent() float64 {
	if s.Sent == 0 {
		return 0
	}
	return float64(s.Sent-s.Received) / float64(s.Sent) * 100.0
}

// AvgRTT returns the average RTT.
func (s *Stat) AvgRTT() time.Duration {
	if s.Received == 0 {
		return 0
	}
	return s.totalRTT / time.Duration(s.Received)
}

// StDev returns the standard deviation in milliseconds.
func (s *Stat) StDev() float64 {
	if s.Received < 2 {
		return 0
	}
	return math.Sqrt(s.m2 / float64(s.Received-1))
}

// GeoMean returns the geometric mean of RTT samples.
func (s *Stat) GeoMean() time.Duration {
	if s.Received == 0 {
		return 0
	}
	ms := math.Exp(s.sumLogRTT / float64(s.Received))
	return time.Duration(ms * float64(time.Millisecond))
}

// Jitter returns the current jitter (|rtt - prevRTT|).
func (s *Stat) Jitter() time.Duration {
	return s.jitterVal
}

// JitterMean returns the running mean jitter.
func (s *Stat) JitterMean() time.Duration {
	return time.Duration(s.jitterMean * float64(time.Millisecond))
}

// SparklineData returns the sparkline ring buffer contents in chronological order.
func (s *Stat) SparklineData() []time.Duration {
	if s.sparkCount == 0 {
		return nil
	}
	result := make([]time.Duration, s.sparkCount)
	start := 0
	if s.sparkCount == sparklineSize {
		start = s.sparkIdx // oldest entry when buffer is full
	}
	for i := 0; i < s.sparkCount; i++ {
		result[i] = s.sparkBuf[(start+i)%sparklineSize]
	}
	return result
}

// RecordLoss records a loss/timeout in the sparkline buffer.
func (s *Stat) RecordLoss() {
	s.sparkBuf[s.sparkIdx] = 0
	s.sparkIdx = (s.sparkIdx + 1) % sparklineSize
	if s.sparkCount < sparklineSize {
		s.sparkCount++
	}
}
