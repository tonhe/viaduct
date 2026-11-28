package hop

import "testing"

// The snapshot carries the settled loss, so a caller that decides on it reads
// the hop once (viaduct#261). It is SettledLoss: the headline loss, with
// provisional loss counted as none.
func TestHop_LossSnapshotCarriesSettledLoss(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rounds int
		echoOK func(ip string) bool
		kind   LossKind
		want   float64
	}{
		// No node answers echo: the hop's loss is measured.
		{"measured", 40, func(string) bool { return false }, LossMeasured, 6.0 / 7.0 * 100},
		// One node answers every echo: the TTL-probe loss is rate limiting.
		{"rate-limited", 40, func(ip string) bool { return ip == silentNodeA }, LossRateLimited, 0},
		// Too few echoes for a verdict: the loss does not count yet.
		{"provisional", 10, func(ip string) bool { return ip == silentNodeA }, LossProvisional, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHop(6)
			for round := 0; round < tc.rounds; round++ {
				runECMPRound(h, round, tc.echoOK)
			}
			snap := h.LossSnapshot()
			if snap.Kind != tc.kind {
				t.Fatalf("fixture: kind = %v, want %v", snap.Kind, tc.kind)
			}
			if tc.kind == LossProvisional && snap.Loss == 0 {
				t.Fatal("fixture: the provisional hop has no loss to leave out")
			}
			if !approx(snap.SettledLoss, tc.want) {
				t.Errorf("snapshot SettledLoss = %v, want %v", snap.SettledLoss, tc.want)
			}
			if got := h.SettledLoss(); snap.SettledLoss != got {
				t.Errorf("snapshot SettledLoss = %v, SettledLoss() = %v", snap.SettledLoss, got)
			}
		})
	}

	if snap := NewHop(1).LossSnapshot(); snap.SettledLoss != 0 {
		t.Errorf("new hop: snapshot SettledLoss = %v, want 0", snap.SettledLoss)
	}
}
