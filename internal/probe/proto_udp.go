package probe

import (
	"encoding/binary"
	"net"
)

// udpChecksumV6 computes the UDP checksum for an IPv6 datagram: the RFC 1071
// sum over the IPv6 pseudo-header (source, destination, upper-layer length,
// next header 17) followed by the UDP header and payload. Pass the datagram
// with its checksum field zeroed.
//
// IPv6 makes the UDP checksum mandatory (RFC 8200 section 8.1): a receiver
// discards a datagram whose checksum field is zero. A sum that computes to
// zero is therefore sent as 0xFFFF (RFC 768).
func udpChecksumV6(src, dst net.IP, datagram []byte) uint16 {
	psh := make([]byte, 40, 40+len(datagram))
	copy(psh[0:16], src.To16())
	copy(psh[16:32], dst.To16())
	binary.BigEndian.PutUint32(psh[32:36], uint32(len(datagram)))
	psh[39] = 17 // UDP
	sum := checksumRFC1071(append(psh, datagram...))
	if sum == 0 {
		return 0xffff
	}
	return sum
}

// UDPProtocol implements ProbeProtocol for UDP probes.
type UDPProtocol struct {
	BaseDstPort int // Base destination port (actual = BaseDstPort + TTL)
}

// NewUDPProtocol creates a UDP protocol with the given base destination port.
func NewUDPProtocol(baseDstPort int) *UDPProtocol {
	return &UDPProtocol{BaseDstPort: baseDstPort}
}

func (p *UDPProtocol) Name() string           { return "udp" }
func (p *UDPProtocol) SupportsMultipath() bool { return true }

// BuildProbe creates a UDP packet and a matching probe key.
// Key encoding: {SrcPort, DstPort, Seq: 0} — matches existing runUDP.
func (p *UDPProtocol) BuildProbe(flowID, ttl, seq int, cfg Config) ([]byte, probeKey, error) {
	srcPort := cfg.BasePort + flowID
	dstPort := p.BaseDstPort + ttl
	pkt, err := buildUDPProbe(srcPort, dstPort, cfg.PayloadSize)
	if err != nil {
		return nil, probeKey{}, err
	}
	// IPv6 requires a UDP checksum; without one the destination discards the
	// probe and never sends Port Unreachable. IPv4 keeps the zero checksum,
	// which is legal there. If either address is unknown the checksum cannot
	// be computed and the field stays zero, as before.
	if cfg.IPVersion == 6 && cfg.SourceIP != nil && cfg.TargetIP != nil {
		binary.BigEndian.PutUint16(pkt[6:8], udpChecksumV6(cfg.SourceIP, cfg.TargetIP, pkt))
	}
	key := probeKey{SrcPort: srcPort, DstPort: dstPort, Seq: 0}
	return pkt, key, nil
}

// IdentifyResponse inspects the inner header from an ICMP error response
// and returns the matching probe key if it contains a UDP packet.
// Supports both IPv4 and IPv6 inner headers.
func (p *UDPProtocol) IdentifyResponse(innerHeader []byte) (*probeKey, error) {
	if len(innerHeader) < 1 {
		return nil, nil
	}
	v := int(innerHeader[0] >> 4)
	if v != 4 && v != 6 {
		return nil, nil
	}
	_, _, proto, srcPort, dstPort, err := ParseInnerHeader(v, innerHeader)
	if err != nil {
		return nil, nil
	}
	if proto != 17 {
		return nil, nil
	}
	key := probeKey{SrcPort: int(srcPort), DstPort: int(dstPort), Seq: 0}
	return &key, nil
}

// IsDestReachedICMP returns true if the ICMP type/code indicates the destination
// was reached. For UDP probes, Port Unreachable means success:
//   IPv4: ICMP Type 3 (Destination Unreachable), Code 3 (Port Unreachable)
//   IPv6: ICMPv6 Type 1 (Destination Unreachable), Code 4 (Port Unreachable)
func (p *UDPProtocol) IsDestReachedICMP(icmpType, icmpCode int) bool {
	if icmpType == 3 && icmpCode == 3 {
		return true
	}
	if icmpType == 1 && icmpCode == 4 {
		return true
	}
	return false
}
