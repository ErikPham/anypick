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
    client/          Client adapters: claude, codex, gemini, kiro, opencode
    provider/        Provider adapters incl. compatible vendors
    catalog/         Vendor metadata, model policies, live model fetch
    discovery/       Model list fallback chain: fresh cache → live → stale → catalog
    modelcache/      Cached vendor model lists
    src/tauri/         Desktop-app stdio core (snapshots + commands), TS protocol compatible
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

CI runs `go vet` + `go test` on every PR (`.github/workflows/ci.yml` `go` job).
`go vet` must stay clean; `-count=1` avoids cache masking flaky tests.

## Invariants (same as TS)

- Proxy secrets never cross an output boundary — only endpoints are printed.
- Activation is planned, journalled, then executed; the journal drives rollback.
- Every persisted mutation runs under the scoped lock coordinator.
- Capability pairings follow `transportFor`; registry membership never grants it.
- Data root is `~/.anypick` (`ANYPICK_HOME` overrides).

See `PARITY_PLAN.md` and `PARITY.md` for the parity contract and its status.
