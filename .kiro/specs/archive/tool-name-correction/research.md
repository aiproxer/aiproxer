# Research & Design Decisions

## Summary
- **Feature**: `tool-name-correction`
- **Discovery Scope**: Extension (brownfield). Light discovery plus a gap analysis against `main` @ `16d8263`; no external dependencies.
- **Key Findings**:
  - The effective tool catalog is already closed and handed to every tool-call finalizer. Frontends reject tool types that are not functions instead of dropping them (`internal/plugins/frontends/openairesponses/decode.go:604`, `openresponses/profile.go:1323`). The assembler passes the post-filter `baseline.Tools` to `Finalize` (`internal/core/runtime/tool_call_assembler.go:530-545`).
  - Hidden steering plus a second B-leg on the same client response already exists as one core transaction (`runContinuationTransaction`, `internal/core/runtime/terminal_decision_continuation.go`), driven today only by a terminal-decision `Continue`.
  - The `B2` request is projected from ingress plus the steering overlay, not from `B1` output. So the invalid name never reaches `B2` either, and no extra filtering is needed.

## Gap Analysis

### Requirement-to-Asset Map
| Req | Existing asset | Gap |
|---|---|---|
| 1.1 | Feature rows are explicit opt-in (`internal/standardplugins/standard_table.go`); no injection unless a feature adds an `Ensure*` step | **Missing**: new feature registration only (no injection) |
| 1.2, 1.3 | Finalizer chain gets `(call, tool, catalog)`; chain order by `Order()`; tool-call repair is order 40, path virtualization 41; the name is updated after a rewrite, so later finalizers see the repaired name | **Missing**: feature finalizer at order 45 |
| 1.4 | `newToolCallAssembler` is disabled when the catalog is empty (`tool_call_assembler.go:131-160`) | None |
| 1.5 | YAML decode pattern in `toolcallrepair/config.go` | **Missing**: feature config |
| 2.1 | Assembler holds each call until `tool_call_finished`. `prepareRecvEvent` (`response_pipeline_observations.go:67`) is the single per-event seam ahead of the assembler, called once in the receive loop (`executor_recv_loop.go:853`) | **Missing**: attempt-local withhold queue and dispatch divert |
| 2.2, 2.3 | `runContinuationTransaction` (steering `Put`, projection, admission, publication, `markContinuationLifecyclePending`, B1 settled as swallowed); non-streaming collects the canonical stream | **Missing**: a second trigger site that synthesizes a `ContinuationIntent` |
| 2.4 | B2 projection excludes B1 output; the overlay is backend-only | None |
| 2.5 | `deactivateContinuationOverlay` runs on every final terminal (`turn_terminal.go`) | **Missing**: deactivate the dedicated correction overlay next to it |
| 3.1, 3.2 | No per-turn correction counter | **Missing**: turn-scoped counter on `turnTerminal` |
| 3.3, 3.4 | Commit is marked at several sites, turn-wide, not per kind | **Missing**: attempt-local released-output facts split into reasoning vs. other visible output |
| 3.5 | `lipapi.NormalizedItems(call)` exposes `ItemKindToolCall` items with `ToolCall.Name` | **Missing**: history scan (core rail) |
| 3.6 | `turnTerminal.supportsContinuation`, `steeringWriterFactory`, `conversationReader` | **Missing**: eligibility check only |
| 3.7 | Assembler drain popped inside the loop and dispatched through `dispatchClientFacingEvent` (`executor_recv_loop.go:669`) | **Missing**: ordered replay of `[queue..., finish]` onto the assembler drain |
| 4.x | None | **Missing**: feature-owned message builder |
| 5.x | Terminal-decision observations log bounded attrs (`emitTerminalDecisionObservation`); repair diagnostics are hash-only (`repair/diagnostic.go`) | **Missing**: one bounded log record per core decision |

### Constraints
- **Exclusive terminal-decision slot**: the terminal-decision provider slot is exclusive and held by Agent Loop Guard (AGENTS.md). Correction must not become a `terminaldecision.Provider`.
- **Closed finalizer vocabulary**: `toolcall.Action` is a closed vocabulary. An unknown action already maps to `unusableResult` (exact originals), so an additive action degrades safely on an older core.
- **Finish call sites**: the finish arrives at `finalizeResponseFinishedAuthority` from four call sites (`executor_recv_loop.go:251, 347, 610, 706`). The authority function is the only common chokepoint.
- **Continuation failure ends the request**: a pre-publication continuation failure claims the request terminal itself (`continuationPrePublicationFailureWithOverlay` calls `claimRequestTerminal` and `finishResponse`). Fail-open release (3.7) is therefore impossible through the unmodified transaction. The correction path needs a failure policy that, except on cancellation, only deactivates the overlay and returns a typed "not published" result to its caller.
- **Finalizer inputs**: finalizers receive no conversation history (`toolcall.Meta` carries identity and views only). So the history rail belongs in core, or `Meta` would need to grow.

### Options
| Option | Description | Trade-offs |
|---|---|---|
| A. Extend existing seams (recommended) | Additive `ActionSteer` on finalizers; core eligibility gate invoked synchronously by the assembler; produced-output facts and withhold divert around `prepareRecvEvent`; finish intercept at its single call site calling the existing transaction with fail-open and preserve-lineage policies | ✅ reuses the publication boundary, overlay lifecycle, billing settlement, lifecycle-marker suppression ❌ touches four core files |
| B. New terminal-decision provider | Implement correction as a provider | ❌ violates the exclusive slot; ❌ decides only at terminal, so the invalid call is already released |
| C. Core-only detection | Core compares names with the catalog itself | ✅ no SDK change ❌ message text and policy in core (AGENTS: mutation behind extensions) |
| D. Completion gate | Buffer the whole response and replace it | ❌ gates cannot open a B-leg; ❌ buffering breaks streaming-first |

- **Effort**: M (3–5 days). New patterns are small; the riskiest part is the fail-open release ordering at the finish chokepoint.
- **Risk**: Medium. It touches B2BUA continuation, but through an existing transaction that is already tested for publication, settlement, and overlay cleanup.

### Research Needed (carried into tasks)
- (Resolved in validation round 1) The recovery drain is popped before the assembler drain, so the design no longer re-queues through the recovery drain. Replay uses the assembler drain only.
- (Resolved in validation round 1) Lineage must be preserved, not advanced (see Design Decisions).
- Confirm during implementation that a pre-output replacement of B2 keeps the projected correction message (pinned facts), so a B2 failover still carries the instruction.

## Research Log

### Catalog completeness
- **Context**: Issue blocker: can the proxy know the agent's tools?
- **Findings**: Every supported client protocol is stateless and re-sends `tools` per request. Non-function tool types are rejected at decode. The finalizer `catalog` is the post-filter list the backend model saw. Client-side on-demand loading (deferred tools announced only in prompt text) looks "unknown". The history rail (3.5) and the budget (3.1–3.2) let those calls through after one attempt.
- **Implications**: Detection needs no new data source.

### Continuation transaction reuse
- **Sources**: `terminal_decision_continuation.go`, `turn_terminal.go:410-460`, `executor_settlement.go:275-305`.
- **Findings**: `finalizeResponseFinishedAuthority` already handles `Continue` by `p.discardPendingCompletion()` and then `hooks.continuation(ctx, intent)`, returning `errTerminalDecisionContinuationPublished` that all four call sites handle. The intent requires `TrajectoryRef`, `Provenance`, `ReasonCode`, and `Instruction` ≤ 4096 bytes (`terminaldecision.MaxInstructionBytes`).
- **Implications**: The correction synthesizes an intent and calls the same hook before consulting the provider. B2's own finish then goes through the normal provider path, so Agent Loop Guard still sees the corrected attempt.

### Visible-output accounting
- **Findings**: `lipapi.OutputCommitted` counts text, reasoning (delta, opaque, part), tool start/args, decision result, and image/file refs. Per-attempt tracking does not exist. `markOutputCommittedForAttempt` is the release-time hook that already receives the attempt.
- **Implications**: Add two attempt-local booleans set at that hook: `releasedVisible` (non-reasoning committed kinds) and `releasedReasoning`.

## Design Decisions

### Decision: Additive finalizer action instead of a new plane or provider
- **Alternatives**: new plane; terminal-decision provider; core-only detection (see Options).
- **Selected**: `toolcall.ActionSteer` with `Result.Steer *toolcall.SteerRequest{Instruction, TurnBudget, AllowAfterReasoning}`.
- **Rationale**: Smallest public surface. Older cores treat it as unusable and release originals. It keeps the policy and message text in the feature.
- **Trade-offs**: The per-turn budget and the reasoning flag travel with each request rather than as a generation setting. That is acceptable because the values are constant per generation.

### Decision: Core-owned safety rails
- **Selected**: Core decides budget, visible output, reasoning, history repeat, and continuation support. The feature decides "is this name invalid" and the message.
- **Rationale**: Rails depend on attempt and turn state that only core observes. A feature cannot be allowed to bypass them (anti-loop and anti-duplication guarantees).

### Decision: Dedicated correction overlay and preserved lineage
- **Context**: Design validation found that the shared `continuationOverlayID` would replace an active Agent Loop Guard instruction. It also found that `nextContinuationIntentFacts` always increments the attempt and overwrites `controlRef` (`terminal_decision_continuation.go:194-207`). Agent Loop Guard would then decode B2's empty control reference as invalid progress (`agentloopguard/provider.go:142-145, 228-233`).
- **Selected**: Correction uses its own overlay ID `tool-name-correction`, deactivated next to the existing one at the final terminal, plus a preserve-lineage policy on the transaction, so B2 inherits B1's continuation facts unchanged.
- **Rationale**: The correction is invisible to the guard. B2 is judged exactly as B1 would have been.
- **Validation round 2**: when B1's lineage is unset, the evidence code derives it from the *current* attempt (`terminal_decision_evidence.go:50-71`). Preserving "unset" would make B2 (Seq > 1) decode as invalid progress (`agentloopguard/provider.go:236-241`). So the policy stores B1's derived lineage as set facts. Round 2 also added two cases: an ordered replay on EOF without a finish (`handleEOF` synthesizes one, `executor_recv_loop.go:405-415`) and on a later finalizer `drainErr` (`:676`).

### Decision: Decide at call completion, continue at B1's backend finish, before dispatch
- **Context**: Validation round 1 found three problems with the first draft, which hooked the correction into `finalizeResponseFinishedAuthority`:
  - the finish is recorded and observed before the authority runs (`executor_recv_loop.go:326-336`);
  - the recovery drain is popped before the assembler drain (`:592` vs `:669`);
  - the gated route discards the evaluated gate candidate.

  So that fail-open replay would reorder events or record them twice.
- **Selected**: Intercept the backend `response_finished` at the single `prepareRecvEvent` call site (`executor_recv_loop.go:853`), before `dispatchClientFacingEvent`, which holds every finish route (`:219-389`). An accepted steer no longer swallows anything. The chain continues as a pass, and every event the attempt would dispatch is diverted into a bounded ordered queue. Fail-open pushes `[queue..., finish]` onto the assembler drain, which is popped at the top of the loop and dispatched through the same `dispatchClientFacingEvent`.
- **Rationale**: Withheld events and the finish then traverse recording, gates, and the authority exactly once, in order. Later mandatory finalizers still run because the chain is not cut. The finish authority stays untouched.
- **Trade-off**: Correction is declined when completion gates are present for the request. Gates buffer the response, so the divert cannot see a released order.

### Decision: Produced, not released, output facts
- **Context**: Validation found several commit sites beyond `markOutputCommittedForAttempt` (`executor_recv_loop.go:200, 271, 734`, `interleaved_stream.go:470`). Text held by a non-live gate is never "released".
- **Selected**: Record produced-output kinds in `prepareRecvEvent` before the assembler. This is one seam and route-independent. The decided call's own tool events do not count.

### Decision: Fail-open continuation failure policy
- **Context**: The existing transaction finalizes the request on any pre-publication failure, which is right for Agent Loop Guard (B1 already delivered its output) but would drop the withheld call here.
- **Alternatives**: (1) pre-check every admission prerequisite before withholding, which cannot cover routing or billing denials; (2) parameterize the transaction's pre-publication failure handler.
- **Selected**: (2). The transaction gains a failure-policy parameter. The existing terminal-decision caller keeps today's handler. The correction caller uses one that deactivates the overlay and returns `errToolCorrectionNotPublished`, except for cancellation and deadline, which keep the existing claim path.
- **Follow-up**: The test asserts that admission-side resources (`ready.Dispose`) are released on the not-published path exactly as today.

### Decision: Reasoning prefix is eligible by default
- **Context**: Reasoning models emit reasoning before almost every tool call. If reasoning blocked correction, V1 would rarely fire for them.
- **Selected**: Default `correct_after_reasoning: true`. Operators can turn it off (3.4) if a client mishandles two reasoning blocks in one response.

### Decision: Default budget of one correction per turn, maximum three
- **Rationale**: Each correction costs another full prompt. A model that misses the same name after an explicit list rarely recovers on a third try. Passing the call through then lets the harness's own unknown-tool error (or on-demand loader) act.

## Synthesis
- **Generalization**: `ActionSteer` is generic ("withhold this completed call and continue hidden with this instruction"). The deferred schema-invalid-arguments trigger reuses it unchanged.
- **Build vs. adopt**: Everything is adopted from existing seams. Only the eligibility rail, the withhold flag, and the fallback release are new.
- **Simplification**:
  - no new overlay ID;
  - no new plane, store, or persisted state;
  - no mid-stream cancel;
  - no closest-name ranking;
  - diagnostics are one log record, not metrics.

## Risks & Mitigations
- **Fail-open release ordering (3.7)**: withheld events must reach the client before the re-queued finish. Mitigation: an integration test asserts the exact client event order on a forced continuation-admission failure.
- **Duplicate reasoning blocks in one response**: possible client confusion. Mitigation: the `correct_after_reasoning` switch, and only one correction by default.
- **Hidden cost**: each correction is a second full backend request. Mitigation: opt-in, budget of one, and the decision log makes the rate visible.
- **Interaction with Agent Loop Guard**: mitigated by the dedicated overlay and preserved lineage; covered by one guard-enabled integration test.
- **Recovery timers while withholding**: idle/TTFT observation sees no client activity; tool-only turns end shortly after the call, and a timeout is handled as a recoverable B1 error.
- **Withhold memory**: bounded queue (256 events, 1 MiB); overflow degrades to ordered pass-through.

## References
- [Issue #997](https://github.com/matdev83/go-llm-interactive-proxy/issues/997): feature request and research notes.
- `docs/adr/0007-canonical-tool-call-repair.md`: finalizer seam, name normalization, fail-open rules.
- `.kiro/steering/routing-and-orchestration.md`: output commitment and recovery rules.
