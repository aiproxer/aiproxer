# Implementation Plan

## Task Format Template

Use whichever pattern fits the work breakdown:

### Major task only
- [status] {{NUMBER}}. {{TASK_DESCRIPTION}}{{PARALLEL_MARK}}
  - _Requirements: {{REQUIREMENT_IDS}}_

### Major + Sub-task structure
- [status] {{MAJOR_NUMBER}}. {{MAJOR_TASK_SUMMARY}}
- [status] {{MAJOR_NUMBER}}.{{SUB_NUMBER}} {{SUB_TASK_DESCRIPTION}}{{SUB_PARALLEL_MARK}}
  - {{DETAIL_ITEM}}
  - {{OBSERVABLE_COMPLETION_ITEM}}
  - _Requirements: {{REQUIREMENT_IDS}}_
  - _Boundary: {{CORE_OR_PLUGIN_OR_SDK_OR_DOCS}}_
  - _Depends: {{TASK_OR_SPEC_DEPENDENCIES}}_
  - _Validation: {{TEST_OR_CHECK_COMMANDS}}_

---

## Tasks

- [x] 1. Canonical event model for the thinking signature
- [x] 1.1 Add `EventReasoningSignatureDelta` kind, `Signature` field, and envelope bound
  - Add `EventReasoningSignatureDelta EventKind = "reasoning_signature_delta"` near the other kind constants in `pkg/lipapi/events.go`.
  - Add `Signature string` to the `Event` struct (near `Delta`); leave it unused by other kinds.
  - In `ValidateEventEnvelope`, reject `len(ev.Signature) > MaxRefStringBytes` with a field-level validation error.
  - Done is observable: a `Signature`-carrying `Event` of the new kind passes `ValidateEventEnvelope` within the bound and is rejected when oversized.
  - _Requirements: 2.1, 2.3_
  - _Boundary: SDK/public contract_
  - _Validation: `go test ./pkg/lipapi/`_
- [x] 1.2 Register the kind in both canonical sequence validators
  - Add `EventReasoningSignatureDelta` to the content-class group (the group that requires `sawMessage`) in both `ValidateEventSequence` validators in `pkg/lipapi/events.go`, so it is accepted after `EventMessageStarted` and rejected before it, and is not rejected as unknown by the `default`.
  - Done is observable: `ValidateEventSequence` accepts `[ResponseStarted, MessageStarted, ReasoningDelta, EventReasoningSignatureDelta, ResponseFinished]` and rejects the same stream with `EventReasoningSignatureDelta` before `MessageStarted`.
  - _Requirements: 2.2, 6.1, 6.2_
  - _Boundary: SDK/public contract_
  - _Depends: 1.1_
  - _Validation: `go test -run TestValidateEventSequence ./pkg/lipapi/`_
- [x] 1.3 Collector no-op and output-commit exclusion
  - Add an explicit no-op `case EventReasoningSignatureDelta:` in `CollectWithLimits` so the signature is not aggregated into `Collected.Reasoning`.
  - In `pkg/lipapi/output_commit.go`, add a comment documenting that the new kind is intentionally excluded from `OutputCommitted` (signature is integrity metadata, not committing content).
  - Done is observable: `Collect` over a stream containing the new kind returns no error and leaves `Collected.Reasoning` unchanged; `OutputCommitted` returns false for the new kind.
  - _Requirements: 2.4, 6.3_
  - _Boundary: SDK/public contract_
  - _Depends: 1.1_
  - _Validation: `go test -run "TestCollect|TestOutputCommitted" ./pkg/lipapi/`_
- [x] 1.4 Canonical contract tests
  - Add tests: `ValidateEventSequence` accept-after-MessageStarted / reject-before; `ValidateEventEnvelope` oversized `Signature` rejected; `OutputCommitted` false; `Collect` ignores the kind without mutating reasoning.
  - Done is observable: the new tests pass and cover requirements 2.1-2.4, 6.1-6.3.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 6.1, 6.2, 6.3_
  - _Boundary: tests_
  - _Depends: 1.1, 1.2, 1.3_
  - _Validation: `go test ./pkg/lipapi/`_

- [x] 2. Anthropic backend signature capture (P)
- [x] 2.1 Capture `anthropic.SignatureDelta`
  - In `internal/plugins/backends/protocols/anthropicmessages/map_events.go`, add `case anthropic.SignatureDelta:` after the `ThinkingDelta` case in the `ContentBlockDeltaEvent` switch, pushing `lipapi.Event{Kind: lipapi.EventReasoningSignatureDelta, Signature: t.Signature}` when `t.Signature != ""`.
  - Done is observable: a stub `ContentBlockDeltaEvent` with a `SignatureDelta` produces an `EventReasoningSignatureDelta` with the signature; absence of `SignatureDelta` produces no such event.
  - _Requirements: 1.1, 1.2, 1.3_
  - _Boundary: backend plugin_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/plugins/backends/protocols/anthropicmessages/`_

- [x] 3. Anthropic frontend stream signature emission (P)
- [x] 3.1 Emit `signature` and `signature_delta` on the thinking block
  - In `internal/plugins/frontends/anthropic/encode.go`: extend `anthropicSSEThinkingBlock` with `Thinking string `json:"thinking"`` and `Signature string `json:"signature"`` (empty on start).
  - Add an `anthropicSSEDeltaSignature` payload struct (`content_block_delta` with `delta.type = "signature_delta"`, `delta.signature`).
  - Add a `thinkingSignature string` local in `WriteStreamSSE`; add `case lipapi.EventReasoningSignatureDelta:` that sets `thinkingSignature = ev.Signature`.
  - In `closeThinkingBlock`, before `content_block_stop`, if `thinkingSignature != ""`, emit `content_block_delta` with `signature_delta` on `thinkingBlockIdx`; reset `thinkingSignature = ""`.
  - Done is observable: a stream with `EventReasoningDelta` + `EventReasoningSignatureDelta` + text yields `content_block_start` (thinking, `signature:""`), `thinking_delta`, `signature_delta` (matching index, before `content_block_stop`), then the text block.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 4.1, 4.2_
  - _Boundary: frontend plugin_
  - _Depends: 1.1_
  - _Validation: `go test -run TestWriteStreamSSE_thinking ./internal/plugins/frontends/anthropic/`_
- [x] 3.2 Frontend stream tests
  - Add tests: `content_block_start` for thinking carries `signature:""`; `signature_delta` fires before `content_block_stop` with the stashed signature and matching index; no `signature_delta` when no signature event arrived (synthesized); two interleaved thinking blocks each emit their own `signature_delta` on their own index.
  - Done is observable: the new tests pass and cover requirements 3.1-3.4, 4.1-4.2.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 4.1, 4.2_
  - _Boundary: tests_
  - _Depends: 3.1_
  - _Validation: `go test ./internal/plugins/frontends/anthropic/`_
- [x] 3.3 Update frontend doc table
  - In `internal/plugins/frontends/anthropic/doc.go`, update the thinking/extended-blocks row to "Supported (encode, thinking + signature)".
  - Done is observable: the doc row reflects signature support.
  - _Requirements: 3.1_
  - _Boundary: docs_
  - _Depends: 3.1_
  - _Validation: `go build ./internal/plugins/frontends/anthropic/`_

- [x] 4. Non-Anthropic frontend no-op regression tests (P)
- [x] 4.1 Assert the signature signal is silently ignored by other frontends
  - Add a regression test in `gemini`, `openailegacy`, and `openairesponses` encode test files that feeds `EventReasoningSignatureDelta` through `WriteStreamSSE` and asserts no panic, no signature-related wire output, and unchanged text/tool output.
  - Done is observable: each frontend's test passes with the new event kind producing no extra wire output.
  - _Requirements: 5.1_
  - _Boundary: tests_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/plugins/frontends/gemini/ ./internal/plugins/frontends/openailegacy/ ./internal/plugins/frontends/openairesponses/`_

- [x] 5. Parity and end-to-end validation
- [x] 5.1 Parity: Anthropic frontend x Anthropic backend signature round-trip
  - Extend `internal/plugins/frontends/parity/visible_thinker_reasoning_test.go` (or add a parity case) that runs an Anthropic-FE x Anthropic-BE path and asserts `signature_delta` and the `signature` field round-trip on the thinking block.
  - Done is observable: the parity test passes for the Anthropic signature round-trip.
  - _Requirements: 1.1, 3.1, 3.2, 4.1_
  - _Boundary: tests_
  - _Depends: 2.1, 3.1_
  - _Validation: `go test -tags=precommit,integration ./internal/plugins/frontends/parity/`_
- [x] 5.2 End-to-end with `cmd/lipstd` and an Anthropic refbackend stub
  - Run `cmd/lipstd` against an `internal/refbackend/anthropicmessages` stub that emits `signature_delta`, and confirm a downstream `internal/refclient/anthropicmessages` client receives `signature_delta` plus `signature` on the thinking block.
  - Done is observable: the downstream client observes the signature on the thinking block end-to-end.
  - _Requirements: 1.1, 3.1, 3.2_
  - _Boundary: tests_
  - _Depends: 2.1, 3.1_
  - _Validation: `go test ./internal/refclient/anthropicmessages/ ./internal/refbackend/anthropicmessages/`_

- [x] 6. Quality gate and PR
- [x] 6.1 Run the quality and parity gates
  - Run `go build ./...`, `go vet ./...`, `golangci-lint run ./...` (gofmt + staticcheck + gofumpt), `make quality-checks`, and `make parity-checks`; fix any findings.
  - Done is observable: all gates pass green locally.
  - _Requirements: 2.1, 3.1, 5.1_
  - _Boundary: tests_
  - _Depends: 1.4, 2.1, 3.2, 4.1, 5.1_
  - _Validation: `make quality-checks && make parity-checks`_
- [x] 6.2 Open a focused PR and babysit CI
  - Create a `fix/anthropic-thinking-signature` branch, commit the spec + implementation, push, open a PR against `main`, and babysit `qa` CI + CodeRabbit until mergeable + green + comments triaged.
  - Done is observable: the PR is open, CI is green, and review comments are triaged.
  - _Requirements: 1.1, 2.1, 3.1_
  - _Boundary: wiring_
  - _Depends: 6.1_
  - _Validation: `gh pr view` shows MERGEABLE + green checks_
