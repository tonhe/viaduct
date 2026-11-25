package probe

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// The ICMP sequence number is 16 bits on the wire. A run's probe counter is
// not: it passes 65,535 after about 25 minutes in the default mode. The stored
// probe key must be the 16-bit value that comes back in the reply, or every
// ICMP TTL probe and every echo goes unmatched from then on (viaduct#75).

func icmpWrapConfig(v int) (Config, net.IP, net.IP) {
	cfg := DefaultConfig()
	cfg.IPVersion = v
	cfg.ICMPID = 0x1234
	cfg.EchoID = 0x4321
	cfg.EchoBudget = 64
	src, dst := net.ParseIP("192.0.2.1"), net.ParseIP("198.51.100.1")
	if v == 6 {
		src, dst = net.ParseIP("2001:db8:0:1::1"), net.ParseIP("2001:db8:100::1")
	}
	cfg.SourceIP, cfg.TargetIP = src, dst
	return cfg, src, dst
}

// wireSeq reads the sequence number from a marshalled ICMP echo.
func wireSeq(pkt []byte) int { return int(binary.BigEndian.Uint16(pkt[6:8])) }

func TestICMPBuildProbe_KeyIsTheSequenceOnTheWire(t *testing.T) {
	for _, v := range []int{4, 6} {
		cfg, src, dst := icmpWrapConfig(v)
		p := NewICMPProtocol(cfg)
		const seq = 65543 // 65536 + 7

		pkt, key, err := p.BuildProbe(0, 5, seq, cfg)
		if err != nil {
			t.Fatalf("IPv%d BuildProbe: %v", v, err)
		}
		if got := wireSeq(pkt); got != 7 {
			t.Fatalf("IPv%d: sequence on the wire = %d, want 7", v, got)
		}
		if key.DstPort != 7 {
			t.Errorf("IPv%d: stored key sequence = %d, want 7, the value a reply carries", v, key.DstPort)
		}

		// A Time Exceeded quoting this probe must identify the same key.
		var quote []byte
		if v == 6 {
			quote = append(buildIPv6HeaderTest(58, src, dst), pkt[:8]...)
		} else {
			quote = append(buildIPv4Header(1, src, dst), pkt[:8]...)
		}
		got, err := p.IdentifyResponse(quote)
		if err != nil || got == nil || *got != key {
			t.Errorf("IPv%d: quoted probe identified as %+v (err %v), want the stored key %+v", v, got, err, key)
		}

		// And an Echo Reply from the target, keyed as the listener keys it.
		pm := newProbeMap()
		pm.addTo(key, 5, 0, time.Now(), ProbeICMPTTL, dst)
		reply := probeKey{SrcPort: cfg.ICMPID, DstPort: wireSeq(pkt)}
		if _, ok := pm.matchEchoReply(reply, dst); !ok {
			t.Errorf("IPv%d: Echo Reply with sequence %d did not match the probe sent with counter %d", v, wireSeq(pkt), seq)
		}
	}
}

func TestEchoTier_MatchesAfterTheCounterPasses16Bits(t *testing.T) {
	for _, v := range []int{4, 6} {
		cfg, _, dst := icmpWrapConfig(v)
		cfg.Protocol = NewICMPProtocol(cfg)
		cfg.ProbeDelay = 0
		tr := NewTracer(dst, cfg)
		hop := net.ParseIP("192.0.2.9")
		if v == 6 {
			hop = net.ParseIP("2001:db8:0:9::1")
		}
		var targets []EchoTarget
		for ttl := 1; ttl <= 8; ttl++ {
			targets = append(targets, EchoTarget{IP: hop, TTL: ttl})
		}
		tr.SubmitEchoTargets(targets)

		// Start just below the 16-bit limit so this batch crosses it.
		snd := &fakeSender{}
		seq := 65532
		tr.sendEchoes(snd, &seq)

		pairs := echoPacketPairs(snd)
		if len(pairs) != 8 {
			t.Fatalf("IPv%d: sent %d echoes, want 8", v, len(pairs))
		}
		for i, p := range pairs {
			reply := probeKey{SrcPort: p[0], DstPort: p[1]}
			rec, ok := tr.pm.matchEchoReply(reply, hop)
			if !ok {
				t.Errorf("IPv%d: echo %d (sequence %d on the wire) is unmatched after the counter passed 65,535", v, i, p[1])
				continue
			}
			if rec.ttl != i+1 || rec.probeType != ProbeICMPEcho {
				t.Errorf("IPv%d: echo %d matched record ttl=%d type=%v, want ttl=%d echo", v, i, rec.ttl, rec.probeType, i+1)
			}
		}
	}
}
