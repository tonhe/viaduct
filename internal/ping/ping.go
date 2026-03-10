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

// Result represents a single ping response.
type Result struct {
	IP   net.IP
	RTT  time.Duration
	Lost bool
}

// Supplementer pings rate-limited hop IPs directly.
type Supplementer struct {
	targets     sync.Map // IP string -> net.IP
	targetCount int32    // atomic count
	maxTargets  int
	interval    time.Duration
	icmpID      int // unique ICMP ID to avoid collision with probe engine
}

// New creates a Supplementer with the given ping interval.
func New(interval time.Duration) *Supplementer {
	return &Supplementer{
		maxTargets: 10,
		interval:   interval,
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

// Run starts the ping loop. Blocks until ctx is cancelled.
func (s *Supplementer) Run(ctx context.Context, results chan<- Result) error {
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return fmt.Errorf("ping socket: %w", err)
	}
	defer conn.Close()

	// Start listener
	go s.listen(ctx, conn, results)

	seq := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.interval):
		}

		seq++
		s.targets.Range(func(key, val any) bool {
			ip := val.(net.IP)
			pkt, err := buildPingPacket(s.icmpID, seq)
			if err != nil {
				return true
			}

			dst := &net.IPAddr{IP: ip}
			conn.SetDeadline(time.Now().Add(time.Second))
			conn.WriteTo(pkt, dst)
			return true
		})
	}
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
