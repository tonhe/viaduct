package hop

import "time"

// ProbeStats accumulates per-probe-type counters for one PathNode.
// Not self-locking: the owning PathNode's mutex guards all access.
//
// Two sent semantics coexist (see design spec "Data model"):
//   - TTL kinds: Sent counts rounds the node existed (markRoundEnd(true));
//     loss is round-based via RecvRounds.
//   - Echo kind: Sent counts actual sends (markSent()); loss is probe-based.
type ProbeStats struct {
	Sent       int
	Received   int
	RecvRounds int

	// ProbesSent is, for the TTL kinds, the number of probes of this type
	// sent to the node's hop in the rounds the node was known: the figure
	// Received is counted against. (Sent counts rounds for those kinds.) For
	// the echo kind it is not used; Sent already counts echoes.
	ProbesSent int

	MinRTT  time.Duration
	MaxRTT  time.Duration
	LastRTT time.Duration
	rttSum  time.Duration

	respondedThisRound bool
}

func (s *ProbeStats) markSent() { s.Sent++ }

func (s *ProbeStats) addReply(rtt time.Duration) {
	s.Received++
	s.LastRTT = rtt
	s.rttSum += rtt
	if s.Received == 1 || rtt < s.MinRTT {
		s.MinRTT = rtt
	}
	if rtt > s.MaxRTT {
		s.MaxRTT = rtt
	}
	s.respondedThisRound = true
}

// markRoundEnd closes out a round. countRound is true for TTL kinds (Sent
// counts rounds) and false for echo (Sent counts sends via markSent).
func (s *ProbeStats) markRoundEnd(countRound bool) {
	if countRound {
		s.Sent++
	}
	if s.respondedThisRound {
		s.RecvRounds++
	}
	s.respondedThisRound = false
}

// RoundLossPercent is the round-based loss used for TTL kinds.
func (s *ProbeStats) RoundLossPercent() float64 {
	if s.Sent == 0 {
		return 0
	}
	return float64(s.Sent-s.RecvRounds) / float64(s.Sent) * 100
}

// ProbeLossPercent is the probe-based loss used for the echo kind.
func (s *ProbeStats) ProbeLossPercent() float64 {
	if s.Sent == 0 {
		return 0
	}
	return float64(s.Sent-s.Received) / float64(s.Sent) * 100
}

func (s *ProbeStats) AvgRTT() time.Duration {
	if s.Received == 0 {
		return 0
	}
	return s.rttSum / time.Duration(s.Received)
}
