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

	destNode := hopMap[destTTL].PrimaryNode()

	// Loss alert: walk backward, find first hop with loss within 2% of destination
	if e.lossThreshold > 0 && destNode.LossPercent() > e.lossThreshold {
		destLoss := destNode.LossPercent()
		for i := destTTL - 1; i >= 1; i-- {
			h, ok := hopMap[i]
			if !ok || h == nil {
				continue
			}
			pn := h.PrimaryNode()
			if pn == nil || pn.GetReceived() == 0 {
				continue
			}
			if pn.LossPercent() >= destLoss-2.0 {
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
