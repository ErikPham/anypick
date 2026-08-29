# AnyPick — one Go engine, two native front ends.
#
# Layout:
#   src/go/      the engine: CLI, terminal UI, desktop service, proxy, hub
#   src/macos/   the macOS app (SwiftUI)
#   src/tauri/   the Linux and Windows app (Tauri 2 + React)
#
# `make test` is `go test ./...`. Nothing wraps it. The only thing this file
# adds is GOPATH hygiene, because the ambient GOPATH on this machine points at
# another project's module cache (see the note on GOPATH below).

SHELL := /bin/bash

GO_DIR := src/go
MACOS_DIR := src/macos
TAURI_DIR := src/tauri
TAURI_CORE := $(TAURI_DIR)/src-tauri

# The module cache must be writable and must not be shared with another
# project. When it is shared, tests that shell out to `go build`
# (src/go/cmd/anypick's requireBuiltCLI) stall in module resolution and the
# package times out after many minutes, which looks exactly like a broken
# change. This is a plain assignment, not `?=`, because the ambient GOPATH
# exports another project's path and `?=` would silently keep it.
GOPATH := $(HOME)/go
export GOPATH

.PHONY: help build test test-race vet fmt fmt-check run sandbox paths \
        release-check release-set release-bump artifact-check \
        app app-build app-test app-verify \
        tauri-deps tauri-ui tauri-build tauri-dev clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ── engine ────────────────────────────────────────────────────────────

build: ## Build the CLI into src/go/bin/anypick-dev
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

# ── path integrity ────────────────────────────────────────────────────

# Several build scripts resolve the repository root by walking a fixed number
# of `..` levels. Moving a directory silently breaks every one of them, and the
# failure surfaces much later as a confusing build error. This target makes the
# assumption explicit and cheap to check. If you move a directory under src/,
# run `make paths` before anything else.
paths: ## Verify every script still resolves the repository root
	@set -e; \
	fail=0; \
	echo "checking repository layout"; \
	for d in $(GO_DIR) $(MACOS_DIR) $(TAURI_DIR); do \
	  test -d "$$d" || { echo "  MISSING $$d"; fail=1; }; \
	done; \
	echo "checking macOS helper script"; \
	got="$$(cd $(MACOS_DIR)/scripts && cd ../../.. && pwd)"; \
	if [ "$$got" = "$$(pwd)" ]; then echo "  OK   bundle-go-helper.sh"; \
	else echo "  FAIL bundle-go-helper.sh resolves to $$got"; fail=1; fi; \
	echo "checking Xcode asset references"; \
	while read -r rel; do \
	  rel="$$(printf '%s' "$$rel" | sed 's/.*path = //; s/;$$//')"; \
	  case "$$rel" in ../*) \
	    if [ ! -e "$(MACOS_DIR)/$$rel" ]; then echo "  FAIL $$rel"; fail=1; else echo "  OK   $$rel"; fi;; \
	  esac; \
	done < <(grep -o 'path = \.\.[^;]*;' $(MACOS_DIR)/AnyPick.xcodeproj/project.pbxproj | sort -u); \
	echo "checking Tauri icon references"; \
	while read -r icon; do \
	  icon="$$(printf '%s' "$$icon" | sed 's/.*"\(.*\)".*/\1/')"; \
	  if [ ! -e "$(TAURI_CORE)/$$icon" ]; then echo "  FAIL $$icon"; fail=1; else echo "  OK   $$icon"; fi; \
	done < <(grep -o '"\.\./\.\./\.\./assets/[^"]*"' $(TAURI_CORE)/tauri.conf.json); \
	echo "checking Go sidecar path in build.rs"; \
	go_dep="$$(grep -o 'rerun-if-changed=[^"]*' $(TAURI_CORE)/build.rs | sed 's/rerun-if-changed=//')"; \
	if [ -d "$(TAURI_CORE)/$$go_dep" ]; then echo "  OK   $$go_dep"; else echo "  FAIL $$go_dep"; fail=1; fi; \
	if [ "$$fail" -ne 0 ]; then echo "path check FAILED"; exit 1; fi; \
	echo "path check OK"

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
	open $(MACOS_DIR)/AnyPick.xcodeproj

app-build: ## Build the app unsigned; a build phase bundles the Go helper
	xcodebuild -project $(MACOS_DIR)/AnyPick.xcodeproj -scheme AnyPick \
	  -destination platform=macOS CODE_SIGNING_ALLOWED=NO build

app-test: ## Build and unit-test the app and the bundled helper contract
	xcodebuild -project $(MACOS_DIR)/AnyPick.xcodeproj -scheme AnyPick \
	  -destination platform=macOS CODE_SIGNING_ALLOWED=NO test

app-verify: app-build ## Build, then assert ADR 0020 on the built bundle
	cd $(GO_DIR) && go run ./cmd/anypick-artifact node-free \
	  "$$DERIVED_DATA_DIR/Build/Products/Debug/AnyPick.app"

# ── Tauri app (Linux / Windows) ───────────────────────────────────────

tauri-deps: ## Install the Tauri UI dependencies
	cd $(TAURI_DIR)/ui && npm install

tauri-ui: ## Build the UI bundle into src/tauri/frontend
	cd $(TAURI_DIR)/ui && npm run build

tauri-build: tauri-ui ## Build the packaged desktop app for this platform
	cd $(TAURI_CORE) && cargo tauri build

tauri-dev: ## Run the desktop app against the real Go service
	cd $(TAURI_DIR)/ui && npm run build
	cd $(TAURI_CORE) && cargo tauri dev

# ── housekeeping ──────────────────────────────────────────────────────

clean: ## Remove build output (keeps the module cache)
	rm -rf $(GO_DIR)/bin
