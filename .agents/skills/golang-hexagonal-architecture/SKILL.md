---
name: golang-hexagonal-architecture
description: "Design and review Go systems with ports and adapters: explicit dependency direction, domain policy, application orchestration, infrastructure boundaries, and testable composition."
---

# Hexagonal architecture in Go

Hexagonal architecture is a dependency-direction discipline, not a directory template. Put stable policy at the center and volatile protocols, databases, queues, and SDKs at the edge. The center depends on small ports; adapters depend on the center's contracts.

## Map the system first

For a review, keep the work read-only unless fixes are requested. Establish the review base and separate introduced regressions from existing debt. Before changing code, identify:

- domain invariants and value types;
- application use cases and transaction boundaries;
- inbound adapters (HTTP, gRPC, CLI, jobs);
- outbound ports (repositories, clocks, publishers, providers);
- concrete adapters and the composition root;
- lifecycle and concurrency owners;
- error and cancellation contracts;
- tests that prove each boundary.

A package belongs in the center because of the policy it owns, not because its directory is named domain. A package belongs at an edge when it translates an external representation or owns external I/O.

## Dependency rules

A useful direction is:

~~~text
inbound adapter -> application/use case -> domain
outbound adapter -> port contract <- application/use case
composition root -> all concrete implementations
~~~

The core must not import database drivers, HTTP frameworks, generated transport code, or provider SDKs. Adapters translate their native errors and data into contracts; the core should not know how an adapter stores or transports them. Avoid pairwise protocol translators: normalize at a canonical boundary.

Define a port where its consumer needs it. Keep methods cohesive and no wider than the use case requires. Use a concrete dependency until substitution is demonstrated; a function type may be enough for a clock or callback. Do not create a repository interface that mirrors every database method when one use case needs one query.

## Use-case shape

Application services orchestrate policy and ports:

~~~go
type UserReader interface {
    Find(ctx context.Context, id string) (User, error)
}
type UserWriter interface {
    Save(ctx context.Context, user User) error
}

type RegisterUser struct {
    read UserReader
    write UserWriter
    now func() time.Time
}

func (uc RegisterUser) Execute(ctx context.Context, input Input) error {
    // validate input and domain invariants
    // call ports with ctx
    // translate expected conflicts; wrap unexpected failures
    return nil
}
~~~

Keep transport validation, protocol authentication, serialization, and status mapping in the inbound adapter. Preserve use-case authorization and domain invariants in application/domain policy so non-HTTP entrypoints enforce them too. Keep SQL, retries tied to a driver, and wire encoding in outbound adapters. If a transaction must cover several ports, expose a unit-of-work boundary owned by the adapter rather than leaking a transaction type into the domain.

## Composition and lifecycle

Construct the graph explicitly in one composition root. Validate configuration before starting servers. The component that creates a client, pool, subscription, or goroutine owns its shutdown and passes dependencies down. When reload replaces live resources, honor the existing lifecycle contract: validate replacements before activation and retire old resources only after their borrowers release them. Use generations/atomic handoff only when the demonstrated sharing and continuity requirements need them.

Avoid globals, hidden init registration, reflection registries, and containers that obscure ownership. A framework can live at the edge if it does not pull framework types into stable policy.

## Testing and review

Test domain rules with no I/O. Test application services with narrow fakes that model port errors, cancellation, and idempotency. Test adapters against the real protocol/driver when translation or transaction semantics matter. Add contract tests when multiple adapters implement one port.

For review findings, report the package/type, violated direction or responsibility, evidence from imports/callers/tests, caller-visible consequence, severity/confidence, and a minimal boundary change. Include executed checks, assumptions, and remaining coverage gaps. Do not demand extra layers, interfaces, or directories without a real variation or ownership problem.
