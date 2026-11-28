package probe

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// closedICMPConn is an icmpPacketConn whose socket has been closed: every
// read fails at once with the error a closed *icmp.PacketConn returns.
type closedICMPConn struct {
	reads atomic.Int64
}

func (c *closedICMPConn) ReadFrom([]byte) (int, net.Addr, error) {
	c.reads.Add(1)
	return 0, nil, &net.OpError{Op: "read", Net: "ip4:icmp", Err: net.ErrClosed}
}

func (c *closedICMPConn) WriteTo([]byte, net.Addr) (int, error) {
	return 0, &net.OpError{Op: "write", Net: "ip4:icmp", Err: net.ErrClosed}
}

func (c *closedICMPConn) Close() error { return nil }

func (c *closedICMPConn) SetReadDeadline(time.Time) error {
	return &net.OpError{Op: "set", Net: "ip4:icmp", Err: net.ErrClosed}
}

// TestListen_ReturnsWhenSocketClosed is the regression test for the busy
// loop on a closed socket: Run closes the ICMP socket when it returns, and
// listen kept reading it, failing at once each time, until its context was
// cancelled. listen must return on the first read that reports the socket
// closed, with the context still live.
func TestListen_ReturnsWhenSocketClosed(t *testing.T) {
	tr := newListenTracer()
	conn := &closedICMPConn{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // only stops the goroutine if listen is still looping

	done := make(chan struct{})
	go func() {
		tr.listen(ctx, conn, make(chan Result, 1))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("listen still running 1s after its socket was closed (%d reads); context was not cancelled", conn.reads.Load())
	}
	if got := conn.reads.Load(); got != 1 {
		t.Errorf("listen read the closed socket %d times, want 1", got)
	}
}
