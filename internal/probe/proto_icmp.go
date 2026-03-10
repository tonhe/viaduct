package probe

import (
	"encoding/binary"
	"math/rand"

	"golang.org/x/net/ipv4"
)

// ICMPProtocol implements ProbeProtocol for ICMP Echo probes.
type ICMPProtocol struct {
	sessionID int // random ID to avoid collision between concurrent via instances
}

// NewICMPProtocol creates an ICMP protocol with a random session ID.
func NewICMPProtocol() *ICMPProtocol {
	return &ICMPProtocol{
		sessionID: rand.Intn(0xFFFF-1) + 1, // 1..65534
	}
}

func (p *ICMPProtocol) Name() string           { return "icmp" }
func (p *ICMPProtocol) SupportsMultipath() bool { return false }

// BuildProbe creates an ICMP Echo Request packet and a matching probe key.
// Key encoding: {SrcPort: sessionID, DstPort: seq, Seq: 0} — matches runICMP.
func (p *ICMPProtocol) BuildProbe(flowID, ttl, seq int, cfg Config) ([]byte, probeKey, error) {
	pkt, err := buildICMPEchoRequest(p.sessionID, seq, cfg.PayloadSize)
	if err != nil {
		return nil, probeKey{}, err
	}
	key := probeKey{SrcPort: p.sessionID, DstPort: seq, Seq: 0}
	return pkt, key, nil
}

// IdentifyResponse inspects the inner header from an ICMP error response
// and returns the matching probe key if it contains an ICMP Echo we sent.
func (p *ICMPProtocol) IdentifyResponse(innerHeader []byte) (*probeKey, error) {
	if len(innerHeader) < 28 {
		return nil, nil
	}
	// Check IP protocol field — must be ICMP (1)
	if innerHeader[9] != 1 {
		return nil, nil
	}
	ihl := int(innerHeader[0]&0x0f) * 4
	if len(innerHeader) < ihl+8 {
		return nil, nil
	}
	icmpData := innerHeader[ihl:]
	// ICMP type should be Echo Request (8)
	if icmpData[0] != 8 {
		return nil, nil
	}
	id := int(binary.BigEndian.Uint16(icmpData[4:6]))
	seq := int(binary.BigEndian.Uint16(icmpData[6:8]))
	key := probeKey{SrcPort: id, DstPort: seq, Seq: 0}
	return &key, nil
}

// IsDestReachedICMP returns true if the ICMP type indicates the destination
// was reached. For ICMP probes, only Echo Reply means success.
func (p *ICMPProtocol) IsDestReachedICMP(icmpType, icmpCode int) bool {
	return icmpType == int(ipv4.ICMPTypeEchoReply)
}
