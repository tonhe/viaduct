package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/net/icmp"

	"github.com/tonhe/viaduct/internal/asn"
	"github.com/tonhe/viaduct/internal/config"
	"github.com/tonhe/viaduct/internal/export"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/probe"
	"github.com/tonhe/viaduct/internal/resolve"
	"github.com/tonhe/viaduct/internal/sysprobe"
	"github.com/tonhe/viaduct/internal/theme"
	"github.com/tonhe/viaduct/internal/tui"
)

var (
	// version is injected by release builds (-ldflags "-X main.version=<tag>").
	// Left at "dev", it is replaced at startup from the build info; see version.go.
	version      = "dev"
	buildVersion = "dev" // injected at build time via -ldflags
)

// tracerIface captures the methods runReport uses on probe.Tracer.
// It is intentionally minimal — only the subset used by the report path.
type tracerIface interface {
	SetOnRoundSent(probe.RoundMarker)
	SetOnRoundEnd(func())
	SetOnEchoSent(func(ip net.IP, ttl int))
	SubmitEchoTargets([]probe.EchoTarget)
	Run(ctx context.Context, results chan<- probe.Result) error
}

// newTracer is a package-level var so tests can swap in a stub.
var newTracer = func(target net.IP, cfg probe.Config) tracerIface {
	return probe.NewTracer(target, cfg)
}

// Compile-time assertion: *probe.Tracer must satisfy cmd/via's tracerIface.
// If probe.Tracer ever loses a method this seam requires, this line fails to
// compile — catching the mismatch before it becomes a runtime panic.
var _ tracerIface = (*probe.Tracer)(nil)

// Compile-time assertion: the hop table takes the tracer's per-type report of
// probes sent. Without it the tracer falls back to a total, and a round whose
// ICMP sends failed is counted as if its first probe had been the ICMP one.
var _ probe.RoundTypeMarker = (*hop.Table)(nil)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(exitStatus(err))
	}
}

// newRootCmd builds the via command with all its flags.
func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "via [target ...]",
		Short:   "A modern traceroute tool with real-time TUI",
		Args:    cobra.RangeArgs(0, maxTargets),
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
	rootCmd.Flags().Bool("no-paths", false, "disable ECMP multipath (shorthand for --paths 1; an error with any other --paths value)")
	rootCmd.Flags().StringP("protocol", "P", "udp", "probe protocol: udp, icmp, tcp, auto")
	rootCmd.Flags().IntP("port", "p", 0, "destination port override (default: protocol-specific)")
	rootCmd.Flags().BoolP("ipv4", "4", false, "force IPv4")
	rootCmd.Flags().BoolP("ipv6", "6", false, "force IPv6")
	rootCmd.Flags().Bool("no-asn", false, "skip ASN lookups")
	rootCmd.Flags().Float64("alert-loss", 5.0, "loss% threshold for destination alert (0 = disabled)")
	rootCmd.Flags().Duration("alert-latency", 0, "latency threshold for destination alert (0 = disabled)")
	rootCmd.Flags().Int("alert-rounds", 3, "consecutive rounds before alert fires")
	rootCmd.Flags().Bool("no-alert", false, "disable alerting")
	rootCmd.Flags().String("theme", "", "color theme (slug name, e.g. 'dracula')")
	rootCmd.Flags().StringP("output", "o", "", "export format: json, csv, or dot (implies --report)")
	rootCmd.Flags().String("targets", "", "file containing one target per line (combined with positional args)")
	rootCmd.Flags().String("debug-log", "", "append diagnostics to this file: send failures, a line per round, per-hop loss snapshots, alerts")

	return rootCmd
}

// lookupFunc is a pluggable DNS resolver function, injectable for tests.
type lookupFunc func(host string) ([]net.IP, error)

// resolveTargetSpecs resolves every target and selects its IP family
// independently. Failures land in TargetSpec.Err; they never abort the
// other targets. Port/ID blocks are assigned by session index. familySrc
// names what forced the family (see familySource) and is used only in error
// messages.
func resolveTargetSpecs(targets []string, forceV4, forceV6 bool, familySrc string, hasV4, hasV6 bool, base probe.Config, protocolName string, lookup lookupFunc) []tui.TargetSpec {
	specs := make([]tui.TargetSpec, len(targets))
	for i, tgt := range targets {
		specs[i] = tui.TargetSpec{Name: tgt, ProtocolName: protocolName}
		// A zoned literal is refused before the lookup, which would return
		// the address without its zone. A link-local target is not supported
		// at all: the trace would be one hop on the local link.
		if zip, ok := zonedLiteral(tgt); ok {
			if isLinkLocalV6(zip) {
				specs[i].Err = fmt.Errorf("%s is a link-local address; link-local targets are not supported", tgt)
			} else {
				specs[i].Err = fmt.Errorf("%s is an address with a zone; zoned targets are not supported, give the address without %%zone", tgt)
			}
			continue
		}
		ips, err := lookup(tgt)
		if err != nil {
			specs[i].Err = fmt.Errorf("resolve %s: %w", tgt, err)
			continue
		}
		// Multicast is not a traceable destination. Reject it here, before any
		// socket is opened, instead of leaving it to whatever the socket layer
		// does. A name that also has unicast addresses uses those.
		ips, err = dropMulticast(tgt, ips)
		if err != nil {
			specs[i].Err = err
			continue
		}
		// The unspecified address (0.0.0.0, ::) and the IPv4 broadcast
		// address are not destinations either; 0.0.0.0 would trace localhost.
		ips, err = dropUnspecifiedBroadcast(tgt, ips)
		if err != nil {
			specs[i].Err = err
			continue
		}
		// Link-local without a zone, or a name that resolves to it. A name that
		// also has other addresses uses those.
		ips, err = dropLinkLocal(tgt, ips)
		if err != nil {
			specs[i].Err = err
			continue
		}
		// An IPv4-mapped IPv6 literal (::ffff:a.b.c.d) is the IPv4 address it
		// embeds and is traced over IPv4 (To4 below does that). Forcing IPv6
		// on one is a contradiction, so say so instead of "no AAAA record".
		if forceV6 && isIPv4MappedLiteral(tgt) {
			specs[i].Err = fmt.Errorf("%s is an IPv4-mapped address and is traced over IPv4; it cannot be used with %s", tgt, familySrc)
			continue
		}
		var v4, v6 net.IP
		for _, ip := range ips {
			if ip4 := ip.To4(); ip4 != nil && v4 == nil {
				v4 = ip4
			} else if ip.To4() == nil && v6 == nil {
				v6 = ip
			}
		}
		ver, ip, err := selectFamily(forceV4, forceV6, v4, v6, hasV4, hasV6)
		if err != nil {
			// Name the target: in multi-target mode each failure is printed
			// on its own line.
			specs[i].Err = fmt.Errorf("%s: %w", tgt, err)
			continue
		}
		// Pre-flight an IPv6 choice: a host can have an IPv6 address and still
		// no route to the target, which used to surface as a socket error
		// mid-trace. Auto-selection falls back to IPv4 when the target has an
		// A record and says so; an explicit -6 is never overridden.
		if ver == 6 {
			if rerr := routeTo(ip); rerr != nil {
				switch {
				case forceV6:
					specs[i].Err = fmt.Errorf("no IPv6 route to %s (%v); IPv6 was forced by %s, so not falling back to IPv4", ip, rerr, familySrc)
					continue
				case v4 != nil:
					specs[i].Notice = fmt.Sprintf("no IPv6 route to %s; using IPv4", ip)
					ver, ip = 4, v4
				default:
					specs[i].Err = fmt.Errorf("no IPv6 route to %s (%v), and %s has no IPv4 address", ip, rerr, tgt)
					continue
				}
			}
		}
		cfg := base
		cfg.IPVersion = ver
		cfg.BasePort, cfg.ICMPID, cfg.EchoID = sessionPorts(i)
		specs[i].IP = ip
		specs[i].Cfg = cfg
	}
	return specs
}

// resolveNumPaths decides how many ECMP flows to probe from the --paths flag
// (flagPaths, and whether it was given), --no-paths, and the config file.
// --no-paths means one flow and is an explicit choice: the config value does
// not override it. Giving it together with a --paths value other than 1 is a
// contradiction and an error.
func resolveNumPaths(flagPaths int, pathsChanged, noPaths bool, configPaths int) (int, error) {
	switch {
	case noPaths && pathsChanged && flagPaths != 1:
		return 0, fmt.Errorf("--no-paths and --paths %d contradict each other; use one of them", flagPaths)
	case noPaths:
		return 1, nil
	case pathsChanged:
		return flagPaths, nil
	default:
		return configPaths, nil
	}
}

// checkFlags reports the flag errors that can be told from the command line
// alone: --no-paths with a contradicting --paths, an unknown --output format,
// an unknown protocol. configProtocol is the protocol the config file names,
// used when -P was not given. runTrace checks each of these again where it
// uses the value; this runs first so that they are reported before anything
// that needs privileges.
func checkFlags(cmd *cobra.Command, configProtocol string) error {
	flagPaths, _ := cmd.Flags().GetInt("paths")
	noPaths, _ := cmd.Flags().GetBool("no-paths")
	if _, err := resolveNumPaths(flagPaths, cmd.Flags().Changed("paths"), noPaths, 1); err != nil {
		return err
	}
	if outputFmt, _ := cmd.Flags().GetString("output"); outputFmt != "" {
		if _, err := export.FormatName(outputFmt); err != nil {
			return err
		}
	}
	protocolName, _ := cmd.Flags().GetString("protocol")
	if !cmd.Flags().Changed("protocol") {
		protocolName = configProtocol
	}
	n := 1
	if _, err := buildProtocol(protocolName, 0, &n, probe.Config{}); err != nil {
		return err
	}
	return nil
}

// pathsClampWarning returns the warning for a flow count outside 1 to
// maxNumPaths, which is clamped, or "" when the count is in range.
func pathsClampWarning(numPaths int) string {
	switch {
	case numPaths > maxNumPaths:
		return fmt.Sprintf("Warning: --paths %d exceeds maximum %d, clamping", numPaths, maxNumPaths)
	case numPaths < 1:
		return fmt.Sprintf("Warning: --paths %d is below 1, using 1 (single path)", numPaths)
	}
	return ""
}

// routeTo reports whether this host has a route to ip. It connects a UDP
// socket, which makes the kernel do the route lookup and sends no packet. A
// variable so tests can replace it.
var routeTo = func(ip net.IP) error {
	network, addr := "udp4", ip.String()+":1"
	if ip.To4() == nil {
		network, addr = "udp6", "["+ip.String()+"]:1"
	}
	conn, err := net.Dial(network, addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

// dropMulticast removes multicast addresses (IPv4 224.0.0.0/4, IPv6 ff00::/8)
// from a target's resolved addresses. If nothing else is left, it returns an
// error naming the target.
func dropMulticast(target string, ips []net.IP) ([]net.IP, error) {
	unicast := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if !ip.IsMulticast() {
			unicast = append(unicast, ip)
		}
	}
	if len(unicast) == 0 && len(ips) > 0 {
		if net.ParseIP(target) != nil {
			return nil, fmt.Errorf("%s is a multicast address; via traces unicast targets only", target)
		}
		return nil, fmt.Errorf("%s resolves only to multicast addresses; via traces unicast targets only", target)
	}
	return unicast, nil
}

// isLinkLocalV6 reports whether ip is an IPv6 link-local unicast address
// (fe80::/10).
func isLinkLocalV6(ip net.IP) bool {
	return ip.To4() == nil && ip.IsLinkLocalUnicast()
}

// zonedLiteral reports whether s is an address literal with a zone, such as
// fe80::1%eth0 or 2001:db8::1%eth0, and returns the address without it.
func zonedLiteral(s string) (net.IP, bool) {
	addr, _, zoned := strings.Cut(s, "%")
	if !zoned {
		return nil, false
	}
	ip := net.ParseIP(addr)
	return ip, ip != nil
}

// dropUnspecifiedBroadcast removes the unspecified addresses (0.0.0.0, ::) and
// the IPv4 limited broadcast address (255.255.255.255) from a target's
// resolved addresses. If nothing else is left, it returns an error naming the
// target.
func dropUnspecifiedBroadcast(target string, ips []net.IP) ([]net.IP, error) {
	kept := make([]net.IP, 0, len(ips))
	var kind string
	for _, ip := range ips {
		switch {
		case ip.IsUnspecified():
			kind = "the unspecified address"
		case ip.Equal(net.IPv4bcast):
			kind = "the broadcast address"
		default:
			kept = append(kept, ip)
		}
	}
	if len(kept) == 0 && len(ips) > 0 {
		if net.ParseIP(target) != nil {
			return nil, fmt.Errorf("%s is %s; via traces unicast targets only", target, kind)
		}
		return nil, fmt.Errorf("%s resolves only to unspecified or broadcast addresses; via traces unicast targets only", target)
	}
	return kept, nil
}

// familySource names what forced the IP family, for error messages: the -4
// or -6 flag, or ip_family in the config file. It returns "" when no family
// was forced.
func familySource(forceV4, forceV6, fromConfig bool) string {
	var ver string
	switch {
	case forceV4:
		ver = "4"
	case forceV6:
		ver = "6"
	default:
		return ""
	}
	if fromConfig {
		return fmt.Sprintf("ip_family = %q in the config file", ver)
	}
	return "-" + ver
}

// dropLinkLocal removes IPv6 link-local addresses (fe80::/10) from a target's
// resolved addresses. If nothing else is left, it returns an error naming the
// target.
func dropLinkLocal(target string, ips []net.IP) ([]net.IP, error) {
	routable := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if !isLinkLocalV6(ip) {
			routable = append(routable, ip)
		}
	}
	if len(routable) == 0 && len(ips) > 0 {
		if net.ParseIP(target) != nil {
			return nil, fmt.Errorf("%s is a link-local address; link-local targets are not supported", target)
		}
		return nil, fmt.Errorf("%s resolves only to link-local addresses; link-local targets are not supported", target)
	}
	return routable, nil
}

// maxEchoBudget is the number of direct echoes a trace sends per round.
const maxEchoBudget = 64

// echoBudgetFor returns the per-round echo budget of each target. Report mode
// traces targets one after another, so each gets the full budget. The TUI
// traces them at once and divides it, with a floor of 4.
func echoBudgetFor(numSessions int, reportMode bool) int {
	if reportMode || numSessions <= 1 {
		return maxEchoBudget
	}
	if b := maxEchoBudget / numSessions; b > 4 {
		return b
	}
	return 4
}

// applyEchoBudget sets the echo budget on every healthy spec's config.
func applyEchoBudget(specs []tui.TargetSpec, reportMode bool) {
	numSessions := 0
	for _, sp := range specs {
		if sp.Err == nil {
			numSessions++
		}
	}
	echoBudget := echoBudgetFor(numSessions, reportMode)
	for i := range specs {
		if specs[i].Err == nil {
			specs[i].Cfg.EchoBudget = echoBudget
		}
	}
}

// isIPv4MappedLiteral reports whether s is an IPv6 literal of the form
// ::ffff:a.b.c.d (or its hex spelling), which stands for an IPv4 address.
func isIPv4MappedLiteral(s string) bool {
	if !strings.Contains(s, ":") {
		return false
	}
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

func runTrace(cmd *cobra.Command, args []string) error {
	// Load config
	appCfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: config load error: %v (using defaults)\n", err)
		appCfg = config.Default()
	}
	config.Validate(appCfg)

	// Resolve theme — CLI flag overrides config
	themeSlug, _ := cmd.Flags().GetString("theme")
	themeExplicit := cmd.Flags().Changed("theme")
	if themeSlug == "" {
		themeSlug = appCfg.Theme
	}
	t := theme.ByName(themeSlug)
	if t == nil {
		if themeExplicit {
			fmt.Fprintf(os.Stderr, "unknown theme %q\nValid themes:\n", themeSlug)
			for _, n := range theme.Names() {
				fmt.Fprintf(os.Stderr, "  %-25s %s\n", n[0], n[1])
			}
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "warning: unknown theme %q in config, using solarized-dark\n", themeSlug)
		t = theme.ByName("solarized-dark")
	}
	theme.Set(*t)

	// Read family flags early — needed before DNS resolution.
	forceV4, _ := cmd.Flags().GetBool("ipv4")
	forceV6, _ := cmd.Flags().GetBool("ipv6")
	if forceV4 && forceV6 {
		return fmt.Errorf("-4 and -6 are mutually exclusive")
	}

	// Apply config default if neither flag was set. familySrc names what
	// forced the family, so error messages blame the right thing.
	familySrc := familySource(forceV4, forceV6, false)
	if !forceV4 && !forceV6 {
		switch appCfg.IPFamily {
		case "4":
			forceV4 = true
		case "6":
			forceV6 = true
		}
		familySrc = familySource(forceV4, forceV6, true)
	}

	// Merge positional args with --targets file.
	targetsFile, _ := cmd.Flags().GetString("targets")
	var fileTargets []string
	if targetsFile != "" {
		fileTargets, err = parseTargetsFile(targetsFile)
		if err != nil {
			return err
		}
	}
	targets, err := mergeTargets(args, fileTargets)
	if err != nil {
		// mergeTargets returns descriptive errors (0 targets / >16 targets).
		return err
	}

	// Flag errors that need no socket and no lookup are reported here, before
	// the capability check: an unprivileged run must not be sent through sudo
	// only to be told its flags contradict each other.
	if err := checkFlags(cmd, appCfg.Protocol); err != nil {
		return err
	}

	// Capability check — probe raw socket. sysprobe has already probed family
	// availability; we open a test socket here to catch transient failures and
	// trigger sudo re-exec if CAP_NET_RAW is missing. We pick IPv4 by default
	// (if hasV4) so the capability check doesn't depend on target resolution.
	hasV4 := sysprobe.HasIPv4Transport()
	hasV6 := sysprobe.HasIPv6Transport()

	capFamily := 4
	if forceV6 || (!forceV4 && !hasV4 && hasV6) {
		capFamily = 6
	}
	testConn, err := icmp.ListenPacket(probe.ListenerNet(capFamily), probe.RawListenAddr(capFamily))
	if err != nil {
		// Not root: re-run under sudo and exit with that run's status.
		// Otherwise report the socket error. See rawSocketDenied.
		return rawSocketDenied(cmd, err)
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
	protocolName, _ := cmd.Flags().GetString("protocol")
	dstPort, _ := cmd.Flags().GetInt("port")
	noASN, _ := cmd.Flags().GetBool("no-asn")
	alertLoss, _ := cmd.Flags().GetFloat64("alert-loss")
	alertLatency, _ := cmd.Flags().GetDuration("alert-latency")
	alertRounds, _ := cmd.Flags().GetInt("alert-rounds")
	noAlert, _ := cmd.Flags().GetBool("no-alert")
	outputFmt, _ := cmd.Flags().GetString("output")

	// Validate and normalize output format
	if outputFmt != "" {
		normalized, err := export.FormatName(outputFmt)
		if err != nil {
			return err
		}
		outputFmt = normalized
		reportMode = true
	}

	// Apply config defaults for flags not explicitly set on CLI
	if !cmd.Flags().Changed("protocol") {
		protocolName = appCfg.Protocol
	}
	if !cmd.Flags().Changed("max-hops") {
		maxHops = appCfg.MaxHops
	}
	if !cmd.Flags().Changed("interval") {
		if d, err := time.ParseDuration(appCfg.Interval); err == nil {
			interval = d
		}
	}
	numPaths, err = resolveNumPaths(numPaths, cmd.Flags().Changed("paths"), noPaths, appCfg.Paths)
	if err != nil {
		return err
	}
	if !cmd.Flags().Changed("no-dns") {
		noDNS = !appCfg.DNSLookups
	}
	if !cmd.Flags().Changed("no-asn") {
		noASN = !appCfg.ASNLookups
	}
	if !cmd.Flags().Changed("no-alert") && !cmd.Flags().Changed("alert-loss") {
		if !appCfg.AlertEnabled {
			noAlert = true
		} else {
			alertLoss = appCfg.AlertLoss
		}
	}
	if !cmd.Flags().Changed("alert-latency") {
		if d, err := time.ParseDuration(appCfg.AlertLatency); err == nil {
			alertLatency = d
		}
	}
	if !cmd.Flags().Changed("alert-rounds") {
		alertRounds = appCfg.AlertRounds
	}

	// Clamp numPaths to the port-block stride invariant. An explicit --paths flag
	// bypasses config.Validate, so we enforce the same bounds here. Without this,
	// --paths 100 host1 host2 would let session 0's flow IDs spill into session 1's
	// port block, causing cross-target reply misattribution.
	if clamped, changed := clampNumPaths(numPaths); changed {
		fmt.Fprintln(os.Stderr, pathsClampWarning(numPaths))
		numPaths = clamped
	}

	if reportMode && count == 0 {
		count = 10
	}

	// Build base probe config (per-target port blocks and IP version filled in by resolveTargetSpecs).
	baseCfg := probe.Config{
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
	}

	// --debug-log: the probe engine's send failures and each target's round
	// records (rounds, per-hop snapshots, alerts) go to a file.
	reportAlert.loss, reportAlert.latency, reportAlert.rounds, reportAlert.off = alertLoss, alertLatency, alertRounds, noAlert
	debugLogPath, _ := cmd.Flags().GetString("debug-log")
	closeDebugLog, err := applyDebugLog(&baseCfg, debugLogPath)
	if err != nil {
		return err
	}
	defer closeDebugLog()
	if baseCfg.Debugf != nil {
		baseCfg.Debugf("via %s (%s): protocol %s, interval %v, timeout %v", displayVersion(version), buildVersion, protocolName, interval, timeout)
	}

	// Build protocol with config available
	if w := icmpPathsWarning(protocolName, numPaths, cmd.Flags().Changed("paths")); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}
	proto, err := buildProtocol(protocolName, dstPort, &numPaths, baseCfg)
	if err != nil {
		return err
	}
	baseCfg.Protocol = proto
	baseCfg.NumPaths = numPaths

	// Draw per-process random port/ID bases before resolving targets.
	initPortAllocator(len(targets))

	// Resolve all targets. hasV4/hasV6 probed once before the loop.
	specs := resolveTargetSpecs(targets, forceV4, forceV6, familySrc, hasV4, hasV6, baseCfg, protocolName, net.LookupIP)

	// The base config's protocol object is shared by every spec. Give each
	// target its own where the protocol keeps per-trace state (auto mode).
	if err := perTargetProtocols(specs, protocolName, dstPort, numPaths); err != nil {
		return err
	}

	// For single-target, preserve the original error semantics: error → exit 1.
	if len(specs) == 1 && specs[0].Err != nil {
		return specs[0].Err
	}

	// For each successfully-resolved spec, determine source IP.
	for i := range specs {
		if specs[i].Err != nil {
			continue
		}
		targetIP := specs[i].IP
		ipVersion := specs[i].Cfg.IPVersion
		udpNet := "udp4"
		dialTarget := targetIP.String() + ":1"
		if ipVersion == 6 {
			udpNet = "udp6"
			dialTarget = "[" + targetIP.String() + "]:1"
		}
		srcConn, dialErr := net.Dial(udpNet, dialTarget)
		if dialErr == nil {
			specs[i].Cfg.SourceIP = srcConn.LocalAddr().(*net.UDPAddr).IP
			srcConn.Close()
		}
		specs[i].Cfg.TargetIP = targetIP
	}

	// Report mode: bypass TUI, run N rounds per target, print/export, exit.
	// Stamp echo budget on healthy specs first (mirrors the TUI path below).
	if reportMode {
		applyEchoBudget(specs, true)
		return runMultiReport(specs, protocolName, noASN, outputFmt)
	}

	// Check that at least one spec is healthy before starting TUI.
	allErrored := true
	for _, sp := range specs {
		if sp.Err == nil {
			allErrored = false
			break
		}
	}
	if allErrored {
		for _, sp := range specs {
			fmt.Fprintf(os.Stderr, "error: %v\n", sp.Err)
		}
		os.Exit(1)
	}

	// Compute per-session echo budget and apply to each healthy spec's config.
	applyEchoBudget(specs, false)

	// Create TUI model using NewMulti (works for 1 or N sessions).
	versionStr := displayVersion(version) + " (" + buildVersion + ")"
	model := tui.NewMulti(specs, versionStr, noASN, alertLoss, alertLatency, alertRounds, noAlert, appCfg)
	model.SetDebugLog(baseCfg.Debugf)

	// Create bubbletea program AFTER model is fully wired.
	// Note: model is passed by value into tea.NewProgram; SetSessionTracer must
	// be called on the model value before it is handed to the program.
	// We create p here but wire tracers first via model pointer methods.
	p := tea.NewProgram(model, tea.WithAltScreen())

	// Create context for background goroutines
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Shared DNS/ASN channels (one resolver/enricher for all sessions).
	dnsResults := make(chan resolve.Result, 64)

	// Per-session wiring.
	for i, sp := range specs {
		if sp.Err != nil {
			// Errored sessions: no tracer, no pumps — the TUI shows the error row.
			continue
		}

		// Wire the protocol-switch callback into the config before creating the tracer.
		// The tracer consumes cfg at construction time (copies the struct).
		sessionIdx := i
		specs[i].Cfg.OnProtocolSwitch = func(newProto string) {
			p.Send(tui.ProtocolSwitchMsg{TargetIdx: sessionIdx, NewProtocol: newProto})
		}

		tracer := newTracer(sp.IP, specs[i].Cfg)
		// SetSessionTracer needs *probe.Tracer for direct field access (Paused).
		// The compile-time guard above ensures the default newTracer always returns
		// a *probe.Tracer; if a test stub reaches this branch, fail fast and clearly.
		pt, ok := tracer.(*probe.Tracer)
		if !ok {
			return fmt.Errorf("internal error: newTracer returned %T, want *probe.Tracer", tracer)
		}
		model.SetSessionTracer(i, pt)
		tracer.SetOnRoundSent(model.SessionTable(i))
		pt.SetOnEchoSent(func(ip net.IP, ttl int) {
			p.Send(tui.EchoSentMsg{TargetIdx: sessionIdx, IP: ip, TTL: ttl})
		})

		tracer.SetOnRoundEnd(func() {
			p.Send(tui.RoundEndMsg{TargetIdx: sessionIdx})
		})

		// Per-session probe results channel.
		probeResults := make(chan probe.Result, 64)
		go func(idx int, tr tracerIface) {
			if runErr := tr.Run(ctx, probeResults); runErr != nil {
				p.Send(tui.ProbeErrorMsg{TargetIdx: idx, Err: runErr})
			}
		}(i, tracer)

		go func(idx int) {
			for {
				select {
				case <-ctx.Done():
					return
				case r := <-probeResults:
					p.Send(tui.HopUpdateMsg{TargetIdx: idx, Result: r})
					if model.Enricher() != nil {
						model.Enricher().Submit(r.IP)
					}
				}
			}
		}(i)
	}

	// Start shared DNS resolver in background.
	go model.Resolver().Run(ctx, dnsResults)

	// Start shared ASN enricher in background.
	asnResults := make(chan asn.Result, 64)
	if model.Enricher() != nil {
		go model.Enricher().Run(ctx, asnResults)
	}

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

	// Run TUI (blocks until quit)
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	// Stop background goroutines
	cancel()

	if m, ok := finalModel.(tui.Model); ok {
		m.DebugFinal()
		fmt.Print(m.FinalSummary())
	}
	return nil
}

// runMultiReport runs report mode for one or more targets sequentially and
// emits results. For a single healthy target the output is identical to the
// pre-multi runReport path. For multiple targets, text mode separates each
// section with a rule line; structured exports use WriteJSONMulti/etc.
func runMultiReport(specs []tui.TargetSpec, protocolName string, noASN bool, outputFmt string) error {
	// Setup notices (e.g. an IPv4 fallback) go to stderr, so they are seen
	// without ending up in piped report or export output.
	for _, sp := range specs {
		if sp.Notice != "" {
			fmt.Fprintf(os.Stderr, "note: %s: %s\n", sp.Name, sp.Notice)
		}
	}
	if len(specs) == 1 {
		sp := specs[0]
		if sp.Err != nil {
			return sp.Err
		}
		return runReport(sp.Name, sp.IP, sp.Cfg, protocolName, noASN, outputFmt)
	}

	// Multi-target path.
	versionStr := displayVersion(version) + " (" + buildVersion + ")"
	// A target that fails does not stop the others. Its error goes to stderr,
	// it stays in structured output with the error and no hops, and the run
	// as a whole fails so the exit status is non-zero.
	reports := make([]export.Report, 0, len(specs))
	failed := 0
	fail := func(sp tui.TargetSpec, err error) {
		failed++
		fmt.Fprintf(os.Stderr, "error: %s: %v\n", sp.Name, err)
		r := export.Report{
			Version:   versionStr,
			Timestamp: time.Now().UTC(),
			Target:    sp.Name,
			Protocol:  protocolName,
			Hops:      []export.Hop{},
			Error:     err.Error(),
		}
		if sp.IP != nil {
			r.TargetIP = sp.IP.String()
		}
		reports = append(reports, r)
	}
	for _, sp := range specs {
		if sp.Err != nil {
			if outputFmt == "" {
				fmt.Printf("── %s ──\nerror: %v\n\n", sp.Name, sp.Err)
			}
			fail(sp, sp.Err)
			continue
		}
		if outputFmt == "" {
			fmt.Printf("── %s (%s) ──\n", sp.Name, sp.IP.String())
		}
		if outputFmt != "" {
			// Collect into report struct without printing.
			table, resolver, enricher, maxTTL, runErr := collectReport(sp.IP, sp.Cfg, noASN)
			if runErr != nil {
				fail(sp, runErr)
				continue
			}
			r := export.BuildReport(versionStr, sp.Name, sp.IP, protocolName, table, resolver, enricher, maxTTL)
			reports = append(reports, r)
		} else {
			// Text mode: run and print inline.
			if err := runReport(sp.Name, sp.IP, sp.Cfg, protocolName, noASN, ""); err != nil {
				fmt.Printf("error: %v\n", err)
				fail(sp, err)
			}
			fmt.Println()
		}
	}

	if outputFmt != "" {
		mr := export.MultiReport{
			Version:   versionStr,
			Timestamp: time.Now().UTC(),
			Targets:   reports,
		}
		var werr error
		switch outputFmt {
		case "json":
			werr = export.WriteJSONMulti(os.Stdout, mr)
		case "csv":
			werr = export.WriteCSVMulti(os.Stdout, mr)
		case "dot":
			werr = export.WriteDOTMulti(os.Stdout, mr)
		}
		if werr != nil {
			return werr
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d targets failed", failed, len(specs))
	}
	return nil
}

// wireEchoTier installs the SetOnEchoSent callback on pt so that each echo
// the tracer dispatches is recorded on the matching PathNode in table.
// Runs on the tracer goroutine; PathNode methods are individually mutex-guarded.
func wireEchoTier(pt tracerIface, table *hop.Table) {
	pt.SetOnEchoSent(func(ip net.IP, ttl int) {
		h := table.Get(ttl)
		if h == nil {
			return
		}
		for _, node := range h.GetNodes() {
			if nip := node.GetIP(); nip != nil && nip.Equal(ip) {
				node.MarkEchoSent()
				break
			}
		}
	})
}

// collectEchoTargets builds the slice of {IP, TTL} pairs from every node in
// table.Snapshot() and is used by collectReport inside its SetOnRoundEnd
// closure.
func collectEchoTargets(table *hop.Table) []probe.EchoTarget {
	// The nodes of the path, up to and including the destination's hop (see
	// hop.Table.EchoNodes for why later hops are left out).
	var pairs []probe.EchoTarget
	for _, n := range table.EchoNodes() {
		pairs = append(pairs, probe.EchoTarget{IP: n.IP, TTL: n.TTL})
	}
	return pairs
}

// collectReport traces one target and returns the hop table, the resolver and
// enricher that hold its names and AS numbers, and the last TTL a report
// shows. Single-target report mode (runReportInner) and multi-target report
// mode (runMultiReport) both collect through it, so a fix here reaches both.
func collectReport(targetIP net.IP, cfg probe.Config, noASN bool) (*hop.Table, *resolve.Resolver, *asn.Enricher, int, error) {
	table := hop.NewTable(cfg.MaxHops)
	tracer := newTracer(targetIP, cfg)
	tracer.SetOnRoundSent(table)

	var resolver *resolve.Resolver
	if !cfg.NoDNS {
		resolver = resolve.New(4)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probeResults := make(chan probe.Result, 64)

	if resolver != nil {
		dnsResults := make(chan resolve.Result, 64)
		go resolver.Run(ctx, dnsResults)
		// Drain the results so the channel never blocks. A report reads
		// hostnames from the resolver's cache (resolver.Lookup).
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

	// Echo attribution via shared helper.
	wireEchoTier(tracer, table)

	debug := newReportDebug(targetIP, cfg)
	tracer.SetOnRoundEnd(func() {
		debug.RoundClosed(table, tui.CloseRound(table))
		// Recompute the echo-target set from the live table each round; the
		// tracer round-robins through it under EchoBudget.
		tracer.SubmitEchoTargets(collectEchoTargets(table))
	})

	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-probeResults:
				recordResult(table, r)
				// Echo replies come from addresses already seen as hops, so
				// they are not submitted for DNS again, as in the TUI.
				if resolver != nil && r.ProbeType != probe.ProbeICMPEcho {
					resolver.Submit(r.IP)
				}
				if enricher != nil {
					enricher.Submit(r.IP)
				}
			}
		}
	}()

	// Blocks until MaxRounds are complete.
	if err := tracer.Run(ctx, probeResults); err != nil {
		return nil, nil, nil, 0, err
	}

	// Give DNS a moment to finish outstanding lookups and let the collector
	// drain buffered probe results.
	time.Sleep(500 * time.Millisecond)
	cancel()
	<-collectorDone
	debug.Final(table)

	return table, resolver, enricher, pathMaxTTL(table), nil
}

// recordResult adds a probe result to the hop table: the sample, and who
// answered it, from which the table tells the destination's TTL.
func recordResult(table *hop.Table, r probe.Result) {
	kind := probeKindFor(r.ProbeType)
	table.GetOrCreate(r.TTL).AddSample(r.IP, r.RTT, r.FlowID, kind)
	table.NoteReply(r.TTL, r.IP, r.FlowID, kind, r.IsTarget)
}

// pathMaxTTL is the last TTL a report shows: the destination's
// (hop.Table.TargetTTL, the same one the TUI, the debug log and alerting
// use), or the highest TTL seen while the target has not answered.
func pathMaxTTL(table *hop.Table) int {
	maxTTL := table.MaxTTLSeen()
	if hit := table.TargetTTL(); hit > 0 && hit < maxTTL {
		maxTTL = hit
	}
	return maxTTL
}

// probeKindFor maps a probe.ProbeType to the hop.ProbeKind slot that a
// result should feed. Mirrors the TUI's onHopUpdate mapping so echo replies
// land in EchoStats and TTL replies in ICMPTTLStats — a prerequisite for
// RateLimitState() to classify a hop correctly in report mode.
func probeKindFor(pt probe.ProbeType) hop.ProbeKind {
	switch pt {
	case probe.ProbeICMPTTL:
		return hop.KindICMPTTL
	case probe.ProbeICMPEcho:
		return hop.KindEcho
	default:
		return hop.KindPrimary
	}
}

// runReportInner traces one target end-to-end, writes its report to stdout
// and returns the fully-populated hop.Table plus any error. It is split out
// of runReport so callers that need the table directly (e.g. tests) can
// obtain it without a test-only hook.
func runReportInner(target string, targetIP net.IP, cfg probe.Config, protocolName string, noASN bool, outputFmt string) (*hop.Table, error) {
	table, resolver, enricher, maxTTL, err := collectReport(targetIP, cfg, noASN)
	if err != nil {
		return nil, fmt.Errorf("tracer error: %w", err)
	}

	if outputFmt != "" {
		versionStr := displayVersion(version) + " (" + buildVersion + ")"
		report := export.BuildReport(versionStr, target, targetIP, protocolName, table, resolver, enricher, maxTTL)
		// A report that could not be written (a full disk, a closed pipe)
		// is a failed run: the caller exits non-zero.
		var werr error
		switch outputFmt {
		case "json":
			werr = export.WriteJSON(os.Stdout, report)
		case "csv":
			werr = export.WriteCSV(os.Stdout, report)
		case "dot":
			werr = export.WriteDOT(os.Stdout, report)
		}
		if werr != nil {
			return table, fmt.Errorf("writing the %s report: %w", outputFmt, werr)
		}
	} else {
		printReport(target, targetIP, table, resolver, enricher, table.TargetTTL(), cfg.NumPaths, protocolName)
	}
	return table, nil
}

// runReport traces one target end-to-end and either prints the plain-text
// report or emits a structured export.
func runReport(target string, targetIP net.IP, cfg probe.Config, protocolName string, noASN bool, outputFmt string) error {
	_, err := runReportInner(target, targetIP, cfg, protocolName, noASN, outputFmt)
	return err
}

// printReport writes the plain-text traceroute table to stdout. The loss
// column is each hop's figure as the TUI shows it (hop.Hop.Loss): "~N%†" for
// echo loss on a rate-limited hop, "N%?" while the cause of the hop's loss is
// not yet known.
func printReport(target string, targetIP net.IP, table *hop.Table, resolver *resolve.Resolver, enricher *asn.Enricher, maxTTLHit int, numPaths int, protocolName string) {
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

	// Compute dynamic IP column width
	ipColWidth := 15
	for _, h := range hops {
		if h.GetIP() != nil {
			if l := len(h.GetIP().String()); l > ipColWidth {
				ipColWidth = l
			}
		}
		if multipath && h.IsDivergent() {
			for _, node := range h.GetNodes() {
				if node.GetIP() != nil {
					if l := len(node.GetIP().String()); l > ipColWidth {
						ipColWidth = l
					}
				}
			}
		}
	}
	if ipColWidth > 39 {
		ipColWidth = 39
	}
	ipFmt := fmt.Sprintf("%%-%ds", ipColWidth)

	// Column headers
	if multipath {
		fmt.Printf(fmt.Sprintf("%%-%ds", 4)+" "+ipFmt+" %-22s %-20s %-8s %-5s %-8s %-8s %-8s %-8s %-8s %-8s %-5s\n",
			"#", "IP", "Hostname", "ASN", "Loss%", "Rnd", "Avg", "Best", "Wrst", "StDev", "Last", "Flows", "Stab")
	} else {
		fmt.Printf(fmt.Sprintf("%%-%ds", 4)+" "+ipFmt+" %-22s %-20s %-8s %-5s %-8s %-8s %-8s %-8s %-8s\n",
			"#", "IP", "Hostname", "ASN", "Loss%", "Rnd", "Avg", "Best", "Wrst", "StDev", "Last")
	}

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
			hopLossStr := hop.FormatLoss(h.Loss())
			sent := h.Rounds()
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
					lossStr = hopLossStr
					sntStr = fmt.Sprintf("%d", sent)
				}

				avg := float64(node.AvgRTT().Microseconds()) / 1000.0
				minRTT := float64(node.GetMinRTT().Microseconds()) / 1000.0
				maxRTT := float64(node.GetMaxRTT().Microseconds()) / 1000.0
				stdev := node.StDev()
				last := float64(node.GetLastRTT().Microseconds()) / 1000.0
				flows := hop.FormatFlowIDs(node.GetFlowIDs())
				stab := fmt.Sprintf("%.0f%%", node.StabilityPercent())

				fmt.Printf("%-4d "+ipFmt+" %-22s %-20s %-8s %-5s %-8s %-8s %-8s %-8s %-8s %-8s %-5s\n",
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

			lossStr := hop.FormatLoss(h.Loss())
			sent := h.Rounds()
			avg := float64(h.AvgRTT().Microseconds()) / 1000.0
			minRTT := float64(h.GetMinRTT().Microseconds()) / 1000.0
			maxRTT := float64(h.GetMaxRTT().Microseconds()) / 1000.0
			stdev := h.StDev()
			last := float64(h.GetLastRTT().Microseconds()) / 1000.0

			if multipath {
				// Still show Flows and Stab columns for consistency
				nodes := h.GetNodes()
				flows := "-"
				stab := "-"
				if len(nodes) == 1 {
					flows = hop.FormatFlowIDs(nodes[0].GetFlowIDs())
					stab = fmt.Sprintf("%.0f%%", nodes[0].StabilityPercent())
				}
				fmt.Printf("%-4d "+ipFmt+" %-22s %-20s %-8s %-5d %-8s %-8s %-8s %-8s %-8s %-8s %-5s\n",
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
				fmt.Printf("%-4d "+ipFmt+" %-22s %-20s %-8s %-5d %-8s %-8s %-8s %-8s %-8s\n",
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

// perTargetProtocols gives every healthy target its own protocol object where
// the protocol holds state. Only auto mode does: its selector records whether
// this trace has fallen back from UDP to ICMP, and a selector shared between
// targets would switch all of them when one got no replies.
func perTargetProtocols(specs []tui.TargetSpec, protocolName string, dstPort, numPaths int) error {
	if protocolName != "auto" {
		return nil
	}
	for i := range specs {
		if specs[i].Err != nil {
			continue
		}
		n := numPaths
		proto, err := buildProtocol(protocolName, dstPort, &n, specs[i].Cfg)
		if err != nil {
			return err
		}
		specs[i].Cfg.Protocol = proto
	}
	return nil
}

func buildProtocol(protocolName string, dstPort int, numPaths *int, cfg probe.Config) (probe.ProbeProtocol, error) {
	switch protocolName {
	case "icmp":
		if *numPaths > 1 {
			*numPaths = 1 // runTrace warns when --paths was given; see icmpPathsWarning
		}
		return probe.NewICMPProtocol(cfg), nil
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
			probe.NewICMPProtocol(cfg),
		), nil
	default:
		return nil, fmt.Errorf("unknown protocol: %s (valid: udp, icmp, tcp, auto)", protocolName)
	}
}

// selectFamily picks an IP version and target address based on user flags
// and local transport capability. Returns (version, target, err).
// hasV4 and hasV6 reflect whether the local host has IPv4/IPv6 transport
// (typically from sysprobe.HasIPv4Transport / HasIPv6Transport).
func selectFamily(forceV4, forceV6 bool, v4, v6 net.IP, hasV4, hasV6 bool) (int, net.IP, error) {
	switch {
	case forceV4:
		if v4 == nil {
			return 0, nil, fmt.Errorf("target has no IPv4 (A) record")
		}
		if !hasV4 {
			return 0, nil, fmt.Errorf("no IPv4 transport on this host")
		}
		return 4, v4, nil
	case forceV6:
		if v6 == nil {
			return 0, nil, fmt.Errorf("target has no IPv6 (AAAA) record")
		}
		if !hasV6 {
			return 0, nil, fmt.Errorf("this host has no IPv6 transport")
		}
		return 6, v6, nil
	}

	// Auto-select is best-effort. Prefer v6 when both target AAAA and local v6 work.
	// Otherwise pick v4 if available — the downstream raw-socket open will trigger
	// sudo re-exec if CAP_NET_RAW is missing. Only error when no address exists.
	if v6 != nil && hasV6 {
		return 6, v6, nil
	}
	if v4 != nil {
		return 4, v4, nil
	}
	if v6 != nil {
		return 6, v6, nil
	}
	return 0, nil, fmt.Errorf("no usable address for target")
}
