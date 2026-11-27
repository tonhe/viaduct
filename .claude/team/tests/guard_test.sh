#!/usr/bin/env bash
# Tests for hooks/guard.sh. Run: .claude/team/tests/guard_test.sh
# Exit 0 when every case passes. Needs bash and python3, nothing else.
set -u
TEAM_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
GUARD="$TEAM_DIR/hooks/guard.sh"
pass=0
fail=0

# json_event TOOL KEY VALUE: one hook input with a single tool_input field.
json_event() {
  python3 -c 'import json,sys; print(json.dumps({"tool_name": sys.argv[1], "tool_input": {sys.argv[2]: sys.argv[3]}}))' "$1" "$2" "$3"
}

# run ROLE TOOL KEY VALUE: print the guard's exit status.
run() {
  local role="$1" event
  shift
  event=$(json_event "$@")
  if [ "$role" = "-" ]; then
    # With no role the guard exits without reading its input.
    env -u VIADUCT_ROLE VIADUCT_TEAM_DIR="$TEAM_DIR" "$GUARD" >/dev/null 2>&1 <<<"$event"
  else
    VIADUCT_ROLE="$role" VIADUCT_TEAM_DIR="$TEAM_DIR" "$GUARD" >/dev/null 2>&1 <<<"$event"
  fi
  echo $?
}

# expect WANT ROLE COMMAND: WANT is allow or block.
expect() {
  local want="$1" role="$2" cmd="$3" got
  got=$(run "$role" Bash command "$cmd")
  check "$want" "$got" "$role: $cmd"
}

# expect_file WANT ROLE PATH
expect_file() {
  local want="$1" role="$2" path="$3" got
  got=$(run "$role" Write file_path "$path")
  check "$want" "$got" "$role writes $path"
}

check() {
  local want="$1" got="$2" what="$3" code=0
  [ "$want" = block ] && code=2
  if [ "$got" = "$code" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    echo "FAIL ($want, exit $got): $what"
  fi
}

# No role: nothing is checked. This is what a CI agent or any other session sees.
expect allow - "gh pr merge 12 --squash"
expect allow - "git push origin issue-12"
expect_file allow - "$TEAM_DIR/hooks/guard.py"

# Unknown role: blocked, so a typo in roles.toml cannot run unguarded.
expect block janitor "ls"

# dev
expect allow dev "git commit -m 'fix(tui): align rows'"
expect allow dev "git push -u origin fix/row-align"
expect allow dev "gh pr create --title x --body y"
expect allow dev "gh issue edit 12 --add-label agent:in-progress"
expect allow dev "gh issue create --title x --body y --label type:bug --label risk:low --label agent:ready"
expect allow dev "gh pr view 12 --json state"
expect block dev "gh pr merge 12 --squash --delete-branch"
expect block dev "gh -R tonhe/viaduct pr merge 12 --auto"
expect block dev "cd /tmp && gh pr merge 12"
expect block dev "bash -c 'gh pr merge 12'"
expect block dev "echo \$(gh pr merge 12)"
expect block dev "gh api -X PUT repos/tonhe/viaduct/pulls/12/merge"
expect block dev "gh issue edit 12 --add-label agent:ready"
expect block dev "gh issue edit 12 --remove-label needs:human,bug"
expect block dev "gh issue edit 12 --add-label risk:low"
expect block dev "gh issue edit 12 --milestone v0.4.0"
expect block dev "gh api -X PATCH repos/tonhe/viaduct/issues/12 -F milestone=5"
expect block dev "gh api -X POST repos/tonhe/viaduct/issues/12/labels -f labels[]=needs:human"
expect block dev "gh api -X DELETE repos/tonhe/viaduct/issues/12/labels/agent:ready"
expect block dev "git push origin HEAD:main"
expect block dev "git push origin main"
expect block dev "git checkout -b issue-12"
expect block dev "git switch -c issue-7"
expect block dev "git push origin issue-12"
expect block dev "git -C /tmp/x push origin fix/a:issue-12"
expect allow dev "git checkout -b fix/issue-12-rows"

# tester
expect allow tester "sudo via -6 -P tcp -r -c 20 example.com"
expect allow tester "git fetch origin"
expect allow tester "git checkout 8cd49c9"
expect allow tester "git log -1 --format=%h"
expect allow tester "gh pr view 12"
expect allow tester "gh pr checks 12"
expect allow tester "gh api repos/tonhe/viaduct/pulls/12"
expect allow tester "gh pr comment 12 --body 'Live check: pass'"
expect block tester "git push origin fix/a"
expect block tester "git commit -am wip"
expect block tester "git -C /tmp/x commit -m wip"
expect block tester "gh issue edit 12 --add-label bug"
expect block tester "gh issue comment 12 --body hi"
expect block tester "gh issue create --title x --body y"
expect block tester "gh pr merge 12"
expect block tester "gh api -X POST repos/tonhe/viaduct/issues/12/comments -f body=hi"
expect block tester "gh api repos/tonhe/viaduct/issues/12/comments -f body=hi"
expect block tester "gh pr review 12 --approve"

# director
expect allow director "gh pr merge 12 --squash --delete-branch"
expect allow director "gh issue edit 12 --add-label agent:ready --milestone v0.4.0"
expect allow director "gh issue comment 12 --body 'Triage: needs a lab scenario'"
expect allow director "git fetch origin"
expect allow director "git log --oneline -5"
expect block director "git commit -m x"
expect block director "git push origin fix/a"
expect_file block director "$TEAM_DIR/../../internal/hop/hop.go"
expect_file block director "$TEAM_DIR/../../README.md"
expect_file allow director "$TEAM_DIR/HANDOFF-director.md"
expect_file allow director "/tmp/owner-decision-draft.md"

# every role: "Owner decision" comments
for role in director dev tester; do
  expect block "$role" "gh pr comment 12 --body 'Owner decision: scope is internal/hop'"
  expect block "$role" "gh issue comment 12 -b '  owner decision - yes'"
  expect block "$role" "gh api -X POST repos/tonhe/viaduct/issues/12/comments -f body='Owner decision: go'"
done
expect allow director "gh issue comment 12 --body 'This needs an Owner decision before work starts'"
draft=$(mktemp)
echo "Owner decision: scope is the alert rules" >"$draft"
expect block director "gh issue comment 12 --body-file $draft"
expect block director "gh api -X POST repos/tonhe/viaduct/issues/12/comments -F body=@$draft"
rm -f "$draft"

# every role: releases
for role in director dev tester; do
  expect block "$role" "git tag -a v0.3.1 -m v0.3.1"
  expect block "$role" "git push origin v0.3.1"
  expect block "$role" "git push --tags"
  expect block "$role" "gh release create v0.3.1"
  expect block "$role" "gh api -X POST repos/tonhe/viaduct/actions/runs/1/pending_deployments -f state=approved"
  expect allow "$role" "git tag --list 'v0.3*'"
  expect allow "$role" "gh release view v0.3.0"
done

# every role: the running team's own files
for role in director dev tester; do
  expect_file block "$role" "$TEAM_DIR/hooks/guard.py"
  expect_file block "$role" "$TEAM_DIR/DELEGATIONS.md"
  expect_file block "$role" "$TEAM_DIR/roles.toml"
done
expect_file allow dev "/tmp/some-worktree/.claude/team/DELEGATIONS.md"
expect_file allow dev "$TEAM_DIR/HANDOFF-dev.md"
expect_file allow tester "/tmp/smoke.sh"

# refspecs: a leading + and any position
expect block dev "git push origin +HEAD:main"
expect block dev "git push --repo=origin HEAD:main"
expect block dev "git push origin fix/a +fix/b:issue-9"
expect block tester "git push origin +v0.3.1"

# GraphQL mutations and aliases are other routes to a merge
expect block dev "gh api graphql -f query='mutation { mergePullRequest(input: {pullRequestId: \"x\"}) { clientMutationId } }'"
expect block dev "gh api graphql -f query='query { viewer { login } }'"
for role in director dev tester; do
  expect block "$role" "gh alias set land 'pr merge'"
done

# GraphQL the guard cannot read is refused for dev
expect block dev "gh api /graphql -f query='mutation { x }'"
expect block dev "gh api graphql -F query=@/tmp/q.graphql"
expect block dev "gh api graphql --input /tmp/q.json"
expect block dev "gh api https://api.github.com/graphql -f query='  mutation { x }'"
expect block dev "gh api graphql -f query='{ viewer { login } }'"
expect block dev "git -c PUSH.Default=matching push origin"
expect block dev "git --config-env=push.default=X push origin"

# pushes that name no single branch
expect block dev "git push --all origin"
expect block dev "git push --mirror"
expect block dev "git push origin 'refs/heads/*:refs/heads/*'"
expect block dev "git -c push.default=matching push origin"
expect block dev "git -c remote.origin.push=HEAD:main push"

# no command-line git configuration and no aliases, for any role
for role in director dev tester; do
  expect block "$role" "git -c alias.p=push p origin main"
  expect block "$role" "git -c remote.origin.mirror=true push origin"
  expect block "$role" "git -C p -c alias.p=push p origin main"
  expect block "$role" "git -calias.p=push p origin main"
  expect block "$role" "git --attr-source push commit -m x"
  expect block "$role" "git --exec-path=/tmp/x status"
  expect allow "$role" "git --no-pager -C /tmp/x log -1"
  expect allow "$role" "git --git-dir=/tmp/x/.git status"
  expect block "$role" "git config alias.p push"
  expect block "$role" "git config --global ALIAS.p push"
  expect allow "$role" "git config user.name"
done

# settings paths in another letter case (macOS file systems ignore case)
expect_file block dev "$HOME/.Claude/settings.json"
expect_file block dev "/tmp/wt/.CLAUDE/Settings.local.json"

# Claude settings files: a role must not switch its own guard off
for role in director dev tester; do
  expect_file block "$role" "$TEAM_DIR/../settings.local.json"
  expect_file block "$role" "$TEAM_DIR/../settings.json"
  expect_file block "$role" "$HOME/.claude/settings.json"
  expect_file block "$role" "/tmp/some-worktree/.claude/settings.local.json"
done
# A checkout under a temp directory is still a repository.
tmprepo=$(mktemp -d)
mkdir -p "$tmprepo/.git" "$tmprepo/internal"
expect_file block director "$tmprepo/internal/hop.go"
expect_file allow dev "$tmprepo/internal/hop.go"
rm -rf "$tmprepo"
expect_file block director "$HOME/.claude/keybindings.json"
expect_file allow director "$HOME/.claude/projects/x/memory/note.md"

echo "guard tests: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
