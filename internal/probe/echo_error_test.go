package probe

import (
	"net"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv6"
)

// An ICMP error is not an Echo Reply (viaduct#85). An error that quotes an
// echo-tier probe must not be recorded as an answer to that echo from whoever
// sent the error: no result, and the echo's record stays in the probe map so
// the echo times out as lost. Errors that quote TTL probes match as before.

// marshalDstUnreachableV6 builds an ICMPv6 Destination Unreachable message.
func marshalDstUnreachableV6(code int, innerData []byte) []byte {
	msg := &icmp.Message{
		Type: ipv6.ICMPTypeDestinationUnreachable,
		Code: code,
		Body: &icmp.DstUnreach{Data: innerData},
	}
	b, _ := msg.Marshal(nil)
	return b
}

// buildICMPInnerHeaderV6 is the quote of an ICMPv6 Echo Request: the IPv6
// header and the first 8 bytes of the echo.
func buildICMPInnerHeaderV6(t *testing.T, src, dst net.IP, id, seq int) []byte {
	t.Helper()
	pkt, err := buildICMPEchoRequest(6, id, seq, 8)
	if err != nil {
		t.Fatal(err)
	}
	return append(buildIPv6HeaderTest(58, src, dst), pkt[:8]...)
}

// echoErrorFamily is what the tests below need for one IP version.
type echoErrorFamily struct {
	name     string
	tracer   func() *Tracer
	firewall net.Addr // a device on the path that is not the echo's destination
	quote    func(t *testing.T, tr *Tracer, id, seq int) []byte
	unreach  func(inner []byte) []byte // administratively prohibited
	exceeded func(inner []byte) []byte
	reply    func(id, seq int) []byte
}

func echoErrorFamilies() []echoErrorFamily {
	src4 := net.ParseIP("198.51.100.7")
	return []echoErrorFamily{
		{
			name:     "IPv4",
			tracer:   newListenTracer,
			firewall: fakeAddr{"203.0.113.99"},
			quote: func(_ *testing.T, tr *Tracer, id, seq int) []byte {
				return buildICMPInnerHeader(src4, tr.cfg.TargetIP, id, seq)
			},
			unreach:  func(inner []byte) []byte { return marshalDstUnreachable(13, inner) },
			exceeded: marshalTimeExceeded,
			reply:    marshalEchoReply,
		},
		{
			name:     "IPv6",
			tracer:   func() *Tracer { return linkLocalTracer(net.ParseIP("2001:db8:100::1")) },
			firewall: &net.IPAddr{IP: net.ParseIP("2001:db8:0:99::1")},
			quote: func(t *testing.T, tr *Tracer, id, seq int) []byte {
				return buildICMPInnerHeaderV6(t, tr.cfg.SourceIP, tr.cfg.TargetIP, id, seq)
			},
			unreach:  func(inner []byte) []byte { return marshalDstUnreachableV6(1, inner) },
			exceeded: marshalTimeExceededV6,
			reply:    marshalEchoReplyV6,
		},
	}
}

func hasRecord(tr *Tracer, key probeKey) bool {
	tr.pm.mu.Lock()
	defer tr.pm.mu.Unlock()
	_, ok := tr.pm.items[key]
	return ok
}

func TestListen_ICMPErrorNeverAnswersAnEcho(t *testing.T) {
	for _, fam := range echoErrorFamilies() {
		for _, errMsg := range []struct {
			name    string
			marshal func(echoErrorFamily) func([]byte) []byte
		}{
			{"Destination Unreachable", func(f echoErrorFamily) func([]byte) []byte { return f.unreach }},
			{"Time Exceeded", func(f echoErrorFamily) func([]byte) []byte { return f.exceeded }},
		} {
			t.Run(fam.name+"/"+errMsg.name, func(t *testing.T) {
				t.Parallel()
				tr := fam.tracer()
				tr.cfg.EchoID = 0xE00E
				target := tr.cfg.TargetIP
				// An echo-tier probe sent to the destination, at the destination's hop.
				key := probeKey{SrcPort: tr.cfg.EchoID, DstPort: 9}
				tr.pm.addTo(key, 5, 0, time.Now(), ProbeICMPEcho, target)

				pkt := errMsg.marshal(fam)(fam.quote(t, tr, tr.cfg.EchoID, 9))
				if r, ok := oneResult(t, tr, pkt, fam.firewall); ok {
					t.Fatalf("%s quoting an echo-tier probe produced a result: %+v", errMsg.name, r)
				}
				if got := tr.targetTTL; got != 0 {
					t.Errorf("targetTTL = %d after the error, want 0", got)
				}
				if !hasRecord(tr, key) {
					t.Fatal("the echo's record was consumed by the error; it must stay and time out as lost")
				}

				// The record is intact: the destination's own Echo Reply still answers it.
				r, ok := oneResult(t, tr, fam.reply(tr.cfg.EchoID, 9), &net.IPAddr{IP: target})
				if !ok {
					t.Fatal("genuine Echo Reply after the error was not matched")
				}
				if r.ProbeType != ProbeICMPEcho || r.TTL != 5 || !r.IP.Equal(target) {
					t.Errorf("result = %+v, want an echo answer from %s at TTL 5", r, target)
				}
			})
		}
	}
}

// Errors that quote a TTL-limited ICMP probe are what a traceroute is made of.
// They match exactly as before, and consume the record.
func TestListen_ICMPErrorStillAnswersICMPTTLProbe(t *testing.T) {
	for _, fam := range echoErrorFamilies() {
		t.Run(fam.name+"/Time Exceeded", func(t *testing.T) {
			tr := fam.tracer()
			tr.cfg.EchoID = 0xE00E
			id := tr.cfg.Protocol.(*ICMPProtocol).sessionID
			key := probeKey{SrcPort: id, DstPort: 7}
			tr.pm.addTo(key, 3, 0, time.Now(), ProbeICMPTTL, tr.cfg.TargetIP)

			r, ok := oneResult(t, tr, fam.exceeded(fam.quote(t, tr, id, 7)), fam.firewall)
			if !ok {
				t.Fatal("Time Exceeded quoting an ICMP TTL probe was not matched")
			}
			if r.TTL != 3 || r.IsTarget || r.ProbeType != ProbeICMPTTL || !r.IP.Equal(peerIP(fam.firewall)) {
				t.Errorf("result = %+v, want an ICMP TTL answer from %s at TTL 3, not the target", r, fam.firewall)
			}
			if hasRecord(tr, key) {
				t.Error("the TTL probe's record was left in the map after it was answered")
			}
		})

		t.Run(fam.name+"/Destination Unreachable", func(t *testing.T) {
			tr := fam.tracer()
			tr.cfg.EchoID = 0xE00E
			id := tr.cfg.Protocol.(*ICMPProtocol).sessionID
			key := probeKey{SrcPort: id, DstPort: 8}
			tr.pm.addTo(key, 4, 0, time.Now(), ProbeICMPTTL, tr.cfg.TargetIP)

			r, ok := oneResult(t, tr, fam.unreach(fam.quote(t, tr, id, 8)), fam.firewall)
			if !ok {
				t.Fatal("Destination Unreachable quoting an ICMP TTL probe was not matched")
			}
			if r.TTL != 4 || r.ProbeType != ProbeICMPTTL || !r.IP.Equal(peerIP(fam.firewall)) {
				t.Errorf("result = %+v, want an ICMP TTL answer from %s at TTL 4", r, fam.firewall)
			}
			if hasRecord(tr, key) {
				t.Error("the TTL probe's record was left in the map after it was answered")
			}
		})
	}
}
