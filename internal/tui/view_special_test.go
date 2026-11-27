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

// withRateLimitedHop makes the hop at the given TTL rate-limited
// (hop.Hop.RateLimitState() == hop.RateLimitYes): TTL-probe loss significantly
// above echo loss, with at least 20 echo samples.
//
// hopsFull() already seeded TTL 3 with one response sample (IP 203.0.113.10).
// We layer on top: close that as round 1, answered. Then 8 rounds in which the
// TTL probe goes unanswered and the node answers every direct echo, 20 echoes
// in all (0% echo loss). The classifier reads that as rate limiting; the loss
// cell shows the echo-derived figure, ~0%†.
//
// The echoes are sent and answered inside their rounds, before each round
// closes, as in a live trace. Each of those rounds therefore writes an echo
// cell (▓) to the sparkline, not a drop: docs/UI-DESIGN.md says a drop is
// written only when no tier reached the hop, so a rate-limited ~0%† never
// sits beside drop cells.
//
// NOTE: callers must apply this AFTER hopsFull() so the sample is already seeded.
func withRateLimitedHop(ttl int) testOpt {
	return func(m *Model) {
		h := m.sessions[0].table.GetOrCreate(ttl)
		// Round 1: mark as sent and received (the sample from hopsFull counts).
		h.MarkRoundSent()
		h.MarkRoundEnd() // respondedThisRound is true from hopsFull's AddSample
		// Rounds 2-9: no answer to the TTL probe; every echo answered. 20
		// echoes over 8 rounds: three in each of the first four, two in each
		// of the rest. The classifier needs 20.
		pn := h.PrimaryNode()
		for i := 0; i < 8; i++ {
			h.MarkRoundSent()
			echoes := 2
			if i < 4 {
				echoes = 3
			}
			for j := 0; pn != nil && j < echoes; j++ {
				pn.MarkEchoSent()
				pn.AddSample(20*time.Millisecond, 0, hop.KindEcho)
			}
			h.MarkRoundEnd()
		}
		// roundsSent=9, roundsRecvd=1: TTL-probe loss 8/9 ≈ 88.9%, far above
		// the echo loss of 0/20 → RateLimitYes. The loss cell shows the echo
		// figure, ~0%†, not 89%.
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
