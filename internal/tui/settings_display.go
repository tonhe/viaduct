package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tonhe/viaduct/internal/theme"
)

const numDisplayItems = 5 // 0:DisplayMode 1:DNS 2:ASN 3:PingSupplement 4:Back

var displayModeOptions = []string{"default", "health", "latency", "variability"}

func cycleDisplayMode(current string) string {
	for i, m := range displayModeOptions {
		if m == current {
			return displayModeOptions[(i+1)%len(displayModeOptions)]
		}
	}
	return displayModeOptions[0]
}

func renderToggle(enabled bool, bg lipgloss.Color) string {
	okStyle := lipgloss.NewStyle().Foreground(theme.Current.Base0B).Background(bg)
	offStyle := lipgloss.NewStyle().Foreground(theme.Current.Base08).Background(bg)
	if enabled {
		return okStyle.Render("[on]")
	}
	return offStyle.Render("[off]")
}

func (s SettingsModel) updateDisplay(msg tea.KeyMsg) (SettingsModel, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		s.displayCursor++
		if s.displayCursor >= numDisplayItems {
			s.displayCursor = numDisplayItems - 1
		}
	case "k", "up":
		s.displayCursor--
		if s.displayCursor < 0 {
			s.displayCursor = 0
		}
	case "enter", " ":
		switch s.displayCursor {
		case 0: // DisplayMode — cycle
			s.config.DisplayMode = cycleDisplayMode(s.config.DisplayMode)
		case 1: // DNS toggle
			s.config.DNSLookups = !s.config.DNSLookups
		case 2: // ASN toggle
			s.config.ASNLookups = !s.config.ASNLookups
		case 3: // PingSupplement toggle
			s.config.PingSupplement = !s.config.PingSupplement
		case 4: // Back
			s.state = settingsMain
		}
	case "esc":
		s.state = settingsMain
	case "ctrl+s":
		t := theme.ByIndex(s.themeIndex)
		if t != nil {
			s.config.Theme = t.Slug
		}
		return s, settingsSaveCmd(s.config)
	}
	return s, nil
}

func (s SettingsModel) viewDisplay() string {
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

	type displayItem struct {
		label    string
		value    string
		isToggle bool
		toggled  bool
	}

	items := []displayItem{
		{label: "Display Mode", value: "[" + s.config.DisplayMode + "]"},
		{label: "DNS Lookups", isToggle: true, toggled: s.config.DNSLookups},
		{label: "ASN Lookups", isToggle: true, toggled: s.config.ASNLookups},
		{label: "Ping Supplement", isToggle: true, toggled: s.config.PingSupplement},
		{label: "Back", value: ""},
	}

	var rows []string
	rows = append(rows, titleStyle.Render("Display & Enrichment"))
	rows = append(rows, dimmedStyle.Render(strings.Repeat("─", innerW)))

	for i, item := range items {
		cursor := "  "
		if i == s.displayCursor {
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

		labelPart := cursor + item.label
		labelLen := len(labelPart)
		valLen := len(valText)
		spacer := innerW - labelLen - valLen
		if spacer < 1 {
			spacer = 1
		}
		plainLine := labelPart + strings.Repeat(" ", spacer) + valText

		if i == s.displayCursor {
			rows = append(rows, highlightStyle.Width(innerW).Render(plainLine))
		} else if item.isToggle {
			line := normalStyle.Render(labelPart+strings.Repeat(" ", spacer)) + renderToggle(item.toggled, bg)
			rows = append(rows, line)
		} else if valText != "" {
			line := normalStyle.Render(labelPart+strings.Repeat(" ", spacer)) + valueStyle.Render(valText)
			rows = append(rows, line)
		} else {
			rows = append(rows, normalStyle.Render(plainLine))
		}
	}

	rows = append(rows, "")
	rows = append(rows, footerStyle.Render("↑↓ navigate • Enter/Space toggle • Esc back"))

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
