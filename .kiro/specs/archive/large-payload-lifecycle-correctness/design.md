# Design Document

## Overview
Repair the accepted-turn lifetime at the frontend, existing wait producer, shared runtime receive authority, and replay spill lifecycle. Existing streaming frontends are the first consumer. Canonical calls and bounded replay remain distinct representations with one shared execution policy.

The slice changes neither eligibility nor billing policy. Failed cleanup remains an operator-visible physical-storage failure, not a revised response outcome. Existing fallback before commitment and prohibition on recovery after content remain unchanged.

### Goals
- Keep accepted replay available through receive-phase replacement.
- Transfer or dispose every returned stream, including partial-error and late-cancellation results.
- Audit successful logical completion without requiring another Recv for EOF.
- Preserve failed unlink identity/error and emit a sanitized, once-only notice.

### Non-Goals
The named proof/parity, assessment-simplification, and eligibility-consolidation follow-up briefs in `requirements.md` stay deferred. No persistent cleanup queue, retry/scavenger worker, public contract/config expansion, unified payload representation, canonical audit rollout, or default enablement is included.

## Boundary Commitments

### This Spec Owns
- **Frontend handoff**: capture rollback before transfer, usable-result validation, callback-safe stream cleanup registration, and consumption of the returned lifetime.
- **Wait handoff**: disposition of an untransferred result by the existing execution producer; both production call sites supply typed disposal.
- **Core outcome projection**: read the existing request terminal and receive-release evidence for the existing wire audit; do not create another terminal authority.
- **Replay storage**: one removal attempt and truthful result at the existing root/last-reader seam; frontend capture binds its safe notification consumer.

### Out of Boundary
No routing, settlement, financial exposure, provider event semantics, secure-session taxonomy, generation identity, proof budget, or eligibility redesign. No edits to the completed archived fast-path or adjacent approved specs.

### Allowed Dependencies
Existing `largebody`, `runtime`, `holdalive`, `frontendpipe`, secure-session app, diagnostics logger, and standard-library channels/mutexes/errors. Core continues to depend on neutral contracts, never concrete frontend/backend/provider packages. Test composition may use `runtimebundle.BuildHost` and existing backend instrumentation.

### Revalidation Triggers
Rebaseline if active billing/high-concurrency work changes terminal winning commands, physical receive completion, attempt publication/withdrawal, stream cancellation, or audit ordering. Revalidate optional-method forwarding when stream wrappers change. New noncooperative worker guarantees, repeated unlink retries, or persistent failed-file retention require a different specification.

## Architecture

### Seams Reused
- `Source.Open/Close`, runtime `ExecuteLargeBody`, and its returned lifecycle wrapper already carry successful replay ownership.
- `holdalive.Wait[T]` gains a typed final `discard func(T)` argument; justified by 2.1–2.2 because generic result ownership cannot be expressed by its non-generic config without assertions. No new worker is introduced.
- The assembled wire `retryRecvStream` supplies a private read-only terminal/receive projection. Binding occurs at stream assembly before publication; no public optional interface or attempt reference escapes.
- Existing internal `SpillConfig`/`CompletedSourceConfig` gain optional `OnRemovalFailure func()` construction input, justified by 4.3 because callers legitimately ignore some Close errors. `Source` stays unchanged.
- Existing `diag.LoggerOrDefault` consumes one fixed safe cleanup notice in production capture. No raw-error logger is used for this signal.

### Authority and Effect Discipline
`research.md` records the constructor/owner/mutation/observer inventory. Physical file/reader cleanup is reversible but fallible; downstream content, usage, audit, and notices are irreversible emissions. Root cleanup does not reverse any of them. No registry, lease authority, billing owner, or terminal state machine is added.

### Project Boundary Questions
- Core owns recovery and terminal truth; frontend owns handoff and wire presentation.
- No new canonical concept; no provider SDK leakage.
- Streaming remains primary; direct/non-stream callers also dispose error-bearing streams.
- Existing output commitment remains the only recovery barrier.
- Privacy improves only for the new fixed cleanup signal; no broader logging-hardening claim.

### Flow
```mermaid
sequenceDiagram
    participant F as Frontend
    participant W as Existing wait producer
    participant R as Runtime receive lifetime
    participant S as Replay source
    F->>W: Execute with typed disposer
    W->>R: Execute accepted replay
    R-->>W: Owned result
    alt Result transferred
        W-->>F: Stream and bounded facts
        F->>F: Register Close before callbacks
        F->>R: Consume canonical events
        R->>S: Open replacement before content only
        F->>R: Close or cancel
        R->>R: Stop publication; read existing terminal evidence
        R->>S: Close root after receive teardown
    else Wait abandoned
        W->>R: Dispose eventual result inline
    end
    S->>S: Final reader enables one unlink attempt
```

## Components and Interfaces
| Component | Package | Intent | Requirements |
| --- | --- | --- | --- |
| Accepted replay handoff | `frontendpipe`, internal `largebody` capability docs | Transfer usable result before callbacks; preserve rollback and real replacement | 1.1, 1.2, 1.3, 1.4 |
| Owned wait result | `holdalive`, `frontendpipe` | Producer disposal or caller transfer, including partial error/direct mode | 2.1, 2.2, 2.3, 2.4 |
| Logical outcome projection | `runtime` | Feed existing wire audit from request outcome plus accepted receive evidence | 3.1, 3.2, 3.3, 3.4 |
| Truthful spill lifecycle | `largebody` | Single remover, retained failure, descriptor-before-unlink ordering | 4.1, 4.2, 4.4 |
| Safe cleanup notice | `largebody`, `frontendpipe` | Source-owned notification despite ignored Close errors | 4.3, 4.4 |

### Accepted Replay Handoff
- Document the internal capability contract: on usable success, the returned lifetime owns source cleanup; callers close the returned stream, not its root. Production runtime preserves pre-return error/panic rollback. Failed or nil-success results do not transfer a usable lifetime.
- Keep a frontend lexical source guard until a nonnil usable stream returns. Remove the immediate successful-path root Close. Register returned-stream Close before `OnWireCommit`, session-header writing, response wrapping, or other fallible callbacks; ensure panic unwinding cannot strand the result.
- Execution error with a partial stream is normalized/disposed at the frontend execute seam. Keep lexical source Close as idempotent rollback. Nil-success is an internal execution failure; close the untransferred root and emit the existing safe error shape, never canonical fallback after the execute commitment.
- Runtime teardown stops/closes the shared receive publication lifetime before root release. Retain existing attempt reader/derived-context cancellation order. Root closure prevents new readers; active readers still finish through the shared lifecycle.
- Existing once-cleanup covers Close, Cancel, receive termination, and panic unwinding. Defer cleanup before inner Close as already done for Cancel. Preserve panic propagation; source disposal and audit must not skip each other on unwinding.

### Owned Wait Result
- Keep disabled/zero-interval/nil-writer paths calling execution directly; normalize partial-error disposal at both concrete frontend execution seams afterward.
- Enabled mode keeps the existing producer, unbuffered result rendezvous, abandonment channel, and producer-done channel. A successful receive transfers ownership, including value-plus-error. Cancellation closes abandonment, does not receive result, and waits only for producer completion or the existing five-second grace.
- Install an idempotent deferred abandonment signal before any writer/timer callback, covering every wait exit including WriteHeader/Flush panic. Cancellation signals it immediately before the grace wait; successful rendezvous has already transferred ownership and cannot be disposed by this later signal. Preserve panic propagation and producer completion after eventual disposal.
- Producer selects between transfer and abandonment, calling the typed disposer on abandonment and then closing producer-done. A selected transfer cannot also be discarded. Cancellation selected with a ready result makes the producer own disposal; ordinary simultaneous readiness retains existing select semantics, not cancellation priority.
- Canonical disposer closes a nonnil `lipapi.EventStream`; wire disposer closes `ExecutionResult.Stream`. After either direct/waited execution, an error-bearing stream is closed, the result cleared, and the original error returned.
- No cleanup waiter goroutine, buffered resource-bearing orphan, detached execution, or new timeout is added. A permanently blocked executor/Close is not guaranteed to terminate; grace bounds caller waiting only. Disposers do not use the response writer and report cleanup failure separately with fixed safe diagnostics.

### Logical Outcome Projection
- Use `retryRecvStream.terminal.requestTerminal().Owner()` outcome/code/state and existing logical receive-release completion. `finished()` alone is not success. No `turnRelease` helper or canonical `FinishTurn` path exists on this baseline; do not invent either.
- Bind scalar read-only evidence to the outer wire cleanup before publication. Capture accepted logical completion at the existing successful receive-release boundary, before cleanup can withdraw pending output; synchronize its observation with concurrent Close/Cancel using the existing publication/receive evidence. Do not inspect raw event kinds or retain an attempt.
- Success requires request success code, released effects, accepted logical completion, and no receive-return failure. A command claimed before release, a backend finish, EOF alone, or an attempt winner is insufficient. Read request evidence, not billing's broader completed/EOF projection.
- The existing once-cleanup appends one `Manager.FinishTurn` audit after shared receive teardown. The existing pre-return failure defer remains exclusive with successful stream transfer. Preserve the current app taxonomy: cancellation and failures project to `surfaced_failure`; no new audit kind.
- Detached audit cleanup uses the existing bounded `cleanupContext` budget rather than unbounded `WithoutCancel`. Keep physical cleanup errors separate from logical outcome and retain existing attempt/authority/usage/billing effects and their once semantics.

| Evidence | Existing audit projection |
| --- | --- |
| Accepted logical success, request success, released effects; Close without EOF | `success` once |
| Same successful evidence followed by EOF or later cancellation | Same success; no relabeling |
| EOF without accepted logical success / `CommandEOF` | `surfaced_failure` |
| Partial/encoder/panic failure, failed effects, durable pending, or receive error before accepted success | `surfaced_failure` |
| Early Close/A-leg cancellation before accepted completion | `surfaced_failure` |
| Replaced attempt, parallel loser, continued/suppressed raw finish, incomplete pending batch | No successful logical completion; no intermediate audit/root cleanup |
| Normal-finish claim but pending output withdrawn | Non-success; command alone is insufficient |

### Truthful Spill Lifecycle and Notice
- Preserve one lifecycle across `SpillBuffer.Complete` and its existing readers. Private state distinguishes pending, removal claimed/in progress, removed, and failed/unresolved; retain the failed path and original error while reachable. No persisted state is introduced.
- Under the lifecycle mutex, root requests deletion and zero readers elects one remover. Snapshot the path and unlock before filesystem I/O. Record the result under lock, then notify outside it. No callback, I/O, or wait under this mutex.
- Successful unlink or `errors.Is(err, os.ErrNotExist)` means confirmed absence and clears the path. Other failures keep path/error and return failure to the triggering root/reader closer. Repeated root Close remains nil and does not retry.
- Both tracked readers close the physical descriptor before releasing their lifecycle count; join descriptor and deferred-removal errors. Keep current second-reader-Close behavior. No wholesale reader-type consolidation in this correctness slice.
- Production capture installs `OnRemovalFailure` using the existing logger fallback and fixed `spill_remove_failed` reason. Capture only logger and existing safe request correlation if available, never request/body/writer/path/raw error/digest/model. Callback delivery is once per lifecycle failure and cannot revise the stream outcome; observer panic must not prevent state publication or owned cleanup.
- Logical reservation releases exactly once on root close, regardless of unlink outcome. Unresolved state has only the existing reachable lifecycle as owner; no indefinite retention, eventual cleanup, physical disk-budget guarantee, or restart recovery is promised.

## File Structure Plan
### Modified Production Files
- `internal/plugins/frontends/holdalive/wait.go` — typed transfer-or-disposal handoff and updated ownership comments.
- `internal/plugins/frontends/frontendpipe/pipe.go` — both typed disposer consumers and partial-error normalization, including direct mode.
- `internal/plugins/frontends/frontendpipe/candidate.go` — safe successful handoff, nil/panic rollback, and spill-notice binding.
- `internal/core/largebody/capability.go` — internal successful source-transfer contract, no public API change.
- `internal/core/largebody/source.go` — one-remover state, deferred error return, safe notification construction input.
- `internal/core/largebody/spill.go` — preserve shared lifecycle/notification through Complete and propagate reader cleanup error.
- `internal/core/runtime/turn_terminal.go` — read-only scalar logical outcome projection on the existing terminal owner, plus the monotonic accepted-completion latch.
- `internal/core/runtime/executor_recv_loop.go` — latch accepted completion on the recovery-drain path that bypasses `finishResponseAtBoundary`.
- `internal/core/runtime/executor_execute_large_body.go` — consume shared outcome in existing audit, ordered/panic-safe once cleanup and bounded audit context.

As implemented, the projection landed on `turnTerminal` rather than `retryRecvStream`/`executor_assemble_stream.go`: the existing Task 5.2 facade gate forbids adding receivers to the EventStream facade, so the evidence stays on its pre-existing owner. Accepted completion is latched at the receive-release delivery boundary and request-terminal code/state is read live at teardown, which is equivalent to pre-publication binding because the latch is monotonic and the request-terminal claim is stable after delivery.

### Tests Modified or Extended
- `internal/plugins/frontends/holdalive/wait_internal_test.go` and new ownership tests — deterministic cancellation/rendezvous/grace disposal schedules.
- Existing frontendpipe keepalive and candidate tests — canonical/wire partial-error cleanup, nil success, callback panic, and pathless production notice.
- Existing `internal/core/largebody/source_test.go`, `fault_privacy_test.go`, and spill lifecycle tests — one unlink claimant and immediate/deferred failure/error identity.
- `internal/core/runtime/large_payload_lifecycle_audit_test.go` — durable audit, panic/Close, and classifier-table schedules through the real executor and audit store.
- `internal/infra/runtimebundle/stock_host_large_payload_lifecycle_test.go` — real frontend → production assessor/executor → receive-phase replacement sentinel.

## Error Handling
Preserve cancellation/execute error identity and existing frontend safe wire mapping. Error-bearing owned streams are disposed before returning the original error. Cleanup errors remain owner-facing errors where available plus independent fixed diagnostics; do not attach raw errors to new operator notices. Retained spill identity/error is private attribution, not a cleanup retry promise.

## Testing Strategy
Extend existing suites, with a claim/source/mutant table in each implementation task's evidence. Every regression must fail before its repair; do not create tests for Markdown or a Cartesian frontend/backend matrix.

| Claim | Cheapest serving observation / concrete mutant |
| --- | --- |
| 1.1–1.4 | Stock `BuildHost`/Responses ServeHTTP sentinel: first OpenWire returns, then Recv produces recoverable pre-output error; replacement uses real adapter/body. Restore immediate root Close → replacement fails. Memory/disk cases, exact rewritten bytes, two wire opens/zero canonical opens, final reclamation; post-content failure → no replacement. |
| 2.1–2.4 | Channel/synctest schedules before/after grace and simultaneous readiness; canonical/wire concrete call sites include result+error, nil, direct mode, 102 behavior, disposal failure, and WriteHeader/Flush panic followed by late result. Restore drain/buffer or omit abandonment-on-unwind/typed disposer → missed Close or stranded producer. |
| 3.1–3.4 | Real executor plus terminal-stopping pump/audit store: accepted finish→Close without EOF, EOF, early cancellation, failure/panic, effect failure/durable pending, replaced attempt, pending withdrawal. Restore EOF/Close guess → wrong audit; assert one audit and unchanged attempt cardinalities. |
| 4.1–4.4 | Inject remove success/absent/failure at root/final reader, shared Complete lifecycle, descriptor-close barrier, root/last-reader race, repeated Close. Clear path/omit return/notice or release reader before descriptor → failed invariant. Verify safe notice despite ignored Close errors and unchanged logical reservation count. |

Local feedback: fresh focused `go test -count=1` on the affected packages; applicable complete package suites and `make dev-contract-check` on the coherent implementation. Use existing architecture/import/ownership gates, not a new bespoke rule. Race evidence is remote GitHub CI only; platform-sensitive storage needs remote OS evidence. No local race bypass.

Before coding and after accepted tasks, run the delivery-plan command from `gap-analysis.md`; all tasks fit one intended corrective PR. Default-off configuration and body/admission limits stay unchanged. Allocation ratchets can check hot-path regressions; no live-provider latency or financial benefit claim is made.
