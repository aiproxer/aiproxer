# pprof workflow

Expose profiles only on an authenticated, access-controlled diagnostic listener. Capture a bounded sample from a representative workload with `go tool pprof` against the protected CPU, heap, or goroutine endpoint.

Use `top`, `list`, `web`, and `-http` to inspect callers and cumulative cost. Compare profiles from the same revision, workload, duration, and runtime settings. Heap `inuse_*` shows live objects at capture; `alloc_*` shows cumulative allocation work. Neither alone proves a memory leak.

Record profile time and workload. Remove or protect debug endpoints after investigation and avoid logging profile URLs or credentials.
