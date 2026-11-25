package probe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"golang.org/x/net/bpf"
)

// Reading TCP replies from a packet capture (viaduct#172).
//
// On macOS the kernel does not deliver TCP segments to raw sockets, so the
// destination's SYN-ACK and RST are read from a BPF device bound to the
// interface the probes leave by. This file has everything about that which
// does not need the device: the kernel filter, the walk over the records one
// read of the device returns, and the stripping of link and IP headers down
// to a TCP segment and its sender. It has no build tag, so it is tested on
// every platform. The device itself is in tcp_recv_bpf_darwin.go.
//
// What is dropped, by the kernel filter and again by the parser here, and
// never guessed at:
//
//   - frames with a VLAN tag (the EtherType is not IPv4 or IPv6);
//   - IPv4 fragments, first or later;
//   - IPv6 packets with any extension header before TCP;
//   - anything too short to hold the headers it claims.

// Link types of a BPF device (DLT_* in <net/bpf.h>). The values are the same
// on every BSD.
const (
	linkNull     = 0  // DLT_NULL: 4-byte address family (loopback, classic utun)
	linkEthernet = 1  // DLT_EN10MB: 14-byte Ethernet header (Ethernet, Wi-Fi)
	linkRaw      = 12 // DLT_RAW: no link header (newer utun, ipsec)
)

const (
	etherTypeIPv4 = 0x0800
	etherTypeIPv6 = 0x86dd
	ipProtoTCP    = 6

	// captureSnapLen is how much of an accepted frame the kernel hands over:
	// the longest link, IP and TCP headers together are 14+60+60 bytes.
	captureSnapLen = 256
)

// linkHeaderLen returns the length of the link header of a BPF link type. ok
// is false for a type this code does not know: the caller reports it and does
// not guess where the IP header starts.
func linkHeaderLen(link int) (n int, ok bool) {
	switch link {
	case linkNull:
		return 4, true
	case linkEthernet:
		return 14, true
	case linkRaw:
		return 0, true
	}
	return 0, false
}

// linkTypeName names a BPF link type for the debug log and for errors.
func linkTypeName(link int) string {
	switch link {
	case linkNull:
		return "NULL (4-byte address family)"
	case linkEthernet:
		return "EN10MB (Ethernet)"
	case linkRaw:
		return "RAW (no link header)"
	}
	return fmt.Sprintf("%d", link)
}

// tcpSegmentFromFrame strips the link header and the IP header from a
// captured frame. It returns the TCP segment and the address it was sent
// from. ok is false for every frame that is not a whole, unfragmented TCP
// packet of the given IP version directly after the link header.
//
// The returned slices alias frame.
func tcpSegmentFromFrame(link, version int, frame []byte) (segment []byte, from net.IP, ok bool) {
	hdr, known := linkHeaderLen(link)
	if !known || len(frame) < hdr {
		return nil, nil, false
	}
	if link == linkEthernet {
		want := uint16(etherTypeIPv4)
		if version == 6 {
			want = etherTypeIPv6
		}
		// A VLAN-tagged frame has 0x8100 here and is dropped.
		if binary.BigEndian.Uint16(frame[12:14]) != want {
			return nil, nil, false
		}
	}
	// The 4 bytes of a NULL link header are an address family whose value and
	// byte order differ between interfaces. The IP version field says the
	// same thing and is checked for every link type.
	return tcpSegmentFromIP(version, frame[hdr:])
}

// tcpSegmentFromIP is tcpSegmentFromFrame for a packet that starts at the IP
// header.
func tcpSegmentFromIP(version int, pkt []byte) (segment []byte, from net.IP, ok bool) {
	if len(pkt) < 1 || int(pkt[0]>>4) != version {
		return nil, nil, false
	}
	switch version {
	case 4:
		if len(pkt) < 20 {
			return nil, nil, false
		}
		ihl := int(pkt[0]&0x0f) * 4
		if ihl < 20 || len(pkt) < ihl {
			return nil, nil, false
		}
		if pkt[9] != ipProtoTCP {
			return nil, nil, false
		}
		// More-fragments set or a fragment offset: a fragment.
		if binary.BigEndian.Uint16(pkt[6:8])&0x3fff != 0 {
			return nil, nil, false
		}
		// Cut what follows the packet (Ethernet pads short frames). A total
		// length beyond what was captured leaves the capture as it is.
		end := len(pkt)
		if total := int(binary.BigEndian.Uint16(pkt[2:4])); total >= ihl && total < end {
			end = total
		}
		segment, from = pkt[ihl:end], net.IP(pkt[12:16])
	case 6:
		if len(pkt) < 40 {
			return nil, nil, false
		}
		// Next header must be TCP itself: an extension header is not walked.
		if pkt[6] != ipProtoTCP {
			return nil, nil, false
		}
		end := len(pkt)
		if total := 40 + int(binary.BigEndian.Uint16(pkt[4:6])); total < end {
			end = total
		}
		segment, from = pkt[40:end], net.IP(pkt[8:24])
	default:
		return nil, nil, false
	}
	if len(segment) < 20 {
		return nil, nil, false
	}
	return segment, from, true
}

// The header BPF puts before every captured frame (struct bpf_hdr): an 8-byte
// timestamp, the captured length, the original length and the length of this
// header including its padding, all in host byte order.
const (
	bpfHdrCaplenOff = 8
	bpfHdrHdrlenOff = 16
	bpfHdrMinLen    = 18
	bpfAlignment    = 4 // BPF_ALIGNMENT: records start on a 4-byte boundary
)

// nextBPFRecord takes the first record off a buffer read from a BPF device.
// It returns the captured frame and the rest of the buffer, starting at the
// next record. ok is false when the buffer does not hold a whole record; the
// caller drops the rest of the buffer.
func nextBPFRecord(buf []byte) (frame, rest []byte, ok bool) {
	if len(buf) < bpfHdrMinLen {
		return nil, nil, false
	}
	caplen := uint64(binary.NativeEndian.Uint32(buf[bpfHdrCaplenOff:]))
	hdrlen := uint64(binary.NativeEndian.Uint16(buf[bpfHdrHdrlenOff:]))
	if hdrlen < bpfHdrMinLen || hdrlen+caplen > uint64(len(buf)) {
		return nil, nil, false
	}
	end := hdrlen + caplen
	frame = buf[hdrlen:end]
	// BPF_WORDALIGN: the next record starts at the next 4-byte boundary.
	next := (end + bpfAlignment - 1) &^ (bpfAlignment - 1)
	if next < uint64(len(buf)) {
		rest = buf[next:]
	}
	return frame, rest, true
}

// tcpReplyFilter builds the kernel filter of the capture device: it accepts a
// frame only if it is TCP of the given IP version, sent from target, to a
// destination port between loPort and hiPort (the source ports of our
// probes). Our own address is deliberately not part of it: the kernel picks
// the source address of each probe and it is not always the one found at
// startup.
//
// It is the same test tcpSegmentFromFrame makes, moved into the kernel so
// that unrelated traffic is never copied to via.
func tcpReplyFilter(link, version int, target net.IP, loPort, hiPort int) ([]bpf.Instruction, error) {
	l, known := linkHeaderLen(link)
	if !known {
		return nil, fmt.Errorf("link type %s", linkTypeName(link))
	}
	if loPort < 0 || hiPort > 0xffff || loPort > hiPort {
		return nil, fmt.Errorf("port range %d-%d", loPort, hiPort)
	}
	off := uint32(l)
	var f filterBuilder

	if link == linkEthernet {
		etherType := uint32(etherTypeIPv4)
		if version == 6 {
			etherType = etherTypeIPv6
		}
		f.add(bpf.LoadAbsolute{Off: 12, Size: 2})
		f.require(bpf.JumpEqual, etherType)
	}
	// IP version.
	f.add(bpf.LoadAbsolute{Off: off, Size: 1})
	f.add(bpf.ALUOpConstant{Op: bpf.ALUOpShiftRight, Val: 4})
	f.require(bpf.JumpEqual, uint32(version))

	switch version {
	case 4:
		ip := target.To4()
		if ip == nil {
			return nil, fmt.Errorf("target %v is not an IPv4 address", target)
		}
		f.add(bpf.LoadAbsolute{Off: off + 9, Size: 1}) // protocol
		f.require(bpf.JumpEqual, ipProtoTCP)
		f.add(bpf.LoadAbsolute{Off: off + 6, Size: 2}) // flags and fragment offset
		f.reject(bpf.JumpBitsSet, 0x3fff)
		f.add(bpf.LoadAbsolute{Off: off + 12, Size: 4}) // source address
		f.require(bpf.JumpEqual, binary.BigEndian.Uint32(ip))
		f.add(bpf.LoadMemShift{Off: off})              // X = IP header length
		f.add(bpf.LoadIndirect{Off: off + 2, Size: 2}) // TCP destination port
	case 6:
		ip := target.To16()
		if ip == nil || target.To4() != nil {
			return nil, fmt.Errorf("target %v is not an IPv6 address", target)
		}
		f.add(bpf.LoadAbsolute{Off: off + 6, Size: 1}) // next header
		f.require(bpf.JumpEqual, ipProtoTCP)
		for i := 0; i < 16; i += 4 { // source address
			f.add(bpf.LoadAbsolute{Off: off + 8 + uint32(i), Size: 4})
			f.require(bpf.JumpEqual, binary.BigEndian.Uint32(ip[i:]))
		}
		f.add(bpf.LoadAbsolute{Off: off + 40 + 2, Size: 2}) // TCP destination port
	default:
		return nil, fmt.Errorf("IP version %d", version)
	}
	f.require(bpf.JumpGreaterOrEqual, uint32(loPort))
	f.reject(bpf.JumpGreaterThan, uint32(hiPort))
	return f.finish(captureSnapLen), nil
}

// tcpReplyFilterText says in words what tcpReplyFilter accepts.
func tcpReplyFilterText(version int, target net.IP, loPort, hiPort int) string {
	skipped := "fragments are not accepted"
	if version == 6 {
		skipped = "packets with an extension header are not accepted"
	}
	return fmt.Sprintf("TCP over IPv%d, from %s, to local ports %d-%d; %s, VLAN-tagged frames are not accepted; our own address is not checked",
		version, target, loPort, hiPort, skipped)
}

// filterBuilder assembles a filter in which every failed test jumps to one
// "drop the frame" instruction at the end.
type filterBuilder struct {
	ins []bpf.Instruction
	// tests are the indexes of the conditional jumps, and whether the frame
	// is dropped when the condition is true (reject) or false (require).
	tests []filterTest
}

type filterTest struct {
	at         int
	dropOnTrue bool
}

func (f *filterBuilder) add(i bpf.Instruction) { f.ins = append(f.ins, i) }

// require drops the frame unless the accumulator passes the test.
func (f *filterBuilder) require(cond bpf.JumpTest, val uint32) {
	f.tests = append(f.tests, filterTest{at: len(f.ins)})
	f.add(bpf.JumpIf{Cond: cond, Val: val})
}

// reject drops the frame if the accumulator passes the test.
func (f *filterBuilder) reject(cond bpf.JumpTest, val uint32) {
	f.tests = append(f.tests, filterTest{at: len(f.ins), dropOnTrue: true})
	f.add(bpf.JumpIf{Cond: cond, Val: val})
}

// finish appends "accept snapLen bytes" and "drop", and points every test at
// the drop.
func (f *filterBuilder) finish(snapLen uint32) []bpf.Instruction {
	f.add(bpf.RetConstant{Val: snapLen})
	drop := len(f.ins)
	f.add(bpf.RetConstant{Val: 0})
	for _, t := range f.tests {
		j := f.ins[t.at].(bpf.JumpIf)
		skip := uint8(drop - t.at - 1)
		if t.dropOnTrue {
			j.SkipTrue = skip
		} else {
			j.SkipFalse = skip
		}
		f.ins[t.at] = j
	}
	return f.ins
}

// captureIface is what choosing the capture interface needs to know of a
// network interface.
type captureIface struct {
	Name     string
	Loopback bool
	Up       bool
	Addrs    []net.IP
}

// pickCaptureInterface returns the interface replies from target arrive on:
// the one that has the source address found at startup. That address may be a
// deprecated one the kernel no longer sends from; it still names the
// interface. A target that is one of this host's own addresses is reached
// over the loopback interface whichever interface has the address.
func pickCaptureInterface(ifaces []captureIface, source, target net.IP) (captureIface, error) {
	if source == nil {
		return captureIface{}, errors.New("no source address is known")
	}
	owner, found := captureIface{}, false
	targetIsLocal := false
	for _, ifc := range ifaces {
		for _, a := range ifc.Addrs {
			if !found && a.Equal(source) {
				owner, found = ifc, true
			}
			if target != nil && a.Equal(target) {
				targetIsLocal = true
			}
		}
	}
	if targetIsLocal {
		for _, ifc := range ifaces {
			if ifc.Loopback && ifc.Up {
				return ifc, nil
			}
		}
	}
	if !found {
		return captureIface{}, fmt.Errorf("no interface has the source address %s", source)
	}
	return owner, nil
}

// tcpCaptureError is why TCP mode cannot run on a platform that reads TCP
// replies from a packet capture. Without the capture the destination can
// never be seen to answer, and the trace would show it as 100% lost; so the
// trace stops and says why.
type tcpCaptureError struct {
	reason string
}

func (e *tcpCaptureError) Error() string {
	return "TCP mode on macOS reads the destination's replies from a packet capture (BPF), which could not be set up: " +
		e.reason + ". Use -P udp or -P icmp instead"
}

// captureDevice is a BPF device, as far as captureSource uses it.
type captureDevice interface {
	// Read does one read of the device into b, which must be the size of the
	// device's buffer. It returns 0 and no error when the device's read
	// timeout passes with nothing captured, and an error that wraps
	// net.ErrClosed once the device is closed.
	Read(b []byte) (int, error)
	Close() error
}

// errCaptureTimeout is what captureSource.ReadFrom returns when the device's
// read timeout passes with nothing to deliver.
var errCaptureTimeout error = captureTimeout{}

type captureTimeout struct{}

func (captureTimeout) Error() string   { return "packet capture: read timeout" }
func (captureTimeout) Timeout() bool   { return true }
func (captureTimeout) Temporary() bool { return true }

// captureSource reads TCP segments from a packet-capture device: it is the
// TCP listener's source where raw sockets do not receive TCP. One goroutine
// reads it; the counters may be read from any.
type captureSource struct {
	dev     captureDevice
	link    int
	version int
	buf     []byte // one device read
	pending []byte // the records of the last read not yet delivered

	reads     atomic.Uint64 // device reads that returned data
	frames    atomic.Uint64 // records walked
	segments  atomic.Uint64 // records delivered as TCP segments
	dropped   atomic.Uint64 // records that were not a whole TCP packet
	malformed atomic.Uint64 // reads cut short at a record that did not parse
}

// newCaptureSource wraps a device whose buffer is bufLen bytes, bound to an
// interface of the given link type, for a trace over the given IP version.
func newCaptureSource(dev captureDevice, link, version, bufLen int) *captureSource {
	return &captureSource{dev: dev, link: link, version: version, buf: make([]byte, bufLen)}
}

// ReadFrom copies the next captured TCP segment into b and returns its
// sender. It blocks for at most the device's read timeout and then returns an
// error whose Timeout method reports true.
func (c *captureSource) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		for len(c.pending) > 0 {
			frame, rest, ok := nextBPFRecord(c.pending)
			if !ok {
				c.malformed.Add(1)
				c.pending = nil
				break
			}
			c.pending = rest
			c.frames.Add(1)
			segment, from, ok := tcpSegmentFromFrame(c.link, c.version, frame)
			if !ok {
				c.dropped.Add(1)
				continue
			}
			c.segments.Add(1)
			// from aliases the device buffer, which the next read overwrites.
			return copy(b, segment), &net.IPAddr{IP: append(net.IP(nil), from...)}, nil
		}
		n, err := c.dev.Read(c.buf)
		if err != nil {
			return 0, nil, err
		}
		if n == 0 {
			return 0, nil, errCaptureTimeout
		}
		c.reads.Add(1)
		c.pending = c.buf[:n]
	}
}

// SetReadDeadline does nothing: the device has a read timeout of its own, set
// when it was opened.
func (c *captureSource) SetReadDeadline(time.Time) error { return nil }

// Close closes the device. A ReadFrom in progress returns within the device's
// read timeout.
func (c *captureSource) Close() error { return c.dev.Close() }

// totals is the debug-log line of what the capture delivered.
func (c *captureSource) totals() string {
	return fmt.Sprintf("%d reads, %d frames, %d TCP segments delivered, %d frames dropped (not a whole TCP packet), %d reads cut short",
		c.reads.Load(), c.frames.Load(), c.segments.Load(), c.dropped.Load(), c.malformed.Load())
}
