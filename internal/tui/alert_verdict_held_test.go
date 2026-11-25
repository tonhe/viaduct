package tui

import (
	"net"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// Once a hop has had a verdict it keeps one (viaduct#149), so a loss alert
// does not clear and come back while the hop's echo totals change.

// heldRound runs one round on h: seven TTL-limited probes, the ICMP one
// answered by ttlFrom, then one echo to every known node, answered by the
// nodes named in echoFrom.
func heldRound(h *hop.Hop, ttlFrom string, echoFrom ...string) {
	h.MarkRoundSentProbes(7)
	h.AddSample(net.ParseIP(ttlFrom), 10*time.Millisecond, 0, hop.KindICMPTTL)
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

// A destination that ignored ping for 30 rounds starts answering. The alert
// that its measured loss raised stays up through the change.
func TestAlert_StaysUpWhenTheDestinationStartsAnsweringEcho(t *testing.T) {
	const dst = "198.51.100.9"
	h := hop.NewHop(3)
	hopMap := map[int]*hop.Hop{3: h}
	engine := NewAlertEngine(5.0, 0, 3)

	for round := 0; round < 30; round++ {
		heldRound(h, dst)
		loss, _ := destAlertMetrics(hopMap, 3)
		engine.Update(loss, 0)
	}
	if engine.State() != AlertDegraded {
		t.Fatalf("fixture: alert state = %v, want AlertDegraded before the destination answers echo", engine.State())
	}
	for round := 30; round < 60; round++ {
		heldRound(h, dst, dst)
		loss, _ := destAlertMetrics(hopMap, 3)
		if loss == 0 {
			t.Errorf("round %d: destAlertMetrics loss = 0; the hop's loss dropped out of alerting", round)
		}
		if changed := engine.Update(loss, 0); changed || engine.State() != AlertDegraded {
			t.Fatalf("round %d: alert changed = %v, state = %v; want it to stay AlertDegraded", round, changed, engine.State())
		}
		if _, kind := h.Loss(); kind == hop.LossProvisional {
			t.Errorf("round %d: hop loss is provisional again", round)
		}
	}
}

// A hop whose only node ignores ping is joined by a node that answers it. The
// silent node leaves the echo totals, which fall below 20 samples; the alert
// keeps its state until the new totals reach 20, and switches then.
func TestAlert_KeepsItsStateWhileTheVerdictIsHeld(t *testing.T) {
	const silent, answering = "192.0.2.32", "192.0.2.31"
	h := hop.NewHop(3)
	hopMap := map[int]*hop.Hop{3: h}
	engine := NewAlertEngine(5.0, 0, 3)

	for round := 0; round < 25; round++ {
		heldRound(h, silent)
		loss, _ := destAlertMetrics(hopMap, 3)
		engine.Update(loss, 0)
	}
	if engine.State() != AlertDegraded {
		t.Fatalf("fixture: alert state = %v, want AlertDegraded", engine.State())
	}
	// The answering node's echoes: one a round, the 20th in round 44.
	for round := 25; round < 44; round++ {
		heldRound(h, answering, answering)
		loss, _ := destAlertMetrics(hopMap, 3)
		if changed := engine.Update(loss, 0); changed || engine.State() != AlertDegraded {
			t.Fatalf("round %d: alert changed = %v, state = %v, loss %.1f; want it to stay AlertDegraded while the verdict is held",
				round, changed, engine.State(), loss)
		}
	}
	heldRound(h, answering, answering)
	if loss, kind := h.Loss(); kind != hop.LossRateLimited || loss != 0 {
		t.Fatalf("with 20 new echo samples hop Loss = %.1f, kind %v; want 0, LossRateLimited", loss, kind)
	}
	loss, _ := destAlertMetrics(hopMap, 3)
	if changed := engine.Update(loss, 0); !changed || engine.State() != AlertHealthy {
		t.Errorf("after the new verdict alert changed = %v, state = %v; want it to switch to AlertHealthy", changed, engine.State())
	}
}
