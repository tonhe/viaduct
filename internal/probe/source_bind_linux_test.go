package probe

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"
)

// The source address on the wire, on real sockets (root or CAP_NET_RAW).
//
// On Linux every address in 127.0.0.0/8 is local, so the host has a second
// address to send from. For a packet to 127.0.0.1 the kernel's own choice of
// source is 127.0.0.1. A trace whose source address is 127.0.0.2 must send
// every probe from 127.0.0.2: a witness socket at 127.0.0.1 reads each probe
// as it arrives and checks where it came from. The replies go to 127.0.0.2,
// where the bound sockets must still receive them.
func TestRun_EveryProbeLeavesFromTheSourceAddress(t *testing.T) {
	source := net.ParseIP("127.0.0.2")
	target := net.ParseIP("127.0.0.1")
	const rounds = 3

	for _, tc := range []struct {
		name       string
		proto      func(t *testing.T) ProbeProtocol
		witnessNet string
		want       ProbeType
		wantProbes int // primary probes on the wire: per round, one hop
	}{
		{"icmp", func(*testing.T) ProbeProtocol { return NewICMPProtocol(DefaultConfig()) }, "ip4:icmp", ProbeICMPTTL, rounds},
		{"udp", func(*testing.T) ProbeProtocol { return NewUDPProtocol(33434) }, "ip4:udp", ProbeUDPTTL, 2 * rounds},
		{"tcp", func(t *testing.T) ProbeProtocol { return NewTCPProtocol(closedLoopbackTCPPort(t, 4, "127.0.0.1")) }, "ip4:tcp", ProbeTCPTTL, 2 * rounds},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Bound to the target address, the witness is handed what is sent
			// to the target and none of the replies, which go to the source.
			witness, err := net.ListenPacket(tc.witnessNet, target.String())
			if err != nil {
				skipUnlessRequired(t, "raw %s socket not available (needs root or CAP_NET_RAW): %v", tc.witnessNet, err)
			}
			defer witness.Close()

			cfg := DefaultConfig()
			cfg.IPVersion = 4
			cfg.ICMPID = 0x5aa5
			cfg.Protocol = tc.proto(t)

			// The witness sees everything of its protocol sent to 127.0.0.1 by
			// anything on the host; the trace's probes are told apart by the
			// ICMP ID or the destination port.
			mine := func(pkt []byte) bool {
				if len(pkt) < 8 {
					return false
				}
				switch p := cfg.Protocol.(type) {
				case *ICMPProtocol:
					return pkt[0] == 8 && binary.BigEndian.Uint16(pkt[4:6]) == uint16(cfg.ICMPID)
				case *UDPProtocol:
					return int(binary.BigEndian.Uint16(pkt[2:4])) == p.BaseDstPort+1 // TTL 1
				case *TCPProtocol:
					return int(binary.BigEndian.Uint16(pkt[2:4])) == p.DstPort
				}
				return false
			}

			var mu sync.Mutex
			from := map[string]int{}
			witnessDone := make(chan struct{})
			go func() {
				defer close(witnessDone)
				buf := make([]byte, 2000)
				for {
					n, addr, err := witness.ReadFrom(buf)
					if err != nil {
						return
					}
					if !mine(buf[:n]) {
						continue
					}
					mu.Lock()
					from[addr.String()]++
					mu.Unlock()
				}
			}()

			cfg.SourceIP = source
			cfg.TargetIP = target
			cfg.MaxHops = 1
			cfg.NumPaths = 2
			cfg.MaxRounds = rounds
			cfg.ProbeDelay = time.Millisecond
			cfg.RoundDelay = 50 * time.Millisecond
			cfg.Timeout = 500 * time.Millisecond
			var sink logSink
			cfg.Debugf = sink.logf

			tr := NewTracer(target, cfg)
			results := make(chan Result, 64)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tr.Run(ctx, results); err != nil {
				skipUnlessRequired(t, "Run could not open its sockets: %v", err)
			}
			cancel()
			answered := 0
		drain:
			for {
				select {
				case r := <-results:
					if r.IsTarget && r.ProbeType == tc.want {
						answered++
					}
				default:
					break drain
				}
			}
			time.Sleep(100 * time.Millisecond)
			_ = witness.SetReadDeadline(time.Now())
			<-witnessDone

			mu.Lock()
			defer mu.Unlock()
			if from[source.String()] < tc.wantProbes {
				t.Errorf("%d %s packets arrived from %s, want at least %d; all arrivals by source: %v\ndebug log: %q",
					from[source.String()], tc.name, source, tc.wantProbes, from, sink.matching(""))
			}
			for addr, n := range from {
				if addr != source.String() {
					t.Errorf("%d %s packets arrived from %s; every probe of the trace must leave from %s", n, tc.name, addr, source)
				}
			}
			if answered != tc.wantProbes {
				t.Errorf("destination answered %d of %d %s probes: replies to %s must reach the bound sockets\ndebug log: %q",
					answered, tc.wantProbes, tc.name, source, sink.matching(""))
			}
		})
	}
}
