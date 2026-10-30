# Implementation Plan

- [x] 1. Characterize the corrective boundaries
- [x] 1.1 Lock in nil-safe generation behavior
  - Add a regression that invokes terminal-provider access on an absent generation and proves the no-provider result without panic.
  - Audit all generation pointer accessors and characterize any documented zero behavior not already covered.
  - Preserve the existing published-generation and request-pinning characterizations as the non-nil control cases.
  - Done means the new test fails on the review baseline specifically because terminal-provider access dereferences the nil receiver, while neighboring accessor expectations remain explicit.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5_
  - _Boundary: Generation Accessor_
  - _Validation: go test -count=1 ./internal/infra/runtimebundle_

- [x] 1.2 (P) Lock in feature-bundle schema negotiation and rollback
  - Characterize empty version-zero and V1 bundles, non-empty plane bundles, lifecycle-only bundles, and unsupported schema versions.
  - Exercise direct contribution, direct generated merge, registry-generated merge, and host/candidate merge using a registry that deliberately returns malformed bundles.
  - Assert contributor attribution, zero returned candidates, unchanged destination contributions, and no lifecycle publication on every failure.
  - Done means malformed non-empty version-zero and unsupported-version bundles fail the tests on the review baseline while valid compatibility cases pass.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 4.3_
  - _Boundary: Bundle Validation Choke Point_
  - _Validation: go test -count=1 ./internal/featurebundle_

- [x] 1.3 (P) Lock in hook projection and mirror-ratchet expectations
  - Characterize populated, absent, nil, explicit-empty, ordered, defensive-copy, and host-policy hook configurations.
  - Add generator tests for valid hook-view metadata and rejection of duplicate, unknown, or type-incompatible targets.
  - Add architecture tests proving a handwritten hook-plane projection is rejected without relying on a named-function exemption.
  - Done means the runtime parity tests describe current hook behavior and the new no-exemption ratchet test fails on the review baseline.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 5.1, 5.2_
  - _Boundary: Hook View Generator, Mirror Scanner_
  - _Validation: go test -count=1 ./internal/archtest ./internal/infra/runtimebundle_

- [x] 1.4 (P) Inventory legacy lifecycle-surface behavior before deletion
  - Identify production and test consumers of the lifecycle-only legacy merge type and its dual-path helpers.
  - Move lifecycle order, nil-versus-empty, conflict, typed-nil, and rollback expectations onto the generated surface as RED-or-preservation tests.
  - Confirm generation compilation does not derive plane or extension behavior from the legacy value.
  - Done means every behavior worth retaining has a generated-path test and every remaining legacy reference is classified for deletion or migration.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5_
  - _Boundary: Generated Merge Surface_
  - _Validation: go test -count=1 ./internal/featurebundle ./internal/infra/runtimebundle ./internal/testkit/planeparity_

- [x] 2. Correct runtime and assembly behavior
- [x] 2.1 Restore nil-safe frozen terminal-provider access
  - Return the zero provider before reading generation state when the receiver is absent.
  - Continue resolving non-nil generations through the generated frozen plane set; do not add a duplicate provider slot or mutable binding.
  - Done means absent generation, absent provider, published provider, request pinning, and conflict rollback tests all pass.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5_
  - _Boundary: Generation Accessor_
  - _Validation: go test -count=1 ./internal/infra/runtimebundle_

- [x] 2.2 (P) Enforce bundle schema validation at the contribution choke point
  - Validate the complete bundle before replay and wrap failures with the normalized contributor identity.
  - Retain transactional plane replay and its existing validation rather than weakening either boundary.
  - Keep every lifecycle append after successful validation and replay in direct, registry, host, and candidate merge paths.
  - Done means all malformed bundles fail identically through every merge entry point without destination or lifecycle mutation, while valid compatibility cases still merge.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 4.3_
  - _Boundary: Bundle Validation Choke Point_
  - _Validation: go test -count=1 ./internal/featurebundle_

- [x] 3. Generate and consume the hook view
- [x] 3.1 Add canonical hook-view declaration metadata
  - Extend plane declarations with optional typed metadata that names their generated hook configuration target.
  - Annotate only the four canonical hook planes; keep host error policy outside extension-plane declarations.
  - Validate target uniqueness, supported target names and types, and membership in the standard manifest.
  - Done means valid manifest metadata parses deterministically and malformed metadata reports the responsible plane and target.
  - _Requirements: 3.2, 3.3, 3.6, 5.1, 5.2, 5.3_
  - _Boundary: Hook View Generator_
  - _Validation: go test -count=1 ./internal/archtest ./pkg/lipsdk/feature_

- [x] 3.2 Emit the typed hook configuration and projection
  - Extend generation to emit the SDK-owned hook configuration and projection from declaration metadata.
  - Preserve generated getter cloning, nil-versus-empty semantics, registration order, and the explicit host policy argument.
  - Regenerate feature-plane output solely through the generator.
  - Done means generated output contains all and only declared hook targets, is formatting-stable, and passes the generated-output check.
  - _Requirements: 3.1, 3.2, 3.3, 3.6, 5.1, 5.2, 5.3_
  - _Boundary: Hook View Generator_
  - _Validation: go test -count=1 ./internal/archtest ./pkg/lipsdk/feature && go run ./scripts/generate-feature-planes.go -check_

- [x] 3.3 Integrate the generated hook view with core and runtime composition
  - Make core hook configuration share the generated SDK type while retaining core-owned sorting and execution.
  - Replace runtimebundle's per-plane reads with direct generated projection consumption.
  - Preserve feature-hook construction, generation compilation, host policy, and lifecycle side-channel behavior.
  - Done means no handwritten production function enumerates the four hook planes and all hook bus parity tests pass.
  - _Depends: 3.2_
  - _Requirements: 3.1, 3.2, 3.3, 5.3, 5.4_
  - _Boundary: Hook Bus Config, Runtime composition integration_
  - _Validation: go test -count=1 ./internal/core/hooks ./internal/infra/runtimebundle_

- [x] 3.4 Remove the hook projection exemption and tighten W5c
  - Delete the exact-symbol hook allowlist and route all handwritten production hook projections through normal mirror inspection.
  - Keep generated files exempt only through the existing generated-file contract.
  - Update architecture catalog descriptions and deterministic baseline facts after the stricter scan passes.
  - Done means adversarial manual projections fail, generated projection passes, and W5c reports zero mirrors without a hook-specific exception.
  - _Depends: 3.3_
  - _Requirements: 3.4, 3.5, 3.6, 5.1, 5.2, 5.3_
  - _Boundary: Mirror Scanner_
  - _Validation: go test -count=1 ./internal/archtest && make arch-report_

- [x] 4. Remove lifecycle-only legacy merge compatibility
- [x] 4.1 Simplify feature assembly to one generated surface
  - Remove the lifecycle-only legacy surface, append path, checked/unchecked legacy bundle merges, generated-to-legacy projections, and dual-return merge APIs.
  - Retain registry bundle construction, registered-feature then host then candidate ordering, generated frozen state, and ordered lifecycles.
  - Preserve zero-surface rollback on bundle, host, candidate, validation, and conflict failures.
  - Done means production feature assembly exposes one generated merge result and no lifecycle-only API claims plane merge semantics.
  - _Depends: 2.2_
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 5.1, 5.2_
  - _Boundary: Generated Merge Surface_
  - _Validation: go test -count=1 ./internal/featurebundle_

- [x] 4.2 Integrate the single surface into generation compilation
  - Update live and candidate generation compilation to consume the simplified merge result.
  - Remove the no-op legacy parameter from extension projection and retain process secret-guard option behavior.
  - Use the generated surface's lifecycle side channel for generation build and candidate overlays.
  - Done means live and candidate generation compilation, rollback, extension projection, observer projection, and lifecycle tests pass without any legacy surface instance.
  - _Depends: 4.1_
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5_
  - _Boundary: Runtime composition integration_
  - _Validation: go test -count=1 ./internal/infra/runtimebundle_

- [x] 4.3 Delete obsolete dual-path test infrastructure and refresh architecture facts
  - Remove legacy parity helpers and tests only after their behavioral assertions exist on the generated path.
  - Remove obsolete architecture catalog and mirror-shape assumptions tied solely to the deleted type while retaining guards against reintroducing named plane transports.
  - Verify there are no residual production or test references to the legacy surface or misleading comments about append-based plane merging.
  - Done means repository search finds no legacy surface symbol, generated-path regressions remain covered, and the architecture report stays deterministic.
  - _Depends: 4.2_
  - _Requirements: 4.4, 4.5, 5.1, 5.2, 5.3_
  - _Boundary: Generated Merge Surface, Mirror Scanner, Tests_
  - _Validation: go test -count=1 ./internal/featurebundle ./internal/archtest ./internal/infra/runtimebundle ./internal/testkit/planeparity && make arch-report_

- [x] 5. Validate performance and corrective completion
- [x] 5.1 Run focused and repository-wide correctness gates
  - Run formatting, generated-output, feature SDK, feature merge, core hook, runtime composition, and architecture tests from a clean corrective tree.
  - Run quality, default test, QA, deterministic architecture report, and runtime help smoke gates.
  - Treat any allocation, hot-path structure, schema, mirror, race, or compatibility failure as blocking.
  - Done means every required command and exact corrective commit are recorded with passing output or an explicit blocker.
  - _Depends: 3.4, 4.3_
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 6.1, 6.2, 6.3, 6.5, 6.8, 7.1_
  - _Boundary: Verification Harness_
  - _Validation: go run ./scripts/generate-feature-planes.go -check && make quality-checks && make test && make qa && make arch-report && go run ./cmd/lipstd --help_

- [x] 5.2 Refresh consolidation benchmark and Linux race evidence
  - Rerun the complete seam benchmark suite and compare `ns/op`, `B/op`, and `allocs/op` with the Wave-0 baseline.
  - Confirm no request-path maps, reflection, key-search loops, or locks were introduced by the correction.
  - Run the exact Linux race scope against the final corrective commit and capture run and job identities.
  - Done means allocation and structural gates pass, fixed-cost deltas are recorded without a performance-neutrality claim, and Linux race evidence is linked.
  - _Depends: 5.1_
  - _Requirements: 6.4, 6.5, 6.6, 6.7, 6.8, 7.4_
  - _Boundary: Verification Harness_
  - _Validation: benchmark suite; go test -count=1 -race ./internal/core/extensions ./internal/infra/runtimebundle on Linux_

- [x] 5.3 Record adjacent SDK-hardening ownership
  - Create and link separate work that decides whether ungenerated SDK planes are rejected or fully supported across freeze, request freeze, validation, candidate replay, and ordinary replay.
  - State that this corrective feature neither removes nor certifies the dynamic map/reflection fallback.
  - Link refreshed fixed-cost benchmark evidence to #394 while retaining its latency, load, optimization, and HOLD boundary.
  - Done means both adjacent scopes have explicit owners and no corrective completion statement absorbs them.
  - _Requirements: 5.5, 5.6, 6.6, 7.3, 7.4_
  - _Boundary: Verification Harness_

- [x] 5.4 Obtain independent review and merged-main certification
  - Review the final implementation specifically against nil access, schema negotiation, zero-mirror hook projection, legacy deletion, boundary preservation, and evidence claims.
  - Merge only after required CI succeeds, then create a fresh worktree from the resulting `origin/main` and rerun the final verification chain.
  - Preserve original closeout evidence as historical, record the corrective certified baseline, and archive this spec only after merged-main verification.
  - Done means independent review returns no must-fix finding, merged-main gates pass, and VERIFIED status references the corrective merge rather than the superseded baseline.
  - _Depends: 5.2, 5.3_
  - _Requirements: 6.8, 7.1, 7.2, 7.3, 7.4, 7.5_
  - _Boundary: Verification Harness_

## Completion Status

- [x] Implementation merged through PR #555 as `1f69c577983cd60b03120ae855bc215e8e5138af`.
- [x] Required CI passed, including Linux race run `33412791511` job `99556140276` and QA run `33412791610` job `99556272903`.
- [x] Fresh merged-main generated, focused package, build, and `lipstd --help` checks passed from a clean worktree.
- [x] Independent merged-main review found no must-fix issue.
- [x] Dynamic-plane compatibility remains explicitly deferred to #554; latency, load, optimization, and HOLD certification remain in #394.
