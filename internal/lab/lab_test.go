//go:build lab

// Package lab verifies via's numbers against real packets on a network built
// from Linux network namespaces (scripts/lab). Nothing here is random: drop
// rules remove exactly every Nth reply of one type, kernel counters record how
// many replies each rule saw and dropped, and a packet capture on the client
// records every probe sent. The tests assert exact equality between those
// figures and what via reports.
//
// Run with `sudo make lab-test`, or `make lab-docker` on a host without root.
package lab

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/export"
)

// Topology facts (see scripts/lab/lib.sh).
const (
	clientV4 = "192.0.2.1"
	clientV6 = "2001:db8:0:1::1"
	dst1V4   = "198.51.100.1"
	dst2V4   = "203.0.113.1"
	dst1V6   = "2001:db8:100::1"

	r1V4, r2V4, r3V4, r3bV4 = "192.0.2.2", "192.0.2.6", "192.0.2.10", "192.0.2.14"
	r1V6, r2V6, r3V6        = "2001:db8:0:1::2", "2001:db8:0:2::2", "2001:db8:0:3::2"

	nsPrefix = "vialab-"
)

var (
	repoRoot string
	viaBin   string
)

func TestMain(m *testing.M) {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "lab tests need root (network namespaces, nftables, raw sockets)")
		os.Exit(1)
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	repoRoot = filepath.Clean(filepath.Join(wd, "..", ".."))
	if r := os.Getenv("LAB_REPO_ROOT"); r != "" {
		repoRoot = r
	}
	viaBin = os.Getenv("VIA_BIN")
	if viaBin == "" {
		viaBin = filepath.Join(repoRoot, "via")
	}
	if _, err := os.Stat(viaBin); err != nil {
		fmt.Fprintf(os.Stderr, "via binary not found at %s (run `make build` or set VIA_BIN)\n", viaBin)
		os.Exit(1)
	}
	if out, err := exec.Command(filepath.Join(repoRoot, "scripts/lab/up.sh")).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "lab up failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = exec.Command(filepath.Join(repoRoot, "scripts/lab/down.sh")).Run()
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// script runs scripts/lab/<name> and fails the test on error.
func script(t *testing.T, name string, args ...string) {
	t.Helper()
	out, err := exec.Command(filepath.Join(repoRoot, "scripts/lab", name), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// reset returns the lab to its clean baseline, now and when the test ends.
func reset(t *testing.T) {
	t.Helper()
	script(t, "reset.sh")
	t.Cleanup(func() { script(t, "reset.sh") })
}

// inNS builds a command that runs inside a lab namespace.
func inNS(node string, name string, args ...string) *exec.Cmd {
	return exec.Command("ip", append([]string{"netns", "exec", nsPrefix + node, name}, args...)...)
}

// runVia runs via in report mode with JSON output from the client namespace.
func runVia(t *testing.T, args ...string) []byte {
	t.Helper()
	full := append([]string{"-r", "-n", "--no-asn", "-o", "json"}, args...)
	cmd := inNS("client", viaBin, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("via %v: %v\nstderr: %s", full, err, stderr.String())
	}
	return stdout.Bytes()
}

// via runs a single-target trace and returns the parsed report.
func via(t *testing.T, args ...string) export.Report {
	t.Helper()
	raw := runVia(t, args...)
	var r export.Report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("parsing via JSON: %v\n%s", err, raw)
	}
	return r
}

// hopByTTL returns the report's hop for ttl, failing if it is absent.
func hopByTTL(t *testing.T, r export.Report, ttl int) export.Hop {
	t.Helper()
	for _, h := range r.Hops {
		if h.TTL == ttl {
			return h
		}
	}
	t.Fatalf("report has no hop with TTL %d (hops: %d)", ttl, len(r.Hops))
	return export.Hop{}
}

// counters reads the named nft counters of table "lab" on node.
// family is "ip" or "ip6". Keys are counter names, values packet counts.
func counters(t *testing.T, node, family string) map[string]int {
	t.Helper()
	out, err := inNS(node, "nft", "-j", "list", "counters", "table", family, "lab").Output()
	if err != nil {
		t.Fatalf("nft list counters on %s: %v", node, err)
	}
	var doc struct {
		Nftables []struct {
			Counter *struct {
				Name    string `json:"name"`
				Packets int    `json:"packets"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("parsing nft JSON: %v\n%s", err, out)
	}
	got := map[string]int{}
	for _, e := range doc.Nftables {
		if e.Counter != nil {
			got[e.Counter.Name] = e.Counter.Packets
		}
	}
	return got
}

// capture records every packet on the client's uplink while fn runs and
// returns the parsed packets.
func capture(t *testing.T, fn func()) []packet {
	t.Helper()
	file := filepath.Join(t.TempDir(), "client.pcap")
	// --immediate-mode: without it libpcap hands packets over a block at a
	// time, and the packets of the last block (up to a second's worth) can be
	// lost when tcpdump is interrupted. That showed as one round of echoes
	// missing from the capture in about one run in three.
	cmd := inNS("client", "tcpdump", "-i", "client-r1", "--immediate-mode", "-U", "-n", "-s", "128", "-w", file)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting tcpdump: %v", err)
	}
	// tcpdump prints "listening on ..." once the capture is live.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stderr.String(), "listening on") {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("tcpdump did not start: %s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	fn()

	time.Sleep(200 * time.Millisecond) // let the last replies reach the capture
	_ = cmd.Process.Signal(os.Interrupt)
	_ = cmd.Wait()
	pkts, err := readPcap(file)
	if err != nil {
		t.Fatalf("reading capture: %v", err)
	}
	return pkts
}

// packet is the part of a captured IP packet the tests care about.
type packet struct {
	src, dst string
	ttl      int // IPv4 TTL or IPv6 hop limit
	proto    int // 1 ICMP, 6 TCP, 17 UDP, 58 ICMPv6
	icmpType int // valid when proto is 1 or 58
}

// readPcap parses a classic pcap file of Ethernet frames carrying IPv4 or
// IPv6. Anything else (ARP, neighbor discovery is kept as ICMPv6) is skipped.
func readPcap(path string) ([]packet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 24 {
		return nil, fmt.Errorf("pcap too short: %d bytes", len(data))
	}
	var order binary.ByteOrder
	switch binary.LittleEndian.Uint32(data[0:4]) {
	case 0xa1b2c3d4, 0xa1b23c4d:
		order = binary.LittleEndian
	case 0xd4c3b2a1, 0x4d3cb2a1:
		order = binary.BigEndian
	default:
		return nil, fmt.Errorf("not a pcap file (magic %x)", data[0:4])
	}
	if lt := order.Uint32(data[20:24]); lt != 1 {
		return nil, fmt.Errorf("unexpected link type %d, want Ethernet", lt)
	}
	var pkts []packet
	off := 24
	for off+16 <= len(data) {
		caplen := int(order.Uint32(data[off+8 : off+12]))
		off += 16
		if off+caplen > len(data) {
			break
		}
		frame := data[off : off+caplen]
		off += caplen
		if len(frame) < 14 {
			continue
		}
		ip := frame[14:]
		switch binary.BigEndian.Uint16(frame[12:14]) {
		case 0x0800:
			if len(ip) < 20 {
				continue
			}
			ihl := int(ip[0]&0x0f) * 4
			p := packet{
				src:   net.IP(ip[12:16]).String(),
				dst:   net.IP(ip[16:20]).String(),
				ttl:   int(ip[8]),
				proto: int(ip[9]),
			}
			if p.proto == 1 && len(ip) > ihl {
				p.icmpType = int(ip[ihl])
			}
			pkts = append(pkts, p)
		case 0x86dd:
			if len(ip) < 40 {
				continue
			}
			p := packet{
				src:   net.IP(ip[8:24]).String(),
				dst:   net.IP(ip[24:40]).String(),
				ttl:   int(ip[7]),
				proto: int(ip[6]),
			}
			if p.proto == 58 && len(ip) > 40 {
				p.icmpType = int(ip[40])
			}
			pkts = append(pkts, p)
		}
	}
	return pkts, nil
}

// probesSentByTTL counts the TTL-limited probes the client sent to target,
// per TTL. Echo-tier probes go out with a full TTL (64) and are excluded.
func probesSentByTTL(pkts []packet, client, target string) map[int]int {
	sent := map[int]int{}
	for _, p := range pkts {
		if p.src == client && p.dst == target && p.ttl <= 32 {
			sent[p.ttl]++
		}
	}
	return sent
}

// ICMP type numbers.
const (
	icmp4TimeExceeded = 11
	icmp6TimeExceeded = 3
)

// timeExceededFrom counts Time Exceeded messages that reached the client from src.
func timeExceededFrom(pkts []packet, src, client string) int {
	n := 0
	for _, p := range pkts {
		if p.src != src || p.dst != client {
			continue
		}
		if (p.proto == 1 && p.icmpType == icmp4TimeExceeded) || (p.proto == 58 && p.icmpType == icmp6TimeExceeded) {
			n++
		}
	}
	return n
}

// echoesTo counts the direct echo requests (full TTL, not TTL-limited probes)
// the client sent to ip, and the echo replies that came back from it.
func echoesTo(pkts []packet, client, ip string) (sent, received int) {
	for _, p := range pkts {
		if p.proto != 1 && p.proto != 58 {
			continue
		}
		request, reply := 8, 0
		if p.proto == 58 {
			request, reply = 128, 129
		}
		switch {
		case p.src == client && p.dst == ip && p.icmpType == request && p.ttl > 32:
			sent++
		case p.src == ip && p.dst == client && p.icmpType == reply:
			received++
		}
	}
	return sent, received
}

// viaText runs via in report mode with the plain-text table and returns it.
func viaText(t *testing.T, args ...string) string {
	t.Helper()
	full := append([]string{"-r", "-n", "--no-asn"}, args...)
	out, err := inNS("client", viaBin, full...).Output()
	if err != nil {
		t.Fatalf("via %v: %v", full, err)
	}
	return string(out)
}

// mtrLoss runs mtr from the client and returns loss percent per hop address.
func mtrLoss(t *testing.T, target string, count int, extra ...string) map[string]float64 {
	t.Helper()
	args := append([]string{"-r", "-n", "-c", strconv.Itoa(count), "-i", "0.2"}, extra...)
	out, err := inNS("client", "mtr", append(args, target)...).CombinedOutput()
	if err != nil {
		t.Fatalf("mtr: %v\n%s", err, out)
	}
	re := regexp.MustCompile(`(?m)^\s*\d+\.\|--\s+(\S+)\s+([0-9.]+)%`)
	loss := map[string]float64{}
	for _, m := range re.FindAllStringSubmatch(string(out), -1) {
		v, _ := strconv.ParseFloat(m[2], 64)
		loss[m[1]] = v
	}
	if len(loss) == 0 {
		t.Fatalf("could not parse mtr output:\n%s", out)
	}
	return loss
}

// knownIssue skips a scenario that fails because of an open bug, so the lab
// can gate merges while the bug is being fixed. The assertions stay as the
// reproduction: run with LAB_STRICT=1 to execute them. Remove the call when
// the issue closes.
func knownIssue(t *testing.T, issue int, what string) {
	t.Helper()
	if os.Getenv("LAB_STRICT") == "1" {
		return
	}
	t.Skipf("known failure, tonhe/viaduct#%d: %s (set LAB_STRICT=1 to run)", issue, what)
}

// wantHop asserts a hop's address, rounds and exact probe counts.
func wantHop(t *testing.T, h export.Hop, ip string, rounds, sent, received int) {
	t.Helper()
	if len(h.Nodes) != 1 || h.Nodes[0].IP != ip {
		var ips []string
		for _, n := range h.Nodes {
			ips = append(ips, n.IP)
		}
		t.Errorf("TTL %d: nodes = %v, want [%s]", h.TTL, ips, ip)
	}
	if h.Rounds != rounds {
		t.Errorf("TTL %d: rounds = %d, want %d", h.TTL, h.Rounds, rounds)
	}
	if h.ProbesSent != sent || h.ProbesReceived != received || h.Sent != sent {
		t.Errorf("TTL %d: probes sent/received = %d/%d (sent field %d), want %d/%d",
			h.TTL, h.ProbesSent, h.ProbesReceived, h.Sent, sent, received)
	}
	// Node counts are in probes too. With one node, its replies are the hop's.
	if len(h.Nodes) == 1 {
		if n := h.Nodes[0]; n.Sent != sent || n.Received != received {
			t.Errorf("TTL %d: node sent/received = %d/%d, want %d/%d", h.TTL, n.Sent, n.Received, sent, received)
		}
	}
	wantLoss := 0.0
	if sent > 0 {
		wantLoss = float64(sent-received) / float64(sent) * 100
	}
	if diff := h.Loss - wantLoss; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("TTL %d: loss = %v%%, want exactly %v%%", h.TTL, h.Loss, wantLoss)
	}
}
