# Extension platform authoring guide

This guide is for **operators and feature-plugin authors** wiring behavior on the stage-four extension platform. It complements the normative spec ([`.kiro/specs/archive/go-core-stage-four-feature-extension-platform/design.md`](../.kiro/specs/archive/go-core-stage-four-feature-extension-platform/design.md)), [ADR 0006](adr/0006-stage-four-extension-seam-map-and-migration.md), and [architecture.md](architecture.md).

## Extension pipeline and legal stages

The core owns a **fixed ordered list** of legal extension stages (requirement **R2**). Inventory exposes this as `extensions.legal_pipeline` and `extensions.stages[]` with `id` and `default_failure` per stage (requirement **R14**).

Canonical order (sixteen stages):

1. `transport_authentication` — standard HTTP only; identity before decode.
2. `session_open` — session/workspace bootstrap; no direct provider calls (use auxiliary client if model work is needed).
3. `secret_guard` — ingress secret detection after BeginTurn; redact or reject before FE checkpoint/traffic/routing.
4. `submit_request` — submit hooks; coarse reject/annotate.
5. `tool_catalog_filter` — remove or annotate tools before backend translation.
6. `request_wide_shaping` — request-wide transforms and request-part hooks.
7. `pre_request_admission` — admission checks after canonical request shaping and before route planning.
8. `route_hinting` — advisory hints; core routing remains authoritative.
9. `candidate_attempt_transform` — per-candidate attempt transforms after interleaved shaping; continue or exclude_candidate before final capabilities.
10. `attempt_lifecycle` — core-owned attempt loop (occupancy usually empty for features).
11. `stream_event_mutation` — response-part hooks.
12. `tool_event_reaction` — tool reactors (provider-agnostic contracts).
13. `completion_gating` — bounded buffering and typed completion decisions.
14. `final_stream_observation` — final canonical stream observation after gates; before traffic/egress.
15. `traffic_observation` — four-leg observers, redactors, privileged capture sinks.
16. `egress_encoding` — frontend encode (core-owned).

Note: `attempt_lifecycle` and `egress_encoding` are legal pipeline labels for inventory, policy, and ordering. They are not separate extension planes in the typed feature bundle; `attempt_lifecycle` is owned by the core attempt loop, and `egress_encoding` stays in frontend/transport encoding.

**Failure policy** per stage is documented in design section **§17** (`FailurePolicyLabel` / `DefaultFailurePolicyForStage` in code). Treat inventory `default_failure` as the operator-visible default; stage runners may narrow further where the contract allows.

## Service facades (when to use which seam)

Use **narrow SDK packages** under `pkg/aipsdk/` — not raw core types, not transport globals, not provider SDKs.

| Concern | Package | Use for |
| --- | --- | --- |
| Session bootstrap | [`pkg/aipsdk/session`](../../pkg/aipsdk/session) | First-turn detection, session labels, opener stage. |
| Workspace metadata | [`pkg/aipsdk/workspace`](../../pkg/aipsdk/workspace) | Project root, markers; consume `WorkspaceView` from resolvers. |
| Request-wide mutation | [`pkg/aipsdk/request`](../../pkg/aipsdk/request) | History-aware shaping distinct from submit hooks. |
| Tool definitions before upstream | [`pkg/aipsdk/toolcatalog`](../../pkg/aipsdk/toolcatalog) | Filter/annotate tools and reconcile tool choice. |
| Tool-use events in stream | [`pkg/aipsdk/hooks`](../../pkg/aipsdk/hooks) | `ToolReactor` — block, rewrite, or pass tool calls/results. |
| Routing intent | [`pkg/aipsdk/routehint`](../../pkg/aipsdk/routehint) | Roles and hints; never bypass core planner rules. |
| Plugin memory | [`pkg/aipsdk/state`](../../pkg/aipsdk/state) | Namespaced TTL state (request/session/principal/global). |
| Private sub-calls | [`pkg/aipsdk/auxiliary`](../../pkg/aipsdk/auxiliary) | Verifier/memory-style calls with lineage; no direct backend handles. |
| Whole-completion control | [`pkg/aipsdk/completion`](../../pkg/aipsdk/completion) | Buffered decisions, replace/replay/reject per typed outcomes. |
| Observation / capture | [`pkg/aipsdk/traffic`](../../pkg/aipsdk/traffic) | Observers vs privileged `CaptureSink`; respect redaction order. |
| Typed bundle assembly | [`pkg/aipsdk/feature`](../../pkg/aipsdk/feature) | `FeatureBundle` (holds `PlaneSet FrozenPlaneSet`, `SchemaVersion`, and optional `Lifecycles`); merges standard extension planes for registration. |

When a plugin needs state across multiple handlers, bind the shared store with `pkg/aipsdk/state.BindPlugin` using the plugin instance ID before writing or reading keys. That keeps per-plugin namespaces isolated while still letting the plugin share state between its own stages.

**Principal on the request path:** transport attaches identity using [`pkg/aipsdk/transport/httpauth`](../../pkg/aipsdk/transport/httpauth) context helpers. That package is the **stable cross-layer contract** for principal values; HTTP middleware and handler types stay in `internal/stdhttp` (design **§13**).

## Privileged surfaces and inventory

- **General traffic observers** receive redacted or structured views after the redaction stage.
- **Privileged raw capture** (`CaptureSink`) is opt-in and must never drive request mutation (design **§10–§11**).
- **Inventory** (`extensions.features[].privileges`) exposes booleans such as `raw_capture`, `auxiliary_requests`, `completion_gate`, and `auth_provider` so reviewers can see elevated capability (requirement **R14**). `auxiliary_requests` is set for bundles that receive the aux client through request transforms, tool catalog filters, or completion gates.

If your feature needs raw bytes or completion-wide control, contribute to the matching standard extension plane (such as `PlaneRawCaptureSinks` or `PlaneCompletionGates`) and expect those flags to flip `true` in diagnostics.

## Extension plane lifecycle and FeatureBundle assembly

All feature plugins contribute capabilities through the typed extension plane lifecycle in [`pkg/aipsdk/feature`](../../pkg/aipsdk/feature). A `FeatureBundle` contains schema version metadata (`SchemaVersionV1`), an immutable [`FrozenPlaneSet`](../../pkg/aipsdk/feature/frozen.go), and optional plugin lifecycles. Note: `FeatureBundle` does **not** contain individual named fields or slices for each extension plane; all extension planes are held within `FeatureBundle.PlaneSet`.

### Frozen PlaneSet lifecycle

The lifecycle proceeds in discrete, fail-before-mutate phases:

1. **Staging**: Construct a mutable contribution set using `feature.NewContributionSet()`.
2. **Contribution**: Add typed capabilities using `feature.Contribute(cs, plane, contributorID, value)`. `Contribute` tags contributions with `SourceFeature` and enforces fail-before-mutate semantics: if validation or combination fails, `cs` remains unmodified and an `*AttributedError` attributing the contributor and plane is returned.
3. **Freezing**: Call `cs.Freeze()` to produce an immutable `FrozenPlaneSet`. Freezing provides top-level collection isolation: slice backing arrays and metadata maps are isolated, while element values (e.g. interface handlers) are shallow-copied, not deep-cloned.
4. **Packaging**: Call `feature.BundleFromPlanes(frozen, lifecycles)` to produce a `FeatureBundle` with `SchemaVersionV1`.
5. **Validation**: Validate the bundle via `bundle.Validate()`.
6. **Reading**: Downstream consumers read values using `feature.Get(bundle.PlaneSet, plane)`. If a plane was not contributed or is absent, `feature.Get` returns the plane's zero value (e.g. `nil` for slice planes). Slice-valued planes return defensive copies on ordinary `Get` calls to prevent caller mutation of the snapshot.
7. **Replay & Thaw**: A frozen set can be replayed to another set via `bundle.PlaneSet.ReplayTo(destSet, contributorID)` or thawed for modification via `bundle.PlaneSet.ToContributions()`.
8. **Request Execution Snapshots**: `feature.FreezeRequestPlanes(frozen)` evaluates declared request materializers to produce an immutable per-request snapshot.

Example matching the canonical feature SDK contract (see `testdata/external_feature_sdk`):

```go
cs := feature.NewContributionSet()
if err := feature.Contribute(cs, feature.PlaneSubmitHooks, "my_plugin_id", []hooks.SubmitHook{hook}); err != nil {
    return feature.FeatureBundle{}, fmt.Errorf("contribute failed: %w", err)
}
bundle := feature.BundleFromPlanes(cs.Freeze(), nil)
if err := bundle.Validate(); err != nil {
    return feature.FeatureBundle{}, fmt.Errorf("bundle validate failed: %w", err)
}
```

### Closed standard-plane catalog and ErrUngeneratedPlane

In v1, AIProxer enforces a **closed standard-plane catalog** declared in the canonical manifest (`pkg/aipsdk/feature/plane_manifest.go`).

- **No dynamic planes in v1**: Arbitrary unbound or dynamically declared `Plane[T]` instances are not supported. Contributing through an ungenerated or unbound plane fails immediately before candidate mutation with `feature.ErrUngeneratedPlane`.
- **Platform-level plane addition**: Adding a new extension plane is an upstream AIProxer SDK/runtime platform change requiring a declaration in `plane_manifest.go` and code regeneration via `go run ./scripts/generate-feature-planes.go`.
- **Canonical generated-policy authority**: Exported `Plane[T]` package variables (such as `PlaneSubmitHooks`, `PlaneRequestTransforms`, etc.) act as typed descriptors. The canonical generated binding (`plane_generated.go`) is the sole authority for production combination rules, nil handling, validator execution, and identity extraction. Copying or mutating exported fields on a `Plane[T]` descriptor (e.g., copying `PlaneSubmitHooks` and altering its `Rules` or `Combine` fields) does **not** redefine the plane; production execution always enforces the canonical generated policy. If an altered copy has a modified plane ID, contribution is rejected with `feature.ErrUngeneratedPlane`.

### Standard distribution boundary

Adding a standard in-process feature implementation to AIProxer follows an explicit boundary:

1. **Feature package ownership**: Feature code lives under `internal/plugins/features/<feature>` and owns its domain state, configuration decoding and bundle construction via a feature-owned constructor or factory (as demonstrated by `toolcallrepair.FeatureBundle(cfg)`, `secretguard.FeatureBundle(cfg)`, and `reasoningpreservation.FeatureBundleWithCompanionPolicy(cfg, ...)`). Simple plane-only features register a thin factory in `internal/standardplugins/features_install.go`; sophisticated process/generation-bound features are composed by `internal/standardplugins/featurehost`. New features must follow the feature-owned model.
2. **Explicit standard registration**: The factory is registered in `internal/standardplugins/features_install.go` and listed in `internal/standardplugins/standard_table.go` (`StandardBundle().Features`).
3. **No core or runtimebundle branching**: Concrete feature packages must not be imported by `internal/core` or `internal/infra/runtimebundle`. `runtimebundle` contains zero direct imports of `internal/plugins/features/*`.
4. **Dedicated composition adapters**: When a feature requires process- or generation-bound capabilities (such as background auxiliary workers or credential matchers), it is assembled via a dedicated typed composition adapter under `internal/standardplugins/featurehost` (e.g., `featurehost/reasoning`, `featurehost/secretguard`, `featurehost/compaction`), not by branching inside generic core orchestration or the runtime bundle. Generic `runtimebundle` calls only the single `featurehost` generation facade.

## Choosing the right seam (feature → seam map)

If nothing below fits, **extend the platform** (new stage or contract) instead of branching core orchestration (design **§18**).

| Feature class (examples) | Primary seam(s) |
| --- | --- |
| Auto-append first prompt, session labels | `session` opener + `request` transform (+ submit as needed) |
| Outbound rewrite, compaction, secrets on wire | `request` transform, request-part hooks |
| Inbound cleanup, think-tags | Response-part hooks or `completion` gates |
| Allowed/blocked tools | `toolcatalog` filter + `hooks` tool reactor |
| Dangerous commands, steering | Tool reactor + `workspace` view + `state` |
| Project root / sandbox | `workspace` resolver + reactors/filters |
| Quality verifier, replacement completion | `completion` gate + `auxiliary` + `state` |
| SSO / API keys from HTTP | `stdhttp` auth providers → `httpauth` principal → core views |
| Usage / transcripts / CBOR | `traffic` observers and capture sinks; `client_to_proxy` can label both raw ingress and the canonical post-submit snapshot, so capture metadata must distinguish the point of collection |
| Auxiliary routing | `auxiliary` client + route roles + `routehint` |

Reference proof plugins under `internal/plugins/features/` demonstrate each class; see [`REFERENCE_PLUGINS.md`](../internal/plugins/features/REFERENCE_PLUGINS.md).

## Reload-friendly execution snapshots

Each request should run against **one immutable** [`internal/core/extensions.RequestRuntimeSnapshot`](../../internal/core/extensions/snapshot.go) for its lifetime (design **§15B**, quality **Q7**). Composition roots build snapshots from registry + config; mutating wiring after publish should use a **new** snapshot generation, not in-place mutation of shared stage chains.

## Context cancellation and bounded stages

Every SDK handler receives a `context.Context` from the core and must treat it as authoritative cancellation. Decision providers and feature hooks that perform loops, sleeps, channel waits, or blocking I/O must monitor `ctx.Done()` / `ctx.Err()` and return promptly when canceled. The core can bound cooperative providers with evaluation deadlines, but Go cannot forcibly stop a goroutine whose provider ignores its context; such code can outlive the request until its own call stack unwinds.

## Public runtime composition options

Closed modules that call `pkg/aipruntime.Build` attach non-money authority
through canonical registrations on `aipruntime.Options`:
`RequestRegistrations`, `AttemptRegistrations`, and `ConcurrencyRegistration`,
and attach optional behaviour through `UsageLimits`, `AdminMounts`,
`EnterpriseCapabilities`, and `MeteringObservationSink`. A monetary host
attaches exactly one explicit `pkg/aipsdk/billing.Binding` through
`pkg/aipruntime.BuildWithBilling`; the public runtime surface carries no money
logic of its own. The in-tree money implementation that predates this seam is
still being relocated to the Enterprise tree, so it still exists today.
Enterprise attachment seams:
[enterprise-extension-boundaries.md](enterprise-extension-boundaries.md).

## Automated guardrails

Architecture tests in [`internal/archtest`](../internal/archtest) and import boundaries in [`internal/core/runtime/boundaries_test.go`](../internal/core/runtime/boundaries_test.go) enforce package rules. See [ADR 0005](adr/0005-architecture-guardrails-and-complexity-budgets.md) for complexity budgets.
