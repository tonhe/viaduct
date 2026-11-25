package probe

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"testing"
	"time"
)

// The Linux half of the receive path on real sockets: a SYN sent on a raw TCP
// socket to a local listener is answered with a SYN-ACK, the kernel delivers
// that to the same raw socket, and the listener matches it; in IPv6 with
// IPV6_CHECKSUM on the socket, which also makes the kernel verify what the
// socket receives. Needs CAP_NET_RAW; skipped without it.
func TestListenTCP_RawSocketDeliversTheSYNACK(t *testing.T) {
	for _, fam := range []struct {
		name    string
		version int
		addr    string
		network string
	}{
		{"IPv4", 4, "127.0.0.1", "tcp4"},
		{"IPv6", 6, "::1", "tcp6"},
	} {
		t.Run(fam.name, func(t *testing.T) {
			ln, err := net.Listen(fam.network, net.JoinHostPort(fam.addr, "0"))
			if err != nil {
				t.Skipf("no %s loopback listener: %v", fam.name, err)
			}
			defer ln.Close()
			port := ln.Addr().(*net.TCPAddr).Port

			rawNet := TransportListenerNet(fam.version, "tcp")
			rawConn, err := net.ListenPacket(rawNet, RawListenAddr(fam.version))
			if err != nil {
				if os.Geteuid() != 0 {
					t.Skipf("raw socket needs CAP_NET_RAW: %v", err)
				}
				t.Fatalf("raw socket: %v", err)
			}

			target := net.ParseIP(fam.addr)
			cfg := DefaultConfig()
			cfg.IPVersion = fam.version
			cfg.SourceIP = target
			cfg.TargetIP = target
			cfg.Protocol = NewTCPProtocol(port)
			cfg.BasePort = 44900
			tr := NewTracer(target, cfg)
			rawConn, cfg.kernelChecksum = tr.prepareSendSocket(realSendSocketOps, rawNet, rawConn)
			defer rawConn.Close()

			src, cleanup, err := tr.tcpSource(rawConn)
			if err != nil {
				t.Fatalf("tcpSource: %v", err)
			}
			defer cleanup()

			results := make(chan Result, 4)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go tr.listenTCP(ctx, src, tr.pm, results)

			const seq = 0x1234
			pkt, key, err := cfg.Protocol.BuildProbe(0, 64, seq, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got := binary.BigEndian.Uint32(pkt[4:8]); got != seq {
				t.Fatalf("probe sequence = %#x", got)
			}
			tr.pm.add(key, 9, 0, time.Now(), ProbeTCPTTL)
			if _, err := rawConn.WriteTo(pkt, &net.IPAddr{IP: target}); err != nil {
				t.Fatalf("send SYN: %v", err)
			}

			select {
			case res := <-results:
				if !res.IsTarget || res.TTL != 9 || !res.IP.Equal(target) {
					t.Errorf("result = %+v, want the target at TTL 9", res)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("the listener's SYN-ACK was not matched; counters: %d segments read, %d from the target, %d SYN-ACK/RST",
					tr.tcpRecv.segments.Load(), tr.tcpRecv.fromTarget.Load(), tr.tcpRecv.replies.Load())
			}
		})
	}
}
