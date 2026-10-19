VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test race cover check eval e2e run mcp hooks clean

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

# eval runs the assistant evals (evals/) against a real model: the one your
# settings choose, else Claude with ANTHROPIC_API_KEY. It costs money and is
# not deterministic, so it is not part of check. EVAL_RUNS repeats each case.
eval:
	BBBUDGET_EVAL=1 BBBUDGET_EVAL_RUNS=$(or $(EVAL_RUNS),1) \
		go test -count=1 -run '^TestEvals$$' -v ./internal/evals

# Browser tests: the real binary and headless Chromium (CHROME=path to pick one).
# Needs Node 22+; skipped when no Chromium is found.
e2e: build
	node --test e2e/*.test.mjs

run: build
	./bin/bbbudget serve

mcp: build
	./bin/bbbudget mcp

hooks:
	-git config --unset core.hooksPath
	pre-commit install --hook-type pre-commit --hook-type pre-push

clean:
	rm -rf bin coverage.out
