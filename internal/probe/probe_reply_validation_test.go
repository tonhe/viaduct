package probe

import (
	"context"
	"net"
	"testing"
	"time"
)

// Reply validation (viaduct#50). The listener must not accept replies that
// only happen to carry matching numbers:
//   - an Echo Reply counts only if it comes from the address the probe was
//     sent to
//   - an ICMP error whose quoted packet cannot be parsed is dropped
//   - the echo tier pings at most 8 addresses per TTL
// Genuine replies, including the shortest quote RFC 792 allows (IP header plus
// 8 bytes of the original datagram), must keep working.

// ---------------------------------------------------------------------------
// Characterization: genuine replies. These pass before and after the change.
// ---------------------------------------------------------------------------

func TestEmbeddedDestMatches_GenuineMinimalQuotes(t *testing.T) {
	src4, dst4 := net.ParseIP("192.0.2.1"), net.ParseIP("198.51.100.1")
	src6, dst6 := net.ParseIP("2001:db8:0:1::1"), net.ParseIP("2001:db8:100::1")

	udp4 := buildUDPInnerHeader(src4, dst4, 44000, 33437)
	icmp4 := buildICMPInnerHeader(src4, dst4, 0x1234, 7)
	udp6 := buildUDPInnerHeaderV6(src6, dst6, 44000, 33437)
	if len(udp4) != 28 || len(icmp4) != 28 {
		t.Fatalf("IPv4 fixtures are %d and %d bytes, want the 28-byte RFC 792 minimum", len(udp4), len(icmp4))
	}
	if len(udp6) != 48 {
		t.Fatalf("IPv6 fixture is %d bytes, want the 48-byte minimum (40 + 8)", len(udp6))
	}

	for _, tc := range []struct {
		name   string
		target net.IP
		inner  []byte
	}{
		{"IPv4 UDP, 28 bytes", dst4, udp4},
		{"IPv4 ICMP, 28 bytes", dst4, icmp4},
		{"IPv6 UDP, 48 bytes", dst6, udp6},
	} {
		if !embeddedDestMatches(tc.target, tc.inner) {
			t.Errorf("%s: genuine minimal quote rejected", tc.name)
		}
	}
	if embeddedDestMatches(net.ParseIP("203.0.113.9"), udp4) {
		t.Error("quote for a different destination accepted")
	}
}

func TestListen_GenuineEchoReplies(t *testing.T) {
	target := net.ParseIP("192.0.2.1")
	hop := net.ParseIP("192.0.2.10")

	t.Run("ICMP TTL probe answered by the target", func(t *testing.T) {
		tr := newListenTracer()
		id := tr.cfg.Protocol.(*ICMPProtocol).sessionID
		tr.pm.addTo(probeKey{SrcPort: id, DstPort: 5}, 4, 0, time.Now(), ProbeICMPTTL, target)
		r := listenOne(t, tr, marshalEchoReply(id, 5), target.String())
		if r == nil || !r.IsTarget || r.TTL != 4 || r.ProbeType != ProbeICMPTTL {
			t.Fatalf("result = %+v, want target reached at TTL 4", r)
		}
	})

	t.Run("echo-tier probe answered by the hop it was sent to", func(t *testing.T) {
		tr := newListenTracer()
		tr.cfg.EchoID = 0xE00E
		tr.pm.addTo(probeKey{SrcPort: tr.cfg.EchoID, DstPort: 9}, 3, 0, time.Now(), ProbeICMPEcho, hop)
		r := listenOne(t, tr, marshalEchoReply(tr.cfg.EchoID, 9), hop.String())
		if r == nil || r.TTL != 3 || r.ProbeType != ProbeICMPEcho || !r.IP.Equal(hop) {
			t.Fatalf("result = %+v, want an echo reply from %s at TTL 3", r, hop)
		}
	})
}

// listenOne feeds one packet from the given sender to the listener and returns
// the result it produced, or nil if it produced none.
func listenOne(t *testing.T, tr *Tracer, pkt []byte, from string) *Result {
	t.Helper()
	conn := newFakeConn()
	conn.push(pkt, fakeAddr{from})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan Result, 1)
	go tr.listen(ctx, conn, results)
	select {
	case r := <-results:
		return &r
	case <-time.After(150 * time.Millisecond):
		return nil
	}
}

// ---------------------------------------------------------------------------
// New behaviour
// ---------------------------------------------------------------------------

func TestEmbeddedDestMatches_UnparseableIsRejected(t *testing.T) {
	target := net.ParseIP("198.51.100.1")
	good := buildUDPInnerHeader(net.ParseIP("192.0.2.1"), target, 44000, 33437)

	badVersion := append([]byte(nil), good...)
	badVersion[0] = 0x75 // version 7

	for _, tc := range []struct {
		name  string
		inner []byte
	}{
		{"empty", nil},
		{"unknown IP version", badVersion},
		{"IPv4 header cut short", good[:12]},
		{"IPv6 header cut short", buildUDPInnerHeaderV6(net.ParseIP("2001:db8::1"), net.ParseIP("2001:db8::2"), 1, 2)[:20]},
	} {
		if embeddedDestMatches(target, tc.inner) {
			t.Errorf("%s: unparseable quote accepted", tc.name)
		}
	}
	// With no target configured there is nothing to compare against.
	if !embeddedDestMatches(nil, good) {
		t.Error("nil target: a parseable quote must not be dropped")
	}
}

func TestListen_EchoReplyFromWrongSenderIsDropped(t *testing.T) {
	target := net.ParseIP("192.0.2.1")
	hop := net.ParseIP("192.0.2.10")
	stranger := "203.0.113.99"

	t.Run("ICMP TTL tier", func(t *testing.T) {
		tr := newListenTracer()
		id := tr.cfg.Protocol.(*ICMPProtocol).sessionID
		key := probeKey{SrcPort: id, DstPort: 5}
		tr.pm.addTo(key, 4, 0, time.Now(), ProbeICMPTTL, target)

		if r := listenOne(t, tr, marshalEchoReply(id, 5), stranger); r != nil {
			t.Fatalf("Echo Reply from %s accepted for a probe sent to %s: %+v", stranger, target, r)
		}
		if got := tr.targetTTL; got != 0 {
			t.Errorf("targetTTL = %d after a forged reply, want 0", got)
		}
		// The forged reply must not consume the record: the genuine one still counts.
		r := listenOne(t, tr, marshalEchoReply(id, 5), target.String())
		if r == nil || !r.IsTarget {
			t.Fatalf("genuine reply after a forged one: result = %+v, want target reached", r)
		}
	})

	t.Run("echo tier", func(t *testing.T) {
		tr := newListenTracer()
		tr.cfg.EchoID = 0xE00E
		key := probeKey{SrcPort: tr.cfg.EchoID, DstPort: 9}
		tr.pm.addTo(key, 3, 0, time.Now(), ProbeICMPEcho, hop)

		if r := listenOne(t, tr, marshalEchoReply(tr.cfg.EchoID, 9), stranger); r != nil {
			t.Fatalf("Echo Reply from %s accepted for an echo sent to %s: %+v", stranger, hop, r)
		}
		r := listenOne(t, tr, marshalEchoReply(tr.cfg.EchoID, 9), hop.String())
		if r == nil || r.ProbeType != ProbeICMPEcho {
			t.Fatalf("genuine reply after a forged one: result = %+v, want an echo result", r)
		}
	})
}

// The send loop records where each ICMP probe went, so the listener has
// something to check the sender against.
func TestRun_RecordsProbeDestinations(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.MaxHops = 2
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 2
	cfg.Timeout = time.Hour // keep records from being swept
	target := net.ParseIP("192.0.2.1")
	hop := net.ParseIP("192.0.2.10")

	snd := &fakeSender{}
	tr := newEchoRunTracer(snd, cfg, target)
	tr.SubmitEchoTargets([]EchoTarget{{IP: hop, TTL: 1}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.run(ctx, nil, make(chan Result, 64)); err != nil {
		t.Fatalf("run: %v", err)
	}

	tr.pm.mu.Lock()
	defer tr.pm.mu.Unlock()
	var ttlProbes, echoes int
	for _, rec := range tr.pm.items {
		switch rec.probeType {
		case ProbeICMPTTL:
			ttlProbes++
			if !rec.dst.Equal(target) {
				t.Errorf("ICMP TTL probe record dst = %v, want the target %v", rec.dst, target)
			}
		case ProbeICMPEcho:
			echoes++
			if !rec.dst.Equal(hop) {
				t.Errorf("echo probe record dst = %v, want the hop %v", rec.dst, hop)
			}
		}
	}
	if ttlProbes == 0 || echoes == 0 {
		t.Fatalf("records: %d ICMP TTL, %d echo; want some of each", ttlProbes, echoes)
	}
}

// A hop legitimately has a handful of ECMP nodes. The echo tier takes at most
// 8 addresses per TTL, so forged replies cannot grow the list of addresses via
// pings without bound.
func TestSubmitEchoTargets_CapsPerTTL(t *testing.T) {
	tr := NewTracer(net.ParseIP("192.0.2.1"), DefaultConfig())
	var in []EchoTarget
	for i := 0; i < 12; i++ {
		in = append(in, EchoTarget{IP: net.IPv4(203, 0, 113, byte(i+1)), TTL: 3})
	}
	in = append(in,
		EchoTarget{IP: net.ParseIP("192.0.2.10"), TTL: 4},
		EchoTarget{IP: net.ParseIP("192.0.2.14"), TTL: 4},
	)
	tr.SubmitEchoTargets(in)

	tr.echo.mu.Lock()
	defer tr.echo.mu.Unlock()
	perTTL := map[int][]net.IP{}
	for _, tgt := range tr.echo.targets {
		perTTL[tgt.TTL] = append(perTTL[tgt.TTL], tgt.IP)
	}
	if got := len(perTTL[3]); got != maxEchoTargetsPerTTL {
		t.Errorf("TTL 3 kept %d targets, want %d", got, maxEchoTargetsPerTTL)
	}
	for i, ip := range perTTL[3] {
		if want := net.IPv4(203, 0, 113, byte(i+1)); !ip.Equal(want) {
			t.Errorf("TTL 3 target %d = %v, want %v (the first %d submitted, in order)", i, ip, want, maxEchoTargetsPerTTL)
		}
	}
	if got := len(perTTL[4]); got != 2 {
		t.Errorf("TTL 4 kept %d targets, want 2 (under the cap, untouched)", got)
	}
}
