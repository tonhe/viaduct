package main

import (
	"errors"
	"net"
	"testing"

	"github.com/tonhe/viaduct/internal/probe"
	"github.com/tonhe/viaduct/internal/tui"
)

// With -P auto, each target decides for itself whether to fall back from UDP
// to ICMP. The selector used to be built once and shared, so one silent
// target switched every target (viaduct#77).

func autoSpecs(t *testing.T, n int) []tui.TargetSpec {
	t.Helper()
	numPaths := 6
	base := probe.DefaultConfig()
	proto, err := buildProtocol("auto", 0, &numPaths, base)
	if err != nil {
		t.Fatal(err)
	}
	base.Protocol = proto
	specs := make([]tui.TargetSpec, n)
	for i := range specs {
		specs[i] = tui.TargetSpec{Name: "t", IP: net.ParseIP("192.0.2.1"), Cfg: base, ProtocolName: "auto"}
	}
	return specs
}

func TestPerTargetProtocols_AutoSelectorsAreNotShared(t *testing.T) {
	specs := autoSpecs(t, 3)
	specs[1].Err = errors.New("did not resolve")
	if err := perTargetProtocols(specs, "auto", 0, 6); err != nil {
		t.Fatal(err)
	}

	a, ok := specs[0].Cfg.Protocol.(*probe.AutoSelector)
	if !ok {
		t.Fatalf("target 0 protocol is %T, want *probe.AutoSelector", specs[0].Cfg.Protocol)
	}
	c, ok := specs[2].Cfg.Protocol.(*probe.AutoSelector)
	if !ok {
		t.Fatalf("target 2 protocol is %T, want *probe.AutoSelector", specs[2].Cfg.Protocol)
	}
	if a == c {
		t.Fatal("two targets share one AutoSelector")
	}

	// One target gets no replies and falls back. The other stays on UDP.
	a.Fallback()
	if got := a.Name(); got != "icmp" {
		t.Errorf("target 0 after fallback = %q, want icmp", got)
	}
	if c.HasSwitched() || c.Name() != "udp" {
		t.Errorf("target 2 = %q (switched %v) after target 0 fell back; want udp, not switched", c.Name(), c.HasSwitched())
	}
}

func TestPerTargetProtocols_OtherProtocolsUntouched(t *testing.T) {
	numPaths := 6
	base := probe.DefaultConfig()
	proto, _ := buildProtocol("udp", 0, &numPaths, base)
	base.Protocol = proto
	specs := []tui.TargetSpec{{Cfg: base}, {Cfg: base}}
	if err := perTargetProtocols(specs, "udp", 0, 6); err != nil {
		t.Fatal(err)
	}
	if specs[0].Cfg.Protocol != proto || specs[1].Cfg.Protocol != proto {
		t.Error("UDP protocol objects hold no per-target state and should be left as they are")
	}
}
