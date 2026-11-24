# Releasing

How viaduct versions are numbered, when a release is cut, and how work gets from an issue into a release.

## Versions

The git tag is the only source of the version. The release build injects it into the binary.

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
- a milestone has no open issues left

The issue lists the commits since the last tag and drafts the release notes, including a Breaking section.

## Release notes

GoReleaser generates the changelog from commit subjects, grouped by commit prefix. `docs:`, `test:` and `ci:` commits and merge commits are left out.

Breaking changes get a hand-written Breaking section. The owner adds it to the draft release, starting from the draft in the release issue, before approving the `release` environment.

GitHub releases are the project's changelog. There is no `CHANGELOG.md` in the repository.

## Who does what

Tagging and approving the `release` environment are always the owner's. No agent tags a release or approves the environment.

The mechanics after a `v*` tag is pushed are in `.github/workflows/release.yml`: the workflow waits for approval in the `release` environment, then GoReleaser builds archives for macOS, Linux and Windows on amd64 and arm64 plus `.deb` and `.rpm` packages and checksums, the Windows installers are built and attached, and the Homebrew tap formula is updated.

## Where the tooling differs from this policy today

These are known gaps between the policy above and the files in the repository. Each needs an owner change to a protected file or an open issue to close.

- `.goreleaser.yml` filters the changelog but defines no groups, so the generated list is flat, in commit order.
- `.goreleaser.yml` sets `release.draft: false`, and GoReleaser only runs after the `release` environment is approved. There is no draft release to edit before approval. Until that changes, the Breaking section is added to the published release from the draft in the release issue.
- `cmd/via/main.go` still carries a version constant that a plain `make build` reports. Release builds override it from the tag. [#27](https://github.com/tonhe/viaduct/issues/27) tracks removing the second source.

## Lifecycle

How an idea becomes part of a release.

1. **Intake.** Anyone files issues. The backlog agent triages each one: type, area, risk, duplicates, and one of `agent:ready`, `needs:human` or `agent:paired`. Triage never assigns a milestone.
2. **Bugs skip planning.** The loop works bugs first, and they ship through the fix release described above. A bug gets a milestone only if it blocks that milestone's feature.
3. **Backlog.** Ideas stay without a milestone. Work that spans several issues becomes an epic with the `roadmap` label. The `stretch` label means not until promoted.
4. **Planning.** When the active milestone has 3 or fewer open issues, the backlog agent opens a "Plan vX.Y" issue labelled `needs:human`. It proposes one theme (the next epic in [ROADMAP.md](ROADMAP.md) order), up to 5 small features, any stretch promotions, and what moves out, each with a size and a recommendation. The owner answers it. Approved epics get `agent:paired` and small items get `agent:ready`.
5. **Size.** One theme per minor, at most about 10 issues, aiming for 2 to 6 weeks. A spec that turns out bigger is split across two minors.
6. **Horizon.** Milestones exist for the active release and the next one or two planned themes. Today that is v0.3.0, v0.4.0 (M6) and v0.5.0 (M7).
7. **Roadmap order.** The owner owns the order. It is a short ordered list of epics in [ROADMAP.md](ROADMAP.md).
