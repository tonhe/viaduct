#!/usr/bin/env bash
# Tests for the files start writes, and for hooks/session-start.sh.
# Run: .claude/team/tests/start_test.sh
# Exit 0 when every case passes. Needs bash and python3, nothing else.
#
# start runs on a copy of the team directory in a scratch repository, with a
# stand-in backend that starts nothing, so no session is touched.
set -u
TEAM_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
HOOK="$TEAM_DIR/hooks/session-start.sh"
pass=0
fail=0

check() { # WHAT GOT WANT
  if [ "$2" = "$3" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL: %s\n  got:  %s\n  want: %s\n' "$1" "$2" "$3"
  fi
}

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

# --- the settings start writes ---

repo="$scratch/repo"
mkdir -p "$repo/.git" "$repo/.claude"
cp -R "$TEAM_DIR" "$repo/.claude/team"
team="$repo/.claude/team"
rm -rf "$team/.run" "$team"/HANDOFF-*.md
cat >"$team/backend/stub.sh" <<'EOF'
be_check() { return 0; }
be_exists() { return 1; }
be_start() { return 0; }
be_stop() { return 0; }
be_list() { :; }
be_attach() { :; }
EOF
cat >"$team/roles.toml" <<'EOF'
[[role]]
name = "test-director"
role = "director"
host = "linux"
cwd = "."

[[role]]
name = "test-dev"
role = "dev"
host = "linux"
cwd = "."
EOF

VIADUCT_CLAUDE_BIN=true "$team/start" --backend stub --host linux >/dev/null 2>&1
check "start exits 0" "$?" "0"

for role in director dev; do
  settings="$team/.run/$role.settings.json"
  got=$(python3 - "$settings" "$team" <<'EOF'
import json, sys
s = json.load(open(sys.argv[1]))
team = sys.argv[2]
pre = s["hooks"]["PreToolUse"][0]
ss = s["hooks"]["SessionStart"]
print(pre["hooks"][0]["command"] == "'%s/hooks/guard.sh'" % team,
      len(ss), ss[0]["matcher"], ss[0]["hooks"][0]["type"],
      ss[0]["hooks"][0]["command"] == "'%s/hooks/session-start.sh'" % team)
EOF
)
  check "$role settings: guard and session-start hooks" "$got" "True 1 ^(clear|compact|resume)\$ command True"
done

# The matcher takes the sources that lose the launch prompt, and not startup,
# which has it.
got=$(python3 - "$team/.run/dev.settings.json" <<'EOF'
import json, re, sys
m = json.load(open(sys.argv[1]))["hooks"]["SessionStart"][0]["matcher"]
print(" ".join(src for src in ("startup", "resume", "clear", "compact", "clearing") if re.search(m, src)))
EOF
)
check "matcher sources" "$got" "resume clear compact"

# --- the hook ---

for role in director dev tester; do
  got=$(VIADUCT_ROLE="$role" VIADUCT_TEAM_DIR=/team "$HOOK")
  want="You are the viaduct $role. Context was reset. Read /team/prompts/$role.md, /team/PROTOCOL.md and /team/DELEGATIONS.md now, then /team/HANDOFF-$role.md if it exists, and carry on from the handoff."
  check "hook for $role" "$got" "$want"
done

# Without VIADUCT_TEAM_DIR the hook names the team directory it is in.
got=$(env -u VIADUCT_TEAM_DIR VIADUCT_ROLE=dev "$HOOK")
check "hook without VIADUCT_TEAM_DIR" "$got" "You are the viaduct dev. Context was reset. Read $TEAM_DIR/prompts/dev.md, $TEAM_DIR/PROTOCOL.md and $TEAM_DIR/DELEGATIONS.md now, then $TEAM_DIR/HANDOFF-dev.md if it exists, and carry on from the handoff."

# No role: not a team session, nothing printed.
got=$(env -u VIADUCT_ROLE VIADUCT_TEAM_DIR=/team "$HOOK"; echo "exit $?")
check "hook with no role" "$got" "exit 0"
got=$(VIADUCT_ROLE= VIADUCT_TEAM_DIR=/team "$HOOK"; echo "exit $?")
check "hook with an empty role" "$got" "exit 0"

echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
