# Requirements Document

## Introduction

OpenAI's September 2026 Sign in with ChatGPT (SIWC) plan-usage flow replaces the private Codex-CLI emulation previously required to use eligible ChatGPT subscriptions from open-source third-party harnesses. AIProxer currently implements that older behavior through the `openai-codex`, `openai-codex-v2`, and `openai-codex-app-server` backend family, including Codex CLI OAuth assumptions, private `chatgpt.com/backend-api/codex/*` endpoints, Codex-specific headers, a bundled Codex system prompt, client-family compatibility adapters, private model-catalog discovery, and Codex-specific continuation/retry/quota behavior.

This specification defines the complete brownfield migration to a new first-party connector that uses OpenAI's documented ChatGPT-plan authorization and public Responses API contract. The new connector must be implemented and proven independently while the legacy connector family remains available. Only after automated verification and explicit empirical human testing succeeds may the implementation remove the complete legacy Codex connector family and all live supporting code, configuration, resources, tests, scripts, and user-facing documentation. The migration is intentionally self-contained: no cleanup, legacy-removal, or compatibility work is deferred to a follow-up specification.

The new backend identifier is `openai-chatgpt-plan`. The legacy identifiers are not retained as compatibility aliases after cutover.

## Requirements

### Requirement 1: New Official ChatGPT Plan Connector
**Objective:** As an AIProxer operator, I want a new backend that uses OpenAI's documented ChatGPT-plan flow, so that third-party harnesses can use eligible ChatGPT subscriptions without impersonating Codex CLI.

#### Acceptance Criteria
1. When `openai-chatgpt-plan` is configured, the system shall register and route to a connector that is independent of the existing `openai-codex`, `openai-codex-v2`, and `openai-codex-app-server` implementations.
2. While the migration is in its proving phase, the system shall allow the new connector and the legacy Codex connector family to coexist without sharing connector-local prompt, transport, continuation, catalog, or authentication state.
3. When the new connector performs inference, the system shall use OpenAI's public `https://api.openai.com/v1` resource and shall not call ChatGPT `backend-api` inference endpoints.
4. When the new connector is selected from any supported frontend that can be represented by the existing canonical translation layer, the system shall route the request through the standard AIProxer frontend/domain pipeline rather than through Codex client-family emulation.
5. The new connector shall not load, import, or depend on the legacy Codex prompt resource, Codex client-family adapters, Codex continuation coordinator, Codex model-catalog package, Codex CLI version logic, or Codex app-server runtime.
6. The new connector shall expose personal/OAuth backend capabilities through the project's capability-oriented backend contract and shall not require a new backend-name hardcode for runtime classification.

### Requirement 2: SIWC Registration and Authorization
**Objective:** As a ChatGPT subscriber, I want AIProxer to authorize through the official SIWC public-client flow, so that I can explicitly grant my ChatGPT plan to the local tool.

#### Acceptance Criteria
1. When a user adds a new ChatGPT-plan profile, the system shall initiate authorization with `client_id=dynamic_agent_client`, a stable host-specific `ext_agent_host_id`, an AIProxer `agent_name_hint`, a fresh `state`, a fresh OIDC `nonce`, and a fresh PKCE S256 challenge.
2. When opening the authorization flow, the system shall use OpenAI's documented accounts authorization endpoint, request `openid profile email offline_access resource.invoke chatgpt.tokens.use.direct`, and set the resource to `https://api.openai.com/v1`.
3. When a loopback callback is used, the system shall bind and advertise `127.0.0.1`, preserve the `/auth/callback` path, and use the exact callback URI for the code exchange; it shall not substitute `localhost`.
4. When a new-registration callback succeeds, the system shall require and persist the issued `client_id` returned by OpenAI and shall not persist `dynamic_agent_client` as the profile's reusable client ID.
5. When exchanging an authorization code, the system shall use the issued client ID, the PKCE verifier, the exact redirect URI, and the `https://api.openai.com/v1` resource without a client secret.
6. When processing the token response, the system shall cryptographically validate the ID token against OpenAI's published OIDC/JWKS metadata, including issuer, audience, expiration, and the request nonce, before activating the profile.
7. When authorization completes without the `chatgpt.tokens.use.direct` granted scope, the system shall retain the validated identity/profile state as non-inference-capable and shall not send plan-funded inference requests until the user reauthorizes with the required permission.
8. When a saved profile is reauthorized, the system shall reuse that profile's issued client ID and the host's stable host ID, keep the returned identity bound to the selected profile, and reject a callback or ID token that resolves to a conflicting registration/identity.

### Requirement 3: Secure Profile, Host, Refresh, and Revocation Lifecycle
**Objective:** As an operator, I want ChatGPT-plan credentials to be managed safely and predictably, so that rotating OAuth credentials cannot leak, race, or become associated with the wrong account.

#### Acceptance Criteria
1. The system shall persist one stable, opaque `ext_agent_host_id` for the AIProxer host and shall keep it separate from per-account profile records.
2. The system shall store each ChatGPT registration separately with its issued client ID, validated issuer and subject, display identity, ID token, access token, refresh token, granted scopes, and expiry/refresh metadata, even when multiple profiles have the same email address.
3. When credential files are created or replaced, the system shall use atomic persistence and owner-only permissions where the operating system supports them, and it shall never write access, refresh, or ID tokens to normal logs, wire-capture payloads, analytics, or exception text.
4. When an access token approaches expiry, the system shall refresh it using the profile's issued client ID and current rotating refresh token, shall include the public API resource, shall serialize refreshes for the same profile, and shall atomically replace the full token set on success.
5. If refresh confirms a terminal invalid/reused/expired refresh token or a disconnected registration, the system shall mark the profile as requiring reauthorization instead of repeatedly retrying the invalid credential.
6. When a user signs out a profile, the system shall attempt refresh-token revocation through the OIDC-discovered revocation endpoint, stop using the token set, and clear local tokens while retaining only the non-secret registration/identity metadata required for later reauthorization.
7. The system shall not treat existing `.codex/auth.json` credentials or legacy `openai-codex` managed-account files as valid SIWC profiles; migration to the new connector shall require a fresh official authorization or an explicitly supported SIWC profile import.
8. Where a self-hosted/remote AIProxer instance cannot receive a local browser loopback callback, the system shall document and support importing a protected SIWC profile created through the documented local authorization workflow without copying a different host's host ID over the target host identity.

### Requirement 4: Public Account-Specific Model Discovery
**Objective:** As a harness user, I want the available model list to come from the selected ChatGPT account, so that AIProxer exposes models that the official plan flow actually advertises.

#### Acceptance Criteria
1. When a ChatGPT-plan profile becomes active, the system shall discover its models with that profile's bearer token through `GET https://api.openai.com/v1/models`.
2. When presenting routable/display models, the system shall preserve the server-provided model slug and ordering and shall honor the documented visibility metadata for listable models.
3. When the active profile changes or is reauthorized, the system shall invalidate/refresh that profile's discovered catalog rather than reuse another profile's model entitlement view.
4. If model discovery fails transiently, the system shall surface the profile/catalog as temporarily unavailable without falling back to a private Codex catalog, a bundled Codex snapshot, or Codex client-version gating.
5. The new connector shall not require the Codex CLI executable or an npm/GitHub Codex client-version lookup for model discovery.

### Requirement 5: Harness Instruction and Tool Fidelity Without Codex Prompt Emulation
**Objective:** As a third-party harness user, I want the harness's own instructions and tools to reach the model faithfully, so that AIProxer does not alter the agent by pretending it is Codex.

#### Acceptance Criteria
1. When a request contains a frontend/system instruction, the new connector shall represent that instruction using SIWC-supported Responses semantics (`instructions` and/or `developer` messages) without prepending, replacing, or merging in the bundled Codex system prompt.
2. When explicit `system` message items would otherwise reach the SIWC Responses endpoint, the system shall transform them deterministically into a supported high-priority representation while preserving their text and relative intent; it shall never send an explicit Responses `role: "system"` item upstream.
3. When a request already contains valid `developer` messages or Responses `instructions`, the system shall preserve their content and precedence without wrapping them in `<user_instructions>` or another Codex-specific markup envelope.
4. When a client supplies function/custom tools, the system shall preserve supported tool names, schemas, tool choices, call IDs, arguments, results, and ordering through the existing protocol/domain translation layer and shall not inject Codex built-in tools.
5. When a frontend cannot be translated faithfully to supported SIWC tool or instruction semantics, the system shall return a typed actionable provider-limitation error rather than applying a hidden client-family prompt/tool workaround.
6. The new connector shall not contain per-harness Kilo, Droid, OpenCode, Pi, Letta Code, or similar client-family adapters; any generally required frontend compatibility fix discovered during implementation shall be made in the appropriate generic frontend/domain translation boundary and covered independently of the ChatGPT-plan connector.
7. When wire capture is enabled for a successful request, inspection shall be able to demonstrate that no bundled Codex prompt, `<user_instructions>` wrapper, `codex_cli_rs` identity, or Codex-only tool injection was added by the new connector.

### Requirement 6: SIWC Responses Request Policy
**Objective:** As an operator, I want every plan-funded request to conform to the documented SIWC preview contract, so that unsupported private or standard-API assumptions do not cause hidden failures.

#### Acceptance Criteria
1. When the new connector sends inference over HTTP, the system shall POST only to `https://api.openai.com/v1/responses` with the selected profile's OAuth bearer token.
2. For every upstream SIWC HTTP inference request, the system shall set `store` to `false` and `stream` to `true`, regardless of whether the downstream client requested a streaming or non-streaming response.
3. If the downstream client requested a non-streaming response, the system shall consume the upstream stream through its terminal event, assemble the canonical result, and return the expected non-streaming frontend response.
4. The system shall not send SIWC-preview fields documented as unsupported, including `background`, `conversation`, `max_output_tokens`, `max_tool_calls`, `metadata`, `moderation`, `multi_agent`, `prompt`, `prompt_cache_retention`, `safety_identifier`, `temperature`, `top_logprobs`, `top_p`, `truncation`, or `user` while those fields remain unsupported by the authoritative contract.
5. When an unsupported field is merely a translator-generated default or unset compatibility artifact, the system shall omit it; when a client explicitly requests semantics that cannot be represented without changing behavior, the system shall reject the request with a field-specific provider-limitation error rather than silently pretending the feature was honored.
6. The system shall not send HTTP `previous_response_id` upstream; it shall send the context required for the turn in the Responses `input` array using the existing proxy/session replay responsibilities.
7. When the selected model supports them under the SIWC contract, the system shall preserve supported text, image, and file inputs and supported function/custom tools; it shall reject unsupported hosted tools/capabilities instead of routing them through Codex app-server as a fallback.
8. The system shall not add legacy Codex request headers such as `OpenAI-Beta: responses=experimental`, Codex `version`, `originator: codex_cli_rs`, `Codex-Task-Type`, `chatgpt-account-id`, Codex `conversation_id`, or Codex `session_id` headers to the direct SIWC request.

### Requirement 7: Stream Completion, Errors, and Retry Semantics
**Objective:** As a harness user, I want reliable completion and actionable failures, so that plan limits and credential problems do not masquerade as successful turns.

#### Acceptance Criteria
1. When consuming an SIWC response stream, the system shall treat the request as successfully completed only after receiving `response.completed`.
2. When the stream emits `response.failed`, `response.incomplete`, an explicit error event, or terminates before a successful terminal event, the system shall return/emit a failure consistent with the selected frontend and shall not synthesize a successful response.
3. When OpenAI returns `subscription_sharing_usage_limit_exceeded`, `subscription_sharing_usage_unavailable`, `subscription_sharing_unsupported_capability`, `subscription_sharing_route_not_supported`, `subscription_sharing_invalid_user`, authorization-context errors, or equivalent documented SIWC codes, the system shall preserve the machine-readable cause and map it to an actionable AIProxer error/recovery category.
4. If a request fails because the access token is expired and the refresh token remains valid, the system shall perform at most one serialized credential-refresh recovery for that request and retry only after the token set is safely replaced.
5. If a request is invalid because of unsupported input/capability semantics, the system shall not retry the same body.
6. If a request fails because the user's ChatGPT plan allowance is exhausted, the system shall not automatically rotate to another saved subscriber profile; profile/account switching shall remain an explicit operator/user decision.
7. When OpenAI indicates a transient user/workspace or infrastructure condition, the system shall apply bounded retry/backoff consistent with existing resilience policy without erasing valid credentials.
8. The system shall preserve usage metadata, correlation identifiers, structured logging, and wire-capture integration available from the public Responses path while redacting all OAuth secrets.

### Requirement 8: Profile Selection, Access Mode, Routing, and Core Integration
**Objective:** As an AIProxer operator, I want the new connector to fit existing routing and security boundaries, so that it behaves like a first-class backend rather than a special-case tunnel.

#### Acceptance Criteria
1. The system shall allow multiple saved ChatGPT-plan profiles and shall provide an explicit way to list, select, add, reauthorize, sign out, and import eligible profiles.
2. When a backend instance is pinned to a profile, requests through that instance shall use only that profile unless the operator explicitly changes the binding.
3. The system shall not use round-robin or quota-driven profile rotation as the default account-selection behavior for the new connector.
4. When AIProxer runs in a mode that prohibits personal OAuth backends, the new connector shall be unavailable through capability-driven access-mode policy without adding a new backend-name string to personal/OAuth classification tables.
5. When resilience, health, or circuit-breaker state is scoped for the new connector, the system shall preserve user/profile isolation using generic personal-backend capabilities rather than a `codex` name heuristic.
6. When model routing enumerates the new backend, the system shall use the active profile's public model catalog and shall not depend on `CodexModelCatalogStage` or a Codex-specific configured-model enumerator.
7. The new connector shall participate in existing Chat Completions and Responses frontend routing, cancellation, usage accounting, wire capture, and health/lifecycle contracts without introducing a new application initialization stage.
8. The new connector shall not inherit the legacy rule that permanently disables history compaction for any session merely because a ChatGPT-plan turn occurred; any context reduction shall follow the generic correctness safeguards already applied to other public Responses backends.

### Requirement 9: Automated Conformance and Regression Coverage
**Objective:** As a maintainer, I want executable proof of the new connector's contract before live cutover, so that the old connector is not removed based only on static reasoning.

#### Acceptance Criteria
1. When the implementation test suite runs without external network access, it shall cover new-registration URL construction, callback validation, PKCE/state/nonce behavior, ID-token verification, granted-scope validation, refresh-token rotation, revocation handling, profile isolation, atomic persistence, and secret redaction with mocks/fixtures.
2. The automated suite shall cover public model discovery, account switching, model-cache invalidation, model-discovery failure, and absence of private-catalog fallback behavior.
3. The automated suite shall cover native Responses requests, translated Chat Completions requests, instruction/system/developer mapping, function/custom tool round trips, forced upstream streaming, downstream non-stream accumulation, full-context replay, cancellation, and terminal-event handling.
4. The automated suite shall assert that the new connector never emits the bundled Codex prompt, `<user_instructions>`, private `backend-api` URLs, Codex CLI originator/version headers, or Codex app-server fallback behavior.
5. The automated suite shall cover documented SIWC unsupported fields and error codes, including the distinction between omitting translator-generated unsupported defaults and rejecting explicitly requested unsupported semantics.
6. While legacy connectors still coexist, integration tests shall prove that selecting `openai-chatgpt-plan` cannot accidentally dispatch through a legacy Codex connector or consume its credential/catalog/continuation state.
7. The implementation shall keep project lint, formatting, typing, spec-state lint, focused connector tests, and the relevant cross-protocol integration suite green before the empirical human-test gate is attempted.

### Requirement 10: Mandatory Empirical Human Acceptance Gate
**Objective:** As the project owner, I want the new official flow proven against real OpenAI service behavior and real third-party harnesses, so that legacy removal happens only after practical compatibility is demonstrated.

#### Acceptance Criteria
1. Before any legacy connector removal task begins, a human tester shall complete a fresh SIWC authorization with at least one eligible real ChatGPT Plus or Pro account and confirm that ChatGPT-plan permission is granted.
2. Before legacy removal, the human tester shall confirm that public model discovery returns a usable account-specific model list and that at least one selected model completes an inference turn through `response.completed`.
3. Before legacy removal, the human tester shall exercise a real third-party harness using the Responses-oriented path (OpenCode or an equivalent supported Responses client) with its normal harness instructions and at least one client-side tool-call round trip.
4. Before legacy removal, the human tester shall exercise at least one different supported third-party harness/frontend path that requires AIProxer translation rather than native Responses passthrough, and shall verify its system/developer instructions and client-side tool calls remain functional.
5. During the empirical harness tests, the human tester shall inspect AIProxer wire evidence and confirm that the upstream request uses `api.openai.com/v1/responses`, `store:false`, `stream:true`, the harness's own high-priority instructions, and no Codex prompt/wrapper/private headers.
6. Before legacy removal, the human tester shall restart AIProxer and confirm that the selected SIWC profile can be reused without re-registering a new OAuth client, and shall exercise the token-refresh path with a controlled near-expiry/forced-refresh scenario.
7. If any empirical gate item fails, the implementation shall repair the new connector and repeat the failed gate; it shall not delete or disable the legacy connectors merely because automated tests pass.
8. Legacy removal shall begin only after the implementation work records explicit human sign-off that all mandatory gate items passed.

### Requirement 11: Complete Legacy Codex Connector Removal After Gate
**Objective:** As a maintainer, I want the obsolete private-protocol stack removed after successful migration, so that the codebase does not carry two competing implementations or future maintenance traps.

#### Acceptance Criteria
1. When the empirical gate is approved, the implementation shall remove the `openai-codex`, `openai-codex-v2`, and `openai-codex-app-server` backend registrations and shall not retain them as aliases to `openai-chatgpt-plan`.
2. After cutover, the implementation shall remove the legacy Codex connector modules/packages, including private HTTP/WebSocket execution, continuation state, prompt resolution, client-family adapters, Codex tool-schema/tool-execution shims, managed Codex OAuth flow/storage/selector logic, quota-header rotation/notification logic, early-session verbosity bump, and GPT-5.5 legacy account compatibility logic where they are not used by another live backend.
3. After cutover, the implementation shall remove the bundled Codex system prompt and shipped/private Codex model-catalog resources plus the Codex model-catalog startup stage, provider/discovery/client-version machinery, Codex-specific model enumerators, and catalog maintenance scripts where they have no remaining live consumer.
4. After cutover, the implementation shall remove the legacy Codex app-server connector, its backend-instance configuration, Codex-specific app-server wiring/tests, and any Codex-only helper code that has no remaining live consumer.
5. After cutover, the implementation shall remove legacy Codex schemas, backend examples, environment variables, CLI flags, account-management/catalog scripts, warmup hooks, resilience hardcodes, OAuth-detector entries, URI-parameter special cases, context-compaction exceptions, and other live core branches that exist solely for the removed connector family.
6. After cutover, the implementation shall delete or rewrite legacy Codex behavior tests and diagnostics so the active test suite validates the new official connector rather than the removed private protocol.
7. Historical archived Kiro specs and Git history may retain legacy names for provenance, but active source, configuration, scripts, tests, and user-facing documentation shall not depend on the removed backend identifiers or private Codex endpoints.
8. The implementation shall not leave a legacy fallback path that reads `.codex/auth.json`, calls `chatgpt.com/backend-api/codex/*`, injects the Codex prompt, launches Codex app-server, or silently maps old backend IDs to the new connector.

### Requirement 12: Final Migration Documentation and No-Follow-Up Completion
**Objective:** As an operator and future maintainer, I want one complete migration with accurate guidance and a clean residual state, so that no hidden second-stage cleanup is required.

#### Acceptance Criteria
1. When the new connector is introduced, the system documentation and example configuration shall describe `openai-chatgpt-plan`, the official SIWC authorization/profile workflow, supported frontend behavior, current SIWC limitations, account switching, model discovery, token refresh, logout/revocation, and self-hosted profile import.
2. When the legacy connectors are removed, the migration documentation shall state that existing `openai-codex*` configuration and credentials are not compatible with the new backend, identify the replacement backend ID, and require a fresh SIWC authorization.
3. The user guide shall clearly distinguish ChatGPT-plan OAuth usage from normal OpenAI API-key `openai`/`openai-responses` backends, which shall remain supported and unaffected.
4. The implementation shall update configuration schemas, sample environment/config files, routing/model documentation, access-mode documentation, CLI help, and backend overview/index pages in the same migration.
5. Before declaring implementation complete, the implementation shall run a residual-reference audit over non-archived source/config/docs/scripts/tests and reconcile every legacy reference as removed, migrated, or intentionally historical/documentary with an explicit allowlist rationale.
6. Before declaring implementation complete, the implementation shall verify that no active startup stage, registry entry, model enumerator, service branch, or optional resource imports removed Codex modules at runtime.
7. The implementation shall complete all cleanup, migration, verification, and documentation work defined in this specification and shall not defer required legacy removal or compatibility cleanup to another Kiro specification.

## Non-Functional Requirements

### NFR 1: Security
- SIWC secrets must remain process-local/protected-at-rest and must be redacted from logs, diagnostics, errors, and wire captures.
- OIDC identity validation must be cryptographic; unverified JWT claim extraction is not sufficient for profile activation.
- Refresh-token rotation must be serialized per profile and persisted atomically.
- New authorization must use fresh state, nonce, and PKCE values for every attempt.

### NFR 2: Reliability
- A transient OpenAI auth/model/inference failure must not corrupt a previously valid saved profile.
- Non-streaming frontend requests must not be reported as successful unless the forced upstream stream reaches `response.completed`.
- Profile switching must never mix tokens, model catalogs, or identity metadata between registrations.

### NFR 3: Maintainability
- The new connector must be smaller and materially simpler than the removed Codex connector family and must not recreate Codex-specific client-family compatibility layers under new names.
- Provider-specific SIWC policy must stay at the connector/provider boundary; generic frontend semantics discovered during migration belong in generic translators/projectors.
- Core runtime classification must use capability contracts rather than adding new `openai-chatgpt-plan` string branches where a generic capability is appropriate.

### NFR 4: Performance
- The new connector shall not add a per-request model-catalog lookup; model discovery is profile lifecycle/cache work.
- Token refresh shall occur only when required by expiry/recovery policy, not before every request.
- Streaming translation/accumulation shall reuse existing Responses streaming machinery where possible and avoid buffering downstream streaming requests.

### NFR 5: Compatibility Boundary
- The migration is intentionally breaking for the three legacy Codex backend IDs after the empirical gate.
- No legacy alias, private endpoint, prompt emulation, or credential fallback is required after cutover.
- Standard OpenAI API-key backends and unrelated provider connectors remain outside the migration and must not regress.

## Glossary

| Term | Definition |
|---|---|
| SIWC | OpenAI Sign in with ChatGPT, including optional ChatGPT plan usage for eligible apps. |
| ChatGPT-plan profile | One validated ChatGPT account/workspace registration with an issued OAuth client ID and token set. |
| Host ID | Stable opaque `ext_agent_host_id` identifying one AIProxer host installation. |
| Issued client ID | Dynamic OAuth client ID returned by OpenAI for a ChatGPT account/workspace registration. |
| Legacy Codex family | `openai-codex`, `openai-codex-v2`, and `openai-codex-app-server` plus their supporting private-protocol implementation. |
| Empirical gate | Mandatory human validation against the real OpenAI service and real third-party harnesses before legacy deletion. |
