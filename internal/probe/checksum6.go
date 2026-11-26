package probe

import (
	"net"
	"strings"

	"golang.org/x/net/ipv6"
)

// Who computes the checksum of an IPv6 UDP or TCP probe (viaduct#171).
//
// The checksum covers a pseudo-header that holds the packet's source address.
// When the raw send socket is not bound, the kernel picks the source address
// of every packet, and on a host with several global addresses it does not
// always pick the one found at startup (Config.SourceIP). A checksum computed
// here over that address is then wrong, and the probe is dropped on the path.
//
// The send socket is now opened bound to Config.SourceIP (source_bind.go,
// viaduct#234), but is unbound when there is no such address or the bind
// fails, so what follows still applies to every send socket.
//
// So the kernel is asked to fill the checksum (IPV6_CHECKSUM): it sums the
// pseudo-header of the address it really sends from. The probe is built with
// the field zero.
//
// If the platform rejects the option, the send socket is replaced by one bound
// to Config.SourceIP, which makes the kernel send from that address, and the
// probe carries its own checksum over it. The two states are never mixed: the
// field is left zero only on a socket that accepted the option.

// Offsets of the checksum field in the transport header.
const (
	udpChecksumOffset = 6
	tcpChecksumOffset = 16
)

// v6ChecksumOffset returns the IPV6_CHECKSUM offset for a raw send socket of
// the given network ("ip6:udp", "ip6:tcp"). ok is false for every other
// network: IPv4 has no such option.
func v6ChecksumOffset(network string) (offset int, ok bool) {
	proto, isV6 := strings.CutPrefix(network, "ip6:")
	if !isV6 {
		return 0, false
	}
	switch proto {
	case "udp":
		return udpChecksumOffset, true
	case "tcp":
		return tcpChecksumOffset, true
	}
	return 0, false
}

// sendSocketOps are the socket calls prepareSendSocket makes. A struct of
// functions so a test can stand in for the kernel.
type sendSocketOps struct {
	setChecksum func(conn net.PacketConn, offset int) error
	listen      func(network, address string) (net.PacketConn, error)
}

// realSendSocketOps are the calls made in production.
var realSendSocketOps = sendSocketOps{setChecksum: setV6Checksum, listen: net.ListenPacket}

// setV6Checksum sets IPV6_CHECKSUM on a raw IPv6 socket: the kernel writes the
// checksum of every packet sent at the given offset of the payload.
func setV6Checksum(conn net.PacketConn, offset int) error {
	return ipv6.NewPacketConn(conn).SetChecksum(true, offset)
}

// prepareSendSocket settles who computes the checksum of the probes sent on a
// raw UDP or TCP socket just opened on network. It returns the socket to send
// on, which the caller owns and closes, and whether the kernel fills the
// checksum; when it does, the probes must be built with a zero checksum field
// (Config.kernelChecksum).
//
// An IPv4 socket is returned as it is. For an IPv6 socket that rejects the
// option, conn is closed and a socket bound to Config.SourceIP is returned in
// its place. If there is no IPv6 source address or the bind fails, conn is
// kept: the probes carry their own checksum, valid whenever the kernel sends
// from Config.SourceIP.
func (t *Tracer) prepareSendSocket(ops sendSocketOps, network string, conn net.PacketConn) (net.PacketConn, bool) {
	offset, ok := v6ChecksumOffset(network)
	if !ok {
		return conn, false
	}
	err := ops.setChecksum(conn, offset)
	if err == nil {
		t.debugf("%s send socket: IPV6_CHECKSUM set, offset %d; the kernel fills the probe checksum", network, offset)
		return conn, true
	}
	src := t.cfg.SourceIP
	if src == nil || src.To4() != nil {
		t.debugf("%s send socket: IPV6_CHECKSUM (offset %d) rejected: %v; no source address to bind to, the socket stays unbound and probes carry their own checksum",
			network, offset, err)
		return conn, false
	}
	bound, bindErr := ops.listen(network, src.String())
	if bindErr != nil {
		t.debugf("%s send socket: IPV6_CHECKSUM (offset %d) rejected: %v; binding to %s failed: %v; the socket stays unbound and probes carry their own checksum",
			network, offset, err, src, bindErr)
		return conn, false
	}
	conn.Close()
	t.debugf("%s send socket: IPV6_CHECKSUM (offset %d) rejected: %v; send socket bound to %s, probes carry their own checksum",
		network, offset, err, src)
	return bound, false
}
