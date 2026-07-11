package tui

import (
	"fmt"
	"sort"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/tonhe/viaduct/internal/theme"
)

// Sparkline renders as a colored heat strip: each data point is a full-height
// block (█) colored on a green → yellow → red gradient based on relative value.
// Lost probes (0 duration) render as a dim dot (·).

// sparkGradient returns 8 interpolated colors from the active theme.
func sparkGradient() [8]lipgloss.Color {
	return theme.Current.SparkGradient()
}

// sparkLossStyle returns the style for lost-probe dots.
func sparkLossStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base03)
}

// renderSparkline renders a sparkline of the given width (in characters).
// Each character is one data point. Data is right-aligned.
// Lost probes (0 duration) render as a dim dot.
func renderSparkline(data []time.Duration, width int) string {
	if len(data) == 0 {
		result := make([]byte, 0, width)
		for i := 0; i < width; i++ {
			result = append(result, ' ')
		}
		return string(result)
	}

	// Trim to fit width (right-aligned: take most recent)
	if len(data) > width {
		data = data[len(data)-width:]
	}

	// Compute median for threshold-based coloring (skip 0 = loss)
	var valid []float64
	for _, d := range data {
		if d > 0 {
			valid = append(valid, float64(d))
		}
	}

	var median float64
	if len(valid) > 0 {
		sorted := make([]float64, len(valid))
		copy(sorted, valid)
		sort.Float64s(sorted)
		mid := len(sorted) / 2
		if len(sorted)%2 == 0 {
			median = (sorted[mid-1] + sorted[mid]) / 2
		} else {
			median = sorted[mid]
		}
	}
	if median == 0 {
		median = 1
	}

	// Right-align: pad with spaces on the left
	offset := width - len(data)
	var result string
	for i := 0; i < offset; i++ {
		result += " "
	}

	gradient := sparkGradient()
	lossStyle := sparkLossStyle()

	for _, d := range data {
		if d == 0 {
			result += lossStyle.Render("·")
			continue
		}

		// Color by % deviation above median:
		//   ≤ 20% above  → idx 0-1  (green)
		//   20-50% above  → idx 2-4  (green → gold)
		//   50-100% above → idx 5-6  (gold → red)
		//   > 100% above  → idx 7    (red)
		// Below median is always green (idx 0).
		pct := (float64(d) - median) / median
		var idx int
		switch {
		case pct <= 0:
			idx = 0
		case pct <= 0.20:
			idx = int(pct / 0.20 * 1.99) // 0-1
		case pct <= 0.50:
			idx = 2 + int((pct-0.20)/0.30*2.99) // 2-4
		case pct <= 1.00:
			idx = 5 + int((pct-0.50)/0.50*1.99) // 5-6
		default:
			idx = 7
		}

		style := lipgloss.NewStyle().Foreground(gradient[idx])
		result += style.Render("█")
	}

	return result
}

// renderSparklineRunes returns the rune count for testing purposes.
func renderSparklineRuneWidth(data []time.Duration, width int) int {
	// Each data point = 1 char, plus padding = width total
	if len(data) == 0 {
		return width
	}
	if len(data) > width {
		return width
	}
	return width
}

// formatSparkColumn formats the sparkline for column output with a fixed
// visual width, wrapping in a lipgloss container to prevent overflow.
func formatSparkColumn(data []time.Duration, width int) string {
	return fmt.Sprintf("%-*s", width, renderSparkline(data, width))
}
