//go:build !darwin

package probe

import "net"

// openTCPSource returns where the TCP listener reads replies from on this
// platform: the raw TCP socket the probes are sent on. Nothing else is
// opened, so there is nothing to close.
//
// On Linux the kernel delivers a copy of every incoming TCP segment to that
// socket. With IPV6_CHECKSUM set on it (checksum6.go) the kernel also checks
// the checksum of what it delivers and discards a segment that fails.
func (t *Tracer) openTCPSource(rawConn net.PacketConn) (tcpSegmentSource, func(), error) {
	t.debugf("tcp receive: reading replies from the raw TCP send socket")
	return rawConn, nil, nil
}
