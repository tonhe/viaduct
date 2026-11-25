package export

import (
	"net"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// Once a hop has had a verdict, loss_kind never returns to "provisional"
// (viaduct#149): while the hop's echo totals are below 20 samples the report
// carries the held verdict, and its figures still agree with each other.

// heldVerdictRound runs one round on h: seven TTL-limited probes, the ICMP
// one answered by ttlFrom ("" for none), then one echo to every known node,
// answered by the nodes named in echoFrom.
func heldVerdictRound(h *hop.Hop, ttlFrom string, echoFrom ...string) {
	h.MarkRoundSentProbes(7)
	if ttlFrom != "" {
		h.AddSample(net.ParseIP(ttlFrom), 10*time.Millisecond, 0, hop.KindICMPTTL)
	}
	nodes := h.GetNodes()
	for _, n := range nodes {
		n.MarkEchoSent()
	}
	for _, n := range nodes {
		for _, ip := range echoFrom {
			if n.GetIP().String() == ip {
				h.AddSample(n.GetIP(), 10*time.Millisecond, 0, hop.KindEcho)
			}
		}
	}
	h.MarkRoundEnd()
}

func TestBuildReport_LossKindNeverReturnsToProvisional(t *testing.T) {
	const silent, answering = "192.0.2.32", "192.0.2.31"

	for _, tc := range []struct {
		name string
		// round runs round number r of the scenario.
		round func(h *hop.Hop, r int)
		// dropAt is the round in which the echo totals fall below 20, and
		// newAt the round in which the new totals reach 20.
		dropAt, newAt int
		heldKind      string
		heldLimited   string
	}{
		{
			// The silent node has the hop to itself for 25 rounds (measured);
			// then a node that answers echo appears and the silent one leaves
			// the totals.
			name: "measured",
			round: func(h *hop.Hop, r int) {
				if r < 25 {
					heldVerdictRound(h, silent)
					return
				}
				heldVerdictRound(h, answering, answering)
			},
			dropAt: 25, newAt: 44,
			heldKind: "measured", heldLimited: "no",
		},
		{
			// The answering node appears in round 12; the silent one stays in
			// the totals until its 20th ignored echo in round 19, with the
			// hop rate-limited by then.
			name: "rate_limited",
			round: func(h *hop.Hop, r int) {
				switch r {
				case 0:
					heldVerdictRound(h, silent, answering)
				case 12:
					heldVerdictRound(h, answering, answering)
				default:
					heldVerdictRound(h, "", answering)
				}
			},
			dropAt: 19, newAt: 31,
			heldKind: "rate_limited", heldLimited: "yes",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tbl := hop.NewTable(30)
			h := tbl.GetOrCreate(1)
			report := func() Hop {
				r := BuildReport("test", "example.net", net.ParseIP("198.51.100.1"), "udp", tbl, nil, nil, 1)
				if len(r.Hops) != 1 {
					t.Fatalf("got %d hops, want 1", len(r.Hops))
				}
				return r.Hops[0]
			}

			for r := 0; r < tc.dropAt; r++ {
				tc.round(h, r)
			}
			before := report()
			if before.LossKind != tc.heldKind {
				t.Fatalf("fixture: loss_kind = %q before the totals fall, want %q", before.LossKind, tc.heldKind)
			}

			for r := tc.dropAt; r < tc.newAt; r++ {
				tc.round(h, r)
				got := report()
				if got.LossKind != tc.heldKind {
					t.Errorf("round %d: loss_kind = %q, want the held %q", r, got.LossKind, tc.heldKind)
				}
				for _, n := range got.Nodes {
					if n.RateLimited != tc.heldLimited {
						t.Errorf("round %d: node %s rate_limited = %q, want %q", r, n.IP, n.RateLimited, tc.heldLimited)
					}
				}
				switch tc.heldKind {
				case "measured":
					// A measured headline is the raw TTL-probe loss, also
					// while the verdict is held.
					if got.HeadlineLoss != got.Loss {
						t.Errorf("round %d: headline_loss_pct = %.2f, loss_pct = %.2f; want them equal for a measured hop",
							r, got.HeadlineLoss, got.Loss)
					}
				case "rate_limited":
					if got.HeadlineLoss != before.HeadlineLoss {
						t.Errorf("round %d: headline_loss_pct = %.2f, want the held echo loss %.2f",
							r, got.HeadlineLoss, before.HeadlineLoss)
					}
				}
			}

			tc.round(h, tc.newAt)
			after := report()
			if after.LossKind != "rate_limited" || after.HeadlineLoss != 0 {
				t.Errorf("with 20 new echo samples: loss_kind %q, headline_loss_pct %.2f; want rate_limited, 0",
					after.LossKind, after.HeadlineLoss)
			}
		})
	}
}
