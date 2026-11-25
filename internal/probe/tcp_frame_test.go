package probe

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/bpf"
)

// Frames as a BPF device on macOS delivers them, built by hand: link header,
// IP header, TCP segment, and the BPF record header in front.

// tcpReplySegment is a 20-byte SYN-ACK from port 443 to dstPort that
// acknowledges ack.
func tcpReplySegment(dstPort int, ack uint32) []byte {
	seg := buildFakeTCPPacket(443, dstPort, 0x12)
	binary.BigEndian.PutUint32(seg[8:12], ack)
	return seg
}

// ip4Packet wraps payload in an IPv4 header with optLen bytes of options.
func ip4Packet(src, dst string, proto byte, optLen int, payload []byte) []byte {
	ihl := 20 + optLen
	p := make([]byte, ihl+len(payload))
	p[0] = 0x40 | byte(ihl/4)
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	p[8] = 64
	p[9] = proto
	copy(p[12:16], net.ParseIP(src).To4())
	copy(p[16:20], net.ParseIP(dst).To4())
	copy(p[ihl:], payload)
	return p
}

// ip6Packet wraps payload in an IPv6 header with the given next-header value.
func ip6Packet(src, dst string, next byte, payload []byte) []byte {
	p := make([]byte, 40+len(payload))
	p[0] = 0x60
	binary.BigEndian.PutUint16(p[4:6], uint16(len(payload)))
	p[6] = next
	p[7] = 64
	copy(p[8:24], net.ParseIP(src).To16())
	copy(p[24:40], net.ParseIP(dst).To16())
	copy(p[40:], payload)
	return p
}

// linkFrame puts the link header of the given type in front of an IP packet.
func linkFrame(link int, pkt []byte) []byte {
	switch link {
	case linkEthernet:
		hdr := make([]byte, 14)
		copy(hdr[0:6], []byte{0x02, 0, 0, 0, 0, 0x01})
		copy(hdr[6:12], []byte{0x02, 0, 0, 0, 0, 0x02})
		etherType := uint16(etherTypeIPv4)
		if pkt[0]>>4 == 6 {
			etherType = etherTypeIPv6
		}
		binary.BigEndian.PutUint16(hdr[12:14], etherType)
		return append(hdr, pkt...)
	case linkNull:
		// The address family, in host order as the loopback interface writes
		// it. The parser does not read it.
		hdr := make([]byte, 4)
		af := uint32(2)
		if pkt[0]>>4 == 6 {
			af = 30
		}
		binary.NativeEndian.PutUint32(hdr, af)
		return append(hdr, pkt...)
	}
	return append([]byte(nil), pkt...)
}

// vlanFrame is an Ethernet frame with an 802.1Q tag before the IP packet.
func vlanFrame(pkt []byte) []byte {
	f := linkFrame(linkEthernet, pkt)
	tagged := append([]byte(nil), f[:12]...)
	tagged = append(tagged, 0x81, 0x00, 0x00, 0x64)
	return append(tagged, f[12:]...)
}

// bpfRecord puts a BPF record header of hdrLen bytes in front of a frame and
// pads the record to the 4-byte boundary the next one starts on.
func bpfRecord(frame []byte, hdrLen int) []byte {
	rec := make([]byte, hdrLen, hdrLen+len(frame)+3)
	binary.NativeEndian.PutUint32(rec[bpfHdrCaplenOff:], uint32(len(frame)))
	binary.NativeEndian.PutUint32(rec[12:], uint32(len(frame)))
	binary.NativeEndian.PutUint16(rec[bpfHdrHdrlenOff:], uint16(hdrLen))
	rec = append(rec, frame...)
	for len(rec)%bpfAlignment != 0 {
		rec = append(rec, 0xee)
	}
	return rec
}

var captureLinks = []struct {
	name string
	link int
}{
	{"ethernet", linkEthernet},
	{"null", linkNull},
	{"raw", linkRaw},
}

const (
	capTarget4 = "192.0.2.1"
	capLocal4  = "198.51.100.10"
	capOther4  = "203.0.113.9"
	capTarget6 = "2001:db8::1"
	capLocal6  = "2001:db8:1::10"
	capOther6  = "2001:db8:2::9"
)

// A SYN-ACK from the target, on every link type and in both families, comes
// out as the TCP segment and the target's address.
func TestTCPSegmentFromFrame_StripsLinkAndIPHeaders(t *testing.T) {
	seg := tcpReplySegment(44000, 102)
	for _, l := range captureLinks {
		for _, fam := range []struct {
			name    string
			version int
			pkt     []byte
			from    string
		}{
			{"v4", 4, ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg), capTarget4},
			{"v4 with IP options", 4, ip4Packet(capTarget4, capLocal4, ipProtoTCP, 8, seg), capTarget4},
			{"v6", 6, ip6Packet(capTarget6, capLocal6, ipProtoTCP, seg), capTarget6},
		} {
			t.Run(l.name+"/"+fam.name, func(t *testing.T) {
				got, from, ok := tcpSegmentFromFrame(l.link, fam.version, linkFrame(l.link, fam.pkt))
				if !ok {
					t.Fatal("frame was not parsed")
				}
				if !bytes.Equal(got, seg) {
					t.Errorf("segment = %x, want %x", got, seg)
				}
				if !from.Equal(net.ParseIP(fam.from)) {
					t.Errorf("sender = %v, want %v", from, fam.from)
				}
				key, isReply := parseTCPResponse(got)
				if !isReply || key.SrcPort != 44000 || key.DstPort != 443 || key.Seq != 101 {
					t.Errorf("parseTCPResponse = %+v, %v; want ports 44000/443, sequence 101", key, isReply)
				}
			})
		}
	}
}

// An Ethernet frame shorter than the minimum is padded on the wire. The
// padding is not part of the segment.
func TestTCPSegmentFromFrame_CutsEthernetPadding(t *testing.T) {
	seg := tcpReplySegment(44000, 102)
	for _, fam := range []struct {
		name    string
		version int
		pkt     []byte
	}{
		{"v4", 4, ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg)},
		{"v6", 6, ip6Packet(capTarget6, capLocal6, ipProtoTCP, seg)},
	} {
		t.Run(fam.name, func(t *testing.T) {
			frame := append(linkFrame(linkEthernet, fam.pkt), 0, 0, 0, 0, 0, 0)
			got, _, ok := tcpSegmentFromFrame(linkEthernet, fam.version, frame)
			if !ok || !bytes.Equal(got, seg) {
				t.Errorf("segment = %x (ok=%v), want %x", got, ok, seg)
			}
		})
	}
}

// Frames that are not a whole, unfragmented TCP packet of the trace's family
// are dropped, never parsed as something they are not.
func TestTCPSegmentFromFrame_Drops(t *testing.T) {
	seg := tcpReplySegment(44000, 102)
	v4 := ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg)
	v6 := ip6Packet(capTarget6, capLocal6, ipProtoTCP, seg)

	laterFragment := append([]byte(nil), v4...)
	binary.BigEndian.PutUint16(laterFragment[6:8], 0x0005) // fragment offset 5
	firstFragment := append([]byte(nil), v4...)
	binary.BigEndian.PutUint16(firstFragment[6:8], 0x2000) // more fragments
	badIHL := append([]byte(nil), v4...)
	badIHL[0] = 0x43 // header length 12
	longIHL := append([]byte(nil), v4...)
	longIHL[0] = 0x4f // header length 60, beyond the packet

	// A hop-by-hop extension header (8 bytes) before TCP.
	ext := append([]byte{ipProtoTCP, 0, 0, 0, 0, 0, 0, 0}, seg...)
	v6ext := ip6Packet(capTarget6, capLocal6, 0, ext)
	// A fragment header before TCP.
	v6frag := ip6Packet(capTarget6, capLocal6, 44, ext)

	cases := []struct {
		name    string
		link    int
		version int
		frame   []byte
	}{
		{"empty", linkEthernet, 4, nil},
		{"ethernet header cut short", linkEthernet, 4, linkFrame(linkEthernet, v4)[:10]},
		{"ethernet header only", linkEthernet, 4, linkFrame(linkEthernet, v4)[:14]},
		{"null header cut short", linkNull, 4, linkFrame(linkNull, v4)[:3]},
		{"unknown link type", 105, 4, linkFrame(linkEthernet, v4)},
		{"vlan-tagged v4", linkEthernet, 4, vlanFrame(v4)},
		{"vlan-tagged v6", linkEthernet, 6, vlanFrame(v6)},
		{"arp", linkEthernet, 4, append(linkFrame(linkEthernet, v4)[:12], 0x08, 0x06, 0, 1, 8, 0, 6, 4)},
		{"v6 frame in a v4 trace", linkEthernet, 4, linkFrame(linkEthernet, v6)},
		{"v4 frame in a v6 trace", linkEthernet, 6, linkFrame(linkEthernet, v4)},
		{"v6 packet in a v4 trace, raw link", linkRaw, 4, v6},
		{"v4 packet in a v6 trace, null link", linkNull, 6, linkFrame(linkNull, v4)},
		{"v4 udp", linkRaw, 4, ip4Packet(capTarget4, capLocal4, 17, 0, seg)},
		{"v4 icmp", linkRaw, 4, ip4Packet(capTarget4, capLocal4, 1, 0, seg)},
		{"v4 later fragment", linkRaw, 4, laterFragment},
		{"v4 first fragment", linkRaw, 4, firstFragment},
		{"v4 header length below 20", linkRaw, 4, badIHL},
		{"v4 header length beyond the packet", linkRaw, 4, longIHL},
		{"v4 header cut short", linkRaw, 4, v4[:19]},
		{"v4 segment cut short", linkRaw, 4, v4[:20+19]},
		{"v6 udp", linkRaw, 6, ip6Packet(capTarget6, capLocal6, 17, seg)},
		{"v6 icmp", linkRaw, 6, ip6Packet(capTarget6, capLocal6, 58, seg)},
		{"v6 hop-by-hop header before tcp", linkRaw, 6, v6ext},
		{"v6 fragment header before tcp", linkRaw, 6, v6frag},
		{"v6 header cut short", linkRaw, 6, v6[:39]},
		{"v6 segment cut short", linkRaw, 6, v6[:40+19]},
		{"ip version 5", linkRaw, 4, append([]byte{0x55}, v4[1:]...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if seg, from, ok := tcpSegmentFromFrame(tc.link, tc.version, tc.frame); ok {
				t.Errorf("frame was parsed as a TCP segment %x from %v; it must be dropped", seg, from)
			}
		})
	}
}

func TestLinkHeaderLen(t *testing.T) {
	for link, want := range map[int]int{linkNull: 4, linkEthernet: 14, linkRaw: 0} {
		if n, ok := linkHeaderLen(link); !ok || n != want {
			t.Errorf("linkHeaderLen(%d) = %d, %v; want %d, true", link, n, ok, want)
		}
	}
	// IEEE 802.11 with radio information, PPP: not guessed at.
	for _, link := range []int{127, 9, 108, -1} {
		if _, ok := linkHeaderLen(link); ok {
			t.Errorf("linkHeaderLen(%d) is known; an unknown link type must be reported", link)
		}
		if name := linkTypeName(link); name == "" {
			t.Errorf("linkTypeName(%d) is empty", link)
		}
	}
}

// One read of a BPF device returns several records, each with its own header
// and padded to a 4-byte boundary. The header's length varies with the link
// type, so it is read from the record.
func TestNextBPFRecord_WalksARead(t *testing.T) {
	frames := [][]byte{
		bytes.Repeat([]byte{0xa1}, 54), // 18+54 = 72: already aligned
		bytes.Repeat([]byte{0xb2}, 61), // padded by 1
		bytes.Repeat([]byte{0xc3}, 1),  // padded by 1 with a 22-byte header
		bytes.Repeat([]byte{0xd4}, 74),
	}
	hdrLens := []int{18, 18, 22, 20}
	var buf []byte
	for i, f := range frames {
		buf = append(buf, bpfRecord(f, hdrLens[i])...)
	}
	rest := buf
	for i, want := range frames {
		var frame []byte
		var ok bool
		frame, rest, ok = nextBPFRecord(rest)
		if !ok {
			t.Fatalf("record %d: not parsed", i)
		}
		if !bytes.Equal(frame, want) {
			t.Fatalf("record %d: frame = %x, want %x", i, frame, want)
		}
	}
	if len(rest) != 0 {
		t.Errorf("%d bytes left after the last record, want 0", len(rest))
	}
}

// The last record of a read is not always padded.
func TestNextBPFRecord_LastRecordWithoutPadding(t *testing.T) {
	want := bytes.Repeat([]byte{0x77}, 61)
	rec := bpfRecord(want, 18)
	rec = rec[:18+61]
	frame, rest, ok := nextBPFRecord(rec)
	if !ok || !bytes.Equal(frame, want) || len(rest) != 0 {
		t.Errorf("frame = %x, rest = %d bytes, ok = %v; want the frame, nothing left", frame, len(rest), ok)
	}
}

func TestNextBPFRecord_RejectsWhatIsNotARecord(t *testing.T) {
	whole := bpfRecord(bytes.Repeat([]byte{1}, 40), 18)

	caplenBeyond := append([]byte(nil), whole...)
	binary.NativeEndian.PutUint32(caplenBeyond[bpfHdrCaplenOff:], 4000)
	caplenHuge := append([]byte(nil), whole...)
	binary.NativeEndian.PutUint32(caplenHuge[bpfHdrCaplenOff:], 0xffffffff)
	hdrlenSmall := append([]byte(nil), whole...)
	binary.NativeEndian.PutUint16(hdrlenSmall[bpfHdrHdrlenOff:], 4)
	hdrlenBeyond := append([]byte(nil), whole...)
	binary.NativeEndian.PutUint16(hdrlenBeyond[bpfHdrHdrlenOff:], 0xffff)

	for name, buf := range map[string][]byte{
		"empty":                        nil,
		"shorter than a header":        whole[:17],
		"captured length beyond read":  caplenBeyond,
		"captured length 4 GiB":        caplenHuge,
		"header length below a header": hdrlenSmall,
		"header length beyond read":    hdrlenBeyond,
		"frame cut short":              whole[:30],
	} {
		t.Run(name, func(t *testing.T) {
			if frame, _, ok := nextBPFRecord(buf); ok {
				t.Errorf("parsed a record with a %d-byte frame; want it rejected", len(frame))
			}
		})
	}
}

// runFilter reports whether the kernel filter accepts a frame, by running the
// assembled program in the x/net/bpf virtual machine.
func runFilter(t *testing.T, prog []bpf.Instruction, frame []byte) bool {
	t.Helper()
	if _, err := bpf.Assemble(prog); err != nil {
		t.Fatalf("the filter does not assemble: %v", err)
	}
	vm, err := bpf.NewVM(prog)
	if err != nil {
		t.Fatalf("the filter is not a valid program: %v", err)
	}
	n, err := vm.Run(frame)
	if err != nil {
		t.Fatalf("running the filter: %v", err)
	}
	return n > 0
}

// The kernel filter accepts the target's TCP segments to our source ports and
// nothing else, on every link type and in both families; and the parser
// agrees with it on every frame.
func TestTCPReplyFilter(t *testing.T) {
	const lo, hi = 44000, 44005
	seg := func(port int) []byte { return tcpReplySegment(port, 102) }

	type frameCase struct {
		name   string
		pkt    []byte
		accept bool
	}
	families := []struct {
		name    string
		version int
		target  string
		cases   []frameCase
	}{
		{"v4", 4, capTarget4, func() []frameCase {
			later := ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg(lo))
			binary.BigEndian.PutUint16(later[6:8], 0x0005)
			first := ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg(lo))
			binary.BigEndian.PutUint16(first[6:8], 0x2000)
			dontFragment := ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg(lo))
			binary.BigEndian.PutUint16(dontFragment[6:8], 0x4000)
			return []frameCase{
				{"from the target, lowest port", ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg(lo)), true},
				{"from the target, highest port", ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg(hi)), true},
				{"from the target, IP options", ip4Packet(capTarget4, capLocal4, ipProtoTCP, 12, seg(lo+1)), true},
				{"from the target, don't-fragment set", dontFragment, true},
				{"to another local address", ip4Packet(capTarget4, capOther4, ipProtoTCP, 0, seg(lo)), true},
				{"from another address", ip4Packet(capOther4, capLocal4, ipProtoTCP, 0, seg(lo)), false},
				{"our own probe going out", ip4Packet(capLocal4, capTarget4, ipProtoTCP, 0, seg(lo)), false},
				{"port below the range", ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg(lo-1)), false},
				{"port above the range", ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg(hi+1)), false},
				{"udp", ip4Packet(capTarget4, capLocal4, 17, 0, seg(lo)), false},
				{"later fragment", later, false},
				{"first fragment", first, false},
				{"ipv6 packet", ip6Packet(capTarget6, capLocal6, ipProtoTCP, seg(lo)), false},
			}
		}()},
		{"v6", 6, capTarget6, func() []frameCase {
			ext := append([]byte{ipProtoTCP, 0, 0, 0, 0, 0, 0, 0}, seg(lo)...)
			lastWord := ip6Packet("2001:db8::2", capLocal6, ipProtoTCP, seg(lo))
			firstWord := ip6Packet("2001:db9::1", capLocal6, ipProtoTCP, seg(lo))
			return []frameCase{
				{"from the target, lowest port", ip6Packet(capTarget6, capLocal6, ipProtoTCP, seg(lo)), true},
				{"from the target, highest port", ip6Packet(capTarget6, capLocal6, ipProtoTCP, seg(hi)), true},
				{"to another local address", ip6Packet(capTarget6, capOther6, ipProtoTCP, seg(lo)), true},
				{"from another address", ip6Packet(capOther6, capLocal6, ipProtoTCP, seg(lo)), false},
				{"from an address that differs in the last word", lastWord, false},
				{"from an address that differs in the first word", firstWord, false},
				{"our own probe going out", ip6Packet(capLocal6, capTarget6, ipProtoTCP, seg(lo)), false},
				{"port below the range", ip6Packet(capTarget6, capLocal6, ipProtoTCP, seg(lo-1)), false},
				{"port above the range", ip6Packet(capTarget6, capLocal6, ipProtoTCP, seg(hi+1)), false},
				{"udp", ip6Packet(capTarget6, capLocal6, 17, seg(lo)), false},
				{"extension header before tcp", ip6Packet(capTarget6, capLocal6, 0, ext), false},
				{"ipv4 packet", ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg(lo)), false},
			}
		}()},
	}
	for _, fam := range families {
		target := net.ParseIP(fam.target)
		for _, l := range captureLinks {
			prog, err := tcpReplyFilter(l.link, fam.version, target, lo, hi)
			if err != nil {
				t.Fatalf("%s/%s: %v", fam.name, l.name, err)
			}
			for _, tc := range fam.cases {
				t.Run(fam.name+"/"+l.name+"/"+tc.name, func(t *testing.T) {
					frame := linkFrame(l.link, tc.pkt)
					if got := runFilter(t, prog, frame); got != tc.accept {
						t.Errorf("filter accepted = %v, want %v", got, tc.accept)
					}
					// What the filter accepts, the parser must deliver as a
					// segment from the target to a port in range. What the
					// filter drops is either dropped by the parser too or
					// fails the listener's own sender and port checks.
					segment, from, ok := tcpSegmentFromFrame(l.link, fam.version, frame)
					if tc.accept {
						if !ok || !from.Equal(target) {
							t.Fatalf("parser: ok=%v from=%v; want a segment from the target", ok, from)
						}
						if port := int(binary.BigEndian.Uint16(segment[2:4])); port < lo || port > hi {
							t.Errorf("parser: destination port %d, outside %d-%d", port, lo, hi)
						}
					}
				})
			}
			t.Run(fam.name+"/"+l.name+"/truncated frames", func(t *testing.T) {
				frame := linkFrame(l.link, fam.cases[0].pkt)
				// Cut anywhere before the TCP destination port: the filter
				// reads past the end and must drop the frame.
				hdr, _ := linkHeaderLen(l.link)
				ipLen := 20
				if fam.version == 6 {
					ipLen = 40
				}
				for n := 0; n < hdr+ipLen+4; n++ {
					if runFilter(t, prog, frame[:n]) {
						t.Fatalf("filter accepted a frame cut to %d bytes", n)
					}
				}
			})
		}
		t.Run(fam.name+"/vlan-tagged", func(t *testing.T) {
			prog, err := tcpReplyFilter(linkEthernet, fam.version, target, lo, hi)
			if err != nil {
				t.Fatal(err)
			}
			if runFilter(t, prog, vlanFrame(fam.cases[0].pkt)) {
				t.Error("filter accepted a VLAN-tagged frame")
			}
		})
	}
}

// The filter hands over only the headers.
func TestTCPReplyFilter_SnapLen(t *testing.T) {
	prog, err := tcpReplyFilter(linkEthernet, 4, net.ParseIP(capTarget4), 44000, 44005)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := bpf.NewVM(prog)
	if err != nil {
		t.Fatal(err)
	}
	big := linkFrame(linkEthernet, ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, append(tcpReplySegment(44000, 1), make([]byte, 1000)...)))
	n, err := vm.Run(big)
	if err != nil || n != captureSnapLen {
		t.Errorf("accepted %d bytes (err %v), want %d", n, err, captureSnapLen)
	}
}

func TestTCPReplyFilter_Errors(t *testing.T) {
	v4, v6 := net.ParseIP(capTarget4), net.ParseIP(capTarget6)
	cases := []struct {
		name    string
		link    int
		version int
		target  net.IP
		lo, hi  int
	}{
		{"unknown link type", 127, 4, v4, 44000, 44005},
		{"v6 target in a v4 trace", linkEthernet, 4, v6, 44000, 44005},
		{"v4 target in a v6 trace", linkEthernet, 6, v4, 44000, 44005},
		{"no target", linkEthernet, 6, nil, 44000, 44005},
		{"unknown IP version", linkEthernet, 5, v4, 44000, 44005},
		{"port range reversed", linkEthernet, 4, v4, 44005, 44000},
		{"port above 65535", linkEthernet, 4, v4, 65530, 65536},
		{"negative port", linkEthernet, 4, v4, -1, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if prog, err := tcpReplyFilter(tc.link, tc.version, tc.target, tc.lo, tc.hi); err == nil {
				t.Errorf("built a %d-instruction filter, want an error", len(prog))
			}
		})
	}
}

func TestTCPReplyFilterText(t *testing.T) {
	v4 := tcpReplyFilterText(4, net.ParseIP(capTarget4), 44000, 44005)
	for _, want := range []string{"IPv4", capTarget4, "44000-44005", "fragments"} {
		if !strings.Contains(v4, want) {
			t.Errorf("filter text %q does not mention %q", v4, want)
		}
	}
	v6 := tcpReplyFilterText(6, net.ParseIP(capTarget6), 44000, 44005)
	for _, want := range []string{"IPv6", capTarget6, "44000-44005", "extension header"} {
		if !strings.Contains(v6, want) {
			t.Errorf("filter text %q does not mention %q", v6, want)
		}
	}
}

func TestPickCaptureInterface(t *testing.T) {
	ip := net.ParseIP
	ifaces := []captureIface{
		{Name: "lo0", Loopback: true, Up: true, Addrs: []net.IP{ip("127.0.0.1"), ip("::1")}},
		{Name: "en0", Up: true, Addrs: []net.IP{ip("198.51.100.10"), ip("2001:db8:1::10"), ip("2001:db8:1::dead")}},
		{Name: "utun3", Up: true, Addrs: []net.IP{ip("203.0.113.77")}},
		{Name: "en5", Up: false},
	}
	cases := []struct {
		name    string
		source  net.IP
		target  net.IP
		want    string
		wantErr string
	}{
		{"v4 source on ethernet", ip("198.51.100.10"), ip(capTarget4), "en0", ""},
		{"v6 source on ethernet", ip("2001:db8:1::10"), ip(capTarget6), "en0", ""},
		{"a second (deprecated) v6 address names the same interface", ip("2001:db8:1::dead"), ip(capTarget6), "en0", ""},
		{"source on a tunnel", ip("203.0.113.77"), ip(capTarget4), "utun3", ""},
		{"loopback source", ip("127.0.0.1"), ip("127.0.0.1"), "lo0", ""},
		{"target is this host's own address", ip("198.51.100.10"), ip("198.51.100.10"), "lo0", ""},
		{"target is another of this host's addresses", ip("198.51.100.10"), ip("203.0.113.77"), "lo0", ""},
		{"no interface has the source", ip("192.0.2.200"), ip(capTarget4), "", "no interface has the source address 192.0.2.200"},
		{"no source address", nil, ip(capTarget4), "", "no source address is known"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickCaptureInterface(ifaces, tc.source, tc.target)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got.Name != tc.want {
				t.Errorf("interface = %q, err = %v; want %q", got.Name, err, tc.want)
			}
		})
	}

	// A local target with no loopback interface up falls back to the
	// source's interface.
	noLoop := []captureIface{{Name: "en0", Up: true, Addrs: []net.IP{ip("198.51.100.10")}}}
	if got, err := pickCaptureInterface(noLoop, ip("198.51.100.10"), ip("198.51.100.10")); err != nil || got.Name != "en0" {
		t.Errorf("without a loopback interface: %q, %v; want en0", got.Name, err)
	}
}

// The error a failed capture setup stops the trace with says why, and names
// the modes that work.
func TestTCPCaptureError(t *testing.T) {
	for _, reason := range []string{
		"no interface has the source address 2001:db8:1::10",
		"unsupported link type 127 on interface en0",
		"no free BPF device (/dev/bpf0 to /dev/bpf255 are all in use)",
	} {
		msg := (&tcpCaptureError{reason: reason}).Error()
		for _, want := range []string{reason, "-P udp", "-P icmp", "macOS"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not contain %q", msg, want)
			}
		}
	}
}

// fakeCaptureDevice is a BPF device whose reads are scripted.
type fakeCaptureDevice struct {
	reads  [][]byte // one entry per read; an empty entry is a read timeout
	err    error    // returned once the reads are used up
	n      int
	closed bool
}

func (d *fakeCaptureDevice) Read(b []byte) (int, error) {
	if d.n >= len(d.reads) {
		if d.err != nil {
			return 0, d.err
		}
		return 0, nil
	}
	r := d.reads[d.n]
	d.n++
	return copy(b, r), nil
}

func (d *fakeCaptureDevice) Close() error { d.closed = true; return nil }

// One read of the device holds several frames. The source delivers the TCP
// segments one at a time, each with its sender, drops what is not TCP, and
// reports a timeout when a read returns nothing.
func TestCaptureSource_DeliversSegmentsOfARead(t *testing.T) {
	first := tcpReplySegment(44000, 102)
	second := tcpReplySegment(44001, 203)
	third := tcpReplySegment(44002, 304)

	var read1 []byte
	read1 = append(read1, bpfRecord(linkFrame(linkEthernet, ip6Packet(capTarget6, capLocal6, ipProtoTCP, first)), 18)...)
	read1 = append(read1, bpfRecord(linkFrame(linkEthernet, ip6Packet(capTarget6, capLocal6, 17, first)), 18)...) // UDP
	read1 = append(read1, bpfRecord(vlanFrame(ip6Packet(capTarget6, capLocal6, ipProtoTCP, first)), 18)...)       // VLAN
	read1 = append(read1, bpfRecord(linkFrame(linkEthernet, ip6Packet(capOther6, capLocal6, ipProtoTCP, second)), 18)...)
	read2 := bpfRecord(linkFrame(linkEthernet, ip6Packet(capTarget6, capLocal6, ipProtoTCP, third)), 18)

	dev := &fakeCaptureDevice{reads: [][]byte{read1, nil, read2}, err: net.ErrClosed}
	src := newCaptureSource(dev, linkEthernet, 6, 4096)
	buf := make([]byte, 1500)

	want := []struct {
		segment []byte
		from    string
	}{{first, capTarget6}, {second, capOther6}}
	for i, w := range want {
		n, from, err := src.ReadFrom(buf)
		if err != nil {
			t.Fatalf("segment %d: %v", i, err)
		}
		if !bytes.Equal(buf[:n], w.segment) {
			t.Errorf("segment %d = %x, want %x", i, buf[:n], w.segment)
		}
		if !peerIP(from).Equal(net.ParseIP(w.from)) {
			t.Errorf("segment %d sender = %v, want %v", i, from, w.from)
		}
	}

	// The second read returned nothing: a timeout, not an end.
	_, _, err := src.ReadFrom(buf)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("after an empty read: err = %v, want a timeout", err)
	}

	n, from, err := src.ReadFrom(buf)
	if err != nil || !bytes.Equal(buf[:n], third) || !peerIP(from).Equal(net.ParseIP(capTarget6)) {
		t.Fatalf("third segment = %x from %v, err %v", buf[:n], from, err)
	}

	// The device is closed: the error passes through.
	if _, _, err := src.ReadFrom(buf); !errors.Is(err, net.ErrClosed) {
		t.Errorf("after close: err = %v, want net.ErrClosed", err)
	}

	if got := src.totals(); got != "2 reads, 5 frames, 3 TCP segments delivered, 2 frames dropped (not a whole TCP packet), 0 reads cut short" {
		t.Errorf("totals = %q", got)
	}
	if err := src.SetReadDeadline(time.Now()); err != nil {
		t.Errorf("SetReadDeadline: %v", err)
	}
	if err := src.Close(); err != nil || !dev.closed {
		t.Errorf("Close: err = %v, device closed = %v", err, dev.closed)
	}
}

// The sender address handed out must not change when the device buffer is
// overwritten by the next read.
func TestCaptureSource_SenderDoesNotAliasTheBuffer(t *testing.T) {
	read1 := bpfRecord(linkFrame(linkRaw, ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, tcpReplySegment(44000, 1))), 18)
	read2 := bpfRecord(linkFrame(linkRaw, ip4Packet(capOther4, capLocal4, ipProtoTCP, 0, tcpReplySegment(44000, 2))), 18)
	src := newCaptureSource(&fakeCaptureDevice{reads: [][]byte{read1, read2}}, linkRaw, 4, 4096)
	buf := make([]byte, 1500)
	_, from1, err := src.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.ReadFrom(buf); err != nil {
		t.Fatal(err)
	}
	if !peerIP(from1).Equal(net.ParseIP(capTarget4)) {
		t.Errorf("the first sender became %v after the next read, want %v", from1, capTarget4)
	}
}

// A read that ends in bytes that are not a record loses those bytes and no
// more: the records before them are delivered, and the next read is parsed.
func TestCaptureSource_MalformedReadIsDropped(t *testing.T) {
	good := tcpReplySegment(44000, 102)
	read1 := bpfRecord(linkFrame(linkNull, ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, good)), 18)
	read1 = append(read1, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10) // not a record
	read2 := bpfRecord(linkFrame(linkNull, ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, good)), 18)
	src := newCaptureSource(&fakeCaptureDevice{reads: [][]byte{read1, read2}}, linkNull, 4, 4096)
	buf := make([]byte, 1500)
	for i := 0; i < 2; i++ {
		n, _, err := src.ReadFrom(buf)
		if err != nil || !bytes.Equal(buf[:n], good) {
			t.Fatalf("segment %d = %x, err %v", i, buf[:n], err)
		}
	}
	if got := src.malformed.Load(); got != 1 {
		t.Errorf("reads cut short = %d, want 1", got)
	}
}

// Whatever bytes arrive, the record walk and the frame parser neither panic
// nor return a segment shorter than a TCP header.
func FuzzCaptureParse(f *testing.F) {
	seg := tcpReplySegment(44000, 102)
	f.Add(bpfRecord(linkFrame(linkEthernet, ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg)), 18), 1, 4)
	f.Add(bpfRecord(linkFrame(linkNull, ip6Packet(capTarget6, capLocal6, ipProtoTCP, seg)), 18), 0, 6)
	f.Add(bpfRecord(vlanFrame(ip4Packet(capTarget4, capLocal4, ipProtoTCP, 0, seg)), 18), 1, 4)
	f.Add([]byte{}, 12, 4)
	f.Fuzz(func(t *testing.T, data []byte, link, version int) {
		rest := data
		for len(rest) > 0 {
			frame, next, ok := nextBPFRecord(rest)
			if !ok {
				break
			}
			if len(next) >= len(rest) {
				t.Fatalf("the walk did not advance: %d bytes left of %d", len(next), len(rest))
			}
			rest = next
			if segment, from, ok := tcpSegmentFromFrame(link, version, frame); ok {
				if len(segment) < 20 {
					t.Fatalf("segment of %d bytes", len(segment))
				}
				if len(from) != net.IPv4len && len(from) != net.IPv6len {
					t.Fatalf("sender of %d bytes", len(from))
				}
				_, _ = parseTCPResponse(segment)
			}
		}
	})
}
