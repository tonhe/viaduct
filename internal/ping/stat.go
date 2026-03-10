package ping

import (
	"math"
	"time"
)

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
}

// AddSample records a successful ping reply.
func (s *Stat) AddSample(rtt time.Duration) {
	s.Sent++
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
}

// AddLoss records a lost ping (no reply).
func (s *Stat) AddLoss() {
	s.Sent++
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
