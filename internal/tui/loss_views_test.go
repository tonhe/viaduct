package tui

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// Every view shows the same loss figure for a hop, and alerting decides on it
// too (viaduct#71).

// lossFixtureHop runs rounds on a new hop: 7 TTL-limited probes a round, of
// which the ICMP probe and answeredUDP of the six UDP probes are answered by
// ip, and one echo a round, always answered.
func lossFixtureHop(ttl int, ip string, rounds, answeredUDP int) *hop.Hop {
	h := hop.NewHop(ttl)
	addr := net.ParseIP(ip)
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
	return h
}

func TestLossCell_ProvisionalIsMarked(t *testing.T) {
	h := lossFixtureHop(7, "192.0.2.7", 12, 0) // 85.7% TTL loss, 11 echoes
	if got := lossCell(h); !strings.Contains(got, "85.7%?") {
		t.Errorf("loss cell = %q, want it to contain 85.7%%? while the verdict is not possible", got)
	}
}

func TestLossCell_RateLimitedAndMeasured(t *testing.T) {
	limited := lossFixtureHop(7, "192.0.2.7", 45, 0)
	if got := lossCell(limited); !strings.Contains(got, "~0%†") {
		t.Errorf("rate-limited loss cell = %q, want ~0%%†", got)
	}
	clean := lossFixtureHop(2, "192.0.2.2", 45, 6)
	if got := lossCell(clean); !strings.Contains(got, "0.0%") || strings.Contains(got, "?") {
		t.Errorf("clean loss cell = %q, want 0.0%% with no marker", got)
	}
}

// A destination that throttles its Port Unreachable replies must not raise a
// loss alert: live, one showed 21% raw loss on a path with none.
func TestDestAlertMetrics_UsesTheSettledFigure(t *testing.T) {
	dst := lossFixtureHop(3, "198.51.100.1", 45, 4) // 28.6% raw, every echo answered
	hopMap := map[int]*hop.Hop{
		1: lossFixtureHop(1, "192.0.2.1", 45, 6),
		3: dst,
	}
	if raw := dst.LossPercent(); raw < 25 {
		t.Fatalf("fixture: raw destination loss = %.1f, want about 28.6", raw)
	}
	loss, latency := destAlertMetrics(hopMap, 3)
	if loss != 0 {
		t.Errorf("alert loss = %.1f, want 0: the destination answers every echo", loss)
	}
	if latency != 10*time.Millisecond {
		t.Errorf("alert latency = %v, want 10ms", latency)
	}
}

func TestDestAlertMetrics_ProvisionalLossDoesNotAlert(t *testing.T) {
	hopMap := map[int]*hop.Hop{2: lossFixtureHop(2, "198.51.100.1", 8, 4)}
	if loss, _ := destAlertMetrics(hopMap, 2); loss != 0 {
		t.Errorf("alert loss = %.1f after 8 rounds, want 0 until the figure is settled", loss)
	}
}

func TestDestStats_UsesTheHeadlineFigure(t *testing.T) {
	tbl := hop.NewTable(30)
	s := &TargetSession{table: tbl}
	addr := net.ParseIP("198.51.100.1")
	h := tbl.GetOrCreate(1)
	for r := 0; r < 45; r++ {
		h.MarkRoundSentProbes(7)
		h.AddSample(addr, 10*time.Millisecond, 0, hop.KindICMPTTL)
		for f := 0; f < 4; f++ {
			h.AddSample(addr, 10*time.Millisecond, f, hop.KindPrimary)
		}
		for _, n := range h.GetNodes() {
			n.MarkEchoSent()
			h.AddSample(n.GetIP(), 10*time.Millisecond, 0, hop.KindEcho)
		}
		h.MarkRoundEnd()
	}
	loss, kind, _ := destStats(s)
	if loss != 0 || kind != hop.LossRateLimited {
		t.Errorf("destStats = %.1f, %v; want 0, LossRateLimited", loss, kind)
	}
}
