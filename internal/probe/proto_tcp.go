package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
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

// tcpKeySeq is the probe-key form of a TCP sequence number.
func tcpKeySeq(seq uint32) int { return int(seq) }

// BuildProbe creates a TCP SYN packet and a matching probe key.
//
// Key encoding: {SrcPort, DstPort, Seq}. The sequence number is what tells
// one probe from another: source and destination port are the same for every
// TTL of a flow, so a key without it made each probe overwrite the record of
// the previous TTL's probe. The sequence number is in the first 8 bytes of
// the TCP header, which every ICMP error quotes, and the target acknowledges
// it plus one, so it can be recovered from either kind of reply.
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

	// On an IPv6 send socket with IPV6_CHECKSUM the kernel fills the checksum,
	// over the source address it really sends from, and the field is left
	// zero here (checksum6.go).
	if cfg.IPVersion != 6 || !cfg.kernelChecksum {
		checksum := TCPChecksum(cfg.IPVersion, cfg.SourceIP, cfg.TargetIP, hdr)
		binary.BigEndian.PutUint16(hdr[tcpChecksumOffset:], checksum)
	}

	key := probeKey{SrcPort: srcPort, DstPort: p.DstPort, Seq: tcpKeySeq(uint32(seq))}
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
	// The quoted TCP header: ports in bytes 0-3, sequence number in 4-7.
	// Without the sequence number the probe cannot be told from the same
	// flow's probes at other TTLs, so such a quote identifies nothing.
	off := TransportOffset(v, innerHeader)
	if off < 0 || len(innerHeader) < off+8 {
		return nil, nil
	}
	seq := binary.BigEndian.Uint32(innerHeader[off+4 : off+8])
	key := probeKey{SrcPort: int(srcPort), DstPort: int(dstPort), Seq: tcpKeySeq(seq)}
	return &key, nil
}

// IsDestReachedICMP returns false for TCP. TCP destination detection happens
// via the TCP listener (SYN-ACK or RST), not via ICMP messages.
func (p *TCPProtocol) IsDestReachedICMP(icmpType, icmpCode int) bool {
	return false
}

// parseTCPResponse extracts a probe key from a TCP segment read by the TCP
// listener. The input starts at the TCP header: a raw "ip4:tcp" socket read
// through package net has its IP header stripped, a raw "ip6:tcp" socket
// never delivers one, and the packet capture used on macOS strips it in
// tcp_frame.go.
// Returns nil if the segment is not a SYN-ACK or an RST with ACK.
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
	// Both a SYN-ACK and the reset a closed port sends in answer to a SYN
	// carry ACK with our sequence number plus one (RFC 793). A reset without
	// ACK answers something else and cannot be tied to a probe.
	if flags&0x10 == 0 {
		return nil, false
	}
	// Reply dst port = our original src port
	ourSrcPort := int(binary.BigEndian.Uint16(tcpData[2:4]))
	// Reply src port = their port
	theirPort := int(binary.BigEndian.Uint16(tcpData[0:2]))
	ack := binary.BigEndian.Uint32(tcpData[8:12])
	key := probeKey{SrcPort: ourSrcPort, DstPort: theirPort, Seq: tcpKeySeq(ack - 1)}
	return &key, true
}

// tcpReadErrorPause is how long the TCP listener waits before it reads again
// after a read error that is neither a timeout nor a close.
const tcpReadErrorPause = 20 * time.Millisecond

// listenTCP reads the destination's SYN-ACK and RST replies from src and
// matches them to outstanding probes. src is the raw TCP socket the probes
// are sent on, or on macOS, where the kernel does not deliver TCP to raw
// sockets, a packet capture on the outgoing interface (tcp_recv.go).
//
// A segment answers a probe only if it was sent by the target. The raw socket
// is handed every TCP segment the host receives, and one from another host
// whose ports and acknowledgement number happen to fit is not the target's
// answer: it is ignored and the probe stays outstanding.
//
// The listener returns when ctx is cancelled or src is closed.
func (t *Tracer) listenTCP(ctx context.Context, src tcpSegmentSource, pm *probeMap, results chan<- Result) {
	buf := make([]byte, 1500)
	stats := &t.tcpRecv
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		src.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, peer, err := src.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue // nothing arrived: check ctx and read again
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(tcpReadErrorPause):
			}
			continue
		}
		stats.segments.Add(1)
		if peer == nil {
			continue
		}
		if from := peerIP(peer); from == nil || !from.Equal(t.target) {
			continue
		}
		stats.fromTarget.Add(1)
		key, isTarget := parseTCPResponse(buf[:n])
		if key == nil || !isTarget {
			continue
		}
		stats.replies.Add(1)
		rec, ok := pm.match(*key)
		if !ok {
			continue
		}
		stats.matched.Add(1)
		// Record the target TTL so the sender can cap its range
		atomic.CompareAndSwapInt32(&t.targetTTL, 0, int32(rec.ttl))

		rtt := time.Since(rec.sentAt)
		select {
		case results <- Result{
			TTL:       rec.ttl,
			IP:        t.target,
			RTT:       rtt,
			IsTarget:  true,
			FlowID:    rec.flowID,
			ProbeType: ProbeTCPTTL,
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
