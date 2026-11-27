package export

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/tonhe/viaduct/internal/hop"
)

// dotIDSanitizer replaces invalid characters in DOT node IDs
var dotIDSanitizer = strings.NewReplacer(".", "_", ":", "_")

// formatHeadline renders a hop's headline loss the way the views do
// (hop.FormatLoss): "~N%†" when rate-limited, "N%?" when provisional, a plain
// "N%" when measured. kind is a Hop.LossKind; an empty kind reads as measured.
func formatHeadline(pct float64, kind string) string {
	k := hop.LossMeasured
	switch kind {
	case "rate_limited":
		k = hop.LossRateLimited
	case "provisional":
		k = hop.LossProvisional
	}
	return hop.FormatLoss(pct, k)
}

// dotEscaper escapes text for use inside a DOT double-quoted string.
// Backslashes are doubled so Graphviz does not read data as one of its label
// escapes (\n, \l, \N, ...), double quotes are escaped so they cannot end the
// string early, and raw line breaks become DOT's \n escape.
var dotEscaper = strings.NewReplacer(
	`\`, `\\`,
	`"`, `\"`,
	"\r\n", `\n`,
	"\n", `\n`,
	"\r", `\n`,
)

// dotQuote returns s as a DOT double-quoted string. Unlike Go's %q it follows
// DOT's escaping rules and leaves non-ASCII text as it is.
func dotQuote(s string) string {
	return `"` + dotEscaper.Replace(s) + `"`
}

// dotLabel returns a DOT double-quoted label whose lines are separated by
// DOT's \n line-break escape. Each line is escaped as dotQuote does.
func dotLabel(lines ...string) string {
	esc := make([]string, len(lines))
	for i, l := range lines {
		esc[i] = dotEscaper.Replace(l)
	}
	return `"` + strings.Join(esc, `\n`) + `"`
}

// dotNodeLabel is the quoted label of a responding node: IP, hostname if
// known, the node's average RTT and its hop's headline loss, one per line.
func dotNodeLabel(h Hop, n Node) string {
	lines := []string{n.IP}
	if n.Hostname != "" {
		lines = append(lines, n.Hostname)
	}
	lines = append(lines, fmt.Sprintf("avg=%.1fms loss=%s", n.RTT.Avg, formatHeadline(h.HeadlineLoss, h.LossKind)))
	return dotLabel(lines...)
}

// sortedASNs returns the keys of an ASN-keyed map in ascending order, so the
// cluster subgraphs are written in the same order on every run.
func sortedASNs[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

// firstErrWriter passes writes to w until one fails. It keeps that error and
// refuses every write after it, so a writer that prints many lines can ignore
// the result of each and return err at the end.
type firstErrWriter struct {
	w   io.Writer
	err error
}

func (f *firstErrWriter) Write(p []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	n, err := f.w.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	f.err = err
	return n, err
}

// WriteDOT writes the report as a Graphviz DOT directed graph.
// Nodes are grouped into ASN cluster subgraphs. Gap hops are
// rendered as dashed circles. Edges connect consecutive TTLs.
// A node's label carries its hop's headline loss (see formatHeadline).
// It returns the first error of out and writes nothing after it.
func WriteDOT(out io.Writer, r Report) error {
	w := &firstErrWriter{w: out}
	fmt.Fprintf(w, "digraph %s {\n", dotQuote("via → "+r.Target))
	fmt.Fprintln(w, "  rankdir=TB;")
	fmt.Fprintln(w, "  node [shape=box, style=filled, fillcolor=\"#e8e8e8\", fontname=\"monospace\", fontsize=10];")
	fmt.Fprintln(w, "  edge [fontname=\"monospace\", fontsize=9];")
	fmt.Fprintln(w)

	type nodeInfo struct {
		id    string
		label string
	}
	asnGroups := make(map[int][]nodeInfo)
	asnNames := make(map[int]string)
	var ungrouped []nodeInfo
	var gapNodes []nodeInfo

	type ttlNodes struct {
		ids []string
	}
	var orderedTTLs []ttlNodes

	for _, h := range r.Hops {
		if len(h.Nodes) == 0 {
			gapID := fmt.Sprintf("gap_%d", h.TTL)
			gapLabel := dotLabel(fmt.Sprintf("TTL %d", h.TTL), "* * *")
			gapNodes = append(gapNodes, nodeInfo{id: gapID, label: gapLabel})
			orderedTTLs = append(orderedTTLs, ttlNodes{ids: []string{gapID}})
			continue
		}

		var ids []string
		for _, n := range h.Nodes {
			nodeID := fmt.Sprintf("ttl%d_%s", h.TTL, dotIDSanitizer.Replace(n.IP))
			ni := nodeInfo{id: nodeID, label: dotNodeLabel(h, n)}
			if n.ASN > 0 {
				asnGroups[n.ASN] = append(asnGroups[n.ASN], ni)
				asnNames[n.ASN] = n.ASOrg
			} else {
				ungrouped = append(ungrouped, ni)
			}
			ids = append(ids, nodeID)
		}
		orderedTTLs = append(orderedTTLs, ttlNodes{ids: ids})
	}

	// ASN cluster subgraphs, in ascending AS order
	for _, asNum := range sortedASNs(asnGroups) {
		nodes := asnGroups[asNum]
		orgName := asnNames[asNum]
		fmt.Fprintf(w, "  subgraph cluster_AS%d {\n", asNum)
		fmt.Fprintf(w, "    label=%s;\n", dotQuote(fmt.Sprintf("AS%d (%s)", asNum, orgName)))
		fmt.Fprintln(w, "    style=dashed;")
		fmt.Fprintln(w, "    color=\"#888888\";")
		fmt.Fprintln(w, "    fontname=\"monospace\";")
		fmt.Fprintln(w, "    fontsize=11;")
		for _, n := range nodes {
			fmt.Fprintf(w, "    %s [label=%s];\n", n.id, n.label)
		}
		fmt.Fprintln(w, "  }")
		fmt.Fprintln(w)
	}

	// Ungrouped nodes (no ASN)
	for _, n := range ungrouped {
		fmt.Fprintf(w, "  %s [label=%s];\n", n.id, n.label)
	}

	// Gap hop nodes
	for _, n := range gapNodes {
		fmt.Fprintf(w, "  %s [label=%s, style=dashed, shape=ellipse, fillcolor=\"#f0f0f0\"];\n", n.id, n.label)
	}
	fmt.Fprintln(w)

	// Edges between consecutive TTLs
	for i := 1; i < len(orderedTTLs); i++ {
		prev := orderedTTLs[i-1]
		curr := orderedTTLs[i]
		for _, fromID := range prev.ids {
			for _, toID := range curr.ids {
				fmt.Fprintf(w, "  %s -> %s;\n", fromID, toID)
			}
		}
	}

	fmt.Fprintln(w, "}")
	return w.err
}
