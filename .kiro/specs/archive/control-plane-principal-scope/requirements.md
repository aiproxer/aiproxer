# Requirements Document

## Introduction

The control-plane principal scope feature establishes one authoritative, protocol-neutral identity and attribution snapshot for each accepted LLM proxy request. Platform operators, feature plugins, usage/admission features, policy features, audit flows, and future administration surfaces need this snapshot so identity, tenant, project, department, credential, and policy attribution are represented consistently without trusting client-provided protocol fields or leaking provider/transport details into core orchestration.

## Boundary Context

- **In scope**: request principal/scope attribution, trusted source precedence, safe read-only exposure, lifecycle propagation, audit-safe evidence, local-mode identity semantics, existing principal compatibility projection, and compatibility expectations for current frontends and feature integrations.
- **Out of scope**: OAuth/SAML provisioning flows, user-directory management, billing, budgeting, rate limiting, PII redaction engines, policy engines, admin GUI, and cloud distribution.
- **Adjacent expectations**: later usage, budget, redaction, policy, and admin features can rely on this feature for stable attribution, but those later features own their own enforcement and reporting behavior.
- **Revalidation triggers**: secure session, diagnostics, startup security posture, frontend parity, and B2BUA lineage must be revalidated when this feature changes principal propagation or request lifecycle evidence.

## Requirements

### Requirement 1: Authoritative Principal/Scope Snapshot
**Objective:** As a platform operator, I want each accepted request to have one authoritative identity and attribution snapshot, so that audit, usage, policy, and diagnostics do not infer identity differently.

#### Acceptance Criteria
1. When an inbound request is accepted for proxy execution, the LLM Interactive Proxy shall associate exactly one authoritative principal/scope snapshot with the request lifecycle.
2. The LLM Interactive Proxy shall distinguish subject categories for human users, service identities, local synthetic identities, and unknown identities when those categories are known.
3. The LLM Interactive Proxy shall distinguish unknown scope fields from known fields whose value is intentionally empty.
4. If an accepted request has no externally authenticated user identity, the LLM Interactive Proxy shall represent the caller with an explicit allowed synthetic or anonymous identity rather than silently omitting identity.
5. The LLM Interactive Proxy shall derive any legacy principal-only view for the same request from the authoritative principal/scope snapshot.
6. If a request is denied before proxy execution, the LLM Interactive Proxy shall not create a successful request lifecycle snapshot for that request.

### Requirement 2: Trusted Identity Source Boundaries
**Objective:** As a security-conscious operator, I want request identity to come from trusted authentication and operator-controlled attribution sources, so that client protocol fields cannot grant authority.

#### Acceptance Criteria
1. When authentication produces an accepted principal, the LLM Interactive Proxy shall derive authoritative identity from trusted authentication results and operator-controlled attribution, not from client-provided protocol payloads alone.
2. If client-provided session, resume, or scope hints conflict with trusted identity authority, the LLM Interactive Proxy shall not elevate authority from the client-provided hints.
3. While the deployment is operating in multi-user or non-loopback mode, the LLM Interactive Proxy shall require accepted requests to satisfy the configured access posture before proxy execution begins.
4. Where local no-auth mode is explicitly allowed, the LLM Interactive Proxy shall mark accepted requests as local single-user scope so downstream features can distinguish them from multi-user traffic.
5. When trusted authentication supplies credential attribution, the LLM Interactive Proxy shall preserve non-secret credential identifiers separately from raw credential material.
6. The LLM Interactive Proxy shall keep raw bearer tokens, API keys, OAuth tokens, resume tokens, and transport header values out of the authoritative principal/scope snapshot.

### Requirement 3: Stable Attribution Coverage
**Objective:** As a future usage, policy, or admin feature author, I want stable attribution fields, so that features can group traffic consistently without inventing private identity models.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall support stable attribution for principal identity, display label, authentication method, credential identifier, roles or scopes, tenant or organization, workspace, project, department, cost center, and policy labels when those values are known.
2. When only a subset of attribution fields is known, the LLM Interactive Proxy shall preserve the known fields without fabricating values for unknown fields.
3. The LLM Interactive Proxy shall expose stable machine-readable identifiers for populated attribution fields that may be used by later audit, usage, policy, and diagnostics features.
4. If an attribution value is intended only for display, the LLM Interactive Proxy shall keep it distinct from identifiers used for authorization, grouping, or enforcement.
5. When local or external authentication lacks optional tenant, project, department, or cost-center attribution, the LLM Interactive Proxy shall leave those fields unknown rather than inferring them from principal names, model names, or transport paths.
6. The LLM Interactive Proxy shall avoid provider-specific and frontend-specific identity terms in the shared attribution model.

### Requirement 4: Lifecycle Propagation and Immutability
**Objective:** As a feature plugin author, I want a consistent request identity view throughout request execution, so that extension behavior and lifecycle records agree about who and what the request belongs to.

#### Acceptance Criteria
1. When request execution begins, the LLM Interactive Proxy shall make the principal/scope snapshot available before downstream backend work starts.
2. While a request is executing, the LLM Interactive Proxy shall preserve the original trusted attribution as immutable request identity evidence.
3. If later lifecycle stages add request annotations, the LLM Interactive Proxy shall keep those annotations separate from the original trusted attribution.
4. When an auxiliary internal request is created from a parent request, the LLM Interactive Proxy shall preserve parent principal/scope attribution and mark the request as internally derived.
5. If request execution creates multiple backend attempts for one logical request, the LLM Interactive Proxy shall keep all attempts associated with the same authoritative request principal/scope snapshot.
6. If multiple lifecycle views expose identity for the same request, the LLM Interactive Proxy shall keep those views consistent with the authoritative principal/scope snapshot.

### Requirement 5: Safe Read-Only Exposure
**Objective:** As a feature plugin author, I want safe read-only access to identity context, so that plugins can make policy and observability decisions without receiving secrets.

#### Acceptance Criteria
1. When a feature integration requests identity context, the LLM Interactive Proxy shall provide a read-only, non-secret view of the principal/scope snapshot.
2. The LLM Interactive Proxy shall not expose raw credentials, raw transport headers, unvetted claim values, or resume authority through the safe identity view.
3. Where claim, role, label, or scope values are included in the safe identity view, the LLM Interactive Proxy shall expose only values considered operator-safe for feature decisions and audit.
4. If an identity field is not safe for feature exposure, the LLM Interactive Proxy shall omit or redact that field rather than exposing the raw value.
5. The LLM Interactive Proxy shall prevent callers that receive the safe identity view from mutating the authoritative request snapshot.

### Requirement 6: Audit-Safe Lifecycle Evidence
**Objective:** As an operator, I want identity and scope to appear consistently in audit and diagnostics evidence, so that request ownership and security decisions are explainable without exposing secrets.

#### Acceptance Criteria
1. When authentication succeeds or fails, the LLM Interactive Proxy shall produce operator-safe decision evidence that includes trace correlation, outcome, reason, and safe principal/scope attribution when available.
2. When a secure session is created, resumed, or denied, the LLM Interactive Proxy shall connect the session evidence to the authoritative principal/scope attribution when available.
3. When backend attempt lineage is recorded, the LLM Interactive Proxy shall make the request principal/scope attribution available for correlation without changing B2BUA recovery semantics.
4. When usage or traffic observers receive lifecycle evidence, the LLM Interactive Proxy shall provide safe attribution identifiers from the authoritative principal/scope snapshot without exposing raw secrets.
5. The LLM Interactive Proxy shall preserve correlation across authentication, session, routing, attempt, and usage evidence for one logical request.

### Requirement 7: Compatibility With Existing Protocol Surfaces
**Objective:** As an existing client user, I want principal/scope attribution to be internal to the proxy, so that current client integrations continue to work without protocol changes.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall preserve current client-facing protocol request and response shapes unless a later feature explicitly changes a protocol surface.
2. When a request lacks optional tenant, project, department, or cost-center attribution, the LLM Interactive Proxy shall continue normal routing and backend execution unless another configured feature denies the request.
3. Where existing feature integrations consume the current principal view, the LLM Interactive Proxy shall continue to provide compatible principal identity fields.
4. The LLM Interactive Proxy shall not require backend providers to understand or receive control-plane principal/scope attribution unless an adapter or later feature explicitly opts into forwarding safe metadata.
5. The LLM Interactive Proxy shall not change existing secure-session resume eligibility, B2BUA recovery, or backend attempt selection solely because optional scope attribution is absent.
6. The LLM Interactive Proxy shall keep non-streaming behavior as collection over the canonical stream path while carrying the same principal/scope attribution.

### Requirement 8: Explicit Scope Exclusions
**Objective:** As a delivery planner, I want this foundation to avoid adjacent feature work, so that later enterprise features can build on it without expanding this spec beyond attribution.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall not implement OAuth or SAML provisioning flows as part of this feature.
2. The LLM Interactive Proxy shall not implement billing, budgeting, rate limiting, allowance management, or spend enforcement as part of this feature.
3. The LLM Interactive Proxy shall not implement PII detection, content redaction policy, dangerous tool policy, or policy decision engines as part of this feature.
4. The LLM Interactive Proxy shall not implement user-directory management, admin GUI workflows, reporting charts, or cross-session search as part of this feature.
5. Where later features are absent, the LLM Interactive Proxy shall make principal/scope attribution available without changing request allow/deny outcomes by itself.
