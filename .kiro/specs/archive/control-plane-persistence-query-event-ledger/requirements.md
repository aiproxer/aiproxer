# Requirements Document

## Introduction

The control-plane persistence, query, and event ledger feature establishes a durable, safe, scope-attributed evidence substrate for the Go LIP runtime. Platform operators, administrators, and future enterprise features need consistent visibility across authentication, secure sessions, logical requests, backend attempts, usage, policy decisions, and audit evidence. Today those facts are available through separate secure-session diagnostics, B2BUA continuity records, token-accounting ledgers, auth events, and observer streams, but the records are fragmented and query surfaces are mostly session-shaped or request-shaped.

This feature defines what the runtime must make observable and query-ready so later audit access, reporting, usage breakdowns, admin APIs, and cross-session diagnostics can rely on one coherent control-plane evidence model without storing raw secrets, replacing existing diagnostics, or changing streaming execution semantics.

## Boundary Context

- **In scope**: safe scope-attributed lifecycle event recording, stable event identity and ordering, evidence-source and availability reporting, stable correlation across sessions and attempts, cross-session query behavior, bounded continuation, usage and policy decision evidence visibility, redaction and retention expectations, readiness/degraded behavior, and compatibility with existing diagnostics and observers.
- **Out of scope**: billing charges, quota or allowance enforcement, rate limiting, policy decision engines, PII or prompt-injection detection engines, OAuth/SAML provisioning, user-directory management, admin GUI, charts, cloud marketplace distribution, provider-specific telemetry forwarding, and automatic migration of historical records between backing evidence stores.
- **Adjacent expectations**: principal/scope attribution supplies stable identity dimensions; admission and policy features supply policy decision records; secure-session, continuity, accounting, auth, usage, traffic, and policy observers supply existing lifecycle facts; future admin, reporting, and budget features consume the query-ready evidence produced here.
- **Revalidation triggers**: secure-session evidence, B2BUA attempt lineage, token accounting, policy decision observation, auth event delivery, diagnostics exposure, startup security posture, streaming behavior, and no-retry-after-output semantics must be revalidated when this feature changes evidence capture or query behavior.

## Requirements

### Requirement 1: Unified Control-Plane Event Capture
**Objective:** As a platform operator, I want authentication, session, attempt, usage, policy, and audit facts captured as consistent control-plane evidence, so that lifecycle facts are not fragmented across unrelated diagnostics surfaces.

#### Acceptance Criteria
1. When an authentication decision is produced, the LLM Interactive Proxy shall make safe control-plane evidence available with the decision outcome, reason, trace correlation, event time, and principal/scope attribution when available.
2. When a secure session is created, resumed, updated, or denied, the LLM Interactive Proxy shall make safe control-plane evidence available that correlates the session with the request trace, principal/scope attribution, session authority state, and continuity identifiers when available.
3. When a backend attempt starts, finishes, fails, is swallowed, loses a race, or produces surfaced output, the LLM Interactive Proxy shall make safe control-plane evidence available that identifies the logical request, A-leg, B-leg, attempt sequence, route outcome, backend, model, surfaced state, timing, and error classification when available.
4. When usage or cost evidence is finalized for a logical request or backend attempt, the LLM Interactive Proxy shall make safe control-plane evidence available with token dimensions, accounting authority, backend/model attribution, and the same request/session/attempt correlation used by other lifecycle evidence.
5. When policy or admission decision evidence is emitted, the LLM Interactive Proxy shall make safe control-plane evidence available with the decision stage, outcome, effect, reason code, visibility, and principal/scope attribution without changing the original decision outcome.
6. When audit evidence is produced for a session, request, attempt, policy decision, or operator-visible lifecycle event, the LLM Interactive Proxy shall make safe control-plane evidence available with correlation identifiers, safe action/result information, event time, and redaction state when available.
7. The LLM Interactive Proxy shall assign stable identities and ordering information to recorded control-plane evidence so query consumers can page, deduplicate, and correlate results deterministically.
8. The LLM Interactive Proxy shall identify the evidence category and evidence availability state for each recorded control-plane record so query consumers can distinguish recorded, partial, unavailable, redacted, expired, and unsupported evidence.

### Requirement 2: Cross-Session Query Behavior
**Objective:** As an operator or future admin feature, I want query-ready views over control-plane evidence, so that I can investigate traffic across sessions, users, projects, models, and time windows.

#### Acceptance Criteria
1. When a query consumer asks for session summaries, the LLM Interactive Proxy shall return matching sessions with stable identifiers, last activity, principal/scope attribution, usage totals when available, attempt counts when available, and evidence availability state.
2. When a query consumer asks for attempt history, the LLM Interactive Proxy shall return matching attempts with chronological ordering, A-leg/B-leg correlation, backend/model attribution, route outcome, surfaced state, timing, error classification, and evidence availability state when available.
3. When a query consumer asks for usage evidence, the LLM Interactive Proxy shall return matching usage rows or aggregates grouped by requested scope, session, backend, model, time window, and accounting plane when those dimensions are available.
4. When a query consumer asks for policy or audit evidence, the LLM Interactive Proxy shall return matching decision and audit records with safe reasons, effects, visibility, correlation identifiers, redaction state, and evidence availability state when available.
5. When a query includes filters for principal, tenant, organization, workspace, project, department, cost center, credential, time range, backend, model, session, A-leg, B-leg, outcome, effect, visibility, or reason code, the LLM Interactive Proxy shall apply the requested filters that are supported by the recorded evidence and report unsupported filters explicitly.
6. When a query result set is larger than the configured or default page size, the LLM Interactive Proxy shall return a bounded page and a stable continuation signal rather than returning an unbounded result.
7. When a query consumer follows a valid continuation signal, the LLM Interactive Proxy shall continue from the prior result position without duplicating or skipping records that remain visible under the same query conditions.
8. If no records match a valid query, the LLM Interactive Proxy shall return an empty result rather than fabricating sessions, attempts, usage, policy decisions, audit entries, or lifecycle events.
9. While the control-plane query capability is disabled, the LLM Interactive Proxy shall report the capability as disabled rather than returning a misleading empty result.

### Requirement 3: Correlation, Authority, and Evidence Consistency
**Objective:** As an operator investigating routing or session behavior, I want evidence from different runtime subsystems to agree on identifiers and outcomes, so that diagnostics explain what actually happened.

#### Acceptance Criteria
1. When multiple evidence records describe one logical request, the LLM Interactive Proxy shall preserve common trace, session, A-leg, B-leg, and attempt identifiers where those identifiers are known.
2. When pre-output failover or parallel racing creates multiple backend attempts for one logical request, the LLM Interactive Proxy shall distinguish surfaced attempts from swallowed, failed, cancelled, or losing attempts.
3. When client-visible output has started for a backend attempt, the LLM Interactive Proxy shall not report a later replacement attempt as if it transparently continued the same surfaced attempt.
4. When existing secure-session, continuity, token-accounting, auth, usage, traffic, or policy evidence describes the same lifecycle fact, the LLM Interactive Proxy shall avoid producing contradictory query results for the same correlation identifiers.
5. When multiple evidence sources can describe the same fact with different detail levels, the LLM Interactive Proxy shall expose enough source or availability context for query consumers to understand whether the result is authoritative, projected, partial, or unavailable.
6. If evidence is partial because a lifecycle stage did not run, did not emit data, was retained elsewhere, or failed before recording, the LLM Interactive Proxy shall expose the missing evidence as unavailable or unknown rather than inventing values.
7. When evidence is derived from an existing diagnostic, observer, or ledger source, the LLM Interactive Proxy shall preserve shared identifiers and safe attribution consistently with that source.

### Requirement 4: Scope Attribution and Privacy Safety
**Objective:** As a security-conscious operator, I want persisted and queried evidence to include useful attribution without exposing credentials or raw transport data, so that diagnostics and reporting remain safe by default.

#### Acceptance Criteria
1. When control-plane evidence includes identity or ownership information, the LLM Interactive Proxy shall use safe principal/scope attribution rather than raw client protocol fields as the authority for identity dimensions.
2. When safe scope attribution includes principal, tenant, organization, workspace, project, department, cost center, credential, roles, safe claims, policy labels, origin, or parent trace information, the LLM Interactive Proxy shall preserve those values for query results and filters when they are available and classified as safe.
3. The LLM Interactive Proxy shall distinguish unknown attribution fields from known fields whose value is intentionally empty in stored and queried evidence.
4. The LLM Interactive Proxy shall not store raw bearer tokens, API keys, OAuth tokens, resume tokens, credential secrets, or raw transport headers in control-plane event records or query results.
5. The LLM Interactive Proxy shall not store raw request or response payload content in control-plane event records unless a privileged capture policy explicitly allows that content to be recorded.
6. Where privileged raw capture or raw audit visibility is allowed, the LLM Interactive Proxy shall mark the resulting evidence as privileged and keep default query results redacted or summarized.
7. If recorded evidence contains redacted, summarized, hashed, expired, unsupported, or unavailable fields, the LLM Interactive Proxy shall expose that state explicitly rather than returning ambiguous empty strings.
8. When query results include roles, safe claims, policy labels, scope labels, usage metadata, or audit labels, the LLM Interactive Proxy shall expose only values classified as safe for operator or feature consumption.

### Requirement 5: Runtime Non-Interference and Streaming Safety
**Objective:** As a client and operator, I want evidence recording to preserve request execution semantics, so that observability does not change successful traffic, failover rules, or streaming behavior.

#### Acceptance Criteria
1. While a request is executing, the LLM Interactive Proxy shall preserve canonical streaming order and response semantics regardless of whether non-mandatory control-plane evidence is recorded immediately, later, or not at all.
2. If non-mandatory control-plane event recording or evidence observation fails, the LLM Interactive Proxy shall make the failure observable to operators without changing a request outcome that would otherwise succeed.
3. If control-plane event recording fails after the first client-visible output event, the LLM Interactive Proxy shall not trigger silent retry, failover, or replacement solely because of that recording failure.
4. Where an operator has configured mandatory control-plane recording for a lifecycle stage, the LLM Interactive Proxy shall fail before the protected upstream work begins if the required evidence cannot be recorded or guaranteed.
5. When mandatory recording is not configured for a lifecycle stage, the LLM Interactive Proxy shall not fail a request solely because query-ready evidence for that stage is unavailable.
6. The LLM Interactive Proxy shall keep non-streaming behavior as collection over the same canonical stream path while preserving the same control-plane correlation and evidence requirements.
7. When evidence recording uses work that can outlive a request, the LLM Interactive Proxy shall make shutdown, cancellation, and degraded-state effects visible to operators without changing already-surfaced client output.

### Requirement 6: Retention, Redaction, and Data Lifecycle Expectations
**Objective:** As an operator responsible for compliance and operational hygiene, I want queryable evidence to respect retention and redaction expectations, so that control-plane records do not become unbounded or overexposed.

#### Acceptance Criteria
1. Where a retention policy is configured, the LLM Interactive Proxy shall make records outside the retained window unavailable to normal query results after the retention action has completed.
2. Where a record is redacted according to an applicable profile, the LLM Interactive Proxy shall preserve safe correlation metadata while withholding fields that are no longer visible under that profile.
3. When a query result omits data because of retention or redaction, the LLM Interactive Proxy shall indicate that the data is unavailable, expired, or redacted when that distinction is known.
4. If retention or redaction affects detailed evidence but aggregate evidence remains available, the LLM Interactive Proxy shall avoid presenting aggregate values as detailed raw records.
5. Where privileged or raw evidence exists but the query consumer is using the default visibility level, the LLM Interactive Proxy shall return redacted or summarized evidence rather than privileged raw evidence.
6. The LLM Interactive Proxy shall not use retention or redaction processing to change routing, policy, usage, or session outcomes for in-flight requests.

### Requirement 7: Operator Readiness and Failure Visibility
**Objective:** As an operator running the standard distribution, I want control-plane persistence and queries to report readiness and failures clearly, so that misconfigured evidence capture does not silently undermine diagnostics.

#### Acceptance Criteria
1. When control-plane persistence or query capability is enabled, the LLM Interactive Proxy shall expose whether the capability is ready, degraded, unavailable, or disabled before operators rely on query results.
2. When control-plane persistence or query capability is degraded, the LLM Interactive Proxy shall expose an operator-visible reason that distinguishes recording, querying, retention, redaction, and backing-capability failures when that distinction is known.
3. If control-plane evidence cannot be recorded because the backing capability is unavailable, the LLM Interactive Proxy shall expose an operator-visible failure reason without leaking raw infrastructure errors to clients.
4. If a query request is invalid, unsupported, too broad, or exceeds configured bounds, the LLM Interactive Proxy shall return a stable error classification rather than executing an unbounded query.
5. While control-plane persistence is disabled, the LLM Interactive Proxy shall preserve existing request execution behavior and report the control-plane query capability as disabled rather than returning misleading empty evidence.
6. Where startup security posture requires durable evidence for an enabled diagnostic or audit feature, the LLM Interactive Proxy shall fail closed before serving traffic if that requirement cannot be satisfied.

### Requirement 8: Compatibility With Existing Diagnostics, Stores, and Observers
**Objective:** As an existing operator, I want the new control-plane foundation to preserve current diagnostics behavior, so that existing secure-session, accounting, and routing evidence remains useful during migration.

#### Acceptance Criteria
1. When existing secure-session diagnostics are enabled, the LLM Interactive Proxy shall continue to provide session list, detail, transcript, audit, and by-A-leg evidence with no loss of currently exposed safe fields.
2. When existing token-accounting evidence is present, the LLM Interactive Proxy shall preserve its request and attempt correlation when presenting control-plane usage views.
3. When existing B2BUA continuity evidence is present, the LLM Interactive Proxy shall preserve A-leg and B-leg lineage semantics in control-plane query results.
4. When existing auth, usage, traffic, or policy observers receive evidence, the LLM Interactive Proxy shall not require those observers to understand the new query capability in order to keep receiving their current events.
5. If new control-plane query views and existing diagnostic views expose the same evidence, the LLM Interactive Proxy shall keep shared identifiers, safe attribution, and redaction state consistent across those views.
6. When existing evidence sources cannot satisfy a requested control-plane filter or detail level, the LLM Interactive Proxy shall report that limitation explicitly rather than silently widening the query.

### Requirement 9: Query and Evidence Contract Boundaries
**Objective:** As a future feature integrator, I want stable query-ready control-plane behavior without depending on current storage details, so that later admin, audit, reporting, and budget features can consume evidence safely.

#### Acceptance Criteria
1. When a future feature consumes control-plane query results, the LLM Interactive Proxy shall provide stable identifiers, correlation fields, evidence category, evidence time, availability state, redaction state, and safe scope attribution where available.
2. When a future feature consumes usage evidence, the LLM Interactive Proxy shall distinguish observed usage, accounting authority, unavailable usage, and failed accounting evidence when those states are known.
3. When a future feature consumes policy or audit evidence, the LLM Interactive Proxy shall preserve safe reason codes, effects, visibility, and correlation identifiers without exposing privileged content by default.
4. If a future feature asks for a query capability that is not supported by recorded evidence, the LLM Interactive Proxy shall return a stable unsupported-capability indication rather than fabricating or silently dropping semantics.
5. The LLM Interactive Proxy shall not require query consumers to know which existing diagnostic, observer, ledger, or store supplied a result in order to use the safe query contract.

### Requirement 10: Explicit Scope Exclusions
**Objective:** As a delivery planner, I want this foundation to avoid implementing adjacent enterprise features, so that later features can build on stable evidence without expanding this spec beyond persistence and query readiness.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall not implement billing charges, invoices, allowance enforcement, spend caps, or rate limiting as part of this feature.
2. The LLM Interactive Proxy shall not implement OAuth, SAML, SCIM, user-directory management, or external identity provisioning as part of this feature.
3. The LLM Interactive Proxy shall not implement PII detection, prompt-injection detection, harmful-content detection, dangerous-tool policy, or policy decision engines as part of this feature.
4. The LLM Interactive Proxy shall not implement a web administration panel, reporting charts, settings UI, policy UI, or cloud marketplace distribution as part of this feature.
5. The LLM Interactive Proxy shall not forward control-plane event records, scope metadata, audit records, or query results to backend providers or client-facing protocol responses by default.
6. The LLM Interactive Proxy shall not require automatic migration of historical records between existing backing evidence stores as part of this feature.
7. The LLM Interactive Proxy shall preserve existing routing, capability negotiation, secure-session authority, and no-retry-after-first-output behavior unless a later approved feature explicitly changes those behaviors.
