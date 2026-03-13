# via (Viaduct)

A modern, terminal-native traceroute tool with real-time ECMP multipath discovery.

`via` replaces `traceroute`, `mtr`, and `paris-traceroute` with a live-updating TUI that shows packet loss, latency stats, reverse DNS, and all parallel paths through load-balanced networks.

## Install

### Homebrew

```bash
brew tap tonhe/tap
brew install viaduct
```

### From source

Requires Go 1.25+:

```bash
git clone https://github.com/tonhe/viaduct.git
cd viaduct
make install    # builds and copies to ~/bin/
```

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

# Disable ping supplement
via --no-ping google.com

# Set alert thresholds (alert on >10% loss sustained for 5 rounds)
via --alert-loss 10 --alert-rounds 5 google.com

# Alert on sustained latency above 200ms
via --alert-latency 200ms google.com

# Disable alerting entirely
via --no-alert google.com
```

### Avoid sudo (Linux only)

Grant raw socket capability once:

```bash
sudo setcap cap_net_raw+ep $(which via)
via google.com    # no sudo needed
```

## What You See

```
  via v0.0.3   Target: google.com (142.250.80.46)    Proto: UDP/ECMP    Probes: 847
──────────────────────────────────────────────────────────────────────────────────────────────
  #         IP                 Hostname               Loss%    Snt   Avg      Best     Wrst     StDev    Last     Stab
  1         192.168.1.1        router.local            0.0%†    10    1.2ms    0.8ms    2.1ms    0.3      1.1ms
  2         10.0.0.1           isp-gw.example.net      0.0%    84    5.4ms    4.8ms    6.9ms    0.5      5.2ms
  3    ├──  72.14.215.65       ae-5.r21.snjsca04.us    0.0%    42    12.3ms   11.1ms   14.2ms   0.8      11.8ms   [100%]
       └──  72.14.215.69       ae-7.r21.snjsca04.us    -        -    13.1ms   11.8ms   15.0ms   0.7      12.5ms   [100%]
  4         *
  5         142.250.80.46      lax17s55-in-f14.1e100   0.0%    84    14.1ms   13.0ms   16.5ms   0.9      13.9ms
──────────────────────────────────────────────────────────────────────────────────────────────
  Tracing... 5/5 hops    Elapsed: 1m24s    DNS: on    6 flows    j/k:scroll p:pause r:reset n:dns [d] Health q:quit
```

## Features

- **Multiple probe protocols** -- UDP (default), TCP SYN, and ICMP modes with `--protocol`/`-P` flag
- **Auto mode** -- starts with UDP, automatically falls back to ICMP if no responses are received
- **TCP SYN probing** -- reaches targets behind firewalls that block UDP/ICMP (port 443 default, configurable with `--port`)
- **ECMP multipath** -- Paris-traceroute-style probing reveals all parallel paths through load-balanced networks (UDP and TCP support multipath, ICMP is single-path)
- **Tree view** -- divergence points auto-expand with `├──`/`└──` connectors showing multiple IPs at the same TTL
- **Path stability** -- 50-round rolling window tracks how consistent each path is (`[100%]` = always present)
- **Automatic rate-limit detection + ping supplement** -- automagically detects devices that rate-limit ICMP TTL Exceeded responses and switches their monitoring to direct ICMP Echo Requests (ping), replacing misleading loss stats with accurate data (marked with `†`)
- **Display modes** -- cycle through 4 views with `d`: Default (standard mtr columns), Health (Loss%, Avg, Delta, Sparkline, Trend), Latency (Avg, Best, Wrst, GMean, Delta, Sparkline), Variability (StDev, Jitter, Jitter Mean, Sparkline, Trend)
- **Inline sparklines** -- heat-strip sparklines with green→red gradient show per-round latency history at a glance (28 samples, lost probes shown as dim dots)
- **Trend detection** -- sliding-window linear regression detects degrading or improving latency trends per hop
- **Hop-to-hop delta** -- shows latency difference between adjacent hops, highlighting the largest contributor in amber
- **Destination alerting** -- monitors destination loss% and latency against configurable thresholds; fires alerts after sustained degradation and identifies the affected hop and ASN
- **Live updating** -- hops appear and stats refine in real time
- **Full mtr columns** -- Snt, Avg, Best, Wrst, StDev, Last, Loss% plus GMean, Jitter, Jitter Mean (budget-based responsive layout adapts to any terminal width)
- **Viewport scrolling** -- j/k to scroll, g/G to jump to top/bottom for long traces
- **ASN enrichment** -- each hop shows its AS number and organization (e.g., `AS13335 Cloudflare`), with AS boundary transitions highlighted in yellow
- **Async DNS** -- hostnames resolve in the background without blocking probes
- **Report mode** -- non-interactive output with Flows and Stability columns (`via -r`)
- **Auto sudo** -- detects missing permissions and re-execs under sudo automatically
- **Clean exit** -- press `q` or `Ctrl-C` for a final summary

## Protocol Modes

| Protocol | Flag | ECMP | Dest Detection | Best For |
|----------|------|------|----------------|----------|
| UDP | `-P udp` (default) | Yes (src-port variation) | ICMP Port Unreachable | General use, ECMP discovery |
| TCP SYN | `-P tcp` | Yes (src-port variation) | SYN-ACK or RST | Targets behind firewalls |
| ICMP | `-P icmp` | No (single-path) | Echo Reply | Simple traces, compatibility |
| Auto | `-P auto` | Yes → No on fallback | Per active protocol | Unknown networks |

**Auto mode** starts with UDP for ECMP discovery. If no responses are received after 3 rounds (~3 seconds), it automatically falls back to ICMP. The TUI header updates to reflect the active protocol.

**ICMP + `--paths`:** Prints a warning and runs single-path (ICMP cannot vary flows for ECMP).

## Rate-Limit Detection & Ping Supplement

Many routers rate-limit ICMP TTL Exceeded responses, causing tools like `mtr` and `traceroute` to show phantom packet loss at intermediate hops. This is the single most common source of misdiagnosis in traceroute output — operators see 60% loss at hop 3 and assume a problem, when the router is simply deprioritizing TTL Exceeded replies.

`via` automagically detects this. After a few probe rounds, it identifies hops where loss is high but all downstream hops are healthy — the hallmark of rate-limiting, not real packet loss. It then launches direct ICMP Echo Requests (pings) to those hops in the background, on a separate socket, and replaces the misleading TTL Exceeded stats with accurate ping-derived data.

The `†` dagger next to the loss column indicates a hop whose stats come from direct ping rather than TTL Exceeded replies. No configuration needed — it just works. Disable with `--no-ping` if you prefer raw TTL Exceeded data.

## Display Modes

Press `d` to cycle through four view presets. Each view focuses on different aspects of the trace:

| Mode | Columns | Best For |
|------|---------|----------|
| **Default** | Loss%, Snt, Avg, Best, Wrst, StDev, Last | General overview (mtr-equivalent) |
| **Health** | Loss%, Avg, Delta, Sparkline, Trend | Quick health assessment |
| **Latency** | Avg, Best, Wrst, GMean, Delta, Sparkline | Detailed latency analysis |
| **Variability** | StDev, Jitter, Jitter Mean, Sparkline, Trend | Stability analysis |

**Additional metrics available in display modes:**

- **Delta** -- hop-to-hop latency difference; the largest delta is highlighted in amber to pinpoint where delay is introduced
- **GMean** -- geometric mean, a better central tendency for skewed latency distributions
- **Jitter / Javg** -- current jitter (`|rtt - prevRTT|`) and running mean jitter
- **Sparkline** -- heat-strip mini chart (green→red gradient) showing per-round average latency (28 rounds); lost probes render as dim `·`
- **Trend** -- sliding-window linear regression over 10 rounds; shows `↑ degrading`, `↓ improving`, or `~ stable`

## Destination Alerting

`via` monitors the destination hop for sustained degradation and alerts when thresholds are exceeded. By default, alerts fire when loss exceeds 5% for 3 consecutive rounds.

When an alert fires, `via` walks backward through the hops to identify which hop and ASN is likely causing the problem, and displays the alert in a red bar above the status line.

Configure with `--alert-loss`, `--alert-latency`, and `--alert-rounds`. Disable with `--no-alert`. Press `r` to reset alert state.

## Architecture

`via` is built on a fully concurrent architecture that keeps every subsystem independent:

- **Protocol-agnostic probe engine** runs in its own goroutine behind a `ProbeProtocol` interface, firing packets without waiting for DNS or the UI
- **DNS resolver** uses a worker pool (4 goroutines) with dedup and caching -- lookups never block probes
- **ASN enricher** uses the same worker pool pattern to look up AS numbers via Team Cymru DNS -- zero config, no API keys
- **TUI renders independently** on a 100ms tick, reading from thread-safe shared state
- **No serialized I/O** -- unlike mtr (single-threaded C with synchronous DNS), every operation runs concurrently

The result: probes keep firing at full speed while hostnames resolve in the background and the UI stays smooth.

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
| `--report` | `-r` | | Report mode: non-interactive, print table and exit |
| `--no-dns` | `-n` | | Skip reverse DNS lookups |
| `--no-asn` | | | Skip ASN lookups |
| `--no-ping` | | | Skip ping supplement for rate-limited hops |
| `--alert-loss` | | 5.0 | Loss% threshold for destination alert (0 = disabled) |
| `--alert-latency` | | 0 | Latency threshold for destination alert (0 = disabled) |
| `--alert-rounds` | | 3 | Consecutive bad rounds before alert fires |
| `--no-alert` | | | Disable alerting |
| `--version` | | | Print version and exit |

## Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `j`/`k` | Scroll up/down |
| `g`/`G` | Jump to top/bottom |
| `p` | Pause/resume probing |
| `r` | Reset all hop stats |
| `n` | Toggle DNS resolution |
| `d` | Cycle display modes (Default → Health → Latency → Variability) |
| `q` | Quit (prints final summary) |

## License

MIT
