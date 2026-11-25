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

.PHONY: build clean install test test-tui test-all test-race golden-update coverage-gate soak-test lab-build lab-test lab-docker

build:
	go build $(LDFLAGS) -o $(BINARY) $(PKG)

clean:
	rm -f $(BINARY) $(LAB_TEST)
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
