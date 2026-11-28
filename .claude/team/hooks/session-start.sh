#!/usr/bin/env bash
# SessionStart hook for the viaduct agent team. See ../README.md.
#
# A role learns who it is from the prompt its launcher passes at start. After
# /clear, an automatic compaction or a resume that prompt is gone, so this
# hook says it again: what it prints is added to the session's context. The
# launcher loads it for the sources clear, compact and resume.
#
# A session with no VIADUCT_ROLE is not a team session: it prints nothing.
#
# Written for bash 3.2, which is what macOS ships.
[ -n "${VIADUCT_ROLE:-}" ] || exit 0

dir="${VIADUCT_TEAM_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
role="$VIADUCT_ROLE"
printf 'You are the viaduct %s. Context was reset. Read %s, %s and %s now, then %s if it exists, and carry on from the handoff.\n' \
  "$role" "$dir/prompts/$role.md" "$dir/PROTOCOL.md" "$dir/DELEGATIONS.md" "$dir/HANDOFF-$role.md"
