# Requirements Document

## Introduction

Go-LIP already contains three config-defined generic compatible backend kinds:

- `custom-openai-legacy-compatible`
- `custom-openai-responses-compatible`
- `custom-anthropic-compatible`

They let operators point the proxy at third-party or local endpoints implementing a supported wire protocol without adding provider-specific Go code. The implementation is substantial but incomplete: it accepts literal YAML credentials, lacks the requested tokenizer and per-instance concurrency options, and is coupled to the current `internal/standardplugins` composition shape.

The completed and archived `backend-connector-plugin-architecture` specification established the surrounding architecture. Non-essential provider connectors are external executable plugins, while dependency-free OpenAI- and Anthropic-compatible modes remain built-in aliases of the essential protocol-family adapters. This feature targets the landed `internal/pluginreg`, `internal/standardplugins`, and `internal/infra/runtimebundle` composition boundaries rather than preserving transitional construction code.

The result is a built-in protocol capability, not an external connector framework or provider catalog. OpenRouter and providers requiring proprietary authentication, headers, routing, inventory, billing, extensions, or error behavior remain external connector plugins.

## Boundary Context

- **In scope:** the three generic compatible kinds; strict configuration; environment-only credential references; unauthenticated endpoints; base URL and route-prefix validation; shared adapter reuse; per-instance concurrency; tokenizer selection; model inventory; canonical parity; diagnostics; migration into the final built-in composition boundary; tests and documentation.
- **Out of scope:** executable plugin discovery/ABI work; provider-specific extensions; OpenRouter; arbitrary YAML transformations; dynamic code loading; canonical redesign; hot reload.
- **Adjacent dependency:** implementation must use the landed interfaces evidenced by `.kiro/specs/archive/backend-connector-plugin-architecture/` (completed at `e796998c`) and must preserve current `Host`/`GenerationRuntime` ownership.
- **Ownership:** configuration and composition at the application edge; protocol behavior in built-in adapters; common admission, tokenization, inventory, credentials, and diagnostics in shared infrastructure; routing and canonical policy in core.

## Requirements

### Requirement 1: Built-In Architectural Classification

**Objective:** As a Go-LIP maintainer, I want generic compatible providers represented as built-in protocol-family modes, so that simple compatible endpoints require neither external plugin artifacts nor provider-specific dependencies.

#### Acceptance Criteria

1. The standard OSS binary shall provide the three existing generic compatible factory kinds without an executable plugin manifest, plugin process, or additional installation.
2. Each generic mode shall reuse its corresponding built-in OpenAI legacy, OpenAI Responses, or Anthropic protocol-family adapter.
3. The modes shall not add a provider SDK, proprietary runtime, authentication scheme, attribution header, routing extension, billing behavior, model taxonomy, or error taxonomy.
4. The modes shall not cross the external backend-plugin ABI solely because operators can configure multiple instances.
5. `internal/core` shall not import, name, switch on, or otherwise depend on these kinds or concrete adapters.
6. Construction and registration shall belong to the post-plugin-architecture built-in composition boundary and shall not recreate a fixed optional connector table.

### Requirement 2: Stable Instance and Routing Configuration

**Objective:** As an existing operator, I want the current backend instance model preserved, so that migration does not break routes or require recompilation.

#### Acceptance Criteria

1. Each backend shall continue to use a `plugins.backends` row with `id`, `kind`, `enabled`, and opaque `config`.
2. The three existing factory-kind strings shall remain stable.
3. Each enabled instance shall require `config.backend_prefix` and `config.base_url`.
4. It may specify `config.api_key_env_var_root`, `config.tokenizer`, `config.max_concurrent_requests`, and the existing model inventory configuration.
5. Multiple instances of every generic kind shall work simultaneously and remain isolated.
6. Runtime IDs, routes, aliases, inventory provenance, and diagnostics shall distinguish instances sharing one factory kind.
7. Unknown fields shall fail under the repository's strict decoding policy.
8. `lipstd check-config` shall validate structural rules without upstream network calls or plugin activation.

### Requirement 3: Environment-Only Credential References

**Objective:** As a security-conscious operator, I want custom compatible backend secrets kept out of YAML.

#### Acceptance Criteria

1. `api_key`, `api_keys`, and inline credential secrets under `credentials` shall be rejected with actionable startup errors for these kinds.
2. The only supported static credential source shall be `api_key_env_var_root`.
3. The resolver shall read `ROOT`, `ROOT_2`, `ROOT_3`, and subsequent numbered keys using the existing credential-pool convention.
4. If the env root is omitted or resolves to no non-empty keys, the backend shall remain valid and operate unauthenticated.
5. OpenAI-compatible modes shall send `Authorization: Bearer <token>` only when a credential is selected.
6. Anthropic-compatible mode shall never emit an empty `x-api-key` or equivalent credential header.
7. Credential references and values shall not appear in logs, diagnostics, inventory, route traces, or errors.
8. Native built-in and external-plugin credential policy shall remain unchanged.

### Requirement 4: Safe Base URL Semantics

**Objective:** As an operator, I want endpoints validated and joined deterministically.

#### Acceptance Criteria

1. `base_url` shall be an absolute `http` or `https` URL with a non-empty host.
2. Userinfo, embedded credentials, unsupported schemes, fragments, and malformed URLs shall be rejected.
3. Explicit ports and intentional path prefixes shall be preserved.
4. One deterministic trailing-slash and endpoint-joining policy shall cover execution and inventory endpoints.
5. Joining shall not duplicate separators or remove meaningful path components.
6. Validation shall perform no DNS resolution or network I/O.
7. OpenAI Chat Completions, Responses, OpenAI models, Anthropic Messages, and Anthropic models joins shall have table-driven tests.

### Requirement 5: Composed Prefix Ownership

**Objective:** As a routing maintainer, I want prefixes validated against the complete composed system.

#### Acceptance Criteria

1. `backend_prefix` shall be non-empty after trimming and contain neither `/` nor `:`.
2. Enabled generic instances shall have unique prefixes.
3. A generic prefix shall not collide with built-in connector kinds or route prefixes.
4. Before plugin activation, it shall not collide with dynamically discovered external-plugin factory kinds available from validated manifests. After activation resolves an external instance profile, its advertised route prefixes shall be checked against the same ownership rules before that generation is published.
5. Validation shall use immutable generation-scoped registry/catalog state, not a manually maintained list; `check-config` shall perform only the manifest-available subset and shall not launch plugin processes.
6. Conflicts shall fail deterministically and identify both bounded owners without secrets.
7. Disabled rows shall follow the repository's established enabled-instance ownership policy.

### Requirement 6: Common Per-Instance Concurrency Admission

**Objective:** As an operator, I want an optional request cap per configured compatible backend.

#### Acceptance Criteria

1. `max_concurrent_requests` shall be optional and non-negative.
2. Omitted or zero shall use the documented project default, which may be unlimited.
3. A positive value shall apply independently to that runtime instance across streaming and non-streaming operations.
4. Two instances of one kind shall not share capacity unless a common runtime policy explicitly defines that behavior.
5. Admission waiting or rejection shall honor context cancellation and deadlines.
6. Capacity shall release exactly once on success, setup failure, provider error, cancellation, stream close, and terminalization.
7. Overload shall use a stable common admission error/status.
8. The implementation shall use the post-Kiro common runtime admission seam, not a semaphore inside protocol codecs.
9. Race, leak, cancellation, and terminal tests shall prove permits cannot be stranded or over-released.

### Requirement 7: Common Tokenizer Selection

**Objective:** As an operator, I want an optional tokenizer override without provider-specific code.

#### Acceptance Criteria

1. `tokenizer` shall be optional; omission preserves current behavior.
2. A configured value shall resolve through the shared tokenizer registry or abstraction.
3. Unknown values shall fail startup validation with an actionable error.
4. The result shall attach through common capability, token-counting, or accounting metadata.
5. Protocol adapters shall not branch on tokenizer names.
6. Separate instances may use different tokenizers without interference.
7. Diagnostics may expose only a bounded tokenizer identifier.

### Requirement 8: Canonical Protocol Parity

**Objective:** As a client and routing maintainer, I want generic modes to behave like their built-in protocol families at the canonical seam.

#### Acceptance Criteria

1. Each mode shall preserve the same canonical call mapping as the reused built-in adapter for advertised capabilities.
2. Streaming shall remain incremental and bounded with ordered events and one terminal outcome.
3. Cancellation shall use the same managed-stream contract as the built-in adapter.
4. Pre-output and post-output failures shall preserve existing retry and output-commitment semantics.
5. Text, reasoning, tools, multimodal content, usage, finish reasons, and errors shall be preserved where advertised.
6. No provider wire type or compatibility-specific concept shall enter `pkg/lipapi` or core orchestration.
7. Unsupported capabilities shall remain explicitly unsupported rather than inferred.
8. Differential deterministic parity tests shall compare generic and built-in behavior.

### Requirement 9: Model Inventory and Provenance

**Objective:** As an operator, I want generic providers integrated with the shared model registry.

#### Acceptance Criteria

1. OpenAI-compatible modes may discover models from the compatible `<base_url>/models` endpoint.
2. Anthropic-compatible mode may discover models from its compatible models endpoint.
3. Inventory authentication shall follow the same optional credential behavior as execution.
4. Existing static inline or file inventory overrides shall remain supported under shared precedence rules.
5. Inventory rows shall retain instance ID, factory kind, prefix, native ID, canonical ID, source, freshness, and capability provenance.
6. Discovery failure shall preserve the last successful inventory where common policy provides that behavior.
7. Inventory shall use common provider contracts without generic-kind branches in runtimebundle or core.
8. Remote and static results shall remain bounded.

### Requirement 10: Diagnostics and Documentation

**Objective:** As an operator, I want clear inspection and guidance.

#### Acceptance Criteria

1. Diagnostics shall identify each instance as a built-in compatible mode with factory kind, runtime ID, prefix, enabled state, and bounded health/inventory state.
2. Diagnostics shall not imply an executable plugin process or manifest exists.
3. Validation errors shall identify the backend instance and invalid field or conflicting owner.
4. Documentation shall explain when to use a generic mode versus a dedicated external connector.
5. OpenRouter shall be explicitly classified as requiring its dedicated external connector.
6. Samples shall use environment-variable roots and never literal YAML secrets.
7. Documentation shall cover unauthenticated endpoints, URL joining, tokenizer, concurrency, inventory, and restart requirements.
8. Main config examples shall match the final schema.

### Requirement 11: Migration and Plugin-Architecture Compatibility

**Objective:** As a maintainer, I want explicit migration constraints so the feature fits the intended final composition model.

#### Acceptance Criteria

1. Implementation shall use the completed backend-plugin architecture already present on the branch, including `pluginreg` lifecycle/discovery composition and `runtimebundle.Host` ownership of immutable `GenerationRuntime` snapshots.
2. Transitional files scheduled for removal shall not be treated as permanent architecture.
3. The root module shall build and test with `GOWORK=off` and without external connector modules present.
4. Generic modes shall remain available in the minimal distribution with no plugin directory.
5. No external connector module requirement, replacement, generated optional table, blank import, or build tag shall be added.
6. Existing valid kinds, runtime IDs, prefixes, and route semantics shall remain stable.
7. Prefix tests shall combine built-ins with manifest-discovered external factory kinds without activating plugin processes, and shall separately prove post-activation advertised-prefix collision rejection before generation publication.
8. Architecture tests shall fail if behavior leaks into core or external plugin-host infrastructure.
9. Native built-in connectors and the external plugin ABI shall remain unchanged.

### Requirement 12: Test-First Delivery and Release Evidence

**Objective:** As a maintainer, I want implementation driven by executable contracts.

#### Acceptance Criteria

1. Interfaces, config contracts, architecture gates, and failing behavior tests shall precede production code.
2. Tests shall cover strict decoding, forbidden credentials, env pooling, no-auth operation, URL validation/joining, and prefix collisions.
3. Tests shall cover same-kind instances with independent credentials, tokenizer, inventory, and concurrency.
4. Tests shall cover streaming/non-streaming admission, cancellation, terminal release, race, and leaks.
5. Tests shall cover canonical parity for all three families including reasoning, tools, multimodal, usage, errors, and terminal ordering.
6. Tests shall cover external factory-kind collisions without process launch and advertised external route-prefix collisions with a fake activated profile, without launching a real process.
7. CLI tests shall cover check-config, routes, inspect/inventory, and examples.
8. Root architecture and module-isolation gates shall pass with `GOWORK=off`.
9. Deterministic pre-merge validation shall require no live provider or real credential.
