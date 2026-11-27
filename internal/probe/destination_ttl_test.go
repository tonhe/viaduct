package probe

import (
	"context"
	"encoding/binary"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// answeringSender is a probeSender for ICMP-mode run() tests that answers the
// probes it is given: it records each probe's TTL by round and, when answer
// says so, queues the target's Echo Reply on the listener's connection.
type answeringSender struct {
	mu     sync.Mutex
	ttl    int
	round  int           // 0-based, advanced by endRound
	sent   map[int][]int // round -> TTLs written
	conn   *fakeICMPConn
	target net.IP
	answer func(round, ttl int) bool
}

func (s *answeringSender) SetTTL(ttl int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ttl = ttl
	return nil
}

func (s *answeringSender) WriteTo(b []byte, _ net.Addr) (int, error) {
	s.mu.Lock()
	round, ttl := s.round, s.ttl
	s.sent[round] = append(s.sent[round], ttl)
	s.mu.Unlock()
	if len(b) >= 8 && s.answer(round, ttl) {
		id := int(binary.BigEndian.Uint16(b[4:6]))
		seq := int(binary.BigEndian.Uint16(b[6:8]))
		s.conn.push(marshalEchoReply(id, seq), fakeAddr{s.target.String()})
	}
	return len(b), nil
}

func (s *answeringSender) endRound() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.round++
}

// highestPerRound returns the highest TTL written in each round.
func (s *answeringSender) highestPerRound(rounds int) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int, rounds)
	for r := 0; r < rounds; r++ {
		for _, ttl := range s.sent[r] {
			if ttl > out[r] {
				out[r] = ttl
			}
		}
	}
	return out
}

// runAnswered runs an ICMP-mode trace of the given number of rounds over
// MaxHops 10 against a target that answers as answer says, and returns the
// highest TTL probed in each round.
func runAnswered(t *testing.T, rounds int, answer func(round, ttl int) bool) []int {
	t.Helper()
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.MaxHops = 10
	cfg.FirstTTL = 1
	cfg.NumPaths = 1
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 50 * time.Millisecond
	cfg.Timeout = 100 * time.Millisecond
	cfg.MaxRounds = rounds
	target := net.ParseIP("192.0.2.1")
	cfg.TargetIP = target

	conn := newFakeConn()
	snd := &answeringSender{sent: make(map[int][]int), conn: conn, target: target, answer: answer}
	tr := NewTracer(target, cfg)
	tr.newSender = func(context.Context, ProbeProtocol, chan<- Result) (probeSender, func(), error) {
		return snd, nil, nil
	}
	tr.SetOnRoundEnd(snd.endRound)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	results := make(chan Result, 64)
	go func() {
		for {
			select {
			case <-results:
			case <-ctx.Done():
				return
			}
		}
	}()
	go tr.listen(ctx, conn, results)
	if err := tr.run(ctx, nil, results); err != nil {
		t.Fatalf("run: %v", err)
	}
	return snd.highestPerRound(rounds)
}

// viaduct#234: one reply from the target at TTL 4 in the first round, in a
// run where it otherwise answers at TTL 7. The cap on the send loop was set
// by that first reply, the first to arrive, and never changed, so the
// destination's own TTL was not probed again. Nothing answers between TTL 4
// and TTL 7 here, so round one still settles on TTL 4 (a gap in the target's
// replies is what lost replies look like), but the tracer now looks one TTL
// further each round the target does not answer, finds it at TTL 7 and keeps
// probing it there.
func TestRun_StrayReplyInRoundOneDoesNotPinTheCap(t *testing.T) {
	got := runAnswered(t, 8, func(round, ttl int) bool {
		if round == 0 && ttl == 4 {
			return true
		}
		return ttl >= 7
	})
	want := []int{10, 4, 5, 6, 7, 7, 7, 7}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("highest TTL probed per round = %v, want %v", got, want)
	}
}

// The path gets longer through hops that answer nothing: from round three
// the target answers from TTL 8, not 6. The cap rises one TTL each round the
// target is not found, probes are sent at the higher TTLs, and the cap stays
// at TTL 8, where the target answers, while the destination's move there is
// being confirmed.
func TestRun_CapRisesWhenTheTargetDoesNotAnswerAtIt(t *testing.T) {
	got := runAnswered(t, 8, func(round, ttl int) bool {
		if round >= 2 {
			return ttl >= 8
		}
		return ttl >= 6
	})
	want := []int{10, 6, 6, 7, 8, 8, 8, 8}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("highest TTL probed per round = %v, want %v", got, want)
	}
}

// The replies the listeners match set the cap round by round. Six UDP flows:
// the target answers flow 4 at TTL 8 and every flow at TTL 9, routers answer
// the other flows at TTL 8. The first reply to arrive is flow 4's at TTL 8;
// the cap is 9, where the target answers every flow. A direct echo's reply
// says nothing about distance.
func TestDestinationReplies_SetTheCap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.MaxHops = 30
	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)

	round := func() {
		tr.noteDestinationReply(probeRecord{ttl: 8, flowID: 4, probeType: ProbeUDPTTL}, true)
		for flow := 0; flow < 6; flow++ {
			if flow != 4 {
				tr.noteDestinationReply(probeRecord{ttl: 8, flowID: flow, probeType: ProbeUDPTTL}, false)
			}
			tr.noteDestinationReply(probeRecord{ttl: 9, flowID: flow, probeType: ProbeUDPTTL}, true)
		}
		// The echo to the target's node at TTL 8, answered by the target.
		tr.noteDestinationReply(probeRecord{ttl: 8, probeType: ProbeICMPEcho}, true)
	}
	round()
	tr.endDestinationRound(30)
	if got := atomic.LoadInt32(&tr.targetTTL); got != 9 {
		t.Fatalf("cap after round 1 = %d, want 9", got)
	}
	for i := 0; i < 5; i++ {
		round()
		tr.endDestinationRound(9)
		if got := atomic.LoadInt32(&tr.targetTTL); got != 9 {
			t.Fatalf("cap = %d, want 9", got)
		}
	}

	// The routers at TTL 8 now answer flow 4 too, and nothing answers at
	// TTL 9 any more: the next round probes past them.
	for flow := 0; flow < 6; flow++ {
		tr.noteDestinationReply(probeRecord{ttl: 9, flowID: flow, probeType: ProbeUDPTTL}, false)
	}
	tr.endDestinationRound(9)
	if got := atomic.LoadInt32(&tr.targetTTL); got != 10 {
		t.Errorf("cap after routers answered at TTL 9 = %d, want 10", got)
	}

	tr.resetDestination()
	if got := atomic.LoadInt32(&tr.targetTTL); got != 0 {
		t.Errorf("cap after reset = %d, want 0", got)
	}
	tr.endDestinationRound(30)
	if got := atomic.LoadInt32(&tr.targetTTL); got != 0 {
		t.Errorf("cap after a round with no reply = %d, want 0 (probe every TTL)", got)
	}
}

// A stable path is probed as before: every TTL in the first round, then up
// to the destination's.
func TestRun_StablePathIsCappedAtTheDestination(t *testing.T) {
	got := runAnswered(t, 5, func(_, ttl int) bool { return ttl >= 6 })
	want := []int{10, 6, 6, 6, 6}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("highest TTL probed per round = %v, want %v", got, want)
	}
}

// The path gets shorter in round 3: the target answers from TTL 4. The cap
// follows after dest.MoveRounds rounds answered there.
func TestRun_CapFollowsAShorterPath(t *testing.T) {
	got := runAnswered(t, 8, func(round, ttl int) bool {
		if round >= 2 {
			return ttl >= 4
		}
		return ttl >= 6
	})
	want := []int{10, 6, 6, 6, 6, 4, 4, 4}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("highest TTL probed per round = %v, want %v", got, want)
	}
}

// A destination that stops answering: the tracer looks further out, one TTL a
// round up to the limit, and returns to the destination's TTL when it answers
// again.
func TestRun_SilentTargetIsLookedForFurtherOut(t *testing.T) {
	got := runAnswered(t, 9, func(round, ttl int) bool {
		return ttl >= 7 && (round < 2 || round >= 7)
	})
	want := []int{10, 7, 7, 8, 9, 10, 10, 10, 7}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("highest TTL probed per round = %v, want %v", got, want)
	}
}
