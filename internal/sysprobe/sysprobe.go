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

// HasIPv6Transport reports whether this host has any non-loopback IPv6 address.
// Returns false only when the kernel lacks IPv6 or no interface is configured
// with a v6 address — not when the caller lacks CAP_NET_RAW.
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
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		is4 := ipnet.IP.To4() != nil
		if v == 4 && is4 {
			return true
		}
		if v == 6 && !is4 {
			return true
		}
	}
	return false
}
