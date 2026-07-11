// Package probe — family.go provides helpers for IPv4 vs IPv6
// behaviors that differ only in small constants or simple branches.
//
// All helpers accept v == 4 or v == 6. Behavior is undefined for
// other values; callers must validate before invoking.
package probe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// ListenerNet returns the `icmp.ListenPacket` network string for raw ICMP.
func ListenerNet(v int) string {
	if v == 6 {
		return "ip6:ipv6-icmp"
	}
	return "ip4:icmp"
}

// RawListenAddr returns the wildcard listen address.
func RawListenAddr(v int) string {
	if v == 6 {
		return "::"
	}
	return "0.0.0.0"
}

// DialNet returns "udp4"/"udp6"/"tcp4"/"tcp6" given a base transport.
func DialNet(v int, transport string) string {
	if v == 6 {
		return transport + "6"
	}
	return transport + "4"
}

// ICMPProtoNum returns the IANA protocol number used for ICMP traffic.
// v4: 1 (ICMP). v6: 58 (ICMPv6).
func ICMPProtoNum(v int) int {
	if v == 6 {
		return 58
	}
	return 1
}

// EchoRequestType returns the icmp.Type for outgoing Echo Requests.
func EchoRequestType(v int) icmp.Type {
	if v == 6 {
		return ipv6.ICMPTypeEchoRequest
	}
	return ipv4.ICMPTypeEcho
}

// EchoReplyType returns the icmp.Type for incoming Echo Replies.
func EchoReplyType(v int) icmp.Type {
	if v == 6 {
		return ipv6.ICMPTypeEchoReply
	}
	return ipv4.ICMPTypeEchoReply
}

// TimeExceededType returns the icmp.Type for Time Exceeded responses.
func TimeExceededType(v int) icmp.Type {
	if v == 6 {
		return ipv6.ICMPTypeTimeExceeded
	}
	return ipv4.ICMPTypeTimeExceeded
}

// ParseInnerHeader extracts source IP, destination IP, transport protocol
// number, and transport src/dst ports from a packet payload as carried
// inside an ICMP error response.
//
// For v == 4: parses IPv4 header (variable-length via IHL) + transport.
// For v == 6: parses IPv6 header (40 bytes) + optional extension headers,
//   following the NextHeader chain until a transport-layer protocol is reached.
// The transport is assumed to be at the start of the returned slice; only
// the first 4 bytes are read (src port, dst port).
func ParseInnerHeader(v int, payload []byte) (src, dst net.IP, proto int, srcPort, dstPort uint16, err error) {
	if v == 6 {
		return parseInnerV6(payload)
	}
	return parseInnerV4(payload)
}

func parseInnerV4(p []byte) (net.IP, net.IP, int, uint16, uint16, error) {
	if len(p) < 20 {
		return nil, nil, 0, 0, 0, errors.New("v4 inner header too short")
	}
	if p[0]>>4 != 4 {
		return nil, nil, 0, 0, 0, errors.New("not an IPv4 header")
	}
	ihl := int(p[0]&0x0f) * 4
	if ihl < 20 || len(p) < ihl+4 {
		return nil, nil, 0, 0, 0, errors.New("v4 inner header truncated")
	}
	proto := int(p[9])
	src := net.IP(append([]byte(nil), p[12:16]...))
	dst := net.IP(append([]byte(nil), p[16:20]...))
	transport := p[ihl:]
	srcPort := binary.BigEndian.Uint16(transport[0:2])
	dstPort := binary.BigEndian.Uint16(transport[2:4])
	return src, dst, proto, srcPort, dstPort, nil
}

// ipv6ExtHeaders lists extension headers that the parser walks through.
// ESP (50) and AH (51) are intentionally omitted because their payloads are opaque.
// RFC 8200 §4.
var ipv6ExtHeaders = map[int]bool{
	0:  true, // Hop-by-Hop Options
	43: true, // Routing
	44: true, // Fragment
	60: true, // Destination Options
}

func parseInnerV6(p []byte) (net.IP, net.IP, int, uint16, uint16, error) {
	if len(p) < 40 {
		return nil, nil, 0, 0, 0, errors.New("v6 inner header too short")
	}
	if p[0]>>4 != 6 {
		return nil, nil, 0, 0, 0, errors.New("not an IPv6 header")
	}
	src := net.IP(append([]byte(nil), p[8:24]...))
	dst := net.IP(append([]byte(nil), p[24:40]...))

	next := int(p[6])
	off := 40

	// Skip extension headers. Each ext header is: [next][hdr ext len][...].
	// hdr ext len is in 8-octet units, not including the first 8 octets.
	// Fragment header (44) has a fixed 8-byte length.
	for ipv6ExtHeaders[next] {
		if off+2 > len(p) {
			return nil, nil, 0, 0, 0, errors.New("v6 ext header truncated")
		}
		hdrLen := 8
		if next != 44 {
			hdrLen = (int(p[off+1]) + 1) * 8
		}
		next = int(p[off])
		off += hdrLen
	}

	if off+4 > len(p) {
		return nil, nil, 0, 0, 0, errors.New("v6 inner: no transport ports")
	}
	srcPort := binary.BigEndian.Uint16(p[off : off+2])
	dstPort := binary.BigEndian.Uint16(p[off+2 : off+4])
	return src, dst, next, srcPort, dstPort, nil
}

// TCPChecksum computes the TCP segment checksum, including the per-family
// pseudo-header. Pass the segment with its own checksum field zeroed.
func TCPChecksum(v int, src, dst net.IP, segment []byte) uint16 {
	if v == 6 {
		return tcpChecksumV6(src.To16(), dst.To16(), segment)
	}
	return tcpChecksumV4(src.To4(), dst.To4(), segment)
}

func tcpChecksumV4(src, dst net.IP, seg []byte) uint16 {
	psh := make([]byte, 12)
	copy(psh[0:4], src)
	copy(psh[4:8], dst)
	psh[9] = 6 // TCP
	binary.BigEndian.PutUint16(psh[10:12], uint16(len(seg)))
	return checksumRFC1071(append(psh, seg...))
}

func tcpChecksumV6(src, dst net.IP, seg []byte) uint16 {
	psh := make([]byte, 40)
	copy(psh[0:16], src)
	copy(psh[16:32], dst)
	binary.BigEndian.PutUint32(psh[32:36], uint32(len(seg)))
	psh[39] = 6 // TCP
	return checksumRFC1071(append(psh, seg...))
}

// IsPrivate returns true for addresses that should not be sent to Team Cymru
// for ASN lookup. Covers v4 RFC 1918 + loopback + link-local, and the v6
// equivalents: ::1, fe80::/10, fc00::/7, ::/128.
func IsPrivate(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		case ip4[0] == 127:
			return true
		case ip4[0] == 169 && ip4[1] == 254:
			return true
		}
		return false
	}
	ip6 := ip.To16()
	if ip6 == nil {
		return false
	}
	if ip6.IsLoopback() {
		return true
	}
	// fe80::/10 link-local
	if ip6[0] == 0xfe && (ip6[1]&0xc0) == 0x80 {
		return true
	}
	// fc00::/7 unique local
	if (ip6[0] & 0xfe) == 0xfc {
		return true
	}
	if ip6.IsUnspecified() {
		return true
	}
	return false
}

// ASNReverseName builds the DNS name for Team Cymru ASN lookup.
//   v4: 1.2.3.4   -> 4.3.2.1.origin.asn.cymru.com
//   v6: 2001:db8::1 -> 1.0.0.0...0.8.b.d.0.1.0.0.2.origin6.asn.cymru.com
// Returns empty string if the IP cannot be classified.
func ASNReverseName(v int, ip net.IP) string {
	if v == 6 {
		ip6 := ip.To16()
		if ip6 == nil || ip6.To4() != nil {
			return ""
		}
		var b []byte
		for i := 15; i >= 0; i-- {
			lo := ip6[i] & 0x0f
			hi := ip6[i] >> 4
			b = append(b, hexNibble(lo), '.', hexNibble(hi), '.')
		}
		return string(b) + "origin6.asn.cymru.com"
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", ip4[3], ip4[2], ip4[1], ip4[0])
}

// TransportOffset returns the byte offset at which the transport-layer
// header begins inside an IP packet of the given version. Returns -1
// if the packet is truncated or malformed. For v6 with extension headers,
// follows the NextHeader chain.
func TransportOffset(v int, p []byte) int {
	if v == 4 {
		if len(p) < 20 {
			return -1
		}
		return int(p[0]&0x0f) * 4
	}
	if v == 6 {
		if len(p) < 40 {
			return -1
		}
		next := int(p[6])
		off := 40
		for ipv6ExtHeaders[next] {
			if off+2 > len(p) {
				return -1
			}
			hdrLen := 8
			if next != 44 {
				hdrLen = (int(p[off+1]) + 1) * 8
			}
			next = int(p[off])
			off += hdrLen
		}
		return off
	}
	return -1
}

// DestinationUnreachableType returns the icmp.Type for Destination Unreachable messages.
func DestinationUnreachableType(v int) icmp.Type {
	if v == 6 {
		return ipv6.ICMPTypeDestinationUnreachable
	}
	return ipv4.ICMPTypeDestinationUnreachable
}

// ICMPTypeNum returns the numeric ICMP type value from an icmp.Type.
// ipv4.ICMPType and ipv6.ICMPType are both uint8 under the interface.
func ICMPTypeNum(t icmp.Type) int {
	switch v := t.(type) {
	case ipv4.ICMPType:
		return int(v)
	case ipv6.ICMPType:
		return int(v)
	default:
		return -1
	}
}

// TransportListenerNet returns the icmp.ListenPacket / net.ListenPacket
// network string for a raw transport-layer socket. Protocol must be "udp" or "tcp".
func TransportListenerNet(v int, proto string) string {
	if v == 6 {
		return "ip6:" + proto
	}
	return "ip4:" + proto
}

func hexNibble(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'a' + (n - 10)
}
