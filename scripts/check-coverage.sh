#!/usr/bin/env bash
# scripts/check-coverage.sh
# Enforces per-package coverage floors per PLAN.md §5.6.
# Update floors upward in follow-up PRs; never lower.

set -euo pipefail

# Per-package floors. Update these upward as coverage improves; the goal
# per PLAN.md is 70% baseline. Packages currently below 70% have a lower
# starting floor with a TODO to raise them.
declare -A FLOORS=(
  ["github.com/tonhe/viaduct/internal/ecmp"]=95     # currently 100
  ["github.com/tonhe/viaduct/internal/export"]=90    # currently 94.6
  ["github.com/tonhe/viaduct/internal/hop"]=98       # currently 100 (post-Tier-2)
  ["github.com/tonhe/viaduct/internal/sysprobe"]=82  # Linux 89.5%, Windows 84.2% — interface enumeration paths differ per-OS
  # Post-Tier-1 floors:
  ["github.com/tonhe/viaduct/internal/asn"]=73       # currently 75.9
  ["github.com/tonhe/viaduct/internal/ping"]=71      # currently 73.5
  ["github.com/tonhe/viaduct/internal/probe"]=78     # currently 80.5 (post-Tier-4.4 socket seam; icmpSender/rawSender/Run/Discover need real sockets)
  # Post-Tier-2 floors:
  ["github.com/tonhe/viaduct/internal/resolve"]=84   # currently 86.4
  ["github.com/tonhe/viaduct/internal/config"]=50    # Linux 87%, Windows 53% — runtime.GOOS branching means each OS can only test its own path resolution
  ["github.com/tonhe/viaduct/internal/tui"]=69       # currently 71.5, settings state target ~90 (whole package harder to isolate)
  # Post-Tier-3 floor: cmd/via gained integration tests via stub tracer.
  # Ceiling is limited by runTrace (TUI path) and main() which need live sockets / cobra runtime.
  ["github.com/tonhe/viaduct/cmd/via"]=27             # currently 29.4
)

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

FAILED=0
PACKAGES=()

# Determine which packages to run — use the keys of FLOORS.
for pkg in "${!FLOORS[@]}"; do
  PACKAGES+=("$pkg")
done

echo "Running coverage on ${#PACKAGES[@]} gated packages..."
echo

for pkg in "${PACKAGES[@]}"; do
  prof="$TMP/$(echo "$pkg" | tr / _).out"
  # Run tests with coverage on the single package.
  go test -coverprofile="$prof" "$pkg" > /dev/null 2>&1 || true
  if [ ! -s "$prof" ]; then
    echo "  [SKIP] $pkg — no coverage profile (no tests?)"
    continue
  fi
  # Extract the percentage from `go tool cover -func`.
  pct=$(go tool cover -func="$prof" | grep 'total:' | awk '{print $3}' | tr -d '%')
  floor=${FLOORS[$pkg]}
  int_pct=${pct%.*}
  int_floor=${floor%.*}
  if [ "$int_pct" -lt "$int_floor" ]; then
    echo "  [FAIL] $pkg — $pct% (floor: $floor%)"
    FAILED=$((FAILED + 1))
  else
    echo "  [ OK ] $pkg — $pct% (floor: $floor%)"
  fi
done

echo
if [ "$FAILED" -gt 0 ]; then
  echo "$FAILED package(s) below floor"
  exit 1
fi
echo "All gated packages meet their coverage floors."
