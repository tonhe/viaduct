package tui

import (
	"sort"

	"github.com/charmbracelet/lipgloss"

	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/theme"
)

// Sparkline renders as a colored heat strip. Solidity encodes how much the
// tracer knows about the hop each round, so the sparkline agrees with the
// echo-based loss column:
//   █ full   direct TTL/UDP RTT      (green → red gradient)
//   ▓ medium echo RTT                (reachable, rate-limited; same gradient)
//   ░ light  known rate-limited, not sampled this round (reachable, no reading)
//   · dot    genuine drop            (a probe was sent, nothing answered)

// sparkGradient returns 8 interpolated colors from the active theme.
func sparkGradient() [8]lipgloss.Color {
	return theme.Current.SparkGradient()
}

// sparkLossStyle returns the style for lost-probe dots and held markers.
func sparkLossStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current.Base03)
}

// sparkGradIdx maps an RTT to a gradient index by its deviation above the
// median. Below median is always the greenest.
func sparkGradIdx(d, median float64) int {
	pct := (d - median) / median
	switch {
	case pct <= 0:
		return 0
	case pct <= 0.20:
		return int(pct / 0.20 * 1.99) // 0-1
	case pct <= 0.50:
		return 2 + int((pct-0.20)/0.30*2.99) // 2-4
	case pct <= 1.00:
		return 5 + int((pct-0.50)/0.50*1.99) // 5-6
	default:
		return 7
	}
}

// renderSparkline renders a sparkline of the given width (in characters).
// Each character is one round. Data is right-aligned. Cells render by kind:
// TTL/Echo as gradient-colored blocks (█/▓), Held as a dim ░, Drop as a dim ·.
func renderSparkline(data []hop.SparkSample, width int) string {
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

	// Median for coloring, over latency-bearing cells (TTL + Echo) only.
	var valid []float64
	for _, s := range data {
		if s.RTT > 0 && (s.Kind == hop.SparkTTL || s.Kind == hop.SparkEcho) {
			valid = append(valid, float64(s.RTT))
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

	for _, s := range data {
		switch s.Kind {
		case hop.SparkDrop:
			result += lossStyle.Render("·")
		case hop.SparkHeld:
			result += lossStyle.Render("░")
		case hop.SparkEcho:
			idx := sparkGradIdx(float64(s.RTT), median)
			result += lipgloss.NewStyle().Foreground(gradient[idx]).Render("▓")
		default: // SparkTTL
			idx := sparkGradIdx(float64(s.RTT), median)
			result += lipgloss.NewStyle().Foreground(gradient[idx]).Render("█")
		}
	}

	return result
}
