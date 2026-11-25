package tui

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// A destination that ignores ping has no node answering echo, so none of its
// echoes are left out of the hop's totals (viaduct#124): its loss is measured
// and the loss alert fires on it, as before.
func TestAlert_DestinationThatIgnoresEchoStillAlerts(t *testing.T) {
	dst := net.ParseIP("198.51.100.9")
	h := hop.NewHop(3)
	hopMap := map[int]*hop.Hop{3: h}
	engine := NewAlertEngine(5.0, 0, 3)

	var loss float64
	for round := 0; round < 40; round++ {
		// Seven TTL-limited probes, one answered; one echo, never answered.
		h.MarkRoundSentProbes(7)
		h.AddSample(dst, 10*time.Millisecond, 0, hop.KindICMPTTL)
		for _, n := range h.GetNodes() {
			n.MarkEchoSent()
		}
		h.MarkRoundEnd()

		loss, _ = destAlertMetrics(hopMap, 3)
		engine.Update(loss, 0)
	}

	if got, kind := h.Loss(); kind != hop.LossMeasured {
		t.Errorf("hop Loss = %.1f, kind %v; want LossMeasured", got, kind)
	}
	if want := 6.0 / 7.0 * 100; loss < want-0.01 || loss > want+0.01 {
		t.Errorf("destAlertMetrics loss = %.2f, want %.2f", loss, want)
	}
	if engine.State() != AlertDegraded {
		t.Fatalf("alert state = %v, want AlertDegraded: sustained measured loss at the destination", engine.State())
	}
	if msg := engine.Message(); !strings.Contains(msg, "85.7%") {
		t.Errorf("alert message = %q, want it to carry the measured loss 85.7%%", msg)
	}
}
