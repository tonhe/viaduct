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

// NewICMPProtocol creates an ICMP protocol with a session ID from config or random.
func NewICMPProtocol(cfg Config) *ICMPProtocol {
	id := cfg.ICMPID
	if id == 0 {
		id = 1 + rand.Intn(65534) // 1..65534
	}
	return &ICMPProtocol{
		sessionID: id,
	}
}

func (p *ICMPProtocol) Name() string           { return "icmp" }
func (p *ICMPProtocol) SupportsMultipath() bool { return false }

// icmpWireSeq is the sequence number an ICMP echo carries for a run's probe
// counter. The field is 16 bits; the counter is not, and passes 65,535 within
// half an hour in the default mode. Probe keys use this value, because it is
// the one that comes back in an Echo Reply or a quoted header.
func icmpWireSeq(seq int) int { return seq & 0xffff }

// BuildProbe creates an ICMP Echo Request packet and a matching probe key.
// Key encoding: {SrcPort: sessionID, DstPort: 16-bit sequence, Seq: 0}.
func (p *ICMPProtocol) BuildProbe(flowID, ttl, seq int, cfg Config) ([]byte, probeKey, error) {
	wire := icmpWireSeq(seq)
	pkt, err := buildICMPEchoRequest(cfg.IPVersion, p.sessionID, wire, cfg.PayloadSize)
	if err != nil {
		return nil, probeKey{}, err
	}
	key := probeKey{SrcPort: p.sessionID, DstPort: wire, Seq: 0}
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
