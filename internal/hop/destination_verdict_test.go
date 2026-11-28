package hop

import (
	"net"
	"testing"
	"time"
)

// The destination's hop is judged on the target alone (viaduct#244): only
// the echoes sent to the target, and only the TTL-probe types the target has
// answered at that TTL. In UDP mode the ICMP TTL probe takes a path one hop
// longer than the UDP flows, so at the destination's TTL a router answers it,
// and every echo sent to that router. Mixed in, the router's answers dilute
// the target's loss.

// destRound runs one UDP-mode round on tbl at ttl: one ICMP TTL probe,
// answered by router when router is not nil, and two UDP probes to the
// target, of which the flows in targetFlows are answered. Then one echo to
// every node of the hop: the router's is answered, the target's when
// targetEcho is true. The round is closed on the hop and on the table.
func destRound(tbl *Table, ttl int, router net.IP, targetFlows []int, targetEcho bool) {
	h := tbl.GetOrCreate(ttl)
	tbl.MarkRoundSentByType(ttl, 1, 2)
	if router != nil {
		h.AddSample(router, time.Millisecond, 0, KindICMPTTL)
		tbl.NoteReply(ttl, router, 0, KindICMPTTL, false)
	}
	for _, f := range targetFlows {
		h.AddSample(destTarget, time.Millisecond, f, KindPrimary)
		tbl.NoteReply(ttl, destTarget, f, KindPrimary, true)
	}
	for _, n := range h.GetNodes() {
		n.MarkEchoSent()
		if !n.IsTarget() || targetEcho {
			h.AddSample(n.GetIP(), time.Millisecond, 0, KindEcho)
		}
	}
	h.MarkRoundEnd()
	tbl.EndRound()
}

// halfLossRounds runs rounds rounds in which the target loses half of its
// replies, UDP and echo alike, and the router at the same TTL loses none.
func halfLossRounds(tbl *Table, ttl, rounds int) {
	for r := 0; r < rounds; r++ {
		destRound(tbl, ttl, destRouter, []int{r % 2}, r%2 == 0)
	}
}

func TestDestinationVerdict_RouterAtTheSameTTLDoesNotDilute(t *testing.T) {
	tbl := NewTable(30)
	halfLossRounds(tbl, 5, 40)
	if got := tbl.TargetTTL(); got != 5 {
		t.Fatalf("fixture: TargetTTL = %d, want 5", got)
	}
	h := tbl.Get(5)
	if len(h.GetNodes()) != 2 {
		t.Fatalf("fixture: %d nodes at the destination, want the target and the router", len(h.GetNodes()))
	}

	pct, kind := h.Loss()
	if kind != LossMeasured || pct != 50 {
		t.Errorf("Loss = %.1f %v, want 50.0 measured: the target lost half", pct, kind)
	}
	if got := h.RateLimitState(); got != RateLimitNo {
		t.Errorf("RateLimitState = %v, want No", got)
	}
	// The raw per-probe loss is the hop's, every probe type.
	if got := h.LossPercent(); got < 33.3 || got > 33.4 {
		t.Errorf("LossPercent = %.2f, want 33.3 (40 of 120 probes)", got)
	}

	// What the verdict was made from, as the export reads it.
	if s, r := h.VerdictEchoTotals(); s != 40 || r != 20 {
		t.Errorf("VerdictEchoTotals = %d/%d, want 40/20: the target's echoes", s, r)
	}
	if s, r := h.VerdictProbeCounts(); s != 80 || r != 40 {
		t.Errorf("VerdictProbeCounts = %d/%d, want 80/40: the UDP probes", s, r)
	}
	if _, _, cs, cr := h.EchoCounts(); cs != 40 || cr != 20 {
		t.Errorf("EchoCounts counted = %d/%d, want 40/20", cs, cr)
	}
	snap := h.LossSnapshot()
	if snap.Loss != 50 || snap.Kind != LossMeasured || snap.SettledLoss != 50 ||
		snap.EchoSent != 40 || snap.EchoReceived != 20 ||
		snap.VerdictProbesSent != 80 || snap.VerdictProbesReceived != 40 ||
		snap.ProbesSent != 120 || snap.ProbesReceived != 80 {
		t.Errorf("LossSnapshot = %+v, disagrees with the hop's methods", snap)
	}
	if h.EchoIgnored(destRouter) || h.EchoIgnored(destTarget) || len(snap.EchoIgnoredIPs) != 0 {
		t.Error("a node is marked as left out of the destination's echo totals")
	}
}

// The same two nodes at a hop that is not the destination's are judged as
// before. Flows of unequal path length: at TTL 8 a router answers flow 0 and
// the target flow 1, losing half; at TTL 9 the target answers both. The
// destination is TTL 9, and TTL 8 is an ordinary hop that lists the target.
func TestDestinationVerdict_OtherHopsAreJudgedAsBefore(t *testing.T) {
	tbl := NewTable(30)
	for r := 0; r < 40; r++ {
		h8, h9 := tbl.GetOrCreate(8), tbl.GetOrCreate(9)
		tbl.MarkRoundSentByType(8, 0, 2)
		tbl.MarkRoundSentByType(9, 0, 2)
		h8.AddSample(destRouter, time.Millisecond, 0, KindPrimary)
		tbl.NoteReply(8, destRouter, 0, KindPrimary, false)
		if r%2 == 0 {
			h8.AddSample(destTarget, time.Millisecond, 1, KindPrimary)
			tbl.NoteReply(8, destTarget, 1, KindPrimary, true)
		}
		for f := 0; f < 2; f++ {
			h9.AddSample(destTarget, time.Millisecond, f, KindPrimary)
			tbl.NoteReply(9, destTarget, f, KindPrimary, true)
		}
		for _, n := range h8.GetNodes() {
			n.MarkEchoSent()
			if !n.IsTarget() || r%2 == 0 {
				h8.AddSample(n.GetIP(), time.Millisecond, 0, KindEcho)
			}
		}
		h8.MarkRoundEnd()
		h9.MarkRoundEnd()
		tbl.EndRound()
	}
	if got := tbl.TargetTTL(); got != 9 {
		t.Fatalf("fixture: TargetTTL = %d, want 9", got)
	}
	h := tbl.Get(8)
	if n := len(h.GetNodes()); n != 2 {
		t.Fatalf("fixture: %d nodes at TTL 8, want 2", n)
	}
	// Every probe (80 sent, 60 answered) against every echo (80, 60).
	pct, kind := h.Loss()
	if kind != LossMeasured || pct != 25 {
		t.Errorf("Loss = %.1f %v, want 25.0 measured, the hop's", pct, kind)
	}
	if s, r := h.VerdictEchoTotals(); s != 80 || r != 60 {
		t.Errorf("VerdictEchoTotals = %d/%d, want 80/60: every node's echoes", s, r)
	}
	if s, r := h.VerdictProbeCounts(); s != 80 || r != 60 {
		t.Errorf("VerdictProbeCounts = %d/%d, want 80/60: every probe", s, r)
	}
}

// The verdict counts only the probe types the target answers there: in UDP
// mode the ICMP TTL probes the router answers are not the target's to lose,
// and once the target answers one, they are.
func TestDestinationVerdict_ProbeTypesTheTargetAnswers(t *testing.T) {
	tbl := NewTable(30)
	halfLossRounds(tbl, 5, 2)
	h := tbl.Get(5)
	if s, r := h.VerdictProbeCounts(); s != 4 || r != 2 {
		t.Fatalf("VerdictProbeCounts = %d/%d, want 4/2: UDP only", s, r)
	}
	// The target answers an ICMP TTL probe at this TTL: that type counts too.
	tbl.MarkRoundSentByType(5, 1, 2)
	h.AddSample(destTarget, time.Millisecond, 0, KindICMPTTL)
	tbl.NoteReply(5, destTarget, 0, KindICMPTTL, true)
	h.MarkRoundEnd()
	tbl.EndRound()
	if s, r := h.VerdictProbeCounts(); s != 9 || r != 5 {
		t.Errorf("VerdictProbeCounts = %d/%d, want 9/5: both types", s, r)
	}
}

// A target that has answered only echoes at the destination's TTL has no
// probe type of its own there: every type counts.
func TestDestinationVerdict_TargetAnsweredOnlyEchoes(t *testing.T) {
	h := NewHop(5)
	h.destination = true
	h.MarkRoundSentByType(1, 2)
	h.AddSample(destRouter, time.Millisecond, 0, KindICMPTTL)
	h.AddSample(destTarget, time.Millisecond, 0, KindEcho)
	h.MarkTarget(destTarget)
	h.MarkRoundEnd()
	if s, r := h.VerdictProbeCounts(); s != 3 || r != 1 {
		t.Errorf("VerdictProbeCounts = %d/%d, want 3/1: every type", s, r)
	}
	if s, r := h.VerdictEchoTotals(); s != 0 || r != 1 {
		t.Errorf("VerdictEchoTotals = %d/%d, want the target's 0/1", s, r)
	}
}

// A destination's hop with no node marked as the target (after a reset, say)
// is judged as a hop of the path until the target answers again.
func TestDestinationVerdict_NoTargetNodeFallsBackToTheHop(t *testing.T) {
	h := NewHop(5)
	h.destination = true
	h.MarkRoundSentByType(1, 2)
	h.AddSample(destRouter, time.Millisecond, 0, KindICMPTTL)
	h.MarkRoundEnd()
	if s, r := h.VerdictProbeCounts(); s != 3 || r != 1 {
		t.Errorf("VerdictProbeCounts = %d/%d, want 3/1", s, r)
	}
}

// The hop's verdict is kept for both ways it can be judged, so a hop that
// stops being the destination's holds its own verdict as a hop of the path,
// not the target's.
func TestDestinationVerdict_HeldPerBasis(t *testing.T) {
	tbl := NewTable(30)
	halfLossRounds(tbl, 5, 40)
	h := tbl.Get(5)
	h.mu.Lock()
	target, path := h.heldTarget, h.held
	h.mu.Unlock()
	if target.verdict != RateLimitNo || target.echoSent != 40 || target.probesSent != 80 {
		t.Errorf("held target verdict = %+v, want No from 80 probes and 40 echoes", target)
	}
	if path.verdict != RateLimitNo || path.echoSent != 80 || path.probesSent != 120 {
		t.Errorf("held path verdict = %+v, want No from 120 probes and 80 echoes", path)
	}

	h.Reset()
	if h.heldTarget.verdict != RateLimitUndetermined || h.held.verdict != RateLimitUndetermined {
		t.Error("Reset kept a held verdict")
	}
}

// The destination moves: the old hop is judged as a hop of the path again,
// the new one on the target.
func TestDestinationVerdict_FollowsTheDestination(t *testing.T) {
	tbl := NewTable(30)
	halfLossRounds(tbl, 5, 1)
	if !tbl.Get(5).destination {
		t.Fatal("TTL 5 is not marked as the destination's hop")
	}
	tbl.Get(5).setDestination(false)
	if s, _ := tbl.Get(5).VerdictProbeCounts(); s != 3 {
		t.Errorf("VerdictProbeCounts sent = %d, want 3 once TTL 5 is not the destination", s)
	}
	tbl.EndRound()
	if !tbl.Get(5).destination {
		t.Error("EndRound did not mark the destination's hop again")
	}
}
