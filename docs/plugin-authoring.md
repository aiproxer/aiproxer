# Plugin authoring guide

This guide explains how to write feature and protocol plugins that preserve the Go proxy architecture. For the complete stage map, see `docs/extension-points.md`. For operator configuration and examples, see `README.md` and `config/config.yaml`. For YAML-only OpenAI/Anthropic-compatible provider wiring, see [`docs/custom-compatible-backends.md`](custom-compatible-backends.md). For the **no-key local stub** maintainer workflow (`check-config`, routes, inventory, serve), see [`docs/dogfood-local.md`](dogfood-local.md).

## Plugin types

- Frontend plugins decode a client protocol into `aipapi.Call` and encode canonical events/errors back to that protocol.
- Backend plugins translate `aipapi.Call` into upstream calls and emit `aipapi.EventStream` values.
- Feature plugins contribute typed capabilities to standard extension planes within an immutable `FrozenPlaneSet` held by `pkg/aipsdk/feature.FeatureBundle`.
- Store plugins provide persistence or continuity backends through composition-root wiring.

Only standard distribution packages (`cmd/aipstd`, `internal/pluginreg`, `internal/standardplugins`, `internal/stdhttp`, and dedicated `internal/standardplugins/featurehost` composition adapters) should import concrete bundled plugins. Core packages and generic `internal/infra/runtimebundle` must remain free of concrete feature implementation imports.

## Feature bundle basics

A feature factory decodes opaque YAML and returns a versioned `feature.FeatureBundle`. A `FeatureBundle` contains schema version metadata (`SchemaVersionV1`), an immutable `FrozenPlaneSet` (`bundle.PlaneSet`), and optional plugin lifecycles.

Note: `FeatureBundle` does **not** contain individual named fields or slices for extension planes. All extension planes are held within the immutable `FrozenPlaneSet`. If a plane is unoccupied, reading it via `feature.Get(bundle.PlaneSet, plane)` returns the plane's zero value.

### Extension plane lifecycle

The feature contribution lifecycle proceeds as follows:

1. **Staging**: Construct a mutable set using `feature.NewContributionSet()`.
2. **Contribution**: Add typed capabilities using `feature.Contribute(cs, plane, contributorID, value)`. `Contribute` tags contributions with `SourceFeature` and enforces fail-before-mutate semantics.
3. **Freezing**: Call `cs.Freeze()` to produce an immutable `FrozenPlaneSet`.
4. **Packaging**: Call `feature.BundleFromPlanes(frozen, lifecycles)` to wrap the frozen planes into a `FeatureBundle` with `SchemaVersionV1`.
5. **Validation**: Validate the bundle via `bundle.Validate()`.
6. **Reading**: Read values using `feature.Get(bundle.PlaneSet, plane)`. If a plane was not contributed or is ungenerated, `feature.Get` returns the zero value of the plane's type and does not search dynamic fallback storage.

Canonical authoring example matching `testdata/external_feature_sdk`:

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

### Closed standard manifest and policy authority

In v1, AIProxer enforces a **closed standard-plane catalog** declared in `pkg/aipsdk/feature/plane_manifest.go`:

- **No dynamic planes in v1**: Arbitrary unbound or dynamically declared `Plane[T]` instances are not supported. Contributing through an ungenerated or unbound plane fails fast before candidate mutation with `feature.ErrUngeneratedPlane`.
- **Platform-level plane addition**: Adding a new extension plane requires an upstream manifest and platform change: declaring the plane in `plane_manifest.go` and regenerating code via `go run ./scripts/generate-feature-planes.go`.
- **Canonical generated-policy authority**: Exported `Plane[T]` descriptor variables (such as `PlaneSubmitHooks`) act as typed descriptors. The canonical generated binding is the sole authority for combination rules, nil handling, validator execution, and identity extraction. Copying or mutating exported fields on a `Plane[T]` descriptor does not redefine the plane; production contribution always executes the canonical generated policy. If an altered copy has a modified plane ID, contribution is rejected with `feature.ErrUngeneratedPlane`.

### Standard distribution boundary

Adding a standard in-process feature implementation to AIProxer follows an explicit boundary:

1. **Feature package ownership**: Feature code lives under `internal/plugins/features/<feature>` and owns its domain state, configuration decoding, and bundle construction via a feature-owned constructor or factory (as demonstrated by `toolcallrepair.FeatureBundle(cfg)`, `secretguard.FeatureBundle(cfg)`, and `reasoningpreservation.FeatureBundleWithCompanionPolicy(cfg, ...)`). Simple plane-only features register a thin factory in `internal/standardplugins/features_install.go`; sophisticated process/generation-bound features are composed by `internal/standardplugins/featurehost`. New features must follow the feature-owned model.
2. **Explicit standard registration**: The factory is registered in `internal/standardplugins/features_install.go` and listed in `internal/standardplugins/standard_table.go` (`StandardBundle().Features`).
3. **No core or runtimebundle branching**: Concrete feature packages must not be imported by `internal/core` or `internal/infra/runtimebundle`. `runtimebundle` contains zero direct imports of `internal/plugins/features/*`.
4. **Dedicated composition adapters**: When a feature requires process- or generation-bound capabilities, it is assembled via a dedicated typed composition adapter under `internal/standardplugins/featurehost` (e.g., `featurehost/reasoning`, `featurehost/secretguard`, `featurehost/compaction`), not by branching inside generic core orchestration or the runtime bundle. Generic `runtimebundle` calls only the single `featurehost` generation facade, never child adapters directly.

Use SDK packages, not core internals:

- `pkg/aipsdk/hooks` for brownfield submit, part, and tool-reactor hooks;
- `pkg/aipsdk/session` for session openers;
- `pkg/aipsdk/workspace` for workspace resolvers;
- `pkg/aipsdk/request` for whole-call canonical transforms;
- `pkg/aipsdk/toolcatalog` for tool definition filtering;
- `pkg/aipsdk/toolpolicy` for allow/deny on canonical tool-call lifecycle events before tool reactors;
- `pkg/aipsdk/routehint` for advisory route preferences;
- `pkg/aipsdk/completion` for whole-completion gates;
- `pkg/aipsdk/traffic` for observation, capture, and redaction;
- `pkg/aipsdk/usage` for accounting-style observation over canonical usage deltas (non-mutating);
- `pkg/aipsdk/state` for plugin-scoped state;
- `pkg/aipsdk/auxiliary` for controlled sub-calls;
- `pkg/aipsdk/secretguard` for opaque ingress secret-guard contracts (`Guard`, `Matcher`, `DecisionEvent`).

## Standard feature: secrets-guard

`secrets-guard` is a bundled standard feature (factory kind and plugin id `secrets-guard`) registered in `internal/standardplugins/`. It contributes one or more `secretguard.Guard` values to `feature.PlaneSecretGuards` within its `FeatureBundle.PlaneSet`, executed at stage id **`secret_guard`** immediately after `securesession.BeginTurn` and before frontend ingress checkpoint, traffic capture, submit hooks, and routing. Only one enabled `secrets-guard` feature instance is supported in v1; multiple enabled registrations must fail startup.

Authoring and extension rules:

- SecretGuard scans only user prompts and genuine tool execution output. Assistant history, model-generated tool calls, instructions and tool definitions are excluded. This pre-dispatch feature must never inspect, redact, collect or buffer provider response events, even when request redaction is enabled.
- Feature code lives in `internal/plugins/features/secretguard/` and must not import runtime, frontends, or backends.
- Catalog construction and Aho–Corasick matching live in `internal/plugins/features/secretguard/engine/`; standard-distribution composition lives in `internal/standardplugins/featurehost/secretguard/` (with generic snapshot assembly in `internal/infra/runtimebundle/`); audit delivery adapters live alongside the featurehost secret-guard adapter.
- SDK consumers receive an opaque **`Matcher` / `MatcherResolver`** via `secretguard.Services`. No API exposes raw catalog values or accepts an environment reader at request time. The opaque matcher belongs only in middleware request context; `AuthenticationResult` carries safe attribution targets only.
- In **`single_user`**, composition loads proxy credential env vars (bare + sparse numbered), a curated popular-env registry, and operator `include_env` / `exclude_env` hints at startup only. The loaded catalog is a startup snapshot; credential rotation requires restarting all replicas and verifying the refreshed catalog after restart.
- In **`multi_user`**, composition selects a request-credential matcher with **zero** process-environment reads, even when `single_user.*` YAML is present (startup rejects that key in multi-user mode). Device/key/fingerprint values are attribution-only and are not scanned as secret catalog entries.
- Enabled configuration requires explicit `action`: `block`, `redact`, or `log`. Disabled feature creates no catalog, no stage work, and no behavior change.
- On match, the runtime emits a dedicated **`secretguard.DecisionEvent`** audit record (safe fields only). Do not overload `policydecision.Record`. Optional `proxy_instance_id` / `pod_id` attribution is future work, not v1.
- The shared declared public-prefix registry is value-based, not env-name based: prefix provenance comes from the loaded secret value, not from the environment-variable name that sourced it.

Operator configuration and examples: [`docs/secrets-guard.md`](secrets-guard.md), [`config/config.yaml`](../config/config.yaml), and [`config/examples/`](../config/examples/).

## Standard feature: reasoning-output-preservation

`reasoning-output-preservation` is a bundled standard feature (plugin id `reasoning-output-preservation`, issue #157) registered in `internal/standardplugins/`. On the standard `aipstd` bootstrap path it is injected enabled (`action: restore`, `use_builtin_catalog: true` / `compatible-auto.v2`) when no matching features row exists; explicit matching `enabled: false` (or omitted enabled) opts out and constructs no participants. It is not a mandatory `StandardDistributionRequirements` entry. Installed/enabled does not mean universally active — capture and restore require catalog/rule eligibility; unmatched candidates are inert. When active it contributes an attempt transform and a final-stream observer factory for bounded process-local reasoning capture/restore. Feature code lives in `internal/plugins/features/reasoningpreservation/` and must not import runtime, frontends, or backends. Shared matcher: `internal/plugins/features/reasoningpreservation/reasoningreplay`. Operator configuration and examples: [`docs/reasoning-output-preservation.md`](reasoning-output-preservation.md), [`config/config.yaml`](../config/config.yaml), [`config/examples/reasoning-preservation-observe.yaml`](../config/examples/reasoning-preservation-observe.yaml), [`config/examples/reasoning-preservation-restore.yaml`](../config/examples/reasoning-preservation-restore.yaml), and the [release checklist](reasoning-output-preservation-release-checklist.md).

## Standard feature: session-classification

`session-classification` is a bundled standard feature (plugin id `session-classification`) registered in `internal/standardplugins/`. It contributes one `session_classifier` plane at stage id **`session_classification`**, whose request-access class is `metadata_only`: it reads bounded session, workspace, and intent metadata and never the canonical request body. Its result is projected onto the turn's `SessionView.Classification` as one bounded, advisory fact.

Authoring and consumption rules:

- Feature code lives in `internal/plugins/features/sessionclassification/`; standard-distribution composition, the durable/memory state holders, the shared coordinator, and the metrics collector live in `internal/standardplugins/featurehost/sessionclassification/`.
- A consumer feature gates on the projection — read `view.Classification.IsCodingAgent()` and decide for itself. Do not re-run the classifier, parse the client `User-Agent`, keep a second client catalog, rescan the transcript, or read a vendor-specific result; the projection exists so detection is implemented once.
- Treat `unknown` as *not yet positively classified*, never as *known not to be a coding agent*. There is no negative classification.
- Classification is advisory derived metadata, not authorization: it grants no tool, no permission, no identity, and no billing entitlement. Enabling the feature gates no existing behaviour.
- The observation and metric surface is closed: labels are members of fixed vocabularies, and session IDs, A-leg IDs, raw User-Agent, filenames, paths, prompts, client metadata, and vendor result strings are never labels.

Operator documentation, the metric and evidence-code contract, the closed label vocabularies, the disabled posture, and correct/incorrect consumer examples: [`docs/session-classification.md`](session-classification.md).

## Standard feature: agent-loop-guard

`agent-loop-guard` is a bundled standard feature (plugin id `agent-loop-guard`) registered in `internal/standardplugins/`, disabled by default. Its bundle assembly `agentloopguard.BuildBundle` (`internal/plugins/features/agentloopguard/bundle.go`) contributes `feature.PlaneTerminalDecisionProvider` when the row is enabled, and additionally `feature.PlaneControlToolProvider` only when the selected strategy is `attempt_completion`. A disabled row contributes neither plane. Feature code lives in `internal/plugins/features/agentloopguard/` and must not import runtime, frontends, or backends; the generic control-tool and terminal-decision seams it uses live in `pkg/aipsdk/controltool` and `pkg/aipsdk/terminaldecision` and must never import ALG.

Unlike the single-plane features above, its factory is **strategy-aware**: one feature id selects two mutually exclusive implementations, and validation happens during YAML decode so default values cannot make an unused strategy look configured. Two plugin-authoring lessons are worth copying: presence-sensitive decode-time validation for mutually exclusive option sets, and a config decoder that stays free of any runtime/provider dependency.

Operator configuration, strategy selection, mutual exclusion, activation eligibility, and the self-attestation trade-off: [`docs/agent-loop-guard.md`](agent-loop-guard.md), [`config/config.yaml`](../config/config.yaml), and [`config/examples/agent-loop-guard-preferred.yaml`](../config/examples/agent-loop-guard-preferred.yaml) / [`config/examples/agent-loop-guard-legacy-verifier.yaml`](../config/examples/agent-loop-guard-legacy-verifier.yaml).

## Authoring rules

- Keep handlers small, deterministic, and context-aware.
- Never retain or mutate canonical pointers after the handler returns unless the interface explicitly grants ownership.
- Use `context.Context` for cancellation and deadlines; do not start per-request background goroutines from handlers.
- Return classified errors instead of panicking. Core isolates panics, but a panic is still a plugin bug.
- Keep provider SDK types inside backend plugins only.
- Keep HTTP transport types inside frontends or stdhttp transport integrations only.
- Bind state with `state.BindPlugin` before sharing state between handlers in the same feature.
- Redact before persistence. Raw capture is privileged and must be disabled unless explicitly configured.
- Use route hints only as hints; the core planner remains authoritative.
- Revalidate assumptions with focused tests whenever a handler mutates the canonical call or event stream.

## Configuration pattern

Feature plugin configuration should be explicit and typed after YAML decode:

```yaml
plugins:
  features:
    - id: ref-tool-policy
      enabled: true
      config:
        block_names: ["dangerous_shell"]
        block_prefixes: ["admin."]
```

A plugin should reject invalid config at startup rather than fail during the first request.

## Backend model inventory

Backend plugins must expose `execbackend.Backend.ModelInventory` with a `pkg/aipsdk/modelinventory.Provider`
and at least one `execbackend.Backend.BackendPrefixes` entry. Prefixes must match the backend factory id
(for example `openai-responses`, `ollama`, `ollama-cloud`) and must be unique across backend connector
kinds at runtime. Multiple instances of the same connector kind may reuse that kind's prefix.
Canonical model IDs must use the `vendor/model` form; do not publish
inventory rows whose canonical id uses a backend prefix qualifier such as `ollama:google/gemma4`.
The core model registry uses this provider at startup and during background refresh to answer fast routing
lookups for canonical model IDs such as `openai/gpt-5`.

Backend authors should choose one inventory source:

- Remote provider API, such as a `/models` endpoint or provider SDK list operation.
- Backend-specific static config file. File inventories should use an `items:` list; `models:` is accepted as
  a compatibility alias when `items:` is absent.
- Inline static config for fixed local/test backends.

Static providers should use `modelinventory.StaticProvider`, which also marks the inventory as non-refreshable.
Dynamic providers must respect `context.Context`; the runtime applies `model_inventory.fetch_timeout` per backend
inventory fetch.

## Testing expectations

Every feature plugin should have:

- unit tests for config decoding and invalid config;
- stage-runner tests for success, fail-open/fail-closed behavior, ordering, and canonical revalidation;
- integration tests through `runtimebundle` or `stdhttp` when the plugin depends on session, routing, or stream behavior;
- regression tests for security-sensitive behavior such as redaction, auth, tool blocking, or capture.

Protocol plugins should also include golden wire fixtures, streaming tests, cancellation tests, and fuzz tests for decoders where practical.

When authoring protocol/frontend adapters for tool use, name assistant tool-call/function-call arguments explicitly in coverage tables and fixture comments; do not bury them under generic "message JSON" language.

## Reference plugins

`internal/plugins/features/REFERENCE_PLUGINS.md` lists reference feature plugins registered by the standard bundle. They are proof plugins, not a license to put product logic into core. Prefer hardening or promoting a reference plugin when it already proves the seam you need.

Current proof areas include first-session auto append, tool policy, workspace guard, traffic transcript/capture, verifier completion gates, and ingress secrets guard.

## Import boundaries

Allowed:

- feature plugin -> `pkg/aipapi`, `pkg/aipsdk`, standard library, narrow external dependencies;
- backend plugin -> provider SDK, `pkg/aipapi`, `pkg/aipsdk`, internal backend-local helpers;
- frontend plugin -> HTTP/wire helpers, `pkg/aipapi`, `pkg/aipsdk`, runtime executor view contracts;
- standard distribution -> concrete bundled plugins and core wiring packages.

Forbidden:

- `internal/core` importing concrete plugins;
- `internal/infra/runtimebundle` importing concrete feature packages (`internal/plugins/features/*`);
- `pkg/aipapi` or `pkg/aipsdk` importing `internal/...` packages;
- provider SDK imports outside backend plugins, refclients, or tests explicitly designed for conformance;
- feature plugins importing executor, routing, or B2BUA internals to bypass SDK seams.

If a plugin cannot be implemented without a forbidden import, document the missing seam before implementing the feature.
