// Package probe implements the TTL-limited packet sending engine.
package probe

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// icmpPacketConn is the subset of *icmp.PacketConn methods used by
// Tracer.listen. Defining it as an interface lets tests substitute a
// fake driven by test channels without requiring CAP_NET_RAW.
// *icmp.PacketConn satisfies this interface trivially.
type icmpPacketConn interface {
	ReadFrom(b []byte) (int, net.Addr, error)
	WriteTo(b []byte, dst net.Addr) (int, error)
	Close() error
	SetReadDeadline(t time.Time) error
}

// Config holds probe engine settings.
type Config struct {
	MaxHops          int
	FirstTTL         int
	Timeout          time.Duration
	ProbeDelay       time.Duration
	RoundDelay       time.Duration // pause between full probe rounds
	PayloadSize      int           // ICMP payload size in bytes
	MaxRounds        int           // 0 = infinite
	NoDNS            bool
	ReportMode       bool
	NumPaths         int           // number of ECMP flow variations (default 6)
	BasePort         int           // starting UDP source port for flow variation
	ICMPID           int           // ICMP session ID override; 0 = random 1..65534
	EchoID           int           // ICMP echo-probe ID; 0 = derive random (single-tracer library use)
	EchoBudget       int           // max echo probes per round; 0 = 64
	Protocol         ProbeProtocol // probe protocol (icmp, udp, tcp)
	SourceIP         net.IP        // local source IP (needed for TCP checksum)
	TargetIP         net.IP        // destination IP (needed for TCP checksum)
	OnProtocolSwitch func(string)  // called when auto mode switches protocol
	IPVersion        int           // 4 or 6 — required, no default

	// SendTimeout is the write deadline set before every probe send. A write
	// that has not completed by then is a failed send. 0 = defaultSendTimeout.
	SendTimeout time.Duration
	// Debugf, when set, receives the probe engine's debug-log lines: send
	// failures (once per address) and, per round, the probes that did not
	// leave. It is called from the send loop and must be safe to call from
	// several tracers at once.
	Debugf func(format string, args ...any)

	// kernelChecksum is set by the tracer, on its own copy of the config, once
	// the raw IPv6 send socket has IPV6_CHECKSUM: the kernel fills the UDP or
	// TCP checksum and the probe is built with the field zero (checksum6.go).
	kernelChecksum bool
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
		IPVersion:   4,
	}
}

// defaultSendTimeout is how long one probe write may block before it is given
// up as a failed send (Config.SendTimeout overrides it).
const defaultSendTimeout = 100 * time.Millisecond

// RoundMarker is notified once per hop per round when probes are dispatched.
// probes is the number of TTL-limited probes actually written for that TTL
// this round (the ICMP TTL probe plus every primary flow); echo-tier probes
// are reported separately through the echo-sent callback.
type RoundMarker interface {
	MarkRoundSent(ttl int, probes int)
}

// RoundTypeMarker is a RoundMarker that is also told how the probes written
// for a TTL divide by type. When the round marker implements it, the tracer
// calls MarkRoundSentByType in place of MarkRoundSent. The split cannot be
// derived from the total: when the ICMP socket stops accepting writes while
// the UDP socket still does, a round's probes are all primary ones.
type RoundTypeMarker interface {
	MarkRoundSentByType(ttl int, icmpTTL int, primary int)
}

// Result is emitted for each probe response.
type Result struct {
	TTL       int
	IP        net.IP
	RTT       time.Duration
	IsTarget  bool      // true if this is the final destination (Echo Reply)
	FlowID    int       // ECMP flow identifier (0-based)
	ProbeType ProbeType
}

// probeKey identifies an outstanding probe by port pair and sequence.
type probeKey struct {
	SrcPort int
	DstPort int
	Seq     int
}

// probeRecord stores metadata for a sent probe.
type probeRecord struct {
	ttl       int
	flowID    int
	sentAt    time.Time
	probeType ProbeType
	// dst is the address the probe was sent to, for probes answered by an
	// Echo Reply (ICMP TTL probes: the target; echo-tier probes: the hop).
	// The listener accepts an Echo Reply only from this address. nil means
	// unknown: no sender check.
	dst net.IP
}

// probeMap tracks outstanding probes.
type probeMap struct {
	mu    sync.Mutex
	items map[probeKey]probeRecord
}

func newProbeMap() *probeMap {
	return &probeMap{items: make(map[probeKey]probeRecord)}
}

func (pm *probeMap) add(key probeKey, ttl int, flowID int, sentAt time.Time, pt ProbeType) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.items[key] = probeRecord{ttl: ttl, flowID: flowID, sentAt: sentAt, probeType: pt}
}

// addTo is add for a probe whose reply must come from dst (see probeRecord.dst).
func (pm *probeMap) addTo(key probeKey, ttl int, flowID int, sentAt time.Time, pt ProbeType, dst net.IP) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.items[key] = probeRecord{ttl: ttl, flowID: flowID, sentAt: sentAt, probeType: pt, dst: dst}
}

// pending counts the probes that are still unanswered and were sent less than
// maxAge ago: the ones a reply can still arrive for.
func (pm *probeMap) pending(maxAge time.Duration) int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	now := time.Now()
	n := 0
	for _, rec := range pm.items {
		if now.Sub(rec.sentAt) < maxAge {
			n++
		}
	}
	return n
}

func (pm *probeMap) remove(key probeKey) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.items, key)
}

// matchEchoReply is match for an Echo Reply received from the given address.
// When the probe's destination is known, a reply from any other address is
// not a reply to that probe: it is ignored and the record is left in place,
// so a forged reply can neither count as an answer nor use up the record
// before the genuine reply arrives.
func (pm *probeMap) matchEchoReply(key probeKey, from net.IP) (probeRecord, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	rec, ok := pm.items[key]
	if !ok {
		return probeRecord{}, false
	}
	if rec.dst != nil && (from == nil || !rec.dst.Equal(from)) {
		return probeRecord{}, false
	}
	delete(pm.items, key)
	return rec, true
}

// peerIP returns the address of a packet's sender without any IPv6 zone.
func peerIP(peer net.Addr) net.IP {
	if a, ok := peer.(*net.IPAddr); ok {
		return a.IP
	}
	return net.ParseIP(peer.String())
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

// matchICMPError is match for an ICMP error (Time Exceeded, Destination
// Unreachable) that quotes the probe. An error is not an Echo Reply, so it
// never answers an echo-tier probe: that record is left in place and the echo
// times out as lost. Every other record is matched and consumed as by match.
func (pm *probeMap) matchICMPError(key probeKey) (probeRecord, bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	rec, ok := pm.items[key]
	if !ok || rec.probeType == ProbeICMPEcho {
		return probeRecord{}, false
	}
	delete(pm.items, key)
	return rec, true
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

// buildICMPEchoRequest creates a serialized ICMP Echo Request packet for the
// given IP version. v == 4 emits ICMPv4 Echo; v == 6 emits ICMPv6 Echo Request.
func buildICMPEchoRequest(v, id, seq, payloadSize int) ([]byte, error) {
	data := make([]byte, payloadSize)
	copy(data, []byte("VIADUCT"))
	msg := &icmp.Message{
		Type: EchoRequestType(v),
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

// probeSender abstracts a socket that can set the per-packet TTL and
// write raw packets. In production the implementation is icmpSender or
// rawSender; in tests a fake drives the send loop without raw sockets.
type probeSender interface {
	SetTTL(ttl int) error
	WriteTo(b []byte, dst net.Addr) (int, error)
}

// Tracer sends ICMP probes and listens for responses.
type Tracer struct {
	cfg           Config
	target        net.IP
	pm            *probeMap
	targetTTL     int32 // atomically set when target is reached; 0 = not yet found
	responseCount int32 // atomic: tracks responses received by listen()
	OnRound       RoundMarker
	OnRoundEnd    func() // called at end of each probe round
	Paused        int32  // atomic: 1 = paused, 0 = running

	// icmpProto is constructed once at Tracer creation and reused across all
	// listener calls. Constructing it per-packet in identifyAny was wasteful;
	// sharing is safe because IdentifyResponse does not consult sessionID.
	icmpProto *ICMPProtocol

	// listenICMP opens the ICMP listener socket. Tests may replace this
	// with a factory that returns a fakeICMPConn to exercise Tracer.listen
	// without CAP_NET_RAW. The default wraps icmp.ListenPacket.
	listenICMP func(network, address string) (icmpPacketConn, error)

	// newSender builds the probeSender used by the send loop. Tests may
	// replace this to exercise run() logic without raw sockets. When nil,
	// run() uses the production socket path.
	newSender func(ctx context.Context, proto ProbeProtocol, results chan<- Result) (probeSender, func(), error)

	// echo tier state: per-tracer target set for full-TTL ICMP Echo probes
	// dispatched from round 2 onward. Nothing here is shared across sessions.
	echo       echoState
	onEchoSent func(ip net.IP, ttl int)

	// sends is the send loop's record of probes that did not leave (send.go).
	sends sendState

	// newEchoSender opens the echo tier's own socket (see echoSender). Tests
	// may replace it. When nil, the production path opens a raw ICMP socket
	// and a run through newSender has no echo socket: the echo tier then
	// shares the TTL probes' sender.
	newEchoSender func() (probeSender, func(), error)
	// echoSock is the socket the echo tier's sends are accounted to: sockEcho
	// when it has its own, sockICMP when it shares the TTL probes' socket.
	// Only the send-loop goroutine touches it.
	echoSock sendSocket
}

// defaultListenICMP wraps icmp.ListenPacket so its return type satisfies
// the icmpPacketConn interface used by the field above.
func defaultListenICMP(network, address string) (icmpPacketConn, error) {
	return icmp.ListenPacket(network, address)
}

// NewTracer creates a tracer for the given target.
func NewTracer(target net.IP, cfg Config) *Tracer {
	// Echo tier ID fallback: mirror proto_icmp.go so direct library users
	// who never set cfg.EchoID still get a non-zero ID. Chosen here, before
	// any goroutine starts, so nothing writes t.cfg once Run is under way.
	if cfg.EchoID == 0 {
		cfg.EchoID = 1 + rand.Intn(65534)
	}
	return &Tracer{
		cfg:        cfg,
		target:     target,
		pm:         newProbeMap(),
		icmpProto:  NewICMPProtocol(cfg),
		listenICMP: defaultListenICMP,
	}
}

// SetOnRoundSent sets the RoundMarker callback.
func (t *Tracer) SetOnRoundSent(r RoundMarker) { t.OnRound = r }

// SetOnRoundEnd sets the round-end callback.
func (t *Tracer) SetOnRoundEnd(f func()) { t.OnRoundEnd = f }

// Run starts sending probes and listening. Results are sent to the results channel.
// If a SentCounter is set, it is called each time a probe is dispatched.
// It runs continuously (like mtr) until the context is cancelled.
// The protocol is determined by cfg.Protocol (ICMP, UDP, or TCP).
func (t *Tracer) Run(ctx context.Context, results chan<- Result) error {
	// ICMP listen socket — receives responses for all protocol modes
	conn, err := icmp.ListenPacket(ListenerNet(t.cfg.IPVersion), RawListenAddr(t.cfg.IPVersion))
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

	// icmpProto is used for the tier-1 ICMP TTL probe every round, regardless
	// of primary protocol. It shares cfg.ICMPID with the listener demux.
	icmpProto := NewICMPProtocol(t.cfg)
	primaryType := ProbeTypeForProtocolName(proto.Name())

	// icmpSnd sends the tier-1 ICMP TTL probe. In production it uses the
	// ICMP listener socket; tests exercising run() may leave conn nil, in
	// which case tier-1 sends are skipped when the primary is not icmp.
	var icmpSnd probeSender
	if t.newSender != nil {
		// Route tier-1 icmp sends through the same fake sender so tests can
		// observe both tiers via a single seam.
	} else if conn != nil {
		icmpSnd = &icmpSender{conn: conn, ipVersion: t.cfg.IPVersion}
	}

	// Fresh sockets (first entry, or re-entry after auto-fallback): nothing is
	// stalled yet.
	t.sends.oneSocket = false
	t.sends.stalled = [numSendSockets]bool{}
	t.sends.counts = [numProbeTypes]sendCounts{}

	// buildCfg is the config the primary probes are built with.
	buildCfg := t.cfg

	// newSender is nil in production; tests inject a fake to avoid raw sockets.
	var snd probeSender
	if t.newSender != nil {
		var cleanup func()
		var err error
		snd, cleanup, err = t.newSender(ctx, proto, results)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return err
		}
		if icmpSnd == nil {
			icmpSnd = snd
			t.sends.oneSocket = true
		}
	} else {
		// Determine send socket based on protocol.
		// For ICMP: tier 1 already sends via the ICMP listener socket;
		// tier 2 is skipped, so no separate sender is needed.
		// For UDP/TCP: open a separate raw socket for tier 2 sends.
		switch proto.Name() {
		case "icmp":
			// icmpSnd already set above from conn; snd is unused.
		default:
			// UDP (and future TCP) use a raw IP socket for the protocol
			rawProto := TransportListenerNet(t.cfg.IPVersion, "udp")
			if proto.Name() == "tcp" {
				rawProto = TransportListenerNet(t.cfg.IPVersion, "tcp")
			}
			rawConn, err := net.ListenPacket(rawProto, RawListenAddr(t.cfg.IPVersion))
			if err != nil {
				return fmt.Errorf("raw %s socket: %w", proto.Name(), err)
			}
			// IPv6: the kernel fills the probe checksum (checksum6.go).
			rawConn, buildCfg.kernelChecksum = t.prepareSendSocket(realSendSocketOps, rawProto, rawConn)
			defer rawConn.Close()
			// For TCP, also start a listener for SYN-ACK/RST responses
			if proto.Name() == "tcp" {
				go t.listenTCP(ctx, rawConn, t.pm, results)
			}
			snd = &rawSender{conn: rawConn, ipVersion: t.cfg.IPVersion}
		}
	}

	// The echo tier sends on a socket of its own (see echoSender).
	echoSnd, closeEcho := t.echoSender(icmpSnd, t.newSender == nil && conn != nil)
	defer closeEcho()

	dst := &net.IPAddr{IP: t.target}
	seq := 0
	round := 0

	for {
		maxTTL := t.cfg.MaxHops
		if reached := int(atomic.LoadInt32(&t.targetTTL)); reached > 0 {
			maxTTL = reached
		}

		for ttl := t.cfg.FirstTTL; ttl <= maxTTL; ttl++ {
			// Cheap context/pause check per TTL — before tier 1.
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			if atomic.LoadInt32(&t.Paused) == 1 {
				time.Sleep(t.cfg.ProbeDelay)
				continue
			}

			// TTL-limited probes actually written for this TTL this round, by
			// type. Reported to the round marker so loss is computed per probe
			// and a probe that did not leave is neither sent nor lost.
			icmpSent, primarySent := 0, 0

			// Tier 1: one ICMP TTL probe per hop per round.
			if icmpSnd != nil {
				seq++
				if pkt, key, err := icmpProto.BuildProbe(0, ttl, seq, t.cfg); err == nil {
					// The only Echo Reply that answers this probe comes from the target.
					t.pm.addTo(key, ttl, 0, time.Now(), ProbeICMPTTL, t.target)
					if t.send(icmpSnd, sockICMP, ProbeICMPTTL, ttl, pkt, dst) {
						icmpSent++
					} else {
						// A probe that never left has no reply to wait for.
						t.pm.remove(key)
					}
				}
				time.Sleep(t.cfg.ProbeDelay)
			}

			// Tier 2: primary flows. Skipped when the primary is ICMP —
			// tier 1 already covers that packet.
			if proto.Name() != "icmp" && snd != nil {
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
					pkt, key, err := proto.BuildProbe(flow, ttl, seq, buildCfg)
					if err != nil {
						continue
					}

					t.pm.add(key, ttl, flow, time.Now(), primaryType)

					// A probe that never left has no reply to wait for: a
					// failed send removes its record.
					if !t.send(snd, sockPrimary, primaryType, ttl, pkt, dst) {
						t.pm.remove(key)
						continue
					}
					primarySent++

					time.Sleep(t.cfg.ProbeDelay)
				}
			}

			// One round-sent notification per TTL per round, carrying the
			// number of probes that went out.
			if t.OnRound != nil {
				if m, ok := t.OnRound.(RoundTypeMarker); ok {
					m.MarkRoundSentByType(ttl, icmpSent, primarySent)
				} else {
					t.OnRound.MarkRoundSent(ttl, icmpSent+primarySent)
				}
			}
		}

		// Tier 3: budgeted ICMP Echo Requests at full TTL for ground-truth
		// reachability. Skipped on round 0; starts round 2 (index >= 1) so
		// the first round establishes hop reachability before echoes begin.
		if round >= 1 && icmpSnd != nil {
			t.sendEchoes(echoSnd, &seq)
		}

		// The round's sends are done: log what did not leave, and let the
		// next round try a stalled socket again.
		t.endSendRound(round + 1)

		// Sweep stale probe entries that never received a response
		t.pm.sweep(2 * t.cfg.Timeout)

		// Auto-fallback: after 3 rounds with no responses, switch to ICMP
		if round >= 2 { // 0-indexed, so round 2 = 3rd round
			t.checkAutoFallback()
		}

		// If auto-fallback triggered, close this round and signal Run() to
		// restart with new sockets for the fallback protocol.
		if auto, ok := t.cfg.Protocol.(*AutoSelector); ok && auto.HasSwitched() {
			if t.OnRoundEnd != nil {
				t.OnRoundEnd()
			}
			return nil
		}

		round++
		lastRound := t.cfg.MaxRounds > 0 && round >= t.cfg.MaxRounds

		// Close the round only AFTER the inter-round pause. Responses come back
		// through an async pipeline (listener → results channel → UI), so
		// closing the round the instant sends finish counts still-in-flight
		// responses as loss even though they arrive and record RTT a few ms
		// later. The pause is the response-collection window; MarkRoundEnd runs
		// once it elapses, so a probe answered within the round is counted for it.
		select {
		case <-ctx.Done():
			if t.OnRoundEnd != nil {
				t.OnRoundEnd()
			}
			return nil
		case <-time.After(t.cfg.RoundDelay):
		}
		// No round follows the last one to pick up its late replies, so it
		// stays open until its probes are answered or time out.
		if lastRound {
			t.awaitFinalReplies(ctx)
		}
		if t.OnRoundEnd != nil {
			t.OnRoundEnd()
		}
		if lastRound {
			return nil
		}
	}
}

// finalReplyGrace is how long the last round stays open after its last reply
// is matched, so the result can travel from the listener to the hop table
// before the round is closed.
const finalReplyGrace = 20 * time.Millisecond

// awaitFinalReplies blocks while probes are outstanding and can still be
// answered: until every one is matched, or the newest is Timeout old, or ctx
// ends. With an interval shorter than the path's round-trip time the final
// round's replies are otherwise still in flight when the run stops, and are
// counted as lost.
func (t *Tracer) awaitFinalReplies(ctx context.Context) {
	if t.pm.pending(t.cfg.Timeout) == 0 {
		return
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for t.pm.pending(t.cfg.Timeout) > 0 {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
	select {
	case <-ctx.Done():
	case <-time.After(finalReplyGrace):
	}
}

// icmpSendConn is what icmpSender needs of *icmp.PacketConn. An interface so a
// test can stand in for the socket.
type icmpSendConn interface {
	IPv4PacketConn() *ipv4.PacketConn
	IPv6PacketConn() *ipv6.PacketConn
	WriteTo(b []byte, dst net.Addr) (int, error)
	SetWriteDeadline(t time.Time) error
}

// icmpSender wraps an ICMP PacketConn for the sender interface.
type icmpSender struct {
	conn      icmpSendConn
	ipVersion int
}

func (s *icmpSender) SetTTL(ttl int) error {
	if s.ipVersion == 6 {
		return s.conn.IPv6PacketConn().SetHopLimit(ttl)
	}
	return s.conn.IPv4PacketConn().SetTTL(ttl)
}
func (s *icmpSender) WriteTo(b []byte, dst net.Addr) (int, error) { return s.conn.WriteTo(b, dst) }

// SetWriteDeadline bounds the next write on the ICMP socket (see send.go).
func (s *icmpSender) SetWriteDeadline(t time.Time) error { return s.conn.SetWriteDeadline(t) }

// echoSender returns the sender the echo tier writes to, and a function that
// closes it when the send loop ends.
//
// The echo tier writes to every node on the path; the TTL probes all go to
// the target. On macOS, with both on one raw ICMPv6 socket, the socket layer
// refused writes for seconds at a time (viaduct#115). So the echo tier gets a
// second ICMP socket, in both families, and the socket that carries the TTL
// probes and receives every reply only ever sends to the target. Nothing is
// read from the echo socket: echo replies reach the listener's socket like
// all inbound ICMP (see openEchoConn for what the echo socket is spared).
//
// shared is the TTL probes' sender. If the second socket cannot be opened the
// echo tier shares it, as it did before, and the debug log says so.
// production is true when the tracer runs on real sockets; a test that
// supplies its sockets through newSender gets an echo socket only if it also
// sets newEchoSender.
func (t *Tracer) echoSender(shared probeSender, production bool) (probeSender, func()) {
	t.echoSock = sockICMP
	open := t.newEchoSender
	if open == nil && production {
		open = t.openEchoSocket
	}
	if open == nil || shared == nil {
		return shared, func() {}
	}
	snd, cleanup, err := open()
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		t.debugf("echo socket: %v; the echo tier shares the probe socket", err)
		return shared, func() {}
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	t.echoSock = sockEcho
	return snd, cleanup
}

// openEchoSocket is the production newEchoSender.
func (t *Tracer) openEchoSocket() (probeSender, func(), error) {
	conn, err := openEchoConn(t.cfg.IPVersion, t.debugf)
	if err != nil {
		return nil, nil, err
	}
	return &icmpSender{conn: conn, ipVersion: t.cfg.IPVersion}, func() { conn.Close() }, nil
}

// openEchoConn opens the echo tier's socket: a raw ICMP socket that is written
// to and never read.
//
// A raw ICMP socket is handed a copy of every inbound ICMP packet of its
// family, whichever socket the request left on. The listener's socket gets its
// copy regardless, so this one is told to take as little as the platform
// allows (see quiet).
func openEchoConn(ipVersion int, debugf func(format string, args ...any)) (*echoConn, error) {
	// The same socket icmp.ListenPacket opens for the listener, held as the
	// net.PacketConn it is so the receive buffer can be set.
	pc, err := net.ListenPacket(ListenerNet(ipVersion), RawListenAddr(ipVersion))
	if err != nil {
		return nil, err
	}
	conn := &echoConn{PacketConn: pc}
	if ipVersion == 6 {
		conn.p6 = ipv6.NewPacketConn(pc)
	} else {
		conn.p4 = ipv4.NewPacketConn(pc)
	}
	conn.quiet(conn.blockAllICMP, debugf)
	return conn, nil
}

// echoConn is the echo tier's raw ICMP socket in the shape icmpSender takes.
type echoConn struct {
	net.PacketConn
	p4 *ipv4.PacketConn
	p6 *ipv6.PacketConn

	// How the socket is kept from collecting inbound ICMP (see quiet). At
	// most one is set.
	filtered     bool // an ICMP filter that blocks every type is in place
	smallRecvBuf bool // no filter: the receive buffer is the kernel's minimum
}

// blockAllICMP sets an ICMP filter that blocks every type. It fails where the
// platform has no such filter for the socket's family.
func (c *echoConn) blockAllICMP() error {
	if c.p6 != nil {
		var f ipv6.ICMPFilter
		f.SetAll(true)
		return c.p6.SetICMPFilter(&f)
	}
	var f ipv4.ICMPFilter
	f.SetAll(true)
	return c.p4.SetICMPFilter(&f)
}

// quiet keeps the unread socket from collecting inbound ICMP, by the best
// means the platform has for the socket's family:
//
//   - A filter that blocks every ICMP type, where setFilter can set one:
//     ICMPv6 on Linux, macOS and the BSDs, ICMPv4 on Linux only. Nothing is
//     delivered to the socket.
//   - Otherwise (ICMPv4 on macOS and the BSDs, both families on Windows,
//     where setFilter reports that no filter is implemented) the receive
//     buffer is shrunk to the smallest the kernel allows. It fills after a
//     packet or two and from then on the kernel drops this socket's copy on
//     arrival.
//
// A full receive buffer on a socket nobody reads costs nothing else: the drop
// is per socket, it does not touch the listener's copy and it does not affect
// sending. A reader would only spend a goroutine and a wake-up per inbound
// ICMP packet to throw the same bytes away.
//
// These are economies, not conditions. If neither can be done the debug log
// says so and the socket is used as it is: it then holds one default receive
// buffer of packets and drops the rest.
func (c *echoConn) quiet(setFilter func() error, debugf func(format string, args ...any)) {
	filterErr := setFilter()
	if filterErr == nil {
		c.filtered = true
		return
	}
	bufErr := errors.New("the socket has no receive-buffer control")
	if rb, ok := c.PacketConn.(interface{ SetReadBuffer(bytes int) error }); ok {
		// Asked for one byte; the kernel rounds up to its minimum.
		bufErr = rb.SetReadBuffer(1)
	}
	if bufErr == nil {
		c.smallRecvBuf = true
		return
	}
	debugf("echo socket: no ICMP filter (%v) and the receive buffer was not shrunk (%v); the socket is used as it is", filterErr, bufErr)
}

func (c *echoConn) IPv4PacketConn() *ipv4.PacketConn { return c.p4 }
func (c *echoConn) IPv6PacketConn() *ipv6.PacketConn { return c.p6 }

// rawSender wraps a raw IP PacketConn for the sender interface.
type rawSender struct {
	conn      net.PacketConn
	ipVersion int
}

func (s *rawSender) SetTTL(ttl int) error {
	if s.ipVersion == 6 {
		return ipv6.NewPacketConn(s.conn).SetHopLimit(ttl)
	}
	return ipv4.NewPacketConn(s.conn).SetTTL(ttl)
}
func (s *rawSender) WriteTo(b []byte, dst net.Addr) (int, error) { return s.conn.WriteTo(b, dst) }

// SetWriteDeadline bounds the next write on the raw UDP or TCP socket.
func (s *rawSender) SetWriteDeadline(t time.Time) error { return s.conn.SetWriteDeadline(t) }

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
//
// conn accepts any icmpPacketConn; in production this is *icmp.PacketConn,
// and in tests it is a fakeICMPConn driven by channels.
func (t *Tracer) listen(ctx context.Context, conn icmpPacketConn, results chan<- Result) {
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

		msg, err := icmp.ParseMessage(ICMPProtoNum(t.cfg.IPVersion), buf[:n])
		if err != nil {
			continue
		}

		var key probeKey
		var isTarget bool

		switch msg.Type {
		case TimeExceededType(t.cfg.IPVersion):
			body, ok := msg.Body.(*icmp.TimeExceeded)
			if !ok || len(body.Data) < 28 {
				continue
			}
			// Try every known probe type's IdentifyResponse so tier-1 ICMP
			// TTL probes and tier-2 primary probes can both be matched even
			// when the primary is not ICMP.
			k, ok := identifyAny(t.cfg, t.icmpProto, body.Data)
			if !ok {
				continue
			}
			// Defense in depth: drop packets whose embedded destination isn't
			// our target. Filters out other via processes' probes (same base
			// collision) whenever targets differ.
			if !embeddedDestMatches(t.cfg.TargetIP, body.Data) {
				continue
			}
			key = *k
			isTarget = false

		case DestinationUnreachableType(t.cfg.IPVersion):
			body, ok := msg.Body.(*icmp.DstUnreach)
			if !ok || len(body.Data) < 28 {
				continue
			}
			k, ok := identifyAny(t.cfg, t.icmpProto, body.Data)
			if !ok {
				continue
			}
			if !embeddedDestMatches(t.cfg.TargetIP, body.Data) {
				continue
			}
			key = *k
			isTarget = proto.IsDestReachedICMP(ICMPTypeNum(DestinationUnreachableType(t.cfg.IPVersion)), msg.Code)

		case EchoReplyType(t.cfg.IPVersion):
			// Echo replies are meaningful in every mode: tier-1 ICMP TTL
			// probes and (later) the ICMP echo tier both surface here.
			echo, ok := msg.Body.(*icmp.Echo)
			if !ok {
				continue
			}
			key = probeKey{SrcPort: echo.ID, DstPort: echo.Seq, Seq: 0}
			// isTarget is decided after probeMap match, based on probeType.

		default:
			continue
		}

		peerIP := peerIP(peer)

		// An Echo Reply answers a probe only if it comes from the address
		// that probe was sent to. ICMP errors come from routers along the
		// path, so they are matched on the quoted packet alone, and never
		// answer an echo-tier probe.
		var rec probeRecord
		var ok bool
		if msg.Type == EchoReplyType(t.cfg.IPVersion) {
			rec, ok = t.pm.matchEchoReply(key, peerIP)
		} else {
			rec, ok = t.pm.matchICMPError(key)
		}
		if !ok {
			continue
		}

		// Primary-only response counting: tier-1 ICMP TTL replies and echoes
		// do not count for auto-fallback (which measures the primary tier).
		if rec.probeType == ProbeUDPTTL || rec.probeType == ProbeTCPTTL {
			atomic.AddInt32(&t.responseCount, 1)
		}

		// For Echo Replies: only ICMP-TTL matches confer target-reached
		// naturally (matches the pre-refactor ICMP-mode behavior). Echoes
		// on the echo tier are ground-truth reachability, not target
		// detection — that is handled by the peer==target check below.
		if msg.Type == EchoReplyType(t.cfg.IPVersion) && rec.probeType == ProbeICMPTTL {
			isTarget = true
		}

		// If the responder IS the target, mark as target even if the ICMP
		// type was TimeExceeded (some hosts echo TTL-exceeded for their own
		// IP instead of DestUnreachable — e.g., 4.2.2.2 via UDP).
		if !isTarget && peerIP != nil && t.cfg.TargetIP != nil && peerIP.Equal(t.cfg.TargetIP) {
			isTarget = true
		}

		// If this is the target, record the TTL so the sender can cap its range
		if isTarget {
			atomic.CompareAndSwapInt32(&t.targetTTL, 0, int32(rec.ttl))
		}
		rtt := time.Since(rec.sentAt)

		select {
		case results <- Result{
			TTL:       rec.ttl,
			IP:        peerIP,
			RTT:       rtt,
			IsTarget:  isTarget,
			FlowID:    rec.flowID,
			ProbeType: rec.probeType,
		}:
		case <-ctx.Done():
			return
		}
	}
}

// innerVersion returns the IP version nibble from an ICMP inner-header body.
// Returns 0 for empty input.
func innerVersion(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	return int(b[0] >> 4)
}

// embeddedDestMatches reports whether the packet quoted in an ICMP error was
// addressed to target. A quote that cannot be parsed does not match: an error
// message is only evidence about our probe if it shows the probe. The shortest
// quote RFC 792 allows, an IP header plus 8 bytes of the original datagram
// (28 bytes for IPv4, 48 for IPv6), parses and matches.
//
// With no target configured there is nothing to compare against, and the
// quote is accepted.
func embeddedDestMatches(target net.IP, inner []byte) bool {
	if target == nil {
		return true
	}
	v := innerVersion(inner)
	if v != 4 && v != 6 {
		return false
	}
	_, dstIP, _, _, _, err := ParseInnerHeader(v, inner)
	if err != nil || dstIP == nil {
		return false
	}
	return dstIP.Equal(target)
}

// identifyAny walks known protocol IdentifyResponse handlers so the listener
// can demux both tier-1 (ICMP TTL) and tier-2 (primary) probes regardless of
// the configured primary protocol. icmpProto is a pre-constructed instance
// supplied by the caller (see Tracer.icmpProto) to avoid a per-packet
// allocation in the hot path; IdentifyResponse does not consult sessionID,
// so sharing the instance across packets is safe.
func identifyAny(cfg Config, icmpProto *ICMPProtocol, inner []byte) (*probeKey, bool) {
	// Try the configured primary first — cheapest hit in the common case.
	if cfg.Protocol != nil {
		if k, err := cfg.Protocol.IdentifyResponse(inner); err == nil && k != nil {
			return k, true
		}
	}
	// Tier-1 ICMP TTL probes exist in every mode; try ICMP demux next.
	if k, err := icmpProto.IdentifyResponse(inner); err == nil && k != nil {
		return k, true
	}
	return nil, false
}
