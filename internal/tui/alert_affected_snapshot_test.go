package tui

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

// FindAffectedHop reads each hop's loss once, from one snapshot, while the
// tracer goes on closing rounds (viaduct#261). Run under -race, this is the
// check that it reads the hops safely. It also pins which hop is named.
func TestFindAffectedHop_WhileRoundsClose(t *testing.T) {
	table := hop.NewTable(30)
	ips := map[int]net.IP{
		1: net.ParseIP("192.0.2.1"),
		2: net.ParseIP("192.0.2.2"),
		3: net.ParseIP("192.0.2.3"),
	}
	// Every hop has answered a probe and none of 20 echoes, so its loss is
	// measured and counts toward alerting.
	for ttl, ip := range ips {
		table.MarkRoundSentByType(ttl, 1, 0)
		table.Get(ttl).AddSample(ip, time.Millisecond, 0, hop.KindICMPTTL)
		for i := 0; i < 20; i++ {
			table.Get(ttl).GetNodes()[0].MarkEchoSent()
		}
	}
	CloseRound(table)
	// round sends one probe to each hop. Hop 1 answers. Hops 2 and 3 do not:
	// the loss starts at hop 2 and the destination, hop 3, has the same.
	round := func() {
		for ttl := range ips {
			table.MarkRoundSentByType(ttl, 1, 0)
		}
		table.Get(1).AddSample(ips[1], time.Millisecond, 0, hop.KindICMPTTL)
		CloseRound(table)
	}
	// After 100 rounds one more round moves a hop's loss by less than 0.01,
	// far inside the 2 points FindAffectedHop allows between two hops.
	for i := 0; i < 100; i++ {
		round()
	}

	hopMap := make(map[int]*hop.Hop)
	for _, h := range table.Snapshot() {
		hopMap[h.TTL] = h
	}
	if got := hopMap[3].LossSnapshot(); got.Kind != hop.LossMeasured || got.SettledLoss < 90 {
		t.Fatalf("fixture: destination loss = %v (%v), want a measured loss above 90", got.SettledLoss, got.Kind)
	}
	if got := hopMap[1].LossSnapshot().SettledLoss; got != 0 {
		t.Fatalf("fixture: hop 1 loss = %v, want 0", got)
	}

	e := NewAlertEngine(5, 0, 3)
	for i := 0; i < 3; i++ {
		e.Update(hopMap[3].LossSnapshot().SettledLoss, 0)
	}
	if e.State() != AlertDegraded {
		t.Fatalf("fixture: state %v, want degraded", e.State())
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				round()
			}
		}
	}()
	for i := 0; i < 500; i++ {
		e.SetAffectedHop(0, "")
		e.FindAffectedHop(hopMap, 3, nil)
		if got := e.AffectedHopTTL(); got != 2 {
			close(stop)
			wg.Wait()
			t.Fatalf("pass %d: affected hop = %d, want 2, the first hop with the destination's loss", i, got)
		}
	}
	close(stop)
	wg.Wait()
}
