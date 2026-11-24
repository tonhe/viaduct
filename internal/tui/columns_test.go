package tui

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/tonhe/viaduct/internal/hop"
)

func TestColumnTargets_MarkedWinOverFit(t *testing.T) {
	m := multiTestModel(t, withSize(120, 40))
	m.sessions[1].marked = true
	got := m.columnTargets()
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("columnTargets = %v, want [1]", got)
	}
}

func TestColumnTargets_NarrowFitsOnePane(t *testing.T) {
	// width 50 fits one pane (50/minPaneWidth = 1); the focused target shows alone.
	m := multiTestModel(t, withSize(50, 40))
	got := m.columnTargets()
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("columnTargets = %v, want [0] at narrow width", got)
	}
}

// TestColumnTargets_FocusedAlwaysShown: when there are more targets than fit,
// the focused target is guaranteed a pane even if it falls past the first N.
func TestColumnTargets_FocusedAlwaysShown(t *testing.T) {
	m := newTestModel(withSize(minPaneWidth, 40)) // fit = 1
	cfg := m.sessions[0].probeCfg
	for i := 1; i < 6; i++ {
		m.sessions = append(m.sessions,
			newSession(fmt.Sprintf("t%d.example", i), net.ParseIP(fmt.Sprintf("192.0.2.%d", i)), cfg, "udp", 5.0, 0, 3, true))
	}
	m.focusedIdx = 4

	got := m.columnTargets()
	if len(got) != 1 || got[0] != 4 {
		t.Fatalf("columnTargets = %v, want [4] (focused guaranteed a pane)", got)
	}
}

func TestColumnTargets_ErroredExcluded(t *testing.T) {
	// wide enough to fit all 3 columns but session[2] is errored — should be excluded
	m := multiTestModel(t, withSize(200, 40))
	got := m.columnTargets()
	for _, idx := range got {
		if m.sessions[idx].startErr != nil {
			t.Errorf("columnTargets included errored session[%d]", idx)
		}
	}
	// Expect [0 1] since session[2] has startErr
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("columnTargets = %v, want [0 1]", got)
	}
}

func TestGolden_ColumnView(t *testing.T) {
	setTestTheme(t)
	m := multiTestModel(t,
		withSize(120, 40),
		withHops(
			hopSample(1, "192.0.2.254", 1300*time.Microsecond),
			hopSample(2, "203.0.113.1", 10200*time.Microsecond),
		),
	)
	// Give target 1 one hop of data using the Snapshot/GetOrCreate pattern
	h1c3 := m.sessions[1].table.GetOrCreate(1)
	h1c3.MarkRoundSent()
	h1c3.AddSample(net.ParseIP("192.0.2.254"), 1300*time.Microsecond, 0, hop.KindPrimary)
	h1c3.MarkRoundEnd()
	m.columnView = true
	goldenCompare(t, "testdata/view/columns_three_targets.golden", m.View())
}

func TestGolden_ColumnView_Marked(t *testing.T) {
	setTestTheme(t)
	m := multiTestModel(t,
		withSize(120, 40),
		withHops(hopSample(1, "192.0.2.254", 1300*time.Microsecond)),
	)
	h1cm := m.sessions[1].table.GetOrCreate(1)
	h1cm.MarkRoundSent()
	h1cm.AddSample(net.ParseIP("192.0.2.254"), 1300*time.Microsecond, 0, hop.KindPrimary)
	h1cm.MarkRoundEnd()
	m.sessions[1].marked = true
	m.columnView = true
	goldenCompare(t, "testdata/view/columns_marked.golden", m.View())
}

// TestColumnView_MissingHopKeepsAlignment verifies that a star (missing hop) in
// one pane does not shift the neighboring pane's borders. With bordered panes
// the invariant is display-width equality: every rendered line is exactly the
// terminal width, so a collapsed cell (the byte-padding bug class) would show up
// as an unequal line. Forced color makes the assertion meaningful.
func TestColumnView_MissingHopKeepsAlignment(t *testing.T) {
	setTestTheme(t)
	forceColor(t)

	m := newTestModel(withSize(120, 40))
	m.sessions[0].target = "one.example"
	cfg := m.sessions[0].probeCfg
	m.sessions = append(m.sessions,
		newSession("two.example", net.ParseIP("192.0.2.2"), cfg, "udp", 5.0, 0, 3, true))

	// Session 0: TTL 1 and TTL 2. Session 1: TTL 1 only — TTL 2 is a star.
	for _, seed := range []struct {
		sess int
		ttl  int
		ip   string
	}{
		{0, 1, "192.0.2.10"}, {0, 2, "192.0.2.11"}, {1, 1, "192.0.2.20"},
	} {
		h := m.sessions[seed.sess].table.GetOrCreate(seed.ttl)
		h.MarkRoundSent()
		h.AddSample(net.ParseIP(seed.ip), time.Duration(seed.ttl)*time.Millisecond, 0, hop.KindPrimary)
		h.MarkRoundEnd()
	}
	m.sessions[0].maxTTLHit = 2
	m.sessions[1].maxTTLHit = 2

	block := m.renderColumns()
	if !strings.Contains(block, "\x1b[") {
		t.Fatal("no ANSI escapes — forceColor did not take effect; width assertion would be vacuous")
	}
	for i, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		if w := lipgloss.Width(line); w != m.width {
			t.Errorf("line %d width = %d, want %d: %q", i, w, m.width, stripANSI(line))
		}
	}
}

// TestColumnView_HeightClamp verifies that column view output never exceeds
// m.height lines even when there are far more TTLs of data than available rows.
func TestColumnView_HeightClamp(t *testing.T) {
	setTestTheme(t)
	const termHeight = 24

	m := multiTestModel(t, withSize(120, termHeight))
	m.columnView = true

	// Seed 30 hops in session 0.
	for ttl := 1; ttl <= 30; ttl++ {
		ip := net.ParseIP("192.0.2.1")
		m.sessions[0].table.GetOrCreate(ttl).AddSample(ip, time.Duration(ttl)*time.Millisecond, 0, hop.KindPrimary)
		m.sessions[0].table.MarkRoundSent(ttl, 1)
	}

	output := stripANSI(m.View())
	lineCount := strings.Count(output, "\n")
	if lineCount >= termHeight {
		t.Errorf("column view emitted %d lines for height=%d (want < %d)",
			lineCount, termHeight, termHeight)
	}
}

// TestColumnView_ClampsToTargetTTL verifies the column view stops rendering at
// the TTL where the destination first answered, matching every other view.
//
// The interleaved ICMP tier is not TTL-limited once it reaches the destination,
// so the destination answers at its own TTL and at every TTL beyond it. Every
// other view clamps to maxTTLHit; columns.go used raw MaxTTLSeen() and repeated
// the destination row for every remaining TTL up to max-hops.
func TestColumnView_ClampsToTargetTTL(t *testing.T) {
	setTestTheme(t)
	m := multiTestModel(t, withSize(120, 40))

	const dest = "192.0.2.9"
	s := m.sessions[0]
	for _, tc := range []struct {
		ttl int
		ip  string
	}{
		{1, "192.0.2.254"},
		{2, "203.0.113.1"},
		{3, dest}, // destination first answers here
		{4, dest}, // ...and keeps answering past it
		{5, dest},
	} {
		h := s.table.GetOrCreate(tc.ttl)
		h.MarkRoundSent()
		h.AddSample(net.ParseIP(tc.ip), 5*time.Millisecond, 0, hop.KindPrimary)
		h.MarkRoundEnd()
	}
	s.targetHit = true
	s.maxTTLHit = 3

	m.columnView = true
	out := stripANSI(m.renderColumns())

	if got := strings.Count(out, dest); got != 1 {
		t.Errorf("destination %s rendered %d times, want 1 - rows past maxTTLHit must be clamped", dest, got)
		t.Logf("rendered column view:\n%s", out)
	}
}

// TestColumnView_ShallowTargetDoesNotRepeatDest: when one pane is deeper than
// another, the shared grid height is the deeper target's depth — but the shallow
// target must not fill its extra rows by repeating its own destination. Rows past
// a session's own maxTTLHit stay blank.
func TestColumnView_ShallowTargetDoesNotRepeatDest(t *testing.T) {
	setTestTheme(t)
	m := multiTestModel(t, withSize(140, 40))

	// Session 0 is deep: hops 1..5, destination at 5.
	for ttl := 1; ttl <= 5; ttl++ {
		h := m.sessions[0].table.GetOrCreate(ttl)
		h.MarkRoundSent()
		h.AddSample(net.ParseIP(fmt.Sprintf("192.0.2.%d", ttl)), 5*time.Millisecond, 0, hop.KindPrimary)
		h.MarkRoundEnd()
	}
	m.sessions[0].targetHit = true
	m.sessions[0].maxTTLHit = 5

	// Session 1 is shallow: destination answers at TTL 2 and (being past the
	// destination) repeats at 3,4,5 in the table.
	const dest = "203.0.113.9"
	h1 := m.sessions[1].table.GetOrCreate(1)
	h1.MarkRoundSent()
	h1.AddSample(net.ParseIP("203.0.113.1"), 5*time.Millisecond, 0, hop.KindPrimary)
	h1.MarkRoundEnd()
	for ttl := 2; ttl <= 5; ttl++ {
		h := m.sessions[1].table.GetOrCreate(ttl)
		h.MarkRoundSent()
		h.AddSample(net.ParseIP(dest), 5*time.Millisecond, 0, hop.KindPrimary)
		h.MarkRoundEnd()
	}
	m.sessions[1].targetHit = true
	m.sessions[1].maxTTLHit = 2

	m.columnView = true
	out := stripANSI(m.renderColumns())

	if got := strings.Count(out, dest); got != 1 {
		t.Errorf("shallow target's destination %s rendered %d times, want 1 - rows past its maxTTLHit must be blank", dest, got)
		t.Logf("rendered column view:\n%s", out)
	}
}
