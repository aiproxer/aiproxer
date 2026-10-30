# Implementation Plan

> The owner authorized implementation and the final F5/F6 design repair without
> changing requirements. Existing checkmarks are historical, not acceptance of
> the final audit findings. Task 3's delayed-result prefix proof and task 5's
> runner-visible fail-open diagnostics require fresh repair/review evidence;
> task 6 integration must be rerun on that snapshot. No checkboxes are changed here.

- [x] 1. Feature configuration and opt-in registration
  - `Config`, `DecodeConfig`, defaults and validation (unknown keys rejected, field-named errors); `ID = "tool-result-elision"`; standard table row and factory; a disabled row contributes no transform.
  - Observable: `lipstd check-config` accepts the example, rejects an invalid value naming the field, and a disabled row leaves the plane set empty.
  - _Requirements: 1.1, 1.4, 4.4_
  - _Boundary: feature plugin + config/wiring_
  - _Depends: none_
  - _Validation: `make dev-test PKGS='./internal/plugins/features/toolresultelision/... ./internal/standardplugins/...'`_
- [x] 2. Trajectory index for both canonical shapes (P)
  - Pure index of assistant tool-call rounds, result locations, resolved tool names, text payload views and exact-replay markers (item ID, item reference, unresolved previous-response delta) over legacy messages and items.
  - Observable: table tests resolve names from calls and the result fallback, mark non-text and exact-replay results ineligible, and return an empty index when rounds cannot be recognised.
  - _Requirements: 2.4, 3.1_
  - _Boundary: feature plugin (domain policy)_
  - _Depends: none_
  - _Validation: `make dev-test PKGS='./internal/plugins/features/toolresultelision/...'`_
- [x] 3. Deterministic planner: duplicates, protected rounds, batch frontier
  - First occurrence kept; later identical results reference the first call; stop at the first protected result (including unique/first/ineligible entries); batch every eligible duplicate in that stable prefix at `min_batch_bytes`, never later islands.
  - Observable: table tests for every acceptance case, plus a property test over random append-only extensions proving prior elisions persist with identical markers and re-runs are identical.
  - _Requirements: 2.1, 2.2, 3.1, 3.2, 3.3, 4.1, 4.2, 4.3_
  - _Boundary: feature plugin (domain policy)_
  - _Depends: 2_
  - _Validation: `make dev-test PKGS='./internal/plugins/features/toolresultelision/...'`_
- [x] 4. Marker rendering and in-place applier
  - Deterministic bounded marker (call ID ≤128 printable ASCII, else skip); replacement on a clone for legacy parts and items keeping position, IDs, call association and name; `Validate` before swapping in; all-or-nothing.
  - Observable: both representations elide correctly, nothing is added, removed or reordered, and a forced validation failure leaves the call byte-identical.
  - _Requirements: 2.1, 2.3, 2.5, 5.3_
  - _Boundary: feature plugin_
  - _Depends: 3_
  - _Validation: `make dev-test PKGS='./internal/plugins/features/toolresultelision/...'`_
- [x] 5. Request transform with fail-open guard and counter recorder
  - `request.Transform` (high `Order`, `FailOpen`, panic-safe, leaves `*call` untouched on error); atomic recorder for considered, elided, skipped_exact and estimated tokens saved; no content in counters.
  - Observable: errors/panics restore exact input and return sanitized classifications; the real FailOpen runner continues un-elided, emits content-free diagnostics and a skip metric. Feature counters move only on successful passes with the closed outcomes.
  - _Requirements: 1.2, 5.1, 5.2, 5.3_
  - _Boundary: feature plugin_
  - _Depends: 4_
  - _Validation: `make dev-test PKGS='./internal/plugins/features/toolresultelision/...'`_
- [x] 5.1 Preserve proven rewritten steering boundaries across duplicate-occurrence drift
  - In Reassert only, use the existing frozen-baseline/early-provenance structural proof to retain the distinguishable original boundary when a resolvable carried anchor and the stored exact occurrence select different slots. Record the effective carried anchor without changing stored identity or snapshot; apply the same contract to legacy and item-message resolution.
  - Preserve exact behavior for Project, absent/refused lineage and matching boundaries; preserve missing-exact carry-forward, exclusions, provenance cleanup, stable-prefix placement and configured missing-anchor policies. No ordinal-only preference, proof weakening, feature-specific core branch, invented turns or reordering.
  - Observable: a focused core regression keeps steering after the rewritten middle of three identical tool results with distinct call IDs, not the later exact duplicate, with correct effective provenance. Revise only the opposing conflict expectation in the existing exact-resolution test; retain its unchanged/exact case and reuse existing refusal/ambiguity, reorder/deletion, missing-evidence and overlay coverage. Add only a focused item-message conflict counterpart if not already covered.
  - _Requirements: 3.4_
  - _Boundary: core/conversationprojection (generic placement contract, implementation and focused tests/comments only)_
  - _Depends: 5_
  - _Validation: `make dev-test PKGS='./internal/core/conversationprojection/...'`; `make dev-contract-check` before acceptance; task 6 validates the executor consumer._
- [x] 6. Executor integration: B-leg only, preflight on reduced call, steering preserved
  - Through the real executor with the feature enabled:
    - the A-leg ingress and continuation record match a disabled run byte for byte;
    - the PTB capture shows markers;
    - the context preflight sees the reduced size;
    - after-message steering anchored to an elided tool-result user message stays at the same boundary after `Reassert`.
  - With the row disabled, B-leg bytes match an absent feature.
  - _Requirements: 1.2, 1.3, 3.4, 5.4_
  - _Boundary: core/runtime integration test + feature plugin_
  - _Depends: 5, 5.1_
  - _Validation: `make dev-test PKGS='./internal/core/runtime/... ./internal/plugins/features/toolresultelision/...'`_
- [x] 7. Process-owned collector export through featurehost
  - Construct one `PrometheusCollector` per process in `featurehost`, register it through the generic metrics registry, and bind its recorder to each generation's bundle (keep-warm precedent). It is inert when metrics are disabled.
  - Observable: series appear only with the closed `outcome` label; the cardinality ratchet is extended; reload keeps one collector.
  - _Requirements: 5.1, 5.2_
  - _Boundary: config/wiring (composition root)_
  - _Depends: 5_
  - _Validation: `make dev-test PKGS='./internal/standardplugins/... ./internal/infra/metrics/...'`_
- [x] 8. Operator example and short README section
  - `config/examples/tool-result-elision.yaml` (disabled by default, with an enabled snippet) and one section in `internal/plugins/features/README.md`: what is elided, what is never touched, defaults, and that large requests take the canonical path when enabled.
  - Observable: `lipstd check-config` accepts the example.
  - _Requirements: 1.1_
  - _Boundary: docs_
  - _Depends: 1_
  - _Validation: `go run ./cmd/lipstd check-config --config config/examples/tool-result-elision.yaml`_

## Implementation Notes
- Identity uses framed exact representation (not Content-else-Text or concatenated Parts); payload bytes are separate size accounting. Ambiguous carriers/auxiliary fields are excluded. Rounds count assistant tool-call turns only; consecutive ToolCall items share one round. The old round-order prefix proof was false for delayed results; task 3 uses the protected-result barrier. Direct Handle failure is intentional: request continuation and skip observation belong to the FailOpen runner, not a swallowed handler error.

## Completion Status

- [x] All nine leaf tasks checked, including prerequisite 5.1; no unchecked, blocked or deferred task remains in this plan.
- [x] The repairs the preamble requires before acceptance were delivered: payload/encoder identity (F1–F3), item-lineage ambiguity refusal (F4), delayed-result stable-prefix batching (F5), runner-visible sanitized fail-open diagnostics (F6), reused-call-ID and repeated-carrier anchor exclusion, and inclusive flush-time anchor uniqueness — each with canonical-valid regressions and independent panel/judge approvals.
- [x] Task 6 integration was rerun on the repaired snapshot through the real executor; feature, encoder, host and runtimebundle consumer suites passed with fresh receipts.
- [x] Implementation PR #1229 `feat: opt-in backend tool-result elision` — head `d652717062138c318342afa702c5199d9e9ea2d8`, merged as `61c955958cf0bd748dd4897a1001a427cf37c9dd` on 2026-10-17T21:51:53Z — https://github.com/matdev83/go-llm-interactive-proxy/pull/1229
- [x] Hosted GitHub checks were billing-blocked; feasible Linux CI equivalents ran locally on the repaired head with source-bound receipts (tagged Linux shards 418 packages, owner-callback gate, QA policy/architecture, pinned lint, govulncheck, distribution build/config checks, official OpenResponses 17/17, SQLite/PostgreSQL 17.11 parity). Windows/macOS, remote-only race and CodeQL (disabled by repository policy) remain unverified and are not claimed; local Go 1.27.2 differs from CI's configured 1.26.x toolchain.
- [x] Deferred scope remains as listed in `requirements.md` (uneventful-result class, superseded reads, lossy truncation and coverage caps, retrieve tool, structural-cause publication, admin surfaces, large-payload wire parity, multimodal) and is not implied by this archive.
- [x] `spec.json` updated: `phase=completed`, `completed=true`, `ready_for_implementation=false`.
