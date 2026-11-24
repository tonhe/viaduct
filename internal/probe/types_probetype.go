package probe

// ProbeType identifies which tier of the interleaved round produced a probe.
type ProbeType int

const (
	ProbeICMPTTL ProbeType = iota
	ProbeUDPTTL
	ProbeTCPTTL
	ProbeICMPEcho
)

func (pt ProbeType) String() string {
	switch pt {
	case ProbeICMPTTL:
		return "icmp-ttl"
	case ProbeUDPTTL:
		return "udp-ttl"
	case ProbeTCPTTL:
		return "tcp-ttl"
	case ProbeICMPEcho:
		return "icmp-echo"
	default:
		return "unknown"
	}
}

// ProbeTypeForProtocolName maps a primary protocol name to its TTL probe type.
func ProbeTypeForProtocolName(name string) ProbeType {
	switch name {
	case "udp":
		return ProbeUDPTTL
	case "tcp":
		return ProbeTCPTTL
	default:
		return ProbeICMPTTL
	}
}
