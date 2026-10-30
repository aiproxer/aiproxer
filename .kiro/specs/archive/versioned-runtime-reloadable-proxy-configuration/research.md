# Current-State Review, Requirements Gap Analysis, Architecture Research, and Design Validation

Generated: 2026-07-19T23:15:51+02:00

## Status

- Repository: `matdev83/go-llm-interactive-proxy`
- Reviewed ref: `main` at `bf3ff3594f18c4b420139ce2e24222a1262a4baa`
- Feature: `versioned-runtime-reloadable-proxy-configuration`
- Source feature request: issue `#189`
- Workflow completed: initialization, requirements generation, mandatory brownfield gap analysis, requirements remediation, design generation, design validation, design correction, and task generation
- Change scope: Kiro specification artifacts only
- Review mode: static source, contract, steering, and prior-spec review through the connected GitHub repository; no runtime implementation or provider test was executed
- Implementation readiness: final design validated; requirements, design, and tasks remain unapproved in `spec.json`

## Reviewed Steering, Rules, Templates, and Patterns

The workflow follows the active repository process and the five-artifact pattern used by recent completed spec-only pull requests.

- `AGENTS.md`
- `.kiro/AGENTS.md`
- `.kiro/steering/{product,structure,tech,api-standards,routing-and-orchestration,testing}.md`
- `.kiro/rules/{ears-format,gap-analysis,design-principles,design-review}.md`
- `.kiro/settings/templates/specs/{init.json,requirements.md,design.md,tasks.md}`
- `.kiro/specs/archive/backend-connector-plugin-architecture/*`
- `.kiro/specs/archive/generic-compatible-backend-modes/*`
- archived dual-plane executable-generation and runtime-hardening specifications
- `docs/runtime-flow.md`, model/routing/extension documentation, and current composition code

The final artifact set is exactly:

1. `spec.json`
2. `requirements.md`
3. `research.md`
4. `design.md`
5. `tasks.md`

## Executive Assessment

The repository already uses immutable atomic publication successfully for narrower runtime planes:

- extension request snapshots are frozen per build and explicitly require new snapshot publication for reload;
- executable policy/economics generations are published through an atomic pointer and retained by in-flight work;
- model registry publication swaps one coherent registry, snapshot, JSON representation, fingerprint, and diagnostics view;
- injectable policy snapshot refresh is explicit, serialized, and leaves the prior executable generation active after failure.

Those patterns are directly reusable as design precedents, but they do not currently cover the complete HTTP-facing runtime. The standard server still builds one handler tree and one executor graph at startup. That handler captures auth, frontends, routes, hooks, backends, model views, diagnostics, metrics, tracing wrappers, and request limits. Updating only one of those elements would create a mixed generation.

A safe implementation therefore requires a new **stable process host plus immutable whole request-plane generations** architecture. The data-plane listener remains fixed. Candidate configuration is loaded and compiled beside the active generation. One atomic pointer publication commits the complete candidate. Requests and streams retain the generation they entered, and the previous generation closes only after request and asynchronous references drain.

The required work is an architectural refactor, not a setter-based enhancement to `runtime.Executor` and not a restart wrapper around `http.Server`.

## Current Runtime and Configuration Assets

### Configuration loading and validation

- `internal/core/config/loader.go`
  - resolves the startup path;
  - reads the complete file with `os.ReadFile`;
  - decodes with `yaml.Unmarshal`;
  - applies defaults;
  - runs `config.Validate`.
- `internal/core/config/model.go`
  - owns typed core configuration;
  - retains plugin-private configuration as opaque `yaml.Node`;
  - contains server, access/auth, observability, routing, continuity, secure-session, plugin, model, accounting, control-plane, and persistence settings.
- `internal/core/config/validate.go`
  - validates most core-owned sections;
  - model aliases and some composition-specific rules are validated later.
- `runtimebundle.BuildBootstrap`
  - applies stream-recovery overrides;
  - validates aliases and custom-compatible prefixes;
  - initializes tracing and logging;
  - creates the registry and standard bundle;
  - injects default features;
  - merges feature surfaces;
  - constructs the app and optionally calls full `runtimebundle.Build`.

### HTTP startup and handler ownership

- `cmd/lipstd/runServeCommand`
  - runs one bootstrap;
  - uses `signal.NotifyContext` for `SIGINT`/`SIGTERM`;
  - passes one config/app/runtime to `stdhttp.RunWithRuntime`.
- `internal/stdhttp/server.go`
  - prepares one handler;
  - creates one `http.Server`;
  - binds one listener;
  - serves until context cancellation;
  - shuts down the app and resource closers.
- `internal/stdhttp/handler.go`
  - mounts metrics, diagnostics, admin surfaces, model views, and frontends;
  - starts feature lifecycles;
  - constructs the final middleware stack.
- `internal/stdhttp/middleware.go`
  - captures configuration and built runtime for auth, access logs, metrics, tracing, identity, recovery, and the route mux.

### Runtime assembly and state ownership

`runtimebundle.Build` currently constructs both request-plane values and long-lived process state:

- database pool registry and migration posture;
- continuity and secure-session stores;
- control-plane, metering, usage-authority, and concurrency services;
- terminal-work processor and provider registry;
- metrics registry and shared outbound HTTP client;
- model catalog, backend instances, model inventory, and refresh loops;
- routing health, affinity state, aliases, and capabilities;
- feature runtime snapshot and executor;
- decode-admission limiter;
- lifecycle and resource closers.

A second full `Build` would therefore duplicate workers and pools or split in-memory state unless ownership is refactored first.

### Existing immutable publication precedents

- `internal/core/extensions.RequestRuntimeSnapshot`
  - frozen after construction;
  - documentation explicitly requires a new snapshot and executor wiring for reload/rebind.
- `internal/core/snapshotgen.Publisher`
  - atomic active pointers;
  - executable-generation validation;
  - live request reference counts;
  - retained generations;
  - pending-provider references;
  - last-good preservation.
- `internal/infra/runtimebundle.SnapshotController`
  - explicit `Refresh`;
  - one serialized refresh;
  - no unmanaged polling;
  - prior executable generation preserved on source failure.
- `internal/core/modelregistry.Runtime`
  - atomic coherent `published` object;
  - last-good behavior on refresh failures;
  - immutable active registry and precomputed `/v1/models` body.
- `pkg/lipruntime.Runtime.RefreshSnapshots`
  - public explicit refresh;
  - in-flight requests retain previously bound executable generations.

### Adjacent connector architecture

The active backend connector plugin specification establishes:

- process-owned trusted manifest discovery;
- immutable factory ownership after startup in the initial architecture;
- lazy activation of already discovered plugin kinds;
- no runtime download, installation, or arbitrary executable discovery;
- supervised instance lifecycle;
- the internal `execbackend.Backend` seam remains the core-facing port.

Runtime configuration reload must compose with that model. It may activate a factory kind already known to the process. It must not silently broaden this feature into runtime code installation.

## Existing Strengths to Preserve

1. **Explicit composition roots.** Startup and plugin construction are centralized and error-returning rather than hidden in globals or `init`.
2. **Immutable executor construction.** Backends, routes, capabilities, accounting, security, and extension state are computed before `runtime.NewExecutor`.
3. **Canonical streaming semantics.** Non-streaming remains collection over the same event stream, and no retry is permitted after client-visible output.
4. **Atomic subsystem publication.** Snapshot and model runtimes already demonstrate last-good immutable swaps.
5. **Generation retention precedent.** Executable policy generations already retain in-flight and pending-provider dependencies.
6. **Reverse-order cleanup.** Runtimebundle and stdhttp already maintain explicit closer ownership and panic isolation.
7. **Per-composition-root registries.** A candidate can construct new instances without mutating a global singleton registry.
8. **Strict startup posture.** Access mode, diagnostics, secure session, credential posture, and persistence validation already fail before serving.
9. **Deterministic test culture.** Race, goleak, fuzz, conformance, architecture, and composed HTTP tests are established release practices.

## Requirement-to-Asset Map

| Requirement area | Current assets | Gap classification | Design consequence |
|---|---|---|---|
| Explicit trigger | `cmd/lipstd` signal shutdown handling | Missing | Add separate SIGHUP adapter and process-owned HTTP management adapter |
| Strict source load | `config.LoadFile` | Partial / unsafe for reload | Split bounded read, strict decode, normalization, and effective validation |
| Last-good commit | subsystem publishers | Missing at whole-runtime level | Add candidate transaction and one atomic generation publication |
| Immutable generation | executor and extension snapshots | Partial | Bind the entire handler/executor graph, not selected fields |
| No-drop serving | stable `http.Server` today | Missing reload dispatcher | Keep listener/server and swap a lease-aware handler generation |
| Shared state | `runtimebundle.Build` | Ownership conflict | Separate process services from generation assembly |
| Reloadability policy | typed config | Missing | Add maintained field-level classification and restart-required errors |
| Dynamic instances | registry/factories | Partial | Reconstruct known factory instances per generation |
| Model versioning | model registry/catalog atomics | Partial | Associate model snapshots with config generations and bind per request |
| Retirement | executable generation refs and closers | Partial | Extend refs to HTTP, streams, async work, providers, and complete resources |
| Management security | diagnostics/auth patterns | Missing dedicated surface | Separate listener, fixed path/source, strong startup posture |
| Observability | slog/metrics/tracing | Partial | Process-owned reload status and generation correlation |
| Public runtime | `pkg/lipruntime` | Missing whole-config reload | Add safe explicit reload/status facade |
| Release evidence | existing test topology | Missing scenarios | Add linearizability, no-drop, failure, race, leak, fuzz, and soak gates |

## Mandatory Brownfield Requirements Gap Analysis

| ID | Severity | Finding | Classification | Required disposition |
|---|---:|---|---|---|
| G-01 | P0 | The process has no runtime configuration reload trigger or coordinator. | Missing capability | Add one explicit coordinator shared by SIGHUP and management API. |
| G-02 | P0 | Adding `SIGHUP` to the current shutdown `NotifyContext` would terminate the server. | Lifecycle conflict | Register HUP separately from INT/TERM. |
| G-03 | P0 | `http.Server.Handler` is one startup-built graph and has no stable generation dispatcher. | Architecture blocker | Keep the server and place an atomic lease-aware dispatcher behind it. |
| G-04 | P0 | The handler captures auth, routes, frontends, hooks, executor, model views, and middleware together. | Consistency blocker | Publish a complete graph; prohibit partial field swaps. |
| G-05 | P0 | `BuildBootstrap` combines process-global tracing/logging with reloadable composition. | Ownership blocker | Split process bootstrap from generation compilation. |
| G-06 | P0 | `tracing.Init` installs a global tracer provider. | Global-state conflict | Keep tracing topology process-owned and startup-only. |
| G-07 | P0 | Metrics registries and collectors are built per `runtimebundle.Build`. | Duplicate-registration/state risk | Use one process-owned metrics registry and generation-safe sinks. |
| G-08 | P0 | Full rebuild opens stores, pools, authority services, and terminal workers. | State split/duplicate worker risk | Hoist them into explicit shared process services. |
| G-09 | P0 | Continuity, secure sessions, A-leg lifecycle/cancellation, affinity, and circuit-breaker state can be recreated per Build. | Correctness/availability risk | Preserve compatible shared state identity across publication. |
| G-10 | P0 | `plugin.Lifecycle.Start/Stop` has no candidate overlap, prepare, quiesce, or rollback contract. | Lifecycle gap | Add internal candidate-safe lifecycle ownership and classify unsupported changes. |
| G-11 | P0 | `execbackend.Backend` exposes no close/resource lifecycle. | Resource-leak gap | Add an internal built-instance wrapper with idempotent close/lifecycle. |
| G-12 | P0 | `config.LoadFile` uses unbounded `os.ReadFile`, accepts whitespace into decode, and is not strict for typed unknown fields. | Input-hardening gap | Add bounded source snapshot and strict one-document decoding. |
| G-13 | P0 | Validation is distributed; inspect mode does not construct all runtime components. | Last-good validation gap | Reuse one effective pipeline and full deterministic candidate construction. |
| G-14 | P0 | No field-level reloadability matrix exists. | Operability/safety gap | Reject process-topology changes with typed restart-required fields. |
| G-15 | P0 | There is no atomic publisher for the complete request-plane runtime. | Missing consistency boundary | Add immutable whole-runtime generations and O(1) commit. |
| G-16 | P0 | HTTP request lifetime alone is insufficient for terminal work, heartbeats, delayed finalizers, and provider references. | Retirement correctness gap | Add transferable async/provider generation pins. |
| G-17 | P0 | Repeated reloads during a very long stream could retain unbounded runtimes and transports. | Resource-bound gap | Add a finite retired-generation/resource budget and reject later publication. |
| G-18 | P0 | A full rebuild could duplicate model/catalog refresh loops and race on shared caches. | Worker/cache ownership gap | Add prepare/activate/quiesce/close phases and clear cache ownership. |
| G-19 | P0 | Model registry refresh can change the active registry during a logical request. | Mid-request consistency gap | Bind one immutable model snapshot for all request attempts. |
| G-20 | P0 | No process-owned management listener remains available when the data-plane handler changes. | Control-plane availability gap | Run reload/status on a separate stable management server. |
| G-21 | P0 | Management authentication and non-loopback posture are undefined. | Security gap | Default loopback; require strong explicit auth for broader/multi-user posture. |
| G-22 | P1 | Concurrent API calls and repeated signals have no busy, queue, or coalescing semantics. | Concurrency gap | One active attempt, bounded signal coalescing, API conflict result. |
| G-23 | P1 | Request cancellation would normally cancel an HTTP-triggered reload. | Ownership gap | Transfer accepted work to a bounded host-owned context and retain status. |
| G-24 | P1 | No effective normalized identity or no-op result exists. | Versioning/operability gap | Compute private effective identity and safe public generation metadata. |
| G-25 | P1 | Current config examples mix process topology and request-plane fields without reload classification. | Documentation/migration gap | Publish an explicit matrix and classify every new field in tests. |
| G-26 | P1 | External plugin instances may not support simultaneous old/new configuration handles. | Adjacent architecture gap | Require overlap support or pre-publication typed rejection. |
| G-27 | P1 | Reload failure could be conflated with active readiness and cause an orchestrator restart. | Availability/observability gap | Separate reload-control posture from data-plane readiness. |
| G-28 | P1 | `pkg/lipruntime.ExecutorView` currently returns one concrete executor; a caller retaining it would remain on the old generation, and its returned streams outlive `Execute`. | Public facade/lifetime gap | Add a stable delegating execution view that pins the selected generation through stream terminal/close and uses process-owned cross-generation cancellation. |
| G-29 | P1 | There are no connection-continuity, linearizability, race, leak, malformed-source, or reload-soak gates. | Release evidence gap | Add deterministic RED scenarios and release gates before implementation. |
| G-30 | P1 | Current metrics could use generation-specific labels and create unbounded cardinality. | Observability risk | Expose aggregate gauges and bounded fixed labels only. |
| G-31 | P2 | The exact default retention count, management port, and cleanup retry policy are not yet code-owned. | Research/implementation decision | Keep behavior bounded and startup-fixed; finalize exact defaults during contract-first implementation. |

## Requirements Remediation Performed

The initial requirements were corrected after the gap analysis to:

- prohibit every implicit file-monitoring and retry mechanism;
- make SIGHUP and the management API explicit adapters over one coordinator;
- keep the source path fixed and forbid arbitrary path or YAML payload submission;
- require bounded one-document strict loading and full deterministic candidate construction;
- make publication last-good, transactional, and no-op aware;
- define a complete immutable generation rather than mutable component slots;
- preserve the listener and server while pinning requests, streams, and async work;
- split process services from request-plane generation resources;
- classify all top-level fields and reject startup-only changes atomically;
- require add/change/remove behavior for configured backend/frontend/feature instances;
- constrain connector activation to already available factory kinds;
- bind model snapshots per logical request and correlate model/config versions;
- add candidate-safe lifecycle, quiesce, close, and retention limits;
- define signal concurrency and cross-platform behavior;
- define a separate loopback-default management server and security posture;
- separate reload health from active data-plane readiness;
- add bounded logs, metrics, traces, status, and audit semantics;
- add a stable public execution facade whose calls acquire the current generation, whose returned streams retain it, and whose A-leg cancellation crosses generation publication;
- add public runtime facade requirements;
- add TDD, linearizability, no-drop, race, leak, fuzz, benchmark, and soak gates.

## Architecture Options Considered

### Option A: Mutate the active executor and handlers under locks

**Approach:** add setters or atomic fields for backends, routes, model registry, hooks, auth, and limits.

**Advantages:**
- superficially small initial diff;
- reuses the current server and executor object.

**Disadvantages:**
- no single consistency boundary across middleware, handler mounts, executor, features, and model views;
- requests can observe mixed generations;
- locks enter the hot path and become difficult to order;
- resource replacement and rollback are component-specific;
- long-lived streams retain function closures and clients that setters cannot safely reclaim.

**Decision:** rejected.

### Option B: Rebuild and restart the current HTTP server in process

**Approach:** run `BuildBootstrap` again, call `Shutdown`, create a new `http.Server`, and rebind.

**Advantages:**
- reuses current bootstrap code;
- simple resource ownership.

**Disadvantages:**
- violates the no-drop requirement;
- terminates or drains active SSE and HTTP/2 streams;
- creates bind gaps and port reuse concerns;
- still duplicates process-global tracing, metrics, stores, and workers during overlap.

**Decision:** rejected.

### Option C: Stable host with process services and immutable generations

**Approach:** construct process services once, compile a complete candidate generation beside the active one, publish through one atomic pointer, and retire by reference drain.

**Advantages:**
- one linearizable publication boundary;
- no listener replacement;
- request/stream consistency;
- last-good rollback;
- explicit resource ownership and bounded retirement;
- aligns with existing snapshot/model publication patterns.

**Disadvantages:**
- requires a deliberate runtimebundle ownership refactor;
- introduces lifecycle and retained-generation complexity;
- demands extensive race and no-drop testing.

**Decision:** selected.

### Option D: Blue/green process replacement behind an external supervisor

**Approach:** launch a second proxy process and switch an external load balancer or inherited listener.

**Advantages:**
- strong process isolation;
- minimal in-process lifecycle changes.

**Disadvantages:**
- does not preserve already accepted TCP/HTTP/2/SSE connections without a complex socket handoff;
- shifts correctness to deployment-specific infrastructure;
- does not satisfy the standard binary’s explicit runtime update requirement;
- duplicates durable and in-memory state unless independently solved.

**Decision:** out of scope; deployments may still use it as an additional operational pattern.

## Selected Direction

1. Resolve and retain the absolute configuration source at startup.
2. Build process-owned services once from startup-only configuration.
3. Compile the initial configuration into generation 1.
4. Serve through a stable generation dispatcher and a stable process-owned management server.
5. Accept reload only from SIGHUP or authenticated POST.
6. Serialize candidate builds and keep all build work off the request path.
7. Read, strictly decode, normalize, validate, fingerprint, and classify the candidate.
8. Reject any startup-only diff without partial application.
9. Construct all generation-owned backends, frontends, features, routes, model views, and handler state in isolation.
10. Complete candidate-safe preparation and rollback registration.
11. Atomically swap the active generation.
12. Quiesce the prior generation and retain it for request, stream, async, and provider work.
13. Close retired resources only after complete drain.
14. Reject further publication when the safe retained-generation budget is exhausted.
15. Expose safe attempt/generation status and bounded observability.
16. Require another explicit trigger after every failure; never watch or auto-retry.

## Implementation Complexity and Risk

- **Effort: XL.** The change crosses configuration, runtime assembly, HTTP serving, plugin lifecycle, model publication, shared state, public facade, observability, and release testing.
- **Risk: High.** Incorrect ownership or lease logic can cause connection drops, mixed-generation execution, state loss, provider-resource leaks, or shutdown races.
- **Primary risk controls:** contract-first interfaces, process/generation ownership inventory, typed reloadability matrix, one atomic commit, transferable generation leases, bounded retention, and race/leak/no-drop soak gates.

## Design Validation

### Validation method

The generated design was reviewed against:

- every numbered acceptance criterion;
- root and Kiro architecture rules;
- current `runtimebundle`, `stdhttp`, config, model, snapshot, terminal-work, and public-runtime code;
- current and adjacent connector specifications;
- streaming, output-commitment, secure-session, continuity, accounting, and diagnostics invariants;
- malformed source, lifecycle failure, trigger concurrency, shutdown, resource pressure, and long-stream scenarios.

### Design Review Summary

The initial design correctly selected immutable whole-runtime generations, but three issues were architectural blockers: management was still inside the swappable handler, retirement tracked only HTTP requests, and candidate construction still rebuilt process-global services. Those issues would have violated availability, terminal-work correctness, and state continuity.

### Critical issues and corrections

#### DV-C1: Management API shared the swappable data-plane generation

- **Concern:** An invalid candidate or a frontend/diagnostics route change could remove or replace the only reload/status control surface.
- **Impact:** Operators could lose recovery access precisely after a failed administrative change, and a broad data-plane bind could accidentally expose reload.
- **Correction:** The final design uses a separate process-owned management listener and startup-fixed authenticated routes.
- **Traceability:** 1.3, 12.1-12.11, 13.1-13.3
- **Evidence:** `design.md` sections “Stable Process Host,” “Management API,” and “Startup-Only Matrix.”

#### DV-C2: Retirement counted only `ServeHTTP` lifetime

- **Concern:** Terminal work, provider finalization, heartbeats, and request-spawned cleanup can outlive the HTTP handler.
- **Impact:** Old backend instances could close while still needed, corrupt settlement, or strand durable work.
- **Correction:** The final design adds transferable request/async/provider generation pins, quiesce-versus-close phases, pending-provider retention, and a finite retained-generation budget.
- **Traceability:** 5.3-5.10, 10.1-10.12, 13.8-13.9
- **Evidence:** `design.md` sections “Generation Lease Protocol,” “Retirement State Machine,” and “Terminal Work and Provider Ownership.”

#### DV-C3: Candidate compilation rebuilt global stores and observability

- **Concern:** Reusing current `BuildBootstrap` would replace global tracing, create new metrics registries, reset memory stores, open duplicate pools, and duplicate workers.
- **Impact:** Reload could split sessions/continuity/accounting state and create process-wide races or leaks.
- **Correction:** The final design splits process bootstrap from generation compilation, injects shared services explicitly, and rejects topology changes through the reloadability matrix.
- **Traceability:** 6.1-6.10, 7.1-7.9, 13.4-13.8
- **Evidence:** `design.md` sections “Process and Generation Ownership,” “Generation Compiler,” and “Reloadability Matrix.”

### Additional hardening corrections

| ID | Finding | Correction applied |
|---|---|---|
| DV-H1 | Same backend ID could be mutated in place. | Always construct a new generation-specific instance; retain old instance until drain. |
| DV-H2 | Model registry could refresh during route planning. | Bind one immutable model snapshot per logical request and all attempts. |
| DV-H3 | Repeated HUP could create unbounded workers. | One active build plus at most one coalesced pending signal. |
| DV-H4 | API request cancellation could abort accepted work. | Transfer accepted reload to a bounded host-owned context and retain status. |
| DV-H5 | Raw-byte digest would republish for comments only and could expose secret correlation. | Use private effective identity for no-op and safe public generation metadata. |
| DV-H6 | Candidate probes could become billable provider traffic. | Prohibit billable inference as a validity gate; optional readiness is explicit/non-billable. |
| DV-H7 | External plugin reconfiguration overlap was unspecified. | Require concurrent instance handles or pre-publication typed rejection. |
| DV-H8 | Old model/health refresh loops could overlap indefinitely. | Add quiesce on retirement and close after references drain. |
| DV-H9 | Reload failure could fail readiness. | Separate reload-control posture from active data-plane readiness. |
| DV-H10 | A retention timeout could violate no-drop. | Reject later publication at the retention limit; never kill old streams for reload. |
| DV-H11 | Startup/check-config/reload could drift. | Require one load/normalize/validate pipeline and compatibility wrappers. |
| DV-H12 | New config fields could default to accidental reloadability. | Add exhaustive classification tests that fail on unclassified fields. |

### Design strengths

- The design extends proven atomic immutable-publication patterns instead of introducing mutable global registries.
- It preserves the repository’s streaming, B2BUA, secure-session, plugin, and no-retry-after-output ownership boundaries.
- It treats invalid input, restart-required changes, lifecycle failure, and resource pressure as explicit last-good outcomes.
- It keeps the request hot path small while assigning all goroutines, channels, closers, and state to explicit owners.

### Final validation verdict

**GO after corrections.**

The final design has one coherent publication boundary, preserves active connections, protects shared process state, provides deterministic restart-required behavior, and supplies a TDD path with race, leak, no-drop, fault, and soak evidence.

## Open Implementation Decisions

These decisions may be finalized during contract-first implementation without changing the requirements:

1. Exact internal package names after import-cycle analysis.
2. The default loopback management port and whether local single-user mode requires an explicit secret by default.
3. The default retained-generation count/resource budget.
4. The exact private effective-canonicalization encoding.
5. Whether generation lease state uses a packed atomic word or an equivalent proven state/ref protocol.
6. Exact timeout defaults per source, compile, preparation, and cleanup stage.
7. Which model/catalog cache adapters become process-owned versus generation-quiesced.
8. The exact internal backend lifecycle wrapper shape.
9. The cleanup retry/backoff policy after a drained-generation close error.
10. The reviewed benchmark threshold used to detect dispatcher overhead regression.

Any proposal to add file watching, automatic retries, partial publication, arbitrary management-supplied paths/YAML, forced old-stream termination, runtime plugin installation, or in-place mutation of an active generation requires requirements and design revalidation.
