package ping

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// Result represents a single ping event.
// Lost=true means a ping was sent (sent marker); Lost=false means a reply was received.
type Result struct {
	IP   net.IP
	RTT  time.Duration
	Lost bool
}

// Supplementer pings rate-limited hop IPs directly.
// Call Open to start, PingAll each round, and Close when done.
type Supplementer struct {
	targets     sync.Map // IP string -> net.IP
	targetCount int32    // atomic count
	maxTargets  int
	icmpID      int // unique ICMP ID to avoid collision with probe engine
	conn        *icmp.PacketConn
	results     chan<- Result
	seq         int
}

// New creates a Supplementer.
func New() *Supplementer {
	return &Supplementer{
		maxTargets: 10,
		icmpID:     50000 + rand.Intn(10000), // 50000-59999 range
	}
}

// Submit adds an IP to the ping target set. Idempotent. Max 10 targets.
func (s *Supplementer) Submit(ip net.IP) {
	if ip == nil {
		return
	}
	key := ip.String()
	if _, loaded := s.targets.LoadOrStore(key, ip); loaded {
		return // already tracked
	}
	count := atomic.AddInt32(&s.targetCount, 1)
	if count > int32(s.maxTargets) {
		// Over limit — remove this one
		s.targets.Delete(key)
		atomic.AddInt32(&s.targetCount, -1)
	}
}

// Open creates the ICMP socket and starts the reply listener.
func (s *Supplementer) Open(ctx context.Context, results chan<- Result) error {
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return fmt.Errorf("ping socket: %w", err)
	}
	s.conn = conn
	s.results = results
	go s.listen(ctx, conn, results)
	return nil
}

// Close shuts down the ICMP socket.
func (s *Supplementer) Close() {
	if s.conn != nil {
		s.conn.Close()
	}
}

// PingAll sends one ICMP Echo Request to each target.
// For each target pinged, a Result{Lost: true} is emitted as a "sent" marker.
func (s *Supplementer) PingAll() {
	if s.conn == nil {
		return
	}
	s.seq++
	s.targets.Range(func(key, val any) bool {
		ip := val.(net.IP)
		pkt, err := buildPingPacket(s.icmpID, s.seq)
		if err != nil {
			return true
		}

		dst := &net.IPAddr{IP: ip}
		s.conn.WriteTo(pkt, dst)

		// Emit "sent" marker so consumer can increment Sent count
		select {
		case s.results <- Result{IP: ip, Lost: true}:
		default:
		}
		return true
	})
}

// listen reads ICMP Echo Replies and matches them to targets.
func (s *Supplementer) listen(ctx context.Context, conn *icmp.PacketConn, results chan<- Result) {
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
			continue
		}

		msg, err := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), buf[:n])
		if err != nil {
			continue
		}

		if msg.Type != ipv4.ICMPTypeEchoReply {
			continue
		}

		echo, ok := msg.Body.(*icmp.Echo)
		if !ok || echo.ID != s.icmpID {
			continue // not our ping
		}

		peerIP := net.ParseIP(peer.String())
		if peerIP == nil {
			continue
		}

		// Check if this IP is one of our targets
		if _, ok := s.targets.Load(peerIP.String()); !ok {
			continue
		}

		// Extract send timestamp from payload to compute RTT
		rtt := extractRTT(echo.Data)

		select {
		case results <- Result{IP: peerIP, RTT: rtt}:
		case <-ctx.Done():
			return
		}
	}
}

// buildPingPacket creates an ICMP Echo Request with a timestamp payload.
func buildPingPacket(id, seq int) ([]byte, error) {
	// Embed send timestamp in payload for RTT calculation
	now := time.Now().UnixNano()
	data := make([]byte, 16)
	for i := 0; i < 8; i++ {
		data[i] = byte(now >> (56 - 8*i))
	}
	copy(data[8:], []byte("VIAPING\x00"))

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

// extractRTT reads the send timestamp from a ping reply payload.
func extractRTT(data []byte) time.Duration {
	if len(data) < 8 {
		return 0
	}
	var ts int64
	for i := 0; i < 8; i++ {
		ts |= int64(data[i]) << (56 - 8*i)
	}
	sent := time.Unix(0, ts)
	return time.Since(sent)
}
