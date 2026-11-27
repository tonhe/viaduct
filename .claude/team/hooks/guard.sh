#!/usr/bin/env bash
# PreToolUse hook for the viaduct agent team. See ../PROTOCOL.md.
#
# Loaded only by sessions that .claude/team/start launches (through
# `claude --settings`). A session with no VIADUCT_ROLE, the nightly CI agents
# included, is never checked: the first line below lets it through.
[ -n "${VIADUCT_ROLE:-}" ] || exit 0

if ! command -v python3 >/dev/null 2>&1; then
  echo "viaduct team guard: python3 is missing, so the role limits cannot be checked. Blocking." >&2
  exit 2
fi
exec python3 "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/guard.py"
