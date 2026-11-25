package probe

import (
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// EchoTarget is one {IP, TTL} pair eligible for ground-truth echo probing.
// Keyed as a pair because IP→TTL is not a function: the destination appears
// at multiple TTLs and ECMP places one IP at different TTLs across flows.
type EchoTarget struct {
	IP  net.IP
	TTL int
}

// echoState is the per-tracer echo tier. Nothing here is shared across
// sessions (demux invariant 2).
type echoState struct {
	mu      sync.Mutex
	targets []EchoTarget
	cursor  int
}

// SubmitEchoTargets replaces the tracer's echo-target set wholesale
// (demux invariant 4: recomputed from the live hop table every round, so
// the set self-heals across route changes and cannot grow stale).
//
// At most maxEchoTargetsPerTTL targets are kept for each TTL: the first ones
// submitted, in order.
func (t *Tracer) SubmitEchoTargets(targets []EchoTarget) {
	kept := make([]EchoTarget, 0, len(targets))
	perTTL := make(map[int]int)
	for _, tgt := range targets {
		if perTTL[tgt.TTL] >= maxEchoTargetsPerTTL {
			continue
		}
		perTTL[tgt.TTL]++
		kept = append(kept, tgt)
	}

	t.echo.mu.Lock()
	defer t.echo.mu.Unlock()
	t.echo.targets = kept
	if len(t.echo.targets) == 0 {
		t.echo.cursor = 0
	} else {
		t.echo.cursor %= len(t.echo.targets)
	}
}

// SetOnEchoSent registers the per-send attribution callback (ip, ttl).
func (t *Tracer) SetOnEchoSent(f func(ip net.IP, ttl int)) { t.onEchoSent = f }

const echoFullTTL = 64

// maxEchoTargetsPerTTL bounds how many addresses the echo tier pings at one
// hop distance. A hop legitimately has a handful of ECMP nodes; the cap keeps
// forged replies from growing the set of addresses via pings without bound.
const maxEchoTargetsPerTTL = 8

// unzonedLinkLocal reports whether ip is an IPv6 link-local address. An echo
// target is an address without a zone (Result.IP carries none), and a
// link-local address without its zone does not say which link it is on: the
// kernel refuses the packet or picks an interface itself. Such a node is not
// echoed. It then has no echo samples, so loss at its hop stays provisional
// ("N%?") instead of being judged against echoes that could not be delivered.
func unzonedLinkLocal(ip net.IP) bool {
	return ip.To4() == nil && ip.IsLinkLocalUnicast()
}

// sendEchoes emits up to budget echo probes, round-robining through the
// target set across rounds. seq resolution: each send registers its pair in
// the probeMap keyed {EchoID, seq}, so the reply attributes to the exact
// {IP, TTL} entry (demux invariant 3).
//
// Nothing is sent while the trace is paused. A send that fails or times out
// is not an echo sent: its record is removed and the echo-sent callback is
// not called. Once a write on the socket has timed out this round (here or
// in the TTL tier, which shares the socket), the remaining echoes of the
// round are not attempted.
func (t *Tracer) sendEchoes(snd probeSender, seq *int) {
	if atomic.LoadInt32(&t.Paused) == 1 {
		return
	}
	// EXPERIMENT ONLY, not for merge: a file named via-exp-no-echo in the
	// working directory switches the echo tier off, to test whether the echo
	// tier is what makes macOS drop IPv6 UDP probes (viaduct#115).
	if _, err := os.Stat("via-exp-no-echo"); err == nil {
		return
	}
	budget := t.cfg.EchoBudget
	if budget <= 0 {
		budget = 64
	}
	t.echo.mu.Lock()
	targets := append([]EchoTarget(nil), t.echo.targets...)
	cursor := t.echo.cursor
	t.echo.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	if budget > len(targets) {
		budget = len(targets)
	}
	for i := 0; i < budget; i++ {
		if atomic.LoadInt32(&t.Paused) == 1 {
			break
		}
		// The socket stopped accepting writes this round: leave the rest of
		// the budget for the next round, cursor in place.
		if t.sends.isStalled(t.echoSock) {
			t.sends.counts[ProbeICMPEcho].skipped += budget - i
			break
		}
		tgt := targets[cursor]
		cursor = (cursor + 1) % len(targets)
		if unzonedLinkLocal(tgt.IP) {
			t.logOnce("echo-skip "+tgt.IP.String(),
				"echo not sent to %s (hop %d): link-local address without a zone; logged once for this address", tgt.IP, tgt.TTL)
			continue
		}
		*seq++
		// Keyed on the 16-bit sequence the echo carries (see icmpWireSeq).
		wire := icmpWireSeq(*seq)
		pkt, err := buildICMPEchoRequest(t.cfg.IPVersion, t.cfg.EchoID, wire, t.cfg.PayloadSize)
		if err != nil {
			continue
		}
		key := probeKey{SrcPort: t.cfg.EchoID, DstPort: wire, Seq: 0}
		// The reply must come from the address this echo is sent to.
		t.pm.addTo(key, tgt.TTL, 0, time.Now(), ProbeICMPEcho, tgt.IP)
		if !t.send(snd, t.echoSock, ProbeICMPEcho, echoFullTTL, pkt, &net.IPAddr{IP: tgt.IP}) {
			t.pm.remove(key)
			continue
		}
		if t.onEchoSent != nil {
			t.onEchoSent(tgt.IP, tgt.TTL)
		}
		time.Sleep(t.cfg.ProbeDelay)
	}
	t.echo.mu.Lock()
	if len(t.echo.targets) > 0 {
		t.echo.cursor = cursor % len(t.echo.targets)
	} else {
		t.echo.cursor = 0
	}
	t.echo.mu.Unlock()
}
