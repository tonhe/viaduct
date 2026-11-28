package hop

import "time"

// ProbeStats accumulates per-probe-type counters for one PathNode.
// Not self-locking: the owning PathNode's mutex guards all access.
//
// Every count is in probes that were written. The two kinds keep their sent
// count in different fields:
//   - Echo kind: Sent counts the echoes sent to the node (markSent).
//   - TTL kinds: ProbesSent counts the probes of this type sent to the node's
//     hop. Sent is not used and stays 0: a TTL-limited probe is sent to a
//     hop, not to a node.
type ProbeStats struct {
	// Sent is the echoes sent to the node. Echo kind only.
	Sent     int
	Received int

	// ProbesSent is, for the TTL kinds, the number of probes of this type
	// sent to the node's hop in the rounds the node was known: the figure
	// Received is counted against. For the echo kind it is not used; Sent
	// already counts echoes.
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

// markRoundEnd closes out a round.
func (s *ProbeStats) markRoundEnd() {
	s.respondedThisRound = false
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
