#!/usr/bin/env bash
# Tear down the network lab. Safe to run when the lab is not up, or half up
# after a crash.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=scripts/lab/lib.sh
source "$here/lib.sh"
lab_require_root

for n in $LAB_NODES; do
  # Deleting a namespace destroys its veth ends, which destroys the peers too.
  ip netns del "$(lab_ns "$n")" 2>/dev/null || true
done
