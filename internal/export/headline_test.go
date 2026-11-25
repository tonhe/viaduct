package export

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// headlineReport is a hand-built report with one hop of each kind the CSV and
// DOT writers have to tell apart: clean, rate-limited (headline 0, raw 85.7),
// provisional, silent, and an ECMP hop with two nodes.
func headlineReport(target string) Report {
	return Report{
		Version: "test", Target: target, TargetIP: "203.0.113.9", Protocol: "udp", Probes: 182,
		Hops: []Hop{
			{TTL: 1, Loss: 0, HeadlineLoss: 0, LossKind: "measured", Rounds: 21, Sent: 42, ProbesSent: 42, ProbesReceived: 42,
				Nodes: []Node{{
					IP: "192.0.2.1", Hostname: "r1.example", ASN: 64496, ASOrg: "EXAMPLE-A",
					RTT: RTTStats{Avg: 1.5}, Loss: 0, Sent: 42, Received: 42,
					Probes: NodeProbes{
						ICMPTTL: ProbeCount{Sent: 21, Received: 21},
						Primary: ProbeCount{Sent: 21, Received: 21},
						Echo:    ProbeCount{Sent: 21, Received: 21},
					},
					RateLimited: "no",
				}}},
			{TTL: 2, Loss: 85.7, HeadlineLoss: 0, LossKind: "rate_limited", Rounds: 21, Sent: 42, ProbesSent: 42, ProbesReceived: 6,
				Nodes: []Node{{
					IP: "192.0.2.2", ASN: 64497, ASOrg: "EXAMPLE-B",
					RTT: RTTStats{Avg: 2.5}, Loss: 85.7, Sent: 42, Received: 6,
					Probes: NodeProbes{
						ICMPTTL: ProbeCount{Sent: 21, Received: 4},
						Primary: ProbeCount{Sent: 21, Received: 2},
						Echo:    ProbeCount{Sent: 21, Received: 21},
					},
					RateLimited: "yes",
				}}},
			{TTL: 3, Loss: 85.7, HeadlineLoss: 85.7, LossKind: "provisional", Rounds: 7, Sent: 14, ProbesSent: 14, ProbesReceived: 2,
				Nodes: []Node{{
					IP:  "198.51.100.3",
					RTT: RTTStats{Avg: 3.5}, Loss: 85.7, Sent: 14, Received: 2,
					Probes: NodeProbes{
						ICMPTTL: ProbeCount{Sent: 7, Received: 1},
						Primary: ProbeCount{Sent: 7, Received: 1},
						Echo:    ProbeCount{Sent: 7, Received: 7},
					},
					RateLimited: "undetermined",
				}}},
			{TTL: 4, Loss: 100, HeadlineLoss: 100, LossKind: "measured", Rounds: 21, Sent: 42, ProbesSent: 42},
			{TTL: 5, Loss: 0, HeadlineLoss: 0, LossKind: "measured", Rounds: 21, Sent: 42, ProbesSent: 42, ProbesReceived: 42,
				Nodes: []Node{
					{
						IP: "203.0.113.5", ASN: 64496, ASOrg: "EXAMPLE-A",
						RTT: RTTStats{Avg: 5.5}, Loss: 0, Sent: 42, Received: 22, FlowIDs: []int{0, 2},
						Probes: NodeProbes{
							ICMPTTL: ProbeCount{Sent: 21, Received: 11},
							Primary: ProbeCount{Sent: 21, Received: 11},
							Echo:    ProbeCount{Sent: 21, Received: 21},
						},
						RateLimited: "no",
					},
					{
						IP:  "203.0.113.6",
						RTT: RTTStats{Avg: 6.5}, Loss: 0, Sent: 42, Received: 20, FlowIDs: []int{1},
						Probes: NodeProbes{
							ICMPTTL: ProbeCount{Sent: 21, Received: 10},
							Primary: ProbeCount{Sent: 21, Received: 10},
							Echo:    ProbeCount{Sent: 21, Received: 20},
						},
						RateLimited: "no",
					},
				}},
		},
	}
}

// headlineMulti is headlineReport, a target that failed, and a short second
// target.
func headlineMulti() MultiReport {
	return MultiReport{
		Version:   "test",
		Timestamp: time.Unix(0, 0).UTC(),
		Targets: []Report{
			headlineReport("a.example"),
			{Version: "test", Target: "bad.example", Protocol: "udp", Error: "no such host"},
			{Version: "test", Target: "b.example", TargetIP: "203.0.113.10", Protocol: "udp", Probes: 14,
				Hops: []Hop{{TTL: 1, Loss: 50, HeadlineLoss: 50, LossKind: "measured", Rounds: 7, Sent: 14, ProbesSent: 14, ProbesReceived: 7,
					Nodes: []Node{{
						IP:  "198.51.100.77",
						RTT: RTTStats{Avg: 7.5}, Loss: 50, Sent: 14, Received: 7,
						Probes: NodeProbes{
							ICMPTTL: ProbeCount{Sent: 14, Received: 7},
							Echo:    ProbeCount{Sent: 7, Received: 7},
						},
						RateLimited: "undetermined",
					}}}}},
		},
	}
}

var wantCSVHeader = []string{
	"ttl", "ip", "hostname", "asn", "as_org",
	"loss_pct", "sent", "received",
	"avg_ms", "best_ms", "worst_ms", "last_ms", "stdev_ms", "jitter_ms",
	"flow_ids", "stability_pct", "trend",
	"headline_loss_pct", "loss_kind", "rate_limited",
	"icmp_ttl_sent", "icmp_ttl_received",
	"primary_sent", "primary_received",
	"echo_sent", "echo_received",
}

// wantHeadlineRows is the data rows WriteCSV must produce for headlineReport.
var wantHeadlineRows = [][]string{
	{"1", "192.0.2.1", "r1.example", "64496", "EXAMPLE-A", "0.0", "42", "42",
		"1.50", "0.00", "0.00", "0.00", "0.00", "0.00", "", "0.0", "",
		"0.0", "measured", "no", "21", "21", "21", "21", "21", "21"},
	{"2", "192.0.2.2", "", "64497", "EXAMPLE-B", "85.7", "42", "6",
		"2.50", "0.00", "0.00", "0.00", "0.00", "0.00", "", "0.0", "",
		"0.0", "rate_limited", "yes", "21", "4", "21", "2", "21", "21"},
	{"3", "198.51.100.3", "", "0", "", "85.7", "14", "2",
		"3.50", "0.00", "0.00", "0.00", "0.00", "0.00", "", "0.0", "",
		"85.7", "provisional", "undetermined", "7", "1", "7", "1", "7", "7"},
	// The silent hop: no node fields, the hop's own figures.
	{"4", "*", "", "", "", "100.0", "42", "0",
		"", "", "", "", "", "", "", "", "",
		"100.0", "measured", "", "", "", "", "", "", ""},
	{"5", "203.0.113.5", "", "64496", "EXAMPLE-A", "0.0", "42", "22",
		"5.50", "0.00", "0.00", "0.00", "0.00", "0.00", "0;2", "0.0", "",
		"0.0", "measured", "no", "21", "11", "21", "11", "21", "21"},
	{"5", "203.0.113.6", "", "0", "", "0.0", "42", "20",
		"6.50", "0.00", "0.00", "0.00", "0.00", "0.00", "1", "0.0", "",
		"0.0", "measured", "no", "21", "10", "21", "10", "21", "20"},
}

func withTarget(target string, rows [][]string) [][]string {
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, append([]string{target}, r...))
	}
	return out
}

func TestCSV_HeadlineLossAndProbeCounts(t *testing.T) {
	multiWant := [][]string{append([]string{"target"}, wantCSVHeader...)}
	multiWant = append(multiWant, withTarget("a.example", wantHeadlineRows)...)
	// bad.example failed: it has no hops and contributes no rows.
	multiWant = append(multiWant, []string{"b.example",
		"1", "198.51.100.77", "", "0", "", "50.0", "14", "7",
		"7.50", "0.00", "0.00", "0.00", "0.00", "0.00", "", "0.0", "",
		"50.0", "measured", "undetermined", "14", "7", "0", "0", "7", "7"})

	tests := []struct {
		name  string
		write func(*bytes.Buffer) error
		want  [][]string
	}{
		{
			name:  "single",
			write: func(b *bytes.Buffer) error { return WriteCSV(b, headlineReport("a.example")) },
			want:  append([][]string{wantCSVHeader}, wantHeadlineRows...),
		},
		{
			name:  "multi",
			write: func(b *bytes.Buffer) error { return WriteCSVMulti(b, headlineMulti()) },
			want:  multiWant,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tc.write(&buf); err != nil {
				t.Fatal(err)
			}
			// ReadAll also checks that every row has the header's field count.
			got, err := csv.NewReader(&buf).ReadAll()
			if err != nil {
				t.Fatalf("output is not a rectangular CSV: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("rows = %d, want %d\n%v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if !reflect.DeepEqual(got[i], tc.want[i]) {
					t.Errorf("row %d:\n got %q\nwant %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// The columns that existed before the headline columns keep their positions.
func TestCSV_ExistingColumnPositionsUnchanged(t *testing.T) {
	old := []string{
		"ttl", "ip", "hostname", "asn", "as_org",
		"loss_pct", "sent", "received",
		"avg_ms", "best_ms", "worst_ms", "last_ms", "stdev_ms", "jitter_ms",
		"flow_ids", "stability_pct", "trend",
	}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, headlineReport("a.example")); err != nil {
		t.Fatal(err)
	}
	got, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0]) <= len(old) {
		t.Fatalf("header has %d columns, want more than %d", len(got[0]), len(old))
	}
	if !reflect.DeepEqual(got[0][:len(old)], old) {
		t.Errorf("leading header columns = %q, want %q", got[0][:len(old)], old)
	}
}

// A TTL that was never probed is a placeholder: ttl and "*", nothing else.
func TestCSV_UnprobedTTLRowIsBlank(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, Report{Target: "a.example", Hops: []Hop{{TTL: 3}}}); err != nil {
		t.Fatal(err)
	}
	got, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, len(wantCSVHeader))
	want[0], want[1] = "3", "*"
	if len(got) != 2 || !reflect.DeepEqual(got[1], want) {
		t.Errorf("rows = %q, want header and %q", got, want)
	}
}

// A multi-target export in which every target failed is a header and no rows.
func TestWriteCSVMulti_FailedTargetHasNoRows(t *testing.T) {
	m := MultiReport{Targets: []Report{
		{Target: "bad.example", Error: "no such host"},
		{Target: "worse.example", Error: "trace failed"},
	}}
	var buf bytes.Buffer
	if err := WriteCSVMulti(&buf, m); err != nil {
		t.Fatal(err)
	}
	got, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0][0] != "target" {
		t.Errorf("rows = %q, want the header only", got)
	}
}

func TestFormatHeadline(t *testing.T) {
	tests := []struct {
		pct  float64
		kind string
		want string
	}{
		{0, "measured", "0.0%"},
		{12.34, "measured", "12.3%"},
		{100, "measured", "100.0%"},
		{0, "rate_limited", "~0%†"},
		{4.6, "rate_limited", "~5%†"},
		{85.7, "provisional", "85.7%?"},
		{7, "", "7.0%"}, // a report built without a kind reads as measured
	}
	for _, tc := range tests {
		if got := formatHeadline(tc.pct, tc.kind); got != tc.want {
			t.Errorf("formatHeadline(%v, %q) = %q, want %q", tc.pct, tc.kind, got, tc.want)
		}
	}
}

func TestDOT_LabelShowsHeadlineLoss(t *testing.T) {
	tests := []struct {
		name   string
		write  func(*bytes.Buffer) error
		id     func(ttl int, ip string) string
		gapID  string
		extras []string
	}{
		{
			name:  "single",
			write: func(b *bytes.Buffer) error { return WriteDOT(b, headlineReport("a.example")) },
			id: func(ttl int, ip string) string {
				return fmt.Sprintf("ttl%d_%s", ttl, strings.ReplaceAll(ip, ".", "_"))
			},
			gapID: "gap_4",
		},
		{
			name:  "multi",
			write: func(b *bytes.Buffer) error { return WriteDOTMulti(b, headlineMulti()) },
			id: func(_ int, ip string) string {
				return "ip_" + strings.ReplaceAll(ip, ".", "_")
			},
			gapID:  "gap_a_example_4",
			extras: []string{`ip_198_51_100_77 [label="198.51.100.77\\navg=7.5ms loss=50.0%"];`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tc.write(&buf); err != nil {
				t.Fatal(err)
			}
			out := buf.String()

			want := []string{
				tc.id(1, "192.0.2.1") + ` [label="192.0.2.1\\nr1.example\\navg=1.5ms loss=0.0%"];`,
				tc.id(2, "192.0.2.2") + ` [label="192.0.2.2\\navg=2.5ms loss=~0%†"];`,
				tc.id(3, "198.51.100.3") + ` [label="198.51.100.3\\navg=3.5ms loss=85.7%?"];`,
				tc.gapID + ` [label="TTL 4\\n* * *", style=dashed`,
				tc.id(5, "203.0.113.5") + ` [label="203.0.113.5\\navg=5.5ms loss=0.0%"];`,
				tc.id(5, "203.0.113.6") + ` [label="203.0.113.6\\navg=6.5ms loss=0.0%"];`,
			}
			want = append(want, tc.extras...)
			for _, s := range want {
				if !strings.Contains(out, s) {
					t.Errorf("output lacks %s\n%s", s, out)
				}
			}
			// The rate-limited hop must not show its raw TTL-probe loss.
			if strings.Contains(out, `avg=2.5ms loss=85.7`) {
				t.Errorf("rate-limited node is labelled with the raw loss\n%s", out)
			}
		})
	}
}

// clusterOrder returns the AS numbers of the cluster subgraphs in the order
// they appear in a DOT document.
func clusterOrder(t *testing.T, dot string) []int {
	t.Helper()
	var order []int
	for _, line := range strings.Split(dot, "\n") {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "subgraph cluster_AS%d {", &n); err == nil {
			order = append(order, n)
		}
	}
	return order
}

func TestDOT_ClusterOrderIsDeterministic(t *testing.T) {
	// Twelve ASes, listed out of order, one node each.
	asns := []int{64507, 64496, 64503, 64499, 64505, 64497, 64506, 64498, 64502, 64500, 64504, 64501}
	var r Report
	r.Target = "a.example"
	for i, a := range asns {
		r.Hops = append(r.Hops, Hop{TTL: i + 1, LossKind: "measured", Nodes: []Node{{
			IP: fmt.Sprintf("192.0.2.%d", i+1), ASN: a, ASOrg: fmt.Sprintf("EXAMPLE-%d", a),
		}}})
	}
	wantOrder := []int{64496, 64497, 64498, 64499, 64500, 64501, 64502, 64503, 64504, 64505, 64506, 64507}

	tests := []struct {
		name  string
		write func(*bytes.Buffer) error
	}{
		{"single", func(b *bytes.Buffer) error { return WriteDOT(b, r) }},
		{"multi", func(b *bytes.Buffer) error { return WriteDOTMulti(b, MultiReport{Targets: []Report{r}}) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var first bytes.Buffer
			if err := tc.write(&first); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 10; i++ {
				var again bytes.Buffer
				if err := tc.write(&again); err != nil {
					t.Fatal(err)
				}
				if again.String() != first.String() {
					t.Fatalf("render %d differs from the first render", i+2)
				}
			}
			if got := clusterOrder(t, first.String()); !reflect.DeepEqual(got, wantOrder) {
				t.Errorf("cluster order = %v, want ascending %v", got, wantOrder)
			}
		})
	}
}
