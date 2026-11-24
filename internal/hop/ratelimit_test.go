package hop

import (
	"net"
	"testing"
	"time"
)

// mkNode builds a node with the given icmpTTL round loss (rounds, recvRounds)
// and echo probe loss (sent, received).
func mkNode(ttlRounds, ttlRecv, echoSent, echoRecv int) *PathNode {
	pn := NewPathNode(net.ParseIP("198.51.100.9"))
	for i := 0; i < ttlRounds; i++ {
		if i < ttlRecv {
			pn.AddSample(time.Millisecond, 0, KindICMPTTL)
		}
		pn.MarkProbeRoundEnd()
	}
	for i := 0; i < echoSent; i++ {
		pn.MarkEchoSent()
	}
	for i := 0; i < echoRecv; i++ {
		pn.AddSample(time.Millisecond, 0, KindEcho)
	}
	return pn
}

func TestRateLimitStateUndeterminedBelowMinSamples(t *testing.T) {
	// Spec test case: 30% TTL loss, 4 echo samples at 40% echo loss.
	pn := mkNode(10, 7, 4, 2) // echo 50% -> still undetermined regardless
	if got := pn.RateLimitState(); got != RateLimitUndetermined {
		t.Errorf("state = %v, want Undetermined (echo Sent < 5)", got)
	}
}

func TestRateLimitStateLimitedAtGuard(t *testing.T) {
	// Fifth sample brings echo loss under TTL - 5pp: 30% TTL, 20% echo.
	pn := mkNode(10, 7, 5, 4)
	if got := pn.RateLimitState(); got != RateLimitYes {
		t.Errorf("state = %v, want Yes (echo 20%% < ttl 30%% - 5pp)", got)
	}
}

func TestRateLimitStateNotLimited(t *testing.T) {
	// Echo loss within 5pp of TTL loss: genuinely lossy hop.
	pn := mkNode(10, 7, 5, 4-1) // echo 40% vs ttl 30% -> No
	if got := pn.RateLimitState(); got != RateLimitNo {
		t.Errorf("state = %v, want No", got)
	}
}

func TestRateLimitStateZeroEcho(t *testing.T) {
	pn := mkNode(10, 5, 0, 0)
	if got := pn.RateLimitState(); got != RateLimitUndetermined {
		t.Errorf("state = %v, want Undetermined (no echo data)", got)
	}
}
