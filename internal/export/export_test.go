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
	h1.AddSample(net.ParseIP("10.0.0.1"), 5*time.Millisecond, 0, hop.KindPrimary)
	h1.AddSample(net.ParseIP("10.0.0.1"), 6*time.Millisecond, 0, hop.KindPrimary)

	h2 := t.GetOrCreate(2)
	h2.AddSample(net.ParseIP("192.168.1.1"), 10*time.Millisecond, 0, hop.KindPrimary)
	h2.AddSample(net.ParseIP("192.168.1.2"), 12*time.Millisecond, 1, hop.KindPrimary)

	// TTL 3 = gap hop (no samples)
	t.GetOrCreate(3)

	h4 := t.GetOrCreate(4)
	h4.AddSample(net.ParseIP("8.8.8.8"), 20*time.Millisecond, 0, hop.KindPrimary)

	return t
}

func TestBuildReport(t *testing.T) {
	table := buildTestTable()
	r := BuildReport("v0.1.0", "example.com", net.ParseIP("8.8.8.8"),
		"udp", table, nil, nil, 4)

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
		"udp", table, nil, nil, 4)

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
		"udp", table, nil, nil, 4)

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
		"udp", table, nil, nil, 4)

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
// TestBuildReport_ProbeCounts: each hop exports the exact probe counts its
// loss figure is computed from, so a consumer can check them against a packet
// capture instead of trusting a percentage.
func TestBuildReport_ProbeCounts(t *testing.T) {
	table := hop.NewTable(1)
	h := table.GetOrCreate(1)
	ip := net.ParseIP("192.0.2.2")
	// Two rounds of 7 probes; 7 answered, then 4 answered.
	for _, answered := range []int{7, 4} {
		h.MarkRoundSentProbes(7)
		for i := 0; i < answered; i++ {
			h.AddSample(ip, time.Millisecond, i, hop.KindPrimary)
		}
		h.MarkRoundEnd()
	}

	r := BuildReport("v0", "198.51.100.1", net.ParseIP("198.51.100.1"), "udp", table, nil, nil, 1)
	if len(r.Hops) != 1 {
		t.Fatalf("hops = %d, want 1", len(r.Hops))
	}
	got := r.Hops[0]
	if got.ProbesSent != 14 || got.ProbesReceived != 11 {
		t.Errorf("ProbesSent, ProbesReceived = %d, %d, want 14, 11", got.ProbesSent, got.ProbesReceived)
	}
	// Sent is in probes, as ProbesSent is; rounds have their own field.
	if got.Sent != 14 || got.Rounds != 2 {
		t.Errorf("Sent, Rounds = %d, %d, want 14 (probes), 2", got.Sent, got.Rounds)
	}

	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"probes_sent": 14`, `"probes_received": 11`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("JSON missing %s", want)
		}
	}
}

// TestBuildReport_ProbesIsTotalSent: the top-level probes field is the number
// of TTL-limited probes sent to the hops in the report. It used to be whatever
// the caller passed, which was 0 in report mode (viaduct#52).
func TestBuildReport_ProbesIsTotalSent(t *testing.T) {
	table := hop.NewTable(4)
	ip := net.ParseIP("192.0.2.2")

	h1 := table.GetOrCreate(1) // 2 rounds of 7, all answered
	for i := 0; i < 2; i++ {
		h1.MarkRoundSentProbes(7)
		for j := 0; j < 7; j++ {
			h1.AddSample(ip, time.Millisecond, j, hop.KindPrimary)
		}
		h1.MarkRoundEnd()
	}
	h2 := table.GetOrCreate(2) // 2 rounds of 7, never answered: a gap hop
	for i := 0; i < 2; i++ {
		h2.MarkRoundSentProbes(7)
		h2.MarkRoundEnd()
	}
	h3 := table.GetOrCreate(3) // beyond the reported depth: not counted
	h3.MarkRoundSentProbes(7)
	h3.AddSample(ip, time.Millisecond, 0, hop.KindPrimary)
	h3.MarkRoundEnd()

	r := BuildReport("v0", "198.51.100.1", net.ParseIP("198.51.100.1"), "udp", table, nil, nil, 2)
	if r.Probes != 28 {
		t.Errorf("Probes = %d, want 28 (14 to each of the two reported hops, the silent one included)", r.Probes)
	}
	if len(r.Hops) != 2 || len(r.Hops[1].Nodes) != 0 {
		t.Fatalf("fixture: want 2 hops with a silent second hop, got %+v", r.Hops)
	}
}

// TestBuildReport_NodeProbeTypes: each node exports its per-probe-type counts
// and its rate-limit state, so a consumer can tell real loss from ICMP rate
// limiting and can check the echo tier against a capture.
func TestBuildReport_NodeProbeTypes(t *testing.T) {
	table := hop.NewTable(1)
	h := table.GetOrCreate(1)
	ip := net.ParseIP("192.0.2.2")
	// Three rounds. ICMP TTL answered once, primary answered every round;
	// two echoes sent, one answered.
	for round := 0; round < 3; round++ {
		h.MarkRoundSentProbes(2)
		if round == 0 {
			h.AddSample(ip, time.Millisecond, 0, hop.KindICMPTTL)
		}
		h.AddSample(ip, time.Millisecond, 0, hop.KindPrimary)
		if round > 0 {
			for _, n := range h.GetNodes() {
				n.MarkEchoSent()
			}
		}
		if round == 1 {
			h.AddSample(ip, time.Millisecond, 0, hop.KindEcho)
		}
		h.MarkRoundEnd()
	}

	r := BuildReport("v0", "198.51.100.1", net.ParseIP("198.51.100.1"), "udp", table, nil, nil, 1)
	n := r.Hops[0].Nodes[0]
	if n.Probes.ICMPTTL.Received != 1 {
		t.Errorf("icmp_ttl received = %d, want 1", n.Probes.ICMPTTL.Received)
	}
	if n.Probes.Primary.Received != 3 {
		t.Errorf("primary received = %d, want 3", n.Probes.Primary.Received)
	}
	if n.Probes.Echo.Sent != 2 || n.Probes.Echo.Received != 1 {
		t.Errorf("echo sent/received = %d/%d, want 2/1", n.Probes.Echo.Sent, n.Probes.Echo.Received)
	}
	if n.RateLimited != "undetermined" {
		t.Errorf("rate_limited = %q, want undetermined (too few echo samples)", n.RateLimited)
	}

	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"probes"`, `"icmp_ttl"`, `"primary"`, `"echo"`, `"rate_limited": "undetermined"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("JSON missing %s", want)
		}
	}
}

func TestBuildReport_HopWithNoNodes(t *testing.T) {
	table := hop.NewTable(2)

	// TTL 1: real hop with data
	h1 := table.GetOrCreate(1)
	h1.MarkRoundSent()
	h1.AddSample(net.ParseIP("10.0.0.1"), 5*time.Millisecond, 0, hop.KindPrimary)
	h1.MarkRoundEnd()

	// TTL 2: hop created in table but never receives any probe replies.
	// MarkRoundSent makes roundsSent non-zero so LossPercent() would be 100%.
	// This ensures the gap path (Hop{TTL: ttl}) gives zero loss, while the
	// non-gap path would give non-zero loss — making the two paths distinguishable.
	h2 := table.GetOrCreate(2)
	for i := 0; i < 10; i++ {
		h2.MarkRoundSent()
		h2.MarkRoundEnd()
	}
	// h2 has no AddSample calls → GetIP() returns nil → must be treated as gap.

	r := BuildReport("v0.1.0", "example.com", net.ParseIP("10.0.0.1"),
		"udp", table, nil, nil, 2)

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
	// A hop that was probed and never answered reports what was sent to it:
	// 10 probes, none answered, 100% loss. (It used to export all zeros while
	// the report's total counted its probes.)
	if hop2.Loss != 100 || hop2.ProbesSent != 10 || hop2.ProbesReceived != 0 || hop2.Rounds != 10 {
		t.Fatalf("silent hop: loss %.1f probes_sent %d probes_received %d rounds %d; want 100, 10, 0, 10",
			hop2.Loss, hop2.ProbesSent, hop2.ProbesReceived, hop2.Rounds)
	}
}

// A TTL with no hop in the table at all (never probed) is exported as a bare
// placeholder with zero counts.
func TestBuildReport_UnprobedTTLIsAPlaceholder(t *testing.T) {
	table := hop.NewTable(30)
	table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), time.Millisecond, 0, hop.KindPrimary)
	r := BuildReport("v0", "example.com", net.ParseIP("198.51.100.1"), "udp", table, nil, nil, 2)
	if len(r.Hops) != 2 {
		t.Fatalf("hops = %d, want 2", len(r.Hops))
	}
	if got := r.Hops[1]; got.TTL != 2 || got.ProbesSent != 0 || got.Loss != 0 || len(got.Nodes) != 0 {
		t.Errorf("unprobed TTL 2 = %+v, want a bare placeholder", got)
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
