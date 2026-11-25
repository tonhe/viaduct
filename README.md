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

> This README describes the development version on `main`. For the latest release, see the [README at v0.2.1](https://github.com/tonhe/viaduct/blob/v0.2.1/README.md).

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
- **Config persistence** — settings saved to `~/.config/via/config.toml` (see [Config file location](#config-file-location) for other platforms); CLI flags override at runtime
- **TUI export** — press <kbd>e</kbd> to write the current trace to a JSON, CSV, or DOT file (`export_format` config key)

---

## Protocol Modes

| Protocol | Flag | ECMP | Dest Detection | Best For |
|----------|------|------|----------------|----------|
| UDP | `-P udp` (default) | Yes (src-port variation) | ICMP Port Unreachable | General use, ECMP discovery |
| TCP SYN | `-P tcp` | Yes (src-port variation) | SYN-ACK or RST | Targets behind firewalls |
| ICMP | `-P icmp` | No (single-path) | Echo Reply | Simple traces, compatibility |
| Auto | `-P auto` | Yes → No on fallback | Per active protocol | Unknown networks |

**Auto mode** starts with UDP for ECMP discovery. If no responses are received after 3 rounds (~3 seconds), it automatically falls back to ICMP. The TUI header updates to reflect the active protocol.

**TCP mode by platform.** The destination's SYN-ACK or RST is what marks it as reached, and where `via` reads it differs:

| Platform | TCP replies are read from | Status |
|----------|---------------------------|--------|
| Linux | the raw TCP socket the probes are sent on | Supported |
| macOS | a packet capture (BPF) on the outgoing interface, because the macOS kernel does not hand TCP segments to raw sockets | Supported |
| FreeBSD, OpenBSD, NetBSD | the raw TCP socket, which is not expected to receive them | Not supported: the destination shows as lost. Use `-P udp` or `-P icmp` |

On macOS the capture needs a free `/dev/bpf*` device (one per target) and an interface type `via` knows: Ethernet, Wi-Fi, loopback and `utun`/`ipsec` tunnels. If it cannot be set up, `via` stops with an error that says why and suggests another mode, for example:

```
TCP mode on macOS reads the destination's replies from a packet capture (BPF), which could not be set up: unsupported link type 127 on interface en0. Use -P udp or -P icmp instead
```

The other reasons are `no interface has the source address <address>` and `no free BPF device`. A reply counts only when it comes from the target's address.

---

## IPv6 Support

`via` runs over IPv4 or IPv6, selecting the family automatically based on the target's DNS records and the host's transport capability. Force a specific family with `-4` or `-6`:

```bash
via google.com          # auto-selects (prefers IPv6 if available)
via -4 google.com       # IPv4
via -6 google.com       # IPv6
```

If `-6` is requested and the target has no AAAA record (or the host has no IPv6 transport), `via` exits with a clear error rather than silently falling back.

- **Capability:** a host counts as IPv6-capable only if it has a global or unique-local IPv6 address. A link-local address alone is not enough.
- **Route pre-flight:** before probing an IPv6 target, `via` checks that the host has a route to it. If family was auto-selected and there is no IPv6 route, it falls back to IPv4 and shows `no IPv6 route to <address>; using IPv4` in the status bar (on stderr in report mode). With `-6` it exits with an error instead.
- **IPv4-mapped addresses:** `::ffff:192.0.2.1` is traced as `192.0.2.1` over IPv4. Combining one with `-6` is an error.
- **Multicast:** multicast targets (`224.0.0.0/4`, `ff00::/8`) are rejected at startup.
- **Link-local:** IPv6 link-local targets (`fe80::/10`), with or without a zone (`fe80::1%eth0`), are not supported and are rejected at startup.
- **Narrow terminals:** a 39-character address is shown in full where it fits. Where it does not, it is shortened in the middle (`2001:db8…55:6661`) so hops that share a prefix stay distinguishable.

---

## Rate-Limit Detection

Many routers rate-limit ICMP TTL Exceeded responses, causing tools like `mtr` and `traceroute` to show phantom packet loss at intermediate hops. This is the single most common source of misdiagnosis in traceroute output — operators see 60% loss at hop 3 and assume a problem, when the router is simply deprioritizing TTL Exceeded replies.

`via` automagically detects this. The tracer interleaves per-round probes with budgeted direct ICMP Echo Requests sent to each discovered hop. When TTL-probe loss is high but the direct echo responses remain healthy, `via` marks the hop rate-limited and reports echo-derived stats instead of the misleading TTL Exceeded data.

The `†` dagger in the loss column (`~N%†`) indicates a hop whose stats come from direct echo probes rather than TTL Exceeded replies. No configuration needed — it just works.

**How loss is counted.** Loss% is per probe: TTL-limited probes that got no reply, over probes sent, across completed rounds. A round still waiting for replies is not counted, so a live view never shows probes in flight as lost. A probe that could not be sent (the socket refused the write, or did not take it within 100 ms) is not counted as sent and not counted as lost; a round in which nothing could be sent to a hop is not a round for that hop and does not count toward alerting. `Rnd` is the number of rounds.

**When a hop is marked rate-limited.** Only when the difference between its TTL-probe loss and its echo loss is statistically significant (p < 0.01) with at least 20 echo samples, so an ordinary lossy link, where both figures are noisy samples of the same loss, is not mistaken for rate limiting. The verdict is made for the hop: every TTL-limited probe sent to it, of every type, against every echo sent to its nodes. The destination is judged the same way, because a host throttles the Port Unreachable errors it sends in answer to UDP probes just as a router throttles Time Exceeded.

**Before the verdict is possible.** Until a hop has 20 echo samples (about 20 rounds), loss at that hop may be rate limiting or may be real. It is shown as `N%?`, dimmed, and does not count toward alerting. Once the samples are in, it becomes either a plain figure or `~N%†`. A hop that answers from an IPv6 link-local address (`fe80::…`) is not echoed, so loss there stays `N%?`.

With several targets in the TUI the verdict takes longer. A trace sends up to 64 echoes a round; the TUI divides that between its targets (64 / targets, never fewer than 4), and a target spreads its share over the nodes of its path. When the share is smaller than the number of nodes, each node gets budget / nodes echoes a round, so 20 samples take about 20 × nodes / budget rounds: about 75 rounds for 16 targets on a 15-node path. Report mode traces targets one after another, so each target gets the full 64.

TUI, report mode and alerting all use the same figure, and the JSON carries it. In the JSON:

- Each hop has `loss_pct` (raw TTL-probe loss), `headline_loss_pct` (the figure shown) and `loss_kind` (`measured`, `rate_limited` or `provisional`).
- Every count is in probes. A hop has `probes_sent`, `probes_received`, `sent` (the same as `probes_sent`) and `rounds`. A hop that never answered has no nodes and still reports what was sent to it.
- A node has `sent` (probes sent to its hop) and `received` (its own replies), a `probes` breakdown by probe type with `sent` and `received` for each, and the `rate_limited` verdict.
- The top-level `probes` is the total sent to the hops in the report, and equals the sum of the hops' `probes_sent`.

CSV carries the same figures as columns (`loss_pct` raw, plus `headline_loss_pct`, `loss_kind`, `rate_limited` and the per-probe-type counts), and a DOT node label shows the hop's headline figure as the views render it (`~N%†`, `N%?` or a plain `N%`).

Echoes go to the nodes of the path only, one per node per round, up to and including the destination.

---

## Display Modes

Press <kbd>d</kbd> to cycle through five view presets:

| Mode | Columns | Best For |
|------|---------|----------|
| **Default** | Loss%, Rnd, Avg, Best, Wrst, StDev, Last | General overview (mtr-equivalent) |
| **Health** | Loss%, Avg, Delta, Sparkline, Trend | Quick health assessment |
| **Latency** | Avg, Best, Wrst, GMean, Delta, Sparkline | Detailed latency analysis |
| **Variability** | StDev, Jitter, Jitter Mean, Sparkline, Trend | Stability analysis |
| **Probe Detail** | Loss%, % of replies, ICMP-TTL, UDP/TCP-TTL, Echo (received/sent), Rnd | Per-probe-type diagnostics, rate-limit verification |

**Additional metrics:**

- **Delta** — hop-to-hop latency difference; the largest delta is highlighted in amber to pinpoint where delay is introduced
- **GMean** — geometric mean, a better central tendency for skewed latency distributions
- **Jitter / Javg** — current jitter (`|rtt - prevRTT|`) and running mean jitter
- **Sparkline** — heat-strip mini chart (green→gold→red gradient) showing per-round average latency, one cell per round for the 14 most recent rounds (28 are kept per node); a round with no reply renders as a dim `·`
- **Trend** — sliding-window linear regression over 10 rounds; shows `▲ degrading`, `▼ improving`, or `— stable`

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
| <kbd>↑</kbd> / <kbd>↓</kbd> | Move focus to the previous / next target (stops at the first and last) |
| <kbd>Tab</kbd> | Move focus to the next target, wrapping from the last to the first |
| <kbd>j</kbd> / <kbd>k</kbd> | Scroll the focused target's hop table down / up (they do not move focus). Column view does not scroll, so there <kbd>j</kbd>, <kbd>k</kbd>, <kbd>g</kbd> and <kbd>G</kbd> do nothing and the hop table keeps its scroll position |
| <kbd>Space</kbd> | Mark/unmark target — also selects which panes show in column view |
| <kbd>c</kbd> | Toggle column view — bordered panes side by side (`# IP Host Avg Loss`), one per target, dividing the terminal width equally; every pane has the same light border, and the focused pane is shown by an accent-colored border and a bold label |

Global keys (`p` pause, `r` reset, `d` display mode) apply to all targets simultaneously.

Per-target IP family is auto-selected independently. If a target fails to resolve or probe, it is shown as errored in its summary row while all other targets continue tracing. If every target errors at startup, `via` exits 1 with each error on stderr.

In report mode (`-r`, or `-o`) with several targets, a target that fails does not stop the others: the rest are traced and reported, each failure is written to stderr, and `via` exits non-zero. In JSON a failed target stays in `targets` with an `error` field and no hops; in CSV it is one row with the message in the `error` column.

Exports in multi-target mode group all targets: JSON uses a `targets` array, CSV adds a leading `target` column, and DOT produces a single digraph with shared-IP node merging. Report mode (`-r`) prints each target's table in sequence.

Quitting the TUI prints a final summary table for every target, not only the focused one. The tables are in the order of the summary panel, with a blank line between them. A target that failed at startup gets one line in place of its table: `via — <target> — error: <message>`.

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

# Export to a file: -o implies report mode and writes to stdout
via -c 5 -o json example.com > trace.json
via -c 5 -o csv example.com > trace.csv

# Render the path as a graph (needs Graphviz)
via -c 5 -o dot example.com | dot -Tsvg > path.svg
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
| `--count` | `-c` | 0 | Number of probe rounds (0 = infinite; report mode runs 10 when this is not set) |
| `--first-ttl` | `-f` | 1 | Starting TTL |
| `--psize` | `-s` | 64 | Payload size in bytes |
| `--paths` | | 6 | Number of ECMP flows to probe |
| `--no-paths` | | | Disable ECMP, single-path mode |
| `--ipv4` | `-4` | | Force IPv4; an error if the target has no A record or the host has no IPv4. Cannot be combined with `-6` |
| `--ipv6` | `-6` | | Force IPv6; an error if the target has no AAAA record or the host has no IPv6 (see [IPv6 Support](#ipv6-support)). Cannot be combined with `-4` |
| `--targets` | | | File of targets to trace, one per line (combined with positional args, max 16 total) |
| `--report` | `-r` | | Report mode: non-interactive, run `--count` rounds (10 if not set), print table and exit |
| `--output` | `-o` | | Export format: `json`, `csv`, or `dot`, written to stdout (implies `--report`; see [Exporting from the command line](#exporting-from-the-command-line)) |
| `--no-dns` | `-n` | | Skip reverse DNS lookups |
| `--no-asn` | | | Skip ASN lookups |
| `--alert-loss` | | 5.0 | Loss% threshold for destination alert (0 = disabled) |
| `--alert-latency` | | 0 | Latency threshold for destination alert (0 = disabled) |
| `--alert-rounds` | | 3 | Consecutive bad rounds before alert fires |
| `--no-alert` | | | Disable alerting |
| `--theme` | | (config) | Color theme slug (e.g. 'dracula', 'nord') |
| `--debug-log` | | | Append diagnostics to this file: send failures, one line per completed round, a per-hop loss snapshot every 10 rounds and at exit, and alerts raised and cleared. See [Debug log](#debug-log) |
| `--version` | `-v` | | Print `via version <version> (build <build>)` and exit. A release build prints the release number (`0.3.0`). `make build` prints `git describe` without the leading `v` (`0.2.1-21-gc6df19c`: 21 commits after v0.2.1, with `-dirty` for a modified tree). A build that sets no version (`go build`, `go install`) prints the module version the Go toolchain recorded; if there is none, `dev+<revision>` (the first 12 characters of the commit, with `.dirty` for a modified tree); if there is no revision either, `dev` |
| `--help` | `-h` | | Print usage and the flag list, then exit |

### Debug log

`--debug-log <file>` appends timestamped lines to a file, in the TUI and in report mode. Each line is tagged with the target's address, so one file holds every target of a run. Without the flag nothing is written.

```
2026-11-25T04:38:44.123Z [192.0.2.9] send failed: icmp-ttl to 192.0.2.9 (ttl 3): no buffer space available; logged once for this address
2026-11-25T04:38:45.201Z [192.0.2.9] round 12: probes not sent: icmp-ttl 0 timed out, 3 failed, 0 skipped
2026-11-25T04:38:46.214Z [192.0.2.9] round 12: took 1.214s; sent icmp-ttl=8 udp-ttl=24 icmp-echo=8; replies icmp-ttl=8 udp-ttl=21 icmp-echo=8; no reply from 1 of 8 hops probed
2026-11-25T04:38:54.302Z [192.0.2.9] snapshot round=20 ttl=3 nodes=198.51.100.1,198.51.100.2 loss=~0%† kind=rate_limited icmp-ttl=20/0 udp-ttl=60/41 echo=38/0 echo-counted=38/0 rate-limited=yes
2026-11-25T04:38:57.310Z [192.0.2.9] round 23: alert raised: Sustained loss to destination: 33.3% (hop 8 affected)
2026-11-25T04:39:05.330Z [192.0.2.9] round 31: alert cleared: Sustained loss to destination: 45.0% (hop 8 affected)
```

- **Send failures**: each address a send failed for (once per address, with the error) and, for each round, the probes that could not be sent.
- **Round line**, one per completed round: the time since the previous round closed, the probes sent in the round by type, the replies counted in the round by type, and how many of the hops probed answered none of their TTL-limited probes. A round in which every probed hop is silent reads `no reply from 8 of 8 hops probed`. A reply is counted in the round in which it arrives, as it is for loss.
- **Snapshot**, one line per hop up to the destination, every 10 rounds (`snapshot`) and once at exit (`final`): the hop's addresses, its loss exactly as the views show it (`12.3%`, `85.7%?`, `~0%†`) with its kind, and the counts behind it as `sent/lost` over completed rounds: TTL-limited probes by type, direct echoes (`echo`), the echoes that count toward the verdict (`echo-counted`, which leaves out a node that ignores ping) and the rate-limit verdict. A `snapshot` set holds the figures its round closed with and is written when the next round closes, so its timestamp is one round later than its `round=`. The log of a run always ends with a `final` set. A run that ends on a multiple of 10 rounds has no `snapshot` set for its last round: the `final` set stands for it and includes any reply that arrived after the round closed. A run that is killed, or whose round loop stalls, right after a multiple of 10 rounds and before the next round closes has no hop lines for that round: its `snapshot` set was still waiting to be written. That set is missing by design, and the round lines up to that round are in the log.
- **Alerts**: a line when an alert is raised and a line when it clears, with the alert's text. Report mode shows no alerts; its log records the ones the TUI would have raised with the same `--alert-*` settings.
- **TCP replies** (`-P tcp` only), lines starting `tcp receive:`: where the destination's replies are read from (on macOS the BPF device, the interface, its link type and the filter in words), and after each round's sends and at exit the running totals: `round 12: tcp receive totals: 208 segments read, 72 from the target, 72 SYN-ACK/RST, 72 matched`. On Linux "segments read" counts every TCP segment the host received. On macOS a last line gives the capture's own counts when the device is closed.

---

## Keyboard Shortcuts

| Key | Action |
|-----|--------|
| <kbd>j</kbd> / <kbd>k</kbd> | Scroll down / up one line |
| <kbd>↓</kbd> / <kbd>↑</kbd> | Scroll down / up one line (single target; with several targets they move focus, see below) |
| <kbd>g</kbd> / <kbd>G</kbd> | Jump to top/bottom |
| <kbd>p</kbd> | Pause/resume probing (every probe type, the direct echoes included) |
| <kbd>r</kbd> | Reset all hop stats |
| <kbd>d</kbd> | Cycle display modes (Default → Health → Latency → Variability → Probe Detail) |
| <kbd>e</kbd> | Export the current trace to a file (see [Exporting from the TUI](#exporting-from-the-tui)) |
| <kbd>q</kbd> | Quit (prints final summary) |
| <kbd>Esc</kbd> | Open settings menu |

**Multi-target mode only:**

| Key | Action |
|-----|--------|
| <kbd>↑</kbd> / <kbd>↓</kbd> | Move focus to the previous / next target (<kbd>j</kbd> / <kbd>k</kbd> still scroll) |
| <kbd>Tab</kbd> | Move focus to the next target, wrapping to the first |
| <kbd>Space</kbd> | Mark/unmark target for column comparison |
| <kbd>c</kbd> | Toggle column view (bordered panes, one per target) |

### Config file location

| Platform | Path |
|----------|------|
| Linux/macOS (default) | `~/.config/via/config.toml` |
| Linux/macOS with `XDG_CONFIG_HOME` set | `$XDG_CONFIG_HOME/via/config.toml` |
| Windows | `%APPDATA%\via\config.toml` (falls back to `%USERPROFILE%\AppData\Roaming\via\config.toml` if `APPDATA` is empty) |

A key from an older release that `via` no longer uses does not stop the file from loading. The key is ignored, and `via` prints one warning line for it on stderr at startup:

```
warning: config: PingSupplement was removed in v0.3.0 and is ignored
```

Saving settings from the menu writes only current keys, so the removed key is gone from the file after the next save. `ping_supplement` (also accepted as `PingSupplement`) is the only such key today.

### Exporting from the TUI

Press <kbd>e</kbd> during a trace to write a snapshot of the current hop table to a file.

- **Format** — `json` by default. Set `export_format` in the config file (see [Config file location](#config-file-location)) to `json`, `csv`, or `dot` to change it. Only these three values are valid; any other value produces an empty file.

  ```toml
  export_format = "csv"
  ```

  `via` reads the config file at startup, so set `export_format` before starting `via` or restart it after changing the file. Pressing <kbd>e</kbd> during a trace keeps using the format loaded at startup.

  The settings menu does not show `export_format`, and saving settings from the menu rewrites the config file without it, so add the line again after saving settings.
- **Location** — the file is written to the current working directory (the directory you ran `via` from).
- **File name** — `via-<target>-<timestamp>.<format>`, where `<target>` is the target as given on the command line and `<timestamp>` is local time as `YYYYMMDD-HHMM` (e.g. `via-google.com-20261124-1530.json`). Exporting again within the same minute overwrites the earlier file. With more than one target the file is `via-multi-<timestamp>.<format>` and holds every target that did not fail at startup.
- **Result** — the status line shows `Exported to <path>` or `Export failed: <error>` for a few seconds.

### Exporting from the command line

`--output` / `-o` exports a trace without the TUI. It takes `json`, `csv` or `dot` (case-insensitive; `graphviz` is accepted for `dot`); any other value is an error.

- **Report mode** — `-o` implies `--report`, so `-r` is optional. `via` runs `--count` rounds (10 if `-c` is not given), writes the export and exits.
- **Where it goes** — standard output. There is no file argument; redirect or pipe it. Warnings and errors go to stderr, so they stay out of the export.

```bash
via -c 5 -o json example.com > trace.json
via -c 5 -o csv example.com > trace.csv
via -c 5 -o dot example.com | dot -Tsvg > path.svg
```

- **JSON** — the full report: version, timestamp, target, protocol, and every hop with its nodes, loss figures, probe counts and RTT stats (see [Rate-Limit Detection](#rate-limit-detection) for the loss fields).
- **CSV** — a header row, then one row per node: `ttl`, `ip`, `hostname`, `asn`, `as_org`, `loss_pct`, `sent`, `received`, `avg_ms`, `best_ms`, `worst_ms`, `last_ms`, `stdev_ms`, `jitter_ms`, `flow_ids` (joined with `;`), `stability_pct`, `trend`, `headline_loss_pct`, `loss_kind`, `rate_limited`, `icmp_ttl_sent`, `icmp_ttl_received`, `primary_sent`, `primary_received`, `echo_sent`, `echo_received`, `error`. `loss_pct` is the raw TTL-probe loss; `headline_loss_pct` and `loss_kind` are the hop's, repeated on each of its nodes. A hop that never answered is one row with `*` as its `ip`, the node columns empty, and the hop's `loss_pct`, `sent`, `received`, `headline_loss_pct` and `loss_kind`. `error` is empty for a target that was traced. With several targets, a target that could not be traced is one row with its `target`, the hop and node columns empty, and the message in `error`.
- **DOT** — a Graphviz digraph of the path: one box per node labelled with IP, hostname, average RTT and loss, nodes grouped into a dashed cluster per AS, and silent hops drawn as dashed ellipses.

With more than one target, the targets are traced one after another and written as a single export: JSON is one object with a `targets` array, CSV has a leading `target` column, and DOT is one graph in which a router seen by several targets is a single node.

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

## Changelog and contributing

Release notes are on the [GitHub releases page](https://github.com/tonhe/viaduct/releases), which is the project's changelog. To contribute, start with [CONTRIBUTING.md](CONTRIBUTING.md). Design notes are in [docs/](docs/): [architecture](docs/ARCHITECTURE.md) and the [TUI design guide](docs/UI-DESIGN.md).

## License

MIT
