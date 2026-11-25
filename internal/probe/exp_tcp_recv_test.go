package probe

// EXPERIMENT ONLY, not for merge (viaduct#172).

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

// expAddrConn is a packet connection whose reads come from a given sender.
type expAddrConn struct {
	mockPacketConn
	from net.Addr
}

func (c *expAddrConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, _, err := c.mockPacketConn.ReadFrom(p)
	if err != nil {
		return n, nil, err
	}
	return n, c.from, nil
}

func TestExpTCPRecv_CountsAndLines(t *testing.T) {
	target := net.ParseIP("192.0.2.1")

	matched := buildFakeTCPPacket(443, 44000, 0x12)
	binary.BigEndian.PutUint32(matched[8:12], 102)
	unmatched := buildFakeTCPPacket(443, 44000, 0x14)
	binary.BigEndian.PutUint32(unmatched[8:12], 999)
	otherFlags := buildFakeTCPPacket(443, 44000, 0x10)

	var mu sync.Mutex
	var lines []string
	cfg := DefaultConfig()
	cfg.Debugf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	tr := NewTracer(target, cfg)

	pm := newProbeMap()
	pm.add(probeKey{SrcPort: 44000, DstPort: 443, Seq: 101}, 7, 0, time.Now(), ProbeTCPTTL)
	pm.add(probeKey{SrcPort: 44000, DstPort: 443, Seq: 108}, 8, 0, time.Now(), ProbeTCPTTL)

	conn := &expAddrConn{
		mockPacketConn: mockPacketConn{reads: []mockRead{
			{data: matched}, {data: unmatched}, {data: otherFlags}, {data: []byte{1, 2, 3}},
			{err: errors.New("boom")},
		}},
		from: &net.IPAddr{IP: target},
	}
	tr.expTCPSocket("ip4:tcp", conn)

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan Result, 4)
	done := make(chan struct{})
	go func() {
		tr.listenTCP(ctx, conn, pm, results)
		close(done)
	}()
	select {
	case <-results:
	case <-time.After(2 * time.Second):
		t.Fatal("no result for the matching SYN-ACK")
	}
	deadline := time.Now().Add(2 * time.Second)
	for tr.expTCP.readErrs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	tr.expTCPSummary("exit")

	s := &tr.expTCP
	got := []int64{s.reads.Load(), s.fromTarget.Load(), s.fromOther.Load(), s.tcpTarget.Load(),
		s.parsed.Load(), s.rejected.Load(), s.matched.Load(), s.unmatched.Load(), s.readErrs.Load()}
	want := []int64{4, 4, 0, 3, 2, 2, 1, 1, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("counters = %v, want %v", got, want)
		}
	}

	mu.Lock()
	all := strings.Join(lines, "\n")
	mu.Unlock()
	for _, wantLine := range []string{
		"raw socket opened: network ip4:tcp",
		"UNMATCHED from 192.0.2.1",
		"flags=RST|ACK",
		"looked up key {SrcPort:44000 DstPort:443 Seq:998}",
		"1 with this port pair, seq 108..108",
		"not a SYN-ACK or RST+ACK",
		"shorter than a TCP header",
		"read error: boom",
		"exit totals: read 4 (errors 1), from target 4, from others 0, TCP segments from target 3, SYN-ACK/RST parsed 2, other flags 2, matched 1, unmatched 1",
	} {
		if !strings.Contains(all, wantLine) {
			t.Errorf("debug log lacks %q:\n%s", wantLine, all)
		}
	}
}

func TestExpTCPRecv_Helpers(t *testing.T) {
	if got := expTCPFlags(0); got != "none" {
		t.Errorf("expTCPFlags(0) = %q", got)
	}
	if got := expTCPFlags(0x12); got != "SYN|ACK" {
		t.Errorf("expTCPFlags(0x12) = %q", got)
	}
	pm := newProbeMap()
	if got := pm.expPending(probeKey{SrcPort: 1, DstPort: 2}); !strings.Contains(got, "none with this port pair") {
		t.Errorf("expPending on an empty map = %q", got)
	}
	// A sender that is not the target, and no debug log: counted, not written.
	tr := NewTracer(net.ParseIP("192.0.2.1"), DefaultConfig())
	tr.expTCPRead(buildFakeTCPPacket(443, 44000, 0x12), &net.IPAddr{IP: net.ParseIP("198.51.100.7")})
	tr.expTCPRead(nil, nil)
	if tr.expTCP.fromOther.Load() != 2 || tr.expTCP.fromTarget.Load() != 0 {
		t.Errorf("fromOther = %d, fromTarget = %d, want 2 and 0", tr.expTCP.fromOther.Load(), tr.expTCP.fromTarget.Load())
	}
}
