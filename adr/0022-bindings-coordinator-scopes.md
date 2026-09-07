# ADR-0022: Binding mutations self-lock by resource scope (CONC-03)

- Status: accepted (2026-09-07)
- Amends: [ADR-0009](0009-mutation-coordinator.md)

## Context

`internal/bindings.Store` owns the `global_bindings`, `project_bindings`, and
`source_resumes` tables. Its methods already required `context.Context`, but
correctness still depended on every caller acquiring the right coordinator lock
first. That violated ADR-0009: a new CLI, TUI, or service caller could write
without serialization and the failure would look like an intermittent state
race.

Activation already holds client plus source scopes and uses re-entrant lock
propagation. Binding writes therefore need to acquire their own scopes while
remaining safe when reached from activation or from the existing CLI lock.

## Decision

1. `bindings.Store` carries the data `Root` and wraps every persisted mutation
   in `lock.WithMutationLock`.
2. Global binding mutations use `client/<clientID>`.
3. A project binding mutation, including delete-all and undo, uses
   `project/<absolute-project-root>`.
4. A source-resume mutation uses `refs.MutationScopeForRef(spec.Source)`, so
   account and pool resumes serialize with the provider's live credential scope,
   while gateways, hubs, and presets retain their exact resource scope.
5. Reads remain unlocked. The lock is a correctness property of writes, not a
   reason to serialize snapshot reads.
6. Activation carries `ProjectRoot` on `Plan` and acquires its project scope
   before executing a project binding step. Nested binding calls then re-enter
   the already-held scope through the same context.

## Consequences

- A binding write is safe even when a caller forgets to lock.
- Existing callers that already hold the scope remain correct and do not
  deadlock because lock identity is propagated through `context.Context`.
- Global/project/source-resume writes have explicit, reviewable scope policy.
- `Root` is now required when composing a binding store; tests use an isolated
  temporary root.

## Rejected alternatives

- **Keep caller locking:** repeats the exact ADR-0009 failure mode.
- **One global bindings lock:** serializes unrelated clients and projects.
- **Lock only around the SQL statement at callers:** compensation and nested
  reads could still interleave, and the store would remain unsafe as an API.
