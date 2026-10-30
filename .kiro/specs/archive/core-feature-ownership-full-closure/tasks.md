# Implementation Plan

## Execution Contract

This plan is written for an instruction-following implementation agent. **Do not redesign the architecture while executing it.** Follow the target ownership and package boundaries in `design.md`. When a task says STOP, stop that wave and repair the SDD rather than inventing a workaround.

### Hard prerequisite

Do **not** execute production tasks merely because this SDD or PR #557 is merged. Start only after the implementation of `.kiro/specs/archive/pre-oss-core-slimming/` is merged/certified and its Task 8.3 residual ownership inventory is present on `main`.

### Permanent rules for every task

- TDD/characterization first: add or identify the failing/behavior-locking test before changing production ownership.
- Keep behavior changes out of structural migration PRs unless the requirement explicitly calls for a contract migration.
- Never import `internal/core` or `internal/infra/runtimebundle` from a concrete feature package.
- Never add a feature ID branch to core/runtimebundle.
- Never add a service locator, reflection DI, `map[string]any` dependency bag, generic workflow engine, or dynamic feature lookup.
- Preserve `ProcessServices`, `ResourceLedger`, `runtimehost.Manager`, request/attempt and B2BUA owners; do not create parallel lifetime authorities.
- Keep published generations immutable; candidate failure must leave the last-good generation serving.
- Do not weaken no-retry/failover-after-client-visible-output semantics.
- A process feature resource has exactly one constructor path and one physical cleanup owner at every merged checkpoint. Do not rely on idempotent closers to make dual ownership safe.
- Keep each implementation PR <=100 changed files unless the repository's explicit large-change authorization is applied. Prefer mechanical-move and integration PRs over one giant patch.
- After each major wave, run focused tests plus `make quality-checks` and `make arch-report` (or the current equivalent) before proceeding.

## Dependency Graph

```text
0 prerequisite gate
  -> 1 post-first census/baselines
     -> 2 standard featurehost foundation
        -> 3 compaction-continuity
        -> 4 conversation-view split
        -> 5 interleaved-thinking split
        -> 6 keep-warm extraction
        -> 7 terminal-policy extraction
     -> 8 public host bindings / lipruntime cleanup
     -> 9 feature config ownership
     -> 10 residual support/compose consolidation
     -> 11 final architecture ratchets + budgets + probes + docs
     -> 12 independent review + merged-main certification
```

For one weak executor, run sequentially in the numbered order. Separate stronger agents may prepare characterization/move-only branches for Tasks 3–7 after Task 2 is merged, but their featurehost/runtime integration must be rebased and merged sequentially.

---

- [x] 0. Verify the predecessor implementation is actually complete
- [x] 0.1 Gate execution on the implemented `pre-oss-core-slimming` SDD
  - Read the archived/completed predecessor spec (`.kiro/specs/archive/pre-oss-core-slimming/`) and its closeout evidence on the **current implementation branch/main**, not only PR #557 text.
  - Locate the predecessor Task 8.3 residual ownership inventory and record its path and exact baseline SHA in this SDD's implementation tracker/PR description.
  - Assert the predecessor's required end-state before changing production code: generated-only standard planes; retired `internal/core/toolcallrepair`, `internal/core/secretguard`, and concrete detector package absent as specified by the final predecessor; zero `runtimebundle -> internal/plugins/features/*` production imports; external feature fixture present; predecessor core budget ratchet active.
  - Add no compatibility workaround if any assertion fails. STOP and finish/fix the predecessor instead.
  - _Requirements: 1.1, 1.2_
  - _Boundary: tests / architecture gate_
  - _Depends: implemented and certified pre-oss-core-slimming_
  - _Validation: focused predecessor architecture tests; `make arch-report`; `git grep` checks from predecessor closeout_

- [x] 1. Freeze the post-first ownership and behavior baseline
- [x] 1.1 Generate the authoritative production ownership census
  - Recursively enumerate production Go packages/files under `internal/core`, `internal/infra/*compose`, `internal/standardplugins`, `internal/pluginreg`, `internal/featurebundle`, `internal/infra/runtimebundle`, `pkg/lipruntime`, and one-feature support packages outside `internal/plugins/features`.
  - Start from predecessor Task 8.3 inventory; refresh it against current `main` and record exact consumers/importers for every residual row.
  - Classify each row using the six categories in Requirement 1 and the Core Admission Test in `design.md`.
  - Expected known rows must include: compaction continuity coordination/state; conversation-view projection + steering services; interleaved thinking/state/config; keep-warm; terminal-decision policy; feature-specific public host options; dedicated reasoning/secretguard/compaction compose helpers; one-feature support such as reasoning replay if still present.
  - For every process-scoped feature resource, additionally record: current constructor, current field/holder, current lifecycle/close registration site, borrowed lower-level dependencies, and the exact later task that will atomically transfer ownership to featurehost. This becomes the executable Wave-2 transition table; no process feature resource may be omitted.
  - Do not classify a mixed package as core. Mark individual responsibilities that must split.
  - Observable completion: zero `unknown`; temporary `mixed/needs split` rows are allowed only when linked to Tasks 3–10 and named by responsibility.
  - _Requirements: 1.3, 1.4, 1.5, 1.6, 2.2_
  - _Boundary: architecture evidence / tests_
  - _Depends: 0.1_
  - _Validation: `go list -deps`/repository import scan; `make arch-report`; inventory self-check test if project has a machine-readable manifest_

- [x] 1.2 Add behavior/lifetime characterization before movement
  - Pin compaction-continuity parent isolation, revision/CAS, job/injection stale-result behavior and reload concurrency.
  - Pin conversation projection/reassertion, never-backend exclusion, anchor fallback/fail-closed, persistence parity and no-plaintext diagnostics.
  - Pin interleaved hidden/visible stream behavior, thinker cycle, memo injection budget, cancellation and visible-output commitment.
  - Pin keep-warm begin/end/committed-turn, scheduling/quiesce/accounting and reload behavior.
  - Pin terminal-policy authority, bounds/capacity, actor tri-state precedence, snapshot and close semantics.
  - Pin current public reasoning-host composition and old config behavior to support later migration tests.
  - Add/identify an ownership-counting test seam for process feature resources: a successful process build must create each enabled process resource once; overlapping generation compilation must not create another process instance; process shutdown must physically close each closable resource once.
  - Reuse existing tests when sufficient; add missing RED/characterization cases only. Do not duplicate giant suites.
  - _Requirements: 2.1, 2.2, 3.4, 4.5, 5.5, 6.5, 7.5_
  - _Boundary: tests_
  - _Depends: 1.1_
  - _Validation: focused package tests under race where state is concurrent; process ownership count tests_

- [x] 1.3 Capture structural and performance baselines
  - Record current non-test LOC for `internal/core`, `internal/infra/runtimebundle`, `internal/standardplugins`, and the planned `featurehost` tree (0 before creation).
  - Record current per-feature fields in `ProcessServices`, executor build/config inputs and `pkg/lipruntime.Options`.
  - Capture predecessor extension/request-snapshot allocation benchmarks and focused conversation projection/keep-warm benchmarks if present.
  - Record current changed-file/change-surface count needed to add one host-bound standard feature with existing host facts; this becomes the final reduction proof.
  - _Requirements: 12.4, 12.5, 12.6, 12.7_
  - _Boundary: tests / architecture evidence_
  - _Depends: 1.1_
  - _Validation: `make arch-report`; focused `go test -bench ... -benchmem`; deterministic grep/change-surface script_

---

- [x] 2. Establish the standard-distribution featurehost composition boundary
- [x] 2.1 Add the small `internal/standardplugins/featurehost` process facade
  - Create the package structure from `design.md`; keep `runtime.go`, `process.go`, `generation.go`, `inputs.go` small and move concrete feature integration to per-feature files/subpackages.
  - `ProcessInput` may contain only generic process capabilities required by the standard feature set. It must not accept `*runtimebundle.BuildOptions`, `*runtimebundle.ProcessServices`, full backend maps, database pool registries, or an `any` services map.
  - Implement `NewProcess` with fail-before-escape ownership: every successfully constructed owned feature resource is recorded before later construction can fail; failure unwinds in reverse order.
  - Implement idempotent `Runtime.Close` for its own resources only. Borrowed `BackgroundAux`, DB pools, secure-session stores, backend hosts and other generic process resources are never closed here.
  - During this foundation task do not construct legacy process feature resources merely because their final owner will be featurehost. The explicit handoff happens in Tasks 3–7/10 as assigned by Task 1.1.
  - Add tests for successful close, partial-construction rollback, close idempotency and cleanup error aggregation/order matching current owner conventions.
  - _Requirements: 2.2, 2.3, 8.1, 8.4, 8.5_
  - _Boundary: standard-distribution composition / process lifecycle_
  - _Depends: 1.2, 1.3_
  - _Validation: `go test -race ./internal/standardplugins/featurehost/...`_

- [x] 2.2 Add generation composition and fixed core-port output
  - Add `GenerationInput`/`GenerationOutput` per design using ordinary `FeatureBundle`/`FrozenPlaneSet`, lifecycles and only the minimal fixed consumer-owned core interfaces required by Tasks 3–7.
  - Do not add a generic service map or `Resolve/Get` API. Do not expose featurehost to request code.
  - Generation output must be immutable/defensively copied where current contracts require it.
  - Candidate failure must not mutate process feature state in a way that changes the last-good generation unless the existing feature explicitly has process-shared semantics and characterization proves it.
  - `CompileGeneration` must not construct any process-scoped resource listed in the Task 1.1 transition table.
  - Add tests for two overlapping generations, failed candidate compile, last-good isolation, and zero process-resource construction during generation compilation.
  - _Requirements: 2.3, 8.3, 8.4, 8.5_
  - _Boundary: standard-distribution composition / generation_
  - _Depends: 2.1_
  - _Validation: featurehost tests; existing generation pin/reload tests; ownership-counting tests_

- [x] 2.3 Integrate one featurehost handle into `ProcessServices` with an explicit interim ownership map
  - Add exactly one `StandardFeatures *featurehost.Runtime` (final spelling may follow repository style) to `ProcessServices`.
  - Before wiring it, materialize the Task 1.1 per-resource transition table in the implementation evidence/test fixture with: resource/class, current constructor, current lifecycle/close registration, current physical owner, featurehost transfer task, and whether the resource is closable.
  - Construct featurehost after the generic dependencies it borrows are ready and before any generation that consumes it can be published.
  - Register exactly one `StandardFeatures.Close` with the process owner in the correct dependency order. At this checkpoint that close may own zero migrated process resources; it MUST NOT close resources still owned by legacy fields/closers.
  - Do not remove existing per-feature fields yet. For each untransferred resource, the legacy constructor/field/close path remains the sole owner and featurehost must not construct or adopt a second instance.
  - If featurehost temporarily needs to observe a legacy resource, pass an explicitly non-owning reference and exclude it from `Runtime.Close`; prefer no reference where possible.
  - For every later transfer, require an atomic handoff: enable the featurehost constructor and remove/disable the legacy constructor plus legacy close registration in the same integration change. Never leave both paths active in a merged checkpoint and never rely on idempotent close for safety.
  - Extend the ownership-counting tests from Task 1.2 to assert, for every enabled process resource: exactly one successful construction, zero duplicate construction across two overlapping generations, and exactly one physical close at process shutdown. Add a negative test that intentionally wires both constructor paths and proves the ownership guard/test fails.
  - Add an architecture test that forbids runtimebundle from accessing featurehost private feature resources.
  - _Requirements: 2.2, 2.3, 8.2, 8.5_
  - _Boundary: runtimebundle composition root / process ownership transition_
  - _Depends: 2.2_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./internal/standardplugins/featurehost/...`; process shutdown + ownership-counting tests_

- [x] 2.4 Route simple predecessor compose adapters through featurehost
  - Move/call the predecessor-created reasoning and secretguard generation composition through featurehost; retain ordinary extension planes as their execution output.
  - These are generation-composition adapters: do not create a new process resource or process closer merely because invocation moves under featurehost. Any feature lifecycle remains on the existing `ResourceLedger` path.
  - Do not change feature behavior or public host API in this task.
  - Generic runtimebundle must call the single featurehost generation facade rather than individual reasoning/secretguard compose functions.
  - If dedicated compose packages remain temporarily, only featurehost imports/calls them.
  - Re-run the Task 2.3 ownership-counting test and confirm counts for all process resources are unchanged by this generation-only routing change.
  - _Requirements: 8.1, 8.3, 11.2, 11.3_
  - _Boundary: standard-distribution composition_
  - _Depends: 2.3_
  - _Validation: reasoning/secretguard parity tests; ownership-counting test; `git grep` proving runtimebundle does not call dedicated feature compose packages_

---

- [x] 3. Make compaction-continuity domain/state feature-owned
- [x] 3.1 Move compaction-continuity domain files mechanically under the feature
  - Move coordinator/types/capsule/jobs/injection/preview/state code and their focused tests from the post-first equivalent of `internal/core/compactioncontinuity` into `internal/plugins/features/compactioncontinuity/state` (or the smallest feature-local subpackage matching design).
  - First commit should be mechanical with minimal import/name edits; do not change algorithms, constants, persistence keys or error semantics.
  - Feature-local package may import `pkg/lipapi`, `pkg/lipsdk/*` and feature-local packages only; no core/runtimebundle imports.
  - _Requirements: 3.1, 3.2, 11.4_
  - _Boundary: feature implementation_
  - _Depends: 2.4_
  - _Validation: moved package tests including race/reload characterization_

- [x] 3.2 Rebuild authoritative parent binding as a featurehost adapter
  - Move/refactor feature-specific pieces of `compactioncompose` parent-port logic into `internal/standardplugins/featurehost/compaction`.
  - Adapter converts already-authoritative core/session/principal facts into the feature's opaque branch binding and implements the existing feature `ParentPort` contract.
  - Do not expose B2BUA/secure-session mutable stores to the feature. Do not let feature state choose an A-leg/branch from child or untrusted request hints.
  - Preserve source/capsule revisions, preview intent binding, pending job validation, injection CAS and stale-result rejection.
  - _Requirements: 3.3, 3.4, 2.1_
  - _Boundary: standard feature adapter / feature_
  - _Depends: 3.1_
  - _Validation: compaction continuity security/parent-port tests; adversarial cross-session/A-leg tests_

- [x] 3.3 Atomically move compaction process ownership under featurehost and delete generic fields/package
  - Featurehost constructs/retains the process-shared coordinator and parent adapter.
  - In the same integration change, remove/disable their legacy constructor and lifecycle/close registration before enabling the featurehost-owned path; update the Task 2.3 transition table from `legacy` to `featurehost`.
  - If the post-first concrete compaction detector/support is still represented by a per-feature `ProcessServices` field, classify it now: if it is part of the same compaction standard-feature process service, transfer its construction/ownership in this task; if Task 1.1 proves it is a distinct support responsibility, assign its atomic handoff to Task 10 and keep it explicitly legacy-owned until then. It may not remain unassigned through Task 10.4.
  - Remove `BranchCoordinator`, compaction-continuity parent-port and every process field transferred here from `ProcessServices`/generic build inputs.
  - Compile the compaction-continuity `FeatureBundle` through featurehost and preserve existing `PlaneCompactionPreservers` behavior.
  - Delete the retired core package and add a resurrection/import ratchet.
  - If coordinator/detector gained a real Close/worker after baseline, STOP and update lifecycle design before wiring cleanup.
  - Re-run counted ownership tests: one process construction, one physical close when closable, zero generation-time duplicates.
  - _Requirements: 3.1, 3.5, 3.6, 8.2_
  - _Boundary: featurehost / runtimebundle cleanup / process ownership / archtest_
  - _Depends: 3.2_
  - _Validation: full compaction tests, generation reload tests, ownership-counting tests, archtest_

---

- [x] 4. Split conversation projection kernel from steering/state services
- [x] 4.1 Extract the pure kernel package without changing behavior
  - Create `internal/core/conversationprojection` (or final reviewed spelling) containing only semantic identity, exclusion filtering, pure projection/reassertion, anchors/provenance and immutable projection DTOs required by core.
  - Move tests that prove these pure invariants with it.
  - No store, writer, DB adapter, config default, metrics implementation or SDK command handler may enter this package.
  - Keep `Project`/reassertion request hot path free of new storage reads, locks, reflection or featurehost lookups.
  - _Requirements: 4.1, 4.4_
  - _Boundary: core kernel_
  - _Depends: 3.3_
  - _Validation: focused projection tests/benchmarks; allocation comparison to Task 1.3_

- [x] 4.2 Move steering/nonforwardable mutable services and persistence outside core
  - Move steering CRUD/state, placement/missing-anchor policy, writer/registrar services, persistence/store contracts/adapters and feature-specific diagnostics to `internal/infra/conversationview/...` as designed.
  - Preserve persisted schema/table compatibility unless an existing migration mechanism explicitly requires a schema move.
  - Keep `pkg/lipsdk/steering`, `nonforwardable`, `localturn` contracts stable; adapters translate to outside-core services.
  - Core receives only an immutable projection snapshot/read view prepared by the external service.
  - _Requirements: 4.2, 4.3, 4.5_
  - _Boundary: driven adapters / feature-support infrastructure_
  - _Depends: 4.1_
  - _Validation: memory/SQLite/Postgres store contract tests; SDK adapter tests; projection integration tests_

- [x] 4.3 Atomically move construction to featurehost and remove the mixed old core package
  - Featurehost constructs the conversation-view state/services and supplies the narrow snapshot/services consumed by core.
  - In the same integration change, remove any legacy process-level constructor/close registration for the transferred service while preserving generic DB pool ownership as borrowed; update the Task 2.3 transition table and ownership-counting test.
  - Delete old `internal/core/conversationview` once all admitted kernel files live in `conversationprojection` and non-kernel files are outside core.
  - Add package ownership ratchets: core projection cannot import outside conversation state/service; outside service may import projection DTOs but not runtime internals.
  - Update runtime executor fields from concrete conversation-view stores/writers to minimal interfaces/DTOs.
  - _Requirements: 4.1, 4.2, 4.6, 8.2_
  - _Boundary: featurehost / core / infra / process ownership_
  - _Depends: 4.2_
  - _Validation: runtime conversation-view integration tests; ownership-counting tests; archtest; DB parity suites_

---

- [x] 5. Split interleaved-thinking routing authority from UX processing
- [x] 5.1 Separate route-cycle state from memo feature state
  - Audit every post-first `interleavedstate` field consumer using the Task 1.1 census.
  - Keep `Role`, selector/cycle sequence and cursor in core only where routing/continuity directly require them.
  - Move memo payload/reference/budget semantics to `internal/plugins/features/interleavedthinking/state` when not required by route selection.
  - Preserve serialized durable compatibility using a persistence compatibility DTO when current rows combine cycle+memo state; do not retain feature algorithms in core to avoid a data migration.
  - Add round-trip tests for old/current stored values.
  - _Requirements: 5.1, 5.4, 5.5_
  - _Boundary: core routing state / feature state / persistence adapter_
  - _Depends: 4.3_
  - _Validation: B2BUA/continuity memory+DB state tests_

- [x] 5.2 Move prompt/memo/shape/sanitize implementation to the interleaved feature
  - Create/expand `internal/plugins/features/interleavedthinking` and mechanically move built-in instructions, instruction-file validation/loading, memo extraction/bounds/storage, executor memo injection and visible-stream sanitization.
  - Define feature-owned `Processor`, per-turn contract and DTOs using only standard library, `pkg/lipapi`, `pkg/lipsdk/*` and feature-local types. These types are deliberately separate from the later core consumer interface; the feature package must never import `internal/core/runtime` just to satisfy that interface.
  - Do not move selector parsing, thinker cycle planning, B-leg opening, failover or output commitment.
  - Add a recursive import-boundary test over the entire interleaved feature tree rejecting `internal/core`, `internal/infra/runtimebundle`, and `internal/standardplugins/featurehost` imports.
  - _Requirements: 5.1, 5.2, 11.4_
  - _Boundary: feature implementation_
  - _Depends: 5.1_
  - _Validation: moved pure tests; prompt/memo/sanitize fuzz where existing; feature import-boundary test_

- [x] 5.3 Introduce the narrow core `InterleavedProcessor` port and explicit featurehost adapter
  - Add the smallest runtime-owned `InterleavedProcessor`/per-turn interface matching the final methods actually needed by current interleaved stream orchestration. Start from design's `BeginTurn`/turn object shape and delete any method not used by existing orchestration.
  - Keep all core interface input/output types core-owned and minimal. Prefer a turn object that retains memo feature state internally so core does not carry memo text; if a core `InterleavedMemo`/reference DTO remains necessary for durable continuity, it may contain only the bounded fields core actually persists/coordinates.
  - Implement `internal/standardplugins/featurehost/interleaved.go` as the **sole adapter** between the core-owned interface and the feature-owned `interleavedthinking.Processor`/`Turn` contracts. The feature does not implement/import the core interface directly.
  - Map every operation explicitly as specified in design: `BeginTurn`, `ShapeThinker`, `ObserveThinkerEvent`, `FinalizeThinker`, and `ShapeExecutor`. Conversion may copy canonical `lipapi.Call`/`lipapi.Event` directly subject to existing clone rules, but routing/core enums and memo/reference DTOs require explicit field-by-field conversion. No `any`, reflection, unchecked type assertion, core store pointer, or generic service lookup.
  - Pass contexts unchanged. Preserve event order/visibility, memo budget/dedup semantics, and existing `errors.Is`/`errors.As` behavior. The adapter must not swallow errors, invent retry/fail-open policy, spawn workers, or take ownership of process resources.
  - Featurehost creates the generation-bound feature processor, wraps it in the adapter, and supplies only the core interface to executor construction. Nil processor means feature disabled and must preserve current disabled behavior.
  - Add compile-time assertions that the featurehost processor and turn adapters implement the core interfaces.
  - Add adapter unit tests with a fake feature processor that assert every input/output field mapping and error path for all five operations, plus context cancellation, nil/disabled behavior, returned event slice isolation as currently required, and the minimized memo/no-memo path selected by implementation.
  - Add runtime integration tests proving hidden/visible/cancellation/failure/output-commit behavior remains identical and an architecture test proving the concrete feature tree still has zero core imports.
  - _Requirements: 5.2, 5.3, 5.5, 8.3_
  - _Boundary: core consumer port / featurehost type adapter / feature implementation boundary_
  - _Depends: 5.2_
  - _Validation: featurehost interleaved adapter tests + compile assertions; runtime interleaved tests hidden/visible/cancellation/failure; recursive feature import archtest_

- [x] 5.4 Delete `internal/core/interleavedthinking` and ratchet the split
  - Remove the old core implementation after all call sites use routing state + processor port.
  - Add archtest forbidding feature prompt/memo/config defaults in core packages and forbidding core imports of the feature.
  - Preserve/extend the adapter compile/import tests from 5.3 so future interface changes cannot force the feature to import core.
  - Confirm no generic workflow/multi-step engine was added.
  - _Requirements: 5.2, 5.6, 12.1, 12.2_
  - _Boundary: core cleanup / archtest_
  - _Depends: 5.3_
  - _Validation: `go test ./internal/core/routing/... ./internal/core/runtime/... ./internal/plugins/features/interleavedthinking/... ./internal/standardplugins/featurehost/...`; archtest_

---

- [x] 6. Extract prompt-cache keep-warm policy from core
- [x] 6.1 Move keep-warm policy/scheduler/manager into a standard feature
  - Create `internal/plugins/features/keepwarm` and move current config/policy/manager/registry/scheduler/lifecycle/accounting/admin/orchestrator logic mechanically with tests.
  - Preserve `pkg/lipsdk/promptcache` as provider-neutral observation/control contract; do not move scheduling policy into SDK.
  - Feature package must not import core/runtimebundle.
  - _Requirements: 6.1, 6.2, 6.4_
  - _Boundary: feature implementation_
  - _Depends: 5.4_
  - _Validation: moved keepwarm unit/race tests_

- [x] 6.2 Bind core lifecycle facts through `PromptCacheMaintenance`
  - Add the minimal runtime consumer interface from design for `BeginRealTurn`, `EndSession`, and committed successful turn facts actually required by current code.
  - Prefer featurehost/lifecycle ownership for `RunDue` and quiesce; add them to the core port only if current authoritative call point truly remains core after integration.
  - `PromptCacheCommittedTurn` must use canonical/SDK DTOs only.
  - Featurehost binds the keep-warm implementation and supplies the interface; nil disables behavior.
  - _Requirements: 6.3, 6.5, 8.3_
  - _Boundary: core consumer port / featurehost_
  - _Depends: 6.1_
  - _Validation: runtime committed-turn/session tests; keepwarm scheduler/quiesce tests_

- [x] 6.3 Atomically remove keep-warm fields/package from generic process/core
  - Enable featurehost ownership of keep-warm process/generation resources and in the same integration change remove their legacy process constructor/lifecycle registration and `KeepwarmPolicy`, `KeepwarmRegistry` and equivalent concrete fields from `ProcessServices`/executor config.
  - Update the Task 2.3 transition table and counted ownership test; require one construction and one physical close/quiesce sequence where applicable.
  - Delete `internal/core/keepwarm` and add resurrection/import ratchet.
  - Verify cleanup/quiesce order: keep-warm dependents stop before borrowed provider control/process resources close; no synchronous provider control is added to session end.
  - _Requirements: 6.1, 6.5, 8.2, 12.2_
  - _Boundary: runtimebundle cleanup / process ownership / archtest_
  - _Depends: 6.2_
  - _Validation: process shutdown/reload + ownership-counting tests under race; archtest_

---

- [x] 7. Move terminal-decision mutable session policy outside core
- [x] 7.1 Move the bounded actor policy store to standard feature infrastructure
  - Move current terminal policy store/tests to `internal/standardplugins/featurehost/sessionpolicy` unless Task 1.1 proves a second independent feature already uses the exact same semantics; only then use `internal/infra/sessionfeaturepolicy`.
  - Preserve key bounds, capacity, authority match, client/operator tri-state precedence, revisions and idempotent close.
  - Do not put terminal provider execution logic into this store.
  - At this mechanical move stage keep the legacy constructor/close owner authoritative until Task 7.3 performs the atomic handoff; do not register the moved store with featurehost twice.
  - _Requirements: 7.1, 7.2, 7.5_
  - _Boundary: standard feature infrastructure_
  - _Depends: 6.3_
  - _Validation: moved store tests under race; ownership-counting test remains on legacy owner_

- [x] 7.2 Replace core mutable policy dependency with effective snapshot reader
  - Add the narrow runtime `TerminalPolicyReader`/query/snapshot contract from design (or smaller if existing request-admission structure can carry the effective value directly).
  - Featurehost exposes an adapter for the moved store but must not become its physical owner until the Task 7.3 constructor/closer handoff is complete.
  - Core request admission sees effective enabled + revision only; actor-specific mutation and key-map ownership stay outside.
  - HTTP/admin mutation endpoints depend on an outside-core application adapter/store, not core implementation.
  - Preserve the existing admission-only lookup invariant: no terminal/stream hot-path mutable policy reads may appear.
  - _Requirements: 7.2, 7.3, 7.4, 8.3_
  - _Boundary: core consumer port / stdhttp adapter / featurehost_
  - _Depends: 7.1_
  - _Validation: request admission tests; HTTP/admin policy tests; terminal provider conflict/chokepoint tests; existing terminal-decision architecture ratchets still green before ownership switch_

- [x] 7.3 Atomically transfer terminal-policy ownership, delete core package, and migrate existing architecture ratchets
  - Enable featurehost/sessionpolicy process construction/ownership and in the **same integration change** remove the legacy `terminaldecisionpolicy.NewStore(...)` construction and legacy close registration from `NewProcessServices` (or the post-first equivalent).
  - Remove `TerminalDecisionPolicy` concrete field from `ProcessServices` and any direct concrete type in executor build inputs; update the Task 2.3 transition table and counted ownership test to require exactly one store construction and one physical close.
  - Delete `internal/core/terminaldecisionpolicy`; add/retain a resurrection ratchet.
  - **Migrate `internal/archtest/terminal_decision_architecture_ratchets_red_test.go` explicitly** rather than allowing the suite to fail on stale ownership assertions:
    - replace the current `ProcessServices.TerminalDecisionPolicy *terminaldecisionpolicy.Store` expectation with an assertion that `ProcessServices` owns only `StandardFeatures *featurehost.Runtime` and contains no terminal-policy concrete field;
    - replace the current `terminaldecisionpolicy.NewStore(...)`-in-`NewProcessServices` assertion with an assertion that the single mutable terminal-policy store constructor exists only under `internal/standardplugins/featurehost/sessionpolicy`/featurehost process construction and is not constructed by generation compilation;
    - adapt `TestTask81TerminalDecisionPolicyLookupStopsAtAdmission` (or its renamed repository-consistent equivalent) from direct `TerminalDecisionPolicy.Snapshot(...)` symbols to the final `TerminalPolicyReader.Effective(...)`/admission snapshot seam while preserving the invariant that only request admission performs the mutable/effective lookup and stream/terminal hot paths do not;
    - preserve the existing exclusive `pkg/lipsdk/terminaldecision` provider-slot, canonical writer/continuation chokepoint, diagnostics-bound and other generic terminal-decision ratchets; do not delete the file wholesale merely because ownership moved.
  - Add a focused architecture/ownership test proving the store constructor is process-owned under featurehost, generation compilation constructs zero stores, and process shutdown closes it exactly once.
  - Confirm `pkg/lipsdk/terminaldecision` exclusive provider and generic terminal decision chokepoint are unchanged.
  - _Requirements: 7.1, 7.4, 8.2, 12.1, 12.2_
  - _Boundary: runtimebundle/core cleanup / featurehost process ownership / existing archtest migration_
  - _Depends: 7.2_
  - _Validation: terminal-decision full tests; `go test ./internal/archtest/...`; ownership-counting/process shutdown tests; grep proving no stale `TerminalDecisionPolicy *terminaldecisionpolicy.Store`/runtimebundle `NewStore` assertion_

---

- [x] 8. Replace feature-specific public host options with typed registrations
- [x] 8.1 Add startup-only `pkg/lipsdk/featurehost` registration envelope
  - Implement `Binding`, `Registration`, validation/identity bounds and duplicate detection from design.
  - No `any` payload, reflection, request-time lookup, resolver/service APIs or globals.
  - Standard SDK binding implementations must be nil-safe; add tests for nil, empty ID, duplicate ID, invalid binding, defensive slice handling and deterministic errors.
  - Add architecture test proving request/runtime hot packages do not import/use registration collections.
  - _Requirements: 9.3, 9.4, 9.5, 9.6_
  - _Boundary: SDK/public contract_
  - _Depends: 7.3_
  - _Validation: `go test ./pkg/lipsdk/featurehost/...`; external compile fixture_

- [x] 8.2 Move reasoning host policy contract out of `pkg/lipruntime`
  - Create `pkg/lipsdk/reasoninghost` (or reviewed equivalent) containing the current host-facing egress action/input/decision/policy and matcher binding semantics, without importing internal feature packages.
  - Add a typed host binding implementing the registration contract.
  - Move/adapt tests so the SDK contract is self-contained and typed-nil behavior remains safe.
  - _Requirements: 9.1, 9.2, 9.3_
  - _Boundary: SDK/public contract_
  - _Depends: 8.1_
  - _Validation: SDK tests; `go list` proving no internal imports_

- [x] 8.3 Teach standard featurehost to consume supported host bindings
  - Generic runtime validates/forwards immutable registrations only.
  - Concrete binding type interpretation/type switches live only in `internal/standardplugins/featurehost/bindings.go`.
  - Bind reasoning host policy/matcher into predecessor reasoning composition without `pkg/lipruntime` or runtimebundle importing the feature.
  - Unknown binding type/ID and duplicate semantic binding fail before serving.
  - No registration lookup on request path; generation holds direct typed bound service/planes.
  - _Requirements: 8.1, 9.3, 9.4, 9.5, 9.6_
  - _Boundary: standard-distribution composition_
  - _Depends: 8.2_
  - _Validation: featurehost host-binding tests; reasoning integration tests_

- [x] 8.4 Collapse `pkg/lipruntime.Options` to one feature-host registration field
  - Add `FeatureHostRegistrations []featurehost.Registration`.
  - Delete `ReasoningCompressionOptions`, `adaptReasoningCompressionOptions`, concrete internal reasoning feature imports and the per-feature `ReasoningCompression` field by end of task.
  - If a public compatibility promise made after this SDD requires temporary source compatibility, implement a one-way deprecated adapter that produces the registration before build, errors on new+old conflict, and is explicitly marked for removal. Do not let standard featurehost inspect both paths.
  - Add external module test showing a host supplies reasoning policy/matcher using only public packages.
  - _Requirements: 9.1, 9.2, 9.7, 13.4_
  - _Boundary: public runtime facade / SDK_
  - _Depends: 8.3_
  - _Validation: `go test ./pkg/lipruntime/...`; external compile-contract fixture; `git grep internal/plugins/features pkg/lipruntime` empty_

---

- [x] 9. Move optional UX configuration/defaults out of core config
- [x] 9.1 Move interleaved feature configuration and built-in prompt to the feature
  - Move `stream_to_client`, memo budget, max memo bytes, instructions file, built-in thinker prompt, file size/path policy and feature-specific validation/defaults to `internal/plugins/features/interleavedthinking/config.go`/`instructions.go`.
  - Core routing may retain only the minimum enablement/selector legality value if route planning genuinely requires it; prefer receiving enabled status through the generation-bound processor/route options rather than a full config object.
  - `internal/core/config` must not import the feature or define its prompt/defaults.
  - _Requirements: 10.1, 10.2, 10.4_
  - _Boundary: feature config / core config cleanup_
  - _Depends: 8.4_
  - _Validation: config parity/migration tests; routing disabled/enabled behavior_

- [x] 9.2 Move keep-warm configuration to feature registration
  - Move current `prompt_cache.keepwarm` semantic config/defaults/validation to the `keepwarm` feature decoder.
  - Generic prompt-cache provider capability/profile config that is independently needed by core/backend contracts may stay; keep-warm scheduling policy may not.
  - Delete core config imports of keepwarm feature implementation.
  - _Requirements: 10.1, 10.2, 10.4_
  - _Boundary: feature config / core config cleanup_
  - _Depends: 9.1_
  - _Validation: keepwarm config parity tests; config package import tests_

- [x] 9.3 Implement one-way legacy YAML normalization only if required
  - First check current compatibility/release policy and repository fixtures. If old top-level syntax must remain accepted, implement `internal/standardplugins/legacyfeatureconfig` normalization exactly as design: legacy -> canonical `plugins.features` node before semantic feature decode; new+legacy conflict errors; one semantic validator.
  - If compatibility is not required, reject old syntax with an explicit migration error and update docs instead. Do **not** retain typed feature semantics in core config as a fallback.
  - Whichever path is selected must be locked by tests and recorded in closeout evidence.
  - _Requirements: 10.3, 10.5_
  - _Boundary: configuration adapter / docs_
  - _Depends: 9.2_
  - _Validation: YAML golden tests for old/new/conflict/defaults; full config tests_

- [x] 9.4 Ratchet optional feature config ownership
  - Add compact archtest rules forbidding imports of `internal/plugins/features/*` from core config and forbidding known optional feature config/default symbols/large prompt literals in `internal/core/config`.
  - Add a structural test that a new standard feature config can be added through feature registration without editing core config production files.
  - _Requirements: 10.4, 12.1_
  - _Boundary: archtest_
  - _Depends: 9.3_
  - _Validation: `go test ./internal/archtest/...`_

---

- [x] 10. Consolidate residual feature-only support and compose packages
- [x] 10.1 Re-run consumer analysis after all known migrations
  - Refresh Task 1.1 census for predecessor reasoning/secretguard compose packages, remaining `compactioncompose`, `internal/reasoningreplay`, and every feature-specific infra/support row.
  - For each package record non-test production consumers and classify using Requirement 11.
  - Explicitly verify that every process resource from the Task 2.3 transition table is now either featurehost-owned or intentionally generic/borrowed; no `legacy`, `unassigned`, or dual-owner row may leave this task.
  - Expected decisions: one-feature algorithm/policy -> feature; host/process translation -> featurehost child/detail; genuinely shared auxiliary infrastructure -> retain generic with documented consumers.
  - No `mixed/deferred` row may leave this task.
  - _Requirements: 11.1, 11.2, 13.1_
  - _Boundary: architecture evidence / ownership closure_
  - _Depends: 9.4_
  - _Validation: import graph scan / `go list`; updated ownership + transition manifest; ownership-counting tests_

- [x] 10.2 Move one-feature support code under feature owners
  - Move reasoning replay/compression helpers and any equivalent one-feature algorithm identified by 10.1 beneath the owning feature package.
  - Update tests/imports mechanically; preserve public SDK separation.
  - Delete obsolete top-level helper packages and add absence ratchets where their return would recreate ambiguous ownership.
  - _Requirements: 11.1, 11.4, 11.5_
  - _Boundary: feature implementation cleanup_
  - _Depends: 10.1_
  - _Validation: affected feature tests; archtest_

- [x] 10.3 Fold feature-specific compose adapters under featurehost where appropriate
  - Move reasoning/secretguard/compaction adapter code that is solely standard-feature composition into featurehost children/details.
  - Keep only genuinely generic shared auxiliary scheduling/executor-runner infrastructure outside featurehost when two independent consumers are proven.
  - Generic runtimebundle may call only the featurehost facade, never child adapters.
  - Do not merge adapters into one generic binder merely because they share `Compile` naming.
  - _Requirements: 8.1, 11.2, 11.3_
  - _Boundary: standard-distribution composition / generic infra cleanup_
  - _Depends: 10.2_
  - _Validation: import graph; featurehost tests; `git grep` for direct runtimebundle adapter calls_

- [x] 10.4 Remove all per-feature fields from generic process/executor composition
  - Audit `ProcessServices`, `ProcessServicesInput`, `executorBuildInput`, `ExecutorConfig` groups, runtimebundle options and runtimehost handoff.
  - Remove any remaining field typed/named for a concrete optional standard feature, except the single `StandardFeatures` handle and minimal fixed consumer interfaces explicitly approved in design.
  - Ordinary extension behavior remains in `FrozenPlaneSet`/request snapshot, not dedicated executor fields.
  - Assert the transition table has no legacy or dual-owned resource and rerun the one-construction/one-close process ownership suite before deleting transitional test scaffolding (retain compact permanent ownership ratchets).
  - Add a source-structure archtest that fails when new per-feature fields matching standard feature IDs/packages appear in these generic aggregates.
  - _Requirements: 8.2, 8.3, 13.4_
  - _Boundary: core/runtimebundle cleanup / process ownership / archtest_
  - _Depends: 10.3_
  - _Validation: runtimebundle/runtime tests; ownership-counting tests; archtest_

---

- [x] 11. Lock the final architecture and prove change-surface reduction
- [x] 11.1 Create the durable core ownership manifest and admission test
  - Produce one machine-readable or compact Go table covering every final top-level `internal/core/*` package with category `kernel invariant` or `generic extension mechanism` plus a concise reason/independent consumer where applicable.
  - Architecture test must fail if a new top-level core package appears without an ownership entry.
  - This is a package admission gate, not a prohibition on all core growth; new entries require explicit architecture review.
  - Do not create task-number-specific scanners; use existing compact archtest rule/table infrastructure.
  - _Requirements: 1.5, 12.3, 13.2_
  - _Boundary: archtest / docs_
  - _Depends: 10.4_
  - _Validation: `go test ./internal/archtest/...`; `make arch-report`_

- [x] 11.2 Add permanent dependency/resurrection ratchets
  - Forbid `internal/core/** -> internal/plugins/features/**`, `runtimebundle -> concrete features`, runtimebundle -> featurehost child packages, feature packages -> core/runtimebundle, and public `pkg/lipruntime -> internal/plugins/features`.
  - Forbid resurrection of all core packages retired by both simplification specs, including final old conversation/interleaved/keepwarm/terminalpolicy paths.
  - Forbid a request-time featurehost service lookup/resolver or arbitrary binding map/reflection API.
  - Preserve migrated existing ratchets such as terminal-decision admission-only lookup and exclusive-provider chokepoint tests; update ownership-specific assertions instead of deleting them.
  - _Requirements: 12.1, 12.2, 12.6_
  - _Boundary: archtest_
  - _Depends: 11.1_
  - _Validation: archtest with synthetic negative fixtures where existing framework supports them_

- [x] 11.3 Reset core and featurehost budgets from measured final code
  - Measure final non-test `internal/core` LOC and set the hard budget to measured final + the repo's small standard fixed headroom. Never preserve deleted feature LOC as spare capacity.
  - Add a separate recursive budget for `internal/standardplugins/featurehost` plus critical-file caps so feature growth cannot turn the facade into a god package.
  - Review existing `runtimebundle` budget and lower it if this spec deletes generic feature wiring; do not increase it to absorb migration scaffolding.
  - Record baseline/final deltas in closeout evidence.
  - _Requirements: 12.4, 12.5_
  - _Boundary: archtest / architecture evidence_
  - _Depends: 11.2_
  - _Validation: `make arch-report`; budget tests_

- [x] 11.4 Run two disposable change-surface probes
  - **Ordinary feature probe**: add a temporary standard feature using existing planes only; production changes must be limited to feature package + standard distribution registration/composition, with zero core/runtimebundle/public SDK production edits.
  - **Host-bound feature probe**: add a temporary host-bound feature using only already-modeled host registration/generic facts; it must require no new `ProcessServices`, `ExecutorConfig` or `pkg/lipruntime.Options` field and no core/runtimebundle production edit.
  - Run tests, record exact touched production files, then revert/delete probe code before merge while retaining evidence/assertion tests where useful.
  - If either probe requires a generic core/runtime/public field edit, this architecture is not closed: fix the composition contract before continuing.
  - _Requirements: 8.6, 8.7, 12.7_
  - _Boundary: disposable architecture proof_
  - _Depends: 11.3_
  - _Validation: git diff/change-surface report + full focused tests_

- [x] 11.5 Re-run fixed-cost performance and concurrency certification
  - Compare predecessor plane/request snapshot allocations; require no regression.
  - Compare conversation projection benchmark/allocation behavior from Task 1.3; structural move must not add request-time lookup/locking/allocation beyond intentional snapshot construction.
  - Run keep-warm/compaction/session-policy concurrent tests under race.
  - On Windows development hosts, use exact Linux CI/race evidence rather than creating Linux wall-clock budgets for local timing.
  - _Requirements: 12.6_
  - _Boundary: tests / performance evidence_
  - _Depends: 11.4_
  - _Validation: focused `-benchmem`; `go test -race`; current CI race workflow_

- [x] 11.6 Reconcile public docs, steering and authoring guidance
  - Update architecture/extension authoring/plugin authoring/core-boundaries/steering docs to final package ownership.
  - Document core admission rule, standard featurehost role, host-feature registrations, feature-owned config, and distinction between kernel routing operators vs optional policy implementations.
  - Document the process-resource handoff rule as an implementation invariant: one constructor/physical close owner; `StandardFeatures.Close` never closes borrowed generic process resources.
  - Remove migration-era statements that name deleted core packages or imply `pkg/lipruntime` gains feature fields.
  - Document any accepted legacy YAML migration path and its status.
  - _Requirements: 13.2, 13.5_
  - _Boundary: docs / steering_
  - _Depends: 11.5_
  - _Validation: `make docs-check`; link/grep checks; `go test ./tools/kiro/speccheck` where applicable_

---

- [x] 12. Close the program with zero residual simplification debt
- [x] 12.1 Regenerate final ownership census and require zero deferred row
  - Repeat Task 1.1 against the final implementation.
  - Every production responsibility must be one of: kernel invariant, generic extension mechanism, optional feature implementation/policy, feature-specific infrastructure/composition, standard-distribution composition. No `mixed`, `unknown`, `temporary`, `compat-to-remove`, or `future simplification` row.
  - Verify every optional feature/policy row is outside core/generic runtime composition and every surviving core row has manifest justification.
  - Verify `ProcessServices`, executor inputs/config and `pkg/lipruntime.Options` satisfy Requirement 13.4 exactly.
  - Verify the Wave-2 ownership transition table is fully discharged: every process feature resource has one final constructor and physical cleanup owner; there are no legacy/non-owned transition rows except intentionally borrowed generic resources.
  - If a material simplification item remains, fix it under this SDD. Do not open a third simplification SDD to declare success.
  - _Requirements: 13.1, 13.2, 13.3, 13.4, 13.7_
  - _Boundary: architecture evidence_
  - _Depends: 11.6_
  - _Validation: ownership manifest/census + process ownership tests; import/structure scans; `make arch-report`_

- [x] 12.2 Run independent architecture review against requirements/design
  - Reviewer must explicitly check: kernel authority preservation; no feature semantics in core; featurehost not DI/service locator; process/generation cleanup singularity; public host binding safety; config single authority; conversation/interleaved split correctness; explicit interleaved adapter without feature->core import; migration of stale ownership-specific architecture tests; no request-hot-path lookup; no output/retry semantic drift.
  - Classify only material findings. Fix blockers/high material findings before closeout; do not generate a new cleanup tracker for them.
  - Record review verdict and resolved findings in normal PR/spec closeout evidence location used by the repo; do not add an unnecessary permanent report if existing spec workflow stores it elsewhere.
  - _Requirements: 2.1, 2.2, 2.5, 13.6, 13.7_
  - _Boundary: review / tests_
  - _Depends: 12.1_
  - _Validation: independent review + focused reruns for repaired findings_

- [x] 12.3 Run full repository certification
  - Run current canonical correctness, architecture, generated-code, docs, vet, vulnerability, module verification and external SDK contract gates.
  - Run exact Linux race certification for concurrency-sensitive moved state/lifetimes.
  - Run relevant fuzz/integration/DB parity suites and fixed-cost benchmarks from Task 11.5.
  - Do not waive a newly introduced failure as “pre-existing”; compare against exact baseline evidence.
  - _Requirements: 13.6_
  - _Boundary: tests / CI evidence_
  - _Depends: 12.2_
  - _Validation: `go test -count=1 ./...`; `go test -race -count=1 ./...`; `make quality-checks`; `make arch-report`; `make docs-check`; generator `-check`; `go vet ./...`; `govulncheck ./...`; `go mod verify`; applicable fuzz/integration commands_

- [x] 12.4 Certify clean merged-main and archive the completed SDD
  - After implementation PRs merge, rerun critical architecture/correctness checks on the exact merged `main` SHA to catch integration drift.
  - Confirm final ownership census, budgets, probe evidence and docs all reference the merged topology, not a stale feature branch.
  - Archive this SDD using repository convention and mark every task complete only after merged-main certification.
  - The closeout statement must explicitly say whether the simplification/migration program is fully closed. The only acceptable successful answer is backed by zero residual ownership debt from Task 12.1.
  - _Requirements: 13.1, 13.5, 13.6, 13.7_
  - _Boundary: spec closeout / CI evidence_
  - _Depends: 12.3_
  - _Validation: exact merged-main SHA + rerun `make quality-checks`, `make arch-report`, critical focused tests_

## Expected Implementation PR/Wave Boundaries

Use these as default checkpoints; split further when the 100-file limit or reviewability requires it:

1. **A — prerequisite/census/characterization + featurehost foundation** (Tasks 0–2). Wave A may introduce the featurehost handle but must preserve the explicit legacy-vs-featurehost ownership table; no process resource has two owners.
2. **B — compaction-continuity extraction** (Task 3).
3. **C — conversation-view split** (Task 4; likely multiple PRs: pure kernel extraction, state/persistence move, wiring/delete).
4. **D — interleaved-thinking split** (Task 5; likely multiple PRs due routing/state/persistence touch surface; the final integration PR must include the explicit featurehost adapter/compile-import tests).
5. **E — keep-warm extraction** (Task 6).
6. **F — terminal-policy extraction** (Task 7; includes migration of existing terminal-decision architecture ratchets).
7. **G — public host registration + `pkg/lipruntime` cleanup** (Task 8).
8. **H — feature config ownership/migration** (Task 9).
9. **I — residual support/compose consolidation + generic aggregate cleanup** (Task 10).
10. **J — permanent ratchets/probes/docs/certification/archive** (Tasks 11–12).

Do not combine C and D into one PR. Both touch core runtime/continuity and are independently high-risk. The goal is chronological correctness and reviewability, not minimum PR count.
