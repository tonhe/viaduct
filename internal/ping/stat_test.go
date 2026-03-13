package ping

import (
	"testing"
	"time"
)

func TestPingStat_AddReply(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	s.MarkSent()
	s.AddReply(20 * time.Millisecond)

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

func TestPingStat_MarkSentWithoutReply(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	s.MarkSent() // no reply = loss
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
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	s.MarkSent() // lost
	loss := s.LossPercent()
	if loss != 50.0 {
		t.Fatalf("expected 50%% loss, got %.1f%%", loss)
	}
}

func TestPingStat_AvgRTT(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	s.MarkSent()
	s.AddReply(20 * time.Millisecond)
	avg := s.AvgRTT()
	if avg != 15*time.Millisecond {
		t.Fatalf("expected avg 15ms, got %v", avg)
	}
}

func TestPingStat_StDev(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	if s.StDev() != 0 {
		t.Fatal("expected 0 stdev for single sample")
	}
	s.MarkSent()
	s.AddReply(20 * time.Millisecond)
	stdev := s.StDev()
	if stdev < 6.0 || stdev > 8.0 {
		t.Fatalf("expected stdev ~7.07, got %.1f", stdev)
	}
}

func TestStatGeoMean(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	s.MarkSent()
	s.AddReply(100 * time.Millisecond)
	gm := s.GeoMean()
	gmMs := float64(gm) / float64(time.Millisecond)
	if gmMs < 30.0 || gmMs > 33.0 {
		t.Fatalf("expected ~31.6ms, got %.1fms", gmMs)
	}
}

func TestStatJitter(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	s.MarkSent()
	s.AddReply(15 * time.Millisecond)
	if s.Jitter() != 5*time.Millisecond {
		t.Fatalf("expected 5ms jitter, got %v", s.Jitter())
	}
}

func TestStatJitterMean(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	s.MarkSent()
	s.AddReply(20 * time.Millisecond) // j=10
	s.MarkSent()
	s.AddReply(15 * time.Millisecond) // j=5
	jm := s.JitterMean()
	jmMs := float64(jm) / float64(time.Millisecond)
	if jmMs < 6.5 || jmMs > 8.5 {
		t.Fatalf("expected ~7.5ms, got %.1fms", jmMs)
	}
}

func TestStatSparklineData(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	s.MarkSent()
	s.AddReply(20 * time.Millisecond)
	data := s.SparklineData()
	if len(data) != 2 {
		t.Fatalf("expected 2, got %d", len(data))
	}
}
