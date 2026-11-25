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

// dotNodeLabel is the label of a responding node: IP, hostname if known, the
// node's average RTT and its hop's headline loss.
func dotNodeLabel(h Hop, n Node) string {
	label := n.IP
	if n.Hostname != "" {
		label += fmt.Sprintf("\\n%s", n.Hostname)
	}
	return label + fmt.Sprintf("\\navg=%.1fms loss=%s", n.RTT.Avg, formatHeadline(h.HeadlineLoss, h.LossKind))
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

// WriteDOT writes the report as a Graphviz DOT directed graph.
// Nodes are grouped into ASN cluster subgraphs. Gap hops are
// rendered as dashed circles. Edges connect consecutive TTLs.
// A node's label carries its hop's headline loss (see formatHeadline).
func WriteDOT(w io.Writer, r Report) error {
	fmt.Fprintf(w, "digraph %q {\n", "via → "+r.Target)
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
			gapLabel := fmt.Sprintf("TTL %d\\n* * *", h.TTL)
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
		fmt.Fprintf(w, "    label=%q;\n", fmt.Sprintf("AS%d (%s)", asNum, orgName))
		fmt.Fprintln(w, "    style=dashed;")
		fmt.Fprintln(w, "    color=\"#888888\";")
		fmt.Fprintln(w, "    fontname=\"monospace\";")
		fmt.Fprintln(w, "    fontsize=11;")
		for _, n := range nodes {
			fmt.Fprintf(w, "    %s [label=%q];\n", n.id, n.label)
		}
		fmt.Fprintln(w, "  }")
		fmt.Fprintln(w)
	}

	// Ungrouped nodes (no ASN)
	for _, n := range ungrouped {
		fmt.Fprintf(w, "  %s [label=%q];\n", n.id, n.label)
	}

	// Gap hop nodes
	for _, n := range gapNodes {
		fmt.Fprintf(w, "  %s [label=%q, style=dashed, shape=ellipse, fillcolor=\"#f0f0f0\"];\n", n.id, n.label)
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
	return nil
}
