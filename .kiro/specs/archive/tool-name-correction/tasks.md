# Implementation Plan

- [x] 1. Add the steer outcome to the public tool-call finalizer contract
  - Add the additive steer action and the steer request (instruction, per-turn budget, reasoning allowance), with bounded validation and a stable unknown-tool reason code. A result that carries a steer request with any other action, or an invalid request, is unusable.
  - Record the new outcome in ADR 0007's decision matrix in one short paragraph.
  - Done when table tests cover valid and invalid steer requests (empty, oversized, invalid UTF-8, budget outside one to three), and the existing toolcall package tests pass unchanged.
  - _Requirements: 2.1, 2.2, 3.1, 3.4_
  - _Boundary: SDK/public contract (SteerOutcome)_
  - _Validation: go test ./pkg/lipsdk/toolcall/..._

- [x] 2. Core correction path
- [x] 2.1 Honor the steer outcome in the tool-call assembler through a synchronous gate
  - When a finalizer returns a usable steer outcome, ask the gate supplied with the event.
  - Whatever the gate answers, continue the finalizer chain as if that finalizer had passed. On acceptance, additionally mark the attempt as withholding.
  - A missing gate declines. An invalid steer result takes the existing unusable-result fallback.
  - Add only the attempt's withholding mark here (set on acceptance, read-only elsewhere); the queue, its bound, and clearing belong to 2.3.
  - Done when assembler tests show:
    - accepted and declined steers both release the normal lifecycle to the drain, byte for byte;
    - later finalizers still run;
    - only acceptance marks the attempt;
    - an earlier repair rewrite is what reaches the gate.
  - _Requirements: 1.3, 2.1_
  - _Boundary: core/runtime (SteerGate)_
  - _Depends: 1_
  - _Validation: go test ./internal/core/runtime/ -run ToolCall_

- [x] 2.2 Implement the correction rails, produced-output facts, and the decision record
  - Record, per backend attempt and before tool-call assembly, whether non-reasoning visible output (excluding the decided call's own tool events) or reasoning output was produced. Record, per client turn, how many corrections were accepted.
  - Decline, in the documented order, for:
    - budget spent;
    - visible output produced;
    - reasoning produced (when not allowed);
    - the same name already called in the client's history;
    - unsupported continuation (protocol, interleaved thinker, completion gates present);
    - unavailable steering.

    Otherwise accept and consume one unit of budget.
  - Emit one bounded decision log record with the hashed tool name, outcome, and reason; never names, arguments, or instruction text.
  - Touches the turn terminal only for the correction counter; 2.4 later adds overlay deactivation there.
  - Done when a table test drives every decline reason, the accept path, and the produced-output classification, and asserts the record contains no raw name.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 5.1, 5.2_
  - _Boundary: core/runtime (CorrectionRails)_

- [x] 2.3 Hold a withholding attempt's output in a bounded ordered queue
  - While an attempt is withholding, divert every event that would be dispatched for it into its queue, in order. This covers fresh events after preparation and tool-call drain pops, including the steered call itself. Usage and billing observation still run.
  - Touches the receive loop at the single event-preparation call site and at the tool-call drain pop; 2.4 adds the finish intercept at the same call site, which must run before this divert.
  - Bound the queue (256 events, 1 MiB of deltas). On overflow, release the queue in order through the normal dispatch path, stop withholding, and record the overflow.
  - When the attempt ends without a backend finish (an end of stream where a finish is synthesized), or a later finalizer error is about to surface, replay the queue in order first so the client sees the call before that finish or error. On recoverable errors and cancellation, discard the queue.
  - Clear the queue and withhold state on attempt disposal and replacement.
  - Done when runtime tests show:
    - text and tool calls after the invalid call, and the call itself, do not reach the client while withholding, while usage is still accounted for;
    - overflow delivers everything in original order, each event recorded once;
    - an end of stream without a finish, and a later finalizer error, each deliver the call before the synthesized finish or the error;
    - a replaced attempt starts clean.
  - _Requirements: 2.1, 2.4, 3.7_
  - _Boundary: core/runtime (AttemptWithhold)_

- [x] 2.4 Continue hidden at the original attempt's backend finish, with fail-open replay
  - Give the existing continuation transaction a failure policy and a lineage policy. The terminal-decision caller keeps today's values.
    - The correction failure policy only deactivates its overlay and reports not-published, except on cancellation or deadline.
    - The preserve-lineage policy keeps prior continuation facts unchanged. When they are unset, it stores the lineage the original attempt would have derived (its trajectory, its own leg as control reference, and its own sequence).
  - Use a dedicated correction overlay, deactivated wherever the existing continuation overlay is (turn terminal).
  - Place the finish intercept at the event-preparation call site, ahead of the divert from 2.3.
  - When a withholding attempt's backend finish arrives, run the transaction with the correction intent before the finish is dispatched.
    - On publication, discard the queue and continue on the corrected attempt.
    - Otherwise, push the queue followed by the finish onto the tool-call drain, clear the withhold state, and record the follow-up failure.
  - Done when runtime tests with a scripted two-attempt backend show:
    - only the corrected call reaches the client, with one response start and one finish, for streaming and collected responses;
    - the follow-up request carries the correction message and not the invalid call;
    - the correction overlay is gone after the turn;
    - a forced admission failure delivers the original output then the original finish, in order, each recorded once, with admission resources disposed;
    - with Agent Loop Guard enabled, the corrected attempt's finish is evaluated with the original lineage, both on a first continuation and after an earlier guard continuation;
    - existing terminal-decision continuation tests pass unchanged.
  - _Requirements: 2.2, 2.3, 2.4, 2.5, 3.7_
  - _Boundary: core/runtime (CorrectionContinuation)_
  - _Depends: 2.1, 2.2, 2.3_

- [x] 3. Tool name correction feature plugin
- [x] 3.1 (P) Decode and validate the feature configuration
  - Decode the correction budget (default one, range one to three), the reasoning switch (default on), and up to 32 pass-through name patterns. Reject out-of-range values and malformed patterns with errors naming the field.
  - Done when table tests cover defaults and every rejection.
  - _Requirements: 1.1, 1.5, 3.1, 3.4_
  - _Boundary: feature plugin (FeatureConfig)_
  - _Depends: 1_

- [x] 3.2 Detect unknown tool names, build the correction message, and package the bundle
  - Pass a call when it names an offered tool, when it matches a pass-through pattern, or when no tools were offered. Steer every other call with the configured budget and reasoning switch.
  - Build the message:
    - a fixed explanation and instruction;
    - the sanitized invalid name, at most 64 printable characters;
    - available names in catalog order, with room reserved so the message including the omitted-count suffix never exceeds 4096 bytes;
    - no descriptions or schemas.
  - Done when table tests cover:
    - known, repaired, pattern, empty-catalog, and unknown cases;
    - message truncation with the omitted count, including a catalog landing exactly on the 4096-byte boundary;
    - hostile names (quotes, control bytes, oversized);
    - the feature bundle contributing exactly one finalizer, ordered after the default tool-call repair and path virtualization orders.
  - Build that bundle from the configuration of 3.1.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 4.1, 4.2, 4.3_
  - _Boundary: feature plugin (CorrectionFinalizer)_

- [x] 4. Register the feature in the standard distribution and prove the first consumer path
  - Register the opt-in feature row and factory without any default injection. Add the operator config example (including the note on repair order) and one README line.
  - Prove end to end through the standard composition that:
    - a configured row installs the finalizer, and an absent row installs nothing;
    - a frontend request whose backend invents a tool name receives only the corrected call.
  - Done when the composition test and one frontend-level integration test pass, and the example config loads.
  - _Requirements: 1.1, 2.3_
  - _Boundary: config/wiring (Registration)_
  - _Depends: 2.4, 3.2_

## Completion Status

- [x] All tasks implemented and merged in #1017 (`ba1df9f9fc46c079acdcb013fd9fa4a38e5ca0fa`), later touched only by the #1055 lint cleanup (`27d5e67a8`).
- [x] Task 4 first-consumer-path proof verified on merged main (`ef0fbe5e7`), locally passing:
  - `internal/stdhttp/tool_name_correction_frontend_test.go` — `TestToolNameCorrection_frontendReceivesOnlyCorrectedCall`: a feature-enabled request whose backend invents `invented_tool` is corrected through the hidden continuation; the follow-up request carries the correction message, only the corrected call reaches the client, and there is exactly one finish.
  - `internal/standardplugins/tool_name_correction_factory_test.go` — a configured row installs exactly one finalizer at order 45; absent and disabled rows install nothing.
  - The operator example `config/examples/tool-name-correction.yaml` is exercised by `TestConfigExamples_passInspectRoutes`.
- [x] Deferred scope remains deferred exactly as listed in `requirements.md` `## Deferred`; no V1 code exists for it.
