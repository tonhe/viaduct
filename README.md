<div align="center">
  <h1>via</h1>
  <p><em>A modern terminal-native traceroute replacement with real-time ECMP multipath discovery</em></p>

  [![Go](https://img.shields.io/github/go-mod/go-version/tonhe/viaduct)](https://go.dev)
  [![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
  [![GitHub release](https://img.shields.io/github/v/release/tonhe/viaduct)](https://github.com/tonhe/viaduct/releases)
  [![macOS](https://img.shields.io/badge/macOS-supported-brightgreen?logo=apple)](https://github.com/tonhe/viaduct)
  [![Linux](https://img.shields.io/badge/Linux-supported-brightgreen?logo=linux&logoColor=white)](https://github.com/tonhe/viaduct)
  [![Windows](https://img.shields.io/badge/Windows-supported-brightgreen?logo=windows&logoColor=white)](https://github.com/tonhe/viaduct)

</div>

Traditional `traceroute` shows one path. Real networks aren't single paths — they're forests of parallel ECMP links, rate-limiting routers, and load balancers that return different answers every probe. `via` sees all of it.

`via` replaces `traceroute`, `mtr`, and `paris-traceroute` with a live-updating TUI that shows packet loss, latency stats, reverse DNS, ASN enrichment, and all parallel paths through load-balanced networks. Trace up to 16 targets simultaneously — each gets its own session with independent port blocks and family selection.

<img src="img/demo.gif" alt="via showing ECMP multipath trace with sparklines and ASN enrichment" width="900">

[Install](#install) · [Quick Start](#quick-start) · [Features](#features) · [Display Modes](#display-modes) · [Themes](#themes) · [Flags](#flags)

---

## How via Compares

| Feature | traceroute | mtr | paris-traceroute | **via** |
|---------|:---:|:---:|:---:|:---:|
| ECMP multipath discovery | — | — | Yes | **Yes** |
| Live-updating TUI | — | Yes | — | **Yes** |
| ASN enrichment | — | — | — | **Yes** |
| Rate-limit detection | — | — | — | **Yes** |
| TCP/UDP/ICMP probes | Partial | ICMP | UDP/ICMP | **All + Auto** |
| Multi-target simultaneous | — | — | — | **Up to 16** |
| Display modes & sparklines | — | — | — | **5 modes** |
| Destination alerting | — | — | — | **Yes** |
| Color themes | — | — | — | **61** |
| Settings persistence | — | — | — | **Yes** |

---

## Install

### Homebrew

```bash
brew install tonhe/tap/viaduct
```

### From source

```bash
git clone https://github.com/tonhe/viaduct.git
cd viaduct
make install    # builds and copies to ~/bin/
```

> [!NOTE]
> **Linux users** can avoid sudo by granting raw socket capability once:
> ```bash
> sudo setcap cap_net_raw+ep $(which via)
> ```

---

## Quick Start

```bash
# Trace with ECMP multipath discovery (default: UDP, 6 flows)
via google.com

# TCP SYN probes — passes firewalls that block UDP/ICMP
via -P tcp google.com

# Auto mode — starts UDP, falls back to ICMP if no responses
via -P auto google.com

# Non-interactive report for scripts
via -r google.com
```

> [!TIP]
> Press <kbd>Esc</kbd> during any trace to open the settings menu — change themes, probe settings, or alert thresholds without restarting.

---

## Features

- **ECMP multipath** — Paris-traceroute-style probing reveals all parallel paths through load-balanced networks with tree-view divergence (`├──`/`└──` connectors)
- **Automatic rate-limit detection** — detects routers that rate-limit ICMP TTL Exceeded by interleaving direct ICMP echo probes; replaces phantom loss with real data (`†` marker)
- **5 display modes** — cycle with <kbd>d</kbd>: Default, Health, Latency, Variability, Probe Detail — each with sparklines, trend detection, hop-to-hop deltas, and per-probe-type stats
- **Destination alerting** — monitors loss% and latency thresholds; identifies the responsible hop and ASN when alerts fire
- **ASN enrichment** — each hop shows its AS number with boundary transitions highlighted; full org name at wide widths
- **3 probe protocols + auto** — UDP (ECMP), TCP SYN (firewall bypass), ICMP (classic), and Auto (UDP with ICMP fallback)
- **61 color themes** — Solarized Dark default; switch with `--theme` or the settings menu
- **Config persistence** — settings saved to `~/.config/via/config.toml`; CLI flags override at runtime

---

## Protocol Modes

| Protocol | Flag | ECMP | Dest Detection | Best For |
|----------|------|------|----------------|----------|
| UDP | `-P udp` (default) | Yes (src-port variation) | ICMP Port Unreachable | General use, ECMP discovery |
| TCP SYN | `-P tcp` | Yes (src-port variation) | SYN-ACK or RST | Targets behind firewalls |
| ICMP | `-P icmp` | No (single-path) | Echo Reply | Simple traces, compatibility |
| Auto | `-P auto` | Yes → No on fallback | Per active protocol | Unknown networks |

**Auto mode** starts with UDP for ECMP discovery. If no responses are received after 3 rounds (~3 seconds), it automatically falls back to ICMP. The TUI header updates to reflect the active protocol.

---

## IPv6 Support

`via` runs over IPv4 or IPv6, selecting the family automatically based on the target's DNS records and the host's transport capability. Force a specific family with `-4` or `-6`:

```bash
via google.com          # auto-selects (prefers IPv6 if available)
via -4 google.com       # IPv4
via -6 google.com       # IPv6
```

If `-6` is requested and the target has no AAAA record (or the host has no IPv6 transport), `via` exits with a clear error rather than silently falling back.

---

## Rate-Limit Detection

Many routers rate-limit ICMP TTL Exceeded responses, causing tools like `mtr` and `traceroute` to show phantom packet loss at intermediate hops. This is the single most common source of misdiagnosis in traceroute output — operators see 60% loss at hop 3 and assume a problem, when the router is simply deprioritizing TTL Exceeded replies.

`via` automagically detects this. The tracer interleaves per-round probes with budgeted direct ICMP Echo Requests sent to each discovered hop. When TTL-probe loss is high but the direct echo responses remain healthy, `via` marks the hop rate-limited and reports echo-derived stats instead of the misleading TTL Exceeded data.

The `†` dagger in the loss column (`~N%†`) indicates a hop whose stats come from direct echo probes rather than TTL Exceeded replies. No configuration needed — it just works.

---

## Display Modes

Press <kbd>d</kbd> to cycle through five view presets:

| Mode | Columns | Best For |
|------|---------|----------|
| **Default** | Loss%, Rnd, Avg, Best, Wrst, StDev, Last | General overview (mtr-equivalent) |
| **Health** | Loss%, Avg, Delta, Sparkline, Trend | Quick health assessment |
| **Latency** | Avg, Best, Wrst, GMean, Delta, Sparkline | Detailed latency analysis |
| **Variability** | StDev, Jitter, Jitter Mean, Sparkline, Trend | Stability analysis |
| **Probe Detail** | ICMP-TTL, UDP/TCP-TTL, Echo (recvd + loss%), Rnd | Per-probe-type diagnostics, rate-limit verification |

**Additional metrics:**

- **Delta** — hop-to-hop latency difference; the largest delta is highlighted in amber to pinpoint where delay is introduced
- **GMean** — geometric mean, a better central tendency for skewed latency distributions
- **Jitter / Javg** — current jitter (`|rtt - prevRTT|`) and running mean jitter
- **Sparkline** — heat-strip mini chart (green→gold→red gradient) showing per-round average latency (28 rounds); lost probes render as dim `·`
- **Trend** — sliding-window linear regression over 10 rounds; shows `↑ degrading`, `↓ improving`, or `~ stable`

---

## Destination Alerting

`via` monitors the destination hop for sustained degradation and alerts when thresholds are exceeded. By default, alerts fire when loss exceeds 5% for 3 consecutive rounds.

When an alert fires, `via` walks backward through the hops to identify which hop and ASN is likely causing the problem, and displays the alert in a red bar above the status line.

Configure with `--alert-loss`, `--alert-latency`, and `--alert-rounds`. Disable with `--no-alert`. Press <kbd>r</kbd> to reset alert state.

---

## Themes

`via` ships with 61 built-in color themes. Set a theme with `--theme <slug>` or choose one in the settings menu (<kbd>Esc</kbd>).

<details>
<summary>Show all 61 themes</summary>

| Family | Themes |
|--------|--------|
| **Solarized** | solarized-dark (default), solarized-light, solarized-dark-hc, solarized-osaka-night, solarized-darcula |
| **Catppuccin** | catppuccin-mocha, catppuccin-macchiato, catppuccin-frappe, catppuccin-latte |
| **Gruvbox** | gruvbox-dark, gruvbox-light, gruvbox-dark-hard, gruvbox-material |
| **Tokyo Night** | tokyo-night, tokyo-night-storm, tokyo-night-moon, tokyo-night-day |
| **One/Atom** | one-dark, one-light, one-half-dark, one-half-light |
| **Rosé Pine** | rose-pine, rose-pine-moon, rose-pine-dawn |
| **Kanagawa** | kanagawa-wave, kanagawa-dragon, kanagawa-lotus |
| **Everforest** | everforest-dark, everforest-light |
| **Fox** | nightfox, carbonfox, nordfox, dawnfox |
| **Monokai** | monokai-classic, monokai-pro, monokai-vivid |
| **GitHub** | github-dark, github-dark-dimmed, github-light |
| **Standalone** | dracula, nord, flexoki-dark, flexoki-light, cyberdream, ayu-mirage, ayu-dark, ayu-light, challenger-deep, night-owl, night-owlish-light, doom-one, moonfly, sonokai, oxocarbon, poimandres, vesper, palenight, horizon, zenburn, modus-operandi, tomorrow |

</details>

---

## Multi-Target Mode

Trace up to 16 destinations simultaneously in one session:

```bash
# Two targets side-by-side
via google.com one.one.one.one

# Targets from a file (one per line, # comments and blank lines ignored)
via --targets /etc/trace-targets.txt

# Combine positional and file targets — all deduped, max 16 total
via google.com --targets targets.txt
```

The TUI switches to a **summary+detail layout**: a header row per target shows target name, hop count, loss%, avg RTT, worst-hop RTT, and alert state. Below the summary, the focused target's full hop table is rendered exactly like single-target mode.

**Navigation (multi-target only):**

| Key | Action |
|-----|--------|
| <kbd>↑</kbd> / <kbd>↓</kbd> or <kbd>j</kbd> / <kbd>k</kbd> | Move focus between targets |
| <kbd>Tab</kbd> | Cycle focus to next target |
| <kbd>Space</kbd> | Mark/unmark target — also selects which panes show in column view |
| <kbd>c</kbd> | Toggle column view — bordered panes side by side (`# IP Host Avg Loss`), one per target, dividing the terminal width equally; the focused pane has a heavy border |

Global keys (`p` pause, `r` reset, `d` display mode) apply to all targets simultaneously.

Per-target IP family is auto-selected independently. If a target fails to resolve or probe, it is shown as errored in its summary row while all other targets continue tracing. If every target errors at startup, `via` exits 1 with each error on stderr.

Exports in multi-target mode group all targets: JSON uses a `targets` array, CSV adds a leading `target` column, and DOT produces a single digraph with shared-IP node merging. Report mode (`-r`) prints each target's table in sequence.

---

## Usage

```bash
# Basic trace with ECMP multipath (default: UDP, 6 flows)
via google.com

# Use TCP SYN probes (port 443 default, passes firewalls)
via -P tcp google.com

# Use ICMP probes (single-path, like classic traceroute)
via -P icmp google.com

# Auto mode: starts UDP, falls back to ICMP if no responses
via -P auto google.com

# TCP with custom port
via -P tcp -p 80 example.com

# Trace with custom hop limit
via --max-hops 15 8.8.8.8

# Set per-probe timeout and interval
via --timeout 2s -i 500ms cloudflare.com

# Probe 12 ECMP flows
via --paths 12 google.com

# Disable ECMP, single-path mode
via --no-paths cloudflare.com

# Report mode (non-interactive, for scripts)
via -r google.com

# Report mode with 5 rounds, no DNS
via -r -c 5 -n 8.8.8.8

# Disable ASN lookups
via --no-asn google.com

# Set alert thresholds (alert on >10% loss sustained for 5 rounds)
via --alert-loss 10 --alert-rounds 5 google.com

# Alert on sustained latency above 200ms
via --alert-latency 200ms google.com

# Disable alerting entirely
via --no-alert google.com

# Trace two targets simultaneously in the TUI
via google.com one.one.one.one

# Trace targets from a file (one per line, # comments ok)
via --targets /etc/trace-targets.txt

# Combine positional and file targets (deduped, max 16 total)
via google.com --targets targets.txt

# Multi-target report mode (text)
via -r -c 3 google.com one.one.one.one

# Multi-target report, JSON export
via -r -c 3 -o json google.com one.one.one.one
```

---

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--protocol` | `-P` | udp | Probe protocol: `udp`, `icmp`, `tcp`, `auto` |
| `--port` | `-p` | (protocol) | Destination port (33434 for UDP, 443 for TCP) |
| `--max-hops` | | 30 | Maximum number of hops to probe |
| `--timeout` | | 1s | Per-probe response timeout |
| `--interval` | `-i` | 1s | Probe round interval |
| `--count` | `-c` | 0 | Number of probe rounds (0 = infinite) |
| `--first-ttl` | `-f` | 1 | Starting TTL |
| `--psize` | `-s` | 64 | Payload size in bytes |
| `--paths` | | 6 | Number of ECMP flows to probe |
| `--no-paths` | | | Disable ECMP, single-path mode |
| `--targets` | | | File of targets to trace, one per line (combined with positional args, max 16 total) |
| `--report` | `-r` | | Report mode: non-interactive, print table and exit |
| `--no-dns` | `-n` | | Skip reverse DNS lookups |
| `--no-asn` | | | Skip ASN lookups |
| `--alert-loss` | | 5.0 | Loss% threshold for destination alert (0 = disabled) |
| `--alert-latency` | | 0 | Latency threshold for destination alert (0 = disabled) |
| `--alert-rounds` | | 3 | Consecutive bad rounds before alert fires |
| `--no-alert` | | | Disable alerting |
| `--theme` | | (config) | Color theme slug (e.g. 'dracula', 'nord') |
| `--version` | | | Print version and exit |

---

## Keyboard Shortcuts

| Key | Action |
|-----|--------|
| <kbd>j</kbd> / <kbd>k</kbd> | Scroll up/down |
| <kbd>g</kbd> / <kbd>G</kbd> | Jump to top/bottom |
| <kbd>p</kbd> | Pause/resume probing |
| <kbd>r</kbd> | Reset all hop stats |
| <kbd>d</kbd> | Cycle display modes (Default → Health → Latency → Variability → Probe Detail) |
| <kbd>q</kbd> | Quit (prints final summary) |
| <kbd>Esc</kbd> | Open settings menu |

**Multi-target mode only:**

| Key | Action |
|-----|--------|
| <kbd>↑</kbd> / <kbd>↓</kbd> | Move focus between targets (also <kbd>j</kbd> / <kbd>k</kbd>) |
| <kbd>Tab</kbd> | Cycle focus to next target |
| <kbd>Space</kbd> | Mark/unmark target for column comparison |
| <kbd>c</kbd> | Toggle column view (bordered panes, one per target) |

---

<details>
<summary>Architecture</summary>

`via` is built on a fully concurrent architecture that keeps every subsystem independent:

- **Protocol-agnostic probe engine** runs in its own goroutine behind a `ProbeProtocol` interface, firing packets without waiting for DNS or the UI
- **DNS resolver** uses a worker pool (4 goroutines) with dedup and caching — lookups never block probes
- **ASN enricher** uses the same worker pool pattern to look up AS numbers via Team Cymru DNS — zero config, no API keys
- **TUI renders independently** on a 100ms tick, reading from thread-safe shared state
- **No serialized I/O** — unlike mtr (single-threaded C with synchronous DNS), every operation runs concurrently

The result: probes keep firing at full speed while hostnames resolve in the background and the UI stays smooth.

</details>

## Testing

`via` runs a cross-platform test matrix on every push to `main` and `feat/*`
branches (see `.github/workflows/test.yml`).

**Locally:**

```
make test          # go test ./...  (fast, no race detector)
make test-race     # go test -race ./...
make test-tui      # TUI package only
make coverage-gate # enforce per-package coverage floors
```

**Regenerating golden files** after intentional TUI output changes:

```
make golden-update
```

Then review the diff to `internal/tui/testdata/**/*.golden` and commit
alongside the code change.

### Soak tests (opt-in)

Long-running live-network sweep to catch environmental, timing, and leak
bugs. NEVER runs on CI. Requires `make install` first plus sudo or
CAP_NET_RAW on `~/bin/via`.

```
make soak-test
```

Runs 10+ trace scenarios (v4/v6/mixed, UDP/TCP/ICMP/auto) against public
hosts, tracks goroutine count and heap growth, and reports any suspicious
patterns. ~30 minute runtime.

**CI matrix:** Ubuntu, macOS, and Windows against Go 1.25.6. Build, race,
coverage, and per-package coverage floors all run on each OS.

## License

MIT
