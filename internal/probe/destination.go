package probe

import (
	"sync/atomic"

	"github.com/tonhe/viaduct/internal/dest"
)

// The send loop probes TTLs up to Tracer.targetTTL once the target has
// answered. The first reply from the target sets it, as it always has. From
// then on the destination tracker corrects it when each round closes: the
// target may answer at more than one TTL (load-balanced paths of unequal
// length, or a single reply that took a shorter path), and the TTL of the
// first reply to arrive need not be the one where it answers every flow
// (viaduct#234).

// noteDestinationReply tells the destination tracker about a matched reply to
// a TTL-limited probe. Direct echoes say nothing about distance.
func (t *Tracer) noteDestinationReply(rec probeRecord, isTarget bool) {
	if rec.probeType == ProbeICMPEcho {
		return
	}
	f := dest.Flow{Kind: int(rec.probeType), ID: rec.flowID, Aux: rec.probeType == ProbeICMPTTL}
	if isTarget {
		t.dest.Target(f, rec.ttl)
	} else {
		t.dest.Router(f, rec.ttl)
	}
}

// endDestinationRound closes a round on the destination tracker and sets the
// TTL the next round probes up to. probedTo is the highest TTL a probe was
// written for in the round, 0 when none was. While no round has placed the
// target the cap is 0 and the next round probes every TTL, whatever the
// first reply set.
func (t *Tracer) endDestinationRound(probedTo int) {
	t.dest.EndRound(probedTo, t.cfg.MaxHops)
	atomic.StoreInt32(&t.targetTTL, int32(t.dest.Reach()))
}

// resetDestination forgets the destination's distance: the next round probes
// every TTL.
func (t *Tracer) resetDestination() {
	t.dest.Reset()
	atomic.StoreInt32(&t.targetTTL, 0)
}
