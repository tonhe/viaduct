package probe

import "encoding/binary"

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
func (p *UDPProtocol) IdentifyResponse(innerHeader []byte) (*probeKey, error) {
	if len(innerHeader) < 28 {
		return nil, nil
	}
	// Validate IP protocol field is UDP (17)
	if innerHeader[9] != 17 {
		return nil, nil
	}
	ihl := int(innerHeader[0]&0x0f) * 4
	if len(innerHeader) < ihl+8 {
		return nil, nil
	}
	udpData := innerHeader[ihl:]
	srcPort := int(binary.BigEndian.Uint16(udpData[0:2]))
	dstPort := int(binary.BigEndian.Uint16(udpData[2:4]))
	key := probeKey{SrcPort: srcPort, DstPort: dstPort, Seq: 0}
	return &key, nil
}

// IsDestReachedICMP returns true if the ICMP type/code indicates the destination
// was reached. For UDP probes, Port Unreachable (type 3, code 3) means success.
func (p *UDPProtocol) IsDestReachedICMP(icmpType, icmpCode int) bool {
	return icmpType == 3 && icmpCode == 3
}
