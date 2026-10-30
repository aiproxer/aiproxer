# Closed-Plane Characterization Evidence

- **Task**: 1.2 (P) Characterize closed-plane compatibility and mutation boundaries
- **Spec**: `pre-oss-core-slimming`
- **Boundary**: `pkg/lipsdk/feature` (Public Feature SDK, test & evidence only)
- **Status**: Completed & Verified

---

## 1. Executive Summary

Task 1.2 pins and characterizes the exact pre-Task-2 behavior of arbitrary unbound planes, adversarial standard-plane copies, and standard generated positive controls before implementing the closed-manifest contract (Issue #554 / Requirements 1.1–1.7).

### Key Characterization Findings

1. **Arbitrary Unbound Plane Incompleteness in Pre-Task-2 Proxy Runtime**:
   - `ContributionSet` and `FrozenPlaneSet.Get` store and retrieve arbitrary unbound `Plane[[]T]` instances in `values map[string]any`.
   - However, **downstream proxy lifecycle operations silently drop unbound fallback planes**:
     - `FreezeRequestPlanes`: Materializes only standard generated fields (`in.frozen.freezeRequest()`), omitting `values map[string]any`. Unbound planes evaluate to `nil` in request snapshots.
     - `FrozenPlaneSet.ReplayTo` / `ReplaySourceTo`: `replayAllPlanesMapTo` iterates only across standard manifest plane IDs in `plane_generated.go`. Unbound planes are not replayed into the target `ContributionSet`.
     - `FrozenPlaneSet.ContributeCandidateTo`: `contributeCandidateMapTo` iterates only across standard candidate planes. Unbound planes in candidate frozen sets are not merged into destination sets.
   - This proves that dynamic/arbitrary plane fallback was an incomplete interim artifact rather than a true production capability. Removing it in Task 2 closes an architectural hole without breaking actual proxy features.

2. **Adversarial Copies of Canonical Standard Planes**:
   - **Changed-ID Copy** (`PlaneSubmitHooks` with mutated `ID = "adversarial.tampered_id"`): Currently accepted by `ContributeSource` because `p.generated.contribute` is non-nil. The closure writes directly into standard typed storage (`submitHooks`), making both the altered ID and the canonical ID observe the contributed value. In Task 2, `gp.planeID != p.ID` must reject the changed-ID copy with `ErrUngeneratedPlane` before mutating state.
   - **Same-ID Copy with Mutated Exported Policy**: Currently, `ContributeSource` evaluates `p.IsNil`, `p.NilPolicy`, `p.Validate`, `p.Identity`, and `p.Rules` directly from the caller-supplied `Plane[T]` descriptor fields before reaching `p.generated.contribute`. However, `p.generated.contribute` executes package-level canonical `Combine` (ignoring `p.Combine`). In Task 2, canonical generated metadata (`gp`) becomes authoritative for *all* contribution policies, preventing caller descriptor mutation from overriding proxy invariants.

3. **Transactional Fail-Before-Mutate Behavior**:
   - Destination sets remain atomically untouched during failed `Contribute`, failed `ReplayTo`, and failed `ContributeCandidateTo` operations.

---

## 2. Mechanical Raw Local Plane Inventory in Existing SDK Tests

A mechanical audit of all `Plane[` declarations across `pkg/lipsdk/feature/*_test.go` produced the following census and migration plan:

| File | Test Function | Plane ID / Declaration | Purpose | Migration Disposition (Task 2) |
|---|---|---|---|---|
| `candidate_freeze_test.go` | `TestContribute_FailBeforeMutate_TableDriven` | `test.fail_before_mutate_table` (`Plane[[]string]`) | Tests fail-before-mutate across table-driven cases with in-place mutating combiner | Migrate to `BindGeneratedAccessForTest` / test binding fixture |
| `candidate_freeze_test.go` | `TestContribute_FailBeforeMutate_TableDriven` | `test.host_only_plane` (`Plane[[]string]`) | Tests source rule rejection (`ErrUnsupportedSource`) for host-only plane | Migrate to `BindGeneratedAccessForTest` / test binding fixture |
| `candidate_freeze_test.go` | `TestContribute_InterfaceValuedPlane_NonSliceCombinerReturn` | `test.interface_plane_non_slice_return` (`Plane[any]`) | Tests non-slice combiner return for interface-valued plane | Migrate to `BindGeneratedAccessForTest` / test binding fixture |
| `diagnostics_and_access_test.go` | `TestPlaneDeclarationValidation_DiagnosticsCompleteness` | 9 local table cases (`test.diag_*`) | Tests static `PlaneDeclaration.ValidateDeclaration()` rules for diagnostics metadata | Retain as descriptor declaration validation (no `Contribute` call) |
| `diagnostics_and_access_test.go` | `TestPlaneDeclarationValidation_NilPolicy` | 4 local table cases (`test.nil_policy`) | Tests static `ValidateDeclaration()` rules for `NilPolicy` enum values | Retain as descriptor declaration validation |
| `diagnostics_and_access_test.go` | `TestContribute_NilPolicy_Reject` | `test.reject_nil_interface`, `test.reject_typed_nil` | Tests `NilReject` policy rejecting nil interface / typed nil during contribute | Migrate to standard generated positive control (`PlaneTerminalDecisionProvider`) or test binding |
| `diagnostics_and_access_test.go` | `TestContribute_NilPolicy_Skip` | `test.skip_nil_interface` | Tests `NilSkip` policy silently skipping nil contributions | Migrate to standard generated positive control (`PlaneSessionOpeners`) or test binding |
| `diagnostics_and_access_test.go` | `TestContribute_NilPolicy_AppliedBeforeValidation` | `test.nil_before_validate` | Proves `NilSkip` skips before invoking `Validate` | Migrate to test binding or standard generated plane |
| `diagnostics_and_access_test.go` | `TestDiagnosticsDescriptor_MaterializeAndPrivileges` | `test.diag_exec` | Tests execution of `MaterializeOccupants` and `ProjectPrivileges` on descriptor | Retain descriptor method test / positive control |
| `diagnostics_and_access_test.go` | `TestContribute_NilPolicy_InterfaceTypedNil` | `test.nil_no_callback`, `test.nil_with_callback`, `test.nil_skip_with_callback` | Proves interface boxed typed-nil behavior with and without `IsNil` callback | Migrate to `BindGeneratedAccessForTest` test binding |
| `diagnostics_and_access_test.go` | `TestPlaneDeclarationValidation_GeneratedIdentityRequiredWhenBound` | `test.bound.missing_identity` | Proves bound exclusive plane requires `generated.identity` | Retain as bound plane validation test |
| `plane_contract_test.go` | `TestPlaneDeclarationValidation_TableDriven` | 20 local table cases | Tests static descriptor validation invariants (`ID`, multiplicity, sources, rules) | Retain as declaration validation tests |
| `plane_contract_test.go` | `TestValidateManifest` | `plane.1`, `plane.2`, `plane.invalid`, `plane.invalid_comb` | Tests `ValidateManifest` for duplicate IDs and invalid declarations | Retain as manifest validation tests |
| `plane_contract_test.go` | `TestContribute_AttributedErrors` | `test.hooks`, `test.host_only`, `test.invalid_comb` | Tests `AttributedError` wrapping for empty plugin ID, unsupported source, invalid declaration | Migrate to `BindGeneratedAccessForTest` test binding |
| `plane_contract_test.go` | `TestContribute_ExclusiveConflict_FailBeforeMutate` | `test.terminal_decision_provider` | Tests exclusive conflict error formatting and fail-before-mutate | Migrate to standard `PlaneTerminalDecisionProvider` or test binding |
| `plane_contract_test.go` | `TestContribute_ScalarMinReduce_RegistrationOrder` | `test.tool_call_finalization_max_args_bytes` | Tests scalar min-reduction combining logic across sequences | Migrate to standard `PlaneToolCallFinalizationMaxArgsBytes` or test binding |
| `plane_contract_test.go` | `TestContribute_OrderedConcatenation` | `test.submit_hooks` | Tests ordered slice concatenation combining logic | Migrate to standard `PlaneSubmitHooks` or test binding |
| `plane_contract_test.go` | `TestContribute_FallibleCombine_FailBeforeMutate` | `test.fallible` | Tests combiner failure returns attributed error and leaves set untouched | Migrate to `BindGeneratedAccessForTest` test binding |
| `plane_contract_test.go` | `TestContribute_MutatingCombiner_FailBeforeMutate` | `test.mutating_combiner` | Tests defensive copy against in-place mutating combiner | Migrate to `BindGeneratedAccessForTest` test binding |
| `plane_contract_test.go` | `TestContribute_CallerSliceMutation_Isolation` | `test.slice_isolation` | Tests defensive copy of caller slice during Contribute | Migrate to standard `PlaneSubmitHooks` or test binding |
| `plane_contract_test.go` | `TestFreeze_Aliasing_Isolation` | `test.freeze_isolation` | Tests defensive cloning during Freeze and Get against slice mutations | Migrate to standard `PlaneSubmitHooks` or test binding |
| `plane_contract_test.go` | `TestContributeSource_Semantics` | `test.multi_source`, `test.feature_only` | Tests ContributeSource under SourceFeature, SourceHost, and host-unsupported rejection | Migrate to `BindGeneratedAccessForTest` test binding |
| `plane_manifest_test.go` | `TestPlaneDeclarationValidation_HookTargetInvariants` | `test_plane`, `test_plane_1`, `test_plane_2` | Tests `HookTarget` declaration validation and manifest uniqueness | Retain as manifest validation tests |
| `custom_plane_test.go` | `TestExternalPlaneDeclaration_SatisfiesInterface` | `custom_external_plane` | Proves custom external structs satisfy `PlaneDeclaration` interface | Retain as manifest interface test |
| `closed_plane_characterization_test.go` | `TestClosedPlane_UnboundFallback_*` | `test.unbound_*` | Characterizes pre-Task-2 fallback behavior across all lifecycle methods | Transition to `ErrUngeneratedPlane` rejection tests in Task 2 |

---

## 3. Detailed Characterization Results (Current vs Target Task 2 Contract)

| Lifecycle / Surface | Current Baseline Behavior (Task 1.2) | Target Contract (Task 2) | Spec Reference |
|---|---|---|---|
| **Arbitrary Unbound Plane Contribution** | Stored in `values map[string]any`; returns `nil` error | Rejected before mutation with `ErrUngeneratedPlane` wrapped in `AttributedError` | Req 1.2, Design 178–203 |
| **Arbitrary Unbound Plane Get** | Retrieves clone from `values map[string]any` if present | Returns typed zero value with zero map lookups | Req 1.3, Design 249–255 |
| **FreezeRequestPlanes on Unbound Plane** | Omitted from frozen request snapshot (returns zero value) | Returns typed zero value (unchanged outcome, zero map overhead) | Req 1.5, Design 225–235 |
| **ReplayTo / ReplaySourceTo on Unbound Plane** | Omitted from replay destination (not replayed) | Unbound plane cannot enter frozen set; no map replay | Req 1.5, Design 225–235 |
| **ContributeCandidateTo on Unbound Plane** | Omitted from candidate merge | Unbound plane cannot enter candidate set; no map merge | Req 1.5, Design 225–235 |
| **Changed-ID Standard Plane Copy** | Dispatches to standard typed storage via unexported closure; accepted | Rejected before mutation with `ErrUngeneratedPlane` (`gp.planeID != p.ID`) | Req 1.2, Design 173, 201 |
| **Same-ID Standard Copy with Mutated `Validate`** | Executes caller's mutated validator before dispatch | Authoritative canonical `gp.validate` executed; caller mutation ignored | Req 1.1, Design 171, 205 |
| **Same-ID Standard Copy with Mutated `Rules`** | Reads caller's mutated source rules; may reject valid source | Authoritative canonical `gp.rules` executed; caller mutation ignored | Req 1.1, Design 171, 205 |
| **Same-ID Standard Copy with Mutated `NilPolicy`** | Reads caller's mutated nil policy | Authoritative canonical `gp.nilPolicy` executed; caller mutation ignored | Req 1.1, Design 171, 205 |
| **Same-ID Standard Copy with Mutated `Identity`** | Reads caller's mutated identity extractor | Authoritative canonical `gp.identity` executed; caller mutation ignored | Req 1.1, Design 171, 205 |
| **Same-ID Standard Copy with Mutated `Combine`** | Ignored by generated closure (executes canonical package Combine) | Authoritative canonical `gp.combine` executed (consistent with current) | Req 1.1, Design 171, 205 |

---

## 4. Standard Generated Positive Controls

The characterization suite pins and validates all canonical standard generated planes across their required properties:

1. **Ordered Slices**:
   - `PlaneSubmitHooks` (`[]hooks.SubmitHook`, `MultOrdered`, `CombConcatenate`)
   - `PlaneRequestTransforms` (`[]request.Transform`, `MultOrdered`, `CombConcatenate`)
   - `PlaneToolReactors` (`[]hooks.ToolReactor`, `MultOrdered`, `CombConcatenate`)
   - `PlaneRequestPartHooks` (`[]hooks.RequestPartHook`, `MultOrdered`, `CombConcatenate`)
   - `PlaneStreamObserverFactories` (`[]response.StreamObserverFactory`, `MultOrdered`, `CombConcatenate`)
2. **Scalar Min-Reduction**:
   - `PlaneToolCallFinalizationMaxArgsBytes` (`int`, `MultOrdered`, `CombReduce`)
3. **Exclusive Identity Slot**:
   - `PlaneTerminalDecisionProvider` (`terminaldecision.Provider`, `MultExclusive`, `CombExclusive`, `NilReject`)
4. **Typed-Nil Handling (`NilSkip`)**:
   - `PlaneSessionOpeners` (`[]session.Opener`, `NilSkip`)
   - `PlaneWorkspaceResolvers` (`[]workspace.Resolver`, `NilSkip`)
   - `PlaneSecretGuards` (`[]secretguard.Guard`, `NilSkip`)
5. **Request Materialization**:
   - `PlanePreRequestHandlers` (`[]prerequest.Handler`, sorted at request snapshot via `prerequest.MaterializeSorted`)
6. **Request Borrowing**:
   - Verified `RequestBorrow: true` on `PlaneToolCallPolicies`, `PlaneToolCallFinalizers`, `PlaneSecretGuards`, `PlaneLocalTurnHandlers`
7. **Diagnostics Projections**:
   - Verified `MaterializeOccupants` and `ProjectPrivileges` on `PlaneSubmitHooks`, `PlaneToolReactors`, `PlaneSecretGuards`

---

## 5. RED Phase Evidence (Task 2 Target Contract Probe)

When asserting the Target Task 2 contract on the current unmigrated codebase, `Contribute` on an unbound plane returns `nil` instead of the expected `ErrUngeneratedPlane`:

```text
--- FAIL: TestClosedPlane_TargetContract_RED_Rejection (0.00s)
    closed_plane_characterization_test.go:177:
        Error Trace: C:/Users/Mateusz/source/repos/go-llm-interactive-proxy-feat-pre-oss-core-slimming/pkg/lipsdk/feature/closed_plane_characterization_test.go:177
        Error: An error is expected but got nil.
        Test: TestClosedPlane_TargetContract_RED_Rejection
        Messages: Task 2 target contract requires unbound plane to be rejected before mutation
FAIL
FAIL	github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature	1.320s
FAIL
```

---

## 6. Verification Commands & Results

```powershell
# Verification of feature SDK and featurebundle packages
go test -count=1 ./pkg/lipsdk/feature ./internal/featurebundle
# Output:
# ok  	github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature	3.178s
# ok  	github.com/matdev83/go-llm-interactive-proxy/internal/featurebundle	0.805s
```
