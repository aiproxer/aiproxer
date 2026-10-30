# Requirements Document

## Introduction
The Go LLM Interactive Proxy needs a clear authentication and access-mode model so local single-user use remains convenient while shared multi-user deployments are safe by default. The feature separates deployment posture from authentication policy, preserves the small core and plugin-first product direction, and prepares the OSS proxy to delegate authentication and session decisions to a future enterprise companion without embedding enterprise-only behavior in this repository.

## Boundary Context
- **In scope**: single-user and multi-user access modes, bind-address safety rules, authentication policy configuration, explicit local no-op and local API-key behavior, remote auth delegation behavior, principal establishment, frontend auth decision events, session-start events, protocol-legal authentication rejection expectations, and OAuth-user backend eligibility rules.
- **Out of scope**: implementing an enterprise product, implementing a complete SSO identity provider, adding provider-specific backend OAuth flows, changing canonical LLM request/event translation, changing routing semantics, or adding new client-facing LLM protocol surfaces.
- **Architecture note**: public contracts carry **DTOs** only where stability matters; **ports** (e.g. event sink, remote decider) are defined in the core packages that consume them, with stdhttp as the primary driving transport adapter; OS identity and default logging live in infrastructure or composition wiring, not in core policy as direct `os`/`slog` dependencies.
- **Adjacent expectations**: existing frontend protocol adapters must continue to expose legal protocol errors, existing routing and streaming guarantees must remain intact, existing principal propagation should remain available to downstream policy surfaces, and secure-session features may be used by later design work without making this requirements phase define their internal architecture.

## Requirements

### Requirement 1: Access Mode Selection
**Objective:** As a proxy operator, I want an explicit deployment mode, so that the proxy applies the correct safety posture for local or shared use.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall support a `single_user` access mode and a `multi_user` access mode.
2. When no access mode is configured, the LLM Interactive Proxy shall start in `single_user` mode.
3. When the operator configures `single_user` mode, the LLM Interactive Proxy shall apply single-user safety rules before accepting frontend traffic.
4. When the operator configures `multi_user` mode, the LLM Interactive Proxy shall apply multi-user safety rules before accepting frontend traffic.
5. If the configured access mode is unknown, then the LLM Interactive Proxy shall fail startup with an operator-visible configuration error.
6. While the LLM Interactive Proxy is running, the LLM Interactive Proxy shall not change the effective access mode without a restart.
7. The LLM Interactive Proxy shall treat absence of an authentication handler as distinct from explicit local no-op authentication.

### Requirement 2: Single-User Network Binding
**Objective:** As a local vibe-coder user, I want single-user mode to be reachable only from my machine, so that personal credentials are not accidentally exposed to a network.

#### Acceptance Criteria
1. While `single_user` mode is active, the LLM Interactive Proxy shall allow only loopback IPv4 and loopback IPv6 listener addresses.
2. When no server address is configured in `single_user` mode, the LLM Interactive Proxy shall use a loopback listener address by default.
3. When an existing configuration relies on an implicit broad listener address in `single_user` mode, the LLM Interactive Proxy shall require the operator to choose an explicit safe loopback address or switch to `multi_user` mode.
4. If `single_user` mode is configured with a non-loopback listener address, then the LLM Interactive Proxy shall fail startup with an operator-visible error explaining that single-user mode is loopback-only.
5. If `single_user` mode is configured with an all-interfaces listener address, then the LLM Interactive Proxy shall fail startup with an operator-visible error explaining that single-user mode must not bind broadly.
6. While `single_user` mode is active, the LLM Interactive Proxy shall preserve existing frontend protocol behavior for requests that arrive through an allowed loopback listener.

### Requirement 3: Multi-User Network Binding and Authentication Requirement
**Objective:** As an operator of a shared proxy, I want multi-user mode to allow server deployments only with authentication, so that remote clients cannot access the proxy anonymously.

#### Acceptance Criteria
1. While `multi_user` mode is active, the LLM Interactive Proxy shall allow the operator to bind to loopback, specific IPv4 or IPv6 addresses, or all-interface IPv4 or IPv6 addresses.
2. While `multi_user` mode is active, the LLM Interactive Proxy shall require an authentication policy stronger than no-op authentication.
3. If `multi_user` mode is configured without required authentication, then the LLM Interactive Proxy shall fail startup with an operator-visible configuration error.
4. When `multi_user` mode is configured with a valid authentication policy, the LLM Interactive Proxy shall require each frontend request to satisfy that policy before request execution.
5. If a frontend request does not satisfy the configured multi-user authentication policy, then the LLM Interactive Proxy shall reject or challenge the request using the legal error shape for the active frontend protocol.
6. If authentication rejects or challenges a frontend request, then the LLM Interactive Proxy shall not open a backend attempt for that request.

### Requirement 4: Authentication Policy Levels
**Objective:** As a proxy admin, I want to choose the required authentication level, so that API keys can represent either a user token or a device/app identity that also requires SSO.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall support a no-auth level, an API-key level, and an API-key-plus-SSO level.
2. While the no-auth level is active, the LLM Interactive Proxy shall allow frontend requests without client-presented credentials only when the active access mode permits no-auth.
3. While the API-key level is active, the LLM Interactive Proxy shall require each frontend request to present a valid API key before request execution.
4. While the API-key-plus-SSO level is active, the LLM Interactive Proxy shall require each frontend request to present a recognized API key and a satisfied SSO user authentication state before request execution.
5. When an API key is used as a device or app identity, the LLM Interactive Proxy shall distinguish that identity from the authenticated human principal in auth results and events.
6. If a request satisfies API-key identity but still requires SSO user authentication, then the LLM Interactive Proxy shall return an authentication challenge or denial that makes the unmet SSO requirement visible to the client or operator.

### Requirement 5: Local No-Op Authentication in Single-User Mode
**Objective:** As a local single-user operator, I want authentication to be frictionless by default, so that local coding workflows work without managing API keys.

#### Acceptance Criteria
1. Where local no-op authentication is selected, the LLM Interactive Proxy shall allow frontend requests without client-presented credentials only when the active access mode permits no-op authentication.
2. While local no-op authentication is active, the LLM Interactive Proxy shall establish a principal for each accepted request.
3. When the LLM Interactive Proxy establishes a local no-op principal, the LLM Interactive Proxy shall infer the principal from the user logged into the current operating-system session when that information is available.
4. If the current operating-system user cannot be determined in local no-op mode, then the LLM Interactive Proxy shall use an explicit local fallback principal and make the fallback observable to the operator.
5. While local no-op authentication is active, the LLM Interactive Proxy shall still emit auth decision and session-start events for accepted requests.
6. If no authentication handler is configured, then the LLM Interactive Proxy shall not silently use anonymous pass-through behavior in place of explicit local no-op authentication.

### Requirement 6: Local API-Key Authentication
**Objective:** As a small deployment operator, I want API-key authentication without an enterprise service, so that the OSS proxy can be safely used in simple shared environments.

#### Acceptance Criteria
1. Where local API-key authentication is selected, the LLM Interactive Proxy shall validate presented API keys against operator-configured local key records.
2. When a presented API key matches a configured local key record, the LLM Interactive Proxy shall establish the principal and key identity associated with that record.
3. If a frontend request omits an API key while local API-key authentication is required, then the LLM Interactive Proxy shall reject or challenge the request using the legal error shape for the active frontend protocol.
4. If a frontend request presents an unknown or invalid API key, then the LLM Interactive Proxy shall reject the request before request execution.
5. The LLM Interactive Proxy shall not expose full API-key secret values in logs, diagnostics, auth events, or protocol error bodies.
6. If a local API-key record is configured without enough information to establish a principal or key identity, then the LLM Interactive Proxy shall fail startup with an operator-visible configuration error.

### Requirement 7: Remote Authentication Delegation
**Objective:** As an enterprise operator, I want the OSS proxy to delegate auth decisions to a remote auth authority, so that closed-source enterprise policy can be enforced without changing the OSS proxy behavior model.

#### Acceptance Criteria
1. Where remote authentication is selected, the LLM Interactive Proxy shall obtain authentication decisions from an operator-configured remote auth authority before request execution.
2. When the remote auth authority allows a request, the LLM Interactive Proxy shall use the returned principal, auth level, and relevant identity attributes for downstream execution context and events.
3. When the remote auth authority denies or challenges a request, the LLM Interactive Proxy shall reject or challenge the request using the legal error shape for the active frontend protocol.
4. If remote authentication is required and the remote auth authority is unreachable or returns an unusable decision, then the LLM Interactive Proxy shall fail closed for protected frontend requests.
5. If remote authentication configuration is incomplete, then the LLM Interactive Proxy shall fail startup with an operator-visible configuration error.
6. The LLM Interactive Proxy shall keep remote auth delegation configurable in the OSS proxy without requiring the enterprise implementation to be present for local no-op or local API-key modes.
7. While remote authentication is selected, the LLM Interactive Proxy shall make remote decision outcomes and remote unavailability distinguishable to the operator without exposing secret material.

### Requirement 8: Auth Decision Events
**Objective:** As an operator, I want every frontend authentication decision to produce an event, so that access can be audited consistently across local and enterprise modes.

#### Acceptance Criteria
1. When a frontend request reaches an authentication decision point, the LLM Interactive Proxy shall emit an auth decision event.
2. The LLM Interactive Proxy shall emit auth decision events for allowed, denied, challenged, and failed authentication outcomes.
3. Each auth decision event shall include enough non-secret information to correlate the decision with the request, access mode, auth policy level, frontend surface, outcome, and principal or device identity when known.
4. While local no-op authentication is active, the LLM Interactive Proxy shall emit auth decision events even though no credential validation occurs.
5. If an auth decision event sink fails while authentication is required, then the LLM Interactive Proxy shall apply the configured event failure policy before request execution continues.
6. The LLM Interactive Proxy shall not include raw API keys, raw SSO tokens, or personal OAuth tokens in auth decision events.
7. When no custom auth decision event sink is configured, the LLM Interactive Proxy shall still provide a default operator-observable auth decision record or explicitly document that auth decision event delivery is disabled.

### Requirement 9: Session-Start Events
**Objective:** As an operator, I want each new client-facing session to produce an auth-related event, so that session activity can be audited independently of individual request authentication.

#### Acceptance Criteria
1. When an authenticated or accepted frontend request starts a new proxy-recognized client-facing session, the LLM Interactive Proxy shall emit a session-start event.
2. While local no-op authentication is active, the LLM Interactive Proxy shall emit session-start events for new sessions using the locally inferred principal.
3. Each session-start event shall include enough non-secret information to correlate the session with the request, access mode, auth policy level, principal, frontend surface, and session identity available to the proxy.
4. If a request belongs to an already recognized session, then the LLM Interactive Proxy shall not emit a duplicate session-start event for that same session start.
5. If session identity cannot be established for a request, then the LLM Interactive Proxy shall make the session-start event outcome explicit rather than silently pretending a stable session exists.
6. The LLM Interactive Proxy shall not include raw resume proofs, raw API keys, raw SSO tokens, or personal OAuth tokens in session-start events.
7. While secure session support is disabled or unavailable, the LLM Interactive Proxy shall define session-start behavior using only session identity that is available to the proxy and shall make any reduced audit certainty visible to the operator.

### Requirement 10: OAuth-User Backend Eligibility
**Objective:** As a shared deployment operator, I want personal OAuth-user backends blocked in multi-user mode, so that one user's delegated credentials are not exposed to other users.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall distinguish backends that require personal OAuth-user credentials from backends that use static service credentials or workload credentials.
2. The LLM Interactive Proxy shall expose backend credential posture in a way that does not require the core to inspect plugin-private backend configuration values.
3. While `single_user` mode is active, the LLM Interactive Proxy shall allow enabled OAuth-user backends when their normal backend configuration is valid.
4. While `multi_user` mode is active, the LLM Interactive Proxy shall reject any enabled OAuth-user backend before accepting frontend traffic.
5. If `multi_user` mode rejects an OAuth-user backend, then the LLM Interactive Proxy shall produce an operator-visible error identifying the incompatible backend instance without exposing credential material.
6. While `multi_user` mode is active, the LLM Interactive Proxy shall continue to allow eligible non-OAuth-user backends when their normal backend configuration is valid.
7. The LLM Interactive Proxy shall not silently disable, hide, or reroute around an enabled OAuth-user backend that violates multi-user mode policy.

### Requirement 11: Configuration Validation and Operator Feedback
**Objective:** As a proxy operator, I want invalid auth and access-mode combinations to fail early with clear feedback, so that unsafe deployments are not started accidentally.

#### Acceptance Criteria
1. When the LLM Interactive Proxy loads runtime configuration, the LLM Interactive Proxy shall validate access mode, listener binding, authentication policy, auth handler selection, and OAuth-user backend eligibility before accepting frontend traffic.
2. If configuration validation fails, then the LLM Interactive Proxy shall fail startup with an error that identifies the invalid setting and the violated rule.
3. If multiple configured settings conflict in a way that would weaken authentication, then the LLM Interactive Proxy shall prefer failing startup over choosing a permissive interpretation.
4. When defaults are applied, the LLM Interactive Proxy shall make the effective access mode, listener binding, and authentication level observable to the operator at startup.
5. The LLM Interactive Proxy shall preserve existing plugin-specific configuration opacity except where access-mode safety requires a backend eligibility classification.
6. If a configuration combines a broad listener default with `single_user` mode, then the LLM Interactive Proxy shall prefer a safe loopback effective listener or fail startup rather than accepting a broad listener silently.

### Requirement 12: Protocol and Runtime Compatibility
**Objective:** As a client or plugin author, I want authentication changes to preserve existing LLM proxy behavior, so that access control does not break protocol translation, streaming, or routing guarantees.

#### Acceptance Criteria
1. When a request is authenticated or accepted, the LLM Interactive Proxy shall preserve the existing canonical request and event behavior for that frontend request.
2. When a request is rejected or challenged by authentication, the LLM Interactive Proxy shall not start backend execution for that request.
3. While a frontend stream is active for an authenticated request, the LLM Interactive Proxy shall preserve existing streaming ordering, cancellation, and error-framing guarantees.
4. The LLM Interactive Proxy shall expose established principal information to existing downstream policy, session, diagnostics, and extension views that already consume principal context.
5. The LLM Interactive Proxy shall avoid adding provider-specific auth semantics to canonical LLM request or event contracts.
6. When authentication rejects or challenges a request before execution, the LLM Interactive Proxy shall render the response through the active frontend surface when that surface requires a protocol-specific error body.
7. The LLM Interactive Proxy shall keep auth decision and session-start events separate from the canonical LLM event stream unless a future approved requirement explicitly changes that boundary.

### Requirement 13: Security and Secret Handling
**Objective:** As a security-conscious operator, I want authentication data and credential classifications handled safely, so that enabling auth does not leak secrets through observability or errors.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall treat API keys, SSO tokens, resume proofs, and personal OAuth tokens as secret material.
2. The LLM Interactive Proxy shall not include secret material in startup errors, request errors, structured logs, diagnostics, auth events, session-start events, or frontend responses.
3. If secret-derived identifiers are needed for correlation, then the LLM Interactive Proxy shall expose only non-secret identifiers or redacted fingerprints.
4. When authentication fails, the LLM Interactive Proxy shall provide enough information for the client or operator to understand the failure category without revealing whether a specific secret value exists.
5. While remote authentication is active, the LLM Interactive Proxy shall not weaken local secret-handling rules for data received from or sent to the remote auth authority.
