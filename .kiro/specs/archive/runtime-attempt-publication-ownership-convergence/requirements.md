# Requirements Document

## Introduction

The merged request-attempt and receive-terminal refactors improved runtime ownership, but their handoff still permits a live attempt before all fallible readiness work completes. Initial and replacement publication can therefore fail after partial transfer; coordinators can still reach the raw stream; parallel arms mutate shared recovery state; and caller context can compete with frozen facts. This brownfield specification closes those seams without changing client, routing, billing, protocol, or plugin behavior.

## Boundary Context

- **In scope**: `internal/core/runtime` attempt acquisition, readiness, publication, replacement, parallel reduction, terminal settlement, context projection, tests, and `internal/archtest` ratchets.
- **Out of scope**: public APIs, selector grammar, protocol/provider adapters, billing or secure-session domain redesign, extension ABI, configuration, and generic workflow/actor/DI/resource-registry frameworks.
- **Boundary ownership**: core application orchestration; existing routing, B2BUA, authority, billing, secure-session, interleaved, observer, and extension domains retain policy ownership.

## Requirements

### Requirement 1: Preserve Runtime Semantics
**Objective:** As an operator, I want lifecycle hardening without behavior drift.

#### Acceptance Criteria
1.1. When initial or replacement execution succeeds, the runtime shall preserve canonical events, terminal outcomes, and observable ordering.
1.2. When recovery runs before output, the runtime shall preserve failover, retry, TTFT, affinity, `[first]`, `[thinker]`, route-override, and error-precedence semantics.
1.3. While output is committed, the runtime shall prohibit silent retry or replacement.
1.4. When attempts start or end, the runtime shall preserve A/B-leg lineage, positive attempt sequence, authority, metering, billing-leg, and billing-call semantics.
1.5. The runtime shall preserve distinct request and attempt terminal lifetimes.
1.6. The implementation shall not change public APIs, plugin/connector contracts, protocol behavior, configuration, or provider boundaries.

### Requirement 2: Own an Attempt from First Acquisition
**Objective:** As an operator, I want one lifecycle authority for every partially or fully acquired attempt.

#### Acceptance Criteria
2.1. When the first attempt-scoped obligation is acquired, the runtime shall attach it to one lifecycle owner that remains responsible through terminal settlement.
2.2. When any later acquisition or initialization fails, the runtime shall settle every acquired budget permit, B-leg, authority, stream, observer, accounting/tool/cache state, metering, and billing obligation exactly once.
2.3. If final-observer startup or any fallible post-open initialization fails, the runtime shall prevent publication.
2.4. If cancellation occurs before publication, the runtime shall cancel/close the stream when acquired and settle all acquired obligations once.
2.5. If a resource was never acquired, the runtime shall not invoke its cleanup effect.
2.6. When prepublication cleanup completes, no reservation, B-leg registration, stream, observer, billing obligation, or attempt-owned goroutine shall remain live.

### Requirement 3: Publish Only Fully Ready Attempts
**Objective:** As a maintainer, I want publication to be the sole visibility boundary.

#### Acceptance Criteria
3.1. When an attempt becomes current, every fallible attempt-local readiness prerequisite shall already have succeeded.
3.2. When the slot accepts an attempt, it shall require a single-use readiness/publication capability rather than a raw owner.
3.3. When initial assembly succeeds, the runtime shall publish the ready attempt and transfer pre-stream request ownership through one non-fallible commit before returning.
3.4. If initial assembly fails before commit, request cleanup shall remain active and the unpublished attempt shall terminalize.
3.5. When replacement opens, the existing current attempt shall remain coherent until readiness and publication acceptance.
3.6. When `Close` races replacement, one slot-owned publication lease shall linearize whether close or replacement wins.
3.7. If publication is denied or its winner commit fails, the unpublished attempt shall terminalize and no half-installed replacement or winner-only effect shall remain.
3.8. When publication succeeds, duplicate publication, ownership transfer, or winner-effect commit shall be impossible.

### Requirement 4: Terminalize Attempts Exactly Once
**Objective:** As an operator, I want every attempt outcome to run one complete settlement protocol.

#### Acceptance Criteria
4.1. When an attempt succeeds, fails, is canceled, replaced, loses a race, never starts, or is denied publication, the runtime shall create one typed terminal command and evidence snapshot.
4.2. When terminalization wins, it shall detach and cancel/close the stream at most once.
4.3. When terminalization wins, it shall finish observation, finalize/release authority, meter egress, release/end the B-leg, append B-leg billing, and record the attempt outcome at most once.
4.4. When terminalization wins, it shall dispose of attempt-local accounting, tool, prompt-cache, and transient observation state.
4.5. If terminal callers race, all callers shall observe one published settlement result without duplicate effects.
4.6. While an attempt terminalizes, request terminal ownership shall remain separate unless logical-request rules require completion.

### Requirement 5: Make Frozen Facts Authoritative
**Objective:** As a security and accounting maintainer, I want admitted typed facts to outrank caller context.

#### Acceptance Criteria
5.1. When preparation freezes identity, session, workspace, secure-turn, route, model, metering, request-authority, and billing facts, core decisions shall use those typed facts.
5.2. When `Recv` or `Close` receives a bare, stale, or conflicting context, business decisions shall match the originally admitted turn.
5.3. When compatibility seams need context values, one projector shall overwrite every authoritative business key, including authoritative absence, from frozen facts.
5.4. When caller cancellation, deadlines, tracing, or diagnostics are present, the projector shall preserve them without allowing business-value override.
5.5. When generation/catalog reload occurs, initial, replacement, parallel, and interleaved work shall remain bound to frozen facts plus typed recovery progress.
5.6. The runtime shall not read caller context as routing, billing, security, metering, model, session, workspace, secure-turn, or authority truth after freeze.

### Requirement 6: Reduce Parallel Arms Serially
**Objective:** As an operator, I want isolated arms and deterministic shared-state reduction.

#### Acceptance Criteria
6.1. When a parallel group starts, each arm shall receive immutable facts and own an independent lifecycle, B-leg, authority, stream, and attempt-local state.
6.2. While workers run, no arm shall mutate shared exclusions, failure history, budgets, TTFT, `[first]`, interleaved, affinity, or slot state.
6.3. When an arm completes, it shall return a typed ready capability or failure delta plus evidence and pending winner effects.
6.4. When outcomes arrive, one coordinator/reducer shall own arm starts, handicap timing, budget/TTFT consumption, failure merge, winner selection, and publication.
6.5. When a winner is selected, only its pending selection effects shall commit and every loser/late arm shall terminalize once.
6.6. When all arms fail, deltas shall merge in stable arm order while preserving existing final-error precedence.
6.7. When real arrival order differs, first-success behavior may differ; for the same controlled arrival order, winner, shared progress, and side effects shall be deterministic.

### Requirement 7: Enforce Lifecycle-Complete Boundaries
**Objective:** As a maintainer, I want coordinators unable to bypass attempt settlement.

#### Acceptance Criteria
7.1. The streaming facade shall retain exactly the established five owners and expose only `Recv` and `Close`.
7.2. When assembler, receive, replacement, parallel, A-leg lifecycle, or request terminal code interacts with an attempt, it shall call lifecycle-complete operations.
7.3. Raw stream and attempt-local resource mutation shall remain private to the attempt owner.
7.4. The runtime shall have one production entry point for attempt-local terminalization.
7.5. While `Recv` coordinates work, control flow shall remain explicit rather than hidden behind a generic dispatcher or workflow engine.
7.6. The implementation shall not introduce a generic mutable state bag, resource registry, actor, service locator, DI container, reflection dispatcher, or universal stage framework.

### Requirement 8: Preserve Domain and Concurrency Boundaries
**Objective:** As an integrator, I want convergence to remain an internal orchestration change.

#### Acceptance Criteria
8.1. When routing, interleaved reasoning, secure-session, billing, hooks, gates, observers, traffic/usage emitters, prompt cache, or tool assembly executes, accepted ordering and policy shall remain unchanged.
8.2. When replacement or terminalization occurs, attempt-derived evidence shall remain attributed to the producing attempt.
8.3. The implementation shall preserve streaming as the canonical path and shall not add provider/protocol logic to core.
8.4. While backend, observer, store, billing, metering, authority, or extension work runs, no coordination lock shall be held across that potentially blocking operation.
8.5. If publication-coupled durable winner state spans multiple writes, the runtime shall use one narrow existing-store atomic compare-and-apply command rather than a generic transaction framework.
8.6. Any new internal store command shall have memory, SQLite, and PostgreSQL semantic parity where those implementations apply.

### Requirement 9: Certify the Architecture Adversarially
**Objective:** As a reviewer, I want discriminating evidence for every ownership boundary.

#### Acceptance Criteria
9.1. When failure is injected after each acquisition, readiness, publication, selection-commit, and terminal effect, tests shall prove exact cleanup and attribution.
9.2. When final-observer startup fails in initial or replacement flow, tests shall prove that no half-published attempt remains.
9.3. When cancellation, `Close`, timeout, receive failure, and publication race, tests shall prove one linearized outcome and exactly-once effects.
9.4. When parallel schedules and context/reload variants run, tests shall prove reducer ownership and frozen-fact authority.
9.5. When repeated scheduling, supported race detection, checkptr, and leak detection run, no race, invalid pointer use, deadlock, or attempt goroutine leak shall be reported.
9.6. When architecture tests scan production code, they shall reject raw stream access, post-publication fallible readiness work, raw slot publication, parallel shared mutation, context-first business resolution, duplicate terminalization, and broad-framework regressions.
9.7. When before/after metrics are compared, the accepted five-owner facade, coordinator fan-out, cross-owner access, state-copy surface, and cleanup-site count shall not regress without a reviewed exception.
9.8. The change shall not be complete until every acceptance criterion has automated evidence or an explicitly approved equivalent and repository quality/parity/platform gates pass.
