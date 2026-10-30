# Channels and `select`

## Ownership

The component that knows no more values can be sent normally closes the channel. Pass direction in signatures and keep ownership in the type that created the channel. A channel value can be copied; that copies a handle, not the queue or the ownership decision.

```go
func producer(ctx context.Context) <-chan Item {
    out := make(chan Item)
    go func() {
        defer close(out)
        for _, item := range loadItems() {
            select {
            case out <- item:
            case <-ctx.Done():
                return
            }
        }
    }()
    return out
}
```

Never assume a send is consumed. A receiver that exits early must cancel the producer or drain the channel. A buffered channel is a bounded queue only when its capacity and full-queue policy are explicit.

## Select patterns

Use a cancellation case when the operation may be abandoned:

```go
select {
case item, ok := <-in:
    if !ok { return nil }
    return process(item)
case <-ctx.Done():
    return ctx.Err()
}
```

Do not use `default` in a loop as a substitute for a blocking policy; it can spin. If a timeout is needed once, `time.After` is straightforward. In a hot or long-lived loop, reuse a timer when measurement warrants it; use the active Go/module timer semantics described below for Stop/Reset.

## Common failure modes

- Sending on a channel after a receiver has closed it panics.
- A `for range` over a channel ends only after close; cancellation must be part of the protocol when close is not guaranteed.
- A nil channel blocks forever on send and receive and disables a select case; use it deliberately to enable/disable a case.
- A send of a mutable pointer is safe only with documented immutability or ownership transfer.

For channel-based timers under Go 1.23+ synchronous semantics, Stop/Reset prevent stale receives; do not add an unconditional drain that can block. Older module/GODEBUG timer modes need the legacy stop/drain protocol with a single receiver. Check the active semantics, not just the installed toolchain: [time.Timer](https://pkg.go.dev/time#Timer).
