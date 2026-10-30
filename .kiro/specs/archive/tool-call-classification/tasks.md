# Implementation Plan

## 1. Freeze Derived-Metadata Contracts With RED Tests

- [x] 1.1 Define RED canonical classifier and ToolEvent metadata tests
  - Add table-driven tests for every exact alias in the requirements, including PascalCase/case variants, surrounding whitespace, empty input, and unknown input.
  - Assert the exact category strings and `MayMutateLocalFS` values, including `apply_patch -> file_edit/true`, generic explicit removal aliases -> `file_remove/true`, read-only web lookup -> `web_access/false`, browser automation -> `web_access/true`, and unknown -> `unknown/true`.
  - Extend `ToolEventFromEvent` tests to require derived fields on name-bearing events and conservative `unknown/true` on a directly projected name-less event before runtime correlation exists.
  - Observable completion: tests compile against the intended canonical API and fail because `ToolCategory`, classifier, and ToolEvent fields do not yet exist.
  - _Requirements: 1.1-1.11, 2.1-2.8, 3.1-3.2, 6.1-6.2, 6.6_
  - _Design rules: D1, D2, D3, D4_
  - _Boundary: pkg/lipapi tests only_
  - _Depends: none_
  - _Validation: go test ./pkg/lipapi/..._

- [x] 1.2 Define RED lifecycle-correlation tests for name-less fragments
  - Add focused runtime tests for `started(name) -> args_delta(no name) -> finished(no name)` and assert the same derived classification reaches the tool-policy/reactor seam on every lifecycle item.
  - Add two interleaved tool-call IDs with different categories and prove classification never crosses IDs.
  - Prove orphan name-less events use `unknown/true`, finished cleanup prevents stale ID reuse, and recv inner-stream reset/replacement clears abandoned lifecycle state.
  - Observable completion: tests fail at the missing request-local lifecycle classifier state while existing stream behavior remains otherwise characterized.
  - _Requirements: 3.2-3.8, 6.3-6.4_
  - _Design rules: D5, D6, D10_
  - _Boundary: internal/core/runtime tests only_
  - _Depends: 1.1_
  - _Validation: go test ./internal/core/runtime/..._

- [x] 1.3 Define RED rewrite-coherence tests
  - Extend hook-bus tests so a reactor rename from a read tool to an exec tool is reclassified before the next reactor observes it.
  - Prove a same-ID rewrite that omits `ToolName` preserves current derived metadata, while a different-ID/name-less replacement becomes `unknown/true`.
  - Prove reactor-authored contradictory category/bool values cannot override classification derived from a non-empty effective name.
  - Add runtime coverage that a completed-call finalizer renamed lifecycle is classified through the normal path and that a later generic response-hook rename refreshes lifecycle state for a subsequent name-less fragment.
  - Observable completion: tests fail until rewrite reconciliation and final-name observation are implemented centrally.
  - _Requirements: 4.1-4.6, 6.5_
  - _Design rules: D6, D7, D8, D9_
  - _Boundary: internal/core/hooks and internal/core/runtime tests_
  - _Depends: 1.1, 1.2_
  - _Validation: go test ./internal/core/hooks/... ./internal/core/runtime/..._

## 2. Implement the Minimal Classifier and Lifecycle Enricher

- [x] 2.1 Implement canonical ToolCategory, name classifier, and additive ToolEvent fields
  - Add `ToolCategory` constants and `ClassifyToolName(name string) (ToolCategory, bool)` in `pkg/lipapi` using only trim, case-fold, and exact switch cases.
  - Keep all aliases in this single helper; do not add regex/fuzzy rules, provider detection, configuration, registry state, argument inspection, or external dependencies.
  - Add `Category` and `MayMutateLocalFS` to `lipapi.ToolEvent` and populate them from `ToolName` in `ToolEventFromEvent`.
  - Make the canonical RED tests from 1.1 green without changing generic `lipapi.Event`.
  - _Requirements: 1.1-1.11, 2.1-2.8, 3.1-3.2, 5.1-5.4, 5.7_
  - _Design rules: D1, D2, D3, D4_
  - _Boundary: pkg/lipapi only_
  - _Depends: 1.1_
  - _Validation: go test ./pkg/lipapi/..._

- [x] 2.2 Implement request-local ToolCallID classification correlation
  - Add one private zero-value runtime helper with a lazily allocated `ToolCallID -> {category, mayMutate}` map; do not add an interface, constructor dependency, mutex, goroutine, TTL, or persistence.
  - Enrich each `ToolEvent` before tool policy: classify/remember non-empty names, inherit known classification for name-less fragments, and otherwise use `unknown/true`.
  - Keep correlation keyed by the incoming/source lifecycle ID even if an outbound reactor event changes its ID.
  - Remember the effective post-reactor classification for the source lifecycle, delete state after finish processing, and clear state when recv replacement/reset abandons an inner lifecycle.
  - Make the RED lifecycle tests from 1.2 green.
  - _Requirements: 3.2-3.8, 5.5-5.6_
  - _Design rules: D5, D6, D10_
  - _Boundary: internal/core/runtime tool-event path only_
  - _Depends: 1.2, 2.1_
  - _Validation: go test ./internal/core/runtime/..._

- [x] 2.3 Reconcile derived metadata across existing rewrite hooks
  - In the existing tool-reactor chain, reclassify every valid rewrite/replace that supplies a non-empty effective `ToolName` before passing it to the next reactor.
  - For same-ID name-less rewrites, preserve current derived metadata; for changed-ID/name-less replacements, force `unknown/true`.
  - Treat category/bool as derived fields: ignore contradictory reactor-authored values whenever an effective name is present.
  - After general response-part hooks, refresh runtime lifecycle state from a final non-empty tool name; do not expose classification on generic `lipapi.Event` or modify response-hook APIs.
  - Reuse the normal lifecycle for completed-call finalizer rewrites; do not modify finalizer/tool-call-repair contracts.
  - Make the RED rewrite tests from 1.3 green.
  - _Requirements: 4.1-4.6, 5.3-5.6_
  - _Design rules: D7, D8, D9_
  - _Boundary: internal/core/hooks + the existing runtime integration points_
  - _Depends: 1.3, 2.1, 2.2_
  - _Validation: go test ./internal/core/hooks/... ./internal/core/runtime/..._

## 3. Prove Compatibility and Keep the Feature Small

- [x] 3.1 Run the cross-harness classification matrix and regression suite
  - Keep one data-driven test matrix as the source of truth for the surveyed Codex/Pi/Cline/OpenCode/Hermes/OpenClaw/Kilo/Claude aliases; case-folding should cover casing dialects rather than duplicate production branches.
  - Add explicit regression rows for OS-command calls containing read-only command text and prove the name-only result stays `os_command/true` without parsing arguments.
  - Verify patch/removal distinctions, read-only web versus browser mutation posture, and unknown conservative fallback.
  - Verify existing policy/reactor signatures, tool-call repair/finalizer behavior, completion gates, and generic canonical event validation remain compatible.
  - _Requirements: 1.1-1.11, 2.1-2.8, 4.1, 5.1-5.7, 6.1-6.7_
  - _Design rules: D1-D10_
  - _Boundary: focused canonical/hook/runtime regression tests; no provider-network tests_
  - _Depends: 2.1, 2.2, 2.3_
  - _Validation: go test ./pkg/lipapi/... ./internal/core/hooks/... ./internal/core/runtime/..._

- [x] 3.2 Run repository architecture and quality gates and remove accidental scope growth
  - Run formatting/vet/architecture checks and the default deterministic unit suite appropriate to the changed packages.
  - Confirm no provider/frontend/backend/connector package was modified to add classification, no generic `lipapi.Event` field was introduced, and no new dependency/config/registry/persistence/goroutine/synchronization surface appeared.
  - Review the final diff for redundant abstractions; if an interface/service wrapper was introduced solely for testability, remove it and test the concrete pure helper/private state directly.
  - Document the boolean as potential capability only and confirm no code path uses it to change tool execution as part of this feature.
  - _Requirements: 2.8, 5.1-5.7, 6.7-6.8_
  - _Design rules: D1-D10; Design Invariants 1-8_
  - _Boundary: repository verification/refactor only_
  - _Depends: 3.1_
  - _Validation: make quality-checks; make test-unit_
