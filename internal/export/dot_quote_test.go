package export

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestDotQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", `""`},
		{"r1.example", `"r1.example"`},
		{`a"b`, `"a\"b"`},
		{`a\b`, `"a\\b"`},
		{`a\nb`, `"a\\nb"`},
		{"a\nb", `"a\nb"`},
		{"a\r\nb", `"a\nb"`},
		{"via → 192.0.2.1", `"via → 192.0.2.1"`},
	}
	for _, tc := range tests {
		if got := dotQuote(tc.in); got != tc.want {
			t.Errorf("dotQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestDotLabel(t *testing.T) {
	tests := []struct {
		lines []string
		want  string
	}{
		{nil, `""`},
		{[]string{"one"}, `"one"`},
		{[]string{"192.0.2.1", "avg=1.5ms loss=0.0%"}, `"192.0.2.1\navg=1.5ms loss=0.0%"`},
		{[]string{`q"uote`, `back\slash`}, `"q\"uote\nback\\slash"`},
	}
	for _, tc := range tests {
		if got := dotLabel(tc.lines...); got != tc.want {
			t.Errorf("dotLabel(%q) = %s, want %s", tc.lines, got, tc.want)
		}
	}
}

// dotWriters runs a check against both the single- and multi-target writers.
func dotWriters(r Report) []struct {
	name  string
	id    string
	write func(*bytes.Buffer) error
} {
	return []struct {
		name  string
		id    string
		write func(*bytes.Buffer) error
	}{
		{"single", "ttl1_192_0_2_1", func(b *bytes.Buffer) error { return WriteDOT(b, r) }},
		{"multi", "ip_192_0_2_1", func(b *bytes.Buffer) error { return WriteDOTMulti(b, MultiReport{Targets: []Report{r}}) }},
	}
}

// TestDOT_LabelLineBreakIsDOTEscape is the regression test for labels written
// with Go's %q, which turned the intended \n line break into \\n (a literal
// backslash followed by n in Graphviz).
func TestDOT_LabelLineBreakIsDOTEscape(t *testing.T) {
	r := Report{
		Target: "203.0.113.9",
		Hops: []Hop{{
			TTL:      1,
			LossKind: "measured",
			Nodes:    []Node{{IP: "192.0.2.1", RTT: RTTStats{Avg: 1.5}}},
		}},
	}
	for _, tc := range dotWriters(r) {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tc.write(&buf); err != nil {
				t.Fatal(err)
			}
			out := buf.String()
			want := tc.id + ` [label="192.0.2.1\navg=1.5ms loss=0.0%"];`
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %s\n%s", want, out)
			}
			if strings.Contains(out, `\\n`) {
				t.Errorf("output contains an escaped backslash before n\n%s", out)
			}
		})
	}
}

// quotedStrings returns the DOT double-quoted strings on a line, unescaped
// only as far as needed to find their ends. ok is false if a string is not
// terminated on the line.
func quotedStrings(line string) (strs []string, ok bool) {
	for i := 0; i < len(line); i++ {
		if line[i] != '"' {
			continue
		}
		var sb strings.Builder
		j := i + 1
		closed := false
		for ; j < len(line); j++ {
			if line[j] == '\\' && j+1 < len(line) {
				sb.WriteByte(line[j])
				sb.WriteByte(line[j+1])
				j++
				continue
			}
			if line[j] == '"' {
				closed = true
				break
			}
			sb.WriteByte(line[j])
		}
		if !closed {
			return strs, false
		}
		strs = append(strs, sb.String())
		i = j
	}
	return strs, true
}

func TestDOT_LabelEscapesQuotesAndBackslashes(t *testing.T) {
	host := `evil"] ; x [label="pwn\host`
	org := `Org "Quoted" \ Inc`
	r := Report{
		Target: `203.0.113.9`,
		Hops: []Hop{{
			TTL:      1,
			LossKind: "measured",
			Nodes: []Node{{
				IP: "192.0.2.1", Hostname: host,
				ASN: 64496, ASOrg: org,
				RTT: RTTStats{Avg: 1.5},
			}},
		}},
	}
	for _, tc := range dotWriters(r) {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tc.write(&buf); err != nil {
				t.Fatal(err)
			}
			out := buf.String()

			wantNode := tc.id + ` [label="192.0.2.1\nevil\"] ; x [label=\"pwn\\host\navg=1.5ms loss=0.0%"];`
			if !strings.Contains(out, wantNode) {
				t.Errorf("output lacks %s\n%s", wantNode, out)
			}
			wantCluster := `label="AS64496 (Org \"Quoted\" \\ Inc)";`
			if !strings.Contains(out, wantCluster) {
				t.Errorf("output lacks %s\n%s", wantCluster, out)
			}

			// Every quoted string ends on its own line, and the node line
			// holds exactly one: the label was not ended early.
			for _, line := range strings.Split(out, "\n") {
				strs, ok := quotedStrings(line)
				if !ok {
					t.Errorf("unterminated quoted string in %q", line)
				}
				if strings.HasPrefix(strings.TrimSpace(line), tc.id+" ") && len(strs) != 1 {
					t.Errorf("node line has %d quoted strings, want 1: %q", len(strs), line)
				}
			}
		})
	}
}

// TestDOT_RendersWithGraphviz feeds the output to Graphviz when it is
// installed, to check it parses.
func TestDOT_RendersWithGraphviz(t *testing.T) {
	dot, err := exec.LookPath("dot")
	if err != nil {
		t.Skip("graphviz dot not found in PATH; skipping render check")
	}
	r := Report{
		Target: "203.0.113.9",
		Hops: []Hop{
			{TTL: 1, LossKind: "measured", Nodes: []Node{{
				IP: "192.0.2.1", Hostname: `odd"name\host`, ASN: 64496,
				ASOrg: `Org "Q" \ Inc`, RTT: RTTStats{Avg: 1.5},
			}}},
			{TTL: 2},
			{TTL: 3, LossKind: "measured", Nodes: []Node{{IP: "2001:db8::1", RTT: RTTStats{Avg: 3}}}},
		},
	}
	for _, tc := range dotWriters(r) {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tc.write(&buf); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(dot, "-Tplain")
			cmd.Stdin = bytes.NewReader(buf.Bytes())
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			plain, err := cmd.Output()
			if err != nil {
				t.Fatalf("dot -Tplain: %v\n%s\ninput:\n%s", err, stderr.String(), buf.String())
			}
			if !strings.HasPrefix(string(plain), "graph ") {
				t.Errorf("unexpected dot -Tplain output:\n%s", plain)
			}
		})
	}
}
