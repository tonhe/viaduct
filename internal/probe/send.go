package probe

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// The send path's guard against a socket that stops accepting writes
// (viaduct#115).
//
// A raw-socket write that the kernel answers with EAGAIN parks the goroutine
// until the socket is writable again. If it never is, a write with no deadline
// stops the send loop for good. So every probe write gets a deadline, and a
// write that fails or times out is a probe that was not sent: its record is
// removed, it is not counted as sent, and the loop moves on.

// sendDeadliner is implemented by a probeSender backed by a socket. The send
// loop sets a write deadline through it immediately before every write. Both
// production senders implement it; a test fake that does not is written to
// without a deadline.
type sendDeadliner interface {
	SetWriteDeadline(t time.Time) error
}

var (
	_ sendDeadliner = (*icmpSender)(nil)
	_ sendDeadliner = (*rawSender)(nil)
)

// sendSocket names the socket a probe is written to. A stall is a property of
// a socket, not of a probe type: the ICMP TTL probes and the echo tier share
// one socket, and the UDP or TCP flows have their own.
type sendSocket int

const (
	sockICMP    sendSocket = iota // ICMP TTL probes (and the echo tier when it has no socket of its own)
	sockPrimary                   // UDP or TCP flow probes
	sockEcho                      // the echo tier's own socket
	numSendSockets
)

const numProbeTypes = int(ProbeICMPEcho) + 1

// maxLoggedSendFailures bounds the once-per-address memory of the send log.
const maxLoggedSendFailures = 256

// sendCounts are the probes of one type that did not leave in a round.
type sendCounts struct {
	timeouts int // the write waited out its deadline
	errors   int // the hop limit could not be set, or the write failed at once
	skipped  int // not attempted: the socket had already timed out this round
}

// sendState is the send loop's record of failed sends. Only the send-loop
// goroutine touches it.
type sendState struct {
	// oneSocket is set when both sockets are the same one, as in tests that
	// route every tier through a single fake sender.
	oneSocket bool
	// stalled marks a socket on which a write timed out this round. The rest
	// of the round's writes to it are skipped: each would wait out its own
	// deadline, and 30 hops of 7 probes plus 64 echoes would stretch the round
	// by seconds.
	stalled [numSendSockets]bool
	counts  [numProbeTypes]sendCounts
	// logged holds the "type address" keys already written to the debug log.
	logged map[string]struct{}
}

func (s *sendState) isStalled(sock sendSocket) bool {
	if s.oneSocket {
		return s.stalled[sockICMP] || s.stalled[sockPrimary]
	}
	return s.stalled[sock]
}

// sendTimeout is the deadline given to each probe write.
func (t *Tracer) sendTimeout() time.Duration {
	if t.cfg.SendTimeout > 0 {
		return t.cfg.SendTimeout
	}
	return defaultSendTimeout
}

// send writes one probe with the given TTL or hop limit and reports whether
// it left. A write deadline is set immediately before the write. A probe that
// did not leave is counted for the round's debug-log line, and the failure is
// logged once per address.
//
// When the write times out, the socket is marked stalled and every later send
// to it in this round is skipped without being attempted.
func (t *Tracer) send(snd probeSender, sock sendSocket, pt ProbeType, ttl int, pkt []byte, dst *net.IPAddr) bool {
	st := &t.sends
	if st.isStalled(sock) {
		st.counts[pt].skipped++
		return false
	}
	err := snd.SetTTL(ttl)
	if err == nil {
		if d, ok := snd.(sendDeadliner); ok {
			err = d.SetWriteDeadline(time.Now().Add(t.sendTimeout()))
		}
	}
	if err == nil {
		_, err = snd.WriteTo(pkt, dst)
	}
	if err == nil {
		return true
	}
	if isSendTimeout(err) {
		st.counts[pt].timeouts++
		st.stalled[sock] = true
		t.logOnce(pt.String()+" "+dst.IP.String(),
			"send timed out after %v: %s to %s (ttl %d): %v; the socket is not accepting writes; logged once for this address",
			t.sendTimeout(), pt, dst.IP, ttl, err)
		return false
	}
	st.counts[pt].errors++
	t.logOnce(pt.String()+" "+dst.IP.String(),
		"send failed: %s to %s (ttl %d): %v; logged once for this address", pt, dst.IP, ttl, err)
	return false
}

// isSendTimeout reports whether a write failed because its deadline passed.
func isSendTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// endSendRound closes the send accounting of a round: one debug-log line if
// any probe did not leave, then a clean slate, so the next round tries every
// socket again.
func (t *Tracer) endSendRound(round int) {
	st := &t.sends
	var parts []string
	for pt, c := range st.counts {
		if c == (sendCounts{}) {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d timed out, %d failed, %d skipped",
			ProbeType(pt), c.timeouts, c.errors, c.skipped))
	}
	if len(parts) > 0 {
		t.debugf("round %d: probes not sent: %s", round, strings.Join(parts, "; "))
	}
	st.counts = [numProbeTypes]sendCounts{}
	st.stalled = [numSendSockets]bool{}
}

// debugf writes one line to the debug log, if there is one.
func (t *Tracer) debugf(format string, args ...any) {
	if t.cfg.Debugf == nil {
		return
	}
	t.cfg.Debugf("[%s] %s", t.target, fmt.Sprintf(format, args...))
}

// logOnce writes a debug-log line the first time it is called with key. A
// stalled socket fails every probe of every round; one line per address says
// all there is to say.
func (t *Tracer) logOnce(key, format string, args ...any) {
	if t.cfg.Debugf == nil {
		return
	}
	st := &t.sends
	if _, seen := st.logged[key]; seen {
		return
	}
	if len(st.logged) >= maxLoggedSendFailures {
		return
	}
	if st.logged == nil {
		st.logged = make(map[string]struct{})
	}
	st.logged[key] = struct{}{}
	t.debugf(format, args...)
}
