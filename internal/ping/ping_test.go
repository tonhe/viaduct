package ping

import (
	"net"
	"testing"
)

func TestSupplementer_SubmitIdempotent(t *testing.T) {
	s := New(0)
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
	s := New(0)
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
	s := New(0)
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
