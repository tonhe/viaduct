package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func twoReports() MultiReport {
	return MultiReport{
		Version:   "0.3.0-test",
		Timestamp: time.Unix(0, 0).UTC(),
		Targets: []Report{
			{Version: "0.3.0-test", Target: "a.example", TargetIP: "192.0.2.1", Protocol: "udp",
				Hops: []Hop{{TTL: 1, Sent: 10, Nodes: []Node{{IP: "192.0.2.254"}}}}},
			{Version: "0.3.0-test", Target: "b.example", TargetIP: "192.0.2.2", Protocol: "udp",
				Hops: []Hop{{TTL: 1, Sent: 10, Nodes: []Node{{IP: "192.0.2.254"}}}}},
		},
	}
}

func TestWriteJSONMulti(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSONMulti(&buf, twoReports()); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Targets []json.RawMessage `json:"targets"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(out.Targets))
	}
}

func TestWriteCSVMulti_TargetColumnFirst(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSVMulti(&buf, twoReports()); err != nil {
		t.Fatal(err)
	}
	header := strings.SplitN(buf.String(), "\n", 2)[0]
	if !strings.HasPrefix(header, "target,") {
		t.Fatalf("csv header = %q, want leading target column", header)
	}
	if !strings.Contains(buf.String(), "b.example") {
		t.Fatal("csv missing second target rows")
	}
}

func TestWriteDOTMulti_SharedNodeMerged(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDOTMulti(&buf, twoReports()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	// The shared hop IP "192.0.2.254" should appear as a label in the graph.
	if strings.Count(out, "192.0.2.254") < 1 {
		t.Fatal("shared hop node missing")
	}

	// Must be a single digraph.
	if strings.Count(out, "digraph") != 1 {
		t.Fatalf("want exactly one digraph, got %d", strings.Count(out, "digraph"))
	}

	// Responding-hop node IDs are now "ip_<sanitized-ip>", not "ttl<N>_<ip>".
	// "192.0.2.254" sanitized is "192_0_2_254", so the node ID is "ip_192_0_2_254".
	// Both targets share TTL 1 with the same IP, so it must be declared exactly once.
	nodeDecls := strings.Count(out, "ip_192_0_2_254 [label=")
	if nodeDecls != 1 {
		t.Fatalf("shared node 'ip_192_0_2_254' declared %d times, want exactly 1 (merged)", nodeDecls)
	}
}

// TestWriteDOTMulti_CrossTTLMerge verifies that the same router IP reached at
// different TTLs by different targets produces a single merged node.
func TestWriteDOTMulti_CrossTTLMerge(t *testing.T) {
	// RFC 5737 documentation range: 203.0.113.7 is the shared router.
	// Target A sees it at TTL 2; target B sees it at TTL 3.
	m := MultiReport{
		Version:   "0.3.0-test",
		Timestamp: time.Unix(0, 0).UTC(),
		Targets: []Report{
			{
				Version: "0.3.0-test", Target: "alpha.example", TargetIP: "203.0.113.1", Protocol: "udp",
				Hops: []Hop{
					{TTL: 1, Sent: 10, Nodes: []Node{{IP: "203.0.113.2"}}},
					{TTL: 2, Sent: 10, Nodes: []Node{{IP: "203.0.113.7"}}},
				},
			},
			{
				Version: "0.3.0-test", Target: "beta.example", TargetIP: "203.0.113.3", Protocol: "udp",
				Hops: []Hop{
					{TTL: 1, Sent: 10, Nodes: []Node{{IP: "203.0.113.4"}}},
					{TTL: 2, Sent: 10, Nodes: []Node{{IP: "203.0.113.5"}}},
					{TTL: 3, Sent: 10, Nodes: []Node{{IP: "203.0.113.7"}}},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteDOTMulti(&buf, m); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	sharedNodeID := "ip_203_0_113_7"

	// The shared router must appear as exactly one node declaration.
	nodeDecls := strings.Count(out, sharedNodeID+" [label=")
	if nodeDecls != 1 {
		t.Fatalf("cross-TTL shared node %q declared %d times, want 1", sharedNodeID, nodeDecls)
	}

	// Both targets must reference the shared node ID in their edges.
	// Target A: ip_203_0_113_2 -> ip_203_0_113_7
	edgeA := "ip_203_0_113_2 -> " + sharedNodeID
	if !strings.Contains(out, edgeA) {
		t.Fatalf("expected edge %q from target A, not found", edgeA)
	}
	// Target B: ip_203_0_113_5 -> ip_203_0_113_7
	edgeB := "ip_203_0_113_5 -> " + sharedNodeID
	if !strings.Contains(out, edgeB) {
		t.Fatalf("expected edge %q from target B, not found", edgeB)
	}
}

// TestWriteDOTMulti_GapNodesNotMerged verifies that gap hops (* * *) at the
// same TTL from different targets produce distinct nodes. Unknown hops at the
// same depth from different traces are unrelated and must not be conflated.
func TestWriteDOTMulti_GapNodesNotMerged(t *testing.T) {
	m := MultiReport{
		Version:   "0.3.0-test",
		Timestamp: time.Unix(0, 0).UTC(),
		Targets: []Report{
			{
				Version: "0.3.0-test", Target: "x.example", TargetIP: "203.0.113.10", Protocol: "udp",
				Hops: []Hop{
					{TTL: 1, Sent: 10, Nodes: []Node{{IP: "203.0.113.11"}}},
					{TTL: 2, Sent: 10, Nodes: nil}, // gap
				},
			},
			{
				Version: "0.3.0-test", Target: "y.example", TargetIP: "203.0.113.20", Protocol: "udp",
				Hops: []Hop{
					{TTL: 1, Sent: 10, Nodes: []Node{{IP: "203.0.113.21"}}},
					{TTL: 2, Sent: 10, Nodes: nil}, // gap at same TTL
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteDOTMulti(&buf, m); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	// Each target must have its own gap node at TTL 2.
	gapX := "gap_x_example_2"
	gapY := "gap_y_example_2"
	if !strings.Contains(out, gapX) {
		t.Fatalf("expected gap node %q for x.example, not found", gapX)
	}
	if !strings.Contains(out, gapY) {
		t.Fatalf("expected gap node %q for y.example, not found", gapY)
	}
}

// TestWriteDOTMulti_ASNClusters verifies that nodes carrying ASN data are
// grouped into cluster subgraphs and that nodes without ASN are emitted
// as ungrouped. Uses RFC 5737/3849 documentation address space.
func TestWriteDOTMulti_ASNClusters(t *testing.T) {
	m := MultiReport{
		Version:   "0.3.0-test",
		Timestamp: time.Unix(0, 0).UTC(),
		Targets: []Report{
			{
				Version: "0.3.0-test", Target: "c.example", TargetIP: "203.0.113.30", Protocol: "udp",
				Hops: []Hop{
					// TTL 1: node with ASN 64496, will land in cluster_AS64496
					{TTL: 1, Sent: 10, Nodes: []Node{
						{IP: "203.0.113.31", ASN: 64496, ASOrg: "TEST-ORG"},
					}},
					// TTL 2: node with ASN 64497, will land in cluster_AS64497
					{TTL: 2, Sent: 10, Nodes: []Node{
						{IP: "203.0.113.32", ASN: 64497, ASOrg: "OTHER-ORG"},
					}},
					// TTL 3: node without ASN — ungrouped
					{TTL: 3, Sent: 10, Nodes: []Node{
						{IP: "203.0.113.33"},
					}},
				},
			},
			{
				Version: "0.3.0-test", Target: "d.example", TargetIP: "203.0.113.40", Protocol: "udp",
				Hops: []Hop{
					// TTL 1: same ASN 64496 as target c — should share the cluster subgraph
					{TTL: 1, Sent: 10, Nodes: []Node{
						{IP: "203.0.113.41", ASN: 64496, ASOrg: "TEST-ORG"},
					}},
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteDOTMulti(&buf, m); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	// cluster_AS64496 must appear exactly once (nodes from both targets share it).
	if strings.Count(out, "subgraph cluster_AS64496") != 1 {
		t.Fatalf("cluster_AS64496 must appear exactly once, got %d", strings.Count(out, "subgraph cluster_AS64496"))
	}

	// cluster_AS64497 must appear exactly once.
	if strings.Count(out, "subgraph cluster_AS64497") != 1 {
		t.Fatalf("cluster_AS64497 must appear exactly once, got %d", strings.Count(out, "subgraph cluster_AS64497"))
	}

	// The ungrouped node (no ASN) must appear outside any subgraph.
	ungroupedID := "ip_203_0_113_33"
	if !strings.Contains(out, ungroupedID) {
		t.Fatalf("ungrouped node %q not found in output", ungroupedID)
	}
	// Ungrouped nodes are indented with two spaces (not four used inside subgraphs).
	if !strings.Contains(out, "  "+ungroupedID+" [label=") {
		t.Fatalf("ungrouped node %q not emitted at top-level indentation", ungroupedID)
	}
}

// TestWriteCSVMulti_RowCountAndOrder verifies the exact row count (1 header +
// one data row per node/gap per target) and that rows appear in target order.
func TestWriteCSVMulti_RowCountAndOrder(t *testing.T) {
	// Target p: 2 hops, each with one node  => 2 data rows
	// Target q: 1 hop with 2 ECMP nodes     => 2 data rows
	// Target r: 1 hop that is a gap          => 1 data row (gap row with ip="*")
	// Total rows: 1 header + 2 + 2 + 1 = 6
	m := MultiReport{
		Version:   "0.3.0-test",
		Timestamp: time.Unix(0, 0).UTC(),
		Targets: []Report{
			{
				Version: "0.3.0-test", Target: "p.example", TargetIP: "198.51.100.1", Protocol: "udp",
				Hops: []Hop{
					{TTL: 1, Sent: 10, Nodes: []Node{{IP: "198.51.100.2"}}},
					{TTL: 2, Sent: 10, Nodes: []Node{{IP: "198.51.100.3"}}},
				},
			},
			{
				Version: "0.3.0-test", Target: "q.example", TargetIP: "198.51.100.10", Protocol: "udp",
				Hops: []Hop{
					{TTL: 1, Sent: 10, Nodes: []Node{
						{IP: "198.51.100.11"},
						{IP: "198.51.100.12"},
					}},
				},
			},
			{
				Version: "0.3.0-test", Target: "r.example", TargetIP: "198.51.100.20", Protocol: "udp",
				Hops: []Hop{
					{TTL: 1, Sent: 10, Nodes: nil}, // gap
				},
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteCSVMulti(&buf, m); err != nil {
		t.Fatal(err)
	}

	records, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	wantRows := 6 // 1 header + 5 data rows
	if len(records) != wantRows {
		t.Fatalf("csv row count = %d, want %d", len(records), wantRows)
	}

	// Header row: leading "target" column.
	if records[0][0] != "target" {
		t.Fatalf("header[0] = %q, want %q", records[0][0], "target")
	}

	// Data rows appear in target order: p.example rows first, then q.example, then r.example.
	if records[1][0] != "p.example" {
		t.Fatalf("row 1 target = %q, want p.example", records[1][0])
	}
	if records[2][0] != "p.example" {
		t.Fatalf("row 2 target = %q, want p.example", records[2][0])
	}
	if records[3][0] != "q.example" {
		t.Fatalf("row 3 target = %q, want q.example", records[3][0])
	}
	if records[4][0] != "q.example" {
		t.Fatalf("row 4 target = %q, want q.example", records[4][0])
	}
	if records[5][0] != "r.example" {
		t.Fatalf("row 5 target = %q, want r.example", records[5][0])
	}

	// The gap row for r.example must have ip column = "*".
	// Column layout: target, ttl, ip, ...
	if records[5][2] != "*" {
		t.Fatalf("gap row ip column = %q, want %q", records[5][2], "*")
	}
}

