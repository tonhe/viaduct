package tui

import (
	"net"
	"time"

	"github.com/tonhe/viaduct/internal/config"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/probe"
)

// echoSinkIface is the narrow interface the session uses to submit echo targets
// each round. *probe.Tracer satisfies this; tests inject a fake.
type echoSinkIface interface {
	SubmitEchoTargets([]probe.EchoTarget)
}

// TargetSession holds all state for one trace target. Model owns one
// session per target and routes messages to them by index.
type TargetSession struct {
	target           string
	targetIP         net.IP
	table            *hop.Table
	tracer           *probe.Tracer
	echoSink         echoSinkIface // submits echo targets each round; default is tracer
	probeCfg         probe.Config
	probeCount       int
	targetHit        bool
	maxTTLHit        int
	protocolName     string
	switchStatusMsg  string
	switchStatusTime time.Time
	alertEngine      *AlertEngine
	startErr         error // resolution/family/socket failure; session never traces
	marked           bool  // comparison mark for column view
	notice           string // setup notice shown in the status bar (TargetSpec.Notice)
}

// newSession creates a TargetSession with the per-target initial state that
// New() previously built inline. All arguments mirror the New() signature.
func newSession(target string, targetIP net.IP, cfg probe.Config, protocolName string, alertLoss float64, alertLatency time.Duration, alertRounds int, noAlert bool) *TargetSession {
	var ae *AlertEngine
	if !noAlert {
		ae = NewAlertEngine(alertLoss, alertLatency, alertRounds)
	}
	return &TargetSession{
		target:       target,
		targetIP:     targetIP,
		table:        hop.NewTable(cfg.MaxHops),
		probeCfg:     cfg,
		protocolName: protocolName,
		alertEngine:  ae,
	}
}

// TargetSpec describes one target for NewMulti. If Err is non-nil the
// session is created in an errored state and never traces.
type TargetSpec struct {
	Name         string
	IP           net.IP
	Cfg          probe.Config
	ProtocolName string
	Err          error
	// Notice is a one-line message about how the target was set up that the
	// user should see while tracing, for example that IPv4 is being used
	// because there is no IPv6 route. Empty for none.
	Notice string
}

// NewMulti creates a Model with one session per spec. Shared resolver,
// enricher, config, and view state live on the Model exactly as in New.
// specs must contain 1 to 16 entries; all sessions share the same alert thresholds.
func NewMulti(specs []TargetSpec, version string, noASN bool, alertLoss float64, alertLatency time.Duration, alertRounds int, noAlert bool, appCfg *config.Config) Model {
	if len(specs) == 0 {
		panic("NewMulti: specs must not be empty")
	}
	m := New(specs[0].Name, specs[0].IP, specs[0].Cfg, version, specs[0].ProtocolName, noASN, alertLoss, alertLatency, alertRounds, noAlert, appCfg)
	// New builds sessions[0] unconditionally; patch its error state after the fact.
	m.sessions[0].startErr = specs[0].Err
	m.sessions[0].notice = specs[0].Notice
	for _, sp := range specs[1:] {
		s := newSession(sp.Name, sp.IP, sp.Cfg, sp.ProtocolName, alertLoss, alertLatency, alertRounds, noAlert)
		s.startErr = sp.Err
		s.notice = sp.Notice
		m.sessions = append(m.sessions, s)
	}
	return m
}
