package hop

import (
	"net"

	"github.com/tonhe/viaduct/internal/dest"
)

// NoteReply tells the table who answered a probe sent with ttl, so it can
// tell which TTL is the destination's (TargetTTL). Call it for every reply
// recorded with Hop.AddSample: ip is the node that answered, flowID and kind
// the probe's flow and type, and isTarget whether the reply came from the
// trace's target.
//
// A reply from the target marks its node at that TTL (PathNode.MarkTarget).
// Replies to TTL-limited probes, from the target and from routers, feed the
// destination tracker; direct echoes say nothing about distance.
func (t *Table) NoteReply(ttl int, ip net.IP, flowID int, kind ProbeKind, isTarget bool) {
	if isTarget {
		t.GetOrCreate(ttl).MarkTarget(ip)
	}
	if kind == KindEcho {
		return
	}
	f := dest.Flow{Kind: int(kind), ID: flowID, Aux: kind == KindICMPTTL}
	if isTarget {
		t.dest.Target(f, ttl)
	} else {
		t.dest.Router(f, ttl)
	}
}

// EndRound closes a round for the table as a whole: the destination's TTL is
// settled from the replies noted since the last one, and the hop at it is
// told it is the destination's, so it is judged on the target alone (see
// Hop.RateLimitState). Every other hop is told it is not. Call it when the
// round has been closed on every hop.
func (t *Table) EndRound() {
	t.dest.EndRound(0, 0)
	target := t.TargetTTL()
	t.mu.RLock()
	defer t.mu.RUnlock()
	for ttl, h := range t.hops {
		h.setDestination(target > 0 && ttl == target)
	}
}

// TargetTTL returns the TTL of the destination's hop, or 0 while the target
// has not answered.
//
// It is the TTL at which the target is the hop for the trace as a whole: the
// lowest at which it answers with no router still answering other flows
// there, judged round by round and moved only on a run of rounds that agree:
// dest.MoveRounds on positive evidence, dest.SilentMoveRounds when the target
// is merely silent at it and answers beyond, which is what loss looks like
// (see dest.Tracker). A lower TTL where the target
// answered once, or answers only some flows, is an ordinary hop that lists
// the target among its nodes.
//
// Until a round in which the target answered has closed, it is the lowest TTL
// at which a node is marked as the target.
func (t *Table) TargetTTL() int {
	if ttl := t.dest.TTL(); ttl > 0 {
		return ttl
	}
	for _, h := range t.Snapshot() {
		for _, n := range h.GetNodes() {
			if n.IsTarget() {
				return h.TTL
			}
		}
	}
	return 0
}
