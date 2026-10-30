# Concurrency debugging

Run a focused reproduction repeatedly and, on a supported platform, `go test -race -count=10 ./path`. Read the race report’s two stacks and identify the missing ownership or synchronization edge; do not fix a race by adding arbitrary sleeps.

For a hang, capture `GOTRACEBACK=all` or a goroutine profile. Look for goroutines blocked on the same channel, lock, wait group, network call, or timer. Check whether a sender/closer exited early and whether cancellation reaches every blocking point.

For a leak, count goroutines before and after a bounded operation, close/cancel the owner, wait with a timeout, and inspect stacks. A leak detector is evidence only after filtering expected runtime goroutines.

In hot loops, `time.NewTimer` plus correct stop/drain/reset handling can reduce timer churn. `time.After` is correct for one-shot waits, and unreachable timers can be collected in modern Go; confirm allocation behavior with a profile.

For channel-based timers under Go 1.23+ synchronous semantics, Stop/Reset prevent stale receives; do not add an unconditional drain that can block. Older module/GODEBUG timer modes need the legacy stop/drain protocol with a single receiver. Check the active semantics, not just the installed toolchain: [time.Timer](https://pkg.go.dev/time#Timer).
