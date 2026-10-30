# Research & Design Decisions

## Summary
- **Feature**: `extension-plane-declaration-consolidation`
- **Discovery Scope**: Extension / Complex Integration (brownfield refactor of the feature-extension surface)
- **Key Findings**:
  - A contributed plane value passes through six-to-seven hand-mirrored representations today (SDK bundle field → `MergedFeatureSurface.Append` → `extensionsFromMerged` **and** `overlayExtensions` → `ExtensionsOptions` field → snapshot accessor → `GenerationBundle.operations` field/accessor, plus `hooksConfigFromMerged` for hook-bus planes).
  - All exotic planes fit one generalization: a typed plane with declared multiplicity (ordered/exclusive) and combination rule (concatenate/reduce/exclusive), plus optional sidecar metadata; secret-guard host injections and observer host-append behavior are expressible as a host pseudo-contributor under the same rules.
  - Zero out-of-tree consumers of `pkg/lipsdk/feature.FeatureBundle` exist (verified across `connectors/`, `connector-support/`), so staged public-shape migration is low-risk.
  - Consumer census: named-plane reads concentrate in ~7 files (`internal/core/extensions/seam_views.go`, `snapshot.go`, `internal/core/diag/inventory_extensions.go`, three executor files, `terminal_decision_policy_admission.go`) plus composition/diag/archtest layers — wave sizing feasible within the 100-file gate.

## Research Log

### Mirror-chain census
- **Context**: Requirements R2/R3 need precise current-state touch points.
- **Sources**: codegraph session analysis; `internal/featurebundle/merge_surface.go`; `internal/infra/runtimebundle/{compile_generation,generation_bundle,build_feature_hooks}.go`; `internal/core/extensions/snapshot.go`.
- **Findings**: see Key Findings; `overlayExtensions` duplicates `extensionsFromMerged` per plane (two sites in one file); `TerminalDecisionProvider` additionally carries private sidecar `terminalDecisionProviderID`.
- **Implications**: consolidation must derive all seven projections generically or delete them.

### Diagnostics inventory shape
- **Context**: R6 equivalence.
- **Sources**: `internal/core/diag/inventory_extensions.go` (510 LOC), `inventory_extensions_stage_test.go`.
- **Findings**: `stageOccupancyFromBundle(b lipfeature.FeatureBundle)` switches over named bundle fields; helpers like `inventoryNonNilAttemptTransforms` are per-plane.
- **Implications**: inventory must iterate the plane catalog instead of named fields; output records stay shape-compatible (golden test).

### Baseline regeneration
- **Context**: R8.2.
- **Sources**: `scripts/arch-report.go`, `Makefile` (`arch-report` target), `testdata/architecture/hexagonal_migration_baseline.json`.
- **Findings**: `make arch-report` regenerates the hexagonal baseline deterministically from a repo scan.
- **Implications**: new ratchets should integrate into this existing command rather than inventing a second tool.

### Consumer census & seam views
- **Context**: wave sizing, R7.
- **Sources**: grep of `SessionOpeners|ToolCatalogFilters|CompletionGates|TerminalDecisionProvider()`; `internal/core/extensions/seam_views.go`.
- **Findings**: narrow consumer-driven view interfaces (`CompletionGatesView`, `TrafficPortBundle`) already exist; empty-result non-nil normalization lives in `seam_views.go`.
- **Implications**: snapshot named accessor methods can remain as one-line delegations (API surface, not declaration mirrors), keeping consumer churn near zero; nil-slicing copy semantics (`append(x[:0:0], x...)`) must be replicated by the generic derivation.

### Public-contract exposure
- **Context**: R5.2.
- **Sources**: repo-wide `lipsdk/feature` import scan including connector modules.
- **Findings**: no connectors/connector-support imports; all consumers in-repo.
- **Implications**: additive deprecation path suffices; document migration in godoc.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| A: consistency gates | Keep six named structs; archtests/codegen enforce sync | Zero runtime risk | Fails R2 intent; codegen toolchain cost | Rejected |
| B: big-bang plane registry | Replace all layers in one change | Cleanest end state | Exceeds change gate; review risk | Rejected |
| C: staged hybrid registry | New canonical typed plane registry; migrate families in waves deleting mirrors per wave | Fits 100-file gate; each wave green/revertible | Temporary dual representation | Selected |

## Design Decisions

### Decision: typed plane manifest with generated adapters as the single declaration site
- **Context**: R2 requires one shared declaration entry per plane.
- **Alternatives Considered**: (1) keep named fields everywhere + hand-maintained glue; (2) heterogeneous `[]any`/map-backed registry with typed assertions; (3) one hand-authored manifest generating typed storage/dispatch/diagnostics adapters.
- **Selected Approach**: Option C with deterministic generation. `Plane[T]` declarations carry source rules, fallible combination, validation, identity/nil policy, and diagnostics metadata; generation emits typed fields and dispatch so the request path uses no map, reflection, unsafe cast, or key search. Builder `ContributionSet` and immutable `FrozenPlaneSet` carry values through merge → generation → request snapshot; `Contribute` and `Get` are package-level generic functions because Go methods cannot take type parameters.
- **Rationale**: satisfies R2–R4 without creating a runtime service locator or impossible heterogeneous generic container; generated files are reported separately by the change-surface gate and require no manual edits.
- **Trade-offs**: introduces a small generator/check tool and generated code; temporary dual paths during waves. This is acceptable only if final ROI probes prove fewer hand-authored integration paths.
- **Follow-up**: generated-output currency, forbidden-mirror scans, hot-path allocation benchmarks, and disposable plane/feature change-surface probes are completion gates.

### Decision: generated immutable catalog vs "no init() state" rule
- **Context**: tech.md forbids `init()` functions/state.
- **Selected Approach**: generated static declarations and an ordered catalog; no `init()` function, runtime registration, or exported mutator.
- **Trade-offs**: generator becomes a build-time prerequisite; its `-check` mode must fail stale output deterministically.
- **Follow-up**: archtest asserts generated-output currency and catalog immutability-by-convention.

### Decision: execution-vs-admission duplicate planes
- **Context**: snapshot exposes pairs (`ToolCallPolicies`/`ToolCallPoliciesExecution`, `LocalTurnHandlers`/`...Execution`, `SecretGuardPlane`/`...ExecutionPlane`).
- **Selected Approach**: distinct planes sharing element types where binding time differs — declaration is cheap by design; avoids hidden conditional projections.
- **Trade-offs**: two catalog entries instead of one attribute; explicit and greppable.

### Decision: source-specific declared composition rules
- **Context**: R4.5 — `Production.TrafficObservers/UsageObservers` and secret-guard environment/inputs/observer inject outside features today.
- **Selected Approach**: each plane declares source-specific rules for feature bundles, host/config values, and generation binders. Host observers append after features; secret-guard environment/input/decision-observer remain host capabilities; config-owned `ToolReactorErrorPolicy` projects directly from frozen config; reasoning-compression and compaction-continuity keep generation-binder ownership but invoke generated replace-by-identity operations instead of field surgery. Combinations are fallible and validate before candidate mutation.
- **Follow-up**: characterize the test-only overlay's finalizer-cap overwrite behavior in W0; preserve it only if intentional, otherwise remove the dead overlay seam explicitly.

### Decision: diagnostics metadata is part of each plane declaration
- **Context**: Existing inventory coalesces several planes into shared stages, applies family-specific sorting/labels/nil filtering, and derives privilege flags; generic catalog iteration alone cannot preserve R6.
- **Selected Approach**: each inventory-visible plane declares stage/coalescing identity, occupant materialization and ordering, nil policy, and privilege projection. Generated adapters invoke typed callbacks; the shared projector has no plane switch arms.
- **Trade-offs**: declarations are richer, but this deletes 500+ lines of per-plane inventory logic only after golden parity proves equivalence.

### Decision: #394 sequencing and evidence boundary
- **Context**: Issue #394 will establish the high-concurrency baseline; this spec changes frozen snapshot storage/accessors and OBSERVE provenance but does not own performance optimization.
- **Selected Approach**: #394 Phase 1 harness work may run in parallel; Phase 2 baseline waits for consolidation completion, or OBSERVE, DELTA-allocation, and HOLD fixed-cost scenarios are refreshed. This spec records absolute seam-view benchmark values and defensive-copy semantics solely as neutrality/compatibility evidence.
- **Trade-offs**: consolidation-first delays baseline freeze but avoids re-baselining a nine-phase performance program; #394 correctness work remains out of scope.

### Decision: core relocation explicitly out of scope; follow-up spec required
- **Context**: The repository carries substantial policy/stage implementation inside `internal/core` (60 packages, ~87k non-test LOC) whose eventual home is outside the kernel (e.g., extensions evidence machinery, `terminalwork`, `toolcallrepair`, detection packages). Relocation was proposed as potentially synergistic with this consolidation.
- **Alternatives Considered**:
  1. Bundle physical relocation of core packages into this spec's waves — rejected: no requirement traceability (R1–R8 cover declaration consolidation only); conflates transform-with-move so parity failures lose attribution; adds import-path/baseline churn to every wave near the 100-file gate; moves code twice (legacy wiring now, final wiring later).
  2. Defer relocation to dedicated follow-up specs sequenced after W5 — selected.
- **Selected Approach**: This spec is deliberately relocation-neutral: plane catalog lives in `pkg/lipsdk`, projections are generic, consumers keep seam-view access — all location-stable under future package moves. After W5 lands and ratchets prove stable, a separate **core kernel-vs-stages decomposition** spec (likely split per subsystem family: extensions/evidence machinery; `terminalwork`/`toolcallrepair`; remaining detection/support packages) defines the kernel boundary, port surfaces, and destination modules with its own requirements/design/tasks.
- **Rationale**: Consolidation is the enabler — once stages consume planes via frozen sets/seam views instead of shared struct intimacy, extraction seams are pre-cut and each future move touches its package exactly once with final wiring. Isolated specs keep every wave's parity evidence attributable and revertible.
- **Trade-offs**: Two review cycles instead of one; interim period retains stage code inside `internal/core`.
- **Follow-up**: Create the kernel-vs-stages decomposition spec after this spec completes; record dependency in that future spec's context.

## Risks & Mitigations
- Parity drift in exotic planes (scalar min-reduce, provider-ID sidecar, secret-guard specials) — golden parity tests comparing legacy vs consolidated composition per wave before deletion.
- Temporary dual representation invites drift — anti-mirror ratchet scoped per completed wave (R5.5).
- Hot-path regression — frozen ordinal-array reads; race test on snapshot freeze/publish; no new allocations per request (R7 parity test).

## References
- `.kiro/specs/archive/extension-scalability-and-architecture-simplification/design.md` — prior art for extension-metadata consolidation (provider axis)
- `.kiro/specs/archive/terminal-decision-feature-extension/design.md` — exclusive-slot semantics origin
- Go spec: methods cannot have type parameters (shapes `Get[P]` free-function API)

---

# Appendix: Gap Analysis (2026-08-26, pre-design)

See prior content preserved below verbatim from `/kiro-validate-gap`.

# Gap Analysis: Extension Plane Declaration Consolidation

Date: 2026-08-26 · Input: requirements.md (requirements-generated) · Method: session-scale codebase investigation (codegraph + targeted reads/greps) against the EARS requirements.

## 1. Current State Investigation

### The mirror chain (measured)

| # | Layer | File(s) | Per-plane cost |
|---|---|---|---|
| 1 | SDK bundle contract | `pkg/lipsdk/feature/bundle.go` | field (+doc) — 27 fields |
| 2 | Merge | `internal/featurebundle/merge_surface.go` | field + Append line + (exclusive: conflict guard) |
| 3 | Composition projection | `internal/infra/runtimebundle/compile_generation.go` | `extensionsFromMerged` copy line (~L312–334) **and** `overlayExtensions` append branch (~L345–380) |
| 4 | Executor extension options | `internal/core/extensions` `ExtensionsOptions` struct | field |
| 5 | Request snapshot | `internal/core/extensions/snapshot.go` | accessor — 31 accessors |
| 6 | Generation bundle | `internal/infra/runtimebundle/generation_bundle.go` | `generationOperations` field + nil-safe accessor |

Plus per-feature: standard-distribution registration, executor stage integration, evidence-projection vocabulary, diagnostics inventory, archtest baselines.

### Non-uniform planes (constraint candidates)

- `SecretGuards`: slice plane plus process-owned extras injected at composition root; root uniqueness validation.
- `ToolCallFinalizationMaxArgsBytes`: scalar plane with min-reduce merge semantics.
- `TerminalDecisionProvider`: exclusive slot plus private provider-ID sidecar.
- `TrafficObservers`/`UsageObservers`: hybrid feature + host-injected slices.
- `Lifecycles`: travels alongside merged surface via separate return path.
- Hook-bus planes project into `internal/core/hooks.Config` via `hooksConfigFromMerged` (seventh site).

## 2. Requirement-to-Asset Map

| Req | Existing asset | Status |
|---|---|---|
| R1 semantics | `Append`, `CompileGeneration`, snapshot freeze | Constraint: preserve |
| R2 single-site declaration | nothing | Missing (core gap) |
| R2.3 ratchet | archtest patterns | Missing new gate |
| R3 additive features | plugin-dir isolation once plane exists | Partially present |
| R4 general exclusive slot | terminaldecision only | Missing generalization |
| R5 staged migration | repo change gate discipline | Constraint: ≤100 Go files/wave |
| R6 derived diagnostics | diag inventory | Unknown → resolved (see Research Log) |
| R7 hot-path neutrality | frozen snapshots | Constraint: fixed-layout frozen set |
| R8 baseline regen | `make arch-report` | Unknown → resolved (deterministic) |

## 3. Options, 4. Complexity, 5. Recommendations

Summarized in Architecture Pattern Evaluation and Design Decisions above (Options A/B/C; Effort L; Risk Medium; five Research Needed items resolved in Research Log).
