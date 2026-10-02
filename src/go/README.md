# AnyPick — Go port (`src/go/`)

Go implementation of AnyPick. This is the engine: CLI, terminal UI and desktop
service all run here, over one bundled Go binary (ADR 0020).

Standalone module `github.com/outof0/anypick/go`, Go 1.26+, `modernc.org/sqlite`
(CGO-free). The terminal UI (`internal/tui`, bubbletea) replaced the Ink/React
tree that used to live in `src/tui`; native releases contain no Node runtime, so
a JavaScript UI cannot be the one users get.

## Layout

```
src/go/
  cmd/anypick/       CLI entry (thin handlers): use/run/current/list/link/unlink/reset,
                     add, account, gateway, preset, proxy (+pool/hub), tui, tray(app),
                     serve, doctor/update/completion/providers/clients,
                     hidden _serve-proxy/_serve-hub/_serve-pool children
  internal/
    tui/             Terminal UI (bubbletea): provider home, accounts, detail,
                     switch/save/proxy/delete. Calls services directly.
    app/             Composition root: createAppReady lifecycle (.migrate.lock,
                     recovery, lease reap, idempotent close)
    db/              SQLite open + WAL + migrations (`user_version` guard)
    store/           Account snapshots + proxy state (writeMeta is the commit point)
    lock/            Scoped, sorted, re-entrant mutation locks (ADR 0009/0011)
    activation/      Planner → Executor + OperationJournal (rollback/recovery)
    proxy/           Loopback-only per-account proxy with per-instance secret (ADR 0006)
    protocol/        OpenAI/Anthropic wire translation + SSE
    hub/             Proxy Hub store/server (token-scoped routes, HMAC'd secrets)
    pools/           Provider pool store
    transfer/        Account import/export envelopes + multi-account backups (SEC-01)
    client/          Client adapters: claude, codex, gemini, kiro, opencode,
                     zcode, dsh, commandcode, cline
                     (workbuddy.go holds the WorkBuddy models.json adapter:
                     the app uses its own login directly and switches
                     accounts natively via its session file)
    provider/        Provider adapters incl. compatible vendors
    catalog/         Vendor metadata, model policies, live model fetch
    discovery/       Model list fallback chain: fresh cache → live → stale → catalog
    modelcache/      Cached vendor model lists
    desktop/         Desktop-app stdio core (snapshots + commands); the protocol
                     version is synchronized across Go, Rust and Swift
    refs/ anyerr/ paths/ …   Resource refs, structured errors, data-root paths
  testdata/parity/   Cross-runtime contract fixtures
```

## Commands

```bash
cd src/go
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
go build -o /tmp/anypick ./cmd/anypick
./scripts/smoke.sh     # end-to-end smoke in a sandboxed HOME/ANYPICK_HOME
```

`GOPATH` must not point at another project's module cache. If `GOMODCACHE` is not
writable, tests that shell out to `go build` (e.g. `cmd/anypick`'s
`requireBuiltCLI`) stall in module resolution and the package times out after
many minutes. A writable cache turns the same suite into ~2 minutes:

```bash
GOPATH=$HOME/go go test ./... -count=1     # warm once: go mod download all
```

CI runs vet, staticcheck and the tests on Ubuntu and Windows
(`.github/workflows/ci.yml`, jobs `go-linux` and `go-windows`). `go vet` must
stay clean; `-count=1` avoids cache masking flaky tests.

## Invariants (AGENTS.md)

- Proxy secrets never cross an output boundary — only endpoints are printed.
- Activation is planned, journalled, then executed; the journal drives rollback.
- Every persisted mutation runs under the scoped lock coordinator.
- Capability pairings come from the adapters in `internal/transport`; registry
  membership never grants one.
- Data root is `~/.anypick` (`ANYPICK_HOME` overrides).

The Go↔TypeScript port trackers (`PARITY_PLAN.md`, `PARITY.md`) were retired
with the native cutover: the TypeScript tree they compared against is gone
(ADR 0020), so there is nothing left to compare, and a tracker that cannot be
updated is a document that starts lying. The recorded state lives in git
history (`git log --diff-filter=D -- src/go/PARITY.md`).
