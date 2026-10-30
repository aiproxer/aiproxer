# Brownfield Design Validation

## Verdict

**GO after ownership/lifetime corrections.** The design is materially simpler than the current `retryRecvStream` topology and can be implemented without changing public behavior, but the first design pass exposed several brownfield traps that would have reproduced the same coupling behind new type names. Those issues are now incorporated into the normative requirements and D1–D18 design rules.

The final design decomposes by **state lifetime and invariant**, not by source file or package name. In particular, request terminal ownership and output commitment span the logical A-leg; attempt terminal ownership follows the replaceable B-leg; response processing owns event state but not settlement; and retry state owns replacement decisions but not attempt resources. This is the core simplification that makes the design a GO rather than a file-level reshuffle.

Baseline reviewed: `main` at `c3b5c872e6e48b6b9c86ea3570530b4fb094767c`.

## Validation Round 1 — Findings and Disposition

### Attempt terminal ownership inside `TurnTerminal` — NO-GO / FIXED

A straightforward terminal extraction could have moved both request and attempt `streamTerminal` objects into `TurnTerminal`. That would preserve the current lifetime mismatch because the request spans the A-leg while the attempt is replaced with each B-leg.

**Correction:** D2/D5 place attempt terminal ownership inside `AttemptSession`. Request terminalization composes with the current attempt when a command covers both scopes, but a replacement creates a fresh attempt owner naturally. The logical request no longer needs `resetAttemptTerminal`.

### Multiple output-commit authorities — NO-GO / FIXED

Commitment affects recovery legality, secure-recorder failure policy, authority state and terminal error mapping. Splitting the current stream without first selecting one authority would invite mirrored booleans.

**Correction:** D5 makes `TurnTerminal` the one request-lifetime commitment authority. Response processing marks commitment through that owner; recovery queries it. Attempt authority may receive one-way notification but does not become a competing truth source.

### Bare `Recv` context could lose pinned request facts — NO-GO / FIXED

The current stream deliberately retains execution views, secure-turn state, metering/request-authority handles, route preferences and bound model/catalog views because later `Recv` callers may pass a bare context. Removing those fields without a replacement authority would silently make recv-phase behavior depend on live generation state.

**Correction:** D1 introduces immutable `recvTurnFacts`, and derived contexts project those authoritative facts outward for existing hooks/SDK seams. Bound model views remain request-pinned through replacement and reload.

### Recovery extraction could violate reservation ordering — NO-GO / FIXED

The existing recv replacement path releases/finalizes a swallowed attempt's authority before admitting its replacement. A recovery controller that opens first and retires the old attempt afterward would reintroduce overlapping reservations and possible quota/credit conflicts.

**Correction:** D2/D3 make prior-attempt terminalization part of the replacement protocol before the replacement becomes authoritative, preserving the current safety ordering.

### Response pipeline could become a new God object — NO-GO / FIXED

Because `response_finished` is where usage reconstruction, preflight and terminal effects meet, it is tempting to let the response pipeline own settlement, billing closure and request finish state.

**Correction:** D4 owns event buffers/transforms/evidence only; D5 owns request terminal effects. The pipeline produces terminal evidence and calls the terminal owner rather than becoming settlement authority.

### Tool and prompt-cache state had mixed lifetimes — NO-GO / FIXED

The per-B-leg tool assembler and prompt-cache source/controller derive from the active backend attempt. Treating them as request-global response state would leak stale attempt state across replacement.

**Correction:** D7/D8 move attempt-local tool/prompt-cache state with `AttemptSession`; only truly logical-stream event correlation/drain state remains in `ResponsePipeline`.

### Interleaved-thinking extraction could steal outer ownership — NO-GO / FIXED

Hidden/visible interleaved wrappers can intentionally retain A-leg end ownership across thinker/executor continuation. A new terminal owner that always ends the A-leg would break that contract.

**Correction:** D5/D9 make A-leg end ownership explicit and preserve the existing outer interleaved coordinator where applicable. Interleaved state is placed by lifetime without redesigning the domain.

### Removing `*Executor` could create a renamed service locator — NO-GO / FIXED

A broad `recvServices`/`TurnServices` bag would technically remove `*Executor` while preserving the same dependency supermarket.

**Correction:** D10 allows only one localized transitional replacement-open adapter around the current upstream attempt pipeline. Each collaborator receives the concrete operations it actually needs. Generic service bags are prohibited and architecture-gated.

### More locks after extraction could make concurrency harder — NO-GO / FIXED

Splitting state into several lock-bearing objects can increase deadlock risk if callers hold one owner's mutex while calling another.

**Correction:** D13 gives each state cluster its own synchronization owner, uses snapshot-then-call semantics, and prohibits undocumented nested locking. The attempt slot lock is held only for pointer snapshot/swap and never across backend I/O or terminal effects.

### Old direct-construction tests could force weak production invariants — VALID / FIXED

Legacy tests may construct `retryRecvStream` directly and rely on nil/lazy initialization. Preserving this pattern for all new owners would add defensive production branches and obscure lifecycle rules.

**Correction:** Requirements 3.9 and D17 require focused fixtures/builders and deletion of test-only weak initialization where production does not require it.

### Negative LOC or smaller files are insufficient proof — VALID / FIXED

A refactor could achieve a smaller façade while increasing forwarding layers and maintaining the same conceptual state graph.

**Correction:** D15 measures direct responsibility clusters, cross-domain receiver methods, broad Executor reachability, synchronization boundaries and state-copy assignments. Net affected production growth is a default NO-GO, but LOC is supporting rather than sole evidence.

## Validation Round 2 — Architecture Checks

### EventStream responsibility — PASS

The façade is left with `Recv`/`Close` control flow, immutable facts and collaborator references. It is not the owner of billing, recovery, tool, authority, interleaved or terminal mutable state.

### Immutable request facts — PASS

`recvTurnFacts` is explicitly non-mutable and excludes retry counters, current attempt resources, terminal state and locks. It preserves bare-context and generation-pinning requirements.

### Attempt lifetime — PASS

`AttemptSession` matches the lifetime of one B-leg and owns the active backend stream, B-leg/candidate, attempt authority, attempt terminal and attempt-local resources. Replacement swaps the owner rather than fields.

### Recovery lifetime — PASS

Recovery state persists across attempts and owns selectors/budgets/exclusions/diagnostics/affinity/interleaved retry continuity. It does not own terminal effects or backend attempt resources.

### Response-pipeline lifetime — PASS

Client evidence, usage dedupe, gates/drains and event correlation stay together. Settlement and routing remain outside the pipeline.

### Request terminal lifetime — PASS

Output commitment, request terminal ownership, request-authority closure, billing-call closure, request-level metering finalization and A-leg end coordination are aligned with the logical request lifetime.

### Terminal state-machine reuse — PASS

The existing `streamTerminal`/`terminal.Owner` mechanics are preserved rather than replaced by a new generic state machine. The refactor changes business ownership around them.

### Close/Recv race — PASS by design, implementation-gated

The one-Recv-plus-concurrent-Close contract remains. A short attempt-slot synchronization boundary provides coherent current-attempt snapshots without holding locks across backend calls. Targeted scheduling/race tests are mandatory before migration completes.

### Replacement authority ordering — PASS

The design explicitly retires/finalizes the prior attempt at the same safety point before replacement admission. No optimistic overlap is introduced.

### Billing/accounting convergence — PASS

The design does not reopen billing architecture. It moves integration state to request/attempt owners while preserving BillingCallID, one-LUR-per-B-leg, call closure, provider/customer usage distinction, authority ordering and durable terminal-work semantics.

### Secure-session semantics — PASS

The authoritative session/turn facts remain pinned; event recording and mandatory failure policy are separated without duplicate commitment truth.

### Interleaved semantics — PASS

Existing thinker/executor state store, wrappers, memo/cycle behavior and outer A-leg hold remain authoritative. Only integration state placement changes.

### Model-view pinning — PASS

Bound registry/catalog/native-model views remain request facts and cannot fall back to a live generation on bare-context replacement.

### Upstream pipeline boundary — PASS

D10 deliberately isolates the current `attemptOpenParams` translation behind one replacement-open adapter. This prevents tranche 1 from absorbing the preparation/candidate-open redesign and gives the chronologically following spec one explicit seam to replace.

### Public/ABI scope — PASS

No public config, CLI, SDK, plugin ABI, route syntax or provider-specific behavior is required.

### Framework avoidance — PASS

The design requires concrete private owners and explicit control flow; no DI container, service locator, actor/event framework, generic state registry or reflection-based dispatcher is introduced.

## Requirement-to-Design Trace

| Requirement | Primary design coverage |
|---|---|
| R1 baseline/evidence | D15, D16, D17 |
| R2 EventStream/concurrency | D11, D12, D13, D14, D16 |
| R3 small façade | target architecture, D10, D12, D15, D17 |
| R4 immutable receive facts | D1 |
| R5 attempt owner | D2, D7, D8, D11, D13 |
| R6 recovery owner | D3, D9, D10, D14 |
| R7 response pipeline | D4, D6, D8, D14 |
| R8 request terminal owner | D5, D11, D13, D14 |
| R9 domain-semantic preservation | D6–D9, D14, D16 |
| R10 synchronization | D11, D13, D16 |
| R11 structural simplification | D10, D15, D17, D18 |

## Design Rule Coverage Check

- D1 receive-turn facts — required and testable.
- D2 attempt ownership — required and testable.
- D3 recovery ownership — required and testable.
- D4 response pipeline — required and testable.
- D5 request terminal/commit authority — required and testable.
- D6 secure recording placement — mapped to characterization and migration tasks.
- D7 prompt-cache placement — mapped to attempt migration tasks.
- D8 tool state placement — mapped to attempt/response migration tasks.
- D9 interleaved placement — mapped to characterization and recovery/terminal migration.
- D10 transitional opener seam — mapped to recovery migration and explicit follow-up handoff.
- D11 Close protocol — mapped to race/scheduling tests and terminal migration.
- D12 explicit Recv control flow — mapped to final façade conversion.
- D13 synchronization — mapped to attempt slot, terminal and race certification.
- D14 terminal/failure matrix — mapped to characterization and parity certification.
- D15 architecture ratchets — mapped to baseline and final simplification tasks.
- D16 testing — mapped across all phases.
- D17 phased migration — reflected directly in implementation phases.
- D18 follow-up dependency — explicit in final certification/handoff.

All D1–D18 rules are actionable without requiring the second specification to be implemented simultaneously.

## Simplification Review

The design deliberately rejects the following superficially simpler alternatives:

1. another file split of `retryRecvStream` receiver methods;
2. renaming the current struct to `TurnContext` or `ExecutionState`;
3. one giant collaborator containing all extracted state;
4. a generic event/state-machine framework;
5. moving mutable business state into `context.Context`;
6. replacing `*Executor` with an equally broad service bag;
7. interface-per-collaborator abstraction;
8. centralizing request and attempt terminals despite different lifetimes;
9. rewriting billing/security/interleaved/routing domains in the same change;
10. judging success only by file length.

The proposed collaborator count is accepted because each owner corresponds to a concrete existing lifetime/invariant and enables deletion of flattened state. If implementation instead adds wrappers without deleting old ownership, the final architecture gate is NO-GO.

## Implementation Risks to Pin With Tests

- old attempt authority is released after rather than before replacement admission;
- concurrent Close snapshots one attempt while terminal effects mutate another;
- output commitment becomes duplicated across terminal/response/recovery;
- request terminal owner ends an A-leg still held by an outer interleaved wrapper;
- bare-context Recv loses pinned model views or billing/metering/session facts;
- response pipeline collapses provider-billable and customer-visible usage;
- per-attempt tool/prompt-cache state survives replacement incorrectly;
- sideband usage is lost during EOF/error/replacement;
- terminal losing claimant observes a different outcome or reruns effects;
- new locks create a Close/Recv deadlock;
- the replacement-open adapter becomes a permanent generic service bag;
- direct test construction forces production owners back to partial/invalid zero states;
- final code moves methods but retains the same cross-domain state graph.

## Final Gate

**GO FOR TASK GENERATION.** The design is behavior-preserving, scoped to the actual 9.5/10 ownership hotspot, and provides measurable deletion/coupling gates. The first validation round's lifetime, commitment, context-pinning, replacement-ordering and synchronization issues are resolved in the normative design.

Implementation remains approval-gated by `spec.json`. The design is intentionally independent of the following upstream pipeline specification except for one explicit replacement-open seam, allowing the two changes to be reviewed and implemented chronologically.
