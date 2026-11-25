package probe

// echo_test.go — Task 8: budgeted per-tracer echo tier (ICMP Echo Request
// with full TTL) with {IP,TTL} seq attribution.
//
// Tests here are the spec's named invariants. Keep the names — they map
// directly to the interleaved-probes plan.
//
// IPv4 fixtures use RFC 5737 (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24).
// IPv6 fixtures use RFC 3849 (2001:db8::/32).

import (
	"context"
	"encoding/binary"
	"go/parser"
	"go/token"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// newEchoRunTracer builds a tracer wired for echo-tier run() tests. The
// primary is ICMP so tier 2 is skipped and only tier 1 (ICMP TTL) and
// tier 3 (echo) writes hit the fake sender.
func newEchoRunTracer(snd *fakeSender, cfg Config, target net.IP) *Tracer {
	tr := NewTracer(target, cfg)
	tr.newSender = func(_ context.Context, _ ProbeProtocol, _ chan<- Result) (probeSender, func(), error) {
		return snd, nil, nil
	}
	return tr
}

// echoPacketIDs returns the ICMP IDs of every echo-shaped packet the fake
// sender captured. It filters to packets sent at TTL == echoFullTTL (64) so
// tier-1 ICMP TTL probes (which use the per-hop TTL) are excluded.
func echoPacketIDs(snd *fakeSender) []int {
	snd.mu.Lock()
	defer snd.mu.Unlock()
	var ids []int
	for _, wp := range snd.written {
		if wp.ttl != echoFullTTL {
			continue
		}
		if !packetIsICMPEcho(wp.pkt) {
			continue
		}
		if len(wp.pkt) < 8 {
			continue
		}
		ids = append(ids, int(binary.BigEndian.Uint16(wp.pkt[4:6])))
	}
	return ids
}

// echoPacketPairs returns (id, seq) pairs from every echo-tier packet.
func echoPacketPairs(snd *fakeSender) [][2]int {
	snd.mu.Lock()
	defer snd.mu.Unlock()
	var out [][2]int
	for _, wp := range snd.written {
		if wp.ttl != echoFullTTL {
			continue
		}
		if !packetIsICMPEcho(wp.pkt) || len(wp.pkt) < 8 {
			continue
		}
		id := int(binary.BigEndian.Uint16(wp.pkt[4:6]))
		seq := int(binary.BigEndian.Uint16(wp.pkt[6:8]))
		out = append(out, [2]int{id, seq})
	}
	return out
}

// TestEchoInvariant2_PerTracerTargets: two Tracers, overlapping IPs
// submitted; echoes sent by tracer A carry A's cfg.EchoID only, and a
// reply with A's ID matched by A yields a Result only on A's channel.
func TestEchoInvariant2_PerTracerTargets(t *testing.T) {
	buildCfg := func(echoID int) Config {
		c := DefaultConfig()
		c.IPVersion = 4
		c.Protocol = NewICMPProtocol(c)
		c.MaxHops = 1
		c.FirstTTL = 1
		c.NumPaths = 1
		c.ProbeDelay = 0
		c.RoundDelay = 5 * time.Millisecond
		c.MaxRounds = 2 // rounds 0 and 1; echoes fire on round 1
		c.EchoID = echoID
		c.EchoBudget = 8
		return c
	}

	target := net.ParseIP("192.0.2.1")
	cfgA := buildCfg(0xA1A1)
	cfgB := buildCfg(0xB2B2)

	sndA := &fakeSender{}
	sndB := &fakeSender{}
	trA := newEchoRunTracer(sndA, cfgA, target)
	trB := newEchoRunTracer(sndB, cfgB, target)

	// Overlapping target set — same IP at same TTL.
	shared := []EchoTarget{{IP: net.ParseIP("198.51.100.1"), TTL: 5}}
	trA.SubmitEchoTargets(shared)
	trB.SubmitEchoTargets(shared)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := trA.run(ctx, nil, make(chan Result, 64)); err != nil {
			t.Errorf("trA.run error: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := trB.run(ctx, nil, make(chan Result, 64)); err != nil {
			t.Errorf("trB.run error: %v", err)
		}
	}()
	wg.Wait()

	// Every echo emitted by A carries A's ID (and none carry B's ID).
	for _, id := range echoPacketIDs(sndA) {
		if id != cfgA.EchoID {
			t.Errorf("A emitted echo ID 0x%x, want 0x%x (must not carry B's ID)", id, cfgA.EchoID)
		}
	}
	if len(echoPacketIDs(sndA)) == 0 {
		t.Error("A emitted no echoes; expected at least one on round 2")
	}
	for _, id := range echoPacketIDs(sndB) {
		if id != cfgB.EchoID {
			t.Errorf("B emitted echo ID 0x%x, want 0x%x (must not carry A's ID)", id, cfgB.EchoID)
		}
	}

	// Reply with A's ID matched via A's probeMap yields Result only on A.
	// Find one A-emitted echo pair.
	pairsA := echoPacketPairs(sndA)
	if len(pairsA) == 0 {
		t.Fatal("A has no echo pairs to test reply matching against")
	}
	replyID, replySeq := pairsA[0][0], pairsA[0][1]
	// The pair must still be resolvable in A's probeMap.
	keyA := probeKey{SrcPort: replyID, DstPort: replySeq, Seq: 0}
	if _, ok := trA.pm.match(keyA); !ok {
		// It's OK if listen() already consumed it — but this test does not
		// run listen(), so pm.add must have persisted the entry. If it's
		// missing here it means sendEchoes never registered it.
		t.Fatalf("A: probeMap missing echo record for (id=0x%x, seq=%d) — sendEchoes did not register", replyID, replySeq)
	}
	// B must NOT have the same key registered.
	keyBSameAsA := probeKey{SrcPort: replyID, DstPort: replySeq, Seq: 0}
	if _, ok := trB.pm.match(keyBSameAsA); ok {
		t.Errorf("B: probeMap should not contain A's echo key {%d,%d,0}", replyID, replySeq)
	}
}

// TestEchoInvariant3_SeqResolvesIPTTLPair: submit the SAME IP at TTL 9
// and TTL 10. One round of sendEchoes emits two echoes with distinct seqs;
// replies for each seq produce Results with TTL 9 and TTL 10, and
// ProbeType == ProbeICMPEcho.
func TestEchoInvariant3_SeqResolvesIPTTLPair(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.RoundDelay = 10 * time.Millisecond
	cfg.EchoID = 0xE33E
	cfg.EchoBudget = 4
	target := net.ParseIP("192.0.2.1")
	cfg.TargetIP = target
	tr := NewTracer(target, cfg)

	ip := net.ParseIP("198.51.100.7")
	tr.SubmitEchoTargets([]EchoTarget{{IP: ip, TTL: 9}, {IP: ip, TTL: 10}})

	snd := &fakeSender{}
	seq := 0
	tr.sendEchoes(snd, &seq)

	pairs := echoPacketPairs(snd)
	if len(pairs) != 2 {
		t.Fatalf("expected 2 echoes for two {IP,TTL} pairs, got %d", len(pairs))
	}
	if pairs[0][1] == pairs[1][1] {
		t.Errorf("expected distinct seqs, got %d and %d", pairs[0][1], pairs[1][1])
	}
	for _, p := range pairs {
		if p[0] != cfg.EchoID {
			t.Errorf("echo ID = 0x%x, want 0x%x", p[0], cfg.EchoID)
		}
	}

	// Simulate reply for each seq: listen() would key {ID, seq}. Look up in
	// probeMap and assert the record's TTL matches the submitted target,
	// AND that the probeType is ProbeICMPEcho — which is what listen() will
	// tag the delivered Result with.
	for i, p := range pairs {
		key := probeKey{SrcPort: p[0], DstPort: p[1], Seq: 0}
		// Use listen() end-to-end so we assert the Result's ProbeType too.
		conn := newFakeConn()
		conn.push(marshalEchoReply(p[0], p[1]), fakeAddr{ip.String()})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		results := make(chan Result, 1)
		go tr.listen(ctx, conn, results)

		timer := time.NewTimer(time.Second)
		select {
		case r := <-results:
			timer.Stop()
			// TTL must match the submitted target for this pair (targets
			// were {ip,9} then {ip,10}, round-robin from cursor 0 → sends in
			// that order).
			wantTTL := 9
			if i == 1 {
				wantTTL = 10
			}
			if r.TTL != wantTTL {
				t.Errorf("pair %d: Result.TTL = %d, want %d", i, r.TTL, wantTTL)
			}
			if r.ProbeType != ProbeICMPEcho {
				t.Errorf("pair %d: Result.ProbeType = %v, want ProbeICMPEcho", i, r.ProbeType)
			}
		case <-timer.C:
			t.Fatalf("pair %d: timed out waiting for echo Result", i)
		}
		cancel()
		_ = key
	}
}

// TestEchoInvariant4_WholesaleReplacement: submit {A,B,C}; then submit
// {B}; next round sends echoes only for B; internal target slice length
// is 1.
func TestEchoInvariant4_WholesaleReplacement(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.EchoID = 0xE44E
	cfg.EchoBudget = 8
	target := net.ParseIP("192.0.2.1")
	tr := NewTracer(target, cfg)

	a := net.ParseIP("198.51.100.1")
	b := net.ParseIP("198.51.100.2")
	c := net.ParseIP("198.51.100.3")
	tr.SubmitEchoTargets([]EchoTarget{{IP: a, TTL: 1}, {IP: b, TTL: 2}, {IP: c, TTL: 3}})
	tr.SubmitEchoTargets([]EchoTarget{{IP: b, TTL: 2}})

	tr.echo.mu.Lock()
	gotLen := len(tr.echo.targets)
	tr.echo.mu.Unlock()
	if gotLen != 1 {
		t.Fatalf("internal target slice length = %d, want 1 after wholesale replacement", gotLen)
	}

	snd := &fakeSender{}
	seq := 0
	tr.sendEchoes(snd, &seq)

	pairs := echoPacketPairs(snd)
	if len(pairs) != 1 {
		t.Fatalf("expected 1 echo after replacement, got %d", len(pairs))
	}
	// The one echo must resolve to B (TTL 2).
	key := probeKey{SrcPort: pairs[0][0], DstPort: pairs[0][1], Seq: 0}
	rec, ok := tr.pm.match(key)
	if !ok {
		t.Fatal("echo pair not registered in probeMap")
	}
	if rec.ttl != 2 {
		t.Errorf("echo TTL record = %d, want 2 (target B)", rec.ttl)
	}
}

// TestEchoBudgetRoundRobin: 10 targets, cfg.EchoBudget=4. Round 1 sends
// echoes for targets[0..3], round 2 for [4..7], round 3 for [8,9,0,1].
// OnEchoSent fires once per actual send with the pair's IP and TTL.
func TestEchoBudgetRoundRobin(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.EchoID = 0xB0B0
	cfg.EchoBudget = 4
	target := net.ParseIP("192.0.2.1")
	tr := NewTracer(target, cfg)

	targets := make([]EchoTarget, 10)
	for i := 0; i < 10; i++ {
		targets[i] = EchoTarget{
			IP:  net.ParseIP("198.51.100." + string(rune('0'+i))),
			TTL: i + 1,
		}
	}
	tr.SubmitEchoTargets(targets)

	var mu sync.Mutex
	var cbs []struct {
		ip  string
		ttl int
	}
	tr.SetOnEchoSent(func(ip net.IP, ttl int) {
		mu.Lock()
		cbs = append(cbs, struct {
			ip  string
			ttl int
		}{ip.String(), ttl})
		mu.Unlock()
	})

	snd := &fakeSender{}
	seq := 0

	// Expected order over three rounds: idx 0..3, 4..7, 8,9,0,1
	wantIdx := [][]int{
		{0, 1, 2, 3},
		{4, 5, 6, 7},
		{8, 9, 0, 1},
	}

	for round := 0; round < 3; round++ {
		before := len(cbs)
		tr.sendEchoes(snd, &seq)
		mu.Lock()
		got := cbs[before:]
		mu.Unlock()
		if len(got) != 4 {
			t.Fatalf("round %d: OnEchoSent fired %d times, want 4", round+1, len(got))
		}
		for i, cb := range got {
			idx := wantIdx[round][i]
			if cb.ip != targets[idx].IP.String() || cb.ttl != targets[idx].TTL {
				t.Errorf("round %d, send %d: callback got (%s, %d), want (%s, %d)",
					round+1, i, cb.ip, cb.ttl, targets[idx].IP.String(), targets[idx].TTL)
			}
		}
	}

	// Also cross-check the send count on the wire.
	pairs := echoPacketPairs(snd)
	if len(pairs) != 12 {
		t.Errorf("total echoes on wire = %d, want 12 (3 rounds x 4 budget)", len(pairs))
	}
}

// TestEchoNotSentInFirstRound: no echoes emitted during round 0 even with
// targets submitted (spec: round 2+).
func TestEchoNotSentInFirstRound(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.MaxHops = 2
	cfg.FirstTTL = 1
	cfg.NumPaths = 1
	cfg.ProbeDelay = 0
	cfg.RoundDelay = 5 * time.Millisecond
	cfg.MaxRounds = 1 // only round 0 runs
	cfg.EchoID = 0xF00F
	cfg.EchoBudget = 4

	target := net.ParseIP("192.0.2.1")
	snd := &fakeSender{}
	tr := newEchoRunTracer(snd, cfg, target)
	tr.SubmitEchoTargets([]EchoTarget{
		{IP: net.ParseIP("198.51.100.1"), TTL: 1},
		{IP: net.ParseIP("198.51.100.2"), TTL: 2},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := tr.run(ctx, nil, make(chan Result, 32)); err != nil {
		t.Fatalf("run error: %v", err)
	}

	if got := echoPacketPairs(snd); len(got) != 0 {
		t.Errorf("round 0 emitted %d echoes, want 0 (echoes start round 2)", len(got))
	}
}

// TestEchoSetTTLFull: echo sends use TTL 64 (echoFullTTL), not a hop-limited TTL.
func TestEchoSetTTLFull(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.EchoID = 0x6464
	cfg.EchoBudget = 3
	target := net.ParseIP("192.0.2.1")
	tr := NewTracer(target, cfg)

	tr.SubmitEchoTargets([]EchoTarget{
		{IP: net.ParseIP("198.51.100.1"), TTL: 1},
		{IP: net.ParseIP("198.51.100.2"), TTL: 5},
		{IP: net.ParseIP("198.51.100.3"), TTL: 20},
	})

	snd := &fakeSender{}
	seq := 0
	tr.sendEchoes(snd, &seq)

	snd.mu.Lock()
	written := append([]writtenProbe(nil), snd.written...)
	snd.mu.Unlock()

	if len(written) != 3 {
		t.Fatalf("got %d echoes, want 3", len(written))
	}
	for i, wp := range written {
		if wp.ttl != echoFullTTL {
			t.Errorf("echo %d TTL = %d, want %d (echoFullTTL)", i, wp.ttl, echoFullTTL)
		}
	}
}

// TestEchoInvariant3_SeqResolvesIPTTLPairV6: v6 mirror of Invariant 3 with
// RFC 3849 fixtures.
func TestEchoInvariant3_SeqResolvesIPTTLPairV6(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 6
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.RoundDelay = 10 * time.Millisecond
	cfg.EchoID = 0xE66E
	cfg.EchoBudget = 4
	target := net.ParseIP("2001:db8::1")
	cfg.TargetIP = target
	tr := NewTracer(target, cfg)

	ip := net.ParseIP("2001:db8::7")
	tr.SubmitEchoTargets([]EchoTarget{{IP: ip, TTL: 9}, {IP: ip, TTL: 10}})

	snd := &fakeSender{}
	seq := 0
	tr.sendEchoes(snd, &seq)

	pairs := echoPacketPairs(snd)
	if len(pairs) != 2 {
		t.Fatalf("v6: expected 2 echoes for two {IP,TTL} pairs, got %d", len(pairs))
	}
	if pairs[0][1] == pairs[1][1] {
		t.Errorf("v6: expected distinct seqs, got %d and %d", pairs[0][1], pairs[1][1])
	}

	for i, p := range pairs {
		conn := newFakeConn()
		conn.push(marshalEchoReplyV6(p[0], p[1]), fakeAddr{ip.String()})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		results := make(chan Result, 1)
		go tr.listen(ctx, conn, results)

		timer := time.NewTimer(time.Second)
		select {
		case r := <-results:
			timer.Stop()
			wantTTL := 9
			if i == 1 {
				wantTTL = 10
			}
			if r.TTL != wantTTL {
				t.Errorf("v6 pair %d: Result.TTL = %d, want %d", i, r.TTL, wantTTL)
			}
			if r.ProbeType != ProbeICMPEcho {
				t.Errorf("v6 pair %d: Result.ProbeType = %v, want ProbeICMPEcho", i, r.ProbeType)
			}
		case <-timer.C:
			t.Fatalf("v6 pair %d: timed out waiting for echo Result", i)
		}
		cancel()
	}
}

// TestEchoCursorSurvivesConcurrentShrink: regression for the cursor writeback
// race. Advance the cursor to index 8 by running sendEchoes with budget 4
// twice (consuming targets[0..3] then [4..7]), then shrink the target set to
// 2 targets via SubmitEchoTargets, then call sendEchoes again. Before the fix
// the stale cursor value (8) would be written back unchecked and the next
// sendEchoes call would index out of range on the 2-entry slice. With the fix
// the cursor is re-bounded and no panic occurs.
func TestEchoCursorSurvivesConcurrentShrink(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.EchoID = 0xCC11
	cfg.EchoBudget = 4
	target := net.ParseIP("192.0.2.1")
	tr := NewTracer(target, cfg)

	// Submit 10 targets; two rounds of sendEchoes advance cursor to 8.
	targets10 := make([]EchoTarget, 10)
	for i := 0; i < 10; i++ {
		targets10[i] = EchoTarget{IP: net.ParseIP("198.51.100.1"), TTL: i + 1}
	}
	tr.SubmitEchoTargets(targets10)

	snd := &fakeSender{}
	seq := 0
	tr.sendEchoes(snd, &seq) // cursor advances to 4
	tr.sendEchoes(snd, &seq) // cursor advances to 8

	tr.echo.mu.Lock()
	cursorBefore := tr.echo.cursor
	tr.echo.mu.Unlock()
	if cursorBefore != 8 {
		t.Fatalf("expected cursor=8 after two rounds of 4, got %d", cursorBefore)
	}

	// Replace with a 2-target set — cursor (8) is now out of range.
	tr.SubmitEchoTargets([]EchoTarget{
		{IP: net.ParseIP("198.51.100.1"), TTL: 1},
		{IP: net.ParseIP("198.51.100.2"), TTL: 2},
	})

	// sendEchoes must not panic, and cursor must be within bounds afterward.
	tr.sendEchoes(snd, &seq)

	tr.echo.mu.Lock()
	cursorAfter := tr.echo.cursor
	lenAfter := len(tr.echo.targets)
	tr.echo.mu.Unlock()

	if lenAfter > 0 && cursorAfter >= lenAfter {
		t.Errorf("cursor out of bounds after shrink: cursor=%d len=%d", cursorAfter, lenAfter)
	}
}

// TestEchoSendFailureLeavesNoProbeRecord: regression for orphan probeMap
// entries on send failure. A fake sender whose WriteTo always returns an error
// must not leave probe records in the probeMap after sendEchoes returns.
func TestEchoSendFailureLeavesNoProbeRecord(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 4
	cfg.Protocol = NewICMPProtocol(cfg)
	cfg.EchoID = 0xCC22
	cfg.EchoBudget = 3
	target := net.ParseIP("192.0.2.1")
	tr := NewTracer(target, cfg)

	tr.SubmitEchoTargets([]EchoTarget{
		{IP: net.ParseIP("198.51.100.1"), TTL: 1},
		{IP: net.ParseIP("198.51.100.2"), TTL: 2},
		{IP: net.ParseIP("198.51.100.3"), TTL: 3},
	})

	snd := &errSender{}
	seq := 0
	tr.sendEchoes(snd, &seq)

	tr.pm.mu.Lock()
	remaining := len(tr.pm.items)
	tr.pm.mu.Unlock()
	if remaining != 0 {
		t.Errorf("probeMap has %d orphan entries after send failure, want 0", remaining)
	}
}

// errSender is a probeSender whose WriteTo always returns an error, used to
// exercise the send-failure cleanup path in sendEchoes.
type errSender struct{}

func (e *errSender) SetTTL(_ int) error                          { return nil }
func (e *errSender) WriteTo(_ []byte, _ net.Addr) (int, error)  { return 0, net.ErrClosed }

// TestEchoInvariant1_NoHopTableImport is the structural spec check: the
// probe package must not import the hop package, so seq→pair resolution
// cannot fall back to a global IP→TTL lookup.
func TestEchoInvariant1_NoHopTableImport(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse package dir: %v", err)
	}
	for _, pkg := range pkgs {
		for fname, f := range pkg.Files {
			for _, imp := range f.Imports {
				if strings.Contains(imp.Path.Value, "internal/hop") {
					t.Errorf("%s imports %s: probe must not reference the hop package (invariant 1)", fname, imp.Path.Value)
				}
			}
		}
	}
}
