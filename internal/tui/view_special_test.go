package tui

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// --- ECMP fixtures ---

// withECMPSmall seeds the hop table with a 3-hop path where TTL 3 diverges
// into two distinct IPs across flow IDs, producing a small ECMP tree.
func withECMPSmall() testOpt {
	return func(m *Model) {
		// TTL 1: single node, both flows see same IP
		m.sessions[0].table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 0, hop.KindPrimary)
		m.sessions[0].table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 1, hop.KindPrimary)
		// TTL 2: single node, both flows see same IP
		m.sessions[0].table.GetOrCreate(2).AddSample(net.ParseIP("198.51.100.5"), 10*time.Millisecond, 0, hop.KindPrimary)
		m.sessions[0].table.GetOrCreate(2).AddSample(net.ParseIP("198.51.100.5"), 10*time.Millisecond, 1, hop.KindPrimary)
		// TTL 3: ECMP divergence — flow 0 and flow 1 see different IPs
		m.sessions[0].table.GetOrCreate(3).AddSample(net.ParseIP("203.0.113.10"), 20*time.Millisecond, 0, hop.KindPrimary)
		m.sessions[0].table.GetOrCreate(3).AddSample(net.ParseIP("203.0.113.20"), 22*time.Millisecond, 1, hop.KindPrimary)
	}
}

// withECMPDivergent seeds a 4-hop path where hops 2-4 each diverge into
// 2-3 distinct nodes, exercising the space-allocation distribution logic.
func withECMPDivergent() testOpt {
	return func(m *Model) {
		// TTL 1: single convergent hop
		m.sessions[0].table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 0, hop.KindPrimary)
		m.sessions[0].table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 1, hop.KindPrimary)
		m.sessions[0].table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 2, hop.KindPrimary)

		// TTL 2: two-way split
		m.sessions[0].table.GetOrCreate(2).AddSample(net.ParseIP("198.51.100.1"), 10*time.Millisecond, 0, hop.KindPrimary)
		m.sessions[0].table.GetOrCreate(2).AddSample(net.ParseIP("198.51.100.2"), 11*time.Millisecond, 1, hop.KindPrimary)
		m.sessions[0].table.GetOrCreate(2).AddSample(net.ParseIP("198.51.100.1"), 10*time.Millisecond, 2, hop.KindPrimary)

		// TTL 3: three-way split
		m.sessions[0].table.GetOrCreate(3).AddSample(net.ParseIP("203.0.113.10"), 20*time.Millisecond, 0, hop.KindPrimary)
		m.sessions[0].table.GetOrCreate(3).AddSample(net.ParseIP("203.0.113.20"), 22*time.Millisecond, 1, hop.KindPrimary)
		m.sessions[0].table.GetOrCreate(3).AddSample(net.ParseIP("203.0.113.30"), 24*time.Millisecond, 2, hop.KindPrimary)

		// TTL 4: two-way split (reconvergence partially)
		m.sessions[0].table.GetOrCreate(4).AddSample(net.ParseIP("198.51.100.10"), 30*time.Millisecond, 0, hop.KindPrimary)
		m.sessions[0].table.GetOrCreate(4).AddSample(net.ParseIP("198.51.100.11"), 32*time.Millisecond, 1, hop.KindPrimary)
		m.sessions[0].table.GetOrCreate(4).AddSample(net.ParseIP("198.51.100.10"), 30*time.Millisecond, 2, hop.KindPrimary)
	}
}

// --- IPv6 fixtures ---

// hopsIPv6Full seeds the hop table with 4 IPv6 hops using RFC 3849 addresses.
func hopsIPv6Full() testOpt {
	return func(m *Model) {
		addrs := []string{
			"2001:db8:1::1",
			"2001:db8:2::1",
			"2001:db8:3::5",
			"2001:db8:cafe::1",
		}
		for i, a := range addrs {
			m.sessions[0].table.GetOrCreate(i+1).AddSample(net.ParseIP(a), time.Duration(5+i*5)*time.Millisecond, 0, hop.KindPrimary)
		}
	}
}

// --- Rate-limited fixture ---

// withRateLimitedHop drives the new per-node tri-state rate-limit detection
// (hop.RateLimitYes) for the given TTL. The algorithm requires:
//   - pn.icmpTTL.RoundLossPercent() > 0
//   - pn.icmpEcho.Sent >= rateLimitMinEchoSamples (5)
//   - pn.icmpEcho.ProbeLossPercent() < pn.icmpTTL.RoundLossPercent() - 5.0
//
// hopsFull() already seeded TTL 3 with one response sample (IP 203.0.113.10).
// We layer on top: mark that sample as "round received" then add 8 more silent
// rounds → heavy TTL probe loss. Then add 20 echo probes, all received (0%
// echo loss), which the classifier reads as rate limiting; the loss cell
// shows the echo-derived figure, ~0%†.
//
// NOTE: callers must apply this AFTER hopsFull() so the sample is already seeded.
func withRateLimitedHop(ttl int) testOpt {
	return func(m *Model) {
		h := m.sessions[0].table.GetOrCreate(ttl)
		// Round 1: mark as sent and received (the sample from hopsFull counts).
		h.MarkRoundSent()
		h.MarkRoundEnd() // respondedThisRound is true from hopsFull's AddSample
		// Rounds 2-9: silent — no response.
		for i := 0; i < 8; i++ {
			h.MarkRoundSent()
			h.MarkRoundEnd()
		}
		// roundsSent=9, roundsRecvd=1 → 8/9 ≈ 88.9% → displayed as ~89%.
		// Now drive echo ground-truth on the primary PathNode: 20 sent, 20
		// received (0% echo loss). TTL probe loss far above echo loss with
		// enough echo samples (the classifier needs 20) → RateLimitYes.
		pn := h.PrimaryNode()
		if pn != nil {
			for i := 0; i < 20; i++ {
				pn.MarkEchoSent()
				pn.AddSample(20*time.Millisecond, 0, hop.KindEcho)
			}
		}
	}
}

// --- Alert fixtures ---

// withAlertHealthy attaches an alert engine with thresholds configured
// but left in the default healthy state (no Update calls).
func withAlertHealthy() testOpt {
	return func(m *Model) {
		m.sessions[0].alertEngine = NewAlertEngine(5.0, 200*time.Millisecond, 3)
		// Default state is AlertHealthy; leave as-is.
	}
}

// withAlertDegraded attaches an alert engine and directly seeds it into
// AlertDegraded state with peak loss data, using same-package field access.
func withAlertDegraded() testOpt {
	return func(m *Model) {
		m.sessions[0].alertEngine = NewAlertEngine(5.0, 200*time.Millisecond, 3)
		m.sessions[0].alertEngine.state = AlertDegraded
		m.sessions[0].alertEngine.peakLoss = 12.5
		m.sessions[0].alertEngine.affectedHop = 3
		m.sessions[0].alertEngine.consecutiveBad = 3
	}
}

// --- Test ---

func TestGolden_Special(t *testing.T) {
	setTestTheme(t)

	cases := []struct {
		name string
		opts []testOpt
	}{
		{
			"ecmp_small",
			[]testOpt{withSize(120, 24), withECMPSmall()},
		},
		{
			"ecmp_divergent",
			[]testOpt{withSize(120, 24), withECMPDivergent()},
		},
		{
			"ipv6_full",
			[]testOpt{withSize(160, 24), withIPVersion(6), hopsIPv6Full()},
		},
		{
			"ipv6_narrow",
			[]testOpt{withSize(80, 24), withIPVersion(6), hopsIPv6Full()},
		},
		{
			"ratelimit",
			[]testOpt{withSize(120, 24), hopsFull(), withRateLimitedHop(3)},
		},
		{
			"alert_healthy",
			[]testOpt{withSize(120, 24), hopsFull(), withAlertHealthy()},
		},
		{
			"alert_degraded",
			[]testOpt{withSize(120, 24), hopsFull(), withAlertDegraded()},
		},
		{
			"ipv6_alert",
			[]testOpt{withSize(160, 24), withIPVersion(6), hopsIPv6Full(), withAlertDegraded()},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(tc.opts...)
			got := m.View()
			path := filepath.Join("testdata", "view", "special_"+tc.name+".golden")
			goldenCompare(t, path, got)
		})
	}
}
