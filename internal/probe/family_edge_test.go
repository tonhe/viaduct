package probe

// family_edge_test.go — additional edge-case and reference-vector tests for
// family.go functions: TCPChecksum known-good vectors, ParseInnerHeader edge
// cases (IHL variations, truncation, stacked extension headers, ESP stop).

import (
	"net"
	"testing"
)

// ---------------------------------------------------------------------------
// TCPChecksum reference vectors
// ---------------------------------------------------------------------------

// tcpChecksumV4_SYN_192_0_2 is the expected checksum for a canonical TCP SYN
// from 192.0.2.1:54321 → 192.0.2.100:80, seq=0xdeadbeef, window=65535, no
// options, checksum field zeroed.
//
// Computed by hand via RFC 1071 pseudo-header construction:
//   pseudo: src(4) + dst(4) + [0x00,0x06] + tcpLen(2) = 12 bytes
//   TCP:    sport(2) + dport(2) + seq(4) + ack(4) + off/flags(2) + win(2) + cksum(2) + urg(2) = 20 bytes
//   Python verification: checksum_rfc1071(psh+tcp) == 0xb95d
//   Plugging 0xb95d back into the cksum field and re-running == 0x0000. ✓
const tcpChecksumV4_SYN_192_0_2 uint16 = 0xb95d

// tcpChecksumV6_SYN_2001_db8 is the expected checksum for a canonical TCP SYN
// from 2001:db8::1:54321 → 2001:db8::100:80, seq=0xdeadbeef, window=65535,
// no options, checksum field zeroed.
//
// Computed identically but with the 40-byte IPv6 pseudo-header.
// Python verification: 0xe150; plug-back verify == 0x0000. ✓
const tcpChecksumV6_SYN_2001_db8 uint16 = 0xe150

// canonicalTCPSYN returns a 20-byte TCP SYN header with:
//   src port = 54321 (0xd431), dst port = 80 (0x0050)
//   seq = 0xdeadbeef, ack = 0
//   data offset = 5, SYN flag (0x02), window = 65535
//   checksum = 0 (caller fills it)
func canonicalTCPSYN() []byte {
	return []byte{
		0xd4, 0x31, // src port 54321
		0x00, 0x50, // dst port 80
		0xde, 0xad, 0xbe, 0xef, // seq
		0x00, 0x00, 0x00, 0x00, // ack
		0x50, 0x02, // data offset=5, SYN
		0xff, 0xff, // window
		0x00, 0x00, // checksum (zeroed)
		0x00, 0x00, // urgent
	}
}

func TestTCPChecksum_IPv4_ReferenceVector(t *testing.T) {
	// Canonical TCP SYN: 192.0.2.1:54321 → 192.0.2.100:80
	// Expected checksum verified by RFC 1071 calculation and Python cross-check.
	src := net.ParseIP("192.0.2.1")
	dst := net.ParseIP("192.0.2.100")
	hdr := canonicalTCPSYN() // checksum field is zero

	got := TCPChecksum(4, src, dst, hdr)
	if got != tcpChecksumV4_SYN_192_0_2 {
		t.Errorf("TCPChecksum IPv4 reference: got 0x%04x, want 0x%04x", got, tcpChecksumV4_SYN_192_0_2)
	}

	// Plug the checksum in and verify: a second pass should return 0.
	hdr[16] = byte(got >> 8)
	hdr[17] = byte(got)
	verify := TCPChecksum(4, src, dst, hdr)
	if verify != 0 {
		t.Errorf("TCPChecksum IPv4 plug-back verify: got 0x%04x, want 0x0000", verify)
	}
}

func TestTCPChecksum_IPv6_ReferenceVector(t *testing.T) {
	// Canonical TCP SYN: 2001:db8::1:54321 → 2001:db8::100:80
	// Expected checksum verified by RFC 1071 calculation and Python cross-check.
	src := net.ParseIP("2001:db8::1")
	dst := net.ParseIP("2001:db8::100")
	hdr := canonicalTCPSYN()

	got := TCPChecksum(6, src, dst, hdr)
	if got != tcpChecksumV6_SYN_2001_db8 {
		t.Errorf("TCPChecksum IPv6 reference: got 0x%04x, want 0x%04x", got, tcpChecksumV6_SYN_2001_db8)
	}

	hdr[16] = byte(got >> 8)
	hdr[17] = byte(got)
	verify := TCPChecksum(6, src, dst, hdr)
	if verify != 0 {
		t.Errorf("TCPChecksum IPv6 plug-back verify: got 0x%04x, want 0x0000", verify)
	}
}

// ---------------------------------------------------------------------------
// ParseInnerHeader — IPv4 edge cases
// ---------------------------------------------------------------------------

// buildV4InnerWithIHL constructs a minimal IPv4 packet where the IP header has
// the given IHL (in 4-byte units), followed by 4 bytes of transport payload.
// srcIP and dstIP are always 192.0.2.1 and 192.0.2.100 (doc space).
func buildV4InnerWithIHL(ihl int, proto byte, sport, dport uint16) []byte {
	headerLen := ihl * 4
	if headerLen < 20 {
		headerLen = 20
	}
	pkt := make([]byte, headerLen+4)
	pkt[0] = 0x40 | byte(ihl) // version=4, IHL
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
	// transport ports at offset headerLen
	pkt[headerLen] = byte(sport >> 8)
	pkt[headerLen+1] = byte(sport)
	pkt[headerLen+2] = byte(dport >> 8)
	pkt[headerLen+3] = byte(dport)
	return pkt
}

func TestParseInnerHeader_IPv4_WithOptions_IHL6(t *testing.T) {
	// IHL=6 → 24-byte IP header; transport starts at byte 24.
	pkt := buildV4InnerWithIHL(6, 6 /*TCP*/, 54321, 80)

	src, dst, proto, srcPort, dstPort, err := ParseInnerHeader(4, pkt)
	if err != nil {
		t.Fatalf("IHL=6: unexpected error: %v", err)
	}
	if !src.Equal(net.ParseIP("192.0.2.1")) {
		t.Errorf("IHL=6: src = %v, want 192.0.2.1", src)
	}
	if !dst.Equal(net.ParseIP("192.0.2.100")) {
		t.Errorf("IHL=6: dst = %v, want 192.0.2.100", dst)
	}
	if proto != 6 {
		t.Errorf("IHL=6: proto = %d, want 6 (TCP)", proto)
	}
	if srcPort != 54321 {
		t.Errorf("IHL=6: srcPort = %d, want 54321", srcPort)
	}
	if dstPort != 80 {
		t.Errorf("IHL=6: dstPort = %d, want 80", dstPort)
	}
}

func TestParseInnerHeader_IPv4_MaxIHL15(t *testing.T) {
	// IHL=15 → 60-byte header; transport at byte 60.
	pkt := buildV4InnerWithIHL(15, 17 /*UDP*/, 1234, 5678)

	src, dst, proto, srcPort, dstPort, err := ParseInnerHeader(4, pkt)
	if err != nil {
		t.Fatalf("IHL=15: unexpected error: %v", err)
	}
	if !src.Equal(net.ParseIP("192.0.2.1")) {
		t.Errorf("IHL=15: src = %v, want 192.0.2.1", src)
	}
	if !dst.Equal(net.ParseIP("192.0.2.100")) {
		t.Errorf("IHL=15: dst = %v, want 192.0.2.100", dst)
	}
	if proto != 17 {
		t.Errorf("IHL=15: proto = %d, want 17 (UDP)", proto)
	}
	if srcPort != 1234 || dstPort != 5678 {
		t.Errorf("IHL=15: ports = (%d,%d), want (1234,5678)", srcPort, dstPort)
	}
}

func TestParseInnerHeader_IPv4_InvalidIHL_LessThan5(t *testing.T) {
	// IHL=4 → 16-byte header, which is illegal (minimum is 5 → 20 bytes).
	// parseInnerV4 checks: ihl < 20.
	pkt := make([]byte, 28)
	pkt[0] = 0x44 // version=4, IHL=4 (16 bytes — invalid)
	pkt[9] = 6    // TCP

	_, _, _, _, _, err := ParseInnerHeader(4, pkt)
	if err == nil {
		t.Error("IHL=4: expected error for invalid IHL, got nil")
	}
}

func TestParseInnerHeader_IPv4_TruncatedTransport(t *testing.T) {
	// Valid 20-byte IP header but only 2 bytes of transport follow
	// (need 4 for ports). parseInnerV4 checks: len(p) < ihl+4.
	pkt := make([]byte, 22) // 20-byte header + 2-byte transport fragment
	pkt[0] = 0x45           // IHL=5
	pkt[9] = 17             // UDP
	// fill in src/dst IPs
	pkt[12] = 192
	pkt[13] = 0
	pkt[14] = 2
	pkt[15] = 1
	pkt[16] = 192
	pkt[17] = 0
	pkt[18] = 2
	pkt[19] = 100

	_, _, _, _, _, err := ParseInnerHeader(4, pkt)
	if err == nil {
		t.Error("truncated transport: expected error, got nil")
	}
}

func TestParseInnerHeader_VersionWrong_V4VersionByte(t *testing.T) {
	// First nibble is 5 (not 4), 20+ bytes present — should error on version check.
	pkt := make([]byte, 24)
	pkt[0] = 0x55 // version=5 (invalid), IHL=5
	pkt[9] = 6

	_, _, _, _, _, err := ParseInnerHeader(4, pkt)
	if err == nil {
		t.Error("version=5: expected error for wrong version byte, got nil")
	}
}

// ---------------------------------------------------------------------------
// ParseInnerHeader — IPv6 edge cases
// ---------------------------------------------------------------------------

// buildV6Inner builds a minimal IPv6 packet. nextHeader is the NextHeader field
// of the IPv6 base header. transport is appended after the base 40-byte header.
func buildV6Inner(nextHeader byte, transport []byte) []byte {
	hdr := make([]byte, 40)
	hdr[0] = 0x60 // version=6, TC=0
	// payload length
	hdr[4] = byte(len(transport) >> 8)
	hdr[5] = byte(len(transport))
	hdr[6] = nextHeader
	hdr[7] = 64 // hop limit
	// src: 2001:db8::1
	hdr[8] = 0x20
	hdr[9] = 0x01
	hdr[10] = 0x0d
	hdr[11] = 0xb8
	hdr[23] = 0x01
	// dst: 2001:db8::100
	hdr[24] = 0x20
	hdr[25] = 0x01
	hdr[26] = 0x0d
	hdr[27] = 0xb8
	hdr[38] = 0x01
	hdr[39] = 0x00
	return append(hdr, transport...)
}

func TestParseInnerHeader_IPv6_StackedExtHeaders_HopRoutingUDP(t *testing.T) {
	// Hop-by-Hop (0) → Routing (43) → UDP (17)
	// Each extension header: [next][len][6 bytes of padding] = 8 bytes (len=0 → 8 bytes)
	hopHdr := []byte{
		43,         // next = Routing
		0,          // hdr ext len = 0 → total 8 bytes
		0, 0, 0, 0, 0, 0, // padding
	}
	routingHdr := []byte{
		17,         // next = UDP
		0,          // hdr ext len = 0 → total 8 bytes
		0, 0, 0, 0, 0, 0, // padding
	}
	udpPayload := []byte{
		0xd4, 0x31, // src port 54321
		0x00, 0x50, // dst port 80
		0x00, 0x0c, // length
		0x00, 0x00, // checksum
	}

	transport := append(hopHdr, routingHdr...)
	transport = append(transport, udpPayload...)
	pkt := buildV6Inner(0 /*Hop-by-Hop*/, transport)

	src, dst, proto, srcPort, dstPort, err := ParseInnerHeader(6, pkt)
	if err != nil {
		t.Fatalf("stacked ext headers: unexpected error: %v", err)
	}
	if !src.Equal(net.ParseIP("2001:db8::1")) {
		t.Errorf("stacked: src = %v, want 2001:db8::1", src)
	}
	if !dst.Equal(net.ParseIP("2001:db8::100")) {
		t.Errorf("stacked: dst = %v, want 2001:db8::100", dst)
	}
	if proto != 17 {
		t.Errorf("stacked: proto = %d, want 17 (UDP)", proto)
	}
	if srcPort != 54321 || dstPort != 80 {
		t.Errorf("stacked: ports = (%d,%d), want (54321,80)", srcPort, dstPort)
	}
}

func TestParseInnerHeader_IPv6_TruncatedExtHeader(t *testing.T) {
	// Stacked: Hop-by-Hop (0) → Routing (43, truncated).
	// The Hop-by-Hop header is complete (8 bytes), but the Routing header
	// is truncated to just 1 byte — enough to enter the loop but not enough
	// to read off+2 when the second extension header starts.
	//
	// Layout in transport payload appended to the 40-byte base header:
	//   [0] = 43 (next=Routing), [1] = 0 (len=0 → 8 bytes), [2..7] padding
	//   After jumping: off = 40+8 = 48. Now only 1 byte follows (p[48]).
	//   Loop condition: ipv6ExtHeaders[43]=true. off+2=50 > len=49 → error.
	hopHdr := []byte{
		43,             // next = Routing (an ext header)
		0,              // hdr ext len = 0 → 8 bytes total
		0, 0, 0, 0, 0, 0, // 6 bytes padding
	}
	// Routing header stub: only 1 byte present instead of the required 2 for "off+2"
	routingTrunc := []byte{0x11} // just the "next" byte; no length byte

	transport := append(hopHdr, routingTrunc...)
	pkt := buildV6Inner(0 /*Hop-by-Hop*/, transport)

	_, _, _, _, _, err := ParseInnerHeader(6, pkt)
	if err == nil {
		t.Error("truncated second ext header: expected error, got nil")
	}
}

func TestParseInnerHeader_IPv6_ESP_StopsChain(t *testing.T) {
	// ESP (protocol 50) is intentionally NOT in ipv6ExtHeaders, so the loop
	// stops immediately. The function then tries to read ports at offset 40,
	// but the ESP payload is opaque — we test the function's actual behavior:
	// either it returns an error (if len < off+4) or it returns proto=50 with
	// whatever ports happen to be in the first 4 bytes.
	//
	// Per the source code: if ESP is the first NextHeader, the loop doesn't
	// execute, off stays at 40, and if there are ≥4 bytes after the base
	// header, it reads "ports" from the ESP header bytes. This is correct
	// behavior per the comment in family.go: ESP is omitted because its
	// payload is opaque. We verify: proto == 50, no panic, no error.
	espPayload := []byte{
		// 4 bytes: SPI (Security Parameters Index) — treated as port pair
		0xab, 0xcd, 0xef, 0x01,
	}
	pkt := buildV6Inner(50 /*ESP*/, espPayload)

	_, _, proto, _, _, err := ParseInnerHeader(6, pkt)
	if err != nil {
		t.Fatalf("ESP next-header: unexpected error: %v", err)
	}
	if proto != 50 {
		t.Errorf("ESP next-header: proto = %d, want 50", proto)
	}
}

func TestParseInnerHeader_VersionWrong_V6VersionByte(t *testing.T) {
	// First nibble is 5 instead of 6; 40+ bytes present — should error.
	pkt := make([]byte, 44)
	pkt[0] = 0x50 // version=5 (wrong for IPv6)

	_, _, _, _, _, err := ParseInnerHeader(6, pkt)
	if err == nil {
		t.Error("version=5 for IPv6: expected error, got nil")
	}
}

func TestParseInnerHeader_IPv6_NoTransportPorts(t *testing.T) {
	// Valid IPv6 base header + no extension headers, but only 3 bytes of payload
	// follow (need 4 for src/dst port pair). Hits the "v6 inner: no transport ports" error.
	// Protocol is UDP (17) — not an ext header, so loop doesn't execute.
	payload := []byte{0xd4, 0x31, 0x00} // 3 bytes: not enough for 4-byte port pair
	pkt := buildV6Inner(17 /*UDP*/, payload)

	_, _, _, _, _, err := ParseInnerHeader(6, pkt)
	if err == nil {
		t.Error("3-byte payload: expected 'no transport ports' error, got nil")
	}
}

// ---------------------------------------------------------------------------
// ICMPTypeNum coverage
// ---------------------------------------------------------------------------

func TestICMPTypeNum_AllBranches(t *testing.T) {
	// Covers the previously-uncovered ICMPTypeNum function (0% coverage).
	import_ipv4 := EchoReplyType(4)   // ipv4.ICMPType
	import_ipv6 := EchoReplyType(6)   // ipv6.ICMPType

	got4 := ICMPTypeNum(import_ipv4)
	if got4 != 0 { // ipv4.ICMPTypeEchoReply = 0
		t.Errorf("ICMPTypeNum(ipv4.EchoReply) = %d, want 0", got4)
	}

	got6 := ICMPTypeNum(import_ipv6)
	if got6 != 129 { // ipv6.ICMPTypeEchoReply = 129
		t.Errorf("ICMPTypeNum(ipv6.EchoReply) = %d, want 129", got6)
	}
}

// ---------------------------------------------------------------------------
// IsPrivate — malformed IP (wrong byte length, not 4 or 16 bytes)
// ---------------------------------------------------------------------------

func TestIsPrivate_MalformedIP_NilTo16(t *testing.T) {
	// A net.IP with 6 bytes is neither a 4-byte nor a 16-byte IP address.
	// ip.To4() returns nil (not 4 bytes). ip.To16() also returns nil (not 4 or 16 bytes).
	// IsPrivate should return false without panic.
	malformed := net.IP([]byte{0xfe, 0x80, 0x00, 0x00, 0x00, 0x01}) // 6 bytes — invalid
	got := IsPrivate(malformed)
	if got {
		t.Error("IsPrivate(malformed 6-byte IP) should return false, not panic")
	}
}

// ---------------------------------------------------------------------------
// TransportOffset — v6 extension header paths (currently at 52.6%)
// ---------------------------------------------------------------------------

func TestTransportOffset_V6_TooShort(t *testing.T) {
	// Packet shorter than the minimum 40-byte IPv6 base header.
	pkt := make([]byte, 20)
	pkt[0] = 0x60
	got := TransportOffset(6, pkt)
	if got != -1 {
		t.Errorf("too-short v6 packet: TransportOffset = %d, want -1", got)
	}
}

func TestTransportOffset_UnknownVersion(t *testing.T) {
	// Version is not 4 or 6 — TransportOffset returns -1.
	pkt := make([]byte, 44)
	pkt[0] = 0x50 // version=5
	got := TransportOffset(5, pkt)
	if got != -1 {
		t.Errorf("unknown version: TransportOffset = %d, want -1", got)
	}
}

func TestTransportOffset_V6_TruncatedExtHeader(t *testing.T) {
	// IPv6 base header with Hop-by-Hop next, but only 1 byte of ext header data.
	// Should return -1.
	pkt := make([]byte, 41) // base 40 + 1 byte
	pkt[0] = 0x60
	pkt[6] = 0   // next = Hop-by-Hop
	pkt[40] = 17 // "next" byte of (truncated) Hop-by-Hop header
	// Missing the length byte at [41] — truncated

	got := TransportOffset(6, pkt)
	if got != -1 {
		t.Errorf("truncated ext header: TransportOffset = %d, want -1", got)
	}
}

func TestTransportOffset_V6_StackedExtHeaders(t *testing.T) {
	// Hop-by-Hop (0) → Routing (43) → UDP (17)
	// base (40) + hop (8) + routing (8) = transport at 56
	pkt := make([]byte, 60)
	pkt[0] = 0x60
	pkt[6] = 0   // next = Hop-by-Hop
	// Hop-by-Hop ext header at offset 40
	pkt[40] = 43 // next = Routing
	pkt[41] = 0  // len = 0 → 8 bytes
	// Routing ext header at offset 48
	pkt[48] = 17 // next = UDP
	pkt[49] = 0  // len = 0 → 8 bytes
	// UDP starts at offset 56

	got := TransportOffset(6, pkt)
	if got != 56 {
		t.Errorf("stacked ext headers: TransportOffset = %d, want 56", got)
	}
}

func TestTransportOffset_V6_FragmentHeader(t *testing.T) {
	// Fragment header (44) is fixed 8 bytes; next header field at offset 40.
	// base (40) + frag (8) = transport at 48
	pkt := make([]byte, 52)
	pkt[0] = 0x60
	pkt[6] = 44 // next = Fragment
	pkt[40] = 17 // next within Fragment header = UDP
	// Fragment header uses fixed 8-byte length regardless of p[41].

	got := TransportOffset(6, pkt)
	if got != 48 {
		t.Errorf("fragment header: TransportOffset = %d, want 48", got)
	}
}
