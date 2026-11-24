package main

import "math/rand"

const maxTargets = 16

const (
	basePortStride = 64 // NumPaths caps at 32; 64 leaves headroom
	maxNumPaths    = 32
	// idSpan is the contiguous ICMP-ID window: 16 TTL-probe IDs + 16 echo IDs.
	idSpan = 2 * maxTargets
)

// Per-process random bases (spec: cross-process collision guard). Set once
// by initPortAllocator before any config is built.
var (
	idBase   int
	portBase int
)

// initPortAllocator draws the per-process random ID and port bases. The port
// window is sized to the actual session count so two concurrent processes
// keep collision odds as low as the session count allows.
//
// ID/port disjointness is required because via's in-process probeMap shares a
// single keyspace across probe tiers: ICMP TTL probes key as
// {SrcPort: sessionID, DstPort: seq, Seq: 0} and UDP probes key as
// {SrcPort: srcPort, DstPort: dstPort, Seq: 0}. Wire-level protocol namespaces
// are separate (ICMP vs UDP), but the demux map is not. If an ICMP sessionID
// equals a UDP source port and the monotonically-increasing seq counter reaches
// the UDP dst-port range (~33434+ttl, reachable after ~124 rounds), probeMap
// entries collide and clobber each other's probeType. The redraw loop below
// guarantees [idBase, idBase+idSpan) never intersects [portBase, portBase+window).
func initPortAllocator(numSessions int) {
	if numSessions < 1 {
		numSessions = 1
	}
	if numSessions > maxTargets {
		numSessions = maxTargets
	}
	window := basePortStride * numSessions
	portBase = 1024 + rand.Intn(65536-window-1024)

	// Draw idBase and redraw until the ID span is disjoint from the port window.
	// Probability of overlap is small (~1.6%), so this loop almost always
	// terminates in one iteration. Cap at 100 attempts then place deterministically.
	portLo := portBase
	portHi := portBase + window - 1
	for i := 0; i < 100; i++ {
		idBase = 1024 + rand.Intn(65536-idSpan-1024)
		idLo := idBase
		idHi := idBase + idSpan - 1
		if idHi < portLo || idLo > portHi {
			return // disjoint
		}
	}
	// Deterministic fallback: place ID span just above the port window, clamped.
	idBase = portHi + 1
	if idBase+idSpan > 65535 {
		idBase = portLo - idSpan
	}
}

// sessionPorts returns the UDP/TCP source-port base, the ICMP TTL-probe ID,
// and the ICMP echo-probe ID for session idx. All three spaces are disjoint
// across all indices (see ports_test.go).
func sessionPorts(idx int) (basePort, icmpID, echoID int) {
	return portBase + idx*basePortStride, idBase + idx, idBase + maxTargets + idx
}

// clampNumPaths bounds n to [1, maxNumPaths]; reports whether clamping occurred.
func clampNumPaths(n int) (int, bool) {
	if n < 1 {
		return 1, true
	}
	if n > maxNumPaths {
		return maxNumPaths, true
	}
	return n, false
}
