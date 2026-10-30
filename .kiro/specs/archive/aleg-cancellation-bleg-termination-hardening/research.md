# Research & Design Decisions

## Summary

- **Feature**: `aleg-cancellation-bleg-termination-hardening`
- **Discovery scope**: brownfield complex integration spanning core runtime lifecycle, backend-plugin ABI/host adapter, connector Execute pumps, and terminal billing evidence.
- **Primary issue**: #431 — prove that A-leg cancellation terminates all related B-legs, prevents future B-leg provider work, and preserves per-leg financial accounting.
- **Merged baseline**: PR #428 (`runtime-attempt-publication-ownership-convergence`) merged to `main` as `7a6c7532`. Its final commit binds unpublished A-leg cancellation to `readyAttempt` ownership; the final merged code was re-audited before this spec was approved.
- **Key findings**:
  - `main` already has sticky A-leg cancellation, active B-leg teardown, serial replacement cancellation guards, detached cancellation billing, ready-before-publication semantics, and exactly-once attempt terminal ownership.
  - `attemptSession.TerminalizeAttempt` is the sole production attempt terminal owner. A-leg lifecycle registration uses a `readyAttempt` lifecycle handle before publication and delegates to the persistent session after consumption. Parallel-loser finalization already converges on that owner.
  - Provider activation is still not linearized against explicit A-leg cancellation. `Backend.Open` happens before the B-leg becomes visible to A-leg lifecycle, so cancellation can win while a delayed/future branch still crosses the economic activation boundary.
  - `ALeg.Cancel` snapshots active B-legs but invokes their cancellation serially, allowing a non-cooperative child to delay siblings.
  - The executable backend-plugin ABI already contains `CANCEL`, `cancel_deadline_unix_ms`, and `CANCEL_OUTCOME`; however, the common `ForwardExecute` helper reads only the initial `START` frame, the host adapter returns after local cancel enqueue, and `CANCEL_OUTCOME` is discarded.
  - Wire `CancelOutcome` has acknowledgment/reason/detail but no actual cancellation mode. Runtime lifecycle wrappers also currently return `CancelModeProvider` unconditionally, losing the physical stream's truthful `CancelResult`.
  - Provider-only accounting sideband is attempt-specific and deduped, but cancellation/parallel terminal paths can bypass the ordinary response-pipeline drain. The converged attempt owner therefore needs a terminal evidence drain so finalizer-unsupported/failure paths keep the best available provider evidence.

## Research Log

### Issue #431 intent and economic correction

- **Context**: The issue asks to cancel all B-legs, including branches scheduled for future failover/hedging, to request protocol-correct cancellation with a short timeout and force fallback, and to preserve billing for B-legs that emitted completion tokens.
- **Sources consulted**: issue #431 body and its audit/design comments; current lifecycle, retry, parallel, connector and billing tests.
- **Findings**:
  - “B-legs that delivered completion tokens” is economically too narrow. A losing/canceled leg can incur input, cache, reasoning, tool or provider-side output charges without surfacing completion tokens.
  - “Protocol-compliant cancel request” cannot mean one universal provider endpoint. Cancellation is provider/protocol-specific: application cancel where supported, otherwise request/stream/transport abort.
  - Force fallback must be per-attempt. Optional connectors can share a process/session across unrelated calls.
- **Implications**:
  - The financial invariant is broadened to **every B-leg that may have incurred provider-billable work**.
  - Cancellation mode must describe the mechanism actually used, not the mechanism requested by core.
  - Force fallback may cancel the active Execute RPC/request/socket/subprocess operation but must not indiscriminately terminate a shared connector instance.

### PR #428 defines the fixed ownership baseline

- **Context**: The initial #431 audit targeted `main` SHA `8563af0`; PR #428 subsequently merged as `7a6c7532` with a final ready-ownership cancellation fix.
- **Sources consulted**: final merged `attempt_session.go`, `executor_open_attempt.go`, `parallel_race.go`, `executor_recv_loop.go`, `turn_terminal.go`, `executor_assemble_stream.go`, `leglifecycle/coordinator.go`, and #428 tests/ratchets.
- **Findings**:
  - `readyAttempt` prevents publication until readiness completes and owns unpublished cancellation. Cancellation racing `Prepare`/`Consume` invalidates the publication capability.
  - `attemptSession.TerminalizeAttempt` owns physical Cancel/Close, observer finish, authority, metering, B-leg release, billing append/finalization, and attempt evidence under one terminal CAS.
  - A-leg lifecycle stores ready/session lifecycle handles rather than raw backend streams.
  - Parallel workers return isolated outcomes to a serial reducer and normal loser teardown uses the converged attempt owner.
  - Both ready/session lifecycle cancel paths still manufacture `CancelModeProvider` instead of preserving the physical stream's `CancelResult`.
  - `Backend.Open` still happens before A-leg lifecycle registration. Parallel paths create/register `readyAttempt` soon after successful Open; the serial path can additionally perform fallible interleaved persistence before `HandoffReady`/registration.
- **Implications**:
  - This spec must **not** introduce another B-leg teardown or parallel-loser billing owner.
  - Cancellation-result fidelity belongs in `TerminalizeAttempt` result/lifecycle-handle plumbing.
  - The provider-activation barrier belongs in `leglifecycle` plus existing attempt-open paths.
  - After Open succeeds, ownership should transfer immediately to an unprepared `readyAttempt`; its lifecycle handle should become A-leg-visible before later fallible post-Open work, and failures should dispose that ready owner.

### Current A-leg lifecycle and provider-activation race

- **Sources consulted**: `internal/core/leglifecycle/coordinator.go`, runtime lifecycle tests, serial and parallel open paths.
- **Findings**:
  - `ALeg.Cancel` is sticky and `RegisterBLeg` after cancellation immediately tears down the late handle.
  - This prevents a late B-leg from surviving but does not prevent provider work from starting because registration occurs after `Backend.Open`.
  - A plain `aScope.Err()` check before Open is insufficient: cancellation can win after the check and before the Open call.
  - Active child cancellation is serial today.
- **Implications**:
  - Add an A-leg-owned **single-use launch permit**. `BeginLaunch` and `Cancel` must linearize under the same A-leg mutex.
  - If cancellation wins first, no Open call occurs.
  - If launch wins first, the A-leg records the derived Open cancellation function before provider activation begins.
  - After successful Open, commit the launch permit to the ready attempt lifecycle handle atomically under the same A-leg authority.
  - Snapshot launches/children under lock, then cancel launch contexts and fan out attempt cancellation concurrently outside the lock.

### Backend-plugin cancellation is currently mostly transport teardown

- **Sources consulted**: `pkg/lipsdk/backendplugin/forward_execute.go`, `pkg/lipsdk/backendplugin/host/session.go`, `internal/infra/backendplugins/adapter/stream.go`, `api/backendplugin/v1/backend.proto`, and connectors using `ForwardExecute`.
- **Findings**:
  - `ForwardExecute` consumes `START`, opens the upstream managed stream, and then receives only upstream events; it does not consume post-START client control frames.
  - A context watcher invokes upstream cancel only after the Execute stream context dies.
  - Host-side `managedStream.Cancel` returns success immediately after enqueuing a CANCEL frame.
  - `managedStream.onPluginFrame` validates but discards `ServerFrameCancelOutcome`.
  - `ClientFrame.CancelDeadlineUnixMS` already exists but is not populated by the adapter.
  - `CancelOutcome` does not carry `CancelMode`.
  - Many connectors share `ForwardExecute`, making the common SDK helper the correct leverage point.
- **Implications**:
  - Keep cancellation on the **same bidirectional Execute stream**; do not add a second unary cancel RPC or attempt registry.
  - Add an additive negotiated backend-plugin minor/feature; at specification time the next available minor is 8, but implementation must bind to the next available minor at implementation time.
  - Extend wire cancellation outcome with truthful cancellation mode.
  - `ForwardExecute` must consume post-START CANCEL/CLOSE_INPUT while upstream execution is active and serialize outgoing server-frame sequencing.
  - Host adapter cancellation must distinguish request, acknowledgment/outcome, terminal completion, timeout, and force-abort.

### Backward compatibility for older connector binaries

- **Context**: Connector ABI is explicitly versioned and external binaries may lag the host.
- **Findings**:
  - Requiring every connector to understand new cancellation semantics immediately would create a breaking rollout.
  - The existing gRPC Execute context already provides a per-attempt transport cancellation seam.
- **Implications**:
  - Gate the richer handshake behind a negotiated feature, proposed name `cancellation_handshake_v1`.
  - When absent, do **not** send an unsupported in-band contract and do not claim provider acknowledgment.
  - Fall back to canceling the active Execute transport/context and report transport or close-only mode truthfully.

### External cancellation semantics

- **Sources consulted**:
  - gRPC cancellation guidance: https://grpc.io/docs/guides/cancellation/
  - gRPC request hedging guidance: https://grpc.io/docs/guides/request-hedging/
  - OpenAI Responses cancel operation: https://developers.openai.com/api/reference/cli/resources/beta/subresources/responses
  - Anthropic TypeScript/Python streaming helpers.
- **Findings**:
  - gRPC cancellation requires propagation to spawned work; hedged outstanding requests are canceled after a winner.
  - Some OpenAI protocols expose an application-level cancel operation.
  - Anthropic streaming helpers primarily expose request/stream abort/close semantics rather than a universal message-level cancel endpoint.
- **Implications**:
  - Provider-native cancellation is valid where the provider really exposes it.
  - Transport abort is the correct final mechanism where the protocol does not provide a stronger application operation.
  - Core must remain provider-neutral and consume only `ManagedEventStream` cancellation contracts.

### Terminal billing and provider sideband evidence

- **Sources consulted**: `response_pipeline_observations.go`, `response_pipeline.go`, `executor_settlement.go`, `billing_leg.go`, backend-plugin accounting sideband, and post-#428 attempt terminalization.
- **Findings**:
  - Provider accounting evidence is host-only and already has explicit source/authority/plane/dedupe semantics.
  - Normal receive drains sideband before and after receive and dedupes per attempt.
  - A cancellation or parallel loser can terminalize without passing through the same normal drain sequence.
  - PR #428 ensures loser/cancel billing now has one terminal owner and can call `FinalizeBilling`, but unsupported/failing finalization can still leave buffered sideband evidence unused.
  - Existing `authorityUsageEvent` and scoped merge logic aggregate provider-authoritative evidence; the design should reuse those semantics rather than introduce last-event-wins accounting.
- **Implications**:
  - Sideband evidence accumulation/drain must become attempt-owned and available to `TerminalizeAttempt` before the billing-leg fallback is frozen.
  - Evidence precedence remains: authoritative finalizer when available; provider sideband/stream evidence as fallback/augmentation; weaker existing estimates; explicit unavailable evidence when nothing trustworthy exists.
  - No monetary rating/journal mutation moves into stream lifecycle.

### Connector contract-test false-positive risk

- **Context**: Existing connector certification treats cancellation success as `HostSession.Cancel()` returning nil.
- **Findings**:
  - The existing host cancel helper can transmit START+CANCEL without proving that the active upstream managed stream consumed cancellation.
- **Implications**:
  - The TCK must use one active Execute attempt and assert the real upstream `ManagedEventStream.Cancel`/terminal path, including mode and deadline behavior.

## Brownfield Gap Analysis

| Desired contract | Current merged state | Gap | Requirement consequence |
|---|---|---|---|
| Sticky A-leg cancellation | Implemented | None | Preserve; do not redesign |
| Exactly-once attempt teardown | Implemented by #428 | None | `TerminalizeAttempt` stays sole owner |
| Unpublished cancellation | Ready-owned after final #428 commit | None | Integrate launch permit into ready ownership |
| No provider launch after A-leg cancellation | Not guaranteed | `Backend.Open` precedes lifecycle registration | Add linearized launch permit |
| Cancel in-flight Open | Only parent/race context cancellation | Explicit A-leg cancel has no launch handle | Track/cancel launch contexts |
| Fast cancellation of siblings | Active children canceled serially | Head-of-line blocking | Concurrent fan-out outside lock |
| Truthful lifecycle CancelResult | Ready/session handles return provider mode | Physical result discarded | Propagate terminal physical cancel result |
| Real plugin CANCEL handshake | Wire shapes exist, helper ignores post-START control | Cancellation degenerates to transport death | Add negotiated active-Execute handshake |
| Cancellation deadline | Field exists but not used end-to-end | No graceful deadline contract | Populate/cap effective deadline |
| Legacy connector compatibility | Current binaries expect old semantics | New behavior could break rollout | Negotiated feature + transport fallback |
| Provider sideband on cancel/loser | Normal receive drains; terminal paths can miss | Finalizer failure/unsupported can lose evidence | Attempt-owned terminal drain |
| Financial leg exact-once | Improved by #428 | Needs evidence strengthening only | Preserve billing owner, improve fallback |

### Requirements repaired after gap analysis

The first requirements draft was tightened in seven ways:

1. Replaced “completion-token-emitting B-leg” with “provider-incurring B-leg”.
2. Made PR #428's final ready-owned cancellation model a preserved baseline rather than a planned refactor.
3. Replaced a simple pre-Open cancellation check with a linearized launch-permit contract.
4. Added independent concurrent sibling cancellation to eliminate head-of-line blocking.
5. Made cancellation mode/progress truthful and observable across the lifecycle wrapper and connector boundary.
6. Added additive protocol negotiation/legacy fallback rather than assuming all connectors upgrade atomically.
7. Added attempt-owned terminal sideband evidence preservation without moving money policy into runtime.

The repaired requirements pass the brownfield requirements gate: each describes observable behavior, avoids provider-specific implementation mandates, and preserves current domain ownership.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / limitations | Verdict |
|---|---|---|---|---|
| Post-Open sticky registration only | Keep current late registration cleanup | Simple | Provider work can already start after cancel | Reject |
| Pre-Open `Err()` check | Check cancellation immediately before Open | Cheap | TOCTOU between check and Open | Reject |
| Stored request context in A-leg | Keep request context and cancel it | Familiar | Violates context ownership style; does not linearize activation | Reject |
| **A-leg launch permit** | One A-leg authority grants/commits provider activation | Linearizable; cancels in-flight Open; provider-neutral | Adds small internal state machine | **Select** |
| Serial child teardown | Existing cancellation loop | Simple | Slow child blocks siblings | Reject |
| **Concurrent bounded fan-out** | Snapshot children, cancel independently outside lock | Prompt siblings; bounded by active children | Requires goroutine ownership/leak tests | **Select** |
| Unary plugin cancel RPC + registry | Address attempts outside Execute stream | Easy request/response semantics | New registry/identity path; duplicate authority | Reject |
| **Same-Execute handshake** | Read CANCEL on active bidi stream, emit outcome/terminal | Reuses stream identity, additive ABI | Requires concurrent pump with serialized send | **Select** |
| Always transport-cancel connector | Cancel Execute RPC only | Backward-compatible | Cannot expose stronger provider controls | Legacy fallback only |
| FinalizeBilling-only loser evidence | Depend exclusively on finalizer | Simple | Unsupported/failing finalizer loses buffered evidence | Reject |
| **Attempt-owned terminal sideband drain** | Retain/dedupe evidence on producing attempt | Correct attribution across cancel/loser/replacement | Requires bounded accumulator | **Select** |

## Design Decisions

### Decision: PR #428 is the fixed ownership baseline
- **Selected approach**: extend `TerminalizeAttempt` and `readyAttempt`; do not create a second cleanup, billing, or B-leg release owner.
- **Rationale**: preserves exact-once physical teardown and the ready-owned unpublished cancellation model.
- **Follow-up**: run a baseline characterization first; if later `main` materially changes #428's contract, repair this spec before implementation continues.

### Decision: Linearize provider activation with an A-leg launch permit
- **Selected approach**: `BeginLaunch` checks cancellation and installs a derived Open cancel function under the A-leg lock. `Commit` replaces the launch entry with a ready lifecycle handle under the same authority; `Abort` removes it.
- **Rationale**: cancellation and provider-activation permission share one linearization authority, eliminating check-then-open TOCTOU.

### Decision: Keep physical teardown in `TerminalizeAttempt`
- **Selected approach**: A-leg lifecycle stores only ready/session lifecycle handles. Fan-out invokes those handles, which delegate to the one attempt terminal owner.
- **Rationale**: no raw backend stream can bypass authority, billing, observer, B-leg release, or evidence settlement.
- **Consequence**: terminal/lifecycle results must expose the actual physical `CancelResult`.

### Decision: Make backend-plugin cancellation an explicit negotiated handshake
- **Selected approach**: add the next available ABI minor/feature, add wire cancel mode, consume CANCEL on the active Execute stream, emit `CancelOutcome`, preserve sequence monotonicity, and emit one terminal when the attempt ends.
- **Rationale**: uses existing bidi stream identity and avoids a second RPC/registry.

### Decision: Legacy connectors use truthful per-attempt transport fallback
- **Selected approach**: without the new feature, force-cancel only the active Execute transport/context and report transport/close-only mode; never claim provider acknowledgment.
- **Rationale**: safe and backward-compatible without decorative cancel frames.

### Decision: Adapter `Cancel` waits for terminal or bounded grace expiry
- **Selected approach**: send negotiated CANCEL with effective deadline, observe outcome, continue accepting accounting/terminal frames until terminal or grace expiry, then force-cancel the Execute context if required. `Close` becomes resource cleanup/join after the cancellation result.
- **Rationale**: distinguishes request, acknowledgment, and terminal state and preserves accounting frames.

### Decision: Sideband evidence becomes an attempt-owned terminal fallback
- **Selected approach**: normal receive and terminal teardown feed the same bounded per-attempt dedupe/evidence set. Terminalization drains remaining `UsageEvidenceSource` data around cancel/close and supplies aggregated provider evidence to billing before finalizer precedence is applied.
- **Rationale**: evidence attribution no longer depends on the current slot or client-visible response path.

### Decision: No universal provider cancellation implementation
- **Selected approach**: backend-family tests document and enforce the mode each adapter actually uses. Provider-native cancellation may be used where real; transport abort is correct where that is the strongest supported protocol mechanism.
- **Rationale**: avoids fake APIs and provider-specific policy in core.

## Risks & Mitigations

- **Cancel/Open race** — one A-leg launch-permit mutex/authority plus deterministic schedule tests.
- **Fan-out goroutine leaks** — snapshot finite active children, no locks across I/O, per-child deadlines, race/goleak coverage.
- **Double teardown after #428** — register only lifecycle handles; keep `TerminalizeAttempt` CAS as sole physical owner.
- **Connector frame-order races** — one serialized server-frame sequencer/sender even when control and upstream receive are concurrent.
- **Old connector compatibility regression** — negotiated minor/feature plus explicit transport fallback tests.
- **Sideband double-counting** — reuse per-attempt dedupe keys and test normal-drain + terminal-drain races.
- **Cancellation latency** — concurrent A-leg fan-out; each B-leg has a short bounded grace interval and force fallback.
- **Shared connector accidentally killed** — abort only active Execute/request operation; instance shutdown remains separate.
- **Provider logic leaks into core** — core sees launch permits, `ManagedEventStream`, `CancelMode`, and canonical usage evidence only.
- **Billing policy leaks into runtime** — terminalization captures evidence/records only; rating and journal mutation stay post-usage billing concerns.

## References

### Repository
- Issue #431 — A-leg cancellation / all B-leg termination and billing contract.
- PR #428 / merge `7a6c7532` — attempt publication/terminal ownership convergence and ready-owned unpublished cancellation.
- `.kiro/steering/product.md`
- `.kiro/steering/routing-and-orchestration.md`
- `.kiro/steering/structure.md`
- `.kiro/steering/tech.md`
- `.kiro/steering/testing.md`
- `internal/core/leglifecycle/coordinator.go`
- `internal/core/runtime/executor_open_attempt.go`
- `internal/core/runtime/attempt_session.go`
- `internal/core/runtime/parallel_race.go`
- `internal/core/runtime/billing_leg.go`
- `pkg/lipapi/lifecycle.go`
- `pkg/lipsdk/backendplugin/forward_execute.go`
- `pkg/lipsdk/backendplugin/host/session.go`
- `internal/infra/backendplugins/adapter/stream.go`
- `api/backendplugin/v1/backend.proto`
- `pkg/lipsdk/backendplugin/contracttest/contracttest.go`

### External
- gRPC Cancellation — https://grpc.io/docs/guides/cancellation/
- gRPC Request Hedging — https://grpc.io/docs/guides/request-hedging/
- OpenAI Responses cancel operation — https://developers.openai.com/api/reference/cli/resources/beta/subresources/responses
- Anthropic TypeScript streaming helpers — https://github.com/anthropics/anthropic-sdk-typescript/blob/main/helpers.md
- Anthropic Python streaming helpers — https://github.com/anthropics/anthropic-sdk-python/blob/main/helpers.md
