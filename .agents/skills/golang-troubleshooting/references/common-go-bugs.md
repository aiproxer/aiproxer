# Common Go failure patterns

## Nil and interfaces

An interface containing a typed nil pointer is non-nil. Nil maps panic on assignment; nil slices are append-safe. Check initialization and method-set assumptions at boundaries.

## Errors and cleanup

Look for ignored errors, `%v` where classification needs `%w`, deferred cleanup that masks a primary error, and log-plus-return duplication. Trace the owner that should classify, handle, and report the error.

## Slices, maps, and concurrency

Subslice aliasing and `append` can mutate retained data. Map variables share map storage; concurrent mutation needs synchronization. A goroutine blocked on a send, receive, lock, semaphore, or timer needs an owner and cancellation policy. Sort map keys when output must be deterministic.

## Time and context

Do not use sleeps as synchronization. A context derived from a request is canceled when the request ends; background work needs a service-owned queue and shutdown. `time.After` in a loop may cause churn, but Go 1.23+ can collect unreachable timers; confirm with profiles before calling it a leak.

## Filesystem and serialization

A lexical path-prefix check does not confine access against traversal or symlinks; use `os.Root` or a directory-scoped equivalent. For JSON, distinguish absent, null, empty, and zero when the API requires presence semantics. Validate sizes and encodings before allocation.

For each suspected bug, add the smallest failing test and inspect callers before changing the local code.
