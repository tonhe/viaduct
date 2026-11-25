package probe

// EXPERIMENT ONLY, not for merge (viaduct#172).
//
// Counters and debug-log lines that answer one question from a --debug-log
// file on any OS: does the raw TCP socket deliver the destination's SYN-ACK
// and RST segments, and if it does, why are they not matched to a probe?
//
// Nothing here changes what the listener does with a packet. Without
// --debug-log the counters are kept and nothing is written.

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync/atomic"
)

// expTCPDetailLimit bounds the per-packet lines of each kind.
const expTCPDetailLimit = 8

// expTCPRecv counts what listenTCP read. The listener goroutine writes it and
// the send loop reads it, so every field is atomic.
type expTCPRecv struct {
	reads      atomic.Int64 // packets returned by ReadFrom
	readErrs   atomic.Int64 // ReadFrom errors other than the read deadline
	fromTarget atomic.Int64 // packets whose sender is the target
	fromOther  atomic.Int64 // packets from any other sender
	tcpTarget  atomic.Int64 // from the target and at least a 20-byte TCP header
	parsed     atomic.Int64 // accepted by parseTCPResponse (SYN-ACK or RST+ACK)
	rejected   atomic.Int64 // refused by parseTCPResponse
	matched    atomic.Int64 // matched a pending probe
	unmatched  atomic.Int64 // parsed, no pending probe under that key

	loggedFirst     atomic.Int64
	loggedRejected  atomic.Int64
	loggedUnmatched atomic.Int64
	loggedErrs      atomic.Int64
}

// expTCPFlags renders the TCP flag byte, for example "SYN|ACK".
func expTCPFlags(f byte) string {
	names := []struct {
		bit  byte
		name string
	}{{0x01, "FIN"}, {0x02, "SYN"}, {0x04, "RST"}, {0x08, "PSH"}, {0x10, "ACK"}, {0x20, "URG"}}
	out := ""
	for _, n := range names {
		if f&n.bit != 0 {
			if out != "" {
				out += "|"
			}
			out += n.name
		}
	}
	if out == "" {
		return "none"
	}
	return out
}

// expTCPSegment describes the first 20 bytes of b read as a TCP header.
func expTCPSegment(b []byte) string {
	if len(b) < 20 {
		return fmt.Sprintf("len=%d (shorter than a TCP header) bytes=%x", len(b), b)
	}
	return fmt.Sprintf("len=%d sport=%d dport=%d seq=%d ack=%d flags=%s first20=%x",
		len(b),
		binary.BigEndian.Uint16(b[0:2]), binary.BigEndian.Uint16(b[2:4]),
		binary.BigEndian.Uint32(b[4:8]), binary.BigEndian.Uint32(b[8:12]),
		expTCPFlags(b[13]), b[:20])
}

// expPending describes the pending records that share key's port pair: how
// many there are and the range of their sequence numbers. It tells a reply
// that missed on the sequence number from one that missed on the ports, and
// from one that arrived after its record was matched or swept.
func (pm *probeMap) expPending(key probeKey) string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	total, same, lo, hi := len(pm.items), 0, 0, 0
	for k := range pm.items {
		if k.SrcPort != key.SrcPort || k.DstPort != key.DstPort {
			continue
		}
		if same == 0 || k.Seq < lo {
			lo = k.Seq
		}
		if same == 0 || k.Seq > hi {
			hi = k.Seq
		}
		same++
	}
	if same == 0 {
		return fmt.Sprintf("pending records: %d in all, none with this port pair", total)
	}
	return fmt.Sprintf("pending records: %d in all, %d with this port pair, seq %d..%d", total, same, lo, hi)
}

// expTCPSocket logs, once per socket, the raw TCP socket's network and local
// address.
func (t *Tracer) expTCPSocket(network string, conn net.PacketConn) {
	local := "unknown"
	if a := conn.LocalAddr(); a != nil {
		local = a.Network() + " " + a.String()
	}
	t.debugf("EXP tcp-recv: raw socket opened: network %s, local address %s, source address used in the checksum %v, base source port %d",
		network, local, t.cfg.SourceIP, t.cfg.BasePort)
}

// expTCPRead records one packet read by listenTCP, before it is parsed.
func (t *Tracer) expTCPRead(b []byte, from net.Addr) {
	s := &t.expTCP
	s.reads.Add(1)
	var src net.IP
	if from != nil {
		src = peerIP(from)
	}
	isTarget := src != nil && src.Equal(t.target)
	if isTarget {
		s.fromTarget.Add(1)
		if len(b) >= 20 {
			s.tcpTarget.Add(1)
		}
	} else {
		s.fromOther.Add(1)
	}
	if s.loggedFirst.Add(1) <= expTCPDetailLimit {
		t.debugf("EXP tcp-recv: read #%d from %v (target: %v): %s", s.reads.Load(), from, isTarget, expTCPSegment(b))
	}
}

// expTCPReadErr records a ReadFrom error that is not the read deadline.
func (t *Tracer) expTCPReadErr(err error) {
	if isSendTimeout(err) {
		return
	}
	s := &t.expTCP
	s.readErrs.Add(1)
	if s.loggedErrs.Add(1) <= expTCPDetailLimit {
		t.debugf("EXP tcp-recv: read error: %v", err)
	}
}

// expTCPRejected records a packet parseTCPResponse refused.
func (t *Tracer) expTCPRejected(b []byte, from net.Addr) {
	s := &t.expTCP
	s.rejected.Add(1)
	if s.loggedRejected.Add(1) <= expTCPDetailLimit {
		t.debugf("EXP tcp-recv: not a SYN-ACK or RST+ACK, from %v: %s", from, expTCPSegment(b))
	}
}

// expTCPUnmatched records a parsed reply with no pending probe under its key.
func (t *Tracer) expTCPUnmatched(b []byte, from net.Addr, key probeKey, pm *probeMap) {
	s := &t.expTCP
	s.parsed.Add(1)
	s.unmatched.Add(1)
	if s.loggedUnmatched.Add(1) <= expTCPDetailLimit {
		t.debugf("EXP tcp-recv: UNMATCHED from %v: %s; looked up key {SrcPort:%d DstPort:%d Seq:%d}; %s",
			from, expTCPSegment(b), key.SrcPort, key.DstPort, key.Seq, pm.expPending(key))
	}
}

// expTCPMatched records a parsed reply that matched a pending probe.
func (t *Tracer) expTCPMatched() {
	t.expTCP.parsed.Add(1)
	t.expTCP.matched.Add(1)
}

// expTCPSummary writes the running totals. when is "round N" or "exit".
func (t *Tracer) expTCPSummary(when string) {
	s := &t.expTCP
	t.debugf("EXP tcp-recv: %s totals: read %d (errors %d), from target %d, from others %d, TCP segments from target %d, SYN-ACK/RST parsed %d, other flags %d, matched %d, unmatched %d",
		when, s.reads.Load(), s.readErrs.Load(), s.fromTarget.Load(), s.fromOther.Load(),
		s.tcpTarget.Load(), s.parsed.Load(), s.rejected.Load(), s.matched.Load(), s.unmatched.Load())
}
