package export

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// TestBuildReport_LiveExportIsOneSnapshot: a report built while rounds are
// closing (an export taken from the TUI) takes each hop's figures from one
// read. The probe counts used to be read apart from the verdict figures, so a
// round closing between the two reads left verdict_probes_sent one round
// ahead of probes_sent on a hop that holds no verdict (viaduct#227).
func TestBuildReport_LiveExportIsOneSnapshot(t *testing.T) {
	table := hop.NewTable(1)
	h := table.GetOrCreate(1)
	ip := net.ParseIP("192.0.2.9")
	targetIP := net.ParseIP("198.51.100.1")

	const rounds = 3000
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// No echo is sent, so the hop never has a verdict to hold and its
		// verdict probe counts are always its current ones.
		for i := 0; i < rounds; i++ {
			h.MarkRoundSentProbes(7)
			for j := 0; j < 4; j++ {
				h.AddSample(ip, time.Millisecond, j, hop.KindPrimary)
			}
			h.MarkRoundEnd()
		}
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	for builds := 0; ; builds++ {
		finished := false
		select {
		case <-done:
			finished = true
		default:
		}
		r := BuildReport("v0", targetIP.String(), targetIP, "udp", table, nil, nil, 1)
		got := r.Hops[0]
		if got.VerdictHeld {
			t.Fatalf("build %d: verdict held on a hop with no echoes", builds)
		}
		if got.VerdictProbesSent != got.ProbesSent || got.VerdictProbesReceived != got.ProbesReceived {
			t.Fatalf("build %d: verdict probes %d/%d, probes %d/%d; want equal",
				builds, got.VerdictProbesSent, got.VerdictProbesReceived, got.ProbesSent, got.ProbesReceived)
		}
		if r.Probes != got.ProbesSent || got.Sent != got.ProbesSent {
			t.Fatalf("build %d: report probes %d, hop sent %d, probes_sent %d; want equal",
				builds, r.Probes, got.Sent, got.ProbesSent)
		}
		var wantLoss float64
		if got.ProbesSent > 0 {
			wantLoss = float64(got.ProbesSent-got.ProbesReceived) / float64(got.ProbesSent) * 100
		}
		if got.Loss != wantLoss || got.HeadlineLoss != wantLoss {
			t.Fatalf("build %d: loss_pct %v, headline %v; want %v from %d/%d",
				builds, got.Loss, got.HeadlineLoss, wantLoss, got.ProbesSent, got.ProbesReceived)
		}
		for _, n := range got.Nodes {
			if n.Loss != got.Loss || n.Sent != got.ProbesSent {
				t.Fatalf("build %d: node loss, sent %v, %d; hop %v, %d",
					builds, n.Loss, n.Sent, got.Loss, got.ProbesSent)
			}
		}
		if finished {
			if got.ProbesSent != 7*rounds || got.Rounds != rounds {
				t.Fatalf("final: probes_sent, rounds = %d, %d; want %d, %d",
					got.ProbesSent, got.Rounds, 7*rounds, rounds)
			}
			return
		}
	}
}
