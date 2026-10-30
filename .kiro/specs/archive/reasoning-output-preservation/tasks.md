# Implementation Plan

**Source context:** GitHub issue [#157 — Reasoning output preservation](https://github.com/matdev83/go-llm-interactive-proxy/issues/157)

> **TDD gate:** Phase 1 introduces public/canonical interfaces, fixtures, and failing executable contracts only. Production runners, stores, feature behavior, and adapter mappings begin in Phase 2. Every later task starts from a focused red test, implements the smallest correct change, and refactors only after the focused package is green.

## Phase 1 — RED: Contracts, Fixtures, and Failing Acceptance Tests

- [x] 1.1 Freeze canonical reasoning and hard replay contracts
  - Add contract shapes and red tests for assistant-only ordered reasoning, dialect/payload validation, byte/count limits, deep cloning, sizing/counting/checkpoint participation, and non-downgradable `reasoning_replay`.
  - Add fixtures for text reasoning, signed thinking, redacted/opaque thinking, multiple blocks, and interleaved tool calls.
  - **Deliverable:** reviewable canonical tests fail only because the contracts are not implemented.
  - _Requirements: 2.1–2.8, 9.1, 9.3_
  - _Design rules: D1, D5, D13_
  - _Boundary: `pkg/lipapi` contracts and tests_
  - _Depends: approved requirements and design_
  - _Validation: focused `go test ./pkg/lipapi/`_

- [x] 1.2 Define attempt-transform contracts and red ordering tests
  - Add `AttemptTransform`, candidate metadata, replay support, `continue`, and `exclude_candidate` shapes plus optional schema-V1 bundle contribution.
  - Prove the stage must run on `CloneCall(baseline)` after interleaved shaping and before final capabilities, context eligibility, preflight, backend-ingress freeze, authorization, and `Open`.
  - **Deliverable:** extension/runtime tests define the missing candidate stage and failover semantics without production implementation.
  - _Requirements: 5.1–5.10, 9.1, 9.4_
  - _Design rules: D2, D3, D5, D11, D13_
  - _Boundary: `pkg/lipsdk/request`, core extensions/runtime, tests_
  - _Depends: 1.1_
  - _Validation: focused request-extension and executor tests_

- [x] 1.3 Define final-stream observer contracts and red lifecycle tests
  - Add observer factory, read-only observer, metadata, and exactly-once outcomes for success release, failure, cancellation, early close, replacement, and gate replacement.
  - Prove observers receive post-response-hook/post-gate events, do not enable completion buffering, and open only for the active surfaced B-leg.
  - **Deliverable:** every commit/discard terminal path is executable before a runner exists.
  - _Requirements: 3.1–3.9, 8.7, 9.1, 9.4_
  - _Design rules: D2, D4, D9, D11, D13_
  - _Boundary: `pkg/lipsdk/response`, core runtime, tests_
  - _Depends: 1.1_
  - _Validation: focused observer, recv, gate, close, and parallel tests_

- [x] 1.4 Add red feature-domain, store, and privacy tests
  - Cover strict config, catalog precedence, exact normalized anchors, placement, preserved/missing/conflicting/ambiguous/unmatched classification, idempotent restoration, and unrepresentable/state policies.
  - Define a TurnStore contract for TTL, turn/per-turn/session bounds, atomic append/eviction, defensive copies, session/plugin isolation, and concurrency.
  - **Deliverable:** feature behavior is specified before config, store, matcher, observer, or transform implementation.
  - _Requirements: 1.1–1.8, 4.1–4.10, 6.1–6.10, 8.1–8.7, 9.1, 9.3_
  - _Design rules: D6, D7, D8, D10, D12, D13_
  - _Boundary: `internal/plugins/features/reasoningpreservation` tests_
  - _Depends: 1.1–1.3_
  - _Validation: focused feature-package tests_

- [x] 1.5 Add red adapter goldens and composed routing scenarios
  - Add Chat, Responses, Anthropic, OpenRouter/compatible, and explicit Gemini-unsupported request/response fixtures.
  - Add disabled, sequential/recv failover, context-limit-after-restoration, weighted, parallel, response-hook mutation, and gate-replacement scenarios.
  - **Deliverable:** protocol and routing gaps are reproducible without provider network calls.
  - _Requirements: 1.1, 2.7, 3.2–3.7, 5.2–5.10, 7.1–7.9, 9.4, 9.5_
  - _Design rules: D1, D3, D4, D5, D9, D12, D13_
  - _Boundary: frontend/backend plugins, runtime integration, tests_
  - _Depends: 1.1–1.3_
  - _Validation: focused adapter, parity, and executor tests_

## Phase 2 — GREEN: Canonical Contracts and Generic Extension Seams

- [x] 2.1 Implement canonical reasoning and hard replay negotiation
  - Implement `PartReasoning`, bounded provider-neutral payloads, assistant-role validation, deep clone/equality/size/token/checkpoint support, and fuzz-safe helpers.
  - Add `CapabilityReasoningReplay` and normalized candidate replay support; keep replay outside soft downgrades.
  - **Deliverable:** canonical calls safely carry historical reasoning without SDK types or mutable aliasing.
  - _Requirements: 2.1–2.8_
  - _Design rules: D1, D5_
  - _Boundary: `pkg/lipapi`_
  - _Depends: 1.1_
  - _Validation: `go test ./pkg/lipapi/`_

- [x] 2.2 Wire both new ports through feature bundles and snapshots
  - Add optional attempt-transform and stream-observer fields to `FeatureBundle`, merge surface, plugin-instance wrappers, immutable snapshots, sorting, validation, and inventory occupancy.
  - Preserve schema V1 additively; omitted fields remain no-op.
  - **Deliverable:** configured plugins can contribute the two generic ports without core importing feature code.
  - _Requirements: 1.1, 3.1, 5.2, 8.4, 9.4_
  - _Design rules: D2, D11, D12_
  - _Boundary: SDK, feature merge, registry, runtimebundle, diagnostics_
  - _Depends: 1.2, 1.3, 2.1_
  - _Validation: focused feature-bundle/registry/snapshot/inventory tests_

- [x] 2.3 Implement candidate attempt-transform execution
  - Resolve backend instance, prefixes, model, and replay profile after interleaved shaping, then run transforms before final capability/context/token/checkpoint/authorization work.
  - Implement candidate exclusion, safe route evidence, transformed-call validation, exact size recomputation, and immutable-baseline assertions across first open, retries, replacements, weighted routes, and parallel arms.
  - **Deliverable:** candidate restoration can change final eligibility/accounting without leaking between attempts.
  - _Requirements: 5.1–5.10_
  - _Design rules: D3, D5, D11_
  - _Boundary: core runtime orchestration_
  - _Depends: 2.2_
  - _Validation: focused executor open/failover/parallel/context/metering tests_

- [x] 2.4 Implement final-canonical-stream observer execution
  - Dispatch defensive final events after response hooks and gate resolution; finalize exactly once on every terminal/close/error path using a fresh bounded context.
  - Open observers only for the active surfaced B-leg and isolate observer failures without changing committed output or causing retry.
  - **Deliverable:** reusable incremental observation exists without full-response buffering.
  - _Requirements: 3.1, 3.2, 3.4–3.6, 3.9, 8.7_
  - _Design rules: D2, D4, D9, D11_
  - _Boundary: core runtime streaming and SDK observer runner_
  - _Depends: 2.2_
  - _Validation: focused recv/gate/parallel/cancellation/close tests_

- [x] 2.5 Add safe generic telemetry and inventory
  - Record fixed stage outcomes, counts, and bytes; reject sensitive fields and unbounded labels.
  - Expose attempt-transform/observer stage occupancy and aggregate posture in diagnostics without payloads or session partitions.
  - **Deliverable:** generic ports are diagnosable under repository privacy/cardinality rules.
  - _Requirements: 8.1–8.7_
  - _Design rules: D10, D11_
  - _Boundary: metrics, logging, diagnostics, tests_
  - _Depends: 2.2–2.4_
  - _Validation: focused metrics/inventory/privacy tests_

## Phase 3 — GREEN: Reasoning Preservation Feature Plugin

- [x] 3.1 Implement strict config and versioned policy catalog
  - Decode unknown-field-rejecting YAML for action, rules, catalog use, failure policies, and bounds.
  - Implement exact instance rules, family-prefix built-ins, model normalization, precedence, and a conservative versioned Kimi/Moonshot seed.
  - **Deliverable:** the feature builds only from valid explicit opt-in configuration.
  - _Requirements: 1.1–1.8, 6.10_
  - _Design rules: D2, D10, D12_
  - _Boundary: feature plugin/config and standard registration_
  - _Depends: 2.2_
  - _Validation: focused config/catalog/registration/check-config tests_

- [x] 3.2 Implement deterministic anchors, placements, and classification
  - Canonically serialize assistant non-reasoning content with length boundaries and normalized JSON/tool arguments, then hash internally.
  - Derive placements and classify exact associations as missing, preserved, conflicting, ambiguous, or unmatched without exposing anchors.
  - **Deliverable:** exact deterministic matching works without stable provider IDs or heuristics.
  - _Requirements: 3.3, 3.7, 4.1–4.10_
  - _Design rules: D6, D7, D10_
  - _Boundary: feature-domain pure logic_
  - _Depends: 2.1, 3.1_
  - _Validation: table tests, property tests, and anchor fuzzing_

- [x] 3.3 Implement the process-local bounded TurnStore
  - Add per-feature-instance concurrent state with opaque authoritative partitions, atomic TTL/turn/byte eviction, defensive copies, and bounded snapshots.
  - Clear reachable payloads on expiry/eviction and treat restart/replica movement as a state miss.
  - **Deliverable:** reasoning state is bounded and inaccessible across sessions or plugin instances.
  - _Requirements: 6.1–6.10_
  - _Design rules: D8, D10, D12_
  - _Boundary: feature-owned state implementation_
  - _Depends: 3.1, 3.2_
  - _Validation: store contract, race tests, deterministic-clock expiry tests_

- [x] 3.4 Implement final-stream capture
  - Accumulate bounded final canonical reasoning/non-reasoning/tool/media state, preserve block order and placements, and append only on `success_released`.
  - Discard failed/cancelled/closed/replaced/gate-replaced/oversized observations and emit safe outcomes only.
  - **Deliverable:** one winning released assistant turn produces at most one valid artifact.
  - _Requirements: 3.1–3.9, 8.1, 8.2, 8.7_
  - _Design rules: D4, D7, D8, D9, D10_
  - _Boundary: feature stream observer_
  - _Depends: 2.4, 3.2, 3.3_
  - _Validation: observer unit/lifecycle/gate/parallel tests_

- [x] 3.5 Implement candidate classification and restoration
  - Snapshot state once, classify without mutation, verify every dialect against candidate support, build complete replacement slices, validate, and atomically apply only successful unique missing restorations.
  - Return `exclude_candidate` or configured log-skip for incompatibility/state errors; never overwrite conflicting reasoning.
  - **Deliverable:** restore mode is exact, ordered, idempotent, and candidate-isolated; observe mode never mutates.
  - _Requirements: 1.7, 1.8, 4.1–4.10, 5.1–5.10, 8.1–8.5_
  - _Design rules: D3, D5, D6, D7, D8, D10_
  - _Boundary: feature attempt transform_
  - _Depends: 2.3, 3.1–3.4_
  - _Validation: focused transform/idempotence/incompatibility/state tests_

## Phase 4 — GREEN: Adapter Replay Dialects

- [x] 4.1 Add OpenAI-compatible Chat reasoning replay
  - Decode recognized assistant reasoning fields into the text dialect, add legal non-stream response representation where supported, and serialize canonical reasoning only through proven compatible backend fields.
  - **Deliverable:** Chat request → canonical → backend and backend → canonical → stream/non-stream goldens pass.
  - _Requirements: 7.1, 7.4, 7.6, 7.8, 7.9_
  - _Design rules: D1, D5_
  - _Boundary: OpenAI legacy/compatible frontend and backend adapters_
  - _Depends: 2.1, 3.5_
  - _Validation: focused decode/payload/stream/non-stream/golden tests_

- [x] 4.2 Add OpenAI Responses reasoning-item replay
  - Decode supported reasoning input items while retaining bounded item metadata and encrypted/opaque replay data; reconstruct legal provider input items through current SDK or bounded adapter-local wire helpers.
  - **Deliverable:** manually managed Responses history round-trips required reasoning items.
  - _Requirements: 7.2, 7.4–7.6, 7.8_
  - _Design rules: D1, D5_
  - _Boundary: OpenAI Responses frontend/backend adapters_
  - _Depends: 2.1, 3.5_
  - _Validation: focused Responses decoder/payload/integration/golden tests_

- [x] 4.3 Add Anthropic thinking and redacted-thinking replay
  - Decode ordered `thinking` and `redacted_thinking` blocks with signatures/opaque data and reconstruct legal backend blocks without inventing signatures.
  - Revalidate the archived thinking-signature contracts and multi-block tool-use fixtures.
  - **Deliverable:** signed and redacted Anthropic history round-trips exactly.
  - _Requirements: 2.2, 3.7, 7.3, 7.4, 7.6, 7.8_
  - _Design rules: D1, D5, D7_
  - _Boundary: Anthropic frontend/backend adapters_
  - _Depends: 2.1, 3.5_
  - _Validation: focused Anthropic decode/payload/signature/parity tests_

- [x] 4.4 Resolve OpenRouter and compatible/custom replay profiles
  - Project effective upstream flavor, family prefixes, selected model, and supported dialects through a pure candidate resolver.
  - Prove arbitrary instance IDs use explicit rules, built-ins use family/model identity, and no dialect crosses an incompatible family.
  - **Deliverable:** compatible routing excludes candidates that cannot legally replay every block.
  - _Requirements: 1.3–1.6, 5.7, 5.9, 7.4–7.6_
  - _Design rules: D5, D11_
  - _Boundary: backend profile/model-capability projection_
  - _Depends: 4.1–4.3_
  - _Validation: focused OpenRouter/compatible resolver/payload/exclusion tests_

- [x] 4.5 Lock unsupported protocol isolation and parity
  - Add Gemini and other no-support tests proving replay is explicitly excluded/skipped and never silently encoded, dropped, or converted.
  - Extend supported streaming/non-streaming frontend/backend parity matrices.
  - **Deliverable:** every supported and unsupported edge has deterministic behavior.
  - _Requirements: 7.6–7.9, 9.5_
  - _Design rules: D1, D5, D12_
  - _Boundary: protocol plugins and parity tests_
  - _Depends: 4.1–4.4_
  - _Validation: `make parity-checks` plus focused unsupported regressions_

## Phase 5 — GREEN: Routing, Lifecycle, Isolation, and Privacy Proofs

- [x] 5.1 Prove sequential and recv-phase failover isolation
  - Cover incompatible first candidate, restored later candidate, state error, restored context-limit exclusion, pre-output replacement, and final all-candidates-incompatible error.
  - Assert every attempt starts from the baseline and each observer finishes exactly once.
  - **Deliverable:** no restored part or pending artifact leaks across sequential attempts.
  - _Requirements: 3.5, 5.2, 5.5–5.9, 9.4_
  - _Design rules: D3, D5, D9_
  - _Boundary: core runtime integration tests_
  - _Depends: 3.4, 3.5, 4.4_
  - _Validation: focused precommit failover/replacement matrix_

- [x] 5.2 Prove weighted and parallel-race behavior
  - Cover independent transformed arm calls, dialect-specific eligibility, losing-arm non-persistence, selected-arm prepended events, cancellation, and winner observer lifecycle.
  - Run aliasing/race assertions over opaque data and store access.
  - **Deliverable:** parallel routing cannot share mutations or persist loser reasoning.
  - _Requirements: 3.6, 5.5, 6.4, 9.4, 9.6_
  - _Design rules: D3, D8, D9_
  - _Boundary: core runtime parallel integration tests_
  - _Depends: 3.3–3.5, 4.4_
  - _Validation: focused parallel tests and race detector_

- [x] 5.3 Prove response-hook, gate, and terminal lifecycle behavior
  - Verify anchors reflect response-hook mutations, gate-replaced originals are discarded, pass/overflow remains incremental, and cancellation/EOF/close never commits partial artifacts.
  - Assert observer/store failures preserve committed output and never cause retry.
  - **Deliverable:** every stream path has one explicit commit/discard result.
  - _Requirements: 3.1, 3.2, 3.4, 3.5, 3.9, 8.7, 9.4_
  - _Design rules: D4, D9, D10_
  - _Boundary: core streaming integration tests_
  - _Depends: 2.4, 3.4_
  - _Validation: focused recv/gate/close/cancellation/output-commit tests_

- [x] 5.4 Prove authoritative session isolation and process-local posture
  - Cover client-hint spoofing, authoritative partition projection, new/resumed sessions, cross-session/plugin isolation, TTL/eviction, restart/state miss, and sticky-session behavior.
  - **Deliverable:** no non-authoritative partition can access or restore an artifact.
  - _Requirements: 6.1–6.9_
  - _Design rules: D8, D10_
  - _Boundary: secure-session/runtime integration and feature state tests_
  - _Depends: 3.3–3.5_
  - _Validation: focused secure-session/store/restart/race tests_

- [x] 5.5 Prove observability privacy and disabled non-interference
  - Assert fixed outcomes, bounded labels, safe errors, static diagnostics/inventory, aggregate counters, and absence of payloads/anchors/session partitions.
  - Benchmark/allocation-test disabled configuration to prove no store, participant, hashing, mutation, or feature telemetry.
  - **Deliverable:** the feature is operable without hidden-reasoning disclosure and absent configuration is behaviorally unchanged.
  - _Requirements: 1.1, 6.10, 8.1–8.7, 9.4_
  - _Design rules: D10, D12_
  - _Boundary: observability, diagnostics, runtime, tests_
  - _Depends: 2.5, 3.1, 3.4, 3.5_
  - _Validation: focused privacy/inventory/metrics tests and disabled benchmark smoke_

## Phase 6 — Documentation, Release Gates, and Handoff

- [x] 6.1 Add operator documentation and examples
  - Document issue #157 context, opt-in actions, rule/catalog precedence, dialect matrix, failure policies, bounds, privacy, process-local/sticky-session posture, restart behavior, and non-goals.
  - Add observe/restore examples with `check-config`, routes, and inventory expectations.
  - **Deliverable:** operators can enable the feature without guessing about compatibility or durability.
  - _Requirements: 1.1, 1.2, 1.5, 6.9, 7.7, 7.9, 8.4, 9.8_
  - _Design rules: D10, D12_
  - _Boundary: docs and config examples_
  - _Depends: Phase 4 and 5 completion_
  - _Validation: `lipstd check-config`, `routes`, and `inventory` against examples_

- [x] 6.2 Complete goldens, conformance, fuzz, race, and architecture gates
  - Register fixtures in the spec bundle/conformance indexes and run decoder/anchor/config fuzzing, strict race tests, extension import guards, and full supported/unsupported parity.
  - **Deliverable:** release evidence covers mutation, lifecycle, adapter, isolation, and privacy boundaries.
  - _Requirements: 9.3–9.6_
  - _Design rules: D1, D2, D4, D5, D8–D12_
  - _Boundary: tests and release gates_
  - _Depends: all Phase 5 tasks, 6.1_
  - _Validation: `make parity-checks` OK; targeted `FuzzComputeAnchor`/`FuzzDecodeConfig` OK; full `make test-fuzz` not re-claimed end-to-end; `make test-race` skipped on Windows (Linux CI required)_

- [x] 6.3 Run repository quality and full QA
  - Run formatting/module/build/vet/architecture, default and tagged tests, lint, vulnerability scan, and full QA; record platform/external-service skips explicitly.
  - **Deliverable:** all locally available required gates are green with reproducible commands.
  - _Requirements: 9.2, 9.7_
  - _Design rules: D11–D13_
  - _Boundary: repository release gates_
  - _Depends: 6.2_
  - _Validation: `make quality-checks` OK; `make qa` OK (2026-07-18) after ledger Postgres unique-ID isolation; race skipped on Windows; full test-fuzz not claimed_

- [x] 6.4 Prepare focused implementation PRs and review handoff
  - Split contract/runtime, feature-plugin, and adapter/release work when review size warrants it; keep every PR independently green and link issue #157 and this approved spec.
  - Include changed-file scope, verification evidence, process-local limitation, and no-retry/privacy review focus.
  - **Deliverable:** implementation can proceed without re-deriving architecture or mixing unrelated work.
  - _Requirements: 9.1, 9.2, 9.7, 9.8_
  - _Design rules: D11–D13_
  - _Boundary: delivery and review_
  - _Depends: 6.3_
  - _Validation: every implementation PR references the approved spec and issue #157_
