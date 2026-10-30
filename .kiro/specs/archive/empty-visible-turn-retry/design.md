# Design Document: empty-visible-turn-retry

## Overview
When a surviving attempt ends on a clean terminal with no visible output, the proxy today
releases that empty finish and the client stalls. With the new operator gate enabled, the
proxy detects the shape *before* the finish is released and replaces the attempt once
through a bounded corrective open of the existing replacement machinery, so the client sees
one ordinary response: either the corrective attempt's content, or (when the corrective
attempt is empty too) its normal empty finish. With the gate disabled, no code path changes
behavior.

Correctness rests on one invariant that is stronger than "no answer text": **no canonical
client-visible frame of a correctable attempt may be released before the decision.** The
existing produced-output facts deliberately exclude reasoning, and adapters emit
`EventResponseStarted` before any content, so a detect-at-finish design would already have
released lifecycle (and possibly reasoning) frames. The slice therefore holds the
attempt's unreleased prefix in a bounded queue and latches a turn-level released-frame
guard; both dissolve on the first visible output, on overflow, or on any non-correctable
terminal.

### Goals
- Gate on: a clean empty visible turn produces exactly one corrective replacement; if the
  replacement produces content, the client sees only that content; a second empty finish is
  delivered as the accepted outcome; every decline is logged with one bounded reason.
- Gate off: client-visible events, usage handling, and metrics are identical to today.
- Done: the discarded attempt settles through existing accounting ports, the existing
  empty-turn metric still counts only the accepted winner, and the new bounded counter and
  decision record explain every decision.

### Non-Goals
- Every item in `requirements.md` Deferred, led by open-first corrective publication (V1
  deliberately keeps the existing replacement ordering, where retirement precedes opening).

## Owner-Approved Decisions
Overriding any of these reopens the design.
- **Failure outcome when the corrective attempt cannot be opened**: the turn ends with an
  explicit recoverable pre-output upstream failure, not the original empty finish. Vision
  basis: a silent empty turn is hidden degradation (line 3); an explicit failure is honest
  and client-retryable (priority 1). Retirement preceding `openReplacement` is a consequence,
  not the justification. Open-first preservation is Deferred.
- **Provider coexistence is conservative in V1**: a wired terminal-decision provider disables
  the correction entirely (Agent Loop Guard contributes one today). Permissive coexistence is
  Deferred.
- **The hold holds reasoning frames**: reasoning-only turns are correctable only inside the
  shared bounded retention budget.

## Boundary Commitments

### This Spec Owns
- The correction trigger, its rails and their deterministic precedence, and the prefix hold
  (arm, hold, release, overflow, discard) in the core receive loop.
- The turn-scoped released-frame guard, the one-shot corrective allowance, and the
  generation-pinned operator gate.
- The discarded attempt's retirement and usage-evidence retention *through existing ports*,
  and the new bounded decision record and counter.

### Out of Boundary
- The empty-visible-turn classification and `lip_empty_visible_turn_total` (unchanged).
- The tool-name correction withhold and `continuationTransactionWithOverlay` (untouched).
- Steering overlays, conversation projection, and any conversation mutation: the
  correction injects nothing.
- The terminal-decision provider contract, routing rules, admission rules, and every
  pricing/rating/settlement rule.

### Allowed Dependencies
- `internal/core/runtime`: receive loop, `turnTerminal`, `attemptSession`, `attemptSlot`,
  `recoveryController`, `responsePipeline` (existing seams only).
- `internal/core/config` + `internal/infra/runtimebundle` for the generation-pinned gate.
- `internal/core/diag` (decision log) and the optional metrics-sink extension pattern in
  `internal/core/runtime/metrics_sink.go` + `internal/infra/metrics/executor_prom.go`.

### Revalidation Triggers
- Receive-loop release order (`dispatchClientFacingEvent`, drains) or the finish routes.
- `finalizeResponseFinishedAuthority` and the terminal decision chokepoint.
- Replacement/admission ordering (`openReplacement`, `tryReplacementIteration`, slot swap).
- Output-commitment rules (`lipapi.OutputCommitted`) or the produced-output facts.

## Architecture

### Seams Reused
- **Finish intercept after `prepareRecvEvent`, before dispatch** (`executor_recv_loop.go`), the
  same position as `runToolCorrection`: backend observation ran once and nothing
  client-visible has been produced yet.
- **Bounded attempt-local hold** shaped like the existing `withheldQueue` (events + bytes,
  deep-copied on enqueue), stored beside it and discarded by the same
  `discardWithheld`/`discardSidebandState` cleanup.
- **Single client-release seam**: held frames are released through
  `dispatchClientFacingEvent`, so hooks, tool policies, gates, recorder, and accumulators run
  exactly once per released frame and never at hold time.
- **Existing replacement machinery in a bounded corrective open mode**: retirement via
  `terminalizeSwallowed`, then one - and only one - corrective open call
  (`recovery.tryReplacementIteration`) followed by the receive-loop publication sequence
  (`ready.Prepare` → `registerReplacement` → `slot.swapIfOpen` → `resetForReplacement` →
  `resetPolicy`). The block is deliberately *not* reused unmodified: its ordinary path re-plans
  candidate after candidate behind keepalives, so the correction adds a bounded branch (one
  call, no keepalive re-plan, failure ends the turn) while the post-error path keeps its loop.
  One `openNext` call evaluates one candidate (`openInitial`'s loop is unused) and the open
  acquires the shared attempt budget itself.
- **Conservative provider coexistence**: a wired terminal-decision provider
  (`terminalDecisionProvider != nil`, contributed today by Agent Loop Guard) makes the
  correction decline, so the provider operates exactly as it does now - no early or second
  provider invocation; permissive coexistence is deferred.
- **Existing failure terminal**: `terminalizeReplacementFailure` for a corrective open that
  cannot publish, given a typed `*lipapi.UpstreamFailureError{Phase: PhasePreOutput,
  Recoverable: true}` cause (today it passes `nil`); the wire shape stays the frontend's.
- **Generation-pinned configuration**: a typed `Config` section resolved by `runtimebundle`
  into an `Executor` field (the `StreamRecovery` shape), captured per turn in
  `bindTurnTerminalRuntime`; a reload builds a new generation, so an in-flight turn keeps its
  gate value.
- **Optional metrics-sink extension** (`EmptyVisibleTurnObserver` precedent) for the new counter.

### Rejected Alternatives
- **Reusing `continuationTransactionWithOverlay`**: it writes a developer steering overlay
  (`writer.Put`) and calls `markCommitted(b1)`; the correction must inject nothing and must
  not commit output it never released.
- **Detect-and-retry at the finish without holding**: `EventResponseStarted` (and often
  reasoning) is already released, so the replacement would duplicate a response prefix.
- **An early `sharedTerminalDecision` invocation at the intercept**: the finish route builds its
  input inside the chokepoint - after the accepted-normal preflight, from the transformed event,
  and from a freshly read accumulator snapshot whose event count, visible text, and
  `OutputCommitted` flag may differ from the pre-dispatch state - so the frozen key can differ
  and the early call would be a second provider invocation. V1 skips whenever a provider is
  wired (rail `terminal_decision_provider`).
- **Excluding the empty candidate to force a different route**: a single-candidate route could
  then never be corrected; V1 reuses existing selection unchanged.
- **Correcting on routes that can start a parallel race**: `tryOpenParallelGroup` acquires the
  attempt budget per arm and can open several provider attempts inside one open call; the rail
  declines such routes.
- **Holding reasoning is a deliberate consequence**: reasoning-only turns are correctable only
  while their frames are still held; once released (overflow), the turn is not retried.

### Project Boundary Questions (AIProxer)
- **Ownership**: core runtime; attempt lifecycle/failover/commitment are core by steering and
  no existing plane carries receive-loop attempt correction, so V1 adds no SDK surface.
- **Canonical concept**: none new; existing `lipapi.Event` values are held and released.
- **Streaming-first**: yes - hold and release operate on the canonical stream, and
  non-streaming remains collection over it.
- **Provider SDK leakage**: none; the slice touches no backend or wire type.
- **No retry after client-visible output**: strengthened, not weakened - correction requires
  zero released frames, and the released-frame latch makes that check explicit.
- **Secure session / diagnostics / startup**: no secure-session change; one decision log and
  one bounded counter; startup unchanged.

## File Structure Plan

### New Files
- `internal/core/runtime/empty_visible_turn_retry.go` — correction decision and rails, bounded
  prefix hold with its queue, decision record, retry observation seam.
- `internal/core/runtime/empty_visible_turn_retry_test.go` — rail order, hold bounds,
  release/discard, record discipline, allowance locality.
- `internal/core/runtime/empty_visible_turn_retry_loop_test.go` — receive-loop scripts:
  corrected turn, repeated empty, overflow, released-frame decline, gate off, open failure.
- `internal/core/config/empty_visible_turn_retry.go` — typed gate, effective resolution.
- `internal/infra/metrics/empty_visible_turn_retry_test.go` — counter family and label ratchet.
- `config/examples/empty-visible-turn-retry.yaml` — disabled default plus enabled snippet.

### Modified Files
- `internal/core/runtime/executor_recv_loop.go` — arm the hold per attempt, drain held frames
  before reading the backend, intercept the prepared finish, run the bounded corrective open
  branch, release before uncorrected finishes, release/discard on EOF, error, and close paths.
- `internal/core/runtime/attempt_session.go` — attempt-local hold state and its inclusion in
  `discardWithheld`.
- `internal/core/runtime/turn_terminal.go` — released-frame latch, one-shot allowance claim,
  pending-correction marker, gate binding, latch calls at release seams.
- `internal/core/runtime/executor_config.go` — the executor-side gate field captured per turn.
- `internal/core/runtime/metrics_sink.go` — optional retry observer and `Executor` recorder.
- `internal/infra/metrics/executor_prom.go` — `lip_empty_visible_turn_retry_total` family and
  sink method; `internal/infra/metrics/empty_visible_turn_test.go` extends the family check.
- `internal/core/config/model.go`, `validate.go` — new section and its validation hook.
- `internal/infra/runtimebundle/build_executor.go` — resolve the section into the executor
  policy that `bindTurnTerminalRuntime` captures.
- `config/config.yaml` — commented reference section next to `stream_recovery`.

## Components and Interfaces

| Component | Package | Intent | Requirements |
|-----------|---------|--------|--------------|
| Correction decision | `internal/core/runtime` | Rails and candidacy, decline record, allowance claim, retirement trigger | 2.1, 2.2, 2.4, 4.1, 4.3, 4.7, 5.2 |
| Prefix hold | `internal/core/runtime` | Bounded hold, release, overflow, discard, release order | 2.3, 3.1, 3.2, 3.3, 3.4, 3.5 |
| Corrective open mode | `internal/core/runtime` | One bounded open call, publication, `corrected`/`failed` record, failure mapping | 4.2, 4.4, 4.5, 4.6 |
| Terminal facts | `internal/core/runtime` | Released-frame latch, one-shot allowance, pending-correction marker, gate binding | 1.3, 2.3, 4.1 |
| Gate config | `internal/core/config` + `runtimebundle` | Typed default-off gate pinned per generation | 1.1, 1.3 |
| Retry observation | `internal/core/runtime` + `internal/infra/metrics` | Bounded counter and decision record | 5.1, 5.2, 5.3, 5.4 |

#### Correction decision (one receive-loop call)
```go
// decision only; performs no client I/O and never mutates the conversation.
func maybeCorrectEmptyVisibleTurn(ctx context.Context, t *turnTerminal, s *retryRecvStream,
    attempt *attemptSession, finish lipapi.Event) (correcting bool, err error)
```
- Candidacy precedes the rails: only `emptyVisibleTurn(attempt, finish)` candidates (clean
  taxonomy, no produced visible output) produce a decision record; other finishes return
  `correcting=false` silently.
- Rails evaluate in a fixed order, first decline recorded: `gate_disabled` (no record),
  `budget_spent`, `released_frames`, `withholding`, `hidden_thinker`, `wire_path`,
  `completion_gates`, `parallel_race`, `terminal_decision_provider`, `replacement_blocked`,
  `attempt_budget`, `hold_overflow`, `hold_not_armed`.
- Accepted (no record yet): claim the allowance, discard the hold, mark the correction pending,
  retire the empty attempt with `terminalizeSwallowed`, and let the bounded corrective open run.
- Record discipline (exactly one record per candidate): a decline writes `skipped` with its
  reason; an accepted correction writes `corrected` only after publication, or `failed` once
  when the open, preparation, registration, or publication fails. A later clean empty attempt is
  its own candidate decision (`skipped`/`budget_spent` with its lineage), never a duplicate.

#### Prefix hold (attempt-local)
```go
type correctionHold struct { // guarded by the existing withholdMu; discarded with it
    state   correctionHoldState    // armed | released | overflowed | discarded
    events  []recvEventPreparation // prepared once, dispatched once, deep-copied
    bytes   int
}
```
- Bound: the hold reuses the existing retention budget (`maxWithheldEvents` = 256 events,
  `maxWithheldBytes` = `1 << 20`) instead of a new arbitrary bound. The two queues cannot be
  active for one attempt: an accepted tool-name correction declines the empty-turn correction,
  and any tool-call output dissolves the hold first.
- Bound rule (mirrors `enqueueWithheld`): hold only while the held event count is below the
  event bound and the held bytes plus this frame stay within the byte bound; otherwise release
  all held frames unchanged, followed by this frame.
- Release enqueues held frames followed by the triggering frame into an ordered per-attempt
  queue (`popCorrectionHeld`) drained through `dispatchClientFacingEvent`; nothing is
  re-transformed.
- Discard clears the queue; it is reached from the correction, an uncorrected finish,
  EOF/error/close with a cancellation cause, and the existing attempt cleanup.
- Dissolve triggers: the first produced visible output (release held frames in order, then that
  output), the bound, or a finish the correction declines (release before the finish).

#### Terminal facts
- `releasedClientFrame`: turn-scoped latch set wherever a canonical frame is returned to the
  client (dispatch and the drain release tails), excluding keepalives; read by the rails.
- `claimCorrectiveRetry() bool`: one-shot compare-and-swap allowance, claimed before the
  fallible open and never refunded across replacements or continuations.
- `correctionPending`: turn-scoped marker set on acceptance and cleared when the corrective
  attempt publishes or its open fails; the replacement block reads it for the bounded mode.
- `bindTurnTerminalRuntime` copies the generation's gate value onto the terminal; the hold is
  armed only when the gate is on, the allowance is intact, and the latch is clear.

#### Corrective open mode
- Exactly one `recovery.tryReplacementIteration` call, relying on the existing guarantee that
  one `openNext` call evaluates one candidate and acquires the shared attempt budget itself.
- On success: `ready.Prepare` → `registerReplacement` → `swapIfOpen` → clear the marker, then
  one `corrected` record (corrective B-leg lineage) and the counter increment.
- On any failure (`err`, `!opened`, prepare, registration, closed publication window): dispose
  any unregistered candidate, clear the marker, take `terminalizeReplacementFailure`, and write
  exactly one `failed` record; never re-plan.
- After publication the turn is ordinary again: its pre-output failures follow existing
  replacement rules and the spent allowance cannot re-trigger a correction.

#### Retry observation
- New optional sink extension `EmptyVisibleTurnRetryObserver` (`OnEmptyVisibleTurnRetry(backend,
  outcome string)`), mirroring `EmptyVisibleTurnObserver`; existing sinks keep working.
- Counter `lip_empty_visible_turn_retry_total{backend, outcome}` with the closed outcome set
  `corrected|skipped|failed`; the reason lives only in the bounded decision record.
- `observeEmptyVisibleTurn` and `lip_empty_visible_turn_total` are untouched: a successful
  correction records no empty winner, and a second empty attempt counts once.

## Error Handling
- Every decline is fail-open: the empty finish is released unchanged after held frames, one
  `skipped` record is written, and the turn ends as it does today.
- **Owner-approved.** A correction that is accepted but whose single bounded open fails (no
  eligible candidate, admission denial, budget exhaustion at open time, preparation,
  registration, or a closed publication window) records `failed` once and ends the turn
  through `terminalizeReplacementFailure` with the typed recoverable pre-output cause; the
  client sees an explicit upstream failure, not a silent empty turn, and no held frame is
  released. Open-first publication preserving the original empty finish is Deferred.
- Cancellation, deadlines, and Close keep their existing claim paths: held frames are
  discarded, never released, and the spent allowance is never refunded.

## Testing Strategy
- Table tests: rails in declared order (each reason, including `terminal_decision_provider`
  and `parallel_race`), reasoning-only candidacy, non-clean terminals, hold bound at the event
  bound and just over the byte bound, deep copy, discard on lifecycle, release order.
- Decision records: exactly one record per candidate - `skipped` at a decline, `corrected`
  only after publication, `failed` once on each failure site, and a second clean empty attempt
  recording its own `skipped`/`budget_spent` (never a duplicate of the first).
- Gate-off characterization first: before the receive loop changes, a scripted test pins
  today's gate-off event sequence, usage, and metrics; it must stay byte-identical throughout.
- Receive-loop scripts (existing `retryRecvStream` harness): empty B1 then contentful B2 yields
  only B2's frames and one corrective open call (no re-plan); empty B1 then empty B2 yields one
  accepted empty finish and one existing-metric increment; reasoning overflow declines; a
  released `response_started` declines; a forced uncorrectable open ends the turn through the
  failure terminal (typed recoverable cause) with no held frame released; a wired
  terminal-decision provider leaves its evaluation path untouched.
- Accounting: the discarded attempt settles as the existing non-surfacing attempt (usage
  evidence retained), the corrective attempt settles as winner, and the client sees no usage
  frame from the discarded attempt.
- Fail-open/failure: cancellation while holding discards; EOF without a finish releases then
  ends as today; concurrent Close during a correction keeps existing semantics.
- Existing generic rules extended: the metrics family ratchet and the example-config loader
  test gain the new entries; no new architecture rule.

## Configuration
```yaml
# default off; pinned per configuration generation
empty_visible_turn_retry:
  enabled: false
```
The section decodes into a typed struct, is validated at load/reload (unknown keys rejected by
the existing decoder), and is resolved into the executor generation; bounds reuse the existing
retention constants (Deferred: operator tuning after measurement).
