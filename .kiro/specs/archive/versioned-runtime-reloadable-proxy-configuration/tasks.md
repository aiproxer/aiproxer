# Implementation Plan

Implementation is TDD-first. Contract tests, ownership gates, reloadability classification, no-drop scenarios, and lifecycle failure matrices are written before production publication is enabled. A task is complete only when its observable completion condition is demonstrated by the named focused validation. The standard binary must retain current startup behavior until the stable dispatcher, initial-generation path, and rollback suite are green.

## Phase 1: Freeze Contracts, Ownership, and RED Scenarios

- [x] 1. Establish the runtime reload contract surface

- [x] 1.1 Inventory current resource ownership and add architecture RED gates
  - Enumerate every resource created by `BuildBootstrap`, `runtimebundle.Build`, `NewStandardHandler`, model/catalog startup, terminal work, metrics, tracing, and server startup.
  - Classify each resource as process-owned, generation-owned, or request/async-work-owned.
  - Add failing architecture tests for a second global tracer/metrics registry, duplicate process worker, unclassified closer, active-runtime mutation setter, and any file-watcher dependency or polling loop.
  - Record the final package/import mapping and protect core from filesystem, signal, stdhttp, runtimebundle, and concrete plugin imports.
  - Observable completion: an ownership inventory covers every current `Built` field/closer and the architecture tests fail for representative forbidden changes.
  - _Requirements: 1.6, 4.9, 6.1-6.10, 16.7, 16.11_
  - _Boundary: Architecture, composition root, and tests_
  - _Depends: none_
  - _Validation: `go test ./internal/archtest/... ./internal/infra/runtimebundle/... -run 'Reload|Ownership|Watcher|ProcessService'`_

- [x] 1.2 Define bounded strict configuration-source and effective-loader RED tests
  - Add table/fuzz fixtures for missing, empty, whitespace, oversize, partial, malformed, multiple-document, trailing-content, unknown-core-field, and valid opaque plugin-node inputs.
  - Characterize current defaults, stream-recovery CLI/environment overrides, standard feature injection, alias validation, prefix validation, and full-build-only failures.
  - Define private raw/effective identity and safe public fingerprint expectations, including comment/key-order no-op and secret-only changes.
  - Prove merely replacing or touching the source file does not change active state without an explicit trigger.
  - Observable completion: failing tests describe one shared startup/check-config/reload pipeline and reject all unsafe source shapes.
  - _Requirements: 1.5-1.7, 2.1-2.10, 3.6-3.8_
  - _Boundary: Config and filesystem driving adapter_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/core/config/... ./internal/infra/configsource/... -run 'Reload|Strict|Effective|Noop|NoWatcher' && go test -fuzz=FuzzReloadConfigSource -fuzztime=30s -run=^$ ./internal/core/config/...`_

- [x] 1.3 Define exhaustive reloadability-policy RED tests
  - Create a typed field inventory for every top-level config section and startup CLI/environment override.
  - Add cases for startup-only, reloadable, conditional, mixed, secret-bearing, and newly added/unclassified fields.
  - Require deterministic sorted bounded `restart_required_fields` output without values.
  - Add a compile-time or structural guard that fails when config fields are added without classification.
  - Observable completion: every current field has one explicit disposition and mixed candidates fail before component construction.
  - _Requirements: 3.5, 7.1-7.9, 12.11_
  - _Boundary: Reload policy and config tests_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/core/configreload/... ./internal/core/config/... -run 'Reloadability|RestartRequired|Unclassified'`_

- [x] 1.4 Define generation-manager linearizability and retention RED tests
  - Add deterministic acquire/publish races, retiring-bit/refcount transitions, exactly-once release, pointer recheck, async-pin transfer, zero-ref drain, and double-close cases.
  - Prohibit `sync.WaitGroup` as the general request refcounter through focused tests/architecture checks.
  - Add blocked SSE/async/provider pins and retained-generation budget cases proving later publication is rejected without killing old work.
  - Use fake clocks and controlled barriers rather than timing sleeps.
  - Observable completion: RED tests state one linearizable commit and all permitted request/retirement interleavings.
  - _Requirements: 5.2-5.10, 10.1-10.12, 15.1-15.6_
  - _Boundary: Runtime host generation state and tests_
  - _Depends: 1.1_
  - _Validation: `go test -race ./internal/infra/runtimehost/... -run 'Generation|Acquire|Publish|Retire|Retention|Pin'`_

- [x] 1.5 Define composed no-drop and trigger-contract RED harnesses
  - Build deterministic `httptest` harnesses for HTTP/1.1 keep-alive, HTTP/2 multiplexing, SSE, non-streaming, cancellation, failover, parallel races, and management access.
  - Add Unix SIGHUP versus INT/TERM contract tests and non-Unix API-only compile tests.
  - Add management authentication, fixed-source, method/content-type/body, busy, disconnect, and status-response goldens.
  - Observable completion: current code fails the explicit reload/no-drop scenarios while unrelated server behavior remains characterized.
  - _Requirements: 1.1-1.9, 5.1-5.10, 11.1-11.9, 12.1-12.11_
  - _Boundary: stdhttp, CLI signal adapters, and composed tests_
  - _Depends: 1.2, 1.4_
  - _Validation: `go test ./cmd/lipstd/... ./internal/stdhttp/... -run 'ConfigReload|SIGHUP|Generation|Management|NoDrop'`_

## Phase 2: Separate Effective Configuration and Process Services

- [x] 2. Build the safe reusable bootstrap foundations

- [x] 2.1 Implement bounded fixed-path source reading and strict effective loading
  - Resolve and retain the absolute source path at startup.
  - Replace unbounded reload reads with a context-aware bounded snapshot and explicit empty/oversize classifications.
  - Decode exactly one strict typed document while preserving plugin-private `yaml.Node` subtrees.
  - Centralize defaults, fixed overrides, standard feature injections, validation, and canonical private identity.
  - Keep raw bytes and private identities out of errors, logs, and public status.
  - Observable completion: startup, `check-config`, and reload fixtures produce the same normalized result and failure categories.
  - _Requirements: 1.7, 2.1-2.10, 3.6-3.8, 14.3_
  - _Boundary: Config and filesystem driven adapter_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/core/config/... ./internal/infra/configsource/... ./cmd/lipstd/... -run 'Load|CheckConfig|Effective|Strict|Reload'`_

- [x] 2.2 Implement the typed reloadability classifier
  - Define maintained comparators by owned configuration section rather than reflection.
  - Classify listener/server, management, access/auth class, global observability, database/store topology, plugin discovery/trust, process budgets, and startup overrides as startup-only.
  - Classify proven generation-owned rows and policy fields as reloadable.
  - Return stable safe paths, total blocked count, and no values.
  - Observable completion: every current field is classified and no unsupported change can reach generation compilation.
  - _Requirements: 3.5, 7.1-7.9_
  - _Boundary: Core reload policy_
  - _Depends: 1.3, 2.1_
  - _Validation: `go test ./internal/core/configreload/... ./internal/core/config/... -run 'Reloadability|RestartRequired|FieldCoverage'`_

- [x] 2.3 Split process-service construction from generation compilation
  - Extract explicit process-owned construction for stores, pools, terminal-work processing, metrics registry, tracing/logger dependencies, plugin discovery/trust, process limiters, and compatible mutable state registries.
  - Define explicit `ProcessServices` inputs with one owner and one shutdown path; do not introduce a service locator.
  - Split existing closer lists by ownership and preserve reverse-order error disposal on partial startup.
  - Keep a compatibility bootstrap wrapper for inspect/serve callers while the host migration proceeds.
  - Observable completion: two candidate compilations share process-service identities and do not open duplicate stores, pools, workers, registries, or tracer providers.
  - _Requirements: 6.1-6.10, 13.3-13.8, 16.3_
  - _Boundary: runtimebundle composition root_
  - _Depends: 1.1, 2.2_
  - _Validation: `go test ./internal/infra/runtimebundle/... -run 'ProcessServices|Ownership|Duplicate|Bootstrap|Closer'`_

- [x] 2.4 Hoist process-capacity and mutable continuity state
  - Move decode admission and other overlap-sensitive process budgets out of `Built`.
  - Preserve in-memory continuity, secure-session, A-leg lifecycle/cancellation, affinity, health observation, and shared-state identity where configuration identity is compatible.
  - Define safe keying/reset semantics for materially changed backend instances and stale affinity/health entries.
  - Prove store/session/A-leg identity survives candidate compile and publication.
  - Observable completion: generation replacement cannot multiply capacity or reset reusable process state.
  - _Requirements: 6.5-6.8, 7.3, 15.6_
  - _Boundary: Runtime shared services and core state adapters_
  - _Depends: 2.3_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./internal/core/continuity/... ./internal/core/securesession/... ./internal/core/affinity/... ./internal/core/policy/... -run 'Reload|Shared|Identity|Stale'`_

- [x] 2.5 Preserve startup, inspect, routes, inventory, and public Build compatibility
  - Route existing commands through the new effective loader and split bootstrap without enabling runtime reload yet.
  - Preserve error classification, standard plugin defaults, CLI flag precedence, and resource cleanup.
  - Add public/internal compatibility tests before deleting or narrowing existing bootstrap entrypoints.
  - Observable completion: current commands and example configs behave identically on valid inputs while strict invalid inputs fail consistently.
  - _Requirements: 2.5-2.7, 16.1-16.4_
  - _Boundary: CLI, runtimebundle, and public runtime compatibility_
  - _Depends: 2.1-2.4_
  - _Validation: `go test ./cmd/lipstd/... ./internal/infra/runtimebundle/... ./pkg/lipruntime/... -run 'Bootstrap|CheckConfig|Routes|Inventory|Compatibility'`_

## Phase 3: Implement Immutable Generations and Stable Serving

- [x] 3. Build the generation publication and request-binding path

- [x] 3.1 Implement race-safe generation state, lease, and atomic manager
  - Implement prepared/active/retiring/quiesced/drained/closing/closed transitions.
  - Implement active pointer acquire with retain and pointer recheck.
  - Add transferable request/async/provider pins and one drained notification.
  - Enforce one atomic publish and a finite startup-fixed retained-generation/resource budget.
  - Observable completion: all linearizability, double-release, drain, and retention RED tests pass under the race detector.
  - _Requirements: 3.1, 4.2-4.4, 5.2-5.4, 10.1-10.12, 15.1-15.6_
  - _Boundary: Runtime host generation state_
  - _Depends: 1.4, 2.3_
  - _Validation: `go test -race ./internal/infra/runtimehost/... -run 'Generation|Lease|Publish|Retire|Retention'`_

- [x] 3.2 Implement candidate resource ledger and lifecycle phases
  - Register candidate resources immediately with idempotent reverse-order rollback.
  - Adapt safe existing feature lifecycles to prepare/activate/quiesce/close ownership.
  - Add internal backend instance wrappers for optional lifecycle, close, idle transport cleanup, and non-billable preflight capability.
  - Reject lifecycle changes that cannot overlap safely rather than publishing a partially supported candidate.
  - Observable completion: injected failures at every acquisition/start point leave no resource, goroutine, subprocess handle, or active mutation behind.
  - _Requirements: 3.2-3.4, 8.6-8.11, 10.5-10.6, 13.4-13.5_
  - _Boundary: runtimebundle generation lifecycle and backend composition_
  - _Depends: 2.3, 3.1_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./internal/infra/runtimehost/... ./internal/plugins/... -run 'Candidate|Rollback|Lifecycle|Close|Preflight'`_

- [x] 3.3 Implement complete generation compilation
  - Compile isolated registry/registration views, feature surfaces, backend instances, inventories, routes, aliases, capabilities, generation-owned HTTP client, executor, app, and model runtime from the candidate plus process services.
  - Construct a complete standard handler without binding a listener and freeze all published maps/slices/config.
  - Ensure no process service is recreated and no active object is mutated.
  - Observable completion: two valid candidates can coexist with different backends/routes/hooks/auth/model views and independently close.
  - _Requirements: 3.1-3.4, 4.1-4.9, 8.1-8.11_
  - _Boundary: runtimebundle and stdhttp generation composition_
  - _Depends: 2.3-2.5, 3.2_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./internal/stdhttp/... -run 'CompileGeneration|Coexist|Immutable|Handler'`_

- [x] 3.4 Add the stable data-plane dispatcher and initial-generation host
  - Place one generation dispatcher behind one long-lived `http.Server`.
  - Compile startup configuration as generation 1 through the same compiler and manager.
  - Attach bound generation metadata and async pin capability to request context.
  - Preserve response-writer interfaces, recovery, metrics, tracing, auth, identity, and access-log behavior.
  - Observable completion: all existing stdhttp tests pass through the dispatcher with reload disabled, and generation 1 is visible in safe diagnostics.
  - _Requirements: 1.1, 4.5-4.9, 5.1-5.7, 13.3, 16.3-16.4_
  - _Boundary: Runtime host and stdhttp data plane_
  - _Depends: 3.1, 3.3_
  - _Validation: `go test ./internal/stdhttp/... ./internal/infra/runtimehost/... ./cmd/lipstd/... -run 'InitialGeneration|Dispatcher|StandardWiring|Identity|Auth'`_

- [x] 3.5 Bind one immutable model view to each logical request
  - Expose a request-bindable model registry/catalog publication view with config and model generation identity.
  - Use the same bound view for route planning, failover, parallel attempts, capability/model legality, and diagnostics.
  - Keep configured model refresh independent from config publication and apply refreshed views only to later requests.
  - Observable completion: a model refresh during a blocked request cannot change any candidate or model mapping for that request.
  - _Requirements: 9.1-9.10_
  - _Boundary: Core runtime, routing, and model registry_
  - _Depends: 3.3, 3.4_
  - _Validation: `go test -race ./internal/core/modelregistry/... ./internal/core/modelcatalog/... ./internal/core/runtime/... ./internal/core/routing/... -run 'BoundModel|Generation|Refresh|Failover|Parallel'`_

- [x] 3.6 Transfer generation ownership to terminal and asynchronous work
  - Retain a generation/provider pin before heartbeats, finalizers, auxiliary work, or terminal intents outlive the HTTP lease.
  - Integrate retained generation/provider lookup with existing executable policy generation and terminal-work ownership without conflating their IDs.
  - Clear pins exactly once on terminal completion, durable handoff completion, or provider resolution.
  - Observable completion: HTTP return cannot close a backend still required by settlement/finalization, and unresolved work blocks unsafe retirement/removal.
  - _Requirements: 5.3, 5.7-5.10, 10.3, 10.7, 13.8_
  - _Boundary: Core runtime, terminal work, and runtime host_
  - _Depends: 3.1, 3.3_
  - _Validation: `go test -race ./internal/core/runtime/... ./internal/core/terminalwork/... ./internal/core/snapshotgen/... ./internal/infra/runtimehost/... -run 'GenerationPin|Terminal|Provider|Heartbeat|Finalize'`_

## Phase 4: Enable Reloadable Request-Plane Composition

- [x] 4. Add safe runtime recomposition by field group

- [x] 4.1 Enable backend add, replace, disable, and removal
  - Add tests and implementation for a backend absent at startup, same-ID changed configuration, disabled/removed backend, and candidate factory failure.
  - Cover all generic compatible kinds and at least one built-in hosted/local deterministic stub.
  - Preserve old backend instances for retired work and close new candidate instances on rollback.
  - Observable completion: new requests route only through the new backend set while old streams continue through old instances.
  - _Requirements: 8.1-8.6, 8.9-8.11, 9.7-9.9_
  - _Boundary: Backend factories, runtimebundle, and routing_
  - _Depends: 3.3, 3.6_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./internal/core/runtime/... ./internal/plugins/backends/... -run 'Reload.*Backend|Add|Replace|Remove|Generic'`_

- [x] 4.2 Integrate already discovered executable plugin factory kinds
  - Revalidate against the implemented backend connector plugin architecture.
  - Keep factory discovery/trust catalog process-owned and startup-fixed.
  - Support candidate activation of an already discovered kind and simultaneous old/new instance handles.
  - Reject unsupported shared-process reconfiguration before publication with a typed safe error.
  - Observable completion: no plugin directory rescan or install occurs, and a deterministic fake host proves overlap or rejection semantics.
  - _Requirements: 7.3, 8.7-8.9, 16.5_
  - _Boundary: Backend plugin host and runtime composition_
  - _Depends: backend-connector-plugin-architecture implementation, 4.1_
  - _Validation: `go test -race ./internal/pluginreg/... ./internal/infra/runtimebundle/... ./internal/infra/backendplugin/... -run 'Reload|Discovered|Overlap|NoRescan|NoInstall'`_

- [x] 4.3 Enable frontend and feature generation replacement
  - Rebuild frontend mounts, auth renderers, feature bundles, hooks, extension snapshots, and app lifecycles as one candidate.
  - Add route conflict, feature uniqueness, lifecycle failure, and old/new handler isolation tests.
  - Ensure management routes remain outside the swappable graph.
  - Observable completion: changed frontend/feature behavior applies only to new requests and cannot remove reload/status access.
  - _Requirements: 4.1, 8.5-8.6, 12.1-12.4_
  - _Boundary: Frontend/feature plugins, featurebundle, and stdhttp_
  - _Depends: 3.2-3.4_
  - _Validation: `go test -race ./internal/featurebundle/... ./internal/plugins/frontends/... ./internal/plugins/features/... ./internal/stdhttp/... -run 'Reload|Generation|RouteConflict|Lifecycle'`_

- [x] 4.4 Enable routing, auth-record, request-limit, HTTP-client, and policy changes
  - Add per-field tests before enabling each reloadable group.
  - Rebuild aliases/default route/health policy/capabilities, local auth key records under fixed handler mode, request body/pending-event/keepalive limits, and generation-owned upstream client.
  - Preserve compatible affinity/health state and reject process-topology changes.
  - Observable completion: every enabled group changes atomically for new requests and every startup-only neighbor returns restart-required.
  - _Requirements: 7.3-7.9, 9.1-9.2, 16.4_
  - _Boundary: Core routing/security policy and runtimebundle wiring_
  - _Depends: 2.2, 2.4, 3.3_
  - _Validation: `go test -race ./internal/core/routing/... ./internal/core/auth/... ./internal/core/runtime/... ./internal/infra/runtimebundle/... ./internal/stdhttp/... -run 'Reload|Alias|AuthKey|Limit|HTTPClient|Health|Affinity'`_

- [x] 4.5 Enable coherent model catalog and inventory generation changes
  - Build candidate inventory from its backend set and configured static/cache/remote policy under existing fail-soft/strict rules.
  - Stop old generation refresh loops at quiesce while retaining immutable bound views.
  - Add `/v1/models`, ETag, diagnostics, stale cache, empty/invalid inventory, and backend removal tests.
  - Observable completion: new model output and routing agree with the active config generation while old requests retain old views.
  - _Requirements: 9.1-9.10, 10.5_
  - _Boundary: Model catalog/registry and runtimebundle_
  - _Depends: 3.5, 4.1_
  - _Validation: `go test -race ./internal/core/modelregistry/... ./internal/core/modelcatalog/... ./internal/infra/runtimebundle/... ./internal/stdhttp/... -run 'Reload|Models|Inventory|ETag|Quiesce'`_

- [x] 4.6 Complete retired-generation quiesce, cleanup, and pressure handling
  - Stop generation background loops after retirement without closing resources needed by pins.
  - Close feature/backend/client/model resources in reverse order after drain.
  - Add cleanup panic/error isolation, bounded retry policy, status reporting, and idle transport closure.
  - Reject a new publication before swap when the retained budget is exhausted.
  - Observable completion: repeated reloads remain bounded and an intentionally blocked old stream causes a safe retention conflict rather than termination.
  - _Requirements: 10.1-10.12, 13.5, 14.1, 15.5-15.6_
  - _Boundary: Runtime host and generation resource ownership_
  - _Depends: 3.1-3.3, 3.6, 4.1-4.5_
  - _Validation: `go test -race ./internal/infra/runtimehost/... ./internal/infra/runtimebundle/... -run 'Quiesce|Cleanup|Retention|BlockedStream|CloseIdle'`_

## Phase 5: Add Coordinator, Signal, Management, and Public Surfaces

- [x] 5. Expose explicit production administration

- [x] 5.1 Implement the serialized reload coordinator and status state machine
  - Implement read, load, no-op, classify, compile, prepare, retention check, publish, rollback, and terminal status transitions.
  - Use one active attempt, bounded host-owned timeout, API busy result, and at most one pending coalesced signal.
  - Prevent publication after shutdown begins and isolate worker panics.
  - Observable completion: every stage fault produces one terminal result and leaves the expected active generation.
  - _Requirements: 1.4, 3.1-3.10, 11.4-11.9, 13.1-13.7_
  - _Boundary: Config reload application orchestration_
  - _Depends: 2.1-2.2, 3.1-3.3, 4.6_
  - _Validation: `go test -race ./internal/core/configreload/... ./internal/infra/runtimehost/... -run 'Coordinator|Busy|Noop|Fault|Shutdown|Coalesce'`_

- [x] 5.2 Implement Unix SIGHUP and non-Unix API-only adapters
  - Register HUP separately from shutdown signals.
  - Deliver to a bounded process-owned channel and record coalescing without one goroutine per signal.
  - Add build-tagged platform tests and verify INT/TERM behavior remains unchanged.
  - Observable completion: HUP publishes a valid candidate on Unix, does not stop the server, and unsupported platforms compile and use API only.
  - _Requirements: 1.2, 1.8-1.9, 11.1-11.9_
  - _Boundary: cmd/lipstd OS driving adapter_
  - _Depends: 1.5, 5.1_
  - _Validation: `go test -race ./cmd/lipstd/... -run 'SIGHUP|SignalReload|SignalShutdown|Platform'`_

- [x] 5.3 Implement the separate management server and authentication posture
  - Add startup-fixed loopback-default listener, reload/status paths, authentication, method/content-type/body guards, and no-CORS posture.
  - Support existing local single-user trust only under explicit loopback conditions; require strong dedicated auth for multi-user/non-loopback.
  - Map terminal results to stable HTTP status/JSON and keep accepted reload running after client disconnect.
  - Ensure the handler cannot supply paths, YAML, URLs, commands, or plugin installation.
  - Observable completion: security and result goldens pass and management remains reachable after invalid data-plane candidates.
  - _Requirements: 1.3, 1.7, 12.1-12.11, 13.1-13.2_
  - _Boundary: stdhttp management driving adapter_
  - _Depends: 1.5, 5.1_
  - _Validation: `go test -race ./internal/stdhttp/admin/configreload/... ./internal/stdhttp/... -run 'Management|ReloadAPI|Auth|Loopback|Disconnect|FixedSource'`_

- [x] 5.4 Add bounded reload observability and safe generation correlation
  - Add structured logs, fixed-label counters/histograms, aggregate active/retired/pinned gauges, and process-owned reload spans.
  - Add bounded status history and protected diagnostics with config/model generation references.
  - Add secret corpus tests for config keys, DSNs, URLs, opaque nodes, and validation failures.
  - Keep reload-control posture separate from active data-plane readiness.
  - Observable completion: metrics have bounded labels, secret scans are clean, and failed reload status does not fail healthy active readiness.
  - _Requirements: 13.1-13.5, 14.1-14.9_
  - _Boundary: Metrics, tracing, logging, diagnostics, and query seam_
  - _Depends: 5.1, 5.3_
  - _Validation: `go test ./internal/infra/metrics/... ./internal/core/diag/... ./internal/infra/runtimehost/... ./internal/stdhttp/... -run 'Reload|Generation|Cardinality|Secret|Readiness'`_

- [x] 5.5 Add the supported `pkg/lipruntime` reload and status facade
  - Expose explicit reload and safe status DTOs without internal types or mutable config.
  - Return a stable delegating `ExecutorView`: each `Execute` acquires the current generation, its returned stream retains the lease through terminal/close, and `CancelALeg` reaches process-owned cross-generation A-leg state.
  - Use the same coordinator/compiler/manager as the standard binary and add a separate-module compile fixture.
  - Preserve existing `RefreshSnapshots` semantics as a subordinate explicit policy refresh, not whole-config reload.
  - Observable completion: a facade obtained before reload executes new calls on the new generation, preserves old returned streams, and cancels an old A-leg.
  - _Requirements: 16.1-16.6, 16.12-16.13_
  - _Boundary: SDK/public runtime facade_
  - _Depends: 5.1, 5.4_
  - _Validation: `go test ./pkg/lipruntime/... ./internal/archtest/... -run 'Reload|Status|ExternalModule|RefreshSnapshots'`_

- [x] 5.6 Implement complete host shutdown ordering and trigger teardown
  - Stop signal/API trigger acceptance, cancel candidate work, shut down data admissions, drain generations, close generation resources, close management, close process services, and flush tracing.
  - Add shutdown races with source read, compilation, publication boundary, active SSE, pending signal, and cleanup error.
  - Observable completion: shutdown publishes no late generation, leaks no worker/channel, and preserves existing graceful server deadline behavior.
  - _Requirements: 1.9, 6.10, 11.9, 13.7-13.10_
  - _Boundary: Runtime host, stdhttp, and CLI composition root_
  - _Depends: 5.1-5.5_
  - _Validation: `go test -race ./internal/infra/runtimehost/... ./internal/stdhttp/... ./cmd/lipstd/... -run 'Shutdown|ReloadRace|LatePublish|Drain'`_

## Phase 6: Full-Stack Certification, Documentation, and Release Gates

- [x] 6. Certify production behavior and migration

- [x] 6.1 Prove zero-drop HTTP and streaming behavior under publication
  - Run HTTP/1.1 keep-alive, HTTP/2 multiplexing, SSE, non-streaming, cancellation, failover, parallel race, pre-output error, and post-output error scenarios.
  - Assert old requests use only old auth/hooks/routes/backends/models and new requests use only new components.
  - Assert listener identity, connection reuse, event order, terminal count, B2BUA lineage, post-publication cancellation of an old A-leg, and no-retry-after-output.
  - Observable completion: repeated valid and invalid reloads cause zero unexpected connection closures or mixed-generation assertions.
  - _Requirements: 4.1-4.9, 5.1-5.10, 9.4-9.9, 13.10, 15.7, 16.12-16.13_
  - _Boundary: Full stdhttp and runtime conformance_
  - _Depends: 4.1-4.6, 5.1-5.6_
  - _Validation: `go test -race ./internal/stdhttp/... ./internal/core/runtime/... -run 'RuntimeConfigReload.*NoDrop|HTTP2|SSE|Failover|Parallel'`_

- [x] 6.2 Prove last-good rollback and restart-required behavior
  - Fault every source, decode, validation, diff, factory, model, lifecycle, handler mount, retention, quiesce, close, and shutdown stage.
  - Include empty/partial file from in-place write and safe atomic rename replacement.
  - Verify mixed reloadable/startup-only changes never partially apply and require another explicit trigger after correction.
  - Observable completion: active generation and request results remain unchanged for every pre-publication failure.
  - _Requirements: 2.1-2.10, 3.1-3.10, 7.1-7.9, 13.1-13.7_
  - _Boundary: Reload transaction fault matrix_
  - _Depends: 5.1, 6.1_
  - _Validation: `go test -race ./internal/core/configreload/... ./internal/infra/runtimehost/... ./internal/infra/runtimebundle/... ./internal/stdhttp/... -run 'LastGood|Rollback|RestartRequired|AtomicRename|FaultMatrix'`_

- [x] 6.3 Prove dynamic component and adjacent plugin compatibility
  - Add/change/remove every generic compatible kind and representative built-in backend instances.
  - Recompose frontend and feature rows, auth records, routes, aliases, models, and request-plane limits.
  - Where backend executable plugins are implemented, activate an already discovered kind and prove old/new instance overlap.
  - Assert no watcher, plugin rescan, download, installation, or arbitrary code path is invoked.
  - Observable completion: the feature request’s new-backend/model behavior works with factories known to the process and no runtime artifact installation.
  - _Requirements: 8.1-8.11, 9.1-9.10, 16.5_
  - _Boundary: Standard composition, backend/frontend/feature plugins, and plugin host_
  - _Depends: 4.1-4.5_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./internal/plugins/... ./internal/pluginreg/... ./internal/stdhttp/... -run 'Reload.*Dynamic|Generic|Discovered|NoInstall|NoWatcher'`_

- [x] 6.4 Run race, leak, fuzz, benchmark, and bounded soak evidence
  - Run focused Linux race tests for publication/acquire/pin/shutdown and the default race gate.
  - Run goleak suites for host, signal, model loops, terminal work, and lifecycle rollback.
  - Fuzz config source, strict YAML, effective canonicalization, reload diff, management decode, and coordinator state transitions.
  - Add benchmarks for dispatcher lease overhead, publication, compilation, and retained memory/resources.
  - Run a precommit/nightly bounded soak with mixed traffic, repeated valid/invalid/no-op/restart-required triggers, blocked old streams, and retention pressure.
  - Observable completion: no race/leak/fuzz failure, no connection drop, bounded resources, and reviewed benchmark evidence.
  - _Requirements: 10.8-10.12, 15.1-15.10, 16.7-16.9_
  - _Boundary: Performance and release testing_
  - _Depends: 6.1-6.3_
  - _Validation: `make test-race && make test-fuzz && make bench && go test -tags=precommit -run TestRuntimeConfigReloadSoak ./internal/stdhttp/...`_

- [x] 6.5 Update configuration schema, operator docs, ADRs, and specification bundle
  - Document management startup fields, strict source limit, explicit triggers, atomic replacement, no-op, restart-required matrix, generation/model status, retained-generation pressure, and security.
  - State prominently that file changes alone do nothing and no watcher/automatic retry exists.
  - Add deterministic local examples for SIGHUP and API reload plus invalid-candidate recovery.
  - Update runtime flow, architecture guardrails, release gates, spec-bundle index, and relevant EchoesVault pages.
  - Observable completion: docs and examples match code-owned fields and every example passes startup/check-config tests.
  - _Requirements: 1.5-1.7, 7.1-7.9, 12.1-12.11, 16.10-16.11_
  - _Boundary: Documentation, config examples, ADR, and knowledge base_
  - _Depends: 5.2-5.5, 6.2-6.3_
  - _Validation: `go test ./cmd/lipstd/... ./internal/qa/... -run 'ConfigExample|Docs|Reload' && go run ./cmd/lipstd check-config --config <each-reload-example>`_

- [x] 6.6 Run final architecture, security, parity, and QA gates
  - Run focused and full unit suites, architecture/guard scripts, parity checks, tagged tests, lint, vulnerability scan, race evidence, fuzz smoke, and reload soak.
  - Verify no new dependency, watcher package, global registry mutation, provider SDK leakage, unclassified field, or spec-unrelated runtime path remains.
  - Record exact commands, platform coverage, benchmark comparison, soak parameters, skipped optional external-service tests, and known limitations.
  - _Evidence: [release-evidence.md](release-evidence.md)_
  - Observable completion: all mandatory gates pass and release evidence proves the issue acceptance criteria without a proxy restart or connection drop.
  - _Requirements: 15.7-15.10, 16.3-16.13_
  - _Boundary: Release certification_
  - _Depends: 6.1-6.5_
  - _Validation: `make quality-checks && make test-unit && make parity-checks && make qa && make test-race && make test-fuzz`_

## Implementation Notes

- Phase 4.1–4.2: `internal/infra/backendplugin` does not exist yet; discovered-factory freeze/overlap/no-rescan lives in `pluginreg` + `runtimebundle.ClassifyBackendOverlap`. Coordinator (5.1) must populate `GenerationCompileInput.LiveFactoryKinds` from active/retained generations.
- Shared-process exclusive kinds reject with `configreload.RestartRequiredError` wrapping `ErrUnsafeLifecycleOverlap` before candidate resource acquisition.
- Task 5.3: management listen worker is allowlisted in `scripts/check-adhoc-goroutines.*`; `internal/stdhttp` line budget raised for `admin/configreload`. Host-wide management/data-plane shutdown ordering remains task 5.6.
- Task 5.4: `internal/core` line budget raised for sanitize/history/diag reload surfaces; reload metrics live in process-owned `metrics.Bundle.Reload` and observer callbacks on the existing coordinator (no second telemetry stack).
- Task 5.5: `pkg/lipruntime` public Reload/Status DTOs map 1:1 from `configreload` categories via unexported coordinator/query seam; paths/secrets omitted; `export_test.go` binds test fakes. Full generation-dispatcher ExecutorView publish wiring remains composition-root owned (no cmd/lipstd changes in 5.5).
- Tasks 5.5–5.6 (production composition): `runtimebundle.AttachReloadHost` binds FixedSource + shared effective loader + `GenerationCompiler` + `runtimehost.Coordinator` onto the initial-generation manager; `lipruntime.Build` and `cmd/lipstd serve` both use that host. `BootstrapResult.FixedStreamRecovery` captures the CLI+env override snapshot once during `BuildBootstrap` and is reused for every effective reload (no env reread in `AttachReloadHost`). Stable `runtimehost.GenerationExecutor` acquires/pins per Execute. SIGHUP adapter sink is the real coordinator (never nil). Management server is **address-env opt-in**: disabled unless `LIP_RELOAD_MANAGEMENT_ADDRESS` supplies the startup-fixed bind (recommended loopback `127.0.0.1:9090`); single_user loopback defaults to local_trust; multi_user or non-loopback requires dedicated bearer from `LIP_RELOAD_MANAGEMENT_TOKEN` (absent → management disabled with warning, data-plane serve continues; weak token rejected). Pre-serve failure paths run `serveStartupRollback` (BeginShutdown → retire generations → close management → close process services when none remain; tracing stays outer defer). `RunWithGenerationHost` shutdown order: BeginShutdown → HTTP drain → coordinator WaitForIdle (candidate rollback) → generation drain → management close → process services; tracing stays outer defer.
- Task 5.6 shutdown ownership repair: `Coordinator` arms a host-owned cancel covering coalesced follow-ups, `BeginShutdown` cancels it (arm/shutdown handshake cannot miss), and `WaitForIdle` is the context-bounded barrier. `Runtime.Close` honors the caller deadline (no `WithoutCancel` strip on drain/tracing), serializes close attempts under a mutex, is idempotent after success, and remains **retryable** after deadline or teardown failure (not `sync.Once`); facade pointers stay so concurrent Reload/Status/Execute fail through manager state.
