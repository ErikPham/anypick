# AnyPick — Go engine + native macOS app.
#
# Conventions: `make test` is `go test ./...`; there is no wrapper around it.
# The only thing this file adds is GOPATH hygiene, because the ambient GOPATH
# on this machine points at another project's module cache (see the comment on
# GOPATH below) and that makes `cmd/anypick` stall for minutes.

SHELL := /bin/bash
GO_DIR := go

# The module cache must be writable and must not be shared with another
# project. When it is shared, tests that shell out to `go build`
# (cmd/anypick's requireBuiltCLI) stall in module resolution and the package
# times out after many minutes, which looks exactly like a broken change.
# This is a plain assignment, not `?=`, on purpose: the ambient GOPATH exports
# another project's path, and `?=` would silently keep it. Command-line
# overrides still win (`make test GOPATH=/somewhere`), and in the common case
# you should not need to set GOPATH at all — Go modules have not required it
# since 1.16, so consider deleting the export from ~/.zshrc.
GOPATH := $(HOME)/go
export GOPATH

.PHONY: help build test test-race vet fmt fmt-check run sandbox \
        release-check release-set release-bump artifact-check \
        app app-test clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ── engine ────────────────────────────────────────────────────────────

build: ## Build the CLI into go/bin/anypick-dev
	cd $(GO_DIR) && go build -o bin/anypick-dev ./cmd/anypick

test: ## Run the Go suite
	cd $(GO_DIR) && go test ./... -count=1 -timeout 900s

test-race: ## Run the Go suite under the race detector
	cd $(GO_DIR) && go test -race ./... -count=1 -timeout 900s

vet: ## go vet
	cd $(GO_DIR) && go vet ./...

fmt: ## Rewrite Go sources with gofmt
	cd $(GO_DIR) && gofmt -w .

fmt-check: ## Fail if any Go source is not gofmt-clean
	cd $(GO_DIR) && test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

run: build ## Build, then run a command: make run ARGS="providers"
	$(GO_DIR)/bin/anypick-dev $(ARGS)

sandbox: build ## Same, against a throwaway HOME and ANYPICK_HOME
	@HOME=$${TMPDIR:-/tmp}/anypick-sandbox/home \
	 ANYPICK_HOME=$${TMPDIR:-/tmp}/anypick-sandbox/data \
	 sh -c 'mkdir -p "$$HOME" "$$ANYPICK_HOME"; exec $(GO_DIR)/bin/anypick-dev $(ARGS)'

# ── release gates ─────────────────────────────────────────────────────

release-check: ## VERSION and Xcode MARKETING_VERSION agree
	cd $(GO_DIR) && go run ./cmd/anypick-release check

release-set: ## make release-set VERSION=1.2.3
	cd $(GO_DIR) && go run ./cmd/anypick-release set $(VERSION)

release-bump: ## make release-bump PART=major|minor|patch
	cd $(GO_DIR) && go run ./cmd/anypick-release bump $(PART)

artifact-check: ## ADR 0020 §8: a built artifact contains no Node runtime
	cd $(GO_DIR) && go run ./cmd/anypick-artifact node-free $(ROOT)

# ── macOS app ─────────────────────────────────────────────────────────

app: ## Open the Xcode project
	open macos/AnyPick.xcodeproj

app-test: ## Build and unit-test the app and the bundled helper contract
	xcodebuild -project macos/AnyPick.xcodeproj -scheme AnyPick \
	  -destination platform=macOS CODE_SIGNING_ALLOWED=NO test

# ── housekeeping ──────────────────────────────────────────────────────

clean: ## Remove build output (keeps the module cache)
	rm -rf $(GO_DIR)/bin
