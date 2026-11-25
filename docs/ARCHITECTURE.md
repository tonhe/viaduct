# Architecture

viaduct (`via`) is a terminal traceroute tool with ECMP multipath discovery, rate-limit detection, ASN enrichment and a bubbletea TUI. It is one Go binary. This document describes the code as it is: packages, how a probe becomes a number on screen, the concurrency model, and the things that are easy to get wrong.

Module path `github.com/tonhe/viaduct`. Binary name `via`. The Go version is the one in `go.mod`.

## Packages

```
cmd/via/            CLI entry point
  main.go           flags, config merge, target resolution, family selection,
                    raw-socket check, TUI wiring, report mode
  ports.go          per-session source-port and ICMP-ID blocks
  targets.go        --targets file parsing, merge, dedupe, 16-target cap
internal/
  probe/            probe engine: Tracer, the three probe tiers, reply matching
    probe.go        Tracer, send loop, ICMP listener, probeMap
    echo.go         echo tier (direct ICMP Echo to discovered hops)
    protocol.go     ProbeProtocol interface
    proto_icmp.go   ICMP Echo probes
    proto_udp.go    UDP probes, IPv4 and IPv6 UDP checksums
    proto_tcp.go    TCP SYN probes, SYN-ACK/RST listener
    tcp_recv.go     where the TCP listener reads replies from, per platform
    tcp_recv_raw.go        every OS but macOS: the raw TCP send socket
    tcp_recv_bpf_darwin.go macOS: a BPF device on the outgoing interface
    tcp_frame.go    BPF filter, BPF record walk, link and IP header stripping
    proto_auto.go   AutoSelector: UDP with one-way fallback to ICMP
    checksum6.go    IPV6_CHECKSUM on the raw IPv6 send sockets
    family.go       everything that differs between IPv4 and IPv6
  hop/              per-TTL and per-responder state: Table, Hop, PathNode,
                    ProbeStats. Loss, rate-limit verdict, sparkline samples
  ecmp/             divergence and convergence analysis over a hop table
  resolve/          reverse DNS worker pool
  asn/              ASN and org lookup over Team Cymru DNS
  sysprobe/         does this host have IPv4 / IPv6 transport
  config/           TOML config file: load, save, validate
  theme/            Base16 themes and the sparkline gradient
  export/           JSON, CSV and DOT exports, single and multi-target
  tui/              bubbletea model, views, settings overlay, alert engine
  lab/              network lab tests (build tag `lab`, tests only)
scripts/
  check-coverage.sh per-package coverage floors
  lab/              network lab topology and scenarios
  mutate/           mutation tester (run with `go run`)
```

`internal/ecmp` is not imported by any other package at present. The TUI finds divergent hops through `hop.Hop.IsDivergent`.

## From command line to probes

`runTrace` in `cmd/via/main.go` does the setup in this order.

1. **Config.** `config.Load` reads `config.toml` from `$XDG_CONFIG_HOME/via/`, else `~/.config/via/`, or `%APPDATA%\via\` on Windows. A missing file gives defaults. `config.Validate` clamps bad values. A flag given on the command line overrides the config value.
2. **Targets.** Positional arguments and the `--targets` file are merged in order and deduplicated. More than 16 is an error.
3. **Capability check.** `sysprobe` reports whether the host has IPv4 and IPv6 transport by looking at interface addresses. IPv6 counts only with a global or unique-local address. A test raw ICMP socket is then opened. If that fails and the process is not root, `via` re-executes itself under `sudo`.
4. **Resolution and family.** `resolveTargetSpecs` resolves each target and picks its family independently. Multicast addresses are dropped. An IPv4-mapped IPv6 literal is traced over IPv4. `selectFamily` honours `-4`/`-6`, otherwise prefers IPv6 when the target has an AAAA record and the host has IPv6 transport. An auto-selected IPv6 target is pre-flighted with a connected UDP socket (a route lookup that sends no packet). With no route, the target falls back to IPv4 and carries a notice. A failure is stored on that target's spec and does not stop the others.
5. **Ports and IDs.** `initPortAllocator` draws random per-process bases. Each session gets a block of 64 source ports, one ICMP ID for TTL probes and one for echo probes (`sessionPorts`). The blocks are disjoint across sessions, and the ID range never overlaps the port range. `--paths` is clamped to 32 so one session's flows can't spill into the next block.
6. **Run.** Report mode (`-r`, or any `-o` format) traces each target in sequence and prints or exports. Otherwise the TUI starts with one session per target.

## The probe engine

A `probe.Tracer` owns one target. `Run` opens a raw ICMP socket for the target's family, starts the listener goroutine, and enters the send loop.

### Sockets

A tracer holds two raw ICMP sockets, and a third raw socket in UDP or TCP mode:

- **The listener's ICMP socket.** Every reply is read from it, and the ICMP TTL probes are written to it. It only ever sends to the target.
- **The echo tier's ICMP socket.** The echo tier writes to every node on the path, and does so on a socket of its own, in both families. With both on one socket, macOS was seen to refuse writes on it for seconds at a time. The echo socket is never read. Echo replies arrive on the listener's socket, as all inbound ICMP does whichever socket the request left on. Where the platform has an ICMP filter for the family (ICMPv6 on Linux, macOS and the BSDs; ICMPv4 on Linux only), a filter that blocks every type keeps the kernel from delivering anything to the echo socket. Where it has none (ICMPv4 on macOS and the BSDs, both families on Windows), the socket's receive buffer is set to the kernel's minimum and the kernel drops that socket's copies once it is full. If the echo socket cannot be opened, the echo tier sends on the listener's socket and the debug log has a line starting `echo socket:`.
- **The UDP or TCP send socket.** The primary flows are written to it. In TCP mode a second listener reads it, except on macOS.
- **macOS, TCP mode: a BPF device.** The macOS kernel hands an incoming TCP segment to its TCP input routine only, so the raw TCP socket can send SYNs and never receives one. The TCP listener reads a BPF device instead (see "TCP replies").

### Three tiers per round

A round walks TTL from `--first-ttl` to the highest TTL worth probing (the destination's TTL once it is known, `--max-hops` before that). For each TTL:

- **Tier 1, ICMP TTL probe.** One ICMP Echo Request with the TTL set, in every protocol mode.
- **Tier 2, primary flows.** With UDP or TCP as the protocol, `--paths` probes that differ only in source port (`BasePort + flow`). UDP's destination port is the base port plus the TTL. TCP sends a SYN to a fixed port. Skipped in ICMP mode, where tier 1 is the probe.

After the TTL walk:

- **Tier 3, echo tier.** From the second round on, direct ICMP Echo Requests at full TTL (64) to hops already discovered. The target set is rebuilt from the hop table at every round end, capped at 8 addresses per TTL, and worked through round-robin under a per-round budget. The budget is 64 for one session and is divided between sessions, with a floor of 4.

Probes are spaced 10 ms apart. After the last send the tracer waits one `--interval`, then closes the round. That wait is the collection window, so a reply that arrives during it counts for the round it belongs to.

While the trace is paused nothing is sent, in any tier. The echo tier does not echo an IPv6 link-local address: an echo target is an address without a zone, and a link-local address without one does not name a link.

### Sends that fail

Every probe goes out through `Tracer.send` (`send.go`), which sets the hop limit, then a write deadline of 100 ms, then writes. A write that fails or does not complete by the deadline is a probe that was not sent: its `probeMap` record is removed, it is not reported as sent, and the loop moves on. After a write times out, the remaining sends to that socket in the round are skipped and counted as not sent, so a socket that has stopped accepting writes costs a round one deadline and not one per probe. The next round tries the socket again. A stall belongs to one socket: the ICMP TTL probes, the UDP or TCP flows and the echo tier each have their own (see "Sockets"), so a stalled echo socket costs a round its echoes and not its TTL probes.

With `--debug-log <file>` the tracer writes one line for each address a send failed for (once per address, with the error) and one line for each round in which probes were not sent, with the counts per probe type.

The same file takes each target's round records, written by `tui.RoundDebug` from the hop table once a round has been closed: one line per round (probes sent and replies counted by type, hops that answered nothing), a per-hop snapshot every 10 rounds and at exit (the headline loss from `hop.Hop.Loss` and the counts behind it), and a line when an alert is raised or cleared. The TUI's sessions and both report-mode paths call it at the point where they close the round, so the log shows the figures the views show. The README's "Debug log" section describes the lines.

### Matching replies

Every probe sent is recorded in the tracer's `probeMap` under a key built from its ports (UDP, TCP) or its ICMP ID and sequence (ICMP). Entries older than twice the timeout are swept at the end of each round.

The ICMP listener handles three message types:

- **Time Exceeded.** The quoted packet is parsed, tried against the primary protocol and then ICMP, and looked up in the `probeMap`. The quoted destination must be this tracer's target, which filters out replies to another `via` process or session.
- **Destination Unreachable.** Same matching. For UDP, Port Unreachable means the destination was reached.
- **Echo Reply.** Matched by ID and sequence, and accepted only when it comes from the address the probe was sent to: the target for a tier-1 probe, the hop for an echo-tier probe.

TCP has a second listener, `listenTCP`. A SYN-ACK, or an RST with ACK, that was sent by the target and matches a probe means the destination was reached. See "TCP replies".

A reply from the target's own address always marks the destination, whatever its ICMP type. Once the destination's TTL is known, later rounds stop there.

Each match becomes a `probe.Result` with the TTL, responder address, RTT, flow ID and probe type (`icmp-ttl`, `udp-ttl`, `tcp-ttl`, `icmp-echo`).

### TCP replies

`listenTCP` (`proto_tcp.go`) reads TCP segments from a `tcpSegmentSource`: bytes that start at the TCP header, and the address they were sent from. `Tracer.tcpSource` picks the source when the send loop starts (`tcp_recv.go`).

- **Linux, Windows and the other BSDs: the raw TCP send socket** (`tcp_recv_raw.go`). Linux delivers a copy of every TCP segment the host receives to it, other connections' included. With `IPV6_CHECKSUM` set on the IPv6 socket the kernel also verifies the checksum of what it delivers. The other BSDs are built this way and are expected to receive nothing, as macOS did: TCP mode is not supported there.
- **macOS: a BPF device** (`tcp_recv_bpf_darwin.go`). The first free `/dev/bpfN` is opened and bound to the interface that has the startup source address (`Config.SourceIP`); a target that is one of the host's own addresses is captured on the loopback interface. The device is in immediate mode with a 100 ms read timeout and, off loopback, sees inbound frames only. One device per tracer, so a multi-target run holds one per target.

The kernel filter (`tcpReplyFilter` in `tcp_frame.go`, assembled with `golang.org/x/net/bpf`) accepts a frame only if it is TCP of the trace's family, sent from the target, to a destination port in the tracer's source-port range. The local address is not part of it: the kernel picks the source address of each probe and it is not always `Config.SourceIP`. `captureSource` walks the records of each read (`nextBPFRecord`: a `bpf_hdr` with its own header length, records aligned to 4 bytes) and strips the link header and the IP header (`tcpSegmentFromFrame`). Three link types are known: Ethernet and Wi-Fi (14 bytes), loopback and classic `utun` (4-byte address family), newer `utun` and `ipsec` (none). The parser repeats the filter's tests, so nothing depends on the filter alone.

Dropped, in the filter and in the parser, and never guessed at: VLAN-tagged frames, IPv4 fragments, IPv6 packets with an extension header before TCP, and frames too short for their headers.

If the device cannot be set up (no interface has the source address, an unknown link type, no free device, a failed ioctl) the send loop returns a `tcpCaptureError` and the trace stops with it. It does not run: without the capture the destination can never be seen to answer.

On every platform a segment answers a probe only if its sender is the target. One from another address is ignored and the probe stays outstanding. The listener returns when its source is closed, which the send loop does on its way out; it retries a read that timed out at once, and any other read error after a short pause.

With `--debug-log` the tracer writes where replies are read from (on macOS: the device, the interface, the link type, the buffer size and the filter in words), the listener's running totals after each round's sends and at exit (`tcp receive totals: N segments read, N from the target, N SYN-ACK/RST, N matched`), and on macOS the capture's own totals and the kernel's counters when the device is closed.

### Auto mode

`-P auto` wraps UDP and ICMP in an `AutoSelector`. If three rounds pass with no reply to any primary (UDP) probe, it switches to ICMP, once, and the send loop restarts on the same listener socket.

## Hop state and the numbers

`hop.Table` maps TTL to `hop.Hop`. A `Hop` holds one `PathNode` per responder address seen at that TTL. More than one node means ECMP divergence at that hop. The node with the most replies is the hop's primary node and supplies the hop-level RTT figures.

**Loss** is per probe, at hop level. The tracer reports how many TTL-limited probes of each type it wrote for a TTL each round (`MarkRoundSentByType`; a probe whose send failed is not among them). A round in which nothing was written for a TTL is not a round for that hop: no probes, no `Rnd`, no sparkline cell. Replies other than echo-tier replies count as received. At round end the running counters are copied to closed counters, and `LossPercent` is computed from the closed ones. Probes in the open round are never counted as lost. `Rounds` is what the `Rnd` column shows.

**Per-node, per-probe-type stats** (`ProbeStats`) are kept for ICMP TTL, primary and echo probes. The two TTL-limited kinds are counted by round at node level, because a TTL-limited probe is addressed to a hop distance and not to a node: sent is the number of rounds the node was known, and a round counts as answered if the node replied in it. The echo kind counts actual sends and replies. Echo replies update only the echo slot. They don't feed RTT averages, jitter or the hop's loss.

**Rate-limit verdict.** The verdict is made for the hop: `Hop.RateLimitState` returns undetermined, no or yes. It compares every TTL-limited probe sent to the hop, of every type, with every echo sent to the hop's nodes. It is undetermined until 20 echoes have been sent to the hop's nodes. After that it is yes when the TTL-probe loss exceeds the echo loss by a two-proportion z-test at z >= 2.58 (p < 0.01). The destination is judged the same way as any other hop.

**The figure shown.** `Hop.Loss` returns the figure every view shows and its kind: echo loss for a rate-limited hop (`~N%†`), TTL-probe loss marked provisional while the verdict is undetermined and there is loss (`N%?`), and plain TTL-probe loss otherwise. `hop.FormatLoss` renders it. Alerting uses `Hop.SettledLoss`, which counts provisional loss as none.

**Hops beyond the destination.** Round one probes every TTL, so the destination is recorded at its own TTL and at every TTL beyond; those entries never update again. `Table.TargetTTL` gives the destination's distance, `Table.EchoNodes` stops there, and views and exports read nothing past it.

**Sparkline samples.** At each round end a node records one sample: its average RTT that round if a TTL-limited probe was answered, else the echo RTT if an echo was answered, else a "held" marker if the node is rate-limited and wasn't echoed that round, else a drop. The last 28 samples are kept.

**Other per-node figures.** Min, max, last and average RTT, standard deviation (Welford's online algorithm), geometric mean, jitter and mean jitter, a trend from a linear regression over the last 10 round averages, the set of flow IDs seen, and stability (the share of the last 50 rounds in which the node answered).

## TUI wiring

`tui.Model` holds one `TargetSession` per target (hop table, tracer, alert engine, error or notice) plus the state shared by all of them: focused session, view mode, column view, scroll, pause, resolver, enricher, config.

Per session, `main.go` starts:

- a goroutine running `Tracer.Run`
- a goroutine that forwards each `probe.Result` to the program as `HopUpdateMsg` and submits the address to the ASN enricher

Shared by all sessions: one resolver, one enricher, and one forwarding goroutine for each that turns results into `HostnameMsg` and `ASNMsg`.

The tracer also calls back at three points: probes written for a TTL (straight into the session's `hop.Table`, which is mutex-guarded), an echo sent (`EchoSentMsg`), and round end (`RoundEndMsg`). On `RoundEndMsg` the model closes the round on every hop, hands the tracer the next echo-target set, and updates the alert engine. The alert engine counts consecutive bad rounds, so a round that sent nothing to the destination's hop (paused, or no send succeeded) is skipped: it is neither a bad round nor a good one.

Messages that belong to one target carry a `TargetIdx`. Hostname and ASN results are applied to matching addresses in every session, because two targets often share their first hops.

The view redraws on a 100 ms tick. `p` pauses every tracer through an atomic flag. `r` resets every table.

If every target fails at setup, `via` prints each error and exits 1 before the TUI starts. A target that fails while others succeed stays in the session list as an error row. In multi-target report mode the other targets are still traced, each failure goes to stderr, a failed target stays in the JSON with an `error` field, and the exit status is non-zero.

## Report mode and exports

Report mode runs the same tracer without the TUI. A collector goroutine applies results to a `hop.Table`, round end closes the round and resubmits echo targets, and after the last round there is a 500 ms grace period for late DNS answers. `--count` defaults to 10 here. Targets run one after another.

The text report marks a rate-limited hop with `~N%†` using `Hop.HeadlineLoss`. Setup notices go to stderr so they stay out of piped output.

`internal/export` builds a `Report` from a hop table and writes JSON, CSV or DOT. A hop carries its per-probe loss with the probe counts behind it, and each node carries counts for each probe type and its rate-limit verdict (`yes`, `no`, `undetermined`). Multi-target exports wrap the reports: a `targets` array in JSON, a leading `target` column in CSV, one digraph in DOT. The same writers serve the `e` key in the TUI, which writes a file in the current directory in the configured `export_format` (JSON by default).

## Enrichment

`resolve.Resolver` and `asn.Enricher` have the same shape: a request channel, a pool of 4 workers, a cache, and a pending set so an address is looked up once. Lookups time out after 2 seconds. A full request queue drops the request, and the address is submitted again by a later reply. Neither is on the probe path.

ASN lookups are two TXT queries to Team Cymru: `<reversed address>.origin.asn.cymru.com` (`origin6` for IPv6) for the AS number, then `AS<number>.asn.cymru.com` for the name. Private, loopback, link-local and unique-local addresses are never sent. They are cached as "Private".

## Concurrency summary

```
main goroutine            tea.Program event loop (Update, View)

per session
  tracer send loop        writes probes, sleeps between them, closes rounds
  ICMP listener           blocking reads with a 200 ms deadline, matches replies
  TCP listener            TCP mode only
  result pump             probe.Result -> HopUpdateMsg, ASN submit

shared
  resolver workers (4)    reverse DNS
  enricher workers (4)    Team Cymru lookups
  two forwarders          resolver / enricher results -> messages
```

`Hop` and `PathNode` guard their own state with mutexes, because the tracer goroutine and the update loop both write to the hop table. Everything else in the model changes only inside `Update`.

## Build and release

`make build` builds `./via` and stamps a build timestamp into `main.buildVersion`. Release builds are made by GoReleaser from a `v*` tag, which sets `main.version` from the tag: archives for macOS, Linux and Windows on amd64 and arm64, `.deb` and `.rpm` packages, Windows installers, and a Homebrew tap update. See [RELEASING.md](../RELEASING.md).

## Dependencies

| Dependency | Purpose |
|---|---|
| `github.com/charmbracelet/bubbletea` | TUI event loop |
| `github.com/charmbracelet/lipgloss` | TUI styling and layout |
| `github.com/spf13/cobra` | flag parsing |
| `github.com/pelletier/go-toml/v2` | config file parsing |
| `golang.org/x/net` (`icmp`, `ipv4`, `ipv6`) | ICMP messages, TTL and hop-limit control |
| `github.com/lucasb-eyer/go-colorful` | sparkline gradient interpolation |
| `github.com/charmbracelet/x/exp/teatest` | TUI end-to-end tests |

## Testing

- **Unit tests** in every package. The tracer has seams for its sockets (`listenICMP`, `newSender`, `newEchoSender`, `newTCPSource`) and `cmd/via` has one for the tracer (`newTracer`), so the send loop, the listener and report mode are tested without raw sockets.
- **Golden files** under `internal/tui/testdata/` pin rendered views and end-to-end TUI runs. Regenerate with `make golden-update`.
- **Coverage floors** per package in `scripts/check-coverage.sh`, enforced by `make coverage-gate` and CI.
- **Fuzzing.** A weekly workflow fuzzes the inner-header parser, transport offset, TCP checksum, TCP response parser, both Team Cymru response parsers and config loading.
- **Network lab.** `internal/lab` runs the real binary in a seven-namespace topology (`client`, `r1`, `r2`, `r3`, `r3b`, `dst1`, `dst2`, dual-stack, documentation addresses only) and asserts exact probe counts against nftables counters and a packet capture. Scenarios cover a clean path, deterministic reply drops, a rate-limited router, added delay, ECMP, IPv6 and multi-target isolation. `make lab-build && sudo make lab-test`, or `make lab-docker`.
- **Soak test.** `make soak-test` traces well-known public hosts (the list is in `cmd/via/soak_test.go`) and watches goroutine count and heap growth. Opt-in, never on CI.
- **Mutation testing.** `go run scripts/mutate/main.go <package>...`.

## Gotchas

**Raw sockets.** Every mode needs a raw ICMP socket and opens a second one for the echo tier, and UDP and TCP modes need another raw socket for sending. That means root, or `CAP_NET_RAW` on Linux. `sysprobe` deliberately does not test socket permissions. It answers "does this host have this address family", and the permission failure is left to the capability check so the `sudo` re-exec still happens.

**A raw-socket write can block.** When the kernel answers a write with `EAGAIN`, Go parks the goroutine until the socket is writable, which on macOS over IPv6 was seen never to happen. No probe is written without a write deadline (see "Sends that fail"). A write the kernel accepts and then never transmits cannot be told from a lost probe, and is counted as one.

**One keyspace for all tiers.** The `probeMap` is shared by ICMP, UDP/TCP and echo probes. ICMP keys are `{ID, sequence}` and UDP keys are `{source port, destination port}`, so an ICMP ID equal to a UDP source port would eventually collide as the sequence counter climbs into the destination-port range. `initPortAllocator` keeps the ID range and the port range disjoint for that reason.

**Reply validation.** An ICMP error is trusted only if its quoted packet parses and is addressed to the tracer's target. An Echo Reply is trusted only from the address that was probed. Loosening either lets one session's replies, another process's replies or forged packets into the stats.

**Routers rate-limit Time Exceeded.** Many routers answer only a fraction of TTL-expired packets while forwarding normally. Without the echo tier this reads as loss at a transit hop. The verdict needs 20 echo samples and a significant difference, so an ordinary lossy link, where both figures are noisy samples of the same loss, is not flagged.

**IPv6 UDP needs a checksum.** A receiver discards an IPv6 UDP datagram with a zero checksum and never sends Port Unreachable. UDP probes carry a checksum in both families.

**The kernel fills the IPv6 probe checksum.** A UDP or TCP checksum covers the packet's source address, and the raw send socket is not bound: the kernel picks the source address of every packet, and on a host with several global IPv6 addresses it does not always pick the one found at startup. A checksum computed in `via` over that address is then wrong and the probe is dropped on the path. The IPv6 send sockets therefore set `IPV6_CHECKSUM` (offset 6 for UDP, 16 for TCP) and the probes are built with the field zero. If a platform rejects the option, that send socket is bound to the startup source address instead and its probes carry their own checksum; the debug log says which happened. IPv4 has no such option: IPv4 probes carry a checksum computed over the startup source address.

**Raw TCP reads.** The TCP listener parses its input as a bare TCP segment with no IP header: package `net` strips the IPv4 header from a raw-socket read, a raw IPv6 socket never delivers one, and the macOS capture strips link and IP headers itself.

**macOS does not deliver TCP to raw sockets.** A raw `ip4:tcp` or `ip6:tcp` socket there sends SYNs and receives nothing, so TCP replies are read from BPF (see "TCP replies"). Consequences: replies that come back on another interface than the one that has the startup source address are not seen; an interface change during a run (Wi-Fi to Ethernet, a VPN coming up) leaves the capture on the old interface until restart; and BPF sees a frame before the kernel has checked its TCP checksum, so a reply with a bad checksum is matched on macOS where Linux's IPv6 raw socket (with `IPV6_CHECKSUM`) would not deliver it.

**The destination answers at every TTL past its own.** Views stop at the lowest TTL at which the destination answered, and the sender stops there too.

**Styled strings and padding.** Pad plain text to its column width first, then apply the style. Padding a string that already contains ANSI escapes miscounts its width. See [UI-DESIGN.md](UI-DESIGN.md).

**DNS is slow.** A reverse lookup can take the full 2-second timeout. Resolution is asynchronous and cached, and nothing on the probe path waits for it.
