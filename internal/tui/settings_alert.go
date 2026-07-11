package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tonhe/viaduct/internal/theme"
)

const numAlertItems = 5 // 0:AlertEnabled 1:AlertLoss 2:AlertLatency 3:AlertRounds 4:Back

func (s SettingsModel) updateAlert(msg tea.KeyMsg) (SettingsModel, tea.Cmd) {
	// When a field is focused, route keys to input handling
	if s.focusedInput >= 0 {
		switch msg.String() {
		case "esc":
			s.focusedInput = -1
			s.inputBuffer = ""
		case "enter":
			s.inputBuffer = strings.TrimSpace(s.inputBuffer)
			switch s.focusedInput {
			case 1: // AlertLoss — float, 0–100
				if v, err := strconv.ParseFloat(s.inputBuffer, 64); err == nil && v >= 0 && v <= 100 {
					s.config.AlertLoss = v
				}
			case 2: // AlertLatency — duration string
				if s.inputBuffer != "" {
					s.config.AlertLatency = s.inputBuffer
				}
			case 3: // AlertRounds — int, 1–20
				if v, err := strconv.Atoi(s.inputBuffer); err == nil && v >= 1 && v <= 20 {
					s.config.AlertRounds = v
				}
			}
			s.focusedInput = -1
			s.inputBuffer = ""
		case "backspace":
			if len(s.inputBuffer) > 0 {
				s.inputBuffer = s.inputBuffer[:len(s.inputBuffer)-1]
			}
		default:
			if len(msg.String()) == 1 {
				s.inputBuffer += msg.String()
			}
		}
		return s, nil
	}

	switch msg.String() {
	case "j", "down":
		s.alertCursor++
		if s.alertCursor >= numAlertItems {
			s.alertCursor = numAlertItems - 1
		}
	case "k", "up":
		s.alertCursor--
		if s.alertCursor < 0 {
			s.alertCursor = 0
		}
	case "enter", " ":
		switch s.alertCursor {
		case 0: // AlertEnabled toggle
			s.config.AlertEnabled = !s.config.AlertEnabled
		case 1: // AlertLoss — focus
			s.focusedInput = 1
			s.inputBuffer = fmt.Sprintf("%.1f", s.config.AlertLoss)
		case 2: // AlertLatency — focus
			s.focusedInput = 2
			s.inputBuffer = s.config.AlertLatency
		case 3: // AlertRounds — focus
			s.focusedInput = 3
			s.inputBuffer = strconv.Itoa(s.config.AlertRounds)
		case 4: // Back
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

func (s SettingsModel) viewAlert() string {
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
	mutedStyle := lipgloss.NewStyle().Foreground(theme.Current.Base04).Background(bg)
	valueStyle := lipgloss.NewStyle().Foreground(theme.Current.Base0D).Background(bg)
	inputStyle := lipgloss.NewStyle().Foreground(theme.Current.Base0A).Background(bg)
	footerStyle := lipgloss.NewStyle().Foreground(theme.Current.Base04).Background(bg)

	alertDisabled := !s.config.AlertEnabled

	type alertItem struct {
		label    string
		value    string
		isToggle bool
		toggled  bool
	}

	lossVal := fmt.Sprintf("%.1f%%", s.config.AlertLoss)
	latencyVal := s.config.AlertLatency
	roundsVal := strconv.Itoa(s.config.AlertRounds)

	if s.focusedInput == 1 {
		lossVal = s.inputBuffer + "│"
	}
	if s.focusedInput == 2 {
		latencyVal = s.inputBuffer + "│"
	}
	if s.focusedInput == 3 {
		roundsVal = s.inputBuffer + "│"
	}

	items := []alertItem{
		{label: "Alerting Enabled", isToggle: true, toggled: s.config.AlertEnabled},
		{label: "Loss Threshold", value: lossVal},
		{label: "Latency Threshold", value: latencyVal},
		{label: "Alert Rounds", value: roundsVal},
		{label: "Back", value: ""},
	}

	var rows []string
	rows = append(rows, titleStyle.Render("Alerting"))
	rows = append(rows, dimmedStyle.Render(strings.Repeat("─", innerW)))

	for i, item := range items {
		// Dim non-toggle items when alerting is disabled (except Back)
		isDimmed := alertDisabled && i > 0 && i < 4

		cursor := "  "
		if i == s.alertCursor {
			cursor = "► "
		}

		// Determine plain text for the value
		valText := item.value
		if item.isToggle {
			if item.toggled {
				valText = "[on]"
			} else {
				valText = "[off]"
			}
		}
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

		if i == s.alertCursor {
			rows = append(rows, highlightStyle.Width(innerW).Render(plainLine))
		} else if i == s.focusedInput {
			line := normalStyle.Render(labelPart+strings.Repeat(" ", spacer)) + inputStyle.Render(valText)
			rows = append(rows, line)
		} else if item.isToggle {
			line := normalStyle.Render(labelPart+strings.Repeat(" ", spacer)) + renderToggle(item.toggled, bg)
			rows = append(rows, line)
		} else if isDimmed {
			rows = append(rows, mutedStyle.Render(plainLine))
		} else if valText != "" {
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
