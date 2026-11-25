package probe

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// A TCP probe is identified by its sequence number (viaduct#70). The key used
// to be source port and destination port only, which are the same for every
// TTL of a flow: each probe overwrote the record of the one before it, and a
// reply slower than the 10 ms gap between probes was credited to the next
// hop, with an RTT measured from the wrong probe.

func tcpTestConfig(v int) Config {
	cfg := DefaultConfig()
	cfg.IPVersion = v
	cfg.BasePort = 44000
	if v == 6 {
		cfg.SourceIP = net.ParseIP("2001:db8:0:1::1")
		cfg.TargetIP = net.ParseIP("2001:db8:100::1")
	} else {
		cfg.SourceIP = net.ParseIP("192.0.2.1")
		cfg.TargetIP = net.ParseIP("198.51.100.1")
	}
	return cfg
}

// quoteOf builds what an ICMP error quotes of a TCP probe: the IP header and
// the first 8 bytes of the TCP header (RFC 792 minimum).
func quoteOf(v int, cfg Config, tcpProbe []byte) []byte {
	if v == 6 {
		return append(buildIPv6HeaderTest(6, cfg.SourceIP, cfg.TargetIP), tcpProbe[:8]...)
	}
	return append(buildIPv4Header(6, cfg.SourceIP, cfg.TargetIP), tcpProbe[:8]...)
}

func TestTCPProbeKeysDifferPerProbe(t *testing.T) {
	cfg := tcpTestConfig(4)
	p := NewTCPProtocol(443)
	_, k5, _ := p.BuildProbe(0, 5, 101, cfg)
	_, k6, _ := p.BuildProbe(0, 6, 102, cfg)
	if k5 == k6 {
		t.Fatalf("the same flow at TTL 5 and TTL 6 has one key %+v: the later probe overwrites the earlier one", k5)
	}
	_, other, _ := p.BuildProbe(1, 5, 101, cfg)
	if other == k5 {
		t.Errorf("two flows at the same TTL share a key %+v", k5)
	}
}

func TestTCPIdentifyResponse_UsesQuotedSequenceNumber(t *testing.T) {
	for _, v := range []int{4, 6} {
		cfg := tcpTestConfig(v)
		p := NewTCPProtocol(443)
		pkt5, k5, _ := p.BuildProbe(2, 5, 101, cfg)
		pkt6, k6, _ := p.BuildProbe(2, 6, 102, cfg)

		got5, err := p.IdentifyResponse(quoteOf(v, cfg, pkt5))
		if err != nil || got5 == nil || *got5 != k5 {
			t.Errorf("IPv%d: quote of the TTL 5 probe identified as %+v (err %v), want %+v", v, got5, err, k5)
		}
		got6, err := p.IdentifyResponse(quoteOf(v, cfg, pkt6))
		if err != nil || got6 == nil || *got6 != k6 {
			t.Errorf("IPv%d: quote of the TTL 6 probe identified as %+v (err %v), want %+v", v, got6, err, k6)
		}
	}
}

func TestTCPIdentifyResponse_QuoteWithoutSequenceNumberIsNotIdentified(t *testing.T) {
	cfg := tcpTestConfig(4)
	p := NewTCPProtocol(443)
	pkt, _, _ := p.BuildProbe(0, 5, 101, cfg)
	// Only the ports: 4 bytes of TCP header. Not enough to tell probes apart.
	short := append(buildIPv4Header(6, cfg.SourceIP, cfg.TargetIP), pkt[:4]...)
	if got, _ := p.IdentifyResponse(short); got != nil {
		t.Errorf("a quote without the sequence number was identified as %+v", got)
	}
}

// The live failure: the reply to the TTL 5 probe arrives after the TTL 6
// probe for the same flow has gone out. It must be credited to TTL 5, with
// TTL 5's send time, and the TTL 6 probe must still be matchable.
func TestListen_TCPReplySlowerThanTheProbeGap(t *testing.T) {
	cfg := tcpTestConfig(4)
	cfg.Protocol = NewTCPProtocol(443)
	cfg.RoundDelay = 10 * time.Millisecond
	tr := NewTracer(cfg.TargetIP, cfg)
	p := cfg.Protocol.(*TCPProtocol)

	pkt5, k5, _ := p.BuildProbe(0, 5, 101, cfg)
	pkt6, k6, _ := p.BuildProbe(0, 6, 102, cfg)
	tr.pm.add(k5, 5, 0, time.Now().Add(-25*time.Millisecond), ProbeTCPTTL)
	tr.pm.add(k6, 6, 0, time.Now(), ProbeTCPTTL)

	conn := newFakeConn()
	conn.push(marshalTimeExceeded(quoteOf(4, cfg, pkt5)), fakeAddr{"192.0.2.55"})
	conn.push(marshalTimeExceeded(quoteOf(4, cfg, pkt6)), fakeAddr{"192.0.2.66"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan Result, 2)
	go tr.listen(ctx, conn, results)

	got := map[int]Result{}
	for len(got) < 2 {
		select {
		case r := <-results:
			got[r.TTL] = r
		case <-time.After(time.Second):
			t.Fatalf("got results for TTLs %v, want 5 and 6", got)
		}
	}
	if r := got[5]; r.IP.String() != "192.0.2.55" {
		t.Errorf("TTL 5 credited to %v, want 192.0.2.55", r.IP)
	}
	if r := got[5]; r.RTT < 25*time.Millisecond {
		t.Errorf("TTL 5 RTT = %v, want at least 25ms (measured from its own probe)", r.RTT)
	}
	if r := got[6]; r.IP.String() != "192.0.2.66" {
		t.Errorf("TTL 6 credited to %v, want 192.0.2.66", r.IP)
	}
}

// tcpReply builds a TCP segment from the target answering a probe with the
// given sequence number.
func tcpReply(ourSrcPort, theirPort int, flags byte, ack uint32) []byte {
	seg := make([]byte, 20)
	binary.BigEndian.PutUint16(seg[0:2], uint16(theirPort))
	binary.BigEndian.PutUint16(seg[2:4], uint16(ourSrcPort))
	binary.BigEndian.PutUint32(seg[8:12], ack)
	seg[12] = 5 << 4
	seg[13] = flags
	return seg
}

func TestParseTCPResponse_MatchesByAcknowledgement(t *testing.T) {
	cfg := tcpTestConfig(4)
	p := NewTCPProtocol(443)
	_, k5, _ := p.BuildProbe(0, 5, 101, cfg)

	for _, tc := range []struct {
		name  string
		flags byte
	}{
		{"SYN-ACK", 0x12},
		{"RST-ACK", 0x14},
	} {
		// The reply acknowledges our sequence number plus one.
		key, ok := parseTCPResponse(tcpReply(cfg.BasePort, 443, tc.flags, 102))
		if !ok || key == nil || *key != k5 {
			t.Errorf("%s acknowledging 102: key %+v ok %v, want %+v", tc.name, key, ok, k5)
		}
	}

	// A reply for a different probe of the same flow must not match this one.
	key, ok := parseTCPResponse(tcpReply(cfg.BasePort, 443, 0x12, 103))
	if ok && key != nil && *key == k5 {
		t.Errorf("a SYN-ACK acknowledging 103 matched the probe with sequence 101")
	}
}
