# Routing and Orchestration (Steering)

## Core Ownership Boundary

Core owns provider-neutral execution semantics:

- selector parsing and route-plan construction;
- model alias expansion and candidate resolution;
- health/exclusion filtering and attempt budgets;
- ordered failover, weighted choice, affinity, races, and time budgets;
- A-leg routing-override authority and turn-level snapshotting;
- B2BUA attempt sequencing, lineage, output commitment, and pre-output recovery;
- cancellation/terminal ownership and provider-neutral evidence flow;
- core execution-stage authority and control-plane projections.

Plugins may contribute policy inputs or optional behavior through SDK contracts. They do not become alternate owners of route planning, B2BUA sequencing, commitment, or retry/failover policy.

The parser and its tests are the source of truth for concrete selector syntax. Steering records semantics, not a token-by-token grammar copy.

## Routing Semantics

Regardless of concrete selector syntax, these rules are invariant:

- **Planning precedes execution**: aliases, overrides, capability checks, eligibility, and candidate shaping are resolved before opening a provider leg where possible.
- **Attempt-local state stays attempt-local**: candidate-specific capability downgrades, provider identity, timeouts, and evidence must not leak into sibling attempts.
- **Affinity is advisory policy, not hidden authority**: it may influence candidate choice but must not bypass capability/security/health rejection.
- **Runtime overrides are A-leg scoped**: changes affect later admitted turns, never mutate an in-flight turn or B-leg, and must be snapshotted before planning.
- **Reload is generation-based**: new configuration affects new admissions through a newly published immutable generation; in-flight turns keep their admitted generation/state.

## Output Commitment and Recovery

1. **Recovery is pre-output only** — transparent failover, retry, or race substitution is allowed only before client-visible canonical output commits an attempt.
2. **First visible content commits** — once an attempt has emitted client-visible content, later failure is terminal for that attempt; completed effects are not replayed.
3. **Every attempt is attributable** — each logical client turn and backend attempt is tracked with lineage/evidence according to the configured continuity mode; persistence durability is a store/topology choice, not part of the attribution invariant.
4. **Race losers terminate cleanly** — losing or canceled attempts are canceled, drained only as needed for bounded terminal evidence, and terminalized exactly once.
5. **Provider-only evidence may survive cancellation** — bounded secret-safe terminal evidence can still feed accounting/diagnostics even when the attempt never commits output.

## Attempt Publication and Terminal Ownership

- Attempt publication is gated: an attempt is not visible to downstream reducers until its required readiness/initialization contract is complete.
- There is one physical terminal owner for an attempt. All teardown, observers, authority settlement, metering, lineage, billing evidence, and local state converge through that at-most-once terminal path.
- A-leg cancellation versus provider activation must be linearizable. Cancellation cannot leave an unowned provider stream or publish a half-initialized attempt.
- Transitional rollback/abort paths should not create a second terminal protocol.

## Generation Compilation and Publication

A runtime generation is an immutable request plane. Candidate compilation/validation and active publication are distinct phases.

- **Compilation must be side-effect isolated**: compiling or validating a candidate generation must not mutate active process feature state or externally visible generation state.
- **Candidate-owned resources use explicit ownership**: resources acquired during compilation belong to the candidate resource ledger and are released on rejection/rollback.
- **Publication-only effects run after publication**: any action that changes process-visible behavior because a generation became active must be registered for the publication phase and execute only after the generation is the active published request plane.
- **Rejected candidates are observationally inert**: a candidate that never publishes must not retarget process metrics, replace active services, or otherwise affect serving behavior.
- **Retirement is manager-owned**: superseded generations drain existing users and close through the generation lifecycle; request paths do not implement ad-hoc generation cleanup.

These rules apply to feature-host output, metrics projections, background workers, reload, and future generation-bound facilities.

## Core vs Feature Policy

Use the kernel/policy test when deciding ownership:

- If logic is required with all optional product features disabled and is necessary to coordinate execution safely, it may be core.
- If logic is optional UX, maintenance, reasoning shaping, safety policy, actor policy, or other feature behavior, the feature owns it and core consumes only a narrow typed port/plane.
- Pure projection/state-machine kernels over canonical facts may stay core when multiple features/protocols rely on the same invariant; mutable feature state and feature-specific policy stay outside core.

No request-time service locator or concrete-feature switch belongs in core/runtime composition.

## Optional attachment boundary

Routing/execution and the money implementation cooperate through narrow lifecycle seams:

- a cheap financial eligibility screen may occur before expensive route expansion;
- route/quote evaluation remains side-effect-free;
- operational exposure is admitted atomically before upstream execution;
- terminal ownership appends immutable per-attempt/call usage evidence;
- customer settlement and provider cost posting happen after usage and are not stream-receive responsibilities.

Those behaviours reach the runtime only through explicitly attached ports, and an unattached port keeps its no-op default. The money implementation and the usage-authority implementation live in the Enterprise tree; this tree keeps only the neutral usage-limit port and the provider quota window, and the stock composition attaches neither, so usage limits are enforced only through an injected port. The attachment ports do not mean the rules are gone: an attached Enterprise implementation brings them. Stream handlers must not rate prices, post journal entries, mutate balances/exposure, or use the legacy token ledger as monetary truth.

## Continuity and Persistence

Continuity implementations may be in-memory or durable, but semantics must remain backend-independent:

- A-leg/B-leg identity and ordering are authoritative product concepts, not database-row accidents.
- Persistence adapters must preserve the same contract across supported database engines.
- Pooler/topology restrictions belong to persistence/infrastructure policy, not routing logic.
- Feature-owned durable state must not turn the core continuity store into a generic feature database.

## Change Procedure

For routing/orchestration changes:

1. Identify whether the change affects parsing, planning, attempt lifecycle, commitment, cancellation, generation lifecycle, or optional feature policy.
2. Preserve single ownership: do not solve a feature problem by adding a second planner/terminal path.
3. Write focused characterization/regression tests before rewiring lifecycle code.
4. Exercise cancellation/race cases when concurrency changes.
5. Verify no retry/failover can occur after commitment.
6. Verify candidate-generation failure leaves active process/generation state unchanged when composition changes.
7. Run architecture and relevant contract/parity gates.

Concrete selector operators, feature names, provider inventories, and store implementations should be looked up in their executable packages/tests rather than duplicated here.
