// Package dnstest is a DNS server for tests. It answers TXT and PTR questions
// from a table, on a loopback UDP socket, and makes itself the only server
// net.DefaultResolver asks. No query leaves the host.
//
// net.DefaultResolver is process-wide, so tests that use a Server must not run
// in parallel with each other or with any test that resolves names.
package dnstest

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// Records is what a Server answers. Names are lower-case and end with a dot,
// as they appear in a question ("1.2.0.192.in-addr.arpa."). Any question that
// is not for a name in the table of its type is answered with NXDOMAIN. A name
// in the table with no records is answered with an empty reply.
type Records struct {
	// TXT maps a name to its TXT records, one record per string.
	TXT map[string][]string
	// PTR maps a name to its PTR targets, each ending with a dot.
	PTR map[string][]string
}

// Server is a running fake DNS server.
type Server struct {
	conn    net.PacketConn
	records Records
	saved   *net.Resolver
	close   sync.Once

	mu       sync.Mutex
	askedTXT []string
}

// Start starts a Server that answers from records and points
// net.DefaultResolver at it. Both are undone by Close, which also runs when
// the test ends.
func Start(tb testing.TB, records Records) *Server {
	tb.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listening for the fake DNS server: %v", err)
	}
	s := &Server{conn: conn, records: records, saved: net.DefaultResolver}
	go s.serve()

	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		// Whatever server the host is configured with, ask the fake one.
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", conn.LocalAddr().String())
		},
	}
	tb.Cleanup(s.Close)
	return s
}

// Close restores the resolver that was in place when the Server started and
// stops the Server. Calling it more than once is safe.
func (s *Server) Close() {
	s.close.Do(func() {
		net.DefaultResolver = s.saved
		s.conn.Close()
	})
}

// AskedTXT returns the name of every TXT question the Server has been asked,
// in order, whether or not it had an answer. A resolver may retry a failed
// name with the host's search domains appended; those questions are included.
func (s *Server) AskedTXT() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.askedTXT...)
}

func (s *Server) serve() {
	buf := make([]byte, 1500)
	for {
		n, from, err := s.conn.ReadFrom(buf)
		if err != nil {
			return // closed
		}
		if out, ok := s.answer(buf[:n]); ok {
			s.conn.WriteTo(out, from)
		}
	}
}

// answer returns the packed reply to one query, or false when the query cannot
// be parsed.
func (s *Server) answer(query []byte) ([]byte, bool) {
	var p dnsmessage.Parser
	hdr, err := p.Start(query)
	if err != nil {
		return nil, false
	}
	q, err := p.Question()
	if err != nil {
		return nil, false
	}
	name := strings.ToLower(q.Name.String())
	resp := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:                 hdr.ID,
			Response:           true,
			RecursionDesired:   hdr.RecursionDesired,
			RecursionAvailable: true,
		},
		Questions: []dnsmessage.Question{q},
	}
	rh := dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: dnsmessage.ClassINET, TTL: 60}

	known := false
	switch q.Type {
	case dnsmessage.TypeTXT:
		s.mu.Lock()
		s.askedTXT = append(s.askedTXT, name)
		s.mu.Unlock()
		records, ok := s.records.TXT[name]
		known = ok
		for _, txt := range records {
			resp.Answers = append(resp.Answers, dnsmessage.Resource{
				Header: rh,
				Body:   &dnsmessage.TXTResource{TXT: []string{txt}},
			})
		}
	case dnsmessage.TypePTR:
		targets, ok := s.records.PTR[name]
		known = ok
		for _, target := range targets {
			resp.Answers = append(resp.Answers, dnsmessage.Resource{
				Header: rh,
				Body:   &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName(target)},
			})
		}
	}
	if !known {
		resp.Header.RCode = dnsmessage.RCodeNameError
	}
	out, err := resp.Pack()
	if err != nil {
		return nil, false
	}
	return out, true
}
