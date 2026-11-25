package tui

import (
	"errors"
	"net"
	"testing"
)

// A run error belongs to one target. With several targets, that target shows
// its error and the others keep tracing; the session ends only when there is
// nothing left to trace. A single-target session ends with the error, as it
// always has.

func threeSessionModel() Model {
	m := newTestModel()
	cfg := m.sessions[0].probeCfg
	m.sessions = append(m.sessions,
		newSession("second.example", net.ParseIP("198.51.100.2"), cfg, "udp", 5.0, 0, 3, true),
		newSession("third.example", net.ParseIP("198.51.100.3"), cfg, "udp", 5.0, 0, 3, true))
	return m
}

func TestOnProbeError_OneTargetOfSeveralDoesNotQuit(t *testing.T) {
	m := threeSessionModel()
	runErr := errors.New("raw udp socket: operation not permitted")

	m2, cmd := m.onProbeError(ProbeErrorMsg{TargetIdx: 1, Err: runErr})

	cmdIsNil(t, cmd)
	if m2.err != nil {
		t.Errorf("model error = %v; one target's failure must not end the session", m2.err)
	}
	if got := m2.sessions[1].startErr; got == nil || got.Error() != runErr.Error() {
		t.Errorf("target 1 error = %v, want %v shown on its row", got, runErr)
	}
	if m2.sessions[0].startErr != nil || m2.sessions[2].startErr != nil {
		t.Error("the other targets were marked failed")
	}
}

func TestOnProbeError_LastHealthyTargetFailingQuits(t *testing.T) {
	m := threeSessionModel()
	first := errors.New("first failure")
	m, _ = m.onProbeError(ProbeErrorMsg{TargetIdx: 0, Err: first})
	m, _ = m.onProbeError(ProbeErrorMsg{TargetIdx: 1, Err: errors.New("second failure")})
	last := errors.New("third failure")

	m2, cmd := m.onProbeError(ProbeErrorMsg{TargetIdx: 2, Err: last})

	cmdIsQuit(t, cmd)
	if m2.err == nil {
		t.Error("model error is nil after every target failed")
	}
}

func TestOnProbeError_UnknownTargetIndexIsIgnored(t *testing.T) {
	m := threeSessionModel()
	m2, cmd := m.onProbeError(ProbeErrorMsg{TargetIdx: 9, Err: errors.New("stray")})
	cmdIsNil(t, cmd)
	if m2.err != nil {
		t.Errorf("model error = %v for a message that names no session", m2.err)
	}
}
