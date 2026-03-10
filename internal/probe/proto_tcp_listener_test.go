package probe

import (
	"encoding/binary"
	"testing"
	"time"
)

// buildFakeTCPPacket creates a minimal 20-byte TCP segment with given ports and flags.
func buildFakeTCPPacket(srcPort, dstPort int, flags byte) []byte {
	pkt := make([]byte, 20)
	binary.BigEndian.PutUint16(pkt[0:2], uint16(srcPort))
	binary.BigEndian.PutUint16(pkt[2:4], uint16(dstPort))
	pkt[12] = 5 << 4 // data offset
	pkt[13] = flags
	return pkt
}

func TestParseTCPResponse_SYNACK(t *testing.T) {
	// SYN-ACK from server: src=443 (server), dst=44000 (our src port)
	pkt := buildFakeTCPPacket(443, 44000, 0x12) // SYN+ACK

	key, isTarget := parseTCPResponse(pkt)
	if key == nil {
		t.Fatal("expected non-nil key for SYN-ACK")
	}
	if !isTarget {
		t.Error("expected isTarget=true for SYN-ACK")
	}
	// Reply dst port (offset 2:4) = 44000 = our original src port
	if key.SrcPort != 44000 {
		t.Errorf("key.SrcPort = %d, want 44000", key.SrcPort)
	}
	// Reply src port (offset 0:2) = 443 = their port
	if key.DstPort != 443 {
		t.Errorf("key.DstPort = %d, want 443", key.DstPort)
	}
	if key.Seq != 0 {
		t.Errorf("key.Seq = %d, want 0", key.Seq)
	}
}

func TestParseTCPResponse_RST(t *testing.T) {
	// RST+ACK from server: src=443, dst=44000
	pkt := buildFakeTCPPacket(443, 44000, 0x14) // RST+ACK

	key, isTarget := parseTCPResponse(pkt)
	if key == nil {
		t.Fatal("expected non-nil key for RST")
	}
	if !isTarget {
		t.Error("expected isTarget=true for RST")
	}
	if key.SrcPort != 44000 {
		t.Errorf("key.SrcPort = %d, want 44000", key.SrcPort)
	}
	if key.DstPort != 443 {
		t.Errorf("key.DstPort = %d, want 443", key.DstPort)
	}
}

func TestParseTCPResponse_NotSYNACKOrRST(t *testing.T) {
	// ACK only (0x10) — not a SYN-ACK or RST
	pkt := buildFakeTCPPacket(443, 44000, 0x10)

	key, _ := parseTCPResponse(pkt)
	if key != nil {
		t.Errorf("expected nil key for ACK-only, got %+v", *key)
	}
}

func TestParseTCPResponse_TooShort(t *testing.T) {
	pkt := make([]byte, 10) // too short

	key, _ := parseTCPResponse(pkt)
	if key != nil {
		t.Errorf("expected nil key for short packet, got %+v", *key)
	}
}

func TestListenTCP_MatchesProbeMap(t *testing.T) {
	// Register a probe in the probeMap
	pm := newProbeMap()
	key := probeKey{SrcPort: 44000, DstPort: 443, Seq: 0}
	pm.add(key, 15, 0, time.Now())

	// Build a fake SYN-ACK packet: server src=443, dst=44000
	pkt := buildFakeTCPPacket(443, 44000, 0x12)

	// Parse the response
	gotKey, isTarget := parseTCPResponse(pkt)
	if gotKey == nil {
		t.Fatal("parseTCPResponse returned nil key")
	}
	if !isTarget {
		t.Error("expected isTarget=true")
	}

	// Match against probeMap
	rec, ok := pm.match(*gotKey)
	if !ok {
		t.Fatalf("probeMap did not match key %+v", *gotKey)
	}
	if rec.ttl != 15 {
		t.Errorf("rec.ttl = %d, want 15", rec.ttl)
	}
	if rec.flowID != 0 {
		t.Errorf("rec.flowID = %d, want 0", rec.flowID)
	}
}
