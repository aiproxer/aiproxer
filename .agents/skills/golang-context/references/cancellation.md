# Cancellation, deadlines, and bounded background work

## Derive ownership

```go
ctx, cancel := context.WithTimeout(parent, 2*time.Second)
defer cancel()

if err := client.Do(ctx); err != nil {
    if errors.Is(err, context.DeadlineExceeded) { ... }
    return err
}
```

Call `cancel` on every path. A child cannot extend its parent deadline. Put a service-level budget at the boundary that owns the operation and pass the derived context down; avoid unrelated per-layer timeouts that make behavior unpredictable.

At a blocking point, select on `ctx.Done()` when abandoning work is valid. If an operation must finish after cancellation, make that policy explicit and bound its duration and queueing.

## `WithoutCancel`

`context.WithoutCancel(parent)` (Go 1.21+) retains values but has no deadline, no `Done` channel, and no cancellation error. It is useful only for copying carefully selected metadata into work that has another owner. It is not a worker lifecycle or shutdown plan.

```go
// Queue owns shutdown and applies its own bounded deadline.
metadataCtx := context.WithoutCancel(requestCtx)
queue.Submit(Work{Trace: TraceFrom(metadataCtx), Payload: payload})
```

Prefer extracting typed values into a job and running the job with the queue’s service context. If preserving context values is unavoidable, derive a fresh bounded context from a worker root and wait for the queue during shutdown. Never launch an untracked goroutine with `WithoutCancel` from a request handler.

## Causes and cleanup

Use `WithCancelCause` when the owner needs to distinguish shutdown from a failed sibling, and inspect `context.Cause`. Return the context error (possibly wrapped with `%w`) so callers can use `errors.Is`. Ensure timers, child goroutines, response bodies, and locks have cleanup paths when cancellation interrupts the operation.
