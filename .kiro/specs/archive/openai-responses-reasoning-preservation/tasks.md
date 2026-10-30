# Implementation Plan

**Spec:** `openai-responses-reasoning-preservation`
**Handoff:** `EchoesVault/daily/2026-07-19.md`
**Status:** Completed and archived by human release-owner decision on 2026-07-28. Local quality/unit/parity/QA, targeted fuzz, deterministic matrices, retention-aware regression coverage, and selected 100-turn soak seeds are accepted as sufficient evidence.

> [!warning] DEPRECATED
> The original closeout policy required Linux `-race`, a full 1000×100 soak, and live-provider smoke. The human release owner waived those requirements on 2026-07-28 as disproportionate for this completed test-harness correction. This waiver is a closeout decision, not fabricated execution evidence.

> **TDD gate:** Every behavioral change starts RED then GREEN. Preserve unrelated branch WIP. Historical specs are read-only. Mark tasks `[x]` only with evidence or an explicit human waiver.

## Phase 0 — Approval Gate

- [x] 0.1 Human approve requirements and design
  - Set `approvals.requirements.approved` and `approvals.design.approved` true only after human review.
  - Optionally choose terminal-only first vs presentation-tagged progressive UX (both specified in design).
  - Flip `ready_for_implementation` true; keep implementation incomplete until Phase 9.
  - **Deliverable:** approved `spec.json` gate.
  - _Requirements: 10.5_
  - _Boundary: spec metadata only_
  - _Depends: none_
  - _Validation: inspect `spec.json`_
  - _Evidence: approvals true; terminal exact-part path chosen; progressive UX deferred (2.6)_

## Phase 1 — Canonical Carrier (RED then GREEN)

- [x] 1.1 RED: dialect-aware terminal exact-part event contracts
  - Failing tests: new/extended event kind, content-class sequencing, clone deep-copy, bounds, Anthropic opaque delta non-overload, presentation-Dialect marker semantics on `EventReasoningDelta` if used.
  - **Deliverable:** RED lipapi tests.
  - _Requirements: 1.1–1.4, 1.6–1.8_
  - _Boundary: `pkg/lipapi` tests_
  - _Depends: 0.1_
  - _Validation: `go test -count=1 ./pkg/lipapi/ -run Reasoning`

- [x] 1.2 RED: Collected ordered exact-part retention
  - Terminal exact Responses part retained in `ReasoningParts`; text builder alone insufficient; FE nonstream collect uses same stream events.
  - **Deliverable:** RED collect tests.
  - _Requirements: 1.5, 1.7, 5.2_
  - _Boundary: `pkg/lipapi` (+ `pkg/lipsdk/response` if needed)_
  - _Depends: 1.1_
  - _Validation: focused collect tests_

- [x] 1.3 GREEN: implement minimal canonical API
  - No provider SDK types; update sequence validators/clone/sizing.
  - **Deliverable:** Phase 1 green; archtest no SDK leak.
  - _Requirements: 1.1–1.8_
  - _Boundary: `pkg/lipapi`, maybe `pkg/lipsdk/response`_
  - _Depends: 1.1, 1.2_
  - _Validation: `go test -count=1 ./pkg/lipapi/ ./pkg/lipsdk/response/`; arch guards_
  - _Evidence: `EventReasoningPart` + `Collected.ReasoningParts`; exclusivity/output-commit hooks_

## Phase 2 — Backend Ingestion

- [x] 2.1 RED: mapper gap + envelope allowlist fixtures
  - Table tests for output_item added/done, summary/text deltas, completed fallback; envelope schema (summary/content arrays; encrypted_content absent/null/value; type/status; reject unknown/non-array).
  - **Deliverable:** RED mapper/envelope tests.
  - _Requirements: 2.1–2.5, 2.9, 2.10_
  - _Boundary: `internal/plugins/backends/openairesponses` + neutral `openairesponsesitem`_
  - _Depends: 1.3_
  - _Validation: focused openairesponses / openairesponsestream / openairesponsesitem tests_

- [x] 2.2 GREEN: assembly state machine + terminal emit (no bare Chat deltas)
  - Mapper-private assembly; terminal exact-part only by default; never bare `EventReasoningDelta` for Responses items.
  - **Deliverable:** incremental path green.
  - _Requirements: 2.1, 2.2, 2.5, 2.8–2.10, 1.3_
  - _Boundary: openairesponses mapper/shared stream_
  - _Depends: 2.1_
  - _Validation: focused mapper tests_

- [x] 2.3 RED/GREEN: completed fallback + dedupe by item id
  - **Deliverable:** equivalence/dedupe green.
  - _Requirements: 2.3, 2.4_
  - _Boundary: openairesponses_
  - _Depends: 2.2_
  - _Validation: dedupe/completed tests_

- [x] 2.4 RED/GREEN: output_index ordering assumption + bounded buffer
  - Conformance: done(i) before content(j>i) on ref fixtures; interleaved fixtures exercise buffer; never rewrite emitted stream; overflow/hole after content => stream terminal error, no retry.
  - **Deliverable:** ordering tests green.
  - _Requirements: 2.6, 2.7, 2.11, 8.3_
  - _Boundary: openairesponses_
  - _Depends: 2.2_
  - _Validation: ordering/buffer/failure-timing tests_

- [x] 2.5 RED/GREEN: malformed/oversize incomplete + post-output failure timing
  - Pre-content vs post-content failure behaviors per design table; no storeable partial exact part.
  - **Deliverable:** negative timing tests green.
  - _Requirements: 2.7, 2.8, 2.11, 8.2_
  - _Boundary: openairesponses_
  - _Depends: 2.2, 2.4_
  - _Validation: negative mapper tests_

- [x] 2.6 Optional: presentation-tagged progressive deltas
  - Explicitly deferred by human decision; terminal exact-part path shipped and dialect-tagged progressive UX remains outside this completed scope.
  - **Deliverable:** optional tests green or explicit defer note.
  - _Requirements: 1.4, 5.7_
  - _Boundary: openairesponses + feature + FE_
  - _Depends: 2.2, 3.1_
  - _Validation: presentation path tests or skip with note_

## Phase 3 — Feature Capture / Restore

- [x] 3.1 RED/GREEN: observer captures terminal exact parts only
  - Store Opaque+dialect+positions; ignore presentation deltas; never Chat+Responses duplicate from one item; reject incomplete.
  - **Deliverable:** observer/store tests green.
  - _Requirements: 3.1–3.5, 3.9–3.11_
  - _Boundary: `internal/plugins/features/reasoningpreservation`_
  - _Depends: 1.3, 2.2_
  - _Validation: `go test -count=1 ./internal/plugins/features/reasoningpreservation/`_

- [x] 3.2 RED/GREEN: restore exact-only + dialect policy + state_error
  - Restore when representable; `on_unrepresentable` reject/log_skip without conversion; invalid envelope => `on_state_error`; no partial submit.
  - **Deliverable:** restore/policy tests green.
  - _Requirements: 3.6, 3.7, 3.12, 8.4_
  - _Boundary: feature plugin_
  - _Depends: 3.1_
  - _Validation: restore/dialect/state_error tests_

- [x] 3.3 RED/GREEN: unmatched inert + default-on Chat regression
  - **Deliverable:** inert + default-on tests green.
  - _Requirements: 3.8, 7.8, 7.9_
  - _Boundary: feature + stdhttp default-on_
  - _Depends: 3.1_
  - _Validation: inert tests + `TestReasoningPreservationHTTP_DefaultOnInjection`_

## Phase 4 — Exact Replay Encoder

- [x] 4.1 RED: forbid summary/text fallback success
  - Update `reasoning_replay_red_test.go`: fallback success forbidden.
  - **Deliverable:** RED encoder tests.
  - _Requirements: 4.1, 4.6, 4.7_
  - _Boundary: openairesponses_
  - _Depends: 0.1_
  - _Validation: replay tests RED_

- [x] 4.2 GREEN: semantic presence encode (raw JSON and/or Opt/Null)
  - Cover encrypted_content absent/null/value; content absent/present array; status when present; no `ToParam()` exact path; unrepresentable fail-closed; ordering preserved; content-safe errors.
  - **Deliverable:** exact encode tests green.
  - _Requirements: 4.1–4.7, 2.10_
  - _Boundary: openairesponses invoke/encode_
  - _Depends: 4.1_
  - _Validation: `go test -count=1 -run ReasoningReplay ./internal/plugins/backends/openairesponses/`_

## Phase 5 — Frontend Fidelity

- [x] 5.1 RED/GREEN: stream encode from exact parts
  - No synthesized IDs/summary when exact part present; no duplicate wire item with presentation path.
  - **Deliverable:** FE stream tests green.
  - _Requirements: 5.1, 5.5–5.7_
  - _Boundary: `internal/plugins/frontends/openairesponses`_
  - _Depends: 1.3_
  - _Validation: focused FE encode tests_

- [x] 5.2 RED/GREEN: frontend nonstream collect (BE still streaming)
  - Prove nonstream uses collect over canonical stream; same exact envelopes; **no backend nonstream path**.
  - **Deliverable:** FE nonstream tests green.
  - _Requirements: 5.2, 1.5_
  - _Boundary: FE + lipapi collect_
  - _Depends: 1.3, 5.1_
  - _Validation: nonstream FE tests_

- [x] 5.3 RED/GREEN: input decode round trip + presence
  - **Deliverable:** round-trip tests green.
  - _Requirements: 5.3, 5.4, 2.10_
  - _Boundary: openairesponses frontend_
  - _Depends: 5.1_
  - _Validation: decode/round-trip tests_

## Phase 6 — Ref Harness

- [x] 6.1 RED/GREEN: Responses refbackend stateful scripts + ordering fixtures
  - Include output_index, completed, interleaved/edge ordering cases.
  - **Deliverable:** refbackend tests green.
  - _Requirements: 6.1, 6.4, 6.5, 2.6_
  - _Boundary: `internal/refbackend/openairesponses`_
  - _Depends: 2.2_
  - _Validation: `go test -count=1 ./internal/refbackend/openairesponses/`_

- [x] 6.2 RED/GREEN: refclient/testkit policies + semantic oracles
  - Drop/preserve/conflict; presence oracles; content-safe failtraces.
  - **Deliverable:** reasoninge2e/refclient tests green.
  - _Requirements: 6.2–6.4_
  - _Boundary: refclient + reasoninge2e_
  - _Depends: 6.1_
  - _Validation: focused testkit/refclient tests_

## Phase 7 — HTTP Combination Matrix

- [x] 7.1 Scaffold Responses-capable stdhttp fixtures (BE streaming always)
  - Temp configs; smoke Responses/Responses multi-turn.
  - **Deliverable:** smoke HTTP green.
  - _Requirements: 7.1, 7.2, 7.6_
  - _Boundary: `internal/stdhttp` tests_
  - _Depends: 3.2, 4.2, 5.3, 6.2_
  - _Validation: `go test -count=1 -run ReasoningPreservationHTTP ./internal/stdhttp/`_

- [x] 7.2 Positive same-dialect cells
  - Chat/Chat regression; Responses/Responses exact end-to-end; stream + FE-nonstream.
  - **Deliverable:** positive same-dialect subtests green.
  - _Requirements: 7.2, 7.6, 7.7, 7.9_
  - _Boundary: stdhttp_
  - _Depends: 7.1_
  - _Validation: deterministic same-dialect HTTP tests_

- [x] 7.3 Asymmetric mixed cells
  - Chat FE/Responses BE: capture/restore exact without requiring Chat opaque client exposure; client omit reasoning; anchors non-reasoning.
  - Responses FE/Chat BE: positive only for Chat dialect presentable as text; document client history.
  - **Deliverable:** asymmetric positives green.
  - _Requirements: 7.3, 7.4, 7.7_
  - _Boundary: stdhttp_
  - _Depends: 7.2_
  - _Validation: mixed-cell HTTP tests_

- [x] 7.4 Cross-dialect negatives + controls
  - reject and log_skip without conversion; gating/opt-in/opt-out/inert; session/anchor/restart; no-retry-after-output.
  - **Deliverable:** negatives + controls green.
  - _Requirements: 7.5, 7.8, 3.8, 8.3_
  - _Boundary: stdhttp_
  - _Depends: 7.3_
  - _Validation: control/negative HTTP tests_
  - _Evidence: topology matrix + non-vacuous runtime `OutputCommitted` gate + HTTP malformed-after-visible-output integration_

## Phase 8 — Hardening

- [x] 8.1 Goldens/conformance for envelope/presence/ordering/FE/BE
  - **Deliverable:** conformance set green.
  - _Requirements: 9.1_
  - _Boundary: adapters + lipapi_
  - _Depends: 2.5, 4.2, 5.3_
  - _Validation: focused package tests_
  - _Evidence: openairesponsesitem + mapper + FE/BE exact fidelity packages green_

- [x] 8.2 Fuzz validators/collect/decode
  - Short during task; 30s in release phase (task 9.2).
  - **Deliverable:** fuzz builds + short run green.
  - _Requirements: 9.2_
  - _Boundary: lipapi/adapters_
  - _Depends: 1.3, 4.2, 5.3_
  - _Validation: short fuzz_
  - _Evidence: 5s smoke + release 30s each PASS — `FuzzCanonizeReasoningItemOpaque`, `FuzzDecodeCreateRequest_reasoningItems`, `FuzzHandleResponseStreamUnion` (BE), `FuzzStreamObserver_exactReasoningPart`_

- [x] 8.3 Cancellation / leak / privacy / reorder-buffer
  - **Deliverable:** cancel/privacy tests green.
  - _Requirements: 8.1, 8.3, 8.5_
  - _Boundary: mapper/feature as touched_
  - _Depends: 2.4, 3.1_
  - _Validation: focused unit tests_
  - _Evidence: Cancel/Close aborts drafts; Failed/Cancelled/Replaced/loser discard; privacy table; ordinary Windows concurrency tests. Linux `-race` was not run and was waived for closeout by the human release owner on 2026-07-28._

- [x] 8.4 Benchmarks if hot-path risk
  - Focused benches added; no hard threshold asserted.
  - _Requirements: 9.4_
  - _Depends: 2.2, 4.2_
  - _Validation: `go test -bench` short_
  - _Evidence: Canonize / mapper Done / FE WriteStreamSSE exact benches ran locally_

- [x] 8.5 Seeded matrix + env soak hooks (cell meanings per Req 7)
  - **Deliverable:** matrix builds; soak smoke documented.
  - _Requirements: 9.5, 9.6_
  - _Boundary: stdhttp + reasoninge2e_
  - _Depends: 7.4_
  - _Validation: precommit matrix; soak smoke if env available_
  - _Evidence: `ResponsesSmokeCases` + topology `responses_seeded_presence_smoke`; soak smoke `LIP_REASONING_E2E_SOAK=1 SEEDS=4 TURNS=2 WORKERS=2` PASS. Retention-aware 100-turn combined soak seeds 488/490/491/499 PASS. Full 1000×100 and live-provider runs were not executed and were waived for closeout by the human release owner on 2026-07-28._

## Phase 9 — Release Gates and Docs

- [x] 9.1 Focused + quality + unit + parity
  - _Requirements: 10.1_
  - _Depends: 7.4, 8.1–8.5_
  - _Validation: focused tests; `make quality-checks`; `make test-unit`; `make parity-checks`_
  - _Evidence (Windows 2026-07-19): `git diff --check` OK; quality-checks ~35s OK (gofmt fix on `reasoning_part_event_test.go`); test-unit ~61s OK; parity-checks ~16s OK; precommit RandomMatrix OK; TopologyMatrix `-count=3` OK_

- [x] 9.2 QA + fuzz + concurrency + soak evidence
  - Closed by accepted evidence plus explicit human waiver of disproportionate external/wide gates.
  - _Requirements: 9.2, 9.3, 9.5, 10.1_
  - _Depends: 9.1_
  - _Validation: `make qa`; targeted fuzz; ordinary concurrency tests; deterministic matrix; bounded-retention soak replays_
  - _Evidence: `make qa` ~77s OK (lint+govulncheck); four fuzz targets 30s each PASS; soak smoke 4×2×2 PASS; retention-aware reasoninge2e/default stdhttp/precommit 64×20 PASS; combined 100-turn seeds 488/490/491/499 PASS; quality and parity checks PASS. Linux `-race`, full 1000×100, and live-provider smoke were not run and are explicitly waived by human release-owner decision dated 2026-07-28._

- [x] 9.3 Docs + EchoesVault honesty update
  - Exact-only allowlist/presence; asymmetric cells; dialect controls; TurnStore non-durability; inert; BE streaming / FE collect nonstream. Do not edit historical specs.
  - Pending external gates explicitly called out (no false green).
  - _Requirements: 10.2–10.4_
  - _Depends: 8.x local evidence (docs may land before CI release gates)_
  - _Validation: doc review + consistency greps_

- [x] 9.4 Spec completion metadata
  - Set `phase: completed`, `implementation_complete: true`, and `completed: true`; archive the spec after recording the human closeout waiver.
  - _Requirements: 10.1, 10.5 as amended by the 2026-07-28 release-owner decision_
  - _Depends: 9.1, 9.2, 9.3_
  - _Validation: inspect tasks + spec.json + archived path_

## Dependency Overview

```text
0.1
 -> 1.1 -> 1.2 -> 1.3
 -> 2.1 -> 2.2 -> 2.3
              -> 2.4 -> 2.5
              -> 2.6 (optional, deferred)
 -> 3.1 -> 3.2
        -> 3.3
 -> 4.1 -> 4.2
 -> 5.1 -> 5.2
        -> 5.3
 -> 6.1 -> 6.2
 -> 7.1 -> 7.2 -> 7.3 -> 7.4
 -> 8.x -> 9.x (completed; external/wide gates waived by human decision)
```
