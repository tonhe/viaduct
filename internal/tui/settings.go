package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tonhe/viaduct/internal/config"
	"github.com/tonhe/viaduct/internal/theme"
)

type settingsState int

const (
	settingsMain           settingsState = iota
	settingsTheme
	settingsProbe
	settingsDisplay
	settingsAlert
	settingsConfirmDiscard
)

// SettingsModel manages the settings overlay.
type SettingsModel struct {
	state         settingsState
	cursor        int            // main menu cursor position
	config        *config.Config // working copy being edited
	savedConfig   *config.Config // reference to the persisted config
	width, height int
	previousTheme theme.Theme    // for Esc-revert in theme picker
	themeIndex    int            // current position in theme list

	// Sub-view state
	probeCursor   int
	displayCursor int
	alertCursor   int

	// Text input state for editable fields
	focusedInput int    // -1 = none focused
	inputBuffer  string

	// Confirm-discard dialog
	confirmCursor int // 0 = Save, 1 = Discard, 2 = Cancel
}

// Bubbletea messages for settings
type SettingsSavedMsg struct{ Config *config.Config }
type SettingsCancelMsg struct{}

// settingsSaveCmd returns a tea.Cmd that emits SettingsSavedMsg.
func settingsSaveCmd(cfg *config.Config) tea.Cmd {
	return func() tea.Msg { return SettingsSavedMsg{Config: cfg} }
}

// settingsCancelCmd returns a tea.Cmd that emits SettingsCancelMsg.
func settingsCancelCmd() tea.Cmd {
	return func() tea.Msg { return SettingsCancelMsg{} }
}

// NewSettings creates a SettingsModel with a deep copy of cfg for editing.
func NewSettings(cfg *config.Config, w, h int) SettingsModel {
	// Deep copy the config so edits don't affect the live config until saved.
	copied := *cfg
	themeIdx := theme.IndexOf(cfg.Theme)
	if themeIdx < 0 {
		themeIdx = 0
	}
	return SettingsModel{
		state:        settingsMain,
		cursor:       0,
		config:       &copied,
		savedConfig:  cfg,
		width:        w,
		height:       h,
		previousTheme: theme.Current,
		themeIndex:   themeIdx,
		focusedInput: -1,
	}
}

// hasChanges returns true if the working config or theme differs from saved state.
func (s SettingsModel) hasChanges() bool {
	// Check theme change (compare current preview to what was set when settings opened)
	if theme.Current.Slug != s.previousTheme.Slug {
		return true
	}
	// Check config fields
	return *s.config != *s.savedConfig
}

// numMainItems is the number of main menu items.
const numMainItems = 6 // 0:Theme 1:Probe 2:Display 3:Alerting 4:Save 5:Cancel

// Update handles key events for the settings overlay.
func (s SettingsModel) Update(msg tea.Msg) (SettingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch s.state {
		case settingsMain:
			return s.updateMain(msg)
		case settingsTheme:
			return s.updateTheme(msg)
		case settingsProbe:
			return s.updateProbe(msg)
		case settingsDisplay:
			return s.updateDisplay(msg)
		case settingsAlert:
			return s.updateAlert(msg)
		case settingsConfirmDiscard:
			return s.updateConfirmDiscard(msg)
		}
	case tea.WindowSizeMsg:
		s.width = msg.Width
		s.height = msg.Height
	}
	return s, nil
}

func (s SettingsModel) updateMain(msg tea.KeyMsg) (SettingsModel, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		s.cursor++
		if s.cursor >= numMainItems {
			s.cursor = numMainItems - 1
		}
	case "k", "up":
		s.cursor--
		if s.cursor < 0 {
			s.cursor = 0
		}
	case "enter", " ":
		return s.selectMain()
	case "esc":
		if s.hasChanges() {
			s.state = settingsConfirmDiscard
			s.confirmCursor = 0
			return s, nil
		}
		// No changes — just close
		theme.Set(s.previousTheme)
		return s, settingsCancelCmd()
	case "ctrl+s":
		// Save and exit
		s.config.Theme = theme.ByIndex(s.themeIndex).Slug
		return s, settingsSaveCmd(s.config)
	}
	return s, nil
}

func (s SettingsModel) selectMain() (SettingsModel, tea.Cmd) {
	switch s.cursor {
	case 0:
		s.state = settingsTheme
	case 1:
		s.state = settingsProbe
	case 2:
		s.state = settingsDisplay
	case 3:
		s.state = settingsAlert
	case 4:
		// Save & Exit
		t := theme.ByIndex(s.themeIndex)
		if t != nil {
			s.config.Theme = t.Slug
		}
		return s, settingsSaveCmd(s.config)
	case 5:
		// Cancel — revert any theme preview
		theme.Set(s.previousTheme)
		return s, settingsCancelCmd()
	}
	return s, nil
}

func (s SettingsModel) updateTheme(msg tea.KeyMsg) (SettingsModel, tea.Cmd) {
	count := theme.Count()
	switch msg.String() {
	case "j", "down":
		s.themeIndex++
		if s.themeIndex >= count {
			s.themeIndex = count - 1
		}
		// Live preview
		if t := theme.ByIndex(s.themeIndex); t != nil {
			theme.Set(*t)
		}
	case "k", "up":
		s.themeIndex--
		if s.themeIndex < 0 {
			s.themeIndex = 0
		}
		// Live preview
		if t := theme.ByIndex(s.themeIndex); t != nil {
			theme.Set(*t)
		}
	case "enter", " ":
		// Confirm selection and return to main
		s.state = settingsMain
	case "esc":
		// Revert to previous theme
		theme.Set(s.previousTheme)
		s.themeIndex = theme.IndexOf(s.previousTheme.Slug)
		if s.themeIndex < 0 {
			s.themeIndex = 0
		}
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

// View renders the settings overlay centered on screen.
func (s SettingsModel) View() string {
	switch s.state {
	case settingsTheme:
		return s.viewTheme()
	case settingsProbe:
		return s.viewProbe()
	case settingsDisplay:
		return s.viewDisplay()
	case settingsAlert:
		return s.viewAlert()
	case settingsConfirmDiscard:
		return s.viewConfirmDiscard()
	default:
		return s.viewMain()
	}
}

func (s SettingsModel) updateConfirmDiscard(msg tea.KeyMsg) (SettingsModel, tea.Cmd) {
	switch msg.String() {
	case "j", "down", "tab":
		s.confirmCursor++
		if s.confirmCursor > 2 {
			s.confirmCursor = 2
		}
	case "k", "up", "shift+tab":
		s.confirmCursor--
		if s.confirmCursor < 0 {
			s.confirmCursor = 0
		}
	case "enter", " ":
		switch s.confirmCursor {
		case 0: // Save
			t := theme.ByIndex(s.themeIndex)
			if t != nil {
				s.config.Theme = t.Slug
			}
			return s, settingsSaveCmd(s.config)
		case 1: // Discard
			theme.Set(s.previousTheme)
			return s, settingsCancelCmd()
		case 2: // Go back
			s.state = settingsMain
		}
	case "esc":
		s.state = settingsMain
	case "s":
		// Quick save
		t := theme.ByIndex(s.themeIndex)
		if t != nil {
			s.config.Theme = t.Slug
		}
		return s, settingsSaveCmd(s.config)
	case "d":
		// Quick discard
		theme.Set(s.previousTheme)
		return s, settingsCancelCmd()
	}
	return s, nil
}

func (s SettingsModel) viewConfirmDiscard() string {
	boxW := s.overlayWidth()

	borderColor := theme.Current.Base0A
	bg := theme.Current.Base01
	titleStyle := lipgloss.NewStyle().Foreground(theme.Current.Base0A).Background(bg).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(theme.Current.Base05).Background(bg)
	highlightStyle := lipgloss.NewStyle().
		Foreground(theme.Current.Base06).
		Background(theme.Current.Base02).
		Bold(true)
	footerStyle := lipgloss.NewStyle().Foreground(theme.Current.Base04).Background(bg)

	innerW := boxW - 4
	if innerW < 10 {
		innerW = 10
	}

	items := []string{"Save changes", "Discard changes", "Go back"}

	var rows []string
	rows = append(rows, titleStyle.Render("Unsaved Changes"))
	rows = append(rows, normalStyle.Render("You have unsaved changes."))
	rows = append(rows, "")

	for i, label := range items {
		cursor := "  "
		if i == s.confirmCursor {
			cursor = "► "
		}
		line := cursor + label
		if i == s.confirmCursor {
			line = highlightStyle.Render(line)
		} else {
			line = normalStyle.Render(line)
		}
		rows = append(rows, line)
	}

	rows = append(rows, "")
	rows = append(rows, footerStyle.Render("↑↓ navigate • Enter select • Esc back"))

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

// settingsBorderColor returns a border color with appropriate contrast for the theme.
// Light themes use Base03 (subtle), dark themes use Base04 (visible).
func settingsBorderColor() lipgloss.Color {
	if theme.Current.Light {
		return theme.Current.Base03
	}
	return theme.Current.Base04
}

// placeOverlay centers a box on screen with the theme background filling whitespace.
func (s SettingsModel) placeOverlay(box string) string {
	return lipgloss.Place(s.width, s.height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceBackground(theme.Current.Base00))
}

// overlayWidth returns the width for the settings box.
func (s SettingsModel) overlayWidth() int {
	w := 50
	if s.width > 0 && s.width-4 < w {
		w = s.width - 4
	}
	return w
}

func (s SettingsModel) viewMain() string {
	boxW := s.overlayWidth()

	borderColor := settingsBorderColor()
	bg := theme.Current.Base01
	titleStyle := lipgloss.NewStyle().Foreground(theme.Current.Base06).Background(bg).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(theme.Current.Base05).Background(bg)
	highlightStyle := lipgloss.NewStyle().
		Foreground(theme.Current.Base06).
		Background(theme.Current.Base02).
		Bold(true)
	dimmedStyle := lipgloss.NewStyle().Foreground(theme.Current.Base04).Background(bg)
	footerStyle := lipgloss.NewStyle().Foreground(theme.Current.Base04).Background(bg)

	// Get current theme name for display
	currentThemeName := ""
	if t := theme.ByIndex(s.themeIndex); t != nil {
		currentThemeName = t.Name
	}

	type menuItem struct {
		label  string
		hint   string
		sep    bool // separator before this item
	}
	items := []menuItem{
		{label: "Theme", hint: currentThemeName},
		{label: "Probe Settings", hint: "›"},
		{label: "Display & Enrichment", hint: "›"},
		{label: "Alerting", hint: "›"},
		{label: "Save & Exit", hint: "", sep: true},
		{label: "Cancel", hint: ""},
	}

	innerW := boxW - 4 // account for border (2) + padding (2)
	if innerW < 10 {
		innerW = 10
	}

	var rows []string
	rows = append(rows, titleStyle.Render("Settings"))
	rows = append(rows, dimmedStyle.Render(strings.Repeat("─", innerW)))

	for i, item := range items {
		if item.sep {
			rows = append(rows, dimmedStyle.Render(strings.Repeat("─", innerW)))
		}

		cursor := "  "
		if i == s.cursor {
			cursor = "► "
		}

		// Build the plain text content of the line
		labelPart := cursor + item.label
		hintPart := item.hint
		labelLen := len(labelPart) // plain ASCII, no ANSI
		hintLen := len(hintPart)
		spacer := innerW - labelLen - hintLen
		if spacer < 1 {
			spacer = 1
		}
		plainLine := labelPart + strings.Repeat(" ", spacer) + hintPart

		if i == s.cursor {
			// Render entire row with highlight background
			rows = append(rows, highlightStyle.Width(innerW).Render(plainLine))
		} else {
			rows = append(rows, normalStyle.Render(plainLine))
		}
	}

	rows = append(rows, "")
	rows = append(rows, footerStyle.Render("↑↓ navigate • Enter select • Esc close"))

	content := strings.Join(rows, "\n")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		BorderBackground(theme.Current.Base00).
		Background(theme.Current.Base01).
		Padding(0, 1).
		Width(boxW).
		Render(content)

	// Center the box on screen
	return s.placeOverlay(box)
}

func (s SettingsModel) viewTheme() string {
	boxW := s.overlayWidth()

	borderColor := settingsBorderColor()
	bg := theme.Current.Base01
	titleStyle := lipgloss.NewStyle().Foreground(theme.Current.Base06).Background(bg).Bold(true)
	highlightStyle := lipgloss.NewStyle().
		Foreground(theme.Current.Base06).
		Background(theme.Current.Base02).
		Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(theme.Current.Base05).Background(bg)
	dimmedStyle := lipgloss.NewStyle().Foreground(theme.Current.Base04).Background(bg)
	footerStyle := lipgloss.NewStyle().Foreground(theme.Current.Base04).Background(bg)

	innerW := boxW - 4
	if innerW < 10 {
		innerW = 10
	}

	themes := theme.All()

	// Determine visible window — show up to 12 themes at a time
	const visibleCount = 12
	start := s.themeIndex - visibleCount/2
	if start < 0 {
		start = 0
	}
	end := start + visibleCount
	if end > len(themes) {
		end = len(themes)
		start = end - visibleCount
		if start < 0 {
			start = 0
		}
	}

	var rows []string
	rows = append(rows, titleStyle.Render("Theme"))
	rows = append(rows, dimmedStyle.Render(strings.Repeat("─", innerW)))

	for i := start; i < end; i++ {
		t := themes[i]
		cursor := "  "
		if i == s.themeIndex {
			cursor = "► "
		}
		line := cursor + t.Name
		if i == s.themeIndex {
			line = highlightStyle.Render(line)
		} else {
			line = normalStyle.Render(line)
		}
		rows = append(rows, line)
	}

	if len(themes) > visibleCount {
		rows = append(rows, dimmedStyle.Render(fmt.Sprintf("  … %d themes total", len(themes))))
	}
	rows = append(rows, "")
	rows = append(rows, footerStyle.Render("↑↓ navigate • Enter confirm • Esc back"))

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

