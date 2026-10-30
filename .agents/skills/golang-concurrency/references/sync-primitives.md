# Synchronization primitives

## Mutexes and atomics

Use `sync.Mutex` for invariants spanning multiple fields. `sync.RWMutex` helps only when read concurrency dominates and profiling supports it; never upgrade an `RLock` to `Lock`. Keep critical sections short and do not call unknown/re-entrant code while holding a lock.

Use typed atomics (`atomic.Int64`, `atomic.Bool`, and related types) for independent values with a clear memory-ordering requirement. The `sync/atomic` API guarantees atomic operations and ordering semantics; it does not promise a lock-free implementation. Do not mix atomic and ordinary access to the same variable.

## Lifetime and initialization

`sync.WaitGroup` waits; it does not collect errors or cancel work. Ensure `Add`/`Go` happens before the corresponding wait and every path calls `Done`. On Go versions that provide `WaitGroup.Go`, use it only for functions whose panic/error behavior fits that API; use `errgroup` when errors or cancellation matter.

Use `sync.OnceValues` for initialization that returns values and errors. If a failed initialization may be retried, use explicit state and locking instead of caching the failure accidentally.

`sync.Pool` may drop values at any time. Store only temporary, reusable objects; reset them before putting them back and never use the pool as a cache or ownership mechanism.

## Specialized choices

`sync.Map` is useful for patterns such as write-once/read-many or disjoint keyed updates. A typed map guarded by a mutex is easier to reason about for most state. `singleflight.Group` deduplicates concurrent calls but does not persist results; pair it with a cache when persistence is intended.

For every primitive, document the protected state, ownership, zero-value policy, and shutdown behavior. Test under the race detector on a supported platform.
