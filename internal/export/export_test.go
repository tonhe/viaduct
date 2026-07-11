package export

import (
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

func buildTestTable() *hop.Table {
	t := hop.NewTable(30)

	h1 := t.GetOrCreate(1)
	h1.AddSample(net.ParseIP("10.0.0.1"), 5*time.Millisecond, 0)
	h1.AddSample(net.ParseIP("10.0.0.1"), 6*time.Millisecond, 0)

	h2 := t.GetOrCreate(2)
	h2.AddSample(net.ParseIP("192.168.1.1"), 10*time.Millisecond, 0)
	h2.AddSample(net.ParseIP("192.168.1.2"), 12*time.Millisecond, 1)

	// TTL 3 = gap hop (no samples)
	t.GetOrCreate(3)

	h4 := t.GetOrCreate(4)
	h4.AddSample(net.ParseIP("8.8.8.8"), 20*time.Millisecond, 0)

	return t
}

func TestBuildReport(t *testing.T) {
	table := buildTestTable()
	r := BuildReport("v0.1.0", "example.com", net.ParseIP("8.8.8.8"),
		"udp", 100, table, nil, nil, 4)

	if r.Version != "v0.1.0" {
		t.Errorf("version = %q, want %q", r.Version, "v0.1.0")
	}
	if r.Target != "example.com" {
		t.Errorf("target = %q, want %q", r.Target, "example.com")
	}
	if r.TargetIP != "8.8.8.8" {
		t.Errorf("targetIP = %q, want %q", r.TargetIP, "8.8.8.8")
	}
	if len(r.Hops) != 4 {
		t.Fatalf("len(hops) = %d, want 4", len(r.Hops))
	}

	// TTL 1: single node
	if len(r.Hops[0].Nodes) != 1 {
		t.Errorf("hop 1 nodes = %d, want 1", len(r.Hops[0].Nodes))
	}
	if r.Hops[0].Nodes[0].IP != "10.0.0.1" {
		t.Errorf("hop 1 IP = %q, want %q", r.Hops[0].Nodes[0].IP, "10.0.0.1")
	}

	// TTL 2: divergent (two nodes)
	if len(r.Hops[1].Nodes) != 2 {
		t.Errorf("hop 2 nodes = %d, want 2", len(r.Hops[1].Nodes))
	}

	// TTL 3: gap hop
	if len(r.Hops[2].Nodes) != 0 {
		t.Errorf("hop 3 nodes = %d, want 0", len(r.Hops[2].Nodes))
	}

	// TTL 4: target
	if len(r.Hops[3].Nodes) != 1 {
		t.Errorf("hop 4 nodes = %d, want 1", len(r.Hops[3].Nodes))
	}
	if r.Hops[3].Nodes[0].IP != "8.8.8.8" {
		t.Errorf("hop 4 IP = %q, want %q", r.Hops[3].Nodes[0].IP, "8.8.8.8")
	}
}

func TestFormatName(t *testing.T) {
	tests := []struct {
		input string
		want  string
		err   bool
	}{
		{"json", "json", false},
		{"JSON", "json", false},
		{"csv", "csv", false},
		{"dot", "dot", false},
		{"graphviz", "dot", false},
		{"xml", "", true},
	}
	for _, tc := range tests {
		got, err := FormatName(tc.input)
		if tc.err && err == nil {
			t.Errorf("FormatName(%q) expected error", tc.input)
		}
		if !tc.err && got != tc.want {
			t.Errorf("FormatName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	table := buildTestTable()
	r := BuildReport("v0.1.0", "example.com", net.ParseIP("8.8.8.8"),
		"udp", 100, table, nil, nil, 4)

	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	var decoded Report
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if decoded.Target != "example.com" {
		t.Errorf("target = %q, want %q", decoded.Target, "example.com")
	}
	if len(decoded.Hops) != 4 {
		t.Errorf("hops = %d, want 4", len(decoded.Hops))
	}
}

func TestWriteCSV(t *testing.T) {
	table := buildTestTable()
	r := BuildReport("v0.1.0", "example.com", net.ParseIP("8.8.8.8"),
		"udp", 100, table, nil, nil, 4)

	var buf bytes.Buffer
	if err := WriteCSV(&buf, r); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")

	// Header + 5 data rows (hop1=1 node, hop2=2 nodes, hop3=gap, hop4=1 node)
	if len(lines) != 6 {
		t.Errorf("CSV lines = %d, want 6 (1 header + 5 data)\n%s", len(lines), buf.String())
	}

	if !strings.HasPrefix(lines[0], "ttl,") {
		t.Errorf("CSV header = %q, expected to start with 'ttl,'", lines[0])
	}

	// Gap hop should have * for IP
	if !strings.Contains(lines[4], ",*,") {
		t.Errorf("gap hop row = %q, expected '*' for IP", lines[4])
	}
}

func TestWriteDOT(t *testing.T) {
	table := buildTestTable()
	r := BuildReport("v0.1.0", "example.com", net.ParseIP("8.8.8.8"),
		"udp", 100, table, nil, nil, 4)

	// Set ASN data for clustering test
	r.Hops[0].Nodes[0].ASN = 64496
	r.Hops[0].Nodes[0].ASOrg = "Example ISP"
	r.Hops[3].Nodes[0].ASN = 15169
	r.Hops[3].Nodes[0].ASOrg = "Google"

	var buf bytes.Buffer
	if err := WriteDOT(&buf, r); err != nil {
		t.Fatalf("WriteDOT: %v", err)
	}

	dot := buf.String()

	if !strings.HasPrefix(dot, "digraph") {
		t.Error("DOT output should start with 'digraph'")
	}
	if !strings.Contains(dot, "cluster_AS64496") {
		t.Error("DOT output should contain cluster_AS64496")
	}
	if !strings.Contains(dot, "cluster_AS15169") {
		t.Error("DOT output should contain cluster_AS15169")
	}
	if !strings.Contains(dot, "10.0.0.1") {
		t.Error("DOT output should contain 10.0.0.1")
	}
	if !strings.Contains(dot, "8.8.8.8") {
		t.Error("DOT output should contain 8.8.8.8")
	}
	if !strings.Contains(dot, "gap_3") {
		t.Error("DOT output should contain gap_3 node")
	}
	if !strings.Contains(dot, "->") {
		t.Error("DOT output should contain edges")
	}
}

// TestBuildReport_HopWithNoNodes tests that a hop in the table that has no nodes
// (GetIP() == nil) is treated as a gap hop in the exported report.
// This kills the surviving mutant: "|| → &&" in the condition
//   !ok || h.GetIP() == nil
// With the mutation, a hop that exists in the table but has no nodes would NOT
// be treated as a gap — instead it would try to iterate over empty nodes and
// produce a hop with loss/sent data but no nodes. The gap path produces
// Hop{TTL: ttl} with zero loss and zero sent, while the non-gap path would
// produce a hop with loss/sent from the Hop object.
func TestBuildReport_HopWithNoNodes(t *testing.T) {
	table := hop.NewTable(2)

	// TTL 1: real hop with data
	h1 := table.GetOrCreate(1)
	h1.AddSample(net.ParseIP("10.0.0.1"), 5*time.Millisecond, 0)
	h1.IncrementSent()

	// TTL 2: hop created in table but never receives any probe replies
	// GetOrCreate creates it; IncrementSent makes sentTotal non-zero so
	// LossPercent() would be non-zero. This ensures the gap path
	// (Hop{TTL: ttl}) gives zero loss, while the non-gap path would give
	// non-zero loss — making the two paths distinguishable.
	h2 := table.GetOrCreate(2)
	for i := 0; i < 10; i++ {
		h2.IncrementSent()
	}
	// h2 has no AddSample calls → GetIP() returns nil → must be treated as gap.

	r := BuildReport("v0.1.0", "example.com", net.ParseIP("10.0.0.1"),
		"udp", 10, table, nil, nil, 2)

	if len(r.Hops) != 2 {
		t.Fatalf("expected 2 hops, got %d", len(r.Hops))
	}

	// TTL 2 must be a gap hop: no nodes.
	hop2 := r.Hops[1]
	if hop2.TTL != 2 {
		t.Fatalf("expected TTL 2, got %d", hop2.TTL)
	}
	if len(hop2.Nodes) != 0 {
		t.Fatalf("TTL 2 should be a gap hop with no nodes, got %d nodes", len(hop2.Nodes))
	}
	// The gap path produces Hop{TTL: ttl} which has Loss=0, Sent=0.
	// If the || → && mutant survived, we'd get Loss=100% and Sent=10 here,
	// which would expose the mutation.
	if hop2.Loss != 0 {
		t.Fatalf("gap hop should have Loss=0, got %.1f (possible mutant || → &&)", hop2.Loss)
	}
}

func TestWriteDOT_IPv6NodeIDs(t *testing.T) {
	r := Report{
		Target: "ipv6.example.com",
		Hops: []Hop{
			{TTL: 1, Nodes: []Node{{IP: "2001:db8::1", Hostname: "rtr1.example.com"}}},
		},
	}
	var buf bytes.Buffer
	if err := WriteDOT(&buf, r); err != nil {
		t.Fatalf("WriteDOT: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ttl1_2001_db8__1") {
		t.Errorf("expected sanitized node ID 'ttl1_2001_db8__1' in output:\n%s", out)
	}
}
