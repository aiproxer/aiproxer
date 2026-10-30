# Implementation Plan

> Execute in order unless a task is marked `(P)`. This is a release-bounded migration, not permission for a general core cleanup. Every production change starts with characterization/RED coverage. Preserve current `main` behavior unless the requirements explicitly change the v1 dynamic-plane compatibility contract.

- [x] 1. Freeze the corrected baseline and characterize the release-bound ownership seams
- [x] 1.1 Capture the exact post-extension-correction baseline
  - Record the implementation base SHA, current standard plane count/IDs, generated-output check result, current `internal/core` non-test line count, current `internal/infra/runtimebundle` tree line count, and current direct-import census for `internal/plugins/features/*` from core/runtimebundle/standardplugins.
  - Record current package/file inventories for `internal/core/toolcallrepair`, `internal/core/secretguard`, `internal/core/compactiondetect`, `internal/plugins/features/toolcallrepair`, and `internal/plugins/features/secretguard` so mechanical moves cannot silently omit production or tests.
  - Capture the fresh extension-plane seam baseline on one named measurement host using the exact command `go test -run '^$' -bench 'Benchmark.*(Completion|Traffic|Secret|Compaction|Terminal)' -benchmem -count=10 ./internal/core/extensions/...`. Record OS, CPU, Go version, `GOMAXPROCS`, power/performance mode if known, command, base SHA, and the unedited raw output. Do not reuse the older Wave-0 numbers when a newer corrected baseline exists.
  - Keep the same host/toolchain/environment available for Task 8.2. If that is impossible, Task 8.2 must recapture both baseline and candidate on one replacement host; never compare fixed-cost `ns/op` across different machines/toolchains.
  - Done means the implementation agent has one durable baseline table/file in normal implementation evidence scope and can name every production/test path to be moved or retained before editing implementation.
  - _Requirements: 6.1, 7.5, 9.1, 9.2, 10.5_
  - _Boundary: Verification / architecture baseline_
  - _Validation: `go run ./scripts/generate-feature-planes.go -check`; `make arch-report`; exact 10-sample benchmark command above_

- [x] 1.2 (P) Characterize closed-plane compatibility and mutation boundaries
  - Add tests using a test-local arbitrary unbound `Plane[[]T]` that currently enters fallback storage; characterize contribute, Get, Freeze, Clone, ToContributions, request freeze/materialization, `FrozenPlaneSet.Validate`, `FeatureBundle.Validate`, ordinary replay, candidate replay, explicit-empty slice, and fail-before-mutate behavior before changing the contract.
  - Add adversarial copies of a canonical standard plane: one with its ID changed and one retaining the same ID while deliberately changing exported policy fields such as source rules, nil policy, validator, identity extractor and combiner. The final contract must reject the changed-ID copy and must not let the same-ID copy alter canonical generated behavior.
  - Inventory every existing raw local plane used by SDK tests to exercise combiner/source/identity behavior, including `TestContribute_FailBeforeMutate_TableDriven` and `TestContribute_InterfaceValuedPlane_NonSliceCombinerReturn`; mark those tests for generated test binding rather than unbound rejection.
  - Keep standard generated planes as positive controls, including ordered, scalar reduce, exclusive identity, typed-nil, request-materialized, request-borrowed, and diagnostics examples where applicable.
  - Done means tests precisely distinguish the compatibility behavior intentionally removed by Requirement 1 from canonical standard-plane semantics that must survive.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7_
  - _Boundary: Public Feature SDK_
  - _Validation: `go test -count=1 ./pkg/lipsdk/feature ./internal/featurebundle`_

- [x] 1.3 (P) Characterize tool-call-repair ownership and behavior before move
  - Run/capture the complete current `internal/core/toolcallrepair` test/fuzz/benchmark inventory and standard feature factory tests; identify all non-test imports of the core package.
  - Add or strengthen one standard factory integration test proving the YAML config maps to finalizer ID/order/max-args/schema limits/on-unrepairable behavior and both contributed tool planes.
  - Add a disabled/absent-feature control proving no repair finalizer is created.
  - Done means the old implementation location is a replaceable oracle and no behavior depends on package path identity.
  - _Requirements: 2.1, 2.2, 2.5, 2.6_
  - _Boundary: Tool-call Repair Feature_
  - _Validation: `go test -count=1 ./internal/core/toolcallrepair ./internal/plugins/features/toolcallrepair ./internal/standardplugins -run 'Tool.*Repair|tool.*repair|Finalizer'`_

- [x] 1.4 (P) Characterize secret-guard source, matcher, audit, and security invariants
  - Pin single-user catalog discovery, include/exclude behavior, known-prefix behavior, min-secret length, matcher options, entry/category inventory, and matcher resolver results.
  - Pin multi-user **zero environment calls** with a panic/counting environment fake; pin disabled **zero environment calls** separately.
  - Pin runtime composition: feature uniqueness, access mode, action, audit policy, observer chaining/default slog observer, catalog inventory, redaction, typed-nil behavior, and failure text/classification currently relied on by tests.
  - Done means package relocation or access-mode type translation cannot weaken a security invariant without a focused RED failure.
  - _Requirements: 3.1, 3.2, 3.3, 3.6, 3.7, 6.2, 9.3_
  - _Boundary: Secret Guard Feature / Composition_
  - _Validation: `go test -count=1 ./internal/core/secretguard ./internal/plugins/features/secretguard ./internal/infra/runtimebundle -run 'Secret|secret|Matcher|Catalog'`_

- [x] 1.5 (P) Characterize compaction detector port semantics and lifetime
  - Enumerate the exact detector methods called by core runtime (`RequestOpened`, `PreviewResponse`, `ResponseReleased`) and assert that inputs/outputs can be represented with existing `lipapi` and `pkg/lipsdk/compaction` contracts.
  - Add fake-detector runtime tests for nil/no-op, request-open ordering, pure preview-before-preserver, response-release commit-after-preserver, panic isolation, and exact correlation fields.
  - Prove the current concrete detector has no `Close`, goroutine, external I/O, or generation-owned resource; if this premise is false, stop implementation and repair the spec before moving it.
  - Done means dependency inversion can be mechanical and `ProcessServices` remains the single lifetime owner.
  - _Requirements: 4.1, 4.2, 4.4, 4.5, 6.3, 9.4_
  - _Boundary: Core Runtime Detector Port / Process Ownership_
  - _Validation: `go test -count=1 ./internal/core/compactiondetect ./internal/core/runtime -run 'Compaction|compaction'`_

- [x] 2. Implement the selected #554 contract on generated standard planes only
- [x] 2.1 Add `ErrUngeneratedPlane` and canonical generated policy metadata
  - Add exactly one errors.Is-compatible public SDK sentinel named `ErrUngeneratedPlane`. Use this identifier in production, tests, docs and the external fixture; do not add an alias/second unsupported-plane sentinel.
  - Extend the generated access binding with deterministic unexported canonical metadata/closures sufficient both to prove the plane is generated and to make production policy authoritative: exact manifest ID plus the source rules, nil handling, validation, identity/conflict and combination behavior currently read from exported `Plane[T]` fields.
  - Validate generated eligibility and exact canonical ID **before** `ValidateDeclaration`, nil policy, source-rule selection, validator, combine, identity extraction, or candidate mutation. After that check, production contribution policy must be read from generated canonical metadata/closures rather than caller-mutable exported fields on `p`.
  - A wholly arbitrary unbound plane and a changed-ID copy must return attributed `ErrUngeneratedPlane`. A same-ID copy with mutated exported policy must either execute the unchanged canonical generated policy or be deterministically rejected by a generated integrity mechanism; it must never redefine the plane.
  - Do not use reflection, a runtime map of plane IDs, function-pointer comparison, pointer identity, mutable registration, or a service registry to decide support/integrity. `init()` must not be used for dynamic registration, support discovery, mutable runtime registration, or integrity lookup. A deterministic generated package-initialization step that snapshots the closed manifest's canonical policy into private typed generated metadata (as implemented in `plane_generated.go`) is permitted.
  - Extend `BindGeneratedAccessForTest` or add an equivalent `_test.go`-only helper so behavior-oriented local test planes can attach isolated canonical generated policy/storage without exposing dynamic binding to external production callers.
  - Done means the RED tests from 1.2 fail/behave exactly as selected and standard generated planes still pass all current declaration/contribution semantics.
  - _Depends: 1.2_
  - _Requirements: 1.1, 1.2, 1.6, 1.7_
  - _Boundary: Public Feature SDK / Generated Binding_
  - _Validation: `go test -count=1 ./pkg/lipsdk/feature`_

- [x] 2.2 Remove arbitrary value/identity storage from `ContributionSet` and migrate behavior tests
  - Delete `values map[string]any` and arbitrary-plane identity fallback from production contribution state; retain only generated typed storage and metadata demonstrably required by generated attribution/diagnostics.
  - Delete reflection-based arbitrary value clone/combine logic from the production contribution path; do not replace it with another erased container.
  - Rework `ContributeSource` so standard-plane source rules, nil policy, validation, identity/conflict handling and combination are driven by the generated canonical policy from 2.1. Do not fall back to mutable exported descriptor policy after eligibility succeeds.
  - Preserve fail-before-mutate by continuing staged/generated clone semantics for every standard combination rule and preserve explicit-empty vs nil semantics through generated typed storage tests.
  - Migrate `TestContribute_FailBeforeMutate_TableDriven`, `TestContribute_InterfaceValuedPlane_NonSliceCombinerReturn`, and every other raw-plane test whose purpose is combiner/source/identity behavior to `BindGeneratedAccessForTest` or the equivalent generated test fixture. Retain separate tests whose purpose is specifically to prove unbound `ErrUngeneratedPlane` rejection.
  - Done means no valid public contribution can enter erased value storage, no same-ID copied descriptor can change canonical policy, and all intended standard/test-fixture contribution paths remain covered.
  - _Depends: 2.1_
  - _Requirements: 1.1, 1.2, 1.4, 1.5, 1.7_
  - _Boundary: Public Feature SDK / Candidate State / Test Fixtures_
  - _Validation: `go test -count=1 ./pkg/lipsdk/feature ./internal/featurebundle`_

- [x] 2.3 Remove arbitrary fallback from freeze, validation and replay paths
  - Delete arbitrary values/identity maps and map-backed Get/Clone/ToContributions/Validate/Replay/CandidateReplay branches from `FrozenPlaneSet`.
  - Modify the plane generator/emitter to stop generating map replay/validation/candidate helper functions; regenerate `plane_generated.go` instead of editing it.
  - Ensure ordinary `Get` on an ungenerated plane returns its zero value because no valid set can contain such a value; it must not search dynamic storage.
  - Explicitly test the complete #554 surface: `ContributionSet.Freeze`, `FrozenPlaneSet.Clone`, `ToContributions`, request freeze/materialization, `FrozenPlaneSet.Validate`, `FeatureBundle.Validate`, ordinary replay, and candidate replay. Each valid path uses generated typed state/metadata only; no map/reflection fallback survives. Preserve fail-before-mutate for replay/destination mutation and prove failed bundle/frozen validation cannot partially mutate a destination/candidate.
  - Keep generated request freeze/materializers, borrowing, identity, binder, diagnostics, hook projection, and all-plane replay behavior unchanged; generated runtime projections must not consult caller-mutated `Plane` descriptor fields.
  - Done means generated code contains no dynamic plane fallback, all #554 freeze/validation/replay paths are directly covered, and `-check` is deterministic.
  - _Depends: 2.2_
  - _Requirements: 1.1, 1.3, 1.4, 1.5, 1.6, 9.1_
  - _Boundary: Public Feature SDK / Generator / Bundle Validation_
  - _Validation: `go run ./scripts/generate-feature-planes.go && go run ./scripts/generate-feature-planes.go -check && go test -count=1 ./pkg/lipsdk/feature ./internal/featurebundle ./internal/archtest`_

- [x] 2.4 Tighten closed-plane architecture and public-contract tests
  - Add architecture checks that reject reintroduction of arbitrary plane `map[string]any`/reflection contribution, freeze, request-freeze, validation or replay storage while allowing unrelated legitimate maps/reflection outside this contract.
  - Convert disposable dynamic-plane tests that were only scaffolding for consolidation into declaration-validation or generated-fixture tests; do not weaken real standard-plane coverage.
  - Add external-package tests using `errors.Is(err, feature.ErrUngeneratedPlane)` and confirm the `FeatureBundle` schema version/standard plane IDs did not change.
  - Add explicit adversarial regression coverage for changed-ID and same-ID-mutated copies so future refactors cannot restore descriptor-field authority accidentally.
  - Keep issue #554 open at this checkpoint: this task implements its SDK policy, but issue closure is owned by Task 8.4 after full production/release verification.
  - Done means #554's selected policy is executable across every acceptance path and cannot silently regress.
  - _Depends: 2.3_
  - _Requirements: 1.1, 1.2, 1.5, 1.6, 1.7, 7.4, 8.3_
  - _Boundary: SDK Contract / Architecture Gates_
  - _Validation: `go test -count=1 ./pkg/lipsdk/feature ./internal/featurebundle ./internal/archtest`_

- [x] 3. Move tool-call repair implementation into the feature
- [x] 3.1 Move the deterministic repair engine under `internal/plugins/features/toolcallrepair/repair`
  - Move the complete production implementation from `internal/core/toolcallrepair` (engine, schema/compiler/cache, catalog index, JSON completion/tail repair, finalizer, diagnostics/reason codes/helpers) into the feature-local `repair` subpackage.
  - Move the corresponding unit, fuzz, benchmark, contract, and regression tests with the implementation; preserve test names/assertions where practical so coverage is visibly continuous.
  - Update imports only; do not rewrite algorithms, reason codes, cache behavior, bounds, or concurrency semantics during the move.
  - Enforce that the new `repair` package imports only standard library, `pkg/lipapi`, `pkg/lipsdk/*`, and feature-local packages.
  - Done means the complete old oracle suite passes at the new location before the old core package is deleted.
  - _Depends: 1.3, 2.4_
  - _Requirements: 2.1, 2.4, 2.6_
  - _Boundary: Tool-call Repair Feature_
  - _Validation: `go test -count=1 ./internal/plugins/features/toolcallrepair/...`; targeted fuzz/bench smoke_

- [x] 3.2 Make the feature root construct its complete bundle
  - Add `toolcallrepair.FeatureBundle(cfg)` (or the repository-consistent equivalent) that translates YAML-decoded feature config into the feature-local finalizer policy and contributes both `PlaneToolCallFinalizers` and `PlaneToolCallFinalizationMaxArgsBytes`.
  - Preserve finalizer ID, order, default/max-args equality, schema limits, and on-unrepairable mapping exactly.
  - Simplify `internal/standardplugins/features_install.go` to decode config and delegate to the feature bundle constructor; remove the core repair import and duplicate policy mapping.
  - Done means standardplugins is distribution wiring only and the feature package owns implementation construction.
  - _Depends: 3.1_
  - _Requirements: 2.1, 2.2, 2.5_
  - _Boundary: Tool-call Repair Feature / Standard Distribution_
  - _Validation: `go test -count=1 ./internal/plugins/features/toolcallrepair/... ./internal/standardplugins -run 'Tool.*Repair|tool.*repair|Finalizer'`_

- [x] 3.3 Delete the old core package and ratchet its absence
  - Delete `internal/core/toolcallrepair` only after all production/test imports have migrated.
  - Add a permanent architecture rule rejecting production resurrection of that package and a recursive feature-tree import boundary for `internal/plugins/features/toolcallrepair`.
  - Update docs/package maps that still identify tool repair as core-owned.
  - Done means repository search has no production import/path for the retired core package and full focused integration remains green.
  - _Depends: 3.2_
  - _Requirements: 2.3, 2.4, 7.1, 7.3_
  - _Boundary: Architecture Ownership_
  - _Validation: `go test -count=1 ./internal/archtest ./internal/core/runtime ./internal/plugins/features/toolcallrepair/... ./internal/standardplugins`_

- [x] 4. Move secret-guard matching/source implementation into the feature
- [x] 4.1 Introduce feature-local source/matcher engine contracts without core imports
  - Create `internal/plugins/features/secretguard/engine` and move the concrete catalog, Aho-Corasick matcher, known-prefix, environment inventory, matcher resolver, and source-policy implementation from `internal/core/secretguard`.
  - Replace the `internal/core/accessmode.Mode` dependency with a closed feature-local mode value; preserve single-user/multi-user semantics exactly.
  - Keep the environment reader as a construction-time port inside the feature implementation/compose boundary; never expose it through request handler services or public SDK.
  - Move all implementation tests/benchmarks first and make them green in the new package before deleting old sources.
  - Done means feature-owned code has zero `internal/core` import while all source/matcher RED tests from 1.4 pass.
  - _Depends: 1.4, 2.4_
  - _Requirements: 3.1, 3.2, 3.3, 3.5, 3.7_
  - _Boundary: Secret Guard Feature Engine_
  - _Validation: `go test -count=1 ./internal/plugins/features/secretguard/...`_

- [x] 4.2 Create explicit `internal/infra/secretguardcompose` assembly
  - Move feature-specific runtime composition out of `runtimebundle/secret_guard_runtime.go` into a dedicated typed adapter.
  - Define composition-neutral adapter input/override types in `secretguardcompose` so `runtimebundle` does **not** import `secretguard/engine`; translate effective core access mode, host single-user overrides, environment, frozen guards, observer, and logger inside the adapter.
  - Move/alias internal `SecretGuardInputs`/environment option types from runtimebundle to the adapter as needed to preserve internal call-site compatibility without importing the concrete feature.
  - Preserve audit observer chaining/default slog observer, failure policy, inventory extras, action/access-mode/version, and matcher resolver exactly.
  - Do not pass `BuildOptions`, `ProcessServices`, full config, registry, or arbitrary dependency maps into the adapter.
  - Done means the adapter owns concrete feature assembly and runtimebundle only supplies explicit generic inputs/consumes output.
  - _Depends: 4.1_
  - _Requirements: 3.1, 3.2, 3.3, 3.6, 3.7, 5.1, 5.4, 5.6_
  - _Boundary: Secret Guard Composition Adapter_
  - _Validation: `go test -count=1 ./internal/infra/secretguardcompose ./internal/infra/runtimebundle ./internal/plugins/features/secretguard/... -run 'Secret|secret|Matcher|Catalog'`_

- [x] 4.3 Delete the old core package and preserve security ratchets
  - Delete `internal/core/secretguard` after production options/tests have migrated to adapter/feature-owned types.
  - Extend the existing feature import-boundary test to cover the full recursive secretguard feature tree, including the new engine subpackage.
  - Add architecture absence/import rules and update steering/docs that claim catalog/matcher construction is core-owned.
  - Done means multi-user and disabled no-environment-read tests, redaction/audit tests, runtime composition tests, and architecture gates all pass without the old package.
  - _Depends: 4.2_
  - _Requirements: 3.2, 3.3, 3.4, 3.5, 3.6, 7.1, 7.3, 9.3_
  - _Boundary: Secret Guard Ownership / Security_
  - _Validation: `go test -count=1 ./internal/plugins/features/secretguard/... ./internal/infra/secretguardcompose ./internal/infra/runtimebundle ./internal/core/runtime ./internal/archtest`_

- [x] 5. Invert and relocate the concrete compaction detector
- [x] 5.1 Define the smallest core runtime consumer port
  - Add one repository-internal detector interface at the core runtime consumer boundary with exactly the three operations characterized in 1.5; use existing `lipapi.Call`, `lipapi.Event`, `compaction.PreservationMeta`, `compaction.Event`, and `compaction.ResponsePreview` types.
  - Change `runtime.CompactionRuntime.Detector`, response-pipeline detector storage, and safe panic wrappers to the interface; preserve nil behavior.
  - Convert request/response correlation construction from concrete detector `RequestMeta`/`ResponseMeta` types to existing `compaction.PreservationMeta` fields without changing values.
  - Do not add the detector interface to public `pkg/lipsdk`, add generic observer registries, or move exact observation-stage timing out of core.
  - Done means runtime tests pass with fakes and core runtime no longer needs concrete detector types.
  - _Depends: 1.5, 2.4_
  - _Requirements: 4.1, 4.2, 4.3, 4.5, 6.3_
  - _Boundary: Core Runtime Consumer Port_
  - _Validation: `go test -count=1 ./internal/core/runtime -run 'Compaction|compaction'`_

- [x] 5.2 Move detector implementation to `internal/infra/compactiondetect`
  - Move detector state, heuristic/rule recognition, fingerprint/content-free helpers, preview logic, and tests/benchmarks from `internal/core/compactiondetect` to the infra implementation package.
  - Adapt public method metadata parameters to the consumer port using `compaction.PreservationMeta` directly or a private translation inside the implementation; do not export a second correlation contract.
  - Preserve bounds, lock scope, lazy sweep, no-background-worker behavior, panic safety at caller, and emitted event bytes/fields.
  - Done means the complete detector suite passes in the new package and it satisfies the runtime interface at compile time.
  - _Depends: 5.1_
  - _Requirements: 4.1, 4.4, 4.7, 9.4_
  - _Boundary: Compaction Detector Implementation_
  - _Validation: `go test -count=1 ./internal/infra/compactiondetect ./internal/core/runtime`_

- [x] 5.3 Preserve process ownership and delete the old detector package
  - Update `runtimebundle` process-service construction to instantiate the new infra detector and store it through the runtime consumer interface; retain one process-owned instance shared by generations.
  - Do not register a detector closer or create a generation copy unless Task 1.5 disproved the no-owned-resource premise and the spec was repaired first.
  - Delete `internal/core/compactiondetect`, add architecture absence/import ratchets, and update package maps/docs.
  - Run exact Linux race coverage over the new detector and runtime integration.
  - Done means process/generation ownership is unchanged and no concrete compaction heuristic remains in core.
  - _Depends: 5.2_
  - _Requirements: 4.3, 4.4, 4.5, 4.6, 7.3, 9.4_
  - _Boundary: ProcessServices / Architecture Ownership_
  - _Validation: `go test -count=1 ./internal/infra/runtimebundle ./internal/core/runtime ./internal/infra/compactiondetect ./internal/archtest`; Linux: `go test -count=1 -race ./internal/infra/compactiondetect ./internal/core/runtime ./internal/infra/runtimebundle`_

- [x] 6. Remove direct concrete-feature knowledge from generic runtimebundle
- [x] 6.1 Move reasoning-compression options and generation binding to `internal/infra/reasoningcompose`
  - Move the concrete reasoning-preservation config scan, prerequisite validation, egress policy lookup/selection, matcher/sanitizer requirement, service construction, bundle reconstruction, attempt-transform binder, and stream-observer binder out of runtimebundle.
  - Move `ReasoningCompressionOptions` to the adapter; preserve a type alias/translation at runtimebundle only if needed for internal/public `pkg/lipruntime` source compatibility, but the runtimebundle alias/file must not import the concrete feature package.
  - Define explicit adapter inputs for registrations, already-resolved BackgroundClient/Poller, trusted option set, and candidate surface. The adapter may import `reasoningpreservation`, `featurebundle`, and `standardplugins` companion policy as required.
  - Keep production/testing option precedence in runtimebundle; do not pass `ProcessServices`/`BuildOptions` into the adapter.
  - Done means all existing reasoning compression characterization/integration/typed-nil/rollback tests pass through the adapter and runtimebundle has no direct reasoning feature import.
  - _Depends: 2.4_
  - _Requirements: 5.1, 5.2, 5.3, 5.6, 5.7, 6.2_
  - _Boundary: Reasoning Composition Adapter_
  - _Validation: `go test -count=1 ./internal/infra/reasoningcompose ./internal/infra/runtimebundle ./internal/plugins/features/reasoningpreservation ./pkg/lipruntime -run 'Reasoning|reasoning|Compression|compression'`_

- [x] 6.2 Converge runtimebundle secret-guard delegation onto `secretguardcompose`
  - Remove any residual feature/config/engine-specific helper from `runtimebundle` after Task 4.2; retain only extraction of effective config/access mode/host options/frozen planes and one adapter call.
  - Ensure runtimebundle does not reconstruct secret-guard config-to-engine policy, matcher settings, or audit chaining after the move.
  - Preserve candidate overlay/reload behavior and generic `ExtensionsOptions` shape through adapter-owned aliases/types as needed.
  - Done means no secret-guard concrete implementation import or duplicated policy remains in runtimebundle.
  - _Depends: 4.2_
  - _Requirements: 5.1, 5.2, 5.4, 5.6, 6.1, 6.2_
  - _Boundary: Generic Runtime Composition_
  - _Validation: `go test -count=1 ./internal/infra/secretguardcompose ./internal/infra/runtimebundle`_

- [x] 6.3 Ratchet runtimebundle to zero concrete-feature imports
  - Add a permanent architecture rule scanning production `internal/infra/runtimebundle` imports and failing on `internal/plugins/features/*`.
  - Verify `compactioncompose` remains a dedicated adapter and do not refactor it for symmetry unless a mechanical type import changed in Task 5.
  - Search runtimebundle for feature IDs/names and classify any remaining occurrence: generic config/diagnostic string may remain only with documented reason; concrete implementation branching must move to an adapter or be reported as a blocker.
  - Done means the production import rule is zero and no feature-specific exception whitelist exists in runtimebundle.
  - _Depends: 6.1, 6.2_
  - _Requirements: 5.2, 5.5, 5.7, 7.2, 7.8_
  - _Boundary: Architecture Gates / Runtime Composition_
  - _Validation: `go test -count=1 ./internal/archtest ./internal/infra/runtimebundle`_

- [x] 7. Certify OSS authoring and permanent simplification ratchets
- [x] 7.1 Add recursive core/feature ownership architecture rules
  - Reuse existing import-rule/source-scan infrastructure to enforce core -> no concrete features, runtimebundle -> no concrete features, and the three retired core package absences.
  - Add recursive feature-tree checks for toolcallrepair and secretguard rather than checking only the root package's direct imports.
  - Add adversarial self-tests proving renamed/nested files or subpackages cannot trivially bypass the rule, without building a large semantic analyzer.
  - Done means each forbidden example fails and valid standardplugins/dedicated compose adapter imports pass.
  - _Depends: 3.3, 4.3, 5.3, 6.3_
  - _Requirements: 7.1, 7.2, 7.3, 7.7, 7.8_
  - _Boundary: Architecture Gates_
  - _Validation: `go test -count=1 ./internal/archtest`_

- [x] 7.2 Reset core/runtimebundle budgets downward and prove change-surface ROI
  - Re-measure final non-test `internal/core` and `internal/infra/runtimebundle` trees after migrations.
  - Set the core budget to measured final + 25 lines; do not retain deleted feature LOC as headroom. If runtimebundle shrank, ratchet its package tree to final + 25; it may not receive a budget increase solely because logic moved behind adapters.
  - Run a disposable existing-standard-plane feature probe from the post-migration tree: feature code + standard registration/test maintenance only, zero core/runtimebundle production edits. Record exact changed paths and remove the probe.
  - Done means simplification has deterministic structural evidence rather than a subjective "cleaner" claim.
  - _Depends: 7.1_
  - _Requirements: 7.5, 7.6, 9.1, 10.5_
  - _Boundary: Architecture Budgets / ROI Evidence_
  - _Validation: `make arch-report`; `go test -count=1 ./internal/archtest/tools/changesurface/... ./internal/archtest`_

- [x] 7.3 Add a fixed external-style OSS feature SDK fixture
  - Create the separate module at exactly `testdata/external_feature_sdk` using the established local-checkout module pattern: `require github.com/matdev83/go-llm-interactive-proxy v0.0.0` plus `replace github.com/matdev83/go-llm-interactive-proxy => ../..`. Do not rely on the workspace or a published module version.
  - The module may import only exported `pkg/lipsdk`/`pkg/lipapi` contracts from the root module plus standard library. Add an architecture/import test preventing repository `internal` imports.
  - Implement a tiny feature using one ordered standard plane through `NewContributionSet` -> `Contribute` -> `Freeze` -> `BundleFromPlanes`, and test the resulting bundle/plane value and ordinary public replay/read behavior.
  - In the same external consumer, construct an arbitrary ungenerated plane and assert contribution fails with `errors.Is(err, feature.ErrUngeneratedPlane)`; do not import test-only binding helpers or `internal` packages.
  - Add `testdata/external_feature_sdk` to the established nested-module QA/tidy loop alongside `testdata/enterprise_module` and `testdata/external_connector`, including the repository contract test that locks that module list. Do not add a separate slow CI matrix.
  - Done means the OSS feature contract is executable outside repository internals and QA cannot silently stop checking the module.
  - _Depends: 2.4_
  - _Requirements: 8.1, 8.2, 8.3_
  - _Boundary: Public SDK / External Consumer TCK_
  - _Validation: `(cd testdata/external_feature_sdk && GOWORK=off go mod tidy -diff && GOWORK=off go test ./...)`; `go test -count=1 ./internal/archtest ./internal/qa`_

- [x] 7.4 Reconcile feature authoring and architecture documentation
  - Update `pkg/lipsdk/feature` godoc, `docs/extension-platform-authoring.md`, `docs/plugin-authoring.md`, `internal/plugins/features/README.md`, architecture/steering package maps, and any direct references affected by moved packages.
  - Remove stale statements that features add named `FeatureBundle` fields/slices; document the frozen PlaneSet lifecycle, `ErrUngeneratedPlane`, canonical generated-policy authority, and closed standard manifest.
  - State the standard distribution boundary precisely: feature-owned bundle constructor/factory behavior plus explicit standard registration; no feature-specific core/runtimebundle branch.
  - State that a new plane requires an upstream manifest/platform change; do not imply arbitrary dynamic planes are supported in v1 or that copying/mutating a `PlaneX` descriptor redefines it.
  - Done means docs match the external fixture and current code, not historical Stage-4 bundle shapes.
  - _Depends: 3.3, 4.3, 5.3, 6.3, 7.3_
  - _Requirements: 1.8, 8.4, 8.5_
  - _Boundary: SDK / Documentation_
  - _Validation: `make docs-check`; `go test -count=1 ./pkg/lipsdk/feature`_

- [x] 8. Prove release-safe behavior and hand off full closure
- [x] 8.1 Run migrated-feature and generation/reload regression gates
  - Run focused SDK/featurebundle/toolrepair/secretguard/compaction/reasoningcompose/runtimebundle/core-runtime suites from a clean tree.
  - Re-run the complete #554 contract suite: unbound rejection, changed-ID rejection, same-ID mutation integrity, contribution/freeze/request-freeze/bundle-validation/ordinary-replay/candidate-replay paths, and external-module classification.
  - Run feature enable/disable/removal reload tests proving old requests stay pinned and new requests receive the new/no-feature surface.
  - Re-run terminal/failover/output-commit focused tests affected by compaction observation seam changes; no routing/B2BUA/billing semantics may change.
  - Done means all named migrations are behavior-preserving except the intentional ungenerated-plane rejection.
  - _Depends: 7.4_
  - _Requirements: 1.1, 1.2, 1.5, 6.1, 6.2, 6.3, 6.4, 6.5, 9.3_
  - _Boundary: Runtime / Generation Verification_
  - _Validation: targeted package suites; `(cd testdata/external_feature_sdk && GOWORK=off go test ./...)`; `make test`_

- [x] 8.2 Refresh hot-path allocation, timing evidence, and Linux race certification
  - On the **same host, CPU/power posture, Go version and `GOMAXPROCS` recorded in Task 1.1**, run the exact same benchmark selector with 10 samples: `go test -run '^$' -bench 'Benchmark.*(Completion|Traffic|Secret|Compaction|Terminal)' -benchmem -count=10 ./internal/core/extensions/...`. Preserve unedited candidate output next to the baseline.
  - Compare baseline vs candidate per benchmark. Use `benchstat` when available on the evidence host; otherwise compute the medians from the 10 raw samples and record the calculation. Do not install/change toolchain packages as part of the product diff merely to compare evidence.
  - **Blocking allocation rule**: candidate median `allocs/op` must be <= baseline median for every benchmark; any increase is NO-GO until removed or the SDD is explicitly repaired. Candidate median `B/op` must also be <= baseline median for every unchanged benchmark; an increase is NO-GO because this refactor is not permitted to buy simplification with extra request-path allocation bytes.
  - **Timing validity rule**: for each benchmark, compute `(p90(ns/op)-p10(ns/op))/median(ns/op)` over the 10 samples. If either baseline or candidate spread exceeds 15%, repeat that batch once on a quiet host before judging timing.
  - **Timing investigation threshold**: for a valid batch, candidate median fixed-cost `ns/op` <= 110% of baseline median is PASS. If it is >110%, repeat a second 10-sample candidate batch under the same conditions. If the repeated median is still >110%, Task 8.2 is blocked pending root-cause review. It may be dispositioned as non-attributable measurement noise only by the independent reviewer with the allocation/structural evidence recorded; this threshold is a same-host regression guard, **not** #394 load/HOLD or cross-machine latency certification.
  - Inspect generated/request execution paths for new reflection, arbitrary map lookup, locks, or per-request goroutines; any such addition attributable to this work is blocking regardless of benchmark noise.
  - Run the exact Linux race command `go test -count=1 -race ./internal/infra/compactiondetect ./internal/core/runtime ./internal/core/extensions ./internal/infra/runtimebundle ./internal/plugins/features/secretguard/...`. If development occurs on Windows, a successful Linux CI/job executing this exact package set is the authoritative evidence; a Windows skip is not a pass.
  - Record exact commit, host/run metadata, raw benchmark files/comparison verdicts, and Linux race run/job evidence. Explicitly state that this is not #394 performance certification.
  - Done means allocation/B-op gates pass, no unresolved >10% valid timing regression remains, request-path structural guarantees hold, and exact Linux race evidence is green.
  - _Depends: 8.1_
  - _Requirements: 9.1, 9.2, 9.3, 9.4_
  - _Boundary: Performance / Concurrency Verification_
  - _Validation: exact 10-sample benchmark command + comparison rules above; exact Linux race command above_

- [x] 8.3 Produce and validate the residual ownership inventory for the full-closure SDD
  - Create exactly `.kiro/specs/pre-oss-core-slimming/residual-ownership-inventory.md`. This is the durable implementation handoff consumed by the second full-closure SDD; do not leave the inventory only in a PR comment or chat transcript.
  - The artifact must contain: implementation/merged-main SHA and inventory date; classification vocabulary; a table with columns `Responsibility`, `Current owner/package`, `Production consumers`, `Classification`, `Why retained/deferred`, `Full-closure action`; summary counts by classification; and an explicit statement that no deferred finding exists only in transient session history.
  - Classify each finding as kernel invariant, generic extension mechanism, concrete optional feature policy, feature-specific infrastructure/composition, or mixed/needs split. Include current owner, concrete production consumers, why it was not moved in this pre-OSS spec, and the intended full-closure action.
  - Mandatory rows: compaction-continuity coordination; conversation-view generic projection vs optional steering policy; interleaved-thinking/state; terminal-decision policy; feature-specific public `pkg/lipruntime` host options/adapters; every dedicated feature compose adapter created/retained here; and any other core package whose primary reason for existence is an optional UX enhancement.
  - Add/extend a compact `tools/kiro/speccheck` contract test that accepts both the active and archived pre-OSS spec location and fails if the inventory is absent, lacks required headings/table columns, or omits a mandatory row. Do not make production runtime code parse spec artifacts.
  - Do not implement the inventory findings in this task. Done means the later SDD can start without redoing basic ownership discovery and the checked artifact is present on the implementation branch/main.
  - _Depends: 8.1_
  - _Requirements: 10.1, 10.2, 10.3, 10.4_
  - _Boundary: Full-closure Handoff / Kiro Evidence_
  - _Validation: `go test -count=1 ./tools/kiro/speccheck`; verify `.kiro/specs/pre-oss-core-slimming/residual-ownership-inventory.md` exists before archive/move_

- [x] 8.4 Run final repository gates, close #554 only after proof, and certify merged main
  - On the final implementation commit run generated-plane check, `make quality-checks`, `make test`, `make qa`, deterministic `make arch-report`, docs checks, the exact external fixture command `(cd testdata/external_feature_sdk && GOWORK=off go mod tidy -diff && GOWORK=off go test ./...)`, `go test -count=1 ./tools/kiro/speccheck`, and `go run ./cmd/lipstd --help`.
  - Require successful **Linux** execution of `go test -count=1 -race ./internal/infra/compactiondetect ./internal/core/runtime ./internal/core/extensions ./internal/infra/runtimebundle ./internal/plugins/features/secretguard/...`. This is a separate release gate; `make quality-checks`/`make qa` do not substitute for it unless a future canonical target demonstrably runs that exact scope.
  - Obtain an independent architecture/code review focused on closed-plane canonical policy and all #554 paths, feature ownership, secret security, detector lifetime/dependency direction, runtimebundle import boundary, immutable generations, benchmark/race evidence, and accidental scope expansion.
  - Merge only after required CI succeeds; then create a clean worktree from resulting `origin/main` and rerun the generated/quality/arch/docs/runtime smoke, external fixture, speccheck, and critical focused SDK/feature tests. Reference the successful Linux race run tied to the final/merged production commit; if the merge changes relevant Go code after that race run, rerun race on the merged SHA.
  - Close issue #554 **only now**, after the selected production contract is implemented and the contribution freeze, request freeze, bundle/frozen validation, ordinary/candidate replay, copied-plane integrity, external fixture, architecture and merged-main gates are proven. Merging this specification alone is never grounds for closing #554.
  - Archive this spec only after merged-main verification and the checked residual inventory is retained/relocated with the archived spec using repository convention; retain #394 as independent performance work and reference the inventory as input to the full-closure SDD.
  - Done means the pre-OSS simplification is release-certified with no must-fix finding, #554 is truthfully resolved by production evidence, and no hidden follow-up prerequisite remains.
  - _Depends: 8.2, 8.3_
  - _Requirements: 1.1-1.8, 8.1-8.5, 9.1-9.5, 10.3, 10.4, 10.5_
  - _Boundary: Release Verification / Delivery_
  - _Validation: `go run ./scripts/generate-feature-planes.go -check`; `make quality-checks`; `make test`; `make qa`; `make arch-report`; `make docs-check`; `(cd testdata/external_feature_sdk && GOWORK=off go mod tidy -diff && GOWORK=off go test ./...)`; `go test -count=1 ./tools/kiro/speccheck`; Linux exact race command; `go run ./cmd/lipstd --help`_

## Execution Notes for Smaller Agents

- Do not redesign while executing. If a current-main fact contradicts this plan materially, stop that subtask, record the contradiction, and repair the active Kiro artifacts before continuing.
- Do not move additional core packages opportunistically. Requirement 10 is a hard scope boundary.
- Do not introduce an abstraction to make the three migrations look uniform. Reuse existing generated planes and explicit composition patterns.
- For package moves, establish RED/characterization coverage first, move implementation + tests, make the new location green, then delete the old location. Never delete the oracle first.
- `plane_generated.go` is generator output. Modify the generator/emitter, regenerate, and use `-check`.
- Treat exported `PlaneX` values as descriptors, not mutable production declarations. Production policy comes from generated canonical metadata after Task 2.1; never restore exported descriptor fields as an alternate authority.
- When converting raw local plane tests, use `_test.go`-only generated bindings; do not export a public helper that lets external consumers manufacture generated eligibility.
- Keep each PR/checkpoint within the 100-Go-file gate. Split by the numbered migration groups above rather than using a blanket override.
- Preserve exact process/generation owners: `ProcessServices`, `ResourceLedger`, `runtimehost.Manager`, request/attempt owners. This spec adds no new lifetime manager.
- Treat the first client-visible content event as irreversible; no part of this cleanup changes retry/failover semantics.
- Do not close #554 when the spec merges. Close it only after Task 8.4 production/merged-main proof.

## Completion Status

- [x] All implementation tasks (1.1–8.4) are checked complete on the final certified baseline.
- [x] Final certified merged-main SHA: `d784a8344888dd9de2141a13d4bf723125d4b08c` (PR #591, `fix(feature): make generated plane policy canonical`).
- [x] Generator output deterministic: `go run ./scripts/generate-feature-planes.go -check` passes on the final baseline.
- [x] Canonical generated policy authoritative across contribution, request freeze/materialization, validation, ordinary/candidate replay, identity replay rules, generation binders, diagnostics, and hook projection; changed-ID `Get` returns zero and changed-ID `FrozenIdentity` returns `("", false)`; isolated global-descriptor mutation regression green.
- [x] Exact Linux race gate green on the final SHA: workflow run `33891386913` (`go test -count=1 -race ./internal/infra/compactiondetect ./internal/core/runtime ./internal/core/extensions ./internal/infra/runtimebundle ./internal/plugins/features/secretguard/...`).
- [x] External SDK fixture green on the final baseline (`testdata/external_feature_sdk`, `GOWORK=off`).
- [x] Independent GO review recorded in `final-closeout-evidence.md`; no must-fix finding remains.
- [x] Residual ownership inventory retained with the archived spec; all 10 deferred findings assigned to `core-feature-ownership-full-closure` (#572); no #572 implementation work included.
