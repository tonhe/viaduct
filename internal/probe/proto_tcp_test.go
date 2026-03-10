package probe

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestTCPProtocol_SupportsMultipath(t *testing.T) {
	p := NewTCPProtocol(443)
	if !p.SupportsMultipath() {
		t.Fatal("TCP should support multipath")
	}
}

func TestTCPProtocol_Name(t *testing.T) {
	p := NewTCPProtocol(443)
	if got := p.Name(); got != "tcp" {
		t.Fatalf("Name() = %q, want %q", got, "tcp")
	}
}

func TestTCPProtocol_BuildProbe(t *testing.T) {
	p := NewTCPProtocol(443)
	cfg := DefaultConfig()
	cfg.SourceIP = net.IPv4(10, 0, 0, 1)
	cfg.TargetIP = net.IPv4(93, 184, 216, 34)
	flowID := 3
	ttl := 5
	seq := 42

	pkt, key, err := p.BuildProbe(flowID, ttl, seq, cfg)
	if err != nil {
		t.Fatalf("BuildProbe error: %v", err)
	}

	wantSrcPort := cfg.BasePort + flowID
	wantDstPort := 443

	// Key must use {SrcPort: basePort+flowID, DstPort: 443, Seq: 0}
	if key.SrcPort != wantSrcPort {
		t.Errorf("key.SrcPort = %d, want %d", key.SrcPort, wantSrcPort)
	}
	if key.DstPort != wantDstPort {
		t.Errorf("key.DstPort = %d, want %d", key.DstPort, wantDstPort)
	}
	if key.Seq != 0 {
		t.Errorf("key.Seq = %d, want 0", key.Seq)
	}

	// Packet must be 20 bytes (TCP header, no options)
	if len(pkt) != 20 {
		t.Fatalf("packet length = %d, want 20", len(pkt))
	}

	// Verify src/dst ports in packet
	gotSrc := int(binary.BigEndian.Uint16(pkt[0:2]))
	gotDst := int(binary.BigEndian.Uint16(pkt[2:4]))
	if gotSrc != wantSrcPort {
		t.Errorf("pkt srcPort = %d, want %d", gotSrc, wantSrcPort)
	}
	if gotDst != wantDstPort {
		t.Errorf("pkt dstPort = %d, want %d", gotDst, wantDstPort)
	}

	// Verify SYN flag is set (byte 13 = 0x02)
	if pkt[13] != 0x02 {
		t.Errorf("TCP flags = 0x%02x, want 0x02 (SYN)", pkt[13])
	}

	// Verify data offset = 5 (20 bytes)
	dataOffset := pkt[12] >> 4
	if dataOffset != 5 {
		t.Errorf("data offset = %d, want 5", dataOffset)
	}

	// Verify sequence number matches seq parameter
	gotSeq := binary.BigEndian.Uint32(pkt[4:8])
	if gotSeq != uint32(seq) {
		t.Errorf("sequence number = %d, want %d", gotSeq, seq)
	}
}

func TestTCPProtocol_IsDestReachedICMP(t *testing.T) {
	p := NewTCPProtocol(443)

	// TCP never uses ICMP for dest detection
	if p.IsDestReachedICMP(3, 3) {
		t.Error("TCP should never return true for IsDestReachedICMP")
	}
	if p.IsDestReachedICMP(11, 0) {
		t.Error("TCP should never return true for IsDestReachedICMP")
	}
	if p.IsDestReachedICMP(0, 0) {
		t.Error("TCP should never return true for IsDestReachedICMP")
	}
}

func TestTCPChecksum_RFC1071(t *testing.T) {
	// Known test vector: [0x00,0x01,0xf2,0x03,0xf4,0xf5,0xf6,0xf7] => 0x220d
	data := []byte{0x00, 0x01, 0xf2, 0x03, 0xf4, 0xf5, 0xf6, 0xf7}
	got := checksumRFC1071(data)
	if got != 0x220d {
		t.Errorf("checksumRFC1071 = 0x%04x, want 0x220d", got)
	}
}

func TestTCPChecksum_ValidatesSelf(t *testing.T) {
	p := NewTCPProtocol(443)
	cfg := DefaultConfig()
	cfg.SourceIP = net.IPv4(10, 0, 0, 1)
	cfg.TargetIP = net.IPv4(93, 184, 216, 34)

	pkt, _, err := p.BuildProbe(0, 5, 1, cfg)
	if err != nil {
		t.Fatalf("BuildProbe error: %v", err)
	}

	// Checksum field must be non-zero
	cksum := binary.BigEndian.Uint16(pkt[16:18])
	if cksum == 0 {
		t.Fatal("checksum should be non-zero")
	}

	// Recompute checksum with the checksum field included — result should be 0
	src := cfg.SourceIP.To4()
	dst := cfg.TargetIP.To4()
	psh := make([]byte, 12)
	copy(psh[0:4], src)
	copy(psh[4:8], dst)
	psh[9] = 6 // TCP
	binary.BigEndian.PutUint16(psh[10:12], uint16(len(pkt)))
	fullData := append(psh, pkt...)
	verify := checksumRFC1071(fullData)
	if verify != 0 {
		t.Errorf("checksum validation = 0x%04x, want 0x0000", verify)
	}
}

func TestTCPProtocol_IdentifyResponse(t *testing.T) {
	p := NewTCPProtocol(443)
	cfg := DefaultConfig()
	cfg.SourceIP = net.IPv4(10, 0, 0, 1)
	cfg.TargetIP = net.IPv4(93, 184, 216, 34)
	flowID := 2

	pkt, wantKey, err := p.BuildProbe(flowID, 4, 1, cfg)
	if err != nil {
		t.Fatalf("BuildProbe: %v", err)
	}

	// Build fake inner header: 20-byte IP header (protocol=6) + TCP packet
	innerHeader := make([]byte, 20+len(pkt))
	innerHeader[0] = 0x45 // IPv4, IHL=5 (20 bytes)
	innerHeader[9] = 6    // protocol = TCP
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

func TestTCPProtocol_IdentifyResponse_RejectsNonTCP(t *testing.T) {
	p := NewTCPProtocol(443)

	// Build inner header with UDP protocol (17) instead of TCP (6)
	innerHeader := make([]byte, 28)
	innerHeader[0] = 0x45 // IPv4, IHL=5
	innerHeader[9] = 17   // protocol = UDP

	got, err := p.IdentifyResponse(innerHeader)
	if err != nil {
		t.Fatalf("IdentifyResponse error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for non-TCP, got %+v", *got)
	}
}
