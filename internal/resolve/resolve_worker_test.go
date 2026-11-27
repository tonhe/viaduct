package resolve

// Tests for what the worker does with each answer of a lookup, for the
// deadline it gives the lookup, and for the system-resolver lookup itself.
// Addresses are from RFC 5737 and RFC 3849.

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// A hostname is sent only when the lookup succeeded and gave a name that is
// still something once its trailing dot is removed. Whatever the lookup
// returned, the address is cached, so it is not looked up again, and a blank
// hostname is never sent.
func TestWorker_WhatEachLookupAnswerBecomes(t *testing.T) {
	failed := errors.New("lookup failed")
	cases := []struct {
		label string
		name  string
		err   error
		want  string // hostname sent and cached; "" means nothing is sent
	}{
		{"a name", "host.example", nil, "host.example"},
		{"a name with a trailing dot", "host.example.", nil, "host.example"},
		{"a single label", "host", nil, "host"},
		{"dots inside the name are kept", "a.b.example.", nil, "a.b.example"},
		{"only the last dot is removed", "host.example..", nil, "host.example."},
		{"no error and an empty name", "", nil, ""},
		{"no error and a name that is only a dot", ".", nil, ""},
		{"an error and no name", "", failed, ""},
		{"an error and a name: the name is not used", "host.example.", failed, ""},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			ip, marker := docIP(10), docIP(11)
			var mu sync.Mutex
			calls := map[string]int{}
			r := newTestResolver(1, func(_ context.Context, got net.IP) (string, error) {
				mu.Lock()
				calls[got.String()]++
				mu.Unlock()
				if got.Equal(marker) {
					return "marker.example", nil
				}
				return tc.name, tc.err
			})
			results := make(chan Result, 4)
			cancel := startResolver(t, r, results)
			defer cancel()

			// One worker handles the addresses in the order submitted, so the
			// marker's result arrives after anything sent for ip.
			if !r.Submit(ip) || !r.Submit(marker) {
				t.Fatal("Submit refused a new address")
			}
			var got []Result
			for {
				res := waitResult(t, results, 2*time.Second)
				if res.IP.Equal(marker) {
					break
				}
				got = append(got, res)
			}

			if tc.want == "" {
				if len(got) != 0 {
					t.Errorf("results sent for %s: %+v, want none", ip, got)
				}
			} else if len(got) != 1 || !got[0].IP.Equal(ip) || got[0].Hostname != tc.want {
				t.Errorf("results sent for %s: %+v, want one with hostname %q", ip, got, tc.want)
			}
			for _, res := range got {
				if res.Hostname == "" {
					t.Errorf("a blank hostname was sent: %+v", res)
				}
			}

			// Cached either way, with the same value that was or was not sent.
			name, cached := r.Lookup(ip)
			if !cached || name != tc.want {
				t.Errorf("Lookup(%s) = %q, %v; want %q, true", ip, name, cached, tc.want)
			}
			// No longer pending, and not looked up a second time.
			if _, pending := r.pending.Load(ip.String()); pending {
				t.Errorf("%s is still marked pending after its lookup", ip)
			}
			if r.Submit(ip) {
				t.Errorf("Submit(%s) accepted an address that is already cached", ip)
			}
			mu.Lock()
			n := calls[ip.String()]
			mu.Unlock()
			if n != 1 {
				t.Errorf("%s was looked up %d times, want 1", ip, n)
			}
		})
	}
}

// Each lookup gets its own two-second deadline, and its context is released
// when the lookup returns.
func TestWorker_LookupHasATwoSecondDeadline(t *testing.T) {
	type seen struct {
		ctx      context.Context
		at       time.Time
		deadline time.Time
		has      bool
	}
	got := make(chan seen, 1)
	r := newTestResolver(1, func(ctx context.Context, _ net.IP) (string, error) {
		s := seen{ctx: ctx, at: time.Now()}
		s.deadline, s.has = ctx.Deadline()
		got <- s
		return "host.example", nil
	})
	results := make(chan Result, 1)
	// The resolver's own context ends in five seconds (startResolver), so a
	// two-second deadline can only be the worker's.
	cancel := startResolver(t, r, results)
	defer cancel()

	r.Submit(docIP(20))
	waitResult(t, results, 2*time.Second)
	s := <-got

	if !s.has {
		t.Fatal("the lookup's context has no deadline")
	}
	// Taken after the worker set it, so at most two seconds ahead. The lower
	// bound allows the worker to be held off the CPU for up to half a second
	// between setting the deadline and calling the lookup, which a loaded CI
	// runner can do; a one-second deadline is still well outside it.
	if ahead := s.deadline.Sub(s.at); ahead > 2*time.Second || ahead < 1500*time.Millisecond {
		t.Errorf("the lookup's deadline was %v ahead, want two seconds", ahead)
	}
	if s.ctx.Err() == nil {
		t.Error("the lookup's context is still live after the lookup returned and its result was sent; it must be cancelled")
	}
}

// The lookup's context ends with the resolver's: a lookup in progress when the
// resolver is stopped is told to stop, and Run returns without waiting out the
// lookup's own deadline.
func TestWorker_StoppingTheResolverStopsALookupInProgress(t *testing.T) {
	started := make(chan struct{})
	var lookupErr error
	r := newTestResolver(1, func(ctx context.Context, _ net.IP) (string, error) {
		close(started)
		<-ctx.Done() // a lookup that takes as long as it is allowed
		lookupErr = ctx.Err()
		return "", ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan Result, 1)
	done := make(chan struct{})
	go func() {
		r.Run(ctx, results)
		close(done)
	}()

	ip := docIP(21)
	r.Submit(ip)
	<-started
	stopped := time.Now()
	cancel()

	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("Run was still going 1.5s after the resolver was stopped: the lookup was not told to stop and ran to its own two-second deadline")
	}
	if took := time.Since(stopped); took > time.Second {
		t.Errorf("Run took %v to return after the resolver was stopped", took)
	}
	if !errors.Is(lookupErr, context.Canceled) {
		t.Errorf("the lookup ended with %v, want context.Canceled from the resolver's context", lookupErr)
	}
	// The interrupted lookup is cached as "no name", and nothing was sent.
	if name, cached := r.Lookup(ip); !cached || name != "" {
		t.Errorf("Lookup(%s) = %q, %v after an interrupted lookup; want \"\", true", ip, name, cached)
	}
	select {
	case res := <-results:
		t.Errorf("a result was sent for an interrupted lookup: %+v", res)
	default:
	}
}

// ---------------------------------------------------------------------------
// defaultLookup, against a DNS server on the loopback interface
// ---------------------------------------------------------------------------

// usePTRServer starts a DNS server on a loopback UDP socket that answers a PTR
// question for a name in ptr with one record per target, and anything else
// with NXDOMAIN. It makes that server the only one net.DefaultResolver asks,
// and undoes both when the test ends. No query leaves the host.
func usePTRServer(t *testing.T, ptr map[string][]string) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the fake DNS server: %v", err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return // closed
			}
			var p dnsmessage.Parser
			hdr, err := p.Start(buf[:n])
			if err != nil {
				continue
			}
			q, err := p.Question()
			if err != nil {
				continue
			}
			resp := dnsmessage.Message{
				Header: dnsmessage.Header{
					ID: hdr.ID, Response: true,
					RecursionDesired: hdr.RecursionDesired, RecursionAvailable: true,
				},
				Questions: []dnsmessage.Question{q},
			}
			targets, known := ptr[strings.ToLower(q.Name.String())]
			if !known || q.Type != dnsmessage.TypePTR {
				resp.Header.RCode = dnsmessage.RCodeNameError
			} else {
				for _, target := range targets {
					resp.Answers = append(resp.Answers, dnsmessage.Resource{
						Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET, TTL: 60},
						Body:   &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName(target)},
					})
				}
			}
			if out, err := resp.Pack(); err == nil {
				conn.WriteTo(out, from)
			}
		}
	}()

	saved := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		// Whatever server the host is configured with, ask the fake one.
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", conn.LocalAddr().String())
		},
	}
	t.Cleanup(func() {
		net.DefaultResolver = saved
		conn.Close()
	})
}

// defaultLookup returns the first name the system resolver gives for the
// address, as the resolver gives it, and no name with the resolver's error
// when there is none.
func TestDefaultLookup(t *testing.T) {
	const (
		v4Name = "7.2.0.192.in-addr.arpa."
		// The 32 nibbles of 2001:db8::7, last first.
		v6Name = "7.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa."
	)
	usePTRServer(t, map[string][]string{
		v4Name: {"first.example.", "second.example."},
		v6Name: {"six.example."},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cases := []struct {
		label   string
		ip      string
		want    string
		wantErr bool
	}{
		{"IPv4 with two names: the first", "192.0.2.7", "first.example.", false},
		{"IPv6", "2001:db8::7", "six.example.", false},
		{"IPv4 with no record", "192.0.2.8", "", true},
		{"IPv6 with no record", "2001:db8::8", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got, err := defaultLookup(ctx, net.ParseIP(tc.ip))
			if (err != nil) != tc.wantErr {
				t.Fatalf("defaultLookup(%s) error = %v, want an error: %v", tc.ip, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("defaultLookup(%s) = %q, want %q", tc.ip, got, tc.want)
			}
		})
	}
}

// New wires the system-resolver lookup in, so a resolver built the usual way
// resolves through it: the name comes back without its trailing dot.
func TestNew_ResolvesThroughTheSystemResolver(t *testing.T) {
	usePTRServer(t, map[string][]string{"9.2.0.192.in-addr.arpa.": {"nine.example."}})
	r := New(1)
	results := make(chan Result, 1)
	cancel := startResolver(t, r, results)
	defer cancel()

	ip := docIP(9)
	r.Submit(ip)
	res := waitResult(t, results, 5*time.Second)
	if !res.IP.Equal(ip) || res.Hostname != "nine.example" {
		t.Errorf("result = %+v, want nine.example for %s", res, ip)
	}
}
