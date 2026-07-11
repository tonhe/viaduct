package ping

import (
	"math"
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

// TestStatSingleSample verifies that after exactly one reply mean=value and stdev=0 (no divide-by-zero).
func TestStatSingleSample(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(42 * time.Millisecond)

	if s.Received != 1 {
		t.Fatalf("expected Received=1, got %d", s.Received)
	}
	sd := s.StDev()
	if math.IsNaN(sd) {
		t.Fatal("StDev is NaN after single sample")
	}
	if sd != 0 {
		t.Fatalf("expected StDev=0 for single sample, got %g", sd)
	}
	gm := s.GeoMean()
	gmMs := float64(gm) / float64(time.Millisecond)
	if math.IsNaN(gmMs) {
		t.Fatal("GeoMean is NaN after single sample")
	}
	// GeoMean of a single value should be that value
	if math.Abs(gmMs-42.0) > 0.1 {
		t.Fatalf("expected GeoMean~42ms, got %.3fms", gmMs)
	}
}

// TestStatTwoIdenticalSamples verifies stdev=0 when both samples are equal (no NaN from Welford).
func TestStatTwoIdenticalSamples(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)

	sd := s.StDev()
	if math.IsNaN(sd) {
		t.Fatal("StDev is NaN for two identical samples")
	}
	if sd != 0 {
		t.Fatalf("expected StDev=0 for two identical samples, got %g", sd)
	}
}

// TestStatAllLoss verifies 100% loss with no received samples produces no NaN in stdev or geomean.
func TestStatAllLoss(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.MarkSent()
	s.MarkSent()

	loss := s.LossPercent()
	if loss != 100.0 {
		t.Fatalf("expected 100%% loss, got %.1f%%", loss)
	}
	sd := s.StDev()
	if math.IsNaN(sd) {
		t.Fatal("StDev is NaN with all-loss stat")
	}
	gm := s.GeoMean()
	if math.IsNaN(float64(gm)) {
		t.Fatal("GeoMean is NaN with all-loss stat")
	}
	if gm != 0 {
		t.Fatalf("expected GeoMean=0 for all-loss, got %v", gm)
	}
}

// TestStatAlternatingLossAndSuccess verifies running stats against hand-calculated values.
// Sequence: sent(no reply), 10ms, sent(no reply), 20ms -> 2 received, 4 sent, 50% loss, avg=15ms.
func TestStatAlternatingLossAndSuccess(t *testing.T) {
	s := &Stat{}
	s.MarkSent() // loss
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	s.MarkSent() // loss
	s.MarkSent()
	s.AddReply(20 * time.Millisecond)

	if s.Sent != 4 {
		t.Fatalf("expected Sent=4, got %d", s.Sent)
	}
	if s.Received != 2 {
		t.Fatalf("expected Received=2, got %d", s.Received)
	}
	loss := s.LossPercent()
	if loss != 50.0 {
		t.Fatalf("expected 50%% loss, got %.1f%%", loss)
	}
	avg := s.AvgRTT()
	if avg != 15*time.Millisecond {
		t.Fatalf("expected avg=15ms, got %v", avg)
	}
	// Welford: n=2, mean=15, M2=(10-15)^2+(20-15)^2 (via running algo) = 50, stdev=sqrt(50/1)=~7.07
	sd := s.StDev()
	if math.IsNaN(sd) {
		t.Fatal("StDev is NaN for alternating sequence")
	}
	if sd < 6.9 || sd > 7.2 {
		t.Fatalf("expected StDev~7.07, got %.4f", sd)
	}
}

// TestStatGeoMeanTinyValue verifies that a very small RTT (0.001ms) does not underflow log.
func TestStatGeoMeanTinyValue(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	// 1 microsecond = 0.001 ms — hits the guard rttMs = 0.001
	s.AddReply(1 * time.Microsecond)

	gm := s.GeoMean()
	gmMs := float64(gm) / float64(time.Millisecond)
	if math.IsNaN(gmMs) || math.IsInf(gmMs, 0) {
		t.Fatalf("GeoMean is invalid (%v) for tiny RTT", gmMs)
	}
	// Guard clamps to 0.001 ms, so geomean should be ~0.001ms
	if gmMs < 0.0009 || gmMs > 0.0011 {
		t.Fatalf("expected GeoMean~0.001ms for tiny RTT, got %.6fms", gmMs)
	}
}

// TestStatZeroRTT verifies that a zero-duration RTT does not produce log(0) = -Inf.
func TestStatZeroRTT(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(0) // zero RTT — hits guard rttMs = 0.001

	gm := s.GeoMean()
	gmMs := float64(gm) / float64(time.Millisecond)
	if math.IsNaN(gmMs) || math.IsInf(gmMs, 0) {
		t.Fatalf("GeoMean is invalid (%v) for zero RTT", gmMs)
	}
	if gmMs < 0 {
		t.Fatalf("expected non-negative GeoMean for zero RTT, got %.6f", gmMs)
	}
}

// TestStatLargeSampleWelford verifies Welford's numerical stability with 1000 samples.
// Uses RTTs alternating between 10ms and 20ms: mean=15ms, stdev=5.002... (population-ish).
// With n=1000 samples evenly split at 10ms and 20ms, sample stdev ~= 5.002506...
func TestStatLargeSampleWelford(t *testing.T) {
	s := &Stat{}
	for i := 0; i < 1000; i++ {
		s.MarkSent()
		if i%2 == 0 {
			s.AddReply(10 * time.Millisecond)
		} else {
			s.AddReply(20 * time.Millisecond)
		}
	}
	if s.Received != 1000 {
		t.Fatalf("expected Received=1000, got %d", s.Received)
	}
	avg := s.AvgRTT()
	if avg != 15*time.Millisecond {
		t.Fatalf("expected avg=15ms for 1000 samples, got %v", avg)
	}
	sd := s.StDev()
	if math.IsNaN(sd) || math.IsInf(sd, 0) {
		t.Fatalf("StDev is invalid (%v) for 1000 samples", sd)
	}
	// Sample stdev of alternating 10/20 with 1000 samples is sqrt(50*1000/999) ~= 5.0025
	if sd < 4.99 || sd > 5.02 {
		t.Fatalf("expected StDev~5.002 for 1000 alternating samples, got %.4f", sd)
	}
}

// TestStatAvgRTTZeroReceived verifies AvgRTT returns 0 when no replies received.
func TestStatAvgRTTZeroReceived(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	if s.AvgRTT() != 0 {
		t.Fatalf("expected AvgRTT=0 with no replies, got %v", s.AvgRTT())
	}
}

// TestStatGeoMeanZeroReceived verifies GeoMean returns 0 when no replies received.
func TestStatGeoMeanZeroReceived(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	if s.GeoMean() != 0 {
		t.Fatalf("expected GeoMean=0 with no replies, got %v", s.GeoMean())
	}
}

// TestStatRecordLoss verifies RecordLoss writes a zero into the sparkline ring buffer.
func TestStatRecordLoss(t *testing.T) {
	s := &Stat{}
	s.MarkSent()
	s.AddReply(10 * time.Millisecond)
	s.MarkSent()
	s.RecordLoss()

	data := s.SparklineData()
	if len(data) != 2 {
		t.Fatalf("expected 2 sparkline entries, got %d", len(data))
	}
	if data[0] != 10*time.Millisecond {
		t.Fatalf("expected first sparkline entry=10ms, got %v", data[0])
	}
	if data[1] != 0 {
		t.Fatalf("expected second sparkline entry=0 (loss), got %v", data[1])
	}
}

// TestStatSparklineDataEmpty verifies SparklineData returns nil when no samples exist.
func TestStatSparklineDataEmpty(t *testing.T) {
	s := &Stat{}
	data := s.SparklineData()
	if data != nil {
		t.Fatalf("expected nil SparklineData for empty stat, got %v", data)
	}
}

// TestStatSparklineWrap verifies the ring buffer correctly wraps and returns entries in order.
func TestStatSparklineWrap(t *testing.T) {
	s := &Stat{}
	// Fill buffer past capacity (sparklineSize=28), writing 30 entries
	for i := 0; i < 30; i++ {
		s.MarkSent()
		rtt := time.Duration(i+1) * time.Millisecond
		s.AddReply(rtt)
	}

	data := s.SparklineData()
	if len(data) != sparklineSize {
		t.Fatalf("expected sparkline length=%d after wrap, got %d", sparklineSize, len(data))
	}
	// After 30 writes into a 28-slot ring, oldest entry is write #3 (3ms),
	// newest is write #30 (30ms). data[0] should be 3ms.
	expectedFirst := time.Duration(30-sparklineSize+1) * time.Millisecond
	if data[0] != expectedFirst {
		t.Fatalf("expected oldest sparkline entry=%v, got %v", expectedFirst, data[0])
	}
	expectedLast := 30 * time.Millisecond
	if data[len(data)-1] != expectedLast {
		t.Fatalf("expected newest sparkline entry=%v, got %v", expectedLast, data[len(data)-1])
	}
}
