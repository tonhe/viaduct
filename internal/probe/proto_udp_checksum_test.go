package probe

import (
	"encoding/binary"
	"net"
	"testing"
)

// onesComplementSum is an independent RFC 1071 sum for the tests: it folds
// the 16-bit words of b and returns the folded sum (not complemented). A
// packet with a correct checksum in place sums to 0xFFFF together with its
// pseudo-header.
func onesComplementSum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return uint16(sum)
}

// udp6PseudoHeader builds the IPv6 pseudo-header for a UDP datagram of the
// given length (RFC 8200 section 8.1).
func udp6PseudoHeader(src, dst net.IP, length int) []byte {
	psh := make([]byte, 40)
	copy(psh[0:16], src.To16())
	copy(psh[16:32], dst.To16())
	binary.BigEndian.PutUint32(psh[32:36], uint32(length))
	psh[39] = 17
	return psh
}

// IPv6 requires a UDP checksum; a receiver discards a datagram whose checksum
// is zero. The lab showed every IPv6 UDP probe being dropped by the
// destination as a checksum error (viaduct#54).
func TestUDPProtocol_BuildProbe_IPv6HasValidChecksum(t *testing.T) {
	src := net.ParseIP("2001:db8:0:1::1")
	dst := net.ParseIP("2001:db8:100::1")
	p := NewUDPProtocol(33434)

	for _, tc := range []struct {
		name          string
		flow, ttl     int
		payload       int
		basePort      int
	}{
		{"even payload", 0, 1, 64, 44000},
		{"odd payload", 3, 7, 33, 44000},
		{"other flow and ttl", 5, 30, 64, 50123},
		{"empty payload", 0, 1, 0, 44000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.IPVersion = 6
			cfg.SourceIP = src
			cfg.TargetIP = dst
			cfg.PayloadSize = tc.payload
			cfg.BasePort = tc.basePort

			pkt, _, err := p.BuildProbe(tc.flow, tc.ttl, 1, cfg)
			if err != nil {
				t.Fatalf("BuildProbe: %v", err)
			}
			if got := binary.BigEndian.Uint16(pkt[6:8]); got == 0 {
				t.Fatalf("UDP checksum is zero; IPv6 requires a checksum")
			}
			whole := append(udp6PseudoHeader(src, dst, len(pkt)), pkt...)
			if got := onesComplementSum(whole); got != 0xffff {
				t.Errorf("pseudo-header + datagram sums to %#04x, want 0xffff (checksum is wrong)", got)
			}
		})
	}
}

// A checksum that depends on the addresses must change when they do.
func TestUDPProtocol_BuildProbe_IPv6ChecksumCoversAddresses(t *testing.T) {
	p := NewUDPProtocol(33434)
	build := func(dst string) uint16 {
		cfg := DefaultConfig()
		cfg.IPVersion = 6
		cfg.SourceIP = net.ParseIP("2001:db8:0:1::1")
		cfg.TargetIP = net.ParseIP(dst)
		pkt, _, err := p.BuildProbe(0, 1, 1, cfg)
		if err != nil {
			t.Fatalf("BuildProbe: %v", err)
		}
		return binary.BigEndian.Uint16(pkt[6:8])
	}
	if a, b := build("2001:db8:100::1"), build("2001:db8:200::1"); a == b {
		t.Errorf("checksum %#04x is the same for two destinations; it must cover the pseudo-header", a)
	}
}

// udp4PseudoHeader builds the IPv4 pseudo-header for a UDP datagram of the
// given length (RFC 768).
func udp4PseudoHeader(src, dst net.IP, length int) []byte {
	psh := make([]byte, 12)
	copy(psh[0:4], src.To4())
	copy(psh[4:8], dst.To4())
	psh[9] = 17
	binary.BigEndian.PutUint16(psh[10:12], uint16(length))
	return psh
}

// A zero UDP checksum is legal in IPv4 but unusual; middleboxes that validate
// or normalise UDP may drop such probes, which shows as false loss. IPv4
// probes carry a real checksum too (viaduct#61).
func TestUDPProtocol_BuildProbe_IPv4HasValidChecksum(t *testing.T) {
	src := net.ParseIP("192.0.2.1")
	dst := net.ParseIP("198.51.100.1")
	p := NewUDPProtocol(33434)

	for _, tc := range []struct {
		name      string
		flow, ttl int
		payload   int
	}{
		{"even payload", 0, 1, 64},
		{"odd payload", 3, 7, 33},
		{"empty payload", 0, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.IPVersion = 4
			cfg.SourceIP = src
			cfg.TargetIP = dst
			cfg.PayloadSize = tc.payload

			pkt, _, err := p.BuildProbe(tc.flow, tc.ttl, 1, cfg)
			if err != nil {
				t.Fatalf("BuildProbe: %v", err)
			}
			if got := binary.BigEndian.Uint16(pkt[6:8]); got == 0 {
				t.Fatalf("UDP checksum is zero")
			}
			whole := append(udp4PseudoHeader(src, dst, len(pkt)), pkt...)
			if got := onesComplementSum(whole); got != 0xffff {
				t.Errorf("pseudo-header + datagram sums to %#04x, want 0xffff (checksum is wrong)", got)
			}
		})
	}
}

// The IPv4 checksum helper must never return zero either: zero on the wire
// means "no checksum" (RFC 768).
func TestUDPChecksumV4_NeverZero(t *testing.T) {
	src := net.ParseIP("192.0.2.1")
	dst := net.ParseIP("198.51.100.1")
	found := false
	for sp := 0; sp < 65536 && !found; sp++ {
		d := make([]byte, 8)
		binary.BigEndian.PutUint16(d[0:2], uint16(sp))
		binary.BigEndian.PutUint16(d[4:6], 8)
		whole := append(udp4PseudoHeader(src, dst, len(d)), d...)
		if onesComplementSum(whole) == 0xffff {
			found = true
			if got := udpChecksumV4(src, dst, d); got != 0xffff {
				t.Errorf("checksum for the zero case = %#04x, want 0xffff", got)
			}
		}
	}
	if !found {
		t.Fatal("no zero-checksum datagram found in the search space; test needs a different vector")
	}
}

// Without both addresses the checksum cannot be computed. The probe is still
// built, as before, so callers that never set SourceIP keep working.
func TestUDPProtocol_BuildProbe_IPv6WithoutAddresses(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IPVersion = 6
	pkt, _, err := NewUDPProtocol(33434).BuildProbe(0, 1, 1, cfg)
	if err != nil {
		t.Fatalf("BuildProbe: %v", err)
	}
	if len(pkt) != 8+cfg.PayloadSize {
		t.Errorf("packet length = %d, want %d", len(pkt), 8+cfg.PayloadSize)
	}
}

// The checksum helper must never return zero: zero on the wire means "no
// checksum", so a computed zero is sent as 0xFFFF (RFC 768).
func TestUDPChecksumV6_NeverZero(t *testing.T) {
	src := net.ParseIP("2001:db8::1")
	dst := net.ParseIP("2001:db8::2")
	// Search a small space of port pairs for a datagram whose checksum would
	// compute to zero, and confirm it is reported as 0xFFFF.
	found := false
	for sp := 0; sp < 65536 && !found; sp++ {
		d := make([]byte, 8)
		binary.BigEndian.PutUint16(d[0:2], uint16(sp))
		binary.BigEndian.PutUint16(d[4:6], 8)
		whole := append(udp6PseudoHeader(src, dst, len(d)), d...)
		if onesComplementSum(whole) == 0xffff { // complement would be 0
			found = true
			if got := udpChecksumV6(src, dst, d); got != 0xffff {
				t.Errorf("checksum for the zero case = %#04x, want 0xffff", got)
			}
		}
	}
	if !found {
		t.Fatal("no zero-checksum datagram found in the search space; test needs a different vector")
	}
}
