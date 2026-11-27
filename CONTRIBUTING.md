# Contributing

The rules for changing this repository are in [CLAUDE.md](CLAUDE.md). They apply to people and to the automated agents alike: tests with every change, coverage floors that only go up, a live check for socket code, an owner's decision for behaviour changes, and the commit message prefixes. Read it before opening a pull request.

## Build and test

You need the Go version named in `go.mod`.

```
git clone https://github.com/tonhe/viaduct.git
cd viaduct
make build       # builds ./via
make test        # fast test run
make test-all    # what CI runs: build, race tests, coverage gate
```

`via` opens raw sockets, so running it needs `sudo` or, on Linux, `sudo setcap cap_net_raw+ep ./via`. The unit tests need neither.

A few tests in `internal/probe` open real raw sockets on the loopback addresses and skip without root. `make test-root-build && sudo make test-root` runs them on Linux and macOS, with `VIA_REQUIRE_RAW_SOCKETS=1` set so that a skip is a failure. CI runs them on every pull request.

The network lab (`make lab-build && sudo make lab-test`, or `make lab-docker`) checks `via`'s numbers against real packets on Linux. CI runs it on every pull request.

## Issues

Bugs and feature requests go in [GitHub issues](https://github.com/tonhe/viaduct/issues). For a bug, include the command line, the operating system, and what you expected to see.

Much of the work here is done by an automated loop that triages issues, writes changes and reviews pull requests. It only works issues opened by the repository owner. An issue from anyone else is read by the owner, who adopts it into the loop if it is going to be worked. Nothing extra is needed from you for that to happen.

## Pull requests

- Run `make test-all` first.
- Keep the change to one topic and use a conventional commit prefix (`feat:`, `fix:`, `docs:`, ...).
- Update `README.md` if user-visible behaviour changes.

## Further reading

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): packages, probe and reply flow, concurrency, known gotchas
- [docs/UI-DESIGN.md](docs/UI-DESIGN.md): colors, glyphs, columns and layout rules for the TUI
- [RELEASING.md](RELEASING.md): how versions are picked and releases are cut
- [ROADMAP.md](ROADMAP.md): what is planned, in order
