package probe

import (
	"testing"
	"time"
)

func TestConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxHops != 30 {
		t.Fatalf("expected MaxHops 30, got %d", cfg.MaxHops)
	}
	if cfg.FirstTTL != 1 {
		t.Fatalf("expected FirstTTL 1, got %d", cfg.FirstTTL)
	}
	if cfg.Timeout != time.Second {
		t.Fatalf("expected Timeout 1s, got %v", cfg.Timeout)
	}
	if cfg.ProbeDelay != 10*time.Millisecond {
		t.Fatalf("expected ProbeDelay 10ms, got %v", cfg.ProbeDelay)
	}
	if cfg.PayloadSize != 64 {
		t.Fatalf("expected PayloadSize 64, got %d", cfg.PayloadSize)
	}
}

func TestBuildICMPEchoRequest(t *testing.T) {
	pkt, err := buildICMPEchoRequest(1234, 1, 64)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkt) == 0 {
		t.Fatal("expected non-empty packet")
	}
}

func TestDefaultConfigECMP(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.NumPaths != 6 {
		t.Fatalf("expected NumPaths 6, got %d", cfg.NumPaths)
	}
	if cfg.BasePort != 44000 {
		t.Fatalf("expected BasePort 44000, got %d", cfg.BasePort)
	}
	if cfg.DestPort != 33434 {
		t.Fatalf("expected DestPort 33434, got %d", cfg.DestPort)
	}
}

func TestResultHasFlowID(t *testing.T) {
	r := Result{TTL: 3, FlowID: 2}
	if r.FlowID != 2 {
		t.Fatalf("expected FlowID 2, got %d", r.FlowID)
	}
}

func TestProbeMapMatchAndExpiry(t *testing.T) {
	pm := newProbeMap()
	key := probeKey{SrcPort: 100, DstPort: 200, Seq: 1}
	pm.add(key, 5, 0, time.Now())

	rec, ok := pm.match(key)
	if !ok {
		t.Fatal("expected to find probe")
	}
	if rec.ttl != 5 {
		t.Fatalf("expected ttl 5, got %d", rec.ttl)
	}

	// Second match should fail (consumed)
	_, ok = pm.match(key)
	if ok {
		t.Fatal("expected probe to be consumed after match")
	}
}

func TestBuildUDPProbe(t *testing.T) {
	pkt, err := buildUDPProbe(44000, 33435, 64)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkt) != 8+64 {
		t.Fatalf("expected packet length %d, got %d", 8+64, len(pkt))
	}
	srcPort := int(pkt[0])<<8 | int(pkt[1])
	if srcPort != 44000 {
		t.Fatalf("expected src port 44000, got %d", srcPort)
	}
	dstPort := int(pkt[2])<<8 | int(pkt[3])
	if dstPort != 33435 {
		t.Fatalf("expected dst port 33435, got %d", dstPort)
	}
}

func TestProbeMapSweep(t *testing.T) {
	pm := newProbeMap()
	old := time.Now().Add(-5 * time.Second)
	fresh := time.Now()

	keyOld := probeKey{SrcPort: 100, DstPort: 200, Seq: 0}
	keyFresh := probeKey{SrcPort: 101, DstPort: 201, Seq: 0}
	pm.add(keyOld, 3, 0, old)
	pm.add(keyFresh, 4, 1, fresh)

	// Sweep entries older than 2 seconds
	pm.sweep(2 * time.Second)

	// Old entry should be gone
	_, ok := pm.match(keyOld)
	if ok {
		t.Fatal("expected stale entry to be swept")
	}

	// Fresh entry should still exist
	rec, ok := pm.match(keyFresh)
	if !ok {
		t.Fatal("expected fresh entry to survive sweep")
	}
	if rec.ttl != 4 {
		t.Fatalf("expected ttl 4, got %d", rec.ttl)
	}
}

func TestProbeMapWithFlowID(t *testing.T) {
	pm := newProbeMap()
	key1 := probeKey{SrcPort: 44000, DstPort: 33435, Seq: 1}
	key2 := probeKey{SrcPort: 44001, DstPort: 33435, Seq: 1}
	pm.add(key1, 5, 0, time.Now())
	pm.add(key2, 5, 1, time.Now())

	rec, ok := pm.match(key1)
	if !ok {
		t.Fatal("expected to find probe for flow 0")
	}
	if rec.ttl != 5 {
		t.Fatalf("expected ttl 5, got %d", rec.ttl)
	}
	if rec.flowID != 0 {
		t.Fatalf("expected flowID 0, got %d", rec.flowID)
	}

	// Verify key2 is independent and carries flowID 1
	rec2, ok2 := pm.match(key2)
	if !ok2 {
		t.Fatal("expected to find probe for flow 1")
	}
	if rec2.flowID != 1 {
		t.Fatalf("expected flowID 1, got %d", rec2.flowID)
	}
}
