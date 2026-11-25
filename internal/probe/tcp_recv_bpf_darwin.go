//go:build darwin

package probe

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// The BPF device the TCP listener reads on macOS (viaduct#172). What is read
// from it is parsed in tcp_frame.go.
//
// Which packages do what, with cgo off:
//
//   - golang.org/x/net/bpf assembles the filter. It cannot open a device.
//   - package syscall opens, reads and closes the device, and makes the
//     ioctls that take a struct: bind to an interface, set the read timeout,
//     install the filter, read the counters. Its BPF helpers are marked
//     deprecated in favour of x/net/bpf, which has no replacement for them;
//     golang.org/x/sys/unix exports no ioctl call on darwin that takes an
//     arbitrary pointer. They are frozen, not removed, and go through the
//     system library's ioctl.
//   - golang.org/x/sys/unix has BIOCSSEESENT, which package syscall lacks.

const (
	// bpfMaxDevices is how many /dev/bpfN are tried. The kernel creates them
	// on demand, up to 256.
	bpfMaxDevices = 256
	// bpfBufferLen is the capture buffer asked for. The kernel's default is
	// 4096 bytes; it caps the request at its own maximum.
	bpfBufferLen = 64 * 1024
	// bpfReadTimeout is how long one read of the device blocks with nothing
	// captured. It bounds how long the listener takes to see a cancelled
	// context, and how long closing the device waits for a read in progress.
	bpfReadTimeout = 100 * time.Millisecond
)

// openTCPSource opens the BPF device the TCP listener reads replies from: on
// the interface that has the startup source address, filtered in the kernel
// to TCP from the target to our source ports. One device per tracer, so a
// multi-target run holds one per target.
//
// Any failure stops the trace with an error that says why and names the
// modes that do work. rawConn, the send socket, is not read on macOS.
func (t *Tracer) openTCPSource(net.PacketConn) (tcpSegmentSource, func(), error) {
	sysIfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, &tcpCaptureError{reason: fmt.Sprintf("the network interfaces could not be listed: %v", err)}
	}
	ifaces := make([]captureIface, 0, len(sysIfaces))
	for _, ifc := range sysIfaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		ci := captureIface{
			Name:     ifc.Name,
			Loopback: ifc.Flags&net.FlagLoopback != 0,
			Up:       ifc.Flags&net.FlagUp != 0,
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				ci.Addrs = append(ci.Addrs, ipn.IP)
			}
		}
		ifaces = append(ifaces, ci)
	}
	ifc, err := pickCaptureInterface(ifaces, t.cfg.SourceIP, t.target)
	if err != nil {
		return nil, nil, &tcpCaptureError{reason: err.Error()}
	}

	fd, path, err := openBPFDevice()
	if err != nil {
		return nil, nil, &tcpCaptureError{reason: err.Error()}
	}
	dev := &bpfDevice{fd: fd}
	fail := func(format string, args ...any) (tcpSegmentSource, func(), error) {
		dev.Close()
		return nil, nil, &tcpCaptureError{reason: fmt.Sprintf(format, args...)}
	}

	// The buffer size can only be set before the device is bound. Failing to
	// enlarge it is not fatal: the default holds a dozen replies.
	if _, err := syscall.SetBpfBuflen(fd, bpfBufferLen); err != nil {
		t.debugf("tcp receive: %s: buffer not enlarged to %d bytes: %v", path, bpfBufferLen, err)
	}
	if err := syscall.SetBpfInterface(fd, ifc.Name); err != nil {
		return fail("%s could not be bound to interface %s: %v", path, ifc.Name, err)
	}
	link, err := syscall.BpfDatalink(fd)
	if err != nil {
		return fail("the link type of interface %s could not be read: %v", ifc.Name, err)
	}
	if _, known := linkHeaderLen(link); !known {
		return fail("unsupported link type %s on interface %s", linkTypeName(link), ifc.Name)
	}
	// Hand over each frame as it arrives, not when the buffer fills.
	if err := syscall.SetBpfImmediate(fd, 1); err != nil {
		return fail("immediate mode could not be set on %s: %v", path, err)
	}
	// A read that finds nothing returns after the timeout, so the reader can
	// stop.
	tv := syscall.NsecToTimeval(bpfReadTimeout.Nanoseconds())
	if err := syscall.SetBpfTimeout(fd, &tv); err != nil {
		return fail("the read timeout could not be set on %s: %v", path, err)
	}
	// Inbound frames only, where that means something. On the loopback
	// interface every frame is tapped on its way out, so there the option
	// would hide the replies. It is an economy: the filter already accepts
	// only frames sent by the target.
	inbound := "no (loopback)"
	if !ifc.Loopback {
		inbound = "yes"
		if err := unix.IoctlSetPointerInt(fd, unix.BIOCSSEESENT, 0); err != nil {
			inbound = fmt.Sprintf("no (%v)", err)
		}
	}
	lo, hi := t.tcpReplyPorts()
	prog, err := tcpReplyFilter(link, t.cfg.IPVersion, t.target, lo, hi)
	if err != nil {
		return fail("the filter could not be built: %v", err)
	}
	raw, err := bpf.Assemble(prog)
	if err != nil {
		return fail("the filter could not be assembled: %v", err)
	}
	insns := make([]syscall.BpfInsn, len(raw))
	for i, r := range raw {
		insns[i] = syscall.BpfInsn{Code: r.Op, Jt: r.Jt, Jf: r.Jf, K: r.K}
	}
	// Installing the filter also empties the buffer of anything captured
	// before it was in place.
	if err := syscall.SetBpf(fd, insns); err != nil {
		return fail("the filter could not be installed on %s: %v", path, err)
	}
	// A read must ask for exactly the buffer's size.
	bufLen, err := syscall.BpfBuflen(fd)
	if err != nil {
		return fail("the buffer size of %s could not be read: %v", path, err)
	}

	t.debugf("tcp receive: BPF device %s on interface %s (source address %s, target %s), link type %s, buffer %d bytes, read timeout %v, inbound only: %s",
		path, ifc.Name, t.cfg.SourceIP, t.target, linkTypeName(link), bufLen, bpfReadTimeout, inbound)
	t.debugf("tcp receive: filter (%d instructions): %s", len(insns), tcpReplyFilterText(t.cfg.IPVersion, t.target, lo, hi))

	src := newCaptureSource(dev, link, t.cfg.IPVersion, bufLen)
	cleanup := func() {
		recv, drop := "unknown", "unknown"
		if st, err := dev.stats(); err == nil {
			recv, drop = fmt.Sprint(st.Recv), fmt.Sprint(st.Drop)
		}
		src.Close()
		t.debugf("tcp receive: BPF device %s closed: %s; kernel counters: %s frames seen on the interface, %s dropped for lack of buffer",
			path, src.totals(), recv, drop)
	}
	return src, cleanup, nil
}

// openBPFDevice opens the first BPF device that is not in use. Each device
// serves one opener; a busy one answers EBUSY.
func openBPFDevice() (fd int, path string, err error) {
	for i := 0; i < bpfMaxDevices; i++ {
		path = fmt.Sprintf("/dev/bpf%d", i)
		fd, err = syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
		if errors.Is(err, syscall.EACCES) {
			// Reading needs only read access.
			fd, err = syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		}
		switch {
		case err == nil:
			return fd, path, nil
		case errors.Is(err, syscall.EINTR):
			i--
		case errors.Is(err, syscall.EBUSY):
			// In use: try the next one.
		case errors.Is(err, syscall.ENOENT):
			// Past the last device the kernel will create.
			if i == 0 {
				return -1, "", errors.New("no free BPF device (/dev/bpf0 does not exist)")
			}
			return -1, "", fmt.Errorf("no free BPF device (/dev/bpf0 to /dev/bpf%d are all in use)", i-1)
		default:
			return -1, "", fmt.Errorf("%s could not be opened: %v", path, err)
		}
	}
	return -1, "", fmt.Errorf("no free BPF device (/dev/bpf0 to /dev/bpf%d are all in use)", bpfMaxDevices-1)
}

// bpfDevice is an open BPF device. It is read with plain blocking reads, each
// bounded by the device's read timeout; the descriptor is not handed to the
// Go runtime's poller.
//
// A read holds mu, and so does the close: the descriptor is never closed
// under a read in progress, which could otherwise end up reading a
// descriptor number the process has reused for something else. Close
// therefore waits for at most one read timeout.
type bpfDevice struct {
	fd      int
	mu      sync.Mutex
	closing atomic.Bool // set first, so the reader does not start another read
	closed  bool        // under mu
}

func (d *bpfDevice) Read(b []byte) (int, error) {
	for {
		if d.closing.Load() {
			return 0, net.ErrClosed
		}
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return 0, net.ErrClosed
		}
		n, err := syscall.Read(d.fd, b)
		d.mu.Unlock()
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("read of the BPF device: %w", err)
		}
		return n, nil
	}
}

// stats reads the kernel's counters for the device.
func (d *bpfDevice) stats() (*syscall.BpfStat, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, net.ErrClosed
	}
	return syscall.BpfStats(d.fd)
}

func (d *bpfDevice) Close() error {
	d.closing.Store(true)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	return syscall.Close(d.fd)
}
