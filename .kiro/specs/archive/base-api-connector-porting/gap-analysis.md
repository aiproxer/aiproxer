# Implementation Gap Analysis

## Base API connector porting

Spec directory: `base-api-connector-porting`

## Summary

The existing Go codebase already has strong foundations for official backend connector work: bundled backend packages exist for OpenAI Responses, OpenAI legacy chat completions, Anthropic, and Gemini; provider SDKs are isolated inside backend plugins; routing and pre-output failover are core-owned; and deterministic refbackend/conformance assets already exist for the target families.

The main gap is the new architecture decision from this spec: backend instance identity must be separated from API-key identity, and credential usefulness state must become a backend-adapter concern rather than a routing/config-discovery concern. Current standard backend configs accept a single `api_key`, bootstrap env loading reads only one key per family, and there is no credential pool/cooldown abstraction yet.

Requirements are not approved yet, but this gap analysis can inform design revisions.

## Current-state assets

### Backend plugin packages

- `internal/plugins/backends/openairesponses/` implements an OpenAI Responses backend using `openai-go`, including request mapping, stream wrapping, event mapping, output media helpers, config tests, integration tests, fuzz tests, and map-event tests.
- `internal/plugins/backends/openailegacy/` implements OpenAI Chat Completions using `openai-go`, including request mapping, streaming chunk mapping, integration tests, fuzz tests, and compatibility tests.
- `internal/plugins/backends/anthropic/` implements Anthropic Messages using `anthropic-sdk-go`, including model-aware capabilities, request mapping, event mapping, tests, and fuzz tests.
- `internal/plugins/backends/gemini/` implements Gemini generateContent using `google.golang.org/genai`, including request mapping, capability tests, event mapping, and integration tests.
- Shared OpenAI capability rules already live in `internal/plugins/backends/openaicaps/`.

### Standard distribution and config assets

- `internal/pluginreg/backends_install.go` decodes backend YAML and constructs backend plugin instances.
- `internal/pluginreg/keys.go` resolves default `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, and `GEMINI_API_KEY` once at composition time.
- `internal/core/config/model.go` already separates plugin factory kind from runtime instance id via `PluginConfig.Kind` and `PluginConfig.ID`.
- `config/config.multi-instance.example.yaml` already demonstrates two runtime backend instances using the same factory kind with distinct ids.

### Routing/orchestration assets

- `internal/core/routing/selector.go` treats the backend selector component as a stable runtime backend id, not a credential id.
- `internal/core/routing/planner.go` expands failover candidates and preserves health/exclusion state at candidate-key granularity.
- `internal/core/runtime/executor_open_attempt.go` owns capability negotiation, attempt-local call derivation, backend opening, recoverable pre-output swallowing, and B-leg lineage.
- `internal/core/runtime/attempt_stream.go` already enforces the no-retry-after-first-output invariant for recoverable stream errors.
- `pkg/lipapi/upstream.go` provides `RecoverablePreOutputError`, `IsRecoverablePreOutput`, and `UpstreamFailure` primitives that adapters can use to return retry-eligible failures.

### Emulator and conformance assets

- `internal/refbackend/openairesponses/`, `internal/refbackend/openaichat/`, `internal/refbackend/anthropicmessages/`, and `internal/refbackend/gemini/` provide deterministic reference backends for the target families.
- `internal/testkit/conformance/` contains text, multimodal, tools, parity, migration, release-gate, and emulator-wiring tests that can be extended for this spec.
- `testdata/migration/` already contains Python LIP migration captures for OpenAI Responses and Anthropic examples.

## Requirement-to-asset map

| Requirement | Current support | Gap tag | Notes |
| --- | --- | --- | --- |
| 1. Official backend scope | Target backend packages already exist; standard bundle includes additional Bedrock/ACP too. | Constraint | Design must scope tasks to four official hosted-provider families and avoid changing Bedrock/ACP except to preserve build/test compatibility. |
| 2. Protocol parity over Python topology | Go backend plugins already use direct Go package boundaries rather than Python registry/class topology. | Partial | Need design guidance that Python captures are evidence only; no explicit artifact currently records this for connector porting. |
| 3. Instance identity separate from credential identity | `PluginConfig.Kind`/`ID`, routing selectors, and multi-instance example already support stable runtime ids. | Partial | Current connector configs still accept exactly one `api_key`, so multi-key behavior is not separated from instance config yet. |
| 4. Credential usefulness state | Recoverable pre-output errors exist, and adapter-owned errors can trigger core failover. | Missing | No credential pool, retry-after parser, per-key cooldown, auth-invalid marker, or tests. |
| 5. Retry/failover ownership | Core already owns inter-backend failover and no-retry-after-output. | Partial | Need adapter-local credential rotation before stream commitment and tests proving rotation does not bypass core attempt semantics. |
| 6. Deployment config + credential pools | Base URL + single API key config exists; env fallback exists for one key per provider. | Missing | Need config schema for `api_keys`, env numbered ingestion policy, compatibility with existing `api_key`, and duplicate/empty validation. |
| 7. Reusable protocol implementations | OpenAI capability sharing exists, but full base OpenAI-compatible deployment profile abstraction does not. | Partial | Need design decision whether to add shared profile/config helpers now or defer until another provider target appears. |
| 8. Explicit capability mismatches | Core negotiation and backend `ResolveCaps` exist. | Partial | Need overlay/profile capability rules to avoid assuming shared base capabilities always apply. |
| 9. Streaming/event mapping | Backend packages are streaming-first and have event mapping tests. | Partial | Need focused parity gaps for usage, terminals, multimodal, and provider-specific stop behavior per family. |
| 10. Emulator-first evidence | Refbackends and conformance tests exist for all four target families. | Partial | Need explicit task ordering that emulator/fixture evidence precedes connector completion claims. |
| 11. Architecture-locking tests | Existing package tests cover mapping, integration, fuzzing, and runtime retry invariants. | Partial | Need new tests specifically for credential pools, stable backend ids with multiple keys, and pre-output credential rotation. |

## Key gaps and constraints

### Missing

- Credential pool abstraction for official hosted-provider backends.
- Per-key state model: usable, cooldown until time, auth invalid, and maybe disabled.
- Retry-after extraction/normalization shared across official provider adapters.
- Config decoding for `api_keys` while preserving backward compatibility with `api_key`.
- Environment loading for numbered keys such as `OPENAI_API_KEY_2` without creating backend instances.
- Tests that prove credential rotation happens only inside a backend instance and only before visible output.

### Partial / reusable

- The backend seam is already small: `execbackend.Backend.Open(ctx, call, cand)`.
- Runtime inter-backend retry and lineage are already implemented; adapters only need to return classified pre-output errors when local credentials are exhausted.
- Official backend packages already isolate SDKs at the edge, satisfying the major dependency constraint.
- Existing refbackends can provide emulator-first evidence, but some may need richer fixtures for rate-limit/auth/error semantics.

### Constraints

- Do not move credential state into `internal/core/routing`; that would violate the spec's separation decision.
- Do not let credential identifiers become selector keys or B-leg candidate keys.
- Avoid broad unification of all OpenAI-compatible behavior unless design proves the shared layer stays small and does not obscure protocol differences.
- Provider SDK clients are currently constructed with a single key. Multi-key rotation may require per-key client construction or request-option injection depending on SDK capabilities.

## Implementation approach options

### Option A: Extend each backend independently

Add `api_keys` support and local cooldown logic separately inside `openairesponses`, `openailegacy`, `anthropic`, and `gemini`.

**Pros**
- Minimal cross-package design.
- Each provider can handle SDK-specific key/client behavior directly.
- Lowest risk of premature shared abstraction.

**Cons**
- Duplicates cooldown/auth-invalid logic.
- Harder to ensure uniform retry-after handling and test behavior.
- More likely to diverge across providers.

**Fit:** Good as a spike, weak as final architecture for this spec.

### Option B: Create a shared credential-pool component for backend adapters

Add a small shared package under `internal/plugins/backends/` for static credential pools and cooldown state, then integrate it into the four official backend packages.

**Pros**
- Cleanly models the spec's main architectural decision.
- Keeps credential state at the adapter edge, outside core routing.
- Enables uniform tests for selection, cooldown, retry-after, auth invalidation, and exhaustion.

**Cons**
- Requires careful API design to avoid over-generalizing provider behavior.
- Requires provider adapters to coordinate stream-open failure classification with credential selection.

**Fit:** Best match for requirements 3-6 and 11.

### Option C: Hybrid approach with shared config/pool primitives and provider-local retry loops

Create shared primitives for config normalization, credential identity/state, and retry-after parsing; keep actual request retry/client construction provider-local.

**Pros**
- Balances reuse with provider-specific SDK realities.
- Avoids forcing a generic HTTP/client abstraction over four SDKs.
- Supports OpenAI-compatible consolidation later without blocking credential-pool work now.

**Cons**
- Requires clear boundaries between shared pool state and provider-local transport loops.
- Design must define exactly how providers report failures back into pool state.

**Fit:** Recommended approach.

## Recommended design direction

Use Option C.

1. Define a small shared credential-pool package under `internal/plugins/backends/` for static API key lists, key selection, cooldown state, auth invalidation, and exhaustion errors.
2. Keep provider SDK construction and provider-specific error classification in each backend package.
3. Decode both legacy `api_key` and new `api_keys` in `internal/pluginreg/backends_install.go`, normalize them into each backend's config, and preserve current single-key behavior.
4. Extend `pluginreg.ResolveUpstreamAPIKeysFromEnv` or adjacent composition-root code to read numbered env vars into provider-specific key slices without creating backend instances.
5. Add tests in layers:
   - shared credential-pool unit tests,
   - pluginreg config/env normalization tests,
   - per-backend adapter tests using `httptest` or refbackend handlers,
   - runtime tests proving backend instance ids and core failover behavior remain stable.
6. Treat shared OpenAI-compatible protocol/profile work as a design decision: add only if it can stay small and directly supports current requirements; otherwise document it as a follow-up seam.

## Research needed for design phase

- Official SDK behavior for per-request API-key override vs per-key client creation:
  - `github.com/openai/openai-go/v3`
  - Anthropic Go SDK
  - `google.golang.org/genai`
- Provider-specific rate-limit/auth error surfaces and how reliably retry-after metadata can be extracted from SDK errors.
- Whether Gemini API keys can be rotated safely by client instance without additional project/location state.
- Whether existing refbackends are sufficient for credential/error-path tests or need explicit 401/429/retry-after fixtures.

## Effort and risk

- **Effort:** L (1-2 weeks). The backend packages and test harnesses already exist, but adding credential pools, env/config migration, and four-provider error handling touches several packages and requires careful tests.
- **Risk:** Medium. The architecture direction is clear and current seams are strong, but provider SDK error metadata and per-key client behavior may require research and provider-specific handling.

## Design-phase guidance

- Keep the design focused on official hosted-provider backends only.
- Make credential pool state adapter-owned; do not modify routing selectors or core candidate identity for key rotation.
- Explicitly define when an adapter may retry another key and when it must return an error to the core.
- Preserve backward compatibility for existing `api_key` configs and single-key env behavior.
- Avoid broad OpenAI-compatible abstraction unless the design can show a small, tested seam with clear provider overlay rules.
