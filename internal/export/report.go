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
	// Probes is the number of TTL-limited probes sent to the hops in the
	// report, over completed rounds: the sum of what was sent to each TTL,
	// including hops that never answered.
	Probes int   `json:"probes"`
	Hops   []Hop `json:"hops"`
	// Error is set in a multi-target report for a target that could not be
	// traced (it did not resolve, or its trace failed). Such a target has no
	// hops. A single-target run that fails prints the error and has no report.
	Error string `json:"error,omitempty"`
}

// Hop represents a single TTL in the trace.
//
// Every count is in probes. Loss is per probe: (ProbesSent - ProbesReceived)
// / ProbesSent over completed rounds. Sent equals ProbesSent; it is kept
// because it was the probe count before interleaved probes. Rounds is the
// number of rounds, as shown in the Rnd column. A hop that never answered has
// no nodes but still reports what was sent to it.
//
// HeadlineLoss is the figure the views show, and LossKind says what it is:
// "measured" (it equals Loss), "rate_limited" (the loss of direct echoes to
// the hop, because its TTL-probe loss is judged to be rate limiting), or
// "provisional" (it equals Loss, but there are not yet enough echo samples to
// say whether that loss is rate limiting).
//
// EchoSent and EchoReceived are the echo totals the verdict and the headline
// were made from: the direct echoes to the hop's nodes, without the nodes
// marked EchoIgnored and without the echoes a node ignored before it first
// answered. They are not the sum of the nodes' raw echo counts. While a hop
// keeps an earlier verdict because its totals fell below 20 samples, they are
// the totals that verdict was made from. A rate-limited HeadlineLoss is
// (EchoSent - EchoReceived) / EchoSent.
type Hop struct {
	TTL            int     `json:"ttl"`
	Nodes          []Node  `json:"nodes,omitempty"`
	Loss           float64 `json:"loss_pct"`
	HeadlineLoss   float64 `json:"headline_loss_pct"`
	LossKind       string  `json:"loss_kind,omitempty"`
	Rounds         int     `json:"rounds"`
	Sent           int     `json:"sent"`
	ProbesSent     int     `json:"probes_sent"`
	ProbesReceived int     `json:"probes_received"`
	EchoSent       int     `json:"echo_sent"`
	EchoReceived   int     `json:"echo_received"`
}

// Node represents a unique responder at a given TTL.
//
// Sent is the TTL-limited probes sent to the node's hop and Received is the
// replies this node sent, both in probes. At a hop with one node,
// 1 - Received/Sent is the hop's loss; at an ECMP hop Received/Sent is the
// node's share. Loss is the hop's raw loss, the same on each of its nodes: a
// TTL-limited probe is addressed to a hop distance, not to a node.
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

	// Probes breaks the node's replies down by probe type. RateLimited is
	// "yes", "no" or "undetermined": whether the node's TTL-probe loss is
	// judged to be ICMP rate limiting rather than real loss.
	Probes      NodeProbes `json:"probes"`
	RateLimited string     `json:"rate_limited"`

	// EchoIgnored is true for a node that is left out of its hop's echo
	// totals: it has answered none of its first 20 echoes while another node
	// of the hop answers echo. Its Probes.Echo counts are still the raw ones.
	EchoIgnored bool `json:"echo_ignored"`
}

// NodeProbes holds per-probe-type counts for one node.
type NodeProbes struct {
	ICMPTTL ProbeCount `json:"icmp_ttl"` // TTL-limited ICMP echo requests
	Primary ProbeCount `json:"primary"`  // TTL-limited UDP or TCP probes
	Echo    ProbeCount `json:"echo"`     // direct ICMP echo requests to this node
}

// ProbeCount is probes sent and replies received for one probe type, both in
// probes. For the two TTL-limited types, Sent is the probes of that type sent
// to the node's hop in the rounds the node was known, because a TTL-limited
// probe is addressed to a hop distance, not to a node; for Echo it is the
// echo requests sent to the node.
type ProbeCount struct {
	Sent     int `json:"sent"`
	Received int `json:"received"`
}

func lossKindName(k hop.LossKind) string { return k.String() }

func rateLimitName(s hop.RateLimitState) string { return s.String() }

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
	protocol string, table *hop.Table,
	resolver *resolve.Resolver, enricher *asn.Enricher,
	maxTTL int) Report {

	r := Report{
		Version:   version,
		Timestamp: time.Now(),
		Target:    target,
		TargetIP:  targetIP.String(),
		Protocol:  protocol,
	}

	hops := table.Snapshot()
	hopMap := make(map[int]*hop.Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}

	for ttl := 1; ttl <= maxTTL; ttl++ {
		h, ok := hopMap[ttl]
		if ok {
			sent, _ := h.ProbeCounts()
			r.Probes += sent // counted whether or not the hop ever answered
		}
		if !ok {
			r.Hops = append(r.Hops, Hop{TTL: ttl})
			continue
		}

		// A hop that never answered has no nodes, and still reports what was
		// sent to it: its probes are in the report's total.
		probesSent, probesRecvd := h.ProbeCounts()
		headline, lossKind := h.Loss()
		eh := Hop{
			TTL:            ttl,
			Loss:           h.LossPercent(),
			HeadlineLoss:   headline,
			LossKind:       lossKindName(lossKind),
			Rounds:         h.Rounds(),
			Sent:           probesSent,
			ProbesSent:     probesSent,
			ProbesReceived: probesRecvd,
		}
		eh.EchoSent, eh.EchoReceived = h.VerdictEchoTotals()

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
				Loss:      h.LossPercent(),
				Sent:      probesSent,
				Received:  node.GetReceived(),
				FlowIDs:   node.GetFlowIDs(),
				Stability: node.StabilityPercent(),
				Trend:     node.Trend(),
			}
			icmpTTL, primary, echo := node.ICMPTTLStats(), node.PrimaryStats(), node.EchoStats()
			en.Probes = NodeProbes{
				ICMPTTL: ProbeCount{Sent: icmpTTL.ProbesSent, Received: icmpTTL.Received},
				Primary: ProbeCount{Sent: primary.ProbesSent, Received: primary.Received},
				Echo:    ProbeCount{Sent: echo.Sent, Received: echo.Received},
			}
			// The verdict is the hop's (see hop.Hop.RateLimitState).
			en.RateLimited = rateLimitName(h.RateLimitState())
			en.EchoIgnored = h.EchoIgnored(ip)

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
