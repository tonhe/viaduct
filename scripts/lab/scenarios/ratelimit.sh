#!/usr/bin/env bash
# ICMP error rate limiting on one router:
#   ratelimit.sh r1        (or dst1, dst2: a host that throttles its errors)
# The router sends at most one ICMP error per second to each destination
# (Linux icmp_ratelimit; the default mask covers Time Exceeded and Destination
# Unreachable but not Echo Reply). It keeps forwarding packets and keeps
# answering pings, which is how a real router with control-plane policing
# behaves: TTL-limited probes look lossy at this hop, direct echoes do not.
#
# How many Time Exceeded replies get through depends on the kernel's token
# bucket and on timing, so tests compare via with the packet capture rather
# than with a fixed number.
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=scripts/lab/lib.sh
source "$here/lib.sh"
lab_require_root

hop=${1:?usage: ratelimit.sh ROUTER|dst1|dst2}
# A destination host can be limited too: it then throttles the Port
# Unreachable errors it sends in answer to UDP probes, and still answers pings.
case " $LAB_ROUTERS dst1 dst2 " in
  *" $hop "*) ;;
  *) echo "unknown router or host $hop" >&2; exit 2 ;;
esac
lab_x "$hop" sysctl -qw net.ipv4.icmp_ratelimit=1000
lab_x "$hop" sysctl -qw net.ipv6.icmp.ratelimit=1000
echo "$hop now limits ICMP errors to one per second"
