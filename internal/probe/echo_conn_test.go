package probe

import (
	"errors"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/icmp"
)

// The echo tier's socket is written to and never read (viaduct#115). What it
// is spared depends on the platform: an ICMP filter that blocks every type
// where there is one, the smallest receive buffer where there is not.

// bufConn is a net.PacketConn that records SetReadBuffer calls.
type bufConn struct {
	net.PacketConn // nil: only SetReadBuffer is called
	sizes          []int
	err            error
}

func (c *bufConn) SetReadBuffer(bytes int) error {
	c.sizes = append(c.sizes, bytes)
	return c.err
}

// plainConn is a net.PacketConn whose receive buffer cannot be set.
type plainConn struct{ net.PacketConn }

// The rule, without a socket: a filter that is set is all there is to do; a
// filter that cannot be set means the smallest receive buffer; and only when
// neither can be done is there a line in the debug log.
func TestEchoConn_QuietFilterOrSmallReceiveBuffer(t *testing.T) {
	noFilter := errors.New("not implemented on this platform")
	for _, tc := range []struct {
		name        string
		filterErr   error
		bufErr      error
		noBuf       bool // the socket has no SetReadBuffer
		wantFilter  bool
		wantSmall   bool
		wantSizes   int
		wantLogged  []string
		wantNoLines bool
	}{
		{name: "filter set", wantFilter: true, wantNoLines: true},
		{name: "no filter, buffer shrunk", filterErr: noFilter, wantSmall: true, wantSizes: 1, wantNoLines: true},
		{name: "no filter, buffer refused", filterErr: noFilter, bufErr: errors.New("setsockopt: invalid argument"),
			wantSizes: 1, wantLogged: []string{"echo socket:", "not implemented on this platform", "invalid argument", "used as it is"}},
		{name: "no filter, no buffer control", filterErr: noFilter, noBuf: true,
			wantLogged: []string{"echo socket:", "not implemented on this platform", "used as it is"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sink logSink
			bc := &bufConn{err: tc.bufErr}
			conn := &echoConn{PacketConn: bc}
			if tc.noBuf {
				conn.PacketConn = plainConn{}
			}
			conn.quiet(func() error { return tc.filterErr }, sink.logf)

			if conn.filtered != tc.wantFilter || conn.smallRecvBuf != tc.wantSmall {
				t.Errorf("filtered=%v smallRecvBuf=%v, want %v and %v", conn.filtered, conn.smallRecvBuf, tc.wantFilter, tc.wantSmall)
			}
			if len(bc.sizes) != tc.wantSizes {
				t.Errorf("receive buffer set %d times (%v), want %d", len(bc.sizes), bc.sizes, tc.wantSizes)
			}
			for _, size := range bc.sizes {
				if size != 1 {
					t.Errorf("receive buffer set to %d bytes, want 1: the kernel rounds up to its minimum", size)
				}
			}
			lines := sink.matching("")
			if tc.wantNoLines {
				if len(lines) != 0 {
					t.Errorf("debug log = %q, want nothing", lines)
				}
				return
			}
			if len(lines) != 1 {
				t.Fatalf("%d debug-log lines, want 1: %q", len(lines), lines)
			}
			for _, want := range tc.wantLogged {
				if !strings.Contains(lines[0], want) {
					t.Errorf("line %q does not contain %q", lines[0], want)
				}
			}
		})
	}
}

// echoFilterExpected reports whether this platform has an ICMP type filter
// for a raw socket of the family: ICMPv6 everywhere but Windows, ICMPv4 only
// on Linux.
func echoFilterExpected(ipVersion int) bool {
	if ipVersion == 6 {
		return runtime.GOOS != "windows"
	}
	return runtime.GOOS == "linux"
}

var echoConnFamilies = []struct {
	name     string
	v        int
	loopback string
}{
	{"IPv4", 4, "127.0.0.1"},
	{"IPv6", 6, "::1"},
}

// maxHeldUnfiltered is the most packets an unread echo socket with no filter
// may be holding after a burst far larger than that. A default receive buffer
// holds several times as many.
const maxHeldUnfiltered = 16

// loopbackBurst sends n echo requests with a 1000-byte payload to the loopback
// address from w. Each is answered by the host; requests and replies are
// handed to every raw ICMP socket of the family that takes them.
func loopbackBurst(t *testing.T, w *icmp.PacketConn, ipVersion int, lo net.IP, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		pkt, err := buildICMPEchoRequest(ipVersion, 0x4242, i+1, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.WriteTo(pkt, &net.IPAddr{IP: lo}); err != nil {
			skipUnlessRequired(t, "echo %d to %s could not be sent on this host: %v", i+1, lo, err)
		}
	}
	// Something did come back.
	buf := make([]byte, 2000)
	_ = w.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := w.ReadFrom(buf); err != nil {
		skipUnlessRequired(t, "no ICMP came back from %s: %v", lo, err)
	}
	// Let the last of the burst be delivered.
	time.Sleep(100 * time.Millisecond)
}

// held reads everything waiting on the echo socket and returns the count.
func held(conn *echoConn) int {
	buf := make([]byte, 2000)
	n := 0
	for ; n < 10000; n++ {
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, _, err := conn.ReadFrom(buf); err != nil {
			break
		}
	}
	return n
}

// The real socket, where the test may open one (root, CAP_NET_RAW, or an
// administrator on Windows). Where the platform has a filter the socket is
// delivered nothing. Where it has none, the socket has the small receive
// buffer and holds next to nothing of a burst.
func TestOpenEchoConn_TakesAsLittleAsThePlatformAllows(t *testing.T) {
	const burst = 200
	for _, fam := range echoConnFamilies {
		t.Run(fam.name, func(t *testing.T) {
			lo := net.ParseIP(fam.loopback)
			witness, err := icmp.ListenPacket(ListenerNet(fam.v), RawListenAddr(fam.v))
			if err != nil {
				skipUnlessRequired(t, "no raw ICMP socket here: %v", err)
			}
			defer witness.Close()

			var sink logSink
			conn, err := openEchoConn(fam.v, sink.logf)
			if err != nil {
				t.Fatalf("openEchoConn: %v", err)
			}
			defer conn.Close()

			// Exactly one of the two treatments, and the one this platform has.
			if want := echoFilterExpected(fam.v); conn.filtered != want || conn.smallRecvBuf == want {
				t.Fatalf("filtered=%v smallRecvBuf=%v on %s, want filtered=%v smallRecvBuf=%v; debug log %q",
					conn.filtered, conn.smallRecvBuf, runtime.GOOS, want, !want, sink.matching(""))
			}
			if lines := sink.matching(""); len(lines) != 0 {
				t.Errorf("debug log = %q, want nothing: the socket got its treatment", lines)
			}

			// It sends.
			cfg := DefaultConfig()
			cfg.IPVersion = fam.v
			tr := NewTracer(lo, cfg)
			pkt, err := buildICMPEchoRequest(fam.v, 0x4242, 7, 16)
			if err != nil {
				t.Fatal(err)
			}
			snd := &icmpSender{conn: conn, ipVersion: fam.v}
			if !tr.send(snd, sockEcho, ProbeICMPEcho, echoFullTTL, pkt, &net.IPAddr{IP: lo}) {
				skipUnlessRequired(t, "the echo to %s could not be sent on this host", lo)
			}

			loopbackBurst(t, witness, fam.v, lo, burst)
			n := held(conn)
			switch {
			case conn.filtered:
				if n != 0 {
					t.Errorf("the filtered echo socket was delivered %d packets, want none: it is never read", n)
				}
			case runtime.GOOS == "windows":
				// What Windows does with a one-byte receive buffer is recorded,
				// not asserted: the treatment was applied (checked above).
				t.Logf("the unfiltered echo socket held %d packets of a burst of %d echoes", n, burst)
			case n > maxHeldUnfiltered:
				t.Errorf("the unfiltered echo socket held %d packets of a burst of %d echoes, want at most %d: its receive buffer is the kernel's minimum",
					n, burst, maxHeldUnfiltered)
			}
		})
	}
}

// The small receive buffer on a real socket, on any platform: with the filter
// taken away, the socket holds next to nothing of a burst.
func TestEchoConn_SmallReceiveBufferHoldsNextToNothing(t *testing.T) {
	const burst = 200
	for _, fam := range echoConnFamilies {
		t.Run(fam.name, func(t *testing.T) {
			lo := net.ParseIP(fam.loopback)
			witness, err := icmp.ListenPacket(ListenerNet(fam.v), RawListenAddr(fam.v))
			if err != nil {
				skipUnlessRequired(t, "no raw ICMP socket here: %v", err)
			}
			defer witness.Close()
			pc, err := net.ListenPacket(ListenerNet(fam.v), RawListenAddr(fam.v))
			if err != nil {
				t.Fatalf("second raw ICMP socket: %v", err)
			}
			defer pc.Close()

			var sink logSink
			conn := &echoConn{PacketConn: pc}
			conn.quiet(func() error { return errors.New("no filter in this test") }, sink.logf)
			if conn.filtered || !conn.smallRecvBuf {
				t.Fatalf("filtered=%v smallRecvBuf=%v, want the small receive buffer; debug log %q",
					conn.filtered, conn.smallRecvBuf, sink.matching(""))
			}

			loopbackBurst(t, witness, fam.v, lo, burst)
			n := held(conn)
			if runtime.GOOS == "windows" {
				t.Logf("the unfiltered echo socket held %d packets of a burst of %d echoes", n, burst)
				return
			}
			if n > maxHeldUnfiltered {
				t.Errorf("the unfiltered echo socket held %d packets of a burst of %d echoes, want at most %d",
					n, burst, maxHeldUnfiltered)
			}
		})
	}
}
