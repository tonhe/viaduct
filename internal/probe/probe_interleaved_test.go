package probe

// probe_interleaved_test.go — Task 6: interleaved ICMP TTL + primary probes
// per round, type-tagged results, embedded-destination guard, and
// primary-only auto-fallback counting.
//
// IPv4 fixtures use RFC 5737 (192.0.2.0/24, 198.51.100.0/24).
// IPv6 fixtures use RFC 3849 (2001:db8::/32).

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv6"
)

// packetIsICMPEcho returns true if pkt looks like an ICMP or ICMPv6 Echo
// Request based on its first byte (type field). ICMPv4 Echo Request = 8;
// ICMPv6 Echo Request = 128.
func packetIsICMPEcho(pkt []byte) bool {
	if len(pkt) < 1 {
		return false
	}
	return pkt[0] == 8 || pkt[0] == 128
}

// TestInterleavedRoundSendsICMPAndPrimary verifies that with UDP primary,
// each TTL in a round produces exactly one ICMP echo-request probe (tier 1)
// plus NumPaths UDP probes (tier 2), and the ICMP tier uses cfg.ICMPID.
func TestInterleavedRoundSendsICMPAndPrimary(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.MaxHops = 2
	cfg.FirstTTL = 1
	cfg.NumPaths = 3
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 1
	cfg.Timeout = shortTestTimeout
	cfg.ICMPID = 0xBEEF // deterministic so we can assert the embedded ID

	target := net.ParseIP("192.0.2.1")
	snd := &fakeSender{}
	tr := newRunTracer(snd, cfg, target)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := tr.run(ctx, nil, make(chan Result, 64)); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	snd.mu.Lock()
	defer snd.mu.Unlock()

	// Expected total: MaxHops * (1 ICMP + NumPaths UDP) = 2 * (1 + 3) = 8.
	wantTotal := 2 * (1 + 3)
	if len(snd.written) != wantTotal {
		t.Fatalf("total probes = %d, want %d", len(snd.written), wantTotal)
	}

	// Per TTL: exactly 1 ICMP and 3 UDP.
	countsByTTL := map[int]struct{ icmp, udp int }{}
	icmpIDs := []int{}
	for _, wp := range snd.written {
		c := countsByTTL[wp.ttl]
		if packetIsICMPEcho(wp.pkt) {
			c.icmp++
			// ICMP header: bytes 0=type, 1=code, 2-3=cksum, 4-5=id, 6-7=seq
			if len(wp.pkt) >= 8 {
				icmpIDs = append(icmpIDs, int(binary.BigEndian.Uint16(wp.pkt[4:6])))
			}
		} else {
			c.udp++
		}
		countsByTTL[wp.ttl] = c
	}
	for ttl := 1; ttl <= 2; ttl++ {
		c := countsByTTL[ttl]
		if c.icmp != 1 {
			t.Errorf("TTL %d: ICMP TTL probe count = %d, want 1", ttl, c.icmp)
		}
		if c.udp != 3 {
			t.Errorf("TTL %d: UDP probe count = %d, want 3", ttl, c.udp)
		}
	}

	// Every tier-1 ICMP probe must carry cfg.ICMPID.
	for _, id := range icmpIDs {
		if id != cfg.ICMPID {
			t.Errorf("ICMP TTL probe ID = 0x%x, want 0x%x", id, cfg.ICMPID)
		}
	}
}

// probeCountMarker records the probe count reported for each TTL.
type probeCountMarker struct {
	mu     sync.Mutex
	probes map[int][]int
}

func (m *probeCountMarker) MarkRoundSent(ttl int, probes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.probes == nil {
		m.probes = map[int][]int{}
	}
	m.probes[ttl] = append(m.probes[ttl], probes)
}

// TestRoundMarkerReportsProbesSent: the round marker is told how many
// TTL-limited probes actually went out for each TTL, so loss can be computed
// per probe. UDP with 3 flows sends 1 ICMP TTL + 3 UDP = 4 per TTL per round.
func TestRoundMarkerReportsProbesSent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.MaxHops = 2
	cfg.FirstTTL = 1
	cfg.NumPaths = 3
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 2
	cfg.Timeout = shortTestTimeout

	snd := &fakeSender{}
	tr := newRunTracer(snd, cfg, net.ParseIP("192.0.2.1"))
	rm := &probeCountMarker{}
	tr.SetOnRoundSent(rm)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.run(ctx, nil, make(chan Result, 64)); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()
	for ttl := 1; ttl <= 2; ttl++ {
		got := rm.probes[ttl]
		if len(got) != 2 || got[0] != 4 || got[1] != 4 {
			t.Errorf("TTL %d: probes reported per round = %v, want [4 4]", ttl, got)
		}
	}
}

// TestRoundMarkerReportsProbesSent_ICMP: ICMP mode sends one probe per TTL.
func TestRoundMarkerReportsProbesSent_ICMP(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.MaxHops = 3
	cfg.FirstTTL = 1
	cfg.NumPaths = 6 // ignored for ICMP
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 1
	cfg.Timeout = shortTestTimeout

	snd := &fakeSender{}
	tr := newRunTracer(snd, cfg, net.ParseIP("192.0.2.1"))
	rm := &probeCountMarker{}
	tr.SetOnRoundSent(rm)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.run(ctx, nil, make(chan Result, 64)); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()
	for ttl := 1; ttl <= 3; ttl++ {
		got := rm.probes[ttl]
		if len(got) != 1 || got[0] != 1 {
			t.Errorf("TTL %d: probes reported = %v, want [1]", ttl, got)
		}
	}
}

// TestICMPModeSkipsPrimary verifies that with ICMP primary, tier 2 is
// skipped — only one probe per TTL fires (the tier-1 ICMP TTL probe).
func TestICMPModeSkipsPrimary(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.MaxHops = 3
	cfg.FirstTTL = 1
	cfg.NumPaths = 6 // must be ignored for ICMP
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 1
	cfg.Timeout = shortTestTimeout
	cfg.ICMPID = 0xCAFE

	target := net.ParseIP("192.0.2.1")
	snd := &fakeSender{}
	tr := newRunTracer(snd, cfg, target)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := tr.run(ctx, nil, make(chan Result, 64)); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	snd.mu.Lock()
	defer snd.mu.Unlock()

	// Expected: exactly one probe per TTL (MaxHops=3 → 3 total).
	if len(snd.written) != 3 {
		t.Fatalf("ICMP mode total probes = %d, want 3 (one per TTL)", len(snd.written))
	}
	for _, wp := range snd.written {
		if !packetIsICMPEcho(wp.pkt) {
			t.Errorf("ICMP mode probe not an ICMP echo (first byte 0x%02x)", wp.pkt[0])
		}
	}
}

// TestListenTagsProbeType_TimeExceededUDP: a TimeExceeded whose embedded
// datagram is a UDP probe matches the outstanding UDP probeRecord and
// emits a Result tagged ProbeUDPTTL.
func TestListenTagsProbeType_TimeExceededUDP(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.RoundDelay = 10 * time.Millisecond
	target := net.ParseIP("192.0.2.1")
	cfg.TargetIP = target
	tr := NewTracer(target, cfg)

	srcPort := cfg.BasePort
	dstPort := 33434 + 4
	key := probeKey{SrcPort: srcPort, DstPort: dstPort, Seq: 0}
	tr.pm.add(key, 4, 0, time.Now(), ProbeUDPTTL)

	inner := buildUDPInnerHeader(net.ParseIP("198.51.100.1"), target, srcPort, dstPort)
	pkt := marshalTimeExceeded(inner)
	conn := newFakeConn()
	conn.push(pkt, fakeAddr{"198.51.100.5"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	results := make(chan Result, 1)
	go tr.listen(ctx, conn, results)

	select {
	case r := <-results:
		if r.ProbeType != ProbeUDPTTL {
			t.Errorf("ProbeType = %v, want ProbeUDPTTL", r.ProbeType)
		}
		if r.IsTarget {
			t.Error("IsTarget should be false for hop response")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for UDP-typed result")
	}
	cancel()
}

// TestListenTagsProbeType_EchoReplyICMPTTL: EchoReply with ID=cfg.ICMPID
// matches an outstanding ProbeICMPTTL and emits ProbeICMPTTL+IsTarget=true.
func TestListenTagsProbeType_EchoReplyICMPTTL(t *testing.T) {
	tr := newListenTracer()
	proto := tr.cfg.Protocol.(*ICMPProtocol)
	id := proto.sessionID
	seq := 42
	key := probeKey{SrcPort: id, DstPort: seq, Seq: 0}
	tr.pm.add(key, 6, 0, time.Now(), ProbeICMPTTL)

	pkt := marshalEchoReply(id, seq)
	conn := newFakeConn()
	conn.push(pkt, fakeAddr{"192.0.2.1"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	results := make(chan Result, 1)
	go tr.listen(ctx, conn, results)

	select {
	case r := <-results:
		if r.ProbeType != ProbeICMPTTL {
			t.Errorf("ProbeType = %v, want ProbeICMPTTL", r.ProbeType)
		}
		if !r.IsTarget {
			t.Error("IsTarget should be true for ICMP TTL echo reply")
		}
		if r.TTL != 6 {
			t.Errorf("TTL = %d, want 6", r.TTL)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ICMP-TTL result")
	}
	cancel()
}

// TestListenTagsProbeType_EchoReplyEchoIDNotMisclassified: an EchoReply
// bearing cfg.EchoID that does NOT match any outstanding probeRecord must
// be silently dropped — it must not be misclassified as ProbeICMPTTL or
// spuriously set IsTarget. (Full ProbeICMPEcho typing arrives in a later
// task once echo-tier state exists.)
func TestListenTagsProbeType_EchoReplyEchoIDNotMisclassified(t *testing.T) {
	tr := newListenTracer()
	tr.cfg.EchoID = 0xE00E
	// Deliberately do NOT add any probeRecord for the echo ID.

	pkt := marshalEchoReply(tr.cfg.EchoID, 1)
	conn := newFakeConn()
	conn.push(pkt, fakeAddr{"192.0.2.1"})

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan Result, 1)
	go tr.listen(ctx, conn, results)

	// Give listen a moment to process, then cancel.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case r := <-results:
		t.Errorf("unmatched EchoID reply should be dropped, got %+v", r)
	default:
	}
}

// TestEmbeddedDestGuard_TimeExceeded: TimeExceeded whose embedded dst IP
// is not cfg.TargetIP is dropped, even when the probe key matches.
func TestEmbeddedDestGuard_TimeExceeded(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.RoundDelay = 10 * time.Millisecond
	target := net.ParseIP("192.0.2.1")
	cfg.TargetIP = target
	tr := NewTracer(target, cfg)

	srcPort := cfg.BasePort
	dstPort := 33434 + 7
	key := probeKey{SrcPort: srcPort, DstPort: dstPort, Seq: 0}
	// Register as if we sent — the key would match if not for the guard.
	tr.pm.add(key, 7, 0, time.Now(), ProbeUDPTTL)

	// Embedded destination is a DIFFERENT IP (e.g., another process' target).
	otherTarget := net.ParseIP("192.0.2.99")
	inner := buildUDPInnerHeader(net.ParseIP("198.51.100.1"), otherTarget, srcPort, dstPort)
	pkt := marshalTimeExceeded(inner)
	conn := newFakeConn()
	conn.push(pkt, fakeAddr{"198.51.100.5"})

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan Result, 1)
	go tr.listen(ctx, conn, results)

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case r := <-results:
		t.Errorf("embedded-dest guard should drop foreign probe, got %+v", r)
	default:
	}
}

// TestAutoFallbackIgnoresICMPTTLReplies: auto-fallback triggers when the
// primary tier has zero responses, even if tier-1 ICMP TTL replies were
// received. Simulates a hostile UDP path where hops answer ICMP but not UDP.
func TestAutoFallbackIgnoresICMPTTLReplies(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewAutoSelector(NewUDPProtocol(33434), NewICMPProtocol(cfg))
	target := net.ParseIP("192.0.2.1")
	cfg.TargetIP = target
	switched := int32(0)
	cfg.OnProtocolSwitch = func(_ string) { atomic.StoreInt32(&switched, 1) }
	tr := NewTracer(target, cfg)

	// Simulate 3 rounds where only tier-1 ICMP TTL replies arrived.
	// listen() would not have incremented responseCount for these, so it
	// remains 0. checkAutoFallback fires unconditionally after round 2.
	// (responseCount is 0 by default.)

	tr.checkAutoFallback()

	auto := cfg.Protocol.(*AutoSelector)
	if !auto.HasSwitched() {
		t.Error("expected fallback when primary got no responses (ICMP TTL replies must not count)")
	}
	if atomic.LoadInt32(&switched) != 1 {
		t.Error("expected OnProtocolSwitch to fire on fallback")
	}
}

// TestListenPrimaryOnlyResponseCount: only ProbeUDPTTL/ProbeTCPTTL matches
// bump responseCount; ProbeICMPTTL matches do not.
func TestListenPrimaryOnlyResponseCount(t *testing.T) {
	// Setup: UDP primary. Two outstanding records — one UDP, one ICMP TTL.
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.RoundDelay = 10 * time.Millisecond
	target := net.ParseIP("192.0.2.1")
	cfg.TargetIP = target
	tr := NewTracer(target, cfg)

	// Register a UDP probe and an ICMP TTL probe.
	udpKey := probeKey{SrcPort: cfg.BasePort, DstPort: 33438, Seq: 0}
	tr.pm.add(udpKey, 4, 0, time.Now(), ProbeUDPTTL)

	// Deliver: a TimeExceeded matching the UDP record.
	inner := buildUDPInnerHeader(net.ParseIP("198.51.100.1"), target, udpKey.SrcPort, udpKey.DstPort)
	pkt := marshalTimeExceeded(inner)
	conn := newFakeConn()
	conn.push(pkt, fakeAddr{"198.51.100.5"})

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan Result, 4)
	go tr.listen(ctx, conn, results)

	// Wait for the UDP result.
	select {
	case <-results:
	case <-time.After(time.Second):
		t.Fatal("expected UDP result")
	}
	// Give listen a beat to bump counters.
	time.Sleep(20 * time.Millisecond)
	cancel()

	if atomic.LoadInt32(&tr.responseCount) != 1 {
		t.Errorf("responseCount = %d, want 1 (UDP only)", atomic.LoadInt32(&tr.responseCount))
	}

	// Now push an ICMP-TTL echo reply and verify it does NOT bump the counter.
	tr2 := NewTracer(target, cfg)
	tr2.cfg.ICMPID = 0xAA55
	icmpKey := probeKey{SrcPort: 0xAA55, DstPort: 9, Seq: 0}
	tr2.pm.add(icmpKey, 9, 0, time.Now(), ProbeICMPTTL)

	echo := marshalEchoReply(0xAA55, 9)
	conn2 := newFakeConn()
	conn2.push(echo, fakeAddr{"192.0.2.1"})

	ctx2, cancel2 := context.WithCancel(context.Background())
	results2 := make(chan Result, 4)
	go tr2.listen(ctx2, conn2, results2)

	select {
	case <-results2:
	case <-time.After(time.Second):
		t.Fatal("expected ICMP-TTL result")
	}
	time.Sleep(20 * time.Millisecond)
	cancel2()

	if atomic.LoadInt32(&tr2.responseCount) != 0 {
		t.Errorf("responseCount = %d, want 0 (ICMP TTL replies must not count)", atomic.LoadInt32(&tr2.responseCount))
	}
}

// ---- IPv6 mirrors (RFC 3849 fixtures) ----

// buildIPv6HeaderTest constructs a 40-byte IPv6 header for test use.
func buildIPv6HeaderTest(nextHeader int, src, dst net.IP) []byte {
	hdr := make([]byte, 40)
	hdr[0] = 0x60 // version=6
	hdr[6] = byte(nextHeader)
	hdr[7] = 64 // hop limit
	copy(hdr[8:24], src.To16())
	copy(hdr[24:40], dst.To16())
	return hdr
}

// buildUDPInnerHeaderV6 constructs an inner IPv6+UDP header for tests.
func buildUDPInnerHeaderV6(src, dst net.IP, srcPort, dstPort int) []byte {
	ipHdr := buildIPv6HeaderTest(17, src, dst) // next=UDP
	udpHdr := make([]byte, 8)
	binary.BigEndian.PutUint16(udpHdr[0:2], uint16(srcPort))
	binary.BigEndian.PutUint16(udpHdr[2:4], uint16(dstPort))
	binary.BigEndian.PutUint16(udpHdr[4:6], 8)
	return append(ipHdr, udpHdr...)
}

// marshalTimeExceededV6 builds an ICMPv6 Time Exceeded message.
func marshalTimeExceededV6(innerData []byte) []byte {
	msg := &icmp.Message{
		Type: ipv6.ICMPTypeTimeExceeded,
		Code: 0,
		Body: &icmp.TimeExceeded{Data: innerData},
	}
	// ICMPv6 marshal wants a pseudo-header address to compute checksum,
	// but ParseMessage doesn't verify checksums when psh is nil — this
	// matches how the existing v4 helpers marshal without a psh.
	b, _ := msg.Marshal(nil)
	return b
}

// marshalEchoReplyV6 builds an ICMPv6 Echo Reply message.
func marshalEchoReplyV6(id, seq int) []byte {
	msg := &icmp.Message{
		Type: ipv6.ICMPTypeEchoReply,
		Code: 0,
		Body: &icmp.Echo{ID: id, Seq: seq},
	}
	b, _ := msg.Marshal(nil)
	return b
}

// TestListenTagsProbeType_V6 mirrors TestListenTagsProbeType_TimeExceededUDP
// and TestListenTagsProbeType_EchoReplyICMPTTL with RFC 3849 fixtures.
func TestListenTagsProbeType_V6(t *testing.T) {
	target := net.ParseIP("2001:db8::1")
	// (a) TimeExceeded / UDP inner
	cfg := DefaultConfig()
	cfg.IPVersion = 6
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.RoundDelay = 10 * time.Millisecond
	cfg.TargetIP = target
	tr := NewTracer(target, cfg)

	srcPort := cfg.BasePort
	dstPort := 33434 + 4
	key := probeKey{SrcPort: srcPort, DstPort: dstPort, Seq: 0}
	tr.pm.add(key, 4, 0, time.Now(), ProbeUDPTTL)

	inner := buildUDPInnerHeaderV6(net.ParseIP("2001:db8::abcd"), target, srcPort, dstPort)
	pkt := marshalTimeExceededV6(inner)
	conn := newFakeConn()
	conn.push(pkt, fakeAddr{"2001:db8::5"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	results := make(chan Result, 1)
	go tr.listen(ctx, conn, results)

	select {
	case r := <-results:
		if r.ProbeType != ProbeUDPTTL {
			t.Errorf("v6 ProbeType = %v, want ProbeUDPTTL", r.ProbeType)
		}
	case <-time.After(time.Second):
		t.Fatal("v6: timed out waiting for UDP-typed result")
	}
	cancel()

	// (b) EchoReply matching ICMP TTL probe
	cfg2 := DefaultConfig()
	cfg2.IPVersion = 6
	cfg2.Protocol = NewICMPProtocol(cfg2)
	cfg2.RoundDelay = 10 * time.Millisecond
	cfg2.TargetIP = target
	tr2 := NewTracer(target, cfg2)
	proto := tr2.cfg.Protocol.(*ICMPProtocol)
	id := proto.sessionID
	seq := 3
	key2 := probeKey{SrcPort: id, DstPort: seq, Seq: 0}
	tr2.pm.add(key2, 10, 0, time.Now(), ProbeICMPTTL)

	pkt2 := marshalEchoReplyV6(id, seq)
	conn2 := newFakeConn()
	conn2.push(pkt2, fakeAddr{"2001:db8::1"})

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	results2 := make(chan Result, 1)
	go tr2.listen(ctx2, conn2, results2)

	select {
	case r := <-results2:
		if r.ProbeType != ProbeICMPTTL {
			t.Errorf("v6 ProbeType = %v, want ProbeICMPTTL", r.ProbeType)
		}
		if !r.IsTarget {
			t.Error("v6 IsTarget should be true for ICMP TTL echo reply")
		}
	case <-time.After(time.Second):
		t.Fatal("v6: timed out waiting for ICMP-TTL echo result")
	}
	cancel2()
}

// TestEmbeddedDestGuard_V6: mirrors v4 embedded-dest guard with RFC 3849.
func TestEmbeddedDestGuard_V6(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 6
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.RoundDelay = 10 * time.Millisecond
	target := net.ParseIP("2001:db8::1")
	cfg.TargetIP = target
	tr := NewTracer(target, cfg)

	srcPort := cfg.BasePort
	dstPort := 33434 + 7
	key := probeKey{SrcPort: srcPort, DstPort: dstPort, Seq: 0}
	tr.pm.add(key, 7, 0, time.Now(), ProbeUDPTTL)

	// Embedded destination is a foreign v6 IP.
	otherTarget := net.ParseIP("2001:db8::dead")
	inner := buildUDPInnerHeaderV6(net.ParseIP("2001:db8::abcd"), otherTarget, srcPort, dstPort)
	pkt := marshalTimeExceededV6(inner)
	conn := newFakeConn()
	conn.push(pkt, fakeAddr{"2001:db8::5"})

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan Result, 1)
	go tr.listen(ctx, conn, results)

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case r := <-results:
		t.Errorf("v6 embedded-dest guard should drop foreign probe, got %+v", r)
	default:
	}
}

// TestTargetDetectionViaICMPTTLProbe: UDP primary, fake conn. Deliver an
// EchoReply with ID=cfg.ICMPID matching the outstanding ICMP-TTL probe for
// TTL 5 where peer == target. Assert Result{IsTarget: true, TTL: 5,
// ProbeType: ProbeICMPTTL} and that subsequent rounds cap maxTTL at 5
// (fake sender sees no probes for TTL > 5 in round 2).
func TestTargetDetectionViaICMPTTLProbe(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.MaxHops = 10
	cfg.FirstTTL = 1
	cfg.NumPaths = 1
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 2
	cfg.ICMPID = 0xBEEF
	target := net.ParseIP("192.0.2.1")
	cfg.TargetIP = target

	// Seed a ICMP-TTL probe record for TTL 5 so listen() can match the reply.
	tr := NewTracer(target, cfg)
	icmpKey := probeKey{SrcPort: cfg.ICMPID, DstPort: 5, Seq: 0}
	tr.pm.add(icmpKey, 5, 0, time.Now(), ProbeICMPTTL)

	// Build an EchoReply from the target with ID=cfg.ICMPID, Seq=5.
	pkt := marshalEchoReply(cfg.ICMPID, 5)
	conn := newFakeConn()
	conn.push(pkt, fakeAddr{"192.0.2.1"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	results := make(chan Result, 16)
	go tr.listen(ctx, conn, results)

	select {
	case r := <-results:
		if r.ProbeType != ProbeICMPTTL {
			t.Errorf("ProbeType = %v, want ProbeICMPTTL", r.ProbeType)
		}
		if !r.IsTarget {
			t.Error("IsTarget should be true when EchoReply peer == target")
		}
		if r.TTL != 5 {
			t.Errorf("TTL = %d, want 5", r.TTL)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for target-detection result")
	}
	cancel()

	// Verify that targetTTL was stored (caps subsequent rounds).
	stored := atomic.LoadInt32(&tr.targetTTL)
	if stored != 5 {
		t.Errorf("targetTTL = %d, want 5", stored)
	}

	// Round-cap verification: wire a fakeSender, set targetTTL=5, run 1 round
	// with MaxHops=10 — no probe should have TTL > 5.
	snd := &fakeSender{}
	cfg2 := DefaultConfig()
	cfg2.IPVersion = 4
	cfg2.Protocol = NewUDPProtocol(33434)
	cfg2.MaxHops = 10
	cfg2.FirstTTL = 1
	cfg2.NumPaths = 1
	cfg2.ProbeDelay = 0
	cfg2.RoundDelay = 5 * time.Millisecond
	cfg2.MaxRounds = 1
	cfg2.Timeout = shortTestTimeout
	cfg2.ICMPID = 0xBEEF
	tr2 := newRunTracer(snd, cfg2, target)
	atomic.StoreInt32(&tr2.targetTTL, 5) // simulate prior detection

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := tr2.run(ctx2, nil, make(chan Result, 64)); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	cancel2()

	snd.mu.Lock()
	defer snd.mu.Unlock()
	for _, wp := range snd.written {
		if wp.ttl > 5 {
			t.Errorf("probe sent for TTL %d > targetTTL 5 — round cap not applied", wp.ttl)
		}
	}
}

type timingRoundMarker struct{ last time.Time }

func (m *timingRoundMarker) MarkRoundSent(_ int, _ int) { m.last = time.Now() }

// TestRoundClosesAfterCollectionWindow verifies OnRoundEnd fires only after the
// inter-round pause, not immediately after sending. Responses arrive through an
// async pipeline (listener → channel → UI); closing the round the instant sends
// finish counts still-in-flight responses as loss even though they arrive and
// record RTT a few ms later. The pause is the collection window, so the gap
// between the last send and the round close must be ~RoundDelay, not ~0.
func TestRoundClosesAfterCollectionWindow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.MaxHops = 2
	cfg.FirstTTL = 1
	cfg.NumPaths = 1
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 60 * time.Millisecond
	cfg.MaxRounds = 1
	cfg.Timeout = shortTestTimeout

	snd := &fakeSender{}
	tr := newRunTracer(snd, cfg, net.ParseIP("192.0.2.1"))

	rm := &timingRoundMarker{}
	tr.SetOnRoundSent(rm)
	var roundEndAt time.Time
	tr.SetOnRoundEnd(func() { roundEndAt = time.Now() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := tr.run(ctx, nil, make(chan Result, 64)); err != nil {
		t.Fatalf("run: %v", err)
	}

	if rm.last.IsZero() || roundEndAt.IsZero() {
		t.Fatal("expected both MarkRoundSent and OnRoundEnd to be called")
	}
	if gap := roundEndAt.Sub(rm.last); gap < cfg.RoundDelay/2 {
		t.Errorf("round closed %v after last send; want >= ~RoundDelay (%v) so in-flight responses are counted, not dropped as loss", gap, cfg.RoundDelay)
	}
}
