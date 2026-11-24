//go:build lab

package lab

import (
	"encoding/json"
	"strconv"
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
func TestLateRepliesAreNotLoss(t *testing.T) {
	const rounds = 10
	for _, m := range modes[:2] {
		t.Run(m.name, func(t *testing.T) {
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

// Scenario 8: IPv6, clean and with every 5th reply from r3 dropped.
func TestIPv6(t *testing.T) {
	const rounds = 20
	path := []string{r1V6, r2V6, r3V6, dst1V6}
	for _, m := range modes[:2] {
		udp6 := func(t *testing.T) {
			if m.name == "udp" {
				knownIssue(t, 54, "IPv6 UDP probes carry a zero checksum; the destination drops them")
			}
		}
		t.Run(m.name+"/clean", func(t *testing.T) {
			udp6(t)
			reset(t)
			r := via(t, traceArgs(m, rounds, dst1V6, "-6")...)
			sent := rounds * m.perRound
			for i, ip := range path {
				wantHop(t, hopByTTL(t, r, i+1), ip, rounds, sent, sent)
			}
		})
		t.Run(m.name+"/every-fifth-dropped", func(t *testing.T) {
			udp6(t)
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
