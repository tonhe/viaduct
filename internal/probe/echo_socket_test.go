package probe

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// The echo tier sends on a socket of its own (viaduct#115). These tests sit at
// the socket-setup seam: newSender supplies the socket of the TTL probes,
// newEchoSender the echo tier's.

// echoSocketTracer is a tracer whose TTL probes go to probeSock and whose echo
// socket is whatever open returns. closed counts calls of the echo socket's
// cleanup.
func echoSocketTracer(cfg Config, target net.IP, probeSock probeSender, open func() (probeSender, error)) (tr *Tracer, closed *int) {
	tr = stallTracer(cfg, target, probeSock)
	closed = new(int)
	tr.newEchoSender = func() (probeSender, func(), error) {
		snd, err := open()
		if err != nil {
			return nil, nil, err
		}
		return snd, func() { *closed++ }, nil
	}
	return tr, closed
}

// Echoes go out on the second socket and TTL-limited probes on the first, in
// both families and with every primary protocol.
func TestRun_EchoTierSendsOnItsOwnSocket(t *testing.T) {
	const rounds = 3
	for _, fam := range stallFamilies {
		for _, p := range stallProtocols {
			t.Run(fam.name+"/"+p.name, func(t *testing.T) {
				cfg, target := stallCfg(fam.v, fam.source, fam.target, p.proto)
				cfg.MaxRounds = rounds
				probeSock := newStallSender(nil)
				echoSock := newStallSender(nil)
				tr, closed := echoSocketTracer(cfg, target, probeSock, func() (probeSender, error) { return echoSock, nil })
				hops := []EchoTarget{
					{IP: net.ParseIP(fam.hop), TTL: 1},
					{IP: net.ParseIP(fam.source), TTL: 2},
				}
				tr.SubmitEchoTargets(hops)
				runFor(t, tr, 5*time.Second)

				// The first socket: every TTL-limited probe, and no echo.
				icmpTTL, primary, echo := probeKinds(probeSock.writes(), target)
				if len(echo) != 0 {
					t.Errorf("%d echoes were written to the TTL probes' socket, want 0: first went to %s", len(echo), echo[0].dst)
				}
				if len(icmpTTL) != rounds*cfg.MaxHops {
					t.Errorf("%d ICMP TTL probes on the probe socket, want %d", len(icmpTTL), rounds*cfg.MaxHops)
				}
				wantPrimary := rounds * cfg.MaxHops * cfg.NumPaths
				if p.name == "icmp" {
					wantPrimary = 0
				}
				if len(primary) != wantPrimary {
					t.Errorf("%d %s probes on the probe socket, want %d", len(primary), p.name, wantPrimary)
				}

				// The second socket: the echoes, at full TTL, and nothing else.
				got := echoSock.writes()
				if want := (rounds - 1) * len(hops); len(got) != want {
					t.Fatalf("%d writes on the echo socket, want %d: %d echo targets in each round from the second", len(got), want, len(hops))
				}
				for _, w := range got {
					if w.dst == target.String() {
						t.Errorf("a probe to the target was written to the echo socket")
					}
					if !packetIsICMPEcho(w.pkt) {
						t.Errorf("a packet that is not an echo request was written to the echo socket (to %s)", w.dst)
					}
					if w.ttl != echoFullTTL {
						t.Errorf("echo to %s written with TTL %d, want %d", w.dst, w.ttl, echoFullTTL)
					}
				}
				if tr.echoSock != sockEcho {
					t.Errorf("echo sends are accounted to socket %d, want the echo socket (%d)", tr.echoSock, sockEcho)
				}
				if *closed != 1 {
					t.Errorf("the echo socket was closed %d times when the send loop ended, want 1", *closed)
				}
			})
		}
	}
}

// When the second socket cannot be opened, the echo tier shares the TTL
// probes' socket, and the debug log says so, once.
func TestRun_EchoTierFallsBackToTheSharedSocket(t *testing.T) {
	const rounds = 3
	for _, fam := range stallFamilies {
		t.Run(fam.name, func(t *testing.T) {
			cfg, target := stallCfg(fam.v, fam.source, fam.target, stallProtocols[0].proto)
			cfg.MaxRounds = rounds
			var sink logSink
			cfg.Debugf = sink.logf
			probeSock := newStallSender(nil)
			opens := 0
			tr, closed := echoSocketTracer(cfg, target, probeSock, func() (probeSender, error) {
				opens++
				return nil, errors.New("socket: too many open files")
			})
			tr.SubmitEchoTargets([]EchoTarget{{IP: net.ParseIP(fam.hop), TTL: 1}})
			var echoed int
			tr.SetOnEchoSent(func(net.IP, int) { echoed++ })
			runFor(t, tr, 5*time.Second)

			if opens != 1 {
				t.Errorf("the echo socket was opened %d times, want 1", opens)
			}
			_, _, echo := probeKinds(probeSock.writes(), target)
			if len(echo) != rounds-1 || echoed != rounds-1 {
				t.Errorf("%d echoes written to the shared socket, %d reported sent; want %d of each", len(echo), echoed, rounds-1)
			}
			if tr.echoSock != sockICMP {
				t.Errorf("echo sends are accounted to socket %d, want the shared ICMP socket (%d)", tr.echoSock, sockICMP)
			}
			lines := sink.matching("echo socket")
			if len(lines) != 1 {
				t.Fatalf("%d fallback lines in the debug log, want 1: %q", len(lines), sink.matching(""))
			}
			for _, want := range []string{"too many open files", "shares the probe socket", "[" + target.String() + "] "} {
				if !strings.Contains(lines[0], want) {
					t.Errorf("fallback line %q does not contain %q", lines[0], want)
				}
			}
			if *closed != 0 {
				t.Errorf("cleanup ran %d times for a socket that was never opened", *closed)
			}
		})
	}
}

// With no fallback there is nothing to log.
func TestRun_EchoSocketOpenedIsNotLogged(t *testing.T) {
	cfg, target := stallCfg(6, "2001:db8:0:1::7", "2001:db8:100::1", stallProtocols[0].proto)
	cfg.MaxRounds = 2
	var sink logSink
	cfg.Debugf = sink.logf
	tr, _ := echoSocketTracer(cfg, target, newStallSender(nil), func() (probeSender, error) { return newStallSender(nil), nil })
	tr.SubmitEchoTargets([]EchoTarget{{IP: net.ParseIP("2001:db8:0:5::1"), TTL: 1}})
	runFor(t, tr, 5*time.Second)
	if got := sink.matching(""); len(got) != 0 {
		t.Errorf("debug log = %q, want nothing: the echo socket opened and every probe left", got)
	}
}

// A stall on the echo socket is the echo socket's alone: the TTL probes'
// socket is not marked stalled and none of its probes is skipped.
func TestRun_EchoSocketStallDoesNotStallTheProbeSocket(t *testing.T) {
	const rounds = 4
	for _, fam := range stallFamilies {
		for _, p := range stallProtocols {
			t.Run(fam.name+"/"+p.name, func(t *testing.T) {
				cfg, target := stallCfg(fam.v, fam.source, fam.target, p.proto)
				cfg.MaxRounds = rounds
				var sink logSink
				cfg.Debugf = sink.logf
				probeSock := newStallSender(nil)
				echoSock := newStallSender(stallAll)
				defer echoSock.close()
				tr, _ := echoSocketTracer(cfg, target, probeSock, func() (probeSender, error) { return echoSock, nil })
				tr.SubmitEchoTargets([]EchoTarget{
					{IP: net.ParseIP(fam.hop), TTL: 1},
					{IP: net.ParseIP(fam.source), TTL: 2},
					{IP: net.ParseIP(fam.hop), TTL: 3},
				})
				var echoed int
				tr.SetOnEchoSent(func(net.IP, int) { echoed++ })
				// What the stall flags say at the end of each round's sends
				// would be gone by OnRoundEnd, so the probes that left are the
				// evidence: every one of them, in every round.
				var sent sentCounter
				tr.SetOnRoundSent(&sent)
				runFor(t, tr, 5*time.Second)

				perTTL := 1
				if p.name != "icmp" {
					perTTL += cfg.NumPaths
				}
				if want := rounds * cfg.MaxHops * perTTL; sent.probes != want {
					t.Errorf("%d TTL-limited probes sent, want %d: the echo socket's stall must not cost the probe socket a send", sent.probes, want)
				}
				if n := len(probeSock.writes()); n != rounds*cfg.MaxHops*perTTL {
					t.Errorf("%d writes on the probe socket, want %d", n, rounds*cfg.MaxHops*perTTL)
				}
				// One echo is attempted per round from the second; after it
				// times out the rest of the round's echoes are skipped.
				if n := len(echoSock.writes()); n != rounds-1 {
					t.Errorf("%d echo writes attempted, want %d: one per round", n, rounds-1)
				}
				if echoed != 0 {
					t.Errorf("%d echoes reported sent on a socket that takes no writes, want 0", echoed)
				}
				for _, line := range sink.matching("probes not sent") {
					if strings.Contains(line, "ttl ") {
						t.Errorf("a TTL-limited probe was not sent while only the echo socket was stalled: %q", line)
					}
					if !strings.Contains(line, "icmp-echo 1 timed out, 0 failed, 2 skipped") {
						t.Errorf("round line = %q, want 1 echo timed out and 2 skipped", line)
					}
				}
			})
		}
	}
}

// The other direction: a stalled probe socket does not stop the echoes, which
// have their own.
func TestRun_ProbeSocketStallDoesNotStallTheEchoSocket(t *testing.T) {
	const rounds = 3
	cfg, target := stallCfg(6, "2001:db8:0:1::7", "2001:db8:100::1", stallProtocols[0].proto)
	cfg.MaxRounds = rounds
	probeSock := newStallSender(stallAll)
	defer probeSock.close()
	echoSock := newStallSender(nil)
	tr, _ := echoSocketTracer(cfg, target, probeSock, func() (probeSender, error) { return echoSock, nil })
	hops := []EchoTarget{{IP: net.ParseIP("2001:db8:0:5::1"), TTL: 1}, {IP: net.ParseIP("2001:db8:0:6::1"), TTL: 2}}
	tr.SubmitEchoTargets(hops)
	runFor(t, tr, 5*time.Second)

	if got, want := len(echoSock.writes()), (rounds-1)*len(hops); got != want {
		t.Errorf("%d echoes written while the probe socket was stalled, want %d", got, want)
	}
}

// The stall accounting itself, socket by socket.
func TestSendState_EchoSocketStallIsItsOwn(t *testing.T) {
	cfg, target := stallCfg(6, "2001:db8:0:1::7", "2001:db8:100::1", stallProtocols[1].proto)
	dst := &net.IPAddr{IP: target}
	hop := &net.IPAddr{IP: net.ParseIP("2001:db8:0:5::1")}
	pkt := []byte{128, 0, 0, 0}

	// oneSocket is how the tests' single fake for the TTL probes and the flows
	// is described; the echo socket is separate either way.
	for _, oneSocket := range []bool{false, true} {
		name := "separate probe sockets"
		if oneSocket {
			name = "one probe socket"
		}
		t.Run(name, func(t *testing.T) {
			tr := NewTracer(target, cfg)
			tr.sends.oneSocket = oneSocket
			echoSock := newStallSender(stallAll)
			defer echoSock.close()
			probeSock := newStallSender(nil)

			if tr.send(echoSock, sockEcho, ProbeICMPEcho, echoFullTTL, pkt, hop) {
				t.Fatal("a write that timed out was reported as sent")
			}
			if !tr.sends.isStalled(sockEcho) {
				t.Error("the echo socket is not marked stalled after its write timed out")
			}
			if tr.sends.isStalled(sockICMP) || tr.sends.isStalled(sockPrimary) {
				t.Errorf("a stall on the echo socket marked a probe socket stalled: %v", tr.sends.stalled)
			}
			if !tr.send(probeSock, sockICMP, ProbeICMPTTL, 1, pkt, dst) {
				t.Error("an ICMP TTL probe was skipped because the echo socket stalled")
			}
			if !tr.send(probeSock, sockPrimary, ProbeUDPTTL, 1, pkt, dst) {
				t.Error("a UDP probe was skipped because the echo socket stalled")
			}
			if tr.send(echoSock, sockEcho, ProbeICMPEcho, echoFullTTL, pkt, hop) {
				t.Error("a send on the stalled echo socket was reported as sent")
			}
			if n := len(echoSock.writes()); n != 1 {
				t.Errorf("%d writes on the stalled echo socket, want 1: the second is skipped", n)
			}

			// And a stalled probe socket leaves the echo socket alone.
			tr.endSendRound(1)
			if tr.sends.isStalled(sockEcho) {
				t.Error("the echo socket is still stalled in the next round")
			}
			tr.sends.stalled[sockICMP] = true
			if tr.sends.isStalled(sockEcho) {
				t.Error("a stall on the ICMP probe socket marked the echo socket stalled")
			}
			if got := tr.sends.isStalled(sockPrimary); got != oneSocket {
				t.Errorf("isStalled(primary) = %v with the ICMP socket stalled and oneSocket=%v", got, oneSocket)
			}
		})
	}
}

