package ping

import (
	"net"
	"testing"
	"time"
)

func TestSupplementer_SubmitIdempotent(t *testing.T) {
	s := New()
	ip := net.ParseIP("1.1.1.1")
	s.Submit(ip)
	s.Submit(ip) // should not panic or add duplicate
	count := 0
	s.targets.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != 1 {
		t.Fatalf("expected 1 target, got %d", count)
	}
}

func TestSupplementer_SubmitMaxTargets(t *testing.T) {
	s := New()
	for i := 0; i < 15; i++ {
		s.Submit(net.IPv4(10, 0, 0, byte(i)))
	}
	count := 0
	s.targets.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != 10 {
		t.Fatalf("expected max 10 targets, got %d", count)
	}
}

func TestSupplementer_SubmitNil(t *testing.T) {
	s := New()
	s.Submit(nil) // should not panic
}

func TestBuildPingPacket(t *testing.T) {
	pkt, err := buildPingPacket(50001, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkt) == 0 {
		t.Fatal("expected non-empty packet")
	}
}

func TestExtractRTT_ShortData(t *testing.T) {
	if extractRTT(nil) != 0 {
		t.Fatal("expected 0 for nil data")
	}
	if extractRTT(make([]byte, 4)) != 0 {
		t.Fatal("expected 0 for short data")
	}
}

func TestStat_MarkSentAndAddReply(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)

	if s.Sent != 2 {
		t.Fatalf("expected Sent=2, got %d", s.Sent)
	}
	if s.Received != 1 {
		t.Fatalf("expected Received=1, got %d", s.Received)
	}
	if s.LossPercent() != 50.0 {
		t.Fatalf("expected 50%% loss, got %.1f%%", s.LossPercent())
	}
}

func TestStat_AddReply(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	s.MarkSent()
	s.AddReply(20 * time.Millisecond)

	if s.Sent != 2 {
		t.Fatalf("expected Sent=2, got %d", s.Sent)
	}
	if s.Received != 2 {
		t.Fatalf("expected Received=2, got %d", s.Received)
	}
	if s.LossPercent() != 0 {
		t.Fatalf("expected 0%% loss, got %.1f%%", s.LossPercent())
	}
	avg := s.AvgRTT()
	if avg != 15*time.Millisecond {
		t.Fatalf("expected avg 15ms, got %v", avg)
	}
	if s.MinRTT != 10*time.Millisecond {
		t.Fatalf("expected min 10ms, got %v", s.MinRTT)
	}
	if s.MaxRTT != 20*time.Millisecond {
		t.Fatalf("expected max 20ms, got %v", s.MaxRTT)
	}
}

func TestStat_StDev(t *testing.T) {
	s := &Stat{}
	for _, rtt := range []time.Duration{10 * time.Millisecond, 20 * time.Millisecond} {
		s.MarkSent()
		s.AddReply(rtt)
	}
	sd := s.StDev()
	if sd < 6.0 || sd > 8.0 {
		t.Fatalf("expected stdev in [6.0, 8.0], got %.2f", sd)
	}
}

func TestStat_LossPercent(t *testing.T) {
	s := &Stat{}
	// 10 sent, 7 replies
	for i := 0; i < 10; i++ {
		s.MarkSent()
	}
	for i := 0; i < 7; i++ {
		s.AddReply(5 * time.Millisecond)
	}
	loss := s.LossPercent()
	if loss != 30.0 {
		t.Fatalf("expected 30%% loss, got %.1f%%", loss)
	}
}

func TestPingAll_NoConn(t *testing.T) {
	s := New()
	s.Submit(net.ParseIP("1.1.1.1"))
	s.PingAll() // should not panic with nil conn
}
