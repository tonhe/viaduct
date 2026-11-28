package export

import (
	"bytes"
	"encoding/csv"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// The destination's row is judged on the target alone (viaduct#244): its
// verdict, headline and echo totals are the target's, even when a router
// answers at the same TTL. In UDP mode the router answers the ICMP TTL probe,
// whose path is one hop longer, and every echo; the target loses half of
// what it is sent.
func destinationTable() *hop.Table {
	const router = "192.0.2.8"
	target := net.ParseIP("198.51.100.1")
	tbl := hop.NewTable(30)
	h := tbl.GetOrCreate(1)
	for r := 0; r < 40; r++ {
		tbl.MarkRoundSentByType(1, 1, 2)
		h.AddSample(net.ParseIP(router), time.Millisecond, 0, hop.KindICMPTTL)
		tbl.NoteReply(1, net.ParseIP(router), 0, hop.KindICMPTTL, false)
		h.AddSample(target, time.Millisecond, r%2, hop.KindPrimary)
		tbl.NoteReply(1, target, r%2, hop.KindPrimary, true)
		for _, n := range h.GetNodes() {
			n.MarkEchoSent()
			if !n.IsTarget() || r%2 == 0 {
				h.AddSample(n.GetIP(), time.Millisecond, 0, hop.KindEcho)
			}
		}
		h.MarkRoundEnd()
		tbl.EndRound()
	}
	return tbl
}

func TestBuildReport_DestinationRowIsTheTargets(t *testing.T) {
	got := buildOneHop(t, destinationTable())
	if len(got.Nodes) != 2 {
		t.Fatalf("fixture: %d nodes, want the target and the router", len(got.Nodes))
	}
	if got.LossKind != "measured" || got.HeadlineLoss != 50 {
		t.Errorf("loss_kind %q, headline_loss_pct %.2f; want measured, 50", got.LossKind, got.HeadlineLoss)
	}
	if got.Loss < 33.3 || got.Loss > 33.4 {
		t.Errorf("loss_pct = %.2f, want the raw 33.3 (40 of 120 probes)", got.Loss)
	}
	if got.EchoSent != 40 || got.EchoReceived != 20 {
		t.Errorf("echo_sent, echo_received = %d, %d; want the target's 40, 20", got.EchoSent, got.EchoReceived)
	}
	if got.VerdictHeld || got.VerdictProbesSent != 80 || got.VerdictProbesReceived != 40 {
		t.Errorf("verdict_held %v, verdict_probes_sent %d, verdict_probes_received %d; want false, 80, 40 (the UDP probes)",
			got.VerdictHeld, got.VerdictProbesSent, got.VerdictProbesReceived)
	}
	for _, n := range got.Nodes {
		if n.EchoIgnored {
			t.Errorf("node %s echo_ignored = true, want false on the destination's row", n.IP)
		}
		if n.RateLimited != "no" {
			t.Errorf("node %s rate_limited = %q, want no", n.IP, n.RateLimited)
		}
	}
	checkReproducibleRow(t, "destination", got, true)
}

// CSV repeats the destination row's figures on each node.
func TestWriteCSV_DestinationRowIsTheTargets(t *testing.T) {
	r := BuildReport("test", "example.net", net.ParseIP("198.51.100.1"), "udp", destinationTable(), nil, nil, 1)
	var buf bytes.Buffer
	if err := WriteCSV(&buf, r); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := make(map[string]int)
	for i, name := range rows[0] {
		col[name] = i
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want a header and two nodes", len(rows))
	}
	for _, row := range rows[1:] {
		for name, want := range map[string]string{
			"headline_loss_pct": "50.0", "loss_kind": "measured",
			"hop_echo_sent": "40", "hop_echo_received": "20",
			"verdict_probes_sent": "80", "verdict_probes_received": "40",
			"echo_ignored": "false",
		} {
			if got := row[col[name]]; got != want {
				t.Errorf("node %s %s = %q, want %q", row[col["ip"]], name, got, want)
			}
		}
	}
}
