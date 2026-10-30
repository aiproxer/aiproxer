# Implementation Gap Analysis: Server Crash Isolation

Generated: 2026-04-24

## Status (2026-04-25)

**Historical document.** The gap analysis below described the tree *before* implementation. The feature is now implemented: `internal/core/safety` supplies `PanicError` and `Call` / `CallValue`; HTTP recovery lives in `internal/stdhttp/recovery.go`; hook panics are converted in `internal/core/hooks`; backend and stream paths use safety in `internal/core/runtime` and `internal/core/stream/keepalive.go`; the `ListenAndServe` worker is guarded in `internal/stdhttp/server.go`; core extension pipeline stages in `internal/core/extensions` wrap plugin calls with the same safety helpers (completion-gate panics return to the runtime mapper and are not swallowed by fail-open). Use `tasks.md`, `design.md`, and package tests as the source of truth going forward.

---

## Executive Summary (original gap analysis)

The server-crash-isolation requirements are feasible within the existing small-core architecture, but the capability is not implemented today. The codebase currently relies on normal Go error returns for expected failures and B2BUA pre-output recovery for classified backend errors; it does not have panic containment boundaries for HTTP requests, hooks, backend attempts, stream workers, or owned server goroutines.

Key findings:

- No production Go code currently uses `recover`, so unexpected panics are not converted into bounded request, attempt, or worker failures.
- Existing HTTP, runtime, hook, backend, stream, diagnostics, and metrics packages provide useful integration points, but each boundary needs explicit panic-to-error behavior.
- Streaming and routing invariants are the main design constraint: containment must not create a second non-streaming execution path, silently retry after output starts, or hide capability mismatches.
- A hybrid approach is likely strongest: add small reusable safety primitives, then integrate them at existing HTTP, hook, backend, stream, and worker seams.
- Requirements are generated but not approved yet; this gap analysis can inform revisions and the design phase.

## Context Loaded

- Spec metadata: `.kiro/specs/archive/server-crash-isolation/spec.json`
- Requirements: `.kiro/specs/archive/server-crash-isolation/requirements.md`
- Steering: `.kiro/steering/product.md`, `.kiro/steering/tech.md`, `.kiro/steering/structure.md`, `.kiro/steering/routing-and-orchestration.md`
- Gap analysis rules: `.opencode/skills/kiro-validate-gap/rules/gap-analysis.md`
- Codebase exploration: HTTP server/middleware, frontend handlers, runtime executor, hook bus, backend seam, stream keepalive, metrics, diagnostics, goroutine quality gate

## Current State Investigation

### Architecture and Ownership Patterns

The repository is organized as a small Go runtime plus explicit plugins:

- `internal/stdhttp/` owns standard HTTP serving and handler composition.
- `internal/core/http/` owns shared HTTP middleware helpers such as trace/request ID and response status recording.
- `internal/core/runtime/` owns request execution, routing, B2BUA attempt lineage, and backend attempt orchestration.
- `internal/core/hooks/` owns hook dispatch and validation for submit, request-part, response-part, and tool reactor chains.
- `internal/core/stream/` owns canonical stream helpers such as keepalive and collection.
- `internal/plugins/frontends/*` decode incoming protocol requests and encode canonical events back to protocol responses.
- `internal/plugins/backends/*` translate canonical requests to upstream provider calls and return canonical event streams.

This aligns with steering: core owns orchestration and plugin seams; provider and transport-specific details stay at the edge; non-streaming remains collection over the canonical event stream.

### Existing HTTP Server and Middleware Assets

Relevant files:

- `internal/stdhttp/server.go:185` builds the final middleware chain.
- `internal/stdhttp/server.go:201` constructs `http.Server` with the composed handler.
- `internal/stdhttp/server.go:212` starts `srv.ListenAndServe()` in an allowlisted goroutine.
- `internal/core/http/middleware.go:12` injects trace IDs and request IDs.
- `internal/core/http/status_recorder.go:14` records response status and forwards streaming-related optional interfaces.
- `internal/stdhttp/accesslog.go:16` records access logs using `ResponseStatusRecorder`.
- `internal/infra/metrics/registry.go:90` records HTTP request metrics with `ResponseStatusRecorder`.
- `internal/core/diag/auth.go:15` protects diagnostics routes with a shared secret when configured.

Current behavior:

- Middleware wraps `next.ServeHTTP`, but no layer recovers from panic.
- `ResponseStatusRecorder` can detect whether `WriteHeader` was called, which is useful for distinguishing pre-commit and post-commit responses.
- Existing middleware preserves `http.Flusher`, `http.Hijacker`, `http.Pusher`, and `io.ReaderFrom`, which is important for streaming compatibility.

Gap:

- Requirement 1 cannot be met because a panic in a handler or middleware is not converted into a request-scoped failure by repository-owned code.
- HTTP metrics and access logs may also miss observations if a panic unwinds past them before they record completion.

### Existing Frontend Handler Assets

Relevant files:

- `internal/plugins/frontends/openairesponses/handler.go:47`
- `internal/plugins/frontends/openailegacy/handler.go:46`
- `internal/plugins/frontends/anthropic/handler.go:49`
- `internal/plugins/frontends/gemini/handler.go:47`
- `internal/plugins/frontends/execerr/execerr.go:10` defines safe internal wire messages for executor failures.
- `internal/plugins/frontends/execerr/execerr.go:34` maps executor errors to frontend-facing outcomes.

Current behavior:

- Frontend handlers validate method/path, read the request body, decode to canonical calls, execute the runtime, and encode stream or non-stream output.
- Expected executor errors are classified and converted to protocol-specific safe wire errors.
- Streaming handlers call protocol-specific SSE writers directly.

Gap:

- There is no local panic containment in frontend handlers.
- A root HTTP recovery middleware could contain handler panics before output starts, but post-output stream panics need careful behavior because headers may already be committed.
- Frontend error formatting is protocol-specific, so a single generic HTTP recovery response may be less precise than frontend-local error mapping for some pre-output failures.

### Existing Runtime Executor and Backend Attempt Assets

Relevant files:

- `internal/core/runtime/executor.go:90` starts canonical execution.
- `internal/core/runtime/executor.go:107` starts and records an OpenTelemetry execution span for returned errors.
- `internal/core/runtime/executor_open_attempt.go:49` plans and opens one backend attempt.
- `internal/core/runtime/executor_open_attempt.go:151` invokes `be.Open`.
- `internal/core/runtime/attempt_stream.go:117` receives events from a backend stream.
- `internal/core/runtime/attempt_stream.go:148` invokes `inner.Recv`.
- `internal/core/runtime/attempt_stream.go:182` marks the attempt committed once output is client-visible.
- `internal/core/runtime/attempt_stream.go:242` distinguishes committed or non-recoverable errors from recoverable pre-output failures.
- `internal/core/execbackend/backend.go:14` defines the executor-consumed backend seam.

Current behavior:

- Expected backend open/recv errors can be classified as recoverable pre-output errors through `lipapi.IsRecoverablePreOutput`.
- Once output is committed, transparent failover is disabled and errors are surfaced.
- Attempt lineage records success, swallowed failure, surfaced failure, and cancellation outcomes.
- Backend capabilities are resolved before execution through `ResolveCaps` or static `Caps`.

Gap:

- Panic in `ResolveCaps`, `be.Open`, `inner.Recv`, or `inner.Close` is not converted to a classified runtime error.
- Panic before output cannot currently participate in existing bounded failover because it is not translated into a recoverable pre-output failure.
- Panic after output could bypass lineage recording and stream cleanup.
- Panic during capability resolution could skip explicit capability rejection and crash rather than fail safely.

### Existing Hook and Extension Assets

Relevant files:

- `internal/core/hooks/bus.go:26` stores immutable sorted hook chains after construction.
- `internal/core/hooks/submit.go:13` runs submit hooks.
- `internal/core/hooks/submit.go:34` invokes `h.Handle`.
- `internal/core/hooks/parts.go:12` runs request-part hooks.
- `internal/core/hooks/parts.go:24` invokes `h.HandleRequestParts`.
- `internal/core/hooks/parts.go:38` runs response-part hooks.
- `internal/core/hooks/parts.go:50` invokes `h.HandleEvent`.
- `internal/core/hooks/tool.go:24` applies tool reactors.
- `internal/core/hooks/tool.go:41` invokes `r.HandleToolEvent`.
- `pkg/lipsdk/hooks/` defines hook failure modes and tool reactor error policies.

Current behavior:

- Hooks and reactors use normal `error` returns.
- Submit and part hooks support fail-open behavior when an error is returned.
- Tool reactors support fail-closed, swallow-event, and default fail-open behavior.
- Hook chains are sorted and expected to be immutable after construction.

Gap:

- Panic is not normalized into an error, so configured hook failure behavior does not apply to panics.
- Panic can bypass post-hook canonical validation and request/stream safety checks.
- Panic from a fail-open hook currently does not continue safely; it escapes the runtime.

### Existing Stream Worker and Goroutine Assets

Relevant files:

- `internal/core/stream/keepalive.go:41` documents the keepalive wrapper and its single background reader goroutine.
- `internal/core/stream/keepalive.go:119` starts the reader goroutine.
- `internal/core/stream/keepalive.go:127` intentionally uses a detached cancel tree for inner reads.
- `internal/core/stream/keepalive.go:221` closes and aborts in-flight reads.
- `scripts/check-adhoc-goroutines.sh:18` allowlists only `internal/stdhttp/server.go` and `internal/core/stream/keepalive.go` for non-test goroutine creation.

Current behavior:

- Goroutine creation is intentionally restricted.
- Keepalive has explicit cancellation/close mechanics and a single reader goroutine per wrapped stream.
- `srv.ListenAndServe` is run in a single server-level goroutine and reports normal errors to `errCh`.

Gap:

- Panic in the keepalive reader goroutine is not surfaced to the affected stream and may terminate the process.
- Panic in the `ListenAndServe` goroutine is not converted to a server-level error on `errCh`.
- There is no general owned-worker supervision convention.
- Adding new goroutines for containment would conflict with the goroutine quality gate unless justified and allowlisted.

### Existing Diagnostics and Metrics Assets

Relevant files:

- `internal/core/diag/` provides trace/call correlation and route trace diagnostics.
- `internal/core/diag/route_trace.go:20` has a lock-protected bounded route trace buffer.
- `internal/core/runtime/metrics_sink.go:6` defines executor metrics observations.
- `internal/infra/metrics/registry.go:89` provides HTTP metrics middleware.
- `internal/plugins/frontends/execerr/execerr.go:10` provides safe client-facing internal error text.

Current behavior:

- Routing decisions, attempts, cancellations, and executor activity are logged or observable through existing paths.
- HTTP metrics classify response status by bounded status class labels.
- Attempt metrics cover recorded attempt outcomes and backend open duration.

Gap:

- There is no stable panic/isolated-crash error type, log attribute, metric, or diagnostic category.
- Panics that bypass normal error returns may skip existing OTEL error recording and attempt metrics.
- Client-safe error behavior exists for executor errors, but not for panic recovery paths outside executor errors.

### Tests and Quality Gates

Relevant assets:

- `internal/stdhttp/server_test.go` covers startup, shutdown, mount failures, and serve errors.
- `internal/core/hooks/*_test.go` covers hook ordering, fail-open/fail-closed, validation, and tool policies.
- `internal/core/runtime/*_test.go` covers routing, attempts, backend credentials, cancellation, and execution semantics.
- `internal/core/stream/*_test.go` covers stream cancellation and keepalive behavior.
- `scripts/check-adhoc-goroutines.sh` and `.ps1` enforce goroutine spawn discipline.

Gap:

- No tests assert panic containment at HTTP, hook, backend, stream, or worker boundaries.
- No tests verify post-output panic does not retry/fail over.
- No tests verify future requests still work after an isolated panic.
- No quality gate enforces that owned goroutines are supervised.

## Requirement-to-Asset Map

| Requirement | Existing assets | Gap status | Notes |
| --- | --- | --- | --- |
| Req 1: Request and connection crash containment | `internal/stdhttp/server.go`, `internal/core/http/middleware.go`, `internal/core/http/status_recorder.go`, frontend handlers | Missing | No panic recovery middleware or handler-local containment. `ResponseStatusRecorder` can help identify committed responses. |
| Req 2: Extension boundary containment | `internal/core/hooks/*`, `pkg/lipsdk/hooks/*` | Missing | Error policies exist, but panics bypass them. Need panic-to-error normalization around hook invocations. |
| Req 3: Backend attempt containment | `internal/core/runtime/executor_open_attempt.go`, `internal/core/runtime/attempt_stream.go`, `internal/core/execbackend/backend.go`, `lipapi` failure classification | Missing / Constraint | Error-based pre-output recovery exists. Panic integration must preserve no-retry-after-output and attempt budgets. |
| Req 4: Shared runtime state integrity | immutable hook bus pattern, route trace locking, attempt lineage store seams | Partial / Constraint | Some shared state is already lock-protected or immutable by convention. Panic paths may bypass cleanup/lineage consistency. |
| Req 5: Owned worker containment | `internal/core/stream/keepalive.go`, `internal/stdhttp/server.go`, goroutine allowlist script | Missing / Constraint | Existing goroutine inventory is intentionally tiny. Need supervision without adding unowned goroutines. |
| Req 6: Operator diagnostics | `diag`, `execerr`, `MetricsSink`, HTTP metrics | Partial | Safe wire messages and correlation exist. No isolated-crash classification or metrics/log convention. |

## Key Technical Needs

- A stable internal representation for recovered panics that can be logged, wrapped, classified, and optionally converted to recoverable pre-output failure.
- HTTP-level containment that preserves streaming interfaces and distinguishes pre-response from committed-response behavior.
- Hook and tool reactor panic normalization that honors existing configured failure behavior.
- Backend open/recv/close/capability panic normalization that preserves B2BUA semantics and the no-retry-after-output rule.
- Worker supervision for existing owned goroutines without expanding the goroutine surface area.
- Diagnostics that distinguish isolated crash failures from validation errors, expected upstream failures, and ordinary cancellations.
- Regression tests covering per-request isolation, post-output behavior, fail-open/fail-closed extension behavior, backend failover eligibility, and goroutine containment.

## Implementation Approach Options

### Option A: Extend Existing Components Only

This option adds `defer recover` blocks directly inside existing HTTP, hook, backend, and stream functions with minimal new types.

Files/modules likely touched:

- `internal/stdhttp/server.go`
- `internal/core/hooks/submit.go`
- `internal/core/hooks/parts.go`
- `internal/core/hooks/tool.go`
- `internal/core/runtime/executor_open_attempt.go`
- `internal/core/runtime/attempt_stream.go`
- `internal/core/stream/keepalive.go`
- package-local tests in each affected package

Compatibility assessment:

- Minimal public API change if all new behavior remains internal.
- Lower risk of over-abstracting.
- Easy to place recovery exactly at each call site.

Trade-offs:

- Pros: smallest footprint, fastest path to initial behavior, no new central abstraction.
- Cons: duplicated panic formatting and logging; higher chance of inconsistent classification across boundaries; harder to test common behavior.

Effort: M (3-7 days). Risk: Medium. The behavior is spread across multiple packages and streaming edge cases are subtle.

### Option B: Create New Safety Components

This option creates a small internal safety package or runtime-local safety component that provides panic-to-error conversion and optional goroutine supervision primitives, then callers integrate it.

Potential new assets:

- `internal/core/safety` or narrowly scoped equivalents near consumers.
- A typed recovered-panic error with component/phase metadata and stack capture for logs.
- Helpers for safe function invocation and owned goroutine execution.

Integration points:

- HTTP middleware can use the shared recovered-panic type for logs/diagnostics.
- Hook bus can convert panics to ordinary errors and reuse existing failure policies.
- Runtime backend paths can map recovered panics into pre-output or post-output failures.
- Keepalive can surface recovered panic as a stream error item.

Compatibility assessment:

- Can remain fully internal with no public `pkg/lipapi` or `pkg/lipsdk` changes if design chooses internal classification only.
- Helps create consistent diagnostics across layers.

Trade-offs:

- Pros: consistent classification, easier common tests, cleaner diagnostics, less duplication.
- Cons: introduces a new cross-cutting core concept; must avoid becoming a generic framework or leaking transport/provider details.

Effort: M to L (4-10 days). Risk: Medium. The primitive is simple, but boundary-specific semantics still need careful integration.

### Option C: Hybrid Boundary-Focused Approach

This option creates only the minimal shared safety primitives, then integrates them explicitly at existing seam boundaries with package-local behavior.

Combination strategy:

- Add a small internal recovered-panic error/helper for consistent metadata and stack-safe logging.
- Add HTTP recovery as a middleware at the standard HTTP boundary.
- Add hook invocation wrappers inside `internal/core/hooks` so existing fail-open/fail-closed policies also apply to panics.
- Add backend open/recv/capability wrappers inside `internal/core/runtime` so panics align with attempt lineage and output-commit semantics.
- Add keepalive goroutine containment in `internal/core/stream` without adding new unowned goroutines.
- Extend metrics/logging only enough to distinguish isolated crashes from normal errors.

Compatibility assessment:

- Preserves existing package ownership and avoids public contract churn.
- Keeps panic semantics close to the core seam that can interpret them.
- Best fit with steering: small core, explicit seams, no broad framework.

Trade-offs:

- Pros: balanced separation, consistent panic representation, boundary-specific semantics, testable incrementally.
- Cons: requires disciplined design to keep shared safety helper small and avoid duplicate policy in HTTP and runtime layers.

Effort: L (1-2 weeks). Risk: Medium. Scope spans several hot-path packages, but existing seams are clear.

## Integration Challenges

### Streaming Commit Semantics

HTTP-level panic recovery alone cannot satisfy all requirements. Once SSE or streaming output has started, the system must not attempt to write a clean new 500 body or silently retry another backend. Runtime-level commit knowledge (`lipapi.OutputCommitted`) and HTTP response commit knowledge (`ResponseStatusRecorder.Status`) both matter.

Design phase should clarify how to reconcile:

- canonical output commitment in `attempt_stream.go`, and
- protocol/HTTP header commitment in frontend encoders.

### Panic vs Recoverable Pre-Output Error

Existing B2BUA recovery is based on typed errors. Requirements want backend pre-output panics to be eligible for existing bounded failover, but not all panics are equally safe to retry. The design needs a precise rule for when a recovered backend panic becomes a recoverable pre-output attempt failure.

### Diagnostics Without Sensitive Leakage

The system needs stack traces or panic values for operator diagnostics, but client responses must remain safe. The design should decide which metadata is retained in error values, which is logged, and which is exposed through diagnostics.

### Shared State Consistency

Attempt lineage, route trace buffers, hook chain immutability, and runtime snapshots are shared or cross-request visible. The design should identify state transitions that need `defer`-based cleanup or outcome recording when a panic is recovered.

### Worker Inventory

The obvious production goroutines are `srv.ListenAndServe` and stream keepalive readers. Design should confirm whether optional tracing/metrics SDKs, lifecycle plugins, or backend SDKs start owned workers that need project-level supervision or are outside this feature's scope.

## Research Needed for Design Phase

- Confirm Go `net/http` panic behavior under the current Go toolchain and whether standard server recovery semantics are sufficient or whether repository-owned recovery is still needed for response shaping and diagnostics.
- Inventory all non-test goroutine creation paths, including generated or indirect long-lived workers from observability setup and lifecycle plugins.
- Decide whether recovered panic classification belongs in a new internal package, inside `internal/core/runtime`, or split by boundary.
- Determine whether metrics require new counters/labels or whether structured logs plus existing attempt/HTTP metrics are sufficient for Req 6.
- Verify frontend stream encoders' behavior when their underlying event stream returns a recovered-panic error after partial output.

## Preferred Direction for Design Consideration

The gap analysis favors Option C as the most balanced path, but this is not a final implementation decision. It fits the repository's pragmatic hexagonal guidance: add a narrow internal safety seam only where it improves boundary ownership, then keep policy decisions at the consuming boundary.

Hexagonal review constraints for design:

- Keep `safety` as an inward core helper; it must not own HTTP response formatting, logging sinks, hook policy, or routing decisions.
- Put concrete HTTP recovery in `internal/stdhttp`, not in shared `internal/core/http`, because HTTP recovery is driving-adapter behavior.
- Keep backend panic mapping in `internal/core/runtime`, where the executor consumes the backend port and owns attempt policy.
- Keep hook panic normalization in `internal/core/hooks`, where hook failure policies are consumed.
- Avoid new exported `pkg/lipapi` / `pkg/lipsdk` panic types unless existing contracts cannot preserve semantics.
- Avoid adapter-defined interfaces or mock-only ports; use concrete helpers and package-local functions unless a real substitution boundary exists.

Design should evaluate these key decisions:

1. What is the minimal recovered-panic error shape?
2. Which recovered panics are eligible for pre-output backend failover?
3. How does HTTP recovery detect and handle committed responses?
4. How are hook fail-open/fail-closed policies applied to panics?
5. What diagnostics are mandatory versus optional when observability is disabled?

## Complexity and Risk

- Overall effort: L (1-2 weeks).
- Overall risk: Medium.

Justification: The codebase already has clear seams for HTTP, hooks, backend attempts, streams, diagnostics, and tests. The main risk is semantic rather than technical: preserving streaming ordering, no-retry-after-output, lineage consistency, and safe client responses while introducing panic containment across several hot paths.

## Suggested Validation Targets

- HTTP request panic before response: returns safe 500 and subsequent request succeeds.
- HTTP/stream panic after output: affected stream stops and no transparent retry occurs.
- Submit/request/response/tool hook panic: configured failure behavior is honored.
- Backend `ResolveCaps`, `Open`, and `Recv` panic: pre-output vs post-output semantics are preserved.
- Keepalive reader panic: affected stream receives a bounded failure and the process stays alive.
- Attempt diagnostics distinguish isolated crash attempts from ordinary upstream failures.
- Goroutine quality gate remains passing without new unowned goroutines.

---

# Design Discovery Addendum: Server Crash Isolation

Generated: 2026-04-24T11:08:33.3428035+02:00

## Summary

- **Feature**: `server-crash-isolation`
- **Discovery Scope**: Extension / complex integration. The feature extends existing HTTP, hook, runtime, stream, and diagnostics paths without adding a new user-facing protocol.
- **Key Findings**:
  - Panic isolation can remain internal; no `pkg/lipapi` or `pkg/lipsdk` contract change is required for the initial design.
  - Existing `lipapi.UpstreamFailure`, `ErrRecoverablePreOutput`, `AttemptOutcome`, and `EventStream` contracts are sufficient to preserve B2BUA and streaming semantics.
  - A small internal safety component plus explicit boundary integrations is the simplest design that avoids duplicated `recover` semantics.

## Research Log

### Existing Contract Compatibility
- **Context**: Determine whether crash isolation requires public canonical or plugin SDK changes.
- **Sources Consulted**: `pkg/lipapi/upstream.go`, `pkg/lipapi/lineage.go`, `pkg/lipapi/events.go`, `pkg/lipsdk/hooks/*.go`.
- **Findings**:
  - `lipapi.UpstreamFailure` already models pre-output vs post-output failures and recoverability.
  - `lipapi.AttemptOutcome` already models swallowed vs surfaced failures; panic can be captured in `AttemptRecord.Reason` instead of a new enum.
  - Hook failure modes and tool reactor policies are error-driven; converting panics to errors lets current policy semantics apply.
- **Implications**:
  - Keep recovered panic types internal to avoid public API churn.
  - Use existing error wrapping and `errors.Is`/`errors.As` behavior rather than introducing exported panic sentinels.

### Integration Points
- **Context**: Identify files and seams that must be implementation-ready in design.md.
- **Sources Consulted**: `internal/stdhttp/server.go`, `internal/core/http/status_recorder.go`, `internal/core/hooks/*`, `internal/core/runtime/*`, `internal/core/stream/keepalive.go`, `internal/infra/metrics/*`.
- **Findings**:
  - HTTP recovery belongs at the standard HTTP boundary and can use `ResponseStatusRecorder` to detect committed responses.
  - Hook recovery belongs inside `internal/core/hooks` so fail-open/fail-closed behavior remains centralized.
  - Backend `Open`, `Recv`, capability resolution, and cleanup close paths are separate runtime boundaries with different post-output behavior.
  - Keepalive has the only request-scoped owned goroutine and must surface panic as a stream error item instead of closing silently.
- **Implications**:
  - Design should use package-local wrappers around a shared internal recovered-panic type.
  - Tasks can be parallelized by boundary after the internal safety contract is established.

### Metrics and Diagnostics
- **Context**: Determine how isolated crashes become operator-visible without leaking details to clients.
- **Sources Consulted**: `internal/infra/metrics/registry.go`, `internal/infra/metrics/executor_prom.go`, `internal/infra/metrics/extension_stages.go`, `internal/core/runtime/metrics_sink.go`, `internal/plugins/frontends/execerr/execerr.go`.
- **Findings**:
  - HTTP metrics are status-class based and can record recovered pre-response panics as `5xx`.
  - Executor metrics are attempt-outcome based; panic does not need a new outcome label if attempt reason/log attributes identify boundary class.
  - Existing safe client message is `internal error`; stack traces should remain log-only.
- **Implications**:
  - Structured logs should carry `panic_boundary`, `panic_value_type`, and trace/attempt identifiers.
  - Prometheus label cardinality should remain bounded; avoid panic-value or hook/backend IDs as high-cardinality labels unless already bounded by existing metrics.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| Direct per-site recovery | Add `defer recover` at each panic boundary | Minimal new files | Duplicated formatting and inconsistent diagnostics | Useful for tiny patches, weaker for this cross-cutting feature |
| Central safety framework | Add broad helper package for all recovery and goroutine supervision | Consistent classification | Risk of framework creep and misplaced policy | Too broad for current scope |
| Hybrid boundary-focused safety | Add minimal internal recovered-panic helpers and integrate at existing seams | Consistent, testable, respects boundaries | Requires discipline to keep policy local | Selected design direction |

## Design Decisions

### Decision: Keep Panic Classification Internal
- **Context**: Recovered panic metadata is needed for logs, metrics, and error handling.
- **Alternatives Considered**:
  1. Add exported `lipapi` panic error or attempt outcome.
  2. Use only plain `fmt.Errorf` at each site.
  3. Add internal recovered-panic type and preserve public contracts.
- **Selected Approach**: Add an internal recovered-panic error type that records boundary class, operation, and stack for logs, while mapping to existing public error contracts where needed.
- **Rationale**: Satisfies diagnostics and classification without expanding stable public APIs.
- **Trade-offs**: External plugins cannot detect recovered panics as a stable exported type; that is acceptable because this feature is server-owned containment.
- **Follow-up**: Implementation must ensure client-facing messages never include stack traces or raw panic values.

### Decision: Preserve Existing Attempt Outcomes
- **Context**: Backend panics must be visible in attempt diagnostics without adding a new lineage enum.
- **Alternatives Considered**:
  1. Add `panic_failure` to `lipapi.AttemptOutcome`.
  2. Encode panic details in `AttemptRecord.Reason` and structured logs.
- **Selected Approach**: Use `AttemptSwallowedFailure` or `AttemptSurfacedFailure` according to existing pre-output/post-output semantics, with bounded reason text and structured log attributes identifying isolated crash boundaries.
- **Rationale**: Avoids public contract churn and keeps dashboards compatible.
- **Trade-offs**: Consumers must inspect reason/logs to distinguish panic from ordinary upstream failure.
- **Follow-up**: Consider a future public enum only if operators need stable external querying by panic category.

### Decision: Build Minimal Helpers Instead of Adopting a Library
- **Context**: Panic recovery uses Go built-ins and must integrate tightly with domain semantics.
- **Alternatives Considered**:
  1. Adopt third-party HTTP recovery middleware.
  2. Build small internal helpers using standard library `recover` and `runtime/debug.Stack`.
- **Selected Approach**: Build minimal internal helpers.
- **Rationale**: The project favors stdlib and needs runtime-specific mapping to B2BUA, hooks, streams, and diagnostics.
- **Trade-offs**: The project owns helper correctness.
- **Follow-up**: Tests must cover all boundary mappings.

## Risks & Mitigations

- Post-output panic accidentally writes a second error response — use committed response and `lipapi.OutputCommitted` state to stop the affected stream without retrying.
- Panic recovery hides programming defects — log structured boundary details and keep recovery limited to explicit trust boundaries.
- Inconsistent panic handling across packages — use one internal recovered-panic type and package-local wrappers.
- Metrics cardinality growth — keep labels bounded and place variable detail in logs or attempt reason text.
- Goroutine supervision expands worker surface — do not add new goroutines; supervise only existing owned goroutines.

## References

- `.kiro/steering/product.md` — small core, explicit plugin seams, observable recovery.
- `.kiro/steering/tech.md` — stdlib preference, streaming-first, no retry after first visible output.
- `.kiro/steering/structure.md` — package ownership and where to change code.
- `.kiro/steering/routing-and-orchestration.md` — B2BUA pre-output recovery and attempt lineage rules.
