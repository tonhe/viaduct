package hop

import (
	"net"
	"strings"
	"testing"
	"time"
)

// Once a hop has had a verdict it never returns to provisional (viaduct#149).
// When its echo totals fall below 20 samples it keeps the last verdict until
// the new totals reach 20.

const (
	latchNodeA = "192.0.2.31"
	latchNodeB = "192.0.2.32"
	latchDst   = "198.51.100.9"
)

// latchRound runs one round on h: seven TTL-limited probes, the ICMP one
// answered by ttlFrom ("" for none), then one echo to every known node,
// answered by the nodes named in echoFrom.
func latchRound(h *Hop, ttlFrom string, echoFrom ...string) {
	h.MarkRoundSentProbes(7)
	if ttlFrom != "" {
		h.AddSample(net.ParseIP(ttlFrom), time.Millisecond, 0, KindICMPTTL)
	}
	nodes := h.GetNodes()
	for _, n := range nodes {
		n.MarkEchoSent()
	}
	for _, n := range nodes {
		for _, ip := range echoFrom {
			if n.GetIP().String() == ip {
				h.AddSample(n.GetIP(), time.Millisecond, 0, KindEcho)
			}
		}
	}
	h.MarkRoundEnd()
}

// assertNotProvisional fails if the hop shows provisional loss in any form.
func assertNotProvisional(t *testing.T, h *Hop, when string) {
	t.Helper()
	loss, kind := h.Loss()
	if kind == LossProvisional {
		t.Errorf("%s: Loss kind is LossProvisional (%.2f); a hop that has had a verdict keeps one", when, loss)
	}
	if got := FormatLoss(loss, kind); strings.Contains(got, "?") {
		t.Errorf("%s: FormatLoss = %q, want no \"?\"", when, got)
	}
	if got := h.RateLimitState(); got == RateLimitUndetermined {
		t.Errorf("%s: RateLimitState is Undetermined", when)
	}
}

// A destination that ignored ping and then starts answering: no node of the
// hop had answered, so nothing had been left out and nothing leaves the
// totals. The hop keeps its measured figure, the one alerting reads.
func TestHopLoss_HopThatStartsAnsweringEchoKeepsItsFigure(t *testing.T) {
	const want = 6.0 / 7.0 * 100
	h := NewHop(9)
	for round := 0; round < 30; round++ {
		latchRound(h, latchDst)
	}
	if loss, kind := h.Loss(); kind != LossMeasured || !approx(loss, want) {
		t.Fatalf("fixture: Loss = %.2f, %v; want %.2f, LossMeasured", loss, kind, want)
	}

	latchRound(h, latchDst, latchDst) // the first echo answer
	if sent, received := h.GetNodes()[0].echoTotals(); sent != 31 || received != 1 {
		t.Errorf("echo totals after the first answer = %d/%d, want 1/31: nothing was left out, so nothing leaves", received, sent)
	}
	if loss, kind := h.Loss(); kind != LossMeasured || !approx(loss, want) {
		t.Errorf("after the first answer Loss = %.2f, %v; want %.2f, LossMeasured", loss, kind, want)
	}
	if got := h.SettledLoss(); !approx(got, want) {
		t.Errorf("after the first answer SettledLoss = %.2f, want %.2f", got, want)
	}
	for round := 31; round < 90; round++ {
		latchRound(h, latchDst, latchDst)
		assertNotProvisional(t, h, "while the destination answers")
		if got := h.SettledLoss(); got == 0 {
			t.Fatalf("round %d: SettledLoss = 0; the hop's loss must not drop out of alerting", round)
		}
	}
}

// A hop whose only node ignores ping has a verdict (measured). A second node
// then appears and answers echo: the silent node is left out, and the totals
// fall to the new node's first echoes. The hop keeps its verdict, with the
// TTL-probe loss as it stands each round, until the new totals reach 20.
func TestHopLoss_NeverProvisionalAfterAVerdict(t *testing.T) {
	h := NewHop(6)
	for round := 0; round < 25; round++ {
		latchRound(h, latchNodeB)
	}
	if loss, kind := h.Loss(); kind != LossMeasured || !approx(loss, 6.0/7.0*100) {
		t.Fatalf("fixture: Loss = %.2f, %v; want 85.71, LossMeasured", loss, kind)
	}
	// A answers its echoes from round 25 on. Its 20th is in round 44.
	for round := 25; round < 44; round++ {
		ttlFrom := latchNodeA
		if round >= 26 && round <= 30 {
			ttlFrom = "" // TTL-probe loss rises while the verdict is held
		}
		latchRound(h, ttlFrom, latchNodeA)
		assertNotProvisional(t, h, "while the new totals are below 20")
		loss, kind := h.Loss()
		if kind != LossMeasured || !approx(loss, h.LossPercent()) {
			t.Errorf("round %d: Loss = %.2f, %v; want the TTL-probe loss %.2f, LossMeasured",
				round, loss, kind, h.LossPercent())
		}
		if got := h.RateLimitState(); got != RateLimitNo {
			t.Errorf("round %d: RateLimitState = %v, want the held No", round, got)
		}
	}
	if got := h.LossPercent(); approx(got, 6.0/7.0*100) {
		t.Fatalf("fixture: TTL-probe loss still %.2f; it should have moved while the verdict was held", got)
	}
	latchRound(h, latchNodeA, latchNodeA)
	if loss, kind := h.Loss(); kind != LossRateLimited || loss != 0 {
		t.Errorf("with 20 new echo samples Loss = %.2f, %v; want 0, LossRateLimited: the new verdict takes over", loss, kind)
	}
}

// A rate-limited hop whose totals fall below 20 keeps "~N%†" with the echo
// loss it showed when they fell, until there are 20 new samples.
func TestHopLoss_RateLimitedHopKeepsItsFigureBelow20Samples(t *testing.T) {
	h := NewHop(6)
	// B answers one TTL probe in round 0 and never an echo. A answers one TTL
	// probe in round 12 and every echo from then on.
	run := func(round int) {
		switch round {
		case 0:
			latchRound(h, latchNodeB, latchNodeA)
		case 12:
			latchRound(h, latchNodeA, latchNodeA)
		default:
			latchRound(h, "", latchNodeA)
		}
	}
	for round := 0; round <= 18; round++ {
		run(round)
	}
	// B: 19 echoes, none answered, still counted. A: 7 of 7.
	const held = 19.0 / 26.0 * 100
	if loss, kind := h.Loss(); kind != LossRateLimited || !approx(loss, held) {
		t.Fatalf("fixture: Loss = %.2f, %v; want %.2f, LossRateLimited", loss, kind, held)
	}
	// Round 19: B has ignored 20 echoes and is left out. A has 8 samples.
	for round := 19; round <= 30; round++ {
		run(round)
		assertNotProvisional(t, h, "while the new totals are below 20")
		loss, kind := h.Loss()
		if kind != LossRateLimited || !approx(loss, held) {
			t.Errorf("round %d: Loss = %.2f, %v; want the held %.2f, LossRateLimited", round, loss, kind, held)
		}
		if got := FormatLoss(loss, kind); got != "~73%†" {
			t.Errorf("round %d: FormatLoss = %q, want %q", round, got, "~73%†")
		}
		if got := h.RateLimitState(); got != RateLimitYes {
			t.Errorf("round %d: RateLimitState = %v, want the held Yes", round, got)
		}
	}
	run(31) // A's 20th echo
	loss, kind := h.Loss()
	if kind != LossRateLimited || loss != 0 {
		t.Errorf("with 20 new echo samples Loss = %.2f, %v; want 0, LossRateLimited", loss, kind)
	}
	if got := FormatLoss(loss, kind); got != "~0%†" {
		t.Errorf("FormatLoss = %q, want %q", got, "~0%†")
	}
}

// The verdict is held from the moment the hop has one, also when it is lost
// again before the round closes: the silent node's 20th echo gives the hop 20
// samples, and a new node's first answer in the same round takes them away.
func TestHopLoss_VerdictReachedAndLostInOneRoundIsKept(t *testing.T) {
	h := NewHop(6)
	for round := 0; round < 19; round++ {
		latchRound(h, latchNodeB)
	}
	if _, kind := h.Loss(); kind != LossProvisional {
		t.Fatalf("fixture: kind = %v, want LossProvisional with 19 echo samples", kind)
	}
	latchRound(h, latchNodeA, latchNodeA)
	assertNotProvisional(t, h, "after the round")
	if loss, kind := h.Loss(); kind != LossMeasured || !approx(loss, 6.0/7.0*100) {
		t.Errorf("Loss = %.2f, %v; want 85.71, LossMeasured", loss, kind)
	}
}

func TestHop_ResetClearsTheHeldVerdict(t *testing.T) {
	h := NewHop(6)
	for round := 0; round < 25; round++ {
		latchRound(h, latchNodeB)
	}
	latchRound(h, latchNodeA, latchNodeA)
	if _, kind := h.Loss(); kind != LossMeasured {
		t.Fatalf("fixture: kind = %v, want LossMeasured (held)", kind)
	}
	h.Reset()
	for round := 0; round < 5; round++ {
		latchRound(h, latchNodeA, latchNodeA)
	}
	if got := h.RateLimitState(); got != RateLimitUndetermined {
		t.Errorf("after Reset RateLimitState = %v, want Undetermined", got)
	}
	if loss, kind := h.Loss(); kind != LossProvisional || !approx(loss, 6.0/7.0*100) {
		t.Errorf("after Reset Loss = %.2f, %v; want 85.71, LossProvisional: the hop starts over", loss, kind)
	}
}
