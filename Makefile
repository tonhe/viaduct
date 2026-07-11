BINARY   := via
PKG      := ./cmd/via
BUILD_TS := $(shell date -u '+%Y%m%d.%H%M')
LDFLAGS  := -ldflags "-X main.buildVersion=$(BUILD_TS)"

.PHONY: build clean install test test-tui test-all test-race golden-update coverage-gate soak-test

build:
	go build $(LDFLAGS) -o $(BINARY) $(PKG)

clean:
	rm -f $(BINARY)
	rm -rf dist/ bin/

install: build
	mkdir -p $(HOME)/bin
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

# Soak test: opt-in, hits real network, requires raw-socket capability.
# Runs the full sweep (10+ scenarios) against public hosts.
# Requires ~/bin/via to be built and installed (make install first).
soak-test:
	@echo "==> Soak test — runs real traces against public hosts."
	@echo "==> Requires sudo or CAP_NET_RAW on ~/bin/via."
	sudo -E go test -tags soak -v -timeout 30m ./cmd/via/...
