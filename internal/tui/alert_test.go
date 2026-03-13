package tui

import (
	"testing"
	"time"
)

func TestAlertEngineHealthyByDefault(t *testing.T) {
	e := NewAlertEngine(5.0, 0, 3)
	if e.State() != AlertHealthy {
		t.Fatalf("expected healthy, got %v", e.State())
	}
	if e.Message() != "" {
		t.Fatalf("expected empty message, got %q", e.Message())
	}
}

func TestAlertEngineLossTriggersAfterNRounds(t *testing.T) {
	e := NewAlertEngine(5.0, 0, 3)
	for i := 0; i < 3; i++ {
		bell := e.Update(10.0, 20*time.Millisecond)
		if i < 2 && bell {
			t.Fatalf("bell should not ring before threshold rounds")
		}
	}
	if e.State() != AlertDegraded {
		t.Fatalf("expected degraded after 3 rounds, got %v", e.State())
	}
}

func TestAlertEngineRecovery(t *testing.T) {
	e := NewAlertEngine(5.0, 0, 3)
	for i := 0; i < 3; i++ {
		e.Update(10.0, 20*time.Millisecond)
	}
	bell := e.Update(0.0, 20*time.Millisecond)
	if e.State() != AlertHealthy {
		t.Fatalf("expected healthy after recovery")
	}
	_ = bell
}

func TestAlertEngineDisabled(t *testing.T) {
	e := NewAlertEngine(0, 0, 3)
	e.Update(50.0, 500*time.Millisecond)
	e.Update(50.0, 500*time.Millisecond)
	e.Update(50.0, 500*time.Millisecond)
	if e.State() != AlertHealthy {
		t.Fatalf("alerts should be disabled with 0 thresholds")
	}
}

func TestAlertEngineLatencyThreshold(t *testing.T) {
	e := NewAlertEngine(0, 100*time.Millisecond, 2)
	e.Update(0.0, 150*time.Millisecond)
	e.Update(0.0, 150*time.Millisecond)
	if e.State() != AlertDegraded {
		t.Fatalf("expected degraded on latency threshold")
	}
}
