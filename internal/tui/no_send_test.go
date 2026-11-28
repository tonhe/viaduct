package tui

import (
	"strings"
	"testing"

	"github.com/tonhe/viaduct/internal/probe"
)

// A session whose first rounds sent nothing (every send failed) has no hop
// row: a TTL nothing was sent to gets none. Every view then says what
// happened where its rows would be, and stops saying it once a probe has
// left (viaduct#154).

const (
	noSendLong1  = "No probe could be sent in the first round. Run with --debug-log FILE to see why."
	noSendLong3  = "No probe could be sent in the first 3 rounds. Run with --debug-log FILE to see why."
	noSendShort3 = "No probe sent in 3 rounds"
)

// unsentRound is a round in which no send succeeded: the tracer reports
// nothing written for every TTL and ends the round.
func unsentRound(m Model, idx int) Model {
	for ttl := 1; ttl <= 5; ttl++ {
		m.sessions[idx].table.MarkRoundSentByType(ttl, 0, 0)
	}
	m, _ = m.onRoundEnd(RoundEndMsg{TargetIdx: idx})
	return m
}

func TestNoSendNotice_TableViewsAndProbeDetail(t *testing.T) {
	m := newTestModel()
	m.width, m.height = 100, 12

	// Before any round has closed the views say nothing: the trace has
	// only just started.
	if out := stripANSI(m.View()); strings.Contains(out, "No probe") {
		t.Fatalf("before the first round closed:\n%s", out)
	}

	m = unsentRound(m, 0)
	if got := len(m.sessions[0].table.Snapshot()); got != 0 {
		t.Fatalf("fixture: %d hop rows after a round that sent nothing, want 0", got)
	}
	if out := stripANSI(m.View()); !strings.Contains(out, noSendLong1) {
		t.Errorf("after one round that sent nothing, want %q:\n%s", noSendLong1, out)
	}
	m = unsentRound(m, 0)
	m = unsentRound(m, 0)

	for _, v := range []ViewMode{ViewDefault, ViewHealth, ViewLatency, ViewVariability, ViewProbeDetail} {
		m.viewMode = v
		out := stripANSI(m.View())
		lines := strings.Split(out, "\n")
		// Title, separator, column header, then the notice where the first
		// row would be.
		if len(lines) < 4 || strings.TrimRight(lines[3], " ") != noSendLong3 {
			t.Errorf("%v view: line 4 is not the notice %q:\n%s", v, noSendLong3, out)
		}
		assertFits(t, v.String(), m.View(), m.width, m.height)
	}

	// A narrow terminal cuts the notice; it does not wrap or overflow.
	m.viewMode = ViewDefault
	m.width = 40
	out := stripANSI(m.View())
	if !strings.Contains(out, "No probe could be sent in the first 3 ro") {
		t.Errorf("at 40 columns the notice is not shown cut:\n%s", out)
	}
	assertFits(t, "narrow", m.View(), 40, m.height)
	m.width = 100

	// With --debug-log the notice points at the log.
	m.sessions[0].debug = &RoundDebug{}
	if out := stripANSI(m.View()); !strings.Contains(out, "No probe could be sent in the first 3 rounds. The debug log says why.") {
		t.Errorf("with a debug log:\n%s", out)
	}
	m.sessions[0].debug = nil

	// The first probe that leaves makes a row, and the notice goes.
	m.sessions[0].table.MarkRoundSentByType(1, 1, 0)
	if out := stripANSI(m.View()); strings.Contains(out, "No probe") {
		t.Errorf("after a probe left, the notice is still shown:\n%s", out)
	}
	m, _ = m.onHopUpdate(HopUpdateMsg{Result: probe.Result{TTL: 1, IP: stallNodeIP(1), ProbeType: probe.ProbeICMPTTL}})
	m, _ = m.onRoundEnd(RoundEndMsg{})
	if out := stripANSI(m.View()); strings.Contains(out, "No probe") || !strings.Contains(out, stallNodeIP(1).String()) {
		t.Errorf("after a hop answered:\n%s", out)
	}
}

// A paused trace sends nothing on purpose: its empty rounds are not counted.
// A reset starts the count over.
func TestNoSendNotice_PauseAndReset(t *testing.T) {
	m := newTestModel()
	m = pressKeys(t, m, "p")
	for i := 0; i < 3; i++ {
		m, _ = m.onRoundEnd(RoundEndMsg{})
	}
	if out := stripANSI(m.View()); strings.Contains(out, "No probe") {
		t.Errorf("a paused trace is reported as unable to send:\n%s", out)
	}
	m = pressKeys(t, m, "p")
	m = unsentRound(m, 0)
	m = unsentRound(m, 0)
	if out := stripANSI(m.View()); !strings.Contains(out, "in the first 2 rounds") {
		t.Errorf("after two rounds that sent nothing:\n%s", out)
	}
	m = pressKeys(t, m, "r")
	if out := stripANSI(m.View()); strings.Contains(out, "No probe") {
		t.Errorf("after a reset the notice is still shown:\n%s", out)
	}
}

// The summary printed when the TUI quits says the same in place of a table
// with no rows.
func TestNoSendNotice_FinalSummary(t *testing.T) {
	m := newTestModel()
	for i := 0; i < 3; i++ {
		m = unsentRound(m, 0)
	}
	out := m.FinalSummary()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "via — example.com (192.0.2.1)") || lines[1] != noSendLong3 {
		t.Errorf("final summary:\n%s\nwant the header line and %q", out, noSendLong3)
	}
}

// Column view: the pane of a target that could not send says so in its first
// row. The pane of a target that is tracing is unchanged.
func TestNoSendNotice_ColumnViewPane(t *testing.T) {
	m := multiTestModel(t)
	m.width, m.height = 100, 14
	m.columnView = true
	for i := 0; i < 3; i++ {
		m = unsentRound(m, 1)
		m.sessions[0].table.MarkRoundSentByType(1, 1, 0)
		m, _ = m.onHopUpdate(HopUpdateMsg{TargetIdx: 0, Result: probe.Result{TTL: 1, IP: stallNodeIP(1), ProbeType: probe.ProbeICMPTTL}})
		m, _ = m.onRoundEnd(RoundEndMsg{TargetIdx: 0})
	}
	out := stripANSI(m.View())
	var row string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, stallNodeIP(1).String()) {
			row = l
		}
	}
	if row == "" || !strings.Contains(row, "│ "+noSendShort3) {
		t.Errorf("the second pane's first row does not say %q:\n%s", noSendShort3, out)
	}
	if strings.Count(out, "No probe") != 1 {
		t.Errorf("the notice is shown %d times, want once, in the pane that could not send:\n%s", strings.Count(out, "No probe"), out)
	}
	assertFits(t, "column view", m.View(), m.width, m.height)
}
