#!/usr/bin/env bash
# Fixed added latency on the link from r2 to r3:
#   delay.sh 300ms
# Everything beyond r2 (r3 and dst1) answers that much later. No jitter and no
# loss, so the expected RTT is the delay plus the sub-millisecond base.
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=scripts/lab/lib.sh
source "$here/lib.sh"
lab_require_root

delay=${1:?usage: delay.sh DELAY (for example 300ms)}
lab_x r2 tc qdisc replace dev r2-r3 root netem delay "$delay"
echo "added $delay on r2 -> r3"
