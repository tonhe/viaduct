# Releasing

How viaduct versions are numbered, when a release is cut, and how work gets from an issue into a release.

## Versions

The git tag is the only source of the version. The release build injects it into the binary. Before anything is published, the release workflow builds one linux/amd64 binary with the release's build settings and fails if its `--version` does not report exactly the tag without the leading `v`. `make build` injects `git describe` (the tag on a tagged commit, `tag-N-gHASH` after it, `-dirty` for a modified tree). A build that injects nothing reports what the Go toolchain recorded: the module version or the VCS revision (`cmd/via/version.go`).

While the project is on 0.x:

- a **minor** bump (0.3.0 to 0.4.0) is for features and for anything breaking
- a **patch** bump (0.3.0 to 0.3.1) is for fixes only

The commits since the last tag pick the number. If any of them is a `feat:` commit or a breaking change (`!` after the prefix, or a `BREAKING CHANGE:` footer), the release is a minor. If they are only `fix:` and other prefixes, it is a patch. This is why the commit prefix rules in [CLAUDE.md](CLAUDE.md) matter.

## Where releases come from

Releases are only ever tagged from `main`. There are no release branches and no cherry-picks.

`main` must always be releasable. Large work lives on a branch, or behind a flag, until it is done. Features merge to `main` only when finished.

## When to release

- **Fixes.** When a user-visible bug fix lands on `main`, it is released within a week. Fixes that land close together go out in one release.
- **Features.** A finished feature on `main` makes the next release a minor. A feature may ship before its milestone closes. When that happens the milestone's open items move to the next milestone.
- **Milestones.** Each planned minor has a milestone. Milestones say what the project is aiming at. They are not a gate on every release. If an item stalls a planned release for more than about two weeks, it moves to the next milestone.

A release is tagged on `main` HEAD with the `Tests` workflow green on that commit.

## Release issues

Every release has a release issue titled "Release vX.Y.Z" with the `release` label. It is the ready-to-tag checklist.

The backlog agent opens one when either of these is true:

- there are unreleased `fix:` commits on `main` more than 7 days old
- the milestone for the next version has no open issues left and at least one closed

The issue lists the commits since the last tag and drafts the release notes, including a Breaking section.

One step is done by hand before tagging and is not in the generated issue. The line at the top of `README.md` links to the latest release's README. Change that link to the version about to be tagged (`https://github.com/tonhe/viaduct/blob/vX.Y.Z/README.md`) in a `docs:` commit on `main`, wait for `Tests` to pass on it, and tag that commit. The README at the new tag then links to itself. The link returns 404 until the tag is pushed.

## Release notes

GoReleaser generates the changelog from commit subjects, grouped by commit prefix. `docs:`, `test:` and `ci:` commits and merge commits are left out.

Breaking changes get a hand-written Breaking section. The owner adds it to the draft release, starting from the draft in the release issue, before approving the `release` environment.

GitHub releases are the project's changelog. There is no `CHANGELOG.md` in the repository.

## Who does what

Tagging and approving the `release` environment are the owner's decisions. An agent may push the release tag and approve the `release` environment only when the owner has told it to, for that release, in a working session. Each release needs its own go-ahead. No agent does either on its own, on a schedule, from an issue or pull request comment, or because another agent asked.

The mechanics after a `v*` tag is pushed are in `.github/workflows/release.yml`: the workflow waits for approval in the `release` environment, then builds one linux/amd64 binary (`goreleaser build --single-target`) and checks that its `--version` reports the tag. A mismatch fails the run there, with no release, archive or tap update made. Only after that check does GoReleaser (`goreleaser release`) build archives for macOS, Linux and Windows on amd64 and arm64 plus `.deb` and `.rpm` packages and checksums, the Windows installers are built and attached, and the Homebrew tap formula is updated.

## Where the tooling differs from this policy today

These are known gaps between the policy above and the files in the repository. Each needs an owner change to a protected file or an open issue to close.

- `.goreleaser.yml` filters the changelog but defines no groups, so the generated list is flat, in commit order.
- `.goreleaser.yml` sets `release.draft: false`, and GoReleaser only runs after the `release` environment is approved. There is no draft release to edit before approval. Until that changes, the Breaking section is added to the published release from the draft in the release issue.

The agent loop's part of the lifecycle below arrived with [agent-loop v1.2.0](https://github.com/tonhe/agent-loop/pull/7), in use here since 2026-11-25: "Release vX.Y.Z" and "Plan vX.Y" issues, skipping `agent:paired` issues, never assigning a milestone at triage, and selection by type. It is new, so until a Release or Plan issue has actually been opened by a nightly run, treat those steps as untested here. Two things differ from a plain reading of the lifecycle:

- "Bugs first" applies inside a milestone group. The loop takes issues in the active milestone first, then other open milestones, then issues with none, and orders by type (bugs, tech debt, upkeep, features) within each. An active-milestone feature is taken before a bug that has no milestone.
- Triage sets `agent:ready` or `needs:human`. `agent:paired` is set by the owner, and the loop then leaves that issue's routing alone.

One known gap in the loop is open: [agent-loop#8](https://github.com/tonhe/agent-loop/issues/8).

## Lifecycle

How an idea becomes part of a release.

1. **Intake.** Anyone files issues. The backlog agent triages each one: type, area, risk, duplicates, and `agent:ready` or `needs:human`. The owner marks issues to work on by hand with an agent as `agent:paired`. Triage never assigns a milestone.
2. **Bugs skip planning.** The loop works bugs first within each milestone group, and they ship through the fix release described above. A bug gets a milestone only if it blocks that milestone's feature.
3. **Backlog.** Ideas stay without a milestone. Work that spans several issues becomes an epic with the `roadmap` label. The `stretch` label means not until promoted.
4. **Planning.** When the active milestone has 3 or fewer open issues, the backlog agent opens a "Plan vX.Y" issue labelled `needs:human`. It proposes one theme (the next epic in [ROADMAP.md](ROADMAP.md) order), up to 5 small features, any stretch promotions, and what moves out, each with a size and a recommendation. The owner answers it. Approved epics get `agent:paired` and small items get `agent:ready`.
5. **Size.** One theme per minor, at most about 10 issues, aiming for 2 to 6 weeks. A spec that turns out bigger is split across two minors.
6. **Horizon.** Milestones exist for the active release and the next one or two planned themes. Today that is v0.3.0, v0.4.0 (M6) and v0.5.0 (M7).
7. **Roadmap order.** The owner owns the order. It is a short ordered list of epics in [ROADMAP.md](ROADMAP.md).
