package main

import (
	"net"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// Report mode's echo targets stop at the destination, as the TUI's do.
func TestCollectEchoTargets_NoneBeyondTheDestination(t *testing.T) {
	tbl := hop.NewTable(30)
	dst := net.ParseIP("198.51.100.1")
	tbl.GetOrCreate(1).AddSample(net.ParseIP("192.0.2.1"), time.Millisecond, 0, hop.KindPrimary)
	for ttl := 2; ttl <= 30; ttl++ {
		h := tbl.GetOrCreate(ttl)
		h.AddSample(dst, time.Millisecond, 0, hop.KindICMPTTL)
		h.MarkTarget(dst)
	}
	got := collectEchoTargets(tbl)
	if len(got) != 2 {
		t.Fatalf("collectEchoTargets returned %d targets, want 2: %v", len(got), got)
	}
	if got[0].TTL != 1 || got[1].TTL != 2 || !got[1].IP.Equal(dst) {
		t.Errorf("targets = %v, want the router at TTL 1 and the destination at TTL 2", got)
	}
}
