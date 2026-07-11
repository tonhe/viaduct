package export

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

var csvHeader = []string{
	"ttl", "ip", "hostname", "asn", "as_org",
	"loss_pct", "sent", "received",
	"avg_ms", "best_ms", "worst_ms", "last_ms", "stdev_ms", "jitter_ms",
	"flow_ids", "stability_pct", "trend",
}

// WriteCSV writes the report as a CSV table with one row per node.
func WriteCSV(w io.Writer, r Report) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	if err := cw.Write(csvHeader); err != nil {
		return err
	}

	for _, h := range r.Hops {
		if len(h.Nodes) == 0 {
			row := make([]string, len(csvHeader))
			row[0] = fmt.Sprintf("%d", h.TTL)
			row[1] = "*"
			if err := cw.Write(row); err != nil {
				return err
			}
			continue
		}

		for _, n := range h.Nodes {
			flowStrs := make([]string, len(n.FlowIDs))
			for i, f := range n.FlowIDs {
				flowStrs[i] = fmt.Sprintf("%d", f)
			}

			row := []string{
				fmt.Sprintf("%d", h.TTL),
				n.IP,
				n.Hostname,
				fmt.Sprintf("%d", n.ASN),
				n.ASOrg,
				fmt.Sprintf("%.1f", n.Loss),
				fmt.Sprintf("%d", n.Sent),
				fmt.Sprintf("%d", n.Received),
				fmt.Sprintf("%.2f", n.RTT.Avg),
				fmt.Sprintf("%.2f", n.RTT.Best),
				fmt.Sprintf("%.2f", n.RTT.Worst),
				fmt.Sprintf("%.2f", n.RTT.Last),
				fmt.Sprintf("%.2f", n.RTT.StDev),
				fmt.Sprintf("%.2f", n.RTT.Jitter),
				strings.Join(flowStrs, ";"),
				fmt.Sprintf("%.1f", n.Stability),
				n.Trend,
			}
			if err := cw.Write(row); err != nil {
				return err
			}
		}
	}

	return cw.Error()
}
