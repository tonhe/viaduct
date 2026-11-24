# CLAUDE.md

Rules for anyone changing this repository, human or agent. The dev agent reads this file on every run. Humans should start with [CONTRIBUTING.md](CONTRIBUTING.md), which points back here.

viaduct (`via`) is a terminal traceroute tool written in Go. Package layout and data flow are in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Anything that touches `internal/tui` follows [docs/UI-DESIGN.md](docs/UI-DESIGN.md). Release policy is in [RELEASING.md](RELEASING.md).

## Build and verify

```
make build       # builds ./via
make test        # go test ./...
make test-all    # build + go test -race ./... + coverage gate
```

Run `make test-all` before pushing. It is the local equivalent of the `Tests` workflow and must pass.

After an intentional change to TUI output, run `make golden-update`, read the diff under `internal/tui/testdata/`, and commit the golden files with the code change.

## Tests

All new code ships with tests.

- A new exported function or method needs unit tests.
- A bug fix needs a regression test that would have caught the bug.
- A feature that crosses package boundaries needs an integration test at the seam where the packages meet.
- Internal helpers with no observable behaviour, generated code and data-only fixtures are exempt.

Conventions:

- Standard library `testing` only. No testify or other assertion libraries.
- Table-driven tests where the case space is small.
- Addresses in fixtures come from the documentation ranges: RFC 5737 (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`) and RFC 3849 (`2001:db8::/32`).

## Coverage floors

`scripts/check-coverage.sh` holds a minimum coverage figure for each package. `make test-all` and CI fail when a package drops below its floor.

Floors only go up. If your change drops a package below its floor, add tests. Do not lower the floor.

## IPv6-protected code

The IPv6 code paths are regression-protected:

- `internal/probe/family.go`
- `internal/sysprobe/`
- the reverse-DNS logic in `internal/asn/` (the Team Cymru lookup names)
- ICMP handling (`internal/probe/proto_icmp.go`, `internal/probe/echo.go`, and the listener in `internal/probe/probe.go`)
- family selection in `cmd/via/main.go`

If a test failure would be fixed by modifying any of these, stop. Pin the current behaviour in the test and report what you found on the issue. Do not change that code to make a test pass.

There is one override. An issue comment written by the repository owner that begins with "Owner decision" and names the approved scope allows work on these paths for that issue. The work stays inside the scope the comment names. A comment from anyone else, or an owner comment that doesn't name the scope, is not an override.

## Paths the dev agent does not change

- `Makefile`
- `scripts/`
- `.goreleaser.yml`
- `.github/`

The first three are the `protected_paths` list in `.github/workflows/agent-dev.yml`. If an issue can't be finished without changing one of these, say so on the issue and leave the change to the owner.

## Network lab

The lab runs `via` against real packets on a topology built from Linux network namespaces (`scripts/lab/`, tests in `internal/lab/` behind the `lab` build tag). Linux only. It sends nothing outside its own namespaces.

```
make lab-build && sudo make lab-test    # on a host where you have root
make lab-docker                         # anywhere with Docker
```

It needs iproute2, nftables, tcpdump and mtr. CI runs it as the `lab` job of the `Tests` workflow, so a pull request gets a lab run even when the author can't run it locally.

Lab assertions are exact counts. A scenario drops exactly every Nth reply, kernel counters and a packet capture record what was sent and dropped, and the test requires `via` to report the same numbers. When a scenario fails, open an issue for it. Do not loosen the assertion, add a tolerance, or skip the test to get a green run.

## How loss is defined

Loss is per probe: TTL-limited probes that got no reply, divided by TTL-limited probes sent, counted over completed rounds. The round still waiting for replies is excluded, so a live view never shows probes in flight as lost. `Rnd` is the number of rounds.

A hop is marked rate-limited (`~N%†`) only when its ICMP TTL-probe loss is significantly higher than its direct-echo loss (two-proportion z-test, p < 0.01) with at least 20 echo probes sent. The destination is never marked. A marked hop shows its echo loss.

The TUI and report mode show the same loss figure for a hop. JSON exports carry the per-probe loss with the probe counts it was computed from, plus per-node counts for each probe type and the `rate_limited` verdict. A change that makes these outputs disagree with each other is a bug.

## Commits and pull requests

Commit subjects use a conventional prefix: `feat:`, `fix:`, `docs:`, `test:`, `ci:`, `refactor:`, `chore:`. A scope in parentheses is fine (`fix(tui): ...`).

Mark a breaking change with `!` after the prefix (`feat!: ...`) or a `BREAKING CHANGE:` footer in the body. The prefixes decide the next version number and build the release notes (see [RELEASING.md](RELEASING.md)), so pick the one that matches the change.

No AI attribution in commits or pull requests: no co-author trailers, no "generated with" footers.

## Other rules

- Never print or log passwords, tokens or other secrets.
- Update `README.md` in the same change when user-visible behaviour changes: flags, keys, columns, output formats, defaults.
- There is no `CHANGELOG.md`. GitHub releases are the changelog.
