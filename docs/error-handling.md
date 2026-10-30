# Error handling policy

Short rules for AIProxer: prefer **stdlib `errors` + small typed domain errors** in contracts; keep **rich context in logs/structured fields**, not in user-facing or high-cardinality error text.

## Contracts (`pkg/aipapi`, `pkg/aipsdk`)

- Use `errors.New`, `fmt.Errorf` with **stable, low-cardinality** messages, and **typed** sentinel or struct errors for behavior the core or plugins must branch on.
- **Do not** add third-party error libraries to public contract packages. Keep them **stdlib-first**.

## Crossing package boundaries

- **Wrap** with `%w` when the caller needs `errors.Is` / `errors.As` or a preserved cause chain. Same package or thin internal helper: plain `return err` is fine when you are not discarding classification.
- At **major boundaries** (e.g. plugin → core, `internal/core` → frontends, HTTP handlers), **do not** use a bare `return err` as the *only* signal: attach a stable top-level message (or a typed/known sentinel) and wrap the inner error so operations/logging can still unwrap.

## Message shape

- **Stable, template-like** top-level messages for categories operators and monitors aggregate (e.g. `upstream request failed`, not an interpolated upstream body snippet).
- **Variable data** (IDs, model names, selector fragments, request excerpts): log with `slog` (or other structured fields), or attach as typed fields on a domain error if tests/assertions need it—not concatenated into the primary error string for every failure.

## Sentinels, typed errors, and wrapping

| Use | When |
| --- | --- |
| **Sentinel** (`var ErrX = errors.New(...)`) | Single fixed fact; identity with `errors.Is` is enough. |
| **Typed error** (struct with fields / methods) | Callers need fields, `errors.As`, or multiple instances of the same class with different data. |
| **Wrapped** (`fmt.Errorf("...: %w", err)`) | Propagate a cause, preserve `Is`/`As` through layers. |

Pick one **primary** classification at each boundary: avoid deep stacks of indistinguishable string wrapping.

## See also

- [Execute error classification (frontends)](execerr-classification.md) — how HTTP frontends map executor failures to wire-safe outcomes.
