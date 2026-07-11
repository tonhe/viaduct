package probe

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
