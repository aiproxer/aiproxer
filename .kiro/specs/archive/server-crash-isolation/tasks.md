# Implementation Plan

**Spec status:** Implementation complete; all in-scope tasks done. **Location:** `.kiro/specs/archive/server-crash-isolation/` (see `spec.json` for `phase`, `implementation_complete`, `archived_at`).

- [x] 1. Establish recovered-panic foundation
- [x] 1.1 Define the internal recovered-panic contract
  - Add a bounded internal panic classification model with boundary, operation, safe value type, and stack metadata.
  - Ensure the error text is safe for wrapping and never includes raw panic values or stack traces.
  - Keep the contract concrete and internal; do not add public `lipapi` / `lipsdk` panic types or adapter-defined interfaces.
  - Add unit tests proving panic metadata is captured and client-safe error text is stable.
  - Done when core packages can create and inspect recovered-panic errors without public API changes.
  - _Requirements: 6.1, 6.2, 6.3, 6.6_
  - _Boundary: RecoveredPanic_

- [x] 1.2 Add shared isolated-crash diagnostics helpers
  - Provide bounded structured diagnostics for recovered panics using existing trace and attempt context where available.
  - Keep stack traces server-side and prevent panic values from becoming metrics labels or client-visible text.
  - Return diagnostic attributes to callers; do not create a package-level logger or move logging sink ownership into core helpers.
  - Add tests proving diagnostic attributes include boundary class and omit sensitive stack details from safe fields.
  - Done when recovery boundaries can emit one consistent isolated-crash diagnostic record.
  - _Requirements: 6.1, 6.3, 6.5, 6.6_
  - _Boundary: CrashDiagnostics_

- [x] 2. Add HTTP request containment
- [x] 2.1 Implement HTTP request recovery behavior
  - Recover request-handler panics at the standard HTTP boundary using the recovered-panic contract.
  - Return a safe internal error before response commitment and avoid writing a second response after commitment.
  - Preserve streaming-related response writer capabilities through existing recorder behavior.
  - Implement concrete HTTP recovery in `internal/stdhttp`; keep `internal/core/runtime` free of HTTP response types.
  - Done when a panicking handler produces a bounded request outcome instead of escaping the server handler chain.
  - _Requirements: 1.1, 1.2, 1.5, 6.2, 6.5_
  - _Boundary: HTTPRecoveryMiddleware_
  - _Depends: 1.1, 1.2_

- [x] 2.2 Wire recovery into the HTTP middleware chain
  - Install recovery at the concrete chain point between transport auth and access logging while keeping request ID, metrics, and tracing outside it.
  - Add tests proving a recovered request panic returns 500, a later request succeeds, and enabled access log or HTTP metrics observes a 5xx result.
  - Done when the standard HTTP server contains request panics without losing status observability.
  - _Requirements: 1.3, 1.6, 6.3, 6.5_
  - _Boundary: HTTPRecoveryMiddleware, HTTPMetricsIntegration_
  - _Depends: 2.1_

- [x] 2.3 Add server-level worker panic reporting
  - Guard the existing server-level serve worker and route recovered panics through the server error path.
  - Make the expected branch explicit: the serve worker is correctness-critical, so a recovered panic is reported as a server-level error for existing shutdown coordination rather than ignored as a request failure.
  - Add a test proving a serve worker panic is observable through the same error path used for serve failures.
  - Done when server-level worker panics are reported distinctly from request-level recovery.
  - _Requirements: 5.2, 5.5, 6.3, 6.6_
  - _Boundary: OwnedWorkerGuard_
  - _Depends: 1.1, 1.2_

- [x] 3. Contain extension and hook panics
- [x] 3.1 Apply panic containment to submit and request-part hooks
  - Convert submit and request-part hook panics into ordinary hook errors.
  - Preserve fail-open and fail-closed behavior after panic conversion, including existing validation before fail-open continuation.
  - Keep hook policy in `internal/core/hooks`; do not add adapter-owned hook interfaces solely for tests.
  - Add tests for fail-open continuation, fail-closed surfacing, and stable hook ordering after a panic.
  - Done when request preparation hook panics follow configured extension behavior without destabilizing unrelated requests.
  - _Requirements: 2.1, 2.4, 2.5, 4.5_
  - _Boundary: HookPanicBoundary_
  - _Depends: 1.1_

- [x] 3.2 Apply panic containment to response-part hooks and tool reactors
  - Convert response event hook and tool reactor panics into ordinary errors consumed by existing stream policies.
  - Preserve fail-open, fail-closed, and swallow-event behavior after panic conversion.
  - Add tests for affected-stream isolation, tool policy handling, and no hook ordering mutation.
  - Done when stream-time extension panics affect only the active stream event or request according to configured policy.
  - _Requirements: 2.2, 2.3, 2.4, 2.5, 4.5_
  - _Boundary: HookPanicBoundary_
  - _Depends: 3.1_

- [x] 3.3 Apply panic containment to core extension pipeline stages (`internal/core/extensions`)
  - Wrap session openers, tool catalog filters, request transforms, route hint providers, and completion gate `Handle` calls with `internal/core/safety` (`Call` / `CallValue`).
  - Fail-open stages skip on returned errors as before; completion-gate panics return immediately so the runtime stream mapper preserves pre-output vs post-output semantics (fail-open does not swallow gate panics).
  - Add regression tests in `internal/core/extensions/extension_stages_panic_test.go`.
  - Done when extension stages cannot unwind the process and follow the same failure-mode conventions as hooks where applicable.
  - _Requirements: 2.1, 2.4, 2.5, 4.5_
  - _Boundary: ExtensionStagePanicBoundary_
  - _Depends: 1.1, 3.2_

- [x] 4. Contain backend attempt and runtime stream panics
- [x] 4.1 Define runtime backend panic classification rules
  - Map recovered backend panics to recoverable pre-output failures only when output is not committed.
  - Map committed-output panics to non-recoverable surfaced stream failures.
  - Keep the mapper protocol-neutral; do not branch on HTTP status, provider SDK types, vendor enums, SQL, or ORM details.
  - Add unit tests proving committed output never maps to recoverable pre-output behavior.
  - Done when runtime panic mapping preserves no-retry-after-output and existing bounded failover semantics.
  - _Requirements: 1.2, 3.1, 3.2, 3.5, 4.3_
  - _Boundary: RuntimePanicMapper_
  - _Depends: 1.1_

- [x] 4.2 Ensure executor entry observations include recovered failures
  - Ensure recovered failures that reach the executor entry path are recorded by the existing execution span and error observation behavior.
  - Add a focused test or characterization proving recovered runtime errors are observable at the executor boundary without adding a new execution path.
  - Done when executor-level observability reports recovered failures consistently with ordinary returned errors.
  - _Requirements: 6.1, 6.3, 6.5_
  - _Boundary: RuntimePanicMapper, CrashDiagnostics_
  - _Depends: 1.2, 4.1_

- [x] 4.3 Apply containment to backend capability and open boundaries
  - Recover backend capability and open panics at runtime-owned seams.
  - Record pre-output open panics as swallowed or surfaced attempt outcomes according to existing failover policy.
  - Preserve backend open duration observation when a panic occurs during open.
  - Do not change backend plugin interfaces or require backend plugins to import `internal/core/safety`.
  - Add tests for explicit capability failure and successful failover after a pre-output open panic.
  - Done when backend capability and open panics cannot escape the executor and attempt lineage remains visible.
  - _Requirements: 3.1, 3.3, 3.4, 3.5, 6.4_
  - _Boundary: BackendPanicBoundary_
  - _Depends: 4.1, 4.2_

- [x] 4.4 Apply containment to backend receive boundaries
  - Recover backend receive panics and route them through pre-output or post-output handling based on active stream commit state.
  - Preserve deterministic event ordering for events already emitted before a recovered receive panic.
  - Add tests for pre-output receive failover, post-output panic surfacing, and no transparent retry after output.
  - Done when backend receive panics preserve lineage, ordering, and no-retry-after-output semantics.
  - _Requirements: 3.1, 3.2, 3.3, 4.1, 4.2, 4.3, 6.4_
  - _Boundary: BackendPanicBoundary, RuntimePanicMapper_
  - _Depends: 4.1, 4.3_

- [x] 4.5 Apply containment to backend cleanup boundaries
  - Recover cleanup close panics without replacing the primary request outcome.
  - Ensure request-local streams and buffers are closed or abandoned safely after isolated failures.
  - Add tests proving cleanup panic diagnostics are recorded and the original surfaced or swallowed outcome remains unchanged.
  - Done when cleanup panics are isolated as diagnostics and do not alter request outcome semantics.
  - _Requirements: 3.6, 4.1, 4.2, 4.4, 6.4_
  - _Boundary: BackendPanicBoundary_
  - _Depends: 4.4_

- [x] 4.6 Apply containment to completion gate callbacks
  - Recover completion gate callback panics during stream processing and map them by the active commit state.
  - Clear or abandon buffered completion-gate state safely when an isolated panic makes the active stream unusable.
  - Add tests proving completion gate panics surface on the affected stream without reordering emitted events or leaking buffers.
  - Done when completion gate panics are explicit stream failures and no longer bypass runtime crash containment.
  - _Requirements: 2.2, 3.2, 4.1, 4.3, 4.4, 6.4_
  - _Boundary: RuntimePanicMapper_
  - _Depends: 4.1, 4.4_

- [x] 5. Contain owned stream worker panics
- [x] 5.1 Apply containment to the keepalive reader worker
  - Recover panics inside the existing request-scoped keepalive reader worker.
  - Publish one bounded stream error to the affected consumer when possible and close the result path without starting replacement workers.
  - Preserve cancellation and close behavior so canceled requests stop contributing output.
  - Add tests for reader panic surfacing, no work leakage to later reads, and cancellation behavior.
  - Done when keepalive reader panics become affected-stream errors and do not terminate the process.
  - _Requirements: 1.4, 5.1, 5.3, 5.4, 5.5_
  - _Boundary: KeepaliveWorkerGuard_
  - _Depends: 1.1_

- [x] 6. Validate integrated crash isolation behavior
- [x] 6.1 Verify HTTP recovery diagnostics and metrics behavior
  - Exercise request-level and server-level HTTP recovery with diagnostics, access logging, and HTTP metrics enabled.
  - Confirm recovered HTTP panics are distinguishable from client validation errors and produce safe client responses.
  - Confirm no stack traces, panic text, request bodies, credentials, or unbounded IDs appear in client responses or metrics labels.
  - Done when HTTP crash diagnostics and observability are consistent across request and server-worker paths.
  - _Requirements: 1.1, 1.3, 1.6, 5.2, 6.1, 6.2, 6.3, 6.5, 6.6_
  - _Boundary: CrashDiagnostics, HTTPMetricsIntegration, OwnedWorkerGuard_
  - _Depends: 2.2, 2.3_

- [x] 6.2 Verify extension and backend diagnostics behavior
  - Exercise hook, tool reactor, backend open, backend receive, cleanup, and completion gate recovery paths with diagnostics enabled.
  - Confirm isolated crashes are distinguishable from expected upstream errors, hook validation failures, and cancellations through logs, attempt outcomes, or bounded metrics.
  - Confirm swallowed and surfaced backend attempt outcomes remain visible through existing attempt diagnostics.
  - Done when operator-visible crash diagnostics are consistent across extension and backend runtime boundaries.
  - _Requirements: 2.1, 2.2, 2.3, 3.1, 3.2, 3.3, 3.6, 6.1, 6.3, 6.4, 6.5, 6.6_
  - _Boundary: CrashDiagnostics, BackendPanicBoundary, HookPanicBoundary_
  - _Depends: 3.2, 4.6_

- [x] 6.3 Verify worker and shared-state integration behavior
  - Exercise keepalive worker recovery alongside request cancellation and backend stream cleanup scenarios.
  - Confirm unrelated requests remain processable after isolated failures and startup configuration or hook ordering remains stable.
  - Confirm no unowned background work or worker replacement behavior was introduced.
  - Done when worker containment and shared runtime integrity hold across the integrated crash-isolation paths.
  - _Requirements: 1.4, 4.1, 4.2, 4.4, 4.5, 5.1, 5.3, 5.4, 5.5_
  - _Boundary: Integration Validation, KeepaliveWorkerGuard_
  - _Depends: 5.1, 6.2_

- [x] 6.4 Run final quality gates for streaming and goroutine safety
  - Run focused tests for HTTP, hooks, runtime, and stream packages.
  - Run race-focused validation for stream and keepalive behavior.
  - Run the repository quality checks and goroutine quality gate to confirm no unowned background work was introduced.
  - Confirm architecture checks still enforce no provider SDK, SQL, ORM, or transport leakage into core orchestration contracts.
  - Done when all targeted package tests, race-relevant validation, quality checks, and goroutine gates pass or any failures are documented with fixes.
  - _Requirements: 1.3, 4.1, 4.3, 5.5_
  - _Boundary: Integration Validation_
  - _Depends: 6.3_
