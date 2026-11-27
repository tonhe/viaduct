# Standing delegations

What the owner has approved in advance for the local agent team. Each entry is an approval the owner would otherwise give by hand. It changes only by a pull request the owner merges, so merging this file is the owner's approval of every entry in it.

If an action is not listed here and `CLAUDE.md` does not already allow it, it is not delegated. Ask the owner in your own session.

## Director

D1. **Merge a dev-role pull request** when all of these hold:
- every required check has passed on the head commit;
- it is not a nightly-loop pull request (no `agent-loop issue=` marker, branch not named `issue-N`);
- it does not change what `via` measures or reports (loss definition, verdicts, destination placement, export field meaning, alert rules), unless the owner's "Owner decision" comment for that scope is on the issue;
- if it touches code that opens, binds, sends on or reads from a probe socket, a passing live check on macOS for the head commit is recorded on the pull request by the tester;
- it does not touch `Makefile`, `scripts/`, `.goreleaser.yml`, `.github/`, `CLAUDE.md`, `RELEASING.md` or `.claude/team/`.

D2. **Merge a docs-only pull request** (Markdown outside the paths in D1's last line) once its required checks have passed.

D3. **Labels and milestones.** Set and change labels and milestones on issues and pull requests the nightly loop has not claimed, following "Filing issues" in `CLAUDE.md`.

D4. **Close an issue** when the pull request that fixes it has merged and its acceptance criteria are met. Not an issue that needs an Owner decision the owner has not posted.

D5. **File issues** for findings, following "Filing issues" in `CLAUDE.md`.

## Dev

V1. **Claim an issue** with `agent:in-progress`, push branches, and open pull requests, as `PROTOCOL.md` describes.

V2. **File issues** for findings, with the labels the filing rules require.

## Tester

T1. **Run `via`, `mtr` and `tcpdump`** under the passwordless test sudo rules on the Mac, for a commit named on a pull request or by the director.

T2. **Post live-check results** as pull request comments.

## Never delegated

These always need the owner, each time:

- pushing a release tag, approving the `release` environment, or changing a published release (`RELEASING.md`);
- writing a comment that begins "Owner decision";
- merging a pull request that touches the paths in D1's last line;
- merging or changing a nightly-loop pull request;
- force-pushing, or deleting a branch another role or the loop created;
- anything on a repository other than `tonhe/viaduct`.
