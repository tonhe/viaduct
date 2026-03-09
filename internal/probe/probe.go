// Package probe implements the TTL-limited packet sending engine.
package probe

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// Config holds probe engine settings.
type Config struct {
	MaxHops     int
	FirstTTL    int
	Timeout     time.Duration
	ProbeDelay  time.Duration
	RoundDelay  time.Duration // pause between full probe rounds
	PayloadSize int           // ICMP payload size in bytes
	MaxRounds   int           // 0 = infinite
	NoDNS       bool
	ReportMode  bool
	NumPaths    int           // number of ECMP flow variations (default 6)
	BasePort    int           // starting UDP source port for flow variation
	DestPort    int           // base UDP destination port (+ TTL)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxHops:     30,
		FirstTTL:    1,
		Timeout:     time.Second,
		ProbeDelay:  10 * time.Millisecond,
		RoundDelay:  time.Second,
		PayloadSize: 64,
		NumPaths:    6,
		BasePort:    44000,
		DestPort:    33434,
	}
}

// SentCounter is called by the Tracer each time a probe is dispatched.
type SentCounter interface {
	IncrementSent(ttl int)
}

// Result is emitted for each probe response.
type Result struct {
	TTL      int
	IP       net.IP
	RTT      time.Duration
	IsTarget bool // true if this is the final destination (Echo Reply)
	FlowID   int  // ECMP flow identifier (0-based)
}

// probeKey identifies an outstanding probe by port pair and sequence.
type probeKey struct {
	SrcPort int
	DstPort int
	Seq     int
}

// probeRecord stores metadata for a sent probe.
type probeRecord struct {
	ttl    int
	flowID int
	sentAt time.Time
}

// probeMap tracks outstanding probes.
type probeMap struct {
	mu    sync.Mutex
	items map[probeKey]probeRecord
}

func newProbeMap() *probeMap {
	return &probeMap{items: make(map[probeKey]probeRecord)}
}

func (pm *probeMap) add(key probeKey, ttl int, flowID int, sentAt time.Time) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.items[key] = probeRecord{ttl: ttl, flowID: flowID, sentAt: sentAt}
}

func (pm *probeMap) match(key probeKey) (probeRecord, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	rec, ok := pm.items[key]
	if ok {
		delete(pm.items, key)
	}
	return rec, ok
}

// sweep removes stale entries older than maxAge to prevent memory leaks
// from probes that never received a response.
func (pm *probeMap) sweep(maxAge time.Duration) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	now := time.Now()
	for k, v := range pm.items {
		if now.Sub(v.sentAt) > maxAge {
			delete(pm.items, k)
		}
	}
}

// buildICMPEchoRequest creates a serialized ICMP Echo Request packet.
func buildICMPEchoRequest(id, seq, payloadSize int) ([]byte, error) {
	data := make([]byte, payloadSize)
	copy(data, []byte("VIADUCT"))
	msg := &icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   id,
			Seq:  seq,
			Data: data,
		},
	}
	return msg.Marshal(nil)
}

// buildUDPProbe creates a serialized UDP packet with the given ports and payload size.
func buildUDPProbe(srcPort, dstPort, payloadSize int) ([]byte, error) {
	pkt := make([]byte, 8+payloadSize) // 8-byte UDP header + payload
	// Source port (big-endian)
	pkt[0] = byte(srcPort >> 8)
	pkt[1] = byte(srcPort)
	// Dest port (big-endian)
	pkt[2] = byte(dstPort >> 8)
	pkt[3] = byte(dstPort)
	// Length
	length := 8 + payloadSize
	pkt[4] = byte(length >> 8)
	pkt[5] = byte(length)
	// Checksum (0 = disabled for IPv4 UDP)
	pkt[6] = 0
	pkt[7] = 0
	// Payload
	copy(pkt[8:], []byte("VIADUCT"))
	return pkt, nil
}

// Tracer sends ICMP probes and listens for responses.
type Tracer struct {
	cfg       Config
	target    net.IP
	pm        *probeMap
	targetTTL int32 // atomically set when target is reached; 0 = not yet found
	OnSent     SentCounter
	OnRoundEnd func() // called at end of each probe round
	Paused     int32  // atomic: 1 = paused, 0 = running
}

// NewTracer creates a tracer for the given target.
func NewTracer(target net.IP, cfg Config) *Tracer {
	return &Tracer{
		cfg:    cfg,
		target: target,
		pm:     newProbeMap(),
	}
}

// Run starts sending probes and listening. Results are sent to the results channel.
// If a SentCounter is set, it is called each time a probe is dispatched.
// It runs continuously (like mtr) until the context is cancelled.
// When NumPaths > 1, UDP probes are sent with per-flow source port variation.
// When NumPaths == 1, ICMP Echo probes are used (original behavior).
func (t *Tracer) Run(ctx context.Context, results chan<- Result) error {
	// ICMP listen socket — receives responses to both ICMP and UDP probes
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return fmt.Errorf("raw socket permission denied: %w", err)
	}
	defer conn.Close()

	// Start listener
	go t.listen(ctx, conn, results)

	if t.cfg.NumPaths > 1 {
		return t.runUDP(ctx, conn)
	}
	return t.runICMP(ctx, conn)
}

// runICMP sends ICMP Echo probes (single-path mode).
func (t *Tracer) runICMP(ctx context.Context, conn *icmp.PacketConn) error {
	pconn := conn.IPv4PacketConn()

	seq := 0
	icmpID := (int(time.Now().UnixNano()) >> 16) & 0xffff

	round := 0
	for {
		maxTTL := t.cfg.MaxHops
		if reached := int(atomic.LoadInt32(&t.targetTTL)); reached > 0 {
			maxTTL = reached
		}
		for ttl := t.cfg.FirstTTL; ttl <= maxTTL; ttl++ {
			select {
			case <-ctx.Done():
				return nil
			default:
			}

			if atomic.LoadInt32(&t.Paused) == 1 {
				time.Sleep(t.cfg.ProbeDelay)
				continue
			}

			seq++
			pkt, err := buildICMPEchoRequest(icmpID, seq, t.cfg.PayloadSize)
			if err != nil {
				continue
			}

			key := probeKey{SrcPort: icmpID, DstPort: seq, Seq: 0}
			t.pm.add(key, ttl, 0, time.Now())

			if err := pconn.SetTTL(ttl); err != nil {
				continue
			}

			dst := &net.IPAddr{IP: t.target}
			if _, err := conn.WriteTo(pkt, dst); err != nil {
				continue
			}

			if t.OnSent != nil {
				t.OnSent.IncrementSent(ttl)
			}

			time.Sleep(t.cfg.ProbeDelay)
		}

		if t.OnRoundEnd != nil {
			t.OnRoundEnd()
		}

		// Sweep stale probe entries that never received a response
		t.pm.sweep(2 * t.cfg.Timeout)

		round++
		if t.cfg.MaxRounds > 0 && round >= t.cfg.MaxRounds {
			return nil
		}

		// Pause between rounds
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(t.cfg.RoundDelay):
		}
	}
}

// runUDP sends UDP probes with per-flow source port variation (ECMP multipath mode).
func (t *Tracer) runUDP(ctx context.Context, _ *icmp.PacketConn) error {
	// Raw UDP send socket for TTL control
	udpConn, err := net.ListenPacket("ip4:udp", "0.0.0.0")
	if err != nil {
		return fmt.Errorf("raw UDP socket: %w", err)
	}
	defer udpConn.Close()

	udpPConn := ipv4.NewPacketConn(udpConn)

	dst := &net.IPAddr{IP: t.target}

	round := 0
	for {
		maxTTL := t.cfg.MaxHops
		if reached := int(atomic.LoadInt32(&t.targetTTL)); reached > 0 {
			maxTTL = reached
		}

		for ttl := t.cfg.FirstTTL; ttl <= maxTTL; ttl++ {
			for flow := 0; flow < t.cfg.NumPaths; flow++ {
				select {
				case <-ctx.Done():
					return nil
				default:
				}

				if atomic.LoadInt32(&t.Paused) == 1 {
					time.Sleep(t.cfg.ProbeDelay)
					continue
				}

				srcPort := t.cfg.BasePort + flow
				dstPort := t.cfg.DestPort + ttl

				pkt, err := buildUDPProbe(srcPort, dstPort, t.cfg.PayloadSize)
				if err != nil {
					continue
				}

				key := probeKey{SrcPort: srcPort, DstPort: dstPort, Seq: 0}
				t.pm.add(key, ttl, flow, time.Now())

				if err := udpPConn.SetTTL(ttl); err != nil {
					continue
				}

				if _, err := udpConn.WriteTo(pkt, dst); err != nil {
					continue
				}

				if t.OnSent != nil {
					t.OnSent.IncrementSent(ttl)
				}

				time.Sleep(t.cfg.ProbeDelay)
			}
		}

		if t.OnRoundEnd != nil {
			t.OnRoundEnd()
		}

		// Sweep stale probe entries that never received a response
		t.pm.sweep(2 * t.cfg.Timeout)

		round++
		if t.cfg.MaxRounds > 0 && round >= t.cfg.MaxRounds {
			return nil
		}

		// Pause between rounds
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(t.cfg.RoundDelay):
		}
	}
}

// listen reads ICMP responses and matches them to outstanding probes.
// It handles responses to both ICMP Echo probes and UDP probes:
//   - ICMP Echo Reply: destination reached (ICMP mode)
//   - ICMP Time Exceeded: intermediate hop (both modes)
//   - ICMP Destination Unreachable (Port Unreachable): destination reached (UDP mode)
func (t *Tracer) listen(ctx context.Context, conn *icmp.PacketConn, results chan<- Result) {
	buf := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			continue // timeout or error, loop back and check ctx
		}

		msg, err := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), buf[:n])
		if err != nil {
			continue
		}

		var key probeKey
		var isTarget bool

		switch msg.Type {
		case ipv4.ICMPTypeTimeExceeded:
			body, ok := msg.Body.(*icmp.TimeExceeded)
			if !ok || len(body.Data) < 28 {
				continue
			}
			// Check the protocol field of the embedded IP header (byte 9)
			proto := body.Data[9]
			if proto == 17 {
				// UDP probe response — extract ports from embedded UDP header
				// IP header (20 bytes) + UDP src port (2) + dst port (2)
				srcPort := int(body.Data[20])<<8 | int(body.Data[21])
				dstPort := int(body.Data[22])<<8 | int(body.Data[23])
				key = probeKey{SrcPort: srcPort, DstPort: dstPort, Seq: 0}
			} else {
				// ICMP probe response — parse embedded ICMP echo
				inner, err := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), body.Data[20:])
				if err != nil {
					continue
				}
				echo, ok := inner.Body.(*icmp.Echo)
				if !ok {
					continue
				}
				key = probeKey{SrcPort: echo.ID, DstPort: echo.Seq, Seq: 0}
			}
			isTarget = false

		case ipv4.ICMPTypeDestinationUnreachable:
			// Port Unreachable = destination reached for UDP probes
			body, ok := msg.Body.(*icmp.DstUnreach)
			if !ok || len(body.Data) < 28 {
				continue
			}
			// Extract UDP ports from embedded packet
			proto := body.Data[9]
			if proto != 17 {
				continue // not a UDP response, ignore
			}
			srcPort := int(body.Data[20])<<8 | int(body.Data[21])
			dstPort := int(body.Data[22])<<8 | int(body.Data[23])
			key = probeKey{SrcPort: srcPort, DstPort: dstPort, Seq: 0}
			isTarget = true

		case ipv4.ICMPTypeEchoReply:
			echo, ok := msg.Body.(*icmp.Echo)
			if !ok {
				continue
			}
			key = probeKey{SrcPort: echo.ID, DstPort: echo.Seq, Seq: 0}
			isTarget = true

		default:
			continue
		}

		rec, ok := t.pm.match(key)
		if !ok {
			continue
		}

		// If this is the target, record the TTL so the sender can cap its range
		if isTarget {
			atomic.CompareAndSwapInt32(&t.targetTTL, 0, int32(rec.ttl))
		}

		peerIP := net.ParseIP(peer.String())
		rtt := time.Since(rec.sentAt)

		select {
		case results <- Result{
			TTL:      rec.ttl,
			IP:       peerIP,
			RTT:      rtt,
			IsTarget: isTarget,
			FlowID:   rec.flowID,
		}:
		case <-ctx.Done():
			return
		}
	}
}
