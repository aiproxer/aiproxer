# Research & Design Decisions: empty-visible-turn-retry

## Summary
- **Feature**: `empty-visible-turn-retry` (GitHub issue #1197)
- **Discovery Scope**: Extension (existing core runtime, existing replacement/terminal machinery)
- **Key Findings**:
  - The trigger shape already exists and is observable: `emptyVisibleTurn` (`internal/core/runtime/empty_visible_turn.go`) classifies a clean terminal with no produced visible output, and `finalizeResponseFinishedAuthority` records it after the accepted winner (`executor_settlement.go:490-496`, #1196 / `0e332869d`). Reasoning-only output counts as empty and is deliberately excluded from the produced-output facts.
  - The finish is irreversible only at `finalizeResponseFinishedAuthority`; before that, the receive loop can divert a prepared finish without releasing anything, exactly as the tool-name correction does (`runToolCorrection`, `tool_name_correction.go:246-296`).
  - `continuationTransactionWithOverlay` is the wrong seam: it persists a steering overlay (`writer.Put`) and calls `markCommitted(b1)`, i.e. it mutates the conversation and commits output the client never received.
  - The existing replacement machinery already owns pre-output attempt replacement, retirement (`terminalizeSwallowed`), admission, attempt budget, and the failure terminal (`terminalizeReplacementFailure`), but its receive-loop block re-plans candidate after candidate behind keepalives and a parallel leading group can race several provider attempts, so the correction needs a bounded corrective open mode (exactly one open call) plus a `parallel_race` rail, with the allowance claimed before the fallible open.
  - One ordering constraint is decisive: `openReplacement` requires the prior attempt retired, so after retirement the original empty finish cannot be replayed; a failed open ends the turn through the existing failure terminal with a typed recoverable pre-output cause (owner-approved: a silent empty turn is hidden degradation; an explicit failure is honest and client-retryable).
  - The exclusive terminal-decision provider cannot be consulted early: the finish route builds its input after the accepted-normal preflight, from the transformed event, and from a freshly read accumulator snapshot whose key inputs can differ, so V1 declines whenever a provider is wired instead of risking a second invocation.

## Research Log

### Existing empty-turn signal and produced-output facts
- **Context**: Issue #1197 builds on `lip_empty_visible_turn_total`; the correction must reuse the classification without loosening it.
- **Sources Consulted**: `internal/core/runtime/empty_visible_turn.go`, `empty_visible_turn_test.go`, `internal/core/runtime/attempt_session.go:199-208,401-448`, `internal/infra/metrics/executor_prom.go:74-104`, commit `0e332869d`.
- **Findings**: `cleanEmptyTurnTerminal` is a bounded allowlist (status `completed`, or the documented clean finish reasons); `producedVisibleOutput` counts text/decision/media/tool lifecycle and excludes reasoning; `outputCommitObservation` is release-based.
- **Implications**: The correction reuses `emptyVisibleTurn` read-only and adds its own retryability predicate; it must not change the metric's meaning, so the discarded attempt records only the new counter/log.

### Where a frame becomes irreversible
- **Context**: The correction must run before any client-visible frame is released.
- **Sources Consulted**: `internal/core/runtime/executor_recv_loop.go` (dispatch closure, drains, EOF/error paths), `internal/core/runtime/executor_settlement.go:275-498`, `pkg/lipapi/output_commit.go`.
- **Findings**: `dispatchClientFacingEvent` is the single transformation/dispatch seam; `finalizeResponseFinishedAuthority` is the single finish chokepoint (reached from four routes); `OutputCommitted` commits on text, decisions, media, tool arguments, and reasoning, but lifecycle frames such as `EventResponseStarted` never commit. Backend adapters emit `EventResponseStarted` before content (`openaicompat/chat_stream.go:252`, `anthropicmessages/map_events.go:179`), and frontends encode it as the stream start.
- **Implications**: "No answer text" is not a sufficient guard: the hold must hold lifecycle frames too, and the released-frame guard must be separate from `producedVisible` and from `terminal.committed()` (which only sees commitment-class frames).

### Precedent: tool-name correction withhold and finish intercept
- **Context**: The closest existing feature withholds output and decides at the finish.
- **Sources Consulted**: `internal/core/runtime/tool_name_correction.go`, `tool_name_correction_withhold_test.go`, `executor_recv_loop.go:699-725,912-950`.
- **Findings**: The hold is a bounded queue (`maxWithheldEvents = 256`, `maxWithheldBytes = 1 << 20`) with deep-copied events; preparation happens once before the hold; release is ordered and dispatched through the normal seam; cancellation discards; lifecycle cleanup runs through `discardSidebandState`/`discardWithheld`.
- **Implications**: Reuse the shape (own queue because the assembler may be nil), the "held events were observed once at preparation" property, the release-through-dispatch rule, and the discard-on-cancellation rule. An accepted tool-name correction takes precedence: the empty-turn rails skip a withholding attempt.

### Rejected seam: `continuationTransactionWithOverlay`
- **Context**: The transaction already publishes a successor B-leg before any output and fails open by leaving the original attempt deliverable.
- **Sources Consulted**: `internal/core/runtime/terminal_decision_continuation.go:62-230,279-314`.
- **Findings**: It requires a steering writer and writes a developer-role overlay; it calls `markCommitted(b1)`; it suppresses the successor's lifecycle prefix via `markContinuationLifecyclePending`; it requires `supportsContinuation`; on failure it deactivates the overlay and returns `errToolCorrectionNotPublished`.
- **Implications**: Reusing it would inject a corrective instruction (forbidden), commit unreleased output (blocks later failover and TTFT scoping), and suppress lifecycle markers the client never saw. The empty-turn correction must instead publish a *replacement* whose own prefix is the first thing the client sees.

### Replacement machinery, budgets, and the retirement ordering
- **Context**: The refined direction mandates one corrective replacement inside existing budgets, with an honest fallback and no routing cascade.
- **Sources Consulted**: `internal/core/runtime/recovery_controller.go`, `executor_open_attempt.go:1043-1205`, `executor_open_loop.go`, `parallel_race.go:182-260`, `executor_clock_rng.go`, `attempt_session.go:1672-1709`, `executor_recv_loop.go:804-859`, `internal/core/routing/initial_candidates.go`.
- **Findings**: `openInitial` loops until a candidate opens; a single `openNext` call evaluates exactly one candidate for sequential and weighted groups, but `tryOpenParallelGroup` acquires the attempt budget per arm and can open several provider attempts inside that one call. The receive-loop replacement block re-invokes `tryReplacementIteration` after `p.keepaliveEvent()` when `!opened`, excluding one candidate per iteration, so a correction placed in that block unbounded would open several corrective B-legs. `openReplacement` computes `prior.authority.Settled() || !prior.authority.IsActive()` and refuses otherwise; the attempt budget is acquired inside `openAttemptTx` (`tryAcquire`, `budget.max` from `effectiveMaxAttempts`). `terminalizeSwallowed` settles the discarded attempt as an existing non-surfacing attempt (`ReleaseKindSwallowed`, `LegOutcomeSwallowed`, `AttemptSwallowedFailure`, `OutcomeReplaced`) with usage evidence intact; `terminalizeReplacementFailure` is the existing failure terminal. `routing.InitialCandidates(sel)` lists selector candidates and flags parallel arms (`IsParallel`).
- **Implications**: The correction must add a bounded branch: exactly one `tryReplacementIteration` call, no keepalive re-plan, failure → `terminalizeReplacementFailure`; routes whose candidates include a parallel arm are declined (`parallel_race`) because one call there can open more than one attempt. The allowance is claimed before the open and never refunded, and after B2 publishes the turn returns to ordinary replacement semantics with no correction-specific planning.

### Terminal-decision provider exclusivity
- **Context**: One core chokepoint owns terminal decisions in a generation, and the provider is reachable in production (Agent Loop Guard contributes `PlaneTerminalDecisionProvider`).
- **Sources Consulted**: `internal/core/runtime/terminal_decision.go`, `terminal_decision_continuation.go`, `executor_settlement.go:275-300`, `response_pipeline.go:783-820`, `internal/standardplugins/features_install.go:47-65`.
- **Findings**: `sharedTerminalDecision` caches a final decision per key, but the finish route builds the input *inside* the chokepoint after the accepted-normal preflight, from the transformed event, and from `p.accumulatorSnapshot()` whose event count, visible text, and `OutputCommitted` flag can differ from the pre-dispatch state; the key therefore may not match an early evaluation, making an early call a second provider invocation. The nil-provider fast path is free (`allowStopDecision("no_provider")`).
- **Implications**: V1 declines the correction whenever `terminalDecisionProvider != nil` (rail `terminal_decision_provider`, bounded reason), leaving provider behaviour bit-for-bit unchanged; permissive coexistence is Deferred.

### Configuration and generation pinning
- **Context**: The gate must be default-off, operator-visible, and stable within a turn.
- **Sources Consulted**: `internal/core/config/model.go`, `interleaved.go`, `validate.go`, `internal/infra/runtimebundle/build_executor.go`, `candidate_compile.go`, `internal/core/runtime/executor_assemble_stream.go:24-50`, `internal/standardplugins/featurehost/generation.go`.
- **Findings**: `interleaved: {enabled: bool}` is the minimal default-off gate precedent; `StreamRecovery` shows config resolved into an `Executor` field at executor build time; the executor is built per compiled generation (`candidate_compile.go` → `buildExecutorRuntime`), and `bindTurnTerminalRuntime`/assembly is where per-turn facts are captured. `config/examples/*.yaml` are all loaded by `TestConfigExamples_passInspectRoutes`.
- **Implications**: A `empty_visible_turn_retry: {enabled: bool}` section resolved in `runtimebundle` and captured on the terminal is enough; no plugin row, no SDK plane, no `pkg/lipapi` change.

### Metrics and observation pattern
- **Context**: The new counter must not disturb the existing empty-turn metric.
- **Sources Consulted**: `internal/core/runtime/metrics_sink.go`, `internal/infra/metrics/executor_prom.go`, `internal/infra/metrics/empty_visible_turn_test.go`, `internal/infra/metrics/bundle_test.go`.
- **Findings**: Optional sink extensions are type-asserted so existing sinks keep working; the family is registered in `reg.MustRegister`; a ratchet test pins the label set; `diag.LogDecision` is the bounded decision-record convention.
- **Implications**: Add one optional observer plus `lip_empty_visible_turn_retry_total{backend, outcome}`; reasons stay in the decision record only.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| Detect at finish, no hold | Retry when the clean empty finish arrives if produced facts are empty | Smallest code | `response_started` (and often reasoning) already released; would duplicate prefixes or violate the released-frame invariant | Rejected |
| Bounded prefix hold (selected) | Hold unreleased frames; release on visible output/overflow; discard on correction | Satisfies "no frame released"; reuses the withhold shape and single dispatch seam | Delays the prefix until first visible output or the bound when the gate is on | Chosen |
| Continuation transaction | Reuse `continuationTransactionWithOverlay` | Existing publication ordering with fail-open fallback | Injects a developer overlay, marks committed, suppresses successor lifecycle | Rejected |
| Replacement machinery (selected) | Retire as non-surfacing then `continue` into the existing replacement block | One bounded attempt inside existing admission/budget/deadline; no new sequencing | Open failure ends the turn explicitly (retirement precedes opening) | Chosen |
| Feature plugin + new plane | Implement as a plugin behind a new SDK plane | Keeps core smaller | No existing plane carries receive-loop correction; V1 forbids new planes | Rejected |

## Design Decisions

### Decision: D1 - Correction trigger at the prepared finish, before any release
- **Context**: The shape is only observable at the accepted clean finish.
- **Alternatives Considered**: (1) terminal-side detection inside `finalizeResponseFinishedAuthority`; (2) receive-loop intercept after `prepareRecvEvent`.
- **Selected Approach**: Intercept the prepared backend finish at the same position as the tool-name correction and either correct or fall through to the unchanged finish routes.
- **Rationale**: Backend observation has already run exactly once, nothing was dispatched, and the request terminal is untouched - so "retire then replace" is legal.
- **Trade-offs**: The intercept must be added to one more place in the loop; the terminal chokepoint stays single because only the receive loop owns this trigger.
- **Follow-up**: Revalidation trigger list in `design.md`.

### Decision: D2 - Bounded prefix hold plus a turn-level released-frame latch
- **Context**: "No canonical client-visible frame released" must hold, and lifecycle frames never commit.
- **Alternatives Considered**: (1) rely on `producedVisible`; (2) rely on `terminal.committed()`; (3) explicit hold + latch.
- **Selected Approach**: Attempt-local bounded hold (events + bytes) armed only while correction is still possible, plus a turn-scoped `releasedClientFrame` latch set at the release seams.
- **Rationale**: Produced facts exclude reasoning by design, and commitment excludes lifecycle frames; only a hold plus a release-side latch covers both.
- **Trade-offs**: With the gate on, prefix frames wait for the first visible output or the bound; reasoning-only turns are correctable only inside the bound (documented, fail-open on overflow).
- **Follow-up**: Bounds reuse the existing withhold constants; operator tuning deferred.

### Decision: D3 - Bounded corrective open mode after retirement
- **Context**: One corrective attempt inside existing budgets, with an honest failure path and no routing cascade.
- **Alternatives Considered**: (1) a new open-first publication sequence; (2) `openReplacement` after `terminalizeSwallowed` with the ordinary unlimited re-plan block; (3) the same retirement plus a bounded corrective branch (one open call, no keepalive re-plan).
- **Selected Approach**: Discard the hold, retire the empty attempt as an existing non-surfacing attempt, claim the one-shot allowance *before* the fallible open, then run exactly one `tryReplacementIteration` through a corrective branch of the replacement block: success publishes B2 and records `corrected`; any failure records `failed` and takes `terminalizeReplacementFailure`.
- **Rationale**: `openReplacement` requires retirement first, one `openNext` call evaluates one candidate and acquires the shared attempt budget itself, and avoiding the keepalive re-plan loop is what turns "at most one corrective attempt" into a real bound (the ordinary path would otherwise exclude one candidate per iteration and open several corrective B-legs). No new API is introduced.
- **Trade-offs**: An open failure after retirement cannot deliver the original empty finish; the turn ends through the existing failure terminal. Routes whose candidates include a parallel arm are declined because one call there can open several attempts. Both consequences are owner-approved V1 decisions; the failure carries a typed recoverable pre-output cause.
- **Follow-up**: After B2 publishes, the turn is ordinary again; the spent allowance never re-triggers a correction and no correction-specific planning follows B2 failures.

### Decision: D4 - Conservative provider coexistence (no early evaluation)
- **Context**: Terminal decisions are exclusive per generation, and the provider is reachable in production (Agent Loop Guard).
- **Alternatives Considered**: (1) evaluate the shared slot early at the intercept and require allow-stop; (2) decline whenever a provider is wired.
- **Selected Approach**: Rail `terminal_decision_provider`: a wired provider disables the correction with a bounded reason; the provider's path is untouched.
- **Rationale**: The chokepoint builds its input after the accepted-normal preflight, from the transformed event, and from a freshly read accumulator snapshot, so an early evaluation can produce a different frozen key - a second provider invocation rather than a cache hit - which V1 must not introduce.
- **Trade-offs**: Enabling Agent Loop Guard disables the corrective retry in V1; permissive coexistence is Deferred.
- **Follow-up**: Define a provider-visible correction request in a later SDK slice.

### Decision: D8 - Exactly one decision record per candidate
- **Context**: An accepted correction can still fail to open; recording `corrected` at retirement would both duplicate and misclaim.
- **Alternatives Considered**: (1) record at retirement and again on failure; (2) record `skipped` at declines and defer the accepted outcome until publication/failure.
- **Selected Approach**: A decline writes one `skipped` record with its reason at the decision; an accepted correction writes `corrected` only after B2 publishes, or `failed` once on the open/prepare/register/publication failure. A later clean empty attempt is its own candidate decision (`budget_spent`), attributed to its own lineage.
- **Rationale**: One record per candidate keeps the log and the counter honest, and never claims a correction that did not happen.
- **Trade-offs**: The record for an accepted correction is delayed until publication; the turn-scoped pending marker makes that window explicit.
- **Follow-up**: None.

### Decision: D5 - Gate in typed core config, pinned to the generation
- **Context**: Default-off operator gate, no new planes or SDK surface.
- **Alternatives Considered**: (1) feature plugin row; (2) typed core config section resolved by `runtimebundle`.
- **Selected Approach**: `empty_visible_turn_retry.enabled` decoded into a typed struct, validated at load, resolved into the executor, and captured onto the terminal per turn.
- **Rationale**: Mirrors `interleaved`/`stream_recovery`; reloads build new generations, so in-flight turns keep their value; no plugin plane carries receive-loop correction.
- **Trade-offs**: The gate lives in the runtime bundle rather than in a feature bundle; that matches the ownership of attempt lifecycle.
- **Follow-up**: None.

### Decision: D6 - Separate retry observation, unchanged existing metric
- **Context**: Operators must see decisions without redefining the accepted-winner signal.
- **Selected Approach**: `lip_empty_visible_turn_retry_total{backend, outcome}` with `corrected|skipped|failed`, plus one bounded decision record carrying the reason; `lip_empty_visible_turn_total` untouched.
- **Rationale**: The existing metric keeps meaning "the client received an empty finish"; discarded attempts are explained by the new signal.
- **Trade-offs**: Two signals must be read together; documented in the config example.
- **Follow-up**: Dashboards deferred.

### Decision: D7 - Retryability is a core predicate, not a change to the classification
- **Context**: Reasoning-only turns are empty but not automatically retryable.
- **Selected Approach**: `emptyVisibleTurn` stays the classification; a separate retryability predicate combines it with the hold state, the released-frame latch, the allowance, the gate, and the path exclusions.
- **Rationale**: Keeps #1196's signal stable and makes each skip reason explicit and bounded.
- **Trade-offs**: Two predicates to keep aligned; a table test pins the pairs.
- **Follow-up**: None.

## Risks & Mitigations
- Held frames grow expensive on long reasoning turns -> the shared event/byte bound fails open, deep copies happen only after the bound check, and lifecycle cleanup discards the queue.
- A gate-on deployment notices prefix latency -> documented consequence of holding; overflow returns to today's streaming; the gate is default off.
- Retirement precedes opening, so an open failure surfaces a failure rather than the empty finish -> rails pre-check allowance, released frames, hold state, replacement-blocked flag, parallel arms, provider presence, and attempt-budget room; the failure is recorded once as `failed`; open-first preservation is Deferred.
- The correction could open more than one attempt -> one `tryReplacementIteration` call, no keepalive re-plan, `parallel_race` rail, allowance claimed before the open.
- A provider could be invoked early or twice -> rail `terminal_decision_provider` declines whenever a provider is wired; the provider path is unchanged.
- Duplicate or premature decision records -> one record per candidate, with `corrected` deferred until publication and `failed` written once per failure site.
- Double accounting of held frames -> held frames are prepared once and dispatched once through `dispatchClientFacingEvent`; tests assert one observation and one transformation per frame.
- Cancellation while holding could leak memory -> the hold is discarded by `discardWithheld` on cancel, close, loss, replacement, and disposal.

## References
- Issue #1197 - opt-in bounded corrective retry for degenerate empty visible turns.
- Commit `0e332869d` / #1196 - empty visible turn counter and classification.
- `internal/core/runtime/empty_visible_turn.go`, `executor_settlement.go` - classification and accepted-winner observation.
- `internal/core/runtime/tool_name_correction.go`, `executor_recv_loop.go` - withhold queue and bounds, finish intercept, release/discard order.
- `internal/core/runtime/terminal_decision_continuation.go`, `terminal_decision.go` - continuation transaction (rejected) and the exclusive provider slot.
- `internal/core/runtime/recovery_controller.go`, `executor_open_attempt.go`, `executor_open_loop.go`, `parallel_race.go`, `executor_clock_rng.go` - replacement, candidate evaluation, parallel races, attempt budget.
- `internal/core/routing/initial_candidates.go` - selector candidate listing with parallel-arm flags.
- `internal/standardplugins/features_install.go` - the production terminal-decision provider contribution (Agent Loop Guard).
- `internal/core/runtime/attempt_session.go`, `turn_terminal.go` - retirement, lifecycle cleanup, turn-scoped facts.
- `internal/core/config/model.go`, `internal/infra/runtimebundle/build_executor.go` - typed config and generation wiring.
- `.kiro/steering/product.md`, `.kiro/steering/delivery.md`, `VISION.md` - ownership, slice budgets, priorities.
