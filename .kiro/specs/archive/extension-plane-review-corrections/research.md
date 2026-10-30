# Research & Design Decisions

## Summary

- **Feature**: `extension-plane-review-corrections`
- **Discovery Scope**: Integration-focused extension
- **Key Findings**:
  - `GenerationBundle.TerminalDecisionProvider` is the only pointer accessor in its type that dereferences the receiver before a nil guard; neighboring accessors already establish the intended zero behavior.
  - `ContributeBundle` is the common generated assembly choke point but currently replays only `PlaneSet`; `FeatureBundle.Validate` already contains the complete schema policy.
  - The hook projection remains a handwritten four-plane adapter and is hidden by `AllowedHookProjections`; the generator has no projection metadata today.
  - `MergedFeatureSurface` now carries only lifecycles. Production compilation ignores that value except for passing it to a no-op `extensionsFromMerged`, so deletion is feasible after test migration.

## Research Log

### Nil-Safe Generation Access

- **Context**: Independent review found a panic regression in no-generation access.
- **Sources Consulted**: `internal/infra/runtimebundle/generation_bundle.go`; request snapshot terminal-decision accessors; terminal-decision parity tests.
- **Findings**:
  - `TerminalDecisionProvider` reads `b.operations.Frozen` without checking `b`.
  - `Handler`, `ExecutorView`, `ReadinessReport`, `BackendIDs`, routing, frontend, auth, resource, publication, quiesce, and close accessors already guard nil receivers.
  - The request snapshot getter remains nil-safe through its zero frozen-plane view.
- **Implications**: Add one regression guard and audit test; retain generated frozen lookup and immutable generation pinning.

### Bundle Schema Boundary

- **Context**: Registry implementations can return bundles without invoking SDK validation.
- **Sources Consulted**: `pkg/lipsdk/feature/bundle.go`; `internal/featurebundle/merge_generated.go`; `internal/featurebundle/merge_surface.go`.
- **Findings**:
  - `FeatureBundle.Validate` permits empty version 0 or V1, and requires V1 for any plane or lifecycle content.
  - `ContributeBundle` is used by direct merge, registry merge, host/candidate merge, and freeze helpers.
  - Lifecycles are appended only after `ContributeBundle` succeeds on generated paths, so validating there preserves rollback.
- **Implications**: Validate once at `ContributeBundle` before replay, wrap contributor identity, and test every public/internal merge route with malformed registry output.

### Hook Projection and Ratchet

- **Context**: W5c reports zero mirrors while exempting the remaining handwritten hook mirror.
- **Sources Consulted**: `pkg/lipsdk/feature/plane_manifest.go`; `internal/archtest/plane_generator.go`; `internal/archtest/plane_emitter.go`; `internal/infra/runtimebundle/build_feature_hooks.go`; `internal/archtest/plane_rules.go`; `internal/archtest/plane_rules_tables.go`.
- **Findings**:
  - Four hook planes are declared in the canonical manifest and share SDK hook types.
  - `HooksConfigFromFrozen` manually calls `Get` four times and adds host policy.
  - `AllowedHookProjections` exempts exactly the two runtimebundle projection functions.
  - The generator already emits typed storage, replay, request views, binders, and diagnostics; it can emit one additional typed hook view from declaration metadata.
  - `pkg/lipsdk/feature` cannot import `internal/core/hooks`, so the generated type must be SDK-owned; core may alias or convert without enumerating planes.
- **Implications**: Add explicit hook-view target metadata to canonical declarations, generate an SDK `HookConfig` projection, make core `hooks.Config` an alias of the generated SDK type, and delete the production projection plus exemption.

### Lifecycle-Only Compatibility Surface

- **Context**: Legacy helper names and comments claim plane merge semantics after named fields were removed.
- **Sources Consulted**: `internal/featurebundle/merge_surface.go`; `internal/featurebundle/merge_generated.go`; `internal/infra/runtimebundle/compile_generation.go`; `internal/infra/runtimebundle/candidate_compile.go`; plane parity harnesses.
- **Findings**:
  - `MergedFeatureSurface` contains only `Lifecycles`.
  - `MergeBundlesChecked` performs no plane validation or conflict check.
  - `compileGeneration` receives the legacy surface only to pass it into `extensionsFromMerged`, which does not read it.
  - Candidate compilation discards the legacy result.
  - Most remaining dependencies are obsolete parity tests and helpers.
- **Implications**: Remove the legacy type and dual-return APIs, rename the generated surface to `MergeSurface` while retaining a temporary alias only if needed to keep a bounded diff, and migrate behavioral coverage to the generated path.

### Dynamic Plane Fallback

- **Context**: Exported generic plane APIs still have map/reflection fallback behavior.
- **Sources Consulted**: `pkg/lipsdk/feature/contributions.go`; `pkg/lipsdk/feature/frozen.go`; generated plane code and tests.
- **Findings**:
  - Standard planes use generated typed storage and avoid fallback on request paths.
  - Ungenerated exported planes can enter map-backed storage but do not compose consistently with generated replay and request freezing.
  - Closing the API is a public compatibility decision rather than a correction to the three P2 findings.
- **Implications**: Do not alter this contract in this spec. Create a linked SDK-hardening specification/issue that chooses closed-manifest rejection or complete dynamic composition.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
|--------|-------------|-----------|---------------------|----------|
| Documentation-only correction | Amend closeout and keep implementation | No code churn | Leaves panic, validation hole, and mirror | Rejected |
| Small local fixes with allowlisted hook adapter | Fix nil/schema only | Minimal diff | Does not satisfy W5c acceptance | Rejected |
| Manifest-derived hook view and legacy deletion | Fix behavior, validation, projection, and stale transport at existing choke points | Satisfies review with one source of truth | Generator and test migration require care | Selected |
| Generic projection or DI framework | Add runtime registry/reflection to map planes into consumers | Flexible | Violates explicit wiring, typed storage, and small-core rules | Rejected |

## Design Decisions

### Decision: Keep the Archived Consolidation Immutable

- **Context**: The original spec and closeout are historical evidence for merged work.
- **Alternatives Considered**:
  1. Move the archived spec back to active and edit its original requirements.
  2. Create a focused corrective spec linked to the archived baseline.
- **Selected Approach**: Use this new corrective spec and preserve the archived bundle unchanged except for future evidence links if delivery policy requires them.
- **Rationale**: Git plus an immutable archive keeps the original approval and evidence auditable while this spec defines the newly discovered scope.
- **Trade-offs**: Reviewers must follow a link between the original and corrective specs.
- **Follow-up**: Final evidence identifies both the original implementation baseline and the corrective certified baseline.

### Decision: Generate an SDK-Owned Hook View

- **Context**: A generated file in `pkg/lipsdk/feature` cannot depend on `internal/core/hooks.Config`.
- **Alternatives Considered**:
  1. Generate a core file from the manifest.
  2. Generate an SDK hook-view type and make core config an alias.
  3. Keep a manual core conversion.
- **Selected Approach**: Generate `HookConfig` and `ProjectHookConfig` in the SDK generated file, with host error policy passed explicitly; alias core `hooks.Config` to the generated type.
- **Rationale**: This removes every handwritten per-plane conversion and keeps dependency direction `internal/core -> pkg/lipsdk`.
- **Trade-offs**: The SDK feature package now exposes a narrow generated consumer view, but it contains only existing public SDK hook types.
- **Follow-up**: Generator tests prove metadata completeness, duplicate rejection, generated determinism, and projection semantics.

### Decision: Validate at ContributeBundle

- **Context**: All assembly routes converge before replay.
- **Alternatives Considered**:
  1. Trust registries to validate.
  2. Validate independently in each merge function.
  3. Validate once in `ContributeBundle` and retain replay validation.
- **Selected Approach**: Option 3.
- **Rationale**: It is the smallest fail-closed choke point and preserves contributor attribution and rollback.
- **Trade-offs**: `PlaneSet` validation occurs twice on an assembly path.
- **Follow-up**: Tests assert no destination or lifecycle mutation after malformed input.

### Decision: Delete Lifecycle-Only Legacy Merge Semantics

- **Context**: Legacy helpers no longer merge planes and production does not consume their result.
- **Alternatives Considered**:
  1. Rewrite comments and retain helpers.
  2. Rename to lifecycle-only helpers.
  3. Remove the type and dual-path APIs.
- **Selected Approach**: Option 3, migrating useful tests to the generated surface.
- **Rationale**: Deletion completes task 9.3 intent and prevents future callers from selecting a misleading path.
- **Trade-offs**: Internal tests and helper packages need coordinated updates.
- **Follow-up**: Preserve lifecycle ordering, nil/empty, conflict, and rollback characterization on the generated path.

## Risks & Mitigations

- Generated hook metadata could become a second manifest inside the generator - keep membership on each plane declaration and validate targets centrally.
- Alias cycles could arise between SDK feature and SDK hooks packages - generated `HookConfig` lives in `feature`, imports `hooks`, and core imports both; `pkg/lipsdk/hooks` must not import `feature`.
- Legacy deletion could accidentally lose behavior tests - migrate assertions before deleting helpers and run affected-test analysis.
- Bundle validation could append lifecycles too early - keep all lifecycle append operations after successful `ContributeBundle` calls.
- Benchmark wording could overclaim latency neutrality - record ns/op and allocations, but leave load/latency/HOLD certification to #394.

## References

- `.kiro/specs/archive/extension-plane-declaration-consolidation/` - original approved design, implementation tasks, and closeout evidence.
- `.kiro/steering/tech.md` - explicit construction, zero-reflection runtime, and canonical verification gates.
- `.kiro/steering/structure.md` - SDK, core, featurebundle, runtimebundle, and architecture-test ownership.
- `AGENTS.md` - repository architecture, testing, delivery, and source-change limits.
