package tui

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// The --debug-log round records: one line per completed round, a per-hop
// snapshot every 10 rounds and at exit, and a line when an alert is raised or
// cleared. A periodic snapshot is written when the round after it closes
// (debuglog_final_test.go has the end-of-run cases).

// debugSink collects debug-log lines as the file would hold them, without the
// timestamp.
type debugSink struct{ lines []string }

func (d *debugSink) logf(format string, args ...any) {
	d.lines = append(d.lines, fmt.Sprintf(format, args...))
}

func (d *debugSink) containing(s string) []string {
	var out []string
	for _, l := range d.lines {
		if strings.Contains(l, s) {
			out = append(out, l)
		}
	}
	return out
}

// debugSim is a stallSim whose session writes to a debug log. Its clock
// advances one second per reading, so every round takes one second.
func debugSim(opts ...testOpt) (*stallSim, *debugSink) {
	sink := &debugSink{}
	s := &stallSim{m: newTestModel(opts...)}
	s.m.SetDebugLog(sink.logf)
	clock := time.Unix(0, 0)
	s.m.sessions[0].debug.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	s.m.sessions[0].debug.last = clock
	return s, sink
}

func TestDebugLog_RoundLine(t *testing.T) {
	s, sink := debugSim()
	s.round(3, true)  // everything answered
	s.round(2, false) // one flow per hop and every echo unanswered
	s.round(0, false) // a blackout: no hop answers
	s.stalledRound()  // nothing left

	want := []string{
		"[192.0.2.1] round 1: took 1s; sent icmp-ttl=3 udp-ttl=6 icmp-echo=3; replies icmp-ttl=3 udp-ttl=6 icmp-echo=3; no reply from 0 of 3 hops probed",
		"[192.0.2.1] round 2: took 1s; sent icmp-ttl=3 udp-ttl=6 icmp-echo=3; replies icmp-ttl=3 udp-ttl=3 icmp-echo=0; no reply from 0 of 3 hops probed",
		"[192.0.2.1] round 3: took 1s; sent icmp-ttl=3 udp-ttl=6 icmp-echo=3; replies icmp-ttl=0 udp-ttl=0 icmp-echo=0; no reply from 3 of 3 hops probed",
		"[192.0.2.1] round 4: took 1s; sent icmp-ttl=0 udp-ttl=0 icmp-echo=0; replies icmp-ttl=0 udp-ttl=0 icmp-echo=0; no reply from 0 of 0 hops probed",
	}
	if len(sink.lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(sink.lines), len(want), strings.Join(sink.lines, "\n"))
	}
	for i := range want {
		if sink.lines[i] != want[i] {
			t.Errorf("line %d:\n got %s\nwant %s", i+1, sink.lines[i], want[i])
		}
	}
}

// With ICMP as the primary protocol there are no UDP or TCP probes to report.
func TestDebugLog_RoundLine_ICMPPrimary(t *testing.T) {
	sink := &debugSink{}
	m := newTestModel()
	m.sessions[0].protocolName = "icmp"
	m.SetDebugLog(sink.logf)
	m.sessions[0].table.MarkRoundSentByType(1, 1, 0)
	m, _ = m.onRoundEnd(RoundEndMsg{})
	if len(sink.lines) != 1 || !strings.Contains(sink.lines[0], "; sent icmp-ttl=1 icmp-echo=0; replies icmp-ttl=0 icmp-echo=0; no reply from 1 of 1 hops probed") {
		t.Errorf("got %q", sink.lines)
	}
}

// A reset zeroes the hop counters; the next round's line still reports that
// round alone.
func TestDebugLog_RoundLine_AfterReset(t *testing.T) {
	s, sink := debugSim()
	for i := 0; i < 3; i++ {
		s.round(3, true)
	}
	s.m.sessions[0].table.ResetAll()
	s.round(3, true)
	got := sink.lines[len(sink.lines)-1]
	want := "[192.0.2.1] round 4: took 1s; sent icmp-ttl=3 udp-ttl=6 icmp-echo=3; replies icmp-ttl=3 udp-ttl=6 icmp-echo=3; no reply from 0 of 3 hops probed"
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}

// The snapshot's loss is the headline figure, in the form the views show.
func TestDebugLog_SnapshotEveryTenRounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rounds int
		round  func(s *stallSim, i int)
		want   string // the destination's line of the last snapshot
	}{
		{
			name: "no loss", rounds: 10,
			round: func(s *stallSim, i int) { s.round(3, true) },
			want:  "[192.0.2.1] snapshot round=10 ttl=3 nodes=192.0.2.3 loss=0.0% kind=measured icmp-ttl=10/0 udp-ttl=20/0 echo=10/0 echo-counted=10/0 rate-limited=undetermined",
		},
		{
			name: "provisional", rounds: 10,
			round: func(s *stallSim, i int) { s.round(2, true) },
			want:  "[192.0.2.1] snapshot round=10 ttl=3 nodes=192.0.2.3 loss=33.3%? kind=provisional icmp-ttl=10/0 udp-ttl=20/10 echo=10/0 echo-counted=10/0 rate-limited=undetermined",
		},
		{
			name: "rate-limited", rounds: 30,
			round: func(s *stallSim, i int) { s.round(1, true) },
			want:  "[192.0.2.1] snapshot round=30 ttl=3 nodes=192.0.2.3 loss=~0%† kind=rate_limited icmp-ttl=30/0 udp-ttl=60/60 echo=30/0 echo-counted=30/0 rate-limited=yes",
		},
		{
			name: "real loss", rounds: 30,
			round: func(s *stallSim, i int) { s.round(2, i%3 != 0) },
			want:  "[192.0.2.1] snapshot round=30 ttl=3 nodes=192.0.2.3 loss=33.3% kind=measured icmp-ttl=30/0 udp-ttl=60/30 echo=30/10 echo-counted=30/10 rate-limited=no",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sink := debugSim()
			for i := 0; i < tc.rounds; i++ {
				tc.round(s, i)
			}
			// The last snapshot is written when the next round closes. That
			// round sends nothing, so the hop's figures stay as they were.
			s.stalledRound()
			snaps := sink.containing(" snapshot ")
			if want := tc.rounds / 10 * stallTTLs; len(snaps) != want {
				t.Fatalf("got %d snapshot lines, want %d (one per hop every 10 rounds):\n%s", len(snaps), want, strings.Join(snaps, "\n"))
			}
			if got := snaps[len(snaps)-1]; got != tc.want {
				t.Errorf("\n got %s\nwant %s", got, tc.want)
			}
			// The figure is the one the views show.
			dest := s.m.sessions[0].table.Get(stallTTLs)
			if shown := hop.FormatLoss(dest.Loss()); !strings.Contains(tc.want, " loss="+shown+" ") {
				t.Errorf("fixture: the hop shows %s, the wanted line says otherwise", shown)
			}
		})
	}
}

// A hop that never answered has no node, and still reports what was sent.
func TestDebugLog_SnapshotSilentHop(t *testing.T) {
	sink := &debugSink{}
	m := newTestModel()
	m.SetDebugLog(sink.logf)
	for i := 0; i < 10; i++ {
		m.sessions[0].table.MarkRoundSentByType(1, 1, 2)
		m, _ = m.onRoundEnd(RoundEndMsg{})
	}
	m, _ = m.onRoundEnd(RoundEndMsg{}) // round 11 closes: round 10's snapshot is written
	snaps := sink.containing(" snapshot ")
	want := "[192.0.2.1] snapshot round=10 ttl=1 nodes=- loss=100.0% kind=measured icmp-ttl=10/10 udp-ttl=20/20 echo=0/0 echo-counted=0/0 rate-limited=undetermined"
	if len(snaps) != 1 || snaps[0] != want {
		t.Errorf("\n got %q\nwant %s", snaps, want)
	}
}

func TestDebugLog_FinalSnapshot(t *testing.T) {
	s, sink := debugSim()
	for i := 0; i < 4; i++ {
		s.round(3, true)
	}
	s.m.DebugFinal()
	finals := sink.containing(" final ")
	if len(finals) != stallTTLs {
		t.Fatalf("got %d final lines, want %d:\n%s", len(finals), stallTTLs, strings.Join(sink.lines, "\n"))
	}
	want := "[192.0.2.1] final round=4 ttl=3 nodes=192.0.2.3 loss=0.0% kind=measured icmp-ttl=4/0 udp-ttl=8/0 echo=4/0 echo-counted=4/0 rate-limited=undetermined"
	if finals[2] != want {
		t.Errorf("\n got %s\nwant %s", finals[2], want)
	}
	if got := sink.containing(" snapshot "); len(got) != 0 {
		t.Errorf("snapshot before round 10: %q", got)
	}
}

func TestDebugLog_AlertRaisedAndCleared(t *testing.T) {
	// Alert above 35% loss for 3 rounds (the fixture of
	// TestRoundsWithNothingSent_DoNotAdvanceTheAlert).
	s, sink := debugSim(withAlertEngine(35, 0, 3))
	for i := 0; i < 20; i++ {
		s.round(2, i%3 != 0)
	}
	if got := sink.containing("alert"); len(got) != 0 {
		t.Fatalf("fixture: alert lines during warm-up: %q", got)
	}
	for i := 0; i < 3; i++ {
		s.round(0, false)
	}
	engine := s.m.sessions[0].alertEngine
	if engine.State() != AlertDegraded {
		t.Fatal("fixture: no alert after three rounds of loss")
	}
	raised := sink.containing("alert raised")
	want := "[192.0.2.1] round 23: alert raised: " + strings.TrimPrefix(engine.Message(), "! ")
	if len(raised) != 1 || raised[0] != want {
		t.Fatalf("\n got %q\nwant %s", raised, want)
	}
	if !strings.Contains(raised[0], "Sustained loss to destination") {
		t.Errorf("alert line does not carry the alert's text: %s", raised[0])
	}

	// More bad rounds do not repeat the line.
	s.round(0, false)
	if got := sink.containing("alert raised"); len(got) != 1 {
		t.Errorf("alert raised logged %d times, want once", len(got))
	}

	// The path recovers: the alert clears, once.
	for i := 0; i < 200 && engine.State() == AlertDegraded; i++ {
		s.round(3, true)
	}
	if engine.State() != AlertHealthy {
		t.Fatal("fixture: alert did not clear")
	}
	s.round(3, true)
	cleared := sink.containing("alert cleared")
	if len(cleared) != 1 || !strings.Contains(cleared[0], ": alert cleared: Sustained loss to destination") {
		t.Errorf("alert cleared lines = %q, want one with the alert's text", cleared)
	}
	if len(sink.containing("alert raised")) != 1 {
		t.Error("alert raised logged again")
	}
}

// Every target writes its own records, tagged with its address.
func TestDebugLog_SeveralTargets(t *testing.T) {
	sink := &debugSink{}
	m := newTestModel()
	second := newSession("example.net", net.ParseIP("2001:db8::7"), m.sessions[0].probeCfg, "udp", 5, 0, 3, false)
	m.sessions = append(m.sessions, second)
	m.SetDebugLog(sink.logf)

	second.table.MarkRoundSentByType(1, 1, 2)
	m, _ = m.onRoundEnd(RoundEndMsg{TargetIdx: 1})
	m.sessions[0].table.MarkRoundSentByType(1, 1, 2)
	m, _ = m.onRoundEnd(RoundEndMsg{TargetIdx: 0})
	m, _ = m.onRoundEnd(RoundEndMsg{TargetIdx: 1})
	m.DebugFinal()

	for _, tc := range []struct {
		prefix string
		want   int
	}{
		{"[2001:db8::7] round 1: ", 1},
		{"[2001:db8::7] round 2: ", 1},
		{"[192.0.2.1] round 1: ", 1},
		{"[192.0.2.1] round 2: ", 0},
		{"[2001:db8::7] final round=2 ttl=1 ", 1},
		{"[192.0.2.1] final round=1 ttl=1 ", 1},
	} {
		if got := sink.containing(tc.prefix); len(got) != tc.want {
			t.Errorf("%d lines with %q, want %d:\n%s", len(got), tc.prefix, tc.want, strings.Join(sink.lines, "\n"))
		}
	}
}

// Without --debug-log nothing is recorded and nothing breaks.
func TestDebugLog_OffWritesNothing(t *testing.T) {
	if d := NewRoundDebug(nil, "192.0.2.1", "udp", nil); d != nil {
		t.Errorf("NewRoundDebug with no log = %v, want nil", d)
	}
	if d := NewReportDebug(nil, "192.0.2.1", "udp", 5, 0, 3, false); d != nil {
		t.Errorf("NewReportDebug with no log = %v, want nil", d)
	}
	var d *RoundDebug
	table := hop.NewTable(30)
	d.RoundClosed(table, nil) // must not panic
	d.Final(table)

	s := &stallSim{m: newTestModel(withAlertEngine(5, 0, 3))}
	s.m.SetDebugLog(nil)
	if s.m.sessions[0].debug != nil {
		t.Error("SetDebugLog(nil) attached a recorder")
	}
	for i := 0; i < 12; i++ {
		s.round(0, false)
	}
	s.m.DebugFinal()
}

// Report mode has no TUI to hold the alert engine: the recorder runs its own,
// with the same thresholds, and logs what the TUI would have shown.
func TestReportDebug_DrivesItsOwnAlertEngine(t *testing.T) {
	sink := &debugSink{}
	d := NewReportDebug(sink.logf, "192.0.2.9", "udp", 5, 0, 3, false)
	table := hop.NewTable(30)
	ip := net.ParseIP("192.0.2.9")
	closeRound := func(answered bool) {
		table.MarkRoundSentByType(1, 1, 2)
		h := table.Get(1)
		if answered {
			h.AddSample(ip, time.Millisecond, 0, hop.KindICMPTTL)
			h.AddSample(ip, time.Millisecond, 0, hop.KindPrimary)
			h.AddSample(ip, time.Millisecond, 1, hop.KindPrimary)
			h.MarkTarget(ip)
		}
		for _, n := range h.GetNodes() {
			n.MarkEchoSent() // never answered: the loss is real
		}
		d.RoundClosed(table, CloseRound(table))
	}
	for i := 0; i < 25; i++ {
		closeRound(true)
	}
	if got := sink.containing("alert"); len(got) != 0 {
		t.Fatalf("alert lines on a healthy path: %q", got)
	}
	for i := 0; i < 15; i++ {
		closeRound(false)
	}
	raised := sink.containing(": alert raised: Sustained loss to destination")
	if len(raised) != 1 {
		t.Fatalf("got %d alert-raised lines, want 1:\n%s", len(raised), strings.Join(sink.containing("alert"), "\n"))
	}
	d.Final(table)
	if got := sink.containing("[192.0.2.9] final round=40 ttl=1 nodes=192.0.2.9 "); len(got) != 1 {
		t.Errorf("final snapshot lines = %q", sink.containing(" final "))
	}

	// --no-alert: rounds and snapshots, no alert lines.
	quiet := &debugSink{}
	d = NewReportDebug(quiet.logf, "192.0.2.9", "udp", 5, 0, 3, true)
	table = hop.NewTable(30)
	for i := 0; i < 10; i++ {
		closeRound(false)
	}
	if got := quiet.containing("alert"); len(got) != 0 {
		t.Errorf("alert lines with alerting off: %q", got)
	}
	if got := quiet.containing(" round 10: "); len(got) != 1 {
		t.Errorf("round lines with alerting off: %q", quiet.lines)
	}
}

// Hops beyond the destination are in the hop table (round one probes every
// TTL before the destination's distance is known) and are listed in the
// snapshot and final sets, marked beyond=yes, with the fields every hop line
// has. Hops up to the destination carry no mark.
func TestDebugLog_HopsBeyondDestination(t *testing.T) {
	sink := &debugSink{}
	d := NewReportDebug(sink.logf, "192.0.2.9", "udp", 5, 0, 3, true)
	table := hop.NewTable(30)
	router, dest := net.ParseIP("198.51.100.1"), net.ParseIP("192.0.2.9")
	answer := func(ttl int, ip net.IP) {
		h := table.Get(ttl)
		h.AddSample(ip, time.Millisecond, 0, hop.KindICMPTTL)
		h.AddSample(ip, time.Millisecond, 0, hop.KindPrimary)
		h.AddSample(ip, time.Millisecond, 1, hop.KindPrimary)
	}
	// Later rounds stop at the destination's TTL.
	pathRound := func() {
		table.MarkRoundSentByType(1, 1, 2)
		table.MarkRoundSentByType(2, 1, 2)
		answer(1, router)
		answer(2, dest)
		d.RoundClosed(table, CloseRound(table))
	}
	// Round one probes TTLs 1 to 4: the destination answers at 2 and at 3,
	// nothing answers at 4.
	for ttl := 1; ttl <= 4; ttl++ {
		table.MarkRoundSentByType(ttl, 1, 2)
	}
	answer(1, router)
	answer(2, dest)
	table.Get(2).MarkTarget(dest)
	answer(3, dest)
	table.Get(3).MarkTarget(dest)
	d.RoundClosed(table, CloseRound(table))
	for i := 1; i < 10; i++ {
		pathRound()
	}
	if got := table.TargetTTL(); got != 2 {
		t.Fatalf("fixture: destination's TTL = %d, want 2", got)
	}
	pathRound() // round 11 closes: round 10's snapshot is written
	d.Final(table)

	const tail = " echo=0/0 echo-counted=0/0 rate-limited=undetermined"
	for _, tc := range []struct {
		label string
		round int
	}{{"snapshot", 10}, {"final", 11}} {
		r := tc.round
		want := []string{
			fmt.Sprintf("[192.0.2.9] %s round=%d ttl=1 nodes=198.51.100.1 loss=0.0%% kind=measured icmp-ttl=%d/0 udp-ttl=%d/0", tc.label, r, r, 2*r) + tail,
			fmt.Sprintf("[192.0.2.9] %s round=%d ttl=2 nodes=192.0.2.9 loss=0.0%% kind=measured icmp-ttl=%d/0 udp-ttl=%d/0", tc.label, r, r, 2*r) + tail,
			fmt.Sprintf("[192.0.2.9] %s round=%d ttl=3 nodes=192.0.2.9 loss=0.0%% kind=measured icmp-ttl=1/0 udp-ttl=2/0", tc.label, r) + tail + " beyond=yes",
			fmt.Sprintf("[192.0.2.9] %s round=%d ttl=4 nodes=- loss=100.0%% kind=measured icmp-ttl=1/1 udp-ttl=2/2", tc.label, r) + tail + " beyond=yes",
		}
		got := sink.containing(" " + tc.label + " ")
		if len(got) != len(want) {
			t.Fatalf("got %d %s lines, want %d (every hop in the table):\n%s", len(got), tc.label, len(want), strings.Join(got, "\n"))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s line %d:\n got %s\nwant %s", tc.label, i+1, got[i], want[i])
			}
		}
	}
}

// While the destination has not answered there is no destination TTL, and no
// hop is marked as beyond it.
func TestDebugLog_NoBeyondMarkWithoutDestination(t *testing.T) {
	sink := &debugSink{}
	d := NewReportDebug(sink.logf, "192.0.2.9", "udp", 5, 0, 3, true)
	table := hop.NewTable(30)
	for ttl := 1; ttl <= 3; ttl++ {
		table.MarkRoundSentByType(ttl, 1, 2)
	}
	d.RoundClosed(table, CloseRound(table))
	d.Final(table)
	if got := sink.containing(" final "); len(got) != 3 {
		t.Fatalf("got %d final lines, want 3:\n%s", len(got), strings.Join(got, "\n"))
	}
	if got := sink.containing("beyond="); len(got) != 0 {
		t.Errorf("hops marked beyond a destination that has not answered: %q", got)
	}
}
