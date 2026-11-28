# Standing delegations

What the owner has approved in advance for the local agent team. Each entry is an approval the owner would otherwise give by hand. It changes only by a pull request the owner merges, so merging this file is the owner's approval of every entry in it.

If an action is not listed here and `CLAUDE.md` does not already allow it, it is not delegated. Ask the owner in your own session.

## Director

D1. **Merge a dev-role pull request** when all of these hold:
- every required check has passed on the head commit;
- it is not a nightly-loop pull request (no `agent-loop issue=` marker, branch not named `issue-N`), or it is one that rule 8 of the owner's rules covers;
- it does not change what `via` measures or reports (loss definition, verdicts, destination placement, export field meaning, alert rules), unless the owner's "Owner decision" comment for that scope is on the issue;
- if it touches code that opens, binds, sends on or reads from a probe socket, a passing live check on macOS for the head commit is recorded on the pull request by the tester;
- it does not touch `Makefile`, `scripts/`, `.goreleaser.yml`, `.github/`, `CLAUDE.md`, `RELEASING.md` or `.claude/team/`.

D2. **Merge a docs-only pull request** (Markdown outside the paths in D1's last line) once its required checks have passed.

D3. **Labels and milestones.** Set and change labels and milestones on issues and pull requests the nightly loop has not claimed, following "Filing issues" in `CLAUDE.md`.

D4. **Close an issue** when the pull request that fixes it has merged and its acceptance criteria are met. Not an issue that needs an Owner decision the owner has not posted.

D5. **File issues** for findings, following "Filing issues" in `CLAUDE.md`.

D6. **Delete a merged pull request's branch** after the merge. Not a branch whose pull request is open or was closed without merging.

D7. **Close an issue as a duplicate**, with a comment that links the issue it duplicates.

## Owner's rules for the director

The owner set these on 2026-11-28. Where a rule here is tighter than a delegation above, the rule wins.

1. **The director decides:** merge order, test-only and docs pull requests, labels, duplicates, branch cleanup, splitting issues, display fixes that change no figure or field, and anything this file covers.

2. **The owner decides, with the director's recommendation:** anything that changes what `via` measures, reports, exports or how it treats input (even if it is labelled a fix), Owner decisions, live-check waivers, nightly-loop pull requests other than the ones rule 8 covers, and any change to `CLAUDE.md`, `RELEASING.md`, `PROTOCOL.md`, `DELEGATIONS.md` or the director's own prompt.

3. **Owner decision comments.** The director may post an Owner decision comment only after the owner says "approved" in chat, and the comment quotes the owner's words.

4. **Changes to its own directions.** The director tells the owner every change it makes to its own prompt, its handoff file or any rules file, and never writes a wider reading of its own authority into any of them.

5. **Merges outside these rules.** A merge the director made outside these rules is listed to the owner with what changed for users and a recommendation. The owner approves or reverts it.

6. **Merge limit.** At most 20 merges per session. Then the director stops and sends the owner the summary.

7. **Where these rules live.** These rules live in this file and change only by a pull request the owner merges. They are not kept in the director's handoff file.

8. **More the director does.** The owner added these on 2026-11-28:
   - **Nightly-loop pull requests labelled `risk:low`.** The director merges one when the pull request carries the `risk:low` label and every required check has passed on the head commit. D1's other conditions still apply to it. This is a merge only: the director does not push to it, edit it, retarget it or comment a fix round on it.
   - **Linux live checks.** The director runs Linux live checks in a container itself and records the result on the pull request as a container run. This adds to the tester's live check on macOS and does not replace it.
   - **Release prep up to the tag.** The director does the steps `RELEASING.md` puts before the tag: the release issue's checklist, the check that `Tests` is green on `main` HEAD, and the draft release notes. It does not push the tag, approve the `release` environment or change a published release.
   - **Closing fixed issues.** The director closes an issue that a merged pull request fixed, as D4 says.

## Dev

V1. **Claim an issue** with `agent:in-progress`, push branches, and open pull requests, as `PROTOCOL.md` describes.

V2. **File issues** for findings, with the labels the filing rules require.

## Tester

T1. **Run `via`, `mtr` and `tcpdump`** under the passwordless test sudo rules on the Mac, for a commit named on a pull request or by the director.

T2. **Post live-check results** as pull request comments.

## Never delegated

These always need the owner, each time:

- pushing a release tag, approving the `release` environment, or changing a published release (`RELEASING.md`);
- writing a comment that begins "Owner decision", except the director under rule 3 of the owner's rules;
- merging a pull request that touches the paths in D1's last line;
- changing a nightly-loop pull request, or merging one that rule 8 of the owner's rules does not cover;
- force-pushing, or deleting a branch whose pull request has not merged;
- anything on a repository other than `tonhe/viaduct`.
