package probe

import (
	"net"
	"testing"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func TestListenerNet(t *testing.T) {
	tests := []struct {
		v    int
		want string
	}{
		{4, "ip4:icmp"},
		{6, "ip6:ipv6-icmp"},
	}
	for _, tc := range tests {
		if got := ListenerNet(tc.v); got != tc.want {
			t.Errorf("ListenerNet(%d) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

func TestRawListenAddr(t *testing.T) {
	if got := RawListenAddr(4); got != "0.0.0.0" {
		t.Errorf("RawListenAddr(4) = %q, want %q", got, "0.0.0.0")
	}
	if got := RawListenAddr(6); got != "::" {
		t.Errorf("RawListenAddr(6) = %q, want %q", got, "::")
	}
}

func TestDialNet(t *testing.T) {
	if got := DialNet(4, "udp"); got != "udp4" {
		t.Errorf("DialNet(4,udp) = %q", got)
	}
	if got := DialNet(6, "udp"); got != "udp6" {
		t.Errorf("DialNet(6,udp) = %q", got)
	}
	if got := DialNet(4, "tcp"); got != "tcp4" {
		t.Errorf("DialNet(4,tcp) = %q", got)
	}
	if got := DialNet(6, "tcp"); got != "tcp6" {
		t.Errorf("DialNet(6,tcp) = %q", got)
	}
}

func TestICMPProtoNum(t *testing.T) {
	if got := ICMPProtoNum(4); got != 1 {
		t.Errorf("ICMPProtoNum(4) = %d, want 1", got)
	}
	if got := ICMPProtoNum(6); got != 58 {
		t.Errorf("ICMPProtoNum(6) = %d, want 58", got)
	}
}

func TestICMPTypes(t *testing.T) {
	if EchoRequestType(4) != ipv4.ICMPTypeEcho {
		t.Error("EchoRequestType(4) wrong")
	}
	if EchoRequestType(6) != ipv6.ICMPTypeEchoRequest {
		t.Error("EchoRequestType(6) wrong")
	}
	if EchoReplyType(4) != ipv4.ICMPTypeEchoReply {
		t.Error("EchoReplyType(4) wrong")
	}
	if EchoReplyType(6) != ipv6.ICMPTypeEchoReply {
		t.Error("EchoReplyType(6) wrong")
	}
	if TimeExceededType(4) != ipv4.ICMPTypeTimeExceeded {
		t.Error("TimeExceededType(4) wrong")
	}
	if TimeExceededType(6) != ipv6.ICMPTypeTimeExceeded {
		t.Error("TimeExceededType(6) wrong")
	}
}

// WIRESHARK-REPLACE-ME: this packet is synthesized by hand. To close the
// self-oracle problem (Tier 4.3 in the test-hardening plan), replace with
// a real capture. See scripts/capture-references.sh and update the comment
// to cite the .pcap file + capture date.
func TestParseInnerHeader_IPv4_TCP(t *testing.T) {
	pkt := []byte{
		0x45, 0x00, 0x00, 0x2c, 0xab, 0xcd, 0x00, 0x00,
		0x40, 0x06, 0x00, 0x00,
		10, 0, 0, 1,
		93, 184, 216, 34,
		0xc3, 0x50,
		0x01, 0xbb,
		0, 0, 0, 1, 0, 0, 0, 0,
	}
	src, dst, proto, srcPort, dstPort, err := ParseInnerHeader(4, pkt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !src.Equal(net.ParseIP("10.0.0.1")) {
		t.Errorf("src = %v, want 10.0.0.1", src)
	}
	if !dst.Equal(net.ParseIP("93.184.216.34")) {
		t.Errorf("dst = %v, want 93.184.216.34", dst)
	}
	if proto != 6 {
		t.Errorf("proto = %d, want 6", proto)
	}
	if srcPort != 50000 {
		t.Errorf("srcPort = %d, want 50000", srcPort)
	}
	if dstPort != 443 {
		t.Errorf("dstPort = %d, want 443", dstPort)
	}
}

func TestParseInnerHeader_IPv6_UDP(t *testing.T) {
	pkt := []byte{
		0x60, 0, 0, 0,
		0, 8, 17, 64,
		0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01,
		0x26, 0x06, 0x47, 0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0x68, 0x10, 0x80, 0xf0,
		0xc3, 0x50,
		0x01, 0xbb,
		0, 8, 0, 0,
	}
	src, dst, proto, srcPort, dstPort, err := ParseInnerHeader(6, pkt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !src.Equal(net.ParseIP("2001:db8::1")) {
		t.Errorf("src = %v, want 2001:db8::1", src)
	}
	if !dst.Equal(net.ParseIP("2606:4700::6810:80f0")) {
		t.Errorf("dst = %v, want 2606:4700::6810:80f0", dst)
	}
	if proto != 17 {
		t.Errorf("proto = %d, want 17", proto)
	}
	if srcPort != 50000 || dstPort != 443 {
		t.Errorf("ports = (%d,%d), want (50000,443)", srcPort, dstPort)
	}
}

func TestParseInnerHeader_IPv6_WithFragmentHeader(t *testing.T) {
	// IPv6 base header (next=44 Fragment), Fragment header (next=17 UDP), then UDP.
	pkt := []byte{
		// IPv6 base header
		0x60, 0, 0, 0,
		0, 16, 44, 64, // payload len=16, next=44 (Fragment)
		0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01,
		0x26, 0x06, 0x47, 0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0x68, 0x10, 0x80, 0xf0,
		// Fragment header (fixed 8 bytes): next=17 (UDP), reserved=0, frag offset/M=0, identification=0
		17, 0, 0, 0, 0, 0, 0, 0,
		// UDP
		0xc3, 0x50, 0x01, 0xbb, 0, 8, 0, 0,
	}
	src, dst, proto, srcPort, dstPort, err := ParseInnerHeader(6, pkt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !src.Equal(net.ParseIP("2001:db8::1")) {
		t.Errorf("src = %v, want 2001:db8::1", src)
	}
	if !dst.Equal(net.ParseIP("2606:4700::6810:80f0")) {
		t.Errorf("dst = %v, want 2606:4700::6810:80f0", dst)
	}
	if proto != 17 {
		t.Errorf("proto = %d, want 17 (UDP)", proto)
	}
	if srcPort != 50000 || dstPort != 443 {
		t.Errorf("ports = (%d,%d), want (50000,443)", srcPort, dstPort)
	}
}

func TestParseInnerHeader_TooShort(t *testing.T) {
	_, _, _, _, _, err := ParseInnerHeader(4, []byte{0x45})
	if err == nil {
		t.Error("expected error for short v4 packet")
	}
	_, _, _, _, _, err = ParseInnerHeader(6, []byte{0x60, 0, 0, 0})
	if err == nil {
		t.Error("expected error for short v6 packet")
	}
}

// WIRESHARK-REPLACE-ME: this test uses a round-trip property (compute sum,
// insert into header, recompute, expect 0) which is self-referential. To
// close the self-oracle problem (Tier 4.3), replace the expected checksum
// with a value captured from real via output and verified by Wireshark.
// See scripts/capture-references.sh.
func TestTCPChecksum_IPv4(t *testing.T) {
	src := net.IPv4(10, 0, 0, 1)
	dst := net.IPv4(93, 184, 216, 34)
	hdr := []byte{
		0xc3, 0x50, 0x01, 0xbb,
		0, 0, 0, 1, 0, 0, 0, 0,
		0x50, 0x02, 0xff, 0xff,
		0, 0, 0, 0,
	}
	sum := TCPChecksum(4, src, dst, hdr)
	hdr[16] = byte(sum >> 8)
	hdr[17] = byte(sum)
	verify := TCPChecksum(4, src, dst, hdr)
	if verify != 0 {
		t.Errorf("v4 checksum verification: got %#x, want 0", verify)
	}
}

func TestTCPChecksum_IPv6(t *testing.T) {
	src := net.ParseIP("2001:db8::1")
	dst := net.ParseIP("2606:4700::6810:80f0")
	hdr := []byte{
		0xc3, 0x50, 0x01, 0xbb,
		0, 0, 0, 1, 0, 0, 0, 0,
		0x50, 0x02, 0xff, 0xff,
		0, 0, 0, 0,
	}
	sum := TCPChecksum(6, src, dst, hdr)
	hdr[16] = byte(sum >> 8)
	hdr[17] = byte(sum)
	verify := TCPChecksum(6, src, dst, hdr)
	if verify != 0 {
		t.Errorf("v6 checksum verification: got %#x, want 0", verify)
	}
}

func TestIsPrivate_IPv4(t *testing.T) {
	priv := []string{"10.0.0.1", "172.20.1.1", "192.168.0.1", "127.0.0.1", "169.254.1.2"}
	for _, s := range priv {
		if !IsPrivate(net.ParseIP(s)) {
			t.Errorf("IsPrivate(%s) = false, want true", s)
		}
	}
	pub := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34"}
	for _, s := range pub {
		if IsPrivate(net.ParseIP(s)) {
			t.Errorf("IsPrivate(%s) = true, want false", s)
		}
	}
}

func TestIsPrivate_IPv6(t *testing.T) {
	priv := []string{"::1", "fe80::1", "fc00::1", "fd12::abcd"}
	for _, s := range priv {
		if !IsPrivate(net.ParseIP(s)) {
			t.Errorf("IsPrivate(%s) = false, want true", s)
		}
	}
	pub := []string{"2001:db8::1", "2606:4700::6810:80f0", "2a00:1450::1"}
	for _, s := range pub {
		if IsPrivate(net.ParseIP(s)) {
			t.Errorf("IsPrivate(%s) = true, want false", s)
		}
	}
}

func TestTransportOffset(t *testing.T) {
	v4 := []byte{0x45, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if got := TransportOffset(4, v4); got != 20 {
		t.Errorf("v4 IHL=5 offset = %d, want 20", got)
	}
	v4opt := append([]byte{0x46}, make([]byte, 27)...)
	if got := TransportOffset(4, v4opt); got != 24 {
		t.Errorf("v4 IHL=6 offset = %d, want 24", got)
	}
	v6 := append([]byte{0x60, 0, 0, 0, 0, 8, 17, 64}, make([]byte, 36)...)
	if got := TransportOffset(6, v6); got != 40 {
		t.Errorf("v6 no-ext offset = %d, want 40", got)
	}
	if got := TransportOffset(4, []byte{0x45}); got != -1 {
		t.Errorf("truncated v4 offset = %d, want -1", got)
	}
}

func TestASNReverseName_IPv4(t *testing.T) {
	got := ASNReverseName(4, net.ParseIP("1.2.3.4"))
	want := "4.3.2.1.origin.asn.cymru.com"
	if got != want {
		t.Errorf("ASNReverseName(4, 1.2.3.4) = %q, want %q", got, want)
	}
}

func TestASNReverseName_IPv6(t *testing.T) {
	got := ASNReverseName(6, net.ParseIP("2001:db8::1"))
	want := "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.origin6.asn.cymru.com"
	if got != want {
		t.Errorf("ASNReverseName(6, 2001:db8::1) = %q, want %q", got, want)
	}
}

func TestDestinationUnreachableType(t *testing.T) {
	if DestinationUnreachableType(4) != ipv4.ICMPTypeDestinationUnreachable {
		t.Error("DestinationUnreachableType(4) wrong")
	}
	if DestinationUnreachableType(6) != ipv6.ICMPTypeDestinationUnreachable {
		t.Error("DestinationUnreachableType(6) wrong")
	}
}

func TestTransportListenerNet(t *testing.T) {
	cases := []struct {
		v     int
		proto string
		want  string
	}{
		{4, "udp", "ip4:udp"},
		{6, "udp", "ip6:udp"},
		{4, "tcp", "ip4:tcp"},
		{6, "tcp", "ip6:tcp"},
	}
	for _, c := range cases {
		if got := TransportListenerNet(c.v, c.proto); got != c.want {
			t.Errorf("TransportListenerNet(%d, %q) = %q, want %q", c.v, c.proto, got, c.want)
		}
	}
}
