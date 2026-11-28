package tui

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The header line, the separator and the status bar fit the terminal at every
// width, in every view (viaduct#68). The hop table already trims its columns;
// these three lines were built as one string and rendered at whatever length
// resulted, so they wrapped and pushed the table down.

var fitWidths = []int{40, 60, 72, 80, 120}

type barCase struct {
	name string
	m    func(w int) Model
}

func barCases() []barCase {
	v6 := func(m Model) Model {
		m.sessions[0].target = "a-rather-long-hostname.subdomain.example.net"
		m.sessions[0].targetIP = net.ParseIP("2001:db8:1234:5678:9abc:def0:1234:5678")
		return m
	}
	return []barCase{
		{"single target, IPv4", func(w int) Model { return multiTargetModel(w, 30, 1, 12) }},
		{"single target, 39-character IPv6", func(w int) Model { return v6(multiTargetModel(w, 30, 1, 12)) }},
		{"summary view, IPv4", func(w int) Model { return multiTargetModel(w, 30, 3, 12) }},
		{"summary view, IPv6", func(w int) Model { return v6(multiTargetModel(w, 30, 3, 12)) }},
		{"column view, IPv4", func(w int) Model {
			m := multiTargetModel(w, 30, 3, 12)
			m.columnView = true
			return m
		}},
		{"column view, IPv6", func(w int) Model {
			m := v6(multiTargetModel(w, 30, 3, 12))
			m.columnView = true
			return m
		}},
		{"Probe Detail, IPv4", func(w int) Model {
			m := multiTargetModel(w, 30, 1, 12)
			m.viewMode = ViewProbeDetail
			return m
		}},
		{"Probe Detail, IPv6, several targets", func(w int) Model {
			m := v6(multiTargetModel(w, 30, 3, 12))
			m.viewMode = ViewProbeDetail
			return m
		}},
		{"scrolled, with a notice", func(w int) Model {
			m := multiTargetModel(w, 12, 3, 25)
			m.autoScroll = false
			m.scrollOffset = 2
			m.sessions[0].notice = "no IPv6 route to 2001:db8::1; using IPv4"
			return m
		}},
	}
}

func TestBars_FitTheTerminal(t *testing.T) {
	for _, tc := range barCases() {
		for _, w := range fitWidths {
			t.Run(fmt.Sprintf("%s/%d", tc.name, w), func(t *testing.T) {
				lines := strings.Split(strings.TrimRight(tc.m(w).View(), "\n"), "\n")
				if len(lines) < 3 {
					t.Fatalf("view has %d lines", len(lines))
				}
				for _, part := range []struct {
					what string
					line string
				}{
					{"header", lines[0]},
					{"separator", lines[1]},
					{"status bar", lines[len(lines)-1]},
				} {
					if got := lipgloss.Width(part.line); got > w {
						t.Errorf("%s is %d columns wide at width %d: %q", part.what, got, w, stripANSI(part.line))
					}
				}
			})
		}
	}
}

// What must survive when a bar is shortened.
func TestBars_KeepWhatMatters(t *testing.T) {
	m := multiTargetModel(60, 12, 3, 25)
	m.autoScroll = false
	m.scrollOffset = 2
	m.sessions[0].notice = "no IPv6 route to 2001:db8::1; using IPv4"
	lines := strings.Split(strings.TrimRight(stripANSI(m.View()), "\n"), "\n")
	header, status := lines[0], lines[len(lines)-1]

	if !strings.Contains(header, "t00.example") {
		t.Errorf("header lost the target: %q", header)
	}
	if !strings.Contains(status, "Tracing") || !strings.Contains(status, "q:quit") {
		t.Errorf("status bar must keep the trace status and q:quit: %q", status)
	}
	// A setup notice outranks the key hints.
	if strings.Contains(status, "j/k:scroll") && !strings.Contains(status, "no IPv6 route") {
		t.Errorf("status bar kept key hints and dropped the notice: %q", status)
	}
}

// Wide enough for everything: the lines are exactly as before.
func TestBars_UnchangedWhenTheyFit(t *testing.T) {
	m := multiTargetModel(200, 30, 1, 12)
	lines := strings.Split(strings.TrimRight(stripANSI(m.View()), "\n"), "\n")
	wantHeader := "via " + m.version + "   Target: t00.example (192.0.2.1)    Proto: UDP/ECMP    Probes: 0"
	if got := strings.TrimRight(lines[0], " "); got != wantHeader {
		t.Errorf("header = %q, want %q", got, wantHeader)
	}
	status := strings.TrimRight(lines[len(lines)-1], " ")
	for _, want := range []string{"Tracing... 12 hops    Elapsed: ", "    DNS: ", "    6 flows", "    j/k:scroll e:export p:pause r:reset [d] ", " q:quit"} {
		if !strings.Contains(status, want) {
			t.Errorf("status bar %q is missing %q", status, want)
		}
	}
}

// The status bar's hop count is pluralised: "1 hop", otherwise "N hops"
// (viaduct#143).
func TestTraceStatus_PluralisesTheHopCount(t *testing.T) {
	tests := []struct {
		hops int
		want string
	}{
		{0, "Tracing... 0 hops"},
		{1, "Tracing... 1 hop"},
		{2, "Tracing... 2 hops"},
		{12, "Tracing... 12 hops"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.hops), func(t *testing.T) {
			m := multiTargetModel(200, 30, 1, tt.hops)
			if got := m.traceStatus(m.focused()); got != tt.want {
				t.Errorf("traceStatus = %q, want %q", got, tt.want)
			}
			// The rendered status bar carries the same text, followed by
			// the next segment and not by a stray "s".
			lines := strings.Split(strings.TrimRight(stripANSI(m.View()), "\n"), "\n")
			status := lines[len(lines)-1]
			if !strings.HasPrefix(status, tt.want+"    ") {
				t.Errorf("status bar %q does not start with %q", status, tt.want)
			}
		})
	}
}

// A paused trace shows PAUSED whatever the hop count.
func TestTraceStatus_Paused(t *testing.T) {
	m := multiTargetModel(200, 30, 1, 1)
	m.paused = true
	if got := m.traceStatus(m.focused()); got != "PAUSED" {
		t.Errorf("traceStatus = %q, want PAUSED", got)
	}
}
