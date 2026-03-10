package probe

import "sync/atomic"

// AutoSelector wraps a primary and fallback protocol.
// Starts with primary (UDP), falls back to secondary (ICMP) on timeout.
type AutoSelector struct {
	primary  ProbeProtocol
	fallback ProbeProtocol
	switched int32 // atomic: 0 = primary, 1 = fallback
}

// NewAutoSelector creates an auto-selecting protocol wrapper.
func NewAutoSelector(primary, fallback ProbeProtocol) *AutoSelector {
	return &AutoSelector{primary: primary, fallback: fallback}
}

func (a *AutoSelector) active() ProbeProtocol {
	if atomic.LoadInt32(&a.switched) == 1 {
		return a.fallback
	}
	return a.primary
}

// Fallback switches to the fallback protocol. Thread-safe, one-way.
func (a *AutoSelector) Fallback() {
	atomic.StoreInt32(&a.switched, 1)
}

// HasSwitched returns true if the fallback was triggered.
func (a *AutoSelector) HasSwitched() bool {
	return atomic.LoadInt32(&a.switched) == 1
}

func (a *AutoSelector) Name() string           { return a.active().Name() }
func (a *AutoSelector) SupportsMultipath() bool { return a.active().SupportsMultipath() }

func (a *AutoSelector) BuildProbe(flowID, ttl, seq int, cfg Config) ([]byte, probeKey, error) {
	return a.active().BuildProbe(flowID, ttl, seq, cfg)
}

func (a *AutoSelector) IdentifyResponse(innerHeader []byte) (*probeKey, error) {
	return a.active().IdentifyResponse(innerHeader)
}

func (a *AutoSelector) IsDestReachedICMP(icmpType, icmpCode int) bool {
	return a.active().IsDestReachedICMP(icmpType, icmpCode)
}
