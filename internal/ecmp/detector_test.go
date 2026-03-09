package ecmp

import (
	"net"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/hop"
)

func makeTestTable() *hop.Table {
	t := hop.NewTable(10)

	// TTL 1: single IP (all flows)
	h1 := t.GetOrCreate(1)
	ip1 := net.ParseIP("10.0.0.1")
	h1.AddSample(ip1, 5*time.Millisecond, 0)
	h1.AddSample(ip1, 5*time.Millisecond, 1)
	h1.AddSample(ip1, 5*time.Millisecond, 2)

	// TTL 2: single IP
	h2 := t.GetOrCreate(2)
	ip2 := net.ParseIP("10.0.1.1")
	h2.AddSample(ip2, 10*time.Millisecond, 0)
	h2.AddSample(ip2, 10*time.Millisecond, 1)
	h2.AddSample(ip2, 10*time.Millisecond, 2)

	// TTL 3: DIVERGENCE — two IPs
	h3 := t.GetOrCreate(3)
	h3.AddSample(net.ParseIP("10.0.2.1"), 12*time.Millisecond, 0)
	h3.AddSample(net.ParseIP("10.0.2.1"), 12*time.Millisecond, 1)
	h3.AddSample(net.ParseIP("10.0.2.2"), 15*time.Millisecond, 2)

	// TTL 4: CONVERGENCE — single IP again
	h4 := t.GetOrCreate(4)
	ip4 := net.ParseIP("10.0.3.1")
	h4.AddSample(ip4, 20*time.Millisecond, 0)
	h4.AddSample(ip4, 20*time.Millisecond, 1)
	h4.AddSample(ip4, 20*time.Millisecond, 2)

	return t
}

func TestDetectDivergence(t *testing.T) {
	table := makeTestTable()
	result := Analyze(table)
	if len(result.DivergencePoints) != 1 {
		t.Fatalf("expected 1 divergence, got %d", len(result.DivergencePoints))
	}
	if result.DivergencePoints[0] != 3 {
		t.Fatalf("expected divergence at TTL 3, got %d", result.DivergencePoints[0])
	}
}

func TestDetectConvergence(t *testing.T) {
	table := makeTestTable()
	result := Analyze(table)
	if len(result.ConvergencePoints) != 1 {
		t.Fatalf("expected 1 convergence, got %d", len(result.ConvergencePoints))
	}
	if result.ConvergencePoints[0] != 4 {
		t.Fatalf("expected convergence at TTL 4, got %d", result.ConvergencePoints[0])
	}
}

func TestNoDivergence(t *testing.T) {
	table := hop.NewTable(10)
	h := table.GetOrCreate(1)
	h.AddSample(net.ParseIP("10.0.0.1"), 5*time.Millisecond, 0)
	result := Analyze(table)
	if len(result.DivergencePoints) != 0 {
		t.Fatalf("expected 0 divergence, got %d", len(result.DivergencePoints))
	}
}
