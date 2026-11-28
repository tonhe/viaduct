package tui

import (
	"strings"
	"testing"
)

// pausedRound is a round a paused tracer closes: nothing is sent, nothing is
// reported as not written, and the round ends.
func (s *stallSim) pausedRound() {
	s.m, _ = s.m.onRoundEnd(RoundEndMsg{})
}

// While the trace is paused the debug log gets no round lines and no
// snapshots, the round number stands still, and the log says when the trace
// was paused and when it resumed (viaduct#166).
func TestDebugLog_Paused(t *testing.T) {
	s, sink := debugSim()
	for i := 0; i < 8; i++ {
		s.round(3, true)
	}
	s.m = pressKeys(t, s.m, "p")
	if !s.m.paused {
		t.Fatal("fixture: p did not pause the model")
	}
	before := len(sink.lines)
	if got := sink.lines[before-1]; got != "[192.0.2.1] paused after round 8" {
		t.Fatalf("after pausing, the last line is %q", got)
	}
	// 25 empty rounds: more than the 10 between snapshots.
	for i := 0; i < 25; i++ {
		s.pausedRound()
	}
	if len(sink.lines) != before {
		t.Fatalf("paused rounds wrote %d lines:\n%s", len(sink.lines)-before, strings.Join(sink.lines[before:], "\n"))
	}

	s.m = pressKeys(t, s.m, "p")
	if got := sink.lines[len(sink.lines)-1]; !strings.HasPrefix(got, "[192.0.2.1] resumed after ") {
		t.Fatalf("after resuming, the last line is %q", got)
	}
	// The empty round the tracer was waiting out when the trace resumed.
	s.pausedRound()
	if got := sink.lines[len(sink.lines)-1]; !strings.HasPrefix(got, "[192.0.2.1] resumed after ") {
		t.Fatalf("the paused tracer's last empty round was recorded: %q", got)
	}

	// The next round is round 9, and it took one second: the paused time is
	// not in it.
	s.round(3, true)
	want := "[192.0.2.1] round 9: took 1s; sent icmp-ttl=3 udp-ttl=6 icmp-echo=3; replies icmp-ttl=3 udp-ttl=6 icmp-echo=3; no reply from 0 of 3 hops probed"
	if got := sink.lines[len(sink.lines)-1]; got != want {
		t.Errorf("first round after resuming:\n got %s\nwant %s", got, want)
	}
	// Round 10 is a snapshot round as usual, written when round 11 closes.
	s.round(3, true)
	s.round(3, true)
	if got := sink.containing("snapshot round=10 "); len(got) != stallTTLs {
		t.Errorf("%d snapshot lines for round 10, want %d", len(got), stallTTLs)
	}
	if got := sink.containing("snapshot round="); len(got) != stallTTLs {
		t.Errorf("%d snapshot lines in all, want %d: none for a paused round", len(got), stallTTLs)
	}
	if got := sink.containing("paused after"); len(got) != 1 {
		t.Errorf("%d pause lines, want 1", len(got))
	}
	if got := sink.containing("resumed after"); len(got) != 1 {
		t.Errorf("%d resume lines, want 1", len(got))
	}
}

// The round that was under way when the trace was paused sent probes: it is
// recorded, with its round number.
func TestDebugLog_PausedMidRound(t *testing.T) {
	s, sink := debugSim()
	s.round(3, true)
	s.m = pressKeys(t, s.m, "p")
	s.round(3, true) // its probes had left before the pause
	s.pausedRound()
	s.pausedRound()
	if got := sink.containing(" round 2: took "); len(got) != 1 {
		t.Errorf("the round under way at the pause was not recorded:\n%s", strings.Join(sink.lines, "\n"))
	}
	if got := sink.containing(" round 3: "); len(got) != 0 {
		t.Errorf("an empty round of the paused trace was recorded: %q", got)
	}
}

// A round in which every send failed is recorded while the trace is running,
// before a pause and after one: that is what the log is for.
func TestDebugLog_StalledRoundIsStillRecorded(t *testing.T) {
	const empty = "sent icmp-ttl=0 udp-ttl=0 icmp-echo=0; replies icmp-ttl=0 udp-ttl=0 icmp-echo=0; no reply from 0 of 0 hops probed"
	s, sink := debugSim()
	s.round(3, true)
	s.stalledRound()
	if got := sink.lines[len(sink.lines)-1]; !strings.Contains(got, " round 2: ") || !strings.HasSuffix(got, empty) {
		t.Fatalf("a round in which every send failed: last line %q", got)
	}

	s.m = pressKeys(t, s.m, "p")
	s.pausedRound()
	s.m = pressKeys(t, s.m, "p")
	s.round(3, true) // round 3: the trace is running again
	s.stalledRound()
	if got := sink.lines[len(sink.lines)-1]; !strings.Contains(got, " round 4: ") || !strings.HasSuffix(got, empty) {
		t.Errorf("a round in which every send failed, after a pause: last line %q", got)
	}
}

// A nil recorder (no --debug-log) takes a pause and a resume.
func TestDebugLog_SetPausedOnNil(t *testing.T) {
	var d *RoundDebug
	d.SetPaused(true)
	d.SetPaused(false)
}
