# Roadmap

viaduct is a terminal traceroute tool that shows more than a single-path hop listing. Its core feature is real-time ECMP multipath discovery: it reveals the parallel paths packets take through the network, where traditional traceroute shows one. It is a Go CLI with a live TUI that combines hop-by-hop probing, ASN enrichment, latency sparklines and multiple probe protocols, and it is meant to replace `traceroute`, `mtr` and `paris-traceroute`.

## Order

The active release is v0.3.0 (multi-target mode and interleaved probes), tracked in [#41](https://github.com/tonhe/viaduct/issues/41). After it, the epics in order:

1. **v0.4.0, M6: network intelligence.** MPLS and VPN/tunnel awareness. [#5](https://github.com/tonhe/viaduct/issues/5)
2. **v0.5.0, M7: bidirectional path analysis.** Agent mode. [#6](https://github.com/tonhe/viaduct/issues/6)

The detail of each epic is in its issue and milestone. The order is the owner's. How the order feeds planning is described in [RELEASING.md](RELEASING.md).

Ideas that are not scheduled carry the [`stretch`](https://github.com/tonhe/viaduct/labels/stretch) label.
