package probe

import (
	"context"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// A send that cannot complete must not stop the round loop (viaduct#115).
//
// On macOS an IPv6 raw-socket write was seen to block for good: the kernel
// answered EAGAIN, Go parked the goroutine until the socket became writable,
// and it never did. The send loop had no write deadline, so the trace stopped.
//
// The fake below behaves like that socket. A write blocks until the write
// deadline and then fails with a timeout; with no deadline it blocks until the
// sender is closed.

// stallSender is a probeSender backed by a "socket" that may stop accepting
// writes. stall decides, per write, whether that write blocks.
type stallSender struct {
	mu       sync.Mutex
	deadline time.Time
	calls    []string        // "ttl", "deadline", "write", in call order
	lead     []time.Duration // how far ahead each deadline was set
	attempts []sendAttempt
	stall    func(pkt []byte, dst net.Addr) bool
	closed   chan struct{}
	once     sync.Once
}

// sendAttempt is one WriteTo call: where it was addressed and whether it left.
type sendAttempt struct {
	ttl  int
	dst  string
	pkt  []byte
	sent bool
}

func newStallSender(stall func(pkt []byte, dst net.Addr) bool) *stallSender {
	return &stallSender{stall: stall, closed: make(chan struct{})}
}

// stallAll blocks every write.
func stallAll([]byte, net.Addr) bool { return true }

func (s *stallSender) close() { s.once.Do(func() { close(s.closed) }) }

func (s *stallSender) SetTTL(ttl int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "ttl")
	s.attempts = append(s.attempts, sendAttempt{ttl: ttl})
	return nil
}

func (s *stallSender) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "deadline")
	s.lead = append(s.lead, time.Until(t))
	s.deadline = t
	return nil
}

func (s *stallSender) WriteTo(b []byte, dst net.Addr) (int, error) {
	s.mu.Lock()
	s.calls = append(s.calls, "write")
	cur := &s.attempts[len(s.attempts)-1]
	cur.dst = dst.String()
	cur.pkt = append([]byte(nil), b...)
	dl := s.deadline
	// A deadline covers one write: the next write needs its own.
	s.deadline = time.Time{}
	blocked := s.stall != nil && s.stall(b, dst)
	if !blocked {
		cur.sent = true
	}
	s.mu.Unlock()
	if !blocked {
		return len(b), nil
	}

	var expired <-chan time.Time
	if !dl.IsZero() {
		timer := time.NewTimer(time.Until(dl))
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case <-expired:
		return 0, os.ErrDeadlineExceeded
	case <-s.closed:
		return 0, net.ErrClosed
	}
}

// writes returns the WriteTo calls made so far.
func (s *stallSender) writes() []sendAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sendAttempt
	for _, a := range s.attempts {
		if a.dst != "" {
			out = append(out, a)
		}
	}
	return out
}

// roundClock records when each round ended.
type roundClock struct {
	mu   sync.Mutex
	ends []time.Time
}

func (c *roundClock) mark() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ends = append(c.ends, time.Now())
}

func (c *roundClock) times() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.ends...)
}

// stallFamilies are the address families the send-path tests run in.
var stallFamilies = []struct {
	name   string
	v      int
	target string
	source string
	hop    string
}{
	{"IPv4", 4, "192.0.2.1", "198.51.100.7", "203.0.113.5"},
	{"IPv6", 6, "2001:db8:100::1", "2001:db8:0:1::7", "2001:db8:0:5::1"},
}

var stallProtocols = []struct {
	name  string
	proto func(Config) ProbeProtocol
}{
	{"icmp", func(c Config) ProbeProtocol { return NewICMPProtocol(c) }},
	{"udp", func(Config) ProbeProtocol { return NewUDPProtocol(33434) }},
	{"tcp", func(Config) ProbeProtocol { return NewTCPProtocol(443) }},
}

// The acceptance test of viaduct#115: a sender whose WriteTo blocks does not
// stop the round loop. Every probe counts as unsent, and each round still
// starts on time.
func TestRun_BlockedWriteDoesNotStopTheRoundLoop(t *testing.T) {
	const rounds = 5
	for _, fam := range stallFamilies {
		for _, p := range stallProtocols {
			t.Run(fam.name+"/"+p.name, func(t *testing.T) {
				target := net.ParseIP(fam.target)
				cfg := DefaultConfig()
				cfg.IPVersion = fam.v
				cfg.SourceIP = net.ParseIP(fam.source)
				cfg.TargetIP = target
				cfg.Protocol = p.proto(cfg)
				cfg.MaxHops = 20
				cfg.NumPaths = 6
				cfg.ProbeDelay = 0
				cfg.RoundDelay = 20 * time.Millisecond
				cfg.MaxRounds = rounds
				cfg.Timeout = 50 * time.Millisecond
				cfg.SendTimeout = 20 * time.Millisecond

				snd := newStallSender(stallAll)
				defer snd.close()
				tr := NewTracer(target, cfg)
				tr.newSender = func(context.Context, ProbeProtocol, chan<- Result) (probeSender, func(), error) {
					return snd, nil, nil
				}
				var sent sentCounter
				tr.SetOnRoundSent(&sent)
				var clock roundClock
				tr.SetOnRoundEnd(clock.mark)

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				start := time.Now()
				go func() { done <- tr.run(ctx, nil, make(chan Result, 64)) }()

				// The test's own timeout: a stuck loop fails here instead of
				// hanging the suite.
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("run: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("the send loop is stuck in a blocked write: %d of %d rounds ended after 5s",
						len(clock.times()), rounds)
				}

				ends := clock.times()
				if len(ends) != rounds {
					t.Fatalf("%d rounds ended, want %d", len(ends), rounds)
				}
				// A round is one stalled write plus the interval. Without the
				// rule that a stalled socket is left alone for the rest of the
				// round, each of the round's writes would wait out its
				// own deadline (20 to 140 writes of 20ms each).
				limit := cfg.RoundDelay + 2*cfg.SendTimeout + 150*time.Millisecond
				prev := start
				for i, end := range ends {
					if took := end.Sub(prev); took > limit {
						t.Errorf("round %d took %v, want under %v: a stalled socket must not stretch the round", i+1, took, limit)
					}
					prev = end
				}
				if n := len(snd.writes()); n != rounds {
					t.Errorf("%d writes attempted over %d rounds, want one per round: after a write times out the socket is left alone until the next round", n, rounds)
				}
				if sent.probes != 0 {
					t.Errorf("round marker counted %d probes sent, want 0: none left", sent.probes)
				}
				tr.pm.mu.Lock()
				left := len(tr.pm.items)
				tr.pm.mu.Unlock()
				if left != 0 {
					t.Errorf("probe map holds %d records for probes that never left, want 0", left)
				}
			})
		}
	}
}
