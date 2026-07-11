package tui

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

// --- ECMP fixtures ---

// withECMPSmall seeds the hop table with a 3-hop path where TTL 3 diverges
// into two distinct IPs across flow IDs, producing a small ECMP tree.
func withECMPSmall() testOpt {
	return func(m *Model) {
		// TTL 1: single node, both flows see same IP
		m.table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 0)
		m.table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 1)
		// TTL 2: single node, both flows see same IP
		m.table.GetOrCreate(2).AddSample(net.ParseIP("198.51.100.5"), 10*time.Millisecond, 0)
		m.table.GetOrCreate(2).AddSample(net.ParseIP("198.51.100.5"), 10*time.Millisecond, 1)
		// TTL 3: ECMP divergence — flow 0 and flow 1 see different IPs
		m.table.GetOrCreate(3).AddSample(net.ParseIP("203.0.113.10"), 20*time.Millisecond, 0)
		m.table.GetOrCreate(3).AddSample(net.ParseIP("203.0.113.20"), 22*time.Millisecond, 1)
	}
}

// withECMPDivergent seeds a 4-hop path where hops 2-4 each diverge into
// 2-3 distinct nodes, exercising the space-allocation distribution logic.
func withECMPDivergent() testOpt {
	return func(m *Model) {
		// TTL 1: single convergent hop
		m.table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 0)
		m.table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 1)
		m.table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 2)

		// TTL 2: two-way split
		m.table.GetOrCreate(2).AddSample(net.ParseIP("198.51.100.1"), 10*time.Millisecond, 0)
		m.table.GetOrCreate(2).AddSample(net.ParseIP("198.51.100.2"), 11*time.Millisecond, 1)
		m.table.GetOrCreate(2).AddSample(net.ParseIP("198.51.100.1"), 10*time.Millisecond, 2)

		// TTL 3: three-way split
		m.table.GetOrCreate(3).AddSample(net.ParseIP("203.0.113.10"), 20*time.Millisecond, 0)
		m.table.GetOrCreate(3).AddSample(net.ParseIP("203.0.113.20"), 22*time.Millisecond, 1)
		m.table.GetOrCreate(3).AddSample(net.ParseIP("203.0.113.30"), 24*time.Millisecond, 2)

		// TTL 4: two-way split (reconvergence partially)
		m.table.GetOrCreate(4).AddSample(net.ParseIP("198.51.100.10"), 30*time.Millisecond, 0)
		m.table.GetOrCreate(4).AddSample(net.ParseIP("198.51.100.11"), 32*time.Millisecond, 1)
		m.table.GetOrCreate(4).AddSample(net.ParseIP("198.51.100.10"), 30*time.Millisecond, 2)
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
			m.table.GetOrCreate(i+1).AddSample(net.ParseIP(a), time.Duration(5+i*5)*time.Millisecond, 0)
		}
	}
}

// --- Rate-limited fixture ---

// withRateLimitedHop constructs a hop at the given TTL that will trigger
// hop.DetectRateLimited detection. The algorithm requires:
//   - hop(ttl).LossPercent() > 1.0
//   - at least one downstream hop (ttl+1) with lower loss
//
// We achieve this by calling IncrementSent many times to inflate sentTotal
// while only adding one real sample (low received count = high loss).
// The downstream hop has zero extra sentTotal so its LossPercent stays 0.
//
// NOTE: Because detection is computed at render time via hop.DetectRateLimited,
// we must ensure the hops table also contains a lower-loss downstream hop.
// This fixture REPLACES any existing hops — callers must apply it after hopsFull()
// so that the base hops are already seeded, then this overrides TTL 3's sent count.
func withRateLimitedHop(ttl int) testOpt {
	return func(m *Model) {
		h := m.table.GetOrCreate(ttl)
		// Inflate sentTotal so this hop appears to have ~50% loss.
		// LossPercent = (sentTotal - received) / sentTotal * 100
		// We already have 1 received sample from hopsFull().
		// We want loss > 1%. With sentTotal=10, received=1 → 90% loss.
		// Downstream hops (from hopsFull) have sentTotal=0 so LossPercent=0.
		// DetectRateLimited: ttl < maxTTL && loss > 1.0 && minDownstream < loss-5.0
		// 0 < 90.0 - 5.0 = 85.0 → true, so TTL 3 is flagged.
		for i := 0; i < 9; i++ {
			h.IncrementSent()
		}
	}
}

// --- Alert fixtures ---

// withAlertHealthy attaches an alert engine with thresholds configured
// but left in the default healthy state (no Update calls).
func withAlertHealthy() testOpt {
	return func(m *Model) {
		m.alertEngine = NewAlertEngine(5.0, 200*time.Millisecond, 3)
		// Default state is AlertHealthy; leave as-is.
	}
}

// withAlertDegraded attaches an alert engine and directly seeds it into
// AlertDegraded state with peak loss data, using same-package field access.
func withAlertDegraded() testOpt {
	return func(m *Model) {
		m.alertEngine = NewAlertEngine(5.0, 200*time.Millisecond, 3)
		m.alertEngine.state = AlertDegraded
		m.alertEngine.peakLoss = 12.5
		m.alertEngine.affectedHop = 3
		m.alertEngine.consecutiveBad = 3
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
