# Implementation Plan

> **Execution barrier:** Phases 1–6 build and automatically verify the new connector while the legacy Codex family remains intact. **Phase 7 is a mandatory human empirical acceptance gate. Phases 8–10 MUST NOT begin until Task 7.5 records explicit human sign-off.** Automated tests cannot waive this barrier.
>
> **Brownfield rule:** Re-inspect `legacy-python-dev` at implementation time. Where adjacent active specs have already landed generic OAuth-capability or Responses plumbing, consume the landed generic API rather than duplicating it. All functional outcomes and final cleanup in this plan remain mandatory.

## Phase 1: Establish the Clean Connector Boundary

- [x] 1. Create the independent `openai-chatgpt-plan` backend and its generic integration seams
- [x] 1.1 Create the new first-party connector package and registration
  - Add `src/connectors/openai_chatgpt_plan/` with a thin `OpenAIChatGPTPlanConnector` facade built on the public OpenAI Responses transport, not on `OpenAICodexConnector`.
  - Register backend type `openai-chatgpt-plan` with vendor prefix/model routing consistent with other OpenAI backends.
  - Make module import/registration side-effect safe: no browser flow, token refresh, network call, or credential prompt during import.
  - Add a structural test that the new package does not import `openai_codex`, `_openai_codex_connector`, `_openai_codex_v2_connector`, `src.resources.codex`, or app-server modules.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5_

- [x] 1.2 Implement/consume generic personal-OAuth capability metadata instead of a new name hardcode
  - Re-check the then-current `BackendCapabilityDescriptor` and active OAuth-plugin work; if `is_oauth_based` / `requires_personal_auth` (or successor generic contract) already exists, use it.
  - Otherwise add only the generic cross-cutting capability fields and core consumers required to classify personal OAuth backends without provider-name lists.
  - Ensure multi-user/startup availability and resilience scoping can use the generic capability for the new connector.
  - Do **not** add `openai-chatgpt-plan` to `KNOWN_OAUTH_CONNECTORS`, `_PERSONAL_BACKEND_TYPES`, substring heuristics, or an equivalent static runtime discriminator.
  - Preserve defaults for backends that do not declare the new generic capabilities.
  - _Requirements: 1.6, 8.4, 8.5, 9.6_

- [x] 1.3 Add normalized connector configuration and examples
  - Define `ChatGPTPlanConfig` (or equivalent typed settings) for profile selection, profile/host storage paths, loopback callback port, and model-catalog TTL.
  - Add schema/default/example configuration using a coherent `OPENAI_CHATGPT_PLAN_*` namespace where environment overrides are appropriate.
  - Do not introduce legacy `OPENAI_CODEX_*` aliases for the new connector.
  - Keep precedence consistent with CLI > ENV > YAML > defaults.
  - _Requirements: 1.1, 8.1, 12.1, 12.3, 12.4_

## Phase 2: Implement Official SIWC Identity and Credential Lifecycle

- [x] 2. Build secure host/profile persistence and dynamic SIWC authorization
- [x] 2.1 Implement stable host state and profile storage
  - Create a schema-versioned host store containing one stable opaque `ext_agent_host_id` for the AIProxer installation.
  - Create schema-versioned per-profile files keyed by validated local profile IDs, not by email.
  - Store issued client ID, validated issuer/subject, safe display identity, granted scopes, token set, expiry metadata, and profile status.
  - Use atomic replacement and owner-only file permissions where supported; add deterministic corruption/permission error handling.
  - Reuse/add secret-redaction helpers so access, refresh and ID tokens never reach normal logs, captures or exception text.
  - _Requirements: 3.1, 3.2, 3.3_

- [x] 2.2 Implement first-time dynamic registration and loopback authorization
  - Generate fresh state, OIDC nonce, and PKCE S256 verifier/challenge for every attempt.
  - Bind callback handling to `127.0.0.1` and advertise the exact `http://127.0.0.1:<port>/auth/callback` redirect URI.
  - Start authorization with `client_id=dynamic_agent_client`, `agent_name_hint`, stable host ID, required SIWC scopes, and `resource=https://api.openai.com/v1`.
  - Require the callback's issued client ID and use it—not `dynamic_agent_client`—for the token exchange and persisted registration.
  - Exchange code with exact redirect URI, PKCE verifier, issued client ID and resource, without a client secret.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5_

- [x] 2.3 Implement cryptographic OIDC identity validation and scope gating
  - Use standards-based OIDC discovery/JWKS validation (prefer existing Authlib dependency) rather than unverified JWT claim extraction.
  - Validate signature, issuer, audience, expiration and request nonce before profile activation.
  - Pin unit tests for each validation failure independently.
  - Record granted scopes and prevent plan-funded inference when `chatgpt.tokens.use.direct` is absent while retaining safe identity/registration metadata for reauthorization.
  - Enforce profile identity consistency during reauthorization; conflicting subject/registration cannot overwrite the selected profile.
  - _Requirements: 2.6, 2.7, 2.8_

- [x] 2.4 Implement rotating token refresh, reauthorization and sign-out/revocation
  - Add per-profile refresh serialization and double-check profile state after acquiring the refresh lock.
  - Refresh with the profile's issued client ID, current refresh token and public API resource; atomically persist the complete rotated token set.
  - Mark terminal invalid/reused/expired/disconnected refresh states as `needs_reauth`; do not loop on a dead refresh token.
  - Reauthorization reuses the issued client ID and stable local host ID.
  - On sign-out, attempt revocation through the OIDC-discovered revocation endpoint, then clear local bearer/token material while preserving only safe registration metadata required for later reauthorization.
  - _Requirements: 3.4, 3.5, 3.6_

- [x] 2.5 Implement explicit profile selection and protected import/export
  - Support multiple saved profiles, including profiles with the same email, without merging them.
  - Resolve profile selection from explicit request/backend instance/config/default semantics and fail clearly when selection is ambiguous.
  - Do not automatically rotate to another profile on plan quota exhaustion or authentication failure.
  - Add explicit secret-bearing profile export/import for remote/self-hosted operation; import must never replace the target host's `ext_agent_host_id` with a source host ID.
  - Never auto-import `.codex/auth.json` or old `openai-codex` managed-account files as SIWC profiles.
  - _Requirements: 3.7, 3.8, 8.1, 8.2, 8.3_

## Phase 3: Replace Private Catalog Discovery With Public Per-Profile Models

- [x] 3. Implement account-specific public model discovery
- [x] 3.1 Add a per-profile `/v1/models` catalog service
  - Authenticate with the selected SIWC profile and call `GET https://api.openai.com/v1/models`.
  - Preserve provider model slugs/order and documented list visibility.
  - Cache by non-secret profile registration identity and invalidate on profile switch, reauthorization, deletion or explicit refresh.
  - Treat transient discovery failure as temporary catalog unavailability; do not load private Codex endpoint results, bundled snapshots, or Codex client-version fallbacks.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5_

- [x] 3.2 Integrate the new catalog with generic routing/model enumeration
  - Expose account-specific models through the current generic backend/model capability discovery seam.
  - Avoid adding a new provider-specific application initialization stage.
  - Ensure model-only and explicit `openai-chatgpt-plan:<model>` selectors behave according to the project's routing conventions without leaking another profile's catalog.
  - Add catalog/routing tests for multiple profiles and stale-cache invalidation.
  - _Requirements: 4.1, 4.3, 8.6_

## Phase 4: Implement SIWC Request Projection Without Codex Prompt Emulation

- [ ] 4. Build a strict SIWC Responses request-policy layer
- [x] 4.1 Preserve native harness instructions using supported Responses semantics
  - Preserve native top-level `instructions` without injecting a Codex default prompt.
  - Project canonical high-priority system instructions to top-level `instructions` when not already represented.
  - Rewrite residual explicit `role=system` input/message items to ordered `developer` items only where needed to preserve semantics because SIWC currently rejects explicit system input items.
  - Preserve existing developer messages and prevent duplicate instruction injection using request provenance, not text-equality heuristics.
  - Add negative tests proving the outbound payload contains no bundled Codex prompt, `<user_instructions>`, Codex environment context, or client-family prompt wrapper.
  - _Requirements: 5.1, 5.2, 5.3, 5.6_

- [x] 4.2 Preserve standard client-side tool and input semantics without client-family adapters
  - Preserve function/custom tool schemas, `tool_choice`, tool call IDs, arguments, and function-call-output linkage where supported by current SIWC documentation.
  - Preserve supported text/image/file input content according to model/provider constraints.
  - Do not add OpenCode/Kilo/Droid/`pi`/Letta client detection, XML tool parsers, proxy-side Codex tool execution, or hidden built-in Codex tools.
  - If a defect is generic to Responses translation, repair it in the generic translator/projector with generic tests rather than adding a SIWC harness workaround.
  - _Requirements: 5.4, 5.5, 6.6, 9.3_

- [x] 4.3 Enforce the SIWC preview field/capability contract
  - Re-check OpenAI's official SIWC limitations at implementation time and encode the then-current support matrix in connector policy tests.
  - Force upstream `store=false` and `stream=true` for every HTTP inference request.
  - Ensure HTTP `previous_response_id` is not sent upstream; rely on AIProxer's resolved/full input replay.
  - Reject explicitly requested unsupported fields/tools with a typed provider-limitation/validation error naming the unsupported capability.
  - Strip only incidental proxy-generated null/default fields that have no client-requested semantic value.
  - Never work around an unsupported hosted tool by calling ChatGPT private endpoints.
  - _Requirements: 6.2, 6.3, 6.4, 6.5, 6.6_

- [ ] 4.4 Prove the outbound request/header boundary is public Responses only
  - Target `POST https://api.openai.com/v1/responses` with the selected SIWC bearer token.
  - Reuse normal public OpenAI headers/correlation behavior only.
  - Add structural/wire tests forbidding `originator=codex_cli_rs`, Codex `version`, `Codex-Task-Type`, `chatgpt-account-id`, legacy conversation/session identity headers, and `chatgpt.com/backend-api/codex` URLs.
  - _Requirements: 1.3, 5.7, 6.1, 6.7, 6.8, 9.4_

## Phase 5: Execute Through Shared Responses Streaming and Normalize SIWC Failures

- [ ] 5. Complete inference execution, terminal semantics and recovery
- [ ] 5.1 Reuse the shared Responses transport for forced upstream streaming
  - Delegate the projected payload to the existing public Responses HTTP/SSE transport rather than implementing a Codex-style executor.
  - Preserve downstream streaming for streaming clients without unnecessary buffering.
  - For downstream non-streaming requests, consume the mandatory upstream stream through the existing accumulator and emit success only after `response.completed`.
  - Treat `response.failed` and `response.incomplete` as terminal non-success outcomes and preserve safe provider details.
  - _Requirements: 6.2, 7.1, 7.2, 8.7_

- [ ] 5.2 Implement SIWC auth/subscription/capability error mapping and bounded retry
  - Map documented subscription-sharing/authorization/capability error codes into actionable typed AIProxer errors while preserving safe upstream codes.
  - On access-token auth failure, perform at most one appropriate refresh-and-retry; if refresh is terminally invalid, mark the selected profile `needs_reauth`.
  - Do not retry the same invalid request body after a provider capability/validation rejection.
  - Do not rotate to another profile on `subscription_sharing_usage_limit_exceeded` or equivalent plan exhaustion.
  - Keep ordinary network/5xx retry behavior bounded and compatible with the generic resilience layer.
  - _Requirements: 7.3, 7.4, 7.5, 7.6, 7.7_

- [ ] 5.3 Preserve cancellation, usage accounting, capture, correlation and redaction (P)
  - Ensure public Responses cancellation/stream cleanup still works for both downstream streaming and accumulated non-streaming calls.
  - Preserve request/session/capture attribution and normalized usage metadata.
  - Add safe `profile_id`/identity-fingerprint diagnostics without logging bearer/refresh/ID token values.
  - Add capture tests proving authorization/OIDC/token secrets are redacted.
  - _Requirements: 3.3, 7.8, 8.7, 9.5_

## Phase 6: Operator UX, Automated Conformance and Coexistence Proof

- [ ] 6. Make the new connector operationally usable and prove it automatically before real-service testing
- [ ] 6.1 Add profile-management CLI/script and user-safe diagnostics (P)
  - Implement list/add/show/reauthorize/refresh/signout/remove plus protected import/export operations described in `design.md`.
  - Ensure list/show never print access, refresh, ID tokens, authorization codes or PKCE verifiers.
  - Provide actionable states for missing plan scope, needs reauth, signed out, model discovery unavailable and ambiguous profile selection.
  - _Requirements: 2.7, 3.5, 3.6, 3.8, 8.1, 12.1_

- [ ] 6.2 Add complete unit and contract coverage for SIWC lifecycle and request policy
  - Mock OAuth authorize/token endpoints, OIDC discovery/JWKS/revocation, public `/models`, and Responses SSE deterministically.
  - Cover dynamic client registration, ID-token validation failures, scope gating, refresh rotation/races, import/host identity invariants, model isolation, instruction projection, unsupported fields, forced upstream streaming, terminal stream states and error mapping.
  - Add structural tests proving the new connector has no legacy Codex package/resource/private-endpoint dependency.
  - _Requirements: 9.1, 9.2, 9.4, 9.5_

- [ ] 6.3 Add cross-frontend integration tests and coexistence tests
  - Native Responses frontend: instructions + tool call/output round trip.
  - OpenAI Chat Completions frontend: system/developer/tool semantics projected legally to SIWC Responses.
  - At least one other supported translated frontend path: verify no client-family special casing is required.
  - Exercise downstream `stream=true` and `stream=false`; assert the upstream request remains `stream=true` in both cases.
  - Run new and old backends in the same test process and prove profile/catalog/continuation state does not cross between them.
  - _Requirements: 1.2, 5.1, 5.4, 5.5, 6.2, 9.3, 9.6_

- [ ] 6.4 Document the new connector before empirical testing
  - Add operator docs for authorization, profiles, current SIWC limitations, model discovery, refresh/reauth/sign-out, profile switching, remote import/export and troubleshooting.
  - Clearly distinguish `openai-chatgpt-plan` from API-key `openai`/`openai-responses` and from the still-temporarily-available legacy Codex backends.
  - Mark legacy Codex usage as pending retirement after successful acceptance, but do not remove it yet.
  - _Requirements: 12.1, 12.3, 12.4_

- [ ] 6.5 Run the pre-gate automated verification suite and repair all regressions
  - Run focused new connector tests, Responses frontend/integration tests, routing/model discovery, access-mode/resilience tests, lint, format and type checks.
  - Confirm the new connector uses only public OpenAI API inference/model endpoints under mocked boundary capture.
  - Do not proceed to the human gate with known connector, typing, lint or relevant integration failures.
  - _Requirements: 9.7, 10.7_

## Phase 7: Mandatory Human Empirical Acceptance Barrier

- [ ] 7. Empirically validate the official flow against a real eligible account and real harnesses
- [ ] 7.1 Complete fresh real SIWC authorization and public model/inference proof
  - A human tester authorizes at least one real eligible ChatGPT Plus or Pro account using the new AIProxer flow and confirms the plan-use permission is granted.
  - Confirm the saved profile uses an issued client ID rather than re-registering through `dynamic_agent_client` on every run.
  - Confirm public `/v1/models` returns a usable account-specific list and at least one selected model reaches `response.completed` through public `/v1/responses`.
  - _Requirements: 10.1, 10.2_

- [ ] 7.2 Validate a native Responses-oriented third-party harness with tool use
  - Run OpenCode or an equivalent supported Responses client in its normal harness mode through `openai-chatgpt-plan`.
  - Verify the harness's own instructions are effective and at least one real client-side tool call/output round trip completes.
  - Do not introduce a harness-specific compatibility patch without first proving the defect is provider-specific rather than generic Responses translation.
  - _Requirements: 10.3_

- [ ] 7.3 Validate a second translated frontend/harness path
  - Exercise a different supported third-party harness/frontend that requires AIProxer protocol translation instead of native Responses passthrough.
  - Verify system/developer instruction priority and a client-side tool round trip.
  - _Requirements: 10.4_

- [ ] 7.4 Inspect wire evidence, restart persistence and refresh behavior
  - Inspect wire/capture evidence and confirm upstream endpoint is `api.openai.com/v1/responses`, with `store:false`, `stream:true`, the harness's own high-priority instructions, and no Codex prompt/wrapper/private headers/private endpoint.
  - Restart AIProxer and prove the same SIWC profile/issued client registration is reused without dynamic re-registration.
  - Exercise a controlled near-expiry or forced-refresh path and confirm rotating token persistence works.
  - Exercise at least one safe negative case (unsupported field or auth failure) and confirm actionable SIWC-native error behavior.
  - _Requirements: 10.5, 10.6_

- [ ] 7.5 Record explicit human acceptance sign-off
  - Record who/when tested and the evidence/outcome for Tasks 7.1–7.4 in the implementation PR/task record or an operator verification artifact.
  - If any gate item fails, return to the relevant implementation phase, repair the new connector and repeat the failed gate.
  - **Do not begin Phase 8 or any legacy deletion task until this checkbox is explicitly approved by the human tester.**
  - _Requirements: 10.7, 10.8_

## Phase 8: Cut Over to the Official Connector After Human Sign-Off

- [ ] 8. Make `openai-chatgpt-plan` the documented ChatGPT-subscription path
- [ ] 8.1 Update migration/configuration guidance for the breaking backend-ID and credential change
  - Replace normal subscription-use examples with `openai-chatgpt-plan`.
  - State that `openai-codex*` configuration and credentials are not compatible with the new backend and that a fresh SIWC authorization is required.
  - Keep API-key `openai`/`openai-responses` guidance unchanged.
  - Do not add old-ID aliases or automatic config rewriting that hides the breaking migration.
  - _Requirements: 11.1, 11.8, 12.2, 12.3_

- [ ] 8.2 Remove any temporary coexistence-only warnings/flags from the new connector
  - Ensure the accepted new connector no longer references legacy availability for fallback or operator routing suggestions.
  - Keep rollback as release/Git rollback, not a hidden runtime private-protocol path.
  - _Requirements: 1.2, 11.8, 12.7_

## Phase 9: Delete the Complete Legacy Codex Family

- [ ] 9. Remove all legacy connectors and live supporting implementation after the gate
- [ ] 9.1 Delete the direct `openai-codex` and `openai-codex-v2` implementations
  - Remove their backend registrations, private connector modules, `openai_codex/` and `openai_codex_v2/` packages once shared-looking helpers have been moved to neutral ownership or proven dead.
  - Remove Codex prompt resolution, client-family adapters, compatibility state/detection, Codex tool schema/tool execution shims, continuation/WS lineage, early-session verbosity bump, GPT-5.5 compatibility, Codex quota handling, managed Codex OAuth/account selection and `.codex/auth.json` fallback logic.
  - Remove obsolete private HTTP/WebSocket executor paths and private endpoint constants.
  - _Requirements: 11.1, 11.2, 11.8_

- [ ] 9.2 Delete `openai-codex-app-server` and Codex-only subprocess/protocol support
  - Remove the app-server backend registration/config and connector.
  - Remove Codex-only helpers, event mapper, runtime fields and workspace rules only after confirming no unrelated live connector consumes them.
  - Remove app-server tests and documentation/config examples.
  - _Requirements: 11.1, 11.4, 11.8_

- [ ] 9.3 Delete the private Codex model-catalog subsystem and resources
  - Remove `src/resources/codex/gpt_5_codex_prompt.md` and private/fallback catalog resource when no live consumer remains.
  - Remove `openai_codex/catalog/`, Codex model-catalog provider/stage, application-stage registration, Codex client-version/GitHub/npm resolution, Codex-specific model enumerators and routing registrations.
  - Remove `list_codex_models.py`, `refresh_codex_model_catalog.py` and other catalog-maintenance scripts that only support the retired family.
  - _Requirements: 11.3, 11.8_

- [ ] 9.4 Remove legacy config, CLI/env, warmup, resilience and protocol special cases
  - Remove old backend schemas/examples/backend-instance files and `OPENAI_CODEX_*` env/config/CLI handling.
  - Remove Codex account-management scripts and old diagnostics/reproduction scripts that no longer describe a live backend.
  - Remove obsolete warmup targeting, Codex OAuth detector entries, resilience type hardcodes, URI parameter branches, context-compaction disablement and session-manager exceptions tied solely to removed backend IDs.
  - Preserve only genuinely generic code still used by live backends; prove ownership with references/tests.
  - _Requirements: 8.8, 11.5, 11.8, 12.4, 12.6_

- [ ] 9.5 Remove/replace legacy behavior tests and stale documentation
  - Delete tests whose only contract is the removed private Codex protocol, prompt, account pool, continuation, app-server or catalog behavior.
  - Move any still-valid generic Responses/OAuth/security regression assertion to the appropriate generic/new-connector suite before deleting the old test.
  - Remove user/developer guidance that tells users to configure/authenticate the retired backends; keep only explicit migration/history wording where necessary.
  - Do not rewrite archived Kiro specs or Git history for cosmetic name removal.
  - _Requirements: 11.6, 11.7, 12.2, 12.4_

## Phase 10: Final Residual Audit and Whole-Repository Verification

- [ ] 10. Prove the migration is final and no live private Codex path remains
- [ ] 10.1 Run an explicit residual-reference and import audit
  - Search non-archived active source/config/scripts/tests/docs for `openai-codex`, `openai_codex`, `backend-api/codex`, `codex_cli_rs`, old prompt/catalog resource names, old OAuth variables and app-server backend ID.
  - Reconcile every hit as removed, migrated, genuinely unrelated live Codex tooling, or intentional migration documentation with a narrow allowlist rationale.
  - Verify no active startup stage, registry entry, model enumerator or service branch imports a deleted Codex module at runtime.
  - _Requirements: 12.5, 12.6_

- [ ] 10.2 Run the full project quality/test gate after teardown
  - Run focused `openai-chatgpt-plan` suites plus project-wide pytest, lint/format and typing gates appropriate to the branch.
  - Run Responses cross-protocol/routing/access-mode/resilience tests and the Kiro spec-state linter.
  - Fix teardown regressions rather than restoring a private compatibility fallback.
  - _Requirements: 9.7, 11.6, 12.6_

- [ ] 10.3 Reconcile migration documentation and final spec state
  - Confirm docs accurately describe SIWC profiles, limitations, model discovery, refresh, sign-out, remote import and the removal of legacy backend IDs.
  - Confirm all tasks in this specification are complete or explicitly closed with evidence; no required cleanup is moved to a follow-up Kiro spec.
  - Update `spec.json` implementation/completion state according to repository Kiro rules and archive the spec only when implementation is actually complete.
  - _Requirements: 12.1, 12.2, 12.4, 12.7_

## Implementation Notes

- Builtin discovery still classifies OAuth connectors by module name (`is_oauth_connector`) without `connector_class`; do not add `openai-chatgpt-plan` to `KNOWN_OAUTH_CONNECTORS` — task 1.2 must use generic capability metadata instead.
- Task 1.2: `BackendCapabilityDescriptor.is_oauth_based` / `requires_personal_auth` default False; ChatGPT-plan declares both True on the connector class. Multi-user discovery imports then unregisters via `_unregister_personal_auth_backends`. Resilience reads the same flags. Do not add the backend ID to name lists.
- Task 1.3: `ChatGPTPlanConfig` in `src/connectors/openai_chatgpt_plan/config.py`; live ENV overlay is `_apply_openai_chatgpt_plan_backend` in `from_env_part3.py` (`OPENAI_CHATGPT_PLAN_*` only). No main-CLI extra flags (same as Codex extras). Do not treat `OPENAI_CODEX_*` as aliases. Pre-commit secret scan flags `sk-` prefixes in tests — use non-token placeholders.
- Task 2.1: host.json + profiles/<profile_id>.json; load must require payload profile_id == filename stem or raise ChatGPTPlanProfileStorageError without token details. `id_token` is in DEFAULT_REDACTED_FIELDS.
- Task 2.2: SIWC authorize `https://auth.openai.com/api/accounts/authorize`, token `https://auth.openai.com/api/accounts/oauth/token`. After token exchange profile is saved with issued client ID and `status=missing_plan_scope`, issuer/subject `pending-oidc-validation` until 2.3 JWKS validation. Package `__init__` must not import `oauth.py`.
- Task 2.3: Authlib JWKS in `oidc.py`; audience is issued client_id; `ready` only when `chatgpt.tokens.use.direct` is granted; `reauthorize` rejects issuer/subject/client_id mismatch without overwrite. Inference later must refuse non-ready profiles.
- Task 2.4: `ChatGPTPlanTokenManager` in `tokens.py` — per-profile lock, post-lock reload, full rotated token persist, HTTP 400/401/403 treated as terminal `needs_reauth`. `sign_out` revokes then `clear_tokens`; keeps issued_client_id.
- Task 2.5: `ChatGPTPlanProfileSelector` precedence is request, backend-instance, config profile_id, sole/default profile, else error listing IDs. Import never writes dest `ext_agent_host_id`. No auto-import of `.codex/auth.json`. CLI is task 6.1.
- Task 3.1: `ChatGPTPlanModelCatalog` GET `https://api.openai.com/v1/models` only; cache fingerprint is issued_client_id+issuer+subject; connector `bind_chatgpt_plan_model_catalog` so package import stays catalog-free. Routing integration is 3.2.
- Task 3.2: `build_chatgpt_plan_model_catalog` caches one catalog/token-manager graph per resolved `profiles_path` so enumerator and connector share invalidation. Register `ChatGPTPlanConfiguredModelEnumerator` in `routing.py`; do not add a ChatGPT-plan application stage. Process-lifetime cache: first-seen `ChatGPTPlanConfig` extras win for that store.
- Task 4.1: `ChatGPTPlanRequestPolicy.project` in `request_policy.py` — native `instructions` kept; empty instructions filled from `CanonicalChatRequest.system_prompt`; residual `role=system` in `input`/`messages` rewritten to `developer`. Duplicate suppression uses `siwc_instruction_source` provenance tags, not text-equality. Do not import request_policy from package `__init__`. Store/stream/unsupported fields are 4.3.
- Task 4.2: SIWC preview matrix (docs 2026-09-30): preserve function/custom tools, `tool_choice`, call/output linkage, text/image/file, and web search. Reject image generation, file search, Code Interpreter, `computer_use_preview`, hosted MCP/connectors, `tool_search`, and top-level `programmatic_tool_calling` via `ResponsesProviderLimitationError`. No client-family adapters. Nested `additional_tools` hosted types not walked (possible 4.3 follow-up).
- Task 4.3: `project()` forces `store=false`/`stream=true`; always omits `previous_response_id`; explicit unsupported fields (canonical + extra_body) raise `ResponsesProviderLimitationError`; incidental serializer defaults are stripped. Explicit `store=True` is rejected; downstream `stream=False` stays `downstream_stream_requested=False`. Nested `additional_tools` hosted types are walked. HTTP/headers are 4.4.
