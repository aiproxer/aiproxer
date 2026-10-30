# Pipelines and worker pools

Build a pipeline only when stages have clear ownership and bounded backpressure. Each stage must stop when its input closes or its context is canceled, close its output exactly once, and release resources on every exit.

## Bounded workers with errors

`errgroup.WithContext` is usually simpler than a hand-rolled semaphore. The group context cancels siblings after the first error, and `SetLimit` bounds active functions:

```go
func ProcessAll(ctx context.Context, jobs []Job, limit int) error {
    if limit < 1 { return fmt.Errorf("limit must be positive") }
    g, ctx := errgroup.WithContext(ctx)
    g.SetLimit(limit)
    for _, job := range jobs {
        job := job
        g.Go(func() error {
            return process(ctx, job)
        })
    }
    return g.Wait()
}
```

If a semaphore is required, acquisition must honor cancellation and the process error must be returned:

```go
select {
case slots <- struct{}{}:
case <-ctx.Done():
    return ctx.Err()
}
defer func() { <-slots }()
if err := process(ctx, job); err != nil { return err }
```

Do not launch one goroutine per unbounded input. Bound queue size, workers, retries, and result storage. Decide whether to stop at the first error, collect all errors with `errors.Join`, or continue best-effort; encode that decision in tests.

## Fan-in and early exit

Fan-in must have one closer and a cancellation path for senders. If a consumer returns early, cancel the group before returning so producers cannot remain blocked. Preserve input ordering only when the contract needs it; otherwise include an index and reorder at the boundary.

## Verification

Test empty input, cancellation while waiting for a slot, worker failure, full output, producer failure, and shutdown. Run `go test -race` when supported and measure queue/worker limits under representative load.
