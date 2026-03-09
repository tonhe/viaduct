package resolve

import (
	"net"
	"testing"
)

func TestCacheHitMiss(t *testing.T) {
	r := New(4)

	// Miss
	if _, ok := r.Lookup(net.ParseIP("1.1.1.1")); ok {
		t.Fatal("expected cache miss")
	}

	// Populate cache
	r.cache.Store("1.1.1.1", "one.one.one.one")

	// Hit
	name, ok := r.Lookup(net.ParseIP("1.1.1.1"))
	if !ok {
		t.Fatal("expected cache hit")
	}
	if name != "one.one.one.one" {
		t.Fatalf("expected one.one.one.one, got %s", name)
	}
}

func TestDeduplication(t *testing.T) {
	r := New(4)
	ip := net.ParseIP("8.8.8.8")

	// Submit same IP twice
	submitted1 := r.Submit(ip)
	submitted2 := r.Submit(ip)

	if !submitted1 {
		t.Fatal("first submit should return true")
	}
	if submitted2 {
		t.Fatal("second submit should return false (dedup)")
	}
}
