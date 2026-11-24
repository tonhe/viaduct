package tui

import (
	"net"
	"time"

	"github.com/tonhe/viaduct/internal/probe"
)

// HopUpdateMsg is sent when a probe response is received.
type HopUpdateMsg struct {
	TargetIdx int
	Result    probe.Result
}

// HostnameMsg is sent when async DNS resolves a hostname.
type HostnameMsg struct {
	IP       net.IP
	Hostname string
}

// TickMsg triggers a TUI re-render.
type TickMsg time.Time

// RoundEndMsg signals the end of a probe round for stability tracking.
type RoundEndMsg struct {
	TargetIdx int
}

// ProbeErrorMsg is sent when the probe engine fails to start.
type ProbeErrorMsg struct {
	TargetIdx int
	Err       error
}

// ProtocolSwitchMsg is sent when auto mode switches to a different protocol.
type ProtocolSwitchMsg struct {
	TargetIdx   int
	NewProtocol string
}

// ASNMsg is sent when an ASN lookup completes.
type ASNMsg struct {
	IP     net.IP
	Number int
	Org    string
}

// ExportDoneMsg is sent when an export file has been written.
type ExportDoneMsg struct {
	Path string
	Err  error
}

// EchoSentMsg reports one echo probe dispatched toward a known {IP, TTL}
// pair, for per-node echo sent accounting.
type EchoSentMsg struct {
	TargetIdx int
	IP        net.IP
	TTL       int
}
