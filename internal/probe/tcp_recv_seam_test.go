package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The TCP receive seam (viaduct#172): a tracer in TCP mode, run through its
// send loop, with the place the TCP listener reads from supplied by the test.
//
// On macOS the raw TCP socket delivers nothing. A tracer that reads only that
// socket never sees the destination; one that reads a packet capture of the
// interface does.

// synSender is a probe socket that tells the test about every TCP SYN written
// with a hop limit that reaches the destination.
type synSender struct {
	mu      sync.Mutex
	ttl     int
	destTTL int
	onSYN   func(syn []byte)
	syns    int
}

func (s *synSender) SetTTL(ttl int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ttl = ttl
	return nil
}

func (s *synSender) WriteTo(b []byte, _ net.Addr) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(b) == 20 && b[13] == 0x02 && s.ttl >= s.destTTL {
		s.syns++
		s.onSYN(append([]byte(nil), b...))
	}
	return len(b), nil
}

// chanCaptureDevice is a BPF device fed by the test: each buffer sent on ch
// is what one read returns.
type chanCaptureDevice struct {
	ch     chan []byte
	closed chan struct{}
	once   sync.Once
}

func newChanCaptureDevice() *chanCaptureDevice {
	return &chanCaptureDevice{ch: make(chan []byte, 256), closed: make(chan struct{})}
}

func (d *chanCaptureDevice) Read(b []byte) (int, error) {
	select {
	case <-d.closed:
		return 0, net.ErrClosed
	default:
	}
	select {
	case rec := <-d.ch:
		return copy(b, rec), nil
	case <-d.closed:
		return 0, net.ErrClosed
	case <-time.After(10 * time.Millisecond):
		return 0, nil // the device's read timeout
	}
}

func (d *chanCaptureDevice) Close() error {
	d.once.Do(func() { close(d.closed) })
	return nil
}

// silentSource is a raw TCP socket on macOS: reads only ever time out.
type silentSource struct {
	reads atomic.Int64
}

func (s *silentSource) ReadFrom([]byte) (int, net.Addr, error) {
	s.reads.Add(1)
	time.Sleep(10 * time.Millisecond)
	return 0, nil, &net.OpError{Op: "read", Err: context.DeadlineExceeded}
}
func (s *silentSource) SetReadDeadline(time.Time) error { return nil }

// seamFamilies are the families and link types the seam tests run in.
var seamFamilies = []struct {
	name    string
	version int
	source  string
	target  string
	other   string
	link    int
}{
	{"IPv4 over Ethernet", 4, capLocal4, capTarget4, capOther4, linkEthernet},
	{"IPv6 over Ethernet", 6, capLocal6, capTarget6, capOther6, linkEthernet},
	{"IPv4 over a tunnel (no link header)", 4, capLocal4, capTarget4, capOther4, linkRaw},
	{"IPv6 over loopback framing", 6, capLocal6, capTarget6, capOther6, linkNull},
}

// synAckFrame is the frame a capture shows when from answers the SYN: link
// header, IP header and a SYN-ACK that acknowledges the SYN's sequence
// number, in a BPF record.
func synAckFrame(link, version int, from, to string, syn []byte) []byte {
	reply := buildFakeTCPPacket(int(binary.BigEndian.Uint16(syn[2:4])), int(binary.BigEndian.Uint16(syn[0:2])), 0x12)
	binary.BigEndian.PutUint32(reply[8:12], binary.BigEndian.Uint32(syn[4:8])+1)
	var pkt []byte
	if version == 6 {
		pkt = ip6Packet(from, to, ipProtoTCP, reply)
	} else {
		pkt = ip4Packet(from, to, ipProtoTCP, 0, reply)
	}
	return bpfRecord(linkFrame(link, pkt), 18)
}

// seamRun runs a TCP trace of a few rounds whose destination is at hop 3 and
// returns the results that name the destination.
func seamRun(t *testing.T, version int, source, target string, snd *synSender, open func(net.PacketConn) (tcpSegmentSource, func(), error)) (dest []Result, tr *Tracer) {
	t.Helper()
	cfg, tgt := stallCfg(version, source, target, func(Config) ProbeProtocol { return NewTCPProtocol(443) })
	cfg.MaxHops = 5
	cfg.MaxRounds = 3
	cfg.RoundDelay = 30 * time.Millisecond
	tr = stallTracer(cfg, tgt, snd)
	tr.newTCPSource = open

	results := make(chan Result, 1024)
	done := make(chan error, 1)
	go func() { done <- tr.run(context.Background(), nil, results) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not finish")
	}
	close(results)
	for r := range results {
		if r.IsTarget {
			dest = append(dest, r)
		}
	}
	return dest, tr
}

// The test this issue turns on. The raw TCP socket delivers nothing, as on
// macOS; the capture of the interface holds the target's SYN-ACK, framed as
// BPF delivers it. The destination must be reported, at its hop, as the
// target, and the send loop must stop probing past it.
func TestTCPSeam_DestinationSeenThroughTheCapture(t *testing.T) {
	for _, fam := range seamFamilies {
		t.Run(fam.name, func(t *testing.T) {
			dev := newChanCaptureDevice()
			snd := &synSender{destTTL: 3}
			snd.onSYN = func(syn []byte) {
				dev.ch <- synAckFrame(fam.link, fam.version, fam.target, fam.source, syn)
			}
			var closed atomic.Bool
			var gotRawConn net.PacketConn = &senderConn{}
			open := func(rawConn net.PacketConn) (tcpSegmentSource, func(), error) {
				gotRawConn = rawConn
				src := newCaptureSource(dev, fam.link, fam.version, 4096)
				return src, func() { closed.Store(true); src.Close() }, nil
			}

			dest, tr := seamRun(t, fam.version, fam.source, fam.target, snd, open)

			if len(dest) == 0 {
				t.Fatalf("the destination was never reported: %d SYNs reached it and each was answered in the capture", snd.syns)
			}
			atHop := 0
			for _, r := range dest {
				// In the first round the probes beyond hop 3 are already on
				// their way when the first answer arrives; they are answered
				// too.
				if r.TTL < 3 || !r.IP.Equal(net.ParseIP(fam.target)) || r.ProbeType != ProbeTCPTTL {
					t.Errorf("destination result = %+v, want the target at TTL 3 or beyond by a TCP probe", r)
				}
				if r.TTL == 3 {
					atHop++
				}
			}
			if atHop == 0 {
				t.Errorf("no destination result at TTL 3 among %d", len(dest))
			}
			if got := atomic.LoadInt32(&tr.targetTTL); got != 3 {
				t.Errorf("targetTTL = %d, want 3: the send loop must stop at the destination's hop", got)
			}
			if got, want := tr.tcpRecv.matched.Load(), uint64(len(dest)); got != want {
				t.Errorf("matched counter = %d, want %d (one per destination result)", got, want)
			}
			if !closed.Load() {
				t.Error("the capture was not closed when the run ended")
			}
			if gotRawConn != nil {
				t.Errorf("the source was opened with raw socket %v; a run through newSender has none", gotRawConn)
			}
		})
	}
}

// The control: the same trace reading only a socket that delivers nothing,
// which is all the raw TCP socket does on macOS, never reports the
// destination. This is what TCP mode did there before the capture.
func TestTCPSeam_SilentRawSocketNeverSeesTheDestination(t *testing.T) {
	fam := seamFamilies[1]
	snd := &synSender{destTTL: 3, onSYN: func([]byte) {}}
	src := &silentSource{}
	dest, tr := seamRun(t, fam.version, fam.source, fam.target, snd,
		func(net.PacketConn) (tcpSegmentSource, func(), error) { return src, nil, nil })
	if len(dest) != 0 {
		t.Fatalf("destination reported %d times from a socket that delivered nothing", len(dest))
	}
	if got := atomic.LoadInt32(&tr.targetTTL); got != 0 {
		t.Errorf("targetTTL = %d, want 0", got)
	}
	if src.reads.Load() == 0 {
		t.Error("the listener never read its source")
	}
}

// A SYN-ACK in the capture that fits a probe but was sent by another address
// is not the destination's answer.
func TestTCPSeam_SegmentFromAnotherAddressIsNotMatched(t *testing.T) {
	for _, fam := range seamFamilies[:2] {
		t.Run(fam.name, func(t *testing.T) {
			dev := newChanCaptureDevice()
			snd := &synSender{destTTL: 3}
			snd.onSYN = func(syn []byte) {
				dev.ch <- synAckFrame(fam.link, fam.version, fam.other, fam.source, syn)
			}
			open := func(net.PacketConn) (tcpSegmentSource, func(), error) {
				src := newCaptureSource(dev, fam.link, fam.version, 4096)
				return src, func() { src.Close() }, nil
			}
			dest, tr := seamRun(t, fam.version, fam.source, fam.target, snd, open)
			if len(dest) != 0 {
				t.Fatalf("a SYN-ACK from %s was reported as the destination %s: %+v", fam.other, fam.target, dest[0])
			}
			if read, fromTarget := tr.tcpRecv.segments.Load(), tr.tcpRecv.fromTarget.Load(); read == 0 || fromTarget != 0 {
				t.Errorf("counters: %d segments read, %d from the target; want some read and none from the target", read, fromTarget)
			}
		})
	}
}

// When the reply source cannot be opened the trace stops with that error. It
// does not run with the destination shown as unreachable.
func TestTCPSeam_SourceThatCannotBeOpenedStopsTheTrace(t *testing.T) {
	fam := seamFamilies[0]
	cfg, tgt := stallCfg(fam.version, fam.source, fam.target, func(Config) ProbeProtocol { return NewTCPProtocol(443) })
	cfg.MaxRounds = 2
	snd := &synSender{destTTL: 3, onSYN: func([]byte) {}}
	tr := stallTracer(cfg, tgt, snd)
	cleaned := false
	tr.newTCPSource = func(net.PacketConn) (tcpSegmentSource, func(), error) {
		return nil, func() { cleaned = true }, &tcpCaptureError{reason: "unsupported link type 127 on interface en0"}
	}
	err := tr.run(context.Background(), nil, make(chan Result, 16))
	var capErr *tcpCaptureError
	if !errors.As(err, &capErr) {
		t.Fatalf("run returned %v, want the capture error", err)
	}
	for _, want := range []string{"unsupported link type 127 on interface en0", "-P udp", "-P icmp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if !cleaned {
		t.Error("what the failed open left behind was not cleaned up")
	}
	snd.mu.Lock()
	defer snd.mu.Unlock()
	if snd.syns != 0 {
		t.Errorf("%d SYNs were sent before the trace stopped, want 0", snd.syns)
	}
}

// Modes other than TCP never open a TCP reply source.
func TestTCPSeam_OnlyTCPModeOpensTheSource(t *testing.T) {
	fam := seamFamilies[0]
	cfg, tgt := stallCfg(fam.version, fam.source, fam.target, func(Config) ProbeProtocol { return NewUDPProtocol(33434) })
	cfg.MaxRounds = 1
	tr := stallTracer(cfg, tgt, &synSender{destTTL: 3, onSYN: func([]byte) {}})
	opened := false
	tr.newTCPSource = func(net.PacketConn) (tcpSegmentSource, func(), error) {
		opened = true
		return &silentSource{}, nil, nil
	}
	runFor(t, tr, 5*time.Second)
	if opened {
		t.Error("UDP mode opened a TCP reply source")
	}
}

// The listener's totals reach the debug log, once per round, when there is
// one.
func TestTCPSeam_DebugLogHasReceiveTotals(t *testing.T) {
	fam := seamFamilies[1]
	dev := newChanCaptureDevice()
	snd := &synSender{destTTL: 3}
	snd.onSYN = func(syn []byte) {
		dev.ch <- synAckFrame(fam.link, fam.version, fam.target, fam.source, syn)
	}
	cfg, tgt := stallCfg(fam.version, fam.source, fam.target, func(Config) ProbeProtocol { return NewTCPProtocol(443) })
	cfg.MaxHops = 5
	cfg.MaxRounds = 3
	cfg.RoundDelay = 30 * time.Millisecond
	var mu sync.Mutex
	var lines []string
	cfg.Debugf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
	}
	tr := stallTracer(cfg, tgt, snd)
	tr.newTCPSource = func(net.PacketConn) (tcpSegmentSource, func(), error) {
		src := newCaptureSource(dev, fam.link, fam.version, 4096)
		return src, func() { src.Close() }, nil
	}
	runFor(t, tr, 10*time.Second)

	mu.Lock()
	defer mu.Unlock()
	var totals []string
	for _, l := range lines {
		if strings.Contains(l, "tcp receive totals:") {
			totals = append(totals, l)
		}
	}
	if len(totals) != 3 {
		t.Fatalf("%d receive-total lines for 3 rounds, want 3:\n%s", len(totals), strings.Join(lines, "\n"))
	}
	last := totals[len(totals)-1]
	if !strings.Contains(last, "round 3:") || strings.Contains(last, " 0 matched") {
		t.Errorf("last totals line = %q, want round 3 with matches", last)
	}
}

func TestTCPReplyPorts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BasePort = 44000
	cfg.NumPaths = 6
	cfg.Protocol = NewTCPProtocol(443)
	if lo, hi := NewTracer(net.ParseIP(capTarget4), cfg).tcpReplyPorts(); lo != 44000 || hi != 44005 {
		t.Errorf("ports = %d-%d, want 44000-44005", lo, hi)
	}
	cfg.NumPaths = 0
	if lo, hi := NewTracer(net.ParseIP(capTarget4), cfg).tcpReplyPorts(); lo != 44000 || hi != 44000 {
		t.Errorf("ports with no paths set = %d-%d, want 44000-44000", lo, hi)
	}
}
