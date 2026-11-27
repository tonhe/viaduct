package ecmp

import (
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// tableOf builds a hop table from the number of nodes at each TTL, starting at
// firstTTL. A count of -1 leaves that TTL out of the table (nothing was sent
// to it or seen at it); 0 makes a hop row with no node (probed, never
// answered). Addresses are from 192.0.2.0/24 (RFC 5737): node n of the hop at
// TTL t is 192.0.2.(10t+n).
func tableOf(firstTTL int, nodes ...int) *hop.Table {
	table := hop.NewTable(30)
	for i, count := range nodes {
		ttl := firstTTL + i
		if count < 0 {
			continue
		}
		h := table.GetOrCreate(ttl)
		for n := 0; n < count; n++ {
			ip := net.ParseIP(fmt.Sprintf("192.0.2.%d", 10*ttl+n))
			h.AddSample(ip, time.Duration(ttl)*time.Millisecond, n, hop.KindPrimary)
		}
	}
	return table
}

// Analyze reports the TTL of every hop where the path splits (a hop with
// several nodes after a hop with at most one, or at the very first hop) and of
// every hop where it merges (a hop with at most one node after a hop with
// several). Both lists are checked whole in every case, so a point that is
// missing, extra or at the wrong TTL fails.
func TestAnalyze_Transitions(t *testing.T) {
	cases := []struct {
		name     string
		firstTTL int
		nodes    []int // nodes at each TTL from firstTTL; -1 = TTL absent, 0 = no node
		wantDiv  []int
		wantConv []int
	}{
		// Divergence at the first hop: there is no hop before it to compare with.
		{"split at the first hop, merged at the second", 1, []int{2, 1}, []int{1}, []int{2}},
		{"split at the first hop and still split at the last", 1, []int{2, 2}, []int{1}, nil},
		{"split at the first hop, merged at the third", 1, []int{2, 2, 1}, []int{1}, []int{3}},
		{"three nodes at the first hop", 1, []int{3, 1, 1}, []int{1}, []int{2}},

		// The convergence lookback: a merge at the second hop of the table
		// looks back at the first.
		{"merge at the second hop of the table", 1, []int{2, 1, 1, 1}, []int{1}, []int{2}},
		{"merge at the third hop of the table", 1, []int{1, 2, 1, 1}, []int{2}, []int{3}},

		// A split that lasts several hops is one divergence and one convergence.
		{"split over three hops", 1, []int{1, 2, 3, 2, 1}, []int{2}, []int{5}},
		{"two separate splits", 1, []int{1, 2, 1, 1, 2, 1}, []int{2, 5}, []int{3, 6}},
		{"splits on alternate hops", 1, []int{2, 1, 2, 1, 2}, []int{1, 3, 5}, []int{2, 4}},
		{"split at the last hop only", 1, []int{1, 1, 2}, []int{3}, nil},
		{"no split anywhere", 1, []int{1, 1, 1}, nil, nil},

		// The points are TTLs, not positions in the table.
		{"table starting at TTL 3", 3, []int{1, 2, 1}, []int{4}, []int{5}},
		{"table starting at TTL 3, split at its first hop", 3, []int{2, 1}, []int{3}, []int{4}},

		// A hop that was probed and never answered has no node: the path is
		// not split there.
		{"a silent hop after a split is a merge", 1, []int{1, 2, 0, 1}, []int{2}, []int{3}},
		{"a silent hop before a split", 1, []int{0, 2, 1}, []int{2}, []int{3}},
		{"a silent first hop, no split", 1, []int{0, 1, 1}, nil, nil},

		// A TTL with no hop row at all is skipped: the hop after it is
		// compared with the last hop that has a row.
		{"an absent TTL inside a split", 1, []int{1, 2, -1, 2, 1}, []int{2}, []int{5}},
		{"an absent TTL after a split: the merge is at the next row", 1, []int{2, -1, 1}, []int{1}, []int{3}},
		{"an absent TTL before a split", 1, []int{1, -1, 2}, []int{3}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Analyze(tableOf(tc.firstTTL, tc.nodes...))
			if !reflect.DeepEqual(got.DivergencePoints, tc.wantDiv) {
				t.Errorf("nodes per TTL %v from TTL %d: divergence at %v, want %v", tc.nodes, tc.firstTTL, got.DivergencePoints, tc.wantDiv)
			}
			if !reflect.DeepEqual(got.ConvergencePoints, tc.wantConv) {
				t.Errorf("nodes per TTL %v from TTL %d: convergence at %v, want %v", tc.nodes, tc.firstTTL, got.ConvergencePoints, tc.wantConv)
			}
		})
	}
}

// A table with fewer than two hop rows has no transition to report, even when
// its one hop has several nodes.
func TestAnalyze_FewerThanTwoHops(t *testing.T) {
	for _, tc := range []struct {
		name  string
		nodes []int
	}{
		{"no hop", nil},
		{"one hop, one node", []int{1}},
		{"one hop, two nodes", []int{2}},
		{"one hop row among absent TTLs", []int{-1, 2, -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Analyze(tableOf(1, tc.nodes...))
			if got.DivergencePoints != nil || got.ConvergencePoints != nil {
				t.Errorf("nodes per TTL %v: divergence %v, convergence %v; want none", tc.nodes, got.DivergencePoints, got.ConvergencePoints)
			}
		})
	}
}
