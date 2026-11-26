// Package dest decides which TTL is the destination's when the target answers
// TTL-limited probes at more than one TTL.
//
// The tracer and the hop table each keep a Tracker and feed it the same
// replies, so the range the tracer probes and the row the views call the
// destination follow one rule.
package dest

import "sync"

// MoveRounds is the number of consecutive rounds that must point at another
// TTL before the destination moves there.
//
// One round is not enough: a single reply from the target at a TTL below its
// distance (a probe that took a shorter path once) would move the destination
// row, which is viaduct#234. Each further round multiplies the chance that
// stray replies move it by the rate of such replies, and delays following a
// real path change by one round. Three keeps both small: replies that stray
// in one round in ten move the destination about once in a thousand rounds,
// and a real change is followed after three rounds, which also fits inside a
// short report run.
const MoveRounds = 3

// Flow identifies one stream of TTL-limited probes that routers forward
// alike: a probe type and, where the type sends several, the flow's number.
//
// Aux marks the ICMP TTL probe: in a UDP or TCP trace it is sent beside the
// trace's own flows, and routers may forward it along another path.
type Flow struct {
	Kind int
	ID   int
	Aux  bool
}

// Tracker follows the destination's TTL over the rounds of one trace. The
// zero value is ready to use, and a Tracker is safe for concurrent use.
//
// Each round it takes the lowest TTL at which the target answered that lies
// above every TTL at which another node answered a flow the target answers.
// A TTL where a router still answers some flows is not the destination's,
// even if the target answers other flows there: with load-balanced paths of
// unequal length the target is the hop for the whole trace only where every
// flow that reaches it has reached it. A flow the target has never answered
// (ICMP probes to a host that ignores them, say) has no say. Neither has the
// ICMP TTL probe (Flow.Aux) once the target has answered a flow of the
// trace's own protocol: where the UDP or TCP flows all reach the target is
// the destination of a UDP or TCP trace, even if ICMP takes a longer path.
// Silence is not evidence either: a TTL where the target answers and nothing
// else does is a candidate even if some probes there went unanswered.
//
// The first round that finds the target settles the destination at once.
// After that it moves, up or down, only when MoveRounds consecutive rounds
// that found the target all point the same way; a round that did not find it
// neither counts nor breaks the run. Moving down it takes the highest TTL of
// those rounds, moving up the lowest.
type Tracker struct {
	mu sync.Mutex

	ttl   int // the destination's TTL; 0 until a round has found the target
	reach int // the highest TTL worth probing; 0 until ttl is known

	// known holds the flows the target has answered.
	known map[Flow]bool

	// The round being collected.
	answered []int        // TTLs at which the target answered
	routers  map[Flow]int // per flow, the highest TTL at which another node answered

	// The run of rounds pointing away from ttl.
	dir    int // -1 below ttl, +1 above, 0 no run
	streak int
	pick   int
}

// Target records that the target answered a TTL-limited probe of flow f sent
// with ttl.
func (t *Tracker) Target(f Flow, ttl int) {
	if ttl <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.answered = append(t.answered, ttl)
	if t.known == nil {
		t.known = make(map[Flow]bool)
	}
	t.known[f] = true
}

// Router records that a node other than the target answered a TTL-limited
// probe of flow f sent with ttl.
func (t *Tracker) Router(f Flow, ttl int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.routers == nil {
		t.routers = make(map[Flow]int)
	}
	if ttl > t.routers[f] {
		t.routers[f] = ttl
	}
}

// EndRound closes the round being collected and applies it.
//
// probedTo is the highest TTL probes were sent to in the round and maxTTL the
// highest the trace may use; they set how far the next round should probe
// (Reach). A caller that only wants the destination's TTL passes 0 for both.
func (t *Tracker) EndRound(probedTo, maxTTL int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// The highest TTL at which a router answered a flow that reaches the
	// target: the destination lies beyond it.
	own := false // the target answers a flow of the trace's own protocol
	for f := range t.known {
		own = own || !f.Aux
	}
	router := 0
	for f, ttl := range t.routers {
		if t.known[f] && !(f.Aux && own) && ttl > router {
			router = ttl
		}
	}
	// The round's candidate: the lowest TTL the target answered at beyond it.
	c := 0
	for _, ttl := range t.answered {
		if ttl > router && (c == 0 || ttl < c) {
			c = ttl
		}
	}
	t.answered, t.routers = t.answered[:0], nil

	switch {
	case c == 0:
		// The target was not found: no evidence about where it is.
	case t.ttl == 0:
		t.ttl = c
	case c == t.ttl:
		t.dir, t.streak = 0, 0
	default:
		dir := 1
		if c < t.ttl {
			dir = -1
		}
		if dir != t.dir {
			t.dir, t.streak, t.pick = dir, 0, c
		}
		t.streak++
		// Down: the highest of the run, where the target answered in every
		// one of its rounds. Up: the lowest it has been seen at.
		if (dir < 0 && c > t.pick) || (dir > 0 && c < t.pick) {
			t.pick = c
		}
		if t.streak >= MoveRounds {
			t.ttl = t.pick
			t.dir, t.streak = 0, 0
		}
	}

	if t.ttl == 0 {
		return // distance unknown: every TTL is probed
	}
	switch {
	case c > 0:
		// Keep probing the TTL the target answers at while a move up to it
		// is being confirmed.
		t.reach = t.ttl
		if c > t.reach {
			t.reach = c
		}
	case probedTo >= t.reach && t.reach < maxTTL:
		// Probed as far as the destination was and the target was not there:
		// it is down, or further away. Look one TTL further each round, or
		// past the router that answered where the target used to.
		if router > t.reach {
			t.reach = router
		}
		t.reach++
		if t.reach > maxTTL {
			t.reach = maxTTL
		}
	}
}

// TTL returns the destination's TTL, or 0 while no round has found the
// target.
func (t *Tracker) TTL() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ttl
}

// Reach returns the highest TTL the next round should probe, or 0 while the
// destination's distance is unknown and every TTL should be probed. It is
// the destination's TTL, raised while the target is not answering there.
func (t *Tracker) Reach() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reach
}

// Reset forgets everything, as at the start of a trace.
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ttl, t.reach = 0, 0
	t.known, t.answered, t.routers = nil, nil, nil
	t.dir, t.streak, t.pick = 0, 0, 0
}
