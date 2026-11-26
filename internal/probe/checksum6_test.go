package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// An IPv6 UDP or TCP probe used to carry a checksum computed over a source
// address chosen at startup, on a socket the kernel was free to send from any
// other address (viaduct#171). The raw send socket now asks the kernel to fill
// the checksum (IPV6_CHECKSUM), and the probe leaves the field zero.

// fakeRawConn is a net.PacketConn that records whether it was closed.
type fakeRawConn struct {
	net.PacketConn
	name   string
	closed bool
}

func (c *fakeRawConn) Close() error { c.closed = true; return nil }

// checksumCall is one request to set IPV6_CHECKSUM on a socket.
type checksumCall struct {
	conn   net.PacketConn
	offset int
}

// listenCall is one request to open a raw socket.
type listenCall struct{ network, address string }

// fakeSocketOps stands in for the socket calls of prepareSendSocket.
type fakeSocketOps struct {
	checksumErr error
	listenErr   error
	bound       *fakeRawConn

	checksums []checksumCall
	listens   []listenCall
}

func (f *fakeSocketOps) ops() sendSocketOps {
	return sendSocketOps{
		setChecksum: func(conn net.PacketConn, offset int) error {
			f.checksums = append(f.checksums, checksumCall{conn, offset})
			return f.checksumErr
		},
		listen: func(network, address string) (net.PacketConn, error) {
			f.listens = append(f.listens, listenCall{network, address})
			if f.listenErr != nil {
				return nil, f.listenErr
			}
			return f.bound, nil
		},
	}
}

// debugSink collects debug-log lines.
type debugSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *debugSink) logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, fmt.Sprintf(format, args...))
}

func (s *debugSink) joined() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.lines, "\n")
}

func TestV6ChecksumOffset(t *testing.T) {
	for _, tc := range []struct {
		network string
		offset  int
		ok      bool
	}{
		{"ip6:udp", 6, true},
		{"ip6:tcp", 16, true},
		{"ip4:udp", 0, false},
		{"ip4:tcp", 0, false},
		{"ip6:ipv6-icmp", 0, false},
		{"", 0, false},
	} {
		t.Run(tc.network, func(t *testing.T) {
			offset, ok := v6ChecksumOffset(tc.network)
			if offset != tc.offset || ok != tc.ok {
				t.Errorf("v6ChecksumOffset(%q) = %d, %v; want %d, %v", tc.network, offset, ok, tc.offset, tc.ok)
			}
		})
	}
}

// The socket-setup seam: an IPv6 raw send socket gets IPV6_CHECKSUM with the
// offset of its protocol's checksum field, and the probes are then built
// without one. An IPv4 socket is left alone.
func TestPrepareSendSocket_RequestsKernelChecksum(t *testing.T) {
	for _, tc := range []struct {
		network    string
		ipVersion  int
		wantOffset int
		wantKernel bool
	}{
		{"ip6:udp", 6, 6, true},
		{"ip6:tcp", 6, 16, true},
		{"ip4:udp", 4, 0, false},
		{"ip4:tcp", 4, 0, false},
	} {
		t.Run(tc.network, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.IPVersion = tc.ipVersion
			sink := &debugSink{}
			cfg.Debugf = sink.logf
			tr := NewTracer(net.ParseIP("2001:db8:100::1"), cfg)
			conn := &fakeRawConn{name: "unbound"}
			f := &fakeSocketOps{}

			got, kernel := tr.prepareSendSocket(f.ops(), tc.network, conn)

			if got != net.PacketConn(conn) {
				t.Errorf("returned a different socket; want the one passed in")
			}
			if conn.closed {
				t.Errorf("the send socket was closed")
			}
			if kernel != tc.wantKernel {
				t.Errorf("kernel checksum = %v, want %v", kernel, tc.wantKernel)
			}
			if len(f.listens) != 0 {
				t.Errorf("opened %d more socket(s); want none", len(f.listens))
			}
			if !tc.wantKernel {
				if len(f.checksums) != 0 {
					t.Errorf("IPV6_CHECKSUM requested %d time(s) on an IPv4 socket", len(f.checksums))
				}
				return
			}
			if len(f.checksums) != 1 {
				t.Fatalf("IPV6_CHECKSUM requested %d time(s), want 1", len(f.checksums))
			}
			if c := f.checksums[0]; c.conn != net.PacketConn(conn) || c.offset != tc.wantOffset {
				t.Errorf("IPV6_CHECKSUM requested with offset %d; want offset %d on the send socket", c.offset, tc.wantOffset)
			}
			if log := sink.joined(); !strings.Contains(log, fmt.Sprintf("offset %d", tc.wantOffset)) {
				t.Errorf("debug log does not record the option and its offset:\n%s", log)
			}
		})
	}
}

// When the platform rejects IPV6_CHECKSUM, the send socket is replaced by one
// bound to the source address the probes' own checksum is computed over, so
// the kernel cannot send from another. The probes keep their own checksum.
func TestPrepareSendSocket_FallsBackToBoundSocket(t *testing.T) {
	rejected := errors.New("protocol not available")
	source := net.ParseIP("2001:db8:0:1::1")

	for _, tc := range []struct {
		name       string
		network    string
		source     net.IP
		listenErr  error
		wantListen *listenCall
		wantBound  bool
		wantLog    string
	}{
		{
			name:       "udp bound to source",
			network:    "ip6:udp",
			source:     source,
			wantListen: &listenCall{"ip6:udp", "2001:db8:0:1::1"},
			wantBound:  true,
			wantLog:    "bound to 2001:db8:0:1::1",
		},
		{
			name:       "tcp bound to source",
			network:    "ip6:tcp",
			source:     source,
			wantListen: &listenCall{"ip6:tcp", "2001:db8:0:1::1"},
			wantBound:  true,
			wantLog:    "bound to 2001:db8:0:1::1",
		},
		{
			name:    "no source address to bind to",
			network: "ip6:udp",
			source:  nil,
			wantLog: "no source address",
		},
		{
			name:    "source address is not IPv6",
			network: "ip6:udp",
			source:  net.ParseIP("192.0.2.1"),
			wantLog: "no source address",
		},
		{
			name:       "bind fails",
			network:    "ip6:tcp",
			source:     source,
			listenErr:  errors.New("cannot assign requested address"),
			wantListen: &listenCall{"ip6:tcp", "2001:db8:0:1::1"},
			wantLog:    "cannot assign requested address",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.IPVersion = 6
			cfg.SourceIP = tc.source
			sink := &debugSink{}
			cfg.Debugf = sink.logf
			tr := NewTracer(net.ParseIP("2001:db8:100::1"), cfg)
			unbound := &fakeRawConn{name: "unbound"}
			bound := &fakeRawConn{name: "bound"}
			f := &fakeSocketOps{checksumErr: rejected, listenErr: tc.listenErr, bound: bound}

			got, kernel := tr.prepareSendSocket(f.ops(), tc.network, unbound)

			if kernel {
				t.Errorf("kernel checksum = true after the option was rejected; the probes would leave with a zero checksum")
			}
			if len(f.checksums) != 1 {
				t.Errorf("IPV6_CHECKSUM requested %d time(s), want 1", len(f.checksums))
			}
			if tc.wantListen == nil {
				if len(f.listens) != 0 {
					t.Errorf("opened a socket at %+v; want none", f.listens)
				}
			} else if len(f.listens) != 1 || f.listens[0] != *tc.wantListen {
				t.Errorf("sockets opened = %+v, want exactly %+v", f.listens, *tc.wantListen)
			}
			if tc.wantBound {
				if got != net.PacketConn(bound) {
					t.Errorf("send socket is not the bound one")
				}
				if !unbound.closed {
					t.Errorf("the unbound socket was left open")
				}
				if bound.closed {
					t.Errorf("the bound socket was closed")
				}
			} else {
				if got != net.PacketConn(unbound) {
					t.Errorf("send socket changed; want the original kept when it cannot be bound")
				}
				if unbound.closed {
					t.Errorf("the only send socket was closed")
				}
			}
			log := sink.joined()
			if !strings.Contains(log, "protocol not available") {
				t.Errorf("debug log does not carry the setsockopt error:\n%s", log)
			}
			if !strings.Contains(log, tc.wantLog) {
				t.Errorf("debug log does not contain %q:\n%s", tc.wantLog, log)
			}
		})
	}
}

// With the kernel filling the checksum, an IPv6 probe leaves the field zero:
// the kernel sums the pseudo-header of the source address it really uses.
// IPv4 has no such option and keeps its own checksum.
func TestBuildProbe_KernelChecksumLeavesFieldZero(t *testing.T) {
	for _, tc := range []struct {
		name      string
		proto     ProbeProtocol
		ipVersion int
		source    string
		target    string
		field     int // offset of the checksum field
		wantZero  bool
	}{
		{"udp v6", NewUDPProtocol(33434), 6, "2001:db8:0:1::1", "2001:db8:100::1", 6, true},
		{"tcp v6", NewTCPProtocol(443), 6, "2001:db8:0:1::1", "2001:db8:100::1", 16, true},
		{"udp v4", NewUDPProtocol(33434), 4, "192.0.2.1", "198.51.100.1", 6, false},
		{"tcp v4", NewTCPProtocol(443), 4, "192.0.2.1", "198.51.100.1", 16, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.IPVersion = tc.ipVersion
			cfg.SourceIP = net.ParseIP(tc.source)
			cfg.TargetIP = net.ParseIP(tc.target)
			cfg.kernelChecksum = true

			pkt, _, err := tc.proto.BuildProbe(2, 5, 77, cfg)
			if err != nil {
				t.Fatalf("BuildProbe: %v", err)
			}
			got := binary.BigEndian.Uint16(pkt[tc.field : tc.field+2])
			if tc.wantZero && got != 0 {
				t.Errorf("checksum field = %#04x, want 0 (the kernel fills it)", got)
			}
			if !tc.wantZero && got == 0 {
				t.Errorf("checksum field is zero; IPv4 probes carry their own checksum")
			}
		})
	}
}

// The option travels through the auto selector to the protocol it wraps.
func TestBuildProbe_KernelChecksumThroughAutoSelector(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 6
	cfg.SourceIP = net.ParseIP("2001:db8:0:1::1")
	cfg.TargetIP = net.ParseIP("2001:db8:100::1")
	cfg.kernelChecksum = true
	pkt, _, err := NewAutoSelector(NewUDPProtocol(33434), NewICMPProtocol(cfg)).BuildProbe(0, 1, 1, cfg)
	if err != nil {
		t.Fatalf("BuildProbe: %v", err)
	}
	if got := binary.BigEndian.Uint16(pkt[6:8]); got != 0 {
		t.Errorf("checksum field = %#04x, want 0", got)
	}
}

// closedTCPPort returns a loopback TCP port with nothing listening on it.
func closedTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		skipUnlessRequired(t, "no IPv6 loopback: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// Real sockets, where the process may open them (root or CAP_NET_RAW). The
// tracer probes the loopback address while its configured source address is
// one the kernel will never send from. A probe carrying a checksum computed
// over that address is discarded by the receiving stack; the destination
// answers (Port Unreachable for UDP, a reset for TCP) only when the checksum
// is valid for the address the kernel really used.
func TestRun_IPv6ProbeChecksumValidForKernelSource(t *testing.T) {
	loopback := net.ParseIP("::1")
	for _, tc := range []struct {
		name    string
		network string
		proto   func(t *testing.T) ProbeProtocol
		want    ProbeType
	}{
		{"udp", "ip6:udp", func(*testing.T) ProbeProtocol { return NewUDPProtocol(33434) }, ProbeUDPTTL},
		{"tcp", "ip6:tcp", func(t *testing.T) ProbeProtocol { return NewTCPProtocol(closedTCPPort(t)) }, ProbeTCPTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probeConn, err := net.ListenPacket(tc.network, "::1")
			if err != nil {
				skipUnlessRequired(t, "raw %s socket not available (needs root or CAP_NET_RAW): %v", tc.network, err)
			}
			offset, _ := v6ChecksumOffset(tc.network)
			err = setV6Checksum(probeConn, offset)
			probeConn.Close()
			if err != nil {
				skipUnlessRequired(t, "this platform rejects IPV6_CHECKSUM on %s (%v); the bound-socket fallback applies", tc.network, err)
			}

			cfg := DefaultConfig()
			cfg.IPVersion = 6
			cfg.Protocol = tc.proto(t)
			cfg.SourceIP = net.ParseIP("2001:db8:0:1::1") // not an address of this host
			cfg.TargetIP = loopback
			cfg.MaxHops = 1
			cfg.NumPaths = 2
			cfg.MaxRounds = 2
			cfg.ProbeDelay = time.Millisecond
			cfg.RoundDelay = 50 * time.Millisecond
			cfg.Timeout = 500 * time.Millisecond
			sink := &debugSink{}
			cfg.Debugf = sink.logf

			tr := NewTracer(loopback, cfg)
			results := make(chan Result, 64)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- tr.Run(ctx, results) }()
			if err := <-done; err != nil {
				skipUnlessRequired(t, "Run could not open its sockets: %v", err)
			}
			close(results)

			answered := 0
			for r := range results {
				if r.ProbeType == tc.want && r.IsTarget {
					answered++
				}
			}
			// 2 rounds of 2 flows at one hop.
			if answered != 4 {
				t.Errorf("destination answered %d of 4 %s probes; a probe with a bad checksum is not answered\ndebug log:\n%s",
					answered, tc.name, sink.joined())
			}
			if log := sink.joined(); !strings.Contains(log, fmt.Sprintf("offset %d", offset)) {
				t.Errorf("debug log does not record IPV6_CHECKSUM being set:\n%s", log)
			}
		})
	}
}
