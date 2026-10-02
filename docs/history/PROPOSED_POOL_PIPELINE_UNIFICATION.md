# Pool pipeline unification — spike plan

**Goal (G0 remaining item):** the pool ring re-implements three egress leaves
(`geminiWithCapture`, `reverseWithCapture`, `translatedWithCapture` in
`src/go/internal/proxy/pool_server.go`) that duplicate the committing leaves in
`src/go/internal/proxy/upstream_serve.go` + `gemini_serve.go`. Fold each pair into
one implementation per egress surface, parameterized by a *sink* (commit vs
capture-for-failover) so the two paths cannot drift again.

## Current state (measured, not assumed)

- `classifyTranslatedFailure`, `translateToGemini`, `callGemini` are **already
  shared** — the leaves do not re-implement them.
- The context-window rewrite and the `Retry-After`-preserving capture (the two
  bugs the doc warns about) are **already fixed** in both copies. The duplication
  today is structural, not a live behavioral bug.
- The ONLY intended difference between a capture leaf and its committing twin is
  **where bytes go on an upstream failure**: the committing path writes to the
  client; the capture path holds the response for the ring to decide failover.
  On success, capture commits then writes the *same* bytes. On client errors
  (413/400/502 body errors) both terminate the request identically.

## Behavioral invariant that must hold (the regression test)

For each surface, drive a fake upstream and assert **byte-for-byte** equality
between the capture leaf and the committing leaf for:

1. success — capture commits, then emits identical bytes/headers;
2. client body errors (413/400) — identical terminal JSON;
3. non-quota upstream failure — captured verbatim *with upstream headers
   (incl. `Retry-After`)*, replayed exactly once by `flushCaptured`;
4. quota-class upstream failure — captured, then the ring decides failover.

Everything except (3)/(4) routing is identical by construction.

## Per-leaf plan

### Step 1 — `translatedWithCapture` ↔ `serveTranslatedMessagesWith` (LOW risk — do first)

Shared: `translateRequestBody` + `SanitizeOpenAIBody` + build `/chat/completions`
request + `injectUpstreamAuthFields`. Body is read by the caller (so
`ReadCappedBody` stays at the door and `TestPipelineBodyPolicy` stays green).

Extract `buildTranslatedChatRequest(r, up, body) (*http.Request, []byte, *translatedBuildError)`
returning a typed build error so each leaf maps it to its own sink (write vs
`commitJSON`). Each leaf keeps its own `ReadCappedBody` + error→sink mapping and
its own response handling (commit vs capture). **Zero behavior change.**
`TestPoolTranslatedMemberContextWindowIsConverted` must stay green.

### Step 2 — `geminiWithCapture` ↔ `serveGemini` (LOW risk)

`translateToGemini` + `callGemini` are already shared. Remaining duplication is
thin: success does `w.commit()` then `writeGeminiResponse`; network error maps
`geminiErrorStatusMessage` → `writeGeminiError` (commit path) vs
`w.capture(status, header, body)` (capture path). Extract
`geminiEgress(r, up) (resp, model, stream, *geminiBuildError)`; each leaf maps
success (commit + `writeGeminiResponse`) and failure (write vs capture).

### Step 3 — `reverseWithCapture` ↔ `serveOpenAIPath` / `serveNativeAnthropic` / `Serve` (DONE)

Unified into one `reverseEgress(r, up, sink, opts)` behind the `egressSink`
interface. The ambiguity (sink shape + SSE through both paths) is resolved:

```go
type egressSink interface {
    commit()
    reject(status int, header http.Header, body []byte)
    onNon2xx(resp *http.Response) error
    asResponseWriter() http.ResponseWriter
}
```

- `commitSink` (committing path) wraps `http.ResponseWriter`: `reject` writes the
  terminal error straight to the client; `onNon2xx` is a no-op so ReverseProxy
  streams the real non-2xx response through unchanged.
- `*captureWriter` satisfies `egressSink`: `reject` -> `capture` (held for the
  ring); `onNon2xx` captures then hands ReverseProxy an empty body so the captured
  bytes replay exactly once (never twice); `asResponseWriter` returns itself, so an
  SSE stream flows to the underlying client after `commit()` and is held before it.

Decision made: `reject` (pre-proxy terminal failure: body-too-large / auth fail)
and `onNon2xx` (captured upstream failure) are **two distinct methods**, because
the committing sink must WRITE on `reject` but NO-OP on `onNon2xx` (ReverseProxy
streams the real body). Body suppression is folded into `onNon2xx` (capture sink
nulls the body; committing sink leaves it). SSE verified for both sinks.

Bonus: `reverseEgress` now enforces the body cap at the reverse door, closing the
pre-existing gap where the generic passthrough streamed the body untouched. The
gate's `cappedDoors` was updated from `serveOpenAIPath` + `reverseWithCapture` to
`reverseEgress` (and mutation-checked). A differential test
`TestReverseEgressCaptureMatchesCommit` asserts byte-for-byte capture == commit
output on success and on a non-quota 400 with `Retry-After`.

## Test strategy

- **Must stay green:** `TestPool*` (failover, deadline, replay-exactly-once,
  quota pattern, context-window), `TestOneEgressDispatchTable`,
  `TestReadCappedBody`, `TestPipelineBodyPolicy`. **All green.**
- **Added:** `TestReverseEgressCaptureMatchesCommit` — capture output == commit
  output byte-for-byte on 200 and on a non-quota 400 (incl. `Retry-After`).

## Sequencing

Step 1 -> Step 2 -> Step 3, each landed independently with all gates green.
**All three are DONE.** G0 pool pipeline unification is complete: every capture
leaf now shares its committing twin's implementation through
`buildTranslatedChatRequest`, `geminiEgress`, and `reverseEgress`, each
parameterized by a sink so the two paths cannot drift again.
