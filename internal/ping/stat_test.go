package ping

import (
	"testing"
	"time"
)

func TestPingStat_AddSample(t *testing.T) {
	s := &Stat{}
	s.AddSample(10 * time.Millisecond)
	s.AddSample(20 * time.Millisecond)

	if s.Sent != 2 {
		t.Fatalf("expected Sent 2, got %d", s.Sent)
	}
	if s.Received != 2 {
		t.Fatalf("expected Received 2, got %d", s.Received)
	}
	if s.MinRTT != 10*time.Millisecond {
		t.Fatalf("expected MinRTT 10ms, got %v", s.MinRTT)
	}
	if s.MaxRTT != 20*time.Millisecond {
		t.Fatalf("expected MaxRTT 20ms, got %v", s.MaxRTT)
	}
	if s.LastRTT != 20*time.Millisecond {
		t.Fatalf("expected LastRTT 20ms, got %v", s.LastRTT)
	}
}

func TestPingStat_AddLoss(t *testing.T) {
	s := &Stat{}
	s.AddSample(10 * time.Millisecond)
	s.AddLoss()
	if s.Sent != 2 {
		t.Fatalf("expected Sent 2, got %d", s.Sent)
	}
	if s.Received != 1 {
		t.Fatalf("expected Received 1, got %d", s.Received)
	}
}

func TestPingStat_LossPercent(t *testing.T) {
	s := &Stat{}
	if s.LossPercent() != 0 {
		t.Fatal("expected 0 loss for empty stat")
	}
	s.AddSample(10 * time.Millisecond)
	s.AddLoss()
	loss := s.LossPercent()
	if loss != 50.0 {
		t.Fatalf("expected 50%% loss, got %.1f%%", loss)
	}
}

func TestPingStat_AvgRTT(t *testing.T) {
	s := &Stat{}
	s.AddSample(10 * time.Millisecond)
	s.AddSample(20 * time.Millisecond)
	avg := s.AvgRTT()
	if avg != 15*time.Millisecond {
		t.Fatalf("expected avg 15ms, got %v", avg)
	}
}

func TestPingStat_StDev(t *testing.T) {
	s := &Stat{}
	s.AddSample(10 * time.Millisecond)
	if s.StDev() != 0 {
		t.Fatal("expected 0 stdev for single sample")
	}
	s.AddSample(20 * time.Millisecond)
	stdev := s.StDev()
	if stdev < 6.0 || stdev > 8.0 {
		t.Fatalf("expected stdev ~7.07, got %.1f", stdev)
	}
}
