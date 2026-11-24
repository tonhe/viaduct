#!/usr/bin/env bash
# Equal-cost multipath: r2 reaches dst1 through both r3 and r3b. r2 hashes on
# addresses and ports (set in up.sh), so probes with different source ports
# can take different next hops. Replies from dst1 always return through r3.
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=scripts/lab/lib.sh
source "$here/lib.sh"
lab_require_root

lab_x r2 ip route replace "$LAB_DST1_V4/32" \
  nexthop via 192.0.2.10 weight 1 \
  nexthop via 192.0.2.14 weight 1
lab_x r2 ip -6 route replace "$LAB_DST1_V6/128" \
  nexthop via 2001:db8:0:3::2 weight 1 \
  nexthop via 2001:db8:0:4::2 weight 1
echo "r2 -> dst1 is now multipath via r3 and r3b"
