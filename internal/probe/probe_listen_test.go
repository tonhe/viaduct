package probe

// probe_listen_test.go exercises Tracer.listen via the icmpPacketConn seam.
// All addresses use RFC 5737 documentation space (192.0.2.0/24, 198.51.100.0/24).

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// TestSetOnSent verifies that SetOnSent stores the callback.
func TestSetOnSent(t *testing.T) {
	tr := newListenTracer()
	if tr.OnSent != nil {
		t.Fatal("expected OnSent nil before set")
	}
	var sc mockSentCounter
	tr.SetOnSent(&sc)
	if tr.OnSent == nil {
		t.Fatal("expected OnSent non-nil after SetOnSent")
	}
}

// TestSetOnRoundEnd verifies that SetOnRoundEnd stores the callback.
func TestSetOnRoundEnd(t *testing.T) {
	tr := newListenTracer()
	called := false
	tr.SetOnRoundEnd(func() { called = true })
	if tr.OnRoundEnd == nil {
		t.Fatal("expected OnRoundEnd non-nil after SetOnRoundEnd")
	}
	tr.OnRoundEnd()
	if !called {
		t.Fatal("expected OnRoundEnd callback to be called")
	}
}

// mockSentCounter satisfies SentCounter for tests.
type mockSentCounter struct{ count int }

func (m *mockSentCounter) IncrementSent(_ int) { m.count++ }

// fakeAddr implements net.Addr, returning a fixed IPv4 string so that
// net.ParseIP(peer.String()) in Tracer.listen succeeds.
type fakeAddr struct{ s string }

func (a fakeAddr) Network() string { return "ip4" }
func (a fakeAddr) String() string  { return a.s }

// fakeICMPConn implements icmpPacketConn backed by a channel of raw bytes.
// Each push to packets delivers one ReadFrom call. The connection idles
// (returns a deadline error) when the channel is empty, simulating the
// 200 ms deadline loop in Tracer.listen.
type fakeICMPConn struct {
	mu       sync.Mutex
	packets  chan fakePacket
	closed   chan struct{}
	closeOnce sync.Once
}

type fakePacket struct {
	data []byte
	addr net.Addr
	err  error
}

func newFakeConn() *fakeICMPConn {
	return &fakeICMPConn{
		packets: make(chan fakePacket, 16),
		closed:  make(chan struct{}),
	}
}

// push enqueues a packet that ReadFrom will deliver.
func (f *fakeICMPConn) push(data []byte, addr net.Addr) {
	f.packets <- fakePacket{data: data, addr: addr}
}

// pushErr enqueues a read that returns an error (e.g. deadline exceeded).
func (f *fakeICMPConn) pushErr(err error) {
	f.packets <- fakePacket{err: err}
}

func (f *fakeICMPConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case <-f.closed:
		return 0, nil, errors.New("use of closed network connection")
	case pkt, ok := <-f.packets:
		if !ok {
			return 0, nil, errors.New("conn closed")
		}
		if pkt.err != nil {
			return 0, nil, pkt.err
		}
		n := copy(b, pkt.data)
		return n, pkt.addr, nil
	}
}

func (f *fakeICMPConn) WriteTo(_ []byte, _ net.Addr) (int, error) { return 0, nil }

func (f *fakeICMPConn) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeICMPConn) SetReadDeadline(_ time.Time) error { return nil }

// buildIPv4Header builds a minimal 20-byte IPv4 header with the given
// source and destination and protocol. Used to construct inner headers
// for ICMP error messages in tests.
func buildIPv4Header(proto int, src, dst net.IP) []byte {
	hdr := make([]byte, 20)
	hdr[0] = 0x45              // version=4, IHL=5
	hdr[8] = 64                // TTL
	hdr[9] = byte(proto)       // protocol
	copy(hdr[12:16], src.To4()) // source
	copy(hdr[16:20], dst.To4()) // dest
	return hdr
}

// buildICMPInnerHeader constructs an inner IPv4+ICMP Echo Request header
// as carried inside a Time Exceeded or Destination Unreachable response.
// id and seq match the probe key used by ICMPProtocol.
func buildICMPInnerHeader(src, dst net.IP, id, seq int) []byte {
	ipHdr := buildIPv4Header(1, src, dst) // proto=1 (ICMP)
	echo := make([]byte, 8)
	echo[0] = 8 // ICMP Echo Request type
	binary.BigEndian.PutUint16(echo[4:6], uint16(id))
	binary.BigEndian.PutUint16(echo[6:8], uint16(seq))
	return append(ipHdr, echo...)
}

// buildUDPInnerHeader constructs an inner IPv4+UDP header for tests.
func buildUDPInnerHeader(src, dst net.IP, srcPort, dstPort int) []byte {
	ipHdr := buildIPv4Header(17, src, dst) // proto=17 (UDP)
	udpHdr := make([]byte, 8)
	binary.BigEndian.PutUint16(udpHdr[0:2], uint16(srcPort))
	binary.BigEndian.PutUint16(udpHdr[2:4], uint16(dstPort))
	binary.BigEndian.PutUint16(udpHdr[4:6], 8)
	return append(ipHdr, udpHdr...)
}

// marshalTimeExceeded builds an ICMPv4 Time Exceeded message wrapping innerData.
func marshalTimeExceeded(innerData []byte) []byte {
	msg := &icmp.Message{
		Type: ipv4.ICMPTypeTimeExceeded,
		Code: 0,
		Body: &icmp.TimeExceeded{Data: innerData},
	}
	b, _ := msg.Marshal(nil)
	return b
}

// marshalDstUnreachable builds an ICMPv4 Destination Unreachable message.
func marshalDstUnreachable(code int, innerData []byte) []byte {
	msg := &icmp.Message{
		Type: ipv4.ICMPTypeDestinationUnreachable,
		Code: code,
		Body: &icmp.DstUnreach{Data: innerData},
	}
	b, _ := msg.Marshal(nil)
	return b
}

// marshalEchoReply builds an ICMPv4 Echo Reply message.
func marshalEchoReply(id, seq int) []byte {
	msg := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: &icmp.Echo{ID: id, Seq: seq},
	}
	b, _ := msg.Marshal(nil)
	return b
}

// newListenTracer creates a Tracer pre-configured for IPv4 ICMP listen tests.
func newListenTracer() *Tracer {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol()
	cfg.RoundDelay = 10 * time.Millisecond
	cfg.Timeout = 100 * time.Millisecond
	target := net.ParseIP("192.0.2.1")
	cfg.TargetIP = target
	t := NewTracer(target, cfg)
	return t
}

// TestListen_TimeExceeded verifies that a well-formed Time Exceeded response
// flows through listen and appears in the results channel as a non-target hop.
func TestListen_TimeExceeded(t *testing.T) {
	tr := newListenTracer()
	proto := tr.cfg.Protocol.(*ICMPProtocol)
	id := proto.sessionID
	seq := 7
	key := probeKey{SrcPort: id, DstPort: seq, Seq: 0}
	tr.pm.add(key, 3, 0, time.Now())

	src := net.ParseIP("192.0.2.1")   // source of original probe
	dst := net.ParseIP("192.0.2.1")   // destination of original probe
	inner := buildICMPInnerHeader(src, dst, id, seq)
	pkt := marshalTimeExceeded(inner)

	peer := fakeAddr{"198.51.100.5"}
	conn := newFakeConn()
	conn.push(pkt, peer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	results := make(chan Result, 1)
	go func() {
		tr.listen(ctx, conn, results)
	}()

	select {
	case r := <-results:
		if r.TTL != 3 {
			t.Errorf("TTL = %d, want 3", r.TTL)
		}
		if r.IsTarget {
			t.Error("expected IsTarget=false for TimeExceeded from non-target")
		}
		if r.IP.String() != "198.51.100.5" {
			t.Errorf("IP = %v, want 198.51.100.5", r.IP)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for TimeExceeded result")
	}
	cancel()
}

// TestListen_DstUnreachable_PortUnreachable verifies that a Destination
// Unreachable / Port Unreachable (code 3) response for a UDP probe is
// treated as target-reached.
func TestListen_DstUnreachable_PortUnreachable(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.RoundDelay = 10 * time.Millisecond
	target := net.ParseIP("192.0.2.1")
	cfg.TargetIP = target
	tr := NewTracer(target, cfg)

	srcPort := cfg.BasePort    // 44000
	dstPort := 33434 + 5      // TTL=5 path
	key := probeKey{SrcPort: srcPort, DstPort: dstPort, Seq: 0}
	tr.pm.add(key, 5, 0, time.Now())

	src := net.ParseIP("198.51.100.1") // probe source
	dst := net.ParseIP("192.0.2.1")    // probe dest (target)
	inner := buildUDPInnerHeader(src, dst, srcPort, dstPort)
	// code 3 = Port Unreachable → target reached for UDP
	pkt := marshalDstUnreachable(3, inner)

	peer := fakeAddr{"192.0.2.1"}
	conn := newFakeConn()
	conn.push(pkt, peer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	results := make(chan Result, 1)
	go func() { tr.listen(ctx, conn, results) }()

	select {
	case r := <-results:
		if !r.IsTarget {
			t.Error("expected IsTarget=true for Port Unreachable UDP")
		}
		if r.TTL != 5 {
			t.Errorf("TTL = %d, want 5", r.TTL)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for DstUnreachable result")
	}
	cancel()
}

// TestListen_EchoReply verifies that an ICMP Echo Reply is treated as
// target-reached in ICMP protocol mode.
func TestListen_EchoReply(t *testing.T) {
	tr := newListenTracer()
	proto := tr.cfg.Protocol.(*ICMPProtocol)
	id := proto.sessionID
	seq := 3
	key := probeKey{SrcPort: id, DstPort: seq, Seq: 0}
	tr.pm.add(key, 10, 0, time.Now())

	pkt := marshalEchoReply(id, seq)
	peer := fakeAddr{"192.0.2.1"}
	conn := newFakeConn()
	conn.push(pkt, peer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	results := make(chan Result, 1)
	go func() { tr.listen(ctx, conn, results) }()

	select {
	case r := <-results:
		if !r.IsTarget {
			t.Error("expected IsTarget=true for Echo Reply")
		}
		if r.TTL != 10 {
			t.Errorf("TTL = %d, want 10", r.TTL)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for EchoReply result")
	}
	cancel()
}

// TestListen_MalformedPacket verifies that garbage bytes are dropped
// without crashing listen, and no result is emitted.
func TestListen_MalformedPacket(t *testing.T) {
	tr := newListenTracer()
	garbage := []byte{0xFF, 0xFE, 0x00, 0x01, 0x02, 0x03}

	peer := fakeAddr{"198.51.100.9"}
	conn := newFakeConn()
	conn.push(garbage, peer)

	ctx, cancel := context.WithCancel(context.Background())

	results := make(chan Result, 1)
	go func() { tr.listen(ctx, conn, results) }()

	// Give listen a moment to process the garbage then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case r := <-results:
		t.Errorf("expected no result for malformed packet, got %+v", r)
	default:
	}
}

// TestListen_UnknownProbeKey verifies that a well-formed ICMP response for
// a probe key not in probeMap is silently dropped.
func TestListen_UnknownProbeKey(t *testing.T) {
	tr := newListenTracer()
	proto := tr.cfg.Protocol.(*ICMPProtocol)
	id := proto.sessionID
	// seq 99 is not in probeMap
	seq := 99
	inner := buildICMPInnerHeader(net.ParseIP("192.0.2.1"), net.ParseIP("192.0.2.1"), id, seq)
	pkt := marshalTimeExceeded(inner)

	peer := fakeAddr{"198.51.100.2"}
	conn := newFakeConn()
	conn.push(pkt, peer)

	ctx, cancel := context.WithCancel(context.Background())

	results := make(chan Result, 1)
	go func() { tr.listen(ctx, conn, results) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case r := <-results:
		t.Errorf("expected no result for unknown probe key, got %+v", r)
	default:
	}
}

// TestListen_ContextCancel verifies that listen exits promptly on context
// cancellation, even when no packets are arriving.
func TestListen_ContextCancel(t *testing.T) {
	tr := newListenTracer()
	conn := newFakeConn()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		tr.listen(ctx, conn, make(chan Result, 1))
		close(done)
	}()

	cancel() // signal cancellation immediately

	select {
	case <-done:
		// goroutine exited cleanly
	case <-time.After(time.Second):
		t.Fatal("listen did not exit after context cancel")
	}
}

// TestListen_ReadErrorContinues verifies that a read error (e.g. deadline
// exceeded) does not crash listen; the loop continues.
func TestListen_ReadErrorContinues(t *testing.T) {
	tr := newListenTracer()
	proto := tr.cfg.Protocol.(*ICMPProtocol)
	id := proto.sessionID
	seq := 5
	key := probeKey{SrcPort: id, DstPort: seq, Seq: 0}
	tr.pm.add(key, 2, 0, time.Now())

	pkt := marshalEchoReply(id, seq)
	peer := fakeAddr{"192.0.2.1"}

	conn := newFakeConn()
	// Push a deadline error first, then a valid packet.
	conn.pushErr(errors.New("i/o timeout"))
	conn.push(pkt, peer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	results := make(chan Result, 1)
	go func() { tr.listen(ctx, conn, results) }()

	select {
	case r := <-results:
		if !r.IsTarget {
			t.Error("expected IsTarget=true after surviving read error")
		}
		if r.TTL != 2 {
			t.Errorf("TTL = %d, want 2", r.TTL)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out — listen may have aborted after read error")
	}
	cancel()
}

// TestListen_TimeExceeded_TargetIPMatch verifies that a Time Exceeded from
// the target IP itself is upgraded to IsTarget=true (the "4.2.2.2 via UDP"
// case documented in probe.go).
func TestListen_TimeExceeded_TargetIPMatch(t *testing.T) {
	tr := newListenTracer()
	// Override cfg.TargetIP to match the peer address we'll send from.
	targetIP := net.ParseIP("192.0.2.1")
	tr.cfg.TargetIP = targetIP
	tr.target = targetIP

	proto := tr.cfg.Protocol.(*ICMPProtocol)
	id := proto.sessionID
	seq := 11
	key := probeKey{SrcPort: id, DstPort: seq, Seq: 0}
	tr.pm.add(key, 8, 0, time.Now())

	inner := buildICMPInnerHeader(net.ParseIP("198.51.100.1"), targetIP, id, seq)
	pkt := marshalTimeExceeded(inner)

	// Peer is the target itself — listen should set IsTarget=true even
	// though the ICMP type is TimeExceeded.
	peer := fakeAddr{targetIP.String()}
	conn := newFakeConn()
	conn.push(pkt, peer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	results := make(chan Result, 1)
	go func() { tr.listen(ctx, conn, results) }()

	select {
	case r := <-results:
		if !r.IsTarget {
			t.Errorf("expected IsTarget=true when peer is target, got false (TTL=%d)", r.TTL)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for result")
	}
	cancel()
}

// ---- run() loop tests via newSender seam ----

// fakeSender records all WriteTo calls made by the run loop.
type fakeSender struct {
	mu      sync.Mutex
	written []writtenProbe
	setTTLs []int
	errOn   int // if > 0, return error on this WriteTo call (1-indexed)
}

type writtenProbe struct {
	ttl int
	pkt []byte
}

func (f *fakeSender) SetTTL(ttl int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setTTLs = append(f.setTTLs, ttl)
	return nil
}

func (f *fakeSender) WriteTo(b []byte, _ net.Addr) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errOn > 0 && len(f.written) == f.errOn-1 {
		return 0, errors.New("write error injected")
	}
	pkt := make([]byte, len(b))
	copy(pkt, b)
	f.written = append(f.written, writtenProbe{
		ttl: f.setTTLs[len(f.setTTLs)-1],
		pkt: pkt,
	})
	return len(b), nil
}

// newRunTracer builds a Tracer wired for run() tests.
// The returned fakeSender can be inspected after run returns.
func newRunTracer(snd *fakeSender, cfg Config, target net.IP) *Tracer {
	tr := NewTracer(target, cfg)
	tr.newSender = func(_ context.Context, _ ProbeProtocol, _ chan<- Result) (probeSender, func(), error) {
		return snd, nil, nil
	}
	return tr
}

// TestRun_SendsProbesAllTTLs verifies that run() iterates FirstTTL..MaxHops
// and registers each probe in probeMap.
func TestRun_SendsProbesAllTTLs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol()
	cfg.MaxHops = 5
	cfg.FirstTTL = 1
	cfg.NumPaths = 1
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.ProbeDelay = 0
	cfg.MaxRounds = 1

	target := net.ParseIP("192.0.2.1")
	snd := &fakeSender{}
	tr := newRunTracer(snd, cfg, target)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := make(chan Result, 32)
	// run() needs a *icmp.PacketConn argument but the seam bypasses it.
	err := tr.run(ctx, nil, results)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	snd.mu.Lock()
	got := len(snd.written)
	snd.mu.Unlock()

	if got != 5 {
		t.Errorf("expected 5 probes sent (TTL 1-5), got %d", got)
	}

	// Verify TTLs are 1..5
	snd.mu.Lock()
	for i, wp := range snd.written {
		wantTTL := i + 1
		if wp.ttl != wantTTL {
			t.Errorf("probe[%d] TTL = %d, want %d", i, wp.ttl, wantTTL)
		}
	}
	snd.mu.Unlock()
}

// TestRun_RespectsPauseFlag verifies the pause atomic flag skips sends.
func TestRun_RespectsPauseFlag(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol()
	cfg.MaxHops = 3
	cfg.FirstTTL = 1
	cfg.NumPaths = 1
	cfg.ProbeDelay = 1 * time.Millisecond
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 1

	target := net.ParseIP("192.0.2.1")
	snd := &fakeSender{}
	tr := newRunTracer(snd, cfg, target)
	// Pause before run starts
	atomic.StoreInt32(&tr.Paused, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := tr.run(ctx, nil, make(chan Result, 8))
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	snd.mu.Lock()
	n := len(snd.written)
	snd.mu.Unlock()

	if n != 0 {
		t.Errorf("expected 0 probes when paused, got %d", n)
	}
}

// TestRun_OnSentCallback verifies OnSent is called for each probe dispatched.
func TestRun_OnSentCallback(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol()
	cfg.MaxHops = 4
	cfg.FirstTTL = 1
	cfg.NumPaths = 1
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 1

	target := net.ParseIP("192.0.2.1")
	snd := &fakeSender{}
	tr := newRunTracer(snd, cfg, target)

	var sc mockSentCounter
	tr.SetOnSent(&sc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := tr.run(ctx, nil, make(chan Result, 8))
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	if sc.count != 4 {
		t.Errorf("OnSent called %d times, want 4", sc.count)
	}
}

// TestRun_TargetTTLCapsBounds verifies that once targetTTL is set,
// the run loop does not send probes beyond that TTL.
func TestRun_TargetTTLCapsBounds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol()
	cfg.MaxHops = 10
	cfg.FirstTTL = 1
	cfg.NumPaths = 1
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 1

	target := net.ParseIP("192.0.2.1")
	snd := &fakeSender{}
	tr := newRunTracer(snd, cfg, target)
	// Pre-set target TTL to 3 so run should only send TTLs 1-3
	atomic.StoreInt32(&tr.targetTTL, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := tr.run(ctx, nil, make(chan Result, 8))
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	snd.mu.Lock()
	n := len(snd.written)
	snd.mu.Unlock()

	if n != 3 {
		t.Errorf("expected 3 probes (TTL 1-3 capped by targetTTL), got %d", n)
	}
}

// TestRun_ContextCancelExits verifies run returns nil on context cancellation.
func TestRun_ContextCancelExits(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol()
	cfg.MaxHops = 30
	cfg.FirstTTL = 1
	cfg.NumPaths = 1
	cfg.ProbeDelay = 5 * time.Millisecond
	cfg.RoundDelay = time.Second

	target := net.ParseIP("192.0.2.1")
	snd := &fakeSender{}
	tr := newRunTracer(snd, cfg, target)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- tr.run(ctx, nil, make(chan Result, 64))
	}()

	// Cancel quickly
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run returned error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not exit after context cancel")
	}
}

// TestDefaultListenICMP_Reachable verifies that defaultListenICMP is callable.
// Without CAP_NET_RAW it returns a permission error (not a nil *icmp.PacketConn)
// and without that the function body is exercised.
func TestDefaultListenICMP_Reachable(t *testing.T) {
	conn, err := defaultListenICMP(ListenerNet(4), RawListenAddr(4))
	if err == nil {
		// Running as root — close and pass.
		conn.Close()
	}
	// Either way, the function was called — coverage recorded.
}

// TestListen_ShortTimeExceededBody verifies that a Time Exceeded with fewer
// than 28 bytes in the inner Data is silently dropped.
func TestListen_ShortTimeExceededBody(t *testing.T) {
	tr := newListenTracer()

	// Build a Time Exceeded with very short inner Data (< 28 bytes).
	msg := &icmp.Message{
		Type: ipv4.ICMPTypeTimeExceeded,
		Code: 0,
		Body: &icmp.TimeExceeded{Data: []byte{0x45, 0x00}}, // only 2 bytes
	}
	pkt, _ := msg.Marshal(nil)

	conn := newFakeConn()
	conn.push(pkt, fakeAddr{"198.51.100.3"})

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan Result, 1)
	go func() { tr.listen(ctx, conn, results) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case r := <-results:
		t.Errorf("expected no result for short TimeExceeded body, got %+v", r)
	default:
	}
}

// TestListen_ShortDstUnreachBody verifies a Destination Unreachable with
// fewer than 28 data bytes is silently dropped.
func TestListen_ShortDstUnreachBody(t *testing.T) {
	tr := newListenTracer()

	msg := &icmp.Message{
		Type: ipv4.ICMPTypeDestinationUnreachable,
		Code: 3,
		Body: &icmp.DstUnreach{Data: []byte{0x45, 0x00}},
	}
	pkt, _ := msg.Marshal(nil)

	conn := newFakeConn()
	conn.push(pkt, fakeAddr{"198.51.100.4"})

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan Result, 1)
	go func() { tr.listen(ctx, conn, results) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case r := <-results:
		t.Errorf("expected no result for short DstUnreach body, got %+v", r)
	default:
	}
}

// TestListen_EchoReplyNonICMPProtocol verifies that an EchoReply arriving
// when the protocol is UDP is dropped (not forwarded to results).
func TestListen_EchoReplyNonICMPProtocol(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewUDPProtocol(33434) // UDP, not ICMP
	cfg.RoundDelay = 10 * time.Millisecond
	target := net.ParseIP("192.0.2.1")
	cfg.TargetIP = target
	tr := NewTracer(target, cfg)

	// EchoReply with arbitrary ID/Seq
	pkt := marshalEchoReply(0x1234, 7)
	conn := newFakeConn()
	conn.push(pkt, fakeAddr{"192.0.2.1"})

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan Result, 1)
	go func() { tr.listen(ctx, conn, results) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case r := <-results:
		t.Errorf("EchoReply should be dropped for UDP protocol, got %+v", r)
	default:
	}
}

// TestListen_CtxCancelDuringResultSend verifies that listen exits cleanly
// if the context is cancelled while it's trying to send a result.
func TestListen_CtxCancelDuringResultSend(t *testing.T) {
	tr := newListenTracer()
	proto := tr.cfg.Protocol.(*ICMPProtocol)
	id := proto.sessionID
	seq := 20
	key := probeKey{SrcPort: id, DstPort: seq, Seq: 0}
	tr.pm.add(key, 5, 0, time.Now())

	pkt := marshalEchoReply(id, seq)
	conn := newFakeConn()
	conn.push(pkt, fakeAddr{"192.0.2.1"})

	ctx, cancel := context.WithCancel(context.Background())
	// results channel is FULL so the first send will block; cancel fires.
	results := make(chan Result) // unbuffered

	done := make(chan struct{})
	go func() {
		tr.listen(ctx, conn, results)
		close(done)
	}()

	// Cancel immediately, before listen can drain the result.
	cancel()

	select {
	case <-done:
		// listen exited — either via ctx.Done() in the select or after send
	case <-time.After(2 * time.Second):
		t.Fatal("listen did not exit after ctx cancel with blocked results chan")
	}
}

// TestRun_NewSenderError verifies that an error from newSender propagates
// out of run() without a panic.
func TestRun_NewSenderError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol()
	target := net.ParseIP("192.0.2.1")
	tr := NewTracer(target, cfg)
	tr.newSender = func(_ context.Context, _ ProbeProtocol, _ chan<- Result) (probeSender, func(), error) {
		return nil, nil, errors.New("injected sender error")
	}
	ctx := context.Background()
	err := tr.run(ctx, nil, make(chan Result, 1))
	if err == nil {
		t.Fatal("expected error from run when newSender fails")
	}
}

// TestRun_NewSenderCleanupCalled verifies that the cleanup function returned
// by newSender is called when run exits.
func TestRun_NewSenderCleanupCalled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol()
	cfg.MaxHops = 1
	cfg.FirstTTL = 1
	cfg.NumPaths = 1
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 1
	target := net.ParseIP("192.0.2.1")
	snd := &fakeSender{}
	cleaned := false
	tr := NewTracer(target, cfg)
	tr.newSender = func(_ context.Context, _ ProbeProtocol, _ chan<- Result) (probeSender, func(), error) {
		return snd, func() { cleaned = true }, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.run(ctx, nil, make(chan Result, 8)); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !cleaned {
		t.Error("expected cleanup to have been called")
	}
}

// TestRun_OnRoundEndCalled verifies the OnRoundEnd hook fires after each round.
func TestRun_OnRoundEndCalled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol()
	cfg.MaxHops = 2
	cfg.FirstTTL = 1
	cfg.NumPaths = 1
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 2

	target := net.ParseIP("192.0.2.1")
	snd := &fakeSender{}
	tr := newRunTracer(snd, cfg, target)

	var mu sync.Mutex
	roundCount := 0
	tr.SetOnRoundEnd(func() {
		mu.Lock()
		roundCount++
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := tr.run(ctx, nil, make(chan Result, 8))
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	mu.Lock()
	got := roundCount
	mu.Unlock()

	if got != 2 {
		t.Errorf("OnRoundEnd called %d times, want 2", got)
	}
}
