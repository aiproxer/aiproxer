# Terminal-decision platform implementation baseline

Task 1.1, phase `1.1-current-baseline`. This is a pre-change, bounded characterization for
Requirements 11.1 and 11.2. Counts are semantic inventories; no source line number is part
of the baseline contract.

## Reproduction commands and current inventory

1. Terminal claim sites:
   `$h=@(rg -n --glob '*.go' --glob '!**/*_test.go' 'TerminalizeAttempt\(|terminalizeRequest\(' internal/core/runtime); @($h|Where-Object {$_ -notmatch 'func \('}).Count`
   reports **23 call sites**: 20 `TerminalizeAttempt` calls and 3 `terminalizeRequest`
   calls. The 20 attempt calls are grouped as: `agent_loop_guard_continuation.go` (2),
   `agent_loop_guard_gate.go` (2), `attempt_session.go` (9), `executor_open_attempt.go` (1),
   `executor_recv_loop.go` (1), `executor_settlement.go` (1), `parallel_race.go` (1), and
   `turn_terminal.go` (3). Request claims are in `executor_settlement.go` (1) and
   `turn_terminal.go` (2).

2. FeatureBundle contribution fields:
   ```powershell
   $f=Get-Content pkg/lipsdk/feature/bundle.go; $s=($f|Select-String '^type FeatureBundle struct').LineNumber; $e=($f|Select-String '^}').Where({$_.LineNumber -gt $s})[0].LineNumber; ($f[($s)..($e-2)]|Where-Object {$_ -match '^\s+[A-Z][A-Za-z0-9]*\s' -and $_ -notmatch 'SchemaVersion'}).Count
   ```
   Result: **25** fields (SubmitHooks, RequestPartHooks, ResponsePartHooks, ToolReactors,
   SessionOpeners, WorkspaceResolvers, ToolCatalogFilters, ToolCallPolicies,
   ToolCallFinalizers, ToolCallFinalizationMaxArgsBytes, RequestTransforms,
   PreRequestHandlers, RouteHintProviders, CompletionGates, AttemptTransforms,
   StreamObserverFactories, TrafficObservers, UsageObservers, RawCaptureSinks,
   TrafficRedactors, CompactionObservers, CompactionPreservers, SecretGuards,
   LocalTurnHandlers, Lifecycles).

3. Concrete ALG references in core:
   `@(rg -l --glob '*.go' --glob '!**/*_test.go' 'agent_loop_guard|AgentLoopGuard' internal/core).Count`
   results in **10 files**: `config/agent_loop_guard.go`, `config/model.go`,
   `config/validate.go`, `configreload/inventory.go`, `configreload/policy.go`,
   `runtime/agent_loop_guard_continuation.go`, `runtime/agent_loop_guard_gate.go`,
   `runtime/conversation_view.go`, `runtime/executor_recv_loop.go`, and
   `stopguardverify/adapter.go`.

4. Current semantic policy-owner fields (six):
   `internal/core/config.Config.AgentLoopGuard`;
   `internal/core/runtime.Executor.LoopGuardFactory`;
   `internal/core/runtime.turnTerminal.loopGuard`;
   `internal/core/runtime.loopguardRuntime.gate`;
   `internal/core/runtime.LoopGuardFactory.config`; and
   `internal/core/runtime.LoopGuardFactory.ports`. They are confirmed by:
   `rg -n --glob '*.go' --glob '!**/*_test.go' 'AgentLoopGuard\s+AgentLoopGuardConfig|LoopGuardFactory\s+\*?LoopGuardFactory|loopGuard\s+\*?LoopGuard|gate\s+\*?stopgate\.Gate|config\s+stopgate\.Config|ports\s+stopgate\.Ports' internal/core/config/model.go internal/core/runtime/executor.go internal/core/runtime/turn_terminal.go internal/core/runtime/agent_loop_guard_gate.go`.

5. Continuation cleanup:
   `$h=@(rg -n --glob '*.go' --glob '!**/*_test.go' 'deactivateGuardOverlay' internal/core/runtime); @($h|Where-Object {$_ -notmatch 'func \('}).Count`
   reports **24 cleanup call sites** (14 in `agent_loop_guard_continuation.go`, 9 in the
   single `retryRecvStream.Recv` implementation in `executor_recv_loop.go`, and 1 in
   `turnTerminal.terminalizeTurn`). The `turnTerminal.deactivateGuardOverlay` definition is
   excluded from the call-site count.

## No-provider and lifecycle characterization

`rg -n --glob '*.go' --glob '!**/*_test.go' 'ProvisionalTerminalProvider' pkg internal`
returns **0**: there is no provider field, merge reference, or provider call in the
pre-change platform. Existing no-provider behavior passes:
`go test ./internal/core/runtime -run '^TestAgentLoopGuard_GuardNil_DisabledBehaviorUnchanged$' -count=1`
→ `ok github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime` (exit 0; duration omitted).

Existing reload/publication order is evidenced by `manager.Publish` in
`internal/infra/runtimehost/manager.go`:
`assignPublishWithInstance → active.Swap → prior.markRetiring → scheduleRetire`.
Existing withdrawal order is evidenced by `retireGeneration` and
`finishRetireClose` in `internal/infra/runtimehost/retire.go`, then `Generation.Close` in
`internal/infra/runtimehost/generation_close.go`:
`BeginQuiesce → runQuiesce → MarkQuiesced → wait Drained → BeginClose → runCleanup → owned.Close`.
Reproduce symbol evidence with:
`rg -n 'assignPublishWithInstance|active\.Swap|markRetiring|scheduleRetire' internal/infra/runtimehost/manager.go`;
`rg -n 'BeginQuiesce|runQuiesce|MarkQuiesced|Drained|finishRetireClose|BeginClose|runCleanup' internal/infra/runtimehost/retire.go`;
`rg -n 'owned\.Close|GenClosed' internal/infra/runtimehost/generation_close.go`.

## Target counts and gate

The approved target is: **1** exclusive provider contribution, **1** core terminal
chokepoint, **0** concrete ALG policy branches in core, **1** process policy owner, and
**0** new generic runtime owners. Task 10.1 must compare implementation evidence against
these targets; unresolved net ownership/change-surface complexity is a no-go under the
ROI and Simplification Gate.

## Final implementation evidence

- `FeatureBundle` has one provider-neutral `TerminalDecisionProvider` contribution;
  composition is singular rather than a provider collection.
- Core has one generic terminal-decision chokepoint and one continuation transaction
  (`runContinuationTransaction`), while `terminaldecisionpolicy.Store` is the single
  process policy owner. Policy is snapshotted once at request admission and is not
  looked up on the continuation hot path.
- Production core has zero concrete imports or implementation references to
  `stopguard`, `stopgate`, `continuationsafety`, or `stopguardverify`. The legacy
  runtime ALG continuation/gate owners and their ALG-specific runtime tests/helpers
  are deleted; production core contains no concrete ALG identifier.
- The measured provider-neutral terminal-decision feature-extension overlay is 597
  production lines, with the 622-line architecture ratchet.
- Verification evidence: `go test ./internal/archtest -count=1`, `go test ./...`,
  and `make quality-checks` all pass.
