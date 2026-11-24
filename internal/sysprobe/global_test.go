package sysprobe

import (
	"net"
	"testing"
)

func addrs(cidrs ...string) []net.Addr {
	var out []net.Addr
	for _, c := range cidrs {
		ip, ipnet, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, &net.IPNet{IP: ip, Mask: ipnet.Mask})
	}
	return out
}

// A host is IPv6-capable for tracing only if it has an IPv6 address that can
// reach beyond its own link. A link-local address is on every interface with
// IPv6 enabled, whether or not there is any IPv6 connectivity. (viaduct#55)
func TestHasFamilyIn_IPv6NeedsMoreThanLinkLocal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		addrs []net.Addr
		want  bool
	}{
		{"no addresses", nil, false},
		{"loopback only", addrs("::1/128", "127.0.0.1/8"), false},
		{"IPv4 only", addrs("192.0.2.10/24"), false},
		{"link-local only", addrs("fe80::1/64", "192.0.2.10/24"), false},
		{"link-local on two interfaces", addrs("fe80::1/64", "fe80::2/64"), false},
		{"global address", addrs("fe80::1/64", "2001:db8::10/64"), true},
		{"unique local address", addrs("fe80::1/64", "fd00:1234::10/64"), true},
		{"global address only", addrs("2001:db8::10/64"), true},
	} {
		if got := hasFamilyIn(tc.addrs, 6); got != tc.want {
			t.Errorf("%s: hasFamilyIn(v6) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// IPv4 detection is unchanged: any non-loopback IPv4 address counts.
func TestHasFamilyIn_IPv4Unchanged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		addrs []net.Addr
		want  bool
	}{
		{"no addresses", nil, false},
		{"loopback only", addrs("127.0.0.1/8"), false},
		{"private address", addrs("10.0.0.5/8"), true},
		{"link-local IPv4", addrs("169.254.1.1/16"), true},
		{"IPv6 only", addrs("2001:db8::10/64"), false},
	} {
		if got := hasFamilyIn(tc.addrs, 4); got != tc.want {
			t.Errorf("%s: hasFamilyIn(v4) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
