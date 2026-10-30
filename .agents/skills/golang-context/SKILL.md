---
name: golang-context
description: "Use Go context.Context correctly across API boundaries: propagation, cancellation, deadlines, timeouts, values, HTTP handlers, and bounded background work. Use when designing or reviewing context flow, request lifetimes, leaked work, or trace metadata."
---

# Go context

Treat a context as a cancellation/deadline signal plus narrowly scoped request metadata. It is not a general parameter bag, configuration object, or replacement for explicit dependencies.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Trace context lineage from each entrypoint to every blocking call, retry, queue, and cleanup operation. Identify any replacement with Background/TODO/WithoutCancel and justify its lifetime.
- Compare deadline budgets across dependent calls; derived deadlines cannot extend a parent. Check cancel functions execute promptly, especially in loops.
- Cancellation signals intent; verify the operation actually stops and that owned goroutines are joined. Required shutdown may need a fresh bounded context after the request expires.
- Inspect value keys, retained objects, identity provenance, cancellation causes, and AfterFunc races. stop does not wait for an already-started callback to finish.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Propagation

- Accept `context.Context` as the first parameter of operations that can block or need cancellation. Do not store it in a struct.
- Pass the caller’s context to downstream I/O, database, HTTP, RPC, and child operations. Do not replace it with `context.Background()` merely to make a call compile.
- Use `context.Background()` at a true process/root boundary and `context.TODO()` only while wiring an unresolved boundary.
- Derive a context for ownership: `WithCancel` for shutdown, `WithTimeout`/`WithDeadline` for a bounded operation, and `WithCancelCause` when the cause is useful. Call the returned cancel function on every path.
- Check `ctx.Err()` or select on `ctx.Done()` at blocking points. A function may finish a small atomic operation after cancellation; define that behavior instead of sprinkling checks mechanically.

```go
func Fetch(ctx context.Context, client *http.Client, url string) ([]byte, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    if err != nil {
        return nil, fmt.Errorf("build request: %w", err)
    }
    resp, err := client.Do(req)
    if err != nil {
        return nil, fmt.Errorf("fetch %q: %w", url, err)
    }
    defer resp.Body.Close()
    return io.ReadAll(resp.Body)
}
```

## Deadlines and background work

Set a deadline at the boundary that owns the service-level budget; avoid stacking arbitrary short timeouts in every layer. A child deadline can only shorten the parent budget. Return cancellation and deadline errors in a way callers can classify with `errors.Is`.

Work that must survive request cancellation needs an explicit owner: a bounded queue, worker/service context, shutdown signal, and wait/flush policy. `context.WithoutCancel` (Go 1.21+) preserves values but intentionally has no deadline and no `Done` channel. If used to copy metadata into queued work, wrap it in a new bounded context owned by the queue; never launch an untracked goroutine from a handler.

## Values

Use an unexported comparable key type and typed accessors. Store request IDs, authentication claims, or trace metadata only when they are request-scoped and safe to copy. Never use context values for required function inputs, optional behavior flags that change API semantics, loggers that should be explicit dependencies, or large mutable objects.

Avoid retaining a request context or its values in caches, goroutines, or long-lived objects. Redact secrets and personal data before putting values into logs or traces.

See [cancellation and deadlines](references/cancellation.md), [HTTP services](references/http-services.md), and [values and tracing](references/values-tracing.md).
