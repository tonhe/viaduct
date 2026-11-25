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
	// Added after the columns above, which keep their positions. loss_pct is
	// the raw TTL-probe loss; headline_loss_pct is the figure the views show
	// and loss_kind says what it is (measured, rate_limited, provisional).
	"headline_loss_pct", "loss_kind", "rate_limited",
	"icmp_ttl_sent", "icmp_ttl_received",
	"primary_sent", "primary_received",
	"echo_sent", "echo_received",
	// echo_sent and echo_received above are the node's raw echo counts.
	// echo_ignored marks a node left out of its hop's echo totals, and
	// hop_echo_sent and hop_echo_received are those totals, the ones the
	// verdict and the headline were made from, repeated on each node's row.
	"echo_ignored", "hop_echo_sent", "hop_echo_received",
	// error is the last column: the message for a target that could not be
	// traced, empty on every row of a target that was.
	"error",
}

// Positions in csvHeader of the hop-level columns a row without a node fills.
const (
	csvColLoss     = 5
	csvColSent     = 6
	csvColReceived = 7
	csvColHeadline = 17
	csvColLossKind = 18
	csvColHopEchoS = 27
	csvColHopEchoR = 28
)

// csvColError is the position in csvHeader of the error column.
var csvColError = len(csvHeader) - 1

// csvLossKind is the hop's loss kind; a report built without one reads as
// measured, as in formatHeadline.
func csvLossKind(h Hop) string {
	if h.LossKind == "" {
		return "measured"
	}
	return h.LossKind
}

// writeCSVRows writes one CSV row per node for r, and one row for each hop
// with no node: "*" as its ip, the node columns empty and, if probes were sent
// to it, the hop's own loss figures and probe counts.
// A report with Error set (a target that could not be traced) gets one row
// with every hop and node column empty and the message in the error column.
// If withTarget is true, r.Target is prepended as the first column.
func writeCSVRows(cw *csv.Writer, r Report, withTarget bool) error {
	if r.Error != "" {
		row := make([]string, len(csvHeader))
		row[csvColError] = r.Error
		if withTarget {
			row = append([]string{r.Target}, row...)
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	for _, h := range r.Hops {
		if len(h.Nodes) == 0 {
			row := make([]string, len(csvHeader))
			row[0] = fmt.Sprintf("%d", h.TTL)
			row[1] = "*"
			if h.ProbesSent > 0 { // a TTL never probed stays a blank placeholder
				row[csvColLoss] = fmt.Sprintf("%.1f", h.Loss)
				row[csvColSent] = fmt.Sprintf("%d", h.ProbesSent)
				row[csvColReceived] = fmt.Sprintf("%d", h.ProbesReceived)
				row[csvColHeadline] = fmt.Sprintf("%.1f", h.HeadlineLoss)
				row[csvColLossKind] = csvLossKind(h)
				row[csvColHopEchoS] = fmt.Sprintf("%d", h.EchoSent)
				row[csvColHopEchoR] = fmt.Sprintf("%d", h.EchoReceived)
			}
			if withTarget {
				row = append([]string{r.Target}, row...)
			}
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
				fmt.Sprintf("%.1f", h.HeadlineLoss),
				csvLossKind(h),
				n.RateLimited,
				fmt.Sprintf("%d", n.Probes.ICMPTTL.Sent),
				fmt.Sprintf("%d", n.Probes.ICMPTTL.Received),
				fmt.Sprintf("%d", n.Probes.Primary.Sent),
				fmt.Sprintf("%d", n.Probes.Primary.Received),
				fmt.Sprintf("%d", n.Probes.Echo.Sent),
				fmt.Sprintf("%d", n.Probes.Echo.Received),
				fmt.Sprintf("%t", n.EchoIgnored),
				fmt.Sprintf("%d", h.EchoSent),
				fmt.Sprintf("%d", h.EchoReceived),
				"", // error
			}
			if withTarget {
				row = append([]string{r.Target}, row...)
			}
			if err := cw.Write(row); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteCSV writes the report as a CSV table with one row per node, and one
// row for a hop that has no node.
func WriteCSV(w io.Writer, r Report) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	if err := cw.Write(csvHeader); err != nil {
		return err
	}

	if err := writeCSVRows(cw, r, false); err != nil {
		return err
	}

	return cw.Error()
}
