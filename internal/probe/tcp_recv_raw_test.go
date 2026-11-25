//go:build !darwin

package probe

import (
	"net"
	"strings"
	"testing"
)

// Off macOS the TCP listener reads the raw socket the probes are sent on, and
// nothing else is opened.
func TestOpenTCPSource_IsTheRawSendSocket(t *testing.T) {
	var logged []string
	cfg := DefaultConfig()
	cfg.Debugf = func(format string, args ...any) {
		logged = append(logged, format)
		for _, a := range args {
			if s, ok := a.(string); ok {
				logged = append(logged, s)
			}
		}
	}
	tr := NewTracer(net.ParseIP("192.0.2.1"), cfg)
	raw := &senderConn{}
	src, cleanup, err := tr.tcpSource(raw)
	if err != nil {
		t.Fatalf("tcpSource: %v", err)
	}
	if src != tcpSegmentSource(raw) {
		t.Errorf("source = %T %p, want the raw socket %p", src, src, raw)
	}
	cleanup() // nothing to close; must not panic
	if !strings.Contains(strings.Join(logged, " "), "raw TCP send socket") {
		t.Errorf("debug log does not say where replies are read from: %q", logged)
	}
}
