// Package sysprobe detects local network transport capabilities.
// It answers "does this host have IPv4 / IPv6 transport" by inspecting
// interface addresses. We deliberately do NOT check whether a raw ICMP
// socket can be opened — that conflates "no transport" with "no
// CAP_NET_RAW" and would break the sudo re-exec flow that expects to
// be reached even when the caller hasn't yet acquired raw-socket
// privileges. Results are cached for the process lifetime.
package sysprobe

import (
	"net"
	"sync"
)

var (
	v4Once sync.Once
	v4OK   bool
	v6Once sync.Once
	v6OK   bool
)

// HasIPv4Transport reports whether this host has any non-loopback IPv4 address.
func HasIPv4Transport() bool {
	v4Once.Do(func() {
		v4OK = hasFamily(4)
	})
	return v4OK
}

// HasIPv6Transport reports whether this host has an IPv6 address that reaches
// beyond its own link (global or unique-local); link-local alone is not
// enough. Returns false when the kernel lacks IPv6 or no interface has such an
// address — not when the caller lacks CAP_NET_RAW.
func HasIPv6Transport() bool {
	v6Once.Do(func() {
		v6OK = hasFamily(6)
	})
	return v6OK
}

func hasFamily(v int) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	return hasFamilyIn(addrs, v)
}

// hasFamilyIn reports whether addrs contains a usable address of family v.
//
// IPv4: any non-loopback address.
//
// IPv6: an address that reaches beyond its own link (global or unique-local).
// A link-local address does not count: every interface with IPv6 enabled has
// one, whether or not the host has any IPv6 connectivity, so counting it made
// hosts with no IPv6 route look IPv6-capable and auto-select IPv6.
func hasFamilyIn(addrs []net.Addr, v int) bool {
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		is4 := ipnet.IP.To4() != nil
		if v == 4 && is4 {
			return true
		}
		if v == 6 && !is4 && ipnet.IP.IsGlobalUnicast() {
			return true
		}
	}
	return false
}
