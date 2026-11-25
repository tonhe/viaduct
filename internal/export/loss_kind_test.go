package export

import (
	"net"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// The JSON carries the figure the views show, and what kind of figure it is,
// next to the raw TTL-probe loss (viaduct#71).

// fillHop runs rounds on the table's hop at ttl: 7 TTL-limited probes a
// round, the ICMP probe and answeredUDP UDP probes answered, one echo a
// round, always answered.
func fillHop(tbl *hop.Table, ttl int, ip string, rounds, answeredUDP int, target bool) {
	h := tbl.GetOrCreate(ttl)
	addr := net.ParseIP(ip)
	if target {
		h.MarkTarget(addr)
	}
	for r := 0; r < rounds; r++ {
		h.MarkRoundSentProbes(7)
		h.AddSample(addr, 10*time.Millisecond, 0, hop.KindICMPTTL)
		for f := 0; f < answeredUDP; f++ {
			h.AddSample(addr, 10*time.Millisecond, f, hop.KindPrimary)
		}
		for _, n := range h.GetNodes() {
			n.MarkEchoSent()
			h.AddSample(n.GetIP(), 10*time.Millisecond, 0, hop.KindEcho)
		}
		h.MarkRoundEnd()
	}
}

func TestBuildReport_LossKinds(t *testing.T) {
	tbl := hop.NewTable(30)
	fillHop(tbl, 1, "192.0.2.1", 45, 6, false)   // clean
	fillHop(tbl, 2, "192.0.2.2", 12, 0, false)   // lossy, verdict not yet possible
	fillHop(tbl, 3, "198.51.100.1", 45, 4, true) // destination throttling its errors

	r := BuildReport("test", "example.net", net.ParseIP("198.51.100.1"), "udp", tbl, nil, nil, 3)
	if len(r.Hops) != 3 {
		t.Fatalf("got %d hops, want 3", len(r.Hops))
	}
	want := []struct {
		kind     string
		headline float64
		limited  string
	}{
		{"measured", 0, "no"},
		{"provisional", r.Hops[1].Loss, "undetermined"},
		{"rate_limited", 0, "yes"},
	}
	for i, w := range want {
		h := r.Hops[i]
		if h.LossKind != w.kind {
			t.Errorf("hop %d loss_kind = %q, want %q", h.TTL, h.LossKind, w.kind)
		}
		if h.HeadlineLoss != w.headline {
			t.Errorf("hop %d headline_loss_pct = %.2f, want %.2f", h.TTL, h.HeadlineLoss, w.headline)
		}
		if got := h.Nodes[0].RateLimited; got != w.limited {
			t.Errorf("hop %d node rate_limited = %q, want %q", h.TTL, got, w.limited)
		}
	}
	if raw := r.Hops[2].Loss; raw < 25 {
		t.Errorf("destination loss_pct = %.1f, want the raw TTL-probe loss (about 28.6) kept", raw)
	}
}
