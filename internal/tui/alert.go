package tui

import (
	"fmt"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// AlertState represents the health state of the alert engine.
type AlertState int

const (
	AlertHealthy  AlertState = iota
	AlertDegraded
)

// AlertEngine tracks destination health and fires alerts on sustained degradation.
type AlertEngine struct {
	lossThreshold    float64       // percentage, 0 = disabled
	latencyThreshold time.Duration // 0 = disabled
	roundsRequired   int

	state          AlertState
	consecutiveBad int
	peakLoss       float64
	peakLatency    time.Duration
	affectedHop    int
	affectedASN    string
	disabled       bool
	lastMaxTTL     int // the last TTL ObserveRound looked at in the round before
}

// NewAlertEngine creates a new alert engine with the given thresholds.
func NewAlertEngine(lossThreshold float64, latencyThreshold time.Duration, roundsRequired int) *AlertEngine {
	disabled := lossThreshold <= 0 && latencyThreshold <= 0
	return &AlertEngine{
		lossThreshold:    lossThreshold,
		latencyThreshold: latencyThreshold,
		roundsRequired:   roundsRequired,
		disabled:         disabled,
	}
}

// Update checks destination metrics for this round.
// Returns true if a bell should ring (state transition).
func (e *AlertEngine) Update(destLoss float64, destLatency time.Duration) bool {
	if e.disabled {
		return false
	}

	bad := false
	if e.lossThreshold > 0 && destLoss > e.lossThreshold {
		bad = true
	}
	if e.latencyThreshold > 0 && destLatency > e.latencyThreshold {
		bad = true
	}

	if bad {
		e.consecutiveBad++
		if destLoss > e.peakLoss {
			e.peakLoss = destLoss
		}
		if destLatency > e.peakLatency {
			e.peakLatency = destLatency
		}
	} else {
		e.consecutiveBad = 0
	}

	prevState := e.state

	if e.consecutiveBad >= e.roundsRequired {
		e.state = AlertDegraded
	} else if !bad {
		e.state = AlertHealthy
		e.peakLoss = 0
		e.peakLatency = 0
	}

	return e.state != prevState
}

// ObserveRound feeds a round that has just been closed to the engine: hops
// are the table's hops, sentTo the TTLs probes were sent to in that round and
// maxTTL the last TTL to look at: the destination's, once known. A nil engine
// (alerting off) does nothing.
func (e *AlertEngine) ObserveRound(hops []*hop.Hop, sentTo map[int]bool, maxTTL int) {
	if e == nil {
		return
	}
	hopMap := make(map[int]*hop.Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}
	// A round that sent nothing to the destination's hop (the trace was
	// paused, or no send to it succeeded) is no evidence about the path:
	// the alert engine counts consecutive bad rounds, and such a round is
	// neither bad nor good.
	if dest := destAlertTTL(hopMap, maxTTL); dest > 0 && !sentTo[dest] {
		return
	}
	// The engine follows the destination's hop, whichever TTL that is. When
	// the destination moves (hop.Table.TargetTTL) the count of bad rounds and
	// a raised alert carry over: they are about the destination, and a good
	// round at its new hop clears them as it would at the old one. Only the
	// hop named as affected is looked for again.
	moved := e.lastMaxTTL != 0 && maxTTL != e.lastMaxTTL
	e.lastMaxTTL = maxTTL
	destLoss, destLatency := destAlertMetrics(hopMap, maxTTL)
	changed := e.Update(destLoss, destLatency)
	if e.State() == AlertDegraded && (changed || moved) {
		e.FindAffectedHop(hopMap, maxTTL, computeDeltas(hopMap, maxTTL))
	}
}

// SetAffectedHop records which hop is causing the problem.
func (e *AlertEngine) SetAffectedHop(ttl int, asn string) {
	e.affectedHop = ttl
	e.affectedASN = asn
}

// State returns the current alert state.
func (e *AlertEngine) State() AlertState {
	return e.state
}

// AffectedHopTTL returns the TTL of the affected hop (0 if none).
func (e *AlertEngine) AffectedHopTTL() int {
	return e.affectedHop
}

// Message returns the alert message, or empty if healthy.
func (e *AlertEngine) Message() string {
	if e.state != AlertDegraded {
		return ""
	}
	var msg string
	if e.peakLoss > 0 {
		msg = fmt.Sprintf("! Sustained loss to destination: %.1f%%", e.peakLoss)
	} else {
		msg = fmt.Sprintf("! Sustained high latency to destination: %v", e.peakLatency)
	}
	if e.affectedHop > 0 {
		msg += fmt.Sprintf(" (hop %d affected", e.affectedHop)
		if e.affectedASN != "" {
			msg += ", " + e.affectedASN
		}
		msg += ")"
	}
	return msg
}

// destAlertMetrics returns the loss and latency alerting decides on: those of
// the last responding hop. The loss is the hop's settled figure, the one the
// table shows: echo loss when the hop is rate-limited, and none while the
// cause of its loss is not yet known. Raw TTL-probe loss would raise an alert
// for a destination that only throttles its ICMP errors.
func destAlertMetrics(hopMap map[int]*hop.Hop, maxTTL int) (loss float64, latency time.Duration) {
	for i := maxTTL; i >= 1; i-- {
		h, ok := hopMap[i]
		if !ok || h == nil {
			continue
		}
		pn := h.PrimaryNode()
		if pn != nil && pn.GetReceived() > 0 {
			return h.SettledLoss(), pn.AvgRTT()
		}
	}
	return 0, 0
}

// destAlertTTL returns the TTL of the hop destAlertMetrics reads: the last
// responding hop, or 0 when no hop has responded.
func destAlertTTL(hopMap map[int]*hop.Hop, maxTTL int) int {
	for i := maxTTL; i >= 1; i-- {
		h, ok := hopMap[i]
		if !ok || h == nil {
			continue
		}
		if pn := h.PrimaryNode(); pn != nil && pn.GetReceived() > 0 {
			return i
		}
	}
	return 0
}

// FindAffectedHop walks backward from the destination to find the first hop
// contributing to the degradation.
func (e *AlertEngine) FindAffectedHop(hopMap map[int]*hop.Hop, maxTTL int, deltas []time.Duration) {
	if e.state != AlertDegraded {
		e.affectedHop = 0
		e.affectedASN = ""
		return
	}

	// Find destination hop (last responding)
	destTTL := 0
	for i := maxTTL; i >= 1; i-- {
		h, ok := hopMap[i]
		if !ok || h == nil {
			continue
		}
		pn := h.PrimaryNode()
		if pn != nil && pn.GetReceived() > 0 {
			destTTL = i
			break
		}
	}
	if destTTL == 0 {
		return
	}

	destHop := hopMap[destTTL]
	destNode := destHop.PrimaryNode()

	// Loss alert: walk backward, find first hop with loss within 2% of destination
	if e.lossThreshold > 0 && destHop.SettledLoss() > e.lossThreshold {
		destLoss := destHop.SettledLoss()
		for i := destTTL - 1; i >= 1; i-- {
			h, ok := hopMap[i]
			if !ok || h == nil {
				continue
			}
			pn := h.PrimaryNode()
			if pn == nil || pn.GetReceived() == 0 {
				continue
			}
			if h.SettledLoss() >= destLoss-2.0 {
				asn, org := pn.GetASN()
				asnStr := ""
				if asn > 0 {
					asnStr = fmt.Sprintf("AS%d %s", asn, org)
				}
				e.SetAffectedHop(i, asnStr)
				return
			}
		}
	}

	// Latency alert: find hop with largest positive delta
	if e.latencyThreshold > 0 && destNode.AvgRTT() > e.latencyThreshold {
		maxDelta := time.Duration(0)
		maxDeltaTTL := destTTL
		for i := 1; i <= maxTTL && i < len(deltas); i++ {
			if deltas[i] > maxDelta {
				maxDelta = deltas[i]
				maxDeltaTTL = i
			}
		}
		if h, ok := hopMap[maxDeltaTTL]; ok && h != nil {
			pn := h.PrimaryNode()
			if pn != nil {
				asn, org := pn.GetASN()
				asnStr := ""
				if asn > 0 {
					asnStr = fmt.Sprintf("AS%d %s", asn, org)
				}
				e.SetAffectedHop(maxDeltaTTL, asnStr)
				return
			}
		}
	}

	// Fallback: destination itself
	asn, org := destNode.GetASN()
	asnStr := ""
	if asn > 0 {
		asnStr = fmt.Sprintf("AS%d %s", asn, org)
	}
	e.SetAffectedHop(destTTL, asnStr)
}

// Reset clears alert state.
func (e *AlertEngine) Reset() {
	e.state = AlertHealthy
	e.consecutiveBad = 0
	e.peakLoss = 0
	e.peakLatency = 0
	e.affectedHop = 0
	e.affectedASN = ""
}
