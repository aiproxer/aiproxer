# Requirements Document

## Introduction

Go-LIP shall harden long-lived runtime resource ownership only where the current composition code still depends on manual cleanup registration or closer propagation. The change is inspired by Cordis v4's revertible-effect discipline — acquisition and its inverse are treated as one ownership operation — but it shall not introduce Cordis's reactive dependency graph, generic context, service lookup, fiber runtime, or per-component hot-swap model.

The existing Go-LIP lifetime model remains authoritative: `ProcessServices` owns process-lifetime resources; immutable configuration generations own generation-lifetime resources through the existing `ResourceLedger`; admitted work pins one generation until completion. The purpose of this spec is to make selected composition-time resource acquisition harder to misuse and easier to review, not to create a new runtime architecture.

## Boundary Context

- **In scope**: private process-resource ownership helper(s), acquire-plus-release registration, removal of closer-slice propagation from selected `ProcessServices` builders, one narrow structured-lifetime helper for existing generation-owned cancel+join loops, targeted architecture tests, race/leak regression tests, and deletion/simplification of superseded cleanup plumbing.
- **Out of scope**: public `pkg/lipapi`, `pkg/lipsdk`, or `pkg/lipruntime` changes; routing, failover, B2BUA, session, billing, stream, provider, or protocol semantics; backend plugin ABI changes; a DI container/service locator; reactive `requires`/`provides`; runtime dependency solving; per-component reload/HMR; a generic effect monad/runtime; conversion of ordinary lexical `defer Close()` code; and repository-wide cleanup rewrites.
- **Boundary ownership**: `internal/infra/runtimebundle` composition and lifecycle ownership only, plus narrowly scoped architecture/test gates. `internal/infra/runtimehost` generation publication/leases remain unchanged.
- **Optional hexagonal lens**: this is composition-root infrastructure. It changes how driven resources are acquired and owned, not domain policy or request orchestration.
- **Revalidation triggers**: any proposal that changes `ResourceLedger` phase semantics, generation retain/drain/close behavior, public plugin contracts, process-vs-generation lifetime assignment, or request-visible behavior requires a fresh design validation instead of being folded into this spec.

## Requirement 1: Preserve the Existing Runtime Model

**Objective:** As a maintainer, I want ownership hardening to fit the converged runtime model, so that a local safety improvement does not reintroduce the architectural complexity that recent refactors removed.

### Acceptance Criteria

1.1. The implementation shall preserve one process runtime / `ProcessServices`, one immutable generation runtime, one host, one reload contract, and manager-owned generation retirement.

1.2. The implementation shall keep `ResourceLedger` as the sole generation-resource phase owner and shall not introduce a second generation lifecycle registry, effect runtime, fiber graph, or cleanup engine.

1.3. The implementation shall not add a DI container, service locator, reflection registry, generic `Get`/`Resolve`/`Provide` API, package-global registry, or `init()` registration.

1.4. The implementation shall make no public API, configuration-schema, plugin-ABI, canonical request/event, routing-selector, or wire-protocol change.

1.5. When ownership hardening can be achieved by adapting an existing owner, the implementation shall extend or wrap that owner rather than replacing it with a parallel abstraction.

1.6. If a proposed migration does not remove manual cleanup plumbing or establish a stronger ownership invariant at the migrated call site, the implementation shall leave that call site unchanged.

## Requirement 2: Atomic Process Resource Ownership

**Objective:** As a runtime maintainer, I want successful process-resource acquisition to transfer cleanup ownership before the resource escapes its construction boundary, so that later failures cannot bypass cleanup registration.

### Acceptance Criteria

2.1. When a targeted owned process-lifetime acquisition succeeds, the composition layer shall require a non-nil release action and shall register that release in the existing `ProcessServices` close set in the same ownership operation before returning the value to later construction steps.

2.2. If a targeted acquisition fails before producing an owned resource, no release action shall be registered; value-only or explicitly non-owning construction shall bypass the owned-acquisition helper rather than representing ownership with a nil release.

2.3. If a later construction step fails after one or more successful targeted acquisitions, the existing process rollback path shall release all registered resources in reverse acquisition order.

2.4. If one or more release actions fail during constructor rollback or process shutdown, the existing aggregate-error behavior shall be preserved and the remaining releases shall still be attempted in reverse order.

2.5. The process ownership primitive shall remain private to runtime composition and shall expose no dependency lookup, resource retrieval, lazy service creation, or cross-package lifecycle API.

2.6. The process ownership primitive shall not add runtime concurrency, background work, reflection, code generation, or a new external dependency.

2.7. The process ownership primitive shall be an append-only facade over the existing `ProcessServices` closer set, not a second release store or shutdown state machine; constructor rollback and normal `ProcessServices.Close` shall consume that same authoritative release set so migrated resources have exactly one cleanup path.

## Requirement 3: Eliminate High-Value Closer Propagation

**Objective:** As a maintainer, I want process builders that currently return cleanup lists to register ownership at acquisition time, so that cleanup correctness is local instead of being reconstructed by their caller.

### Acceptance Criteria

3.1. Selected `ProcessServices` construction paths that currently return a resource plus one or more process closers shall instead register those closers with the process owner before the builder returns successfully.

3.2. The migration shall target the existing process-owned usage-authority, concurrency-authority, persistence, accounting-store, metering, and terminal-work construction paths where closer propagation crosses builder boundaries; adjacent single-closer paths shall remain unchanged unless migration demonstrably deletes more lifecycle plumbing than it adds.

3.3. When a selected builder acquires multiple resources and later fails, every successfully acquired resource shall already be registered with the process owner before the next fallible construction step that depends on it.

3.4. Special ownership cases whose current semantics would be obscured by the helper — including pool claim/prune ordering or platform-specific plugin artifact/staging teardown — may remain explicit, and the design shall not force them through the abstraction merely for uniformity.

3.5. The migrated constructor path shall remove superseded caller-side closer aggregation/adaptation code rather than retaining both old and new ownership mechanisms.

3.6. Existing release ordering for plugin host/artifacts/staging, process stores, pools, and dependent services shall remain behaviorally equivalent after the migration.

3.7. The migration shall stay inside the runtime composition boundary and shall not require changes to domain service interfaces solely to satisfy the ownership helper.

## Requirement 4: Structured Lifetime for Selected Generation-Owned Loops

**Objective:** As a maintainer, I want the existing cancel-plus-join pattern for long-lived generation-owned loops to be one lifecycle operation, so that a future loop cannot be started without an owned shutdown path.

### Acceptance Criteria

4.1. Where runtime composition currently starts a generation-owned background loop whose complete shutdown contract is cancellation plus join, the implementation shall use one private helper that couples loop startup with registration of its cancel-and-join release action.

4.2. The helper shall ensure the loop cannot perform application work before its shutdown ownership has been established with the existing `ResourceLedger`; its blocked start gate shall observe cancellation and re-check cancellation before entering application work.

4.3. The helper shall preserve the caller-selected existing cleanup phase. For the model-registry refresh migration, cancel-and-join shall remain `PhaseQuiesce`, catalog close shall remain `PhaseClose`, and refresh termination shall complete before catalog close.

4.4. If `ResourceLedger` registration performs synchronous immediate cleanup because the ledger is sealed, closing, or already quiesced, cancellation shall unblock the gated loop and join shall complete without leak or deadlock.

4.5. The helper shall not become a general worker pool, scheduler, async task framework, error bus, or replacement for ordinary request-scoped goroutines.

4.6. Only existing long-lived composition-owned loops with the exact cancel-and-join lifetime shape shall be migrated; other goroutines shall remain unchanged unless separately justified.

4.7. The initial migration shall include the generation-owned model-registry refresh loop and may include another runtimebundle loop only if the same lifetime contract is demonstrated by characterization tests.

## Requirement 5: Preserve Existing Generation and Backend Semantics

**Objective:** As a maintainer, I want ownership hardening to leave already-correct lifecycle mechanisms alone, so that the refactor does not trade simple proven behavior for theoretical uniformity.

### Acceptance Criteria

5.1. `ResourceLedger` prepare, activate, publish, quiesce, rollback, close, reverse-order cleanup, late-acquisition, and retry semantics shall remain unchanged unless a test exposes a correctness defect that requires separate design review.

5.2. Backend plugin factory contracts, `BackendBuildResult`, executable connector process supervision, and backend instance lifecycle interfaces shall remain unchanged.

5.3. Existing backend cleanup transfer shall remain generation-owned and shall not be moved into the process owner.

5.4. Generation publication, request/stream pinning, retained-generation limits, quiesce/drain sequencing, and manager-owned retirement shall remain unchanged.

5.5. The refactor shall not change request execution, routing/failover, no-retry-after-output, accounting/billing, secure-session, model selection, protocol translation, or client-observable error semantics.

5.6. Ordinary short lexical resources whose lifetime is already safely expressed with `defer` shall not be migrated to the new ownership primitive.

## Requirement 6: TDD, Architecture Gates, and Measurable Simplification

**Objective:** As a maintainer, I want the ownership change proven by behavior and constrained by architecture tests, so that the new abstraction pays for itself in deleted complexity.

### Acceptance Criteria

6.1. Before implementation, RED tests shall pin owned-acquisition non-nil release enforcement, registration into the authoritative `ProcessServices` closer set, reverse-order rollback, aggregate cleanup errors, constructor partial-failure behavior, and exactly-once normal shutdown.

6.2. Before migrating a generation-owned loop, RED tests shall pin cancellation-aware start gating, synchronous immediate cleanup, join-before-close, rollback, `PhaseQuiesce` refresh-before-`PhaseClose` catalog ordering, already-closing ownership, and leak-free behavior.

6.3. Targeted race/leak tests shall prove that the structured loop helper does not leak goroutines or race with generation retirement.

6.4. Architecture tests shall prevent the new private ownership primitive from becoming public, crossing out of runtime composition, or acquiring `Get`/`Resolve`/`Provide` service-locator behavior.

6.5. Architecture or source-shape tests shall prevent the selected migrated process builders from reintroducing caller-visible closer-list returns or parallel ownership paths.

6.6. Final implementation review shall remove superseded helper closures, closer plumbing, or lifecycle wrappers made unnecessary by the migration.

6.7. If the final production diff adds more lifecycle concepts than it deletes or makes a migrated path harder to trace from acquisition to release, the implementation shall simplify or revert that migration rather than expanding the abstraction.

6.8. Focused unit tests, targeted race/leak tests, repository architecture/quality checks, and default unit tests shall pass without provider credentials or network-dependent inference.

6.9. No performance improvement is required, but benchmark or profiling evidence shall show no material regression on generation acquire/dispatch or request hot paths because the new ownership helpers run only during composition/lifecycle work.
