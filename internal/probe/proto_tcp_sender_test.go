package probe

import (
	"context"
	"encoding/binary"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// The TCP listener's two rules about where a segment comes from and when it
// stops (viaduct#172):
//
//   - a SYN-ACK or RST answers a probe only if its sender is the target;
//   - a listener whose socket has been closed returns. It does not go on
//     reading the closed socket until the context is cancelled.

// senderConn is a raw TCP socket that delivers one segment from a chosen
// sender and then only times out.
type senderConn struct {
	from      net.Addr
	segment   []byte
	delivered atomic.Bool
}

func (c *senderConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if c.delivered.CompareAndSwap(false, true) {
		return copy(p, c.segment), c.from, nil
	}
	time.Sleep(20 * time.Millisecond)
	return 0, nil, &net.OpError{Op: "read", Err: context.DeadlineExceeded}
}
func (c *senderConn) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }
func (c *senderConn) Close() error                              { return nil }
func (c *senderConn) LocalAddr() net.Addr                       { return &net.IPAddr{} }
func (c *senderConn) SetDeadline(time.Time) error               { return nil }
func (c *senderConn) SetReadDeadline(time.Time) error           { return nil }
func (c *senderConn) SetWriteDeadline(time.Time) error          { return nil }

// A SYN-ACK that acknowledges an outstanding probe is matched when it comes
// from the target, and is not matched when it comes from any other address.
// The unmatched probe stays outstanding, so the genuine answer can still
// arrive.
func TestListenTCP_SenderMustBeTheTarget(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		from    net.Addr
		matched bool
	}{
		{"v4 from the target", "192.0.2.1", &net.IPAddr{IP: net.ParseIP("192.0.2.1")}, true},
		{"v4 from another address", "192.0.2.1", &net.IPAddr{IP: net.ParseIP("198.51.100.7")}, false},
		{"v6 from the target", "2001:db8::1", &net.IPAddr{IP: net.ParseIP("2001:db8::1")}, true},
		{"v6 from another address", "2001:db8::1", &net.IPAddr{IP: net.ParseIP("2001:db8::bad")}, false},
		{"v6 link-local target with a zone", "fe80::1", &net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "eth0"}, true},
		{"sender unknown", "192.0.2.1", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synack := buildFakeTCPPacket(443, 44000, 0x12)
			binary.BigEndian.PutUint32(synack[8:12], 102) // acknowledges sequence 101

			pm := newProbeMap()
			key := probeKey{SrcPort: 44000, DstPort: 443, Seq: 101}
			pm.add(key, 7, 0, time.Now(), ProbeTCPTTL)

			tr := NewTracer(net.ParseIP(tc.target), DefaultConfig())
			results := make(chan Result, 4)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go tr.listenTCP(ctx, &senderConn{from: tc.from, segment: synack}, pm, results)

			select {
			case res := <-results:
				if !tc.matched {
					t.Fatalf("a segment from %v was matched as the target's answer: %+v", tc.from, res)
				}
				if !res.IsTarget || res.TTL != 7 || !res.IP.Equal(net.ParseIP(tc.target)) {
					t.Errorf("result = %+v, want the target at TTL 7", res)
				}
			case <-time.After(300 * time.Millisecond):
				if tc.matched {
					t.Fatal("the target's SYN-ACK was not matched")
				}
				if n := pm.pending(time.Hour); n != 1 {
					t.Errorf("%d probes outstanding after a foreign segment, want 1: the probe must stay matchable", n)
				}
				if got := atomic.LoadInt32(&tr.targetTTL); got != 0 {
					t.Errorf("targetTTL = %d after a foreign segment, want 0", got)
				}
			}
		})
	}
}

// closedConn is a raw TCP socket that has been closed: every read fails at
// once with net.ErrClosed.
type closedConn struct {
	senderConn
	reads atomic.Int64
}

func (c *closedConn) ReadFrom([]byte) (int, net.Addr, error) {
	c.reads.Add(1)
	return 0, nil, &net.OpError{Op: "read", Err: net.ErrClosed}
}

// When its socket is closed the listener returns, without the context being
// cancelled. Before, it read the closed socket in a loop with no pause until
// the cancel.
func TestListenTCP_ReturnsWhenItsSocketIsClosed(t *testing.T) {
	tr := NewTracer(net.ParseIP("192.0.2.1"), DefaultConfig())
	conn := &closedConn{}
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		tr.listenTCP(ctx, conn, newProbeMap(), make(chan Result, 1))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("the listener is still running 500ms after its socket was closed, with the context live; it read the closed socket %d times", conn.reads.Load())
	}
	if n := conn.reads.Load(); n != 1 {
		t.Errorf("the closed socket was read %d times, want 1", n)
	}
}

// errorConn is a raw TCP socket whose every read fails at once with an error
// that is neither a timeout nor a close.
type errorConn struct {
	senderConn
	reads atomic.Int64
}

func (c *errorConn) ReadFrom([]byte) (int, net.Addr, error) {
	c.reads.Add(1)
	return 0, nil, &net.OpError{Op: "read", Err: net.UnknownNetworkError("test")}
}

// A read error that is not a timeout and not a close is retried after a
// pause, not in a tight loop.
func TestListenTCP_PausesAfterAReadError(t *testing.T) {
	tr := NewTracer(net.ParseIP("192.0.2.1"), DefaultConfig())
	conn := &errorConn{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tr.listenTCP(ctx, conn, newProbeMap(), make(chan Result, 1))
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the listener did not stop when the context was cancelled")
	}
	// 200ms of failing reads: a handful with a pause, hundreds of thousands
	// without one.
	if n := conn.reads.Load(); n < 2 || n > 100 {
		t.Errorf("a failing socket was read %d times in 200ms, want a few (retried, with a pause)", n)
	}
}
