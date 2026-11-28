package tui

import "github.com/tonhe/viaduct/internal/config"

// viewModeForDisplayMode maps a display_mode setting (config.Config.DisplayMode,
// one of displayModeOptions) to the view it selects. "probes" is Probe Detail.
// A value that is none of them selects the default view, as config.Validate
// would have made it.
func viewModeForDisplayMode(mode string) ViewMode {
	switch mode {
	case "health":
		return ViewHealth
	case "latency":
		return ViewLatency
	case "variability":
		return ViewVariability
	case "probes":
		return ViewProbeDetail
	default:
		return ViewDefault
	}
}

// startViewMode is the view a session opens in: the one its config names, or
// the default view when there is no config.
func startViewMode(cfg *config.Config) ViewMode {
	if cfg == nil {
		return ViewDefault
	}
	return viewModeForDisplayMode(cfg.DisplayMode)
}

// viewModeAfterSave is the view to show once settings have been saved. A
// Display Mode that was changed in the menu switches the running view to the
// one chosen. One that was left as it was changes nothing, so a view picked
// with the d key is not taken away by saving some other setting.
func viewModeAfterSave(current ViewMode, before, after string) ViewMode {
	if before == after {
		return current
	}
	return viewModeForDisplayMode(after)
}
