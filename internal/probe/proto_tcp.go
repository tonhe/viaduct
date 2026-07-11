package probe

import (
	"context"
	"encoding/binary"
	"net"
	"sync/atomic"
	"time"
)

// TCPProtocol implements ProbeProtocol for TCP SYN probes.
type TCPProtocol struct {
	DstPort int // Fixed destination port (default 443)
}

// NewTCPProtocol creates a TCP protocol with the given fixed destination port.
func NewTCPProtocol(dstPort int) *TCPProtocol {
	return &TCPProtocol{DstPort: dstPort}
}

func (p *TCPProtocol) Name() string           { return "tcp" }
func (p *TCPProtocol) SupportsMultipath() bool { return true }

// BuildProbe creates a TCP SYN packet and a matching probe key.
// Key encoding: {SrcPort, DstPort, Seq: 0} — Seq is always 0 in the key.
func (p *TCPProtocol) BuildProbe(flowID, ttl, seq int, cfg Config) ([]byte, probeKey, error) {
	srcPort := cfg.BasePort + flowID

	hdr := make([]byte, 20) // TCP header, 20 bytes, no options
	binary.BigEndian.PutUint16(hdr[0:2], uint16(srcPort))
	binary.BigEndian.PutUint16(hdr[2:4], uint16(p.DstPort))
	binary.BigEndian.PutUint32(hdr[4:8], uint32(seq)) // sequence number
	binary.BigEndian.PutUint32(hdr[8:12], 0)          // ack number
	hdr[12] = 5 << 4                                  // data offset (5*4=20 bytes)
	hdr[13] = 0x02                                    // SYN flag
	binary.BigEndian.PutUint16(hdr[14:16], 65535)     // window size

	checksum := TCPChecksum(cfg.IPVersion, cfg.SourceIP, cfg.TargetIP, hdr)
	binary.BigEndian.PutUint16(hdr[16:18], checksum)

	key := probeKey{SrcPort: srcPort, DstPort: p.DstPort, Seq: 0}
	return hdr, key, nil
}

// IdentifyResponse inspects the inner header from an ICMP error response
// and returns the matching probe key if it contains a TCP packet.
func (p *TCPProtocol) IdentifyResponse(innerHeader []byte) (*probeKey, error) {
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
	if proto != 6 {
		return nil, nil
	}
	key := probeKey{SrcPort: int(srcPort), DstPort: int(dstPort), Seq: 0}
	return &key, nil
}

// IsDestReachedICMP returns false for TCP. TCP destination detection happens
// via the TCP listener (SYN-ACK or RST), not via ICMP messages.
func (p *TCPProtocol) IsDestReachedICMP(icmpType, icmpCode int) bool {
	return false
}

// parseTCPResponse extracts a probe key from a raw TCP response.
// On macOS, net.ListenPacket("ip4:tcp" or "ip6:tcp") strips the IP header, so the input
// is the TCP segment directly (no IP header).
// Returns nil if the packet is not a SYN-ACK or RST.
func parseTCPResponse(tcpData []byte) (*probeKey, bool) {
	if len(tcpData) < 20 {
		return nil, false
	}
	flags := tcpData[13]
	isSYNACK := flags&0x12 == 0x12
	isRST := flags&0x04 != 0
	if !isSYNACK && !isRST {
		return nil, false
	}
	// Reply dst port = our original src port
	ourSrcPort := int(binary.BigEndian.Uint16(tcpData[2:4]))
	// Reply src port = their port
	theirPort := int(binary.BigEndian.Uint16(tcpData[0:2]))
	key := probeKey{SrcPort: ourSrcPort, DstPort: theirPort, Seq: 0}
	return &key, true
}

// listenTCP listens for SYN-ACK and RST responses on a raw TCP socket.
// On macOS, net.ListenPacket("ip4:tcp" or "ip6:tcp") delivers TCP segments without IP header.
func (t *Tracer) listenTCP(ctx context.Context, conn net.PacketConn, pm *probeMap, results chan<- Result) {
	buf := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			continue
		}
		key, isTarget := parseTCPResponse(buf[:n])
		if key == nil || !isTarget {
			continue
		}
		rec, ok := pm.match(*key)
		if !ok {
			continue
		}
		// Record the target TTL so the sender can cap its range
		atomic.CompareAndSwapInt32(&t.targetTTL, 0, int32(rec.ttl))

		rtt := time.Since(rec.sentAt)
		select {
		case results <- Result{
			TTL:      rec.ttl,
			IP:       t.target,
			RTT:      rtt,
			IsTarget: true,
			FlowID:   rec.flowID,
		}:
		case <-ctx.Done():
			return
		}
	}
}

// checksumRFC1071 computes the Internet checksum per RFC 1071.
func checksumRFC1071(data []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(data); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(data[i : i+2]))
	}
	if len(data)%2 == 1 {
		sum += uint32(data[len(data)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
