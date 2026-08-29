# AnyPick — Go port (`go/`)

Go implementation of the AnyPick core. Mirrors the TypeScript layer in
`src/` as a standalone module (`github.com/outof0/anypick/go`, Go 1.23+,
`modernc.org/sqlite`). The terminal UI intentionally ships only with the
TypeScript build.

## Layout

```
go/
  cmd/anypick/       CLI entry (thin handlers): use/run/current/list/link/unlink/reset,
                     add, account, gateway, preset, proxy (+pool/hub), tray(app),
                     serve, doctor/update/completion/providers/clients,
                     hidden _serve-proxy/_serve-hub/_serve-pool children
  internal/
    app/             Composition root: createAppReady lifecycle (.migrate.lock,
                     legacy .hotplug migration, recovery, lease reap, idempotent close)
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
    desktop/         Desktop-app stdio core (snapshots + commands), TS protocol compatible
    refs/ anyerr/ paths/ …   Resource refs, structured errors, data-root paths
  testdata/parity/   Cross-runtime contract fixtures
```

## Commands

```bash
cd go
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
go build -o /tmp/anypick ./cmd/anypick
./scripts/smoke.sh     # end-to-end smoke in a sandboxed HOME/ANYPICK_HOME
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
