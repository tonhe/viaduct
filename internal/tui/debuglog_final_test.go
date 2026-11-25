package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/probe"
)

// The debug log's per-hop lines at the end of a run: the final set is always
// written, and a periodic snapshot of the same round is not written as well.

// hopLines are the per-hop lines of the log, snapshot and final, in order.
func (d *debugSink) hopLines() []string {
	var out []string
	for _, l := range d.lines {
		if strings.Contains(l, " snapshot round=") || strings.Contains(l, " final round=") {
			out = append(out, l)
		}
	}
	return out
}

// A run that ends on a multiple of 10 rounds writes its hop lines once, as
// the final set.
func TestDebugLog_RunEndingOnSnapshotRound_WritesFinalOnly(t *testing.T) {
	for _, rounds := range []int{10, 20} {
		t.Run(fmt.Sprintf("%d rounds", rounds), func(t *testing.T) {
			s, sink := debugSim()
			for i := 0; i < rounds; i++ {
				s.round(3, true)
			}
			s.m.DebugFinal()

			if got := sink.containing(fmt.Sprintf(" snapshot round=%d ", rounds)); len(got) != 0 {
				t.Errorf("round %d is written as a snapshot and as the final set:\n%s", rounds, strings.Join(sink.hopLines(), "\n"))
			}
			// Earlier periodic snapshots are still there, once each.
			if got, want := len(sink.containing(" snapshot ")), (rounds/10-1)*stallTTLs; got != want {
				t.Errorf("got %d snapshot lines, want %d:\n%s", got, want, strings.Join(sink.hopLines(), "\n"))
			}
			finals := sink.containing(" final ")
			if len(finals) != stallTTLs {
				t.Fatalf("got %d final lines, want %d:\n%s", len(finals), stallTTLs, strings.Join(sink.hopLines(), "\n"))
			}
			verdict := "undetermined" // fewer than 20 echoes
			if rounds >= 20 {
				verdict = "no"
			}
			want := fmt.Sprintf("[192.0.2.1] final round=%d ttl=3 nodes=192.0.2.3 loss=0.0%% kind=measured icmp-ttl=%d/0 udp-ttl=%d/0 echo=%d/0 echo-counted=%d/0 rate-limited=%s",
				rounds, rounds, 2*rounds, rounds, rounds, verdict)
			if finals[2] != want {
				t.Errorf("\n got %s\nwant %s", finals[2], want)
			}
			// The log ends with the final set.
			tail := sink.lines[len(sink.lines)-stallTTLs:]
			for i, l := range tail {
				if l != finals[i] {
					t.Errorf("the log does not end with the final set:\n%s", strings.Join(tail, "\n"))
					break
				}
			}
		})
	}
}

// A run that ends between two snapshot rounds writes the final set alone.
func TestDebugLog_RunOfSevenRounds_WritesFinalOnly(t *testing.T) {
	s, sink := debugSim()
	for i := 0; i < 7; i++ {
		s.round(3, true)
	}
	s.m.DebugFinal()
	got := sink.hopLines()
	if len(got) != stallTTLs {
		t.Fatalf("got %d hop lines, want %d:\n%s", len(got), stallTTLs, strings.Join(got, "\n"))
	}
	for i, l := range got {
		if want := fmt.Sprintf("[192.0.2.1] final round=7 ttl=%d ", i+1); !strings.HasPrefix(l, want) {
			t.Errorf("hop line %d = %s, want prefix %s", i+1, l, want)
		}
	}
}

// A reply that arrives between the close of round 10 and the exit is in the
// final set, and the figures from before it are not written.
func TestDebugLog_ReplyBeforeFinal_FinalShowsNewerFigures(t *testing.T) {
	s, sink := debugSim()
	for i := 0; i < 9; i++ {
		s.round(3, true)
	}
	s.round(3, false) // round 10 closes with its echoes unanswered
	s.m, _ = s.m.onHopUpdate(HopUpdateMsg{Result: probe.Result{
		TTL: stallTTLs, IP: stallNodeIP(stallTTLs), RTT: time.Millisecond, ProbeType: probe.ProbeICMPEcho,
	}})
	s.m.DebugFinal()

	got := sink.hopLines()
	want := []string{
		"[192.0.2.1] final round=10 ttl=1 nodes=192.0.2.1 loss=0.0% kind=measured icmp-ttl=10/0 udp-ttl=20/0 echo=10/1 echo-counted=10/1 rate-limited=undetermined",
		"[192.0.2.1] final round=10 ttl=2 nodes=192.0.2.2 loss=0.0% kind=measured icmp-ttl=10/0 udp-ttl=20/0 echo=10/1 echo-counted=10/1 rate-limited=undetermined",
		"[192.0.2.1] final round=10 ttl=3 nodes=192.0.2.3 loss=0.0% kind=measured icmp-ttl=10/0 udp-ttl=20/0 echo=10/0 echo-counted=10/0 rate-limited=undetermined",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d hop lines, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("hop line %d:\n got %s\nwant %s", i+1, got[i], want[i])
		}
	}
}

// A run that goes on past a snapshot round has that round's snapshot, with
// the figures the round closed with, written before the next round's line.
func TestDebugLog_SnapshotWrittenWhenTheNextRoundCloses(t *testing.T) {
	s, sink := debugSim()
	for i := 0; i < 10; i++ {
		s.round(3, true)
	}
	if got := sink.hopLines(); len(got) != 0 {
		t.Fatalf("hop lines before the run is known to go on:\n%s", strings.Join(got, "\n"))
	}
	s.round(0, false) // round 11: nothing answers

	n := len(sink.lines)
	if n < stallTTLs+2 {
		t.Fatalf("got %d lines:\n%s", n, strings.Join(sink.lines, "\n"))
	}
	if !strings.HasPrefix(sink.lines[n-1], "[192.0.2.1] round 11: ") {
		t.Errorf("last line = %s, want round 11's", sink.lines[n-1])
	}
	want := "[192.0.2.1] snapshot round=10 ttl=3 nodes=192.0.2.3 loss=0.0% kind=measured icmp-ttl=10/0 udp-ttl=20/0 echo=10/0 echo-counted=10/0 rate-limited=undetermined"
	if got := sink.lines[n-2]; got != want {
		t.Errorf("line before round 11's:\n got %s\nwant %s", got, want)
	}
	if got := sink.lines[n-2-stallTTLs]; !strings.HasPrefix(got, "[192.0.2.1] round 10: ") {
		t.Errorf("line before the snapshot = %s, want round 10's", got)
	}

	// The exit after round 11 writes the final set; round 10's snapshot stays
	// written once.
	s.m.DebugFinal()
	if got := sink.containing(" snapshot round=10 "); len(got) != stallTTLs {
		t.Errorf("got %d snapshot lines for round 10, want %d", len(got), stallTTLs)
	}
	if got := sink.containing(" final round=11 "); len(got) != stallTTLs {
		t.Errorf("got %d final lines for round 11, want %d", len(got), stallTTLs)
	}
}
