# Implementation Plan

## Execution Rules

- Follow TDD: characterization and architecture RED tests precede production ownership moves.
- Keep every task independently reviewable and limited to no more than five concrete actions.
- Preserve `lipapi.EventStream`, routing/retry behavior, B2BUA lifecycle, billing, usage authority, metering, secure-session, interleaved-thinking, prompt-cache, tool, observer, and protocol semantics.
- Move ownership and delete the old state in the same phase; do not maintain long-lived dual-write compatibility.
- Prefer package-private concrete collaborators. Do not introduce a universal mutable turn object, broad service bag, DI container, actor/event framework, reflection dispatcher, or interface-per-type abstraction.
- The current upstream `preparedRequest` / `routePlanState` / `attemptOpenParams` pipeline remains outside this implementation except for the narrow replacement-open adapter required by D10.
- A negative line-count delta is supporting evidence, not the objective. The final gate is fewer responsibility clusters, fewer cross-domain receiver methods, fewer implicit state handoffs, and clearer synchronization ownership.

## Phase 1 — Freeze Behavior, Ownership, and Concurrency

### Task 1.1 — Build the recv/terminal ownership and coupling baseline

- Add a deterministic inventory of every direct `retryRecvStream` field and synchronization primitive, classifying each as immutable request fact, current-attempt state, recovery state, response-pipeline state, request-terminal state, or adapter infrastructure.
- Add an AST/reporting fixture that counts direct façade fields by responsibility, `retryRecvStream` receiver methods by responsibility, direct domain-package fan-out, broad `*Executor` reachability, and explicit stream-assembly/replacement state-copy assignments.
- Record the current duplicate/mirrored authorities for commitment, finished state, attempt terminal reset, current attempt identity, and request facts recovered from context.
- Add RED target expectations expressing the desired final ownership topology without using one-file LOC as the primary gate.
- Check in the baseline evidence in a machine-readable form suitable for final before/after comparison.

_Requirements: 1.1–1.2, 1.8–1.10, 11.1–11.3, 11.6, 11.12_

_Design: D15, D16, D17_

_Validation: the current distributed God-object topology is measured deterministically and the target architecture assertions are RED._

### Task 1.2 — Freeze core Recv, Close, failure, and terminal behavior

- Add table-driven characterization for normal streaming, EOF, explicit Close, caller cancel, A-leg cancel, timeout/TTFT/idle timeout, backend panic mapping, and fatal pre/post-commit errors.
- Add deterministic pre-commit recoverable failure/replacement and replacement-exhaustion cases, pinning current error precedence and client-visible behavior.
- Add a scheduling test with one `Recv` blocked in backend I/O while `Close` competes, proving the current supported one-Recv-plus-concurrent-Close contract and exactly-once terminal result.
- Characterize detached bounded terminal work and loser terminal-claim behavior so cancellation does not cause duplicate or missing terminal effects.
- Capture current canonical event ordering/error vocabulary as behavior assertions rather than implementation-shape assertions.

_Requirements: 1.3, 1.7, 2.1–2.10, 8.7–8.11, 10.1–10.6_

_Design: D11–D14, D16_

_Validation: the fundamental EventStream/concurrency/terminal matrix is green against the current implementation and ready to guard extraction._

### Task 1.3 — Freeze response, usage, billing, security, and observer behavior

- Characterize completion-gate buffering/draining, recovery drain, mandatory client-facing preflight, synthesized usage before `response_finished`, and sideband usage before/during/after backend receive.
- Characterize provider/operator settlement evidence versus customer-visible reconstructed usage and ensure neither view is lost on finish/error/replacement.
- Characterize one billing leg usage record per B-leg, one BillingCallID closure per logical invocation, request/attempt authority settlement/release, and durable terminal-work progression.
- Characterize tool-call finalization/classification, prompt-cache observation, secure-session event recording/failure policy, final-stream observers, traffic/usage observations, and compaction observations.
- Add a concurrent terminal-snapshot case proving response/customer evidence is coherent while the single Recv owner is mutating it and Close competes.

_Requirements: 1.4, 1.6, 7.1–7.11, 8.1–8.9, 9.1–9.8, 10.5–10.7_

_Design: D4–D8, D11, D14, D16_

_Validation: all cross-domain terminal/evidence behaviors that currently depend on `retryRecvStream` state are pinned before ownership moves._

### Task 1.4 — Freeze replacement pinning and interleaved continuity

- Add bare-context `Recv` tests proving request-bound exec/session facts, metering/request authority, billing identity, route preferences, and model registry/catalog/native-model views survive generation refresh and recv-phase replacement.
- Add hidden and visible interleaved-thinking cases covering A-leg hold/end ownership, thinker/executor continuation, cycle cursor, memo visibility/injection, and replacement behavior.
- Add replacement tests proving swallowed-attempt authority is finalized/released before replacement admission and that B-leg logging/evidence remains attached to the correct attempt.
- Characterize attempt-local tool/prompt-cache reset versus request/recovery interleaved state carryover across replacement.
- Pin sticky affinity and preferred-candidate behavior required by recv-phase replacement without changing selector grammar.

_Requirements: 1.5, 4.1–4.10, 5.5, 5.9–5.11, 6.3–6.10, 8.10, 9.4–9.11_

_Design: D1–D3, D7–D10, D14, D16_

_Validation: request pinning, replacement ordering, attempt-local reset, and interleaved outer ownership are behaviorally frozen._

## Phase 2 — Establish Immutable Facts and Current-Attempt Ownership

### Task 2.1 — Introduce immutable receive-turn facts and context projection

- Add package-private `recvTurnFacts` (or equivalent) containing the cloned baseline request, trace/A-leg identity, pinned exec/session/model views, route preferences, secure-turn identity, and stable owner references required after assembly.
- Move request-bound model/catalog/native-model capture into construction of this immutable facts boundary and prove it cannot consult a new live generation during replacement.
- Replace business reliance on arbitrary caller `Recv` context with an explicit context projector from facts while preserving tracing, cancellation, diagnostics, and SDK extension propagation.
- Remove duplicate independently mutable request-fact copies from the stream façade as each facts field becomes authoritative.
- Make Task 1.4 bare-context/generation-pinning tests green with no new public DTO or mutable turn bag.

_Requirements: 3.1, 3.4, 3.6–3.10, 4.1–4.10, 9.4, 9.8, 10.8_

_Design: D1, D12, D13_

_Validation: one immutable private facts value is the request-lifetime authority for recv-phase pinned business facts._

### Task 2.2 — Introduce one current-B-leg `AttemptSession`

- Add a package-private attempt owner containing the active managed backend stream, B-leg, candidate, attempt authority, attempt terminal owner, and attempt identity needed for logging/settlement.
- Move attempt terminal initialization from request-lifetime stream state into attempt construction and eliminate lazy/reset semantics that exist only because attempt ownership was flattened.
- Give the attempt owner idempotent terminal/cancel/close operations that preserve current outcome logging, sideband usage draining, and authority finalization behavior.
- Update initial stream assembly to construct one attempt owner from `attemptOpenResult` rather than copying B-leg/candidate/authority/inner fields onto the façade.
- Migrate affected tests to focused fixtures/builders so production owners can enforce valid initialization invariants.

_Requirements: 3.3–3.5, 3.9, 5.1–5.2, 5.5–5.10, 8.4, 10.1–10.4_

_Design: D2, D11, D13, D17_

_Validation: one opened B-leg maps to one coherent attempt owner and no request-lifetime type owns a resettable attempt terminal._

### Task 2.3 — Move attempt-local accounting, tool, prompt-cache, and observation state

- Move attempt accounting and provider-side usage consumption that is intrinsically B-leg scoped behind the attempt owner while preserving request-level/customer evidence handoff.
- Move the per-B-leg tool-call assembler/finalizer state to the attempt owner and preserve existing bounds/finalizer/reset semantics.
- Move prompt-cache source/controller selection to attempt construction so replacement cannot retain a controller/source from the old backend.
- Place any final-observation state proven attempt-local with the attempt owner while leaving logical-stream observers in the response pipeline for Phase 4.
- Add replacement tests proving old attempt-local state is finalized/discarded and the replacement receives state derived only from its own candidate/backend.

_Requirements: 5.3, 5.8–5.11, 7.4, 7.6–7.7, 9.6–9.7, 9.10_

_Design: D2, D7, D8, D14_

_Validation: attempt-local state follows the B-leg lifetime and cannot leak across replacement._

### Task 2.4 — Add coherent current-attempt snapshot/swap and retire scattered attempt fields

- Introduce a minimal attempt slot/snapshot mechanism that atomically identifies the current `AttemptSession` for Recv/Close without holding synchronization across backend I/O or terminal effects.
- Convert recv-phase replacement to terminalize/finalize the snapshotted prior attempt before constructing and atomically installing the replacement according to current authority ordering.
- Add scheduling tests for Close racing replacement, a caller retaining an old attempt snapshot while a replacement publishes, and sideband usage arriving during retirement.
- Delete the façade's old `inner`, B-leg, candidate, authority, attempt-terminal, attempt-local tool/prompt-cache, and associated reset/mutex fields once the slot/session is authoritative.
- Make Phase 1 replacement/Close behavior green under targeted race testing.

_Requirements: 3.1–3.5, 5.4–5.10, 10.1–10.5, 10.9–10.10, 11.4_

_Design: D2, D11, D13, D17_

_Validation: attempt replacement is one coherent owner swap with no half-old/half-new mutable stream state._

## Phase 3 — Establish Logical Request Terminal Ownership

### Task 3.1 — Introduce `TurnTerminal` and one output-commit authority

- Add a package-private request-lifetime terminal owner reusing the existing request-scope `streamTerminal`/`terminal.Owner` mechanics rather than introducing a new generic state machine.
- Move output commitment into this owner as one idempotent authority and provide narrow query/mark operations for response and recovery paths.
- Compose request terminal commands with the currently snapshotted attempt's own attempt terminal according to existing `sdkterminal.Command` scope rules.
- Preserve losing-claim wait/publication, gate-replacement rejection, panic/error handling, and durable-work state progression.
- Delete the façade's duplicate `committed`, request terminal, and related terminal-result state as the new owner becomes authoritative.

_Requirements: 8.1–8.4, 8.7–8.9, 8.11–8.12, 10.5, 11.4_

_Design: D5, D11, D13, D14, D17_

_Validation: commitment and request terminal outcome each have exactly one request-lifetime authority while attempt terminals remain replaceable attempt state._

### Task 3.2 — Move request authority, metering finalization, and billing-call closure behind terminal ownership

- Route request-authority terminalization/release and request-level metering finalization through `TurnTerminal` using the stable admitted owner references from receive-turn facts.
- Move BillingCallID closure/request-level billing identity handling out of the stream façade while preserving immutable pricing/policy references captured at admission.
- Keep per-B-leg billing recording associated with the relevant attempt and retain dedupe when request/attempt terminal effects race or overlap.
- Preserve provider/operator versus customer-visible usage evidence and all current prepaid/postpaid/durable terminal-work safety semantics.
- Delete obsolete billing-call closure, request-authority, metering-terminal, and request-finished forwarding state/methods from the façade.

_Requirements: 3.3, 7.4, 8.5–8.9, 9.4–9.8, 10.7, 11.4_

_Design: D4, D5, D14, D17_

_Validation: one logical request terminal owner coordinates request-level economic closure without absorbing attempt-level leg ownership._

### Task 3.3 — Make A-leg end and interleaved outer ownership explicit

- Move A-leg end-once coordination behind `TurnTerminal` and represent whether base terminal or an outer interleaved wrapper owns final A-leg end as an explicit ownership mode/strategy.
- Preserve hidden/visible thinker wrappers that hold the A-leg through combined thinker/executor continuation and release it exactly once at the existing semantic boundary.
- Ensure cancellation/timeout/error/normal-finish paths all use the explicit end authority rather than scattered `holdALegEnd`/`endOnce` checks.
- Preserve keep-warm session/foreground interactions without making keep-warm an owner of terminal truth.
- Delete old façade A-leg end flags/once guards once all Phase 1 interleaved/cancellation tests are green.

_Requirements: 8.5, 8.10–8.11, 9.9, 9.11, 10.5, 11.4_

_Design: D5, D9, D11, D14, D17_

_Validation: A-leg end has one explicit owner and interleaved outer coordination remains behaviorally identical._

### Task 3.4 — Consolidate terminal entry points and remove terminal forwarding from the façade

- Replace stream-level terminal helper families with narrow terminal-owner operations for normal finish, cancel, timeout, partial error, swallowed attempt, and committed gate-replacement cases.
- Make terminal snapshots consume coherent evidence from response/attempt owners through explicit snapshot APIs rather than reading arbitrary façade fields.
- Remove obsolete `runStreamTerminal`, request/attempt terminal reset plumbing, direct billing handoff callbacks, and duplicate finished/terminal checks from the façade when no longer authoritative.
- Add focused tests proving every terminal command executes effects once and maps loser/error outcomes exactly as the Phase 1 baseline.
- Re-run targeted billing/usage-authority/terminal-work suites before proceeding to recovery/response extraction.

_Requirements: 3.5, 8.3–8.12, 9.4–9.8, 10.3–10.7, 11.4, 11.7_

_Design: D5, D11, D13, D14, D16, D17_

_Validation: terminal mechanics/effects no longer depend on a shared stream state bag and all exactly-once contracts remain green._

## Phase 4 — Extract Recovery and Response State

### Task 4.1 — Introduce the recovery controller and localized replacement-open adapter

- Move selector/session state, exclusions, retry/TTFT budgets, request-size estimate, affinity identity, rejection/admission diagnostics, context-limit state, transform exclusions, and last parallel failure into one recovery owner.
- Move retry/interleaved cycle/suppression state that genuinely persists across attempts into that owner while keeping attempt-local role/effects with `AttemptSession`.
- Introduce the D10 narrow replacement-open request/result adapter around the current upstream candidate-open path; localize any temporary translation to existing `attemptOpenParams` there.
- Make recovery query `TurnTerminal` for commitment and invoke attempt retirement/replacement through explicit operations rather than direct façade fields.
- Delete the corresponding flat recovery/routing fields from the façade as Phase 1 replacement/affinity/error tests become green.

_Requirements: 3.2–3.8, 6.1–6.7, 6.9–6.11, 9.9, 11.4, 11.11_

_Design: D3, D9, D10, D12, D17, D18_

_Validation: retry/failover state has one owner and the EventStream façade no longer reconstructs `attemptOpenParams` from its own fields._

### Task 4.2 — Preserve replacement error precedence and old-attempt retirement through the recovery owner

- Encode current no-eligible error precedence for transport rejection, admission failure, capability rejection, context-limit exhaustion, transform exclusion, and parallel failure in focused recovery tests and typed internal outcomes where useful.
- Preserve swallowed-attempt authority finalization/release before replacement admission and ensure recovery cannot bypass current-attempt terminalization.
- Preserve sticky affinity clearing/reselection, preferred candidate ordering, first-request state, deterministic RNG behavior, TTFT budget, and interleaved cycle advancement.
- Add replacement-exhaustion and committed/mandatory-recorder-failure cases proving recovery cannot open an illegal B-leg after request terminal/commit constraints apply.
- Keep selector parsing/grammar and `routing.ExpandFailoverGroups` semantics unchanged and explicitly out of this refactor.

_Requirements: 5.5, 6.4–6.11, 8.2, 8.8–8.9, 9.2–9.3, 11.8, 11.11_

_Design: D3, D6, D9, D10, D14, D18_

_Validation: all replacement decisions and error precedence match baseline without duplicating routing semantics._

### Task 4.3 — Introduce the response pipeline for event/evidence state

- Move customer-visible accumulation, seen/internal usage evidence dedupe, completion-gate buffers/drains, recovery drain, and coherent terminal evidence snapshots into one response owner.
- Centralize the existing client-facing event sequence for hooks/gates/preflight/usage synthesis/observation without reordering current semantics.
- Keep provider/operator settlement evidence distinct from customer-visible reconstructed usage and provide explicit final evidence to `TurnTerminal`.
- Ensure response owner mutations are single-Recv-owned while terminal/Close snapshots use a minimal coherent synchronization boundary with no cross-owner lock nesting.
- Delete migrated event/gate/usage accumulation fields and receiver helpers from the façade after normal/recovery/gate finish tests are green.

_Requirements: 3.3–3.5, 7.1–7.5, 7.9–7.11, 10.1–10.3, 10.6, 11.4_

_Design: D4, D12, D13, D17_

_Validation: client-event processing state has one cohesive owner and cannot independently terminalize the request._

### Task 4.4 — Move tool classification, secure recording, and logical-stream observations into the response boundary

- Move logical-stream tool classification/correlation/drain state into the response owner while using the active attempt's per-B-leg assembler/finalizer for attempt-local tool state.
- Route secure-session event recording through the response path and return typed outcomes to recovery/terminal policy without duplicating commitment or hard-stop truth.
- Move logical-stream final observer, traffic/usage observer, and compaction integration off the EventStream façade while preserving current failure policies and ordering.
- Verify prompt-cache observation remains attempt-local and that only emitted observations/evidence cross into request-level response/terminal owners.
- Delete the obsolete cross-domain `retryRecvStream` receiver methods/files or reduce them to owner-local implementation as appropriate.

_Requirements: 7.6–7.9, 9.1–9.3, 9.10, 11.4–11.7_

_Design: D4, D6–D8, D12, D17_

_Validation: tool/security/observer changes no longer require the EventStream façade to own their mutable domain state._

## Phase 5 — Shrink the Façade and Certify Structural Simplification

### Task 5.1 — Convert `retryRecvStream` into the final small EventStream façade

- Rewrite `Recv`/`Close` around immutable facts, current-attempt slot, recovery, response, and terminal owners while keeping the hot-path control flow explicit and readable.
- Remove the broad `*Executor` field from the final façade and give each collaborator only its required operations; retain the D10 opener adapter as the sole intentional upstream bridge.
- Remove context cache or isolate it as adapter-only infrastructure if profiling still justifies it; ensure no business authority depends on cached context.
- Remove remaining migrated cross-domain fields, nil/lazy initialization branches, and forwarding methods from the façade and migrate tests to valid owner fixtures.
- Confirm `Executor.Execute` remains a compact orchestration shell and does not absorb recv/terminal responsibilities.

_Requirements: 2.1–2.10, 3.1–3.10, 10.8, 11.4, 11.10–11.11_

_Design: D10–D13, D17, D18_

_Validation: the returned stream is behaviorally identical but structurally an adapter over cohesive owners, with no direct Executor service-locator dependency._

### Task 5.2 — Activate ownership, dependency, and deletion architecture ratchets

- Make the Phase 1 field/responsibility inventory gate green and assert exactly one authority for current attempt, attempt terminal, commitment, recovery diagnostics, response accumulation, and request terminal state.
- Add/activate AST gates preventing broad `*Executor` reachability from the façade, cross-domain mutable fields on it, universal state/service bags, and generic resolver/container patterns in the affected runtime path.
- Measure before/after cross-domain receiver methods, direct domain-package fan-out, synchronization primitives at the façade boundary, and state-copy assignments during stream assembly/replacement.
- Require net deletion in the affected production state-handoff/forwarding surface unless a final design review records a concrete invariant reduction justifying any growth.
- Delete temporary migration adapters/compatibility helpers not required by D10 and update architecture budgets/allowlists only by ratcheting them tighter or documenting a justified authority boundary.

_Requirements: 1.9–1.10, 3.6–3.10, 11.1–11.6, 11.9, 11.12_

_Design: D15, D17_

_Validation: structural metrics prove ownership simplification rather than code movement._

### Task 5.3 — Run concurrency, domain, parity, and full repository regression gates

- Run targeted race/scheduling tests for current-attempt swap, blocked Recv/Close, terminal claims, response snapshots, billing leg/call closure, sideband usage, and secure-recorder failure; add leak/deadlock coverage for any new waiting primitive.
- Run runtime executor/retry, routing, B2BUA, usage-authority/accounting, billing, secure-session, prompt-cache, interleaved-thinking, completion/tool, and extension observer suites.
- Run frontend/protocol conformance and parity suites proving canonical events, failover, cancellation, tool/usage output, and OpenResponses behavior are unchanged.
- Run repository formatting, vet/lint, architecture, quality, and supported race gates without weakening assertions or skips.
- Record any platform limitation accurately rather than claiming unrun evidence.

_Requirements: 2.1–2.10, 7.11, 9.1–9.12, 10.9–10.10, 11.7–11.8_

_Design: D14–D16_

_Validation: all affected domain/protocol/concurrency gates are green and no behavior regression is hidden by the ownership refactor._

### Task 5.4 — Perform final simplification review and hand off the upstream seam

- Compare the final ownership graph to the Phase 1 baseline and issue GO/NO-GO based on responsibility count, mutable ownership, dependency reachability, synchronization topology, state-copy reduction, affected production deletion, and test evidence.
- Remove any remaining wrapper/type that only renames old state, and reject a final universal `TurnContext`, broad services aggregate, duplicated terminal truth, or forwarding-only collaborator.
- Confirm public SDK/config/backend ABI, routing grammar, billing/security/interleaved domain architecture, and generation semantics remained unchanged.
- Document the single remaining D10 replacement-open seam and the facts/attempt contract that the chronologically following `request-attempt-pipeline-state-simplification` implementation may replace.
- Do not begin the upstream pipeline refactor until this tranche's ownership/race/parity gates are green; if the structural simplification gate is not met, re-scope before proceeding.

_Requirements: 1.10, 3.6–3.8, 11.5–11.12_

_Design: D15–D18_

_Validation: final review demonstrates real complexity reduction and leaves one explicit, bounded seam for tranche 2 rather than an unfinished half-refactor._
