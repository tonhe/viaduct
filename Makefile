BINARY   := via
PKG      := ./cmd/via
BUILD_TS := $(shell date -u '+%Y%m%d.%H%M')
LDFLAGS  := -ldflags "-X main.buildVersion=$(BUILD_TS)"

.PHONY: build clean install test

build:
	go build $(LDFLAGS) -o $(BINARY) $(PKG)

clean:
	rm -f $(BINARY)
	rm -rf dist/ bin/

install: build
	mkdir -p $(HOME)/bin
	cp $(BINARY) $(HOME)/bin/$(BINARY)

test:
	go test ./...
