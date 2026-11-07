# Requirements Document

## Introduction

This specification replaces the Go implementation's private ChatGPT Codex HTTP integration with the official OpenAI **Sign in with ChatGPT (SIWC) / ChatGPT plan usage** flow announced in September 2026.

The current Go implementation is an independently packaged executable backend plugin under `connectors/codex/`, exports `openai-codex` (inference) and `openai-codex-app-server` (agent runtime), translates canonical `pkg/aipapi` calls across the backend-plugin ABI, and owns private ChatGPT/Codex OAuth compatibility, private headers, WebSocket state, Codex model-catalog discovery, session heuristics, and native Codex reasoning/compaction state.

The replacement MUST preserve AIProxer's architecture: frontends decode to canonical `aipapi`, core remains provider-neutral, optional providers remain executable connectors behind the versioned backend-plugin ABI, streaming remains the primary execution model, and provider OAuth state remains outside `pkg/aipapi` and `internal/core`.

Migration is staged. The official direct-inference connector is empirically proven while existing Codex factories remain independently usable. Only after explicit human live validation succeeds may the legacy private Codex inference factory and its obsolete support code be removed. The existing Codex app-server and code it still needs remain supported; its replacement is deferred to #1633.

## Boundary Context

- **In scope**: a new external SIWC connector; host/profile identity; OAuth registration/refresh/logout; public `/v1/models`; public `/v1/responses`; SIWC request policy; the minimal OpenAI Responses hosted-declaration carriage needed for hosted web search; canonical event mapping; coexistence; empirical validation; legacy direct-inference removal and safe cleanup.
- **Out of scope**: new frontend protocols; generic hosted-tool support, `namespace`/tool-grouping, or a frontend protocol redesign beyond that minimal carriage; OpenAI-specific canonical/core types; paid/remotely hosted token-sharing product support; private ChatGPT endpoints; emulation of unsupported hosted tools; preservation of private Codex `/responses/compact`.
- **Adjacent expectations**: routing, output commitment, reasoning preservation, accounting, extension admission, and connector-host contracts are revalidated, not redesigned without evidence.
- **Boundary ownership**: the frontend owns wire preservation of the hosted web-search declaration identity; the existing generic opaque-extension admission owns cross-backend capability gating; the SIWC connector owns omission/strict policy; routing/commitment/canonical events/accounting stay in existing core/SDK contracts.
- **Declaration carriage boundary**: the end-to-end slice preserves the documented hosted web-search declaration types as a bounded opaque extension admitted only by exact backend support. It does not add a canonical hosted-tool field/type, change generic core, or let a non-supporting backend serve the request with the declaration silently absent.
- **Revalidation triggers**: SIWC endpoints/scopes, Responses preview restrictions, hosted web-search declaration types, opaque-extension admission/capability contracts, backend-plugin ABI, canonical reasoning/tool semantics, local-only credential security, Codex app-server RPC.

## Requirements

### Requirement 1: Independent Official ChatGPT-Plan Connector
**Objective:** As a maintainer, I want a new connector implemented independently of the private Codex HTTP stack, so that the migration simplifies architecture rather than preserving obsolete compatibility machinery.

#### Acceptance Criteria
1. When the new connector is built, it shall be an independently buildable executable backend connector module communicating with the host only through the versioned backend-plugin ABI.
2. The direct-inference factory shall use a new backend kind distinct from `openai-codex`, provisionally `openai-chatgpt-plan`.
3. The new connector shall not import `connectors/codex/internal/...`, root `internal/...`, or provider-private implementation packages.
4. The root module shall not gain a mandatory dependency on the new connector module.
5. The standard distribution shall discover the connector through existing trusted manifest/release metadata.
6. While the new connector is under validation, `openai-codex` and `openai-codex-app-server` shall remain independently usable and shall not redirect internally to the new implementation.
7. The direct factory shall advertise inference execution class, dynamic inventory, streaming/tool capabilities supported by SIWC, the exact hosted-declaration extension it consumes, and local/personal-auth scope; the runtime resolved profile shall declare the opaque-extension capability and exact hosted-declaration extension, with the static descriptor capability mirrored.
8. The direct factory's packaged manifest export shall declare `access_scope: local_only` and `credential_mode: oauth_user`: the effective backend credential is the selected user's SIWC OAuth profile, so the generic `oauth_user => local_only` manifest rule applies.
9. The connector shall not implement any connector-local single-user / server-mode check. Multi-user rejection is provided once by the generic host backend security-profile gate, which runs before backend construction, connector `Configure`, and any child-process launch.
10. Absence from the host-owned multi-user approval registry shall keep the direct factory denied in `access.mode: multi_user`. The connector shall not attempt to appear in, or override, that registry.

### Requirement 2: Official SIWC Registration and Profile Lifecycle
**Objective:** As a local operator, I want the connector to perform OpenAI's documented Sign in with ChatGPT flow, so that plan usage is authorized without impersonating Codex CLI.

#### Acceptance Criteria
1. First registration shall use `client_id=dynamic_agent_client`, a stable per-host `ext_agent_host_id`, the product's actual stable agent name, fresh `state`, fresh OIDC `nonce`, and PKCE S256.
2. Successful first registration shall persist the issued `client_id`; `dynamic_agent_client` shall never be persisted or used as the issued client id for token exchange.
3. Authorization shall request the documented identity/offline/resource/direct-plan permissions including `chatgpt.tokens.use.direct` for resource `https://api.openai.com/v1`.
4. The loopback callback shall validate `state` before code exchange and the pending authorization attempt shall retain the exact selected `redirect_uri`, including scheme, loopback host, port, and path.
5. Authorization-code exchange shall send that exact same `redirect_uri` value byte-for-byte with the issued client id, authorization code, `code_verifier`, and resource; the implementation shall not reconstruct or substitute the callback URI for exchange. Code exchange and refresh shall use the documented `auth.openai.com/api/accounts/...` endpoints and no client secret.
6. ID tokens shall be validated against OpenAI JWKS for signature, issuer, audience, expiry, nonce and stable subject.
7. The granted scopes shall be validated for direct plan usage before models or inference are called.
8. Returning authorization shall reuse the saved issued client id and confirm the authenticated subject matches the selected profile before replacing credentials.
9. Refresh shall be serialized per profile and replacement refresh tokens shall be persisted atomically.
10. Terminal refresh failures shall require reauthorization; transient network/5xx failures shall not erase otherwise valid credentials.
11. Stored profile state shall distinguish local profile id/label, validated subject, issued client id, host id, granted scopes, access token, refresh token, expiry, optional retained ID token/login hints, and reauthorization state; email shall not be the authoritative key.
12. Tokens, retained ID-token hints, and OpenAI encrypted auth metadata shall be absent from normal logs, analytics, diagnostics, captures and support-facing errors.
13. Multiple saved registrations may be listed/selected explicitly, but the connector shall not pool plan allowances or rotate automatically to another subscriber because one profile exhausted quota.
14. Logout shall first attempt to end the renewable session through the `revocation_endpoint` discovered from OpenAI's OIDC configuration, using a form-encoded POST containing the saved refresh token, `token_type_hint=refresh_token`, and that registration's issued client id. Network/5xx revocation failures shall be retried with bounded backoff while the refresh token remains available. If sign-out completes locally without confirmed remote revocation, the connector shall clear local tokens, report that remote revocation was not confirmed, and direct the user to disconnect the app in ChatGPT Settings; confirmed remote revocation and local-only completion shall remain distinguishable outcomes.

### Requirement 3: Stable Agent Host Identity
**Objective:** As a user, I want one stable OpenAI agent-host identity per installation/host, so that SIWC attribution is correct across restarts.

#### Acceptance Criteria
1. Before first SIWC authorization, the connector shall create or load a stable opaque `ext_agent_host_id`.
2. Restarting AIProxer or the connector process shall reuse the same host id.
3. The host id shall use an OpenAI-supported opaque format, preferring an RFC 9278 JWK-thumbprint URI when practical, otherwise a persisted `urn:uuid:` value.
4. It shall not encode email, account id, workspace name or other user identity.
5. Importing a profile to another host shall not overwrite the destination host's own host id.
6. Host-id creation/storage shall be protected, atomic and race-safe.

### Requirement 4: Public Responses API Transport
**Objective:** As an AIProxer client, I want plan inference to use the public Responses API, so that private ChatGPT/Codex transport contracts disappear.

#### Acceptance Criteria
1. Direct inference shall default exclusively to `POST https://api.openai.com/v1/responses`, never `chatgpt.com/backend-api/codex/...`.
2. The selected profile access token shall be sent as ordinary bearer authorization only to the documented public resource.
3. Every HTTP inference request shall use `store:false` and `stream:true`, regardless of downstream delivery mode.
4. The connector shall consume the upstream stream through a terminal event and treat only `response.completed` as success.
5. `response.failed`, `response.incomplete`, stream errors, cancellation, interruption, and EOF before terminal completion shall remain distinguishable failure outcomes.
6. Upstream events shall become canonical `aipapi` events through the existing streaming-first backend-plugin Execute path; downstream non-streaming remains collection over that path.
7. After client-visible output commits an attempt, the connector/host shall not transparently restart it on another profile or provider transport.
8. The connector shall not send Codex-private headers including `originator: codex_cli_rs`, `version`, `Codex-Task-Type`, `conversation_id`, `session_id`, `chatgpt-account-id`, or Codex beta headers.
9. Any user-agent/application identification shall truthfully identify AIProxer rather than Codex CLI.

### Requirement 5: Native Harness Instructions Without Codex Prompt Emulation
**Objective:** As a third-party coding harness, I want my own instructions preserved normally, so that AIProxer no longer imposes Codex identity semantics.

#### Acceptance Criteria
1. The new connector shall not define, load, prepend, merge or fall back to the legacy `You are Codex...` identity instruction or an equivalent Codex-CLI prompt.
2. Canonical `Call.Instructions` shall map to Responses `instructions` when present.
3. Explicit system-role conversation items shall not be emitted as SIWC `input` message items.
4. Necessary system-role conversation text shall be merged deterministically into `instructions` or another documented legal developer-instruction representation without altering user/assistant/tool chronology.
5. Legal developer-role input items may remain developer messages when that preserves semantics better.
6. No `<user_instructions>` wrapper or other Codex prompt glue shall be injected.
7. Arbitrary harness instructions shall work without client-family detection as a correctness prerequisite.
8. Live validation shall prove two materially different coding harnesses work without the legacy Codex identity prompt appearing upstream.

### Requirement 6: SIWC Preview Request Policy
**Objective:** As an operator, I want unsupported SIWC features handled deterministically, so that client variability cannot cause undefined/private behavior.

#### Acceptance Criteria
1. Before provider execution, one connector-owned policy component shall enforce the current documented SIWC contract and either produce a legal request or a stable local/provider-limitation error.
2. The connector shall not forward unsupported top-level fields including `background`, `conversation`, `max_output_tokens`, `max_tool_calls`, `metadata`, `moderation`, `multi_agent`, `prompt`, `prompt_cache_retention`, `safety_identifier`, `temperature`, `top_logprobs`, `top_p`, `truncation`, and `user`.
3. HTTP requests shall omit `previous_response_id` and carry required history in `input`.
4. When an OpenAI Responses request carries a hosted web-search tool declaration, the OpenAI Responses frontend shall preserve the declaration's documented type for backend admission rather than reject or flatten it into a function/custom tool declaration, and the SIWC connector shall omit the declaration from the provider request and record an operator-visible omission under the default interoperable posture of the operator-configurable policy, or reject the request before provider execution under the operator-selected strict posture; a backend that does not explicitly support that preserved declaration shall reject the request before provider execution rather than silently ignore it, only the SIWC connector shall apply the omission, and no wire fast path may execute a request carrying that declaration before canonical admission/policy. The connector shall never silently drop, fabricate, or reorder model-authored web-search history items or events, and shall reject before provider execution any request whose web-search history it cannot preserve exactly. Every other unsupported hosted tool (image generation, file search, Code Interpreter, native computer use, hosted MCP/connectors, Responses `tool_search`, and top-level `programmatic_tool_calling`) shall be rejected before provider execution under both postures.
5. Function/custom tools shall remain supported with stable call/result identifiers through canonical events; a function or custom tool named `web_search` shall remain a caller-executed tool and shall not be placed in the hosted-declaration extension.
6. Text, supported images and supported file input shall remain available when model/canonical representation permits.
7. Unsupported audio/video/upload/transcription assumptions shall fail explicitly rather than route or degrade silently.
8. SIWC policy and the hosted-declaration carriage shall remain provider-local; no OpenAI-specific field/switch shall be added to `pkg/aipapi` or generic core. The carriage shall use the existing opaque-extension admission seam, and generic core shall gate only on the generic opaque-extension capability and the exact extension identity.
9. The policy shall be centralized/table-driven enough that preview changes do not create scattered string branches.

### Requirement 7: Model Inventory Bound to the Selected ChatGPT Profile
**Objective:** As a user, I want model choices to reflect my active ChatGPT registration, not a bundled Codex CLI catalog.

#### Acceptance Criteria
1. Direct inventory shall use authenticated `GET https://api.openai.com/v1/models` with the same selected profile used for inference.
2. Only list-visible entries shall be surfaced and provider ordering shall be preserved unless a canonical inventory contract explicitly requires deterministic sorting.
3. Provider slug shall remain the native model id and display name shall be preserved where supported.
4. Switching profile shall invalidate or segregate cached inventory so catalogs cannot cross registration boundaries.
5. The new direct connector shall not run `codex debug models`, require a Codex executable, parse the private Codex catalog, apply Codex client-version gates, or use the old shipped snapshot.
6. Inventory failure shall not silently substitute another profile's catalog; any bounded last-known cache must be profile-local and explicitly marked.
7. Listing a model shall not be treated as entitlement proof; completed inference is authoritative for the selected request.

### Requirement 8: Error, Quota, Account and Usage Semantics
**Objective:** As an operator, I want SIWC failures represented accurately without obsolete Codex pool recovery.

#### Acceptance Criteria
1. Errors shall preserve upstream HTTP status, machine-readable code and request id when available, plus bounded/redacted diagnostic text.
2. Direct admission 401/403 shall remain distinct from stream-terminal failures.
3. `subscription_sharing_usage_limit_exceeded` and `subscription_sharing_usage_unavailable` shall terminate the selected-profile attempt and shall not rotate to another subscriber.
4. `subscription_sharing_unsupported_capability` shall be non-retryable for an unchanged body.
5. Route-not-supported, invalid-user/context, temporary-user-unavailable, revocation and refresh failures shall follow documented SIWC recovery classes, not legacy `x-codex-*` heuristics.
6. Legacy free-plan GPT downgrade, `PlanTypeHint`, and silent model substitution shall not exist in the new connector.
7. Usage from completed Responses streams shall feed existing accounting-evidence contracts without manufacturing API-dollar charges for subscription-funded usage.
8. Diagnostics shall identify selected profile only through a non-secret local key/label.

### Requirement 9: Official SIWC Codex App-Server Mode — Deferred
**Deferred to [#1633](https://github.com/aiproxer/aiproxer-enterprise/issues/1633), milestone `Splitted OSS / Enterprise repos`.** The following is historical handoff context, not a requirement or closeout gate for the direct-inference migration. Revalidate it before future execution; preserve the legacy app-server until a separately approved migration.
**Objective:** As an operator who explicitly wants Codex agent-runtime semantics, I want app-server to use the same official SIWC profile without another Codex login.

#### Acceptance Criteria
1. The migration shall provide a new app-server factory distinct from legacy `openai-codex-app-server`, provisionally `openai-chatgpt-plan-app-server`.
2. The child shall receive the selected SIWC access token via a dedicated environment variable such as `ACCESS_TOKEN`.
3. Launch configuration shall target public Responses at `https://api.openai.com/v1`, `wire_api="responses"`, the token env key, `requires_openai_auth=false`, and `supports_websockets=false`.
4. `initialize.params.clientInfo` shall truthfully identify this application with stable name plus title/version.
5. App-server thread ids remain agent-runtime state and shall not become canonical core conversation authority.
6. On access-token renewal, app-server shall restart with the new token and resume the saved thread only through documented RPC.
7. `model/list` may serve app-server-local catalog UX; account-specific current availability shall use public `/v1/models`.
8. This factory shall remain `agent_runtime` and preserve existing routing-composition safety restrictions.
9. This factory's packaged manifest export shall declare `access_scope: local_only` and `credential_mode: oauth_user`, because the app-server child is fed the selected user's SIWC access token under Requirement 9.2. The first-party `agent_runtime => local_only` invariant and the generic `oauth_user => local_only` manifest rule therefore both apply.
10. This factory shall not implement a connector-local single-user / server-mode check; multi-user rejection is provided by the generic host gate before backend construction and connector configuration.

### Requirement 10: Native Context, Reasoning and Compaction Migration
**Objective:** As a maintainer, I want private Codex-native context retired or explicitly revalidated, so the official connector has no undocumented dependency.

#### Acceptance Criteria
1. The new direct connector shall not call private `/responses/compact` or depend on `codex.responses.compaction.v2`.
2. HTTP correctness shall be based on required canonical history replay, not private response-id continuity/checkpoints.
3. Existing generic AIProxer context/compaction may reduce context only where its canonical semantics are valid; private Codex compaction shall not be recreated under another name.
4. Exact public Responses reasoning items/encrypted reasoning may be preserved only if deterministic and live SIWC tests prove support.
5. Failure to validate exact reasoning replay shall not block baseline connector correctness; advertised capability shall narrow honestly rather than using private fallback.
6. The new direct connector shall not carry old conversation-header affinity, private WebSocket continuation, early/mid-session verbosity turn state, or GPT downgrade state absent a new public requirement.
7. No public/core contract shall survive solely to support retired private Codex compaction after migration.

### Requirement 11: Client-Compatibility Feature Reconciliation
**Objective:** As a maintainer, I want `codex-client-compat` reduced to behavior still genuinely required, so the new connector does not inherit private-Codex prompt hacks.

#### Acceptance Criteria
1. Baseline Responses-capable coding harnesses shall work with the new backend while `codex-client-compat` is disabled.
2. The feature shall not target the new backend merely because it replaces `openai-codex`.
3. Existing `openai_codex.*` canonical extension keys shall not become part of the new connector API.
4. Harness-specific transformations still required by evidence shall remain feature-owned and use backend-neutral/capability-oriented semantics where practical.
5. Bridges whose sole purpose was Codex identity/system-prompt compensation shall be removed after legacy deletion.
6. Provider-neutral tool-history normalization shall live at the canonical/frontend/generic Responses boundary when justified, not as client-name branching in the new backend.
7. Tests shall prove OpenCode plus at least one of Pi/Droid/Hermes retains correct tool behavior after reconciliation.

### Requirement 12: Mandatory Empirical Migration Gate
**Objective:** As the project owner, I want real human evidence before deletion, so the new official route is proven in coding-agent use.

#### Acceptance Criteria
1. No legacy-removal task may begin before a human explicitly records this gate as PASS.
2. The gate shall complete fresh SIWC registration with an eligible Plus/Pro account and verify issued client id, host-id reuse, scope validation, persistence, restart and refresh.
3. It shall list models from public `/v1/models` and complete inference through public `/v1/responses`.
4. It shall test downstream streaming and downstream non-streaming while proving upstream remained `stream:true` and `store:false`.
5. It shall exercise OpenCode and at least one materially different coding harness.
6. At least one scenario shall execute a function/custom tool, return the tool result and continue to a completed response.
7. At least one multi-turn scenario shall prove required HTTP history replay without `previous_response_id`.
8. Sanitized wire evidence shall prove absence of private `backend-api` URLs, Codex impersonation headers, `chatgpt-account-id`, and default Codex identity prompt.
9. A controlled usage-limit/error scenario or faithful provider-supported equivalent shall prove there is no silent profile rotation.
10. The deferred SIWC app-server path is not a gate for this direct-inference migration; its future work order requires live validation before any app-server replacement.
11. Sign-off evidence shall record build/commit, OS, plan class, harness/version and model(s), without credentials or raw sensitive prompt content.

### Requirement 13: Legacy Codex Removal After the Gate
**Objective:** As a maintainer, I want the migration finished by this same spec, with no obsolete private stack left behind.

#### Acceptance Criteria
1. After Requirement 12 passes, `openai-codex` private HTTP factory/export and its manifest/release/config/example/docs references shall be removed.
2. Legacy `openai-codex-app-server` shall remain independently usable, including its required shared code. Replacement or removal is deferred to #1633 and requires separate approval and live evidence.
3. Private ChatGPT defaults, Codex CLI OAuth client/token behavior, `~/.codex/auth.json` discovery for the migrated backend, Codex request headers and private WebSocket transport shall be removed from supported production paths.
4. Obsolete managed-Codex account pooling, plan downgrade, quota-header/session-affinity, and verbosity-turn logic shall be removed when unreachable.
5. Old Codex model catalog/snapshot code shall be removed only when no supported factory needs it; preserve what the legacy app-server uses.
6. Private native-context/compaction state and accounting sideband shall be removed when no supported path uses them.
7. Dead tests, testkit assumptions, architecture exceptions, compatibility targeting, release metadata, examples and docs shall be removed/reconciled.
8. Repository-wide searches for private Codex endpoint strings, legacy factory ids, CLI impersonation headers and obsolete extension names shall have no unexplained production hits.
9. Root/connector dependency graphs shall be pruned of dependencies used only by the retired implementation.
10. No pooling, failover, or quota-sharing across different users' ChatGPT-plan registrations shall be introduced at any point in the supported end state. Active-profile selection is a single local user's registration, never an availability or quota mechanism.

### Requirement 14: Verification, Security and Release Quality
**Objective:** As a release maintainer, I want deterministic security and quality evidence before shipping this auth/transport migration.

#### Acceptance Criteria
1. Unit tests shall cover PKCE/state/nonce, ID-token validation, scope validation, issued-client handling, host-id persistence, profile selection, serialized refresh, token rotation persistence, quarantine and logout.
2. Unit tests shall cover request projection, unsupported-field/tool policy, hosted-declaration carriage and name non-classification, instruction mapping, tools, model listing, error classification and terminal streams.
3. Integration tests shall prove exact HTTP methods/paths/headers/bodies/SSE and absence of private Codex fields.
4. The direct migration shall preserve the existing app-server; tests of the deferred SIWC app-server belong to #1633.
5. Concurrency-sensitive profile refresh, first-start host-id creation, profile switch vs inventory refresh, and shutdown shall be covered under current repository verification policy. App-server restart belongs to #1633.
6. Fuzz/property tests shall cover callback parsing, JWT/JWKS errors, bounded error bodies, SSE parsing and policy filtering where useful.
7. Architecture tests shall prove no OpenAI-specific branch/import enters `internal/core`, no provider-private package enters public SDKs, and connector modules remain independently buildable with `GOWORK=off`.
8. Focused tests, `make quality-checks`, `make test-unit`, `make parity-checks`, relevant connector release gates and appropriate wide QA shall pass before legacy removal merges.
9. Documentation shall describe direct SIWC inference and distinguish it from the retained legacy Codex app-server, including security, preview limits, supported deployment scope and profile management; it shall not describe the deferred SIWC app-server as implemented.
10. Release notes shall explain the intentional direct route/config migration, private-inference removal and retained legacy app-server.
11. The direct factory is `oauth_user` and `local_only`, absent from the host-owned multi-user approval registry. The retained legacy app-server remains `local_only` and unapproved for multi-user; future factory posture is owned by #1633.
12. Manifest/descriptor parity tests shall prove the direct export's packaged manifest posture equals its runtime `FactoryDescriptor` `AccessScope` / `CredentialMode`; static `FactoryDescriptor.StaticCapabilities.OpaqueExtensions` and runtime `ResolvedProfile.Capabilities.OpaqueExtensions` shall both be true but shall not be treated as interchangeable, and runtime admission authority shall be the resolved profile plus the exact `ResolvedProfile.DialectSupport.ExtensionTypes` entry; the proof shall show that static descriptor capability alone does not admit a request.
13. No configuration knob shall be introduced that permits `local_only` or `oauth_user` backends in `access.mode: multi_user`. A future shared-service ChatGPT-plan backend is a separate security design with a distinct factory and an explicit host approval, not a reclassified one.

## Deferred

- SIWC-backed Codex app-server implementation, acceptance, and any later legacy app-server replacement: [#1633](https://github.com/aiproxer/aiproxer-enterprise/issues/1633), post-OSS split milestone. Retain the legacy app-server and its dependencies in this spec's end state.
- OpenAI Responses `namespace` tool declarations and `additional_tools` input items, including stable namespaced function call/result identity, remain a separate slice. This spec's hosted web-search carriage does not make Codex requests that send them work.
