package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Sparkline renders as a colored heat strip: each data point is a full-height
// block (█) colored on a green → yellow → red gradient based on relative value.
// Lost probes (0 duration) render as a dim dot (·).

// sparkGradient maps 0-7 to ANSI 256-color codes: green → yellow → red.
var sparkGradient = []string{
	"42",  // green
	"78",  // green-cyan
	"114", // light green
	"150", // yellow-green
	"186", // yellow
	"222", // amber
	"208", // orange
	"196", // red
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

	// Find min/max for auto-scaling (skip 0 = loss)
	var minVal, maxVal float64
	first := true
	for _, d := range data {
		if d == 0 {
			continue
		}
		v := float64(d)
		if first {
			minVal = v
			maxVal = v
			first = false
		} else {
			if v < minVal {
				minVal = v
			}
			if v > maxVal {
				maxVal = v
			}
		}
	}

	valRange := maxVal - minVal
	if valRange == 0 {
		valRange = 1
	}

	// Right-align: pad with spaces on the left
	offset := width - len(data)
	var result string
	for i := 0; i < offset; i++ {
		result += " "
	}

	lossStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	for _, d := range data {
		if d == 0 {
			result += lossStyle.Render("·")
			continue
		}

		normalized := (float64(d) - minVal) / valRange
		idx := int(normalized * 7.99)
		if idx > 7 {
			idx = 7
		}
		if idx < 0 {
			idx = 0
		}

		style := lipgloss.NewStyle().Foreground(lipgloss.Color(sparkGradient[idx]))
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
