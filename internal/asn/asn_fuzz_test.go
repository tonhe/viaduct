package asn

import "testing"

func FuzzParseOriginResponse(f *testing.F) {
	f.Add("13335 | 1.1.1.0/24 | US | arin | 2014-03-28")
	f.Add("13335 15169 | 1.1.1.0/24 | US | arin | 2014-03-28") // multi
	f.Add("")                                                    // empty
	f.Add(" | | ")                                               // pipes only
	f.Add("notanumber | ...")                                    // malformed ASN
	f.Fuzz(func(t *testing.T, s string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on %q: %v", s, r)
			}
		}()
		_, _ = parseOriginResponse(s)
	})
}

func FuzzParseASNameResponse(f *testing.F) {
	f.Add("13335 | US | arin | 2014-03-28 | CLOUDFLARENET")
	f.Add("")
	f.Add("no pipes at all")
	f.Add("| | | |")
	f.Fuzz(func(t *testing.T, s string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on %q: %v", s, r)
			}
		}()
		_ = parseASNameResponse(s)
	})
}
