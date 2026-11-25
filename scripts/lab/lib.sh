#!/usr/bin/env bash
# Shared definitions for the network lab. Sourced by up.sh, down.sh, reset.sh
# and scenarios/*.sh; not meant to be run on its own.
#
# Topology (one network namespace per node, veth pairs between them):
#
#   client ── r1 ── r2 ── r3 ─── dst1
#                    └── r3b ──┬─ dst1   (second path, used by the ECMP scenario)
#                              └─ dst2
#
# Every address is from a documentation range (RFC 5737, RFC 3849) and no
# namespace has a route out of the lab.

LAB_PREFIX=${LAB_PREFIX:-vialab}
LAB_NODES="client r1 r2 r3 r3b dst1 dst2"
LAB_ROUTERS="r1 r2 r3 r3b"

# Target addresses (loopbacks on the destination nodes).
LAB_DST1_V4=198.51.100.1
LAB_DST2_V4=203.0.113.1
LAB_DST1_V6=2001:db8:100::1
LAB_DST2_V6=2001:db8:200::1

lab_ns() { echo "${LAB_PREFIX}-$1"; }

# lab_x NODE CMD... runs CMD inside NODE's namespace.
lab_x() { local n=$1; shift; ip netns exec "$(lab_ns "$n")" "$@"; }

lab_require_root() {
  if [[ $(id -u) -ne 0 ]]; then
    echo "the network lab needs root (network namespaces, tc, raw sockets)" >&2
    exit 1
  fi
}

# lab_clear_impairments removes every netem qdisc and restores the baseline
# sysctls and routes, so each scenario starts from the clean topology.
lab_clear_impairments() {
  local n dev
  for n in $LAB_NODES; do
    for dev in $(lab_x "$n" ip -o link show | awk -F': ' '{print $2}' | cut -d@ -f1); do
      [[ $dev == lo ]] && continue
      lab_x "$n" tc qdisc del dev "$dev" root 2>/dev/null || true
    done
    # Drop rules and their counters (scenarios/drop-replies.sh).
    lab_x "$n" nft delete table ip lab 2>/dev/null || true
    lab_x "$n" nft delete table ip6 lab 2>/dev/null || true
  done
  for n in $LAB_ROUTERS dst1 dst2; do
    # Baseline: no ICMP error rate limiting anywhere, so loss is zero unless a
    # scenario adds it. Linux defaults would limit Time Exceeded to ~1/s.
    lab_x "$n" sysctl -qw net.ipv4.icmp_ratelimit=0
    lab_x "$n" sysctl -qw net.ipv6.icmp.ratelimit=0
  done
  # Single path to dst1 through r3.
  lab_x r2 ip route replace "$LAB_DST1_V4/32" via 192.0.2.10
  lab_x r2 ip -6 route replace "$LAB_DST1_V6/128" via 2001:db8:0:3::2
}
