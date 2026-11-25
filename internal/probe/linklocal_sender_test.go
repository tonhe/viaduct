package probe

import (
	"context"
	"net"
	"testing"
	"time"
)

// A zoned IPv6 link-local sender (fe80::1%eth0). On IPv6 the first hop is
// often a link-local router, so if its replies stopped matching, that hop
// would read as lost on every trace.
//
// Condition of the Owner decision on viaduct#4: replies from such a sender
// are matched as they are on main, for Echo Replies and for ICMP errors.
// On main the listener matched both on the probe key alone and never looked
// at the sender to decide a match. Here Echo Replies are also checked against
// the address the probe was sent to (viaduct#50); the zone must not make that
// check fail.

func linkLocalTracer(target net.IP) *Tracer {
	cfg := DefaultConfig()
	cfg.IPVersion = 6
	cfg.ICMPID = 0x1234
	cfg.EchoID = 0x4321
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.RoundDelay = 10 * time.Millisecond
	cfg.Timeout = 100 * time.Millisecond
	cfg.SourceIP = net.ParseIP("2001:db8:0:1::1")
	cfg.TargetIP = target
	return NewTracer(target, cfg)
}

func oneResult(t *testing.T, tr *Tracer, data []byte, from net.Addr) (Result, bool) {
	t.Helper()
	conn := newFakeConn()
	conn.push(data, from)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan Result, 1)
	go tr.listen(ctx, conn, results)
	select {
	case r := <-results:
		return r, true
	case <-time.After(500 * time.Millisecond):
		return Result{}, false
	}
}

func TestListen_ZonedLinkLocalSender_TimeExceededIsMatched(t *testing.T) {
	target := net.ParseIP("2001:db8:100::1")
	tr := linkLocalTracer(target)
	router := &net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "eth0"}

	// An ICMP TTL probe at hop 1, answered by the link-local first hop.
	pkt, key, err := NewICMPProtocol(tr.cfg).BuildProbe(0, 1, 7, tr.cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr.pm.addTo(key, 1, 0, time.Now(), ProbeICMPTTL, target)
	quote := append(buildIPv6HeaderTest(58, tr.cfg.SourceIP, target), pkt[:8]...)

	r, ok := oneResult(t, tr, marshalTimeExceededV6(quote), router)
	if !ok {
		t.Fatal("Time Exceeded from the zoned link-local router was not matched; on main it is")
	}
	if r.TTL != 1 || r.IsTarget {
		t.Errorf("result = TTL %d, IsTarget %v; want TTL 1, not the target", r.TTL, r.IsTarget)
	}
	// On main the address was lost (net.ParseIP rejects a zoned string, so
	// Result.IP was nil). Here the router's address is reported, zone removed.
	if !r.IP.Equal(net.ParseIP("fe80::1")) {
		t.Errorf("result IP = %v, want fe80::1", r.IP)
	}
}

func TestListen_ZonedLinkLocalSender_EchoReplyIsMatched(t *testing.T) {
	// Echo tier: a direct echo to the link-local first hop, answered from the
	// same address with its zone attached.
	target := net.ParseIP("2001:db8:100::1")
	tr := linkLocalTracer(target)
	hop := net.ParseIP("fe80::1")
	key := probeKey{SrcPort: tr.cfg.EchoID, DstPort: 9}
	tr.pm.addTo(key, 1, 0, time.Now(), ProbeICMPEcho, hop)

	r, ok := oneResult(t, tr, marshalEchoReplyV6(tr.cfg.EchoID, 9), &net.IPAddr{IP: hop, Zone: "eth0"})
	if !ok {
		t.Fatal("Echo Reply from the zoned link-local hop was not matched; on main it is")
	}
	if r.TTL != 1 || r.ProbeType != ProbeICMPEcho || !r.IP.Equal(hop) {
		t.Errorf("result = TTL %d, type %v, IP %v; want TTL 1, echo, fe80::1", r.TTL, r.ProbeType, r.IP)
	}
}

func TestListen_ZonedLinkLocalTarget_EchoReplyIsMatched(t *testing.T) {
	// ICMP TTL tier with a link-local destination: the probe is addressed to
	// the target, and the target's Echo Reply arrives with a zone.
	target := net.ParseIP("fe80::2")
	tr := linkLocalTracer(target)
	_, key, err := NewICMPProtocol(tr.cfg).BuildProbe(0, 1, 11, tr.cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr.pm.addTo(key, 1, 0, time.Now(), ProbeICMPTTL, target)

	r, ok := oneResult(t, tr, marshalEchoReplyV6(tr.cfg.ICMPID, 11), &net.IPAddr{IP: target, Zone: "eth0"})
	if !ok {
		t.Fatal("Echo Reply from the zoned link-local target was not matched; on main it is")
	}
	if r.TTL != 1 || !r.IsTarget {
		t.Errorf("result = TTL %d, IsTarget %v; want TTL 1 and the target", r.TTL, r.IsTarget)
	}
}
