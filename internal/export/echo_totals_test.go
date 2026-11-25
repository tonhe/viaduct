package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"testing"

	"github.com/tonhe/viaduct/internal/hop"
)

// A node that ignores echo is left out of its hop's echo totals, so the raw
// per-node echo counts do not add up to what the verdict used. The export
// marks the node (echo_ignored) and carries the hop's totals (echo_sent,
// echo_received), from which a consumer can work out the verdict and the
// headline without anything else (viaduct#147).

// consumerVerdict is the rate-limit rule as the README states it, written
// against the export alone: at least 20 echoes, and TTL-probe loss higher than
// echo loss in a two-proportion z-test at p < 0.01.
func consumerVerdict(h Hop) string {
	if h.EchoSent < 20 {
		return "undetermined"
	}
	n1, n2 := float64(h.ProbesSent), float64(h.EchoSent)
	if n1 == 0 {
		return "no"
	}
	lost1 := math.Max(0, float64(h.ProbesSent-h.ProbesReceived))
	lost2 := math.Max(0, float64(h.EchoSent-h.EchoReceived))
	pooled := (lost1 + lost2) / (n1 + n2)
	variance := pooled * (1 - pooled) * (1/n1 + 1/n2)
	if variance <= 0 {
		return "no"
	}
	if (lost1/n1-lost2/n2)/math.Sqrt(variance) >= 2.58 {
		return "yes"
	}
	return "no"
}

// checkReproducible fails unless the hop's verdict, loss kind and headline
// follow from its own exported counts.
func checkReproducible(t *testing.T, when string, h Hop) {
	t.Helper()
	verdict := consumerVerdict(h)
	for _, n := range h.Nodes {
		if n.RateLimited != verdict {
			t.Errorf("%s: node %s rate_limited = %q; the hop's counts (probes %d/%d, echo %d/%d) give %q",
				when, n.IP, n.RateLimited, h.ProbesSent, h.ProbesReceived, h.EchoSent, h.EchoReceived, verdict)
		}
	}
	wantKind, wantHeadline := "measured", h.Loss
	switch {
	case verdict == "yes":
		wantKind = "rate_limited"
		wantHeadline = float64(h.EchoSent-h.EchoReceived) / float64(h.EchoSent) * 100
	case verdict == "undetermined" && h.Loss > 0 && len(h.Nodes) > 0:
		wantKind = "provisional"
	}
	if h.LossKind != wantKind {
		t.Errorf("%s: loss_kind = %q; the hop's counts give %q", when, h.LossKind, wantKind)
	}
	if math.Abs(h.HeadlineLoss-wantHeadline) > 1e-9 {
		t.Errorf("%s: headline_loss_pct = %.4f; the hop's counts (echo %d/%d, loss_pct %.4f) give %.4f",
			when, h.HeadlineLoss, h.EchoSent, h.EchoReceived, h.Loss, wantHeadline)
	}
}

func buildOneHop(t *testing.T, tbl *hop.Table) Hop {
	t.Helper()
	r := BuildReport("test", "example.net", net.ParseIP("198.51.100.1"), "udp", tbl, nil, nil, 1)
	if len(r.Hops) != 1 {
		t.Fatalf("got %d hops, want 1", len(r.Hops))
	}
	return r.Hops[0]
}

func nodeByIP(t *testing.T, h Hop, ip string) Node {
	t.Helper()
	for _, n := range h.Nodes {
		if n.IP == ip {
			return n
		}
	}
	t.Fatalf("no node %s in the hop", ip)
	return Node{}
}

func TestBuildReport_LeftOutNodeIsMarkedAndVerdictReproduces(t *testing.T) {
	const silent, answering = "192.0.2.32", "192.0.2.31"
	tbl := hop.NewTable(30)
	h := tbl.GetOrCreate(1)
	// The two nodes answer the ICMP TTL probe in turn; only one answers echo.
	for r := 0; r < 40; r++ {
		from := answering
		if r%2 == 1 {
			from = silent
		}
		heldVerdictRound(h, from, answering)
	}
	got := buildOneHop(t, tbl)

	if n := nodeByIP(t, got, silent); !n.EchoIgnored {
		t.Errorf("node %s echo_ignored = false, want true: it answered none of its %d echoes", silent, n.Probes.Echo.Sent)
	}
	if n := nodeByIP(t, got, answering); n.EchoIgnored {
		t.Errorf("node %s echo_ignored = true, want false", answering)
	}
	// Raw per-node counts: 40 of 40 and 0 of 39. Only the first count.
	rawSent := 0
	for _, n := range got.Nodes {
		rawSent += n.Probes.Echo.Sent
	}
	if rawSent != 79 {
		t.Fatalf("fixture: per-node echo sent adds up to %d, want 79", rawSent)
	}
	if got.EchoSent != 40 || got.EchoReceived != 40 {
		t.Errorf("hop echo_sent, echo_received = %d, %d; want 40, 40", got.EchoSent, got.EchoReceived)
	}
	if got.LossKind != "rate_limited" || got.HeadlineLoss != 0 {
		t.Fatalf("fixture: loss_kind %q, headline %.2f; want rate_limited, 0", got.LossKind, got.HeadlineLoss)
	}
	checkReproducible(t, "left-out node", got)
}

// Through a held verdict (the echo totals fell below 20 samples) the exported
// totals are the ones the held verdict and headline were made from.
func TestBuildReport_EchoTotalsReproduceVerdictEveryRound(t *testing.T) {
	const silent, answering = "192.0.2.32", "192.0.2.31"
	for _, tc := range []struct {
		name  string
		round func(h *hop.Hop, r int)
	}{
		{"measured then left out", func(h *hop.Hop, r int) {
			if r < 25 {
				heldVerdictRound(h, silent)
				return
			}
			heldVerdictRound(h, answering, answering)
		}},
		{"rate limited then left out", func(h *hop.Hop, r int) {
			switch r {
			case 0:
				heldVerdictRound(h, silent, answering)
			case 12:
				heldVerdictRound(h, answering, answering)
			default:
				heldVerdictRound(h, "", answering)
			}
		}},
		{"starts answering", func(h *hop.Hop, r int) {
			from := answering
			if r%2 == 1 {
				from = silent
			}
			if r < 26 || r%5 == 4 {
				heldVerdictRound(h, from, answering)
				return
			}
			heldVerdictRound(h, from, answering, silent)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tbl := hop.NewTable(30)
			h := tbl.GetOrCreate(1)
			for r := 0; r < 60; r++ {
				tc.round(h, r)
				checkReproducible(t, fmt.Sprintf("round %d", r), buildOneHop(t, tbl))
			}
		})
	}
}

func TestExport_EchoIgnoredAndHopEchoTotalsFields(t *testing.T) {
	r := Report{Version: "test", Target: "a.example", Protocol: "udp", Hops: []Hop{
		{TTL: 1, Loss: 85.7, HeadlineLoss: 0, LossKind: "rate_limited", Rounds: 40,
			Sent: 280, ProbesSent: 280, ProbesReceived: 40, EchoSent: 40, EchoReceived: 40,
			Nodes: []Node{
				{IP: "192.0.2.31", Loss: 85.7, Sent: 280, Received: 20, RateLimited: "yes",
					Probes: NodeProbes{Echo: ProbeCount{Sent: 40, Received: 40}}},
				{IP: "192.0.2.32", Loss: 85.7, Sent: 280, Received: 20, RateLimited: "yes", EchoIgnored: true,
					Probes: NodeProbes{Echo: ProbeCount{Sent: 39}}},
			}},
		{TTL: 2, Loss: 100, HeadlineLoss: 100, LossKind: "measured", Rounds: 40, Sent: 280, ProbesSent: 280},
	}}

	t.Run("json", func(t *testing.T) {
		var buf bytes.Buffer
		if err := WriteJSON(&buf, r); err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Hops []map[string]json.RawMessage `json:"hops"`
		}
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		for i, want := range []map[string]string{
			{"echo_sent": "40", "echo_received": "40"},
			{"echo_sent": "0", "echo_received": "0"},
		} {
			for k, v := range want {
				if got := string(doc.Hops[i][k]); got != v {
					t.Errorf("hop %d: %s = %q, want %s", i+1, k, got, v)
				}
			}
		}
		var nodes []map[string]json.RawMessage
		if err := json.Unmarshal(doc.Hops[0]["nodes"], &nodes); err != nil {
			t.Fatal(err)
		}
		// The flag is written for every node, false included.
		for i, want := range []string{"false", "true"} {
			if got := string(nodes[i]["echo_ignored"]); got != want {
				t.Errorf("node %d: echo_ignored = %q, want %s", i, got, want)
			}
		}
	})

	t.Run("csv", func(t *testing.T) {
		var buf bytes.Buffer
		if err := WriteCSV(&buf, r); err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(&buf).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		col := map[string]int{}
		for i, name := range rows[0] {
			col[name] = i
		}
		for _, name := range []string{"echo_ignored", "hop_echo_sent", "hop_echo_received"} {
			if _, ok := col[name]; !ok {
				t.Fatalf("no %s column in %q", name, rows[0])
			}
		}
		if last := rows[0][len(rows[0])-1]; last != "error" {
			t.Errorf("last column = %q, want error", last)
		}
		for i, want := range [][3]string{
			{"false", "40", "40"}, // the hop's totals, repeated on each node
			{"true", "40", "40"},
			{"", "0", "0"}, // a hop with no node: its totals, no flag
		} {
			row := rows[i+1]
			got := [3]string{row[col["echo_ignored"]], row[col["hop_echo_sent"]], row[col["hop_echo_received"]]}
			if got != want {
				t.Errorf("row %d: echo_ignored, hop_echo_sent, hop_echo_received = %q, want %q", i+1, got, want)
			}
		}
	})
}
