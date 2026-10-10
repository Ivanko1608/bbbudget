VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test race cover check e2e run mcp hooks clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/bbbudget ./cmd/bbbudget

test:
	go test ./...

race:
	go test -race ./...

cover:
	go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

check:
	gofmt -l . | (! grep .) && go vet ./... && go test -race ./...

# Browser tests: the real binary and headless Chromium (CHROME=path to pick one).
# Needs Node 22+; skipped when no Chromium is found.
e2e: build
	node --test e2e/*.test.mjs

run: build
	./bin/bbbudget serve

mcp: build
	./bin/bbbudget mcp

hooks:
	git config core.hooksPath .githooks

clean:
	rm -rf bin coverage.out
