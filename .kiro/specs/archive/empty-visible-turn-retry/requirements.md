# Requirements Document

## Project Description (Input)
Coding agents and CLI clients stall when a surviving backend attempt ends on a clean
terminal with zero visible output: the client receives a completion with nothing to act
on. Issue #1197 asks for an operator-opt-in, bounded corrective retry for exactly that
shape. The observability-only half already shipped (`lip_empty_visible_turn_total`,
#1196, commit `0e332869d`): it detects the shape but deliberately retries nothing. This
spec delivers the retry: at most one corrective attempt per logical turn, started before
any canonical client-visible frame of the degenerate attempt is released, reusing the
existing replacement, attempt-slot, terminal, and accounting machinery.

## Introduction
The proxy already knows how to replace an attempt before any client-visible frame is
released: retry/failover does it for recoverable pre-output failures, and the tool-name
correction withholds output for a hidden follow-up. What is missing is a trigger for the
degenerate *success* shape - a clean completion with no visible content - which today is
released to the client as an empty turn.

The retry is a client-visible behavior, so it is opt-in, off by default, and pinned to the
configuration generation that admits the turn. To keep the existing guarantee that no
transparent retry happens after client-visible output, the proxy must release *no*
canonical frame of an attempt that can still be corrected - not merely no answer text.
That is stronger than the existing produced-output facts, which deliberately classify
reasoning-only output as empty, so this slice adds a bounded prefix hold and a
released-frame guard instead of loosening the classification.

## V1 Slice
- **Delivers**: With the gate enabled, a clean empty visible turn is replaced once by a
  corrective attempt before anything reaches the client; if the corrective attempt
  produces content, the client sees only that content; if it is empty again, the client
  sees its normal empty finish. With the gate disabled, nothing changes.
- **First consumer**: Agent harnesses and CLI clients running against operators who enable
  the gate, on the primary canonical execution path.
- **Safe degradation**: Every decline is fail-open: the empty finish is delivered
  unchanged with one bounded reason logged. When an accepted corrective replacement cannot
  be opened, the turn ends with an explicit, recoverable upstream failure instead of the
  empty finish (owner-approved). Justification under `VISION.md`: an empty finish stalls the
  agent loop silently, which is the hidden degradation line 3 forbids; an explicit failure is
  honest, observable, and lets clients apply their own retry, which serves session
  continuity (priority 1). The rails pre-check the common open-failure causes, so this
  outcome is rare. Open-first publication that keeps the original empty finish is Deferred.

## Deferred
- Open-first corrective publication that preserves the original empty finish when the
  successor cannot be opened (#1236) — follow-up: needs its own publication-ordering slice (an
  overlay-free, non-committing variant of the continuation transaction); V1 keeps the
  existing replacement ordering, where retirement precedes opening.
- Corrective retry on the large-body/wire execution path (#1237) — follow-up: secondary-path
  parity once the primary path shows the signal moving.
- Corrective retry inside hidden thinker legs (#1238) — follow-up: the hidden leg is not a client
  turn; the client-visible executor leg owns the finish.
- Correction of truncated and refusal terminals (for example length-limit continuation) (#1239)
  — follow-up: different terminal taxonomy needs its own policy and budgets.
- Operator-tunable hold bounds and per-backend corrective budgets (#1240) — follow-up:
  measure first with the bounded decision counter.
- Correcting output that is non-empty but unusable (repetition, refusals with content) (#1241)
  — follow-up: needs an output-quality signal, not an empty-turn signal.
- Permissive coexistence with a wired terminal-decision provider (for example allowing the
  correction under Agent Loop Guard) (#1242) — follow-up: needs a frozen, preflight-stable
  provider input or a provider-visible correction request; V1 declines whenever a provider is wired.

## Boundary Context
- **In scope**: The opt-in gate; the correction trigger and its rails; the bounded prefix
  hold and the released-frame guard; the single corrective replacement through existing
  admission/budget/deadline machinery; discard semantics and usage-evidence retention for
  the replaced empty attempt; the bounded decision record and counter.
- **Out of scope**: The empty-visible-turn classification and its existing metric (they
  keep their meaning), the tool-name correction and its continuation transaction, steering
  overlays and conversation mutation, the terminal-decision provider contract, routing
  rules, and all pricing/rating/settlement logic.
- **Adjacent expectations**: The terminal-decision provider remains the exclusive terminal
  authority and is not preempted. Billing and usage keep their existing ports: the
  discarded attempt is retired exactly as an existing non-surfacing attempt, and no new
  money logic is introduced. Existing no-retry-after-output guarantees are strengthened,
  never weakened.
- **Boundary ownership**: Core runtime (receive loop, turn terminal, attempt lifecycle) plus
  config/wiring in the composition root. No new `pkg/lipapi` or `pkg/lipsdk` surface.
- **Optional hexagonal lens**: Domain policy and app orchestration in core runtime decision
  helpers; driven adapters are the configuration loader and the Prometheus sink; the
  composition root is the runtime bundle that pins the gate into the generation.
- **Revalidation triggers**: Receive-loop release ordering; the terminal chokepoint;
  replacement/admission ordering; attempt-slot publication; output-commitment rules; the
  optional metrics sink interfaces.

## Requirements

### Requirement 1: Opt-in, generation-pinned corrective retry
**Objective:** As an operator, I want the corrective retry to be off unless I enable it,
so that default deployments behave exactly as they do today.

#### Acceptance Criteria
1. The proxy shall keep the corrective retry disabled unless an operator explicitly enables it.
2. While the corrective retry is disabled, the proxy shall produce client-visible events, usage handling, and observable signals identical to a deployment without the feature.
3. When a configuration generation has the corrective retry enabled, the proxy shall apply it only to turns admitted under that generation.

### Requirement 2: The correction applies to exactly one shape
**Objective:** As an operator, I want the retry to fire only on the degenerate empty turn,
so that it can never interfere with a turn that already produced something.

#### Acceptance Criteria
1. When a surviving attempt's accepted finish is a clean completion and that attempt produced no visible output, the proxy shall classify the turn as an empty visible turn, counting reasoning-only output as no visible output.
2. If the accepted finish is truncated, a refusal, an error, a cancellation, or otherwise not a clean completion, then the proxy shall never attempt a corrective retry.
3. If any canonical client-visible frame of the logical turn has already been released - including response lifecycle, usage, and reasoning frames - then the proxy shall deliver the finish unchanged and shall not attempt a corrective retry.
4. Where the empty finish belongs to a hidden thinker leg, a large-body/wire turn, a completion-gate turn, a turn already bound to a tool-name correction, or a route that can start a parallel race, the proxy shall not attempt a corrective retry, shall leave the existing owner's handling of that finish unchanged, and shall record one bounded reason.

### Requirement 3: Nothing client-visible is released before the correction
**Objective:** As an operator, I want the correction decided before the client sees any
frame, so that a corrected turn looks like one ordinary response.

#### Acceptance Criteria
1. While the corrective retry is enabled and the turn can still be corrected, the proxy shall hold the attempt's produced canonical client-visible frames instead of releasing them, within a bounded number of events and bytes.
2. When a held attempt produces its first visible output, the proxy shall release the held frames followed by that output in their original order, and shall stop correcting that turn.
3. If the next frame would exceed the bound - the held event count is already at its bound, or the held bytes plus that frame would exceed the byte bound - then the proxy shall release all held frames unchanged, followed by that frame, and shall stop correcting that turn.
4. When the proxy delivers a finish it did not correct, the proxy shall release the held frames in their original order before delivering that finish.
5. If the turn is canceled or closed, or the attempt is replaced or disposed, then the proxy shall discard the held frames without releasing them.
6. The proxy shall observe each produced frame's backend-side accounting exactly once, and shall transform and record each released frame exactly once at release.

### Requirement 4: One corrective attempt through the existing machinery
**Objective:** As an operator, I want the retry to be a single bounded replacement inside
existing budgets, so that it cannot multiply attempts, cost, or admission.

#### Acceptance Criteria
1. The proxy shall attempt at most one corrective retry per logical turn, shall consume that allowance before the corrective attempt is opened, and shall not restore it for a replacement or continued attempt.
2. When the proxy attempts a corrective retry, the proxy shall perform exactly one corrective attempt-open through the existing replacement admission and publication machinery within the existing attempt, routing, admission, and deadline budgets - never a corrective re-plan loop - reusing existing candidate selection, including retrying the only candidate of a single-candidate route, and without excluding the empty candidate.
3. The proxy shall not inject corrective instructions, overlays, or conversation turns, and shall not treat the corrective retry itself as committed client output.
4. When the proxy discards an empty attempt for a corrective retry, the proxy shall release none of that attempt's frames or usage to the client and shall retain its usage evidence for operator accounting.
5. If the corrective attempt ends as another clean empty visible turn, then the proxy shall deliver its finish as the accepted turn outcome and shall not attempt a further retry.
6. If the corrective attempt cannot be opened, then the proxy shall end the turn with an explicit upstream failure classified as recoverable and pre-output, release no held frame, and shall not attempt a further corrective retry.
7. Where a generation has a terminal-decision provider wired, the proxy shall leave that provider's operation unchanged and shall not attempt a corrective retry.

### Requirement 5: Honest observability and accounting
**Objective:** As an operator, I want to see what the retry did without changing existing
signals, so that I can verify it from metrics and logs alone.

#### Acceptance Criteria
1. The proxy shall keep `lip_empty_visible_turn_total{backend}` as the accepted-winner signal, incrementing it only for the attempt whose finish the client receives.
2. When the corrective retry is enabled and an empty visible turn is detected, the proxy shall record exactly one bounded decision record for that turn: the bounded decline reason, or, when a corrective retry was attempted, the single outcome `corrected` or `failed` once that attempt is published or has failed.
3. The proxy shall count corrective retry decisions on a bounded counter labelled by backend and a closed outcome set.
4. The proxy shall record no message content, reasoning text, instructions, or model identifiers in those decision records and counters.
