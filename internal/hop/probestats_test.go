package hop

import (
	"testing"
	"time"
)

// For the TTL kinds a round end counts nothing: the rounds a node was known
// are not probes sent to it (viaduct#154). Replies are counted as they come.
func TestProbeStatsTTLKindRoundEndCountsNothing(t *testing.T) {
	var s ProbeStats
	// 4 rounds; replies in rounds 1, 2, 4.
	for round := 1; round <= 4; round++ {
		if round != 3 {
			s.addReply(10 * time.Millisecond)
		}
		s.markRoundEnd()
	}
	if s.Sent != 0 {
		t.Errorf("Sent = %d, want 0: a round end is not a send", s.Sent)
	}
	if s.Received != 3 {
		t.Errorf("Received = %d, want 3", s.Received)
	}
}

func TestProbeStatsEchoKindProbeLoss(t *testing.T) {
	var s ProbeStats
	for i := 0; i < 4; i++ {
		s.markSent()
	}
	s.addReply(5 * time.Millisecond)
	s.addReply(6 * time.Millisecond)
	s.addReply(7 * time.Millisecond)
	s.markRoundEnd() // a round end must not touch Sent
	if s.Sent != 4 {
		t.Errorf("Sent = %d, want 4 (sends)", s.Sent)
	}
	if s.Received != 3 {
		t.Errorf("Received = %d, want 3", s.Received)
	}
	if got := s.ProbeLossPercent(); got != 25.0 {
		t.Errorf("ProbeLossPercent = %v, want 25", got)
	}
}

func TestProbeStatsRTTTracking(t *testing.T) {
	var s ProbeStats
	s.addReply(20 * time.Millisecond)
	s.addReply(10 * time.Millisecond)
	s.addReply(30 * time.Millisecond)
	if s.MinRTT != 10*time.Millisecond {
		t.Errorf("MinRTT = %v", s.MinRTT)
	}
	if s.MaxRTT != 30*time.Millisecond {
		t.Errorf("MaxRTT = %v", s.MaxRTT)
	}
	if s.LastRTT != 30*time.Millisecond {
		t.Errorf("LastRTT = %v", s.LastRTT)
	}
	if got := s.AvgRTT(); got != 20*time.Millisecond {
		t.Errorf("AvgRTT = %v, want 20ms", got)
	}
}

func TestProbeStatsZeroDivision(t *testing.T) {
	var s ProbeStats
	if s.ProbeLossPercent() != 0 || s.AvgRTT() != 0 {
		t.Error("zero-value ProbeStats must report zeros, not NaN/panic")
	}
}
