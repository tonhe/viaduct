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

# every role: "Owner decision" by every way a body can reach GitHub.
# The comment on issue 234 was posted, and later edited, with the body in a
# file that the same command wrote and with the path in a shell variable. The
# guard read nothing and allowed it. A body the guard cannot read is blocked.
od=$(mktemp -d)
printf 'Owner decision: scope is the alert rules\n' >"$od/decision.md"
printf '\n\n   \n**Owner decision:** scope is the alert rules\n' >"$od/marked.md"
{ printf '%0600d' 0 | tr 0 '\n'; printf 'Owner decision: far down\n'; } >"$od/padded.md"
printf 'This needs an Owner decision before work starts\n' >"$od/plain.md"
printf '{"body":"Owner decision: go"}' >"$od/decision.json"
printf '{"event":"COMMENT","body":"fine","comments":[{"path":"a.go","line":1,"body":"Owner decision: go"}]}' >"$od/review.json"
printf '{"body":"Checked on macOS, all green"}' >"$od/plain.json"
printf 'body=Owner decision: go' >"$od/notjson.txt"
C=repos/tonhe/viaduct

# expect_cwd WANT ROLE DIR COMMAND: the hook input carries the session's cwd.
expect_cwd() {
  local want="$1" role="$2" dir="$3" cmd="$4" event got
  event=$(python3 -c 'import json,sys; print(json.dumps({"tool_name": "Bash", "cwd": sys.argv[1], "tool_input": {"command": sys.argv[2]}}))' "$dir" "$cmd")
  VIADUCT_ROLE="$role" VIADUCT_TEAM_DIR="$TEAM_DIR" "$GUARD" >/dev/null 2>&1 <<<"$event"
  got=$?
  check "$want" "$got" "$role in $dir: $cmd"
}

for role in director dev tester; do
  # inline, every spelling
  expect block "$role" "gh pr comment 12 --body='Owner decision: go'"
  expect block "$role" "gh pr comment 12 -b'Owner decision: go'"
  expect block "$role" "gh pr comment 12 -b 'Owner decision: go' -R tonhe/viaduct"
  # text in front that a reader does not see as text
  expect block "$role" "gh pr comment 12 --body '**Owner decision:** go'"
  expect block "$role" "gh pr comment 12 --body '## Owner decision'"
  expect block "$role" "gh pr comment 12 --body '> Owner decision: go'"
  expect block "$role" "gh pr comment 12 --body '<!-- note -->Owner decision: go'"
  expect block "$role" "gh pr comment 12 --body '_Owner  decision_ go'"
  # a file that exists, every spelling
  expect block "$role" "gh pr comment 12 --body-file $od/decision.md"
  expect block "$role" "gh pr comment 12 --body-file=$od/decision.md"
  expect block "$role" "gh pr comment 12 -F $od/decision.md"
  expect block "$role" "gh pr comment 12 -F$od/decision.md"
  expect block "$role" "gh pr comment 12 --body-file $od/marked.md"
  expect block "$role" "gh pr comment 12 --body-file $od/padded.md"
  expect block "$role" "gh pr comment 12 --edit-last --body-file $od/decision.md"
  # standard input: the guard cannot read it
  expect block "$role" "gh pr comment 12 --body-file - < $od/decision.md"
  expect block "$role" "cat $od/decision.md | gh pr comment 12 --body-file -"
  expect block "$role" "gh pr comment 12 -F - <<'EOF'
Owner decision: go
EOF"
  expect block "$role" "gh pr comment 12 --body-file - <<'EOF'
Checked on macOS, all green
EOF"
  # a file the guard cannot read, or cannot trust
  expect block "$role" "gh pr comment 12 --body-file $od/missing.md"
  expect block "$role" "echo 'Owner decision: go' > $od/new.md && gh pr comment 12 --body-file $od/new.md"
  expect block "$role" "cp $od/decision.md $od/plain.md && gh pr comment 12 --body-file $od/plain.md"
  expect block "$role" "cd $od && gh pr comment 12 --body-file decision.md"
  expect block "$role" "cd $od && gh pr comment 12 --body-file plain.md"
  expect_cwd block "$role" "$od" "gh pr comment 12 --body-file decision.md"
  expect block "$role" 'gh pr comment 12 --body-file $S/decision.md'
  expect block "$role" 'gh pr comment 12 --body-file "$(mktemp)"'
  # a body built when the command runs
  expect block "$role" 'gh pr comment 12 --body "$BODY"'
  expect block "$role" 'gh pr comment 12 --body "${BODY}"'
  expect block "$role" 'gh pr comment 12 --body $BODY'
  expect block "$role" 'B="Owner decision: go"; gh pr comment 12 --body "$B"'
  expect block "$role" "gh pr comment 12 --body \"\$(cat $od/decision.md)\""
  expect block "$role" "gh pr comment 12 --body \"\`cat $od/decision.md\`\""
  expect block "$role" "gh pr comment 12 --body \$(cat $od/decision.md)"
  expect block "$role" "sh -c 'gh pr comment 12 --body \"\$B\"'"
  # other commands that carry a comment
  expect block "$role" "gh pr comment 12 --edit-last --body 'Owner decision: go'"
done

# The remaining forms are the director's: the dev and tester roles are held
# to fewer gh commands, so a block for them would not test this rule.
# gh pr and gh issue commands that carry a body or a comment
expect block director "gh issue comment 12 --edit-last --body 'Owner decision: go'"
expect block director "gh issue comment 12 --edit-last -F $od/decision.md"
expect block director "gh issue edit 12 --body 'Owner decision: go'"
expect block director "gh issue edit 12 --body-file $od/decision.md"
expect block director "gh pr edit 12 --body 'Owner decision: go'"
expect block director "gh issue create --title t --body-file $od/decision.md"
expect block director "gh pr review 12 --comment --body 'Owner decision: go'"
expect block director "gh pr review 12 --approve -b 'Owner decision: go'"
expect block director "gh pr review 12 -c -b 'Owner decision: go'"
expect block director "gh pr review 12 -cb 'Owner decision: go'"
expect block director "gh pr review 12 --comment --body-file $od/decision.md"
expect block director "gh pr review 12 --request-changes --body-file $od/decision.md"
expect block director "gh pr review 12 -a -F -"
expect block director "gh pr close 12 --comment 'Owner decision: go'"
expect block director "gh pr close 12 -c 'Owner decision: go'"
expect block director "gh pr close 12 -dc 'Owner decision: go'"
expect block director "gh pr close 12 --comment=\"\$NOTE\""
expect block director "gh issue close 12 --comment 'Owner decision: go'"
expect block director "gh issue close 12 -c 'Owner decision: go' --reason completed"
expect block director "gh issue close 12 -c \"\$(cat $od/decision.md)\""
expect block director "gh pr merge 12 -R tonhe/viaduct --squash --delete-branch --body 'Owner decision: go'"
expect block director "gh pr merge 12 --squash --body-file $od/decision.md"
expect block director "gh pr merge 12 -sdb 'Owner decision: go'"
expect block director "gh pr merge 12 --squash -F -"
expect block director "gh pr merge 12 --squash --body \"\$MSG\""

# gh api: each endpoint that creates or edits a comment, a review or a review comment
for call in \
  "-X POST $C/issues/12/comments" \
  "$C/issues/12/comments" \
  "-X PATCH $C/issues/comments/7230155611" \
  "--method PATCH $C/issues/comments/7230155611" \
  "-X POST $C/pulls/12/reviews" \
  "-X PUT $C/pulls/12/reviews/99" \
  "-X PUT $C/pulls/12/reviews/99/dismissals" \
  "-X POST $C/pulls/12/comments" \
  "-X POST $C/pulls/12/comments/5/replies" \
  "-X PATCH $C/pulls/comments/5" \
  "-X PATCH $C/issues/12" \
  "-X PATCH $C/pulls/12"; do
  expect block director "gh api $call -f body='Owner decision: go'"
  expect block director "gh api $call -fbody='Owner decision: go'"
  expect block director "gh api $call --raw-field body='Owner decision: go'"
  expect block director "gh api $call --raw-field=body='Owner decision: go'"
  expect block director "gh api $call -F body='Owner decision: go'"
  expect block director "gh api $call -Fbody='Owner decision: go'"
  expect block director "gh api $call --field body='Owner decision: go'"
  expect block director "gh api $call --field=body='Owner decision: go'"
  expect block director "gh api $call -f body='**Owner decision**: go'"
  expect block director "gh api $call -F body=@$od/decision.md"
  expect block director "gh api $call -Fbody=@$od/decision.md"
  expect block director "gh api $call --field=body=@$od/decision.md"
  expect block director "gh api $call -F body=@$od/missing.md"
  expect block director "gh api $call -F body=@-"
  expect block director "gh api $call -F body=@- < $od/decision.md"
  expect block director "gh api $call -F body=@\$S/decision.md"
  expect block director "gh api $call -f body=\"\$BODY\""
  expect block director "gh api $call -f body=\"\$(cat $od/decision.md)\""
  expect block director "gh api $call -f \"\$FIELD\""
  expect block director "gh api $call --input $od/decision.json"
  expect block director "gh api $call --input=$od/decision.json"
  expect block director "gh api $call --input $od/review.json"
  expect block director "gh api $call --input $od/notjson.txt"
  expect block director "gh api $call --input $od/missing.json"
  expect block director "gh api $call --input -"
  expect block director "gh api $call --input - < $od/decision.json"
  expect block director "jq -n '{body: \"x\"}' | gh api $call --input -"
  expect block director "gh api $call --input \"\$F\""
done
expect block director "gh api -X POST $C/pulls/12/reviews -f event=COMMENT -f 'comments[][path]=a.go' -f 'comments[][body]=Owner decision: go'"
expect block director "gh api -X POST $C/pulls/12/reviews -f event=COMMENT -f 'comments[][body]='\"\$B\""

# The two commands that put the comment on issue 234, in the form they had.
expect block director "S=$od && cat > \$S/od-234.md <<'EOF'
Owner decision: approved, for the change in #242 and nothing wider.

**Question 1, which row is the destination.**
EOF
gh api -X POST $C/issues/234/comments -F body=@\$S/od-234.md --jq .html_url"
expect block director "S=$od && { printf '%s\n\n' 'Owner decision: approved, for the change in #242 and nothing wider.'; tail -n +3 \$S/od-234.md; } > \$S/od-234-v2.md && head -5 \$S/od-234-v2.md && gh api -X PATCH $C/issues/comments/7230155611 -F body=@\$S/od-234-v2.md --jq '.html_url, .body[0:260]'"
# The same with a literal path: the file is written by the command that posts it.
expect block director "cat > $od/od-234.md <<'EOF'
Checked, all green
EOF
gh api -X POST $C/issues/234/comments -F body=@$od/od-234.md"

# GraphQL (the director's only: the dev role is blocked from the endpoint)
expect block director "gh api graphql -f query='mutation { addComment(input: {subjectId: \"I_1\", body: \"Owner decision: go\"}) { clientMutationId } }'"
expect block director "gh api graphql -f query='mutation(\$b: String!) { addComment(input: {subjectId: \"I_1\", body: \$b}) { clientMutationId } }' -f b='Owner decision: go'"
expect block director "gh api graphql -f query='mutation(\$b: String!) { addComment(input: {subjectId: \"I_1\", body: \$b}) { clientMutationId } }' -f b=\"\$B\""
expect block director "gh api graphql -F query=@$od/missing.graphql"

# What still works: a body the guard can read and that is not an Owner decision.
for role in director dev tester; do
  expect allow "$role" "gh pr comment 12 --body 'Checked on macOS, all green'"
  expect allow "$role" "gh pr comment 12 --body 'costs \$5, see \`make test-all\`'"
  expect allow "$role" "gh pr comment 12 --body-file $od/plain.md"
  expect allow "$role" "gh pr comment 12 -F $od/plain.md"
  expect_cwd allow "$role" "$od" "gh pr comment 12 --body-file plain.md"
done
expect allow director "gh issue comment 12 --body 'The owner decision is still needed'"
expect allow director "gh issue close 12 --reason duplicate --comment 'Duplicate of #11'"
expect allow director "gh pr review 12 --approve --body 'Read the diff and the checks'"
expect allow director "gh api -X POST $C/issues/12/comments -F body=@$od/plain.md"
expect allow director "gh api -X PATCH $C/issues/comments/5 --input $od/plain.json"
expect allow director "gh api -X POST $C/issues/12/comments -f body='Checked, all green'"
expect allow director "gh api -X POST $C/issues/12/dependencies/blocked_by -F issue_id=\$(gh api $C/issues/11 --jq .id)"
expect allow director "gh api -X POST $C/issues/12/dependencies/blocked_by -F issue_id=\"\$ID\""
expect allow director "gh api $C/issues/234/comments --jq '.[] | select(.body | test(\"Owner decision\"))'"
expect allow dev "gh pr create -R tonhe/viaduct --base main --head fix/a --title 'fix: a' --body-file $od/plain.md"
expect allow dev "gh api -X PATCH $C/pulls/12 -F body=@$od/plain.md"
rm -rf "$od"

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
