package probe

import (
	"fmt"
	"net"
	"testing"
)

// echoTargets returns n targets, one per TTL starting at 1.
func echoTargets(n int) []EchoTarget {
	var out []EchoTarget
	for i := 1; i <= n; i++ {
		out = append(out, EchoTarget{IP: net.ParseIP(fmt.Sprintf("192.0.2.%d", i)), TTL: i})
	}
	return out
}

func TestEchoRounds(t *testing.T) {
	// Twelve addresses at one TTL: only maxEchoTargetsPerTTL (8) are kept.
	var crowded []EchoTarget
	for i := 1; i <= 12; i++ {
		crowded = append(crowded, EchoTarget{IP: net.ParseIP(fmt.Sprintf("198.51.100.%d", i)), TTL: 5})
	}
	cases := []struct {
		name    string
		targets []EchoTarget
		budget  int
		want    int
	}{
		{"no targets", nil, 4, 1},
		{"fits in one round", echoTargets(4), 4, 1},
		{"one over the budget", echoTargets(5), 4, 2},
		{"exact multiple", echoTargets(12), 4, 3},
		{"remainder rounds up", echoTargets(25), 4, 7},
		{"zero budget means 64", echoTargets(64), 0, 1},
		{"zero budget, 65 targets", echoTargets(65), 0, 2},
		{"negative budget means 64", echoTargets(100), -1, 2},
		{"per-TTL cap applies", crowded, 4, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EchoRounds(tc.targets, tc.budget); got != tc.want {
				t.Errorf("EchoRounds(%d targets, budget %d) = %d, want %d", len(tc.targets), tc.budget, got, tc.want)
			}
		})
	}
}

// EchoRounds counts the targets SubmitEchoTargets keeps, so the figure it
// gives is the number of rounds sendEchoes takes to cover the stored set.
func TestEchoRounds_MatchesSubmittedSet(t *testing.T) {
	targets := append(echoTargets(6), EchoTarget{IP: net.ParseIP("203.0.113.1"), TTL: 3})
	for i := 0; i < 10; i++ {
		targets = append(targets, EchoTarget{IP: net.ParseIP(fmt.Sprintf("203.0.113.%d", 10+i)), TTL: 4})
	}
	tr := &Tracer{}
	tr.SubmitEchoTargets(targets)
	kept := len(tr.echo.targets)
	const budget = 3
	want := (kept + budget - 1) / budget
	if got := EchoRounds(targets, budget); got != want {
		t.Errorf("EchoRounds = %d, want %d (%d targets kept)", got, want, kept)
	}
}
