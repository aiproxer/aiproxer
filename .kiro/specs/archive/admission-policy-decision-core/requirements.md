# Requirements Document

## Introduction

The admission-policy-decision-core feature establishes a shared, protocol-neutral decision foundation for LLM Interactive Proxy admission, policy, and enforcement behavior. Platform operators and feature/plugin authors need one consistent way to express allow, deny, mutation, replacement, and pass-through decisions so later budgeting, rate limiting, tool policy, redaction, and safety features can compose without inventing incompatible decision models. The feature must preserve existing protocol compatibility, streaming semantics, routing behavior, secure-session authority, and audit safety while making decision outcomes observable and testable.

## Boundary Context

- **In scope**: protocol-neutral decision vocabulary, stage/outcome legality, scope-aware decision context, deterministic policy ordering, stage-appropriate decision outcomes, failure modes, client-safe denial behavior, audit-safe decision evidence, and compatibility projection for existing hook/extension behavior.
- **Out of scope**: concrete budget, billing, rate limit, PII, redaction, prompt-injection, dangerous-tool, brand-safety, OAuth/SAML, user-directory, admin GUI, reporting, charting, cross-session search, and cloud distribution features.
- **Adjacent expectations**: later enterprise policy features can rely on this feature for stable admission and decision semantics, but those later features own their own business rules, stores, management workflows, and reporting behavior.
- **Revalidation triggers**: routing, streaming, secure-session, diagnostics, frontend error rendering, extension-stage ordering, and startup security posture must be revalidated when this feature changes decision semantics or lifecycle timing.

## Requirements

### Requirement 1: Protocol-Neutral Decision Vocabulary
**Objective:** As a feature/plugin author, I want a shared decision vocabulary, so that independent policy features can compose without incompatible allow/deny/rewrite semantics.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall define policy decision outcomes in protocol-neutral terms that do not depend on any frontend or backend wire protocol.
2. The LLM Interactive Proxy shall distinguish decisions that allow execution, deny execution, annotate execution, mutate request or response content, replace response content, and pass content through unchanged.
3. Where a decision denies or rejects execution, the LLM Interactive Proxy shall associate the decision with a stable reason code and a client-safe message or category.
4. Where a decision mutates or replaces content, the LLM Interactive Proxy shall preserve enough decision metadata for operators to identify that policy-controlled shaping occurred.
5. If a decision outcome is unknown, unsupported, or invalid for the current lifecycle position, the LLM Interactive Proxy shall reject that decision deterministically instead of applying a partial or implicit behavior.
6. The LLM Interactive Proxy shall represent policy decisions with enough shared metadata to identify lifecycle position, decision provider, outcome, reason, client-safe category, failure behavior, and evidence visibility.
7. Where existing extension outcomes are projected into shared policy decision evidence, the LLM Interactive Proxy shall preserve the original extension behavior while using the shared vocabulary for operator evidence.

### Requirement 2: Scope-Aware Decision Context
**Objective:** As a platform operator, I want decisions to use trusted request attribution, so that policy behavior can be grouped and explained by principal, tenant, project, department, workspace, and policy labels.

#### Acceptance Criteria
1. When a policy decision is evaluated for an accepted request, the LLM Interactive Proxy shall make the authoritative principal/scope attribution available to the decision in a safe read-only form.
2. When optional scope fields are unknown, the LLM Interactive Proxy shall preserve the unknown state instead of inferring tenant, project, department, cost center, or workspace values from client payloads.
3. The LLM Interactive Proxy shall keep raw API keys, bearer tokens, OAuth tokens, resume tokens, transport headers, and unvetted claims out of policy decision context.
4. When an auxiliary or internally derived request is evaluated, the LLM Interactive Proxy shall preserve parent request attribution and mark the decision context as internally derived.
5. If no trusted scope is available for a request that reaches decision evaluation, the LLM Interactive Proxy shall use the same explicit local or anonymous identity semantics as the accepted request lifecycle.
6. When a decision context is exposed to policy code, the LLM Interactive Proxy shall provide the authoritative scope separately from legacy principal compatibility fields.

### Requirement 3: Admission Lifecycle Coverage
**Objective:** As a platform operator, I want policies to run at the correct lifecycle moments, so that requests can be stopped or shaped before unsafe or expensive work occurs.

#### Acceptance Criteria
1. When a request is accepted for proxy execution, the LLM Interactive Proxy shall complete applicable pre-execution admission decisions before backend work starts.
2. When a decision needs request content before route planning or backend selection, the LLM Interactive Proxy shall evaluate that decision before committing to a backend attempt.
3. When a decision applies to tool-call behavior, the LLM Interactive Proxy shall evaluate it against canonical tool lifecycle information rather than frontend-specific tool syntax.
4. When a decision applies to response or completion content, the LLM Interactive Proxy shall evaluate it without creating a second non-streaming execution path for normal streaming traffic.
5. While secure-session authority is required, the LLM Interactive Proxy shall not let policy decision evaluation bypass secure-session validation or resume authority checks.
6. The LLM Interactive Proxy shall make the set of legal decision outcomes for each lifecycle position visible to operators and tests.

### Requirement 4: Deterministic Policy Composition
**Objective:** As a feature/plugin author, I want predictable decision composition, so that multiple policy features can run together without order-dependent surprises.

#### Acceptance Criteria
1. Where multiple decision providers apply to the same lifecycle position, the LLM Interactive Proxy shall evaluate them in a deterministic order visible to operators and tests.
2. When a decision denies execution, the LLM Interactive Proxy shall stop later decision providers at that lifecycle position unless the decision semantics explicitly allow continued observation.
3. When multiple mutation or replacement decisions apply, the LLM Interactive Proxy shall apply them in the configured deterministic order and preserve decision evidence for each applied decision.
4. If two applicable decisions produce incompatible outcomes for the same lifecycle position, the LLM Interactive Proxy shall resolve the conflict deterministically and record the selected outcome and reason.
5. The LLM Interactive Proxy shall prevent policy decision providers from silently changing route planning, retry, or failover semantics unless the decision outcome explicitly represents that influence.
6. When existing decision providers produce outcomes through legacy extension interfaces, the LLM Interactive Proxy shall map those outcomes into shared decision evidence without changing their configured execution order.

### Requirement 5: Client-Safe Outcomes And Protocol Compatibility
**Objective:** As an existing client user, I want policy decisions to surface through legal protocol behavior, so that current client integrations remain stable when operators enable admission policies.

#### Acceptance Criteria
1. When a policy decision denies a request before backend output starts, the LLM Interactive Proxy shall surface the denial through the legal error shape of the active frontend protocol.
2. When a policy decision denies or rejects response content after client-visible output has started, the LLM Interactive Proxy shall surface the outcome without silently retrying or failing over to another backend attempt.
3. The LLM Interactive Proxy shall preserve existing successful request and response shapes when no policy decision changes the request or response.
4. The LLM Interactive Proxy shall not expose internal policy identifiers, raw prompts, raw backend payloads, secrets, or unsafe claim values in client-facing denial messages.
5. If a frontend cannot legally represent a policy outcome exactly, the LLM Interactive Proxy shall map the outcome to the nearest stable client-safe error category and preserve the exact decision evidence for operators.
6. When a policy failure or malformed policy decision is surfaced to a frontend, the LLM Interactive Proxy shall classify it separately from capability rejects, backend failures, auth failures, and secure-session denials.

### Requirement 6: Failure, Timeout, And Cancellation Behavior
**Objective:** As a platform operator, I want policy failures to behave predictably, so that safety posture and availability trade-offs are explicit.

#### Acceptance Criteria
1. Where a policy decision provider declares fail-closed behavior, if that provider fails during decision evaluation, the LLM Interactive Proxy shall deny or fail the affected lifecycle step with a stable policy-failure reason.
2. Where a policy decision provider declares fail-open behavior, if that provider fails during decision evaluation, the LLM Interactive Proxy shall continue the affected lifecycle step and record the skipped failure evidence.
3. When decision evaluation exceeds its allowed time budget, the LLM Interactive Proxy shall apply the provider's configured failure behavior rather than waiting indefinitely.
4. When the client request context is canceled, the LLM Interactive Proxy shall stop policy decision work that is no longer needed and shall not convert cancellation into an unrelated policy denial.
5. If policy decision evaluation panics or produces malformed output, the LLM Interactive Proxy shall isolate the fault and handle it according to the applicable failure behavior.
6. If a decision provider returns an outcome that is not legal for the active lifecycle position, the LLM Interactive Proxy shall treat that outcome as a malformed policy decision and apply the applicable failure behavior.

### Requirement 7: Audit-Safe Decision Evidence
**Objective:** As a platform operator, I want decision evidence to be inspectable, so that policy behavior can be audited without leaking sensitive content.

#### Acceptance Criteria
1. When a policy decision is evaluated, the LLM Interactive Proxy shall emit operator-visible evidence containing trace correlation, lifecycle position, decision provider identity, outcome, reason code, and safe principal/scope attribution.
2. When a decision mutates, replaces, denies, or skips content, the LLM Interactive Proxy shall make the decision outcome distinguishable from routing failures, backend failures, auth failures, and secure-session denials.
3. The LLM Interactive Proxy shall keep raw secrets, raw transport headers, unredacted sensitive content, and unsafe claim values out of default decision evidence.
4. Where privileged diagnostics include richer decision details, the LLM Interactive Proxy shall require explicit diagnostic exposure posture before those details are available.
5. The LLM Interactive Proxy shall preserve correlation between policy decision evidence, secure-session evidence, routing attempt lineage, usage observations, and traffic observations for the same logical request.
6. The LLM Interactive Proxy shall expose policy decision evidence through an operator-observable path without requiring usage observers or traffic observers to interpret policy semantics.
7. The LLM Interactive Proxy shall avoid emitting raw prompts, raw backend payloads, secrets, transport headers, or unbounded user-controlled values in default policy decision evidence.

### Requirement 8: Routing, Streaming, And Continuity Invariants
**Objective:** As a platform operator, I want policy decisions to respect core proxy invariants, so that admission controls do not weaken routing or streaming guarantees.

#### Acceptance Criteria
1. When a policy decision occurs before backend work starts, the LLM Interactive Proxy shall record that no backend attempt was committed because of the decision.
2. When backend output has become client-visible, the LLM Interactive Proxy shall not perform transparent retry or failover because of a later policy decision.
3. While streaming a response, the LLM Interactive Proxy shall preserve deterministic canonical event ordering around any policy-controlled response decision.
4. Where a policy decision changes request content before backend execution, the LLM Interactive Proxy shall evaluate capability requirements against the effective request that will be sent onward.
5. The LLM Interactive Proxy shall keep provider-specific policy interpretation out of shared decision semantics unless a later adapter-specific feature explicitly owns that behavior.
6. When a pre-execution policy decision denies a request, the LLM Interactive Proxy shall make the absence of backend attempts distinguishable from backend attempts that failed before output.

### Requirement 9: Compatibility With Existing Extension Surfaces
**Objective:** As an existing plugin author, I want current hooks and feature plugins to keep working, so that the decision foundation can be adopted incrementally.

#### Acceptance Criteria
1. When existing submit, request transform, part hook, tool reactor, pre-request, route hint, traffic observer, usage observer, or completion gate integrations are configured, the LLM Interactive Proxy shall preserve their existing behavior unless they opt into new decision semantics.
2. Where an existing integration already returns allow, deny, reject, mutate, replace, or fail-open/fail-closed behavior, the LLM Interactive Proxy shall be able to represent that behavior in the shared decision evidence.
3. The LLM Interactive Proxy shall not require backend providers to understand policy decision metadata unless a later feature explicitly opts into forwarding safe metadata.
4. The LLM Interactive Proxy shall not require client protocol changes for existing clients to benefit from admission decisions.
5. If an existing extension cannot be represented losslessly in the shared decision vocabulary, the LLM Interactive Proxy shall preserve existing behavior and record only the compatible decision evidence.
6. The LLM Interactive Proxy shall not require existing extension providers to migrate to a new policy interface in order to keep their current behavior.

### Requirement 10: Explicit Scope Exclusions
**Objective:** As a delivery planner, I want this foundation to avoid concrete enterprise feature work, so that later features can build on it without expanding this spec beyond decision semantics.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall not implement concrete budgeting, billing, allowance, rate-limit, or spend-enforcement rules as part of this feature.
2. The LLM Interactive Proxy shall not implement concrete PII detection, prompt-injection detection, harmful-content detection, brand-safety policy, or dangerous-tool policy rules as part of this feature.
3. The LLM Interactive Proxy shall not implement OAuth, SAML, SCIM, user-directory, group-management, or provisioning flows as part of this feature.
4. The LLM Interactive Proxy shall not implement admin GUI workflows, reporting charts, cross-session search, or cloud marketplace distribution as part of this feature.
5. Where later concrete policy features are absent, the LLM Interactive Proxy shall make the decision foundation available without changing request outcomes by itself.
