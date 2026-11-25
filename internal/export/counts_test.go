package export

import (
	"net"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// The count fields in a report are all in probes, as they were before
// interleaved probes, and rounds have their own field:
//
//	hop:  sent == probes_sent, probes_received, rounds
//	node: sent (probes sent to the hop), received (this node's replies)
//	node probes.icmp_ttl / probes.primary / probes.echo: sent and received
//	      are both probes of that type
//
// A hop that never answered still reports what was sent to it.

// runHop runs rounds on the hop at ttl: perRound TTL-limited probes a round,
// the first of them the ICMP probe. answered of them are answered by ip,
// ICMP probe first. ip == "" leaves the hop silent.
func runHop(tbl *hop.Table, ttl int, ip string, rounds, perRound, answered int) {
	h := tbl.GetOrCreate(ttl)
	for r := 0; r < rounds; r++ {
		h.MarkRoundSentProbes(perRound)
		if ip != "" {
			addr := net.ParseIP(ip)
			for i := 0; i < answered; i++ {
				kind := hop.KindPrimary
				if i == 0 {
					kind = hop.KindICMPTTL
				}
				h.AddSample(addr, 10*time.Millisecond, i, kind)
			}
		}
		h.MarkRoundEnd()
	}
}

func TestBuildReport_CountsAreInProbes(t *testing.T) {
	tbl := hop.NewTable(30)
	runHop(tbl, 1, "192.0.2.1", 10, 7, 7) // UDP mode, clean
	runHop(tbl, 2, "192.0.2.2", 10, 7, 4) // UDP mode, ICMP + 3 of 6 UDP answered
	r := BuildReport("test", "example.net", net.ParseIP("198.51.100.1"), "udp", tbl, nil, nil, 2)

	h1 := r.Hops[0]
	if h1.Rounds != 10 || h1.Sent != 70 || h1.ProbesSent != 70 || h1.ProbesReceived != 70 {
		t.Errorf("clean hop: rounds %d sent %d probes_sent %d probes_received %d; want 10, 70, 70, 70",
			h1.Rounds, h1.Sent, h1.ProbesSent, h1.ProbesReceived)
	}
	n1 := h1.Nodes[0]
	if n1.Sent != 70 || n1.Received != 70 {
		t.Errorf("clean node: sent %d received %d; want 70 and 70, both in probes", n1.Sent, n1.Received)
	}
	if got := n1.Probes.ICMPTTL; got != (ProbeCount{Sent: 10, Received: 10}) {
		t.Errorf("clean node icmp_ttl = %+v, want 10 sent, 10 received", got)
	}
	if got := n1.Probes.Primary; got != (ProbeCount{Sent: 60, Received: 60}) {
		t.Errorf("clean node primary = %+v, want 60 sent, 60 received (6 flows a round)", got)
	}

	n2 := r.Hops[1].Nodes[0]
	if n2.Sent != 70 || n2.Received != 40 {
		t.Errorf("lossy node: sent %d received %d; want 70 and 40", n2.Sent, n2.Received)
	}
	if got := n2.Probes.Primary; got != (ProbeCount{Sent: 60, Received: 30}) {
		t.Errorf("lossy node primary = %+v, want 60 sent, 30 received", got)
	}
	for _, n := range []Node{n1, n2} {
		for name, pc := range map[string]ProbeCount{"icmp_ttl": n.Probes.ICMPTTL, "primary": n.Probes.Primary} {
			if pc.Received > pc.Sent {
				t.Errorf("%s %s: received %d exceeds sent %d", n.IP, name, pc.Received, pc.Sent)
			}
		}
	}
}

func TestBuildReport_ICMPModeHasNoPrimaryProbes(t *testing.T) {
	tbl := hop.NewTable(30)
	runHop(tbl, 1, "192.0.2.1", 10, 1, 1)
	r := BuildReport("test", "example.net", net.ParseIP("198.51.100.1"), "icmp", tbl, nil, nil, 1)
	n := r.Hops[0].Nodes[0]
	if got := n.Probes.Primary; got != (ProbeCount{}) {
		t.Errorf("primary = %+v in ICMP mode, want none sent: there are no UDP or TCP probes", got)
	}
	if got := n.Probes.ICMPTTL; got != (ProbeCount{Sent: 10, Received: 10}) {
		t.Errorf("icmp_ttl = %+v, want 10 and 10", got)
	}
	if n.Sent != 10 || n.Received != 10 {
		t.Errorf("node sent %d received %d, want 10 and 10", n.Sent, n.Received)
	}
}

func TestBuildReport_SilentHopReportsWhatWasSent(t *testing.T) {
	tbl := hop.NewTable(30)
	runHop(tbl, 1, "192.0.2.1", 10, 7, 7)
	runHop(tbl, 2, "", 10, 7, 0) // never answers
	runHop(tbl, 3, "192.0.2.3", 10, 7, 7)
	r := BuildReport("test", "example.net", net.ParseIP("198.51.100.1"), "udp", tbl, nil, nil, 3)

	silent := r.Hops[1]
	if len(silent.Nodes) != 0 {
		t.Fatalf("silent hop has %d nodes, want none", len(silent.Nodes))
	}
	if silent.Rounds != 10 || silent.Sent != 70 || silent.ProbesSent != 70 || silent.ProbesReceived != 0 {
		t.Errorf("silent hop: rounds %d sent %d probes_sent %d probes_received %d; want 10, 70, 70, 0",
			silent.Rounds, silent.Sent, silent.ProbesSent, silent.ProbesReceived)
	}
	if silent.Loss != 100 || silent.HeadlineLoss != 100 || silent.LossKind != "measured" {
		t.Errorf("silent hop: loss_pct %.1f headline %.1f kind %q; want 100, 100, measured",
			silent.Loss, silent.HeadlineLoss, silent.LossKind)
	}
	sum := 0
	for _, h := range r.Hops {
		sum += h.ProbesSent
	}
	if sum != r.Probes {
		t.Errorf("sum of hops[].probes_sent = %d, top-level probes = %d; want them equal", sum, r.Probes)
	}
}
