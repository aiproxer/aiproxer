# Requirements Document

## Introduction

Model capability handling in the Go LIP needs to support administrators and runtime routing while replacing the Python-era `models.dev` assumptions. Today capability data is scattered across backend-specific hardcoded catalogs, config hints, and ad hoc discovery paths; the new behavior should provide a locally cached, automatically refreshed capability catalog with conservative fallback, override layers, partial matching, and routing-time filtering so model/backend pairs are selected safely without disruptive fetches.

## Boundary Context

- **In scope**: model capability catalog enablement, refresh behavior, local fallback behavior, operator configuration, operator overrides, model-name matching, per-candidate routing eligibility, request-shape capability checks, context-size limit checks, and operator-visible diagnostics.
- **Out of scope**: changing provider protocols, adding provider-specific live discovery protocols, replacing route selector syntax, guaranteeing exact provider tokenization, pricing or billing enforcement, exposing raw catalog schemas as public API contracts, and post-output transparent failover.
- **Adjacent expectations**: existing frontend adapters provide canonical request shape, existing routing expands route candidates, existing capability negotiation rejects or downgrades incompatible candidates, and backend adapters remain responsible for provider-specific request execution.

## Requirements

### Requirement 1: Catalog Availability and Refresh
**Objective:** As an operator, I want model capability data to refresh automatically without disrupting traffic, so that routing decisions can use current model facts while remaining available during update failures.

#### Acceptance Criteria
1. Where the models.dev catalog feature is enabled, the Go LIP runtime shall use the latest valid local catalog snapshot for request-time decisions.
2. When a catalog update succeeds, the Go LIP runtime shall make the updated catalog snapshot available for subsequent routing and capability decisions without interrupting in-flight requests.
3. If a catalog update cannot be fetched, parsed, or validated, the Go LIP runtime shall continue using the latest valid local catalog snapshot.
4. If no valid local catalog snapshot exists and the feature is enabled, the Go LIP runtime shall fall back to existing backend-declared capabilities and shall expose that catalog data is unavailable.
5. While catalog updates are enabled, the Go LIP runtime shall retry failed updates according to configured timing without blocking request processing.
6. If fetched catalog data uses an unsupported or invalid schema, the Go LIP runtime shall reject that update and shall continue using the latest valid local catalog snapshot.
7. When a catalog snapshot is used for decisions, the Go LIP runtime shall expose a snapshot generation, timestamp, or equivalent freshness indicator when available.

### Requirement 2: Catalog Configuration
**Objective:** As an operator, I want explicit controls for catalog usage and refresh cadence, so that deployments can choose the right balance between current metadata and operational stability.

#### Acceptance Criteria
1. The Go LIP runtime shall allow operators to enable or disable use of models.dev catalog data.
2. The Go LIP runtime shall allow operators to enable or disable automatic catalog updates independently from catalog usage.
3. Where automatic catalog updates are enabled, the Go LIP runtime shall allow operators to configure the update interval.
4. Where automatic catalog updates are enabled, the Go LIP runtime shall allow operators to configure the catalog source location and local cache location.
5. If catalog usage is disabled, the Go LIP runtime shall route and negotiate capabilities using existing backend declarations and configured overrides that do not depend on models.dev data.
6. If catalog configuration is invalid, the Go LIP runtime shall reject startup or reload with an operator-visible configuration error.

### Requirement 3: Capability and Limit Normalization
**Objective:** As a platform user, I want provider catalog facts to become protocol-neutral capability and limit facts, so that routing decisions are based on request semantics rather than provider-specific metadata.

#### Acceptance Criteria
1. When catalog data is available for a model, the Go LIP runtime shall derive protocol-neutral capability facts needed for request compatibility decisions.
2. When catalog data includes model context or output limits, the Go LIP runtime shall make those limits available for request compatibility decisions.
3. If catalog data contains provider-specific metadata that is not used for request compatibility, the Go LIP runtime shall not expose that metadata as a required runtime capability.
4. If a requested capability cannot be confirmed by a matching administrator override or a matching catalog entry, the Go LIP runtime shall not treat catalog data alone as a reason to reject the candidate.
5. The Go LIP runtime shall preserve existing explicit downgrade behavior for capabilities that are already classified as safely downgradable.
6. If catalog data lacks enough information to derive a specific capability or limit, the Go LIP runtime shall leave that capability or limit unknown unless an administrator override supplies it.

### Requirement 4: Model Name Matching
**Objective:** As an operator, I want model names from routes and catalogs to match even when providers use different prefixes, so that capability decisions work across hosted, proxied, and aliased models.

#### Acceptance Criteria
1. When a route model exactly matches a catalog model name, the Go LIP runtime shall use the exact match for catalog capability decisions.
2. When exact matching fails and a route model has a provider prefix, the Go LIP runtime shall attempt deterministic matching using the normalized model name without the provider prefix.
3. When normalized matching identifies exactly one catalog model, the Go LIP runtime shall use that catalog model for capability decisions and record that the match was non-exact.
4. If normalized matching identifies multiple possible catalog models, the Go LIP runtime shall treat the catalog match as ambiguous and shall not silently choose one.
5. If no catalog match is found, the Go LIP runtime shall fall back to backend declarations and applicable overrides for that candidate.
6. When a candidate is evaluated against administrator overrides or catalog data, the Go LIP runtime shall classify the match as exact, non-exact, ambiguous, or no-match for diagnostics.

### Requirement 5: Operator Overrides
**Objective:** As an administrator, I want to override model facts globally or for a specific backend/model pair, so that deployment-specific behavior can correct or narrow catalog data.

#### Acceptance Criteria
1. The Go LIP runtime shall allow administrators to define overrides by model name.
2. The Go LIP runtime shall allow administrators to define overrides by specific backend and model pair.
3. When a backend/model-pair override exactly matches a candidate, the Go LIP runtime shall use that override before any model-name override or catalog match.
4. When no backend/model-pair override matches and a model-name override matches a candidate, the Go LIP runtime shall use that model-name override before any catalog match.
5. When no administrator override matches a candidate and catalog data matches the candidate, the Go LIP runtime shall use the catalog data for compatibility decisions.
6. If no administrator override or catalog data matches a candidate, the Go LIP runtime shall not apply proxy-side capability or context-size limiting based on this feature.
7. If an override conflicts with another applicable source, the Go LIP runtime shall expose the effective source precedence in operator diagnostics.
8. If an override references an unknown model, the Go LIP runtime shall accept it as an operator-defined fact and shall expose that it did not originate from catalog data.

### Requirement 6: Candidate Compatibility Filtering
**Objective:** As a client user, I want multi-candidate routes to avoid incompatible backend/model pairs before execution, so that requests fail over only to candidates that can satisfy the current request shape.

#### Acceptance Criteria
1. When a request route expands to multiple backend/model candidates, the Go LIP runtime shall evaluate each backend/model candidate independently for capability compatibility.
2. When a candidate lacks a required hard capability for the current request shape, the Go LIP runtime shall exclude that candidate before upstream execution.
3. When a candidate only lacks capabilities that are explicitly downgradable, the Go LIP runtime shall apply the existing downgrade behavior before upstream execution.
4. If no administrator override or catalog data matches a candidate, the Go LIP runtime shall not exclude that candidate based on model capability catalog checks.
5. If all candidates are excluded by capability compatibility, the Go LIP runtime shall fail the request with an explicit capability mismatch error.
6. The Go LIP runtime shall preserve weighted routing and ordered failover semantics among candidates that remain compatible.

### Requirement 7: Context and Size Compatibility
**Objective:** As a client user, I want routes to avoid models that cannot fit the current request or session context, so that oversized requests fail early or choose compatible candidates.

#### Acceptance Criteria
1. When a candidate has a known context limit, the Go LIP runtime shall compare the current request size estimate against that candidate's context limit before upstream execution.
2. When session or continuity context contributes to the request sent upstream, the Go LIP runtime shall include that contribution in the compatibility decision when an estimate is available.
3. If the request size estimate exceeds a candidate's known context limit, the Go LIP runtime shall exclude that candidate before upstream execution.
4. If no administrator override or catalog data provides a context limit for a candidate, the Go LIP runtime shall not exclude that candidate based on model context size checks.
5. If no request size estimate is available, the Go LIP runtime shall not exclude a candidate solely because context size is unknown.
6. If all candidates are excluded by known context limits, the Go LIP runtime shall fail the request with an explicit context limit error.
7. When a context decision uses an estimated request size rather than an exact provider token count, the Go LIP runtime shall make that estimate basis visible in diagnostics when diagnostics are enabled.

### Requirement 8: Request-Time Safety and Failover Semantics
**Objective:** As an operator, I want catalog-based filtering to respect existing LIP failover guarantees, so that model capability decisions do not change streaming safety semantics.

#### Acceptance Criteria
1. The Go LIP runtime shall perform catalog-based compatibility decisions before the selected candidate starts producing client-visible output.
2. When catalog-based filtering excludes a candidate, the Go LIP runtime shall treat the exclusion as pre-output routing eligibility, not as a post-output retry.
3. Once a backend attempt has produced client-visible output, the Go LIP runtime shall not switch candidates because of catalog data or catalog refresh results.
4. While a request is in progress, the Go LIP runtime shall use a stable capability decision for each evaluated candidate.
5. If catalog data changes during a request, the Go LIP runtime shall apply the change only to subsequent candidate evaluations or subsequent requests.

### Requirement 9: Operator Diagnostics
**Objective:** As an operator, I want to understand catalog state and routing exclusions, so that capability-driven routing decisions are auditable and debuggable.

#### Acceptance Criteria
1. The Go LIP runtime shall expose whether catalog usage is enabled, disabled, unavailable, or using a stale local snapshot.
2. The Go LIP runtime shall expose the timestamp or generation of the catalog snapshot used for decisions when that information is available.
3. When a candidate is excluded because of capability or context compatibility, the Go LIP runtime shall make the exclusion reason visible in routing diagnostics or attempt lineage.
4. When a non-exact model match is used, the Go LIP runtime shall make the matched catalog model and match confidence visible in diagnostics.
5. If catalog matching is ambiguous, the Go LIP runtime shall expose the ambiguity without silently selecting one candidate catalog entry.
6. If catalog refresh fails, the Go LIP runtime shall expose the latest failure reason category without exposing secrets or user request content.
7. When an effective capability or limit fact is used, the Go LIP runtime shall expose whether it came from a backend/model override, a model override, catalog data, or existing backend declarations.

### Requirement 10: Privacy and External Access Boundaries
**Objective:** As a security-conscious operator, I want catalog refreshes to be isolated from request payloads, so that metadata updates do not leak user prompts, credentials, or session data.

#### Acceptance Criteria
1. When the Go LIP runtime refreshes external catalog data, it shall not include client prompts, tool payloads, session transcripts, or provider API keys in the catalog update request.
2. If external catalog access is disabled by configuration, the Go LIP runtime shall not attempt to fetch catalog updates.
3. The Go LIP runtime shall not require external network access at request time to perform catalog-based compatibility decisions.
4. If catalog refresh diagnostics are exposed, the Go LIP runtime shall not include secrets or user request content in those diagnostics.
5. The Go LIP runtime shall not expose raw external catalog fields as stable public API fields unless they are mapped to protocol-neutral runtime facts.

### Requirement 11: Compatibility With Existing Backend Declarations
**Objective:** As a backend plugin maintainer, I want existing backend capability declarations to remain meaningful, so that catalog support improves routing without replacing adapter-specific knowledge.

#### Acceptance Criteria
1. Where no catalog match or administrator override applies to a candidate, the Go LIP runtime shall continue using existing backend-declared capabilities without adding catalog-based capability or context-size limits.
2. When backend-declared capabilities and administrator or catalog model facts both apply, the Go LIP runtime shall avoid advertising a capability that the backend adapter cannot implement.
3. If a backend adapter cannot implement a feature that the model catalog says the model supports, the Go LIP runtime shall treat that feature as unsupported for that backend/model candidate.
4. The Go LIP runtime shall preserve existing behavior for deployments that do not enable models.dev catalog usage.
