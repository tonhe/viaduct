package probe

import (
	"encoding/binary"
	"math/rand"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
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
	pkt, err := buildICMPEchoRequest(cfg.IPVersion, p.sessionID, seq, cfg.PayloadSize)
	if err != nil {
		return nil, probeKey{}, err
	}
	key := probeKey{SrcPort: p.sessionID, DstPort: seq, Seq: 0}
	return pkt, key, nil
}

// IdentifyResponse inspects the inner header from an ICMP error response
// and returns the matching probe key if it contains an ICMP Echo we sent.
// The IP version of the inner header is detected from the first nibble.
func (p *ICMPProtocol) IdentifyResponse(innerHeader []byte) (*probeKey, error) {
	if len(innerHeader) < 1 {
		return nil, nil
	}
	v := int(innerHeader[0] >> 4)
	if v != 4 && v != 6 {
		return nil, nil
	}
	off := TransportOffset(v, innerHeader)
	if off < 0 || len(innerHeader) < off+8 {
		return nil, nil
	}
	icmpData := innerHeader[off:]
	// ICMP Echo Request type byte: v4 = 8, v6 = 128.
	wantType := byte(8)
	if v == 6 {
		wantType = 128
	}
	if icmpData[0] != wantType {
		return nil, nil
	}
	id := int(binary.BigEndian.Uint16(icmpData[4:6]))
	seq := int(binary.BigEndian.Uint16(icmpData[6:8]))
	key := probeKey{SrcPort: id, DstPort: seq, Seq: 0}
	return &key, nil
}

// IsDestReachedICMP returns true if the ICMP type indicates the destination
// was reached. Accepts both ICMPv4 Echo Reply (type 0) and ICMPv6 Echo Reply
// (type 129); the type spaces don't overlap so accepting both is safe.
func (p *ICMPProtocol) IsDestReachedICMP(icmpType, icmpCode int) bool {
	return icmpType == int(ipv4.ICMPTypeEchoReply) ||
		icmpType == int(ipv6.ICMPTypeEchoReply)
}
