package tui

import (
	"net"
	"time"

	"github.com/tonhe/viaduct/internal/probe"
)

// HopUpdateMsg is sent when a probe response is received.
type HopUpdateMsg struct {
	Result probe.Result
}

// HostnameMsg is sent when async DNS resolves a hostname.
type HostnameMsg struct {
	IP       net.IP
	Hostname string
}

// TickMsg triggers a TUI re-render.
type TickMsg time.Time

// RoundEndMsg signals the end of a probe round for stability tracking.
type RoundEndMsg struct{}

// ProbeErrorMsg is sent when the probe engine fails to start.
type ProbeErrorMsg struct {
	Err error
}
