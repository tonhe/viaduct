package tui

import (
	"path/filepath"
	"testing"
	"time"
)

// hopsFull returns a testOpt that seeds the hop table with a full 4-hop path
// using RFC 5737 documentation addresses.
func hopsFull() testOpt {
	return withHops(
		hopSample(1, "192.0.2.1", 5*time.Millisecond),
		hopSample(2, "198.51.100.5", 10*time.Millisecond),
		hopSample(3, "203.0.113.10", 20*time.Millisecond),
		hopSample(4, "203.0.113.20", 25*time.Millisecond),
	)
}

// hopsSingle returns a testOpt that seeds the hop table with a single hop.
func hopsSingle() testOpt {
	return withHops(
		hopSample(1, "192.0.2.1", 5*time.Millisecond),
	)
}

// withTargetHit sets m.targetHit = true and m.maxTTLHit = ttl so the status
// bar renders the "Tracing... N/M hops" form used when the destination is reached.
func withTargetHit(ttl int) testOpt {
	return func(m *Model) {
		m.targetHit = true
		m.maxTTLHit = ttl
	}
}

func TestGolden_HopTable(t *testing.T) {
	setTestTheme(t)

	cases := []struct {
		name    string
		width   int
		height  int
		view    ViewMode
		hopsOpt testOpt
		extras  []testOpt
	}{
		{
			name:    "w40_default_empty",
			width:   40,
			height:  24,
			view:    ViewDefault,
			hopsOpt: nil,
		},
		{
			name:    "w80_default_full",
			width:   80,
			height:  24,
			view:    ViewDefault,
			hopsOpt: hopsFull(),
		},
		{
			name:    "w120_default_full",
			width:   120,
			height:  24,
			view:    ViewDefault,
			hopsOpt: hopsFull(),
		},
		{
			name:    "w160_default_full",
			width:   160,
			height:  24,
			view:    ViewDefault,
			hopsOpt: hopsFull(),
		},
		{
			name:    "w80_health_full",
			width:   80,
			height:  24,
			view:    ViewHealth,
			hopsOpt: hopsFull(),
		},
		{
			name:    "w120_health_full",
			width:   120,
			height:  24,
			view:    ViewHealth,
			hopsOpt: hopsFull(),
		},
		{
			name:    "w80_latency_full",
			width:   80,
			height:  24,
			view:    ViewLatency,
			hopsOpt: hopsFull(),
		},
		{
			name:    "w120_latency_full",
			width:   120,
			height:  24,
			view:    ViewLatency,
			hopsOpt: hopsFull(),
		},
		{
			name:    "w80_variability_full",
			width:   80,
			height:  24,
			view:    ViewVariability,
			hopsOpt: hopsFull(),
		},
		{
			name:    "w120_variability_full",
			width:   120,
			height:  24,
			view:    ViewVariability,
			hopsOpt: hopsFull(),
		},
		{
			name:    "w80_default_single",
			width:   80,
			height:  24,
			view:    ViewDefault,
			hopsOpt: hopsSingle(),
		},
		{
			name:    "w120_default_targethit",
			width:   120,
			height:  24,
			view:    ViewDefault,
			hopsOpt: hopsFull(),
			extras:  []testOpt{withTargetHit(4)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := []testOpt{withSize(tc.width, tc.height), withView(tc.view)}
			if tc.hopsOpt != nil {
				opts = append(opts, tc.hopsOpt)
			}
			opts = append(opts, tc.extras...)

			m := newTestModel(opts...)
			got := m.View()
			path := filepath.Join("testdata", "view", "hoptable_"+tc.name+".golden")
			goldenCompare(t, path, got)
		})
	}
}
