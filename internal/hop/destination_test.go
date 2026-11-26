package hop

import (
	"net"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/dest"
)

// Which TTL is the destination's when the target answers at more than one
// (viaduct#234). TestTargetTTL_IsTheNearestHopTheDestinationAnswersAt pins
// what holds until a round has closed; these pin what holds after.

var (
	destTarget = net.ParseIP("198.51.100.1")
	destRouter = net.ParseIP("192.0.2.8")
)

// noteReply records a reply to a probe as the views' collectors do.
func noteReply(tbl *Table, ttl int, ip net.IP, flow int, kind ProbeKind) {
	tbl.GetOrCreate(ttl).AddSample(ip, time.Millisecond, flow, kind)
	tbl.NoteReply(ttl, ip, flow, kind, ip.Equal(destTarget))
}

func TestTargetTTL_SettlesWhenTheFirstRoundCloses(t *testing.T) {
	tbl := NewTable(30)
	for _, ttl := range []int{9, 7, 8} {
		noteReply(tbl, ttl, destTarget, 0, KindICMPTTL)
	}
	if got := tbl.TargetTTL(); got != 7 {
		t.Fatalf("TargetTTL = %d before the round closes, want 7 (the nearest marked)", got)
	}
	tbl.EndRound()
	if got := tbl.TargetTTL(); got != 7 {
		t.Errorf("TargetTTL = %d, want 7", got)
	}
}

// One reply from the target below its distance, in a later round, does not
// move the destination; neither do two rounds of them in a row.
func TestTargetTTL_AStrayReplyDoesNotMoveIt(t *testing.T) {
	tbl := NewTable(30)
	noteReply(tbl, 7, destTarget, 0, KindICMPTTL)
	tbl.EndRound()
	for i := 0; i < dest.MoveRounds-1; i++ {
		noteReply(tbl, 4, destTarget, 0, KindICMPTTL)
		noteReply(tbl, 7, destTarget, 0, KindICMPTTL)
		tbl.EndRound()
	}
	noteReply(tbl, 7, destTarget, 0, KindICMPTTL)
	tbl.EndRound()
	if got := tbl.TargetTTL(); got != 7 {
		t.Errorf("TargetTTL = %d, want 7", got)
	}
	// The target stays marked at TTL 4: that hop lists it as a node.
	if n := tbl.Get(4).PrimaryNode(); n == nil || !n.IsTarget() {
		t.Error("the target's node at TTL 4 lost its mark")
	}
	// Echo targets run up to the destination, TTL 4's node included.
	var ttls []int
	for _, n := range tbl.EchoNodes() {
		ttls = append(ttls, n.TTL)
	}
	if len(ttls) != 2 || ttls[0] != 4 || ttls[1] != 7 {
		t.Errorf("echo node TTLs = %v, want [4 7]", ttls)
	}
}

// The target answers flow 1 at TTL 8 and both flows at TTL 9; a router
// answers flow 0 at TTL 8. The destination is TTL 9, from the first round.
func TestTargetTTL_FlowsOfUnequalPathLength(t *testing.T) {
	tbl := NewTable(30)
	for round := 0; round < 5; round++ {
		noteReply(tbl, 8, destRouter, 0, KindPrimary)
		noteReply(tbl, 8, destTarget, 1, KindPrimary)
		noteReply(tbl, 9, destTarget, 0, KindPrimary)
		noteReply(tbl, 9, destTarget, 1, KindPrimary)
		if round == 0 {
			if got := tbl.TargetTTL(); got != 8 {
				t.Fatalf("TargetTTL = %d before the first round closes, want 8", got)
			}
		}
		tbl.EndRound()
		if got := tbl.TargetTTL(); got != 9 {
			t.Fatalf("round %d: TargetTTL = %d, want 9", round+1, got)
		}
	}
	var last EchoNode
	for _, n := range tbl.EchoNodes() {
		last = n
	}
	if last.TTL != 9 || !last.IP.Equal(destTarget) {
		t.Errorf("last echo node = %v at TTL %d, want the target at TTL 9", last.IP, last.TTL)
	}
}

// The destination moves down when the target answers at a lower TTL, with
// no router there, for dest.MoveRounds rounds in a row; and back up the same
// way.
func TestTargetTTL_FollowsARealPathChange(t *testing.T) {
	tbl := NewTable(30)
	noteReply(tbl, 8, destRouter, 0, KindICMPTTL)
	noteReply(tbl, 9, destTarget, 0, KindICMPTTL)
	tbl.EndRound()
	for i := 1; i <= dest.MoveRounds; i++ {
		if got := tbl.TargetTTL(); got != 9 {
			t.Fatalf("TargetTTL = %d after %d rounds at TTL 8, want 9", got, i-1)
		}
		noteReply(tbl, 8, destTarget, 0, KindICMPTTL)
		noteReply(tbl, 9, destTarget, 0, KindICMPTTL)
		tbl.EndRound()
	}
	if got := tbl.TargetTTL(); got != 8 {
		t.Fatalf("TargetTTL = %d, want 8 after %d rounds", got, dest.MoveRounds)
	}
	for i := 1; i <= dest.MoveRounds; i++ {
		if got := tbl.TargetTTL(); got != 8 {
			t.Fatalf("TargetTTL = %d after %d rounds back at TTL 9, want 8", got, i-1)
		}
		noteReply(tbl, 8, destRouter, 0, KindICMPTTL)
		noteReply(tbl, 9, destTarget, 0, KindICMPTTL)
		tbl.EndRound()
	}
	if got := tbl.TargetTTL(); got != 9 {
		t.Errorf("TargetTTL = %d, want 9 after %d rounds", got, dest.MoveRounds)
	}
}

// Direct echoes say nothing about distance: the target's reply to an echo
// sent for its node at a TTL beyond the destination does not count.
func TestTargetTTL_EchoRepliesAreNotEvidence(t *testing.T) {
	tbl := NewTable(30)
	noteReply(tbl, 5, destTarget, 0, KindICMPTTL)
	noteReply(tbl, 6, destTarget, 0, KindICMPTTL)
	tbl.EndRound()
	for i := 0; i < 2*dest.MoveRounds; i++ {
		noteReply(tbl, 6, destTarget, 0, KindEcho)
		tbl.EndRound()
	}
	if got := tbl.TargetTTL(); got != 5 {
		t.Errorf("TargetTTL = %d, want 5", got)
	}
}

// ResetAll forgets the destination with the hops' nodes.
func TestTargetTTL_ResetAll(t *testing.T) {
	tbl := NewTable(30)
	noteReply(tbl, 5, destTarget, 0, KindICMPTTL)
	tbl.EndRound()
	tbl.ResetAll()
	if got := tbl.TargetTTL(); got != 0 {
		t.Errorf("TargetTTL = %d after ResetAll, want 0", got)
	}
}
