# Architecture current state

Operator-facing map of AIProxer: streaming-first control plane, standard `aipstd` distribution, hybrid backends.

The durable source of truth is split by purpose:

- `AGENTS.md` - agent and repository guardrails.
- `.kiro/steering/*.md` - enduring product, API, routing, structure, tech, and testing memory.
- `.kiro/specs/{feature}/` - active spec artifacts. Finished and superseded specs live under `.kiro/specs/archive/`.
- `README.md` - current runnable distribution, configuration, security, and QA overview.
- `docs/dogfood-local.md` - canonical **no-key** stub workflow (`aipstd check-config`, `routes`, `inventory`, `serve`) aligned with `config/examples/*.yaml`.
- `docs/runtime-config-reload.md` - explicit SIGHUP/management-API runtime config reload (no watcher; atomic source replace; generation publication).
- `docs/proxy-identity.md` - A-leg/B-leg identity carriers, modes, allowlist/exclusions, OpenRouter attribution.
- `docs/conversation-view.md` - A-leg/B-leg conversation-view projection (client-visible/backend-hidden vs backend-visible/client-hidden, whole-message granularity, semantic identity, fixed anchors, cache-prefix invariants).
- `docs/session-classification.md` - optional session classification (`unknown`/`coding_agent`, proxy-owned authoritative keying, local heuristic rules, optional TypeSafe/Jev remote modes, fail-open behavior, bounded diagnostics).
- `docs/architecture.md` - this current-state runtime map.

## Product shape

The Go proxy is a streaming-first control plane between multiple client-facing APIs and multiple backend API families. Frontend adapters decode wire protocols to `pkg/aipapi` canonical calls. Backend adapters translate canonical calls to provider or emulator calls and return canonical event streams. Core orchestration stays provider-agnostic.

The standard distribution (`cmd/aipstd`) wires essential plugins through `internal/standardplugins`, `internal/featurebundle`, `internal/infra/runtimebundle`, and `internal/stdhttp`, and may discover optional **executable** backend connectors from trusted roots. Hybrid composition is recorded in [`docs/adr/0008-hybrid-backend-connector-plugins.md`](adr/0008-hybrid-backend-connector-plugins.md). Core packages do not import concrete optional connectors or provider SDKs.

## Runtime flow

The implemented request path is:

1. HTTP ingress lands in a bundled frontend mounted by `internal/stdhttp`.
2. Transport/auth middleware attaches principal information through `pkg/aipsdk/transport/httpauth` and `pkg/aipsdk/execview` context contracts.
3. The frontend decodes its wire request into a `pkg/aipapi.Call` and invokes the runtime executor.
4. The executor validates the canonical call and publishes the immutable `internal/core/extensions.RequestRuntimeSnapshot` on the request context.
5. Secure-session preparation resolves principal and workspace context, opens or resumes the authoritative session, and creates or fetches A-leg continuity state.
6. Submit hooks and extension stages run over the canonical call: session open, tool catalog filtering, request-wide shaping, route hinting, and brownfield request-part hooks at their defined positions.
7. Core routing parses the selector, applies default backend/model resolution and model aliases, expands failover candidates, applies route hints as advisory preferences, and enforces the attempt budget.
8. Capability negotiation and model-catalog eligibility checks run per candidate before upstream I/O. Unsupported required semantics reject explicitly or apply attempt-local downgrades when negotiation allows them.
9. The executor allocates a B-leg, emits traffic observations when configured, opens the selected backend, and returns a canonical event stream.
10. Response-part hooks, tool reactors, completion gates, traffic observers, secure-session recording, and attempt lineage run on the stream path where those handlers are configured.
11. Frontend encoders convert canonical events and canonical errors into protocol-legal responses. Non-streaming responses are collected from the same event path.

Recoverable upstream failures may trigger failover only before the first downstream content event is emitted. After output starts, failures are terminal for that attempt and are surfaced through protocol-legal frontend error handling.

## Core-owned behavior

`internal/core` owns orchestration rather than provider semantics:

- routing selector parsing, weighted failover, route hints, candidate health, max-attempt policy, and A-leg runtime routing overrides;
- B2BUA A-leg/B-leg continuity, attempt lineage, and pre-output recovery;
- the optional attachment points for authorize-before-upstream and the terminal usage-evidence handoff (`aipruntime.BuildWithBilling`, `Options.UsageLimits`, `Options.AdminMounts`); an unattached port stays no-op (no stream-time money);
- secure-session authority, resume policy, and session-start audit emission;
- capability negotiation, model catalog eligibility, and explicit mismatch failures;
- hook and extension stage execution order, failure policy, timeout boundaries where implemented, and panic isolation;
- canonical event collection, stream error classification, and resource bounds;
- conversation-view projection (pure kernel in `internal/core/conversationprojection`: A-leg-owned snapshot, early backend-effective projection, final reassertion before PTB/`Backend.Open`, generic local-turn seam; mutable steering/tag state lives outside core in `internal/infra/conversationview` and is composed by `internal/standardplugins/featurehost`) — see `docs/conversation-view.md` for visibility directions, whole-message granularity, semantic identity, fixed anchors, cache-prefix stability, and limits.

These concerns are shared runtime semantics. Provider request shapes, SDK clients, wire payloads, and protocol-specific error rendering stay in adapters and plugins.

## Plugin-owned behavior

Official protocol adapters:

- frontends (`internal/plugins/frontends/`): OpenResponses 2026-04-24, OpenAI Responses, legacy OpenAI-compatible chat/completions, Anthropic Messages, Gemini generateContent;
- **essential** backends (`internal/plugins/backends/` + `EssentialBackendBundle`): OpenAI Responses, legacy OpenAI-compatible, Anthropic, Gemini, Bedrock Converse, Alibaba Token Plan International, plus built-in custom-compatible kinds;
- **optional** backends (`connectors/`): executable gRPC plugins (OpenRouter, NVIDIA, Hugging Face, Ollama/local runtimes, OpenCode, Codex, ACP-family CLIs, `local-stub`, …) registered via closed manifests — not fixed essential tables;
- features: standard feature implementations under `internal/plugins/features/` (compaction-continuity state, interleaved-thinking UX processing, keep-warm scheduling/policy, reasoning preservation, secret guard, tool-call repair, plus noop/reference plugins that prove SDK seams). Concrete standard-feature process/generation assembly has one explicit home outside generic composition: `internal/standardplugins/featurehost`. Generic `runtimebundle.ProcessServices` holds a single `StandardFeatures` handle plus narrow core consumer ports — never per-feature fields — and `pkg/aipruntime.Options` carries only the `FeatureHostRegistrations` envelope, never per-feature options.

The composition root may import essential plugins and host discovered connector factories. Core packages must not import concrete connectors.

## Extension platform

Feature plugins contribute a `pkg/aipsdk/feature.FeatureBundle` (schema version `SchemaVersionV1`, an immutable `FrozenPlaneSet`, and optional plugin lifecycles). Rather than named bundle fields, typed capabilities are assembled into standard extension planes via the `ContributionSet` → `Contribute` → `Freeze` → `BundleFromPlanes` lifecycle:

- brownfield submit, request-part, response-part, and tool-reactor hooks;
- session openers and workspace resolvers;
- tool catalog filters, request-wide transforms, and pre-request admission handlers;
- route hint providers and completion gates;
- traffic observers, raw capture sinks, redactors, secret guards, and terminal-decision providers.

In v1, the extension-plane catalog is closed (`pkg/aipsdk/feature/plane_manifest.go`). Arbitrary unbound planes are rejected with `ErrUngeneratedPlane`, and the canonical generated binding is authoritative for production plane policy; copying or mutating descriptor fields does not redefine standard plane behavior. In-process features own their domain state, configuration decoding and bundle construction; standard features are registered explicitly in `internal/standardplugins`, with zero direct feature imports in `internal/core` or `internal/infra/runtimebundle`.

The core materializes these into a frozen request runtime snapshot. Hooks mutate or decide, observers record, stores persist, resolvers discover context, and auxiliary clients perform controlled sub-calls. Do not merge those concerns into a single super hook.

See `docs/extension-points.md`, `docs/extension-platform-authoring.md`, and `docs/plugin-authoring.md` for the stage table and authoring rules.

## Canonical runtime ownership

This distribution has exactly four converged ownership surfaces:

1. **One process runtime / `ProcessServices`** — process-owned services (stores, shared limiters, metrics/tracing providers, listeners, capacity) constructed once under `runtimebundle.NewProcessServices` / `runtimehost` and retained for the process lifetime. There is a single process-services owner per Host.
2. **One generation runtime** — an immutable request-plane `GenerationRuntime` compiled and published per config generation, acquired on admission, and retained by in-flight streams until they drain.
3. **One host (private-field Host)** — `runtimebundle.Host` returned by `runtimebundle.BuildHost` owns startup, reload coordination, generation publication/retention, and shutdown. Host fields are unexported; callers use Host methods / the public `aipruntime.Runtime` facade. **`Host.Close` is the sole process shutdown coordinator**; `pkg/aipruntime.Runtime.Close` and CLI teardown delegate to it. **Manager-owned retirement** drains and closes superseded generations; Host does not reimplement generation closer loops.
4. **One reload contract** — public/SDK reload DTOs live only in `pkg/aipsdk/configreload` (`Trigger`, `Result`, `Status`, `HistoryEntry`, closed categories). Reload is explicit-only (SIGHUP, management API, public facade); there is no watcher, polling, or automatic retry.

**Candidate assembly is private and temporary.** Package-private `candidateAssembly` / opaque compile handles exist only while a candidate is being built or validated; they are not a runtime API and are not retained after publish or dry-run rollback.

**True unpublished validation:** `runtimebundle.ValidateDistribution` (CLI `aipstd check-config`) compiles through the same generation compiler in dry-run mode and **always rolls back** — it never publishes or retains a generation (no fake check-config publication).

Public `pkg/aipruntime.Runtime` is a thin facade over that one host. Supported public methods: `Build`, `Serve`, `ExecutorView`, `Ready`, `Capabilities`, `MeteringQuerier`, `ReadinessReport`, `RefreshSnapshots`, `Reload`, `ReloadStatus`, `ReloadControl`, `Close`. Public `aipruntime.Options` carries the canonical non-money registrations (`RequestRegistrations`, `AttemptRegistrations`, `ConcurrencyRegistration`) plus the optional attachment seams (`UsageLimits`, `AdminMounts`, `EnterpriseCapabilities`, `MeteringObservationSink`). A monetary host attaches exactly one explicit binding through `aipruntime.BuildWithBilling`; rating and settlement are not runtime-composition concerns. The final ownership split is not done: the legacy in-tree money composition is still part of this tree and still backs the stock composition, which declares the billing capability, while the usage-authority implementation is still physically in this tree pending relocation and backs no stock composition — usage limits are enforced only through an injected port. Deleted dual-bootstrap / legacy-options paths are not part of the current architecture.

## Composition and startup

`cmd/aipstd serve` and the public `aipruntime.Build` facade both obtain a complete process-owned `Host` from exactly one `runtimebundle.BuildHost` call. `BuildHost` performs the standard startup sequence as one owned transaction:

1. load YAML config once (the strict effective loader) and validate model aliases;
2. evaluate the serve-only `--multi-user` CLI gate against that same accepted snapshot;
3. initialize tracing and logging;
4. create an isolated `pluginreg.Registry` with `pluginreg.NewRegistry`;
5. resolve default upstream API keys from environment variables;
6. install the standard (essential) bundle on that registry via `standardplugins.InstallStandardBundleOn`;
7. discover and register optional backend connector manifests when configured (`plugins.backend_discovery`);
8. validate mandatory bundled factories;
9. merge configured feature bundles with `featurebundle.MergeFeatureSurface` (simplified via `MergeBundles`/`Append` helpers) and build hooks in `runtimebundle` (`BuildFeatureHooks`);
10. construct process services and publish request-plane **generation 1** through `runtimebundle` / `runtimehost`;
11. bind the fixed-source reload coordinator and stable executor onto that same generation, returning one complete `Host`.

`cmd/aipstd serve` then serves data-plane HTTP through a generation dispatcher; optional management reload HTTP binds only when `AIP_RELOAD_MANAGEMENT_ADDRESS` is set. Unix `SIGHUP` invokes the same coordinator. Any startup failure rolls back everything `BuildHost` acquired internally and returns a nil `Host` — no partial ownership escapes to the caller.

The registry is composition-root state, not core global state. Essential static tables live under `internal/standardplugins`; optional backends attach as discovered executable plugins ([ADR 0008 hybrid connectors](adr/0008-hybrid-backend-connector-plugins.md)); feature merge is `internal/featurebundle`; hook bus construction stays in `internal/infra/runtimebundle`. Startup remains explicit — no package-level mutable registries and no Go native `plugin`. Runtime reload publishes a new immutable generation for new admissions without replacing the data-plane listener; see [`runtime-config-reload.md`](runtime-config-reload.md) and [ADR 0008 versioned reload](adr/0008-versioned-runtime-config-reload.md).

## Diagnostics and operations

When enabled by config, diagnostics expose health, attempt lineage, route trace, plugin inventory, model-catalog status, metrics, and pprof paths. Treat diagnostics as operator surfaces: bind them safely, use `diagnostics.shared_secret` outside localhost-only development, and keep labels/cardinality bounded.

Before serving, operators can run **`aipstd check-config`**, **`routes`**, and **`inventory`** against the same YAML (see `docs/dogfood-local.md`) without opening client traffic. `check-config` shares the reload generation compiler in dry-run/rollback mode.

Traffic observation and capture are privileged extension paths. Redaction must happen before persistence or long-term observer storage.

## Core admission and feature ownership

A production responsibility may remain under `internal/core` only when it is required with all optional standard features disabled, or when it is a feature-neutral extension mechanism with recorded independent consumers. Kernel routing operators (selector grammar, route planning, B-leg sequencing, output commitment) stay core-owned; optional UX policy (memo/shaping/sanitization, steering placement, keep-warm scheduling, terminal actor policy) lives in the owning feature. The durable per-package justification table is `internal/archtest/core_ownership.go`; a new top-level core package fails architecture tests until it gains an entry.

`internal/standardplugins/featurehost` is the only composition layer that knows the concrete standard feature set. It constructs feature process state (nested under one `ProcessServices.Close` registration) and compiles generation output (ordinary planes plus narrow core consumer ports). It exposes no request-time resolver, registry, or binding map: generation holds direct typed references. A process feature resource has exactly one constructor path and one physical cleanup owner; borrowed generic resources (DB pools, secure-session stores, backend hosts) are never closed by featurehost.

Trusted host capabilities arrive as startup-only typed registrations (`pkg/aipsdk/featurehost` envelope plus narrow SDK packages such as `pkg/aipsdk/reasoninghost`); generic runtime forwards them immutably and only featurehost interprets concrete binding types. Optional feature configuration is decoded by the owning feature from its `plugins.features` YAML payload. One-way legacy top-level aliases (`interleaved:`, `prompt_cache.keepwarm:`) normalize to the canonical feature node in `internal/standardplugins/legacyfeatureconfig` before semantic decode; new-plus-legacy conflicts fail deterministically.

## Architecture boundaries

<!-- architecture-contract: non-cartesian-release-evidence -->

Release evidence is additive (frontend TCK, canonical-core TCK, backend-family TCK, provider-profile certification, connector TCK, protocol compliance, and a bounded real-stack sentinel). It is not a cartesian frontend-by-backend product.

Permanent rules:

- Core packages do not import concrete plugins.
- Core, `pkg/aipapi`, and `pkg/aipsdk` do not import provider SDKs.
- Protocol adapters translate only protocol-to-canonical or canonical-to-protocol; no pairwise translators.
- Non-streaming behavior is a collector over canonical event streams.
- Capability mismatches are handled explicitly, interoperability first: the default is a logged, attempt-local downgrade; an operator-selected strict policy rejects instead ([`VISION.md`](../VISION.md)).
- Advanced request, response, tool, capture, memory, verifier, and safety features use SDK seams before core logic changes.

Architecture tests under `internal/archtest` and related package tests enforce many of these boundaries. Run `make arch-report` for a deterministic snapshot of package sizes, import fan-out, and hexagonal baseline classifications. Enterprise attach seams: [`docs/enterprise-extension-boundaries.md`](enterprise-extension-boundaries.md).

**Single-module layout:** the repository intentionally ships one `go.mod`. Boundary tests enforce SDK isolation and dependency direction; a module split is deferred until concrete distribution pain appears.
