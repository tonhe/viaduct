//go:build lab

package lab

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/tonhe/viaduct/internal/export"
)

// mode is one probe protocol: its flags, how many TTL-limited probes via
// sends per hop per round, and which drop counter its primary probes hit.
type mode struct {
	name     string
	args     []string
	perRound int    // 1 ICMP TTL probe + primary flows
	primary  string // counter suffix for the primary probe's replies ("" = none)
}

var modes = []mode{
	{name: "udp", args: nil, perRound: 7, primary: "udp"},
	{name: "icmp", args: []string{"-P", "icmp"}, perRound: 1, primary: ""},
	{name: "tcp", args: []string{"-P", "tcp"}, perRound: 7, primary: "tcp"},
}

var pathToDst1 = []string{r1V4, r2V4, r3V4, dst1V4}

func traceArgs(m mode, rounds int, target string, extra ...string) []string {
	args := append([]string{}, m.args...)
	args = append(args, "-c", strconv.Itoa(rounds), "-i", "200ms")
	args = append(args, extra...)
	return append(args, target)
}

// Scenario 1: clean path. Every probe is answered; via's counts equal the
// packets on the wire.
func TestCleanPath(t *testing.T) {
	const rounds = 10
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			reset(t)
			var r export.Report
			pkts := capture(t, func() { r = via(t, traceArgs(m, rounds, dst1V4)...) })

			if len(r.Hops) != len(pathToDst1) {
				t.Fatalf("hops = %d, want %d", len(r.Hops), len(pathToDst1))
			}
			want := rounds * m.perRound
			onWire := probesSentByTTL(pkts, clientV4, dst1V4)
			for i, ip := range pathToDst1 {
				ttl := i + 1
				wantHop(t, hopByTTL(t, r, ttl), ip, rounds, want, want)
				if onWire[ttl] != want {
					t.Errorf("TTL %d: %d probes on the wire, want %d", ttl, onWire[ttl], want)
				}
			}
			for i, ip := range pathToDst1[:3] {
				if got := timeExceededFrom(pkts, ip, clientV4); got != want {
					t.Errorf("TTL %d: %d Time Exceeded on the wire from %s, want %d", i+1, got, ip, want)
				}
			}
		})
	}
}

// Scenario 2: exactly every 5th reply from r3 is dropped on its way back.
// via must report exactly the replies that survived.
func TestEveryFifthReplyFromR3Dropped(t *testing.T) {
	const rounds = 20
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			reset(t)
			script(t, "scenarios/drop-replies.sh", "r3", "5")
			var r export.Report
			pkts := capture(t, func() { r = via(t, traceArgs(m, rounds, dst1V4)...) })
			c := counters(t, "r2", "ip")

			// What the kernel saw and dropped, per reply type.
			if c["seen_ttl_icmp"] != rounds || c["drop_ttl_icmp"] != rounds/5 {
				t.Errorf("ICMP TTL replies from r3: seen %d dropped %d, want %d and %d",
					c["seen_ttl_icmp"], c["drop_ttl_icmp"], rounds, rounds/5)
			}
			seen, dropped := c["seen_ttl_icmp"], c["drop_ttl_icmp"]
			if m.primary != "" {
				wantSeen := rounds * (m.perRound - 1)
				s, d := c["seen_ttl_"+m.primary], c["drop_ttl_"+m.primary]
				if s != wantSeen || d != wantSeen/5 {
					t.Errorf("%s replies from r3: seen %d dropped %d, want %d and %d",
						m.primary, s, d, wantSeen, wantSeen/5)
				}
				seen, dropped = seen+s, dropped+d
			}

			sent := rounds * m.perRound
			wantHop(t, hopByTTL(t, r, 1), r1V4, rounds, sent, sent)
			wantHop(t, hopByTTL(t, r, 2), r2V4, rounds, sent, sent)
			wantHop(t, hopByTTL(t, r, 3), r3V4, rounds, sent, seen-dropped)
			wantHop(t, hopByTTL(t, r, 4), dst1V4, rounds, sent, sent)

			// The capture agrees with the kernel counters.
			if got := timeExceededFrom(pkts, r3V4, clientV4); got != seen-dropped {
				t.Errorf("%d Time Exceeded from r3 reached the client, want %d (seen %d - dropped %d)",
					got, seen-dropped, seen, dropped)
			}
		})
	}
}

// Second opinion: mtr over the same rules. mtr sends one ICMP probe per hop
// per cycle, so every 5th reply dropped is exactly 20.0%.
func TestEveryFifthReplyFromR3Dropped_MtrAgrees(t *testing.T) {
	reset(t)
	script(t, "scenarios/drop-replies.sh", "r3", "5")
	loss := mtrLoss(t, dst1V4, 20)
	want := map[string]float64{r1V4: 0, r2V4: 0, r3V4: 20, dst1V4: 0}
	for ip, w := range want {
		if got, ok := loss[ip]; !ok || got != w {
			t.Errorf("mtr loss at %s = %v (present %v), want %v", ip, got, ok, w)
		}
	}
}

// Every reply from r2 is dropped: hop 2 never answers, the hops beyond it are
// unaffected.
func TestAllRepliesFromR2Dropped(t *testing.T) {
	const rounds = 10
	for _, m := range modes[:2] {
		t.Run(m.name, func(t *testing.T) {
			reset(t)
			script(t, "scenarios/drop-replies.sh", "r2", "1")
			r := via(t, traceArgs(m, rounds, dst1V4)...)
			sent := rounds * m.perRound
			wantHop(t, hopByTTL(t, r, 1), r1V4, rounds, sent, sent)
			if h := hopByTTL(t, r, 2); len(h.Nodes) != 0 {
				t.Errorf("TTL 2: got %d nodes, want none (every reply was dropped)", len(h.Nodes))
			}
			wantHop(t, hopByTTL(t, r, 3), r3V4, rounds, sent, sent)
			wantHop(t, hopByTTL(t, r, 4), dst1V4, rounds, sent, sent)
		})
	}
}

// Scenario 6: with -c 5 the fifth round is sent, counted, and not lost.
func TestFinalRoundIsCounted(t *testing.T) {
	const rounds = 5
	for _, m := range modes[:2] {
		t.Run(m.name, func(t *testing.T) {
			reset(t)
			r := via(t, traceArgs(m, rounds, dst1V4)...)
			sent := rounds * m.perRound
			for i, ip := range pathToDst1 {
				wantHop(t, hopByTTL(t, r, i+1), ip, rounds, sent, sent)
			}
		})
	}
}

// Scenario 4: replies that take longer than the interval (300 ms delay,
// 200 ms interval, 1 s timeout) are late, not lost.
//
// The 2-round case is the regression check for closing a round the moment its
// probes are sent: with that bug the last round's replies are still in flight
// when the run ends and hops 3 and 4 report 1 of 2 answered. In longer runs
// other per-round work happens to delay the close, which hides the bug, so
// the short run is the one that matters.
func TestLateRepliesAreNotLoss(t *testing.T) {
	for _, rounds := range []int{2, 10} {
		for _, m := range modes[:2] {
			t.Run(m.name+"/"+strconv.Itoa(rounds)+"-rounds", func(t *testing.T) {
				reset(t)
				script(t, "scenarios/delay.sh", "300ms")
				r := via(t, traceArgs(m, rounds, dst1V4)...)
				sent := rounds * m.perRound
				for i, ip := range pathToDst1 {
					h := hopByTTL(t, r, i+1)
					wantHop(t, h, ip, rounds, sent, sent)
					if i >= 2 && len(h.Nodes) == 1 {
						if avg := h.Nodes[0].RTT.Avg; avg < 300 || avg > 320 {
							t.Errorf("TTL %d: avg RTT = %.1f ms, want 300-320 (300 ms added delay)", h.TTL, avg)
						}
					}
				}
			})
		}
	}
}

// Scenario 8: IPv6, clean and with every 5th reply from r3 dropped.
func TestIPv6(t *testing.T) {
	const rounds = 20
	path := []string{r1V6, r2V6, r3V6, dst1V6}
	for _, m := range modes[:2] {
		t.Run(m.name+"/clean", func(t *testing.T) {
			reset(t)
			r := via(t, traceArgs(m, rounds, dst1V6, "-6")...)
			sent := rounds * m.perRound
			for i, ip := range path {
				wantHop(t, hopByTTL(t, r, i+1), ip, rounds, sent, sent)
			}
		})
		t.Run(m.name+"/every-fifth-dropped", func(t *testing.T) {
			reset(t)
			script(t, "scenarios/drop-replies.sh", "r3", "5", "6")
			r := via(t, traceArgs(m, rounds, dst1V6, "-6")...)
			c := counters(t, "r2", "ip6")
			seen, dropped := c["seen_ttl_icmp"], c["drop_ttl_icmp"]
			if m.primary != "" {
				seen, dropped = seen+c["seen_ttl_"+m.primary], dropped+c["drop_ttl_"+m.primary]
			}
			sent := rounds * m.perRound
			if seen != sent || dropped != sent/5 {
				t.Errorf("replies from r3: seen %d dropped %d, want %d and %d", seen, dropped, sent, sent/5)
			}
			wantHop(t, hopByTTL(t, r, 2), r2V6, rounds, sent, sent)
			wantHop(t, hopByTTL(t, r, 3), r3V6, rounds, sent, seen-dropped)
			wantHop(t, hopByTTL(t, r, 4), dst1V6, rounds, sent, sent)
		})
	}
}

// Scenario 5: r1 limits its ICMP errors to one per second but answers every
// ping. via must flag r1 as rate-limited, must not flag anything else, and
// the echo figures it reports for r1 must be the packets on the wire.
func TestRateLimitedRouterIsFlagged(t *testing.T) {
	const rounds = 60
	for _, m := range modes[:2] {
		t.Run(m.name, func(t *testing.T) {
			reset(t)
			script(t, "scenarios/ratelimit.sh", "r1")
			var r export.Report
			pkts := capture(t, func() { r = via(t, traceArgs(m, rounds, dst1V4)...) })

			h1 := hopByTTL(t, r, 1)
			if len(h1.Nodes) != 1 || h1.Nodes[0].IP != r1V4 {
				t.Fatalf("TTL 1 nodes = %+v, want exactly %s", h1.Nodes, r1V4)
			}
			n := h1.Nodes[0]

			// TTL probes: via received exactly the Time Exceeded that reached
			// the client, and that is well short of what it sent.
			onWire := timeExceededFrom(pkts, r1V4, clientV4)
			sent := rounds * m.perRound
			if h1.ProbesSent != sent || h1.ProbesReceived != onWire {
				t.Errorf("r1 TTL probes sent/received = %d/%d, want %d/%d (Time Exceeded on the wire)",
					h1.ProbesSent, h1.ProbesReceived, sent, onWire)
			}
			if onWire >= sent {
				t.Fatalf("r1 answered every TTL probe (%d of %d); the rate limit did not take effect", onWire, sent)
			}

			// Echo tier: exact agreement with the capture, and no echo lost.
			wireSent, wireRecv := echoesTo(pkts, clientV4, r1V4)
			if n.Probes.Echo.Sent != wireSent || n.Probes.Echo.Received != wireRecv {
				t.Errorf("r1 echo sent/received = %d/%d, capture shows %d/%d",
					n.Probes.Echo.Sent, n.Probes.Echo.Received, wireSent, wireRecv)
			}
			if wireRecv != wireSent {
				t.Errorf("r1 answered %d of %d echoes on the wire; a rate-limited router answers all of them", wireRecv, wireSent)
			}
			if n.RateLimited != "yes" {
				t.Errorf("r1 rate_limited = %q, want yes", n.RateLimited)
			}

			// Nothing beyond r1 is affected or flagged.
			for i, ip := range pathToDst1[1:] {
				h := hopByTTL(t, r, i+2)
				wantHop(t, h, ip, rounds, sent, sent)
				for _, node := range h.Nodes {
					if node.RateLimited == "yes" {
						t.Errorf("TTL %d (%s) is flagged rate-limited; only r1 is", h.TTL, node.IP)
					}
				}
			}
		})
	}
}

// The text report shows a rate-limited hop's echo loss, marked ~N%†. Every
// echo to r1 is answered, so that is ~0%†, not the TTL probe loss.
func TestRateLimitedRouter_ReportShowsEchoLoss(t *testing.T) {
	reset(t)
	script(t, "scenarios/ratelimit.sh", "r1")
	out := viaText(t, "-P", "icmp", "-c", "60", "-i", "200ms", dst1V4)
	var row string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, r1V4+" ") {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("no row for %s in report:\n%s", r1V4, out)
	}
	if !strings.Contains(row, "~0%†") {
		t.Errorf("r1 row should show echo-derived loss ~0%%†; got:\n%s", row)
	}
}

// Real loss is not rate limiting. Every 5th reply of every type from r3 is
// dropped, echoes included, so TTL probe loss and echo loss are both exactly
// 20%. Ten runs; nothing may be flagged in any of them.
func TestRealLossIsNeverFlaggedAsRateLimiting(t *testing.T) {
	const rounds = 25
	runs := map[string]int{"icmp": 10, "udp": 3} // udp rounds are slower; the rule is the same
	for _, m := range modes[:2] {
		t.Run(m.name, func(t *testing.T) {
			for run := 1; run <= runs[m.name]; run++ {
				reset(t)
				script(t, "scenarios/drop-replies.sh", "r3", "5")
				r := via(t, traceArgs(m, rounds, dst1V4)...)
				h3 := hopByTTL(t, r, 3)
				if h3.ProbesReceived >= h3.ProbesSent {
					t.Fatalf("run %d: no loss at r3 (%d/%d); the drop rule did not take effect", run, h3.ProbesReceived, h3.ProbesSent)
				}
				for _, h := range r.Hops {
					for _, node := range h.Nodes {
						if node.RateLimited == "yes" {
							t.Errorf("run %d: TTL %d (%s) flagged rate-limited on a path with real loss only (echo %d/%d, icmp ttl %d/%d)",
								run, h.TTL, node.IP, node.Probes.Echo.Received, node.Probes.Echo.Sent,
								node.Probes.ICMPTTL.Received, node.Probes.ICMPTTL.Sent)
						}
					}
				}
			}
		})
	}
}

// An IPv4-mapped IPv6 literal is traced as the IPv4 address it embeds, over
// IPv4, along the IPv4 path.
func TestIPv4MappedLiteralTracesOverIPv4(t *testing.T) {
	const rounds = 5
	reset(t)
	r := via(t, "-P", "icmp", "-c", strconv.Itoa(rounds), "-i", "200ms", "::ffff:"+dst1V4)
	if r.TargetIP != dst1V4 {
		t.Errorf("target_ip = %s, want %s", r.TargetIP, dst1V4)
	}
	for i, ip := range pathToDst1 {
		wantHop(t, hopByTTL(t, r, i+1), ip, rounds, rounds, rounds)
	}
}

// Targets that cannot be traced are refused before any probe is sent.
func TestUntraceableTargetsAreRefusedAtStartup(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"IPv4 multicast", []string{"224.0.0.251"}, "multicast"},
		{"IPv6 multicast", []string{"ff02::1"}, "multicast"},
		{"IPv4-mapped literal with -6", []string{"-6", "::ffff:" + dst1V4}, "IPv4-mapped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset(t)
			var stderr string
			pkts := capture(t, func() {
				cmd := inNS("client", viaBin, append([]string{"-r", "-n", "--no-asn", "-c", "2"}, tc.args...)...)
				out, err := cmd.CombinedOutput()
				stderr = string(out)
				if err == nil {
					t.Errorf("via exited zero; output:\n%s", out)
				}
			})
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("error output does not mention %q:\n%s", tc.want, stderr)
			}
			for _, p := range pkts {
				if p.src == clientV4 || p.src == clientV6 {
					if p.proto == 58 && p.icmpType >= 133 && p.icmpType <= 137 {
						continue // neighbor discovery, not a probe
					}
					t.Errorf("a packet left the client for a refused target: %+v", p)
					break
				}
			}
		})
	}
}

// Scenario 9: two targets in one session. Loss on dst1's path must not show
// up on dst2's.
func TestMultiTargetIsolation(t *testing.T) {
	const rounds = 20
	reset(t)
	script(t, "scenarios/drop-replies.sh", "r3", "5")
	raw := runVia(t, "-c", strconv.Itoa(rounds), "-i", "200ms", dst1V4, dst2V4)
	var mr export.MultiReport
	if err := json.Unmarshal(raw, &mr); err != nil {
		t.Fatalf("parsing multi-target JSON: %v\n%s", err, raw)
	}
	if len(mr.Targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(mr.Targets))
	}
	c := counters(t, "r2", "ip")
	seen := c["seen_ttl_icmp"] + c["seen_ttl_udp"]
	dropped := c["drop_ttl_icmp"] + c["drop_ttl_udp"]
	sent := rounds * 7
	if seen != sent {
		t.Errorf("replies from r3 seen = %d, want %d", seen, sent)
	}

	d1, d2 := mr.Targets[0], mr.Targets[1]
	wantHop(t, hopByTTL(t, d1, 3), r3V4, rounds, sent, seen-dropped)
	wantHop(t, hopByTTL(t, d1, 4), dst1V4, rounds, sent, sent)

	for i, ip := range []string{r1V4, r2V4, r3bV4, dst2V4} {
		wantHop(t, hopByTTL(t, d2, i+1), ip, rounds, sent, sent)
	}
}

// Scenario 10: r2 has two equal-cost next hops toward dst1. UDP flows with
// different source ports must reveal both, with nothing lost.
func TestECMPDiscoversBothPaths(t *testing.T) {
	const rounds = 10
	reset(t)
	script(t, "scenarios/ecmp.sh")
	r := via(t, "-c", strconv.Itoa(rounds), "-i", "200ms", dst1V4)
	h := hopByTTL(t, r, 3)
	got := map[string]bool{}
	for _, n := range h.Nodes {
		got[n.IP] = true
	}
	if !got[r3V4] || !got[r3bV4] || len(got) != 2 {
		t.Errorf("TTL 3 nodes = %v, want exactly %s and %s", got, r3V4, r3bV4)
	}
	if sent := rounds * 7; h.ProbesSent != sent || h.ProbesReceived != sent {
		t.Errorf("TTL 3 probes sent/received = %d/%d, want %d/%d", h.ProbesSent, h.ProbesReceived, sent, sent)
	}
}
