# Implementation Plan

Implementation is TDD-first and deletion-oriented. Every phase begins with characterization or architecture RED gates, converts all consumers to the canonical path, deletes an obsolete concept, and lowers the relevant budget. A task is complete only when its observable completion condition and named validation pass. No phase may be satisfied by moving unchanged logic between files.

## Phase 1: Freeze Behavior, Measure the Baseline, and Block Further Growth

- [x] 1. Establish the contraction safety envelope

- [x] 1.1 Record exact architecture and behavior baselines
  - Run `make arch-report` at reviewed commit `efe4624909cea318c7211d5cb3734059d3210802` and record affected package/file non-test lines, fan-out/fan-in, exported public symbols, and current compatibility symbols.
  - Record repeated benchmark baselines for generation acquire/release, publish, dispatcher, candidate compilation, and successful/no-op reload using equivalent-host `benchstat`.
  - Inventory production and test callers of `Built`, `Build`, `RunWithRuntime`, `RequestPlane`, `requestPlaneAsBuilt`, `AttachReloadHost`, duplicate reload contracts, and deprecated public Options.
  - Observable completion: a committed baseline table names every old-path caller and provides reproducible metrics for Requirements 11 and 13.
  - _Requirements: 1.1-1.10, 11.1-11.9, 13.1-13.10_
  - _Boundary: Architecture, tests, and docs_
  - _Depends: none_
  - _Validation: `make arch-report && make bench`_

- [x] 1.2 Add critical-file freeze budgets for current hotspots (P)
  - Add budgets for reload coordinator, generation state, candidate compilation, process runtime construction, and public runtime build/facade.
  - Set initial ceilings to current measured values with no growth headroom beyond formatter variance.
  - Add comments containing final target ceilings and the phase that must lower each budget.
  - Observable completion: representative one-line growth in every hotspot fails `TestCriticalFileLineBudgets`.
  - _Requirements: 11.1-11.4_
  - _Boundary: Architecture tests_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/archtest/... -run 'CriticalFile|LineComplexity'`_

- [x] 1.3 Add behavior characterization matrix for deletion seams (P)
  - Map existing tests covering standard startup, legacy `Build`, generation startup, HTTP mounts, reload, public facade, validation, and shutdown.
  - Add only missing characterization for middleware/mount parity, partial bootstrap cleanup, public Close retry, capability reporting, and check-config non-public behavior.
  - Keep protocol, routing, streaming, auth, management, accounting, and provider behavior assertions unchanged.
  - Observable completion: every production legacy caller has a named behavior test or is proven dead before migration starts.
  - _Requirements: 1.1-1.10, 12.1-12.4, 13.1-13.5_
  - _Boundary: Tests_
  - _Depends: 1.1_
  - _Validation: `go test ./cmd/lipstd/... ./internal/stdhttp/... ./internal/infra/runtimebundle/... ./pkg/lipruntime/... -run 'Compatibility|Standard|Bootstrap|Close|Capability|CheckConfig'`_

- [x] 1.4 Add RED architecture gates for final convergence
  - Add initially targeted tests for no production `requestPlaneAsBuilt`, no canonical-to-legacy runtime adapter, one reload contract declaration, one serve host builder, and one startup effective-load owner.
  - Keep gates scoped or allowlisted until their corresponding conversion task removes the current violation.
  - Prohibit new legacy path call sites immediately.
  - Observable completion: a synthetic new use of each forbidden pattern fails while the current known migration inventory remains explicit.
  - _Requirements: 3.1-3.10, 4.1-4.9, 7.1-7.8, 11.4, 12.5-12.6_
  - _Boundary: Architecture tests_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/archtest/... -run 'RuntimeConvergence|ReloadContract|HostPath|ConfigLoad'`_

## Phase 2: Canonicalize the Reload Contract

- [x] 2. Define one secret-safe reload vocabulary

- [x] 2.1 Add canonical reload contract tests and public package
  - Add RED tests for trigger kinds, result categories, result/history/status defensive copying, closed vocabulary, and secret-safe field inventory.
  - Create dependency-neutral `pkg/lipsdk/configreload` contract types with no internal imports.
  - Preserve the existing category strings and safe fields.
  - Observable completion: internal, public, and HTTP packages can compile against one canonical contract without mapping.
  - _Requirements: 7.1-7.8, 13.4-13.5_
  - _Boundary: SDK/public contract_
  - _Depends: 1.4_
  - _Validation: `go test ./pkg/lipsdk/configreload/... ./internal/archtest/... -run 'ReloadContract|PublicContract'`_

- [x] 2.2 Migrate runtimehost and observers to the canonical contract
  - Replace internal trigger/result/status declarations used by orchestration with canonical types.
  - Keep reloadability policy, safe failure mapping, sanitizers, and private active-source/effective state internal.
  - Update metrics, tracing, history, and diagnostics projections without changing labels or categories.
  - Observable completion: runtimehost contains no duplicate trigger/result category declarations and focused reload tests remain green.
  - _Requirements: 6.2-6.4, 7.1-7.8_
  - _Boundary: Runtime orchestration and observability_
  - _Depends: 2.1_
  - _Validation: `go test ./internal/core/configreload/... ./internal/infra/runtimehost/... ./internal/infra/metrics/... -run 'Reload|History|Observer|Category'`_

- [x] 2.3 Migrate public and HTTP consumers and delete mirror mapping
  - Use aliases/thin delegation in `pkg/lipruntime` for supported public names.
  - Reduce management HTTP DTOs to serialization/status policy only.
  - Delete `pkg/lipruntime/reload_map.go` and field-for-field domain copies.
  - Activate the single-contract architecture gate and lower affected file budgets.
  - Observable completion: every category is declared once and public/HTTP goldens remain byte-compatible where promised.
  - _Requirements: 7.1-7.8, 10.1-10.4, 11.2-11.4_
  - _Boundary: Public facade, HTTP driving adapter, architecture_
  - _Depends: 2.2_
  - _Validation: `go test ./pkg/lipruntime/... ./internal/stdhttp/admin/configreload/... ./internal/archtest/... -run 'Reload|DTO|External|Contract'`_

## Phase 3: Introduce Focused HTTP Composition and the Canonical Generation Runtime

- [x] 3. Replace broad runtime bags at the HTTP boundary

- [x] 3.1 Add focused HTTP mount contract RED tests
  - Characterize every mount helper's actual dependency usage, middleware order, route set, route-conflict behavior, auth order, metrics/tracing, and frontend configuration.
  - Add compile-time tests requiring mount helpers to accept only cohesive capability groups and no closer/lifecycle owner.
  - Add an architecture test prohibiting `*runtimebundle.Built` in stdhttp production mount signatures.
  - Observable completion: tests define `StandardHTTPInput` groups and fail against current broad signatures.
  - _Requirements: 3.4-3.7, 9.1-9.7, 12.1-12.5_
  - _Boundary: HTTP driving adapter and tests_
  - _Depends: 1.3, 1.4_
  - _Validation: `go test ./internal/stdhttp/... ./internal/archtest/... -run 'MountContract|Middleware|RouteConflict|BuiltDependency'`_

- [x] 3.2 Convert mount helpers to cohesive immutable inputs
  - Introduce execution/routing, security, operations/diagnostics, models, and frontend groups.
  - Pass each mount only the group it consumes.
  - Preserve all existing handler behavior and management-plane separation.
  - Remove broad `Built` parameters from stdhttp production mount helpers.
  - Observable completion: stdhttp composes the current legacy and generation callers using focused inputs with no behavior diff.
  - _Requirements: 1.1-1.9, 9.1-9.7_
  - _Boundary: HTTP driving adapter_
  - _Depends: 3.1_
  - _Validation: `go test ./internal/stdhttp/... -run 'Mount|Middleware|Frontend|Diagnostics|Admin|Auth|Metrics'`_

- [x] 3.3 Define the canonical GenerationRuntime ownership contract
  - Add RED tests for one generation owner, immutable groups, narrow runtimehost capabilities, no generic dependency lookup, and exact rollback/quiesce/close ownership.
  - Build the canonical generation runtime around the existing resource ledger and grouped sub-runtimes.
  - Make it directly satisfy handler, executor view, model binder, terminal provider, readiness, and backend-kind capabilities.
  - Do not add a one-getter-per-dependency interface.
  - Observable completion: one unpublished generation runtime independently compiles, serves a handler, exposes narrow capabilities, and closes exactly once.
  - _Requirements: 2.1-2.8, 3.5-3.8, 8.1-8.4, 9.1-9.4_
  - _Boundary: Runtimebundle composition root_
  - _Depends: 3.2_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./internal/infra/runtimehost/... -run 'GenerationRuntime|Ownership|Immutable|Capability|Lifecycle'`_

- [x] 3.4 Make GenerationCompiler return only GenerationRuntime
  - Merge feature surface once per candidate.
  - Construct the grouped HTTP input and compose the handler during generation compilation.
  - Remove duplicate candidate/request-plane/publication field projections as consumers migrate.
  - Preserve candidate fault injection and reverse rollback.
  - Observable completion: two different generation runtimes coexist and close independently without copied runtime bags.
  - _Requirements: 2.2-2.8, 3.6-3.8, 4.7, 5.1, 9.1-9.7_
  - _Boundary: Runtimebundle composition root_
  - _Depends: 3.3_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./internal/stdhttp/... -run 'CompileGeneration|Coexist|Fault|Rollback|Handler'`_

- [x] 3.5 Activate HTTP and generation architecture gates
  - Prohibit production `requestPlaneAsBuilt`, broad RequestPlane getters, stdhttp `Built` dependencies, and candidate legacy closer projections.
  - Delete compatibility-only request-plane mapping tests after replacing them with handler behavior tests.
  - Lower candidate compilation and relevant stdhttp file budgets.
  - Observable completion: the canonical generation path reaches stdhttp without conversion to any legacy aggregate.
  - _Requirements: 3.4-3.10, 9.1-9.7, 11.2-11.4, 12.5-12.7_
  - _Boundary: Architecture and tests_
  - _Depends: 3.4_
  - _Validation: `go test ./internal/archtest/... ./internal/stdhttp/... ./internal/infra/runtimebundle/... -run 'RuntimeConvergence|RequestPlane|Built|Generation'`_

## Phase 4: Delete the Legacy Runtime and Serve Path

- [x] 4. Remove the old runtime graph after consumers migrate
  - _Completion: Tasks 4.1-4.4 complete; legacy `Built`, compatibility `Build`, production `RunWithRuntime`, legacy closer views, and their allowlists are removed and guarded against reintroduction._

- [x] 4.1 Migrate remaining internal and test callers from `Build` and `Built`
  - Convert supported tests and helper constructors to ProcessRuntime plus GenerationRuntime or focused test builders.
  - Replace legacy runtime behavior assertions with canonical host/generation assertions.
  - Identify and delete dead compatibility callers rather than wrapping them.
  - Observable completion: repository code search shows only the declarations scheduled for deletion.
  - _Requirements: 3.1-3.10, 12.3-12.7_
  - _Boundary: Runtimebundle and tests_
  - _Depends: 3.5_
  - _Validation: `go test ./internal/infra/runtimebundle/... ./internal/stdhttp/... ./internal/testkit/... -run 'Build|Compatibility|Generation'`_

- [x] 4.2 Delete `Built`, compatibility `Build`, and legacy closer views
  - Remove `built.go`, the compatibility `Build` implementation, candidate `Closers`, and ledger `LegacyClosers` if no remaining supported consumer exists.
  - Preserve the resource ledger as the canonical lifecycle owner.
  - Remove obsolete comments, docs, and test fixtures.
  - Observable completion: no production type named `Built` or aggregate runtime closer list remains.
  - _Requirements: 2.8, 3.1-3.3, 3.8-3.10, 8.3-8.4_
  - _Boundary: Runtimebundle composition root_
  - _Depends: 4.1_
  - _Validation: `go test ./internal/infra/runtimebundle/... ./internal/archtest/... -run 'Built|BuildCompatibility|Closer|Ledger'`_

- [x] 4.3 Delete production `RunWithRuntime` and App-owned serve lifecycle
  - Migrate remaining supported server tests to the stable generation host.
  - Delete `RunWithRuntime`, its closer release path, and App-owned production serve lifecycle.
  - Retain only handler/server utilities used by canonical generation host serving.
  - Observable completion: one production data-plane serving API remains.
  - _Requirements: 3.2-3.3, 4.6-4.7, 8.6-8.7_
  - _Boundary: stdhttp and runtime host_
  - _Depends: 4.2_
  - _Validation: `go test -race ./internal/stdhttp/... ./cmd/lipstd/... -run 'GenerationHost|Shutdown|Server|NoDrop'`_

- [x] 4.4 Activate deleted-symbol gates and ratchet package budgets
  - Forbid production `Built`, compatibility `Build`, `RunWithRuntime`, `requestPlaneAsBuilt`, and legacy candidate closer projections.
  - Remove allowlists introduced in Phase 1.
  - Lower runtimebundle/stdhttp budgets to measured post-deletion values.
  - Observable completion: reintroducing any old symbol or equivalent compatibility direction fails architecture tests.
  - _Requirements: 3.1-3.10, 11.2-11.9_
  - _Boundary: Architecture tests_
  - _Depends: 4.2, 4.3_
  - _Validation: `go test ./internal/archtest/... -run 'RuntimeConvergence|DeletedSymbol|LineComplexity' && make arch-report`_

## Phase 5: Converge Startup, Host Ownership, and Validation

- [x] 5. Build one complete host from one config snapshot
  - _Completion: Tasks 5.1-5.5 complete; `BuildHost` owns one-snapshot startup, validation is unpublished, inspect operations are purpose-specific, and dual bootstrap/attachment paths are deleted._

- [x] 5.1 Add one-snapshot host construction RED tests
  - Add a controlled source hook proving multi-user/startup gates, generation 1, process runtime, reload active source/effective, and public fingerprint use one accepted snapshot.
  - Add partial-failure cleanup matrices for loader, process runtime, generation compile, publish, coordinator bind, and tracing.
  - Add architecture tests prohibiting production `AttachReloadHost` and multiple effective loads in serve/public Build.
  - Observable completion: current two-load/two-step path fails deterministic tests.
  - _Requirements: 4.1-4.8, 12.1-12.5_
  - _Boundary: Composition root, CLI, public facade, tests_
  - _Depends: 4.4_
  - _Validation: `go test -race ./cmd/lipstd/... ./internal/infra/runtimebundle/... ./pkg/lipruntime/... -run 'OneSnapshot|HostBuild|PartialCleanup|TOCTOU'`_

- [x] 5.2 Implement BuildHost as one owned startup transaction
  - Load and normalize one effective snapshot.
  - Evaluate CLI/access/startup gates before expensive resources.
  - Construct ProcessRuntime, compile/publish generation 1, bind reload state/coordinator, and return one Host.
  - Keep rollback internal and return no partially owned result on error.
  - Observable completion: both `cmd/lipstd serve` and public Build receive a complete Host from one call.
  - _Requirements: 2.1-2.8, 4.1-4.8, 8.6-8.7, 10.1-10.4_
  - _Boundary: Runtimebundle composition root_
  - _Depends: 5.1_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./cmd/lipstd/... ./pkg/lipruntime/... -run 'BuildHost|InitialGeneration|ReloadHost|Cleanup'`_

- [x] 5.3 Split explicit inspect and inventory operations (P)
  - Replace broad BootstrapResult outputs with purpose-specific inspection values.
  - Keep routes and inventory on the shared strict loader and standard registry without building process/generation runtime.
  - Remove serve-mode App construction and duplicate feature-surface merge.
  - Observable completion: inspect/routes/inventory commands expose only required artifacts and current outputs remain stable.
  - _Requirements: 4.6-4.9, 12.8_
  - _Boundary: CLI and composition root_
  - _Depends: 5.1_
  - _Validation: `go test ./cmd/lipstd/... ./internal/infra/runtimebundle/... -run 'Inspect|Routes|Inventory|FeatureSurface|Compatibility'`_

- [x] 5.4 Implement true unpublished ValidateDistribution
  - Use the same effective loader, ProcessRuntime builder, GenerationCompiler, and HTTP composer.
  - Do not construct Manager, generation IDs, active pointer, listeners, or retirement workers.
  - Roll back generation resources and close validation process resources deterministically.
  - Migrate `check-config` to this entrypoint.
  - Observable completion: tests prove zero publication while startup/reload rejection parity remains exact.
  - _Requirements: 5.1-5.6_
  - _Boundary: Config/wiring and CLI driving adapter_
  - _Depends: 5.2_
  - _Validation: `go test -race ./cmd/lipstd/... ./internal/infra/runtimebundle/... ./internal/core/configreload/... -run 'CheckConfig|ValidateDistribution|NoPublish|Parity|Rollback'`_

- [x] 5.5 Delete dual bootstrap and host attachment paths
  - Remove nullable composer architecture selection, serve-mode legacy products, `AttachReloadHost`, and duplicated caller cleanup.
  - Activate single-host/single-load architecture gates.
  - Lower bootstrap, public build, and command file budgets.
  - Observable completion: one standard Host build operation owns startup and one validation operation owns check-config.
  - _Requirements: 4.1-4.9, 5.1-5.6, 10.1-10.4, 11.2-11.4_
  - _Boundary: Composition root, CLI, public facade, architecture_
  - _Depends: 5.2-5.4_
  - _Validation: `go test ./internal/archtest/... ./cmd/lipstd/... ./internal/infra/runtimebundle/... ./pkg/lipruntime/... -run 'HostPath|ConfigLoad|Bootstrap|Attach|CheckConfig'`_

## Phase 6: Contract the Reload Coordinator by Ownership

- [x] 6. Separate reload concurrency, transaction, and state
  - _Completion: Tasks 6.1-6.5 complete; AttemptGate, AttemptRunner, ReloadState, and the reduced Coordinator have distinct ownership with race/soak coverage._

- [x] 6.1 Add AttemptGate RED concurrency suite
  - Specify atomic start registration, busy API result, bounded HUP pending/coalescing, shutdown rejection, cancellation, exact finish, and idle wait.
  - Use barriers and channels; prohibit polling and timing sleeps as synchronization.
  - Add race tests for TryStart/Finish/Wait/BeginShutdown interleavings.
  - Observable completion: current coordinator-only implementation cannot satisfy the isolated gate contract.
  - _Requirements: 6.1, 6.5-6.9, 13.2_
  - _Boundary: Runtimehost app orchestration and tests_
  - _Depends: 5.5_
  - _Validation: `go test -race ./internal/infra/runtimehost/... -run 'AttemptGate|Busy|Coalesce|Idle|Shutdown'`_

- [x] 6.2 Implement AttemptGate and remove idle polling
  - Create the attempt lease/completion signal under one lock transition.
  - Move active cancel, pending HUP, coalesced count, shutdown, and idle wait into the gate.
  - Delete the busy-before-armed window and timed polling loop.
  - Observable completion: gate tests pass under race and coordinator no longer owns attempt channel/once fields.
  - _Requirements: 6.1, 6.5-6.9_
  - _Boundary: Runtimehost app orchestration_
  - _Depends: 6.1_
  - _Validation: `go test -race ./internal/infra/runtimehost/... -run 'AttemptGate|Coordinator.*Shutdown|WaitForIdle'`_

- [x] 6.3 Extract AttemptRunner with deterministic outcome tests
  - Add table tests for read, source integrity, load, no-op, classify, compile, prepare, retention, publish, cancellation, panic, and rollback.
  - Move one-attempt workflow and internal error-to-canonical-result mapping into AttemptRunner.
  - Return immutable AttemptOutcome state updates; do not mutate history/status inside Runner.
  - Observable completion: runner tests require no signal/coalescing setup and coordinator contains no detailed stage switch.
  - _Requirements: 6.2, 6.4, 6.10-6.11, 7.7_
  - _Boundary: Runtimehost app orchestration_
  - _Depends: 6.2_
  - _Validation: `go test -race ./internal/infra/runtimehost/... -run 'AttemptRunner|Load|Classify|Compile|Rollback|Panic'`_

- [x] 6.4 Extract ReloadState and migrate status/history
  - Add concurrent tests for ActiveInput, Apply, Snapshot, last success/failure, no-op, source posture, model fingerprint, and defensive copies.
  - Move active effective/source and safe status/history to one state owner.
  - Keep observer side effects outside the state lock.
  - Observable completion: status tests instantiate ReloadState without Manager, Source, Loader, or Compiler.
  - _Requirements: 6.3-6.4, 7.1-7.8_
  - _Boundary: Runtimehost app orchestration and query state_
  - _Depends: 6.3_
  - _Validation: `go test -race ./internal/infra/runtimehost/... -run 'ReloadState|Status|History|SourcePosture|ModelGeneration'`_

- [x] 6.5 Reduce Coordinator to thin orchestration and ratchet its budget
  - Compose gate, runner, state, and observer.
  - Preserve host-owned timeout, caller detachment, queued follow-up loop, and result delivery.
  - Delete old state fields/helpers and lower `coordinator.go` to at most 300 non-test lines.
  - Observable completion: all focused reload, source-integrity, no-op, coalescing, shutdown, and soak tests pass with the smaller coordinator.
  - _Requirements: 6.1-6.11, 11.2-11.3, 13.2_
  - _Boundary: Runtimehost app orchestration and architecture_
  - _Depends: 6.2-6.4_
  - _Validation: `go test -race ./internal/infra/runtimehost/... ./internal/stdhttp/... -run 'Coordinator|Reload|Noop|SourceIntegrity|Retention|Shutdown|Soak' && go test ./internal/archtest/... -run 'CriticalFile'`_

## Phase 7: Consolidate Generation Lifecycle and Process Shutdown

- [x] 7. Establish one lifecycle truth per layer
  - _Completion: Tasks 7.1-7.4 complete; GenerationRuntime/resource ledger owns generation phases, Manager owns retirement, and Host owns process shutdown._

- [x] 7.1 Add lifecycle ownership and duplicate-idempotency RED gates
  - Inventory every `sync.Once`, closed flag, close result, quiesce result, retirement status, and shutdown coordinator around generation resources.
  - Add tests proving one resource owner handles rollback/quiesce/close and wrappers cannot cache a second result.
  - Add architecture checks for duplicate lifecycle guards around GenerationRuntime.
  - Observable completion: current CandidateRuntime/GenerationBundle/Generation layering is explicitly identified and failing against the target.
  - _Requirements: 8.1-8.10, 11.4, 12.1-12.5_
  - _Boundary: Runtimehost/runtimebundle lifecycle and tests_
  - _Depends: 6.5_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./internal/infra/runtimehost/... ./internal/archtest/... -run 'LifecycleOwner|DuplicateOnce|Quiesce|Close|Retire'`_

- [x] 7.2 Make GenerationRuntime/resource ledger the sole resource phase owner
  - Move or retain idempotency and cached phase results in one canonical owner.
  - Remove wrapper quiesce/close once guards and legacy close paths.
  - Preserve rollback-before-publication and retryable close-after-quiesce semantics.
  - Observable completion: each generation resource is quiesced/closed exactly once through one owner under fault and race tests.
  - _Requirements: 2.4-2.5, 8.1-8.4, 8.8_
  - _Boundary: Runtimebundle generation lifecycle_
  - _Depends: 7.1_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... -run 'ResourceLedger|GenerationRuntime|Rollback|Quiesce|Close|Retry'`_

- [x] 7.3 Move retirement scheduling under Manager ownership
  - Keep retirement policy separately testable but manager-owned.
  - Remove mutable second-authority retirement status and derive/emit results from generation transitions.
  - Preserve independent per-generation retirement, bounded retries, panic isolation, and no forced pin termination.
  - Lower generation and lifecycle file budgets toward targets.
  - Observable completion: Manager tests cover publish through retirement without callers constructing ad hoc lifecycle workers.
  - _Requirements: 8.1-8.5, 8.8-8.10, 11.2-11.3_
  - _Boundary: Runtimehost generation lifecycle_
  - _Depends: 7.2_
  - _Validation: `go test -race ./internal/infra/runtimehost/... -run 'Manager|Retire|Concurrent|Cleanup|Panic|Pinned'`_

- [x] 7.4 Make Host the sole process shutdown coordinator
  - Implement reject reload, wait candidate idle, stop admissions, retire generations, close process runtime, and tracing-last ordering in Host.
  - Migrate cmd and public facade to one `Host.Close`.
  - Delete duplicated rollback/shutdown orchestration and add ordering/race tests.
  - Observable completion: command/public packages do not call Manager or ProcessRuntime shutdown primitives directly.
  - _Requirements: 8.6-8.10, 10.1-10.4_
  - _Boundary: Composition root, CLI, public facade_
  - _Depends: 7.3_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./cmd/lipstd/... ./pkg/lipruntime/... -run 'HostClose|ShutdownOrder|Retry|ConcurrentClose|ReloadRace'`_

## Phase 8: Simplify the Public Facade and Quarantine Legacy Options

- [x] 8. Converge the public standard-runtime API
  - _Completion: Tasks 8.1-8.4 complete under the approved alpha-stage compatibility amendment; the facade is host-backed and public options use canonical registrations only._

- [x] 8.1 Reduce Runtime to one host-facing dependency
  - Add public external tests for ExecutorView, Ready, capabilities, reload/status, snapshot refresh, and Close retry/idempotency.
  - Replace concrete host internals and build-time booleans with one private host interface and derived capabilities.
  - Keep only facade synchronization required by the documented public contract.
  - Lower `pkg/lipruntime` build/facade file budget to at most 150 lines.
  - Observable completion: public Runtime contains no Manager, ProcessRuntime, Coordinator, source, or tracing ownership fields.
  - _Requirements: 10.1-10.4, 11.2-11.3_
  - _Boundary: Public SDK facade_
  - _Depends: 7.4_
  - _Validation: `go test -race ./pkg/lipruntime/... ./testdata/enterprise_module/... -run 'Facade|External|Capability|Reload|Close'`_

- [x] 8.2 Isolate the current-major legacy option adapter
  - Characterize every deprecated field conversion and error.
  - Move legacy pairing/filtering/ID logic behind one explicit outer adapter before canonical Options normalization and HostBuilder.
  - Ensure canonical construction accepts descriptor-bound registrations only.
  - Add an architecture test forbidding legacy option types below the public adapter.
  - Observable completion: no internal runtimebundle/runtimehost code can observe deprecated provider/rater fields.
  - _Requirements: 10.5-10.7, 12.5_
  - _Boundary: Public SDK compatibility adapter_
  - _Depends: 8.1_
  - _Validation: `go test ./pkg/lipruntime/... ./internal/archtest/... -run 'LegacyOptions|Registration|Boundary|Canonical'`_

- [x] 8.3 Publish the public migration contract and removal gate
  - Document field-by-field migration to request, attempt, concurrency, and rater registrations.
  - Mark the final legacy-support release and next compatible major deletion target.
  - Add tests preventing new fields or behavior in the legacy adapter.
  - Observable completion: maintainers and external module fixtures have an unambiguous migration path.
  - _Requirements: 10.7-10.10, 12.8_
  - _Boundary: Public docs and tests_
  - _Depends: 8.2_
  - _Validation: `go test ./pkg/lipruntime/... ./internal/qa/... -run 'Legacy|Migration|Docs|External'`_

- [x] 8.4 Remove legacy public options at the compatible major boundary
  - Delete deprecated fields, LegacyOptions/BuildLegacy adapter if used, descriptor-family filtering, legacy IDs, and legacy normalization tests.
  - Update external module fixtures to canonical registrations.
  - Activate architecture gates requiring one public options model.
  - Observable completion: canonical `Options` and normalization contain one registration language with no legacy branches.
  - _Requirements: 10.5-10.10, 11.4-11.9_
  - _Boundary: Public SDK major-version change_
  - _Depends: 8.3, alpha-stage breaking-change approval (2026-07-25)_
  - _Validation: `go test ./pkg/lipruntime/... ./internal/archtest/... -run 'Options|Registration|External|LegacyAbsent' && (cd testdata/enterprise_module && GOWORK=off go test ./... -count=1)`_

## Phase 9: Ratchet Budgets, Documentation, and Release Certification

- [x] 9. Prove the codebase is smaller and behaviorally unchanged
  - _Completion: Tasks 9.1-9.5 complete. `baseline/final-release-evidence.md` records PASS for approved behavior, race, fuzz, soak, architecture, security, QA, shrinkage, and benchmark gates at certified implementation SHA `a5a2d375c767b3dad8225de0879f5a6c6f4b1ee5`._

- [x] 9.1 Remove obsolete compatibility tests and stale documentation
  - Delete tests that only preserve removed production paths.
  - Retain or rewrite externally observable behavior tests against Host/GenerationRuntime.
  - Update architecture, package map, runtime flow, reload, enterprise extension, and release-gate documents.
  - Observable completion: documentation names one process runtime, one generation runtime, one host, and one reload contract.
  - _Requirements: 12.7-12.8_
  - _Boundary: Tests and docs_
  - _Depends: 8.1-8.4_
  - _Validation: `go test ./internal/qa/... ./cmd/lipstd/... -run 'Docs|Architecture|Examples|Compatibility'`_

- [x] 9.2 Apply final critical-file and package ratchets
  - Set coordinator ≤300, generation ≤400, candidate compilation ≤350, process runtime construction ≤300, and public facade build ≤150, or remove the named file.
  - Lower affected package budgets below the reviewed baseline.
  - Require rationale plus an old-path deletion milestone for any future increase.
  - Observable completion: architecture tests enforce the final post-contraction sizes.
  - _Requirements: 11.1-11.4, 11.7-11.9_
  - _Boundary: Architecture tests_
  - _Depends: 9.1_
  - _Validation: `go test ./internal/archtest/... -run 'CriticalFile|LineComplexity|RuntimeConvergence'`_

- [x] 9.3 Verify net shrinkage and dependency simplification
  - Run the architecture report and compare to Phase 1 baseline.
  - Prove at least 800 affected non-test production lines removed.
  - Record deleted symbols, reduced propagation points, fan-out/fan-in changes, and zero remaining parallel runtime paths.
  - Fail completion if shrinkage comes only from moving code or excluding files.
  - Observable completion: committed report satisfies 11.5-11.9.
  - _Requirements: 11.5-11.9_
  - _Boundary: Architecture metrics and docs_
  - _Depends: 9.2_
  - _Validation: `make arch-report`_

- [x] 9.4 Run focused behavior, race, fuzz, and soak certification
  - Run reload no-drop, source integrity, rollback, restart-required, retention, management, SIGHUP, lifecycle, shutdown, public facade, and external module suites.
  - Run full race and bounded reload soak with goleak.
  - Run config/reload/lifecycle fuzz smoke and architecture scans.
  - Observable completion: all mandatory focused gates pass with no behavior waiver.
  - _Requirements: 1.1-1.10, 13.1-13.5, 13.9-13.10_
  - _Boundary: Tests and release validation_
  - _Depends: 9.3_
  - _Validation: `make test && make test-race && make test-fuzz && go test -tags=precommit -run '^TestRuntimeConfigReloadSoak$' -count=1 -v ./internal/stdhttp/...`_

- [x] 9.5 Run benchmark/security gates and publish final release evidence
  - Compare repeated equivalent-host benchmarks with Phase 1 via `benchstat`.
  - Enforce the 10 percent candidate compile time/allocation threshold or obtain explicit evidence-based approval.
  - Run lint, module verification, govulncheck, marker scans, and architecture report.
  - Record exact commands, platform, Go version, base/final SHA, metrics, deletions, public API migration status, and known limitations.
  - Observable completion: final evidence demonstrates both behavior preservation and material maintainability improvement.
  - _Requirements: 13.1-13.10_
  - _Boundary: Release validation and docs_
  - _Depends: 9.4_
  - _Validation: `make bench && golangci-lint run --timeout=10m && go mod verify && go run golang.org/x/vuln/cmd/govulncheck@latest ./...`_
