package dest

import "testing"

// round is what one round feeds the tracker: the TTLs the target answered at
// and the TTLs routers answered at, per flow number.
type round struct {
	target  map[int][]int // flow -> TTLs the target answered at
	routers map[int][]int // flow -> TTLs another node answered at
}

func feed(tr *Tracker, r round, probedTo, maxTTL int) {
	for f, ttls := range r.target {
		for _, ttl := range ttls {
			tr.Target(Flow{ID: f}, ttl)
		}
	}
	for f, ttls := range r.routers {
		for _, ttl := range ttls {
			tr.Router(Flow{ID: f}, ttl)
		}
	}
	tr.EndRound(probedTo, maxTTL)
}

func one(ttls ...int) map[int][]int { return map[int][]int{0: ttls} }

func TestTrackerUnknownUntilTheTargetAnswers(t *testing.T) {
	var tr Tracker
	feed(&tr, round{routers: one(1, 2, 3)}, 30, 30)
	if tr.TTL() != 0 || tr.Reach() != 0 {
		t.Errorf("TTL %d Reach %d, want 0 and 0: the target has not answered", tr.TTL(), tr.Reach())
	}
}

// The first round probes every TTL, and the target answers each probe that
// reaches it. The destination is the lowest of those TTLs, at once.
func TestTrackerFirstRoundSettlesAtOnce(t *testing.T) {
	var tr Tracker
	feed(&tr, round{target: one(5, 6, 7, 8), routers: one(1, 2, 3, 4)}, 8, 8)
	if tr.TTL() != 5 || tr.Reach() != 5 {
		t.Errorf("TTL %d Reach %d, want 5 and 5", tr.TTL(), tr.Reach())
	}
}

// viaduct#234, first case: one flow, and one reply from the target at TTL 21
// in a run where it otherwise answers at TTL 25. The hops between are silent.
func TestTrackerStrayReplyInTheFirstRound(t *testing.T) {
	var tr Tracker
	feed(&tr, round{target: one(21, 25, 26, 27, 28, 29, 30)}, 30, 30)
	if tr.TTL() != 21 {
		t.Fatalf("after round 1: TTL %d, want 21 (the lowest answered, as before)", tr.TTL())
	}
	// Nothing answers at TTL 21 again: the tracker looks one TTL further each
	// round until it finds the target.
	for want := 22; want <= 25; want++ {
		feed(&tr, round{}, tr.Reach(), 30)
		if tr.Reach() != want {
			t.Fatalf("Reach %d, want %d", tr.Reach(), want)
		}
		if tr.TTL() != 21 {
			t.Fatalf("TTL moved to %d on silence", tr.TTL())
		}
	}
	// The target answers at 25. The destination follows after MoveRounds
	// rounds, and the tracer keeps probing TTL 25 meanwhile.
	for i := 1; i <= MoveRounds; i++ {
		if tr.TTL() != 21 {
			t.Fatalf("TTL %d before round %d at TTL 25, want 21", tr.TTL(), i)
		}
		feed(&tr, round{target: one(25)}, tr.Reach(), 30)
		if tr.Reach() != 25 {
			t.Fatalf("Reach %d, want 25", tr.Reach())
		}
	}
	if tr.TTL() != 25 {
		t.Errorf("TTL %d, want 25 after %d rounds answered there", tr.TTL(), MoveRounds)
	}
}

func TestTrackerStrayReplyLaterNeverMoves(t *testing.T) {
	var tr Tracker
	feed(&tr, round{target: one(25)}, 30, 30)
	for i := 0; i < 20; i++ {
		// Two rounds in a row with a reply at TTL 21, then a normal one.
		feed(&tr, round{target: one(21, 25)}, 25, 30)
		feed(&tr, round{target: one(21, 25)}, 25, 30)
		feed(&tr, round{target: one(25)}, 25, 30)
		if tr.TTL() != 25 || tr.Reach() != 25 {
			t.Fatalf("TTL %d Reach %d, want 25 and 25", tr.TTL(), tr.Reach())
		}
	}
}

// viaduct#234, second case: six flows, the target answers flow 4 at TTL 8
// and every flow at TTL 9; routers answer the other flows at TTL 8.
func TestTrackerUnequalPathLengths(t *testing.T) {
	var tr Tracker
	r := round{
		target:  map[int][]int{4: {8, 9}},
		routers: map[int][]int{},
	}
	for f := 0; f < 6; f++ {
		r.routers[f] = []int{1, 2, 3, 4, 5, 6, 7}
		if f != 4 {
			r.target[f] = []int{9}
			r.routers[f] = append(r.routers[f], 8)
		}
	}
	for i := 0; i < 10; i++ {
		feed(&tr, r, 9, 30)
		if tr.TTL() != 9 || tr.Reach() != 9 {
			t.Fatalf("round %d: TTL %d Reach %d, want 9 and 9", i+1, tr.TTL(), tr.Reach())
		}
	}
}

// A flow the target never answers has no say: routers answering the ICMP
// probes beyond the TTL where the target answers TCP do not push the
// destination out.
func TestTrackerFlowTheTargetNeverAnswersIsIgnored(t *testing.T) {
	var tr Tracker
	icmp, tcp := Flow{Kind: 1}, Flow{Kind: 2}
	for i := 0; i < 5; i++ {
		tr.Target(tcp, 9)
		tr.Router(icmp, 9)
		tr.Router(icmp, 10)
		tr.Router(icmp, 11)
		tr.EndRound(11, 30)
		if tr.TTL() != 9 {
			t.Fatalf("TTL %d, want 9", tr.TTL())
		}
	}
}

// The path gets longer for some flows: routers now answer them at the TTL
// that was the destination's. The tracker probes past those routers at once
// and the destination moves after MoveRounds rounds.
func TestTrackerRouterAtTheDestinationTTLRaisesReach(t *testing.T) {
	var tr Tracker
	feed(&tr, round{target: map[int][]int{0: {8}, 1: {8}}}, 30, 30)
	feed(&tr, round{target: map[int][]int{0: {8}}, routers: map[int][]int{1: {8}}}, 8, 30)
	if tr.TTL() != 8 || tr.Reach() != 9 {
		t.Fatalf("TTL %d Reach %d, want 8 and 9", tr.TTL(), tr.Reach())
	}
	for i := 0; i < MoveRounds; i++ {
		feed(&tr, round{target: map[int][]int{0: {8, 9}, 1: {9}}, routers: map[int][]int{1: {8}}}, 9, 30)
	}
	if tr.TTL() != 9 || tr.Reach() != 9 {
		t.Errorf("TTL %d Reach %d, want 9 and 9", tr.TTL(), tr.Reach())
	}
}

func TestTrackerPathGetsShorter(t *testing.T) {
	var tr Tracker
	feed(&tr, round{target: one(9), routers: one(8)}, 30, 30)
	for i := 1; i < MoveRounds; i++ {
		feed(&tr, round{target: one(8, 9)}, 9, 30)
		if tr.TTL() != 9 {
			t.Fatalf("TTL %d after %d rounds at 8, want 9", tr.TTL(), i)
		}
	}
	feed(&tr, round{target: one(8, 9)}, 9, 30)
	if tr.TTL() != 8 || tr.Reach() != 8 {
		t.Errorf("TTL %d Reach %d, want 8 and 8 after %d rounds", tr.TTL(), tr.Reach(), MoveRounds)
	}
}

// Moving down takes the highest TTL of the run: the one the target answered
// at in every round of it.
func TestTrackerMoveDownTakesTheHighestOfTheRun(t *testing.T) {
	var tr Tracker
	feed(&tr, round{target: one(9)}, 30, 30)
	feed(&tr, round{target: one(6, 9)}, 9, 30)
	feed(&tr, round{target: one(7, 9)}, 9, 30)
	feed(&tr, round{target: one(6, 9)}, 9, 30)
	if tr.TTL() != 7 {
		t.Errorf("TTL %d, want 7", tr.TTL())
	}
}

// Moving up takes the lowest: a round that lost the probe at the new TTL
// and was answered one further does not push the destination out.
func TestTrackerMoveUpTakesTheLowestOfTheRun(t *testing.T) {
	var tr Tracker
	feed(&tr, round{target: one(5)}, 30, 30)
	feed(&tr, round{target: one(8)}, 9, 30)
	feed(&tr, round{target: one(7)}, 9, 30)
	feed(&tr, round{target: one(9)}, 9, 30)
	if tr.TTL() != 7 {
		t.Errorf("TTL %d, want 7", tr.TTL())
	}
}

// A destination that stops answering keeps its TTL. The tracker looks
// further, up to the trace's limit, and comes back when the target answers
// where it was.
func TestTrackerTargetGoesSilentAndReturns(t *testing.T) {
	var tr Tracker
	feed(&tr, round{target: one(5)}, 8, 8)
	for i := 0; i < 10; i++ {
		feed(&tr, round{routers: one(1, 2, 3, 4)}, tr.Reach(), 8)
	}
	if tr.TTL() != 5 || tr.Reach() != 8 {
		t.Fatalf("TTL %d Reach %d, want 5 and 8 (the limit)", tr.TTL(), tr.Reach())
	}
	feed(&tr, round{target: one(5, 6, 7, 8)}, 8, 8)
	if tr.TTL() != 5 || tr.Reach() != 5 {
		t.Errorf("TTL %d Reach %d, want 5 and 5", tr.TTL(), tr.Reach())
	}
}

// A round that sent nothing as far as the destination (the trace was paused)
// is no reason to look further.
func TestTrackerRoundThatDidNotProbeTheDestination(t *testing.T) {
	var tr Tracker
	feed(&tr, round{target: one(5)}, 8, 8)
	feed(&tr, round{}, 0, 8)
	feed(&tr, round{}, 4, 8)
	if tr.Reach() != 5 {
		t.Errorf("Reach %d, want 5", tr.Reach())
	}
}

func TestTrackerIgnoresNonPositiveTTL(t *testing.T) {
	var tr Tracker
	tr.Target(Flow{}, 0)
	tr.EndRound(0, 0)
	if tr.TTL() != 0 {
		t.Errorf("TTL %d, want 0", tr.TTL())
	}
}

func TestTrackerReset(t *testing.T) {
	var tr Tracker
	feed(&tr, round{target: one(5)}, 8, 8)
	tr.Target(Flow{}, 3)
	tr.Reset()
	tr.EndRound(8, 8)
	if tr.TTL() != 0 || tr.Reach() != 0 {
		t.Errorf("TTL %d Reach %d after Reset, want 0 and 0", tr.TTL(), tr.Reach())
	}
}
