package asn

import (
	"net"
	"testing"
)

// ---------------------------------------------------------------------------
// parseOriginResponse
// ---------------------------------------------------------------------------

func TestParseOriginResponse(t *testing.T) {
	tests := []struct {
		name    string
		txt     string
		want    int
		wantErr bool
	}{
		{"valid single ASN", "13335 | 1.1.1.0/24 | US | arin | 2014-03-28", 13335, false},
		{"valid multiple ASNs first wins", "13335 15169 | 1.1.1.0/24 | US | arin | 2014-03-28", 13335, false},
		{"multi-ASN second not returned", "15169 13335 | 8.8.8.0/24 | US | arin | 2014-03-28", 15169, false},
		{"invalid non-numeric ASN", "abc | 1.1.1.0/24 | US | arin | 2014-03-28", 0, true},
		{"empty string", "", 0, true},
		{"missing middle fields", "13335 | | |", 13335, false},
		{"extra whitespace around ASN", "  13335  |  1.1.1.0/24  |  US  ", 13335, false},
		{"extra whitespace multi-ASN", "  13335  15169  |  1.1.1.0/24  |  US  ", 13335, false},
		{"no pipe separator", "13335", 13335, false},
		{"trailing newline", "13335 | 1.1.1.0/24\n", 13335, false},
		{"trailing space and newline", "13335 | 1.1.1.0/24 \n", 13335, false},
		{"only whitespace", "   ", 0, true},
		{"pipe only", "| 1.1.1.0/24 | US", 0, true},
		// Origin6 — same format, verify v6 lookups parse identically
		{"origin6 v6 response", "13335 | 2606:4700::/32 | US | arin | 2014-03-28", 13335, false},
		{"origin6 multi-ASN v6", "13335 15169 | 2606:4700::/32 | US | arin | 2014-03-28", 13335, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseOriginResponse(tt.txt)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseOriginResponse(%q) error = %v, wantErr %v", tt.txt, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseOriginResponse(%q) = %v, want %v", tt.txt, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseASNameResponse
// ---------------------------------------------------------------------------

func TestParseASNameResponse(t *testing.T) {
	tests := []struct {
		name string
		txt  string
		want string
	}{
		{"valid", "13335 | US | arin | 2014-03-28 | CLOUDFLARENET", "CLOUDFLARENET"},
		{"empty string", "", ""},
		{"unicode org name", "13335 | US | arin | 2014-03-28 | CLOUDFLARE, INC., U.S.", "CLOUDFLARE, INC., U.S."},
		{"extra whitespace in org", "13335 | US | arin | 2014-03-28 |  CLOUDFLARENET  ", "CLOUDFLARENET"},
		{"no pipe returns whole string trimmed", "CLOUDFLARENET", "CLOUDFLARENET"},
		{"trailing newline in org", "13335 | US | arin | 2014-03-28 | CLOUDFLARENET\n", "CLOUDFLARENET"}, // strings.TrimSpace trims the trailing newline
		{"org name contains pipe-like content", "13335 | US | arin | 2014-03-28 | FOO | BAR", "BAR"},
		{"single pipe only", "13335 |", ""},
		{"whitespace only last field", "13335 | US | arin |   ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseASNameResponse(tt.txt)
			if got != tt.want {
				t.Errorf("parseASNameResponse(%q) = %q, want %q", tt.txt, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FormatASN / FormatASNShort
// ---------------------------------------------------------------------------

func TestFormatASN(t *testing.T) {
	tests := []struct {
		name   string
		number int
		org    string
		want   string
	}{
		{"number and org", 13335, "CLOUDFLARENET", "AS13335 (CLOUDFLARENET)"},
		{"number only", 13335, "", "AS13335"},
		{"org only", 0, "CLOUDFLARENET", "CLOUDFLARENET"},
		{"both zero", 0, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatASN(tt.number, tt.org)
			if got != tt.want {
				t.Errorf("FormatASN(%d, %q) = %q, want %q", tt.number, tt.org, got, tt.want)
			}
		})
	}
}

func TestFormatASNShort(t *testing.T) {
	tests := []struct {
		name   string
		number int
		want   string
	}{
		{"positive ASN", 13335, "AS13335"},
		{"zero ASN", 0, ""},
		{"negative ASN", -1, ""},
		{"large ASN", 400644, "AS400644"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatASNShort(tt.number)
			if got != tt.want {
				t.Errorf("FormatASNShort(%d) = %q, want %q", tt.number, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ipVersion
// ---------------------------------------------------------------------------

func TestIPVersion(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want int
	}{
		{"IPv4", "192.0.2.1", 4},
		{"IPv4 loopback", "127.0.0.1", 4},
		// Documentation IPv6 (2001:db8::/32) — safe to use in tests
		{"IPv6 doc prefix", "2001:db8::1", 6},
		{"IPv6 loopback", "::1", 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("net.ParseIP(%q) returned nil", tt.ip)
			}
			got := ipVersion(ip)
			if got != tt.want {
				t.Errorf("ipVersion(%q) = %d, want %d", tt.ip, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Submit edge cases
// ---------------------------------------------------------------------------

func TestSubmitCachesPrivateIPs(t *testing.T) {
	e := New(1)
	ip := net.ParseIP("192.168.1.1")
	ok := e.Submit(ip)
	if ok {
		t.Error("Submit(private IP) should return false (cached immediately, not queued)")
	}
	info, found := e.Lookup(ip)
	if !found {
		t.Fatal("private IP should be in cache after Submit")
	}
	if info.Org != "Private" {
		t.Errorf("private IP Org = %q, want %q", info.Org, "Private")
	}
	if info.Number != 0 {
		t.Errorf("private IP Number = %d, want 0", info.Number)
	}
}

// TestSubmitPrivateLoopback verifies loopback is treated as private.
func TestSubmitPrivateLoopback(t *testing.T) {
	e := New(1)
	ip := net.ParseIP("127.0.0.1")
	ok := e.Submit(ip)
	if ok {
		t.Error("Submit(127.0.0.1) should return false — loopback is private")
	}
	info, found := e.Lookup(ip)
	if !found {
		t.Fatal("loopback IP should be in cache after Submit")
	}
	if info.Org != "Private" {
		t.Errorf("loopback Org = %q, want %q", info.Org, "Private")
	}
}

// TestSubmitPrivateV6UniqueLocal uses fc00::/7 which is private.
func TestSubmitPrivateV6UniqueLocal(t *testing.T) {
	e := New(1)
	ip := net.ParseIP("fc00::1")
	ok := e.Submit(ip)
	if ok {
		t.Error("Submit(fc00::1) should return false — unique local is private")
	}
	info, found := e.Lookup(ip)
	if !found {
		t.Fatal("fc00::1 should be in cache after Submit")
	}
	if info.Org != "Private" {
		t.Errorf("fc00::1 Org = %q, want %q", info.Org, "Private")
	}
}

func TestSubmitDedup(t *testing.T) {
	e := New(1)
	ip := net.ParseIP("192.0.2.1")
	// Pre-populate cache to simulate already-resolved
	e.cache.Store(ip.String(), Info{Number: 13335, Org: "CLOUDFLARENET"})

	ok := e.Submit(ip)
	if ok {
		t.Error("Submit(already cached IP) should return false")
	}
}

// TestSubmitPendingDedup verifies that a second Submit while the first is
// still in-flight (pending, not yet cached) returns false.
func TestSubmitPendingDedup(t *testing.T) {
	e := New(0) // 0 workers — nothing drains the channel
	ip := net.ParseIP("192.0.2.2")

	first := e.Submit(ip)
	if !first {
		t.Fatal("first Submit should return true (newly submitted)")
	}
	second := e.Submit(ip)
	if second {
		t.Error("second Submit for same IP (pending) should return false")
	}
}

// TestSubmitChannelFull verifies graceful drop when the request channel is full.
func TestSubmitChannelFull(t *testing.T) {
	e := &Enricher{
		workers:    0,
		reqCh:      make(chan net.IP, 1), // capacity 1
		lookupFunc: lookupCymru,
	}

	ip1 := net.ParseIP("192.0.2.10")
	ip2 := net.ParseIP("192.0.2.11")

	first := e.Submit(ip1) // fills the channel
	if !first {
		t.Fatal("first Submit should return true")
	}

	second := e.Submit(ip2) // channel full — should drop gracefully
	if second {
		t.Error("Submit when channel full should return false")
	}

	// ip2 should NOT be in pending after being dropped
	_, inPending := e.pending.Load(ip2.String())
	if inPending {
		t.Error("dropped IP should be removed from pending map")
	}
}

func TestSubmitNil(t *testing.T) {
	e := New(1)
	ok := e.Submit(nil)
	if ok {
		t.Error("Submit(nil) should return false")
	}
}

// ---------------------------------------------------------------------------
// Lookup
// ---------------------------------------------------------------------------

func TestLookupMiss(t *testing.T) {
	e := New(1)
	_, found := e.Lookup(net.ParseIP("192.0.2.1"))
	if found {
		t.Error("Lookup on empty cache should return false")
	}
}

func TestLookupHit(t *testing.T) {
	e := New(1)
	ip := net.ParseIP("192.0.2.1")
	want := Info{Number: 64496, Org: "EXAMPLE-ASN"}
	e.cache.Store(ip.String(), want)

	got, found := e.Lookup(ip)
	if !found {
		t.Fatal("Lookup should find pre-populated cache entry")
	}
	if got != want {
		t.Errorf("Lookup() = %+v, want %+v", got, want)
	}
}
