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
# Basic trace with ECMP multipath (default: 6 flows)
via google.com

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
```

### Avoid sudo (Linux only)

Grant raw socket capability once:

```bash
sudo setcap cap_net_raw+ep $(which via)
via google.com    # no sudo needed
```

## What You See

```
  via v0.0.1   Target: google.com (142.250.80.46)    Proto: UDP/ECMP    Probes: 847
──────────────────────────────────────────────────────────────────────────────────────────────
  #         IP                 Hostname               Loss%    Snt   Avg      Best     Wrst     StDev    Last     Stab
  1         192.168.1.1        router.local            ~50%     84    1.2ms    0.8ms    2.1ms    0.3      1.1ms
  2         10.0.0.1           isp-gw.example.net      0.0%    84    5.4ms    4.8ms    6.9ms    0.5      5.2ms
  3    ├──  72.14.215.65       ae-5.r21.snjsca04.us    0.0%    42    12.3ms   11.1ms   14.2ms   0.8      11.8ms   [100%]
       └──  72.14.215.69       ae-7.r21.snjsca04.us    -        -    13.1ms   11.8ms   15.0ms   0.7      12.5ms   [100%]
  4         *
  5         142.250.80.46      lax17s55-in-f14.1e100   0.0%    84    14.1ms   13.0ms   16.5ms   0.9      13.9ms
──────────────────────────────────────────────────────────────────────────────────────────────
  Tracing... 5/5 hops    Elapsed: 1m24s    DNS: on    6 flows    j/k:scroll p:pause r:reset n:dns d:compact q:quit
```

## Features

- **ECMP multipath** -- Paris-traceroute-style UDP probing reveals all parallel paths through load-balanced networks (6 flows by default)
- **Tree view** -- divergence points auto-expand with `├──`/`└──` connectors showing multiple IPs at the same TTL
- **Path stability** -- 50-round rolling window tracks how consistent each path is (`[100%]` = always present)
- **ICMP rate-limit detection** -- hops that rate-limit TTL Exceeded replies show `~N%` in dim instead of false-positive red loss
- **Live updating** -- hops appear and stats refine in real time
- **Full mtr columns** -- Snt, Avg, Best, Wrst, StDev, Last, Loss% (adaptive to terminal width)
- **Viewport scrolling** -- j/k to scroll, g/G to jump to top/bottom for long traces
- **Async DNS** -- hostnames resolve in the background without blocking probes
- **Report mode** -- non-interactive output with Flows and Stability columns (`via -r`)
- **Auto sudo** -- detects missing permissions and re-execs under sudo automatically
- **Clean exit** -- press `q` or `Ctrl-C` for a final summary

## Architecture

`via` is built on a fully concurrent architecture that keeps every subsystem independent:

- **Probe engine** runs in its own goroutine, firing packets without waiting for DNS or the UI
- **DNS resolver** uses a worker pool (4 goroutines) with dedup and caching -- lookups never block probes
- **TUI renders independently** on a 100ms tick, reading from thread-safe shared state
- **No serialized I/O** -- unlike mtr (single-threaded C with synchronous DNS), every operation runs concurrently

The result: probes keep firing at full speed while hostnames resolve in the background and the UI stays smooth.

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--max-hops` | | 30 | Maximum number of hops to probe |
| `--timeout` | | 1s | Per-probe response timeout |
| `--interval` | `-i` | 1s | Probe round interval |
| `--count` | `-c` | 0 | Number of probe rounds (0 = infinite) |
| `--first-ttl` | `-f` | 1 | Starting TTL |
| `--psize` | `-s` | 64 | ICMP payload size in bytes |
| `--paths` | | 6 | Number of ECMP flows to probe |
| `--no-paths` | | | Disable ECMP, single-path mode |
| `--report` | `-r` | | Report mode: non-interactive, print table and exit |
| `--no-dns` | `-n` | | Skip reverse DNS lookups |
| `--version` | | | Print version and exit |

## Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `j`/`k` | Scroll up/down |
| `g`/`G` | Jump to top/bottom |
| `p` | Pause/resume probing |
| `r` | Reset all hop stats |
| `n` | Toggle DNS resolution |
| `d` | Toggle compact display |
| `q` | Quit (prints final summary) |

## Roadmap

- **M3:** UDP/TCP probe modes, ASN enrichment, ping supplement for rate-limited hops
- **M4:** Display mode cycling, latency sparklines, multi-target, JSON/CSV/DOT export
- **M5:** Theme engine with 20 built-in color themes
- **M6:** MPLS decoding, VPN/tunnel detection
- **M7:** Bidirectional path analysis with remote agent

## License

MIT
