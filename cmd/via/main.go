package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/net/icmp"

	"github.com/tonhe/viaduct/internal/asn"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/ping"
	"github.com/tonhe/viaduct/internal/probe"
	"github.com/tonhe/viaduct/internal/resolve"
	"github.com/tonhe/viaduct/internal/tui"
)

var (
	version      = "0.0.2"
	buildVersion = "dev" // injected at build time via -ldflags
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "via <target>",
		Short:   "A modern traceroute tool with real-time TUI",
		Args:    cobra.ExactArgs(1),
		Version: version + " (build " + buildVersion + ")",
		RunE:    runTrace,

		SilenceUsage: true,
	}

	rootCmd.Flags().Int("max-hops", 30, "maximum number of hops")
	rootCmd.Flags().Duration("timeout", time.Second, "per-probe timeout")
	rootCmd.Flags().DurationP("interval", "i", time.Second, "probe round interval")
	rootCmd.Flags().IntP("count", "c", 0, "number of probe rounds (0 = infinite)")
	rootCmd.Flags().IntP("first-ttl", "f", 1, "starting TTL")
	rootCmd.Flags().IntP("psize", "s", 64, "ICMP payload size in bytes")
	rootCmd.Flags().BoolP("report", "r", false, "report mode: run --count rounds (default 10), print table, exit")
	rootCmd.Flags().BoolP("no-dns", "n", false, "skip reverse DNS lookups")
	rootCmd.Flags().Int("paths", 6, "number of ECMP flow variations (1 = disable multipath)")
	rootCmd.Flags().Bool("no-paths", false, "disable ECMP multipath (shorthand for --paths 1)")
	rootCmd.Flags().StringP("protocol", "P", "udp", "probe protocol: udp, icmp, tcp, auto")
	rootCmd.Flags().IntP("port", "p", 0, "destination port override (default: protocol-specific)")
	rootCmd.Flags().Bool("no-asn", false, "skip ASN lookups")
	rootCmd.Flags().Bool("no-ping", false, "skip ping supplement for rate-limited hops")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runTrace(cmd *cobra.Command, args []string) error {
	target := args[0]

	// Resolve target to IPv4
	ips, err := net.LookupIP(target)
	if err != nil {
		return fmt.Errorf("failed to resolve %q: %w", target, err)
	}
	var targetIP net.IP
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			targetIP = ip4
			break
		}
	}
	if targetIP == nil {
		return fmt.Errorf("no IPv4 address found for %q", target)
	}

	// Permission check: try opening a raw socket
	testConn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		if os.Geteuid() != 0 {
			// Try re-exec under sudo
			if sudoErr := reExecWithSudo(); sudoErr == nil {
				return nil // parent exits cleanly
			}
		}
		return fmt.Errorf("raw socket permission denied: %w\n\nRun with sudo or set capability:\n  sudo setcap cap_net_raw+ep $(which via)", err)
	}
	testConn.Close()

	// Read flags
	maxHops, _ := cmd.Flags().GetInt("max-hops")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	interval, _ := cmd.Flags().GetDuration("interval")
	count, _ := cmd.Flags().GetInt("count")
	firstTTL, _ := cmd.Flags().GetInt("first-ttl")
	psize, _ := cmd.Flags().GetInt("psize")
	reportMode, _ := cmd.Flags().GetBool("report")
	noDNS, _ := cmd.Flags().GetBool("no-dns")
	numPaths, _ := cmd.Flags().GetInt("paths")
	noPaths, _ := cmd.Flags().GetBool("no-paths")
	if noPaths {
		numPaths = 1
	}
	protocolName, _ := cmd.Flags().GetString("protocol")
	dstPort, _ := cmd.Flags().GetInt("port")
	noASN, _ := cmd.Flags().GetBool("no-asn")
	noPing, _ := cmd.Flags().GetBool("no-ping")

	if reportMode && count == 0 {
		count = 10
	}

	// Build protocol before constructing config
	proto, err := buildProtocol(protocolName, dstPort, &numPaths)
	if err != nil {
		return err
	}

	cfg := probe.Config{
		MaxHops:     maxHops,
		FirstTTL:    firstTTL,
		Timeout:     timeout,
		ProbeDelay:  10 * time.Millisecond,
		RoundDelay:  interval,
		PayloadSize: psize,
		MaxRounds:   count,
		NoDNS:       noDNS,
		ReportMode:  reportMode,
		NumPaths:    numPaths,
		BasePort:    44000,
		Protocol:    proto,
	}

	// Determine source IP
	srcConn, err := net.Dial("udp4", targetIP.String()+":1")
	if err == nil {
		cfg.SourceIP = srcConn.LocalAddr().(*net.UDPAddr).IP
		srcConn.Close()
	}
	cfg.TargetIP = targetIP

	// Report mode: bypass TUI, run N rounds, print table, exit
	if cfg.ReportMode {
		return runReport(target, targetIP, cfg, protocolName, noASN, noPing)
	}

	// Create TUI model
	versionStr := "v" + version + " (" + buildVersion + ")"
	model := tui.New(target, targetIP, cfg, versionStr, protocolName, noASN, noPing)

	// Create bubbletea program
	p := tea.NewProgram(model, tea.WithAltScreen())

	// Wire auto-mode protocol switch callback
	cfg.OnProtocolSwitch = func(newProto string) {
		p.Send(tui.ProtocolSwitchMsg{NewProtocol: newProto})
	}

	// Create context for background goroutines
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Channels for results
	probeResults := make(chan probe.Result, 64)
	dnsResults := make(chan resolve.Result, 64)

	// Start probe tracer in background
	tracer := probe.NewTracer(targetIP, cfg)
	tracer.OnSent = model.Table()
	// Start ping supplementer
	var supplementer *ping.Supplementer
	pingResults := make(chan ping.Result, 64)
	if !noPing {
		supplementer = ping.New(time.Second)
		go func() {
			if err := supplementer.Run(ctx, pingResults); err != nil {
				// Ping socket failure is non-fatal
			}
		}()
	}

	tracer.OnRoundEnd = func() {
		p.Send(tui.RoundEndMsg{})
		if supplementer != nil {
			hops := model.Table().Snapshot()
			maxTTL := model.Table().MaxTTLSeen()
			rl := hop.DetectRateLimited(hops, maxTTL)
			hopMap := make(map[int]*hop.Hop, len(hops))
			for _, h := range hops {
				hopMap[h.TTL] = h
			}
			for ttl := range rl {
				if h, ok := hopMap[ttl]; ok {
					if ip := h.GetIP(); ip != nil {
						supplementer.Submit(ip)
					}
				}
			}
		}
	}
	model.SetTracer(tracer)
	go func() {
		if err := tracer.Run(ctx, probeResults); err != nil {
			p.Send(tui.ProbeErrorMsg{Err: err})
		}
	}()

	// Start DNS resolver in background
	go model.Resolver().Run(ctx, dnsResults)

	// Start ASN enricher in background
	asnResults := make(chan asn.Result, 64)
	if model.Enricher() != nil {
		go model.Enricher().Run(ctx, asnResults)
	}

	// Forwarding goroutine: probe results -> TUI (and submit to resolver/enricher)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-probeResults:
				p.Send(tui.HopUpdateMsg{Result: r})
				if model.Enricher() != nil {
					model.Enricher().Submit(r.IP)
				}
			}
		}
	}()

	// Forwarding goroutine: DNS results -> TUI
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-dnsResults:
				p.Send(tui.HostnameMsg{IP: r.IP, Hostname: r.Hostname})
			}
		}
	}()

	// Forwarding goroutine: ASN results -> TUI
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-asnResults:
				p.Send(tui.ASNMsg{IP: r.IP, Number: r.Info.Number, Org: r.Info.Org})
			}
		}
	}()

	// Forwarding goroutine: ping results -> TUI
	if !noPing {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case r := <-pingResults:
					p.Send(tui.PingUpdateMsg{IP: r.IP, RTT: r.RTT, Lost: r.Lost})
				}
			}
		}()
	}

	// Run TUI (blocks until quit)
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	// Stop background goroutines
	cancel()

	if m, ok := finalModel.(tui.Model); ok {
		fmt.Print(m.FinalSummary())
	}
	return nil
}

func runReport(target string, targetIP net.IP, cfg probe.Config, protocolName string, noASN bool, noPing bool) error {
	table := hop.NewTable(cfg.MaxHops)
	tracer := probe.NewTracer(targetIP, cfg)
	tracer.OnSent = table

	var resolver *resolve.Resolver
	if !cfg.NoDNS {
		resolver = resolve.New(4)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probeResults := make(chan probe.Result, 64)

	// Start DNS resolver if enabled
	if resolver != nil {
		dnsResults := make(chan resolve.Result, 64)
		go resolver.Run(ctx, dnsResults)
		// Drain DNS results to prevent channel blocking;
		// printReport reads hostnames via resolver.Lookup() cache
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-dnsResults:
				}
			}
		}()
	}

	// Start ASN enricher if enabled
	var enricher *asn.Enricher
	if !noASN {
		enricher = asn.New(4)
		asnResults := make(chan asn.Result, 64)
		go enricher.Run(ctx, asnResults)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-asnResults:
				}
			}
		}()
	}

	// Start ping supplementer
	var supplementer *ping.Supplementer
	pingResultsCh := make(chan ping.Result, 64)
	if !noPing {
		supplementer = ping.New(time.Second)
		go func() {
			supplementer.Run(ctx, pingResultsCh)
		}()
	}

	// Collect ping results
	pingStats := make(map[string]*ping.Stat)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-pingResultsCh:
				key := r.IP.String()
				stat, ok := pingStats[key]
				if !ok {
					stat = &ping.Stat{}
					pingStats[key] = stat
				}
				if r.Lost {
					stat.AddLoss()
				} else {
					stat.AddSample(r.RTT)
				}
			}
		}
	}()

	tracer.OnRoundEnd = func() {
		for _, h := range table.Snapshot() {
			h.MarkRoundEnd()
		}
		if supplementer != nil {
			hops := table.Snapshot()
			maxTTL := table.MaxTTLSeen()
			rl := hop.DetectRateLimited(hops, maxTTL)
			hopMap := make(map[int]*hop.Hop, len(hops))
			for _, h := range hops {
				hopMap[h.TTL] = h
			}
			for ttl := range rl {
				if h, ok := hopMap[ttl]; ok {
					if ip := h.GetIP(); ip != nil {
						supplementer.Submit(ip)
					}
				}
			}
		}
	}

	// Track target hit for display (atomic for cross-goroutine safety)
	var maxTTLHit int32

	// Collect probe results in a goroutine
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-probeResults:
				h := table.GetOrCreate(r.TTL)
				h.AddSample(r.IP, r.RTT, r.FlowID)
				if r.IsTarget {
					cur := atomic.LoadInt32(&maxTTLHit)
					if cur == 0 || int32(r.TTL) < cur {
						atomic.StoreInt32(&maxTTLHit, int32(r.TTL))
					}
				}
				if resolver != nil {
					resolver.Submit(r.IP)
				}
				if enricher != nil {
					enricher.Submit(r.IP)
				}
			}
		}
	}()

	// Run tracer (blocks until MaxRounds complete)
	if err := tracer.Run(ctx, probeResults); err != nil {
		return fmt.Errorf("tracer error: %w", err)
	}

	// Give DNS a moment to finish outstanding lookups and
	// allow collector to drain any buffered probe results
	time.Sleep(500 * time.Millisecond)
	cancel()
	<-collectorDone // wait for collector goroutine to exit

	printReport(target, targetIP, table, resolver, enricher, int(atomic.LoadInt32(&maxTTLHit)), cfg.NumPaths, protocolName, pingStats)
	return nil
}

func printReport(target string, targetIP net.IP, table *hop.Table, resolver *resolve.Resolver, enricher *asn.Enricher, maxTTLHit int, numPaths int, protocolName string, pingStats map[string]*ping.Stat) {
	multipath := numPaths > 1

	// Header
	protoLabel := strings.ToUpper(protocolName)
	if protocolName != "icmp" && numPaths > 1 {
		protoLabel += "/ECMP"
	}
	if multipath {
		fmt.Printf("via — %s (%s) — %s — %d flows\n", target, targetIP.String(), protoLabel, numPaths)
	} else {
		fmt.Printf("via — %s (%s) — %s\n", target, targetIP.String(), protoLabel)
	}

	// Column headers
	if multipath {
		fmt.Printf("%-4s %-18s %-22s %-20s %-8s %-5s %-8s %-8s %-8s %-8s %-8s %-8s %-5s\n",
			"#", "IP", "Hostname", "ASN", "Loss%", "Snt", "Avg", "Best", "Wrst", "StDev", "Last", "Flows", "Stab")
	} else {
		fmt.Printf("%-4s %-18s %-22s %-20s %-8s %-5s %-8s %-8s %-8s %-8s %-8s\n",
			"#", "IP", "Hostname", "ASN", "Loss%", "Snt", "Avg", "Best", "Wrst", "StDev", "Last")
	}

	// Determine max TTL to display
	maxTTL := table.MaxTTLSeen()
	if maxTTLHit > 0 && maxTTLHit < maxTTL {
		maxTTL = maxTTLHit
	}

	// Build hop map
	hops := table.Snapshot()
	hopMap := make(map[int]*hop.Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}

	rateLimited := hop.DetectRateLimited(hops, maxTTL)

	prevASN := 0

	for ttl := 1; ttl <= maxTTL; ttl++ {
		h, ok := hopMap[ttl]
		if !ok || h.GetIP() == nil {
			fmt.Printf("%-4d *\n", ttl)
			continue
		}

		if multipath && h.IsDivergent() {
			// Show one row per unique IP (PathNode) at this TTL
			// Hop-level loss on first row, "-" on subsequent (ECMP can't attribute per-node)
			nodes := h.GetNodes()
			sort.Slice(nodes, func(i, j int) bool {
				return nodes[i].GetReceived() > nodes[j].GetReceived()
			})
			hopLoss := h.LossPercent()
			sent := h.GetSent()
			nodeIdx := 0
			for _, node := range nodes {
				ip := node.GetIP()
				if ip == nil {
					continue
				}
				hostname := node.GetHostname()
				if hostname == "" && resolver != nil {
					if name, found := resolver.Lookup(ip); found && name != "" {
						hostname = name
					}
				}
				if len(hostname) > 20 {
					hostname = hostname[:20]
				}

				asnLabel := ""
				currentASN := 0
				if enricher != nil {
					if info, ok := enricher.Lookup(ip); ok {
						currentASN = info.Number
						asnLabel = asn.FormatASN(info.Number, info.Org)
					}
				}
				if len(asnLabel) > 20 {
					asnLabel = asnLabel[:20]
				}

				lossStr := "-"
				sntStr := "-"
				if nodeIdx == 0 {
					// Check for ping supplement data
					if ps, ok := pingStats[ip.String()]; ok && rateLimited[ttl] && ps.Sent > 0 {
						lossStr = fmt.Sprintf("%.1f%%†", ps.LossPercent())
						sntStr = fmt.Sprintf("%d", ps.Sent)
					} else if rateLimited[ttl] {
						lossStr = fmt.Sprintf("~%.0f%%", hopLoss)
						sntStr = fmt.Sprintf("%d", sent)
					} else {
						lossStr = fmt.Sprintf("%.1f%%", hopLoss)
						sntStr = fmt.Sprintf("%d", sent)
					}
				}

				var avg, minRTT, maxRTT, stdev, last float64
				if nodeIdx == 0 {
					if ps, ok := pingStats[ip.String()]; ok && rateLimited[ttl] && ps.Received > 0 {
						avg = float64(ps.AvgRTT().Microseconds()) / 1000.0
						minRTT = float64(ps.MinRTT.Microseconds()) / 1000.0
						maxRTT = float64(ps.MaxRTT.Microseconds()) / 1000.0
						stdev = ps.StDev()
						last = float64(ps.LastRTT.Microseconds()) / 1000.0
					} else {
						avg = float64(node.AvgRTT().Microseconds()) / 1000.0
						minRTT = float64(node.GetMinRTT().Microseconds()) / 1000.0
						maxRTT = float64(node.GetMaxRTT().Microseconds()) / 1000.0
						stdev = node.StDev()
						last = float64(node.GetLastRTT().Microseconds()) / 1000.0
					}
				} else {
					avg = float64(node.AvgRTT().Microseconds()) / 1000.0
					minRTT = float64(node.GetMinRTT().Microseconds()) / 1000.0
					maxRTT = float64(node.GetMaxRTT().Microseconds()) / 1000.0
					stdev = node.StDev()
					last = float64(node.GetLastRTT().Microseconds()) / 1000.0
				}
				flows := hop.FormatFlowIDs(node.GetFlowIDs())
				stab := fmt.Sprintf("%.0f%%", node.StabilityPercent())

				fmt.Printf("%-4d %-18s %-22s %-20s %-8s %-5s %-8s %-8s %-8s %-8s %-8s %-8s %-5s\n",
					ttl,
					ip.String(),
					hostname,
					asnLabel,
					lossStr,
					sntStr,
					fmt.Sprintf("%.1f", avg),
					fmt.Sprintf("%.1f", minRTT),
					fmt.Sprintf("%.1f", maxRTT),
					fmt.Sprintf("%.1f", stdev),
					fmt.Sprintf("%.1f", last),
					flows,
					stab,
				)
				if currentASN != 0 {
					prevASN = currentASN
				}
				nodeIdx++
			}
		} else {
			// Single-path row (or multipath but only one IP at this TTL)
			ip := h.GetIP()
			hostname := h.GetHostname()
			if hostname == "" && resolver != nil {
				if name, found := resolver.Lookup(ip); found && name != "" {
					hostname = name
				}
			}
			if len(hostname) > 20 {
				hostname = hostname[:20]
			}

			asnLabel := ""
			currentASN := 0
			if enricher != nil {
				if info, ok := enricher.Lookup(ip); ok {
					currentASN = info.Number
					asnLabel = asn.FormatASN(info.Number, info.Org)
				}
			}
			if len(asnLabel) > 20 {
				asnLabel = asnLabel[:20]
			}

			var lossStr string
			var avg, minRTT, maxRTT, stdev, last float64
			var sent int
			if ps, ok := pingStats[ip.String()]; ok && rateLimited[ttl] && ps.Sent > 0 {
				lossStr = fmt.Sprintf("%.1f%%†", ps.LossPercent())
				sent = ps.Sent
				if ps.Received > 0 {
					avg = float64(ps.AvgRTT().Microseconds()) / 1000.0
					minRTT = float64(ps.MinRTT.Microseconds()) / 1000.0
					maxRTT = float64(ps.MaxRTT.Microseconds()) / 1000.0
					stdev = ps.StDev()
					last = float64(ps.LastRTT.Microseconds()) / 1000.0
				}
			} else {
				loss := h.LossPercent()
				lossStr = fmt.Sprintf("%.1f%%", loss)
				if rateLimited[ttl] {
					lossStr = fmt.Sprintf("~%.0f%%", loss)
				}
				sent = h.GetSent()
				avg = float64(h.AvgRTT().Microseconds()) / 1000.0
				minRTT = float64(h.GetMinRTT().Microseconds()) / 1000.0
				maxRTT = float64(h.GetMaxRTT().Microseconds()) / 1000.0
				stdev = h.StDev()
				last = float64(h.GetLastRTT().Microseconds()) / 1000.0
			}

			if multipath {
				// Still show Flows and Stab columns for consistency
				nodes := h.GetNodes()
				flows := "-"
				stab := "-"
				if len(nodes) == 1 {
					flows = hop.FormatFlowIDs(nodes[0].GetFlowIDs())
					stab = fmt.Sprintf("%.0f%%", nodes[0].StabilityPercent())
				}
				fmt.Printf("%-4d %-18s %-22s %-20s %-8s %-5d %-8s %-8s %-8s %-8s %-8s %-8s %-5s\n",
					ttl,
					ip.String(),
					hostname,
					asnLabel,
					lossStr,
					sent,
					fmt.Sprintf("%.1f", avg),
					fmt.Sprintf("%.1f", minRTT),
					fmt.Sprintf("%.1f", maxRTT),
					fmt.Sprintf("%.1f", stdev),
					fmt.Sprintf("%.1f", last),
					flows,
					stab,
				)
			} else {
				fmt.Printf("%-4d %-18s %-22s %-20s %-8s %-5d %-8s %-8s %-8s %-8s %-8s\n",
					ttl,
					ip.String(),
					hostname,
					asnLabel,
					lossStr,
					sent,
					fmt.Sprintf("%.1f", avg),
					fmt.Sprintf("%.1f", minRTT),
					fmt.Sprintf("%.1f", maxRTT),
					fmt.Sprintf("%.1f", stdev),
					fmt.Sprintf("%.1f", last),
				)
			}
			if currentASN != 0 {
				prevASN = currentASN
			}
		}
	}
	_ = prevASN // report mode doesn't use color
}

func buildProtocol(protocolName string, dstPort int, numPaths *int) (probe.ProbeProtocol, error) {
	switch protocolName {
	case "icmp":
		if *numPaths > 1 {
			fmt.Fprintln(os.Stderr, "Warning: ICMP mode does not support multipath discovery, ignoring --paths")
			*numPaths = 1
		}
		return probe.NewICMPProtocol(), nil
	case "udp":
		if dstPort == 0 {
			dstPort = 33434
		}
		return probe.NewUDPProtocol(dstPort), nil
	case "tcp":
		if dstPort == 0 {
			dstPort = 443
		}
		return probe.NewTCPProtocol(dstPort), nil
	case "auto":
		if dstPort == 0 {
			dstPort = 33434
		}
		return probe.NewAutoSelector(
			probe.NewUDPProtocol(dstPort),
			probe.NewICMPProtocol(),
		), nil
	default:
		return nil, fmt.Errorf("unknown protocol: %s (valid: udp, icmp, tcp, auto)", protocolName)
	}
}

func reExecWithSudo() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command("sudo", append([]string{exe}, os.Args[1:]...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
