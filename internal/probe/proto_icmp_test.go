package probe

import (
	"encoding/binary"
	"testing"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

func TestICMPProtocol_SupportsMultipath(t *testing.T) {
	p := NewICMPProtocol()
	if p.SupportsMultipath() {
		t.Fatal("ICMP should not support multipath")
	}
}

func TestICMPProtocol_Name(t *testing.T) {
	p := NewICMPProtocol()
	if got := p.Name(); got != "icmp" {
		t.Fatalf("Name() = %q, want %q", got, "icmp")
	}
}

func TestICMPProtocol_BuildProbe(t *testing.T) {
	p := NewICMPProtocol()
	cfg := DefaultConfig()
	seq := 42

	pkt, key, err := p.BuildProbe(0, 5, seq, cfg)
	if err != nil {
		t.Fatalf("BuildProbe error: %v", err)
	}

	// Key must use {SrcPort: sessionID, DstPort: seq, Seq: 0}
	if key.SrcPort != p.sessionID {
		t.Errorf("key.SrcPort = %d, want sessionID %d", key.SrcPort, p.sessionID)
	}
	if key.DstPort != seq {
		t.Errorf("key.DstPort = %d, want seq %d", key.DstPort, seq)
	}
	if key.Seq != 0 {
		t.Errorf("key.Seq = %d, want 0", key.Seq)
	}

	// Packet must parse as valid ICMP Echo
	msg, err := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), pkt)
	if err != nil {
		t.Fatalf("ParseMessage error: %v", err)
	}
	if msg.Type != ipv4.ICMPTypeEcho {
		t.Errorf("type = %v, want Echo", msg.Type)
	}
	echo, ok := msg.Body.(*icmp.Echo)
	if !ok {
		t.Fatal("body is not *icmp.Echo")
	}
	if echo.ID != p.sessionID {
		t.Errorf("echo.ID = %d, want %d", echo.ID, p.sessionID)
	}
	if echo.Seq != seq {
		t.Errorf("echo.Seq = %d, want %d", echo.Seq, seq)
	}
}

func TestICMPProtocol_BuildProbe_ConsistentSessionID(t *testing.T) {
	p := NewICMPProtocol()
	cfg := DefaultConfig()

	_, key1, err := p.BuildProbe(0, 1, 1, cfg)
	if err != nil {
		t.Fatalf("BuildProbe 1: %v", err)
	}
	_, key2, err := p.BuildProbe(0, 2, 2, cfg)
	if err != nil {
		t.Fatalf("BuildProbe 2: %v", err)
	}

	if key1.SrcPort != key2.SrcPort {
		t.Errorf("session IDs differ: %d vs %d", key1.SrcPort, key2.SrcPort)
	}
}

func TestICMPProtocol_IdentifyResponse(t *testing.T) {
	p := NewICMPProtocol()
	cfg := DefaultConfig()
	seq := 7

	pkt, wantKey, err := p.BuildProbe(0, 3, seq, cfg)
	if err != nil {
		t.Fatalf("BuildProbe: %v", err)
	}

	// Build a fake inner header: 20-byte IP header (protocol=1) + ICMP Echo packet
	innerHeader := make([]byte, 20+len(pkt))
	innerHeader[0] = 0x45 // IPv4, IHL=5 (20 bytes)
	innerHeader[9] = 1    // protocol = ICMP
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

func TestICMPProtocol_IdentifyResponse_RejectsNonICMP(t *testing.T) {
	p := NewICMPProtocol()

	// Build inner header with UDP protocol (17) instead of ICMP (1)
	innerHeader := make([]byte, 28)
	innerHeader[0] = 0x45 // IPv4, IHL=5
	innerHeader[9] = 17   // protocol = UDP

	got, err := p.IdentifyResponse(innerHeader)
	if err != nil {
		t.Fatalf("IdentifyResponse error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for non-ICMP, got %+v", *got)
	}
}

func TestICMPProtocol_IsDestReachedICMP(t *testing.T) {
	p := NewICMPProtocol()

	// Echo Reply (type 0) = destination reached
	if !p.IsDestReachedICMP(int(ipv4.ICMPTypeEchoReply), 0) {
		t.Error("Echo Reply should be dest reached")
	}

	// TTL Exceeded (type 11) = not destination
	if p.IsDestReachedICMP(int(ipv4.ICMPTypeTimeExceeded), 0) {
		t.Error("Time Exceeded should not be dest reached")
	}
}

func TestICMPProtocol_IdentifyResponse_IPv6(t *testing.T) {
	p := NewICMPProtocol()
	p.sessionID = 0x1234

	inner := []byte{
		0x60, 0, 0, 0, 0, 8, 58, 64,
		0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01,
		0x26, 0x06, 0x47, 0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0x68, 0x10, 0x80, 0xf0,
		128, 0, 0, 0,
		0x12, 0x34,
		0x00, 0x05,
	}
	key, err := p.IdentifyResponse(inner)
	if err != nil || key == nil {
		t.Fatalf("IdentifyResponse failed: err=%v key=%v", err, key)
	}
	if key.SrcPort != 0x1234 || key.DstPort != 5 {
		t.Errorf("key = %+v, want SrcPort=0x1234 DstPort=5", key)
	}
}

// helper to build a fake ICMP Echo Request for IdentifyResponse tests
func buildFakeInnerICMP(id, seq int) []byte {
	// 20-byte IP header + 8-byte ICMP header
	buf := make([]byte, 28)
	buf[0] = 0x45 // IPv4, IHL=5
	buf[9] = 1    // ICMP
	// ICMP type = 8 (Echo)
	buf[20] = 8
	buf[21] = 0
	binary.BigEndian.PutUint16(buf[24:26], uint16(id))
	binary.BigEndian.PutUint16(buf[26:28], uint16(seq))
	return buf
}
