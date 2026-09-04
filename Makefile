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

# Resolve the Go toolchain once, and derive `gofmt` from wherever `go` landed.
#
# CI gets a toolchain from actions/setup-go. A developer machine may not have
# one on PATH at all — and a bare `go: command not found` (exit 127) reads
# exactly like a change that broke the build. The usual response to that is to
# run the gate "later", and a gate that is not run is a gate that is green.
# CODE_REVIEW.md §1: a rule that cannot be followed decays, and takes the rest
# of the standard with it. So search the usual install locations, and when
# nothing is there say what to install instead of exiting 127.
GO_SEARCH := $(shell command -v go 2>/dev/null) \
             $(wildcard $(HOME)/sdk/go*/bin/go) \
             $(wildcard /usr/local/go/bin/go) \
             $(wildcard /opt/homebrew/bin/go)
GO := $(firstword $(GO_SEARCH) go)
GOFMT := $(patsubst %go,%gofmt,$(GO))

# go.mod requires go1.26. An older local toolchain delegates upward only when
# GOTOOLCHAIN is `auto`; pinned `local` it dies on a module-version error that
# looks like a broken change. `?=` so a deliberate override still wins.
export GOTOOLCHAIN ?= auto

.PHONY: help go-toolchain build test test-race vet fmt fmt-check lint review run sandbox paths \
        release-check release-plan release-set release-bump artifact-check \
        protocol-sync gates \
        app app-build app-test app-verify \
        tauri-deps tauri-ui tauri-check tauri-build tauri-dev clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ── engine ────────────────────────────────────────────────────────────

# Prerequisite of every target that shells out to Go. It fails with the fix
# rather than a bare 127, because the cost of an unrunnable gate is not the
# error — it is that the gate quietly stops being part of anyone's routine.
go-toolchain:
	@command -v "$(GO)" >/dev/null 2>&1 || { \
	  echo "go not found. Looked on PATH and in $(HOME)/sdk/go*/bin."; \
	  echo "install:  https://go.dev/dl"; \
	  echo "or point at a toolchain you already have:"; \
	  echo "  export PATH=/path/to/go/bin:\$$PATH"; \
	  exit 1; }

build: go-toolchain ## Build the CLI into src/go/bin/anypick-dev
	cd $(GO_DIR) && $(GO) build -o bin/anypick-dev ./cmd/anypick

test: go-toolchain ## Run the Go suite
	cd $(GO_DIR) && $(GO) test ./... -count=1 -timeout 900s

test-race: go-toolchain ## Run the Go suite under the race detector
	cd $(GO_DIR) && $(GO) test -race ./... -count=1 -timeout 900s

vet: go-toolchain ## go vet
	cd $(GO_DIR) && $(GO) vet ./...

fmt: go-toolchain ## Rewrite Go sources with gofmt
	cd $(GO_DIR) && $(GOFMT) -w .

fmt-check: go-toolchain ## Fail if any Go source is not gofmt-clean
	cd $(GO_DIR) && test -z "$$($(GOFMT) -l .)" || { $(GOFMT) -l .; exit 1; }

# Prefer the GOPATH this Makefile pins. The ambient GOPATH on this machine
# belongs to another project, and its staticcheck was built with an older Go:
# it cannot analyse a module that requires go1.26 and dies with a "module
# requires at least go1.26.0" message that looks like a broken change.
STATICCHECK := $(firstword $(wildcard $(GOPATH)/bin/staticcheck) \
                          $(shell command -v staticcheck 2>/dev/null) staticcheck)

# `go vet` is the floor, not the ceiling. This must fail when the tool is
# absent rather than skip: a gate that quietly does nothing is worse than no
# gate, because the next person reads "lint: ok" and stops looking.
# staticcheck shells out to `go list`, so it needs the same toolchain every
# other target resolves — without it this gate dies with "go command required"
# and stops being part of anyone's routine (same failure as review's 127).
lint: go-toolchain ## staticcheck over the engine (CODE_REVIEW.md §4)
	@test -x "$(STATICCHECK)" || { \
	  echo "staticcheck not found (looked in $(GOPATH)/bin and on PATH)."; \
	  echo "install: go install honnef.co/go/tools/cmd/staticcheck@v0.8.1"; \
	  exit 1; }
	cd $(GO_DIR) && PATH="$(dir $(GO)):$$PATH" "$(STATICCHECK)" ./...

# The four gates CODE_REVIEW.md §4 asks for, in one command. `lint` is separate
# because it needs a tool this machine may not have; CI installs it before use.
review: fmt-check vet test gates ## Every gate in CODE_REVIEW.md §4, in one command
	@echo "review gates green. 'make lint' (staticcheck) also runs in CI."

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
#
# NEVER put a `#` comment inside this recipe's backslash-continuation chain.
# Make joins the continuation into a single line before the shell sees it, so
# the first `#` comments out the rest of the target — including the final
# `exit 1`. That is exactly what happened here: a four-line explanation of
# `--no-index` silently disabled the git-ignored-source check, the
# authority-documents check, and the failure exit, so `make paths` reported
# success no matter what. It was dead from the commit that added it, while
# CODE_REVIEW.md §10 recorded it as landed. Put comments here, above the
# target, or on their own `true # …` line.
#
# What the checks below cover, and why each exists:
#   - git-ignored .go source: an unanchored .gitignore pattern swallowed
#     src/go/internal/refs once, so main did not build and `git status` said
#     nothing. `--no-index` matters: plain `git check-ignore` skips paths that
#     are already tracked, so it would never flag a pattern that happens to
#     match something in the index. Ask what the pattern excludes.
#   - tracked build artifacts: naming them individually is how src/go/anypick,
#     a 16.9 MB Mach-O, got committed — the old pattern was `go/anypick`,
#     written before commit 44e5046 moved everything under src/, and once a
#     file is tracked .gitignore no longer applies to it. So detect the
#     artifact (`grep -I` treats a binary as non-matching) instead of listing
#     names.
#   - authority documents: AGENTS.md points at documents a clean checkout must
#     have, and an untracked doc breaks review for every agent starting fresh.
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
	echo "checking macOS helper Go module path"; \
	helper_go="$$(awk '/^go_root=/ { s=$$0; sub(/.*repo_root\}\//, "", s); gsub(/"/, "", s); print s; exit }' $(MACOS_DIR)/scripts/bundle-go-helper.sh)"; \
	if [ -z "$$helper_go" ]; then \
	  echo "  FAIL cannot read go_root out of bundle-go-helper.sh"; fail=1; \
	elif [ ! -f "$$helper_go/go.mod" ]; then \
	  echo "  FAIL helper builds from $$helper_go/go.mod (missing)"; fail=1; \
	else echo "  OK   $$helper_go/go.mod"; fi; \
	echo "checking Xcode SRCROOT references"; \
	while read -r rel; do \
	  if [ ! -e "$(MACOS_DIR)/$$rel" ]; then echo "  FAIL SRCROOT/$$rel"; fail=1; \
	  else echo "  OK   SRCROOT/$$rel"; fi; \
	done < <(sed -n 's@.*SRCROOT)/@@p' $(MACOS_DIR)/AnyPick.xcodeproj/project.pbxproj \
	         | sed 's@".*@@; s@\\.*@@' | grep -v '^$$' | sort -u); \
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
	echo "checking GitHub Actions working-directories"; \
	while read -r wd; do \
	  case "$$wd" in ''|'.'|'*$${{'*) continue;; esac; \
	  case "$$wd" in *'$${{'*) echo "  SKIP $$wd (expression)"; continue;; esac; \
	  if [ -d "$$wd" ]; then echo "  OK   $$wd"; \
	  else echo "  FAIL working-directory '$$wd' does not exist"; fail=1; fi; \
	done < <(grep -h -o 'working-directory:[[:space:]]*[^[:space:]]*' .github/workflows/*.yml \
	         | sed 's/working-directory:[[:space:]]*//' | sort -u); \
	echo "checking no Go source is git-ignored"; \
	ignored="$$(find $(GO_DIR) -name '*.go' -not -path '*/bin/*' \
	          | git check-ignore --no-index --stdin 2>/dev/null || true)"; \
	if [ -n "$$ignored" ]; then \
	  printf '%s\n' "$$ignored" | sed 's/^/  FAIL /'; \
	  echo "       git-ignored source can never be committed, and git status"; \
	  echo "       stays completely silent about it (see .gitignore)"; \
	  fail=1; \
	else \
	  echo "  OK   every .go file under $(GO_DIR) is visible to git"; \
	fi; \
	echo "checking no build artifact is tracked"; \
	total="$$(git ls-files src/ | wc -l | tr -d ' ')"; \
	text="$$(git grep -Il . -- src/ 2>/dev/null | wc -l | tr -d ' ')"; \
	empty="$$(git ls-files src/ | while read -r f; do [ -s "$$f" ] || echo "$$f"; done | wc -l | tr -d ' ')"; \
	not_artifact="$$((text + empty))"; \
	if [ "$$total" != "$$not_artifact" ]; then \
	  echo "  FAIL $$((total - not_artifact)) tracked binary(ies) under src/ (total $$total, text $$text, empty $$empty)"; \
	  echo "       list them:  git ls-files src/ | grep -vFxf <(git grep -Il . -- src/)"; \
	  echo "       untrack it: git rm --cached <path> (the file stays on disk)"; \
	  fail=1; \
	else \
	  echo "  OK   every tracked file under src/ is text (no binary committed)"; \
	fi; \
	echo "checking the authority documents are tracked"; \
	for doc in AGENTS.md CODE_REVIEW.md CONTRIBUTING.md src/macos/README.md; do \
	  if git ls-files --error-unmatch "$$doc" >/dev/null 2>&1; then \
	    echo "  OK   $$doc"; \
	  else \
	    echo "  FAIL $$doc is not tracked by git, so AGENTS.md points at a"; \
	    echo "       document that a clean checkout does not have"; fail=1; \
	  fi; \
	done; \
	if [ "$$fail" -ne 0 ]; then echo "path check FAILED"; exit 1; fi; \
	echo "path check OK"

# The desktop stdio protocol version is declared once per language. Nothing
# links them, so bumping one and missing the others produces an app that
# refuses its own handshake at runtime while every test stays green — Go only
# ever agrees with itself. This target is the missing link.
protocol-sync: ## All three front ends declare the same protocol version
	@set -e; \
	go_v="$$(sed -n 's/^const ProtocolVersion = \([0-9]*\).*/\1/p' \
	        $(GO_DIR)/internal/buildinfo/buildinfo.go | head -1)"; \
	rs_v="$$(sed -n 's/.*PROTOCOL_VERSION: u32 = \([0-9]*\);.*/\1/p' \
	        $(TAURI_CORE)/src/protocol.rs | head -1)"; \
	sw_v="$$(sed -n 's/.*static let protocolVersion = \([0-9]*\).*/\1/p' \
	        $(MACOS_DIR)/AppServiceClient.swift | head -1)"; \
	echo "  go=$${go_v:-?} rust=$${rs_v:-?} swift=$${sw_v:-?}"; \
	fail=0; \
	if [ -z "$$go_v" ]; then echo "  FAIL could not read the Go version"; fail=1; fi; \
	if [ -z "$$rs_v" ]; then echo "  FAIL could not read the Rust version"; fail=1; fi; \
	if [ -z "$$sw_v" ]; then echo "  FAIL could not read the Swift version"; fail=1; fi; \
	if [ "$$fail" -eq 0 ] && [ "$$go_v" != "$$rs_v" ]; then \
	  echo "  FAIL rust ($$rs_v) disagrees with go ($$go_v)"; fail=1; fi; \
	if [ "$$fail" -eq 0 ] && [ "$$go_v" != "$$sw_v" ]; then \
	  echo "  FAIL swift ($$sw_v) disagrees with go ($$go_v)"; fail=1; fi; \
	if [ "$$fail" -ne 0 ]; then echo "protocol check FAILED"; exit 1; fi; \
	echo "protocol check OK ($$go_v)"

# Every cheap cross-language gate in one place. CI runs this so a broken
# assumption fails in seconds instead of as a confusing build error later.
gates: paths protocol-sync release-check ## Run every cross-language gate

# ── release gates ─────────────────────────────────────────────────────

release-check: go-toolchain ## Every version source agrees with VERSION
	cd $(GO_DIR) && $(GO) run ./cmd/anypick-release check

release-plan: go-toolchain ## Print the release the Conventional Commits imply
	cd $(GO_DIR) && $(GO) run ./cmd/anypick-release plan

release-set: go-toolchain ## make release-set VERSION=1.2.3
	cd $(GO_DIR) && $(GO) run ./cmd/anypick-release set $(VERSION)

release-bump: go-toolchain ## make release-bump PART=major|minor|patch
	cd $(GO_DIR) && $(GO) run ./cmd/anypick-release bump $(PART)

artifact-check: go-toolchain ## ADR 0020 §8: a built artifact contains no Node runtime
	cd $(GO_DIR) && $(GO) run ./cmd/anypick-artifact node-free $(ROOT)

# ── macOS app ─────────────────────────────────────────────────────────

app: ## Open the Xcode project
	open $(MACOS_DIR)/AnyPick.xcodeproj

app-build: ## Build the app unsigned; a build phase bundles the Go helper
	xcodebuild -project $(MACOS_DIR)/AnyPick.xcodeproj -scheme AnyPick \
	  -destination platform=macOS CODE_SIGNING_ALLOWED=NO build

app-test: ## Build and unit-test the app and the bundled helper contract
	xcodebuild -project $(MACOS_DIR)/AnyPick.xcodeproj -scheme AnyPick \
	  -destination platform=macOS CODE_SIGNING_ALLOWED=NO test

app-verify: app-build go-toolchain ## Build, then assert ADR 0020 on the built bundle
	cd $(GO_DIR) && $(GO) run ./cmd/anypick-artifact node-free \
	  "$$DERIVED_DATA_DIR/Build/Products/Debug/AnyPick.app"

# ── Tauri app (Linux / Windows) ───────────────────────────────────────

tauri-deps: ## Install the Tauri UI dependencies
	cd $(TAURI_DIR)/ui && npm install

tauri-ui: ## Build the UI bundle into src/tauri/frontend
	cd $(TAURI_DIR)/ui && npm run build

tauri-check: ## Typecheck the UI without emitting output
	cd $(TAURI_DIR)/ui && npm run typecheck

tauri-build: tauri-ui ## Build the packaged desktop app for this platform
	cd $(TAURI_CORE) && cargo tauri build

tauri-dev: ## Run the desktop app against the real Go service
	cd $(TAURI_DIR)/ui && npm run build
	cd $(TAURI_CORE) && cargo tauri dev

# ── housekeeping ──────────────────────────────────────────────────────

clean: ## Remove build output (keeps the module cache)
	rm -rf $(GO_DIR)/bin
