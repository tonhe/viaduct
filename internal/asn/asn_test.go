package asn

import (
	"net"
	"testing"
)

func TestParseOriginResponse(t *testing.T) {
	tests := []struct {
		name    string
		txt     string
		want    int
		wantErr bool
	}{
		{"valid single ASN", "13335 | 1.1.1.0/24 | US | arin | 2014-03-28", 13335, false},
		{"valid multiple ASNs", "13335 15169 | 1.1.1.0/24 | US | arin | 2014-03-28", 13335, false},
		{"invalid non-numeric", "abc | 1.1.1.0/24 | US | arin | 2014-03-28", 0, true},
		{"empty string", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseOriginResponse(tt.txt)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseOriginResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseOriginResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseASNameResponse(t *testing.T) {
	tests := []struct {
		name string
		txt  string
		want string
	}{
		{"valid", "13335 | US | arin | 2014-03-28 | CLOUDFLARENET", "CLOUDFLARENET"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseASNameResponse(tt.txt)
			if got != tt.want {
				t.Errorf("parseASNameResponse() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"10.x", "10.0.0.1", true},
		{"172.16.x", "172.16.0.1", true},
		{"172.31.x", "172.31.255.255", true},
		{"172.15.x (public)", "172.15.0.1", false},
		{"192.168.x", "192.168.1.1", true},
		{"127.x loopback", "127.0.0.1", true},
		{"169.254.x link-local", "169.254.1.1", true},
		{"public 1.1.1.1", "1.1.1.1", false},
		{"public 8.8.8.8", "8.8.8.8", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			got := isPrivateIP(ip)
			if got != tt.want {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

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

func TestSubmitDedup(t *testing.T) {
	e := New(1)
	ip := net.ParseIP("1.1.1.1")
	// Pre-populate cache
	e.cache.Store(ip.String(), Info{Number: 13335, Org: "CLOUDFLARENET"})

	ok := e.Submit(ip)
	if ok {
		t.Error("Submit(already cached IP) should return false")
	}
}

func TestSubmitNil(t *testing.T) {
	e := New(1)
	ok := e.Submit(nil)
	if ok {
		t.Error("Submit(nil) should return false")
	}
}

func TestLookupMiss(t *testing.T) {
	e := New(1)
	_, found := e.Lookup(net.ParseIP("1.1.1.1"))
	if found {
		t.Error("Lookup on empty cache should return false")
	}
}
