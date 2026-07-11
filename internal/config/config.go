package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Config holds all via configuration values.
type Config struct {
	Theme          string  `toml:"theme"`
	Protocol       string  `toml:"protocol"`
	MaxHops        int     `toml:"max_hops"`
	Interval       string  `toml:"interval"`
	Paths          int     `toml:"paths"`
	DisplayMode    string  `toml:"display_mode"`
	DNSLookups     bool    `toml:"dns_lookups"`
	ASNLookups     bool    `toml:"asn_lookups"`
	PingSupplement bool    `toml:"ping_supplement"`
	AlertEnabled   bool    `toml:"alert_enabled"`
	AlertLoss      float64 `toml:"alert_loss"`
	AlertLatency   string  `toml:"alert_latency"`
	AlertRounds    int     `toml:"alert_rounds"`
	ExportFormat   string  `toml:"export_format"`
	IPFamily       string  `toml:"ip_family"` // "auto" | "4" | "6"; default "auto"
}

// Default returns a Config with all default values applied.
func Default() *Config {
	return &Config{
		Theme:          "solarized-dark",
		Protocol:       "udp",
		MaxHops:        30,
		Interval:       "1s",
		Paths:          6,
		DisplayMode:    "default",
		DNSLookups:     true,
		ASNLookups:     true,
		PingSupplement: true,
		AlertEnabled:   true,
		AlertLoss:      5.0,
		AlertLatency:   "0s",
		AlertRounds:    3,
		IPFamily:       "auto",
	}
}

// configDir returns the platform-appropriate config directory for via.
func configDir() string {
	if runtime.GOOS == "windows" {
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			appdata = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
		}
		return filepath.Join(appdata, "via")
	}
	// Unix: prefer XDG_CONFIG_HOME, fall back to ~/.config
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg != "" {
		return filepath.Join(xdg, "via")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "via")
}

// configPath returns the full path to the config file.
func configPath() string {
	return filepath.Join(configDir(), "config.toml")
}

// Load reads the TOML config file and returns a merged Config. If the file
// does not exist, Default() is returned with no error. Malformed TOML returns
// an error. Missing fields in the file keep their default values.
func Load() (*Config, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	// Start from defaults so any field not present in the file keeps its default.
	cfg := Default()
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes the config to disk using hand-crafted TOML with section comments.
// The config directory is created if it does not exist.
func Save(c *Config) error {
	dir := configDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}

	path := configPath()
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("config: create %s: %w", path, err)
	}
	defer file.Close()

	lines := []string{
		"# via configuration",
		"# Edit this file to customise via's behaviour.",
		"",
		"# Visual theme (slug format, e.g., solarized-dark, tokyo-night, catppuccin-mocha)",
		fmt.Sprintf("theme = %q", c.Theme),
		"",
		"# ── Probe Settings ──────────────────────────────────────────────────────────",
		"",
		"# Probe protocol: udp, icmp, tcp, auto",
		fmt.Sprintf("protocol = %q", c.Protocol),
		"",
		"# IP family: auto, 4 (IPv4), or 6 (IPv6)",
		fmt.Sprintf("ip_family = %q", c.IPFamily),
		"",
		"# Maximum number of hops to probe (1–64)",
		fmt.Sprintf("max_hops = %d", c.MaxHops),
		"",
		"# Time between probe rounds (e.g. \"1s\", \"500ms\")",
		fmt.Sprintf("interval = %q", c.Interval),
		"",
		"# Number of ECMP paths to discover per hop (1–32)",
		fmt.Sprintf("paths = %d", c.Paths),
		"",
		"# Display mode: default, health, latency, variability",
		fmt.Sprintf("display_mode = %q", c.DisplayMode),
		"",
		"# ── Lookup Settings ──────────────────────────────────────────────────────────",
		"",
		"# Resolve hop IP addresses to hostnames",
		fmt.Sprintf("dns_lookups = %v", c.DNSLookups),
		"",
		"# Annotate hops with AS number / org info",
		fmt.Sprintf("asn_lookups = %v", c.ASNLookups),
		"",
		"# Supplement ECMP probe results with ICMP ping measurements",
		fmt.Sprintf("ping_supplement = %v", c.PingSupplement),
		"",
		"# ── Alert Settings ───────────────────────────────────────────────────────────",
		"",
		"# Enable alerting when thresholds are breached",
		fmt.Sprintf("alert_enabled = %v", c.AlertEnabled),
		"",
		"# Loss percentage threshold that triggers an alert (0–100)",
		fmt.Sprintf("alert_loss = %.1f", c.AlertLoss),
		"",
		"# Latency threshold that triggers an alert (e.g. \"100ms\", \"0s\" to disable)",
		fmt.Sprintf("alert_latency = %q", c.AlertLatency),
		"",
		"# Number of consecutive rounds that must breach thresholds before alerting (1–20)",
		fmt.Sprintf("alert_rounds = %d", c.AlertRounds),
	}

	for _, line := range lines {
		if _, err := file.WriteString(line + "\n"); err != nil {
			return fmt.Errorf("config: write %s: %w", path, err)
		}
	}
	return nil
}

// Validate clamps / corrects invalid Config values in-place.
func Validate(c *Config) {
	d := Default()

	// theme: must be non-empty
	if c.Theme == "" {
		c.Theme = d.Theme
	}

	// protocol: must be one of the known values
	switch c.Protocol {
	case "udp", "icmp", "tcp", "auto":
		// valid
	default:
		c.Protocol = d.Protocol
	}

	// max_hops: clamp 1–64
	if c.MaxHops < 1 {
		c.MaxHops = 1
	} else if c.MaxHops > 64 {
		c.MaxHops = 64
	}

	// interval: must be a valid duration
	if _, err := time.ParseDuration(c.Interval); err != nil {
		c.Interval = d.Interval
	}

	// paths: clamp 1–32
	if c.Paths < 1 {
		c.Paths = 1
	} else if c.Paths > 32 {
		c.Paths = 32
	}

	// display_mode: must be one of the known values
	switch c.DisplayMode {
	case "default", "health", "latency", "variability":
		// valid
	default:
		c.DisplayMode = d.DisplayMode
	}

	// alert_loss: clamp 0–100
	if c.AlertLoss < 0 {
		c.AlertLoss = 0
	} else if c.AlertLoss > 100 {
		c.AlertLoss = 100
	}

	// alert_latency: must be a valid duration
	if _, err := time.ParseDuration(c.AlertLatency); err != nil {
		c.AlertLatency = d.AlertLatency
	}

	// alert_rounds: clamp 1–20
	if c.AlertRounds < 1 {
		c.AlertRounds = 1
	} else if c.AlertRounds > 20 {
		c.AlertRounds = 20
	}

	// ip_family: must be one of the known values
	switch c.IPFamily {
	case "", "auto", "4", "6":
		// valid; empty string is treated as "auto"
		if c.IPFamily == "" {
			c.IPFamily = "auto"
		}
	default:
		fmt.Fprintf(os.Stderr, "warning: invalid ip_family %q in config, using auto\n", c.IPFamily)
		c.IPFamily = "auto"
	}
}
