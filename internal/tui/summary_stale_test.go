package tui

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// Round one probes every TTL, because the destination's distance is not known
// yet, and the destination answers each probe that reaches it. The table then
// holds the destination at its own TTL and at every TTL beyond, and those
// later entries never update again. The summary panel must read the
// destination's own hop, not the last entry that ever answered (viaduct#76).

// staleTailSession builds a session whose destination is at TTL 3 and whose
// table also holds the round-one entries at TTL 4 to 6: clean, at 3 ms. The
// destination's own hop then runs 40 rounds losing 4 of 7 probes at 80 ms.
func staleTailSession() *TargetSession {
	tbl := hop.NewTable(30)
	dst := net.ParseIP("198.51.100.1")
	for ttl, ip := range map[int]string{1: "192.0.2.1", 2: "192.0.2.2"} {
		h := tbl.GetOrCreate(ttl)
		for r := 0; r < 41; r++ {
			h.MarkRoundSentProbes(7)
			for f := 0; f < 7; f++ {
				h.AddSample(net.ParseIP(ip), time.Duration(ttl)*time.Millisecond, f, hop.KindPrimary)
			}
			h.MarkRoundEnd()
		}
	}
	// Round one: the destination answers at TTL 3 to 6.
	for ttl := 3; ttl <= 6; ttl++ {
		h := tbl.GetOrCreate(ttl)
		h.MarkRoundSentProbes(7)
		for f := 0; f < 7; f++ {
			h.AddSample(dst, 3*time.Millisecond, f, hop.KindPrimary)
		}
		h.MarkTarget(dst)
		h.MarkRoundEnd()
	}
	// Rounds 2 to 41: only TTL 3 is probed; 3 of 7 answered, at 80 ms, and
	// every echo answered, so the loss is settled as real once 20 are in.
	h := tbl.GetOrCreate(3)
	for r := 0; r < 40; r++ {
		h.MarkRoundSentProbes(7)
		for f := 0; f < 3; f++ {
			h.AddSample(dst, 80*time.Millisecond, f, hop.KindPrimary)
		}
		h.MarkRoundEnd()
	}
	return &TargetSession{target: "dst.example", targetIP: dst, table: tbl, maxTTLHit: 3}
}

func TestDestStats_IgnoresHopsBeyondTheDestination(t *testing.T) {
	s := staleTailSession()
	want, _ := s.table.Get(3).Loss()
	if want < 50 {
		t.Fatalf("fixture: destination hop loss = %.1f, want over 50", want)
	}
	loss, _, avg := destStats(s)
	if loss != want {
		t.Errorf("summary loss = %.1f, want the destination hop's %.1f (TTL 6 holds round one only: 0%%)", loss, want)
	}
	if avg < 50*time.Millisecond {
		t.Errorf("summary avg = %v, want the destination hop's (about 76 ms), not round one's 3 ms", avg)
	}
}

func TestSummaryHops_IsThePathLength(t *testing.T) {
	s := staleTailSession()
	if got := summaryHops(s); got != 3 {
		t.Errorf("summary HOPS = %d, want 3 (the destination's distance), not the highest TTL probed in round one", got)
	}
}

func TestWorstHop_IgnoresHopsBeyondTheDestination(t *testing.T) {
	s := staleTailSession()
	// The largest step up in latency is into the destination (2 ms to ~76 ms).
	if got := worstHop(s); got != "198.51.100.1" {
		t.Errorf("worst hop = %q, want 198.51.100.1", got)
	}
	// A stale tail must not win: make TTL 5's round-one sample very slow.
	s.table.GetOrCreate(5).AddSample(net.ParseIP("198.51.100.1"), 5*time.Second, 0, hop.KindPrimary)
	if got := worstHop(s); strings.TrimSpace(got) != "198.51.100.1" {
		t.Errorf("worst hop = %q after a slow stale sample at TTL 5, want 198.51.100.1", got)
	}
	// worstHop reports an address, so also check which TTL it came from.
	if ttl := worstHopTTL(s); ttl != 3 {
		t.Errorf("worst hop TTL = %d, want 3", ttl)
	}
}

func TestSummaryHops_BeforeTheDestinationIsKnown(t *testing.T) {
	tbl := hop.NewTable(30)
	tbl.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), time.Millisecond, 0, hop.KindPrimary)
	tbl.GetOrCreate(4).AddSample(net.ParseIP("192.0.2.4"), time.Millisecond, 0, hop.KindPrimary)
	s := &TargetSession{table: tbl}
	if got := summaryHops(s); got != 4 {
		t.Errorf("summary HOPS = %d, want 4: every hop seen counts until the destination answers", got)
	}
}
