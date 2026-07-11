package export

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/tonhe/viaduct/internal/asn"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/resolve"
)

// Report captures a point-in-time trace snapshot for export.
type Report struct {
	Version   string    `json:"version"`
	Timestamp time.Time `json:"timestamp"`
	Target    string    `json:"target"`
	TargetIP  string    `json:"target_ip"`
	Protocol  string    `json:"protocol"`
	Probes    int       `json:"probes"`
	Hops      []Hop     `json:"hops"`
}

// Hop represents a single TTL in the trace.
type Hop struct {
	TTL   int     `json:"ttl"`
	Nodes []Node  `json:"nodes,omitempty"`
	Loss  float64 `json:"loss_pct"`
	Sent  int     `json:"sent"`
}

// Node represents a unique responder at a given TTL.
type Node struct {
	IP        string   `json:"ip"`
	Hostname  string   `json:"hostname,omitempty"`
	ASN       int      `json:"asn,omitempty"`
	ASOrg     string   `json:"as_org,omitempty"`
	RTT       RTTStats `json:"rtt"`
	Loss      float64  `json:"loss_pct"`
	Sent      int      `json:"sent"`
	Received  int      `json:"received"`
	FlowIDs   []int    `json:"flow_ids,omitempty"`
	Stability float64  `json:"stability_pct,omitempty"`
	Trend     string   `json:"trend,omitempty"`
}

// RTTStats holds round-trip time statistics in milliseconds.
type RTTStats struct {
	Avg    float64 `json:"avg_ms"`
	Best   float64 `json:"best_ms"`
	Worst  float64 `json:"worst_ms"`
	Last   float64 `json:"last_ms"`
	StDev  float64 `json:"stdev_ms"`
	Jitter float64 `json:"jitter_ms"`
}

func msFromDuration(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

// BuildReport constructs a Report from the current trace state.
func BuildReport(version string, target string, targetIP net.IP,
	protocol string, probeCount int, table *hop.Table,
	resolver *resolve.Resolver, enricher *asn.Enricher,
	maxTTL int) Report {

	r := Report{
		Version:   version,
		Timestamp: time.Now(),
		Target:    target,
		TargetIP:  targetIP.String(),
		Protocol:  protocol,
		Probes:    probeCount,
	}

	hops := table.Snapshot()
	hopMap := make(map[int]*hop.Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}

	for ttl := 1; ttl <= maxTTL; ttl++ {
		h, ok := hopMap[ttl]
		if !ok || h.GetIP() == nil {
			r.Hops = append(r.Hops, Hop{TTL: ttl})
			continue
		}

		eh := Hop{
			TTL:  ttl,
			Loss: h.LossPercent(),
			Sent: h.GetSent(),
		}

		for _, node := range h.GetNodes() {
			ip := node.GetIP()
			if ip == nil {
				continue
			}

			en := Node{
				IP:       ip.String(),
				Hostname: node.GetHostname(),
				RTT: RTTStats{
					Avg:    msFromDuration(node.AvgRTT()),
					Best:   msFromDuration(node.GetMinRTT()),
					Worst:  msFromDuration(node.GetMaxRTT()),
					Last:   msFromDuration(node.GetLastRTT()),
					StDev:  node.StDev(),
					Jitter: msFromDuration(node.Jitter()),
				},
				Loss:      node.LossPercent(),
				Sent:      node.GetSent(),
				Received:  node.GetReceived(),
				FlowIDs:   node.GetFlowIDs(),
				Stability: node.StabilityPercent(),
				Trend:     node.Trend(),
			}

			if en.Hostname == "" && resolver != nil {
				if name, ok := resolver.Lookup(ip); ok {
					en.Hostname = name
				}
			}

			asnNum, asnOrg := node.GetASN()
			en.ASN = asnNum
			en.ASOrg = asnOrg

			eh.Nodes = append(eh.Nodes, en)
		}

		r.Hops = append(r.Hops, eh)
	}

	return r
}

// FormatName returns a valid format name from user input, or an error.
func FormatName(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "json":
		return "json", nil
	case "csv":
		return "csv", nil
	case "dot", "graphviz":
		return "dot", nil
	default:
		return "", fmt.Errorf("unknown export format %q (valid: json, csv, dot)", s)
	}
}
