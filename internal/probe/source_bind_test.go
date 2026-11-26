package probe

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"sort"
	"strings"
	"testing"
	"time"
)

// Every probe of a trace leaves from one source address (viaduct#234): the
// sockets that send probes are bound to the address found at startup
// (Config.SourceIP), so the kernel does not choose a source for each packet.

// listenRecorder stands in for net.ListenPacket: it records the calls and
// fails the ones made at the addresses in fail.
type listenRecorder struct {
	calls []listenCall
	fail  map[string]error
}

func (r *listenRecorder) listen(network, address string) (string, error) {
	r.calls = append(r.calls, listenCall{network, address})
	if err := r.fail[address]; err != nil {
		return "", err
	}
	return "socket at " + address, nil
}

// The rule, without a socket: which address each kind of probe socket is
// opened at, and what the debug log says about it. One line per socket.
func TestBindToSource(t *testing.T) {
	noAddr := errors.New("cannot assign requested address")
	noPerm := errors.New("operation not permitted")

	for _, tc := range []struct {
		name      string
		ipVersion int
		network   string
		source    string // "" = none found at startup
		unbound   bool   // the platform's probe sockets are left unbound
		fail      map[string]error
		wantCalls []listenCall
		wantConn  string
		wantErr   error
		wantLog   []string
	}{
		{
			name: "IPv4 ICMP socket", ipVersion: 4, network: "ip4:icmp", source: "192.0.2.7",
			wantCalls: []listenCall{{"ip4:icmp", "192.0.2.7"}},
			wantConn:  "socket at 192.0.2.7",
			wantLog:   []string{"test socket (ip4:icmp)", "bound to source address 192.0.2.7"},
		},
		{
			name: "IPv6 ICMP socket", ipVersion: 6, network: "ip6:ipv6-icmp", source: "2001:db8:0:1::7",
			wantCalls: []listenCall{{"ip6:ipv6-icmp", "2001:db8:0:1::7"}},
			wantConn:  "socket at 2001:db8:0:1::7",
			wantLog:   []string{"test socket (ip6:ipv6-icmp)", "bound to source address 2001:db8:0:1::7"},
		},
		{
			name: "IPv4 UDP send socket", ipVersion: 4, network: "ip4:udp", source: "192.0.2.7",
			wantCalls: []listenCall{{"ip4:udp", "192.0.2.7"}},
			wantConn:  "socket at 192.0.2.7",
			wantLog:   []string{"bound to source address 192.0.2.7"},
		},
		{
			name: "IPv6 UDP send socket", ipVersion: 6, network: "ip6:udp", source: "2001:db8:0:1::7",
			wantCalls: []listenCall{{"ip6:udp", "2001:db8:0:1::7"}},
			wantConn:  "socket at 2001:db8:0:1::7",
			wantLog:   []string{"bound to source address 2001:db8:0:1::7"},
		},
		{
			name: "IPv4 TCP send socket", ipVersion: 4, network: "ip4:tcp", source: "192.0.2.7",
			wantCalls: []listenCall{{"ip4:tcp", "192.0.2.7"}},
			wantConn:  "socket at 192.0.2.7",
			wantLog:   []string{"bound to source address 192.0.2.7"},
		},
		{
			name: "IPv6 TCP send socket", ipVersion: 6, network: "ip6:tcp", source: "2001:db8:0:1::7",
			wantCalls: []listenCall{{"ip6:tcp", "2001:db8:0:1::7"}},
			wantConn:  "socket at 2001:db8:0:1::7",
			wantLog:   []string{"bound to source address 2001:db8:0:1::7"},
		},
		{
			name: "no source address found", ipVersion: 6, network: "ip6:ipv6-icmp",
			wantCalls: []listenCall{{"ip6:ipv6-icmp", "::"}},
			wantConn:  "socket at ::",
			wantLog:   []string{"no source address was found at startup", "not bound", "the kernel chooses the source address of each probe"},
		},
		{
			name: "source address of the other family (v4 for v6)", ipVersion: 6, network: "ip6:udp", source: "192.0.2.7",
			wantCalls: []listenCall{{"ip6:udp", "::"}},
			wantConn:  "socket at ::",
			wantLog:   []string{"192.0.2.7", "is not an IPv6 address", "not bound"},
		},
		{
			name: "source address of the other family (v6 for v4)", ipVersion: 4, network: "ip4:udp", source: "2001:db8:0:1::7",
			wantCalls: []listenCall{{"ip4:udp", "0.0.0.0"}},
			wantConn:  "socket at 0.0.0.0",
			wantLog:   []string{"2001:db8:0:1::7", "is not an IPv4 address", "not bound"},
		},
		{
			// A link-local address is bound with its interface, which the
			// startup address does not carry.
			name: "link-local source address", ipVersion: 6, network: "ip6:ipv6-icmp", source: "fe80::1",
			wantCalls: []listenCall{{"ip6:ipv6-icmp", "::"}},
			wantConn:  "socket at ::",
			wantLog:   []string{"fe80::1", "link-local", "not bound"},
		},
		{
			name: "unspecified source address", ipVersion: 4, network: "ip4:icmp", source: "0.0.0.0",
			wantCalls: []listenCall{{"ip4:icmp", "0.0.0.0"}},
			wantConn:  "socket at 0.0.0.0",
			wantLog:   []string{"no source address was found at startup", "not bound"},
		},
		{
			name: "bind fails: unbound socket instead", ipVersion: 6, network: "ip6:tcp", source: "2001:db8:0:1::7",
			fail:      map[string]error{"2001:db8:0:1::7": noAddr},
			wantCalls: []listenCall{{"ip6:tcp", "2001:db8:0:1::7"}, {"ip6:tcp", "::"}},
			wantConn:  "socket at ::",
			wantLog:   []string{"binding to source address 2001:db8:0:1::7 failed", "cannot assign requested address", "not bound"},
		},
		{
			// The error of the unbound open is the one returned: it is what
			// the caller reported before sockets were bound.
			name: "no socket at all", ipVersion: 4, network: "ip4:icmp", source: "192.0.2.7",
			fail:      map[string]error{"192.0.2.7": noAddr, "0.0.0.0": noPerm},
			wantCalls: []listenCall{{"ip4:icmp", "192.0.2.7"}, {"ip4:icmp", "0.0.0.0"}},
			wantErr:   noPerm,
		},
		{
			name: "platform whose sockets stay unbound", ipVersion: 4, network: "ip4:icmp", source: "192.0.2.7", unbound: true,
			wantCalls: []listenCall{{"ip4:icmp", "0.0.0.0"}},
			wantConn:  "socket at 0.0.0.0",
			wantLog:   []string{"source address 192.0.2.7 is not used", "left unbound on this platform"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := bindProbeSockets
			bindProbeSockets = !tc.unbound
			defer func() { bindProbeSockets = saved }()

			cfg := DefaultConfig()
			cfg.IPVersion = tc.ipVersion
			if tc.source != "" {
				cfg.SourceIP = net.ParseIP(tc.source)
			}
			var sink logSink
			cfg.Debugf = sink.logf
			target := net.ParseIP("2001:db8:100::1")
			if tc.ipVersion == 4 {
				target = net.ParseIP("198.51.100.1")
			}
			tr := NewTracer(target, cfg)
			rec := &listenRecorder{fail: tc.fail}

			conn, err := bindToSource(tr, "test socket", rec.listen)(tc.network, RawListenAddr(tc.ipVersion))

			if len(rec.calls) != len(tc.wantCalls) {
				t.Fatalf("sockets opened = %+v, want %+v", rec.calls, tc.wantCalls)
			}
			for i, want := range tc.wantCalls {
				if rec.calls[i] != want {
					t.Errorf("open %d = %+v, want %+v", i+1, rec.calls[i], want)
				}
			}
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if conn != tc.wantConn {
				t.Errorf("socket = %q, want %q", conn, tc.wantConn)
			}
			lines := sink.matching("")
			if tc.wantErr != nil {
				// The caller reports the error; nothing was opened to describe.
				if len(lines) != 0 {
					t.Errorf("debug log = %q, want nothing", lines)
				}
				return
			}
			if len(lines) != 1 {
				t.Fatalf("%d debug-log lines, want 1: %q", len(lines), lines)
			}
			for _, want := range append(tc.wantLog, "["+target.String()+"] ") {
				if !strings.Contains(lines[0], want) {
					t.Errorf("line %q does not contain %q", lines[0], want)
				}
			}
		})
	}
}

// Every socket the package opens for probes is opened through bindToSource.
// This test reads the package source and fails when a socket is opened beside
// it: such a socket is unbound, and the kernel picks its packets' source.
func TestProbeSockets_EveryOpenGoesThroughBindToSource(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	// Direct opens that are not probe sockets of a trace.
	allowedDirect := map[string]bool{
		// A wrapper kept for the listener seam; it opens nothing in a run.
		"probe.go:defaultListenICMP": true,
	}
	var bound []string
	for _, pkg := range pkgs {
		for fname, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				where := fname + ":" + funcName(fn)
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch fun := call.Fun.(type) {
					case *ast.Ident:
						if fun.Name == "bindToSource" {
							bound = append(bound, where)
						}
					case *ast.SelectorExpr:
						if strings.HasPrefix(fun.Sel.Name, "Listen") && !allowedDirect[where] {
							t.Errorf("%s: %s.%s called directly: the socket is not bound to the trace's source address",
								fset.Position(call.Pos()), exprString(fun.X), fun.Sel.Name)
						}
					}
					return true
				})
			}
		}
	}
	sort.Strings(bound)
	// The ICMP probe socket (Run), the echo tier's socket (openEchoSocket) and
	// the UDP or TCP send socket (run).
	want := []string{"probe.go:Tracer.Run", "probe.go:Tracer.openEchoSocket", "probe.go:Tracer.run"}
	if strings.Join(bound, " ") != strings.Join(want, " ") {
		t.Errorf("sockets opened through bindToSource in %v, want exactly %v", bound, want)
	}
}

// Real sockets, where the process may open them (root or CAP_NET_RAW). A trace
// of the loopback address with the loopback address as its source: every probe
// socket is bound to it, and the destination's replies (Echo Reply, Port
// Unreachable, a reset) still reach the bound sockets and are matched.
func TestRun_ProbeSocketsBoundToSourceStillReceive(t *testing.T) {
	if !bindProbeSockets {
		t.Skip("probe sockets are not bound on this platform")
	}
	for _, fam := range echoConnFamilies {
		lo := net.ParseIP(fam.loopback)
		for _, tc := range []struct {
			name    string
			proto   func(t *testing.T) ProbeProtocol
			want    ProbeType
			sockets []string
		}{
			{"icmp", func(*testing.T) ProbeProtocol { return NewICMPProtocol(DefaultConfig()) }, ProbeICMPTTL,
				[]string{"icmp socket", "echo socket"}},
			{"udp", func(*testing.T) ProbeProtocol { return NewUDPProtocol(33434) }, ProbeUDPTTL,
				[]string{"icmp socket", "echo socket", "udp send socket"}},
			{"tcp", func(t *testing.T) ProbeProtocol { return NewTCPProtocol(closedLoopbackTCPPort(t, fam.v, fam.loopback)) }, ProbeTCPTTL,
				[]string{"icmp socket", "echo socket", "tcp send socket"}},
		} {
			t.Run(fam.name+"/"+tc.name, func(t *testing.T) {
				cfg := DefaultConfig()
				cfg.IPVersion = fam.v
				cfg.Protocol = tc.proto(t)
				cfg.SourceIP = lo
				cfg.TargetIP = lo
				cfg.MaxHops = 1
				cfg.NumPaths = 2
				cfg.MaxRounds = 2
				cfg.ProbeDelay = time.Millisecond
				cfg.RoundDelay = 50 * time.Millisecond
				cfg.Timeout = 500 * time.Millisecond
				var sink logSink
				cfg.Debugf = sink.logf

				tr := NewTracer(lo, cfg)
				results := make(chan Result, 64)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := tr.Run(ctx, results); err != nil {
					skipUnlessRequired(t, "Run could not open its sockets: %v", err)
				}
				// Run can return while its TCP listener is still sending on
				// results (viaduct#219): stop it and drain without closing.
				cancel()
				answered, icmpAnswered := 0, 0
			drain:
				for {
					select {
					case r := <-results:
						if r.IsTarget && r.ProbeType == tc.want {
							answered++
						}
						if r.IsTarget && r.ProbeType == ProbeICMPTTL {
							icmpAnswered++
						}
					default:
						break drain
					}
				}

				for _, sock := range tc.sockets {
					lines := sink.matching(sock + " (")
					if len(lines) != 1 || !strings.Contains(lines[0], "bound to source address "+lo.String()) {
						t.Errorf("%s: debug log = %q, want one line saying it is bound to source address %s\nwhole log: %q",
							sock, lines, lo, sink.matching(""))
					}
				}
				if unbound := sink.matching("not bound"); len(unbound) != 0 {
					t.Errorf("a probe socket was left unbound: %q", unbound)
				}
				// 2 rounds at one hop: one ICMP probe a round, and two flows a
				// round for UDP and TCP.
				if icmpAnswered != 2 {
					t.Errorf("%d of 2 ICMP probes answered on the bound ICMP socket", icmpAnswered)
				}
				if want := map[string]int{"icmp": 2, "udp": 4, "tcp": 4}[tc.name]; answered != want {
					t.Errorf("destination answered %d of %d %s probes on sockets bound to %s\ndebug log: %q",
						answered, want, tc.name, lo, sink.matching(""))
				}
			})
		}
	}
}

// closedLoopbackTCPPort returns a loopback TCP port of the family with nothing
// listening on it.
func closedLoopbackTCPPort(t *testing.T, ipVersion int, loopback string) int {
	t.Helper()
	l, err := net.Listen(DialNet(ipVersion, "tcp"), net.JoinHostPort(loopback, "0"))
	if err != nil {
		skipUnlessRequired(t, "no IPv%d loopback: %v", ipVersion, err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}
