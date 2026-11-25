package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// MultiReport wraps one Report per target. Single-target export keeps the
// v0.2.x formats untouched; these writers are used only when len > 1.
type MultiReport struct {
	Version   string    `json:"version"`
	Timestamp time.Time `json:"timestamp"`
	Targets   []Report  `json:"targets"`
}

// WriteJSONMulti writes all targets as a JSON object with a "targets" array.
// The envelope also carries version and timestamp from the MultiReport.
func WriteJSONMulti(w io.Writer, m MultiReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

// WriteCSVMulti writes all targets as a single CSV table. The column layout is
// identical to WriteCSV but with a leading "target" column. All targets share
// one header row; rows from each target follow in order. A target that could
// not be traced (Error set, no hops) contributes one row: its target name, the
// hop and node columns empty and the message in the trailing error column.
func WriteCSVMulti(w io.Writer, m MultiReport) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	header := append([]string{"target"}, csvHeader...)
	if err := cw.Write(header); err != nil {
		return err
	}

	for _, r := range m.Targets {
		if err := writeCSVRows(cw, r, true); err != nil {
			return err
		}
	}

	return cw.Error()
}

// WriteDOTMulti writes all targets as a single Graphviz DOT directed graph.
//
// Node-ID scheme:
//   - Responding hops: "ip_<sanitized-ip>" — keyed by IP only, not TTL.
//     The same router reached at different TTLs by different targets is one
//     node; edges from each target/depth converge on it. First-seen wins for
//     the label (stats may differ across targets; we pick one to avoid
//     conflicting label declarations).
//   - Gap hops (* * *): "gap_<sanitized-target>_<ttl>" — kept distinct per
//     target because two unknown hops at the same TTL from different targets
//     are unrelated; merging them would falsely imply a shared router.
func WriteDOTMulti(w io.Writer, m MultiReport) error {
	title := "via → multi-target"
	fmt.Fprintf(w, "digraph %q {\n", title)
	fmt.Fprintln(w, "  rankdir=TB;")
	fmt.Fprintln(w, "  node [shape=box, style=filled, fillcolor=\"#e8e8e8\", fontname=\"monospace\", fontsize=10];")
	fmt.Fprintln(w, "  edge [fontname=\"monospace\", fontsize=9];")
	fmt.Fprintln(w)

	type nodeInfo struct {
		id    string
		label string
		asn   int
		asOrg string
	}

	// seenNodes tracks node IDs already emitted to avoid duplicate declarations.
	seenNodes := make(map[string]bool)
	// seenEdges tracks "from -> to" pairs already emitted.
	seenEdges := make(map[string]bool)

	// asnGroups collects all nodes belonging to each ASN across all targets.
	asnGroups := make(map[int][]nodeInfo)
	asnNames := make(map[int]string)
	var ungrouped []nodeInfo
	var gapNodes []nodeInfo

	// Collect all edges from all targets.
	type edge struct{ from, to string }
	var allEdges []edge

	for _, r := range m.Targets {
		// sanitizedTarget is used to build per-target gap node IDs.
		sanitizedTarget := dotIDSanitizer.Replace(r.Target)

		type ttlNodes struct {
			ids []string
		}
		var orderedTTLs []ttlNodes

		for _, h := range r.Hops {
			if len(h.Nodes) == 0 {
				// Gap nodes are per-target+TTL: unknown hops at the same TTL
				// from different targets are unrelated and must not merge.
				// Note: dotIDSanitizer maps '.' and ':' to '_', so distinct targets could
				// theoretically collide; accepted because DNS names cannot contain '_' in practice.
				gapID := fmt.Sprintf("gap_%s_%d", sanitizedTarget, h.TTL)
				if !seenNodes[gapID] {
					seenNodes[gapID] = true
					gapLabel := fmt.Sprintf("TTL %d\\n* * *", h.TTL)
					gapNodes = append(gapNodes, nodeInfo{id: gapID, label: gapLabel})
				}
				orderedTTLs = append(orderedTTLs, ttlNodes{ids: []string{gapID}})
				continue
			}

			var ids []string
			for _, n := range h.Nodes {
				// Key responding hops by IP only (not TTL) so the same router
				// reached at different depths by different targets is one node.
				nodeID := "ip_" + dotIDSanitizer.Replace(n.IP)
				if !seenNodes[nodeID] {
					seenNodes[nodeID] = true
					// First-seen wins for the label. If another target later
					// observes the same IP with different stats, we keep the
					// label already emitted to avoid conflicting declarations.
					ni := nodeInfo{id: nodeID, label: dotNodeLabel(h, n), asn: n.ASN, asOrg: n.ASOrg}
					if n.ASN > 0 {
						asnGroups[n.ASN] = append(asnGroups[n.ASN], ni)
						asnNames[n.ASN] = n.ASOrg
					} else {
						ungrouped = append(ungrouped, ni)
					}
				}
				ids = append(ids, nodeID)
			}
			orderedTTLs = append(orderedTTLs, ttlNodes{ids: ids})
		}

		// Collect edges for this target.
		for i := 1; i < len(orderedTTLs); i++ {
			prev := orderedTTLs[i-1]
			curr := orderedTTLs[i]
			for _, fromID := range prev.ids {
				for _, toID := range curr.ids {
					key := fromID + " -> " + toID
					if !seenEdges[key] {
						seenEdges[key] = true
						allEdges = append(allEdges, edge{from: fromID, to: toID})
					}
				}
			}
		}
	}

	// Emit ASN cluster subgraphs in ascending AS order, so the output is the
	// same on every run.
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

	// Emit ungrouped nodes.
	for _, n := range ungrouped {
		fmt.Fprintf(w, "  %s [label=%q];\n", n.id, n.label)
	}

	// Emit gap nodes.
	for _, n := range gapNodes {
		fmt.Fprintf(w, "  %s [label=%q, style=dashed, shape=ellipse, fillcolor=\"#f0f0f0\"];\n", n.id, n.label)
	}
	fmt.Fprintln(w)

	// Emit edges.
	for _, e := range allEdges {
		fmt.Fprintf(w, "  %s -> %s;\n", e.from, e.to)
	}

	fmt.Fprintln(w, "}")
	return nil
}
