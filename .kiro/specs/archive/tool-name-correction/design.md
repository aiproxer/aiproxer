# Design Document

## Overview
This slice lets an opt-in feature plugin mark a completed tool call as naming a tool the model was never offered. Core then withholds that call and everything the same backend attempt (B1) produces after it. At B1's backend finish, core reopens the turn once through the existing hidden-continuation transaction with a developer-role correction message. Only the corrected attempt's (B2) output reaches the client, on the same response. The first consumer is any agent harness reaching the proxy through a continuation-capable frontend with `tool-name-correction` enabled.

When a core safety rail declines, the call goes through exactly as today. When the follow-up cannot be published or the withheld output outgrows its bound, the withheld events and B1's finish are replayed through the normal client path in their original order.

### Goals
- An invalid first tool call on a tool-only (or reasoning-then-tool) attempt never reaches the client; one corrected attempt replaces it (2.1–2.5).
- Every decision degrades to today's behaviour and is visible as one bounded log record (3.x, 5.x).
- Done when a scripted two-attempt backend proves the client receives only the corrected call, for streaming and non-streaming, and each rail and the fail-open path deliver the original output in order.

### Non-Goals
Deferred in `requirements.md` (follow-up: #997):
- text-preamble turns;
- mid-stream cancellation of B1;
- partially released batches;
- allowed-tools and tool-choice violations;
- unrepairable arguments;
- closest-name ranking;
- tool calls on requests that offer no tools;
- large-body/wire parity, metrics, and enabling by default.

## Boundary Commitments

### This Spec Owns
- The additive `toolcall.ActionSteer` outcome and `toolcall.SteerRequest` contract.
- Core correction rails (budget, produced-output facts, history repeat, continuation support), attempt-local withholding, the correction trigger at B1's backend finish, and the ordered fail-open replay.
- Two policy parameters on the existing continuation transaction: the failure policy and the lineage policy. The existing caller keeps today's values.
- A dedicated `tool-name-correction` steering overlay, deactivated wherever the existing continuation overlay is.
- The `tool-name-correction` feature plugin: config, finalizer, correction message, and standard registration without injection.

### Out of Boundary
- Tool-call repair normalization, Agent Loop Guard decisions and lineage semantics, the terminal-decision provider slot, routing, failover and pre-output retry, the finish-authority chokepoint, frontend/backend wire encoding, conversation-view store semantics, billing policy.
- Any persisted state; any new extension plane or stage.

### Allowed Dependencies
- Feature → `pkg/lipsdk/toolcall`, `pkg/lipsdk/feature`, `pkg/lipapi`, `gopkg.in/yaml.v3`, stdlib (`path`). No `internal/core/*`.
- Core → its existing imports (`pkg/lipsdk/toolcall` is already imported). Core never imports the feature package.
- `internal/standardplugins` → feature package (registration only).

### Revalidation Triggers
- Changes to `continuationTransactionWithOverlay` publication, failure, or lineage semantics, or to overlay deactivation.
- Changes to the receive loop's drain order or to `dispatchClientFacingEvent`.
- Changes to `toolcall.Action` vocabulary, finalizer ordering, or the assembler's chain semantics.
- New client protocols added to `supportsContinuationForCall`.

## Architecture

### Seams Reused
- **Tool-call finalizer plane** (`feature.PlaneToolCallFinalizers`) and the per-B-leg assembler. The assembler already holds each call until `tool_call_finished`, passes the effective catalog, and continues the chain with a rewritten name.
- **Single backend-event seam** `prepareRecvEvent`. Its one call site in the receive loop (`executor_recv_loop.go`, ahead of every finish route) is where B1's backend finish is intercepted, before any client recording, observation, gate, or finish authority runs.
- **Assembler drain**, popped at the top of the receive loop and dispatched through `dispatchClientFacingEvent`, the same dispatcher fresh events use. Fail-open replay pushes onto it, so replayed events, including the finish, take the normal routes exactly once.
- **Continuation transaction** (`continuationTransactionWithOverlay`), which owns steering `Put`, projection, admission, publication, lifecycle-marker suppression, and B1 swallowed settlement. Every failure after the `Put` already disposes the prepared attempt.
- New contract surface: `ActionSteer` + `SteerRequest` only. Justification: 2.1 needs a finalizer to ask core to withhold and continue. `Pass`, `Rewrite`, and `Reject` cannot express it, and no plane or provider acts before the call is released (research: Options B–D).

### Project Boundary Questions (Go LIP)
- **Core-owned or plugin-owned?** Detection and message text are plugin-owned. Rails, withholding, continuation, and replay are core-owned.
- **New canonical concept?** No. It is one additive SDK outcome; `pkg/lipapi` is unchanged.
- **Streaming-first preserved?** Yes. Only output after an accepted steer, within one attempt, is held, and that output is bounded.
- **Provider SDK leakage?** None.
- **No retry/failover after first output?** Yes. This is a semantic continuation through the existing transaction, not a transparent retry. Withheld events never commit output, and rail 3.3 declines once the attempt produced non-reasoning output.
- **Secure-session, diagnostics, startup security?** Diagnostics only: one bounded, hash-only record. Withheld events are recorded only if replayed, exactly once, at dispatch.

### Flow
```mermaid
sequenceDiagram
    participant B1 as Backend attempt B1
    participant Asm as Tool call assembler
    participant Rail as Correction rails
    participant Loop as Receive loop
    participant Tx as Continuation transaction
    participant B2 as Backend attempt B2
    participant C as Client
    B1->>Asm: tool call finished with unknown name
    Asm->>Rail: steer gate
    alt declined
        Asm->>C: call released as today
    else accepted
        Rail-->>Asm: accept and consume budget
        Asm->>Loop: chain continues as pass
        Loop->>Loop: hold call and later B1 output in order
        B1->>Loop: backend response finished
        Loop->>Tx: correction intent with fail-open and preserved lineage
        alt published
            Tx->>B2: ingress plus correction overlay
            B2->>C: corrected output on the same response
        else not published or bound exceeded
            Loop->>C: held events then finish via normal dispatch
        end
    end
```

## File Structure Plan

### New Files
- `pkg/lipsdk/toolcall/steer.go`: `ActionSteer`, `SteerRequest`, bounds, `Validate`, `ReasonUnknownToolName`.
- `internal/core/runtime/tool_name_correction.go`:
  - rails (`admitToolCorrection`), reason codes, and the decision log record;
  - the attempt withhold queue and its bound;
  - the finish intercept (`runToolCorrection`) and the ordered fail-open replay.
- `internal/plugins/features/toolnamecorrection/doc.go`: package doc.
- `internal/plugins/features/toolnamecorrection/config.go`: `ID`, `Config`, `DecodeConfig`, defaults, validation.
- `internal/plugins/features/toolnamecorrection/finalizer.go`: `Finalizer` (`ID`, `Order`, `Finalize`).
- `internal/plugins/features/toolnamecorrection/message.go`: bounded correction-message builder and name sanitizer.
- `internal/plugins/features/toolnamecorrection/bundle.go`: `FeatureBundle(cfg)`.
- `config/examples/tool-name-correction.yaml`: operator example.

### Modified Files
- `pkg/lipsdk/toolcall/finalizer.go`: `Result.Steer *SteerRequest` field.
- `internal/core/runtime/tool_call_assembler.go`: an `ActionSteer` branch that calls the steer gate supplied with `ingest`. Accept and decline both continue the chain as a pass; accept additionally marks the attempt as withholding.
- `internal/core/runtime/attempt_session.go`: attempt-local produced-output facts, the `withholding` mark (set by the assembler when the steer gate accepts, read by the rails and the receive loop), and the withhold queue, all cleared on disposal and replacement.
- `internal/core/runtime/turn_terminal.go`: turn-scoped correction counter; correction overlay deactivated next to `continuationOverlayID`.
- `internal/core/runtime/response_pipeline_observations.go`: `prepareRecvEvent` records produced-output facts by kind before the assembler and passes the steer gate to `ingest`.
- `internal/core/runtime/executor_recv_loop.go`:
  - at the single `prepareRecvEvent` call site, route a withholding attempt's backend finish to `runToolCorrection`;
  - at that site and at the assembler-drain pop, divert a withholding attempt's events into its queue instead of dispatching them.
- `internal/core/runtime/terminal_decision_continuation.go`: failure-policy and lineage-policy parameters on `continuationTransactionWithOverlay`. `runContinuationTransaction` passes today's behaviour.
- `internal/standardplugins/standard_table.go`, `features_install.go`: registration row and factory (no `Ensure*` injection).
- `README.md`: one line in the feature-plugin row pointing at the example.
- `docs/adr/0007-canonical-tool-call-repair.md`: one paragraph recording `ActionSteer` in the decision matrix.

## Components and Interfaces

| Component | Package | Intent | Requirements |
|-----------|---------|--------|--------------|
| SteerOutcome | `pkg/lipsdk/toolcall` | Additive finalizer outcome asking core to withhold and continue | 2.1, 2.2, 3.1, 3.4 |
| SteerGate | `internal/core/runtime` (assembler) | Honors `ActionSteer` through the rails; chain continues as pass | 1.3, 2.1 |
| CorrectionRails | `internal/core/runtime` | Budget, produced output, history, continuation support; decision record | 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 5.1, 5.2 |
| AttemptWithhold | `internal/core/runtime` (attempt, receive loop) | Holds an accepted attempt's later events in order, bounded | 2.1, 2.4, 3.7 |
| CorrectionContinuation | `internal/core/runtime` (receive loop, transaction) | Intercepts B1's finish; publishes B2 or replays in order | 2.2, 2.3, 2.4, 2.5, 3.7 |
| CorrectionFinalizer | `internal/plugins/features/toolnamecorrection` | Detects unknown names; builds the message and the bundle | 1.2, 1.3, 1.4, 4.1, 4.2, 4.3 |
| FeatureConfig | `internal/plugins/features/toolnamecorrection` | Decodes and validates operator config | 1.1, 1.5, 3.1, 3.4 |
| Registration | `internal/standardplugins` | Standard opt-in row | 1.1 |

#### SteerOutcome
```go
// ActionSteer asks core to withhold this completed call and continue the turn
// once, hidden, with Steer.Instruction. Core may decline; the call then passes.
const ActionSteer Action = ActionReject + 1

const (
	MaxSteerInstructionBytes = 4096
	MinSteerTurnBudget       = 1
	MaxSteerTurnBudget       = 3
	ReasonUnknownToolName    = "unknown_tool_name"
)

type SteerRequest struct {
	Instruction         string // required, valid UTF-8, <= MaxSteerInstructionBytes
	TurnBudget          uint8  // corrections allowed per client turn, [1,3]
	AllowAfterReasoning bool   // reasoning produced by the attempt does not block
}

func (r SteerRequest) Validate() error // wraps ErrInvalidSteer

// Result gains: Steer *SteerRequest // set only with ActionSteer
```
- `ActionSteer` requires a valid `Steer`, empty `ToolName`, and nil `ArgsJSON`. Any other action with non-nil `Steer`, or an invalid `Steer`, takes the existing `unusableResult` fallback. A core that predates `ActionSteer` does the same through its `default` branch.
- Whatever the gate decides, the chain continues as if the steering finalizer returned `ActionPass`. Later finalizers, including mandatory decisions, still run, and the emitted lifecycle is the normal one.

#### SteerGate and CorrectionRails
- `ingest` receives a gate `func(name string, req toolcall.SteerRequest) bool` built by `prepareRecvEvent` for the current attempt. It is synchronous, does no I/O, and is owned by the single receive loop. A nil gate declines.
- `prepareRecvEvent` records produced-output facts before the assembler sees an event:
  - `producedVisible`: any `lipapi.OutputCommitted` kind other than reasoning, where a tool event counts only for a call ID other than the call being decided;
  - `producedReasoning`: `EventReasoningDelta`, `EventReasoningOpaqueDelta`, `EventReasoningPart`.

  Both are attempt-local, so B2 starts clean, and they count output whether or not it was released (3.3, 3.4).
- The gate declines, in this order, with reason codes:
  1. `budget_spent`: turn counter ≥ `req.TurnBudget`.
  2. `visible_output`: `producedVisible`.
  3. `reasoning_output`: `producedReasoning && !req.AllowAfterReasoning`.
  4. `history_repeat`: any `lipapi.NormalizedItems(ingress)` tool call has exactly `name`.
  5. `continuation_unsupported`: any of `!terminal.supportsContinuation`, an interleaved-thinker terminal, or completion gates present for the request.
  6. `steering_unavailable`: no steering writer factory or conversation reader.
- Otherwise it accepts with `corrected` and increments the turn counter. The budget is consumed on acceptance, so a replaced attempt cannot loop.
- Decision record (5.1, 5.2): one `slog` record `tool_name_correction` on the response pipeline logger with `trace_id`, `b_leg_id`, `tool_name_hash` (SHA-256 prefix), `outcome` (`corrected` or `delivered`), and `reason`. Later outcomes add a second record: `delivered/followup_failed` or `delivered/withhold_overflow`. Records never carry names, arguments, or instruction text.

#### AttemptWithhold and CorrectionContinuation
- **Withhold.** While an attempt is withholding, every event that would be dispatched for it is appended to the attempt's queue instead, in order. This applies to fresh events after `prepareRecvEvent` and to assembler-drain pops, including the steered call's own lifecycle. Usage and billing observation still run in `prepareRecvEvent`. Withheld events are never recorded, observed, or committed.
- **Bound.** The queue holds at most 256 events and 1 MiB of event deltas. Overflow ends withholding: the queue is pushed onto the assembler drain, the overlay is not touched, and the attempt continues as pass-through (`withhold_overflow`).
- **Finish intercept.** At the `prepareRecvEvent` call site, a withholding attempt's backend `response_finished` goes to `runToolCorrection(ctx, terminal, s, attempt, finish)` before dispatch. That function calls `continuationTransactionWithOverlay` with:
  - `TrajectoryRef`: B1 BLegID;
  - `Provenance` and `ReasonCode`: `"tool_name_correction"`;
  - `Instruction`;
  - overlay ID `tool-name-correction`;
  - the correction failure policy;
  - the preserve-lineage policy.
- **Lineage.** The preserve-lineage policy makes B2 inherit B1's lineage:
  - when B1's `continuationIntentFacts` are set, they are kept unchanged;
  - when they are unset, they are stored as set facts with the values B1 would have derived (`terminal_decision_evidence.go:50-71`): the trajectory from the call, the control reference = B1 BLegID, and the attempt = B1 Seq.

  Agent Loop Guard therefore evaluates B2's finish exactly as it would have evaluated B1's. The separate overlay never replaces an active Agent Loop Guard instruction.
- **Published.** The queue is discarded and the loop continues on B2. B2's finish takes the normal path, including the terminal-decision provider. The correction overlay is deactivated at the turn's final terminal, next to the existing overlay (2.5).
- **Not published.** The failure policy deactivates the correction overlay and returns `errToolCorrectionNotPublished` without claiming the request terminal. Cancellation and deadline causes keep the existing claim path. `runToolCorrection` then:
  1. pushes `[queue..., finish]` onto the assembler drain;
  2. clears the withhold state;
  3. records `delivered/followup_failed`.

  The loop pops and dispatches them through the normal routes in order: recording, gates, and finish authority each run exactly once (3.7).

#### CorrectionFinalizer and FeatureConfig
- `Finalize` returns `ActionPass` when:
  - `tool.Name != ""` (the name is in the catalog);
  - the catalog is empty;
  - or `path.Match` succeeds against any `pass_through_names` pattern.

  Otherwise it returns `ActionSteer{Instruction: buildMessage(name, catalog), TurnBudget, AllowAfterReasoning}` with `ReasonUnknownToolName`. `Order()` is 45, after the default orders of tool-call repair (40) and path virtualization (41). The example config notes that a repair order above 45 means near-miss names are corrected instead of repaired. That is still safe, just costlier.
- `buildMessage` writes a fixed sentence:

  > Tool "<n>" does not exist. Available tools: "a", "b", …. Call one of the available tools, or answer without calling a tool.

  `<n>` is the invented name reduced to printable non-quote ASCII and truncated to 64 runes. Each available tool is listed exactly, as a JSON string literal, so no catalog name is altered and none can break out of the sentence. Encoded names are added in catalog order while the whole message, including the closing sentence and room reserved for the widest possible `… and K more` suffix, stays ≤ `MaxSteerInstructionBytes` (4096). On overflow the message appends that suffix. The result therefore always passes `SteerRequest.Validate`. No descriptions or schemas are included.
- The finalizer is stateless after construction, so it is safe for concurrent use.

## Error Handling
- **Fail-open everywhere:**
  - an invalid steer result is unusable (existing fallback);
  - a rail decline is a pass;
  - a not-published follow-up or a queue overflow replays the held output in order.
- **B1 ends without a backend finish, or a later finalizer fails:** the queue is replayed in order before the existing handling continues. This covers an EOF where `handleEOF` synthesizes a finish, and a `drainErr` surfaced by `popDrainError`. The client therefore sees what it would see today: the call followed by that finish or that error.
- **B1 fails with a recoverable error** (idle, transport) before replay: the queue is discarded and existing pre-output recovery may replace B1 transparently, because withheld events never committed output. Cancellation discards the queue. A replacement attempt starts clean, and the consumed budget stays consumed.
- **Known risk: timers.** While withholding, client-side activity is not observed by stream recovery and the first-token deadline stays armed. This is acceptable for V1 because a tool-only turn finishes shortly after its call. A timeout falls under the recoverable-error rule above.
- **Post-publication settlement error** follows the existing transaction semantics unchanged.
- **Config.** Errors name the field: `max_corrections_per_turn` outside [1,3], `pass_through_names` longer than 32 entries, or a malformed pattern (`path.ErrBadPattern`).

## Testing Strategy
Follows Test Proportionality (`.kiro/steering/testing.md`).
- **Table tests:**
  - `SteerRequest.Validate` and result validity;
  - finalizer decisions (known, repaired, pattern, empty catalog, unknown);
  - message bounds (including a catalog that lands exactly on the 4096-byte boundary), truncation count and name sanitizing;
  - config decode errors;
  - gate decline order for each rail;
  - produced-output classification, where the decided call's own events do not count.
- **Integration (core seam):** runtime tests with a scripted two-attempt backend (B1: reasoning + unknown call + finish; B2: valid call):
  - the client receives only B2's call;
  - there is one response start and one finish, streaming and collected;
  - the overlay is deactivated after the turn;
  - the B2 request carries the developer message and not B1's call.
- **Fail-open:** forced admission failure, and separately queue overflow → the client receives B1's events (the original call plus any later output) followed by B1's finish, each recorded once, in order; admission resources are disposed.
- **Rails:** one integration case for `visible_output` (text before the call) and one for `history_repeat`. Budget exhaustion uses a backend that misses twice.
- **Agent Loop Guard:** with the guard enabled, B2's finish is evaluated with B1's lineage (no `invalid_progress`), both for a first continuation (unset lineage, B2 Seq > 1) and after an earlier guard continuation.
- **EOF and drain error:** B1 ending by EOF without a finish, and a later finalizer error, each deliver the original call before the synthesized finish or the error.
- **Composition:** a standard-distribution test that the feature row builds, contributes one finalizer at order 45, and is absent unless configured.
- **Existing generic rules extended:** none. The feature package already falls under the existing features-must-not-import-core archtest.

## Configuration
```yaml
plugins:
  features:
    - id: tool-name-correction
      enabled: true            # opt-in; absent or false = feature off
      config:
        max_corrections_per_turn: 1   # [1,3], default 1
        correct_after_reasoning: true # default true
        pass_through_names: []        # path.Match globs, e.g. ["mcp__*"], max 32
```
