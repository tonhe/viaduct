package probe

import (
	"encoding/binary"
	"testing"
)

func TestUDPProtocol_SupportsMultipath(t *testing.T) {
	p := NewUDPProtocol(33434)
	if !p.SupportsMultipath() {
		t.Fatal("UDP should support multipath")
	}
}

func TestUDPProtocol_Name(t *testing.T) {
	p := NewUDPProtocol(33434)
	if got := p.Name(); got != "udp" {
		t.Fatalf("Name() = %q, want %q", got, "udp")
	}
}

func TestUDPProtocol_BuildProbe(t *testing.T) {
	p := NewUDPProtocol(33434)
	cfg := DefaultConfig()
	flowID := 3
	ttl := 5

	pkt, key, err := p.BuildProbe(flowID, ttl, 0, cfg)
	if err != nil {
		t.Fatalf("BuildProbe error: %v", err)
	}

	wantSrcPort := cfg.BasePort + flowID
	wantDstPort := p.BaseDstPort + ttl

	// Key must use {SrcPort: basePort+flowID, DstPort: baseDstPort+ttl, Seq: 0}
	if key.SrcPort != wantSrcPort {
		t.Errorf("key.SrcPort = %d, want %d", key.SrcPort, wantSrcPort)
	}
	if key.DstPort != wantDstPort {
		t.Errorf("key.DstPort = %d, want %d", key.DstPort, wantDstPort)
	}
	if key.Seq != 0 {
		t.Errorf("key.Seq = %d, want 0", key.Seq)
	}

	// Packet must have correct UDP src/dst ports
	if len(pkt) < 8 {
		t.Fatalf("packet too short: %d bytes", len(pkt))
	}
	gotSrc := int(binary.BigEndian.Uint16(pkt[0:2]))
	gotDst := int(binary.BigEndian.Uint16(pkt[2:4]))
	if gotSrc != wantSrcPort {
		t.Errorf("pkt srcPort = %d, want %d", gotSrc, wantSrcPort)
	}
	if gotDst != wantDstPort {
		t.Errorf("pkt dstPort = %d, want %d", gotDst, wantDstPort)
	}
}

func TestUDPProtocol_IsDestReachedICMP(t *testing.T) {
	p := NewUDPProtocol(33434)

	// Port Unreachable (type 3, code 3) = destination reached
	if !p.IsDestReachedICMP(3, 3) {
		t.Error("Port Unreachable (3,3) should be dest reached")
	}

	// TTL Exceeded (type 11, code 0) = not destination
	if p.IsDestReachedICMP(11, 0) {
		t.Error("TTL Exceeded should not be dest reached")
	}
}

func TestUDPProtocol_IdentifyResponse(t *testing.T) {
	p := NewUDPProtocol(33434)
	cfg := DefaultConfig()
	flowID := 2
	ttl := 4

	pkt, wantKey, err := p.BuildProbe(flowID, ttl, 0, cfg)
	if err != nil {
		t.Fatalf("BuildProbe: %v", err)
	}

	// Build fake inner header: 20-byte IP header (protocol=17) + UDP packet
	innerHeader := make([]byte, 20+len(pkt))
	innerHeader[0] = 0x45 // IPv4, IHL=5 (20 bytes)
	innerHeader[9] = 17   // protocol = UDP
	copy(innerHeader[20:], pkt)

	got, err := p.IdentifyResponse(innerHeader)
	if err != nil {
		t.Fatalf("IdentifyResponse error: %v", err)
	}
	if got == nil {
		t.Fatal("IdentifyResponse returned nil")
	}
	if *got != wantKey {
		t.Errorf("key = %+v, want %+v", *got, wantKey)
	}
}

func TestUDPProtocol_IdentifyResponse_RejectsNonUDP(t *testing.T) {
	p := NewUDPProtocol(33434)

	// Build inner header with TCP protocol (6) instead of UDP (17)
	innerHeader := make([]byte, 28)
	innerHeader[0] = 0x45 // IPv4, IHL=5
	innerHeader[9] = 6    // protocol = TCP

	got, err := p.IdentifyResponse(innerHeader)
	if err != nil {
		t.Fatalf("IdentifyResponse error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for non-UDP, got %+v", *got)
	}
}

func TestUDPProtocol_IdentifyResponse_IPv6(t *testing.T) {
	p := NewUDPProtocol(33434)
	inner := []byte{
		0x60, 0, 0, 0, 0, 8, 17, 64,
		0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01,
		0x26, 0x06, 0x47, 0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0x68, 0x10, 0x80, 0xf0,
		0xab, 0xcd, 0x82, 0x9a,
		0, 8, 0, 0,
	}
	key, err := p.IdentifyResponse(inner)
	if err != nil || key == nil {
		t.Fatalf("IdentifyResponse failed: err=%v key=%v", err, key)
	}
	if key.SrcPort != 0xabcd || key.DstPort != 33434 {
		t.Errorf("key = %+v, want SrcPort=0xabcd DstPort=33434", key)
	}
}

func TestUDPProtocol_IsDestReachedICMP_IPv6(t *testing.T) {
	p := NewUDPProtocol(33434)
	if !p.IsDestReachedICMP(1, 4) {
		t.Error("IsDestReachedICMP(1, 4) for ICMPv6 Port Unreachable = false, want true")
	}
	if p.IsDestReachedICMP(3, 3) != true {
		t.Error("IsDestReachedICMP(3, 3) for ICMPv4 Port Unreachable = false, want true")
	}
	if p.IsDestReachedICMP(3, 0) {
		t.Error("IsDestReachedICMP(3, 0) for unrelated = true, want false")
	}
}
