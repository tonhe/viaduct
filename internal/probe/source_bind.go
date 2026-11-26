package probe

import (
	"fmt"
	"runtime"
)

// One source address per trace (viaduct#234).
//
// A socket that is not bound to an address has the kernel choose the source
// address of every packet it sends. On a host with several addresses of a
// family (an IPv6 host with temporary addresses has more than one global
// address) the kernel does not always choose the same one: macOS was seen
// sending 3 of 40 echo requests of one trace from another of the host's global
// addresses. To a router that balances load, a packet from another source
// address belongs to another flow, so the probes of one trace were spread over
// several paths.
//
// So every socket that sends probes is bound to the source address found at
// startup (Config.SourceIP): the ICMP probe socket, the echo tier's socket and
// the UDP or TCP send socket, in both families. The kernel then sends from
// that address and no other.
//
// The ICMP probe socket also receives, as does the TCP send socket on Linux.
// A bound raw socket is handed only the packets addressed to its address, and
// every reply to a probe is: Time Exceeded, Destination Unreachable, Echo
// Reply and the target's SYN-ACK or RST are all sent to the probe's source
// address. So the same bind serves both directions, and no per-packet source
// (IPV6_PKTINFO) is needed, which would also have nothing to offer IPv4.
//
// Binding is a preference, not a condition. With no usable source address, or
// if the bind fails, the socket is opened unbound, as all of them were before,
// and the debug log says so. The address is not looked up again during a run:
// if it leaves the host (a temporary address expiring), sends fail and are
// reported as send failures until the trace is restarted.

// bindProbeSockets is false on the platforms whose probe sockets are left
// unbound. Windows: a raw socket bound to an address has not been verified
// there, for sending or for receiving ICMP errors, so it keeps the unbound
// sockets it had.
var bindProbeSockets = runtime.GOOS != "windows"

// sourceBindAddr returns the address the trace's probe sockets are bound to.
// When there is none, addr is empty and reason says why, in the words of the
// debug log.
func (t *Tracer) sourceBindAddr() (addr, reason string) {
	src := t.cfg.SourceIP
	if src == nil || src.IsUnspecified() {
		return "", "no source address was found at startup"
	}
	if !bindProbeSockets {
		return "", fmt.Sprintf("source address %s is not used: probe sockets are left unbound on this platform", src)
	}
	isV4 := src.To4() != nil
	if isV4 != (t.cfg.IPVersion == 4) {
		return "", fmt.Sprintf("the source address found at startup, %s, is not an IPv%d address", src, t.cfg.IPVersion)
	}
	if !isV4 && src.IsLinkLocalUnicast() {
		// An IPv6 link-local address can only be bound together with its
		// interface, which Config.SourceIP does not carry.
		return "", fmt.Sprintf("the source address found at startup, %s, is link-local and cannot be bound without its interface", src)
	}
	return src.String(), ""
}

// bindToSource wraps listen, a function that opens a socket at an address, so
// that the socket is opened bound to the trace's source address. The address
// the returned function is called with is the wildcard, used when the socket
// cannot be bound: no usable source address, or a bind that fails. what names
// the socket in the debug log, which gets one line for every socket opened.
//
// An error is returned only when the unbound socket cannot be opened either,
// and it is that open's error: what the caller reported before sockets were
// bound.
func bindToSource[C any](t *Tracer, what string, listen func(network, address string) (C, error)) func(network, address string) (C, error) {
	return func(network, wildcard string) (C, error) {
		const unbound = "the socket is not bound and the kernel chooses the source address of each probe"
		addr, reason := t.sourceBindAddr()
		if addr != "" {
			conn, err := listen(network, addr)
			if err == nil {
				t.debugf("%s (%s): bound to source address %s", what, network, addr)
				return conn, nil
			}
			reason = fmt.Sprintf("binding to source address %s failed: %v", addr, err)
		}
		conn, err := listen(network, wildcard)
		if err != nil {
			return conn, err
		}
		t.debugf("%s (%s): %s; %s", what, network, reason, unbound)
		return conn, nil
	}
}
