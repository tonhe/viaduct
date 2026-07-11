package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tonhe/viaduct/internal/theme"
)

const numProbeItems = 6 // 0:Protocol 1:IPFamily 2:MaxHops 3:Interval 4:Paths 5:Back

var protocolOptions = []string{"udp", "icmp", "tcp", "auto"}

func cycleProtocol(current string) string {
	for i, p := range protocolOptions {
		if p == current {
			return protocolOptions[(i+1)%len(protocolOptions)]
		}
	}
	return protocolOptions[0]
}

func (s SettingsModel) updateProbe(msg tea.KeyMsg) (SettingsModel, tea.Cmd) {
	// When a field is focused, route keys to input handling
	if s.focusedInput >= 0 {
		switch msg.String() {
		case "esc":
			s.focusedInput = -1
			s.inputBuffer = ""
		case "enter":
			s.inputBuffer = strings.TrimSpace(s.inputBuffer)
			switch s.focusedInput {
			case 2: // MaxHops
				if v, err := strconv.Atoi(s.inputBuffer); err == nil && v >= 1 && v <= 64 {
					s.config.MaxHops = v
				}
			case 3: // Interval
				if s.inputBuffer != "" {
					s.config.Interval = s.inputBuffer
				}
			case 4: // Paths
				if v, err := strconv.Atoi(s.inputBuffer); err == nil && v >= 1 && v <= 32 {
					s.config.Paths = v
				}
			}
			s.focusedInput = -1
			s.inputBuffer = ""
		case "backspace":
			if len(s.inputBuffer) > 0 {
				s.inputBuffer = s.inputBuffer[:len(s.inputBuffer)-1]
			}
		default:
			// Accept printable characters
			if len(msg.String()) == 1 {
				s.inputBuffer += msg.String()
			}
		}
		return s, nil
	}

	// No field focused — handle navigation
	switch msg.String() {
	case "j", "down":
		s.probeCursor++
		if s.probeCursor >= numProbeItems {
			s.probeCursor = numProbeItems - 1
		}
	case "k", "up":
		s.probeCursor--
		if s.probeCursor < 0 {
			s.probeCursor = 0
		}
	case "enter", " ":
		switch s.probeCursor {
		case 0: // Protocol — cycle
			s.config.Protocol = cycleProtocol(s.config.Protocol)
		case 1: // IP family — cycle
			switch s.config.IPFamily {
			case "auto", "":
				s.config.IPFamily = "4"
			case "4":
				s.config.IPFamily = "6"
			case "6":
				s.config.IPFamily = "auto"
			}
		case 2: // MaxHops — focus
			s.focusedInput = 2
			s.inputBuffer = strconv.Itoa(s.config.MaxHops)
		case 3: // Interval — focus
			s.focusedInput = 3
			s.inputBuffer = s.config.Interval
		case 4: // Paths — focus
			s.focusedInput = 4
			s.inputBuffer = strconv.Itoa(s.config.Paths)
		case 5: // Back
			s.state = settingsMain
			s.focusedInput = -1
			s.inputBuffer = ""
		}
	case "esc":
		s.state = settingsMain
		s.focusedInput = -1
		s.inputBuffer = ""
	case "ctrl+s":
		t := theme.ByIndex(s.themeIndex)
		if t != nil {
			s.config.Theme = t.Slug
		}
		return s, settingsSaveCmd(s.config)
	}
	return s, nil
}

func (s SettingsModel) viewProbe() string {
	boxW := s.overlayWidth()
	innerW := boxW - 4
	if innerW < 10 {
		innerW = 10
	}

	borderColor := settingsBorderColor()
	bg := theme.Current.Base01
	titleStyle := lipgloss.NewStyle().Foreground(theme.Current.Base06).Background(bg).Bold(true)
	highlightStyle := lipgloss.NewStyle().
		Foreground(theme.Current.Base06).
		Background(theme.Current.Base02).
		Bold(true)
	dimmedStyle := lipgloss.NewStyle().Foreground(theme.Current.Base04).Background(bg)
	normalStyle := lipgloss.NewStyle().Foreground(theme.Current.Base05).Background(bg)
	valueStyle := lipgloss.NewStyle().Foreground(theme.Current.Base0D).Background(bg)
	footerStyle := lipgloss.NewStyle().Foreground(theme.Current.Base04).Background(bg)
	inputStyle := lipgloss.NewStyle().Foreground(theme.Current.Base0A).Background(bg)

	type probeItem struct {
		label string
		value string
	}

	// Build value strings
	protocolVal := fmt.Sprintf("[%s]", s.config.Protocol)

	ipFamilyVal := "[auto]"
	switch s.config.IPFamily {
	case "4":
		ipFamilyVal = "[IPv4]"
	case "6":
		ipFamilyVal = "[IPv6]"
	}

	maxHopsVal := strconv.Itoa(s.config.MaxHops)
	intervalVal := s.config.Interval
	pathsVal := strconv.Itoa(s.config.Paths)

	// When a field is focused, show the input buffer
	if s.focusedInput == 2 {
		maxHopsVal = s.inputBuffer + "│"
	}
	if s.focusedInput == 3 {
		intervalVal = s.inputBuffer + "│"
	}
	if s.focusedInput == 4 {
		pathsVal = s.inputBuffer + "│"
	}

	items := []probeItem{
		{label: "Protocol", value: protocolVal},
		{label: "IP family", value: ipFamilyVal},
		{label: "Max Hops", value: maxHopsVal},
		{label: "Interval", value: intervalVal},
		{label: "ECMP Paths", value: pathsVal},
		{label: "Back", value: ""},
	}

	var rows []string
	rows = append(rows, titleStyle.Render("Probe Settings"))
	rows = append(rows, dimmedStyle.Render(strings.Repeat("─", innerW)))

	for i, item := range items {
		cursor := "  "
		if i == s.probeCursor {
			cursor = "► "
		}

		// Plain text value (no ANSI)
		valText := item.value

		// Show input cursor when focused
		if i == s.focusedInput {
			valText = s.inputBuffer + "│"
		}

		labelPart := cursor + item.label
		labelLen := len(labelPart)
		valLen := len(valText)
		spacer := innerW - labelLen - valLen
		if spacer < 1 {
			spacer = 1
		}
		plainLine := labelPart + strings.Repeat(" ", spacer) + valText

		if i == s.probeCursor {
			rows = append(rows, highlightStyle.Width(innerW).Render(plainLine))
		} else if i == s.focusedInput {
			// Focused input: render value in input color
			line := normalStyle.Render(labelPart+strings.Repeat(" ", spacer)) + inputStyle.Render(valText)
			rows = append(rows, line)
		} else if valText != "" {
			// Normal with value
			line := normalStyle.Render(labelPart+strings.Repeat(" ", spacer)) + valueStyle.Render(valText)
			rows = append(rows, line)
		} else {
			rows = append(rows, normalStyle.Render(plainLine))
		}
	}

	rows = append(rows, "")
	if s.focusedInput >= 0 {
		rows = append(rows, footerStyle.Render("Type value • Enter confirm • Esc cancel"))
	} else {
		rows = append(rows, footerStyle.Render("↑↓ navigate • Enter edit • Esc back"))
	}

	content := strings.Join(rows, "\n")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		BorderBackground(theme.Current.Base00).
		Background(theme.Current.Base01).
		Padding(0, 1).
		Width(boxW).
		Render(content)

	return s.placeOverlay(box)
}
