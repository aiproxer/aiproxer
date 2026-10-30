# Requirements Document

## Introduction

Go-LIP is intended to serve large numbers of long-lived and concurrent LLM requests continuously. Its current standard runtime is assembled once at process startup: configuration is loaded, backends and feature chains are constructed, one HTTP handler graph is mounted, and the process serves that graph until shutdown. Any operational configuration change therefore requires a process restart, which can terminate or disrupt active streams and prevents introducing a newly configured backend instance without a maintenance window.

The `versioned-runtime-reloadable-proxy-configuration` feature adds explicit, transactional runtime configuration reload. A reload constructs a complete immutable request-plane generation beside the active generation, validates and prepares it without exposing traffic, atomically publishes it for new requests, and retires the previous generation only after all work bound to it has drained. The data-plane listener and `http.Server` remain alive for the process lifetime.

Configuration reload is deliberately operator-triggered. Editing the configuration file alone has no runtime effect. There is no file watcher, directory watcher, modification-time polling, debounce loop, periodic rescan, or automatic retry. Supported triggers are `SIGHUP` on platforms that provide it and an authenticated management HTTP API that is bound to loopback by default. Both triggers invoke the same coordinator and the same validation and publication pipeline.

## Boundary Context

- **In scope:** explicit reload triggers; bounded, integrity-checked, strict configuration loading; normalized effective configuration identity; field-level reloadability classification; immutable whole-runtime request-plane generations; zero-drop atomic publication; request, stream, and asynchronous-work generation pinning; backend/frontend/feature replacement; versioned routing and model inventory; shared process-service continuity; safe generation retirement; management API and signal adapters; observability; public runtime facade; deterministic race, leak, fault, and soak evidence.
- **Out of scope:** file watchers or automatic reload; process replacement; listener handoff between processes; runtime download or installation of connector code; arbitrary Go shared-object loading; remote configuration services; distributed consensus for synchronizing configuration across proxy processes; changing canonical protocol semantics; forcibly terminating old streams to complete a reload.
- **Adjacent expectations:** the active `backend-connector-plugin-architecture` and `generic-compatible-backend-modes` specifications remain authoritative for connector classification and factory discovery. This feature may activate or reconfigure instances of factory kinds already available to the running process; it does not install new artifacts or silently rescan plugin directories.
- **Boundary ownership:** reload policy and generation publication are process/runtime orchestration concerns; filesystem and signal handling are infrastructure/driving adapters; backend construction remains in backend factories; HTTP management remains in `internal/stdhttp`; canonical routing and stream semantics remain core-owned.
- **Optional hexagonal lens:** the reload coordinator is application orchestration; configuration source and management/signal entrypoints are driving adapters; backend, store, inventory, and observability implementations are driven adapters; `cmd/lipstd`, runtime host, and `runtimebundle` are composition roots.
- **Revalidation triggers:** changes to `runtimebundle.Build` ownership, plugin lifecycle contracts, `execbackend.Backend`, frontend mount contracts, model registry publication, secure-session or continuity stores, terminal-work provider ownership, routing/affinity/health state, `http.Server` setup, tracing/metrics bootstrap, configuration-source integrity policy, management browser-origin policy, or backend plugin discovery/trust policy.

## Requirements

### Requirement 1: Explicit Operator-Controlled Reload Triggers

**Objective:** As an operator, I want configuration reload to occur only after an explicit administrative action, so that production behavior cannot change because of incidental filesystem activity.

#### Acceptance Criteria

1.1. When the process starts successfully, the proxy shall publish the startup configuration through the same generation-publication model used by later reloads.
1.2. When a supported Unix-like process receives `SIGHUP`, the proxy shall request one explicit configuration reload without treating the signal as process termination.
1.3. When an authorized client invokes the configured reload management endpoint, the proxy shall request one explicit configuration reload.
1.4. The proxy shall route signal-triggered and API-triggered reloads through one coordinator and one candidate validation, compilation, publication, and retirement pipeline.
1.5. While no explicit reload trigger is accepted, the proxy shall continue using the active configuration even if the source file is created, removed, renamed, replaced, touched, or edited.
1.6. The proxy shall not implement a file watcher, directory watcher, modification-time poller, periodic configuration scan, debounce loop, implicit reload, or automatic retry of a failed reload.
1.7. The management API shall not accept arbitrary configuration paths, inline YAML, remote URLs, or source overrides; every reload shall re-read the absolute source path fixed at process startup.
1.8. Where `SIGHUP` is unavailable, the proxy shall remain reloadable through the management API and shall compile without a platform-specific signal dependency.
1.9. When graceful shutdown begins, the proxy shall reject new reload triggers and shall not publish a new generation after shutdown ownership has transferred to the shutdown path.

### Requirement 2: Bounded, Strict, Integrity-Checked, and Deterministic Configuration Loading

**Objective:** As an operator, I want every candidate configuration loaded through a strict and deterministic integrity protocol, so that malformed, torn, partial, or ambiguous input can never become active.

#### Acceptance Criteria

2.1. When a reload begins, the configuration source shall open the fixed startup path, capture a stable platform file identity and metadata from the opened handle, read one bounded byte snapshot, revalidate the same handle after reading, and verify that the path still resolves to that handle identity before accepting the source snapshot.
2.2. If the source is missing, unreadable, non-regular or unsupported, empty, whitespace-only, unstable during the read, or exceeds the startup-fixed size limit, then the reload shall fail without replacing the active generation.
2.3. If the source contains invalid YAML, more than one YAML document, trailing non-document content, duplicate structure rejected by project policy, or an unknown typed core field, then the reload shall fail with an actionable safe error.
2.4. Where plugin-private configuration is represented as an opaque `yaml.Node`, the loader shall preserve that subtree for its registered factory while keeping root and core-owned configuration strict.
2.5. The loader shall apply defaults, startup CLI overrides, startup environment-derived overrides, standard-distribution feature injections, and normalization in one deterministic order shared by startup, `check-config`, and runtime reload.
2.6. The effective candidate shall run all applicable core validation, access/auth posture validation, routing alias validation, custom-compatible prefix validation, feature composition validation, frontend validation, backend-factory validation, lifecycle compatibility validation, and runtime assembly validation before publication.
2.7. `check-config` shall invoke the same generation compiler in an isolated dry-run mode with complete rollback, or a provably equivalent shared validation pipeline; parity tests shall prove that every deterministic candidate rejection produced by reload is also produced by `check-config` without publishing or retaining candidate resources.
2.8. Candidate validation shall not make billable LLM calls or require transient provider availability; any optional remote readiness probe shall be explicit, non-billable, bounded, and outside the mandatory validity decision.
2.9. A materially changed source shall be eligible for reload only when delivered through the documented atomic replacement protocol: the new path target shall have a different stable file identity from the active source, and a same-identity source whose private digest changed shall be rejected as a non-atomic in-place update. A same-identity source with the same private digest may return a no-op. Platforms or filesystems that cannot prove stable identity and atomic replacement shall report runtime source reload unavailable unless an explicitly supported integrity-verified source adapter is configured. Tests shall prove that valid-looking truncated or partially rewritten YAML cannot publish.
2.10. Validation errors shall identify safe field paths, plugin instance IDs, source-integrity categories, and bounded reason categories without including raw secrets, opaque configuration values, credentials, prompt data, full paths where prohibited, or full file contents.

### Requirement 3: Transactional Last-Good Publication

**Objective:** As an operator, I want reload to be all-or-nothing, so that a failed candidate cannot leave the proxy in a partially updated state.

#### Acceptance Criteria

3.1. The proxy shall keep the previously published generation active until a complete candidate is ready for atomic publication.
3.2. If any read, source-integrity, decode, normalization, validation, diff, construction, preparation, or readiness stage fails, then the active generation shall remain unchanged.
3.3. Candidate construction shall use isolated mutable build state and shall not mutate the active configuration, executor, backend map, feature chains, frontend routes, model registry, auth state, or routing policy in place.
3.4. Every candidate-owned resource shall be registered for rollback when acquired and shall be released in reverse ownership order if candidate preparation fails.
3.5. A candidate containing both reloadable changes and restart-required changes shall be rejected as one transaction; the proxy shall not apply only the reloadable subset.
3.6. When the normalized effective candidate is identical to the active effective configuration, the coordinator shall return a successful no-op result without publishing a new generation.
3.7. The no-op comparison shall include secret-sensitive and environment-resolved effective values through an internal non-exported identity, while logs and APIs shall expose only secret-safe generation metadata.
3.8. Only successful publication of a materially different effective configuration shall allocate the next monotonically increasing process-local configuration generation ID.
3.9. If candidate construction or lifecycle code panics at an isolated boundary, then the reload attempt shall fail, candidate resources shall be reclaimed, and the active generation shall continue serving.
3.10. Each trigger shall produce one terminal result classified as published, no-op, invalid, restart-required, busy, retention-blocked, canceled, preparation-failed, source-integrity-failed, or internal-failed.

### Requirement 4: Immutable and Coherent Runtime Generations

**Objective:** As a runtime maintainer, I want each published configuration represented by one immutable coherent generation, so that a request cannot observe a mixture of old and new components.

#### Acceptance Criteria

4.1. A published runtime generation shall own only one private immutable generation bundle containing its normalized generation configuration projection, executor, backend instance set, routing/default-route/alias policy, capability view, frontend handler graph, transport-auth policy, feature/hook surface, model snapshot binder, request-plane limits, and generation-owned resources; process-owned services shall remain outside that bundle under a separate owner.
4.2. The generation shall carry a monotonic configuration generation ID, previous-generation ID, source/effective identity held privately, safe public fingerprint, trigger kind, loaded timestamp, published timestamp, and relevant model/policy generation references.
4.3. Configuration projections, maps, slices, route tables, hook chains, backend maps, handler dependencies, and model snapshots owned by a published generation shall be deeply immutable or privately encapsulated so callers cannot mutate them after preparation.
4.4. A configuration change shall construct a new generation and new generation-owned component instances instead of mutating objects already reachable by in-flight requests.
4.5. Every new request shall bind exactly one configuration generation before transport-authenticated request execution reaches mutable routing or backend selection.
4.6. Routing decisions, attempt lineage, terminal work, access logs, traces, and protected diagnostics shall retain the bound configuration generation ID where that correlation is safe and relevant.
4.7. The proxy shall distinguish the configuration generation from subordinate model-inventory, model-catalog, authority, rating, and policy snapshot versions.
4.8. Public status and diagnostics shall expose only bounded, non-secret generation metadata and shall never expose private effective digests, mutable bundle handles, or serialized configuration.
4.9. When a generation is published, all components reachable through its handler and executor shall either be privately owned by that generation or be non-owning references to explicitly classified process services; no direct retention of mixed-ownership bootstrap aggregates or unclassified closer lists shall remain.

### Requirement 5: Zero-Drop Atomic Data-Plane Publication

**Objective:** As a client, I want active requests and streams to continue without interruption during reload, so that configuration maintenance does not cause connection drops or protocol corruption.

#### Acceptance Criteria

5.1. The data-plane TCP listener and `http.Server` shall remain bound and serving for the process lifetime across every successful or failed reload.
5.2. Publication shall atomically replace one active generation pointer behind a stable data-plane dispatcher without shutting down, closing, or replacing the data-plane server.
5.3. A request that acquired generation N before publication of generation N+1 shall continue using generation N until its request and bound asynchronous work complete.
5.4. A request arriving after publication shall acquire generation N+1 and shall not enter generation N.
5.5. A persistent HTTP/1.1 keep-alive connection shall select the active generation independently for each new HTTP request; the TCP connection itself shall not be pinned to one generation.
5.6. Concurrent HTTP/2 streams on one connection shall bind generations independently according to when each stream enters the dispatcher.
5.7. An SSE or other streaming HTTP response shall retain its generation for the complete stream lifecycle, including cancellation and terminal cleanup.
5.8. Reload shall not force-close, truncate, re-encode, retry, or transparently migrate an in-flight request or stream to a newer generation.
5.9. The atomic publication operation shall perform only bounded validation of prepared state, retention-budget reservation, generation-ID assignment, active-pointer/state commit, and retirement marking. It shall perform no configuration I/O, backend construction, lifecycle start, quiesce call, cleanup call, model fetch, or old-generation drain wait. Quiesce and cleanup shall run after commit under separately owned lifecycle coordination, and their failures shall be reported independently.
5.10. Where a frontend uses hijacking or upgrades in the future, the connection owner shall explicitly transfer and retain the generation lease until the upgraded connection closes.

### Requirement 6: Process-Service Continuity and Ownership

**Objective:** As a platform operator, I want durable and process-wide state to survive configuration publication, so that reload cannot reset sessions, continuity, accounting, or operational telemetry.

#### Acceptance Criteria

6.1. The runtime shall classify every assembled resource as process-owned, generation-owned, or request/async-work-owned before runtime reload is enabled.
6.2. The continuity store, secure-session store, control-plane stores, metering journal, authority/concurrency stores, database pool registry, and terminal-work store/processor shall normally remain process-owned across generations.
6.3. Reload shall not create a second terminal-work processor, duplicate a process-global durable-store worker, or open an independent unbounded pool for a store already owned by process services.
6.4. The logger sink, OpenTelemetry provider/exporter, Prometheus registry, process collectors, data-plane server, management server, and plugin discovery/trust catalog shall remain process-owned in the initial implementation.
6.5. Shared frontend decode-admission budgets and other process-capacity limiters shall remain process-owned so overlapping generations cannot multiply configured capacity.
6.6. In-memory continuity, secure-session, A-leg lifecycle/cancellation, state, affinity, routing-health observation, and similar mutable service identity shall not be silently reset merely because a request-plane generation changes.
6.7. Where mutable routing state is reusable, the proxy shall key reuse by stable backend instance identity and compatible configuration identity; a materially changed backend instance shall not inherit unsafe health or affinity state.
6.8. If an affinity or health record references a backend no longer present in the active generation, then new routing shall ignore or safely invalidate that record rather than selecting an unavailable backend.
6.9. Generation compilation shall receive process services through explicit construction inputs, shall retain only non-owning classified references where necessary, and shall not reach services through globals or a service locator.
6.10. Process-owned services shall close only during process shutdown, after new admissions stop and all retained generations and owned workers have completed their shutdown ordering.

### Requirement 7: Explicit Field-Level Reloadability Policy

**Objective:** As an operator, I want a deterministic statement of which settings reload and which require restart, so that a configuration change cannot appear successful while silently retaining old process topology.

#### Acceptance Criteria

7.1. The system shall maintain an explicit typed reloadability policy covering every top-level configuration section and every startup/runtime override.
7.2. The reloadability decision shall compare normalized typed values and opaque plugin rows through maintained comparators; it shall not depend on reflection-driven field walking or ad hoc YAML text comparison.
7.3. Data-plane listener address and server timeouts, management listener/auth settings, access mode, auth-handler class, global tracing topology, metrics enablement/path, logger sink/format, database pool topology, store type/path/DSN/schema mode, plugin discovery/trust paths, source-integrity policy, and process-capacity budgets shall be startup-only in the initial implementation.
7.4. Backend/frontend/feature rows, routing selectors and aliases, request-plane health policy, model catalog/inventory policy, generation-owned HTTP client tuning, request/stream limits, local auth records within a fixed auth mode, and request-plane policy/rating configuration shall be reloadable where their backing process-service topology is unchanged.
7.5. If a candidate changes a startup-only field, then the reload shall fail with a restart-required result before candidate publication.
7.6. A restart-required result shall return a deterministic sorted and bounded set of safe field paths plus the total number of blocked changes, without returning old or new secret values.
7.7. Startup CLI gates and overrides, including multi-user posture and command-line stream-recovery overrides, shall remain fixed and shall be reapplied consistently when deriving every candidate.
7.8. Tests shall enumerate every top-level configuration section and fail when a newly added field lacks an explicit reloadability classification.
7.9. Expanding a startup-only field to runtime-reloadable shall require focused lifecycle, security, continuity, rollback, and no-drop revalidation rather than an undocumented comparator change.

### Requirement 8: Dynamic Backend, Frontend, and Feature Recomposition

**Objective:** As an operator, I want to add, change, disable, or remove configured request-plane components at runtime, so that provider and policy changes do not require a proxy restart.

#### Acceptance Criteria

8.1. When a valid candidate adds an enabled backend instance whose factory kind is already available to the running process, the new generation shall construct and route to that instance without restarting the proxy.
8.2. The proxy shall support adding a generic compatible backend row that was absent from the startup configuration, provided its built-in factory kind is registered and the row passes current validation.
8.3. When a backend instance retains the same runtime ID but changes generation-owned configuration, the new generation shall receive a newly constructed instance and the old generation shall keep the prior instance until it drains.
8.4. When a backend instance is disabled or removed, new requests shall not select it after publication, while already bound requests and pending terminal work may continue using its retired-generation resources.
8.5. When frontend or feature rows change, the candidate shall build a complete new handler and hook/extension graph and shall publish it as part of the same generation transaction.
8.6. Plugin-private configuration shall be validated by the selected factory before publication; a failure in one new or changed plugin instance shall reject the complete candidate.
8.7. Reload shall not download, install, update, discover from new paths, or execute an untrusted connector artifact; the process-owned factory catalog is fixed at startup in the initial implementation.
8.8. Where the executable backend-plugin architecture is present, reload may activate an already discovered factory kind, and the plugin host shall support concurrent old/new configured instance handles or reject the candidate before publication with a typed lifecycle-compatibility error.
8.9. Backend and feature construction shall expose idempotent candidate rollback and retired-generation close semantics for resources such as clients, subprocess instance handles, goroutines, and idle transports.
8.10. Environment-backed or secret-provider-backed credentials shall be resolved for the candidate without logging values, and their resolved identity shall participate privately in no-op and instance-replacement decisions.
8.11. A newly constructed backend shall not be required to execute a billable inference request as a publication gate.

### Requirement 9: Versioned Routing and Model Availability

**Objective:** As a routing operator, I want routing and model availability to change coherently with configuration, so that new requests never see a model catalog that disagrees with the active backend set.

#### Acceptance Criteria

9.1. A reloadable candidate may change default routes, model aliases, route prefixes, attempt limits, health policy, affinity policy, and configured backend/model mappings as one generation.
9.2. The candidate shall validate every effective default route and alias result against the candidate backend and model capability view before publication.
9.3. The model registry and model-catalog view associated with a configuration generation shall be initialized and published coherently with that generation.
9.4. Each request shall bind one immutable model-registry/catalog snapshot for route planning and all failover or parallel attempts created by that logical request.
9.5. A model inventory refresh occurring after a request binds shall not change that request’s candidate legality or model mapping mid-flight.
9.6. `/v1/models`, model diagnostics, and route diagnostics shall report coherent configuration-generation and model-snapshot identity and shall use an appropriate generation-aware ETag where supported.
9.7. If a candidate model inventory is structurally invalid or contains no usable models where current policy requires usable inventory, then the candidate shall fail and the previously active generation shall remain available.
9.8. Existing configured background model/catalog refresh remains separately owned and shall not become a configuration-file watcher or implicit configuration reload path.
9.9. When a backend is removed, new model snapshots shall not advertise its models, while retired requests may continue using the model snapshot and backend instance bound to their generation.
9.10. The system shall not infer new backend capabilities or model support merely because a reload introduced an unrecognized model identifier.

### Requirement 10: Safe Generation Retirement and Resource Reclamation

**Objective:** As a runtime maintainer, I want retired generations to drain safely and remain bounded, so that reload neither leaks resources nor violates the no-drop guarantee.

#### Acceptance Criteria

10.1. A generation shall have explicit preparing, active, retiring, quiescing, quiesced, drained, closing, closed, and failed lifecycle states or equivalent observable transitions.
10.2. The generation manager shall prevent new leases from attaching to a retiring generation while allowing existing request leases to finish.
10.3. Generation retention shall account for active HTTP requests, streaming responses, request-spawned asynchronous work, lease heartbeats, delayed finalizers, and pending terminal/provider work that still needs generation-owned resources.
10.4. The lease implementation shall use a race-safe atomic reference/state protocol or equivalent and shall not use `sync.WaitGroup.Add` racing with `Wait` as a general request refcounter.
10.5. After publication marks a prior generation retiring, a separately owned lifecycle worker may quiesce refresh loops or admission-independent generation workers without blocking publication or new requests, while preserving immutable state and resources required by pinned work.
10.6. After every generation reference and required pending-provider reference reaches zero, the runtime shall stop generation lifecycles in reverse order and close generation-owned resources exactly once.
10.7. Pending durable terminal work shall retain or resolve the provider generation required for correct settlement/finalization and shall prevent unsafe backend removal while that dependency is unresolved.
10.8. The runtime shall enforce a startup-fixed finite limit on simultaneously retained retired generations or equivalent retained-resource budget.
10.9. If publishing a candidate would exceed the safe retained-generation/resource limit, then publication shall be rejected before the active pointer changes and the candidate shall be rolled back.
10.10. The proxy shall report which retention category is blocking publication through bounded safe diagnostics without exposing request content or credentials.
10.11. A long-running stream shall not be forcibly terminated merely to free a retired-generation slot; operational pressure shall reject a later reload instead.
10.12. Quiesce or cleanup failures shall be reported and retried only through an explicitly owned lifecycle/cleanup policy; they shall not roll back or corrupt the already active newer generation.

### Requirement 11: Unix Signal Handling and Trigger Concurrency

**Objective:** As an operator, I want predictable signal behavior under concurrent administration, so that repeated signals cannot create unbounded work or accidentally shut down the proxy.

#### Acceptance Criteria

11.1. On supported Unix-like platforms, `SIGHUP` shall be registered separately from `SIGINT` and `SIGTERM`.
11.2. `SIGINT` and `SIGTERM` shall retain their graceful-shutdown semantics and shall not trigger configuration reload.
11.3. The signal handler shall perform only bounded non-blocking trigger delivery and shall not read configuration, construct runtime objects, or log secrets in the signal-notification path.
11.4. The coordinator shall allow at most one candidate build at a time.
11.5. If `SIGHUP` arrives while a reload is active, then the proxy may coalesce signals into at most one pending explicit reload and shall record the coalesced count.
11.6. API callers shall receive a deterministic busy/conflict result rather than creating an unbounded queue when a reload is already active.
11.7. The implementation shall not start one goroutine per signal or per rejected trigger and shall own every reload worker, channel, cancellation path, and shutdown wait.
11.8. Platform-specific files and tests shall prove Unix `SIGHUP` behavior and non-Unix API-only behavior without importing unsupported signal constants.
11.9. If shutdown races with a pending signal reload, then shutdown shall win publication ownership and the candidate shall be canceled and reclaimed.

### Requirement 12: Dedicated Management HTTP API and Security Posture

**Objective:** As an administrator, I want a secure and stable HTTP control surface for reload, so that automated operations can trigger and inspect reload independently from the swappable data plane.

#### Acceptance Criteria

12.1. The reload management API shall run on a process-owned HTTP listener and handler that is not replaced when request-plane generations change.
12.2. The management listener shall bind to an explicit loopback address by default and shall not inherit a broad data-plane bind.
12.3. The API shall provide a `POST` reload action and a read-only status action under startup-fixed paths.
12.4. The reload action shall re-read only the fixed startup configuration source and shall accept no configuration body, path, URL, shell command, or plugin-install instruction.
12.5. In explicit single-user local mode, a loopback-only management endpoint may use the documented local trust posture; multi-user mode or any non-loopback management bind shall require a dedicated strong startup-fixed authentication secret or injected administrator authenticator. Cookie-based browser authentication shall not be used for the reload action.
12.6. A non-loopback management bind shall require explicit opt-in, startup validation, authentication, request-size bounds, and protected diagnostics posture.
12.7. Unauthorized, malformed-method, wrong-content-type, oversized, and browser-originated cross-origin requests shall be rejected before invoking the coordinator. The default non-browser API shall reject any non-empty `Origin` unless it exactly matches an explicitly configured startup-fixed allowlist, shall reject `Sec-Fetch-Site: cross-site` or `same-site`, shall permit absent fetch-metadata headers for non-browser administrative clients, shall not emit permissive CORS headers, and shall not treat a preflight request as authorization. Tests shall cover simple cross-origin POSTs, credentialed/preflight attempts, spoofed allowed origins, same-origin policy where enabled, and ordinary CLI clients.
12.8. The reload response shall use stable safe result categories and appropriate HTTP status codes for published/no-op, invalid, restart-required, busy, retention-blocked, canceled, source-integrity failure, and internal/preparation failure outcomes.
12.9. Once an authorized reload is accepted, client disconnection or request-context cancellation shall not cancel the host-owned bounded reload attempt; the terminal result shall remain available from status.
12.10. Management responses shall expose attempt and generation metadata, safe field paths, timestamps, and bounded reason categories without serializing configuration or secret values.
12.11. Management listener address, paths, authentication mode, authentication material source, browser-origin allowlist, body limits, source-integrity policy, and reload timeout shall be startup-only fields.

### Requirement 13: Failure Isolation, Readiness, and Shutdown Ordering

**Objective:** As an operator, I want reload failure isolated from healthy traffic, so that a bad administrative change does not cause an availability incident.

#### Acceptance Criteria

13.1. If a reload fails before publication, then active data-plane health and readiness shall continue to reflect the last-good active generation.
13.2. The proxy shall expose reload-control status separately from data-plane readiness so a failed attempt is visible without causing an orchestrator to restart a healthy proxy by default.
13.3. Startup shall fail closed if no initial generation can be validated, prepared, and published.
13.4. A candidate shall be published only after every mandatory generation-owned component is locally prepared and every required lifecycle invariant is satisfied.
13.5. If post-publication quiesce, retirement, or cleanup of the prior generation fails, then the new active generation shall remain active and the failure shall be surfaced separately.
13.6. A failed reload shall not be retried automatically; a later attempt requires a new explicit signal or API action.
13.7. If process shutdown begins during candidate preparation, then preparation shall receive cancellation, publication shall be prohibited, and candidate rollback shall complete before shared services close.
13.8. During process shutdown, the proxy shall stop accepting new data-plane requests, drain active/retired generations under the existing graceful-shutdown policy, stop generation lifecycles, and then close process services in ownership order.
13.9. If the graceful process-shutdown deadline expires, then existing shutdown policy may terminate remaining work; a configuration reload itself shall not create a separate forced-drain deadline for old generations.
13.10. Recovery and error mapping shall preserve the existing no-retry-after-first-client-visible-output invariant within every generation.

### Requirement 14: Reload Observability and Auditability

**Objective:** As an operator, I want bounded and correlated reload telemetry, so that I can determine what happened without exposing sensitive configuration.

#### Acceptance Criteria

14.1. The proxy shall retain bounded status for the active generation, current attempt, most recent successful publication, most recent failed/no-op attempt, source-integrity posture, and retained generations.
14.2. Structured reload logs shall include attempt ID, trigger kind, safe stage, result category, active/candidate generation IDs when assigned, duration, and bounded field/reason counts.
14.3. Logs, traces, metrics, APIs, and diagnostics shall not include raw YAML, secret values, API keys, DSNs, credential-bearing URLs, opaque plugin configuration, prompts, or responses.
14.4. Prometheus metrics shall count reload attempts and measure stage/total duration using fixed bounded labels such as trigger, result, and error category.
14.5. Metrics shall expose active/retired generation counts and aggregate pinned-reference posture without using generation ID, backend ID, model ID, file path, source identity, or user-controlled text as a metric label.
14.6. Where tracing is enabled, a reload attempt shall create process-owned spans for source read/integrity, validation, compilation, preparation, publication, quiesce, and cleanup without replacing the global tracer provider.
14.7. Request, routing, and attempt diagnostics shall include the bound configuration generation and model snapshot references where cardinality and privacy policy permit.
14.8. The management status surface shall distinguish an invalid candidate, source-integrity failure, restart-required change, retention blockage, quiesce/cleanup failure, and healthy active generation.
14.9. Signal and API triggers shall be auditable through the same result vocabulary while preserving only safe requester metadata.

### Requirement 15: Concurrency, Performance, and Resource Bounds

**Objective:** As a high-throughput operator, I want reload to have negligible request-path impact and bounded control-plane work, so that 24/7 traffic remains stable under repeated administration.

#### Acceptance Criteria

15.1. Active-generation acquisition and release shall be O(1), shall perform no filesystem or network I/O, and shall not take a process-wide configuration mutex on the request path.
15.2. Request execution shall not parse configuration, compute configuration diffs, construct backends, or wait for generation retirement.
15.3. Candidate compilation shall run off the request path and shall not hold locks required by active request routing or stream delivery.
15.4. Publication shall be a bounded atomic commit independent of candidate size, quiesce duration, and old-generation drain duration.
15.5. The coordinator shall bound source bytes, decode complexity, source-integrity checks, candidate build time, remote optional probes, retained generations, status history, and concurrent reload work.
15.6. Overlapping active and retired generations shall remain bounded by the configured retention/resource policy, and generation-owned HTTP transports shall release idle connections after drain.
15.7. Deterministic load tests shall prove that reload under high HTTP/1.1, HTTP/2, streaming, cancellation, failover, and parallel-race traffic creates no connection drops, duplicate terminal events, or generation mixing.
15.8. Race-detector and goleak tests shall cover trigger concurrency, source replacement races, publication/acquire races, request/async pin transfer, shutdown races, lifecycle rollback, post-commit quiesce, and retirement cleanup.
15.9. Benchmarks shall measure dispatcher acquire/release overhead, atomic publication, candidate compilation, and retained-generation memory/resource growth against a documented baseline.
15.10. A performance regression beyond an implementation-defined reviewed threshold shall block release or require explicit documented acceptance; one benchmark run shall not be treated as proof.

### Requirement 16: Public Facade, Compatibility, and Release Evidence

**Objective:** As a maintainer and integrator, I want one supported reload capability across the standard binary and public runtime facade, so that the feature remains maintainable and independently testable.

#### Acceptance Criteria

16.1. The standard binary and `pkg/lipruntime` facade shall use the same internal reload coordinator, generation compiler, publication semantics, and status model rather than separate implementations.
16.2. The public facade shall expose explicit reload and safe status operations without exposing `internal/...` types, mutable configuration objects, provider SDK types, raw handlers, mixed-ownership bootstrap aggregates, or runtimebundle internals.
16.3. Existing startup, `check-config`, routes, inventory, and normal serving behavior shall remain compatible when runtime reload configuration is omitted or left at defaults, except that changed-content runtime reload requires the documented atomic replacement source protocol.
16.4. Existing valid backend kind strings, runtime IDs, route selectors, canonical streaming behavior, B2BUA lineage, secure-session authority, and no-retry-after-output semantics shall remain stable.
16.5. Implementation shall revalidate the active backend connector plugin architecture so a reload can activate already discovered factory kinds without adding runtime installation or untrusted discovery.
16.6. No new provider-specific or frontend-protocol-specific concept shall be added to `pkg/lipapi` solely for configuration reload.
16.7. Interfaces, lifecycle contracts, source-integrity contracts, reloadability classifiers, API contracts, and failing concurrency/no-drop scenarios shall be written before production implementation.
16.8. Deterministic tests shall require no live provider, real credential, mutable external service, or filesystem watcher.
16.9. Release evidence shall include focused unit and composed tests, `check-config`/reload rejection parity, atomic replacement/torn-write tests, browser-origin management tests, architecture gates, race and leak runs, fuzz smoke for source/decode/diff inputs, parity checks, full QA, and a bounded reload soak.
16.10. Operator documentation shall describe explicit triggers, mandatory atomic file replacement for changed content, unsupported-filesystem posture, the reloadability matrix, restart-required errors, management authentication and browser-origin rejection, version/status semantics, failure recovery, retained-generation pressure, and the absolute absence of automatic file watching.
16.11. The specification bundle and architecture documentation shall identify configuration-generation publication as a core runtime invariant and list changes that require design revalidation.
16.12. An `ExecutorView` or equivalent public execution handle obtained before a reload shall remain valid; each new `Execute` call shall bind the current generation and the returned event stream shall retain that generation until terminal completion or close.
16.13. A cancellation request for an A-leg created before publication shall remain able to reach the process-owned A-leg lifecycle and active backend work after a newer generation becomes active.
