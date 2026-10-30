# Requirements Document

## Introduction

The versioned runtime-reload implementation established the correct product behavior: one stable data-plane listener, immutable request-plane generations, atomic last-good publication, generation pinning for in-flight and asynchronous work, bounded retention, explicit administrative triggers, and safe retirement. The implementation is heavily tested and operationally credible.

The migration did not fully replace the previous runtime architecture. The current `main` branch still carries overlapping runtime aggregates and compatibility paths: `ProcessServices`, `CandidateRuntime`, `Built`, `RequestPlane`, and `GenerationBundle`; the new request-plane view is converted back into a legacy `Built` for HTTP mounting; bootstrap can select old or new ownership models through a nullable composer; `check-config` publishes a real generation before tearing it down; reload result/status vocabulary is mirrored across internal, public, and HTTP packages; and generation lifecycle and host shutdown ownership are spread across several types.

The `runtime-architecture-convergence-and-shrinkage` effort completes that migration without changing implemented functionality. It removes obsolete paths, narrows ownership, makes state machines locally understandable, and uses the existing race, soak, rollback, no-drop, security, conformance, and compatibility suites as the behavior-preservation contract.

## Boundary Context

- **In scope:** process-versus-generation runtime ownership; standard-distribution bootstrap; generation compilation; HTTP request-plane composition; legacy `Built` and `RunWithRuntime` retirement; true dry-run validation; reload coordinator decomposition; reload contract canonicalization; generation retirement and shutdown ownership; public `pkg/lipruntime` facade simplification; deprecated option quarantine/removal; architecture budgets and shrinkage evidence; documentation and migration evidence.
- **Out of scope:** changing LLM protocol behavior; changing canonical `lipapi` request/event semantics; introducing new providers or frontends; changing routing algorithms, retry/failover policy, no-retry-after-output behavior, token accounting semantics, authority semantics, secure-session behavior, source-integrity policy, management authentication, listener topology, or plugin discovery/trust policy; introducing a DI container, reflection registry, generic workflow engine, or runtime plugin installation.
- **Adjacent expectations:** the completed `versioned-runtime-reloadable-proxy-configuration` specification remains authoritative for externally visible reload behavior and safety invariants. Backend connector and generic-compatible-backend specifications remain authoritative for provider lifecycle and factory ownership. This specification may replace internal composition contracts only where behavior remains equivalent.
- **Boundary ownership:** `internal/infra/runtimebundle` owns concrete process/generation composition; `internal/infra/runtimehost` owns generation publication, reload admission/orchestration, retention, and retirement policy; `internal/stdhttp` owns HTTP handler construction and transport DTOs; `pkg/lipsdk` owns any canonical safe reload contract; `pkg/lipruntime` remains the public standard-runtime facade.
- **Optional hexagonal lens:** runtimebundle is the composition root; runtimehost is application/runtime orchestration; stdhttp and cmd/lipstd are driving adapters; filesystem/config source, metrics, stores, and provider factories remain driven adapters; public contract types remain dependency-neutral.
- **Revalidation triggers:** changes to `runtimebundle.Build`, `ProcessServices`, `CandidateRuntime`, `GenerationBundle`, `runtimehost.Generation`, `runtimehost.Manager`, `runtimehost.Coordinator`, `stdhttp.ComposeRequestPlane`, management DTOs, `pkg/lipruntime.Options`, public reload types, lifecycle/closer contracts, architecture budgets, or standard-distribution startup commands.

## Requirements

### Requirement 1: Preserve All Implemented Behavior and Safety Invariants

**Objective:** As an operator and maintainer, I want the architecture contraction to preserve every implemented product behavior, so that maintainability improves without changing client, provider, security, or operational semantics.

#### Acceptance Criteria

1.1. When the refactored standard distribution serves a request, the proxy shall preserve canonical request decoding, routing, capability negotiation, backend invocation, event streaming, error rendering, accounting, authority, and diagnostics behavior.

1.2. While a request, stream, retry/failover sequence, parallel race, terminal-work item, heartbeat, provider operation, or delayed finalizer is bound to a generation, the proxy shall preserve the current generation-pinning and no-drop guarantees.

1.3. If any backend attempt has emitted client-visible output, the proxy shall preserve the prohibition on retry, failover, or transparent migration to another backend or generation.

1.4. When configuration reload is triggered, the proxy shall preserve explicit-trigger-only behavior, strict source integrity, last-good rollback, restart-required rejection, busy/coalescing semantics, bounded retention, and atomic publication.

1.5. When startup, reload, validation, retirement, or shutdown fails, the proxy shall preserve current safe error categories, secret redaction, panic isolation, reverse-order cleanup, and retryable teardown behavior.

1.6. The system shall preserve current access-mode, transport-auth, management-auth, browser-origin, secure-session, diagnostics, control-plane, token-accounting, metering, and provider-credential postures.

1.7. The system shall preserve the canonical streaming-first execution model and shall not introduce a separate non-streaming execution engine.

1.8. The system shall preserve provider-family and plugin boundaries and shall not move provider/protocol-specific behavior into core or composition-generic packages.

1.9. The system shall introduce no DI container, reflection-based service registry, global mutable runtime registry, `init()` registration, generic workflow framework, or `map[string]any` dependency transport.

1.10. Where public source compatibility must change, the system shall make the change only through an explicit documented compatibility boundary and shall provide a deterministic migration path.

### Requirement 2: Establish One Explicit Runtime Ownership Model

**Objective:** As a runtime maintainer, I want one authoritative ownership model, so that every resource has one lifecycle owner and dependency propagation is locally understandable.

#### Acceptance Criteria

2.1. The standard runtime shall classify every production resource as process-owned, generation-owned, or request/async-work-owned.

2.2. The composition root shall expose one canonical process-lifetime aggregate for process-owned services and one canonical immutable generation-lifetime aggregate for generation-owned services and allowed non-owning process references.

2.3. Process-owned stores, pools, process workers, metrics/tracing topology, listeners, plugin discovery/trust catalog, and shared capacity limiters shall have one process owner and shall not be duplicated per generation.

2.4. Generation-owned executor, backend instances, feature surface, model views, routing/config projections, HTTP handler graph, and resource ledger shall have one generation owner and shall not close process-owned services.

2.5. Request and asynchronous ownership shall be represented through generation leases or pins rather than through copied runtime aggregates or unclassified closer lists.

2.6. The canonical process and generation aggregates shall group dependencies by cohesive capability and shall not expose a flat service-locator bag.

2.7. If a new runtime dependency is introduced, the architecture shall require updates only at its owning construction boundary and at the focused consumer or capability group that uses it.

2.8. The system shall not retain an aggregate whose ownership meaning changes depending on the call path, such as a type that sometimes owns closers and sometimes carries the same fields with closers intentionally nil.

### Requirement 3: Delete the Legacy Runtime Composition Graph

**Objective:** As a maintainer, I want the completed generation architecture to replace the legacy runtime path, so that new features do not require synchronized edits across parallel dependency models.

#### Acceptance Criteria

3.1. The production standard-distribution path shall not construct or consume `runtimebundle.Built`.

3.2. The production codebase shall not retain the compatibility `runtimebundle.Build` path after all supported callers migrate to process-plus-generation composition.

3.3. The production HTTP server shall not retain or invoke `stdhttp.RunWithRuntime`.

3.4. The HTTP request-plane composer shall not convert a generation view back into `Built`, and `requestPlaneAsBuilt` or an equivalent rehydration helper shall not exist.

3.5. The system shall not replace `RequestPlane` with another broad interface containing one getter per runtime dependency.

3.6. The canonical generation runtime shall directly satisfy the narrow publication, executor-view, model-binding, terminal-provider, readiness, and backend-kind capabilities that runtimehost consumes.

3.7. HTTP mounting shall consume focused immutable capability groups or transport-specific input values with no lifecycle ownership.

3.8. Legacy closer projections such as candidate `Closers` derived from the resource ledger shall be removed after compatibility callers migrate.

3.9. `BootstrapResult` and equivalent startup results shall not carry mutually exclusive old-runtime and generation-host products.

3.10. Architecture tests shall fail if any deleted legacy symbol or equivalent old-path dependency graph reappears in production code.

### Requirement 4: Build One Complete Host from One Effective Startup Snapshot

**Objective:** As an operator, I want startup to construct one complete host from one accepted configuration snapshot, so that validation, generation 1, and reload state cannot disagree.

#### Acceptance Criteria

4.1. The standard serve path shall use one host-building operation that either returns a complete owned host or returns an error after internal rollback.

4.2. When startup succeeds, the host builder shall read and normalize the effective configuration exactly once.

4.3. The serve-only multi-user CLI consistency gate and all other startup-fixed override validation shall evaluate the same effective snapshot that becomes generation 1.

4.4. The initial generation, process runtime, reload coordinator active-effective state, active source identity, and safe public fingerprint shall derive from that same accepted snapshot.

4.5. Production callers shall not perform a second `AttachReloadHost` step after bootstrap.

4.6. A nullable handler composer or equivalent optional field shall not select between two complete runtime ownership architectures.

4.7. The generation-host serve path shall not construct `runtime.App`, start App-owned feature lifecycles, or merge the same feature surface twice.

4.8. Partial startup cleanup, tracing shutdown, process-runtime close, and unpublished-generation rollback shall be owned inside the host builder rather than repeated by `cmd/lipstd` and `pkg/lipruntime`.

4.9. Inspect, routes, inventory, validation, and serve operations shall use explicit entrypoints with outputs appropriate to each operation rather than a single result containing every possible artifact.

### Requirement 5: Make Configuration Validation a True Unpublished Dry Run

**Objective:** As an operator, I want `check-config` to validate the real generation compiler without publication, so that validation has production parity without executing an artificial lifecycle.

#### Acceptance Criteria

5.1. When `check-config` runs, the system shall use the same strict effective loader, standard bundle, generation compiler, handler composer, and deterministic validation rules used by startup and reload.

5.2. `check-config` shall not create a generation manager, allocate a published generation ID, swap an active pointer, retain a generation, or invoke published-generation retirement.

5.3. `check-config` shall not bind a data-plane or management listener.

5.4. If dry-run compilation succeeds, the system shall quiesce or roll back the unpublished generation resources and then close any validation-owned process resources in deterministic reverse ownership order.

5.5. If dry-run compilation fails, the system shall return the same safe deterministic category that startup or reload would return for the same candidate.

5.6. Parity tests shall prove that deterministic compile-time rejections are identical across `check-config`, startup generation construction, and reload candidate construction.

### Requirement 6: Separate Reload Admission, Attempt Execution, and Reload State

**Objective:** As a runtime maintainer, I want reload concurrency and workflow responsibilities separated, so that changes to one state machine do not require reasoning about all reload behavior.

#### Acceptance Criteria

6.1. One reload-attempt gate shall exclusively own busy admission, queued/coalesced signal state, active-attempt cancellation, shutdown rejection, and idle notification.

6.2. One attempt runner shall exclusively own the linear read, load, no-op, classify, compile, prepare, retention-admit, publish, and rollback transaction.

6.3. One reload-state owner shall exclusively own active effective/source state, last result, last success, last failure, bounded history, safe model-generation metadata, and status snapshots.

6.4. The coordinator shall orchestrate the gate, runner, state owner, and observer without directly implementing detailed source/load/classification/compile branches.

6.5. The reload idle barrier shall not use polling, periodic sleeps, or a window in which busy state is visible before the completion signal is armed.

6.6. Starting an attempt shall atomically create the attempt completion/cancellation lease before the attempt is observable as active.

6.7. Completing or abandoning an attempt shall release its lease exactly once and wake all idle waiters.

6.8. Signal coalescing shall preserve the existing bounded semantics: one active attempt, at most one pending follow-up at a time, and a safe count of additional coalesced signals.

6.9. API-triggered reload while busy shall preserve the current conflict result and shall not queue an unbounded follow-up.

6.10. Panic isolation, host-owned timeout, caller-cancellation detachment, shutdown cancellation, and last-good publication semantics shall remain unchanged.

6.11. Gate, runner, state, and coordinator behavior shall be independently unit-testable without constructing unrelated filesystem, compiler, manager, or HTTP dependencies.

### Requirement 7: Declare the Secret-Safe Reload Contract Once

**Objective:** As an SDK and adapter maintainer, I want one canonical reload vocabulary, so that result categories and status fields cannot drift across internal, public, and HTTP layers.

#### Acceptance Criteria

7.1. Trigger kinds, terminal result categories, secret-safe result fields, history fields, and public status fields shall be declared in one dependency-neutral public contract package.

7.2. Runtimehost, `pkg/lipruntime`, management HTTP, diagnostics, metrics labeling, and tests shall consume the same canonical trigger and result category types.

7.3. Internal source identity, private digest, mutable effective configuration, filesystem handle identity, and other private reload state shall remain in internal types and shall not be added to the public contract.

7.4. `pkg/lipruntime` shall preserve supported public names through type aliases or thin delegation where source compatibility is required.

7.5. The HTTP management adapter shall add only transport serialization and HTTP-status policy and shall not redeclare the complete reload domain model.

7.6. Enum-to-identical-enum mapping switches and field-for-field reload status copying shall be deleted.

7.7. If an unknown internal terminal condition occurs, the system shall map it once at the internal error boundary to a canonical safe category rather than at every consumer.

7.8. Public and HTTP reload views shall remain bounded and shall not expose raw YAML, credentials, DSNs, private digests, arbitrary configuration values, or prohibited full paths.

### Requirement 8: Give Generation Lifecycle and Shutdown One Definitive Owner per Layer

**Objective:** As a concurrency maintainer, I want lifecycle invariants owned once, so that idempotency wrappers do not mask conflicting transition logic.

#### Acceptance Criteria

8.1. A generation state object shall own generation identity, immutable publication metadata, lifecycle/refcount state, the drained notification, and the bound canonical generation runtime.

8.2. The generation manager shall own active publication, retained generations, retention admission, retirement scheduling, and all-generation shutdown.

8.3. The canonical generation runtime and its resource ledger shall exclusively own candidate rollback, generation quiesce, and generation resource close.

8.4. Wrapper objects shall not add independent `sync.Once`, closed flags, or close-result caches around the same generation-runtime quiesce or close operation.

8.5. A retirement collaborator may implement policy and retries, but it shall be manager-owned, shall not hold a second authoritative lifecycle state, and shall derive or emit status from the generation transition result.

8.6. The process host shall exclusively own shutdown ordering: reject reloads, wait for candidate work, stop new admissions, retire/drain generations, close process runtime, and close tracing last.

8.7. `pkg/lipruntime.Runtime.Close` and `cmd/lipstd` shall delegate host shutdown rather than reimplement manager/process/coordinator ordering.

8.8. If cleanup fails in a retryable closing state, the system shall preserve the current retry contract and shall not mark the generation successfully closed.

8.9. The system shall not kill pinned requests or asynchronous work solely to reclaim retention capacity.

8.10. Lifecycle state-machine tests shall validate legal transitions, exact ownership, concurrent retirement, retryable cleanup, panic isolation, and shutdown races against the definitive owners.

### Requirement 9: Replace Broad HTTP Runtime Bags with Focused Composition Inputs

**Objective:** As an HTTP adapter maintainer, I want focused immutable mount inputs, so that HTTP concerns do not depend on a legacy all-runtime aggregate.

#### Acceptance Criteria

9.1. The standard HTTP composer shall receive a purpose-specific immutable composition input that carries no closers or lifecycle mutation controls.

9.2. The HTTP composition input shall group execution/routing, security, operations/diagnostics, model, frontend, and shared process references by cohesive concern.

9.3. Each mount helper shall accept only the capability group it requires rather than a broad `Built` or generation object.

9.4. Process-owned and generation-owned references included in HTTP inputs shall be explicitly documented as non-owning and shall be immutable or defensively copied where required.

9.5. The HTTP composer shall preserve route conflict detection, middleware order, auth order, metrics/tracing, recovery, request limits, model routes, diagnostics, accounting/admin routes, and frontend mounts.

9.6. Reload/status management routes and the management listener shall remain process-owned outside the swappable request-plane handler graph.

9.7. Adding a new HTTP-only diagnostic capability shall not require adding a field to process runtime, candidate runtime, legacy `Built`, request-plane getters, and generation bundle simultaneously.

### Requirement 10: Make the Public Runtime a Thin Host Facade and Retire the Legacy Options API

**Objective:** As a library consumer, I want a stable minimal public facade with one canonical configuration model, so that internal ownership changes do not leak into public composition.

#### Acceptance Criteria

10.1. `pkg/lipruntime.Runtime` shall retain one immutable host-facing dependency and only the synchronization required by its public API contract.

10.2. Executor access, readiness, reload, status, capabilities, snapshot refresh, and close shall delegate to the host or a narrow host interface.

10.3. The public facade shall not directly retain or coordinate concrete manager, process-runtime, coordinator, source, tracing-shutdown, or generation-owner fields.

10.4. Production capability status shall be derived from the host or active generation rather than copied into a set of build-time booleans.

10.5. The canonical public `Options` model shall contain only descriptor-bound request, attempt, concurrency, and rater registrations.

10.6. **Alpha-stage compatibility decision (approved 2026-07-25 by maintainer matdev83):** because the project is alpha with no supported stable release or user contract on the legacy fields, the deprecated parallel provider/rater fields shall be removed in this convergence rather than quarantined until a future major version. Removed fields: `RequestProviders`, `AttemptProviders`, `ConcurrencyProvider`, `Rater`, `ProviderDescriptors`. Canonical replacements: `RequestRegistrations`, `AttemptRegistrations`, `ConcurrencyRegistration`, `RaterRegistrations`, with the provider descriptor embedded on each registration.

10.7. No legacy option model, adapter, descriptor-pairing path, stage-family filter, or legacy ID (`legacy-production-rater`) shall remain in production code or be reintroduced.

10.8. Host construction and public normalization shall accept registration-only `Options` / `ProductionOptions`; parallel provider/rater fields and the former `legacy_options` adapter shall stay absent (enforced by architecture and package tests).

10.9. A migration guide shall map every removed field to its canonical registration replacement, record the alpha-stage approval, and identify the last commit that still contained the legacy fields plus the first registration-only head.

10.10. Public external-module tests shall prove registration-only API behavior and that no supported consumer path depends on the removed fields.

### Requirement 11: Ratchet Architecture Budgets Downward and Prove Material Shrinkage

**Objective:** As a maintainer, I want architecture guardrails to force convergence, so that the migration cannot leave new gravity wells or regrow deleted paths.

#### Acceptance Criteria

11.1. Before production refactoring begins, critical-file budgets shall be added for the current reload/runtime hotspots, including coordinator, generation, candidate compilation, process runtime construction, and public runtime build.

11.2. Initial hotspot budgets shall freeze current growth and shall be lowered after each completed contraction phase.

11.3. The final non-test line ceilings shall be no more than 300 lines for reload coordinator orchestration, 400 lines for the generation state object, 350 lines for generation candidate compilation, 300 lines for process runtime construction, and 150 lines for public runtime build/facade assembly, unless a lower-level split demonstrably removes the named file.

11.4. Architecture tests shall forbid production references to deleted legacy runtime symbols, compatibility rehydration helpers, duplicate reload contract declarations, duplicate startup config loads, and the old two-step host attachment path.

11.5. The final implementation shall reduce non-test production Go lines in the affected `runtimebundle`, `runtimehost`, `stdhttp`, `cmd/lipstd`, and `pkg/lipruntime` surfaces by at least 800 lines relative to reviewed commit `efe4624909cea318c7211d5cb3734059d3210802`.

11.6. Moving unchanged logic between files or packages shall not count as shrinkage evidence.

11.7. If a package budget is temporarily raised during migration, the same change shall record the old-path deletion milestone and the budget shall ratchet below its pre-spec value before implementation completion.

11.8. The architecture report shall record before/after non-test lines, critical file sizes, internal fan-out/fan-in, exported public symbols, deleted production symbols, and remaining compatibility exceptions.

11.9. The implementation shall not declare completion while any parallel production runtime composition path or mirrored reload model identified by this specification remains.

### Requirement 12: Execute the Migration Incrementally with TDD and Explicit Deletion Gates

**Objective:** As a reviewer, I want the contraction delivered in behavior-locked phases, so that large deletions remain reviewable, bisectable, and safe.

#### Acceptance Criteria

12.1. Before each ownership or deletion phase, the implementation shall add or identify characterization tests for the behavior and failure paths being preserved.

12.2. Interfaces, architecture gates, and failing tests shall precede production changes for coordinator, lifecycle, HTTP composition, bootstrap, public facade, and compatibility removal.

12.3. Each implementation pull request shall state which old concept or path it deletes; a refactor that only relocates the same concepts shall not satisfy a phase.

12.4. The migration shall convert consumers to the canonical path before deleting the old producer or aggregate.

12.5. Intermediate compatibility adapters shall be private, directional toward the canonical model, and accompanied by a concrete deletion task and architecture test.

12.6. Production main shall not retain two independently supported serve paths after the host-convergence phase merges.

12.7. Tests whose only purpose is preserving a deleted compatibility path shall be removed or rewritten against the canonical behavior surface.

12.8. Architecture, operator, runtime-flow, public API, and enterprise-extension documentation shall be updated in the same phase that changes the corresponding boundary.

12.9. The implementation shall remain buildable and testable after every phase and shall not require a long-lived feature branch with an untestable intermediate architecture.

### Requirement 13: Re-run Production-Grade Verification and Guard Performance

**Objective:** As an operator, I want the simplified architecture certified against the existing release bar, so that code deletion does not reduce reliability or observability.

#### Acceptance Criteria

13.1. The final implementation shall pass the repository default quality, unit, conformance, race, fuzz-smoke, benchmark, module-integrity, lint, architecture, and vulnerability gates.

13.2. Focused reload tests shall cover no-drop HTTP/1.1, HTTP/2, SSE, cancellation, failover, parallel race, no-op source-baseline advancement, restart-required rejection, retention pressure, management API, SIGHUP, and shutdown races.

13.3. Lifecycle tests shall cover candidate rollback, quiesce, drain, retryable close, panic isolation, concurrent retirement, process shutdown ordering, and retained-generation cleanup.

13.4. Public and HTTP contract tests shall cover reload status serialization, public aliases/delegation, external-module compatibility, and absence of private reload fields.

13.5. Architecture tests shall prove no provider SDK, filesystem/signal adapter, stdhttp dependency, concrete plugin, DI container, global registry, watcher, or polling mechanism leaks into prohibited layers.

13.6. Repeated benchmark comparison shall show no material regression in generation acquire/release, publication, request dispatch, or candidate compilation attributable to the contraction; any accepted regression shall include measured evidence and rationale.

13.7. Candidate compilation allocations and elapsed time shall not regress by more than 10 percent in repeated equivalent-host `benchstat` comparisons without explicit approval.

13.8. The final release evidence shall record exact commands, reviewed SHA, platform, Go version, before/after architecture metrics, benchmark comparison, known limitations, and any intentionally deferred public-major cleanup.

13.9. Live provider calls and credentials shall remain optional and shall not be required to prove the internal architecture contraction.

13.10. The implementation shall preserve current cross-platform compilation and Unix/non-Unix signal contract coverage.
