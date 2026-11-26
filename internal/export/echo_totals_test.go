package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"strings"
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
// echo loss in a two-proportion z-test at p < 0.01. The TTL-probe counts are
// the ones the verdict was made from (verdict_probes_sent,
// verdict_probes_received), which differ from the current counts while the
// hop holds an earlier verdict (viaduct#200).
func consumerVerdict(h Hop) string {
	return zTestVerdict(h.VerdictProbesSent, h.VerdictProbesReceived, h.EchoSent, h.EchoReceived)
}

// currentCountsVerdict is the same rule on the hop's current TTL-probe counts
// (probes_sent, probes_received): what a consumer got before the export
// carried the verdict's own counts.
func currentCountsVerdict(h Hop) string {
	return zTestVerdict(h.ProbesSent, h.ProbesReceived, h.EchoSent, h.EchoReceived)
}

func zTestVerdict(probesSent, probesReceived, echoSent, echoReceived int) string {
	if echoSent < 20 {
		return "undetermined"
	}
	n1, n2 := float64(probesSent), float64(echoSent)
	if n1 == 0 {
		return "no"
	}
	lost1 := math.Max(0, float64(probesSent-probesReceived))
	lost2 := math.Max(0, float64(echoSent-echoReceived))
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
			t.Errorf("%s: node %s rate_limited = %q; the hop's counts (verdict probes %d/%d, echo %d/%d) give %q",
				when, n.IP, n.RateLimited, h.VerdictProbesSent, h.VerdictProbesReceived, h.EchoSent, h.EchoReceived, verdict)
		}
	}
	// A hop that is not holding a verdict made it from its current counts.
	if !h.VerdictHeld && (h.VerdictProbesSent != h.ProbesSent || h.VerdictProbesReceived != h.ProbesReceived) {
		t.Errorf("%s: verdict_held is false, verdict_probes_sent, verdict_probes_received = %d, %d; want the current %d, %d",
			when, h.VerdictProbesSent, h.VerdictProbesReceived, h.ProbesSent, h.ProbesReceived)
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

// ttlLossRound runs one round of seven TTL-limited probes of which ttlAnswers
// are answered by ttlFrom, then one echo to each node, answered by the nodes
// named in echoFrom.
func ttlLossRound(h *hop.Hop, ttlAnswers int, ttlFrom string, echoFrom ...string) {
	h.MarkRoundSentProbes(7)
	for i := 0; i < ttlAnswers; i++ {
		h.AddSample(net.ParseIP(ttlFrom), 0, 0, hop.KindICMPTTL)
	}
	nodes := h.GetNodes()
	for _, n := range nodes {
		n.MarkEchoSent()
	}
	for _, n := range nodes {
		for _, ip := range echoFrom {
			if n.GetIP().String() == ip {
				h.AddSample(n.GetIP(), 0, 0, hop.KindEcho)
			}
		}
	}
	h.MarkRoundEnd()
}

// The TTL-probe loss moves while a rate-limited verdict is held: the hop
// starts answering its TTL probes. The z-test on the current probes_sent and
// probes_received with the held echo totals then says "no", while the export
// still says "yes". verdict_held says why, and verdict_probes_sent and
// verdict_probes_received are the counts that give "yes" (viaduct#200).
func TestBuildReport_HeldVerdictReproducesWhenTTLLossMoves(t *testing.T) {
	const silent, answering = "192.0.2.32", "192.0.2.31"
	// The silent node's 20th ignored echo, in round 19, takes it out of the
	// echo totals; the answering node's echoes alone reach 20 in round 31.
	const dropAt, newAt = 19, 31
	tbl := hop.NewTable(30)
	h := tbl.GetOrCreate(1)
	round := func(r int) {
		switch {
		case r == 0:
			ttlLossRound(h, 1, silent)
		case r < 12:
			ttlLossRound(h, 0, "")
		case r < dropAt:
			// The answering node appears in round 12 and answers every echo.
			ttlLossRound(h, boolToInt(r == 12), answering, answering)
		default:
			// From the round the totals fall, every TTL probe is answered.
			ttlLossRound(h, 7, answering, answering)
		}
	}

	for r := 0; r < dropAt; r++ {
		round(r)
		got := buildOneHop(t, tbl)
		if got.VerdictHeld {
			t.Fatalf("round %d: verdict_held = true before the echo totals fall", r)
		}
		checkReproducible(t, fmt.Sprintf("round %d", r), got)
	}
	before := buildOneHop(t, tbl)
	if before.LossKind != "rate_limited" || before.ProbesSent != 133 || before.ProbesReceived != 2 {
		t.Fatalf("fixture: before the totals fall: loss_kind %q, probes %d/%d; want rate_limited, 133/2",
			before.LossKind, before.ProbesSent, before.ProbesReceived)
	}

	differed := 0
	for r := dropAt; r < newAt; r++ {
		round(r)
		when := fmt.Sprintf("round %d", r)
		got := buildOneHop(t, tbl)
		if !got.VerdictHeld {
			t.Errorf("%s: verdict_held = false while the hop holds its verdict", when)
		}
		for _, n := range got.Nodes {
			if n.RateLimited != "yes" {
				t.Errorf("%s: node %s rate_limited = %q, want the held yes", when, n.IP, n.RateLimited)
			}
		}
		// The counts the held verdict was made from, not the current ones.
		if got.VerdictProbesSent != before.ProbesSent || got.VerdictProbesReceived != before.ProbesReceived {
			t.Errorf("%s: verdict_probes_sent, verdict_probes_received = %d, %d; want %d, %d from when the totals fell",
				when, got.VerdictProbesSent, got.VerdictProbesReceived, before.ProbesSent, before.ProbesReceived)
		}
		if want := before.ProbesSent + 7*(r-dropAt+1); got.ProbesSent != want {
			t.Errorf("%s: probes_sent = %d, want the current %d", when, got.ProbesSent, want)
		}
		if got.EchoSent != before.EchoSent || got.EchoReceived != before.EchoReceived {
			t.Errorf("%s: echo_sent, echo_received = %d, %d; want the held %d, %d",
				when, got.EchoSent, got.EchoReceived, before.EchoSent, before.EchoReceived)
		}
		if currentCountsVerdict(got) != "yes" {
			differed++
		}
		checkReproducible(t, when, got)
	}
	if differed == 0 {
		t.Fatal("fixture: the z-test on the current probe counts never differed from the held verdict")
	}
	last := buildOneHop(t, tbl)
	if got := currentCountsVerdict(last); got != "no" {
		t.Fatalf("fixture: in the last held round the current counts (probes %d/%d, echo %d/%d) give %q, want no",
			last.ProbesSent, last.ProbesReceived, last.EchoSent, last.EchoReceived, got)
	}

	round(newAt)
	after := buildOneHop(t, tbl)
	if after.VerdictHeld {
		t.Error("with 20 new echo samples: verdict_held = true, want false")
	}
	if after.VerdictProbesSent != after.ProbesSent || after.VerdictProbesReceived != after.ProbesReceived {
		t.Errorf("with 20 new echo samples: verdict_probes_sent, verdict_probes_received = %d, %d; want the current %d, %d",
			after.VerdictProbesSent, after.VerdictProbesReceived, after.ProbesSent, after.ProbesReceived)
	}
	checkReproducible(t, "with 20 new echo samples", after)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestExport_VerdictHeldAndVerdictProbeFields(t *testing.T) {
	r := Report{Version: "test", Target: "a.example", Protocol: "udp", Hops: []Hop{
		{TTL: 1, Loss: 60.4, HeadlineLoss: 73.1, LossKind: "rate_limited", Rounds: 31,
			Sent: 217, ProbesSent: 217, ProbesReceived: 86, EchoSent: 26, EchoReceived: 7,
			VerdictHeld: true, VerdictProbesSent: 133, VerdictProbesReceived: 2,
			Nodes: []Node{
				{IP: "192.0.2.31", Loss: 60.4, Sent: 217, Received: 85, RateLimited: "yes"},
				{IP: "192.0.2.32", Loss: 60.4, Sent: 217, Received: 1, RateLimited: "yes", EchoIgnored: true},
			}},
		{TTL: 2, Loss: 100, HeadlineLoss: 100, LossKind: "measured", Rounds: 31,
			Sent: 217, ProbesSent: 217, VerdictProbesSent: 217},
		{TTL: 3}, // never probed
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
		// The fields are written for every hop, false and zero included.
		for i, want := range []map[string]string{
			{"verdict_held": "true", "verdict_probes_sent": "133", "verdict_probes_received": "2"},
			{"verdict_held": "false", "verdict_probes_sent": "217", "verdict_probes_received": "0"},
			{"verdict_held": "false", "verdict_probes_sent": "0", "verdict_probes_received": "0"},
		} {
			for k, v := range want {
				if got := string(doc.Hops[i][k]); got != v {
					t.Errorf("hop %d: %s = %q, want %s", i+1, k, got, v)
				}
			}
		}
		// They follow the echo totals in the hop object.
		out := buf.String()
		last := -1
		for _, name := range []string{"echo_sent", "echo_received", "verdict_held", "verdict_probes_sent", "verdict_probes_received"} {
			at := strings.Index(out, `"`+name+`"`)
			if at < 0 {
				t.Fatalf("no %s in the JSON", name)
			}
			if at < last {
				t.Errorf("%s comes before the field it should follow", name)
			}
			last = at
		}
	})

	t.Run("csv", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			write  func(*bytes.Buffer) error
			offset int // columns before ttl
		}{
			{"single", func(b *bytes.Buffer) error { return WriteCSV(b, r) }, 0},
			{"multi", func(b *bytes.Buffer) error {
				return WriteCSVMulti(b, MultiReport{Version: "test", Targets: []Report{r}})
			}, 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var buf bytes.Buffer
				if err := tc.write(&buf); err != nil {
					t.Fatal(err)
				}
				rows, err := csv.NewReader(&buf).ReadAll()
				if err != nil {
					t.Fatal(err)
				}
				header := rows[0]
				// After the #147 columns, before error, which stays last.
				wantTail := []string{"echo_ignored", "hop_echo_sent", "hop_echo_received",
					"verdict_held", "verdict_probes_sent", "verdict_probes_received", "error"}
				if len(header) != 33+tc.offset {
					t.Fatalf("header has %d columns, want %d: %q", len(header), 33+tc.offset, header)
				}
				gotTail := header[len(header)-len(wantTail):]
				for i := range wantTail {
					if gotTail[i] != wantTail[i] {
						t.Fatalf("last columns = %q, want %q", gotTail, wantTail)
					}
				}
				first := len(header) - 4 // verdict_held
				for i, want := range [][3]string{
					{"true", "133", "2"}, // the hop's values, repeated on each node
					{"true", "133", "2"},
					{"false", "217", "0"}, // a hop with no node: its own values
					{"", "", ""},          // a TTL never probed stays blank
				} {
					row := rows[i+1]
					got := [3]string{row[first], row[first+1], row[first+2]}
					if got != want {
						t.Errorf("row %d: verdict_held, verdict_probes_sent, verdict_probes_received = %q, want %q", i+1, got, want)
					}
					if row[len(row)-1] != "" {
						t.Errorf("row %d: error = %q, want empty", i+1, row[len(row)-1])
					}
				}
			})
		}
	})
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
