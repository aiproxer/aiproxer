# Requirements Document

## Introduction

The Go LLM Interactive Proxy needs a token-accounting subsystem that separates provider-billable, client-visible, and proxy-billable usage without leaking tokenizer vendor dependencies into the core domain or public contracts. The v1 implementation wires runtime counting, preflight enforcement, stream reconstruction, ledgering, admin counting, and observability behind explicit core and infrastructure boundaries.

## Boundary Context

- **In scope**: token-accounting planes, provenance, authority, adapter boundaries, fallback strategy, transformed-response accounting, preflight budgeting, ledgering, diagnostics, admin token count surfaces, tokenizer scope, security, observability, performance expectations, and backward-compatibility rules.
- **Runtime scope**: v1 includes runtime counting, local tokenizer wiring, storage schema, HTTP/admin handlers, and user-visible accounting behavior where configured.
- **Approval gating note**: Kiro workflow approval state remains recorded in `spec.json`, but this v1 implementation is wired and validated through the repository test suite.
- **Architecture constraints**: core packages must remain provider-SDK free, tokenizer dependencies must stay out of `pkg/lipapi`, `pkg/lipsdk`, and core domain/application packages, and provider-specific semantics must remain behind protocol/backend adapters.

## Requirements

### Requirement 1: Distinct Usage Planes
**Objective:** As a platform operator, I want token accounting to distinguish commercial viewpoints, so that billing, transparency, and internal control do not overwrite each other.

#### Acceptance Criteria
1. WHEN the proxy records usage for a logical request, THEN it shall preserve separate values for `provider_billable`, `client_visible`, and `proxy_billable` planes.
2. IF a plane is unavailable, THEN the system shall represent that plane as unavailable without fabricating a count.
3. WHEN multiple planes are present for the same request or attempt, THEN the system shall retain each plane independently rather than silently replacing one plane with another.
4. IF downstream protocols expose only one usage view, THEN the system shall define which plane it populates and which planes remain unset.

### Requirement 2: Provenance and Authority
**Objective:** As an auditor, I want every token value to declare where it came from and how authoritative it is, so that operators can trust or dispute it correctly.

#### Acceptance Criteria
1. WHEN the proxy captures a token count, THEN it shall record provenance identifying the source category, including provider-reported API counts, local tokenizer estimates, administrator-supplied metadata, or transformed-response recomputation.
2. WHEN multiple candidate counts exist for the same plane, THEN the system shall preserve authority metadata that distinguishes authoritative, delegated, estimated, and advisory values.
3. IF a provider-reported count conflicts with a local estimate for the same plane, THEN the provider-reported count shall remain preserved as the authoritative provider value and the estimate shall not silently overwrite it.
4. WHEN usage is emitted to diagnostics or ledger surfaces, THEN provenance and authority shall be queryable without exposing request content.

### Requirement 3: Provider Count API First
**Objective:** As a backend integrator, I want official provider count APIs to be the primary counting path, so that provider billing aligns with provider-owned tokenizers whenever available.

#### Acceptance Criteria
1. IF a backend provider exposes a supported token count API or equivalent authoritative usage surface, THEN the proxy shall prefer that source for `provider_billable` accounting.
2. WHEN a provider count API succeeds before request execution, THEN the system shall be able to use that result for preflight checks without invoking local tokenizers for the same plane unless configured for comparison.
3. IF a provider count API is unavailable, unsupported, disabled, or fails before output, THEN the system shall surface an explicit fallback reason rather than silently claiming provider authority.
4. WHEN provider counts are unavailable for a protocol or model, THEN the system shall use configured fallback paths without changing public contracts.

### Requirement 4: Local Tokenizer Fallback
**Objective:** As an operator, I want local tokenizer fallback to exist where justified, so that budgeting and advisory accounting can still work when providers do not expose counts.

#### Acceptance Criteria
1. IF provider-authoritative counts are unavailable for a configured model family, THEN the system shall support a local tokenizer fallback path for non-authoritative or configured-authority planes.
2. WHEN a local tokenizer fallback is used, THEN the recorded usage shall identify the tokenizer family, version or revision if available, and estimate authority.
3. IF no safe tokenizer mapping exists for a request, THEN the system shall fail that counting attempt explicitly or mark it unavailable according to configuration rather than inventing a count.
4. WHEN a local tokenizer library is configured, THEN it shall remain isolated behind adapters and shall not appear in `pkg/lipapi`, `pkg/lipsdk`, or core orchestration packages.

### Requirement 5: Custom Tokenizer Metadata
**Objective:** As an administrator, I want model-to-tokenizer metadata to be configurable, so that unsupported or custom model names can still be mapped intentionally.

#### Acceptance Criteria
1. WHEN an administrator configures custom tokenizer metadata for a model, THEN the system shall preserve that mapping separately from provider-native metadata.
2. IF custom metadata is malformed, ambiguous, or references an unsupported tokenizer family, THEN configuration validation shall fail before runtime.
3. WHEN both provider-derived metadata and administrator metadata exist, THEN the system shall apply deterministic precedence rules and record the source used.
4. IF custom metadata is absent, THEN the system shall not assume a tokenizer mapping beyond documented defaults.

### Requirement 6: Transformed Response Accounting
**Objective:** As a protocol maintainer, I want accounting to reflect proxy-visible transformations, so that client-visible usage does not misrepresent what the client actually received.

#### Acceptance Criteria
1. WHEN the proxy mutates request or response payloads through canonical adapters or hook-driven transformations, THEN the design shall support accounting for both upstream/provider-visible and downstream/client-visible token surfaces.
2. IF the proxy injects, removes, rewrites, or redacts content after backend generation, THEN client-visible accounting shall not silently reuse provider-visible counts as though no transformation occurred.
3. WHEN transformed-response accounting cannot be computed exactly, THEN the system shall preserve the provider value and mark the client-visible value as estimated or unavailable.
4. WHEN multiple attempts occur before output, THEN accounting shall support attempt-scoped usage lineage without double-counting a single client-visible final response.

### Requirement 7: Preflight Context and Budget Checks
**Objective:** As a routing and policy maintainer, I want token estimates available before execution, so that context windows and budget policies can reject unsafe work early.

#### Acceptance Criteria
1. WHEN preflight policy checks require token counts, THEN the system shall support request-scoped counting before backend execution starts.
2. IF preflight counting fails and policy requires a count, THEN execution shall fail explicitly before downstream content is emitted.
3. WHEN preflight counting succeeds, THEN the result shall be usable by context-window and budget policy without requiring runtime tokenization inside transport codecs.
4. IF policy marks counting as advisory only, THEN execution may continue with unavailable counts while preserving the advisory failure reason.

### Requirement 8: Billing Ledger
**Objective:** As a finance or operations user, I want durable accounting records, so that usage can be reconciled per request and attempt.

#### Acceptance Criteria
1. WHEN billing-ledger recording is enabled, THEN the system shall define a ledger record model that can store request identity, attempt identity, usage plane, token dimensions, provenance, authority, and timestamps.
2. IF a logical request has multiple backend attempts before client-visible output, THEN the ledger design shall support attempt lineage without losing which attempt produced which provider-billable count.
3. WHEN no durable ledger is configured, THEN the system shall still allow ephemeral accounting surfaces without changing canonical request or event contracts.
4. WHEN ledger writes fail, THEN the error policy shall be explicitly classified as required or best-effort rather than implicit.

### Requirement 9: Admin Token Count Endpoint
**Objective:** As an operator, I want a protected admin counting surface, so that I can inspect token counts or dry-run budgets without sending client traffic through normal frontends.

#### Acceptance Criteria
1. WHEN an admin token count endpoint is enabled, THEN it shall be exposed only on protected administrative surfaces and not as an unauthenticated public frontend path.
2. WHEN the endpoint returns count results, THEN it shall include planes, provenance, authority, and unavailable reasons without exposing secrets or persisted prompt content beyond the submitted request scope.
3. IF counting requires provider-side APIs, THEN the endpoint shall honor existing trust and credential boundaries and shall not leak provider credentials in responses or logs.
4. WHEN the endpoint is disabled, THEN the system shall not expose it through inventory or HTTP routing as active.

### Requirement 10: Tokenizer Scope and Supported Families
**Objective:** As a maintainer, I want the first tokenizer scope to stay intentionally narrow, so that v1 can deliver value without uncontrolled dependency sprawl.

#### Acceptance Criteria
1. WHEN local tokenizers are introduced, THEN initial scope shall be limited to the documented `tiktoken` and Hugging Face tokenizer families or documented equivalents approved by the spec.
2. IF a model requires an unsupported tokenizer family, THEN configuration or runtime shall report unsupported scope rather than degrading silently.
3. WHEN tokenizer family support expands later, THEN the addition shall be isolated to adapter/configuration layers without changing public usage-plane contracts.
4. WHEN tokenizer packages are imported, THEN architecture tests shall guard forbidden package zones against direct dependency drift.

### Requirement 11: Security and Privacy
**Objective:** As a security reviewer, I want token accounting to avoid becoming a data leak surface, so that usage observability does not expose prompts or secrets.

#### Acceptance Criteria
1. WHEN token counting uses provider APIs, THEN the design shall minimize transmitted data to the least content required by that provider surface.
2. WHEN token counting uses local tokenizers, THEN logs, metrics, diagnostics, and ledger records shall avoid storing raw prompt or completion content unless a separate feature explicitly owns that capture.
3. IF counting requests or failures include model metadata, THEN responses and logs shall redact credentials, bearer tokens, cookies, and provider-specific secrets.
4. WHEN the admin token count endpoint or ledger surfaces are enabled, THEN they shall follow existing administrative protection rules and local-only safety defaults where applicable.

### Requirement 12: Observability and Performance
**Objective:** As an operator, I want counting to be measurable and bounded, so that it can be operated safely in production.

#### Acceptance Criteria
1. WHEN token accounting is enabled, THEN the system shall define observability for count source selection, fallback reasons, latency, and unavailable-rate without requiring request payload logging.
2. IF provider count APIs or local tokenizers add latency, THEN the system shall measure that overhead separately from backend generation latency.
3. WHEN counting work is repeated for the same canonical payload in a single request lifecycle, THEN the design shall allow memoization or reuse in later phases without changing correctness semantics.
4. IF counting cannot finish within configured limits, THEN the system shall fail or degrade according to explicit policy rather than blocking indefinitely.

### Requirement 13: Backward Compatibility
**Objective:** As a platform maintainer, I want token accounting to be additive, so that existing frontend/backend behavior remains stable until explicitly enabled.

#### Acceptance Criteria
1. WHEN token accounting is disabled, THEN existing request execution, streaming behavior, and protocol responses shall remain unchanged.
2. WHEN new accounting data is added to internal records or admin surfaces, THEN existing stable public contracts shall remain backward compatible unless a later approved spec explicitly changes them.
3. IF counting support is partial for some providers or models, THEN unsupported cases shall fail explicitly or remain unavailable according to configuration without regressing successful non-accounting traffic.
4. WHEN future phases introduce tokenizer dependencies, THEN those dependencies shall not force unrelated users of `pkg/lipapi` or `pkg/lipsdk` to transitively pull tokenizer libraries.
