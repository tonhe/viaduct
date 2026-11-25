package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// TestRenderHopRateLimitedCell exercises the loss-cell branch that used to
// consult the pingStats supplement. Task 12 rewired it to read the primary
// node's tri-state RateLimitState() / EchoStats().
//
//   - RateLimitYes: cell shows "~N%†" where N is echoLoss.ProbeLossPercent(),
//     padded (with the dagger counted) to width 8.
//   - RateLimitUndetermined (no echo samples): plain "N.N%" with no tilde and
//     no dagger, padded to width 8. Regression: an earlier bug read echo loss
//     even when the guard rejected the sample, producing a spurious "~0%†".
func TestRenderHopRateLimitedCell(t *testing.T) {
	t.Run("RateLimitYes shows ~0%† padded to 8", func(t *testing.T) {
		// hopsFull() + withRateLimitedHop(3) seeds TTL 3 with 9 icmpTTL sends,
		// 1 reply (88.9% round loss) and 5 echo sends / 5 echo replies
		// (0% echo loss) → RateLimitYes. The dagger asserts the branch fired.
		m := newTestModel(withSize(120, 24), hopsFull(), withRateLimitedHop(3))
		plain := stripANSI(m.View())
		if !strings.Contains(plain, "~0%†") {
			t.Fatalf("expected rate-limited loss cell ~0%%† for TTL 3; view:\n%s", plain)
		}

		// Verify the dagger sits inside the loss cell (no accidental leak into
		// the Rnd column beside it): the token "~0%†" is immediately followed
		// by trailing padding spaces before the Rnd count.
		idx := strings.Index(plain, "~0%†")
		if idx < 0 {
			t.Fatalf("dagger token missing; view:\n%s", plain)
		}
		// Byte width of "~0%†" is 6 (dagger is 3 bytes UTF-8) but display
		// width is 4 columns; padCell must pad using display width, then style.
		// After "~0%†" we should see spaces before the "9" Sent counter.
		tail := plain[idx+len("~0%†"):]
		if !strings.HasPrefix(strings.TrimLeft(tail, " "), "9") {
			t.Errorf("expected dagger cell to be followed by padding and Sent=9; tail: %q", tail[:20])
		}
	})

	t.Run("RateLimitUndetermined shows plain loss, no tilde, no dagger", func(t *testing.T) {
		// hopsFull() alone: TTL 3 has no rounds sent, so LossPercent() == 0
		// and RateLimitState() == RateLimitUndetermined. Expect "0.0%".
		m := newTestModel(withSize(120, 24), hopsFull())
		plain := stripANSI(m.View())
		// TTL 3 line begins with "3 " (dim TTL) then IP. Find it.
		var ttl3 string
		for _, line := range strings.Split(plain, "\n") {
			trimmed := strings.TrimLeft(line, " ")
			if strings.HasPrefix(trimmed, "3 ") || strings.HasPrefix(trimmed, "3\t") {
				ttl3 = line
				break
			}
			if strings.HasPrefix(trimmed, "3  ") {
				ttl3 = line
				break
			}
		}
		if ttl3 == "" {
			// Fall back: any line containing 203.0.113.10 (TTL 3's IP).
			for _, line := range strings.Split(plain, "\n") {
				if strings.Contains(line, "203.0.113.10") {
					ttl3 = line
					break
				}
			}
		}
		if ttl3 == "" {
			t.Fatalf("could not locate TTL 3 hop row in view:\n%s", plain)
		}
		if strings.Contains(ttl3, "†") {
			t.Errorf("undetermined-state row must not carry the dagger; row: %q", ttl3)
		}
		if strings.Contains(ttl3, "~") {
			t.Errorf("undetermined-state row must not carry the tilde; row: %q", ttl3)
		}
		if !strings.Contains(ttl3, "0.0%") {
			t.Errorf("expected plain 0.0%% loss cell for TTL 3; row: %q", ttl3)
		}
	})

	// Sanity: padCell pads by display width (lipgloss.Width), so a cell whose
	// content contains a multi-byte rune still reaches exactly 8 columns.
	t.Run("padCell counts display width, not bytes", func(t *testing.T) {
		got := padCell("~0%†", 8)
		// "~0%†" is 4 display columns; padCell should append 4 spaces.
		if want := "~0%†    "; got != want {
			t.Errorf("padCell(%q, 8) = %q, want %q", "~0%†", got, want)
		}
	})

	// Sanity: padCell leaves already-wide-enough content untouched.
	t.Run("padCell no-op when width met", func(t *testing.T) {
		if got := padCell("12345678", 8); got != "12345678" {
			t.Errorf("padCell(%q, 8) mutated width-perfect input: %q", "12345678", got)
		}
	})

	// Sanity: the render test above depends on hop.RateLimitYes classification
	// firing. Assert the fixture actually produced that state so a fixture
	// regression can't silently pass the render test.
	t.Run("fixture actually produces RateLimitYes on TTL 3", func(t *testing.T) {
		m := newTestModel(withSize(120, 24), hopsFull(), withRateLimitedHop(3))
		h := m.sessions[0].table.Get(3)
		if h == nil {
			t.Fatal("TTL 3 hop missing from fixture")
		}
		pn := h.PrimaryNode()
		if pn == nil {
			t.Fatal("TTL 3 has no primary node")
		}
		if got := pn.RateLimitState(); got != hop.RateLimitYes {
			t.Errorf("fixture RateLimitState = %v, want RateLimitYes (%v)", got, hop.RateLimitYes)
		}
		// Rendered elapsed field is derived from time.Since; irrelevant here.
		_ = time.Second
	})
}
