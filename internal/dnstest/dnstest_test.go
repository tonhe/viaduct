package dnstest

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	txtName = "1.2.0.192.origin.asn.cymru.com."
	ptrName = "7.2.0.192.in-addr.arpa."
	// A name the table holds with no records at all.
	emptyName = "empty.example."
)

func testRecords() Records {
	return Records{
		TXT: map[string][]string{
			txtName:   {"64496 | 192.0.2.0/24 | ZZ", "64497 | 192.0.2.0/25 | ZZ"},
			emptyName: {},
		},
		PTR: map[string][]string{
			ptrName: {"first.example.", "second.example."},
		},
	}
}

func lookupCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// isNotFound reports whether err is the resolver's "no such host".
func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

func TestServer_AnswersTXTFromTheTable(t *testing.T) {
	Start(t, testRecords())
	got, err := net.DefaultResolver.LookupTXT(lookupCtx(t), txtName)
	if err != nil {
		t.Fatalf("LookupTXT: %v", err)
	}
	sort.Strings(got)
	want := []string{"64496 | 192.0.2.0/24 | ZZ", "64497 | 192.0.2.0/25 | ZZ"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TXT records = %q, want %q", got, want)
	}
}

func TestServer_AnswersPTRFromTheTable(t *testing.T) {
	Start(t, testRecords())
	got, err := net.DefaultResolver.LookupAddr(lookupCtx(t), "192.0.2.7")
	if err != nil {
		t.Fatalf("LookupAddr: %v", err)
	}
	sort.Strings(got)
	want := []string{"first.example.", "second.example."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PTR targets = %q, want %q", got, want)
	}
}

func TestServer_UnknownNamesDoNotExist(t *testing.T) {
	Start(t, testRecords())
	ctx := lookupCtx(t)

	if got, err := net.DefaultResolver.LookupTXT(ctx, "unknown.example."); !isNotFound(err) {
		t.Errorf("TXT for an unknown name = %q, %v; want a not-found error", got, err)
	}
	if got, err := net.DefaultResolver.LookupAddr(ctx, "192.0.2.99"); !isNotFound(err) {
		t.Errorf("PTR for an unknown address = %q, %v; want a not-found error", got, err)
	}
}

// query sends one question straight to the server and returns the reply, so
// the response code and the answer count can be read as they were sent.
func query(t *testing.T, s *Server, name string, typ dnsmessage.Type) dnsmessage.Message {
	t.Helper()
	q := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 0x1234, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET}},
	}
	packed, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	out, ok := s.answer(packed)
	if !ok {
		t.Fatalf("no answer to %s %v", name, typ)
	}
	var reply dnsmessage.Message
	if err := reply.Unpack(out); err != nil {
		t.Fatalf("unpacking the reply: %v", err)
	}
	return reply
}

func TestServer_ReplyCodes(t *testing.T) {
	s := Start(t, testRecords())
	cases := []struct {
		name        string
		question    string
		typ         dnsmessage.Type
		wantRCode   dnsmessage.RCode
		wantAnswers int
	}{
		{"TXT name in the table", txtName, dnsmessage.TypeTXT, dnsmessage.RCodeSuccess, 2},
		{"PTR name in the table", ptrName, dnsmessage.TypePTR, dnsmessage.RCodeSuccess, 2},
		{"a name with no records is an empty reply, not NXDOMAIN", emptyName, dnsmessage.TypeTXT, dnsmessage.RCodeSuccess, 0},
		{"unknown TXT name", "unknown.example.", dnsmessage.TypeTXT, dnsmessage.RCodeNameError, 0},
		{"unknown PTR name", "9.2.0.192.in-addr.arpa.", dnsmessage.TypePTR, dnsmessage.RCodeNameError, 0},
		{"a TXT name asked as PTR", txtName, dnsmessage.TypePTR, dnsmessage.RCodeNameError, 0},
		{"a PTR name asked as TXT", ptrName, dnsmessage.TypeTXT, dnsmessage.RCodeNameError, 0},
		{"a type the server does not hold", txtName, dnsmessage.TypeA, dnsmessage.RCodeNameError, 0},
		{"the name is matched without regard to case", strings.ToUpper(txtName), dnsmessage.TypeTXT, dnsmessage.RCodeSuccess, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := query(t, s, tc.question, tc.typ)
			if reply.Header.RCode != tc.wantRCode {
				t.Errorf("response code = %v, want %v", reply.Header.RCode, tc.wantRCode)
			}
			if len(reply.Answers) != tc.wantAnswers {
				t.Errorf("answers = %d, want %d", len(reply.Answers), tc.wantAnswers)
			}
			if reply.Header.ID != 0x1234 || !reply.Header.Response || !reply.Header.RecursionDesired {
				t.Errorf("header = %+v, want the query's ID and recursion flag on a response", reply.Header)
			}
			if len(reply.Questions) != 1 || reply.Questions[0].Type != tc.typ {
				t.Errorf("questions = %+v, want the one asked", reply.Questions)
			}
		})
	}
}

func TestServer_UnparsableQueryGetsNoAnswer(t *testing.T) {
	s := Start(t, testRecords())
	for name, pkt := range map[string][]byte{
		"empty":       {},
		"short":       {0x12, 0x34, 0x01},
		"no question": {0x12, 0x34, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
	} {
		if out, ok := s.answer(pkt); ok {
			t.Errorf("%s packet: answered with %d bytes, want no answer", name, len(out))
		}
	}
}

// AskedTXT lists TXT questions in order, answered or not, and nothing else.
func TestServer_AskedTXT(t *testing.T) {
	s := Start(t, testRecords())
	if got := s.AskedTXT(); len(got) != 0 {
		t.Fatalf("AskedTXT before any question = %v, want none", got)
	}
	query(t, s, txtName, dnsmessage.TypeTXT)
	query(t, s, ptrName, dnsmessage.TypePTR)
	query(t, s, "Unknown.Example.", dnsmessage.TypeTXT)

	got := s.AskedTXT()
	want := []string{txtName, "unknown.example."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AskedTXT = %q, want %q", got, want)
	}
	// The slice is the caller's: changing it does not change the record.
	got[0] = "changed"
	if again := s.AskedTXT(); again[0] != txtName {
		t.Errorf("AskedTXT after the caller changed its copy = %q, want %q first", again, txtName)
	}
}

func TestClose_RestoresTheResolverAndStopsTheServer(t *testing.T) {
	before := net.DefaultResolver
	s := Start(t, testRecords())
	if net.DefaultResolver == before {
		t.Fatal("Start left net.DefaultResolver unchanged")
	}
	addr := s.conn.LocalAddr().String()

	s.Close()
	if net.DefaultResolver != before {
		t.Error("Close did not put back the resolver that was in place at Start")
	}
	// The socket is closed: a second listener can take the same port.
	again, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Errorf("the server's port is still held after Close: %v", err)
	} else {
		again.Close()
	}
	s.Close() // a second Close, and the one the test's cleanup runs, are no-ops
	if net.DefaultResolver != before {
		t.Error("a second Close changed net.DefaultResolver")
	}
}

// Start registers Close with the test, so a test that never calls it still
// leaves the resolver as it found it.
func TestStart_ClosesWhenTheTestEnds(t *testing.T) {
	before := net.DefaultResolver
	t.Run("inner", func(t *testing.T) {
		Start(t, testRecords())
	})
	if net.DefaultResolver != before {
		t.Error("net.DefaultResolver was not restored when the test that started the server ended")
	}
}
