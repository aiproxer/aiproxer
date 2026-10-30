# Research and Design Decisions

## Summary
- **Feature**: `large-payload-lifecycle-correctness`.
- **Discovery**: Full brownfield integration investigation, scoped to four lifecycle defects; no new external dependency research is needed because the selected mechanisms reuse Go channels, mutexes, errors, and existing runtime seams.
- **Source baseline**: `10ed7ca12e2a7b30fc4d8f19d7b0dc27fca33e36`.
- **Audit baseline**: `8a041a2ad9c9c2c0d0bf8ee57005fe66f7479d6c`; the intervening changes touch only ACP stdio, CI, and change-size tests. `git diff --name-only <audit> HEAD` confirms no lifecycle-source drift.
- **Historical evidence**: `architecture-review-20261013-194245.html` in external agent scratch. The report and scratch overlays supplement, but are not prerequisites for implementing the repository-contained specification. `$SCRATCH` below denotes that external ext4 scratch directory, not a fixed machine path.
- **Key finding**: Distinct representations are valid; ownership gaps are at existing handoffs. Neither a unified payload DTO nor an alternate execution engine is justified.

## Brownfield Authority and Effect Map
| Resource/state | Constructor and identity | Current authority / observers | Cleanup and withdrawal | Effect class |
| --- | --- | --- | --- | --- |
| Replay root | Frontend capture; immutable source/digest for one turn | Frontend before handoff, existing runtime execution lifetime afterward | Close returned stream before source; failed handoff remains lexical | Request-local reversible resource |
| Replay reader | `Source.Open`, independent offset-zero cursor | Attempt/body wrapper; shared spill lifecycle counts active readers | Close descriptor, then release tracked reader; root cannot wait for leaked readers | Request-local reversible resource |
| Logical spool reservation | Capture reservation; source carries its claim | Existing spool ledger and root close | Exactly-once release on root close, independent of physical unlink | Bounded process accounting, not disk reclamation or financial exposure |
| Execution result | Existing executor call, canonical stream or `ExecutionResult.Stream` | Caller after successful transfer; current holdalive producer before transfer | Caller closes consumed result; producer must dispose abandoned result | Owned asynchronous handoff |
| Attempt/recovery | Shared attempt session and retry receive stream; B-leg identity | Existing core attempt, recovery, and output-commit authorities | Existing cancellation/terminalization; no replacement after committed content | Execution policy and irreversible downstream emission |
| Logical turn audit | Secure-session turn identity | Existing turn finalization seam | Once per logical turn, consuming the shared outcome rather than cleanup guessing | Irreversible audit emission |
| Plaintext spill | Shared `spillLifecycle` across buffer/completed source; private file identity | Lifecycle alone mutates path, count, and delete state | Root requests removal; final reader enables one removal attempt | Local filesystem resource with fallible inverse |
| Cleanup notice | Existing frontend diagnostics/logger, captured at source construction | Source lifecycle reports once; logger observes | Fixed sanitized event, never response-writer access | Irreversible bounded diagnostic emission |

## Research Log

### Replay handoff and terminal audit
- `internal/plugins/frontends/frontendpipe/candidate.go:629–644` closes the source on failed execution and again immediately after successful execution. The runtime retains the source for receive-phase replacement and later cleanup (`internal/core/runtime/executor_execute_large_body.go:626–635,726–760`).
- Historical frontend probe reopened the accepted source during response consumption and received `largebody: source is closed`. Its production receive-replacement consequence was source-traced, not executed through a complete stock host.
- Historical real-executor and production SSE-pump probes observed success-terminal → Close yielding `surfaced_failure`, whereas success-terminal → EOF → Close yielded success. Keepalive EOF prefetch can mask this schedule.
- Corrections must preserve failed-handoff cleanup, returned-stream disposal, replacement readers, early close, and the logical-versus-attempt terminal distinction.
- Exact authority: `retryRecvStream.terminal` is the request's `turnTerminal`; `requestTerminal().Owner().Outcome()` supplies the winning code/command and `State()` supplies final effects. `finished()` alone is completion, not success. `attemptSession.terminalEvidence()` is not a logical success oracle.
- `streamAssembler.assemble` constructs this authority before publication (`executor_assemble_stream.go:24–68`) and returns the retry stream directly for wire (`74–96`). Bind a private read-only projection here; do not retain an attempt or add a public optional interface.
- Require successful request code, released effects, and accepted logical completion from the receive-release path, with no receive-return failure. `CommandEOF` without accepted success is non-success. Raw finish events may continue/recover; provisional normal-finish claims and withdrawn pending output are insufficient.
- Baseline correction: there is no canonical `Execute`/receive caller of `Manager.FinishTurn`, and no `turnRelease` symbol. Audit currently exists in the wire failure defer and outer cleanup. Adding canonical audit behavior would expand this slice; instead feed existing wire cleanup the shared request evidence.
- Register successful returned-stream cleanup before `OnWireCommit`, session-header writes, or wrapping. Guard nil-success/partial-error results; no canonical fallback after commitment. Preserve runtime source rollback on pre-return failure/panic and make wrapper Close panic-safe like its existing Cancel.
- Best serving witness: extend `internal/infra/runtimebundle/stock_host_reachability_test.go:2383–2661`, which uses `BuildHost`, production assessor/executor, standard frontend, and real compatible adapter. Inject `RecoverablePreOutputError` from first stream Recv, not Open; replacement must reopen the real source. Include memory/disk capture and a post-output negative control.

### Abandoned results
- `internal/plugins/frontends/holdalive/wait.go:36–58` uses a buffered result and discards a cancellation drain; late completion after grace can remain buffered without a disposer.
- Exactly two production call sites are in `frontendpipe/pipe.go:188–213`: canonical `lipapi.EventStream` and `largebody.ExecutionResult`. Their partial-result/error branches also lack stream disposal (`pipe.go:435–446`, `candidate.go:629–636`).
- Select a typed `discard func(T)` argument at the existing generic wait seam. A non-generic config callback would require assertions; a resource registry or generic config family is unnecessary.
- Use an unbuffered result rendezvous, an abandonment signal, and producer completion. Receiving transfers ownership. Cancellation closes abandonment and waits for producer completion or the existing grace; it never drains a resource-bearing result. The existing producer performs disposal after abandonment.
- Independent design review identified a writer-panic exit gap: abandonment must be signaled idempotently on every wait exit, with its defer installed before WriteHeader/Flush. Otherwise a late cooperative result strands at the rendezvous. Add late-result/partial-error schedules after writer panic; preserve panic propagation and exclusive transfer.
- At both frontend execution seams, also close and clear a stream accompanying an error after direct or waited execution. Preserve the original error, and never write an abandoned response.
- Bounds: no additional cleanup goroutine and no buffered orphan. A permanently blocked executor or disposer remains outside any guaranteed termination claim; grace bounds caller waiting, not provider execution or filesystem I/O.

### Spill removal
- `internal/core/largebody/source.go:114–145` forgets the path after immediate removal failure and discards deferred failure; both tracked readers currently cannot return deferred unlink errors (`source.go:379–388`, `spill.go:721–729`).
- `SpillBuffer.Complete` already shares one lifecycle with completed sources and existing readers (`spill.go:629–638`); preserve it.
- Select a single claimed removal attempt: root requests deletion, zero readers enables claim, filesystem I/O runs unlocked, success/already-absent clears path, failure retains path/error and unresolved state. Return delayed failure through reader Close, joined with descriptor failure.
- Keep repeated root Close returning nil and performing no retry, as required by existing fault tests (`fault_privacy_test.go:376–399`). Retained attribution lasts while the lifecycle is reachable; no indefinite retention, eventual deletion, or restart recovery is promised.
- An optional internal `OnRemovalFailure func()` on the existing spill/source configurations supplies an independent safe operator signal. Wire production capture to `diag.LoggerOrDefault`; do not call raw-error logging or capture the request/body/writer.
- Logical reservations still release on root close. `spool.go:32–44,356–399` bounds in-flight logical reservations, not orphaned disk occupancy.

## Adjacent Spec Compatibility
- `high-concurrency-performance-hardening` is approved/ready. Its design requires reuse of existing stream ownership and avoids speculative owner/pool growth (`design.md:7,716–717`). This slice changes handoff correctness, not concurrency tuning or semaphore policy.
- `billing-financial-safety-contracts` is approved/ready. Its design separates physical cleanup from financial finality (`design.md:31–43,184`). Preserve existing usage/billing owners; never equate root Close or unlink with exposure release or settlement.
- These are shared-seam revalidation dependencies, not prerequisite implementation tasks. Rebase/review if their work changes receive outcome, audit, or cleanup seams before this slice lands.
- The original fast-path spec is completed under `.kiro/specs/archive/large-payload-streaming-fast-path/`. Its requirements 10.1–10.4 and 20.9–20.10 establish replay/recovery and tracked-reader cleanup contracts. Do not reopen or edit that history.

## Alternatives and Synthesis
| Option | Consequence | Decision |
| --- | --- | --- |
| No change | Preserves reproducible replay, disposal, audit, and unlink defects | Reject |
| Smallest local hardening of existing owners | Corrects ownership transfer and error/outcome propagation; no new execution owner | Select |
| New shared lifecycle/lease abstraction | Adds a second protocol for already-owned request resources | Reject; no demonstrated need |
| Generic effect registry/container/scavenger | Expands scope, retention, shutdown, and privacy contracts | Reject |

- **Generalization**: Both representation variants need result transfer-or-disposal; the existing generic wait seam captures this variation with one typed callback. They do not need a common payload representation.
- **Build vs adopt**: Reuse Go channel rendezvous and existing spill mutex/reader tracking. No dependency, public interface, schema, feature plane, or background service is added.
- **Simplification**: Delete premature successful-handoff Close and buffered orphan/result-drain behavior; keep source/attempt/receive authorities and local cleanup wrappers. Unrelated reader consolidation stays deferred.
- **Outcome separation**: Audit consumes logical execution evidence. Physical cleanup errors and diagnostic events do not revise business outcomes or trigger recovery.

## Evidence and Limits
- Controller ran fresh full non-race `go test -count=1 ./internal/plugins/frontends/holdalive ./internal/core/largebody`: both PASS on the current baseline. These existing tests do not prove the missing contracts fixed.
- Controller rebound the historical frontend overlay to this worktree and ran `go test -overlay="$SCRATCH/lifecycle-spec-frontend-overlay.json" ./internal/plugins/frontends/frontendpipe -run '^TestAuditHoldaliveCanceledResultMustBeDisposed$' -count=1 -v`: expected FAIL, `closes=0`. This fresh negative assertion reproduces abandonment on the current source without repository test edits.
- The same current-worktree overlay's `TestAuditSourceMustRemainOpenDuringResponseConsumption` freshly failed as intended with `largebody: source is closed`.
- Current-worktree runtime overlay `$SCRATCH/lifecycle-spec-runtime-overlay.json` freshly ran `TestIndependentAuditA` and `TestIndependentAuditAProductionPump`: PASS assertions of the defect. No-extra-EOF / close-before-prefetch cases record `surfaced_failure`, while extra EOF / natural prefetch record `success`. These passes are not repaired-behavior evidence.
- Independent discovery also ran passing focused existing holdalive/storage and frontend keepalive tests. Historical defect probes and source rebaselining establish planning evidence, not implementation completion.
- Replay/outcome discovery ran fresh existing runtime ownership/replacement cases and `TestStockHost_OpenAIResponses_WireReachability_AndDeclineParity`: PASS. These establish harness viability, not the missing replacement/audit regressions.
- No runtime code was changed, no new regression was installed, and no whole-repository QA, remote race run, live provider request, charge-impact measurement, or Windows execution is claimed.
- Later implementation must make each regression fail first and prove repaired behavior through the real serving seams; the synthetic/mocked historical probes are not a replacement for the composed sentinel.
