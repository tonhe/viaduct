package hop

import (
	"net"
	"testing"
	"time"
)

// The first round probes every TTL up to the maximum, because the
// destination's distance is not known yet. The destination answers all the
// probes that reach it, so it is recorded as a node at its own TTL and at
// every TTL beyond. Echoes go to the nodes of the path only: sending one a
// round to each of those extra entries put 26 echo requests a round on the
// destination in the lab, where one is meant.

func TestEchoNodes_StopAtTheDestination(t *testing.T) {
	tbl := NewTable(30)
	dst := net.ParseIP("198.51.100.1")
	tbl.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), time.Millisecond, 0, KindPrimary)
	tbl.GetOrCreate(2).AddSample(net.ParseIP("192.0.2.2"), time.Millisecond, 0, KindPrimary)
	tbl.GetOrCreate(2).AddSample(net.ParseIP("192.0.2.3"), time.Millisecond, 1, KindPrimary)
	for ttl := 3; ttl <= 30; ttl++ {
		h := tbl.GetOrCreate(ttl)
		h.AddSample(dst, time.Millisecond, 0, KindICMPTTL)
		h.MarkTarget(dst)
	}

	got := map[string]int{}
	for _, n := range tbl.EchoNodes() {
		got[n.IP.String()+"@"+itoa(n.TTL)]++
	}
	want := []string{"192.0.2.1@1", "192.0.2.2@2", "192.0.2.3@2", "198.51.100.1@3"}
	if len(got) != len(want) {
		t.Errorf("EchoNodes = %v, want exactly %v", got, want)
	}
	for _, k := range want {
		if got[k] != 1 {
			t.Errorf("EchoNodes has %d of %s, want 1 (all: %v)", got[k], k, got)
		}
	}
}

func TestEchoNodes_AllNodesWhileTheDestinationIsUnknown(t *testing.T) {
	tbl := NewTable(30)
	tbl.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), time.Millisecond, 0, KindPrimary)
	tbl.GetOrCreate(4).AddSample(net.ParseIP("192.0.2.4"), time.Millisecond, 0, KindPrimary)
	if got := tbl.EchoNodes(); len(got) != 2 {
		t.Errorf("EchoNodes = %v, want both known nodes", got)
	}
	if got := tbl.TargetTTL(); got != 0 {
		t.Errorf("TargetTTL = %d, want 0 before the destination has answered", got)
	}
}

func TestTargetTTL_IsTheNearestHopTheDestinationAnswersAt(t *testing.T) {
	tbl := NewTable(30)
	dst := net.ParseIP("198.51.100.1")
	for _, ttl := range []int{9, 7, 8} {
		h := tbl.GetOrCreate(ttl)
		h.AddSample(dst, time.Millisecond, 0, KindICMPTTL)
		h.MarkTarget(dst)
	}
	if got := tbl.TargetTTL(); got != 7 {
		t.Errorf("TargetTTL = %d, want 7", got)
	}
}

func itoa(n int) string {
	if n >= 10 {
		return string(rune('0'+n/10)) + string(rune('0'+n%10))
	}
	return string(rune('0' + n))
}
