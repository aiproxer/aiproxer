# Design Document: claude-agent-acp-background

## Overview
This follow-up adds bounded lifecycle tracking for background tasks and subagents. Native Agent/Task tools are already available under the connector's default bypass policy; explicit permission deny retains archived tools-off behavior. Proposed `background_policy` controls tracking/visibility, not the shipped permission default. The connector remains the Claude-specific owner; shared ACP seams remain opt-in.

## Policy and enablement
`background_policy: deny|allow` joins `credential_source` and `admin_policy` in the connector configuration (example: `config/examples/claude-agent-acp.yaml` gains a disabled `allow` row). Enablement requires all of:

- the existing local-only posture and `admin_policy: honor` gate (unchanged);
- a background-enablement certification record bound to the exact profile digest (runtime tree + sidecar), analogous to the existing `r4-linux-amd64-deny` pin; a compiled-in constant (`backgroundCertifiedProfile`) names the digest that the resolver must observe;
- the connector's permission policy (default bypass); no separate permission-profile certification is required;
- an admin-policy re-check and identity re-check before the first spawn (existing `ContextStarter` path).

With background tracking off, the selected permission-policy metadata and environment stay unchanged. Under tracking `allow`, the same owners preserve:

- the default bypass built-in tool set, or tools-off restrictions when permission deny is explicitly selected; `agents` stays at the adapter-supported shape;
- the same strict MCP/plugins/settings posture and selected permission policy;
- the selected permission policy's environment: bypass omits `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS`, deny retains it.

Everything else (roots, credential sources, env allowlist, argv) is untouched. The `Disclosure` gains `background_policy` so `doctor`/resolved-profile evidence names the posture.

## Bounded lifecycle owner

The shipped minimum barrier lives in the connector-owned sidecar: no successful
prompt terminal until SDK idle and no live task; drain timeout/cancellation stops
SDK tasks before closing the query. Existing failed-admission stream close then
retires the runtime. The mechanisms below are proposed enhanced tracking, not a
prerequisite left missing from default bypass. Reuse must never weaken this barrier.

The bounded drain is not native persistent-background compatibility: a task still
alive after the five-second post-result deadline causes terminal failure and
retirement. A dedicated Linux subreaper owns detached descendants; SDK control
success is not termination proof. Pool and transport retirement expose join
points so instance cleanup cannot delete state while a peer shutdown is active.
Cleanup completion comes from verified supervisor process exit, never the writable
advisory marker. OS-reaper failure retains the supervisor/state and retries, so
retirement may remain pending indefinitely; unexpected owner death propagates a
sticky incomplete-cleanup error and forbids reuse. This fail-closed baseline does
not provide strict bounded containment. The five-second task drain restriction
is accepted for this slice; persistent background execution remains deferred.
### Runtime state
`SubprocessRuntime` (`connector-support/acp/runtime_pool.go`) gains connector-set background state via an additive, opt-in protocol interface:

```go
type BackgroundActivitySpec interface {
    // TrackBackground returns the background-activity tracker for a session.
    BackgroundTracker() BackgroundTracker
}
type BackgroundTracker interface {
    // Observe consumes an out-of-turn notification; ok reports whether it
    // described background activity the runtime must track.
    Observe(probe map[string]any) (active bool)
    // Quiesced reports whether tracked work has settled; FlushTimeout bounds it.
    Quiesced() bool
}
```

`claudeagentacp` implements the tracker against the adapter's verified notification kinds (task/subagent lifecycle, async task updates). Exact kinds are pinned by task 1 fixtures before wiring; unknown kinds are ignored by the tracker and fall through to the diagnostics path. With no spec opt-in, the pool behaves exactly as today.

### States and transitions
- A turn's `poolManagedStream` release path asks the tracker whether background work remains. If yes, the runtime is marked `backgroundActive`; `lastActivity` updates do not make it reapable.
- A later foreground turn on a `backgroundActive` runtime: the orchestrator waits up to `backgroundQuiesceDeadline` (new `RuntimePoolConfig` bound, default shared with the shutdown deadline) for `Quiesced()`; on timeout it kills the runtime, resets history, and spawns a replacement that replays the transcript (`computeHistoryAndUserMessage` reset path already exists).
- Cancel: the existing cancel profile runs, then the tracker's work is terminated by killing the runtime within the same claim, preserving exactly one terminal outcome.
- Close/idle reap/reconfigure drain: `KillRuntime` already terminates the process tree; the deadline bounds the wait for child exit and is surfaced in logs. No owned descendant may outlive the runtime.

### Out-of-turn diagnostics
`connector-support/acp` transport currently drops notifications when no prompt is active. Additive `TransportOptions.CaptureOutOfTurn` (test-visible, default false): when enabled (only the `allow` connector), unmatched notifications are handed to an optional `OutOfTurnObserver` set by the protocol; `claudeagentacp` maps tracker-relevant kinds via the tracker and everything else into bounded redacted `EventWarning`-style diagnostics on the *next* stream or the instance diagnostics log, with a fixed size cap and no raw payload retention. `deny` transports set neither flag (behavior unchanged).

## Error and terminal semantics
Existing classification is reused: post-output failures stay terminal (`ErrStreamTerminal`), pre-output failures keep failover eligibility, and background-specific stuck conditions surface as the existing capability/generic kinds with safe messages. The tracker never emits content events. `plan_exhausted`/`authentication_required` paths are unchanged.

## Certification
A background certification builds the packaged proxy + connector with a synthetic certified profile fixture (licensed install path already required) and records, against the exact digest: background start/completion between turns, out-of-turn visibility, cancel with active work, shutdown with active work, and stuck replacement. The enabling constant is updated only when all rows pass and the record is added to the spec's `research.md`/completion evidence; portable Go tests alone never enable `allow`.

## Testing strategy
Red-first per task: table tests for policy gating and meta emission; tracker unit tests over recorded notification fixtures; pool lifecycle tests (unsettled reuse wait, timeout replacement, cancel, close quiescence) using the existing fake-process harness; transport tests for opt-in capture; regression tests proving `deny` byte-identity and other connectors unchanged (cursor/agy/gemini suites). Integration: fake-adapter two-turn run with background between turns proving no interleaving and no content fabrication.

## Risks and mitigations
- Upstream notification shapes drift → pinned profile + fixtures; unknown kinds degrade to diagnostics, never state changes.
- Stuck detection false positives → bounded deadline only on claimed runtimes; replacement replays history (existing reset path) and is logged with a stable reason.
- Scope creep into steering/MCP → explicitly deferred; the tracker owns lifecycle only.
