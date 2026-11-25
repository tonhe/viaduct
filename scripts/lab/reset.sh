#!/usr/bin/env bash
# Remove every impairment (drop rules and their counters, netem, rate limits,
# multipath routes) and return the lab to the clean baseline. Run between
# scenarios and between tools, so no counter is shared by two runs.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=scripts/lab/lib.sh
source "$here/lib.sh"
lab_require_root
lab_clear_impairments
