package probe

import (
	"net"
	"testing"
)

// FuzzParseInnerHeader stress-tests the v4/v6 inner-header parser used
// when handling ICMP TimeExceeded responses. Wire input is untrusted,
// so this must not panic on any []byte + version combination.
func FuzzParseInnerHeader(f *testing.F) {
	// Seeds: canonical v4 TCP, canonical v6 UDP, truncated, malformed
	f.Add(4, []byte{0x45, 0x00, 0x00, 0x2c, 0xab, 0xcd, 0x00, 0x00,
		0x40, 0x06, 0x00, 0x00, 10, 0, 0, 1, 93, 184, 216, 34,
		0xc3, 0x50, 0x01, 0xbb, 0, 0, 0, 1, 0, 0, 0, 0})
	f.Add(6, []byte{0x60, 0, 0, 0, 0, 8, 17, 64,
		0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01,
		0x26, 0x06, 0x47, 0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0x68, 0x10, 0x80, 0xf0,
		0xc3, 0x50, 0x01, 0xbb, 0, 8, 0, 0})
	f.Add(4, []byte{0x45})    // truncated
	f.Add(6, []byte{0x60, 0}) // truncated
	f.Add(0, []byte{})        // empty + invalid version
	f.Add(9, []byte{0xff})    // garbage version

	f.Fuzz(func(t *testing.T, v int, payload []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on v=%d payload=%x: %v", v, payload, r)
			}
		}()
		// Constrain v to sensible range so we mostly hit interesting parser paths.
		v = v & 0x0F
		_, _, _, _, _, _ = ParseInnerHeader(v, payload)
	})
}

// FuzzTransportOffset verifies the extension-header chain walker never panics.
func FuzzTransportOffset(f *testing.F) {
	f.Add(4, []byte{0x45, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Add(6, make([]byte, 40))
	f.Add(6, append([]byte{0x60, 0, 0, 0, 0, 0, 0, 0}, make([]byte, 40)...))
	f.Fuzz(func(t *testing.T, v int, p []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on v=%d payload=%x: %v", v, p, r)
			}
		}()
		v = v & 0x0F
		_ = TransportOffset(v, p)
	})
}

// FuzzTCPChecksum verifies the checksum function survives arbitrary
// src/dst/segment inputs — real inputs come from wire and can be anything.
func FuzzTCPChecksum(f *testing.F) {
	// Seed with v4 + v6 canonical + odd/short segments
	f.Add(4, []byte{10, 0, 0, 1}, []byte{93, 184, 216, 34}, []byte{0, 0, 0, 0})
	f.Add(6, make([]byte, 16), make([]byte, 16), []byte{0xff})
	f.Add(4, []byte{}, []byte{}, []byte{})
	f.Fuzz(func(t *testing.T, v int, src, dst, seg []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on v=%d src=%x dst=%x seg=%x: %v", v, src, dst, seg, r)
			}
		}()
		v = v & 0x0F
		// Force src/dst to right length for the version to hit the real code path.
		var sIP, dIP net.IP
		if v == 6 && len(src) >= 16 && len(dst) >= 16 {
			sIP = net.IP(src[:16])
			dIP = net.IP(dst[:16])
		} else if len(src) >= 4 && len(dst) >= 4 {
			sIP = net.IP(src[:4])
			dIP = net.IP(dst[:4])
		} else {
			return // skip - not enough bytes for either family
		}
		_ = TCPChecksum(v, sIP, dIP, seg)
	})
}
