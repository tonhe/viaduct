package tui

// ViewMode represents the current display mode
type ViewMode int

const (
	ViewDefault     ViewMode = iota
	ViewHealth
	ViewLatency
	ViewVariability
	ViewProbeDetail
	ViewCount // sentinel for cycling
)

func (v ViewMode) Next() ViewMode {
	return (v + 1) % ViewCount
}

func (v ViewMode) String() string {
	switch v {
	case ViewDefault:
		return "Default"
	case ViewHealth:
		return "Health"
	case ViewLatency:
		return "Latency"
	case ViewVariability:
		return "Variability"
	case ViewProbeDetail:
		return "Probe Detail"
	default:
		return "Unknown"
	}
}

// Columns returns the ordered list of stat column names for this view.
// Core columns (#, IP, Hostname, ASN) are always present and not listed here.
func (v ViewMode) Columns() []string {
	switch v {
	case ViewDefault:
		return []string{"Loss%", "Rnd", "Avg", "Best", "Wrst", "StDev", "Last", "Spark"}
	case ViewHealth:
		return []string{"Loss%", "Avg", "Delta", "Spark", "Trend"}
	case ViewLatency:
		return []string{"Avg", "Best", "Wrst", "Last", "Delta", "GMean", "Spark"}
	case ViewVariability:
		return []string{"Avg", "StDev", "Jttr", "Javg", "Loss%", "Spark"}
	case ViewProbeDetail:
		return nil // custom renderer, not columnLayout-driven
	default:
		return nil
	}
}
