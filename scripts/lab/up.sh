#!/usr/bin/env bash
# Build the network lab: seven namespaces, dual-stack veth links, static
# routes. Idempotent: it tears down any previous lab first. See lib.sh for the
# topology.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=scripts/lab/lib.sh
source "$here/lib.sh"
lab_require_root

"$here/down.sh"

for n in $LAB_NODES; do
  ip netns add "$(lab_ns "$n")"
  lab_x "$n" ip link set lo up
  # Asymmetric return paths exist in the ECMP scenario; strict reverse-path
  # filtering would drop them.
  lab_x "$n" sysctl -qw net.ipv4.conf.all.rp_filter=0
  lab_x "$n" sysctl -qw net.ipv4.conf.default.rp_filter=0
  # No duplicate address detection: addresses are usable immediately.
  lab_x "$n" sysctl -qw net.ipv6.conf.all.accept_dad=0
  lab_x "$n" sysctl -qw net.ipv6.conf.default.accept_dad=0
done

# link A B A4 B4 A6 B6: a veth pair between A and B. The interface in A is
# named "A-B" and the one in B is "B-A". /30 for IPv4, /64 for IPv6.
link() {
  local a=$1 b=$2 a4=$3 b4=$4 a6=$5 b6=$6
  ip link add "$a-$b" netns "$(lab_ns "$a")" type veth peer name "$b-$a" netns "$(lab_ns "$b")"
  lab_x "$a" ip addr add "$a4/30" dev "$a-$b"
  lab_x "$b" ip addr add "$b4/30" dev "$b-$a"
  lab_x "$a" ip -6 addr add "$a6/64" dev "$a-$b" nodad
  lab_x "$b" ip -6 addr add "$b6/64" dev "$b-$a" nodad
  lab_x "$a" ip link set "$a-$b" up
  lab_x "$b" ip link set "$b-$a" up
}

link client r1   192.0.2.1  192.0.2.2  2001:db8:0:1::1 2001:db8:0:1::2
link r1     r2   192.0.2.5  192.0.2.6  2001:db8:0:2::1 2001:db8:0:2::2
link r2     r3   192.0.2.9  192.0.2.10 2001:db8:0:3::1 2001:db8:0:3::2
link r2     r3b  192.0.2.13 192.0.2.14 2001:db8:0:4::1 2001:db8:0:4::2
link r3     dst1 192.0.2.17 192.0.2.18 2001:db8:0:5::1 2001:db8:0:5::2
link r3b    dst1 192.0.2.21 192.0.2.22 2001:db8:0:6::1 2001:db8:0:6::2
link r3b    dst2 192.0.2.25 192.0.2.26 2001:db8:0:7::1 2001:db8:0:7::2

# Target addresses live on the destinations' loopbacks, so dst1 is reachable
# through either r3 or r3b.
lab_x dst1 ip addr add "$LAB_DST1_V4/32" dev lo
lab_x dst1 ip -6 addr add "$LAB_DST1_V6/128" dev lo nodad
lab_x dst2 ip addr add "$LAB_DST2_V4/32" dev lo
lab_x dst2 ip -6 addr add "$LAB_DST2_V6/128" dev lo nodad

for r in $LAB_ROUTERS; do
  lab_x "$r" sysctl -qw net.ipv4.ip_forward=1
  lab_x "$r" sysctl -qw net.ipv6.conf.all.forwarding=1
done
# Hash on addresses and ports, so flows with different source ports can take
# different next hops when a multipath route is installed (ECMP scenario).
lab_x r2 sysctl -qw net.ipv4.fib_multipath_hash_policy=1
lab_x r2 sysctl -qw net.ipv6.fib_multipath_hash_policy=1

# IPv4 routes.
lab_x client ip route add default via 192.0.2.2
lab_x r1     ip route add default via 192.0.2.6
lab_x r2     ip route add 192.0.2.0/30 via 192.0.2.5
lab_x r2     ip route add 192.0.2.16/30 via 192.0.2.10
lab_x r2     ip route add 192.0.2.20/30 via 192.0.2.14
lab_x r2     ip route add 192.0.2.24/30 via 192.0.2.14
lab_x r2     ip route add "$LAB_DST2_V4/32" via 192.0.2.14
lab_x r3     ip route add default via 192.0.2.9
lab_x r3     ip route add "$LAB_DST1_V4/32" via 192.0.2.18
lab_x r3b    ip route add default via 192.0.2.13
lab_x r3b    ip route add "$LAB_DST1_V4/32" via 192.0.2.22
lab_x r3b    ip route add "$LAB_DST2_V4/32" via 192.0.2.26
lab_x dst1   ip route add default via 192.0.2.17
lab_x dst2   ip route add default via 192.0.2.25

# IPv6 routes.
lab_x client ip -6 route add default via 2001:db8:0:1::2
lab_x r1     ip -6 route add default via 2001:db8:0:2::2
lab_x r2     ip -6 route add 2001:db8:0:1::/64 via 2001:db8:0:2::1
lab_x r2     ip -6 route add 2001:db8:0:5::/64 via 2001:db8:0:3::2
lab_x r2     ip -6 route add 2001:db8:0:6::/64 via 2001:db8:0:4::2
lab_x r2     ip -6 route add 2001:db8:0:7::/64 via 2001:db8:0:4::2
lab_x r2     ip -6 route add "$LAB_DST2_V6/128" via 2001:db8:0:4::2
lab_x r3     ip -6 route add default via 2001:db8:0:3::1
lab_x r3     ip -6 route add "$LAB_DST1_V6/128" via 2001:db8:0:5::2
lab_x r3b    ip -6 route add default via 2001:db8:0:4::1
lab_x r3b    ip -6 route add "$LAB_DST1_V6/128" via 2001:db8:0:6::2
lab_x r3b    ip -6 route add "$LAB_DST2_V6/128" via 2001:db8:0:7::2
lab_x dst1   ip -6 route add default via 2001:db8:0:5::1
lab_x dst2   ip -6 route add default via 2001:db8:0:7::1

# Baseline sysctls and the single-path route to dst1.
lab_clear_impairments

# Wait until both targets answer over both families (neighbor discovery).
for t in "$LAB_DST1_V4" "$LAB_DST2_V4" "$LAB_DST1_V6" "$LAB_DST2_V6"; do
  ok=0
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if lab_x client ping -c 1 -W 1 "$t" >/dev/null 2>&1; then ok=1; break; fi
    sleep 0.2
  done
  if [[ $ok -ne 1 ]]; then echo "lab: $t is not reachable from client" >&2; exit 1; fi
done
echo "lab up: client -> r1 -> r2 -> r3 -> dst1 ($LAB_DST1_V4, $LAB_DST1_V6); r2 -> r3b -> dst2 ($LAB_DST2_V4, $LAB_DST2_V6)"
