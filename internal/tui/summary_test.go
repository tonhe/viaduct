package tui

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tonhe/viaduct/internal/hop"
)

// multiTestModel builds a 3-target model with fixture data:
// target 0 healthy with 3 hops, target 1 with one hop of data, target 2 errored at startup.
func multiTestModel(t *testing.T, opts ...testOpt) Model {
	t.Helper()
	m := newTestModel(opts...)
	m.sessions[0].target = "one.example"

	s2 := newSession("two.example", net.ParseIP("192.0.2.2"), m.sessions[0].probeCfg, "udp", 5.0, 0, 3, true)
	s3 := newSession("bad.example", nil, m.sessions[0].probeCfg, "udp", 5.0, 0, 3, true)
	s3.startErr = fmt.Errorf("cannot resolve host")

	m.sessions = append(m.sessions, s2, s3)
	return m
}

func TestGolden_SummaryView(t *testing.T) {
	setTestTheme(t)
	m := multiTestModel(t,
		withSize(120, 40),
		withHops(
			hopSample(1, "192.0.2.254", 1300*time.Microsecond),
			hopSample(2, "203.0.113.1", 10200*time.Microsecond),
			hopSample(3, "192.0.2.1", 12400*time.Microsecond),
		),
	)
	// Give target 1 one hop of data.
	h1s1 := m.sessions[1].table.GetOrCreate(1)
	h1s1.MarkRoundSent()
	h1s1.AddSample(net.ParseIP("192.0.2.254"), 1300*time.Microsecond, 0, hop.KindPrimary)
	h1s1.MarkRoundEnd()

	goldenCompare(t, "testdata/view/summary_three_targets.golden", m.View())
}

func TestGolden_SummaryView_SixteenTargets(t *testing.T) {
	setTestTheme(t)
	m := newTestModel(
		withSize(120, 50),
		withHops(hopSample(1, "192.0.2.254", 1300*time.Microsecond)),
	)
	m.sessions[0].target = "t00.example"
	for i := 1; i < 16; i++ {
		s := newSession(
			fmt.Sprintf("t%02d.example", i),
			net.ParseIP(fmt.Sprintf("192.0.2.%d", i+1)),
			m.sessions[0].probeCfg,
			"udp", 5.0, 0, 3, true,
		)
		m.sessions = append(m.sessions, s)
	}
	goldenCompare(t, "testdata/view/summary_sixteen_targets.golden", m.View())
}

func TestGolden_SummaryView_SingleTargetUnchanged(t *testing.T) {
	setTestTheme(t)
	m := newTestModel(
		withSize(100, 24),
		withHops(hopSample(1, "192.0.2.254", 1300*time.Microsecond)),
	)
	if len(m.sessions) != 1 {
		t.Fatal("expected single session")
	}
	out := m.View()
	if strings.Contains(stripANSI(out), "TARGET") {
		t.Fatal("single-target view must not render the summary bar")
	}
}

func TestGolden_SummaryView_ErroredFocus(t *testing.T) {
	setTestTheme(t)
	m := multiTestModel(t,
		withSize(120, 40),
		withHops(
			hopSample(1, "192.0.2.254", 1300*time.Microsecond),
			hopSample(2, "203.0.113.1", 10200*time.Microsecond),
			hopSample(3, "192.0.2.1", 12400*time.Microsecond),
		),
	)
	// Focus the errored session (index 2).
	m.focusedIdx = 2

	// Must not panic (nil targetIP) and must show the error message.
	out := m.View()
	plain := stripANSI(out)
	if !strings.Contains(plain, "cannot resolve host") {
		t.Errorf("errored-focus view should contain error message; got:\n%s", plain)
	}
	if !strings.Contains(plain, "bad.example") {
		t.Errorf("errored-focus view should contain the target name; got:\n%s", plain)
	}

	goldenCompare(t, "testdata/view/summary_errored_focus.golden", out)
}

func TestSummary_TruncateSummary(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		// ASCII cases
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		// n=4: must fit in 4 visual columns → 3 ASCII + "…" (1 col) = 4 cols
		{"hello", 4, "hel…"},
		{"hello", 1, "h"},
		{"hello", 0, ""},
		{"", 5, ""},
		// Multi-byte UTF-8: 'é' is U+00E9, two bytes but one rune/cell.
		// "héllo" is 5 runes. n=3 → 2 runes + "…" = "hé…"
		{"héllo", 10, "héllo"},
		{"héllo", 5, "héllo"},
		{"héllo", 3, "hé…"},
		{"héllo", 1, "h"},
		// Exact-length multi-byte: no truncation needed
		{"héllo", 6, "héllo"},
		// n<=1 edge: only one rune returned, no ellipsis
		{"héllo", 0, ""},
		{"éxample", 1, "é"},
	}
	for _, c := range cases {
		got := truncateSummary(c.in, c.n)
		if got != c.want {
			t.Errorf("truncateSummary(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncateSummary(%q, %d) produced invalid UTF-8: %q", c.in, c.n, got)
		}
	}
}

func TestSummary_DestStats_Empty(t *testing.T) {
	s := newSession("t", net.ParseIP("192.0.2.1"), newTestModel().sessions[0].probeCfg, "udp", 0, 0, 3, true)
	loss, _, avg := destStats(s)
	if loss != 0 || avg != 0 {
		t.Errorf("destStats empty table = (%v, %v), want (0, 0)", loss, avg)
	}
}

func TestSummary_WorstHop_Empty(t *testing.T) {
	s := newSession("t", net.ParseIP("192.0.2.1"), newTestModel().sessions[0].probeCfg, "udp", 0, 0, 3, true)
	got := worstHop(s)
	if got != "—" {
		t.Errorf("worstHop empty table = %q, want %q", got, "—")
	}
}

func TestSummary_WorstHop_WithData(t *testing.T) {
	s := newSession("t", net.ParseIP("192.0.2.1"), newTestModel().sessions[0].probeCfg, "udp", 0, 0, 3, true)
	s.table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 0, hop.KindPrimary)
	s.table.GetOrCreate(2).AddSample(net.ParseIP("192.0.2.2"), 50*time.Millisecond, 0, hop.KindPrimary) // big delta
	s.table.GetOrCreate(3).AddSample(net.ParseIP("192.0.2.3"), 55*time.Millisecond, 0, hop.KindPrimary)
	got := worstHop(s)
	// Hop 2 has the largest positive delta (50-5=45ms).
	if got != "192.0.2.2" {
		t.Errorf("worstHop = %q, want 192.0.2.2", got)
	}
}

// TestSummary_WorstHop_Gap verifies that worstHop matches what largestDeltaTTL
// picks when there is a non-responding gap hop (TTL 2). computeDeltas carries
// prevAvg across the gap, so TTL 3 (20ms) sees delta = 20-5 = 15ms, not 20ms
// from zero as a naive reset-on-gap implementation would compute.
func TestSummary_WorstHop_Gap(t *testing.T) {
	s := newSession("t", net.ParseIP("192.0.2.1"), newTestModel().sessions[0].probeCfg, "udp", 0, 0, 3, true)
	// TTL 1: 5ms, TTL 2: non-responding (no samples), TTL 3: 20ms
	s.table.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), 5*time.Millisecond, 0, hop.KindPrimary)
	// TTL 2: create the hop entry but add no samples (simulates * gap)
	s.table.GetOrCreate(2)
	s.table.GetOrCreate(3).AddSample(net.ParseIP("192.0.2.3"), 20*time.Millisecond, 0, hop.KindPrimary)

	// Build the same hopMap that worstHop will build and verify largestDeltaTTL
	// independently so we can assert worstHop matches it.
	snap := s.table.Snapshot()
	hopMap := make(map[int]*hop.Hop, len(snap))
	maxTTL := 0
	for _, h := range snap {
		hopMap[h.TTL] = h
		if h.TTL > maxTTL {
			maxTTL = h.TTL
		}
	}
	deltas := computeDeltas(hopMap, maxTTL)
	wantTTL := largestDeltaTTL(deltas)
	wantIP := ""
	if wantTTL != 0 {
		if h, ok := hopMap[wantTTL]; ok {
			if ip := h.GetIP(); ip != nil {
				wantIP = ip.String()
			}
		}
	}
	if wantIP == "" {
		wantIP = "—"
	}

	got := worstHop(s)
	if got != wantIP {
		t.Errorf("worstHop gap scenario = %q, want %q (largestDeltaTTL=%d)", got, wantIP, wantTTL)
	}
	// TTL 3 should be the largest contributor: delta = 20ms - 5ms = 15ms
	// (prevAvg carried across the gap, not reset to 0).
	if wantTTL != 3 {
		t.Errorf("largestDeltaTTL for gap scenario = %d, want 3 (gap must not reset prevAvg)", wantTTL)
	}
}

// TestSummary_DestStats_TrailingGapHop verifies that destStats returns the
// stats of the last *responding* hop, skipping any trailing hop that has no
// samples (a non-responding gap hop at the end of the path).
func TestSummary_DestStats_TrailingGapHop(t *testing.T) {
	s := newSession("t", net.ParseIP("192.0.2.1"), newTestModel().sessions[0].probeCfg, "udp", 0, 0, 3, true)
	// TTL 1: responding with known RTT
	h1 := s.table.GetOrCreate(1)
	h1.MarkRoundSent()
	h1.AddSample(net.ParseIP("192.0.2.1"), 10*time.Millisecond, 0, hop.KindPrimary)
	h1.MarkRoundEnd()
	// TTL 2: created but no samples — simulates a non-responding destination
	h2 := s.table.GetOrCreate(2)
	h2.MarkRoundSent()
	h2.MarkRoundEnd()

	loss, _, avg := destStats(s)
	// Should report TTL 1 stats, not TTL 2 zeros.
	if avg == 0 {
		t.Errorf("destStats with trailing gap hop: avg = 0, want non-zero (should use last responding hop)")
	}
	// TTL 1 has 1 sent and 1 received → 0% loss.
	if loss != 0 {
		t.Errorf("destStats with trailing gap hop: loss = %v, want 0%%", loss)
	}
	if avg != 10*time.Millisecond {
		t.Errorf("destStats with trailing gap hop: avg = %v, want 10ms", avg)
	}
}

func TestSummary_MultiTarget_NoSummaryInSingleTarget(t *testing.T) {
	setTestTheme(t)
	m := newTestModel(withSize(120, 30))
	out := m.View()
	plain := stripANSI(out)
	// Summary header keyword "TARGET" must not appear in single-target mode.
	for _, line := range strings.Split(plain, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "TARGET") {
			t.Errorf("single-target view must not contain summary header; found: %q", line)
		}
	}
}

func TestSummary_FocusedRowHighlighted(t *testing.T) {
	setTestTheme(t)
	m := multiTestModel(t, withSize(120, 40))
	m.focusedIdx = 0
	out := m.View()
	// The focused row starts with the focus arrow.
	if !strings.Contains(out, "▶") {
		t.Error("focused row must contain focus arrow ▶")
	}
}

func TestSummary_MarkedTarget(t *testing.T) {
	setTestTheme(t)
	m := multiTestModel(t, withSize(120, 40))
	m.sessions[1].marked = true
	out := m.View()
	plain := stripANSI(out)
	if !strings.Contains(plain, "*two.example") {
		t.Errorf("marked target should render with * prefix; got:\n%s", plain)
	}
}

func TestSummary_AlertPrefixedWithTargetName(t *testing.T) {
	setTestTheme(t)
	m := multiTestModel(t,
		withSize(120, 40),
		withHops(
			hopSample(1, "192.0.2.254", 1300*time.Microsecond),
			hopSample(2, "203.0.113.1", 250*time.Millisecond), // high latency
		),
		withAlertEngine(5.0, 100*time.Millisecond, 1),
	)
	// Trigger the alert engine manually.
	ae := m.sessions[0].alertEngine
	ae.Update(0, 250*time.Millisecond)

	out := m.View()
	plain := stripANSI(out)
	// In multi-target mode the alert bar should be prefixed with the target name.
	if !strings.Contains(plain, "one.example:") {
		t.Errorf("multi-target alert should be prefixed with target name; got:\n%s", plain)
	}
}
