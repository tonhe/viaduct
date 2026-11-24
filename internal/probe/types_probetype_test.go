package probe

import "testing"

func TestProbeTypeString(t *testing.T) {
	cases := []struct {
		pt   ProbeType
		want string
	}{
		{ProbeICMPTTL, "icmp-ttl"},
		{ProbeUDPTTL, "udp-ttl"},
		{ProbeTCPTTL, "tcp-ttl"},
		{ProbeICMPEcho, "icmp-echo"},
		{ProbeType(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.pt.String(); got != c.want {
			t.Errorf("ProbeType(%d).String() = %q, want %q", c.pt, got, c.want)
		}
	}
}

func TestProbeTypeForProtocolName(t *testing.T) {
	cases := []struct {
		name string
		want ProbeType
	}{
		{"udp", ProbeUDPTTL},
		{"tcp", ProbeTCPTTL},
		{"icmp", ProbeICMPTTL},
	}
	for _, c := range cases {
		if got := ProbeTypeForProtocolName(c.name); got != c.want {
			t.Errorf("ProbeTypeForProtocolName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
