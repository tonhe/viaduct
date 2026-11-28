// Package dest decides which TTL is the destination's when the target answers
// TTL-limited probes at more than one TTL.
//
// The tracer and the hop table each keep a Tracker and feed it the same
// replies, so the range the tracer probes and the row the views call the
// destination follow one rule.
package dest

import "sync"

// MoveRounds is the number of rounds of positive evidence that move the
// destination: the target answering below the destination's TTL, or a router
// answering at it or beyond it (see Tracker).
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

// SilentMoveRounds is the number of rounds that move the destination up on
// the target's replies alone: the target answered at one and the same TTL
// beyond the destination's, and neither there nor in between, with no router
// seen at the destination's TTL.
//
// That is also exactly what loss at the destination looks like once the
// tracer probes beyond it, so it takes far more rounds than MoveRounds. With
// the target where it is and losing probes, a round's probes at the
// destination's TTL and at a TTL x beyond it are lost alike. Of the rounds
// that end such a run or count toward it, "answered at x and at nothing
// nearer" is never the likelier outcome than "answered at the destination's
// TTL": with independent loss p and x one hop out its share is p/(1+p), less
// further out. A run of 24 has a chance of at most (p/(1+p))^23: 1 in 10^11
// at 50% loss, 4 in 10^8 at 90%. A run can start in at most p(1-p) of the
// rounds, so at 90% loss and a round a second that is one false move in
// about ten years; TestLossyDestinationKeepsItsTTL has none in 500,000
// rounds. The price is that a path that really becomes longer through hops
// that answer nothing is followed 24 rounds after the target is found again.
const SilentMoveRounds = 24

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
// Each round it takes a candidate: the lowest TTL at which the target
// answered that lies above every TTL at which another node answered a flow
// the target answers. A TTL where a router still answers some flows is not
// the destination's, even if the target answers other flows there: with
// load-balanced paths of unequal length the target is the hop for the whole
// trace only where every flow that reaches it has reached it. A flow the
// target has never answered (ICMP probes to a host that ignores them, say)
// has no say. Neither has a router that answers the ICMP TTL probe (Flow.Aux)
// once the target has answered a flow of the trace's own protocol: where the
// UDP or TCP flows all reach the target is the destination of a UDP or TCP
// trace, even if ICMP takes a longer path. Only replies are evidence; a probe
// that went unanswered is not, because a lost reply, a throttled one and a
// hop that answers nothing look the same.
//
// The first round that finds the target settles the destination at once, on
// its candidate. Gaps in the target's replies above it mean nothing: a lost
// reply and a hop that answers nothing look the same. A stray reply in that
// round (a probe that took a shorter path once) is left out when routers
// answer between it and the target's distance; when nothing answers there,
// the destination settles on the stray and moves up on the target's replies
// alone, the slow way below.
//
// After that the destination moves only on a run of rounds that point the
// same way. A round with the candidate at the destination's TTL ends a run; a
// round without a candidate neither counts nor ends it.
//
//   - Down, when the target answers below the destination's TTL with no
//     router there: MoveRounds rounds, to the highest candidate of the run.
//   - Up, when a router answers at the destination's TTL or beyond it: the
//     path is longer. MoveRounds such rounds, to the lowest candidate.
//   - Up, when the target is merely silent at the destination's TTL and
//     answers beyond it: that is what loss at the destination looks like, so
//     it takes SilentMoveRounds rounds with the same candidate, counted from
//     the last round in which the target answered nearer than that.
//
// A destination that loses probes, however many, therefore keeps its TTL.
type Tracker struct {
	mu sync.Mutex

	ttl   int // the destination's TTL; 0 until a round has found the target
	reach int // the highest TTL worth probing; 0 until ttl is known

	// known holds the flows the target has answered.
	known map[Flow]bool

	// The round being collected.
	answered []int        // the TTL of each reply from the target
	routers  map[Flow]int // per flow, the highest TTL at which another node answered

	// The run of rounds pointing away from ttl.
	dir    int // -1 below ttl, +1 above, 0 no run
	pick   int // where positive evidence would move the destination
	streak int // rounds of positive evidence in the run
	// silent counts, for each TTL beyond the destination's, the rounds of an
	// upward run whose candidate it was since the target last answered
	// nearer than it.
	silent map[int]int
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

	// The highest TTL at which a router answered a flow that counts: the
	// destination lies beyond it.
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
	// The round's candidate: the lowest TTL beyond it at which the target
	// answered any probe. Probes that went unanswered below the candidate's
	// TTL or at it say nothing: a lost reply, a throttled one and a hop that
	// answers nothing look the same.
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
		t.dir = 0
	case c < t.ttl:
		if t.dir != -1 {
			t.dir, t.pick, t.streak = -1, c, 0
		}
		// The highest of the run: where the target answered in every one of
		// its rounds.
		if c > t.pick {
			t.pick = c
		}
		t.streak++
		if t.streak >= MoveRounds {
			t.ttl, t.dir = t.pick, 0
		}
	default:
		if t.dir != 1 {
			t.dir, t.pick, t.streak, t.silent = 1, c, 0, make(map[int]int)
		}
		if c < t.pick {
			t.pick = c // the lowest the target has been seen at
		}
		// On the target's replies alone: this round counts for its candidate,
		// and takes away the rounds of every TTL further out, where the
		// target cannot be if it answers here.
		for ttl := range t.silent {
			if ttl > c {
				delete(t.silent, ttl)
			}
		}
		t.silent[c]++
		if router >= t.ttl {
			t.streak++ // a router where the destination was: the path is longer
		}
		switch {
		case t.streak >= MoveRounds:
			t.ttl, t.dir = t.pick, 0
		case t.silent[c] >= SilentMoveRounds:
			t.ttl, t.dir = c, 0
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
	t.dir, t.pick, t.streak, t.silent = 0, 0, 0, nil
}
