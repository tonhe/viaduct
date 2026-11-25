package probe

import (
	"net"
	"sync/atomic"
	"time"
)

// Where the TCP listener reads the destination's SYN-ACK and RST from
// (viaduct#172).
//
//   - macOS: a BPF device on the outgoing interface (tcp_recv_bpf_darwin.go).
//     The kernel there hands an incoming TCP segment to its TCP input routine
//     only; a raw "ip4:tcp" or "ip6:tcp" socket can send SYNs and never
//     receives a segment.
//   - Everywhere else: the raw socket the SYNs are sent on (tcp_recv_raw.go).
//     Linux delivers a copy of every incoming TCP segment to it. The other
//     BSDs are built this way too and are expected to receive nothing, as
//     macOS did: TCP mode is not supported there.
//
// Sending is the same on every platform.

// tcpSegmentSource is what the TCP listener reads: TCP segments with no IP
// header, each with the address it was sent from. A raw TCP socket
// (net.PacketConn) is one as it is.
//
// ReadFrom returns an error whose Timeout method reports true when nothing
// arrived in time, and an error that wraps net.ErrClosed once the source has
// been closed.
type tcpSegmentSource interface {
	ReadFrom(b []byte) (n int, from net.Addr, err error)
	SetReadDeadline(t time.Time) error
}

// tcpSource returns the source the TCP listener reads for this run, and a
// function that closes whatever was opened for it. rawConn is the raw TCP
// send socket; it is nil in a test that supplies its sockets through
// newSender.
func (t *Tracer) tcpSource(rawConn net.PacketConn) (tcpSegmentSource, func(), error) {
	open := t.newTCPSource
	if open == nil {
		open = t.openTCPSource
	}
	src, cleanup, err := open(rawConn)
	if cleanup == nil {
		cleanup = func() {}
	}
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return src, cleanup, nil
}

// tcpReplyPorts returns the range of local ports a reply to one of this
// tracer's TCP probes is addressed to: the probes' source ports, one per
// flow.
func (t *Tracer) tcpReplyPorts() (lo, hi int) {
	flows := t.cfg.NumPaths
	if flows < 1 || (t.cfg.Protocol != nil && !t.cfg.Protocol.SupportsMultipath()) {
		flows = 1
	}
	return t.cfg.BasePort, t.cfg.BasePort + flows - 1
}

// tcpRecvStats counts what the TCP listener read, for the debug log.
type tcpRecvStats struct {
	segments   atomic.Uint64 // TCP segments read from the source
	fromTarget atomic.Uint64 // of those, sent by the target
	replies    atomic.Uint64 // of those, a SYN-ACK or an RST with ACK
	matched    atomic.Uint64 // of those, matched to an outstanding probe
}

// logTCPReceive writes the listener's running totals to the debug log. label
// says when: "round 3", "at exit".
func (t *Tracer) logTCPReceive(label string) {
	if t.cfg.Debugf == nil {
		return
	}
	s := &t.tcpRecv
	t.debugf("%s: tcp receive totals: %d segments read, %d from the target, %d SYN-ACK/RST, %d matched",
		label, s.segments.Load(), s.fromTarget.Load(), s.replies.Load(), s.matched.Load())
}
