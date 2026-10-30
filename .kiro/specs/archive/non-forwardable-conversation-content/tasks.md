# Implementation Plan

Implementation is TDD-first. Every production task follows RED → GREEN → refactor. Each sub-task contains no more than five concrete actions.

No task implements interactive commands, Quality Verifier policy, quota-notification policy, or any other concrete producer. Generic fake producers/handlers are used only to certify the infrastructure.

## 1. Freeze Conversation-View Contracts With RED Tests

- [x] 1.1 Freeze semantic message identity and anchor contracts
  - Add RED table tests covering legacy `Instructions`/`Messages` versus item-authority identity equivalence, line-ending/JSON normalization, transient-field exclusion, and repeated-identical-message occurrence ordinals.
  - Add RED negatives for non-message items and partial content-part/sub-string identity requests.
  - Pin a versioned v1 digest format and deterministic canonicalization without persisting/logging plaintext.
  - _Boundary: domain policy / tests_
  - _Depends: none_
  - _Validation: `go test ./internal/core/conversationview/...`_
  - _Requirements: 1.1-1.10, 13.7_

- [x] 1.2 Freeze coherent snapshot and store semantics
  - Define RED contract tests for one A-leg `Snapshot` containing `never_backend` tags plus active steering, with narrow Reader/Tagger/Steering mutation ports.
  - Cover tag batch atomicity/idempotency/4096 cap and overlay no-op/replace/deactivate, revision, immutable slot ordering, 64-overlay and byte caps.
  - Cover A-leg not-found/delete/recreate and owned-copy snapshot semantics.
  - Pin mutation-vs-snapshot linearization behavior for concurrent turns.
  - _Boundary: domain policy / driven-adapter contract tests_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/core/conversationview/... ./internal/core/b2bua/...`_
  - _Requirements: 2.1-2.12, 3.1-3.7, 9.2-9.3, 9.11-9.17, 13.8_

- [x] 1.3 Freeze projection, placement, and cache-prefix invariants
  - Add RED tests for exclusion-first/injection-second projection under legacy and item authority, dependency cleanup, validation, and exact-once steering.
  - Pin `stable_prefix` and `after_ingress_tail`→fixed semantic anchor resolution, including rejection of unsafe/non-forwardable anchors.
  - Across at least three append-only turns, prove unchanged steering produces an exact-prefix normalized model-visible trajectory and never follows the moving tail.
  - Cover `stable_prefix_fallback` and `fail_closed` when a fixed anchor disappears, plus deterministic multiple-overlay slot order.
  - _Boundary: domain policy / tests_
  - _Depends: 1.1, 1.2_
  - _Validation: `go test ./internal/core/conversationview/...`_
  - _Requirements: 5.3-5.7, 9.4-9.8, 10.1-10.9, 10.12_

- [x] 1.4 Freeze SDK producer and local-turn contracts
  - Add RED compile/validation tests for `pkg/lipsdk/nonforwardable`, `pkg/lipsdk/steering`, and `pkg/lipsdk/localturn`.
  - Require bounded steering ID/message/placement/reason/policy types and trusted writer Put/Deactivate semantics without a client transport API.
  - Require local-turn Match to claim only complete normalized source messages and Handle to return bounded assistant text rather than arbitrary streams.
  - Freeze FeatureBundle merge/order/nil-validation behavior for local-turn handlers.
  - _Boundary: SDK/public contract / tests_
  - _Depends: none_
  - _Validation: `go test ./pkg/lipsdk/... ./internal/featurebundle/...`_
  - _Requirements: 4.1-4.6, 7.1-7.13, 9.1-9.5, 9.19-9.20, 13.7_

## 2. Implement the Minimal Domain and Persistence

- [x] 2.1 Implement semantic identity and pure projection
  - Implement `internal/core/conversationview` v1 semantic identity/occurrence helpers with no frontend/provider imports.
  - Implement pure exclusion, in-call dependency cleanup, stable-prefix/fixed-anchor injection, deterministic slot ordering, and anchor policies.
  - Validate resulting calls and return typed fail-closed projection errors; never silently move steering to current tail.
  - Keep request-local provenance separate from replay identity so final reassertion can recognize projection-owned copies safely.
  - _Boundary: domain policy_
  - _Depends: 1.1, 1.3_
  - _Validation: `go test ./internal/core/conversationview/...`_
  - _Requirements: 1.1-1.10, 5.3-5.9, 9.5-9.8, 10.1-10.9_

- [x] 2.2 Implement memory A-leg conversation-view state
  - Extend `b2bua.MemoryStore` with the focused optional conversation-view capability under its existing A-leg lock/lifecycle.
  - Implement one coherent deep-copy Snapshot plus atomic tag batch and steering Put/replace/deactivate with state/overlay revisions and slot allocation.
  - Enforce all count/byte limits and A-leg deletion/eviction semantics without widening `b2bua.Store`.
  - Add compile-time capability assertions and make old/no-state A-legs read as an empty snapshot.
  - _Boundary: driven adapter / core continuity_
  - _Depends: 1.2, 2.1_
  - _Validation: `go test ./internal/core/b2bua/... ./internal/core/conversationview/...`_
  - _Requirements: 2.1-2.12, 3.1-3.7, 9.11-9.17, 13.3, 13.8_

- [x] 2.3 Implement Bun SQLite/PostgreSQL persistence
  - Add additive A-leg-owned migration(s) for exclusion identities, steering overlay payload/anchor/slot/revision state, and coherent state revision as needed.
  - Implement Reader/Tagger/Steering semantics using existing A-leg row-lock/transaction patterns with deterministic ordering and atomic bounds enforcement.
  - Add SQLite restart/delete/recreate/no-op/revision tests and PostgreSQL-gated parity/concurrency tests.
  - Ensure A-leg deletion removes dependent state and shared-process reads require no process cache.
  - _Boundary: driven adapter / persistence_
  - _Depends: 1.2, 2.1_
  - _Validation: `go test ./internal/core/continuity/bunstore/...`; PostgreSQL integration with existing DSN gate_
  - _Requirements: 2.3-2.10, 9.16-9.17, 11.5, 13.3, 13.8_

- [x] 2.4 Implement trusted registrar and steering application services
  - Implement the narrow `nonforwardable` registrar adapter and `steering.Writer` application service over authoritative A-leg conversation-view ports.
  - For `after_ingress_tail`, resolve/validate the current terminal forwardable user message and persist a fixed semantic anchor at Put time.
  - Persist the already rendered model-visible steering payload per revision; semantic no-op Put remains a no-op and plaintext stays out of normal telemetry.
  - Wire services through explicit process/composition construction without a global service locator or client frontend exposure.
  - _Boundary: app orchestration / SDK adapter / composition root_
  - _Depends: 1.4, 2.2, 2.3_
  - _Validation: `go test ./pkg/lipsdk/... ./internal/infra/runtimebundle/... ./internal/core/conversationview/...`_
  - _Requirements: 2.8, 3.6, 9.1-9.3, 9.5-9.7, 9.12-9.20, 13.2, 13.4_

## 3. Integrate Base Projection and Local Success

- [x] 3.1 Split/factor request preparation only as needed to expose the authoritative pre-B-leg seam
  - Characterize existing ordering from accepted canonical ingress through secure A-leg authority, CTP evidence, submit policy, backend-oriented pre-request work, billing and route planning.
  - Refactor minimally so one conversation-view Snapshot/local-turn decision can occur after trusted A-leg/secret/submit boundaries but before inference-specific work.
  - Preserve the unmodified client/A-leg ingress view for CTP/continuation while passing a separate projected clone to backend-oriented stages.
  - Prove no-tag/no-overlay/no-local-claim behavior preserves existing routing/billing/stream semantics.
  - _Boundary: app orchestration / runtime_
  - _Depends: 2.1, 2.2, 2.3_
  - _Validation: focused `go test ./internal/core/runtime/...`_
  - _Requirements: 5.1-5.2, 5.5, 5.8-5.10, 12.1, 13.6_

- [x] 3.2 Integrate one coherent early backend-effective projection
  - Snapshot conversation-view state once after authoritative A-leg resolution and carry it in prepared request state.
  - Project exclusion + persistent steering before backend request/pre-request transforms, context estimation, billing, routing and capability work.
  - Fail closed on snapshot/projection error and prove filtered content does not affect route/context/cost while steering does.
  - Emit bounded projection evidence (counts/revisions/placement classes) without message/steering text.
  - _Boundary: app orchestration / runtime_
  - _Depends: 3.1, 2.4_
  - _Validation: `go test ./internal/core/runtime/...`_
  - _Requirements: 5.1-5.10, 9.8, 10.1-10.5, 12.3-12.4, 13.1_

- [x] 3.3 Implement the generic two-phase local-turn stage
  - Add FeatureBundle/runtime-snapshot support for ordered `localturn.Handler` contributions and run pure Match against the preserved ingress view.
  - On claim, validate source indexes and commit source `never_backend` tags before Handle; merge successful tags into the request-local snapshot.
  - After Handle returns text, construct the canonical assistant message, commit its tag, then create the local finite EventStream from exactly that content.
  - Enforce no B-leg/provider/inference billing after claim and deterministic release of any earlier request concurrency authority.
  - _Boundary: SDK extension + app orchestration_
  - _Depends: 1.4, 2.4, 3.1_
  - _Validation: `go test ./pkg/lipsdk/... ./internal/featurebundle/... ./internal/core/runtime/...`_
  - _Requirements: 4.1-4.6, 7.1-7.13, 8.1-8.7_

- [x] 3.4 Certify local canonical response behavior through frontends
  - Add generic local-stream helpers/factory using existing canonical event sequence with no background goroutine and no provider usage/B-leg identity.
  - Extend bounded official frontend contract tests so streaming/non-streaming encoders accept the same local stream and replay decodes to identity-equivalent assistant content.
  - Prove local reply/claimed source remain client-visible in A-leg/continuation yet are filtered on the next backend turn.
  - Keep all tests producer-neutral; add no command/quota/verifier implementation.
  - _Boundary: core stream / frontend contract tests_
  - _Depends: 3.3_
  - _Validation: `go test ./internal/testkit/contract/... ./internal/plugins/frontends/...` (focused packages)_
  - _Requirements: 8.1-8.7, 11.1-11.3, 13.12-13.14_

## 4. Enforce Final B-Leg View and Cache Stability

- [x] 4.1 Add final conversation-view reassertion at the shared candidate-open choke point
  - After candidate/interleaved/attempt transforms, rebuild/reassert the frozen snapshot so excluded messages cannot reappear and active steering is exact-once at the intended placement.
  - Validate the reasserted candidate call and require normal candidate adaptation to preserve required steering semantics; reject instead of silently move/drop.
  - Emit PTB only from the final reasserted/adapted call, then call backend `Open`.
  - Use no durable store read in this stage; every attempt/race arm receives the same frozen snapshot.
  - _Boundary: app orchestration / runtime safety boundary_
  - _Depends: 3.2_
  - _Validation: `go test ./internal/core/runtime/...`_
  - _Requirements: 6.1-6.9, 11.7, 12.2, 13.1_

- [x] 4.2 Add adversarial late-transform and multi-path runtime tests
  - Add fake attempt transforms that reintroduce tagged messages and remove/move/duplicate persistent steering; prove reassertion restores or rejects before PTB/Open.
  - Cover initial open, pre-output failover/retry, parallel/race, TTFT replacement and interleaved thinker/executor paths through the single guard.
  - Prove an in-flight turn remains on snapshot N while a concurrent mutation N+1 applies to the next turn.
  - Prove existing no-retry-after-output behavior is unchanged.
  - _Boundary: runtime tests_
  - _Depends: 4.1_
  - _Validation: focused runtime suites + targeted `go test -race ./internal/core/runtime`_
  - _Requirements: 6.4-6.9, 9.15, 13.10-13.11, 13.15_

- [x] 4.3 Certify prompt-cache structural invariants and mutation discontinuities
  - Build multi-turn canonical fixtures showing fixed activation ordering (`U_N, STEER, A_N, U_N+1`) and stable-prefix placement across at least three append-only turns.
  - Assert same overlay revision has identical role/text/anchor/order and no per-turn dynamic model-visible metadata; current-tail reinjection must fail tests.
  - Cover create/replace/move/deactivate as explicit cache discontinuities followed by restored prefix stability.
  - Cover anchor disappearance for both stable-prefix fallback and fail-closed policy.
  - _Boundary: domain/runtime cache regression tests_
  - _Depends: 2.1, 3.2, 4.1_
  - _Validation: `go test ./internal/core/conversationview/... ./internal/core/runtime/...`_
  - _Requirements: 10.1-10.12, 13.9-13.10_

- [x] 4.4 Add bounded backend-family translation sentinels
  - Add representative OpenAI-family, Anthropic-family and Gemini-family tests proving the final canonical steering order survives translation and is not silently dropped/repositioned.
  - Prove unsupported required role/placement rejects explicitly through normal pre-open semantics.
  - Keep provider cache controls/TTL/`PromptCacheKey` unchanged and assert no visibility-specific provider branch is introduced.
  - Do not add a frontend×backend Cartesian matrix.
  - _Boundary: backend/adaptor contract tests_
  - _Depends: 4.1, 4.3_
  - _Validation: focused backend-family contract suites; `make parity-checks` where applicable_
  - _Requirements: 6.5, 10.10-10.14, 13.6, 13.17_

## 5. Continuation, Reload, Observability, and Delivery Gates

- [x] 5.1 Certify replay/continuation/reload separation of A-leg and B-leg truth
  - Add legacy full-history and OpenResponses `previous_response_id` tests proving client-visible local messages materialize then filter, while backend-only steering is reconstructed only after materialization.
  - Prove hidden steering never enters frontend continuation/client response/CTP augmentation but is present in PTB/backend calls.
  - Prove durable restart and runtime generation reload retain exclusions/active steering even when the producer/handler is absent afterward.
  - Add shared PostgreSQL process-style tests where one writer mutation is observed by a later turn snapshot without a stale process cache.
  - _Boundary: frontend/runtime/continuity integration tests_
  - _Depends: 2.3, 3.2, 3.4, 4.1_
  - _Validation: focused OpenResponses/runtime/Bun suites_
  - _Requirements: 2.4-2.10, 9.8-9.10, 11.1-11.7, 13.14_

- [x] 5.2 Add bounded diagnostics and security/privacy guards
  - Add content-free metrics/log events for filtering, steering injection/mutation revisions, anchor fallback/failure, projection failure and cache discontinuity.
  - Enforce bounded reason/source codes and prohibit steering/message plaintext or raw digests as high-cardinality labels.
  - Document/test that hidden steering is visible to the remote model/provider and must not carry credentials/secrets.
  - Verify no client/data-plane visibility or steering mutation surface exists and existing secret-guard ordering remains intact.
  - _Boundary: observability/security/docs_
  - _Depends: 2.4, 3.3, 4.1_
  - _Validation: focused diagnostics/security tests_
  - _Requirements: 9.18-9.19, 12.1-12.9, 13.16_

- [x] 5.3 Add performance/race/architecture certification
  - Benchmark/profile no-state fast path and bounded worst cases (4096 exclusion identities, 64 overlays/256 KiB) and confirm no per-candidate I/O.
  - Run concurrent mutation/snapshot/runtime tests under race detector and verify no watcher/background cleanup/service-locator pattern was introduced.
  - Add architecture gates proving core imports no provider/frontends, base/public continuity stores remain unchanged, and provider cache policy/`PromptCacheKey` was not moved into conversation-view core.
  - Review implementation for unnecessary abstractions and keep changed-file scope under repository guardrails.
  - _Boundary: tests/architecture/performance_
  - _Depends: 4.4, 5.1, 5.2_
  - _Validation: targeted benchmarks; `go test -race`; `go test ./internal/archtest/...`; `make quality-checks`_
  - _Requirements: 10.10-10.14, 13.1-13.6, 13.15-13.18_

- [x] 5.4 Update producer and architecture documentation, then run final quality gates
  - Document both visibility directions, local-turn causal tagging, trusted steering Put/Deactivate, fixed activation anchor, cache discontinuities, anchor fallback, and whole-message limits.
  - Document explicitly that interactive commands, Quality Verifier logic and quota-notification policy are separate consumers and require no core projection redesign.
  - Run deterministic unit/contract suites, SQLite tests, PostgreSQL-gated parity where available, architecture checks and targeted race tests; record any environment-gated skips.
  - Perform final traceability review against every requirement and remove any command/verifier/quota/provider-cache-policy implementation that slipped into the diff.
  - _Boundary: docs/tests/final review_
  - _Depends: 5.3_
  - _Validation: `make quality-checks`; `make test-unit`; `make parity-checks`; targeted `go test -race`; PostgreSQL integration when configured_
  - _Requirements: 13.7-13.18_

## Implementation Notes

- Any task adding non-test Go lines under `internal/core` must bump the `internal/core` ratchet in `internal/archtest/budgets.go` (`LineBudgets`, measured+25) with one rationale comment in the existing chronological style; `go test ./internal/archtest/...` is a required per-task gate alongside the focused package tests (caught after 1.1 review; reviewer missed it, controller verification caught it).
- Task 2.2/2.3 store work must: extract the conversationview contract suite into a reusable driver taking port constructors (currently hardwired to `*ReferenceStore` in `storecontract_test.go`), run it against Memory/Bun adapters, and prefer unexporting `GetOverlay` (or gating it for tests) so the narrow Reader/Tagger/SteeringStore surface stays canonical. Tighten `TestContract_ConcurrentSmoke` to exact count and add an aggregate-cap replace-overflow assertion when touching that suite.
- SDK follow-ups from 1.4 review (apply when tasks 2.4/3.3 touch these packages): remove or wire the dead `validateHandlerID` in `pkg/lipsdk/localturn/types.go`; decide whether to unify typed-nil (`localturn.IsNilHandler`) vs `== nil` checks across `pkg/lipsdk/feature/bundle.go` ordered lists; consider sentinel-wrapped errors for SDK validation if typed classification becomes necessary (currently plain `fmt.Errorf`, tests use `require.Error`).
- `internal/archtest` contains MORE gates than line budgets — run the FULL `./internal/archtest/...` suite per task: `TestShrinkage_NetReductionMeetsRequirement115` (Req 11.5 net-reduction floor over runtimebundle/runtimehost/stdhttp/cmd/lipruntime surfaces; only ~70 lines of margin, so additions to those trees need deliberate overlay accounting or compensating placement) and `TestHexagonalMigrationBaselineMatchesGoList` (locked direct-import baselines per composition package; new imports there require deliberate baseline updates). Task 2.4's runtimebundle composition seam tripped both; fixed by relocating capability helpers to `internal/core/conversationview/sdkadapter/services.go` (runtimebundle left pristine). Tasks 3.x/5.x touching runtimebundle/stdhttp must plan for these gates up front.
- Task 5.2 must wire `SteeringState.CacheDiscontinuityKind`/`CacheDiscontinuityPlacement` to bounded diagnostics (`operation`, `placement` only); task 4.3 records the fields across Reference/Memory/Bun stores, but the SDK writer mapping intentionally drops them today. Never label by OverlayID, A-leg ID, digest, or plaintext.

## Completion Status

- [x] All implementation tasks completed and verified on `origin/main` @ `b763a772`
- [x] Task completion gate satisfied: every checkbox is `[x]` (geoip 33/33, non-forwardable 20/20, runtime 24/24, request tranche via typed pipeline)
- [x] Implementation PR(s) merged:
  - PR #435 `feat: add non-forwardable conversation content` — head `304cc4a685ac18f8a43182192c9e98aa39c9fbf4` merged as `b763a772aecc265c643802363bc7fc340437adda` on 2026-08-23T21:02:18Z — https://github.com/matdev83/go-llm-interactive-proxy/pull/435
- [x] Required focused tests and architecture gates pass on merged baseline (see PR CI)
- [x] No successor-only removals falsely marked complete; successor scope documented as deferred (see spec project_description)
- [x] `spec.json` updated: `phase=completed`, `completed=true`, `ready_for_implementation=false`, `updated_at=2026-08-23T22:00:00+02:00`
