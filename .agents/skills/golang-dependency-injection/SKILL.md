---
name: golang-dependency-injection
description: "Design and review Go dependency construction: consumer interfaces, typed nils, graph resolution, scopes, resource ownership, and DI library contracts."
---

# Dependency injection in Go

Dependency injection is explicit construction of a value's collaborators. It is useful when a component has a real substitution, lifecycle, or configuration boundary. It is not a reason to wrap every concrete type in an interface.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Map registrations and constructor calls to actual consumers. Follow missing/circular dependencies, constructor failure, partial initialization, lazy first use, and shutdown.
- Check typed-nil interface dependencies, ignored provider errors, override timing, and whether validation occurs before exposing a partially constructed service.
- Assign lifetimes and close authority to every resource; a borrowed dependency should not be closed by each consumer. Scope/clone operations may share external resources.
- Use a new seam only when a demonstrated variation, test contract, or ownership problem justifies it. Inspect the resolved graph rather than assuming generics make it compile-time safe.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Default: compose explicitly

Prefer constructors that validate required dependencies and return a concrete type:

~~~go
type Store interface {
    Find(ctx context.Context, id string) (User, error)
}

type Service struct {
    store Store
    clock func() time.Time
}

func NewService(store Store, clock func() time.Time) (*Service, error) {
    if store == nil || clock == nil {
        return nil, errors.New("service dependencies are required")
    }
    return &Service{store: store, clock: clock}, nil
}
~~~

The `store == nil` check rejects a nil interface, not an interface holding a typed nil pointer. Define the dependency’s nil-receiver policy and validate concrete providers before boxing them; avoid generic reflection-based nil detection.

Define a small interface at the consuming package when substitution is needed. Accept a concrete type when its behavior is stable and there is no useful alternative. Return a concrete type unless an interface is intentionally the public boundary. Function values are often the clearest one-operation seam.

Keep dependency construction in the composition root. Pass a request context to operations; never store a context in a long-lived dependency. A singleton is a lifetime decision, not a DI feature. The owner of a connection, worker, or client must also own its close/stop operation. Avoid service locators, mutable globals, hidden init registration, and containers threaded through business functions.

## Choosing a library

- Manual construction: default for small and medium graphs, easiest to debug and compile.
- google/wire: the upstream repository is archived/read-only; do not describe it as a current default. Existing code may still use generated providers, but verify the pinned version and migration plan.
- uber-go/dig: runtime reflection graph; useful for large application composition, but missing providers and type mismatches surface during container construction/invocation.
- uber-go/fx: lifecycle-oriented application framework built around dig; adopt only when its module and lifecycle conventions are worth the coupling.

Review the actual module version before writing snippets. Do not compare libraries using unsupported claims about speed, safety, or lifecycle semantics.

## Testing and review

Manual constructors are usually tested with fakes. For a container, create a fresh root per test, register overrides before invoking the service, assert both resolution and lifecycle errors, and shut it down with a bounded context. Never share a mutable global injector between tests.

For a refactor, map concrete dependencies, globals, init side effects, lifetimes, and callers first. Introduce one seam at a time and keep behavior tests green. Verify race-sensitive lifecycle code with the target platform's race gate where available.
