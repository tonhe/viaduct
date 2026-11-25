package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// A probe that did not leave is neither sent nor lost in an export
// (viaduct#115): probes_sent, sent, the per-type counts and both loss figures
// come from what the tracer reported as written.

// unsentTable is one hop over four rounds of one ICMP TTL probe and two flows:
//
//	round 1  everything sent and answered
//	round 2  nothing left (every send failed or was skipped)
//	round 3  the ICMP probe did not leave; both flows went out and were answered
//	round 4  everything sent; one flow unanswered
func unsentTable() *hop.Table {
	table := hop.NewTable(1)
	ip := net.ParseIP("192.0.2.2")
	sample := func(kind hop.ProbeKind, flow int) {
		table.GetOrCreate(1).AddSample(ip, time.Millisecond, flow, kind)
	}
	end := func() { table.GetOrCreate(1).MarkRoundEnd() }

	table.MarkRoundSentByType(1, 1, 2)
	sample(hop.KindICMPTTL, 0)
	sample(hop.KindPrimary, 0)
	sample(hop.KindPrimary, 1)
	end()

	table.MarkRoundSentByType(1, 0, 0)
	end()

	table.MarkRoundSentByType(1, 0, 2)
	sample(hop.KindPrimary, 0)
	sample(hop.KindPrimary, 1)
	end()

	table.MarkRoundSentByType(1, 1, 2)
	sample(hop.KindICMPTTL, 0)
	sample(hop.KindPrimary, 0)
	end()
	return table
}

func TestExport_UnsentProbesAreNotSentAndNotLost_JSON(t *testing.T) {
	r := BuildReport("v0", "198.51.100.1", net.ParseIP("198.51.100.1"), "udp", unsentTable(), nil, nil, 1)
	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Probes int `json:"probes"`
		Hops   []struct {
			Rounds         int     `json:"rounds"`
			Sent           int     `json:"sent"`
			ProbesSent     int     `json:"probes_sent"`
			ProbesReceived int     `json:"probes_received"`
			Loss           float64 `json:"loss_pct"`
			Headline       float64 `json:"headline_loss_pct"`
			Nodes          []struct {
				Sent     int `json:"sent"`
				Received int `json:"received"`
				Probes   struct {
					ICMPTTL struct{ Sent, Received int } `json:"icmp_ttl"`
					Primary struct{ Sent, Received int } `json:"primary"`
				} `json:"probes"`
			} `json:"nodes"`
		} `json:"hops"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Hops) != 1 || len(doc.Hops[0].Nodes) != 1 {
		t.Fatalf("want 1 hop with 1 node, got %s", buf.String())
	}
	h := doc.Hops[0]

	// Written: 3 + 0 + 2 + 3 = 8 probes, 7 answered. The 4 that did not leave
	// (3 in round 2, 1 in round 3) appear nowhere.
	if h.ProbesSent != 8 || h.Sent != 8 || doc.Probes != 8 {
		t.Errorf("probes_sent = %d, sent = %d, probes = %d; want 8 each", h.ProbesSent, h.Sent, doc.Probes)
	}
	if h.ProbesReceived != 7 {
		t.Errorf("probes_received = %d, want 7", h.ProbesReceived)
	}
	if want := 12.5; h.Loss != want || h.Headline != want {
		t.Errorf("loss_pct = %v, headline_loss_pct = %v; want %v (1 of 8), not the 5 of 12 that counting unsent probes gives", h.Loss, h.Headline, want)
	}
	if h.Rounds != 3 {
		t.Errorf("rounds = %d, want 3: the round in which nothing left is not a round", h.Rounds)
	}
	n := h.Nodes[0]
	if n.Sent != 8 || n.Received != 7 {
		t.Errorf("node sent, received = %d, %d; want 8, 7", n.Sent, n.Received)
	}
	if n.Probes.ICMPTTL.Sent != 2 || n.Probes.ICMPTTL.Received != 2 {
		t.Errorf("icmp_ttl = %d sent, %d received; want 2, 2", n.Probes.ICMPTTL.Sent, n.Probes.ICMPTTL.Received)
	}
	if n.Probes.Primary.Sent != 6 || n.Probes.Primary.Received != 5 {
		t.Errorf("primary = %d sent, %d received; want 6, 5", n.Probes.Primary.Sent, n.Probes.Primary.Received)
	}
	if got := n.Probes.ICMPTTL.Sent + n.Probes.Primary.Sent; got != h.ProbesSent {
		t.Errorf("per-type sent adds to %d, hop probes_sent is %d: the outputs disagree", got, h.ProbesSent)
	}
}

func TestExport_UnsentProbesAreNotSentAndNotLost_CSV(t *testing.T) {
	r := BuildReport("v0", "198.51.100.1", net.ParseIP("198.51.100.1"), "udp", unsentTable(), nil, nil, 1)
	var buf bytes.Buffer
	if err := WriteCSV(&buf, r); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want a header and one row, got %d rows", len(rows))
	}
	row := rows[1]
	if row[csvColSent] != "8" || row[csvColReceived] != "7" || row[csvColLoss] != "12.5" || row[csvColHeadline] != "12.5" {
		t.Errorf("sent, received, loss, headline = %s, %s, %s, %s; want 8, 7, 12.5, 12.5",
			row[csvColSent], row[csvColReceived], row[csvColLoss], row[csvColHeadline])
	}
}
