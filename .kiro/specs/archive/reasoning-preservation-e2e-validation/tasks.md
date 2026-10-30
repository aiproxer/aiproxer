# Implementation Plan

**Source context:** Follow-up full HTTP E2E validation for [`.kiro/specs/archive/reasoning-output-preservation/`](../reasoning-output-preservation/) (issue [#157](https://github.com/matdev83/go-llm-interactive-proxy/issues/157)).

> **TDD gate:** Preserve existing branch WIP under `internal/testkit/reasoninge2e` and `internal/refbackend/openaichat` exactly as the reusable base—extend, do not rewrite. Every behavioral production change starts from a failing E2E/focused red test. No relaxed matching or synthesis. OpenAI Responses HTTP E2E remains deferred.

## Phase 1 — Preserve WIP and Freeze Harness Contracts

- [x] 1.1 Inventory and lock reusable WIP contracts
  - Confirm `reasoninge2e` plan/oracle APIs and OpenAI Chat `Responder`/`OnRequestBody` behavior remain the harness foundation.
  - Add or extend package docs stating full-HTTP E2E ownership and content-safe oracle rules.
  - **Deliverable:** focused `go test ./internal/testkit/reasoninge2e/ ./internal/refbackend/openaichat/` green without rewriting existing WIP.
  - _Requirements: 2.5, 2.6_
  - _Boundary: testkit + openaichat refbackend_
  - _Depends: approved requirements and design_
  - _Validation: `go test -count=1 ./internal/testkit/reasoninge2e/ ./internal/refbackend/openaichat/`_

- [x] 1.2 Add content-safe failtrace helpers (red then green)
  - Cover seed/mode/turn/structural fields; assert fixtures with reasoning/signature/opaque bytes never appear in error strings.
  - **Deliverable:** `failtrace` (or equivalent) unit tests green and reusable by drivers.
  - _Requirements: 5.7, 5.8, 6.6, 8.2_
  - _Boundary: `internal/testkit/reasoninge2e`_
  - _Depends: 1.1_
  - _Validation: `go test -count=1 -run Failtrace ./internal/testkit/reasoninge2e/`_

- [x] 1.3 Extend plan helpers for matrix category forcing (P)
  - Support precomputed 20-turn plans, streaming alternation flags, and forced categories (conflict, ambiguity/changed anchor, tools, mixed reason/no-reason).
  - **Deliverable:** deterministic plan builder tests for forced categories without HTTP.
  - _Requirements: 5.1–5.6, 3.5–3.8_
  - _Boundary: `internal/testkit/reasoninge2e`_
  - _Depends: 1.1_
  - _Validation: `go test -count=1 ./internal/testkit/reasoninge2e/`_

## Phase 2 — Emulator Scripting for Chat and Anthropic

- [x] 2.1 Script OpenAI Chat multi-turn reasoning and tool responses
  - Extend preserved `Responder` path to emit deterministic JSON/SSE containing reasoning metadata and tool calls/results needed by E2E turns.
  - Capture backend request bodies for oracle observation without logging secrets or payloads in tests.
  - **Deliverable:** refbackend unit tests prove scripted stream/non-stream bodies and request capture.
  - _Requirements: 2.2, 4.1, 4.5, 7.3_
  - _Boundary: `internal/refbackend/openaichat`_
  - _Depends: 1.1_
  - _Validation: `go test -count=1 ./internal/refbackend/openaichat/`_

- [x] 2.2 Script Anthropic signed thinking and redacted_thinking (P)
  - Add deterministic emulator responses/capture sufficient for cross-check E2E (not a full Anthropic matrix).
  - **Deliverable:** anthropicmessages refbackend tests cover thinking + redacted_thinking shapes.
  - _Requirements: 4.2_
  - _Boundary: `internal/refbackend/anthropicmessages`_
  - _Depends: 1.1_
  - _Validation: `go test -count=1 ./internal/refbackend/anthropicmessages/`_

## Phase 3 — Standard-Stack HTTP Driver and Deterministic Controls

- [x] 3.1 Bootstrap stdhttp E2E fixture via BuildBootstrap
  - Wire temp configs for disabled/observe/restore against emulator URLs using standard plugin registration and `stdhttp`.
  - Prove at least one multi-turn HTTP round trip through real Chat adapters (red until driver works).
  - **Deliverable:** fixture helper + smoke E2E green on the standard stack.
  - _Requirements: 1.1–1.5, 2.1_
  - _Boundary: `internal/stdhttp` tests + composition_
  - _Depends: 2.1_
  - _Validation: `go test -count=1 -run ReasoningPreservationHTTP ./internal/stdhttp/`_

- [x] 3.2 Implement stateful client transcript driver
  - Maintain observed vs submitted history; feed backend observations into `reasoninge2e.Check`.
  - **Deliverable:** driver unit/E2E glue asserts oracle on a preserve-all and drop-all path.
  - _Requirements: 2.1–2.4, 3.3, 3.4_
  - _Boundary: `internal/stdhttp` tests + testkit_
  - _Depends: 3.1, 1.2_
  - _Validation: `go test -count=1 -run ReasoningPreservationHTTP ./internal/stdhttp/`_

- [x] 3.3 Deterministic control suite (default tags)
  - Cover disabled, observe, restore drop-all nonstream/stream, preserve-all, reasoning+tools, mixed reason/no-reason, conflict, ambiguity/changed anchor, authoritative session isolation.
  - **Deliverable:** named subtests in the ordinary suite, all green or intentionally red only for known defect 3.4.
  - _Requirements: 3.1–3.10, 8.1, 8.3, 8.4_
  - _Boundary: `internal/stdhttp` default tests_
  - _Depends: 3.2_
  - _Validation: `go test -count=1 -run TestReasoningPreservationHTTP_Deterministic ./internal/stdhttp/`_

- [x] 3.4 Red case: OpenAI Chat assistant reasoning + tool-call replay
  - Encode the likely defect before any production patch; keep failure content-safe.
  - **Deliverable:** failing focused E2E/regression naming the structural mismatch.
  - _Requirements: 7.1–7.4, 4.1, 4.5_
  - _Boundary: tests first; production only in 3.5_
  - _Depends: 3.3_
  - _Validation: `go test -count=1 -run ReasoningToolReplay ./internal/stdhttp/` (expect RED until 3.5)_

- [x] 3.5 Narrow production fix for proven Chat reasoning+tool defect
  - Patch only the failing adapter/replay boundary; re-run 3.4 to green; no semantic relaxation.
  - **Deliverable:** RED→GREEN with focused adapter tests retained.
  - _Requirements: 7.1–7.5_
  - _Boundary: OpenAI Chat frontend/backend adapters (as proven)_
  - _Depends: 3.4_
  - _Validation: `go test -count=1 -run ReasoningToolReplay ./internal/stdhttp/ ./internal/plugins/frontends/openailegacy/ ./internal/plugins/backends/openailegacy/`_

- [x] 3.6 Anthropic thinking / redacted_thinking HTTP cross-check (P)
  - Deterministic observe/restore (or observe-only if restore path limited) cross-check through real Anthropic adapters + emulator.
  - **Deliverable:** named Anthropic E2E subtests green.
  - _Requirements: 4.2, 4.5_
  - _Boundary: `internal/stdhttp` tests + anthropic adapters_
  - _Depends: 3.1, 2.2_
  - _Validation: `go test -count=1 -run ReasoningPreservationHTTP_Anthropic ./internal/stdhttp/`_

## Phase 4 — Seeded Matrix and Soak Wiring

- [x] 4.1 Implement 64×20 precommit matrix driver
  - Split 16 drop-all / 16 always-reason+random client / 32 combined; alternate streaming; force categories; content-safe failures.
  - File uses `//go:build precommit` per design suite policy.
  - **Deliverable:** matrix compiles only with `-tags=precommit` and passes under that tag.
  - _Requirements: 5.1–5.9_
  - _Boundary: `internal/stdhttp` precommit tests + testkit matrix_
  - _Depends: 3.3, 1.3, 3.5_
  - _Validation: `go test -tags=precommit -count=1 -run TestReasoningPreservationHTTP_RandomMatrix ./internal/stdhttp/`_

- [x] 4.2 Implement env-gated soak (1000×100) helpers and test
  - Bounded workers, fresh sessions, hard timeout, single-seed replay; skip when env unset.
  - **Deliverable:** soak test skips by default; runs when gate set.
  - _Requirements: 6.1, 6.5, 6.6_
  - _Boundary: testkit soak + stdhttp soak test_
  - _Depends: 4.1_
  - _Validation: default `go test` skips; gated run documented in Make target_

- [x] 4.3 Add Make target and GitHub workflow for soak (P)
  - Dedicated Make target; workflow with `workflow_dispatch` + nightly schedule; not PR-mandatory.
  - **Deliverable:** workflow file + Makefile entry; docs mention non-PR status.
  - _Requirements: 6.2–6.4_
  - _Boundary: Makefile + `.github/workflows`_
  - _Depends: 4.2_
  - _Validation: `make` help/target dry visibility; workflow YAML validates structurally_

## Phase 5 — Docs, Review, and Release Evidence

- [x] 5.1 Update operator docs and release checklist
  - Document full HTTP E2E, default vs `precommit` matrix, soak opt-in, Responses deferral, issue #157 + parent/follow-up spec links.
  - **Deliverable:** docs/checklist paragraphs and commands match actual targets.
  - _Requirements: 4.3, 4.4, 9.1, 9.6_
  - _Boundary: docs_
  - _Depends: 4.3_
  - _Validation: manual doc review + commands listed are runnable_

- [x] 5.2 Update EchoesVault reasoning-output-preservation page (P)
  - Reflect follow-up E2E validation, suite topology, and deferrals; keep index convention.
  - **Deliverable:** EchoesVault page (+ index entry if required) aligned with docs.
  - _Requirements: 9.2_
  - _Boundary: EchoesVault_
  - _Depends: 5.1_
  - _Validation: page links resolve to this spec path_

- [x] 5.3 Independent review against requirements and design
  - Adversarial pass for boundary drift, suite-policy mistakes, privacy leaks in traces, and Responses-scope creep.
  - **Deliverable:** review notes with PASS or enumerated fixes applied.
  - _Requirements: 9.3_
  - _Boundary: review only_
  - _Depends: 5.1, 5.2, 4.1, 3.6_
  - _Validation: review checklist completed_

- [x] 5.4 Run focused, race, quality, parity, and QA gates
  - Record evidence; do not claim Windows race-green if no-op; Linux race via CI/`make test-race` where available.
  - **Deliverable:** completion evidence table in checklist or task notes.
  - _Requirements: 9.4, 9.5_
  - _Boundary: verification_
  - _Depends: 5.3_
  - _Validation: focused E2E + `make quality-checks` + `make parity-checks` + `make qa` (+ race where supported)_

## Definition of Done

- All tasks 1.1–5.4 complete with green validation commands (soak remains env-opt-in).
- Deterministic HTTP E2E in the default suite; matrix under `precommit`; soak Make/workflow present and non-PR-mandatory.
- OpenAI Chat deep coverage + Anthropic thinking cross-check present; Responses HTTP E2E explicitly deferred in docs.
- Branch WIP preserved as the harness base; production diffs limited to proven defects.
- Independent review PASS; docs/checklist/EchoesVault updated; issue #157 and parent spec linked.
- No reasoning/signature/opaque payloads in ordinary failure output.
