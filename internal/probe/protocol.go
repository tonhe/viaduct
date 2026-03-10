package probe

// ProbeProtocol abstracts probe construction and response matching.
type ProbeProtocol interface {
	// BuildProbe creates raw packet bytes and a key for matching responses.
	BuildProbe(flowID, ttl, seq int, cfg Config) ([]byte, probeKey, error)

	// IdentifyResponse inspects the inner header from an ICMP TTL Exceeded
	// or Destination Unreachable response. Returns the matching probe key,
	// or nil if this protocol can't match the packet.
	IdentifyResponse(innerHeader []byte) (*probeKey, error)

	// IsDestReachedICMP returns true if this ICMP type/code means the target
	// was reached via the ICMP listener path. TCP always returns false here.
	IsDestReachedICMP(icmpType, icmpCode int) bool

	// SupportsMultipath indicates whether this protocol can vary flows for ECMP.
	SupportsMultipath() bool

	// Name returns "icmp", "udp", or "tcp" for display.
	Name() string
}
