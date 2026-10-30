# Test helpers

Mark helpers with `t.Helper()` so failures point at the call site. Use `t.Cleanup()` for owned resources; cleanup runs in last-in, first-out order.

```go
func eventually(t *testing.T, timeout time.Duration, check func() bool) {
    t.Helper()
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        if check() { return }
        time.Sleep(10 * time.Millisecond)
    }
    t.Fatal("condition was not met before deadline")
}
```

Prefer synchronization, injected clocks, or `testing/synctest` for deterministic code over polling. When polling is unavoidable, use a bounded deadline and include useful state in the failure.
