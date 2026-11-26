BINARY   := via
PKG      := ./cmd/via
BUILD_TS := $(shell date -u '+%Y%m%d.%H%M')
# The git tag is the only source of the version. `git describe` gives the tag
# on a tagged commit and tag-N-gHASH after it, with -dirty for a modified
# tree; the leading "v" is dropped, as GoReleaser does for release builds.
# Outside a git checkout this is empty, and via falls back to the build info
# Go recorded (see cmd/via/version.go).
VERSION  := $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null))
LDFLAGS  := -ldflags "-X main.version=$(VERSION) -X main.buildVersion=$(BUILD_TS)"

LAB_TEST := lab.test

# The real-socket tests of internal/probe, which need root (see test-root).
# A test that is not built for the platform (TestBPF_ is macOS only,
# TestListenTCP_ is Linux only) simply does not match, which leaves four on
# each of the two systems. test-root checks that count, so a test that is
# renamed and falls out of the pattern fails the run instead of going missing.
ROOT_TEST       := probe.root.test
ROOT_TESTS      := ^(TestRun_IPv6ProbeChecksumValidForKernelSource|TestOpenEchoConn_TakesAsLittleAsThePlatformAllows|TestEchoConn_SmallReceiveBufferHoldsNextToNothing|TestBPF_SYNACKFromALocalListenerIsReceived|TestListenTCP_RawSocketDeliversTheSYNACK)$$
ROOT_TEST_COUNT := 4

.PHONY: build clean install test test-tui test-all test-race golden-update coverage-gate soak-test lab-build lab-test lab-docker test-root-build test-root

build:
	go build $(LDFLAGS) -o $(BINARY) $(PKG)

clean:
	rm -f $(BINARY) $(LAB_TEST) $(ROOT_TEST)
	rm -rf dist/ bin/

install: build
	mkdir -p $(HOME)/bin
	# Unlink before copy so the destination gets a fresh inode. Overwriting an
	# ad-hoc-signed binary in place (plain cp) leaves AMFI's cached cdhash for
	# the old inode in force; the new bytes fail that check and every exec is
	# SIGKILLed ("zsh: killed") even though `codesign -v` passes. rm sidesteps
	# the cache. BSD `install` does the same unlink-then-write.
	rm -f $(HOME)/bin/$(BINARY)
	cp $(BINARY) $(HOME)/bin/$(BINARY)

# Run all tests (fast). Matches what basic CI runs.
test:
	go test ./...

# Run all tests with race detection. Matches the CI "Test (race)" step.
test-race:
	go test -race ./...

# Full local equivalent of CI: build + race tests + coverage gate.
# Use this before pushing to catch what CI would catch.
test-all: build test-race coverage-gate
	@echo
	@echo "All checks passed. Safe to push."

# TUI package tests only. Fast feedback for TUI work.
test-tui:
	go test -race ./internal/tui/...

# Regenerate golden files. Uses -update-goldens (renamed to avoid collision
# with teatest's transitive dep -update flag).
golden-update:
	go test ./internal/tui/... -update-goldens

# Run the per-package coverage floor check (matches CI).
coverage-gate:
	bash scripts/check-coverage.sh

# Root-only tests: the tests of internal/probe that open real raw sockets (and
# /dev/bpf* on macOS) and skip without root, so the unprivileged runs above
# never execute them. They send to the loopback addresses only (127.0.0.1 and
# ::1). Linux and macOS.
#
#   make test-root-build && sudo make test-root
#
# Building is separate from running so the build uses your Go toolchain and
# only the run needs root. VIA_REQUIRE_RAW_SOCKETS=1 turns the tests' skips
# into failures: a host that cannot run one of them fails here instead of
# passing with the test skipped.
test-root-build:
	go test -c -o $(ROOT_TEST) ./internal/probe

test-root:
	@if [ "$$(id -u)" != "0" ]; then \
		echo "test-root needs root (raw sockets; /dev/bpf* on macOS)."; \
		echo "Run 'make test-root-build' and then 'sudo make test-root'."; \
		exit 1; \
	fi
	@if [ ! -x $(ROOT_TEST) ]; then \
		echo "Run 'make test-root-build' first, as your normal user."; \
		exit 1; \
	fi
	@n=$$(./$(ROOT_TEST) -test.list '$(ROOT_TESTS)' | wc -l); \
	if [ "$$n" -ne $(ROOT_TEST_COUNT) ]; then \
		echo "ROOT_TESTS matches $$n tests in $(ROOT_TEST), want $(ROOT_TEST_COUNT): was one renamed?"; \
		exit 1; \
	fi
	cd internal/probe && env VIA_REQUIRE_RAW_SOCKETS=1 $(CURDIR)/$(ROOT_TEST) -test.v -test.count 1 -test.timeout 5m -test.run '$(ROOT_TESTS)'

# Network lab: checks via's numbers against real packets on a topology built
# from Linux network namespaces (scripts/lab, internal/lab). Deterministic: it
# drops exact packets and asserts exact counts. Linux only; sends nothing
# outside its own namespaces.
#
#   make lab-build && sudo make lab-test     on a host where you have root
#   make lab-docker                          anywhere with Docker
#
# Building is separate from running so the build uses your Go toolchain and
# only the run needs root. Needs iproute2, nftables, tcpdump and mtr.
lab-build: build
	go test -c -tags lab -o $(LAB_TEST) ./internal/lab

lab-test:
	@if [ "$$(id -u)" != "0" ]; then \
		echo "lab-test needs root (network namespaces, nftables, raw sockets)."; \
		echo "Run 'make lab-build' and then 'sudo make lab-test', or use 'make lab-docker'."; \
		exit 1; \
	fi
	@if [ ! -x $(LAB_TEST) ] || [ ! -x $(BINARY) ]; then \
		echo "Run 'make lab-build' first, as your normal user."; \
		exit 1; \
	fi
	cd internal/lab && LAB_REPO_ROOT=$(CURDIR) VIA_BIN=$(CURDIR)/$(BINARY) $(CURDIR)/$(LAB_TEST) -test.v -test.timeout 15m

lab-docker: lab-build
	docker build -q -t vialab:local scripts/lab
	docker run --rm --privileged -v $(CURDIR):/src:ro -w /src/internal/lab \
		-e LAB_STRICT vialab:local /src/$(LAB_TEST) -test.v -test.timeout 15m

# Soak test: opt-in, hits real network, requires raw-socket capability.
# Runs the full sweep (10+ scenarios) against public hosts.
# Requires ~/bin/via to be built and installed (make install first).
soak-test:
	@echo "==> Soak test — runs real traces against public hosts."
	@echo "==> Requires sudo or CAP_NET_RAW on ~/bin/via."
	sudo -E go test -tags soak -v -timeout 30m ./cmd/via/...
