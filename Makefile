# Trace — self-hosted Git forge for small teams (github.com/GrayCodeAI/trace).
# Run `make help` for the targets. Every target runs with GOWORK=off so a
# go.work file in a parent directory cannot change what is built.

NAME     := trace
MAIN_PKG := ./cmd/trace
export GOWORK := off

VERSION ?= $(shell v=$$(head -n1 VERSION 2>/dev/null | tr -d '[:space:]'); if [ -n "$$v" ]; then echo "$$v"; else echo dev; fi)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildDate=$(DATE)

.PHONY: help build install test race vet fmt fmt-check tidy-check vulncheck check clean version

help: ## List targets.
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

build: ## Build bin/trace with version information.
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(NAME) $(MAIN_PKG)

install: ## Install trace into $(go env GOPATH)/bin.
	CGO_ENABLED=0 go install -trimpath -ldflags="$(LDFLAGS)" $(MAIN_PKG)

test: ## Run the test suite (starts local listeners, shells out to git and ssh).
	go test -count=1 -timeout=20m ./...

race: ## Run the test suite with the race detector.
	go test -race -count=1 -timeout=30m ./...

vet: ## Run go vet.
	go vet ./...

fmt: ## Format the source with gofmt.
	gofmt -w .

fmt-check: ## Fail if any file is not gofmt-formatted.
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

tidy-check: ## Fail if go.mod or go.sum are not tidy.
	go mod tidy
	git diff --exit-code -- go.mod go.sum

vulncheck: ## Scan reachable code for known vulnerabilities (needs network).
	go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...

check: fmt-check vet build race ## The local gate: formatting, vet, build, race tests.

version: ## Print the version from VERSION.
	@echo $(VERSION)

clean: ## Remove build output.
	rm -rf bin dist
