---
name: golang-design-patterns
description: "Design and review idiomatic Go construction, composition, options, state, retry, and lifecycle patterns against demonstrated invariants."
---

# Go design patterns

Patterns are trade-offs, not mandatory shapes. Start from the invariant, ownership, and demonstrated variation in the codebase. Prefer direct composition and a small amount of duplication over a speculative framework.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Name the invariant and demonstrated variation before recommending a pattern. Compare the proposed mechanism with direct code and count the states, branches, and ownership decisions it introduces.
- Check option validation/order, mutable captured configuration, constructors bypassed by zero values, and enum/state transitions at all public entrypoints.
- Trace retry eligibility, idempotency, partial downstream results, bounded delay, context cancellation, and side effects across attempts.
- Walk partial construction and explicit Close before runtime cleanup. Check wrappers/embedding preserve methods, errors, aliases, blocking, and serialization contracts.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Construction and composition

Use a constructor when a value needs validation, collaborators, or a non-obvious invariant. A zero value may be the best API when it is useful and safe. Return a concrete type by default; expose an interface only at a substitution boundary. Define interfaces near consumers and keep them small.

Functional options are useful when callers need a stable constructor with several optional, independent settings:

~~~go
type Option func(*Client) error

func WithTimeout(d time.Duration) Option {
    return func(c *Client) error {
        if d < 0 { return errors.New("negative timeout") }
        c.timeout = d
        return nil
    }
}
~~~

Use an options struct when fields are related and the configuration is naturally inspectable. Ensure option application order is either irrelevant or documented.

Composition is Go's inheritance alternative. Embed only when promoted behavior and substitutability are intentional; otherwise use named fields. A type assertion should be rare and should handle failure.

## Boundaries and data flow

Keep domain policy independent of transport, persistence, and vendor SDKs. Put adapters at package boundaries and translate wire types into canonical domain values. A closed enum or state machine may use a switch; do not invent a plugin registry for one fixed set.

Keep mutation at the owner boundary. Copy caller-owned maps/slices when retaining them, or document immutability. Use typed errors or wrapping for machine-readable classification:

~~~go
if err != nil {
    return fmt.Errorf("load profile: %w", err)
}
~~~

A formatted string is not an API security boundary. Public handlers should map internal errors to a deliberately safe wire contract while preserving errors.Is/As internally.

## Context, retry, and lifecycle

Pass context.Context through I/O and cancellation-aware operations. Do not store it. Retry only transient, idempotent operations; bound attempts and delay, use a timer/select on ctx.Done, and return the last error with context when canceled. Never retry after downstream content or a non-idempotent side effect without an explicit protocol.

A component that starts a goroutine, ticker, file watcher, server, or subscription owns its stop path. Prefer Close/Shutdown methods with idempotent behavior and a bounded context. A finalizer or runtime cleanup hook is not a deterministic replacement for Close. runtime.AddCleanup can handle unreachable objects in cycles, but provides neither prompt execution nor execution before process exit. The cleanup closure and argument must not retain the target. Keep explicit Close primary and coordinate fallback cleanup so it cannot double-release a resource.

Avoid init for business wiring. Package initialization follows dependency rules, but relying on source-file order or side effects across files is brittle. Construct dependencies in an explicit composition root.

## Review checklist

For a pattern change, ask:

- What invariant does the pattern make easier to preserve?
- What variation is real today?
- Which package owns construction, state, cleanup, and error translation?
- Can a function value or concrete type replace an interface?
- Are aliases, retries, blocking, cancellation, and nil semantics documented?
- Is the change observable in focused tests?

Useful patterns include consumer-owned interfaces, functional options, adapter/port boundaries, explicit state machines, immutable value objects, and bounded worker ownership. Avoid singleton globals, service locators, hidden registries, generic “utils” packages, and patterns copied solely from another language.
