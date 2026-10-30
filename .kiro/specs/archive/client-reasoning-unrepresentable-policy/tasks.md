# Implementation Plan (spec-only; NOT authorized — awaits requirements approval)

> Implementation must not begin until requirements and design are
> owner-approved in `spec.json` (`ready_for_implementation: true`). The tasks
> below are the proposed V1 work breakdown for review alongside the spec.

- [x] 1. Attempt policy key with log_skip default and reject option
  - Resolve `log_skip`/`reject` per attempt at executor level, default `log_skip` in the standard distribution, reusing the existing vocabulary with no second knob.
  - _Requirements: 1.3, 1.4_
  - _Boundary: core/runtime config_
  - _Depends: none_
  - _Validation: `go test ./internal/core/runtime/ -run TestAttemptPolicy`_
- [x] 2. Conditional reasoning_replay downgrade through the Negotiate seam
  - `reasoning_replay` downgradable only under `log_skip`; hard under `reject`; lossless candidates unaffected.
  - _Requirements: 1.1, 1.2, 1.5_
  - _Boundary: pkg/lipapi_
  - _Depends: 1_
  - _Validation: `go test ./pkg/lipapi/ -run TestNegotiate`_
- [x] 3. Clone-only strip with byte-identical remainder and no reorder
  - Strip reasoning parts on the per-attempt clone; everything else byte-identical and order-preserved; never mutate the shared call; never reorder.
  - _Requirements: 2.1, 2.2, 2.3_
  - _Boundary: core/runtime + pkg/lipapi_
  - _Depends: 2_
  - _Validation: `go test ./internal/core/runtime/ ./pkg/lipapi/`_
- [x] 4. Unified restored/client-sent rule with single accounting
  - Same per-candidate outcome for restored and client-sent reasoning; one downgrade record; no restore into an incapable candidate under `log_skip`; existing `on_unrepresentable` behaviour under `reject`.
  - _Requirements: 3.1, 3.2, 3.3, 3.4_
  - _Boundary: core/runtime + feature plugin contract_
  - _Depends: 3_
  - _Validation: `go test ./internal/plugins/features/reasoningpreservation/ ./internal/core/runtime/`_
- [x] 5. Skip observability: counter and content-safe decision log
  - Bounded `reasoning_replay_skipped{backend}` counter plus one content-safe decision entry per stripped attempt.
  - _Requirements: 4.1, 4.2, 4.3_
  - _Boundary: core/runtime telemetry_
  - _Depends: 3_
  - _Validation: `go test ./internal/core/runtime/ -run TestSkipObservability`_
- [x] 6. End-to-end proof: frontend body, two candidates, strict emulator
  - Codex-style body with a reasoning item through the real frontend; lossless OpenAI-responses candidate receives exact items; profile candidate receives the stripped clone.
  - _Requirements: 1.1, 1.5, 2.1_
  - _Boundary: frontend plugin + backend plugin + core/runtime_
  - _Depends: 4, 5_
  - _Validation: `go test ./internal/standardplugins/ ./internal/plugins/frontends/...`_
- [x] 7. Adjacency ordering test and operator docs reversal notice
  - `callA, reasoning, callB, outA, outB` reaches `deepseek-responses` and goes out as `callA, callB, outA, outB` with no refusal; trapped-message control still refuses; docs record the default and the line-34 reversal.
  - _Requirements: 5.1, 5.2, 5.3, 1.3_
  - _Boundary: backend plugin quirk + docs_
  - _Depends: 6_
  - _Validation: `go test ./internal/plugins/backends/openaicompat/`_

## Completion Status

- [x] All seven implementation tasks delivered in #1157 (`7293cf4a6e4a1b5992064378039fa16b6c158c44`), authorized by the owner merge of spec PR #1147.
- [x] Task evidence on merged main:
  - Tasks 1-3: `routing.attempt_policy` (default log_skip, strict reject) in `internal/core/config`; capability downgrade through `lipapi.NegotiateWithAttemptPolicy` and `AdmitCandidate` in `pkg/lipapi` / `internal/core/capabilities`; clone-only strip in the executor paths.
  - Task 4: unified restored/client-sent rule with single accounting - `internal/plugins/features/reasoningpreservation/client_reasoning_test.go`.
  - Task 5: bounded `reasoning_replay_skipped{backend}` counter and one content-safe decision entry - `internal/infra/metrics/executor_prom.go`, `internal/core/runtime/executor_admission.go`.
  - Task 6: end-to-end frontend proof over the settled attempt-policy matrix - `internal/core/runtime/client_reasoning_e2e_test.go`, `internal/stdhttp/reasoning_preservation_http_topology_matrix_test.go`.
  - Task 7: adjacency ordering test (`TestQuirks_ResponsesStrippedToolLoopPasses`) - `internal/plugins/backends/openaicompat/quirks_client_reasoning_test.go`; operator reversal notice in `docs/reasoning-output-preservation.md`.
- [x] The task boxes and spec-only notes above reflect the generation snapshot; implementation was delivered separately and merged, so this bundle has no residual implementation work.
