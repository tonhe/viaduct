package theme

import (
	"fmt"
	"strconv"

	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/charmbracelet/lipgloss"
)

// Theme holds a Base16 color palette plus metadata.
type Theme struct {
	Name  string
	Slug  string
	Light bool

	Base00 lipgloss.Color // Background
	Base01 lipgloss.Color // Lighter background
	Base02 lipgloss.Color // Selection background
	Base03 lipgloss.Color // Comments, invisibles
	Base04 lipgloss.Color // Dark foreground
	Base05 lipgloss.Color // Default foreground
	Base06 lipgloss.Color // Light foreground
	Base07 lipgloss.Color // Lightest foreground
	Base08 lipgloss.Color // Red
	Base09 lipgloss.Color // Orange
	Base0A lipgloss.Color // Yellow
	Base0B lipgloss.Color // Green
	Base0C lipgloss.Color // Cyan
	Base0D lipgloss.Color // Blue
	Base0E lipgloss.Color // Magenta
	Base0F lipgloss.Color // Brown
}

// Current is the active theme. Set by init() in themes.go.
var Current Theme

// themes and themeOrder are populated by init() in themes.go.
var themes map[string]Theme
var themeOrder []Theme

// Set updates the active theme.
func Set(t Theme) {
	Current = t
}

// ByName returns the theme with the given slug, or nil if not found.
func ByName(slug string) *Theme {
	t, ok := themes[slug]
	if !ok {
		return nil
	}
	return &t
}

// ByIndex returns the theme at position i in display order, or nil if out of range.
func ByIndex(i int) *Theme {
	if i < 0 || i >= len(themeOrder) {
		return nil
	}
	t := themeOrder[i]
	return &t
}

// All returns all themes in display order.
func All() []Theme {
	out := make([]Theme, len(themeOrder))
	copy(out, themeOrder)
	return out
}

// Names returns slug/display-name pairs in display order.
func Names() [][2]string {
	out := make([][2]string, len(themeOrder))
	for i, t := range themeOrder {
		out[i] = [2]string{t.Slug, t.Name}
	}
	return out
}

// Count returns the number of registered themes.
func Count() int {
	return len(themeOrder)
}

// IndexOf returns the index of the theme with the given slug, or -1 if not found.
func IndexOf(slug string) int {
	for i, t := range themeOrder {
		if t.Slug == slug {
			return i
		}
	}
	return -1
}

// SparkGradient computes 8 interpolated colors for sparkline heat strips.
// Uses a fixed data-visualization gradient (green → gold → red) rather than
// theme accent colors, since Base16 accents are designed for syntax highlighting
// and often produce muddy gradients when interpolated (e.g., Solarized's olive
// green blends to sickly yellow). Lightness is adjusted for light vs dark themes.
func (t Theme) SparkGradient() [8]lipgloss.Color {
	// Fixed endpoints tuned for data-viz clarity on terminal backgrounds.
	var ok, boundary, errEnd colorful.Color
	if t.Light {
		// Slightly darker/more saturated for light backgrounds
		ok = colorful.Hsl(140, 0.70, 0.40)      // rich green
		boundary = colorful.Hsl(45, 0.85, 0.45)  // warm gold
		errEnd = colorful.Hsl(5, 0.80, 0.45)     // clear red
	} else {
		// Vivid for dark backgrounds
		ok = colorful.Hsl(140, 0.75, 0.55)       // bright green
		boundary = colorful.Hsl(45, 0.90, 0.55)  // bright gold
		errEnd = colorful.Hsl(5, 0.85, 0.50)     // bright red
	}

	var stops [8]lipgloss.Color

	// First 4 stops: green → gold, indices 0–3
	for i := 0; i < 4; i++ {
		frac := float64(i) / 3.0
		c := ok.BlendHcl(boundary, frac)
		stops[i] = lipgloss.Color(colorfulToHex(c))
	}

	// Last 4 stops: gold → red, indices 4–7
	for i := 0; i < 4; i++ {
		frac := float64(i) / 3.0
		c := boundary.BlendHcl(errEnd, frac)
		stops[4+i] = lipgloss.Color(colorfulToHex(c))
	}

	return stops
}

// hexToColorful parses a hex color string (with or without leading #) into a
// colorful.Color. Returns black on parse failure.
func hexToColorful(hex string) colorful.Color {
	if len(hex) > 0 && hex[0] != '#' {
		hex = "#" + hex
	}
	c, err := colorful.Hex(hex)
	if err != nil {
		return colorful.Color{}
	}
	return c
}

// colorfulToHex converts a colorful.Color to a CSS hex string (#rrggbb).
func colorfulToHex(c colorful.Color) string {
	r := uint8(clamp01(c.R) * 255)
	g := uint8(clamp01(c.G) * 255)
	b := uint8(clamp01(c.B) * 255)
	return fmt.Sprintf("#%s%s%s",
		byteToHex(r),
		byteToHex(g),
		byteToHex(b),
	)
}

func byteToHex(v uint8) string {
	s := strconv.FormatInt(int64(v), 16)
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

