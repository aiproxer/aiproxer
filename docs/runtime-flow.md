# Runtime flow

This document expands the request lifecycle in `docs/architecture.md`.

## Startup flow

`cmd/aipstd` is the standard distribution entrypoint. Its startup path is intentionally explicit and matches the converged ownership model in [`architecture.md`](architecture.md): **one process runtime / `ProcessServices`**, **one generation runtime** (`GenerationRuntime`), **one private-field host** (`runtimebundle.Host` from `runtimebundle.BuildHost`; **`Host.Close`** owns shutdown; **Manager-owned retirement** drains superseded generations), and **one reload contract** (`pkg/aipsdk/configreload`, explicit triggers only). Candidate assembly stays package-private and temporary (not a runtime API). `ValidateDistribution` / `check-config` is true unpublished validation (compile + rollback; never publish).

`runtimebundle.BuildHost` owns the whole sequence as one transaction and returns a complete `Host` or rolls back and returns nil; `cmd/aipstd serve` and public `aipruntime.Build` are its only production callers. Public `aipruntime.Build` normalizes descriptor-bound registrations on `aipruntime.Options`, then calls canonical registration-only Host construction through `runtimebundle.BuildHost`. Field migration: [`legacy-options-migration.md`](legacy-options-migration.md). Supported public `Runtime` methods: `Build`, `ExecutorView`, `Ready`, `Capabilities`, `MeteringQuerier`, `ReadinessReport`, `RefreshSnapshots`, `Reload`, `ReloadStatus`, `ReloadControl`, `Close`.

1. The strict effective loader (`LoadBootstrapEffectiveWithSource`, the sole config-load owner) reads YAML into typed config exactly once and runs `routing.ValidateModelAliasesConfig`.
2. `BuildHost` evaluates the serve-only `--multi-user` CLI gate against that same accepted snapshot before acquiring any expensive resource.
3. `tracing.Init` and `logging.NewLogger` create process infrastructure.
4. `pluginreg.NewRegistry` creates an isolated registry for this process.
5. The host captures fallback hosted-provider keys once per candidate through the policy-driven credential snapshot (`upstream_credentials` policy, sparse numbered env scan); serving paths never read the process environment.
6. `standardplugins.InstallStandardBundleOn` installs official backend, frontend, feature, and auth-renderer factories.
7. `config.RegistrationsFromConfig` selects configured plugin instances.
8. `featurebundle.MergeFeatureSurface` builds hook and extension chains from configured feature plugins.
9. `runtimebundle` constructs the process runtime and publishes request-plane generation 1 as a `GenerationRuntime` (executor, stores, model catalog, metrics, tracing, health, diagnostics, handler graph) — all still inside `BuildHost`.
10. `BuildHost` binds the startup-fixed config source, effective loader, generation compiler, and reload coordinator onto that same generation and returns the complete `Host`; `stdhttp` serves through a generation dispatcher until context cancellation. Optional management reload HTTP starts only when `AIP_RELOAD_MANAGEMENT_ADDRESS` is set; Unix `SIGHUP` invokes the same coordinator. CLI and public facade shutdown delegate to `Host.Close`.

Custom tests and future alternate distributions should follow the same shape: construct an explicit registry or bundle, publish an immutable generation, then serve. Operator reload contract: [`runtime-config-reload.md`](runtime-config-reload.md) (`pkg/aipsdk/configreload`).

## Request flow

### 1. HTTP ingress and frontend decode

A bundled frontend owns wire-level details: route path, request body shape, streaming flags, protocol-specific validation, and protocol-specific error rendering. It decodes to `aipapi.Call` and calls the runtime executor. It must not call backend plugins directly.

### 2. Transport auth and principal context

HTTP auth is a transport concern in `internal/stdhttp`. Stable identity crosses into runtime through `pkg/aipsdk/transport/httpauth` and `pkg/aipsdk/execview`, not through `*http.Request` or middleware-specific types.

### 3. Canonical validation and runtime snapshot

The executor validates `aipapi.Call` before orchestration. When present, `extensions.RequestRuntimeSnapshot` is attached to the context. A snapshot is immutable for the request lifetime; runtime config reload publishes a new generation (and snapshot) for **new** admissions instead of mutating objects reachable by in-flight work.

### 4. Session, workspace, and A-leg authority

Secure-session preparation is core-owned. The runtime resolves workspace metadata, opens or resumes the authoritative proxy session, maps denials to stable public errors, and fetches the B2BUA A-leg. Client-provided session hints are hints only; they do not authorize resume or A-leg selection.

### 5. Pre-routing mutation and policy stages

Before route planning, the runtime runs configured feature stages in the legal order:

- `session_open` for first-turn labels and bootstrap metadata;
- `submit_request` for brownfield submit hooks;
- `traffic_observation` for canonical client-to-proxy snapshots where configured;
- `tool_catalog_filter` for outbound tool definition policy;
- `request_wide_shaping` for whole-call transforms;
- `route_hinting` for advisory route preferences.

Mutation stages must leave the canonical call valid. Stage runners validate after mutation where they own canonical changes.

### 6. Route planning and capability negotiation

The executor parses the route selector, applies aliases and default backend resolution, expands weighted/failover candidates, applies candidate health, and honors route hints as advisory preferences. For each candidate, capability negotiation and model-catalog eligibility run before backend open. A candidate can be rejected, downgraded attempt-locally, or opened.

### 7. B-leg open and upstream stream

For an eligible candidate, the executor allocates the next B-leg, runs request-part hooks, merges route query parameters into generation options, emits proxy-to-backend traffic observations when configured, and calls the selected backend's `Open` method.

Backends return `aipapi.EventStream`. They translate provider SDK or wire events into canonical events and classify recoverable pre-output failures with enough metadata for routing and diagnostics.

### 8. Stream receive path

The returned stream wrapper preserves the no-retry-after-output invariant. Before first output, recoverable open or recv failures may consume additional B-legs and try another candidate. After first output, failures are terminal for that stream.

On received events, the stream path applies response-part hooks, tool reactors, completion gates, secure-session recording, traffic observation, and attempt outcome recording where those handlers are configured. Frontend encoders then render canonical events into legal streaming or collected protocol responses.

### 9. Client-side cancellation

Frontend adapters translate protocol-specific cancel operations into `aipapi.ALegCancelRequest`; core cancellation remains A-leg based. OpenAI Responses cancel accepts the proxy response id from `/v1/responses/{response_id}/cancel` as the primary correlation carrier for normal clients; proxy-issued response ids carry the A-leg and authoritative session binding needed for core authorization. `X-AIP-A-Leg-Id` remains an AIProxer-private fallback for internal/test clients and older responses. Frontends must not require a private AIProxer header when the public protocol already carries an opaque response id issued by this proxy.

## Failure model

- Bad client input fails at frontend decode or canonical validation.
- Unsupported required capabilities fail before upstream I/O for the selected candidate.
- Recoverable upstream failures can be swallowed only before output starts.
- Post-output failures surface as terminal stream errors.
- Extension failures follow stage-specific failure policy; fail-open stages log and continue, fail-closed stages reject.
- Panics at extension/backend boundaries are isolated and mapped to structured errors or fail-open skips according to the boundary and stage.

## Runtime config reload (process host)

Reload is operator-triggered only (`SIGHUP` and/or management `POST /admin/config/reload`). File edits alone do nothing. The coordinator re-reads the fixed startup path under a 2 MiB strict YAML bound, requires atomic path replacement for changed content, classifies restart-required vs reloadable fields, compiles a candidate generation, and atomically swaps the active pointer. Failures keep last-good; correction requires another explicit trigger. Details, status fields, retention (default budget 8), and management opt-in live in [`runtime-config-reload.md`](runtime-config-reload.md).

## Where to change behavior

- Change frontend wire decoding or encoding in `internal/plugins/frontends/<id>`.
- Change backend provider mapping in `internal/plugins/backends/<id>`.
- Change shared canonical semantics in `pkg/aipapi` only when multiple protocols need the concept.
- Change plugin author contracts in `pkg/aipsdk`.
- Change route planning, B2BUA, secure-session, or no-retry semantics in `internal/core`.
- Change standard wiring in `cmd/aipstd`, `internal/pluginreg`, `internal/infra/runtimebundle`, or `internal/stdhttp`.
- Change reload policy/triggers in `internal/core/configreload`, `internal/infra/runtimehost`, `internal/infra/configsource`, and `internal/stdhttp/admin/configreload` (see ADR 0008).
