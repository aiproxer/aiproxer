---
name: golang-concurrency
description: "Design and review concurrent Go code involving goroutines, channels, select, mutexes, atomics, worker pools, errgroup, singleflight, cancellation, and fan-out/fan-in. Use when preventing races, deadlocks, leaks, lost errors, or unbounded work."
---

# Go concurrency

Make concurrency structured: every goroutine has an owner, an exit condition, a bounded resource budget, and a defined error path. Concurrency is not automatically faster; establish the sequential baseline and measure contention or throughput before optimizing.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Map shared state, synchronization, goroutine owners, close authority, and happens-before edges. Walk success, sibling failure, caller cancellation, and shutdown schedules.
- Check cancellation while admitting work as well as during execution: errgroup.SetLimit can block Go; recursive admission can deadlock. Trace lock order and calls made while holding locks.
- Inspect select behavior for closed/nil channels, default-case spinning, and simultaneous cancellation/readiness. Cancellation selection is not a priority guarantee.
- Check atomic operations preserve whole invariants, pooled objects are no longer aliased at Put, and singleflight callers do not incorrectly cancel work shared by others.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## First questions

1. What state is shared, and who owns mutation?
2. Who starts and stops each goroutine? What happens when the caller is canceled or a sibling fails?
3. Can sends, receives, semaphore acquisition, and result delivery block forever?
4. Which error wins, and how are sibling errors and cleanup errors reported?
5. Are buffers, queues, retries, and fan-out bounded?

Prefer a mutex for shared state with short critical sections. Prefer a channel when the operation naturally transfers work or ownership. Use typed `sync/atomic` values for small independent counters/flags. Concurrent read-only map access is safe; any concurrent mutation requires synchronization. `sync.Map` is a specialized choice, not a default replacement for a typed map plus mutex.

## Ownership rules

- The sender normally closes a channel, and only when it owns the decision that no more values can be sent. A receiver should not close a channel it does not own.
- `chan<-` and `<-chan` in function signatures make direction explicit.
- Sending a pointer is valid when the pointed-to object is immutable or ownership is transferred. Copies are not universally safer; document the ownership rule.
- A `select` needs a cancellation or shutdown case only when the operation can otherwise outlive its owner. Do not add a `ctx.Done()` case that silently abandons required work.
- Every acquired resource (semaphore slot, lock, buffer, connection) has a release path on success, error, and cancellation.

## Choosing primitives

| Need | Default | Watch for |
| --- | --- | --- |
| Wait for work with no error result | `sync.WaitGroup` | Positive Add/Go on an empty group precedes Wait; adding while nonempty is allowed by contract. WaitGroup.Go (Go 1.25+) owns Done and its callback must not panic |
| Collect errors and cancel siblings | `errgroup.WithContext` | Return the group error; apply `SetLimit` for bounded concurrency |
| Deduplicate an expensive concurrent call | `singleflight.Group` | Shares an in-flight result, not a persistent cache; define waiter cancellation and retry policy |
| Protect fields/invariants | `sync.Mutex` or `sync.RWMutex` | Never hold a lock across slow or re-entrant calls without a reason |
| One-time result, including an error | `sync.OnceValues` or explicit state | Cache failure intentionally; do not hide initialization errors |
| Temporary reusable buffers | `sync.Pool` | Reset before `Put`; it is a performance hint, not storage |

`sync/atomic` promises atomic operations and ordering semantics; do not infer a particular lock-free implementation from the API. Typed atomics are easier to review than untyped operations.

## Cancellation and timers

Pass context from the owning request or service. A worker that may block on input, output, a semaphore, or a timer should select on cancellation where abandoning the operation is valid. Use `time.NewTimer` and stop/drain/reset it in long-running loops to reduce allocation churn; `time.After` is still correct for one-shot waits and is not a universal memory leak.

For background work that must outlive a request, submit to a bounded queue owned by a service with a shutdown context and wait for completion. `context.WithoutCancel` preserves values but removes cancellation; never use it as a complete lifecycle plan.

## Verification

Write tests for cancellation, full/closed channels, first-error behavior, queue limits, and shutdown. Run focused tests; in this repository never run `go test -race` locally (scheduled remote scans own race coverage, see `AGENTS.md` Verification). For leak-sensitive code, use explicit shutdown plus a bounded wait; a goroutine-count check is evidence, not a proof. See [channel patterns](references/channels-and-select.md), [pipelines](references/pipelines.md), and [synchronization](references/sync-primitives.md).
