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
	Protocol          ProbeProtocol // probe protocol (icmp, udp, tcp)
	SourceIP          net.IP        // local source IP (needed for TCP checksum)
	TargetIP          net.IP        // destination IP (needed for TCP checksum)
	OnProtocolSwitch  func(string)  // called when auto mode switches protocol
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
		Protocol:    NewUDPProtocol(33434),
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
	cfg           Config
	target        net.IP
	pm            *probeMap
	targetTTL     int32 // atomically set when target is reached; 0 = not yet found
	responseCount int32 // atomic: tracks responses received by listen()
	OnSent        SentCounter
	OnRoundEnd    func() // called at end of each probe round
	Paused        int32  // atomic: 1 = paused, 0 = running
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
// The protocol is determined by cfg.Protocol (ICMP, UDP, or TCP).
func (t *Tracer) Run(ctx context.Context, results chan<- Result) error {
	// ICMP listen socket — receives responses for all protocol modes
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return fmt.Errorf("raw socket permission denied: %w", err)
	}
	defer conn.Close()

	// Start listener (runs for the lifetime of Run, handles all protocol modes)
	go t.listen(ctx, conn, results)

	for {
		err := t.run(ctx, conn, results)
		if err != nil {
			return err
		}
		// Check if we returned due to context cancellation
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		// If auto-fallback happened, run() returned nil to signal restart.
		// The listen goroutine continues on the same ICMP socket.
		// run() will open new send sockets for the fallback protocol.
		auto, ok := t.cfg.Protocol.(*AutoSelector)
		if !ok || !auto.HasSwitched() {
			return nil // Normal completion
		}
		// Continue loop — run() will re-enter with ICMP send sockets
	}
}

// run is the protocol-agnostic send loop.
func (t *Tracer) run(ctx context.Context, conn *icmp.PacketConn, results chan<- Result) error {
	proto := t.cfg.Protocol

	// Determine send socket based on protocol.
	// For ICMP: send via the ICMP listener socket itself.
	// For UDP/TCP: open a separate raw socket for sending.
	type sender interface {
		SetTTL(ttl int) error
		WriteTo(b []byte, dst net.Addr) (int, error)
	}

	var snd sender
	switch proto.Name() {
	case "icmp":
		pconn := conn.IPv4PacketConn()
		snd = &icmpSender{pconn: pconn, conn: conn}
	default:
		// UDP (and future TCP) use a raw IP socket for the protocol
		rawProto := "ip4:udp"
		if proto.Name() == "tcp" {
			rawProto = "ip4:tcp"
		}
		rawConn, err := net.ListenPacket(rawProto, "0.0.0.0")
		if err != nil {
			return fmt.Errorf("raw %s socket: %w", proto.Name(), err)
		}
		defer rawConn.Close()
		// For TCP, also start a listener for SYN-ACK/RST responses
		if proto.Name() == "tcp" {
			go t.listenTCP(ctx, rawConn, t.pm, results)
		}
		snd = &rawSender{pconn: ipv4.NewPacketConn(rawConn), conn: rawConn}
	}

	dst := &net.IPAddr{IP: t.target}
	seq := 0
	round := 0

	for {
		maxTTL := t.cfg.MaxHops
		if reached := int(atomic.LoadInt32(&t.targetTTL)); reached > 0 {
			maxTTL = reached
		}

		for ttl := t.cfg.FirstTTL; ttl <= maxTTL; ttl++ {
			numPaths := t.cfg.NumPaths
			if !proto.SupportsMultipath() {
				numPaths = 1
			}
			for flow := 0; flow < numPaths; flow++ {
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
				pkt, key, err := proto.BuildProbe(flow, ttl, seq, t.cfg)
				if err != nil {
					continue
				}

				t.pm.add(key, ttl, flow, time.Now())

				if err := snd.SetTTL(ttl); err != nil {
					continue
				}

				if _, err := snd.WriteTo(pkt, dst); err != nil {
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

		// Auto-fallback: after 3 rounds with no responses, switch to ICMP
		if round >= 2 { // 0-indexed, so round 2 = 3rd round
			t.checkAutoFallback()
		}

		// If auto-fallback triggered, return nil to signal Run() to restart
		// with new sockets for the fallback protocol
		if auto, ok := t.cfg.Protocol.(*AutoSelector); ok && auto.HasSwitched() {
			return nil
		}

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

// icmpSender wraps an ICMP PacketConn for the sender interface.
type icmpSender struct {
	pconn *ipv4.PacketConn
	conn  *icmp.PacketConn
}

func (s *icmpSender) SetTTL(ttl int) error             { return s.pconn.SetTTL(ttl) }
func (s *icmpSender) WriteTo(b []byte, dst net.Addr) (int, error) { return s.conn.WriteTo(b, dst) }

// rawSender wraps a raw IP PacketConn for the sender interface.
type rawSender struct {
	pconn *ipv4.PacketConn
	conn  net.PacketConn
}

func (s *rawSender) SetTTL(ttl int) error             { return s.pconn.SetTTL(ttl) }
func (s *rawSender) WriteTo(b []byte, dst net.Addr) (int, error) { return s.conn.WriteTo(b, dst) }

// checkAutoFallback switches from UDP to ICMP if no responses have been received.
func (t *Tracer) checkAutoFallback() {
	auto, ok := t.cfg.Protocol.(*AutoSelector)
	if !ok || auto.HasSwitched() {
		return
	}
	if atomic.LoadInt32(&t.responseCount) > 0 {
		return // got responses, no need to switch
	}
	// No responses — fallback to ICMP
	auto.Fallback()
	atomic.StoreInt32(&t.targetTTL, 0)
	if t.cfg.OnProtocolSwitch != nil {
		t.cfg.OnProtocolSwitch("icmp")
	}
}

// listen reads ICMP responses and matches them to outstanding probes.
// It handles responses for all protocol modes:
//   - ICMP Echo Reply: destination reached (ICMP mode only)
//   - ICMP Time Exceeded: intermediate hop (all modes) — uses Protocol.IdentifyResponse
//   - ICMP Destination Unreachable: destination reached (UDP/TCP) — uses Protocol.IdentifyResponse
func (t *Tracer) listen(ctx context.Context, conn *icmp.PacketConn, results chan<- Result) {
	proto := t.cfg.Protocol
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
			k, err := proto.IdentifyResponse(body.Data)
			if err != nil || k == nil {
				continue
			}
			key = *k
			isTarget = false

		case ipv4.ICMPTypeDestinationUnreachable:
			body, ok := msg.Body.(*icmp.DstUnreach)
			if !ok || len(body.Data) < 28 {
				continue
			}
			k, err := proto.IdentifyResponse(body.Data)
			if err != nil || k == nil {
				continue
			}
			key = *k
			isTarget = proto.IsDestReachedICMP(int(ipv4.ICMPTypeDestinationUnreachable), msg.Code)

		case ipv4.ICMPTypeEchoReply:
			// Echo Reply is only meaningful for ICMP protocol
			if !proto.IsDestReachedICMP(int(ipv4.ICMPTypeEchoReply), 0) {
				continue
			}
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

		atomic.AddInt32(&t.responseCount, 1)

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
