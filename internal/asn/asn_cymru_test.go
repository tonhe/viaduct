package asn

// Tests for the Team Cymru lookup itself: the names it queries, what it does
// with each answer, and the fields the two parsers take from a record.
//
// lookupCymru asks net.DefaultResolver. The tests point that resolver at a DNS
// server on the loopback interface that answers from a table, so no query
// leaves the host. Addresses are from RFC 5737 and RFC 3849, AS numbers from
// the documentation range of RFC 5398 (64496-64511).

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/dnstest"
)

// askedCymru returns the Team Cymru names the server was asked for, in order.
// A resolver may also try a failed name with the host's search domains
// appended; those questions are not Team Cymru names and are left out.
func askedCymru(dns *dnstest.Server) []string {
	var names []string
	for _, n := range dns.AskedTXT() {
		if strings.HasSuffix(n, ".asn.cymru.com.") {
			names = append(names, n)
		}
	}
	return names
}

const (
	docV4       = "192.0.2.1"
	docV4Origin = "1.2.0.192.origin.asn.cymru.com."
	docV6       = "2001:db8::1"
	// The 32 nibbles of 2001:db8::1, last first.
	docV6Origin = "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.origin6.asn.cymru.com."
	docASName   = "as64500.asn.cymru.com."

	originV4Record = "64500 | 192.0.2.0/24 | ZZ | iana | 2000-01-01"
	originV6Record = "64500 | 2001:db8::/32 | ZZ | iana | 2000-01-01"
	asNameRecord   = "64500 | ZZ | iana | 2000-01-01 | EXAMPLE-NET - Example Network, ZZ"
	exampleOrg     = "EXAMPLE-NET - Example Network, ZZ"
)

// lookupCymru asks for the origin record of the address, then for the name of
// the AS that record gives, and returns both. The names it asks for are the
// ones Team Cymru serves; a change to either is a change to what is looked up.
func TestLookupCymru(t *testing.T) {
	cases := []struct {
		name      string
		ip        string
		txt       map[string][]string
		want      Info
		wantAsked []string
	}{
		{
			name:      "IPv4: origin then AS name",
			ip:        docV4,
			txt:       map[string][]string{docV4Origin: {originV4Record}, docASName: {asNameRecord}},
			want:      Info{Number: 64500, Org: exampleOrg},
			wantAsked: []string{docV4Origin, docASName},
		},
		{
			name:      "IPv6: origin6 then AS name",
			ip:        docV6,
			txt:       map[string][]string{docV6Origin: {originV6Record}, docASName: {asNameRecord}},
			want:      Info{Number: 64500, Org: exampleOrg},
			wantAsked: []string{docV6Origin, docASName},
		},
		{
			name:      "several origin records: the first is used",
			ip:        docV4,
			txt:       map[string][]string{docV4Origin: {originV4Record, "64501 | 192.0.2.0/25 | ZZ | iana | 2000-01-01"}, docASName: {asNameRecord}},
			want:      Info{Number: 64500, Org: exampleOrg},
			wantAsked: []string{docV4Origin, docASName},
		},
		{
			// The empty-response path: the origin record exists and holds
			// nothing. No AS number, so no AS name is asked for.
			name:      "empty origin record",
			ip:        docV4,
			txt:       map[string][]string{docV4Origin: {""}, docASName: {asNameRecord}},
			want:      Info{},
			wantAsked: []string{docV4Origin},
		},
		{
			name:      "origin record without a number",
			ip:        docV4,
			txt:       map[string][]string{docV4Origin: {"NA | 192.0.2.0/24 | ZZ | iana | 2000-01-01"}, docASName: {asNameRecord}},
			want:      Info{},
			wantAsked: []string{docV4Origin},
		},
		{
			name:      "no origin record (NXDOMAIN)",
			ip:        docV4,
			txt:       map[string][]string{docASName: {asNameRecord}},
			want:      Info{},
			wantAsked: []string{docV4Origin},
		},
		{
			name:      "origin known, no AS name record: the number alone",
			ip:        docV4,
			txt:       map[string][]string{docV4Origin: {originV4Record}},
			want:      Info{Number: 64500},
			wantAsked: []string{docV4Origin, docASName},
		},
		{
			name:      "origin known, empty AS name record: the number alone",
			ip:        docV4,
			txt:       map[string][]string{docV4Origin: {originV4Record}, docASName: {""}},
			want:      Info{Number: 64500},
			wantAsked: []string{docV4Origin, docASName},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dns := dnstest.Start(t, dnstest.Records{TXT: tc.txt})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			got := lookupCymru(ctx, net.ParseIP(tc.ip))
			if got != tc.want {
				t.Errorf("lookupCymru(%s) = %+v, want %+v", tc.ip, got, tc.want)
			}
			asked := askedCymru(dns)
			if strings.Join(asked, " ") != strings.Join(tc.wantAsked, " ") {
				t.Errorf("names asked = %v, want %v", asked, tc.wantAsked)
			}
		})
	}
}

// The origin record's first field holds the AS number, or several separated by
// spaces. Nothing after the first "|" is read, with or without spaces around it.
func TestParseOriginResponse_Fields(t *testing.T) {
	cases := []struct {
		name string
		txt  string
		want int
	}{
		{"well-formed", "64500 | 192.0.2.0/24 | ZZ | iana | 2000-01-01", 64500},
		{"no spaces around the separators", "64500|192.0.2.0/24|ZZ|iana|2000-01-01", 64500},
		{"several ASNs, no space before the separator", "64500 64501|192.0.2.0/24|ZZ", 64500},
		{"short: number and prefix", "64500|192.0.2.0/24", 64500},
		{"short: number and an empty field", "64500|", 64500},
		{"short: the number alone", "64500", 64500},
		{"a number in a later field is not the ASN", "64500 | 64501 | 64502", 64500},
		{"IPv6 prefix", "64500|2001:db8::/32|ZZ|iana|2000-01-01", 64500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOriginResponse(tc.txt)
			if err != nil {
				t.Fatalf("parseOriginResponse(%q): %v", tc.txt, err)
			}
			if got != tc.want {
				t.Errorf("parseOriginResponse(%q) = %d, want %d", tc.txt, got, tc.want)
			}
		})
	}
}

// A record with no usable number is an error that says which part was wrong,
// and the number returned with it is zero.
func TestParseOriginResponse_Malformed(t *testing.T) {
	cases := []struct {
		name    string
		txt     string
		wantErr string
	}{
		{"empty record", "", "empty response"},
		{"empty first field", "| 192.0.2.0/24 | ZZ", "empty ASN field"},
		{"blank first field", "   |192.0.2.0/24", "empty ASN field"},
		{"only blanks", " \t ", "empty ASN field"},
		{"first field is not a number", "NA | 192.0.2.0/24 | ZZ", `invalid ASN "NA"`},
		{"AS prefix on the number", "AS64500 | 192.0.2.0/24", `invalid ASN "AS64500"`},
		{"the number is in the second field", "| 64500 | ZZ", "empty ASN field"},
		{"first of several is not a number", "x 64500 | 192.0.2.0/24", `invalid ASN "x"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOriginResponse(tc.txt)
			if err == nil {
				t.Fatalf("parseOriginResponse(%q) = %d, want an error", tc.txt, got)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("parseOriginResponse(%q) error = %q, want it to say %q", tc.txt, err, tc.wantErr)
			}
			if got != 0 {
				t.Errorf("parseOriginResponse(%q) returned %d with its error, want 0", tc.txt, got)
			}
		})
	}
}

// The AS name record's last field is the organisation. Every field before it
// is ignored, however many there are.
func TestParseASNameResponse_Fields(t *testing.T) {
	cases := []struct {
		name string
		txt  string
		want string
	}{
		{"well-formed", asNameRecord, exampleOrg},
		{"no spaces around the separators", "64500|ZZ|iana|2000-01-01|EXAMPLE-NET", "EXAMPLE-NET"},
		{"each field differs: the last one is returned", "first | second | third | fourth | fifth", "fifth"},
		{"short: two fields", "64500 | EXAMPLE-NET", "EXAMPLE-NET"},
		{"short: one field", "EXAMPLE-NET", "EXAMPLE-NET"},
		{"short: one field with blanks around it", "  EXAMPLE-NET\t", "EXAMPLE-NET"},
		{"spaces inside the organisation are kept", "64500 | ZZ | Example  Network", "Example  Network"},
		{"empty last field", "64500 | ZZ | iana | 2000-01-01 |", ""},
		{"only a separator", "|", ""},
		{"empty record", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseASNameResponse(tc.txt); got != tc.want {
				t.Errorf("parseASNameResponse(%q) = %q, want %q", tc.txt, got, tc.want)
			}
		})
	}
}
