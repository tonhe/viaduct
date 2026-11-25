package hop

import (
	"net"
	"testing"
	"time"
)

// A node that ignores ping says nothing about the path: its unanswered echoes
// are its own filter, not loss. At an ECMP hop they used to be summed with the
// other nodes' echoes, so the hop's headline was the share of nodes that
// ignore ping (viaduct#124: two nodes, one silent, raw 85.7%, headline ~50%†).

const (
	silentNodeA = "192.0.2.21"
	silentNodeB = "192.0.2.22"
)

// runECMPRound runs one round on a two-node hop: seven TTL-limited probes of
// which only the ICMP one is answered, by the two nodes in turn, then one echo
// to every known node, answered when echoOK returns true for its address.
func runECMPRound(h *Hop, round int, echoOK func(ip string) bool) {
	answering := silentNodeA
	if round%2 == 1 {
		answering = silentNodeB
	}
	h.MarkRoundSentProbes(7)
	h.AddSample(net.ParseIP(answering), time.Millisecond, 0, KindICMPTTL)
	for _, n := range h.GetNodes() {
		n.MarkEchoSent()
		if echoOK(n.GetIP().String()) {
			h.AddSample(n.GetIP(), time.Millisecond, 0, KindEcho)
		}
	}
	h.MarkRoundEnd()
}

func TestHopLoss_ECMPNodeThatIgnoresEchoIsLeftOut(t *testing.T) {
	h := NewHop(6)
	for round := 0; round < 40; round++ {
		runECMPRound(h, round, func(ip string) bool { return ip == silentNodeA })
	}
	if got := h.LossPercent(); !approx(got, 6.0/7.0*100) {
		t.Fatalf("fixture: TTL probe loss = %.2f, want 85.71", got)
	}
	if got := h.RateLimitState(); got != RateLimitYes {
		t.Errorf("RateLimitState = %v, want Yes: the node that answers echo answers all of them", got)
	}
	loss, kind := h.Loss()
	if kind != LossRateLimited || loss != 0 {
		t.Errorf("Loss = %.2f, %v; want 0, LossRateLimited: the silent node's echoes are not samples of the path", loss, kind)
	}
	if got := FormatLoss(loss, kind); got != "~0%†" {
		t.Errorf("FormatLoss = %q, want %q", got, "~0%†")
	}
	if loss, limited := h.HeadlineLoss(); !limited || loss != 0 {
		t.Errorf("HeadlineLoss = %.2f, %v; want 0, true", loss, limited)
	}
}

func TestHopLoss_NodeThatStartsAnsweringEchoIsCountedAgain(t *testing.T) {
	h := NewHop(6)
	// Node B appears in round 1 and ignores its first 25 echoes.
	for round := 0; round < 26; round++ {
		runECMPRound(h, round, func(ip string) bool { return ip == silentNodeA })
	}
	if loss, kind := h.Loss(); kind != LossRateLimited || loss != 0 {
		t.Fatalf("while B is silent Loss = %.2f, %v; want 0, LossRateLimited", loss, kind)
	}
	// From here B answers, except every fifth echo: 35 echoes, 7 lost.
	for i := 0; i < 35; i++ {
		lose := i%5 == 4
		runECMPRound(h, 26+i, func(ip string) bool { return ip == silentNodeA || !lose })
	}
	// A: 61 sent, 61 answered. B: 35 counted, 28 answered. The 25 echoes B
	// ignored before its first answer stay out.
	want := 7.0 / 96.0 * 100
	loss, kind := h.Loss()
	if kind != LossRateLimited || !approx(loss, want) {
		t.Errorf("Loss = %.2f, %v; want %.2f, LossRateLimited: B's echoes count from its first answer on", loss, kind, want)
	}
}

// A silent node is left out only when another node of its hop answers echo.
// A hop where no node answers echo keeps every echo in its totals: its loss
// is measured, as it was before viaduct#124, and it can alert. The commonest
// such hop is a destination that ignores ping.
func TestHopLoss_HopWhereNoNodeAnswersEchoIsMeasured(t *testing.T) {
	const want = 6.0 / 7.0 * 100
	for _, tc := range []struct {
		name  string
		round func(h *Hop, round int)
		nodes int
	}{
		{"single node", func(h *Hop, _ int) {
			runRound(h, 7, []hopReply{{"198.51.100.9", KindICMPTTL}}, false)
		}, 1},
		{"two nodes", func(h *Hop, round int) {
			runECMPRound(h, round, func(string) bool { return false })
		}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHop(6)
			for round := 0; round < 40; round++ {
				tc.round(h, round)
			}
			if got := len(h.GetNodes()); got != tc.nodes {
				t.Fatalf("fixture: %d nodes, want %d", got, tc.nodes)
			}
			for _, n := range h.GetNodes() {
				if s := n.EchoStats(); s.Sent < echoSilentAfter || s.Received != 0 {
					t.Fatalf("fixture: node %s echo %d/%d, want at least %d sent and none answered",
						n.GetIP(), s.Received, s.Sent, echoSilentAfter)
				}
			}
			if got := h.RateLimitState(); got != RateLimitNo {
				t.Errorf("RateLimitState = %v, want No: every echo is lost too", got)
			}
			loss, kind := h.Loss()
			if kind != LossMeasured || !approx(loss, want) {
				t.Errorf("Loss = %.2f, %v; want %.2f, LossMeasured", loss, kind, want)
			}
			if got := FormatLoss(loss, kind); got != "85.7%" {
				t.Errorf("FormatLoss = %q, want %q", got, "85.7%")
			}
			// SettledLoss is the figure alerting reads.
			if got := h.SettledLoss(); !approx(got, want) {
				t.Errorf("SettledLoss = %.2f, want %.2f: measured loss alerts", got, want)
			}
		})
	}
}

// A hop where no node answered echo, and one then starts to (viaduct#149):
// nothing had been left out of the totals, so the node that starts answering
// keeps every echo it ignored. The hop still has its echo samples and its
// verdict; it does not start over.
func TestHopLoss_FirstEchoAnswerOnASilentHopKeepsTheVerdict(t *testing.T) {
	h := NewHop(6)
	for round := 0; round < 30; round++ {
		runECMPRound(h, round, func(string) bool { return false })
	}
	if _, kind := h.Loss(); kind != LossMeasured {
		t.Fatalf("fixture: kind = %v, want LossMeasured while no node answers echo", kind)
	}
	answersA := func(ip string) bool { return ip == silentNodeA }
	runECMPRound(h, 30, answersA)
	if sent, received := h.GetNodes()[0].echoTotals(); sent != 31 || received != 1 {
		t.Errorf("A's echo totals after its first answer = %d/%d, want 1/31: its ignored echoes stay in", received, sent)
	}
	if loss, kind := h.Loss(); kind != LossMeasured || !approx(loss, 6.0/7.0*100) {
		t.Errorf("after A's first answer Loss = %.2f, %v; want 85.71, LossMeasured", loss, kind)
	}
	for round := 31; round < 50; round++ {
		runECMPRound(h, round, answersA)
		if _, kind := h.Loss(); kind == LossProvisional {
			t.Fatalf("round %d: LossProvisional; the hop has had a verdict", round)
		}
	}
	// A: 50 echoes, the first 30 ignored. B, which still ignores ping, is out
	// now that another node answers.
	if loss, kind := h.Loss(); kind != LossRateLimited || !approx(loss, 60) {
		t.Errorf("after 20 answered echoes Loss = %.2f, %v; want 60, LossRateLimited", loss, kind)
	}
}

// When the only node that answered echo goes silent, it stays in the totals
// with every echo it now loses, and the node that never answered stays out.
// The hop's echo loss climbs until it no longer differs from its TTL-probe
// loss; the verdict is then No and the loss is the measured TTL-probe figure.
func TestHopLoss_AnsweringNodeThatGoesSilentKeepsCounting(t *testing.T) {
	h := NewHop(6)
	for round := 0; round < 40; round++ {
		runECMPRound(h, round, func(ip string) bool { return ip == silentNodeA })
	}
	if loss, kind := h.Loss(); kind != LossRateLimited || loss != 0 {
		t.Fatalf("fixture: Loss = %.2f, %v; want 0, LossRateLimited", loss, kind)
	}
	// A stops answering. Ten rounds on its echo loss is 10 of 50.
	for round := 40; round < 50; round++ {
		runECMPRound(h, round, func(string) bool { return false })
	}
	if loss, kind := h.Loss(); kind != LossRateLimited || !approx(loss, 20) {
		t.Errorf("10 rounds after A went silent Loss = %.2f, %v; want 20, LossRateLimited (A's echoes only)", loss, kind)
	}
	// 800 rounds on it is 800 of 840, as high as the TTL-probe loss.
	for round := 50; round < 840; round++ {
		runECMPRound(h, round, func(string) bool { return false })
	}
	if got := h.RateLimitState(); got != RateLimitNo {
		t.Errorf("RateLimitState = %v, want No: echoes are now lost as often as TTL probes", got)
	}
	if loss, kind := h.Loss(); kind != LossMeasured || !approx(loss, 6.0/7.0*100) {
		t.Errorf("Loss = %.2f, %v; want 85.71, LossMeasured", loss, kind)
	}
}

// A node is left out only once it has ignored 20 echoes. One that answers
// within its first 20 is counted in full, lost echoes included. One that
// answers later counts from that answer on if it was being left out (leftOut:
// another node of its hop answers echo, so the hop calls joinEchoTotals), and
// in full if it was not (viaduct#149).
func TestPathNode_EchoTotals(t *testing.T) {
	for _, tc := range []struct {
		name         string
		silent       int // echoes ignored before the first answer
		answered     int // echoes answered after that
		leftOut      bool
		wantSent     int
		wantReceived int
	}{
		{"never echoed", 0, 0, true, 0, 0},
		{"answers every echo", 0, 30, true, 30, 30},
		{"silent below the limit", 19, 0, true, 19, 0},
		{"silent at the limit", 20, 0, true, 0, 0},
		{"silent past the limit", 50, 0, true, 0, 0},
		{"answers its 20th echo", 19, 5, true, 24, 5},
		{"left out, answers its 21st echo", 20, 5, true, 5, 5},
		{"left out, answers after a long silence", 50, 3, true, 3, 3},
		{"not left out, answers its 21st echo", 20, 5, false, 25, 5},
		{"not left out, answers after a long silence", 50, 3, false, 53, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pn := NewPathNode(net.ParseIP("2001:db8::1"))
			for i := 0; i < tc.silent; i++ {
				pn.MarkEchoSent()
			}
			for i := 0; i < tc.answered; i++ {
				pn.MarkEchoSent()
				if tc.leftOut {
					pn.joinEchoTotals()
				}
				pn.AddSample(time.Millisecond, 0, KindEcho)
			}
			sent, received := pn.echoTotals()
			if sent != tc.wantSent || received != tc.wantReceived {
				t.Errorf("echoTotals = %d, %d; want %d, %d", sent, received, tc.wantSent, tc.wantReceived)
			}
			// The raw counters are not changed by the rule.
			if s := pn.EchoStats(); s.Sent != tc.silent+tc.answered || s.Received != tc.answered {
				t.Errorf("EchoStats = %d sent, %d received; want %d, %d", s.Sent, s.Received, tc.silent+tc.answered, tc.answered)
			}
		})
	}
}

// Answers that arrive after later echoes went out prove the earlier echoes
// were not ignored: an answer is always counted together with its echo, so
// the totals never hold more answers than echoes.
func TestPathNode_EchoTotalsCountLateAnswers(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.24"))
	for i := 0; i < 40; i++ {
		pn.MarkEchoSent()
	}
	for i := 0; i < 40; i++ {
		pn.joinEchoTotals() // left out: another node of the hop answers echo
		pn.AddSample(time.Millisecond, 0, KindEcho)
	}
	if sent, received := pn.echoTotals(); sent != 40 || received != 40 {
		t.Errorf("echoTotals = %d, %d; want 40, 40", sent, received)
	}
}

func TestPathNode_ResetForgetsEchoSilence(t *testing.T) {
	pn := NewPathNode(net.ParseIP("192.0.2.23"))
	for i := 0; i < 30; i++ {
		pn.MarkEchoSent()
	}
	pn.MarkEchoSent()
	pn.joinEchoTotals()
	pn.AddSample(time.Millisecond, 0, KindEcho)
	pn.Reset()
	for i := 0; i < 4; i++ {
		pn.MarkEchoSent()
		pn.AddSample(time.Millisecond, 0, KindEcho)
	}
	if sent, received := pn.echoTotals(); sent != 4 || received != 4 {
		t.Errorf("after Reset echoTotals = %d, %d; want 4, 4", sent, received)
	}
}
