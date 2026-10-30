# Research & Design Decisions

## Summary

- **Feature**: `base-api-connector-porting`
- **Discovery Scope**: Extension with complex integration points
- **Key Findings**:
  - The codebase already contains the four target backend packages, a small executor-consumed backend seam, stable routing ids, and deterministic refbackend/conformance assets.
  - The main missing capability is not protocol scaffolding but credential-pool architecture: current official backends accept one `api_key`, env bootstrap reads one key per family, and no per-key usefulness state exists.
  - Official SDKs support explicit API-key configuration at client construction, so the safest design is shared credential-pool state plus provider-local client/request handling.

## Research Log

### Existing backend integration points

- **Context**: Requirements 1.1, 3.1, 5.4, 9.1, and 10.1 depend on existing plugin and runtime seams.
- **Sources Consulted**:
  - `internal/plugins/backends/openairesponses/`
  - `internal/plugins/backends/openailegacy/`
  - `internal/plugins/backends/anthropic/`
  - `internal/plugins/backends/gemini/`
  - `internal/core/execbackend/backend.go`
  - `internal/core/runtime/executor_open_attempt.go`
  - `internal/core/runtime/attempt_stream.go`
- **Findings**:
  - The executor consumes `execbackend.Backend` with `Caps`, `ResolveCaps`, and `Open` only.
  - Backends already map canonical calls into provider SDK params and provider streams into canonical event streams.
  - Core failover is triggered by `lipapi.IsRecoverablePreOutput` during open and receive phases.
  - The runtime already refuses retry after stream commitment.
- **Implications**:
  - Credential rotation can remain inside backend adapters without changing routing selector semantics.
  - When all local credentials are exhausted before output, adapters can return a classified recoverable pre-output error for core-owned inter-backend failover.

### Configuration and identity model

- **Context**: Requirements 3.1-3.4 and 6.1-6.4 require backend instance identity to stay separate from API-key identity.
- **Sources Consulted**:
  - `internal/core/config/model.go`
  - `internal/core/routing/selector.go`
  - `internal/pluginreg/backends_install.go`
  - `internal/pluginreg/keys.go`
  - `config/config.multi-instance.example.yaml`
- **Findings**:
  - Runtime backend ids are already separate from backend factory kinds.
  - Routing selectors treat the backend component as a stable backend id and allow dots in backend names.
  - Standard backend YAML currently decodes `base_url` and single `api_key` only.
  - Env bootstrap currently resolves only `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, and `GEMINI_API_KEY`.
- **Implications**:
  - The design should add `api_keys` as a plugin config field while preserving `api_key` compatibility.
  - Numbered env vars should become additional credentials on default backend instances, not generated backend ids.

### Official SDK constraints

- **Context**: Requirements 4.3, 5.1, and 6.2 require choosing how credentials are applied to upstream requests.
- **Sources Consulted**:
  - Context7 `/openai/openai-go` documentation
  - Context7 `/anthropics/anthropic-sdk-go` documentation
  - Context7 `/websites/pkg_go_dev_google_golang_org_genai` documentation
  - Current backend SDK client construction in `invoke.go` files
- **Findings**:
  - OpenAI Go supports `option.WithAPIKey`, request options, custom headers, response capture, and typed API errors.
  - Anthropic Go supports `option.WithAPIKey`, request options, middleware, and typed API errors.
  - Google GenAI `ClientConfig` accepts `APIKey`, `HTTPClient`, and `HTTPOptions.BaseURL`.
  - Current backends build clients from `Config.APIKey`; Gemini creates the client during `Open`, while OpenAI/Anthropic create clients during backend construction.
- **Implications**:
  - The design should avoid a generic request executor over SDKs.
  - Provider packages should own client construction per credential or per request, while a shared pool owns key state.
  - Retry-after parsing should accept generic HTTP status/header inputs and let provider packages adapt SDK errors into that shape.

### Emulator and test assets

- **Context**: Requirements 9.4, 10.1-10.3, and 11.1-11.4 require deterministic verification.
- **Sources Consulted**:
  - `internal/refbackend/openairesponses/`
  - `internal/refbackend/openaichat/`
  - `internal/refbackend/anthropicmessages/`
  - `internal/refbackend/gemini/`
  - `internal/testkit/conformance/`
  - `.kiro/specs/archive/base-api-connector-porting/gap-analysis.md`
- **Findings**:
  - Refbackend packages already exist for all four in-scope backend families.
  - Existing conformance tests cover text, multimodal, tools, parity, migration, and release-gate paths.
  - Existing refbackends may need explicit 401/429/retry-after controls for credential-pool tests.
- **Implications**:
  - Tasks should add focused error-path fixtures rather than inventing live-provider tests.
  - Connector completion should be gated on deterministic tests and not on live credentials.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
| --- | --- | --- | --- | --- |
| Extend each backend independently | Add multi-key and cooldown logic separately in each provider package | Fast local changes, minimal shared package design | Duplicated state logic, inconsistent retry behavior | Rejected as final direction |
| Shared credential pool with generic SDK wrapper | One shared package owns key state and provider request execution | Maximum reuse | Forces provider SDKs through an artificial abstraction | Rejected as over-generalized |
| Shared credential pool plus provider-local retry loops | Shared package owns key state and normalization; providers own SDK clients and error adaptation | Clean boundary, avoids core leakage, fits SDK differences | Requires clear contracts between pool and adapters | Selected |

## Design Decisions

### Decision: Backend instance identity remains deployment identity

- **Context**: Python LIP used numbered backend instances for per-key failover, but this spec requires a simpler Go model.
- **Alternatives Considered**:
  1. Preserve per-key backend instances for compatibility.
  2. Treat backend instances as deployments and credentials as attached state.
- **Selected Approach**: Backend instance ids continue to represent routeable upstream deployments. API keys become credentials in a pool owned by that backend instance.
- **Rationale**: Routing selectors, diagnostics, and B-leg lineage stay stable and meaningful. Credential cooldown does not pollute core routing identity.
- **Trade-offs**: Operators cannot route to a specific API key by selector; this is intentional because API keys are operational state, not product-level route targets.
- **Follow-up**: Document config examples that show multiple backend instances only for genuinely distinct targets.

### Decision: Build a small adapter-edge credential pool

- **Context**: Requirements require per-key cooldown, auth-invalid state, and retry-after awareness.
- **Alternatives Considered**:
  1. Use existing core health maps.
  2. Build provider-local pools in each backend.
  3. Build a shared adapter-edge pool.
- **Selected Approach**: Add a narrow shared package under `internal/plugins/backends/credpool` for static credential state and selection.
- **Rationale**: Core health maps are candidate-level, not credential-level. A shared pool avoids duplicate state logic while keeping provider details at the edge.
- **Trade-offs**: The package must stay narrow and avoid provider SDK imports.
- **Follow-up**: Unit tests must prove no provider or core coupling emerges.

### Decision: Provider packages adapt SDK errors locally

- **Context**: SDKs expose different error types and client construction patterns.
- **Alternatives Considered**:
  1. Shared generic HTTP error decoder for all SDKs.
  2. Provider-local classifiers that return shared pool outcomes.
- **Selected Approach**: Each provider backend classifies its own SDK errors and marks the selected credential through the shared pool.
- **Rationale**: This preserves SDK isolation and avoids a leaky generic transport wrapper.
- **Trade-offs**: Some duplicated error-classification tests are expected.
- **Follow-up**: Design should define common classification outputs: rate limited with optional retry-after, auth invalid, transient recoverable, surfaced failure.

### Decision: Defer broad OpenAI-compatible profile abstraction

- **Context**: Many OpenAI-compatible targets differ mainly by base URL, headers, and small quirks.
- **Alternatives Considered**:
  1. Add a full OpenAI-compatible profile framework now.
  2. Add only narrow shared helpers needed by current requirements.
- **Selected Approach**: Add only the narrow shared concepts required now: credential pooling and optional capability/profile hooks if needed. Do not add a broad provider profile framework until a concrete compatible provider requires it.
- **Rationale**: This follows the steering preference for small seams and avoids speculative abstractions.
- **Trade-offs**: Future OpenAI-compatible providers may require a follow-up spec to formalize overlays.
- **Follow-up**: Keep terminology in design.md so future work can extend via deployment profiles and provider overlays without changing current contracts.

## Risks & Mitigations

- Provider SDK error metadata may not expose retry-after uniformly — Mitigate with provider-local classifiers and tests using SDK-accessible response/header mechanisms where available; default to conservative cooldown when no retry-after is available.
- Credential rotation could accidentally bypass the no-retry-after-first-output invariant — Mitigate with adapter tests that inject errors before first canonical output and runtime tests that ensure post-output errors surface.
- Shared credential pool could become a generic backend framework — Mitigate with a narrow package contract that stores and selects credentials only, with no provider SDK imports and no request execution.
- Existing single-key configs could break — Mitigate by preserving `api_key` and env single-key behavior while adding `api_keys` as additive config.

## References

- `.kiro/steering/product.md` — product promise and official backend target families.
- `.kiro/steering/api-standards.md` — canonical middle, adapter ownership, and streaming-first API rules.
- `.kiro/steering/routing-and-orchestration.md` — core-owned failover and no post-output retry behavior.
- `.kiro/steering/structure.md` — package ownership and backend plugin placement.
- `.kiro/steering/tech.md` — Go stack, official SDK policy, and dependency constraints.
- Context7 `/openai/openai-go` — OpenAI Go SDK API-key options, request options, and error/response handling.
- Context7 `/anthropics/anthropic-sdk-go` — Anthropic Go SDK API-key options, request options, and error handling.
- Context7 `/websites/pkg_go_dev_google_golang_org_genai` — GenAI client config and API-key behavior.
