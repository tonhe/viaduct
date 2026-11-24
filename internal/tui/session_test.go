package tui

import (
	"fmt"
	"net"
	"testing"

	"github.com/tonhe/viaduct/internal/probe"
)

func TestNewMulti(t *testing.T) {
	cfg := probe.DefaultConfig()
	cfg.IPVersion = 4
	specs := []TargetSpec{
		{Name: "a.example", IP: net.ParseIP("192.0.2.1"), Cfg: cfg, ProtocolName: "udp"},
		{Name: "b.example", IP: net.ParseIP("192.0.2.2"), Cfg: cfg, ProtocolName: "udp"},
		{Name: "bad.example", Err: fmt.Errorf("no AAAA record")},
	}
	m := NewMulti(specs, "0.0.0-test", true, 5.0, 0, 3, true, nil)
	if len(m.sessions) != 3 {
		t.Fatalf("sessions = %d, want 3", len(m.sessions))
	}
	if m.focusedIdx != 0 {
		t.Fatalf("focusedIdx = %d, want 0", m.focusedIdx)
	}
	if m.sessions[2].startErr == nil {
		t.Fatal("session 2 should carry startErr")
	}
	if m.sessions[0].table == nil || m.sessions[1].table == nil {
		t.Fatal("healthy sessions need tables")
	}
	if m.sessions[2].table == nil {
		t.Fatal("errored session still needs a non-nil table for safe rendering")
	}
}
