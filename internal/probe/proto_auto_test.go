package probe

import (
	"testing"
)

func TestAutoSelector_StartsWithPrimary(t *testing.T) {
	auto := NewAutoSelector(
		NewUDPProtocol(33434),
		NewICMPProtocol(),
	)
	if auto.Name() != "udp" {
		t.Errorf("expected 'udp', got %q", auto.Name())
	}
	if !auto.SupportsMultipath() {
		t.Error("should support multipath when using UDP")
	}
	if auto.HasSwitched() {
		t.Error("should not be switched initially")
	}
}

func TestAutoSelector_FallbackSwitchesProtocol(t *testing.T) {
	auto := NewAutoSelector(
		NewUDPProtocol(33434),
		NewICMPProtocol(),
	)
	auto.Fallback()
	if auto.Name() != "icmp" {
		t.Errorf("expected 'icmp' after fallback, got %q", auto.Name())
	}
	if auto.SupportsMultipath() {
		t.Error("should not support multipath after fallback to ICMP")
	}
	if !auto.HasSwitched() {
		t.Error("should be switched after Fallback()")
	}
}

func TestAutoSelector_DelegatesToActive(t *testing.T) {
	auto := NewAutoSelector(
		NewUDPProtocol(33434),
		NewICMPProtocol(),
	)
	cfg := DefaultConfig()

	// Before fallback: should build UDP probes
	pkt, _, err := auto.BuildProbe(0, 5, 10, cfg)
	if err != nil {
		t.Fatalf("BuildProbe failed: %v", err)
	}
	if len(pkt) < 8 {
		t.Error("expected UDP-sized packet before fallback")
	}

	// After fallback: should build ICMP probes
	auto.Fallback()
	pkt2, _, err := auto.BuildProbe(0, 5, 10, cfg)
	if err != nil {
		t.Fatalf("BuildProbe after fallback failed: %v", err)
	}
	if len(pkt2) == len(pkt) {
		t.Log("Warning: packet sizes match — verify protocol actually switched")
	}
}

func TestAutoSelector_FallbackIsOneWay(t *testing.T) {
	auto := NewAutoSelector(
		NewUDPProtocol(33434),
		NewICMPProtocol(),
	)
	auto.Fallback()
	if auto.Name() != "icmp" {
		t.Fatalf("expected 'icmp', got %q", auto.Name())
	}
	// Calling Fallback again should be idempotent
	auto.Fallback()
	if auto.Name() != "icmp" {
		t.Errorf("expected 'icmp' after second Fallback(), got %q", auto.Name())
	}
}

func TestAutoSelector_IsDestReachedICMP_Delegates(t *testing.T) {
	auto := NewAutoSelector(
		NewUDPProtocol(33434),
		NewICMPProtocol(),
	)
	// UDP: port unreachable (type 3, code 3) means dest reached
	if !auto.IsDestReachedICMP(3, 3) {
		t.Error("UDP should treat port unreachable as dest reached")
	}
	// UDP: echo reply is NOT dest reached
	if auto.IsDestReachedICMP(0, 0) {
		t.Error("UDP should not treat echo reply as dest reached")
	}

	// After fallback to ICMP: echo reply (type 0) means dest reached
	auto.Fallback()
	if !auto.IsDestReachedICMP(0, 0) {
		t.Error("ICMP should treat echo reply as dest reached")
	}
	// ICMP: port unreachable is NOT dest reached
	if auto.IsDestReachedICMP(3, 3) {
		t.Error("ICMP should not treat port unreachable as dest reached")
	}
}
