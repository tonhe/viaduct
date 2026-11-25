package main

import "testing"

func TestSessionPortsSpacesDisjoint(t *testing.T) {
	// Wire-level protocol namespaces (ICMP vs UDP) are separate, so a raw
	// overlap between idBase and portBase is harmless at the kernel level.
	// However, via's in-process probeMap shares a single keyspace across probe
	// tiers: if a sessionID equals a session's UDP source port AND the
	// monotonically-increasing seq counter reaches the UDP dst-port range,
	// probeMap entries collide. initPortAllocator now guarantees the entire
	// ICMP ID span [idBase, idBase+idSpan) is disjoint from the port window
	// [portBase, portBase+window). This test verifies both invariants.
	initPortAllocator(maxTargets)

	type span struct{ lo, hi int } // inclusive

	// Namespace 1: UDP/TCP port windows — must be mutually disjoint.
	var portSpans []span
	for idx := 0; idx < maxTargets; idx++ {
		basePort, _, _ := sessionPorts(idx)
		portSpans = append(portSpans, span{basePort, basePort + basePortStride - 1})
	}
	for i := 0; i < len(portSpans); i++ {
		for j := i + 1; j < len(portSpans); j++ {
			if portSpans[i].lo <= portSpans[j].hi && portSpans[j].lo <= portSpans[i].hi {
				t.Errorf("port span %d [%d,%d] overlaps port span %d [%d,%d]",
					i, portSpans[i].lo, portSpans[i].hi, j, portSpans[j].lo, portSpans[j].hi)
			}
		}
	}

	// Namespace 2: ICMP IDs (icmpID and echoID) — must all be mutually distinct.
	var idSpans []span
	for idx := 0; idx < maxTargets; idx++ {
		_, icmpID, echoID := sessionPorts(idx)
		idSpans = append(idSpans, span{icmpID, icmpID}, span{echoID, echoID})
	}
	for i := 0; i < len(idSpans); i++ {
		for j := i + 1; j < len(idSpans); j++ {
			if idSpans[i].lo <= idSpans[j].hi && idSpans[j].lo <= idSpans[i].hi {
				t.Errorf("ICMP ID span %d [%d,%d] overlaps ICMP ID span %d [%d,%d]",
					i, idSpans[i].lo, idSpans[i].hi, j, idSpans[j].lo, idSpans[j].hi)
			}
		}
	}

	// Cross-namespace: ICMP ID span must not intersect the port window.
	// Both are already computed above; re-derive the aggregate window bounds.
	portLo := portBase
	portHi := portBase + basePortStride*maxTargets - 1
	idLo := idBase
	idHi := idBase + idSpan - 1
	if idHi >= portLo && idLo <= portHi {
		t.Errorf("ICMP ID span [%d,%d] intersects port window [%d,%d]",
			idLo, idHi, portLo, portHi)
	}
}

// TestSessionPortsIDWindowDisjointFromPortWindow verifies across many random
// draws that initPortAllocator never places the ICMP ID span inside the port
// window. This is the property that prevents probeMap key collisions between
// ICMP TTL probes (keyed by sessionID) and UDP probes (keyed by srcPort).
func TestSessionPortsIDWindowDisjointFromPortWindow(t *testing.T) {
	for trial := 0; trial < 500; trial++ {
		initPortAllocator(maxTargets)
		portLo := portBase
		portHi := portBase + basePortStride*maxTargets - 1
		for idx := 0; idx < maxTargets; idx++ {
			_, icmpID, echoID := sessionPorts(idx)
			if icmpID >= portLo && icmpID <= portHi {
				t.Fatalf("trial %d session %d: icmpID %d falls inside port window [%d,%d]",
					trial, idx, icmpID, portLo, portHi)
			}
			if echoID >= portLo && echoID <= portHi {
				t.Fatalf("trial %d session %d: echoID %d falls inside port window [%d,%d]",
					trial, idx, echoID, portLo, portHi)
			}
		}
	}
}

func TestSessionPortsBounds(t *testing.T) {
	for trial := 0; trial < 200; trial++ {
		initPortAllocator(maxTargets)
		for idx := 0; idx < maxTargets; idx++ {
			basePort, icmpID, echoID := sessionPorts(idx)
			if basePort < 1024 || basePort+basePortStride-1 > 65535 {
				t.Fatalf("port window [%d,%d] out of range", basePort, basePort+basePortStride-1)
			}
			if icmpID < 1024 || icmpID > 65535 {
				t.Fatalf("icmpID %d out of range", icmpID)
			}
			if echoID < 1024 || echoID > 65535 {
				t.Fatalf("echoID %d out of range", echoID)
			}
		}
	}
}

func TestSessionPortsWindowSizedToSessionCount(t *testing.T) {
	// One session: port window must fit even with the smallest allocation span.
	for trial := 0; trial < 200; trial++ {
		initPortAllocator(1)
		basePort, _, _ := sessionPorts(0)
		if basePort < 1024 || basePort+basePortStride-1 > 65535 {
			t.Fatalf("single-session port window [%d,%d] out of range", basePort, basePort+63)
		}
	}
}

func TestSessionPortsRandomizedAcrossProcSim(t *testing.T) {
	// Two simulated processes must (usually) land on different bases.
	initPortAllocator(1)
	p1, i1, _ := sessionPorts(0)
	same := 0
	for trial := 0; trial < 20; trial++ {
		initPortAllocator(1)
		p2, i2, _ := sessionPorts(0)
		if p1 == p2 && i1 == i2 {
			same++
		}
	}
	if same == 20 {
		t.Fatal("allocator returned identical bases 20/20 times — not randomized")
	}
}

func TestSessionPorts_RelativeStructure(t *testing.T) {
	initPortAllocator(maxTargets)
	for i := 0; i < maxTargets-1; i++ {
		p0, id0, echo0 := sessionPorts(i)
		p1, id1, echo1 := sessionPorts(i + 1)
		if p1 != p0+basePortStride {
			t.Errorf("session %d port %d, session %d port %d: want stride %d", i, p0, i+1, p1, basePortStride)
		}
		if id1 != id0+1 {
			t.Errorf("session %d icmpID %d, session %d icmpID %d: want stride 1", i, id0, i+1, id1)
		}
		if echo1 != echo0+1 {
			t.Errorf("session %d echoID %d, session %d echoID %d: want stride 1", i, echo0, i+1, echo1)
		}
	}
	// echoID(i) == icmpID(i) + maxTargets
	for i := 0; i < maxTargets; i++ {
		_, id, echo := sessionPorts(i)
		if echo != id+maxTargets {
			t.Errorf("session %d: echoID %d != icmpID %d + maxTargets %d", i, echo, id, maxTargets)
		}
	}
}

// TestMaxNumPaths_BelowStride verifies the compile-time-ish invariant:
// maxNumPaths must be strictly less than basePortStride so that flow IDs
// for one session never reach the next session's port block.
func TestMaxNumPaths_BelowStride(t *testing.T) {
	if maxNumPaths >= basePortStride {
		t.Fatalf("maxNumPaths (%d) must be < basePortStride (%d) to prevent port-block overlap", maxNumPaths, basePortStride)
	}
}

// TestClampNumPaths exercises the full input space of the helper.
func TestClampNumPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input       int
		wantOut     int
		wantClamped bool
	}{
		{input: 0, wantOut: 1, wantClamped: true},
		{input: -5, wantOut: 1, wantClamped: true},
		{input: 1, wantOut: 1, wantClamped: false},
		{input: 6, wantOut: 6, wantClamped: false},
		{input: 32, wantOut: 32, wantClamped: false},
		{input: 33, wantOut: 32, wantClamped: true},
		{input: 100, wantOut: 32, wantClamped: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run("", func(t *testing.T) {
			t.Parallel()
			got, clamped := clampNumPaths(tc.input)
			if got != tc.wantOut {
				t.Errorf("clampNumPaths(%d) value = %d, want %d", tc.input, got, tc.wantOut)
			}
			if clamped != tc.wantClamped {
				t.Errorf("clampNumPaths(%d) clamped = %v, want %v", tc.input, clamped, tc.wantClamped)
			}
		})
	}
}
