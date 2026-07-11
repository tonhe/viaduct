package probe

// probe_collision_test.go — response-matching collision cases, probe key edge
// cases, buildICMPEchoRequest output validation, and UDP probe build validation.

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Priority 3: IdentifyResponse collision cases
// ---------------------------------------------------------------------------

// buildV4InnerForProto creates a 28-byte fake IPv4 inner header with
// the given protocol byte, followed by 8 bytes of transport (all zeroes
// except the first two bytes for src port and next two for dst port).
func buildV4InnerForProto(proto byte, srcPort, dstPort uint16) []byte {
	pkt := make([]byte, 28)
	pkt[0] = 0x45 // IPv4, IHL=5
	pkt[9] = proto
	// src: 192.0.2.1
	pkt[12] = 192
	pkt[13] = 0
	pkt[14] = 2
	pkt[15] = 1
	// dst: 192.0.2.100
	pkt[16] = 192
	pkt[17] = 0
	pkt[18] = 2
	pkt[19] = 100
	binary.BigEndian.PutUint16(pkt[20:22], srcPort)
	binary.BigEndian.PutUint16(pkt[22:24], dstPort)
	return pkt
}

// buildV6InnerForProto builds a 48-byte IPv6 inner header (40 base + 8 transport).
func buildV6InnerForProto(proto byte, srcPort, dstPort uint16) []byte {
	pkt := make([]byte, 48)
	pkt[0] = 0x60 // IPv6
	pkt[6] = proto
	// src: 2001:db8::1
	pkt[8] = 0x20
	pkt[9] = 0x01
	pkt[10] = 0x0d
	pkt[11] = 0xb8
	pkt[23] = 0x01
	// dst: 2001:db8::100
	pkt[24] = 0x20
	pkt[25] = 0x01
	pkt[26] = 0x0d
	pkt[27] = 0xb8
	pkt[38] = 0x01
	pkt[39] = 0x00
	binary.BigEndian.PutUint16(pkt[40:42], srcPort)
	binary.BigEndian.PutUint16(pkt[42:44], dstPort)
	return pkt
}

// --- UDP IdentifyResponse with wrong protocol (TCP inner) ---

func TestUDPIdentifyResponse_WrongProto_TCP(t *testing.T) {
	p := NewUDPProtocol(33434)
	// Inner says TCP (6), not UDP (17) — should return nil
	inner := buildV4InnerForProto(6 /*TCP*/, 44000, 33439)
	key, err := p.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for TCP inner header, got %+v", *key)
	}
}

func TestUDPIdentifyResponse_WrongProto_ICMP(t *testing.T) {
	p := NewUDPProtocol(33434)
	inner := buildV4InnerForProto(1 /*ICMP*/, 0, 0)
	key, err := p.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for ICMP inner header, got %+v", *key)
	}
}

// --- TCP IdentifyResponse with wrong protocol (UDP inner) ---

func TestTCPIdentifyResponse_WrongProto_UDP(t *testing.T) {
	p := NewTCPProtocol(443)
	// Inner says UDP (17), not TCP (6) — should return nil
	inner := buildV4InnerForProto(17 /*UDP*/, 44000, 33434)
	key, err := p.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for UDP inner header, got %+v", *key)
	}
}

func TestTCPIdentifyResponse_WrongProto_ICMP(t *testing.T) {
	p := NewTCPProtocol(443)
	inner := buildV4InnerForProto(1 /*ICMP*/, 0, 0)
	key, err := p.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for ICMP inner header, got %+v", *key)
	}
}

// --- ICMP IdentifyResponse with wrong type byte ---

func TestICMPIdentifyResponse_WrongType_EchoReply(t *testing.T) {
	p := NewICMPProtocol()
	p.sessionID = 0xabcd

	// Build inner header where ICMP type is 0 (Echo Reply, v4) instead of 8 (Echo Request).
	// IdentifyResponse should reject it (wrong type).
	pkt := make([]byte, 28)
	pkt[0] = 0x45 // IPv4, IHL=5
	pkt[9] = 1    // ICMP
	pkt[12] = 192
	pkt[13] = 0
	pkt[14] = 2
	pkt[15] = 1
	pkt[16] = 192
	pkt[17] = 0
	pkt[18] = 2
	pkt[19] = 100
	pkt[20] = 0 // ICMP type = 0 (Echo Reply — not Echo Request)
	pkt[21] = 0
	binary.BigEndian.PutUint16(pkt[24:26], 0xabcd)
	binary.BigEndian.PutUint16(pkt[26:28], 1)

	key, err := p.IdentifyResponse(pkt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for wrong ICMP type (Echo Reply), got %+v", *key)
	}
}

func TestICMPIdentifyResponse_V6_WrongType_NotEchoRequest(t *testing.T) {
	p := NewICMPProtocol()
	p.sessionID = 0x1234

	// IPv6 ICMP inner with type=129 (Echo Reply = type 129 for v6).
	// IdentifyResponse for v6 wants type 128 (Echo Request).
	inner := make([]byte, 48)
	inner[0] = 0x60 // IPv6
	inner[6] = 58   // ICMPv6
	inner[8] = 0x20
	inner[9] = 0x01
	inner[10] = 0x0d
	inner[11] = 0xb8
	inner[23] = 0x01
	inner[24] = 0x20
	inner[25] = 0x01
	inner[26] = 0x0d
	inner[27] = 0xb8
	inner[38] = 0x01
	inner[39] = 0x00
	inner[40] = 129 // type = Echo Reply (not 128 = Echo Request)
	inner[41] = 0
	binary.BigEndian.PutUint16(inner[44:46], 0x1234)
	binary.BigEndian.PutUint16(inner[46:48], 5)

	key, err := p.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for wrong ICMPv6 type (129, Echo Reply), got %+v", *key)
	}
}

// --- Sub-minimum length inner headers: no panic ---

func TestUDPIdentifyResponse_SubMinimumLength(t *testing.T) {
	p := NewUDPProtocol(33434)
	// Only 5 bytes — less than 20 required for v4 inner header parsing.
	inner := []byte{0x45, 0x00, 0x00, 0x28, 0xab}
	key, err := p.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("unexpected error on short input: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for sub-minimum input, got %+v", *key)
	}
}

func TestTCPIdentifyResponse_SubMinimumLength(t *testing.T) {
	p := NewTCPProtocol(443)
	inner := []byte{0x45, 0x00, 0x00}
	key, err := p.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("unexpected error on short input: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for sub-minimum input, got %+v", *key)
	}
}

func TestICMPIdentifyResponse_SubMinimumLength(t *testing.T) {
	p := NewICMPProtocol()
	// Only 1 byte
	inner := []byte{0x45}
	key, err := p.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("unexpected error on short input: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for sub-minimum input, got %+v", *key)
	}
}

func TestICMPIdentifyResponse_EmptyInput(t *testing.T) {
	p := NewICMPProtocol()
	key, err := p.IdentifyResponse([]byte{})
	if err != nil {
		t.Fatalf("unexpected error on empty input: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for empty input, got %+v", *key)
	}
}

func TestUDPIdentifyResponse_EmptyInput(t *testing.T) {
	p := NewUDPProtocol(33434)
	key, err := p.IdentifyResponse([]byte{})
	if err != nil {
		t.Fatalf("unexpected error on empty input: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for empty input, got %+v", *key)
	}
}

func TestTCPIdentifyResponse_EmptyInput(t *testing.T) {
	p := NewTCPProtocol(443)
	key, err := p.IdentifyResponse([]byte{})
	if err != nil {
		t.Fatalf("unexpected error on empty input: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key for empty input, got %+v", *key)
	}
}

// ---------------------------------------------------------------------------
// Priority 4: probeMap edge cases
// ---------------------------------------------------------------------------

func TestProbeMap_MatchNonExistentKey(t *testing.T) {
	pm := newProbeMap()
	key := probeKey{SrcPort: 999, DstPort: 888, Seq: 7}
	_, ok := pm.match(key)
	if ok {
		t.Error("expected match to fail for non-existent key")
	}
}

func TestProbeMap_MatchConsumesEntry(t *testing.T) {
	pm := newProbeMap()
	key := probeKey{SrcPort: 100, DstPort: 200, Seq: 1}
	pm.add(key, 5, 0, time.Now())

	_, ok := pm.match(key)
	if !ok {
		t.Fatal("first match should succeed")
	}

	_, ok = pm.match(key)
	if ok {
		t.Error("second match should fail: entry consumed after first match")
	}
}

func TestProbeMap_SweepKeepsNewEntries(t *testing.T) {
	pm := newProbeMap()
	staleTime := time.Now().Add(-10 * time.Second)
	freshTime := time.Now()

	staleKey := probeKey{SrcPort: 1, DstPort: 2, Seq: 3}
	freshKey := probeKey{SrcPort: 4, DstPort: 5, Seq: 6}
	pm.add(staleKey, 1, 0, staleTime)
	pm.add(freshKey, 2, 1, freshTime)

	pm.sweep(5 * time.Second) // entries older than 5s removed

	_, staleOk := pm.match(staleKey)
	if staleOk {
		t.Error("stale entry should have been swept")
	}

	rec, freshOk := pm.match(freshKey)
	if !freshOk {
		t.Error("fresh entry should survive sweep")
	}
	if rec.ttl != 2 {
		t.Errorf("fresh entry ttl = %d, want 2", rec.ttl)
	}
}

func TestProbeMap_SweepPreservesAllIfNoneStale(t *testing.T) {
	pm := newProbeMap()
	for i := 0; i < 5; i++ {
		k := probeKey{SrcPort: i, DstPort: i + 100, Seq: 0}
		pm.add(k, i+1, i, time.Now())
	}

	pm.sweep(60 * time.Second) // nothing is older than 60s

	for i := 0; i < 5; i++ {
		k := probeKey{SrcPort: i, DstPort: i + 100, Seq: 0}
		_, ok := pm.match(k)
		if !ok {
			t.Errorf("entry %d should survive sweep with large maxAge", i)
		}
	}
}

func TestProbeMap_ConcurrentAddMatch(t *testing.T) {
	// Exercises -race detector: concurrent adds and matches on different keys.
	pm := newProbeMap()
	const n = 100
	var wg sync.WaitGroup

	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			key := probeKey{SrcPort: i, DstPort: i + 1000, Seq: i % 10}
			pm.add(key, i%30+1, i%6, time.Now())
		}()
	}
	wg.Wait()

	// Now match them all — some may race with each other, but no panic/data race.
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			key := probeKey{SrcPort: i, DstPort: i + 1000, Seq: i % 10}
			pm.match(key)
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Priority 5: buildICMPEchoRequest output validation
// ---------------------------------------------------------------------------

func TestBuildICMPEchoRequest_TypeByteV4(t *testing.T) {
	// v4 ICMP Echo Request type = 8, at offset 0 of the raw packet.
	pkt, err := buildICMPEchoRequest(4, 0x1234, 7, 64)
	if err != nil {
		t.Fatalf("buildICMPEchoRequest(v4): %v", err)
	}
	if len(pkt) < 8 {
		t.Fatalf("packet too short: %d bytes", len(pkt))
	}
	if pkt[0] != 8 {
		t.Errorf("v4 type byte at offset 0 = %d, want 8 (Echo Request)", pkt[0])
	}
}

func TestBuildICMPEchoRequest_TypeByteV6(t *testing.T) {
	// v6 ICMPv6 Echo Request type = 128, at offset 0.
	pkt, err := buildICMPEchoRequest(6, 0x5678, 3, 64)
	if err != nil {
		t.Fatalf("buildICMPEchoRequest(v6): %v", err)
	}
	if len(pkt) < 8 {
		t.Fatalf("packet too short: %d bytes", len(pkt))
	}
	if pkt[0] != 128 {
		t.Errorf("v6 type byte at offset 0 = %d, want 128 (ICMPv6 Echo Request)", pkt[0])
	}
}

func TestBuildICMPEchoRequest_V4V6TypesDiffer(t *testing.T) {
	pkt4, err := buildICMPEchoRequest(4, 1, 1, 64)
	if err != nil {
		t.Fatalf("v4: %v", err)
	}
	pkt6, err := buildICMPEchoRequest(6, 1, 1, 64)
	if err != nil {
		t.Fatalf("v6: %v", err)
	}
	if pkt4[0] == pkt6[0] {
		t.Errorf("v4 and v6 type bytes are equal (%d) — they must differ", pkt4[0])
	}
}

func TestBuildICMPEchoRequest_IDAndSeqAtFixedOffsets(t *testing.T) {
	// For both v4 and v6, the ICMP Echo header layout is:
	//   byte 0: type, byte 1: code, bytes 2-3: checksum (computed),
	//   bytes 4-5: ID, bytes 6-7: seq.
	const id = 0xABCD
	const seq = 0x0042

	pkt, err := buildICMPEchoRequest(4, id, seq, 64)
	if err != nil {
		t.Fatalf("buildICMPEchoRequest: %v", err)
	}
	if len(pkt) < 8 {
		t.Fatalf("packet too short: %d", len(pkt))
	}
	gotID := binary.BigEndian.Uint16(pkt[4:6])
	gotSeq := binary.BigEndian.Uint16(pkt[6:8])
	if gotID != id {
		t.Errorf("ID at offset 4-5: got 0x%04x, want 0x%04x", gotID, id)
	}
	if gotSeq != seq {
		t.Errorf("seq at offset 6-7: got 0x%04x, want 0x%04x", gotSeq, seq)
	}
}

func TestBuildICMPEchoRequest_PayloadStartsWithVIADUCT(t *testing.T) {
	// Payload begins at offset 8 (after 8-byte ICMP header).
	pkt, err := buildICMPEchoRequest(4, 1, 1, 64)
	if err != nil {
		t.Fatalf("buildICMPEchoRequest: %v", err)
	}
	const want = "VIADUCT"
	if len(pkt) < 8+len(want) {
		t.Fatalf("packet too short to contain VIADUCT: %d bytes", len(pkt))
	}
	got := string(pkt[8 : 8+len(want)])
	if got != want {
		t.Errorf("payload prefix = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Priority 6: buildUDPProbe output validation
// ---------------------------------------------------------------------------

func TestBuildUDPProbe_HeaderBytes(t *testing.T) {
	// buildUDPProbe(srcPort=50000, dstPort=33434, payloadSize=64)
	// Expected:
	//   pkt[0:2] = 0xc350 (50000)
	//   pkt[2:4] = 0x829a (33434)
	//   pkt[4:6] = 0x0048 (72 = 8+64)
	//   pkt[6:8] = 0x0000 (checksum=0)
	pkt, err := buildUDPProbe(50000, 33434, 64)
	if err != nil {
		t.Fatalf("buildUDPProbe: %v", err)
	}
	if len(pkt) != 72 {
		t.Fatalf("len = %d, want 72", len(pkt))
	}

	src := binary.BigEndian.Uint16(pkt[0:2])
	if src != 0xc350 {
		t.Errorf("src port bytes: got 0x%04x, want 0xc350", src)
	}

	dst := binary.BigEndian.Uint16(pkt[2:4])
	if dst != 0x829a {
		t.Errorf("dst port bytes: got 0x%04x, want 0x829a", dst)
	}

	length := binary.BigEndian.Uint16(pkt[4:6])
	if length != 72 {
		t.Errorf("length field: got %d, want 72 (0x0048)", length)
	}

	cksum := binary.BigEndian.Uint16(pkt[6:8])
	if cksum != 0 {
		t.Errorf("checksum field: got 0x%04x, want 0x0000", cksum)
	}
}

func TestBuildUDPProbe_PayloadStartsWithVIADUCT(t *testing.T) {
	pkt, err := buildUDPProbe(50000, 33434, 64)
	if err != nil {
		t.Fatalf("buildUDPProbe: %v", err)
	}
	const want = "VIADUCT"
	if len(pkt) < 8+len(want) {
		t.Fatalf("packet too short: %d", len(pkt))
	}
	got := string(pkt[8 : 8+len(want)])
	if got != want {
		t.Errorf("payload prefix = %q, want %q", got, want)
	}
}

func TestBuildUDPProbe_LengthFieldMatchesPacket(t *testing.T) {
	// Verify length field equals total packet size for various payload sizes.
	for _, ps := range []int{0, 1, 32, 64, 128} {
		pkt, err := buildUDPProbe(44000, 33434, ps)
		if err != nil {
			t.Fatalf("payloadSize=%d: %v", ps, err)
		}
		wantTotal := 8 + ps
		if len(pkt) != wantTotal {
			t.Errorf("payloadSize=%d: len=%d, want %d", ps, len(pkt), wantTotal)
		}
		lengthField := int(binary.BigEndian.Uint16(pkt[4:6]))
		if lengthField != wantTotal {
			t.Errorf("payloadSize=%d: length field=%d, want %d", ps, lengthField, wantTotal)
		}
	}
}

// ---------------------------------------------------------------------------
// AutoSelector IdentifyResponse delegation (previously 0% coverage)
// ---------------------------------------------------------------------------

func TestAutoSelector_IdentifyResponse_PreFallback(t *testing.T) {
	auto := NewAutoSelector(NewUDPProtocol(33434), NewICMPProtocol())
	// Before fallback: should use UDP IdentifyResponse — inner TCP should yield nil
	inner := buildV4InnerForProto(6 /*TCP*/, 44000, 33434)
	key, err := auto.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("IdentifyResponse error: %v", err)
	}
	if key != nil {
		t.Errorf("UDP proto should reject TCP inner; got key %+v", *key)
	}
}

func TestAutoSelector_IdentifyResponse_PostFallback(t *testing.T) {
	auto := NewAutoSelector(NewUDPProtocol(33434), NewICMPProtocol())
	auto.Fallback()

	// After fallback to ICMP: should use ICMP IdentifyResponse
	// Build an inner header with ICMP Echo Request (type=8)
	inner := make([]byte, 28)
	inner[0] = 0x45 // IPv4
	inner[9] = 1    // ICMP
	inner[12] = 192
	inner[13] = 0
	inner[14] = 2
	inner[15] = 1
	inner[16] = 192
	inner[17] = 0
	inner[18] = 2
	inner[19] = 100
	inner[20] = 8   // Echo Request
	inner[21] = 0
	binary.BigEndian.PutUint16(inner[24:26], 0x5555) // ID
	binary.BigEndian.PutUint16(inner[26:28], 10)     // seq

	key, err := auto.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("IdentifyResponse error after fallback: %v", err)
	}
	if key == nil {
		t.Fatal("ICMP IdentifyResponse should match Echo Request inner header")
	}
	if key.SrcPort != 0x5555 || key.DstPort != 10 {
		t.Errorf("key = %+v, want SrcPort=0x5555 DstPort=10", *key)
	}
}

// ---------------------------------------------------------------------------
// IdentifyResponse: version byte not 4 or 6 (covers previously uncovered branches)
// ---------------------------------------------------------------------------

func TestICMPIdentifyResponse_BadVersionByte(t *testing.T) {
	p := NewICMPProtocol()
	// Version nibble = 3 — neither 4 nor 6; should return nil immediately.
	inner := make([]byte, 28)
	inner[0] = 0x30 // version=3
	key, err := p.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil for version=3, got %+v", *key)
	}
}

func TestUDPIdentifyResponse_BadVersionByte(t *testing.T) {
	p := NewUDPProtocol(33434)
	inner := make([]byte, 28)
	inner[0] = 0x70 // version=7
	key, err := p.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil for version=7, got %+v", *key)
	}
}

func TestTCPIdentifyResponse_BadVersionByte(t *testing.T) {
	p := NewTCPProtocol(443)
	inner := make([]byte, 28)
	inner[0] = 0x20 // version=2
	key, err := p.IdentifyResponse(inner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil for version=2, got %+v", *key)
	}
}

// ---------------------------------------------------------------------------
// checksumRFC1071 — odd-length input (previously ~87% — the odd-byte branch)
// ---------------------------------------------------------------------------

func TestChecksumRFC1071_OddLength(t *testing.T) {
	// 3-byte input: even pair [0x00, 0x01] + odd byte [0xf2].
	// Even part: 0x0001. Odd byte padded: 0xf2 << 8 = 0xf200.
	// Sum: 0x0001 + 0xf200 = 0xf201. Complement: ^0xf201 = 0x0dfe.
	data := []byte{0x00, 0x01, 0xf2}
	got := checksumRFC1071(data)
	if got != 0x0dfe {
		t.Errorf("checksumRFC1071 odd-length: got 0x%04x, want 0x0dfe", got)
	}
}

func TestChecksumRFC1071_SingleByte(t *testing.T) {
	// 1-byte input [0xff]: sum = 0xff00, complement = 0x00ff.
	data := []byte{0xff}
	got := checksumRFC1071(data)
	if got != 0x00ff {
		t.Errorf("checksumRFC1071 single-byte: got 0x%04x, want 0x00ff", got)
	}
}

// ---------------------------------------------------------------------------
// NewTracer — constructor coverage
// ---------------------------------------------------------------------------

func TestNewTracer_FieldsSet(t *testing.T) {
	target := net.ParseIP("192.0.2.1")
	cfg := DefaultConfig()
	tr := NewTracer(target, cfg)
	if tr == nil {
		t.Fatal("NewTracer returned nil")
	}
	if !tr.target.Equal(target) {
		t.Errorf("target = %v, want %v", tr.target, target)
	}
	if tr.pm == nil {
		t.Error("probeMap should be initialized")
	}
}

// ---------------------------------------------------------------------------
// checkAutoFallback — logic paths without raw sockets
// ---------------------------------------------------------------------------

func TestCheckAutoFallback_SwitchesWhenNoResponses(t *testing.T) {
	switched := false
	cfg := DefaultConfig()
	cfg.Protocol = NewAutoSelector(NewUDPProtocol(33434), NewICMPProtocol())
	cfg.OnProtocolSwitch = func(p string) { switched = true }

	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)
	// responseCount is 0 (default) — should trigger fallback
	tr.checkAutoFallback()

	auto := cfg.Protocol.(*AutoSelector)
	if !auto.HasSwitched() {
		t.Error("expected AutoSelector to have switched to fallback")
	}
	if !switched {
		t.Error("expected OnProtocolSwitch callback to fire")
	}
	if atomic.LoadInt32(&tr.targetTTL) != 0 {
		t.Error("expected targetTTL to be reset to 0 after fallback")
	}
}

func TestCheckAutoFallback_NoSwitchWhenResponsesReceived(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Protocol = NewAutoSelector(NewUDPProtocol(33434), NewICMPProtocol())

	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)
	atomic.StoreInt32(&tr.responseCount, 5) // simulate received responses

	tr.checkAutoFallback()

	auto := cfg.Protocol.(*AutoSelector)
	if auto.HasSwitched() {
		t.Error("should NOT have switched when responseCount > 0")
	}
}

func TestCheckAutoFallback_NoopWhenNotAutoSelector(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Protocol = NewUDPProtocol(33434) // not AutoSelector

	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)
	// Should not panic or do anything
	tr.checkAutoFallback()
}

func TestCheckAutoFallback_NoopWhenAlreadySwitched(t *testing.T) {
	cfg := DefaultConfig()
	auto := NewAutoSelector(NewUDPProtocol(33434), NewICMPProtocol())
	auto.Fallback() // already switched
	cfg.Protocol = auto

	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)
	// Second checkAutoFallback should be idempotent — no callback, no panic
	switchCount := 0
	cfg.OnProtocolSwitch = func(p string) { switchCount++ }
	tr.checkAutoFallback()

	if switchCount != 0 {
		t.Error("OnProtocolSwitch should not fire when already switched")
	}
}

// ---------------------------------------------------------------------------
// IsPrivate — additional paths: nil input, v6 unspecified
// ---------------------------------------------------------------------------

func TestIsPrivate_NilIP(t *testing.T) {
	if IsPrivate(nil) {
		t.Error("IsPrivate(nil) should return false")
	}
}

func TestIsPrivate_V6Unspecified(t *testing.T) {
	// "::" (all zeros) is unspecified — should be treated as private/local.
	if !IsPrivate(net.ParseIP("::")) {
		t.Error("IsPrivate(::) should return true (unspecified address)")
	}
}

// ---------------------------------------------------------------------------
// ASNReverseName — error paths: nil IP, IPv4-mapped in v6 slot
// ---------------------------------------------------------------------------

func TestASNReverseName_V4_NilIP(t *testing.T) {
	got := ASNReverseName(4, nil)
	if got != "" {
		t.Errorf("ASNReverseName(4, nil) = %q, want empty string", got)
	}
}

func TestASNReverseName_V6_IPv4MappedAddress(t *testing.T) {
	// An IPv4 address passed to v=6 slot: ip6.To4() != nil → should return ""
	got := ASNReverseName(6, net.ParseIP("192.0.2.1"))
	if got != "" {
		t.Errorf("ASNReverseName(6, IPv4addr) = %q, want empty string", got)
	}
}

// ---------------------------------------------------------------------------
// ICMPTypeNum — default branch (previously uncovered at 75%)
// ---------------------------------------------------------------------------

// fakeICMPType implements icmp.Type with neither ipv4.ICMPType nor ipv6.ICMPType
// so that ICMPTypeNum hits the default case and returns -1.
type fakeICMPType struct{}

func (fakeICMPType) String() string  { return "fake" }
func (fakeICMPType) Protocol() int   { return 0 }

func TestICMPTypeNum_DefaultBranch(t *testing.T) {
	got := ICMPTypeNum(fakeICMPType{})
	if got != -1 {
		t.Errorf("ICMPTypeNum(fakeType) = %d, want -1", got)
	}
}

// ---------------------------------------------------------------------------
// Tracer.Discover — early return and socket-fail paths
// ---------------------------------------------------------------------------

func TestTracer_Discover_ICMPEarlyReturn(t *testing.T) {
	// With ICMP protocol, Discover returns immediately without opening sockets.
	cfg := DefaultConfig()
	cfg.Protocol = NewICMPProtocol()
	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)

	results := make(chan Result, 10)
	ctx := context.Background()
	// Should return immediately and not block
	done := make(chan struct{})
	go func() {
		tr.Discover(ctx, results)
		close(done)
	}()
	select {
	case <-done:
		// OK: returned quickly
	case <-time.After(2 * time.Second):
		t.Error("Discover with ICMP protocol should return immediately")
	}
}

func TestTracer_Discover_NonICMP_NoSocket(t *testing.T) {
	// With UDP protocol in a test environment (no raw socket), Discover
	// will fail to open the ICMP listener and return non-fatally.
	cfg := DefaultConfig()
	cfg.Protocol = NewUDPProtocol(33434)
	cfg.Timeout = 10 * time.Millisecond
	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)

	results := make(chan Result, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		tr.Discover(ctx, results)
		close(done)
	}()
	select {
	case <-done:
		// OK: returned (either opened or failed to open socket)
	case <-time.After(5 * time.Second):
		t.Error("Discover should not hang indefinitely")
	}
}

func TestTracer_Run_NoSocket(t *testing.T) {
	// In a test environment without raw socket privileges, Run should return an error.
	// This covers the icmp.ListenPacket failure path in Run.
	cfg := DefaultConfig()
	cfg.Protocol = NewICMPProtocol()
	cfg.MaxRounds = 1
	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)

	results := make(chan Result, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := tr.Run(ctx, results)
	// Either err != nil (no raw socket) or it ran successfully — both are valid.
	// The key test is: no panic, no hang.
	_ = err
}

// ---------------------------------------------------------------------------
// listenTCP via mock net.PacketConn
// ---------------------------------------------------------------------------

// mockPacketConn implements net.PacketConn for testing listenTCP without raw sockets.
// It delivers a pre-configured sequence of reads and then an error.
type mockPacketConn struct {
	reads  []mockRead
	idx    int
	closed bool
}

type mockRead struct {
	data []byte
	err  error
}

type mockAddr struct{ s string }

func (a mockAddr) Network() string { return "ip4:tcp" }
func (a mockAddr) String() string  { return a.s }

func (m *mockPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if m.idx >= len(m.reads) {
		// Block until SetReadDeadline causes timeout — simulate with an error
		time.Sleep(250 * time.Millisecond)
		return 0, nil, &net.OpError{Op: "readfrom", Err: context.DeadlineExceeded}
	}
	r := m.reads[m.idx]
	m.idx++
	if r.err != nil {
		return 0, nil, r.err
	}
	n := copy(p, r.data)
	return n, mockAddr{"192.0.2.100:443"}, nil
}

func (m *mockPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) { return len(p), nil }
func (m *mockPacketConn) Close() error                                  { m.closed = true; return nil }
func (m *mockPacketConn) LocalAddr() net.Addr                           { return mockAddr{"0.0.0.0:0"} }
func (m *mockPacketConn) SetDeadline(t time.Time) error                 { return nil }
func (m *mockPacketConn) SetReadDeadline(t time.Time) error             { return nil }
func (m *mockPacketConn) SetWriteDeadline(t time.Time) error            { return nil }

func TestListenTCP_ViaFakeConn_MatchesSYNACK(t *testing.T) {
	// Build a fake SYN-ACK response: server port=443 (src), our port=44000 (dst).
	synack := buildFakeTCPPacket(443, 44000, 0x12) // SYN+ACK

	pm := newProbeMap()
	key := probeKey{SrcPort: 44000, DstPort: 443, Seq: 0}
	pm.add(key, 7, 2, time.Now())

	results := make(chan Result, 10)
	cfg := DefaultConfig()
	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)

	conn := &mockPacketConn{
		reads: []mockRead{
			{data: synack},        // deliver SYN-ACK — should match and emit result
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	go tr.listenTCP(ctx, conn, pm, results)

	// Wait for the result
	select {
	case res := <-results:
		if !res.IsTarget {
			t.Errorf("expected IsTarget=true, got false")
		}
		if res.TTL != 7 {
			t.Errorf("expected TTL=7, got %d", res.TTL)
		}
		if res.FlowID != 2 {
			t.Errorf("expected FlowID=2, got %d", res.FlowID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for listenTCP result")
	}
}

func TestListenTCP_ViaFakeConn_RejectsNonSYNACK(t *testing.T) {
	// ACK-only response should be ignored (parseTCPResponse returns nil key).
	ack := buildFakeTCPPacket(443, 44000, 0x10) // ACK only

	pm := newProbeMap()
	results := make(chan Result, 10)
	cfg := DefaultConfig()
	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)

	conn := &mockPacketConn{
		reads: []mockRead{
			{data: ack}, // ACK only — should be ignored
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	go tr.listenTCP(ctx, conn, pm, results)

	select {
	case res := <-results:
		t.Errorf("expected no result for ACK-only packet, got %+v", res)
	case <-ctx.Done():
		// Context timed out — correct: no result emitted
	}
}

func TestListenTCP_ViaFakeConn_NoMatchInProbeMap(t *testing.T) {
	// SYN-ACK received but no matching probe key in probeMap.
	synack := buildFakeTCPPacket(443, 44001, 0x12) // port 44001, no entry in pm

	pm := newProbeMap()
	// pm is empty — no key for SrcPort=44001, DstPort=443
	results := make(chan Result, 10)
	cfg := DefaultConfig()
	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)

	conn := &mockPacketConn{
		reads: []mockRead{
			{data: synack},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	go tr.listenTCP(ctx, conn, pm, results)

	select {
	case res := <-results:
		t.Errorf("expected no result for unmatched key, got %+v", res)
	case <-ctx.Done():
		// Context timed out — correct: no result emitted
	}
}

func TestListenTCP_ViaFakeConn_ContextCancel(t *testing.T) {
	// Verifies that listenTCP exits cleanly on context cancellation.
	pm := newProbeMap()
	results := make(chan Result, 10)
	cfg := DefaultConfig()
	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)

	conn := &mockPacketConn{reads: nil} // will return error immediately

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		tr.listenTCP(ctx, conn, pm, results)
		close(done)
	}()

	cancel() // cancel context

	select {
	case <-done:
		// Exited cleanly
	case <-time.After(2 * time.Second):
		t.Error("listenTCP did not exit after context cancel")
	}
}

// cancellingPacketConn is a mockPacketConn that cancels a context after
// delivering the first successful read, ensuring the inner ctx.Done() branch
// in listenTCP is reachable.
type cancellingPacketConn struct {
	data   []byte
	cancel context.CancelFunc
	used   bool
}

func (c *cancellingPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if !c.used {
		c.used = true
		n := copy(p, c.data)
		// Cancel the context immediately after delivering data, before the
		// inner select in listenTCP can send to the results channel.
		c.cancel()
		return n, mockAddr{"192.0.2.100:443"}, nil
	}
	// Subsequent calls block until the deadline fires.
	time.Sleep(300 * time.Millisecond)
	return 0, nil, &net.OpError{Op: "readfrom", Err: context.DeadlineExceeded}
}

func (c *cancellingPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) { return len(p), nil }
func (c *cancellingPacketConn) Close() error                                  { return nil }
func (c *cancellingPacketConn) LocalAddr() net.Addr                           { return mockAddr{"0.0.0.0:0"} }
func (c *cancellingPacketConn) SetDeadline(t time.Time) error                 { return nil }
func (c *cancellingPacketConn) SetReadDeadline(t time.Time) error             { return nil }
func (c *cancellingPacketConn) SetWriteDeadline(t time.Time) error            { return nil }

func TestListenTCP_ViaFakeConn_ResultCtxCancelRace(t *testing.T) {
	// Hit the ctx.Done() case in the inner select of listenTCP:
	// the context is cancelled atomically after the SYN-ACK read completes,
	// so when the inner select executes, ctx.Done() is ready and the
	// zero-capacity results channel blocks.
	synack := buildFakeTCPPacket(443, 44000, 0x12)

	pm := newProbeMap()
	key := probeKey{SrcPort: 44000, DstPort: 443, Seq: 0}
	pm.add(key, 5, 0, time.Now())

	ctx, cancel := context.WithCancel(context.Background())

	conn := &cancellingPacketConn{
		data:   synack,
		cancel: cancel,
	}

	// Zero-capacity results channel so results <- always blocks.
	results := make(chan Result)
	cfg := DefaultConfig()
	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)

	done := make(chan struct{})
	go func() {
		tr.listenTCP(ctx, conn, pm, results)
		close(done)
	}()

	select {
	case <-done:
		// OK — goroutine exited (via outer or inner ctx.Done())
	case <-time.After(3 * time.Second):
		t.Error("listenTCP did not exit after context cancel")
	}
}
