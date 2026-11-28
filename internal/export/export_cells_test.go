package export

// Exact-output tests for the writers and for BuildReport: every CSV cell by
// column name, every DOT node and edge, the hostname a report carries, and
// what a failing writer gives back. Addresses are from RFC 5737 and RFC 3849,
// AS numbers from RFC 5398.

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"net"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/dnstest"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/resolve"
)

// ---------------------------------------------------------------------------
// CSV
// ---------------------------------------------------------------------------

// parseCSV reads a CSV export into its header and one map per row, keyed by
// column name. It fails if a row does not have exactly one cell per column.
func parseCSV(t *testing.T, data []byte) (header []string, rows []map[string]string) {
	t.Helper()
	cr := csv.NewReader(bytes.NewReader(data))
	cr.FieldsPerRecord = -1 // checked below, with a message that names the row
	records, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("the export is not valid CSV: %v\n%s", err, data)
	}
	if len(records) == 0 {
		t.Fatal("the export has no header row")
	}
	header = records[0]
	for i, rec := range records[1:] {
		if len(rec) != len(header) {
			t.Fatalf("row %d has %d cells, the header has %d columns: %q", i+1, len(rec), len(header), rec)
		}
		row := make(map[string]string, len(header))
		for j, name := range header {
			row[name] = rec[j]
		}
		rows = append(rows, row)
	}
	return header, rows
}

// wantRow checks one parsed row against want, cell by cell. A column not named
// in want must be empty.
func wantRow(t *testing.T, what string, header []string, got map[string]string, want map[string]string) {
	t.Helper()
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("%s: the test names a column %q the export does not have", what, name)
		}
	}
	for _, name := range header {
		if got[name] != want[name] {
			t.Errorf("%s: column %s = %q, want %q", what, name, got[name], want[name])
		}
	}
}

// cellReport is a report in which every exported value differs from every
// other, so a value written to the wrong column cannot pass:
//
//	TTL 1  one node, every node field set
//	TTL 2  no node, probes sent: a silent hop with its own loss figures
//	TTL 3  no node, nothing sent: a placeholder
//	TTL 4  two nodes
func cellReport() Report {
	return Report{
		Version: "test", Target: "target.example", TargetIP: "192.0.2.99", Protocol: "udp",
		Hops: []Hop{
			{
				TTL: 1, Loss: 12.5, HeadlineLoss: 2.5, LossKind: "rate_limited", Rounds: 40,
				Sent: 280, ProbesSent: 280, ProbesReceived: 245,
				EchoSent: 41, EchoReceived: 39,
				// Not held, while the node below is echo_ignored: the two
				// true/false columns of its row differ.
				VerdictHeld: false, VerdictProbesSent: 270, VerdictProbesReceived: 236,
				Nodes: []Node{{
					IP: "192.0.2.1", Hostname: "one.example", ASN: 64500, ASOrg: "EXAMPLE-NET, ZZ",
					RTT:  RTTStats{Avg: 1.11, Best: 0.22, Worst: 3.33, Last: 1.44, StDev: 0.55, Jitter: 0.66},
					Loss: 12.5, Sent: 280, Received: 245,
					FlowIDs: []int{0, 2, 5}, Stability: 87.5, Trend: "degrading",
					Probes: NodeProbes{
						ICMPTTL: ProbeCount{Sent: 40, Received: 31},
						Primary: ProbeCount{Sent: 240, Received: 214},
						Echo:    ProbeCount{Sent: 43, Received: 42},
					},
					RateLimited: "yes", EchoIgnored: true,
				}},
			},
			{
				TTL: 2, Loss: 100, HeadlineLoss: 99.5, LossKind: "provisional", Rounds: 9,
				Sent: 63, ProbesSent: 63, ProbesReceived: 1,
				EchoSent: 7, EchoReceived: 6,
				VerdictHeld: true, VerdictProbesSent: 61, VerdictProbesReceived: 2,
			},
			{TTL: 3},
			{
				TTL: 4, Loss: 0, HeadlineLoss: 0, LossKind: "measured", Rounds: 40,
				Sent: 280, ProbesSent: 280, ProbesReceived: 280,
				Nodes: []Node{
					{IP: "198.51.100.4", RTT: RTTStats{Avg: 4.5}, Sent: 280, Received: 200, FlowIDs: []int{1}, RateLimited: "no"},
					{IP: "2001:db8::4", RTT: RTTStats{Avg: 4.75}, Sent: 280, Received: 80, RateLimited: "no"},
				},
			},
		},
	}
}

// The cells of each kind of row, by column name.
var (
	wantNodeRow = map[string]string{
		"ttl": "1", "ip": "192.0.2.1", "hostname": "one.example", "asn": "64500", "as_org": "EXAMPLE-NET, ZZ",
		"loss_pct": "12.5", "sent": "280", "received": "245",
		"avg_ms": "1.11", "best_ms": "0.22", "worst_ms": "3.33", "last_ms": "1.44", "stdev_ms": "0.55", "jitter_ms": "0.66",
		"flow_ids": "0;2;5", "stability_pct": "87.5", "trend": "degrading",
		"headline_loss_pct": "2.5", "loss_kind": "rate_limited", "rate_limited": "yes",
		"icmp_ttl_sent": "40", "icmp_ttl_received": "31",
		"primary_sent": "240", "primary_received": "214",
		"echo_sent": "43", "echo_received": "42",
		"echo_ignored": "true", "hop_echo_sent": "41", "hop_echo_received": "39",
		"verdict_held": "false", "verdict_probes_sent": "270", "verdict_probes_received": "236",
	}
	// A silent hop: "*" in the ip column and the hop's own figures in theirs.
	// Every node column is empty.
	wantSilentRow = map[string]string{
		"ttl": "2", "ip": "*",
		"loss_pct": "100.0", "sent": "63", "received": "1",
		"headline_loss_pct": "99.5", "loss_kind": "provisional",
		"hop_echo_sent": "7", "hop_echo_received": "6",
		"verdict_held": "true", "verdict_probes_sent": "61", "verdict_probes_received": "2",
	}
	// A TTL nothing was sent to: the TTL and the "*", nothing else.
	wantPlaceholderRow = map[string]string{"ttl": "3", "ip": "*"}
	wantFirstOfTwo     = map[string]string{
		"ttl": "4", "ip": "198.51.100.4", "asn": "0",
		"loss_pct": "0.0", "sent": "280", "received": "200",
		"avg_ms": "4.50", "best_ms": "0.00", "worst_ms": "0.00", "last_ms": "0.00", "stdev_ms": "0.00", "jitter_ms": "0.00",
		"flow_ids": "1", "stability_pct": "0.0",
		"headline_loss_pct": "0.0", "loss_kind": "measured", "rate_limited": "no",
		"icmp_ttl_sent": "0", "icmp_ttl_received": "0", "primary_sent": "0", "primary_received": "0",
		"echo_sent": "0", "echo_received": "0",
		"echo_ignored": "false", "hop_echo_sent": "0", "hop_echo_received": "0",
		"verdict_held": "false", "verdict_probes_sent": "0", "verdict_probes_received": "0",
	}
)

// withCells returns a copy of row with the given cells changed or added.
func withCells(row map[string]string, cells ...string) map[string]string {
	out := make(map[string]string, len(row)+len(cells)/2)
	for k, v := range row {
		out[k] = v
	}
	for i := 0; i+1 < len(cells); i += 2 {
		out[cells[i]] = cells[i+1]
	}
	return out
}

// Every cell of every row of a single-target export, read by column name from
// the parsed CSV: a node's row, a silent hop's row, a placeholder's row, and
// the two rows of a hop with two nodes.
func TestWriteCSV_ExactCells(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, cellReport()); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	header, rows := parseCSV(t, buf.Bytes())

	if !reflect.DeepEqual(header, wantCSVHeader) {
		t.Errorf("header:\n got  %q\n want %q", header, wantCSVHeader)
	}
	if len(rows) != 5 {
		t.Fatalf("%d rows, want 5: one per node and one per hop without a node\n%s", len(rows), buf.String())
	}
	wantRow(t, "the node's row", header, rows[0], wantNodeRow)
	wantRow(t, "the silent hop's row", header, rows[1], wantSilentRow)
	wantRow(t, "the placeholder's row", header, rows[2], wantPlaceholderRow)
	wantRow(t, "the first of two nodes", header, rows[3], wantFirstOfTwo)
	wantRow(t, "the second of two nodes", header, rows[4], withCells(wantFirstOfTwo,
		"ip", "2001:db8::4", "received", "80", "avg_ms", "4.75", "flow_ids", ""))
}

// A silent hop carries its figures from the first probe sent to it: one probe
// is enough, none is a placeholder.
func TestWriteCSV_SilentHopWithOneProbe(t *testing.T) {
	r := Report{Hops: []Hop{
		{TTL: 1, ProbesSent: 1, Loss: 100, HeadlineLoss: 100, LossKind: "provisional", VerdictProbesSent: 1},
		{TTL: 2},
	}}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, r); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	header, rows := parseCSV(t, buf.Bytes())
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	wantRow(t, "a silent hop after one probe", header, rows[0], map[string]string{
		"ttl": "1", "ip": "*",
		"loss_pct": "100.0", "sent": "1", "received": "0",
		"headline_loss_pct": "100.0", "loss_kind": "provisional",
		"hop_echo_sent": "0", "hop_echo_received": "0",
		"verdict_held": "false", "verdict_probes_sent": "1", "verdict_probes_received": "0",
	})
	wantRow(t, "a TTL nothing was sent to", header, rows[1], map[string]string{"ttl": "2", "ip": "*"})
}

// A hop with no loss kind (a report built by hand, or by an older caller)
// reads as measured, on a node's row and on a silent hop's row alike.
func TestWriteCSV_EmptyLossKindReadsAsMeasured(t *testing.T) {
	r := Report{Hops: []Hop{
		{TTL: 1, ProbesSent: 4, Nodes: []Node{{IP: "192.0.2.1"}}},
		{TTL: 2, ProbesSent: 4},
	}}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, r); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	_, rows := parseCSV(t, buf.Bytes())
	for i, row := range rows {
		if row["loss_kind"] != "measured" {
			t.Errorf("row %d: loss_kind = %q, want measured", i+1, row["loss_kind"])
		}
	}
}

// The multi-target export is the same table with the target's name in a first
// column: every other cell is where the single-target export has it, on every
// kind of row.
func TestWriteCSVMulti_ExactCells(t *testing.T) {
	second := Report{Target: "second.example", Hops: []Hop{{TTL: 1, ProbesSent: 5, ProbesReceived: 0, Loss: 100, HeadlineLoss: 100, LossKind: "measured"}}}
	failed := Report{Target: "bad.example", Error: "cannot resolve host"}
	var buf bytes.Buffer
	if err := WriteCSVMulti(&buf, MultiReport{Targets: []Report{cellReport(), second, failed}}); err != nil {
		t.Fatalf("WriteCSVMulti: %v", err)
	}
	header, rows := parseCSV(t, buf.Bytes())

	if want := append([]string{"target"}, wantCSVHeader...); !reflect.DeepEqual(header, want) {
		t.Errorf("header:\n got  %q\n want %q", header, want)
	}
	if len(rows) != 7 {
		t.Fatalf("%d rows, want 7: five for the first target, one each for the others\n%s", len(rows), buf.String())
	}
	first := "target.example"
	wantRow(t, "the node's row", header, rows[0], withCells(wantNodeRow, "target", first))
	wantRow(t, "the silent hop's row", header, rows[1], withCells(wantSilentRow, "target", first))
	wantRow(t, "the placeholder's row", header, rows[2], withCells(wantPlaceholderRow, "target", first))
	wantRow(t, "the first of two nodes", header, rows[3], withCells(wantFirstOfTwo, "target", first))
	wantRow(t, "the second target's silent hop", header, rows[5], map[string]string{
		"target": "second.example", "ttl": "1", "ip": "*",
		"loss_pct": "100.0", "sent": "5", "received": "0",
		"headline_loss_pct": "100.0", "loss_kind": "measured",
		"hop_echo_sent": "0", "hop_echo_received": "0",
		"verdict_held": "false", "verdict_probes_sent": "0", "verdict_probes_received": "0",
	})
	wantRow(t, "the failed target's row", header, rows[6], map[string]string{
		"target": "bad.example", "error": "cannot resolve host",
	})
}

// ---------------------------------------------------------------------------
// DOT
// ---------------------------------------------------------------------------

var (
	dotEdgeLine = regexp.MustCompile(`(?m)^\s*(\S+) -> (\S+);$`)
	dotNodeLine = regexp.MustCompile(`(?m)^\s*(\S+) \[label=("(?:[^"\\]|\\.)*")(.*)\];$`)
)

// dotEdges returns the edges of a DOT export as "from -> to", in file order.
func dotEdges(dot string) []string {
	var edges []string
	for _, m := range dotEdgeLine.FindAllStringSubmatch(dot, -1) {
		edges = append(edges, m[1]+" -> "+m[2])
	}
	return edges
}

// dotNodes returns the label of every node declared in a DOT export, by ID,
// with any attributes that follow the label.
func dotNodes(dot string) map[string][2]string {
	nodes := map[string][2]string{}
	for _, m := range dotNodeLine.FindAllStringSubmatch(dot, -1) {
		nodes[m[1]] = [2]string{m[2], m[3]}
	}
	return nodes
}

// Every node and every edge of a DOT export. A node's label is its address,
// its hostname if it has one, and its average with its hop's headline loss. An
// edge joins each node of a hop to each node of the next, the first hop's
// included, and a hop with no node is a node of its own.
func TestWriteDOT_NodesLabelsAndEdges(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDOT(&buf, cellReport()); err != nil {
		t.Fatalf("WriteDOT: %v", err)
	}
	dot := buf.String()

	const dashed = `, style=dashed, shape=ellipse, fillcolor="#f0f0f0"`
	wantNodes := map[string][2]string{
		// With a hostname: three lines.
		"ttl1_192_0_2_1": {`"192.0.2.1\none.example\navg=1.1ms loss=~2%†"`, ""},
		"gap_2":          {`"TTL 2\n* * *"`, dashed},
		"gap_3":          {`"TTL 3\n* * *"`, dashed},
		// Without one: two lines, no empty line between them.
		"ttl4_198_51_100_4": {`"198.51.100.4\navg=4.5ms loss=0.0%"`, ""},
		"ttl4_2001_db8__4":  {`"2001:db8::4\navg=4.8ms loss=0.0%"`, ""},
	}
	if got := dotNodes(dot); !reflect.DeepEqual(got, wantNodes) {
		for id, want := range wantNodes {
			if got[id] != want {
				t.Errorf("node %s: label and attributes = %q, want %q", id, got[id], want)
			}
		}
		for id := range got {
			if _, ok := wantNodes[id]; !ok {
				t.Errorf("unexpected node %s = %q", id, got[id])
			}
		}
	}

	wantEdges := []string{
		"ttl1_192_0_2_1 -> gap_2", // the first edge: from the first hop
		"gap_2 -> gap_3",
		"gap_3 -> ttl4_198_51_100_4",
		"gap_3 -> ttl4_2001_db8__4",
	}
	if got := dotEdges(dot); !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges:\n got  %q\n want %q", got, wantEdges)
	}

	// The one node with an AS number is in that AS's cluster; the rest are not
	// in any.
	cluster := regexp.MustCompile(`(?s)subgraph cluster_AS64500 \{\n\s*label="AS64500 \(EXAMPLE-NET, ZZ\)";.*?\n  \}`).FindString(dot)
	if cluster == "" {
		t.Fatalf("no cluster for AS64500 with its organisation as the label:\n%s", dot)
	}
	if !strings.Contains(cluster, "ttl1_192_0_2_1 [label=") {
		t.Errorf("the node of AS64500 is not declared inside its cluster:\n%s", cluster)
	}
	if n := strings.Count(dot, "subgraph cluster_"); n != 1 {
		t.Errorf("%d clusters, want 1: nodes without an AS number are not clustered", n)
	}
	for _, id := range []string{"ttl4_198_51_100_4", "ttl4_2001_db8__4", "gap_2", "gap_3"} {
		if strings.Contains(cluster, id) {
			t.Errorf("%s is inside the AS64500 cluster", id)
		}
	}
	if !strings.HasPrefix(dot, `digraph "via → target.example" {`) || !strings.HasSuffix(dot, "}\n") {
		t.Errorf("the graph does not open with its title and close with a brace:\n%s", dot)
	}
}

// Edges join every node of one hop to every node of the next: two nodes into
// three is six edges, three into one is three.
func TestWriteDOT_EdgeCountBetweenSplitHops(t *testing.T) {
	node := func(ip string) Node { return Node{IP: ip} }
	r := Report{Target: "t", Hops: []Hop{
		{TTL: 1, Nodes: []Node{node("192.0.2.1"), node("192.0.2.2")}},
		{TTL: 2, Nodes: []Node{node("198.51.100.1"), node("198.51.100.2"), node("198.51.100.3")}},
		{TTL: 3, Nodes: []Node{node("203.0.113.1")}},
	}}
	var buf bytes.Buffer
	if err := WriteDOT(&buf, r); err != nil {
		t.Fatalf("WriteDOT: %v", err)
	}
	var want []string
	for _, from := range []string{"ttl1_192_0_2_1", "ttl1_192_0_2_2"} {
		for _, to := range []string{"ttl2_198_51_100_1", "ttl2_198_51_100_2", "ttl2_198_51_100_3"} {
			want = append(want, from+" -> "+to)
		}
	}
	for _, from := range []string{"ttl2_198_51_100_1", "ttl2_198_51_100_2", "ttl2_198_51_100_3"} {
		want = append(want, from+" -> ttl3_203_0_113_1")
	}
	if got := dotEdges(buf.String()); !reflect.DeepEqual(got, want) {
		t.Errorf("edges (%d):\n got  %q\n want (%d) %q", len(got), got, len(want), want)
	}
}

// One hop has nothing to join: no edge. No hop at all is still a graph.
func TestWriteDOT_OneHopOrNoneHasNoEdge(t *testing.T) {
	for _, r := range []Report{
		{Target: "t"},
		{Target: "t", Hops: []Hop{{TTL: 1, Nodes: []Node{{IP: "192.0.2.1"}}}}},
	} {
		var buf bytes.Buffer
		if err := WriteDOT(&buf, r); err != nil {
			t.Fatalf("WriteDOT: %v", err)
		}
		if edges := dotEdges(buf.String()); len(edges) != 0 {
			t.Errorf("%d hops: edges %q, want none", len(r.Hops), edges)
		}
		if n := len(dotNodes(buf.String())); n != len(r.Hops) {
			t.Errorf("%d hops: %d nodes declared, want %d", len(r.Hops), n, len(r.Hops))
		}
	}
}

// The multi-target graph carries the hostname in a node's label too, and joins
// each target's consecutive hops, the first pair included.
func TestWriteDOTMulti_HostnameLabelAndEdges(t *testing.T) {
	a := Report{Target: "a.example", Hops: []Hop{
		{TTL: 1, LossKind: "measured", Nodes: []Node{{IP: "192.0.2.1", Hostname: "gw.example", RTT: RTTStats{Avg: 1}}}},
		{TTL: 2, LossKind: "measured", Nodes: []Node{{IP: "198.51.100.1", RTT: RTTStats{Avg: 2}}}},
	}}
	b := Report{Target: "b.example", Hops: []Hop{
		{TTL: 1, LossKind: "measured", Nodes: []Node{{IP: "192.0.2.1", Hostname: "gw.example", RTT: RTTStats{Avg: 1}}}},
		{TTL: 2, LossKind: "measured", Nodes: []Node{{IP: "203.0.113.1", RTT: RTTStats{Avg: 3}}}},
	}}
	var buf bytes.Buffer
	if err := WriteDOTMulti(&buf, MultiReport{Targets: []Report{a, b}}); err != nil {
		t.Fatalf("WriteDOTMulti: %v", err)
	}
	dot := buf.String()

	nodes := dotNodes(dot)
	if got, want := nodes["ip_192_0_2_1"][0], `"192.0.2.1\ngw.example\navg=1.0ms loss=0.0%"`; got != want {
		t.Errorf("the shared first hop's label = %s, want %s", got, want)
	}
	if got, want := nodes["ip_198_51_100_1"][0], `"198.51.100.1\navg=2.0ms loss=0.0%"`; got != want {
		t.Errorf("a node without a hostname: label = %s, want %s", got, want)
	}
	got := dotEdges(dot)
	sort.Strings(got)
	want := []string{"ip_192_0_2_1 -> ip_198_51_100_1", "ip_192_0_2_1 -> ip_203_0_113_1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges:\n got  %q\n want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// A writer that fails
// ---------------------------------------------------------------------------

// failAfter accepts limit bytes and then fails every write.
type failAfter struct {
	limit   int
	written int
}

var errDiskFull = errors.New("no space left on device")

func (f *failAfter) Write(p []byte) (int, error) {
	if f.written+len(p) > f.limit {
		return 0, errDiskFull
	}
	f.written += len(p)
	return len(p), nil
}

// bigReport is a report whose CSV is several times the CSV writer's 4 KB
// buffer, so the writer has to reach the underlying file before it is done.
func bigReport() Report {
	r := Report{Target: "target.example"}
	for ttl := 1; ttl <= 200; ttl++ {
		r.Hops = append(r.Hops, Hop{TTL: ttl, ProbesSent: 7, Nodes: []Node{{IP: "192.0.2.1", Hostname: "a-long-hostname.example"}}})
	}
	return r
}

// The JSON writers hand back the error of a writer that fails, at the first
// byte or part of the way through.
func TestWriteJSON_WriterError(t *testing.T) {
	for _, limit := range []int{0, 100} {
		if err := WriteJSON(&failAfter{limit: limit}, bigReport()); !errors.Is(err, errDiskFull) {
			t.Errorf("WriteJSON to a writer that fails after %d bytes: error = %v, want %v", limit, err, errDiskFull)
		}
		m := MultiReport{Targets: []Report{bigReport()}}
		if err := WriteJSONMulti(&failAfter{limit: limit}, m); !errors.Is(err, errDiskFull) {
			t.Errorf("WriteJSONMulti to a writer that fails after %d bytes: error = %v, want %v", limit, err, errDiskFull)
		}
	}
}

// The CSV writers hand back the error of a writer that fails while a report
// larger than their buffer is being written, whether it fails at once or after
// the first full buffer. A report smaller than the buffer reaches the writer
// only when the CSV writer flushes, and the error comes back from there too.
func TestWriteCSV_WriterErrorOnALargeReport(t *testing.T) {
	small := cellReport()
	if err := WriteCSV(&failAfter{}, small); !errors.Is(err, errDiskFull) {
		t.Errorf("WriteCSV of a small report to a failing writer: error = %v, want %v", err, errDiskFull)
	}
	if err := WriteCSVMulti(&failAfter{}, MultiReport{Targets: []Report{small}}); !errors.Is(err, errDiskFull) {
		t.Errorf("WriteCSVMulti of a small report to a failing writer: error = %v, want %v", err, errDiskFull)
	}
	for _, limit := range []int{0, 5000} {
		if err := WriteCSV(&failAfter{limit: limit}, bigReport()); !errors.Is(err, errDiskFull) {
			t.Errorf("WriteCSV to a writer that fails after %d bytes: error = %v, want %v", limit, err, errDiskFull)
		}
		m := MultiReport{Targets: []Report{bigReport(), bigReport()}}
		if err := WriteCSVMulti(&failAfter{limit: limit}, m); !errors.Is(err, errDiskFull) {
			t.Errorf("WriteCSVMulti to a writer that fails after %d bytes: error = %v, want %v", limit, err, errDiskFull)
		}
	}
}

// ---------------------------------------------------------------------------
// BuildReport: hostnames, nodes without an address, TTLs without a hop
// ---------------------------------------------------------------------------

// resolverKnowing returns a resolver that has already resolved each address in
// names to the name given. It resolves them through a DNS server on a loopback
// UDP socket that answers PTR questions from the table, with
// net.DefaultResolver pointed at it until the resolver is ready. No query
// leaves the host.
func resolverKnowing(t *testing.T, names map[string]string) *resolve.Resolver {
	t.Helper()
	ptr := map[string][]string{}
	for ip, name := range names {
		arpa, err := dnsReverse(ip)
		if err != nil {
			t.Fatal(err)
		}
		ptr[arpa] = []string{name + "."}
	}
	dns := dnstest.Start(t, dnstest.Records{PTR: ptr})
	defer dns.Close()

	r := resolve.New(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan resolve.Result, len(names))
	done := make(chan struct{})
	go func() {
		r.Run(ctx, results)
		close(done)
	}()
	for ip := range names {
		r.Submit(net.ParseIP(ip))
	}
	for range names {
		select {
		case <-results:
		case <-time.After(10 * time.Second):
			t.Fatal("the resolver did not resolve the test's addresses")
		}
	}
	cancel()
	<-done
	return r
}

// dnsReverse returns the reverse-lookup name of an IPv4 address.
func dnsReverse(ip string) (string, error) {
	v4 := net.ParseIP(ip).To4()
	if v4 == nil {
		return "", errors.New("dnsReverse: " + ip + " is not an IPv4 address")
	}
	parts := make([]string, 0, 4)
	for i := 3; i >= 0; i-- {
		parts = append(parts, strings.TrimSpace(strings.Split(net.IPv4(0, 0, 0, v4[i]).String(), ".")[3]))
	}
	return strings.Join(parts, ".") + ".in-addr.arpa.", nil
}

// A report takes a node's hostname from the node when the node has one, and
// from the resolver's cache only when it has none. A name the resolver holds
// never replaces one already set on the node.
func TestBuildReport_Hostnames(t *testing.T) {
	const (
		preset   = "192.0.2.1" // hostname set on the node; the resolver knows another
		resolved = "192.0.2.2" // no hostname on the node; the resolver knows one
		unknown  = "192.0.2.3" // no hostname anywhere
	)
	table := hop.NewTable(30)
	for i, ip := range []string{preset, resolved, unknown} {
		h := table.GetOrCreate(i + 1)
		h.MarkRoundSent()
		h.AddSample(net.ParseIP(ip), time.Millisecond, 0, hop.KindPrimary)
		h.MarkRoundEnd()
	}
	table.Get(1).SetHostname("set-on-the-node.example")

	resolver := resolverKnowing(t, map[string]string{
		preset:   "from-the-resolver.example",
		resolved: "resolved.example",
	})

	hostnames := func(r Report) []string {
		var out []string
		for _, h := range r.Hops {
			for _, n := range h.Nodes {
				out = append(out, n.Hostname)
			}
		}
		return out
	}

	with := BuildReport("test", "target.example", net.ParseIP("192.0.2.99"), "udp", table, resolver, nil, 3)
	if got, want := hostnames(with), []string{"set-on-the-node.example", "resolved.example", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("hostnames with a resolver = %q, want %q", got, want)
	}

	without := BuildReport("test", "target.example", net.ParseIP("192.0.2.99"), "udp", table, nil, nil, 3)
	if got, want := hostnames(without), []string{"set-on-the-node.example", "", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("hostnames without a resolver = %q, want %q", got, want)
	}
}

// A node with no address is left out of the report. The hop it is on is still
// there, with its counts and its other nodes.
func TestBuildReport_NodeWithoutAnAddressIsLeftOut(t *testing.T) {
	table := hop.NewTable(30)
	h := table.GetOrCreate(1)
	h.MarkRoundSentProbes(2)
	h.AddSample(nil, time.Millisecond, 0, hop.KindPrimary)
	h.AddSample(net.ParseIP("192.0.2.1"), 2*time.Millisecond, 1, hop.KindPrimary)
	h.MarkRoundEnd()
	if n := len(h.GetNodes()); n != 2 {
		t.Fatalf("the hop holds %d nodes, want 2: one of them without an address", n)
	}

	r := BuildReport("test", "target.example", net.ParseIP("192.0.2.99"), "udp", table, nil, nil, 1)
	if len(r.Hops) != 1 {
		t.Fatalf("%d hops, want 1", len(r.Hops))
	}
	got := r.Hops[0]
	if len(got.Nodes) != 1 || got.Nodes[0].IP != "192.0.2.1" {
		t.Errorf("nodes = %+v, want only 192.0.2.1", got.Nodes)
	}
	if got.ProbesSent != 2 || got.ProbesReceived != 2 {
		t.Errorf("probes sent/received = %d/%d, want 2/2: the hop's counts are not the listed nodes'", got.ProbesSent, got.ProbesReceived)
	}

	// The same hop with only the node that has no address: a hop with no node.
	only := hop.NewTable(30)
	oh := only.GetOrCreate(1)
	oh.MarkRoundSent()
	oh.AddSample(nil, time.Millisecond, 0, hop.KindPrimary)
	oh.MarkRoundEnd()
	r = BuildReport("test", "target.example", net.ParseIP("192.0.2.99"), "udp", only, nil, nil, 1)
	if len(r.Hops) != 1 || len(r.Hops[0].Nodes) != 0 || r.Hops[0].ProbesSent != 1 {
		t.Errorf("hops = %+v, want one hop with no node and one probe sent", r.Hops)
	}
}

// A TTL with no hop row is a bare placeholder: its TTL and nothing else. The
// report has one entry for every TTL up to the one asked for, in order, and
// stops there.
func TestBuildReport_TTLsWithoutAHopArePlaceholders(t *testing.T) {
	table := hop.NewTable(30)
	for _, ttl := range []int{2, 4, 5} {
		h := table.GetOrCreate(ttl)
		h.MarkRoundSent()
		h.AddSample(net.ParseIP("192.0.2.1"), time.Millisecond, 0, hop.KindPrimary)
		h.MarkRoundEnd()
	}

	r := BuildReport("test", "target.example", net.ParseIP("192.0.2.99"), "udp", table, nil, nil, 4)
	if len(r.Hops) != 4 {
		t.Fatalf("%d hops, want 4: TTLs 1 to 4, and not the hop at TTL 5", len(r.Hops))
	}
	for i, h := range r.Hops {
		if h.TTL != i+1 {
			t.Errorf("hop %d has TTL %d, want %d", i, h.TTL, i+1)
		}
	}
	for _, ttl := range []int{1, 3} {
		if got := r.Hops[ttl-1]; !reflect.DeepEqual(got, Hop{TTL: ttl}) {
			t.Errorf("TTL %d = %+v, want a bare placeholder", ttl, got)
		}
	}
	for _, ttl := range []int{2, 4} {
		if got := r.Hops[ttl-1]; len(got.Nodes) != 1 || got.ProbesSent != 1 {
			t.Errorf("TTL %d = %+v, want one node and one probe sent", ttl, got)
		}
	}
	// Probes sent to the hops in the report, and not to the one beyond it.
	if r.Probes != 2 {
		t.Errorf("Probes = %d, want 2", r.Probes)
	}

	// Nothing asked for: no hop.
	if r := BuildReport("test", "target.example", net.ParseIP("192.0.2.99"), "udp", table, nil, nil, 0); len(r.Hops) != 0 {
		t.Errorf("maxTTL 0: %d hops, want none", len(r.Hops))
	}
}
