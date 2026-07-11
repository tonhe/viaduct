package probe

import "testing"

// FuzzParseTCPResponse stress-tests the raw TCP response parser.
func FuzzParseTCPResponse(f *testing.F) {
	f.Add([]byte{0xc3, 0x50, 0x01, 0xbb, 0, 0, 0, 1, 0, 0, 0, 0, 0x50, 0x12, 0xff, 0xff, 0, 0, 0, 0})
	f.Add([]byte{0x00, 0x00, 0x00, 0x00}) // too short
	f.Add([]byte{})                        // empty
	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on %x: %v", data, r)
			}
		}()
		_, _ = parseTCPResponse(data)
	})
}
