//go:build darwin

package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// The one test that exercises the real macOS kernel (viaduct#172): a SYN sent
// on a raw TCP socket to a listener on this host is answered with a SYN-ACK,
// which the raw socket never sees and the BPF device on the loopback
// interface must deliver. Needs root (raw socket, /dev/bpf*); skipped
// without.
//
//	sudo go test ./internal/probe -run TestBPF -v -count=1
func TestBPF_SYNACKFromALocalListenerIsReceived(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: raw TCP socket and /dev/bpf*")
	}
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
				t.Fatalf("raw socket: %v", err)
			}

			var mu sync.Mutex
			var log []string
			target := net.ParseIP(fam.addr)
			cfg := DefaultConfig()
			cfg.IPVersion = fam.version
			cfg.SourceIP = target
			cfg.TargetIP = target
			cfg.Protocol = NewTCPProtocol(port)
			cfg.BasePort = 44900
			cfg.NumPaths = 2
			cfg.Debugf = func(format string, args ...any) {
				mu.Lock()
				defer mu.Unlock()
				log = append(log, fmt.Sprintf(format, args...))
			}
			tr := NewTracer(target, cfg)
			rawConn, cfg.kernelChecksum = tr.prepareSendSocket(realSendSocketOps, rawNet, rawConn)
			defer rawConn.Close()

			src, cleanup, err := tr.tcpSource(rawConn)
			if err != nil {
				t.Fatalf("BPF setup: %v", err)
			}
			dumpLog := func() string {
				mu.Lock()
				defer mu.Unlock()
				return strings.Join(log, "\n")
			}

			results := make(chan Result, 8)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			listenerDone := make(chan struct{})
			go func() {
				tr.listenTCP(ctx, src, tr.pm, results)
				close(listenerDone)
			}()

			// Two flows, as a trace sends them.
			for flow := 0; flow < 2; flow++ {
				pkt, key, err := cfg.Protocol.BuildProbe(flow, 64, 0x1000+flow, cfg)
				if err != nil {
					t.Fatal(err)
				}
				tr.pm.add(key, 9, flow, time.Now(), ProbeTCPTTL)
				if _, err := rawConn.WriteTo(pkt, &net.IPAddr{IP: target}); err != nil {
					t.Fatalf("send SYN: %v", err)
				}
			}

			seen := map[int]bool{}
			deadline := time.After(3 * time.Second)
			for len(seen) < 2 {
				select {
				case res := <-results:
					if !res.IsTarget || res.TTL != 9 || !res.IP.Equal(target) {
						t.Errorf("result = %+v, want the target at TTL 9", res)
					}
					seen[res.FlowID] = true
				case <-deadline:
					cleanup()
					t.Fatalf("%d of 2 SYN-ACKs matched; counters: %d segments read, %d from the target, %d SYN-ACK/RST\n%s",
						len(seen), tr.tcpRecv.segments.Load(), tr.tcpRecv.fromTarget.Load(), tr.tcpRecv.replies.Load(), dumpLog())
				}
			}

			// Closing the device ends the listener, with the context live.
			cleanup()
			select {
			case <-listenerDone:
			case <-time.After(2 * time.Second):
				t.Error("the listener did not return after the BPF device was closed")
			}
			t.Logf("debug log:\n%s", dumpLog())
			if !strings.Contains(dumpLog(), "BPF device /dev/bpf") {
				t.Error("the debug log does not name the BPF device")
			}
		})
	}
}

// Without root the device cannot be opened, and the error says so and names
// the modes that work. Skipped as root, and where /dev/bpf0 is readable.
func TestBPF_SetupFailureNamesTheReason(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root the device opens")
	}
	target := net.ParseIP("127.0.0.1")
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.SourceIP = target
	cfg.TargetIP = target
	cfg.Protocol = NewTCPProtocol(443)
	tr := NewTracer(target, cfg)
	src, cleanup, err := tr.tcpSource(nil)
	if err == nil {
		cleanup()
		t.Skipf("/dev/bpf* can be opened without root here (source %T)", src)
	}
	var capErr *tcpCaptureError
	if !errors.As(err, &capErr) {
		t.Fatalf("err = %v (%T), want a tcpCaptureError", err, err)
	}
	t.Logf("setup error: %v", err)
	for _, want := range []string{"-P udp", "-P icmp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// A source address no interface has is a setup failure of its own.
func TestBPF_NoInterfaceForTheSourceAddress(t *testing.T) {
	target := net.ParseIP("192.0.2.1")
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.SourceIP = net.ParseIP("192.0.2.200")
	cfg.TargetIP = target
	cfg.Protocol = NewTCPProtocol(443)
	_, _, err := NewTracer(target, cfg).tcpSource(nil)
	if err == nil || !strings.Contains(err.Error(), "no interface has the source address 192.0.2.200") {
		t.Fatalf("err = %v, want the missing-interface reason", err)
	}
}
