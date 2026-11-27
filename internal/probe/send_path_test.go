package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// Send-path behaviour around viaduct#115: where the write deadline is set,
// what a failed send is counted as, the echo tier while paused, and what the
// debug log says. The fake socket (stallSender) is in send_deadline_test.go.

// stallCfg is a small, fast trace configuration for one family and protocol.
func stallCfg(v int, source, target string, proto func(Config) ProbeProtocol) (Config, net.IP) {
	tgt := net.ParseIP(target)
	cfg := DefaultConfig()
	cfg.IPVersion = v
	cfg.SourceIP = net.ParseIP(source)
	cfg.TargetIP = tgt
	cfg.Protocol = proto(cfg)
	cfg.MaxHops = 3
	cfg.NumPaths = 2
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.Timeout = 50 * time.Millisecond
	cfg.SendTimeout = 20 * time.Millisecond
	cfg.ICMPID = 0x1111
	cfg.EchoID = 0x2222
	cfg.EchoBudget = 8
	return cfg, tgt
}

func stallTracer(cfg Config, target net.IP, snd probeSender) *Tracer {
	tr := NewTracer(target, cfg)
	tr.newSender = func(context.Context, ProbeProtocol, chan<- Result) (probeSender, func(), error) {
		return snd, nil, nil
	}
	return tr
}

// runFor runs the send loop to completion, failing the test if it does not
// finish in time.
func runFor(t *testing.T, tr *Tracer, limit time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- tr.run(context.Background(), nil, make(chan Result, 256)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(limit):
		t.Fatalf("run did not finish within %v", limit)
	}
}

// probeKinds sorts the writes a sender saw into the three tiers.
func probeKinds(writes []sendAttempt, target net.IP) (icmpTTL, primary, echo []sendAttempt) {
	for _, w := range writes {
		switch {
		case w.dst != target.String():
			echo = append(echo, w)
		case packetIsICMPEcho(w.pkt):
			icmpTTL = append(icmpTTL, w)
		default:
			primary = append(primary, w)
		}
	}
	return icmpTTL, primary, echo
}

// Every probe write, of every tier and in both families, is preceded by a
// write deadline: hop limit, deadline, write, in that order, with nothing in
// between.
func TestSend_WriteDeadlineIsSetBeforeEveryWrite(t *testing.T) {
	const rounds = 2
	for _, fam := range stallFamilies {
		for _, p := range stallProtocols {
			t.Run(fam.name+"/"+p.name, func(t *testing.T) {
				t.Parallel()
				cfg, target := stallCfg(fam.v, fam.source, fam.target, p.proto)
				cfg.MaxRounds = rounds
				snd := newStallSender(nil) // every write succeeds
				tr := stallTracer(cfg, target, snd)
				tr.SubmitEchoTargets([]EchoTarget{{IP: net.ParseIP(fam.hop), TTL: 1}})
				runFor(t, tr, 5*time.Second)

				snd.mu.Lock()
				calls := append([]string(nil), snd.calls...)
				lead := append([]time.Duration(nil), snd.lead...)
				snd.mu.Unlock()

				if len(calls)%3 != 0 {
					t.Fatalf("%d sender calls, want a multiple of 3 (ttl, deadline, write): %v", len(calls), calls)
				}
				for i := 0; i < len(calls); i += 3 {
					if calls[i] != "ttl" || calls[i+1] != "deadline" || calls[i+2] != "write" {
						t.Fatalf("send %d was %v, want [ttl deadline write]", i/3, calls[i:i+3])
					}
				}
				// Measured inside the fake, a moment after the deadline was
				// computed: at most the send timeout ahead, and not a zero or
				// stale time.
				for i, d := range lead {
					if d > cfg.SendTimeout || d < cfg.SendTimeout-time.Second {
						t.Errorf("deadline %d was set %v ahead, want the send timeout (%v)", i, d, cfg.SendTimeout)
					}
				}

				icmpTTL, primary, echo := probeKinds(snd.writes(), target)
				wantPrimary := rounds * cfg.MaxHops * cfg.NumPaths
				if p.name == "icmp" {
					wantPrimary = 0
				}
				if len(icmpTTL) != rounds*cfg.MaxHops {
					t.Errorf("%d ICMP TTL probes written, want %d", len(icmpTTL), rounds*cfg.MaxHops)
				}
				if len(primary) != wantPrimary {
					t.Errorf("%d %s probes written, want %d", len(primary), p.name, wantPrimary)
				}
				// One echo target, echoed from the second round on.
				if len(echo) != rounds-1 {
					t.Errorf("%d echoes written, want %d", len(echo), rounds-1)
				}
				if got, want := len(lead), len(icmpTTL)+len(primary)+len(echo); got != want {
					t.Errorf("%d deadlines set for %d writes", got, want)
				}
			})
		}
	}
}

func TestSendTimeout_DefaultsWhenUnset(t *testing.T) {
	cfg := DefaultConfig()
	if got := NewTracer(net.ParseIP("192.0.2.1"), cfg).sendTimeout(); got != defaultSendTimeout {
		t.Errorf("sendTimeout = %v with none configured, want %v", got, defaultSendTimeout)
	}
	cfg.SendTimeout = 7 * time.Millisecond
	if got := NewTracer(net.ParseIP("192.0.2.1"), cfg).sendTimeout(); got != 7*time.Millisecond {
		t.Errorf("sendTimeout = %v, want the configured 7ms", got)
	}
}

// recordingConn stands in for the socket under a production sender.
type recordingConn struct {
	net.PacketConn // nil: only the methods below are called
	calls          []string
	deadline       time.Time
}

func (c *recordingConn) SetWriteDeadline(t time.Time) error {
	c.calls = append(c.calls, "deadline")
	c.deadline = t
	return nil
}

func (c *recordingConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	c.calls = append(c.calls, "write")
	return len(b), nil
}

func (c *recordingConn) IPv4PacketConn() *ipv4.PacketConn { return nil }
func (c *recordingConn) IPv6PacketConn() *ipv6.PacketConn { return nil }

// Both production senders pass the deadline and the write to their socket.
func TestProductionSenders_SetWriteDeadlineOnTheirSocket(t *testing.T) {
	when := time.Now().Add(time.Minute)
	dst := &net.IPAddr{IP: net.ParseIP("2001:db8::1")}

	senders := []struct {
		name string
		make func(*recordingConn) interface {
			sendDeadliner
			WriteTo([]byte, net.Addr) (int, error)
		}
	}{
		{"icmpSender", func(c *recordingConn) interface {
			sendDeadliner
			WriteTo([]byte, net.Addr) (int, error)
		} {
			return &icmpSender{conn: c, ipVersion: 6}
		}},
		{"rawSender", func(c *recordingConn) interface {
			sendDeadliner
			WriteTo([]byte, net.Addr) (int, error)
		} {
			return &rawSender{conn: c, ipVersion: 6}
		}},
	}
	for _, s := range senders {
		t.Run(s.name, func(t *testing.T) {
			conn := &recordingConn{}
			snd := s.make(conn)
			if err := snd.SetWriteDeadline(when); err != nil {
				t.Fatal(err)
			}
			if _, err := snd.WriteTo([]byte{1, 2, 3}, dst); err != nil {
				t.Fatal(err)
			}
			if !conn.deadline.Equal(when) {
				t.Errorf("socket deadline = %v, want %v", conn.deadline, when)
			}
			if strings.Join(conn.calls, ",") != "deadline,write" {
				t.Errorf("socket calls = %v, want [deadline write]", conn.calls)
			}
		})
	}
}

// The deadline works on a real kernel socket: a write on a socket whose
// deadline has passed fails with a timeout, and send reports the probe as not
// sent. A UDP socket stands in for the raw one, which needs privileges.
func TestSend_RealSocketDeadlineFailsTheSend(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback UDP socket: %v", err)
	}
	defer conn.Close()

	cfg := DefaultConfig()
	cfg.IPVersion = 4
	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)
	snd := &rawSender{conn: conn, ipVersion: 4}

	dst := conn.LocalAddr().(*net.UDPAddr)

	// The kernel socket itself: a deadline in the past fails the write, and
	// the error is recognised as a timeout.
	if err := snd.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := snd.WriteTo([]byte("probe"), dst); !isSendTimeout(err) {
		t.Fatalf("write past its deadline returned %v, want a timeout", err)
	}
	// A deadline is per write: with a fresh one the same socket writes again.
	if err := snd.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := snd.WriteTo([]byte("probe"), dst); err != nil {
		t.Fatalf("write with a fresh deadline failed: %v", err)
	}

	// rawSender writes to whatever address it is given; the UDP stand-in needs
	// a UDP address, so the write goes through a sender that supplies one. It
	// also moves the deadline send sets into the past, so the write is certain
	// to find it passed. A very short timeout is not enough for that: where the
	// clock is coarse (Windows), "now plus a nanosecond" is still now when the
	// write starts, and the write succeeds.
	udp := udpAddrSender{rawSender: snd, to: dst, shift: -time.Hour}
	if tr.send(udp, sockPrimary, ProbeUDPTTL, 5, []byte("probe"), &net.IPAddr{IP: dst.IP}) {
		t.Fatal("send reported success on a socket whose write deadline had passed")
	}
	if !tr.sends.stalled[sockPrimary] {
		t.Error("a write that timed out did not mark its socket stalled")
	}
	if got := tr.sends.counts[ProbeUDPTTL].timeouts; got != 1 {
		t.Errorf("timeouts counted = %d, want 1", got)
	}
}

// udpAddrSender is a rawSender whose writes go to a UDP address. shift is
// added to every write deadline it is given.
type udpAddrSender struct {
	*rawSender
	to    *net.UDPAddr
	shift time.Duration
}

func (u udpAddrSender) SetWriteDeadline(t time.Time) error {
	return u.rawSender.SetWriteDeadline(t.Add(u.shift))
}

func (u udpAddrSender) WriteTo(b []byte, _ net.Addr) (int, error) {
	return u.rawSender.WriteTo(b, u.to)
}

func TestIsSendTimeout(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"deadline exceeded", os.ErrDeadlineExceeded, true},
		{"wrapped deadline", &net.OpError{Op: "write", Err: os.ErrDeadlineExceeded}, true},
		{"closed socket", net.ErrClosed, false},
		{"plain error", errors.New("no route to host"), false},
	}
	for _, c := range cases {
		if got := isSendTimeout(c.err); got != c.want {
			t.Errorf("isSendTimeout(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// A stall belongs to a socket. A timed-out write on the ICMP socket skips the
// rest of that socket's sends for the round; the UDP or TCP socket carries on.
func TestSend_StallIsPerSocketAndLastsOneRound(t *testing.T) {
	cfg, target := stallCfg(6, "2001:db8:0:1::7", "2001:db8:100::1", func(Config) ProbeProtocol { return NewUDPProtocol(33434) })
	tr := NewTracer(target, cfg)
	icmpSock := newStallSender(stallAll)
	defer icmpSock.close()
	udpSock := newStallSender(nil)
	dst := &net.IPAddr{IP: target}
	pkt := []byte{128, 0, 0, 0}

	if tr.send(icmpSock, sockICMP, ProbeICMPTTL, 1, pkt, dst) {
		t.Fatal("a write that timed out was reported as sent")
	}
	if tr.send(icmpSock, sockICMP, ProbeICMPTTL, 2, pkt, dst) {
		t.Fatal("a send on a stalled socket was reported as sent")
	}
	if n := len(icmpSock.writes()); n != 1 {
		t.Errorf("%d writes on the stalled socket, want 1: the second send is skipped, not attempted", n)
	}
	if !tr.send(udpSock, sockPrimary, ProbeUDPTTL, 2, pkt, dst) {
		t.Error("the UDP socket's send failed because the ICMP socket stalled")
	}
	if c := tr.sends.counts[ProbeICMPTTL]; c.timeouts != 1 || c.skipped != 1 || c.errors != 0 {
		t.Errorf("ICMP TTL counts = %+v, want 1 timeout and 1 skipped", c)
	}

	// The next round tries the socket again.
	tr.endSendRound(1)
	icmpSock.mu.Lock()
	icmpSock.stall = nil
	icmpSock.mu.Unlock()
	if !tr.send(icmpSock, sockICMP, ProbeICMPTTL, 1, pkt, dst) {
		t.Error("the socket was not tried again in the next round")
	}
}

// typeMarker records what the tracer reports through RoundTypeMarker.
type typeMarker struct {
	mu      sync.Mutex
	icmp    map[int]int
	primary map[int]int
	plain   int // calls to MarkRoundSent, which must not happen
}

func newTypeMarker() *typeMarker {
	return &typeMarker{icmp: map[int]int{}, primary: map[int]int{}}
}

func (m *typeMarker) MarkRoundSent(int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plain++
}

func (m *typeMarker) MarkRoundSentByType(ttl, icmpTTL, primary int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.icmp[ttl] += icmpTTL
	m.primary[ttl] += primary
}

// failICMPSender fails every ICMP write at once (not a timeout) and accepts
// everything else: the ICMP socket is broken, the UDP or TCP one is not.
type failICMPSender struct{ fakeSender }

func (f *failICMPSender) WriteTo(b []byte, dst net.Addr) (int, error) {
	if packetIsICMPEcho(b) {
		return 0, errors.New("sendto: no route to host")
	}
	return f.fakeSender.WriteTo(b, dst)
}

// The tracer reports the probes that left, by type. With the ICMP probes
// failing and the flows going out, a TTL's round is all primary probes.
func TestRun_ReportsProbesSentByType(t *testing.T) {
	for _, fam := range stallFamilies {
		for _, tc := range []struct {
			name        string
			proto       func(Config) ProbeProtocol
			snd         probeSender
			wantICMP    int
			wantPrimary int
		}{
			{"udp, all sent", stallProtocols[1].proto, &fakeSender{}, 1, 2},
			{"tcp, all sent", stallProtocols[2].proto, &fakeSender{}, 1, 2},
			{"icmp, all sent", stallProtocols[0].proto, &fakeSender{}, 1, 0},
			{"udp, ICMP sends fail", stallProtocols[1].proto, &failICMPSender{}, 0, 2},
			{"udp, every send fails", stallProtocols[1].proto, &errSender{}, 0, 0},
		} {
			t.Run(fam.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				cfg, target := stallCfg(fam.v, fam.source, fam.target, tc.proto)
				cfg.MaxRounds = 1
				tr := stallTracer(cfg, target, tc.snd)
				m := newTypeMarker()
				tr.SetOnRoundSent(m)
				runFor(t, tr, 5*time.Second)

				if m.plain != 0 {
					t.Errorf("MarkRoundSent called %d times on a marker that takes the split by type", m.plain)
				}
				for ttl := 1; ttl <= cfg.MaxHops; ttl++ {
					if m.icmp[ttl] != tc.wantICMP || m.primary[ttl] != tc.wantPrimary {
						t.Errorf("TTL %d: reported %d ICMP + %d primary probes sent, want %d + %d",
							ttl, m.icmp[ttl], m.primary[ttl], tc.wantICMP, tc.wantPrimary)
					}
				}
			})
		}
	}
}

// An echo that cannot be written is not an echo sent, and it does not stop
// the TTL probes of later rounds.
func TestRun_StalledEchoIsNotCountedAndDoesNotStopTheTrace(t *testing.T) {
	const rounds = 4
	for _, fam := range stallFamilies {
		t.Run(fam.name, func(t *testing.T) {
			t.Parallel()
			cfg, target := stallCfg(fam.v, fam.source, fam.target, stallProtocols[0].proto)
			cfg.MaxRounds = rounds
			// Writes to the target go out; writes to the hops block.
			snd := newStallSender(func(_ []byte, dst net.Addr) bool { return dst.String() != target.String() })
			defer snd.close()
			tr := stallTracer(cfg, target, snd)
			tr.SubmitEchoTargets([]EchoTarget{
				{IP: net.ParseIP(fam.hop), TTL: 1},
				{IP: net.ParseIP(fam.source), TTL: 2},
				{IP: net.ParseIP(fam.hop), TTL: 3},
			})
			var echoesSent int32
			tr.SetOnEchoSent(func(net.IP, int) { atomic.AddInt32(&echoesSent, 1) })
			var sent sentCounter
			tr.SetOnRoundSent(&sent)
			runFor(t, tr, 5*time.Second)

			if n := atomic.LoadInt32(&echoesSent); n != 0 {
				t.Errorf("echo-sent callback ran %d times for echoes that never left, want 0", n)
			}
			if sent.probes != rounds*cfg.MaxHops {
				t.Errorf("%d TTL probes counted as sent, want %d: the stalled echoes must not cost a round its TTL probes",
					sent.probes, rounds*cfg.MaxHops)
			}
			_, _, echo := probeKinds(snd.writes(), target)
			// One echo is attempted per round from round 2; once it times out
			// the other two targets are left for the next round.
			if len(echo) != rounds-1 {
				t.Errorf("%d echo writes attempted, want %d: one per round", len(echo), rounds-1)
			}
			// The target that timed out is not retried first: the cursor moves
			// past it, so one bad address cannot starve the others.
			if len(echo) >= 2 && echo[0].dst == echo[1].dst {
				t.Errorf("rounds 2 and 3 both tried %s first; the cursor must move past a target that timed out", echo[0].dst)
			}
			tr.pm.mu.Lock()
			var echoRecords int
			for _, rec := range tr.pm.items {
				if rec.probeType == ProbeICMPEcho {
					echoRecords++
				}
			}
			tr.pm.mu.Unlock()
			if echoRecords != 0 {
				t.Errorf("probe map holds %d records for echoes that never left, want 0", echoRecords)
			}
		})
	}
}

// Cancelling the context while writes are blocking stops the loop within a
// send timeout: nothing waits on a write that will not finish.
func TestRun_StalledSocketDoesNotBlockShutdown(t *testing.T) {
	cfg, target := stallCfg(6, "2001:db8:0:1::7", "2001:db8:100::1", stallProtocols[1].proto)
	cfg.MaxRounds = 0 // until cancelled
	cfg.RoundDelay = time.Millisecond
	snd := newStallSender(stallAll)
	defer snd.close()
	tr := stallTracer(cfg, target, snd)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.run(ctx, nil, make(chan Result, 8)) }()
	time.Sleep(3 * cfg.SendTimeout) // a few stalled rounds
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return after cancel while the socket was stalled")
	}
}

// The echo tier stops while the trace is paused, and starts again on the
// next round after it is resumed (the leftover of viaduct#62).
func TestEcho_NotSentWhilePaused_ResumesOnTheNextRound(t *testing.T) {
	for _, fam := range stallFamilies {
		t.Run(fam.name, func(t *testing.T) {
			cfg, target := stallCfg(fam.v, fam.source, fam.target, stallProtocols[0].proto)
			cfg.MaxRounds = 7
			snd := newStallSender(nil)
			tr := stallTracer(cfg, target, snd)
			tr.SubmitEchoTargets([]EchoTarget{
				{IP: net.ParseIP(fam.hop), TTL: 1},
				{IP: net.ParseIP(fam.source), TTL: 2},
			})

			echoesSoFar := func() int {
				_, _, echo := probeKinds(snd.writes(), target)
				return len(echo)
			}
			// Runs on the send loop at the end of each round. Rounds 1-2 run,
			// 3-5 are paused, 6-7 run again.
			var perRound []int
			tr.SetOnRoundEnd(func() {
				perRound = append(perRound, echoesSoFar())
				switch len(perRound) {
				case 2:
					atomic.StoreInt32(&tr.Paused, 1)
				case 5:
					atomic.StoreInt32(&tr.Paused, 0)
				}
			})
			runFor(t, tr, 5*time.Second)

			if len(perRound) != 7 {
				t.Fatalf("%d rounds ended, want 7", len(perRound))
			}
			// Echoes written in each round: none in round 1 (echoes start in
			// round 2), two per running round after that, none while paused.
			want := []int{0, 2, 0, 0, 0, 2, 2}
			prev := 0
			for i, total := range perRound {
				if got := total - prev; got != want[i] {
					t.Errorf("round %d: %d echoes written, want %d (rounds 3-5 are paused)", i+1, got, want[i])
				}
				prev = total
			}
		})
	}
}

// sendEchoes called while paused writes nothing, whatever the caller.
func TestSendEchoes_PausedWritesNothing(t *testing.T) {
	cfg, target := stallCfg(6, "2001:db8:0:1::7", "2001:db8:100::1", stallProtocols[0].proto)
	tr := NewTracer(target, cfg)
	tr.SubmitEchoTargets([]EchoTarget{{IP: net.ParseIP("2001:db8:0:5::1"), TTL: 1}})
	snd := &fakeSender{}
	seq := 0

	atomic.StoreInt32(&tr.Paused, 1)
	tr.sendEchoes(snd, &seq)
	if n := len(snd.written); n != 0 {
		t.Fatalf("%d echoes written while paused, want 0", n)
	}
	atomic.StoreInt32(&tr.Paused, 0)
	tr.sendEchoes(snd, &seq)
	if n := len(snd.written); n != 1 {
		t.Fatalf("%d echoes written after resuming, want 1", n)
	}
}

// An IPv6 link-local hop address has no zone by the time it is an echo
// target, so it is not echoed: no write, no echo-sent callback, no record.
// The other targets are echoed as usual, and IPv4 link-local is unaffected.
func TestSendEchoes_SkipsIPv6LinkLocalWithoutZone(t *testing.T) {
	cfg, target := stallCfg(6, "2001:db8:0:1::7", "2001:db8:100::1", stallProtocols[0].proto)
	var lines []string
	cfg.Debugf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	tr := NewTracer(target, cfg)
	linkLocal := net.ParseIP("fe80::1")
	global := net.ParseIP("2001:db8:0:5::1")
	tr.SubmitEchoTargets([]EchoTarget{{IP: linkLocal, TTL: 1}, {IP: global, TTL: 2}})

	var echoed []string
	tr.SetOnEchoSent(func(ip net.IP, _ int) { echoed = append(echoed, ip.String()) })
	snd := newStallSender(nil)
	seq := 0
	for i := 0; i < 3; i++ {
		tr.sendEchoes(snd, &seq)
	}

	for _, w := range snd.writes() {
		if w.dst == linkLocal.String() {
			t.Fatalf("an echo was written to %s, a link-local address with no zone", linkLocal)
		}
	}
	if len(snd.writes()) != 3 || len(echoed) != 3 {
		t.Errorf("%d echoes written, %d reported sent; want 3 and 3, all to %s", len(snd.writes()), len(echoed), global)
	}
	for _, ip := range echoed {
		if ip != global.String() {
			t.Errorf("echo reported sent to %s, want only %s", ip, global)
		}
	}
	tr.pm.mu.Lock()
	for _, rec := range tr.pm.items {
		if rec.dst.Equal(linkLocal) {
			t.Error("a probe record was left for the link-local echo that was not sent")
		}
	}
	tr.pm.mu.Unlock()
	var skips int
	for _, l := range lines {
		if strings.Contains(l, "fe80::1") {
			skips++
		}
	}
	if skips != 1 {
		t.Errorf("the skipped address was logged %d times over 3 rounds, want once: %q", skips, lines)
	}

	if unzonedLinkLocal(net.ParseIP("169.254.1.1")) {
		t.Error("an IPv4 link-local address was treated as needing a zone")
	}
	if unzonedLinkLocal(global) {
		t.Error("a global address was treated as link-local")
	}
}

// logSink collects debug-log lines.
type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logSink) matching(sub string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range l.lines {
		if strings.Contains(line, sub) {
			out = append(out, line)
		}
	}
	return out
}

// A send failure is logged once per address, however many packets fail, and
// each round with unsent probes gets one line of counts.
func TestDebugLog_SendFailureOncePerAddress(t *testing.T) {
	const rounds = 5
	cfg, target := stallCfg(6, "2001:db8:0:1::7", "2001:db8:100::1", stallProtocols[1].proto)
	cfg.MaxRounds = rounds
	var sink logSink
	cfg.Debugf = sink.logf
	// Writes to the target block; the socket would take writes to the hops.
	hopA, hopB := net.ParseIP("2001:db8:0:5::1"), net.ParseIP("2001:db8:0:6::1")
	snd := newStallSender(func(_ []byte, dst net.Addr) bool { return dst.String() == target.String() })
	defer snd.close()
	tr := stallTracer(cfg, target, snd)
	tr.SubmitEchoTargets([]EchoTarget{{IP: hopA, TTL: 1}, {IP: hopB, TTL: 2}})
	runFor(t, tr, 5*time.Second)

	// The target: one line for the ICMP TTL probe that timed out. The UDP
	// probes to it were skipped, not attempted, so they have no line.
	if got := sink.matching("send timed out"); len(got) != 1 || !strings.Contains(got[0], "icmp-ttl to "+target.String()) {
		t.Errorf("timeout lines = %q, want exactly one, for icmp-ttl to %s", got, target)
	}
	// The stalled socket is the echo tier's too, so no echo was attempted and
	// none is logged.
	if got := sink.matching("send failed"); len(got) != 0 {
		t.Errorf("failure lines = %q, want none: the socket was stalled before the echo tier ran", got)
	}
	perRound := sink.matching("probes not sent")
	if len(perRound) != rounds {
		t.Fatalf("%d per-round lines, want %d: %q", len(perRound), rounds, perRound)
	}
	// Round 1: 1 ICMP TTL timeout, the other 2 TTLs' ICMP probes and all 6 UDP
	// probes skipped. No echoes in round 1.
	if want := "round 1: probes not sent: icmp-ttl 1 timed out, 0 failed, 2 skipped; udp-ttl 0 timed out, 0 failed, 6 skipped"; !strings.HasSuffix(perRound[0], want) {
		t.Errorf("round 1 line = %q, want it to end %q", perRound[0], want)
	}
	if !strings.Contains(perRound[1], "icmp-echo 0 timed out, 0 failed, 2 skipped") {
		t.Errorf("round 2 line = %q, want the 2 skipped echoes counted", perRound[1])
	}
	for _, line := range sink.matching("") {
		if !strings.HasPrefix(line, "["+target.String()+"] ") {
			t.Errorf("line %q does not name its target", line)
		}
	}
}

// With a working socket, an address whose sends fail at once is logged once,
// and the other addresses are still probed.
func TestDebugLog_ImmediateFailureOncePerAddress(t *testing.T) {
	const rounds = 5
	cfg, target := stallCfg(4, "198.51.100.7", "192.0.2.1", stallProtocols[0].proto)
	cfg.MaxRounds = rounds
	var sink logSink
	cfg.Debugf = sink.logf
	hopA, hopB := net.ParseIP("203.0.113.5"), net.ParseIP("203.0.113.6")
	snd := &addrFailSender{
		stall: newStallSender(nil),
		fail:  map[string]error{hopA.String(): errors.New("sendto: no route to host")},
	}
	tr := stallTracer(cfg, target, snd)
	tr.SubmitEchoTargets([]EchoTarget{{IP: hopA, TTL: 1}, {IP: hopB, TTL: 2}})
	var echoed []string
	tr.SetOnEchoSent(func(ip net.IP, _ int) { echoed = append(echoed, ip.String()) })
	runFor(t, tr, 5*time.Second)

	got := sink.matching("send failed")
	if len(got) != 1 || !strings.Contains(got[0], "icmp-echo to "+hopA.String()) || !strings.Contains(got[0], "no route to host") {
		t.Errorf("failure lines = %q, want exactly one, for icmp-echo to %s with the error", got, hopA)
	}
	if n := len(sink.matching("probes not sent")); n != rounds-1 {
		t.Errorf("%d per-round lines, want %d (one for each round with echoes)", n, rounds-1)
	}
	if len(echoed) != rounds-1 {
		t.Errorf("%d echoes reported sent, want %d: one to %s per round", len(echoed), rounds-1, hopB)
	}
	for _, ip := range echoed {
		if ip != hopB.String() {
			t.Errorf("echo reported sent to %s, whose sends fail", ip)
		}
	}
}

// addrFailSender fails writes to chosen addresses at once and passes the rest
// to a stallSender.
type addrFailSender struct {
	stall *stallSender
	fail  map[string]error
}

func (a *addrFailSender) SetTTL(ttl int) error               { return a.stall.SetTTL(ttl) }
func (a *addrFailSender) SetWriteDeadline(t time.Time) error { return a.stall.SetWriteDeadline(t) }
func (a *addrFailSender) WriteTo(b []byte, dst net.Addr) (int, error) {
	if err, ok := a.fail[dst.String()]; ok {
		return 0, err
	}
	return a.stall.WriteTo(b, dst)
}

// The once-per-address memory is bounded, and logging is off without a sink.
func TestLogOnce_BoundedAndSilentWithoutSink(t *testing.T) {
	cfg := DefaultConfig()
	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)
	tr.logOnce("k", "no sink: must not panic")
	tr.debugf("no sink: must not panic")
	if len(tr.sends.logged) != 0 {
		t.Error("keys were remembered with no debug log to write to")
	}

	var sink logSink
	cfg.Debugf = sink.logf
	tr = NewTracer(net.ParseIP("192.0.2.1"), cfg)
	for i := 0; i < maxLoggedSendFailures+50; i++ {
		tr.logOnce(fmt.Sprintf("addr-%d", i), "line %d", i)
		tr.logOnce(fmt.Sprintf("addr-%d", i), "line %d again", i)
	}
	if n := len(sink.matching("line")); n != maxLoggedSendFailures {
		t.Errorf("%d lines logged, want %d: one per key up to the bound", n, maxLoggedSendFailures)
	}
}
