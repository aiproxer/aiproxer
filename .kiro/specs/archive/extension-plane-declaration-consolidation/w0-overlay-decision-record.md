# Extension Overlay Decision Record (W0 Characterization for Migration Waves)

**Spec**: `extension-plane-declaration-consolidation`  
**Date**: 2026-08-26  
**Task**: 1.2 Capture scalar, exclusive, and test-overlay parity  
**Target Execution Waves**: W1 (Hooks), W2 (Observers), W3 (Request-Shaping), W4 (Tools, Task 7.2), W5 (Compaction/Secrets/Terminal)

---

## 1. Executive Summary

During Task 1.2 characterization, we conducted an exhaustive census of all 26 feature extension planes, 4 host capability fields, and the lifecycle side-channel across the merge engine (`internal/featurebundle/merge_surface.go`), composition projection (`extensionsFromMerged`), and the test-only extension overlay (`overlayExtensions` in `compile_generation.go`).

The test-only overlay seam (`overlayExtensions`) exists solely to support test fixtures injecting synthetic options into `CompileGeneration` via `GenerationCompileInput.CandidateOpts`. In production runtime boot and config reload, `in.CandidateOpts` is `nil` and `overlayExtensions` is never invoked.

This document records the exact characterization of every field, catalogs all discrepancies between the merge engine and the overlay seam, and establishes authoritative, conservative **PRESERVE** or **REMOVE** decisions so downstream waves (specifically W4 Task 7.2 and W5 Task 8.4) can execute without re-litigating overlay semantics.

---

## 2. Census and Decision Matrix

| # | Plane / Field Name | Multiplicity & Layer | Merge Engine Rule (`MergedFeatureSurface.Append`) | Test Overlay Rule (`overlayExtensions`) | Discrepancy? | Decision for Migration | Rationale & Reachability Evidence |
|---|---|---|---|---|---|---|---|
| 1 | `SubmitHooks` | Ordered Slice (`pkg/lipsdk/hooks`) | Append | Omitted (handled via `hooksConfigFromMerged`) | N/A | **REMOVE** direct overlay | Hook bus is constructed separately as derived view in W1 (`hooks.New(hooksConfigFromMerged)`). |
| 2 | `RequestPartHooks` | Ordered Slice (`pkg/lipsdk/hooks`) | Append | Omitted | N/A | **REMOVE** direct overlay | Derived hook view in W1. |
| 3 | `ResponsePartHooks` | Ordered Slice (`pkg/lipsdk/hooks`) | Append | Omitted | N/A | **REMOVE** direct overlay | Derived hook view in W1. |
| 4 | `ToolReactors` | Ordered Slice (`pkg/lipsdk/hooks`) | Append | Omitted | N/A | **REMOVE** direct overlay | Derived hook view in W1. |
| 5 | `ToolReactorErrorPolicy` | Host Scalar / Config | Parsed from frozen config | Omitted | N/A | **REMOVE** direct overlay | Config-owned host scalar projected in W1. |
| 6 | `Lifecycles` | Ordered Slice (`pkg/lipsdk/plugin`) | Append (separated to side channel) | Overlaid via `CandidateOpts.FeatureLifecycles` | No (explicit channel) | **PRESERVE** | Lifecycles remain a separately owned lifecycle side channel; used in rollback tests. |
| 7 | `SessionOpeners` | Ordered Slice (`pkg/lipsdk/session`) | Append | Append (`dst = append(dst, src...)`) | No | **PRESERVE** append | Standard slice concatenation; unified in W3. |
| 8 | `WorkspaceResolvers` | Ordered Slice (`pkg/lipsdk/workspace`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W3. |
| 9 | `ToolCatalogFilters` | Ordered Slice (`pkg/lipsdk/toolcatalog`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W4. |
| 10 | `ToolCallPolicies` | Ordered Slice (`pkg/lipsdk/toolpolicy`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W4. |
| 11 | `ToolCallFinalizers` | Ordered Slice (`pkg/lipsdk/toolcall`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W4. |
| 12 | `ToolCallFinalizationMaxArgsBytes` | Scalar Int | **Min-reduction** of positive values (`if b > 0 && (m <= 0 \|\| b < m) { m = b }`); zero is unset | **Overwrite-if-positive** (`if src > 0 { dst = src }`) | **YES (Divergence)** | **REMOVE** test overwrite divergence; **CONVERGE** on min-reduction in W4 (Task 7.2) | Overwrite-if-positive in overlay was an unpinned legacy anomaly. Zero production callers and zero test fixtures pass positive scalar overlay. In W4 (Task 7.2), unified declarative min-reduction (`CombReduce`) will apply to all paths. |
| 13 | `RequestTransforms` | Ordered Slice (`pkg/lipsdk/request`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W3. |
| 14 | `PreRequestHandlers` | Ordered Slice (`pkg/lipsdk/prerequest`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W3. |
| 15 | `RouteHintProviders` | Ordered Slice (`pkg/lipsdk/routehint`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W3. |
| 16 | `CompletionGates` | Ordered Slice (`pkg/lipsdk/completion`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W3. |
| 17 | `AttemptTransforms` | Ordered Slice (`pkg/lipsdk/request`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W3. |
| 18 | `StreamObserverFactories` | Ordered Slice (`pkg/lipsdk/response`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W2. |
| 19 | `TrafficObservers` | Ordered Slice (`pkg/lipsdk/traffic`) | Append (feature), then Host TrafficObservers appended | Append | No | **PRESERVE** append | Feature-then-host ordering preserved in W2. |
| 20 | `UsageObservers` | Ordered Slice (`pkg/lipsdk/usage`) | Append (feature), then Host UsageObservers appended | Append | No | **PRESERVE** append | Feature-then-host ordering preserved in W2. |
| 21 | `RawCaptureSinks` | Ordered Slice (`pkg/lipsdk/traffic`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W2. |
| 22 | `TrafficRedactors` | Ordered Slice (`pkg/lipsdk/traffic`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W2. |
| 23 | `CompactionObservers` | Ordered Slice (`pkg/lipsdk/compaction`) | Append (`extensionsFromMerged` copies) | **Omitted** from `overlayExtensions` (src is ignored, dst retains dst only) | **YES (Omission)** | **PRESERVE** omission during W0-W4; **CONVERGE** on standard append in W5a | Pre-existing omission in `overlayExtensions` (also omitted from `hasExtensionOverlay`). In W5a, candidate `FrozenPlaneSet` concatenation applies uniformly. |
| 24 | `CompactionPreservers` | Ordered Slice / Generation Binder | Append on `MergedFeatureSurface`, then rebound via `bindCompactionContinuity` | **Omitted** (not present on `ExtensionsOptions`) | **YES (Omission)** | **PRESERVE** omission | Compaction preservers are managed by the generation binder (`internal/infra/compactioncompose`), not plane overlay. Migrates to typed replacement binder in W5a. |
| 25 | `SecretGuards` | Ordered Slice (`pkg/lipsdk/secretguard`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W5a. |
| 26 | `LocalTurnHandlers` | Ordered Slice (`pkg/lipsdk/localturn`) | Append | Append | No | **PRESERVE** append | Standard slice concatenation; unified in W5b. |
| 27 | `TerminalDecisionProvider` | Exclusive Single Slot (`pkg/lipsdk/terminaldecision`) | **Exclusive Conflict Rejection** (`ErrTerminalDecisionProviderConflict` with `%q and %q`) | **First-Wins** (`if dst == nil { dst = src }`, second contribution dropped silently) | **YES (Divergence)** | **REMOVE** silent first-wins; **CONVERGE** on strict exclusive conflict error in W5b (Task 8.4) | First-wins in overlay was defensive code. No test or production caller relies on silent dropping of conflicting providers. In W5b, unified declarative exclusive slot rule (`CombExclusive`) enforces fail-closed rejection across all paths. |
| 28 | `SecretGuardInputs` | Host Capability Struct | Injected from `ps.opts.Extensions.SecretGuardInputs` | **Omitted** from `overlayExtensions` (dst keeps dst) | **YES (Omission)** | **PRESERVE** omission | Composition-root host capability; not a feature plane. |
| 29 | `SecretGuardEnvironment` | Host Capability Interface | Injected from `ps.opts.Extensions.SecretGuardEnvironment` | Overwrite-if-non-nil (`if src != nil { dst = src }`) | No | **PRESERVE** overwrite-if-non-nil | Host capability override preserved until W5a. |
| 30 | `SecretDecisionObserver` | Host Capability Interface | Injected from `ps.opts.Extensions.SecretDecisionObserver` | Overwrite-if-non-nil (`if src != nil { dst = src }`) | No | **PRESERVE** overwrite-if-non-nil | Host capability override preserved until W5a. |

---

## 3. Discrepancy Deep-Dive and Migration Analysis

### 3.1 Discrepancy 1: `ToolCallFinalizationMaxArgsBytes` (Scalar Min-Reduction vs Overwrite)

- **Characterized Behavior**:
  - Merge engine (`(*MergedFeatureSurface).Append`): takes `min(dst, src)` across all positive contributions. Zeros and negatives are ignored as unset/invalid.
  - Overlay seam (`overlayExtensions`): if `src > 0`, it unconditionally sets `dst = src`. This causes `overlayExtensions` with `dst=1024, src=4096` to produce `4096`, overwriting the smaller buffer cap.
- **Reachability & Callers**:
  - Production code: `in.CandidateOpts` is never set during runtime operations.
  - Test suite census: No test in the repository sets `in.CandidateOpts.Extensions.ToolCallFinalizationMaxArgsBytes`. Only characterization unit tests (`TestOverlayExtensions_FinalizerCapOverwriteIfPositiveDivergence`) exercise this branch.
- **Decision for W4 (Task 7.2)**:
  - **REMOVE** the test-only overwrite divergence.
  - When Task 7.2 migrates tool finalizers and buffer reduction, delete `overlayExtensions` and project candidate options from the unified `FrozenPlaneSet` using the declared `CombReduce` min-reduction rule.

### 3.2 Discrepancy 2: `TerminalDecisionProvider` (Exclusive Conflict vs First-Wins)

- **Characterized Behavior**:
  - Merge engine: Rejects second provider contribution with `ErrTerminalDecisionProviderConflict: "<first>" and "<second>"`. Leaves receiver byte-for-byte unchanged (fail-before-mutate).
  - Overlay seam: `if dst.TerminalDecisionProvider == nil { dst.TerminalDecisionProvider = src.TerminalDecisionProvider }`. If `dst` already has a provider and `src` contributes another, `src` is silently ignored with no error.
- **Reachability & Callers**:
  - Production code: Never passes duplicate providers.
  - Test suite census: Tests using `CandidateOpts` (`terminal_decision_lifecycle_projection_red_test.go`) use it for lifecycle testing or fallback validation, never relying on silent dropping of conflicting providers.
- **Decision for W5b (Task 8.4)**:
  - **REMOVE** silent first-wins divergence.
  - In Task 8.4, candidate compilation will validate and combine exclusive slots via the manifest's declared `CombExclusive` rule, raising `ErrTerminalDecisionProviderConflict` whenever two distinct providers target the same candidate.

### 3.3 Discrepancy 3: `CompactionObservers` & `CompactionPreservers` Omission

- **Characterized Behavior**:
  - `CompactionObservers` is copied by `extensionsFromMerged`, but was omitted from `overlayExtensions` and `hasExtensionOverlay`.
  - `CompactionPreservers` is not on `ExtensionsOptions` because it is rebound at the generation level via `bindCompactionContinuity`.
- **Decision for W5a (Tasks 8.1 & 8.2)**:
  - **PRESERVE** omission during W0–W4.
  - In W5a, `CompactionObservers` joins the declarative slice planes (`CombConcatenate`), and `CompactionPreservers` uses the declared generation-binder replacement operation (`CombReplaceByIdentity`).

### 3.4 Discrepancy 4: `SecretGuardInputs` Omission

- **Characterized Behavior**:
  - `SecretGuardInputs` is present on `ExtensionsOptions`, but `overlayExtensions` does not modify it.
- **Decision**:
  - **PRESERVE** omission. `SecretGuardInputs` is a process host capability injected at `BuildHost`, not a feature contribution.

---

## 4. Directive for Task 7.2 (W4)

When Task 7.2 (`Migrate tool finalizers and finalizer-buffer reduction`) is executed:
1. Apply the **REMOVE** decision for the `ToolCallFinalizationMaxArgsBytes` overlay overwrite rule.
2. The plane declaration for `ToolCallFinalizationMaxArgsBytes` shall declare:
   - Multiplicity: `MultOrdered` (or scalar reduction)
   - Source Rule for Feature: `CombReduce` (min-reduction)
   - Identity: `nil`
3. Delete the hand-written `ToolCallFinalizationMaxArgsBytes` branch in `overlayExtensions` and `Append`.
4. Do not re-litigate the overwrite rule during W4 reviews; cite this decision record as the agreed architectural baseline.