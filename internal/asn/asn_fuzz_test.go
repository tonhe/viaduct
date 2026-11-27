package asn

import "testing"

func FuzzParseOriginResponse(f *testing.F) {
	f.Add("64496 | 192.0.2.0/24 | US | arin | 2014-03-28")
	f.Add("64496 64497 | 192.0.2.0/24 | US | arin | 2014-03-28") // multi
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
	f.Add("64496 | US | arin | 2014-03-28 | EXAMPLE-NET")
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
