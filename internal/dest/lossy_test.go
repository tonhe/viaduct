package dest

import (
	"fmt"
	"math/rand"
	"testing"
)

// A lossy destination is the main thing a traceroute measures, and loss at
// the destination must not look like a longer path. These tests play a stable
// path against a tracker the way the tracer does (probe up to Reach, every
// TTL while it is 0) with seeded random loss at the target, and require the
// destination's TTL to stay where the target is.

// lossModel decides, round by round, whether the target answers a probe.
type lossModel interface {
	nextRound()
	lost() bool
	String() string
}

// independent loses each probe with probability p.
type independent struct {
	p float64
	r *rand.Rand
}

func (m *independent) nextRound()     {}
func (m *independent) lost() bool     { return m.r.Float64() < m.p }
func (m *independent) String() string { return fmt.Sprintf("independent p=%.2f", m.p) }

// gilbert is a two-state burst model. In the bad state, entered from the
// good one with probability enter each round and left with probability leave,
// every probe of the round is lost; in the good state each probe is lost with
// probability p.
type gilbert struct {
	p, enter, leave float64
	bad             bool
	r               *rand.Rand
}

func (m *gilbert) nextRound() {
	if m.bad {
		m.bad = m.r.Float64() >= m.leave
	} else {
		m.bad = m.r.Float64() < m.enter
	}
}
func (m *gilbert) lost() bool { return m.bad || m.r.Float64() < m.p }
func (m *gilbert) String() string {
	return fmt.Sprintf("bursts enter=%.2f leave=%.2f p=%.2f", m.enter, m.leave, m.p)
}

// simResult is what a simulated trace did.
type simResult struct {
	settledAt   int // the round the destination first was at the target's TTL, 0 if never
	movesAfter  int // changes of the destination's TTL after that round
	wrongAfter  int // rounds after it with the destination elsewhere
	wrongBefore int // rounds before it with the destination known and elsewhere
	extraProbes int // TTLs probed beyond the target's, after round one
}

// simulate traces a stable path for the given rounds: routers answer every
// probe below dist, and the target answers each of the flows probes sent with
// a TTL of dist or more unless the model loses it. With one flow the trace is
// an ICMP-mode one; with more, flow 0 is the ICMP TTL probe of a UDP or TCP
// trace and the others are the trace's own flows.
func simulate(m lossModel, dist, maxTTL, flows, rounds int) simResult {
	var tr Tracker
	var res simResult
	last := 0
	for i := 1; i <= rounds; i++ {
		m.nextRound()
		to := tr.Reach()
		if to == 0 {
			to = maxTTL
		} else if to > dist {
			res.extraProbes += to - dist
		}
		for ttl := 1; ttl <= to; ttl++ {
			for id := 0; id < flows; id++ {
				f := Flow{Kind: 1, ID: id}
				if id == 0 {
					f = Flow{Aux: true}
				}
				if ttl < dist {
					tr.Router(f, ttl)
				} else if !m.lost() {
					tr.Target(f, ttl)
				}
			}
		}
		tr.EndRound(to, maxTTL)
		cur := tr.TTL()
		switch {
		case res.settledAt == 0 && cur == dist:
			res.settledAt = i
		case res.settledAt == 0 && cur != 0:
			res.wrongBefore++
		case res.settledAt != 0:
			if cur != last {
				res.movesAfter++
			}
			if cur != dist {
				res.wrongAfter++
			}
		}
		last = cur
	}
	return res
}

// Once the destination is at the target's TTL it never leaves it, whatever
// the loss: 5000 rounds a case, loss up to 90%, independent and in bursts,
// one flow (ICMP mode) and seven (the ICMP TTL probe and six flows of a UDP
// or TCP trace), five seeds each. And it gets there: a first round that
// lost the probe at the target's TTL settles too far out, and the target's
// later answers at its TTL bring the destination down.
func TestLossyDestinationKeepsItsTTL(t *testing.T) {
	const dist, maxTTL, rounds = 10, 30, 5000
	type mk func(r *rand.Rand) lossModel
	var models []mk
	for _, p := range []float64{0.05, 0.1, 0.3, 0.5, 0.7, 0.9} {
		p := p
		models = append(models, func(r *rand.Rand) lossModel { return &independent{p: p, r: r} })
	}
	for _, g := range []gilbert{
		{p: 0, enter: 0.1, leave: 0.3},   // clean, with outages of about 3 rounds
		{p: 0.1, enter: 0.2, leave: 0.2}, // half the time in outages of about 5 rounds
		{p: 0.5, enter: 0.05, leave: 0.1},
		{p: 0.3, enter: 0.3, leave: 0.5},
	} {
		g := g
		models = append(models, func(r *rand.Rand) lossModel { c := g; c.r = r; return &c })
	}
	for _, flows := range []int{1, 7} {
		for _, model := range models {
			for seed := int64(1); seed <= 5; seed++ {
				m := model(rand.New(rand.NewSource(seed)))
				res := simulate(m, dist, maxTTL, flows, rounds)
				name := fmt.Sprintf("%s, %d flows, seed %d", m, flows, seed)
				t.Logf("%s: at TTL %d from round %d (%d rounds elsewhere before), %d moves after, %d TTLs probed beyond it in %d rounds",
					name, dist, res.settledAt, res.wrongBefore, res.movesAfter, res.extraProbes, rounds)
				if res.settledAt == 0 || res.settledAt > rounds/2 {
					t.Errorf("%s: the destination was first at TTL %d in round %d, want it there in the first half of the run", name, dist, res.settledAt)
					continue
				}
				if res.movesAfter != 0 || res.wrongAfter != 0 {
					t.Errorf("%s: the destination left TTL %d: %d moves, %d rounds elsewhere (settled in round %d, %d rounds elsewhere before)",
						name, dist, res.movesAfter, res.wrongAfter, res.settledAt, res.wrongBefore)
				}
			}
		}
	}
}

// The first round settles the destination on the lowest TTL the target
// answered at, whatever replies were lost above it: 5000 first rounds a case,
// independent loss from 5% to 90%, one flow and seven. A gap in the target's
// replies is never read as a longer path (the bound is none at all), so a
// short report on a lossy path does not end rows beyond the target.
func TestFirstRoundSettlesOnTheLowestAnsweredTTL(t *testing.T) {
	const dist, maxTTL, rounds = 10, 30, 5000
	for _, flows := range []int{1, 7} {
		for _, p := range []float64{0.05, 0.1, 0.3, 0.5, 0.7, 0.9} {
			r := rand.New(rand.NewSource(1))
			found, above := 0, 0
			for i := 0; i < rounds; i++ {
				var tr Tracker
				lowest := 0
				for ttl := 1; ttl <= maxTTL; ttl++ {
					for id := 0; id < flows; id++ {
						f := Flow{Kind: 1, ID: id}
						if id == 0 {
							f = Flow{Aux: true}
						}
						switch {
						case ttl < dist:
							tr.Router(f, ttl)
						case r.Float64() >= p:
							tr.Target(f, ttl)
							if lowest == 0 {
								lowest = ttl
							}
						}
					}
				}
				tr.EndRound(maxTTL, maxTTL)
				if lowest != 0 {
					found++
				}
				if tr.TTL() != lowest {
					above++
				}
			}
			t.Logf("p=%.2f, %d flows: %d of %d first rounds found the target; first settle away from the lowest answered TTL in %d", p, flows, found, rounds, above)
			if above != 0 {
				t.Errorf("p=%.2f, %d flows: the first settle was not the lowest TTL the target answered at in %d of %d first rounds, want 0", p, flows, above, rounds)
			}
		}
	}
}
