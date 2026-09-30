# Research & Design Decisions

---
**Purpose**: Capture brownfield discovery, external protocol research, gap analysis, and design decisions for the complete migration from AIProxer's private Codex-compatible ChatGPT-subscription backends to OpenAI's documented Sign in with ChatGPT (SIWC) plan-usage flow.
---

## Summary

- **Feature**: `openai-chatgpt-plan-connector-migration`
- **Discovery Scope**: Complex brownfield replacement and retirement
- **Base investigated**: `legacy-python-dev`
- **Key findings**:
  - OpenAI's September 2026 SIWC flow is not merely an approval of the old Codex CLI technique. It defines a new public OAuth + Responses API contract for eligible open-source/local harnesses.
  - The current `openai-codex` family is deeply coupled to Codex CLI identity and private ChatGPT backend behavior: private `backend-api` endpoints, Codex-specific headers, bundled Codex instructions, private model discovery, Codex client versioning, continuation state, client-family adapters, and Codex-specific quota/account rotation.
  - AIProxer's existing public `OpenAIResponsesConnector` and Responses translation/streaming infrastructure are a better implementation base than the current Codex connector.
  - The new SIWC transport has a narrower preview request contract than normal API-key Responses traffic, so a connector-local request-policy layer is required rather than blindly reusing generic `openai-responses` payloads.
  - The current core still contains name-based OAuth/personal-backend handling for the Codex family. The migration must either consume the generic capability work if it has landed by implementation time or implement the minimum generic capability plumbing itself. It must not add a new `openai-chatgpt-plan` hardcode.
  - A clean coexistence period followed by a mandatory real-service/human acceptance gate is safer than an in-place rewrite. Once that gate passes, keeping the old connectors would create unnecessary protocol and maintenance debt; this spec therefore includes their complete removal.

## Source-of-Truth Order Used

1. Current user direction in this migration request.
2. OpenAI's September 2026 SIWC documentation.
3. `legacy-python-dev` source, tests, Kiro steering, and active specifications.
4. Historical archived Codex refactor specifications, used only as provenance for old behavior and known coupling.

## Research Log

### OpenAI SIWC Is a New First-Party Contract

- **Context**: Determine whether the DevDay announcement merely permits the existing Codex CLI OAuth/private-backend flow or changes the required integration model.
- **Sources Consulted**:
  - https://developers.openai.com/cookbook/articles/sign-in-with-chatgpt
  - https://developers.openai.com/siwc/token-sharing-open-source/
  - https://developers.openai.com/siwc/token-sharing-open-source/sign-in
  - https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference
  - https://developers.openai.com/siwc/token-sharing-open-source/token-reference
  - https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery
  - https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations
- **Findings**:
  - Initial registration starts with `client_id=dynamic_agent_client` and returns an issued app/client ID (`oaiapp_...`) that is reused for that registration.
  - The authorization flow uses a stable app-host identifier (`ext_agent_host_id`), PKCE S256, `state`, and an OIDC nonce.
  - The required plan-use scopes include `resource.invoke` and `chatgpt.tokens.use.direct`; the resource is `https://api.openai.com/v1`.
  - The loopback redirect is explicitly `http://127.0.0.1:<port>/auth/callback`; `localhost` is not an interchangeable redirect spelling.
  - Model discovery is through authenticated public `GET https://api.openai.com/v1/models`.
  - Inference is through public `POST https://api.openai.com/v1/responses`; OpenAI explicitly says not to use ChatGPT `backend-api` endpoints.
  - HTTP SIWC inference currently requires `store:false` and `stream:true` on every upstream request.
  - Normal Responses `instructions` and developer-message semantics are supported. Explicit Responses input items with `role:"system"` are currently rejected, so a deterministic projection is needed.
  - HTTP `previous_response_id` is not supported in this preview; callers replay the required input context.
  - Several normal Responses fields and hosted tools are currently unsupported and should not be passed through opportunistically.
  - Access tokens are short-lived and refresh tokens rotate. Refresh must therefore be concurrency-safe and persisted atomically.
  - The service exposes documented subscription-sharing error codes for plan availability/exhaustion and capability errors.
- **Implications**:
  - The legacy Codex endpoint, identity emulation, prompt preservation, and private model catalog are not part of the new contract.
  - A fresh connector should model SIWC directly instead of trying to gradually mutate a Codex-CLI compatibility connector.

### Current Legacy Connector Is a Codex-Identity Emulation Stack

- **Context**: Establish what must not leak into the new implementation and what must ultimately be removed.
- **Sources Consulted**:
  - `src/connectors/_openai_codex_connector.py`
  - `src/connectors/_openai_codex_v2_connector.py`
  - `src/connectors/openai_codex_app_server.py`
  - `src/connectors/openai_codex/`
  - `src/resources/codex/`
  - `docs/user_guide/backends/openai-codex.md`
- **Findings**:
  - `OpenAICodexConnector` declares `backend_type = "openai-codex"` and carries the bundled prompt resource `gpt_5_codex_prompt.md`.
  - It identifies upstream traffic with `originator: codex_cli_rs`, Codex-style version/User-Agent values, `Codex-Task-Type`, conversation/session headers, and `chatgpt-account-id`.
  - The execution path targets `https://chatgpt.com/backend-api/codex/responses` by default.
  - Prompt handling defaults to `prompt_mode=codex_default`; foreign system instructions are separated from the validated Codex instructions and wrapped as `<user_instructions>`.
  - The `openai_codex` package owns a large amount of legacy behavior: request/payload construction, prompt resolution, client-family adapters, compatibility state, Codex tools/tool schemas, managed OAuth, account selection, quota handling, continuation, WebSocket logic, model catalog and downgrade compatibility.
  - `openai-codex-v2` subclasses the same connector and adds managed WebSocket v2 lineage/continuation behavior rather than representing a distinct public provider contract.
  - `openai-codex-app-server` launches the Codex CLI as a nested local agent runtime over JSON-RPC and therefore has semantics materially different from a transparent model backend.
- **Implications**:
  - Reusing these modules would risk accidentally preserving precisely the private-protocol assumptions the migration is intended to eliminate.
  - The new connector must not import from the legacy package except through genuinely generic modules that are first moved/refactored to neutral ownership.

### Existing Public Responses Connector Is the Correct Reuse Seam

- **Context**: Find the smallest clean implementation base already present in AIProxer.
- **Sources Consulted**:
  - `src/connectors/openai_responses.py`
  - `src/connectors/openai.py`
  - `src/core/domain/translators/responses/request.py`
  - `src/core/app/controllers/responses_controller.py`
  - active `responses-api-frontend-compliance` specification
- **Findings**:
  - `OpenAIResponsesConnector` is already a thin specialization of `OpenAIConnector` targeting `/v1/responses`.
  - `OpenAIConnector.responses()` already provides bearer authentication, public Responses URL construction, SSE streaming, non-stream accumulation, cancellation integration, capture boundaries, and normal error translation.
  - AIProxer's Responses frontend preserves native `input` and `instructions` when available and has cross-protocol projectors/normalization work in flight.
  - Generic Responses request serialization currently allows fields that SIWC preview rejects (`metadata`, `conversation`, `previous_response_id`, `background`, `truncation`, etc.), so SIWC needs an explicit compatibility/policy filter after generic projection and before transport.
  - The generic OpenAI request serializer retains chat-style `messages`; therefore the SIWC connector needs a deterministic step that turns canonical high-priority system semantics into Responses `instructions`/developer items rather than relying on current chat serialization.
- **Implications**:
  - New code should be a small first-party provider package built on public Responses infrastructure, not a fork of Codex executor logic.
  - Any translator defect found that affects all Responses providers should be repaired generically rather than hidden inside SIWC-specific client-family code.

### Harness Prompt Semantics Can Be Native Instead of Codex-Preserved

- **Context**: Resolve the original problem that prompted this migration: Codex's original system prompt had to survive, forcing AIProxer to glue other harness instructions around it.
- **Sources Consulted**:
  - OpenAI SIWC preview limitations documentation
  - `_openai_codex_request_translator.py`
  - `openai_codex/prompt.py`
  - tests for Codex prompt handling and client-family adapters
- **Findings**:
  - SIWC supports top-level `instructions` and developer messages.
  - The old `Instructions are not valid` behavior is tied to the private Codex contract, not the documented SIWC public contract.
  - Current Codex logic deliberately suppresses foreign `role=system` items and turns them into `<user_instructions>` below Codex's default instructions.
- **Implications**:
  - The new connector should carry the harness's own high-priority instructions as the actual high-priority model instructions.
  - It must not load the bundled Codex prompt and must not introduce `<user_instructions>` wrappers.
  - Because explicit Responses `role=system` input items are currently rejected, a deterministic projection rule is required: canonical/top-level system instructions become `instructions`; residual ordered system items become developer items only when they cannot be represented as top-level instructions without losing ordering/provenance.

### Current OAuth Storage Is Not a Valid SIWC Profile Store

- **Context**: Determine whether the existing managed Codex OAuth implementation can simply be renamed/reused.
- **Sources Consulted**:
  - `src/connectors/openai_codex/managed_oauth_constants.py`
  - `managed_oauth_flow.py`
  - `managed_oauth_models.py`
  - `credentials.py`
- **Findings**:
  - The old flow uses a fixed Codex CLI OAuth client ID and the older auth/token endpoints/scopes.
  - The old records are centered around Codex account IDs and Codex quota/account rotation behavior, not SIWC's issued client ID + OIDC subject + host identity model.
  - SIWC requires OIDC ID-token validation, nonce checking, granted-scope validation, issued-client persistence, and resource-bound refresh semantics.
  - Existing `.codex/auth.json` data does not prove an official SIWC registration for AIProxer.
- **Implications**:
  - Do not silently import old Codex credentials.
  - Implement a fresh host/profile store with explicit SIWC identity semantics.
  - Reauthorization should preserve the profile's issued client ID and host identity; multiple profiles must remain distinct even if display emails match.

### Account Pools and Quota Rotation Should Not Be Carried Forward as Default Semantics

- **Context**: Decide whether legacy round-robin/automatic subscriber rotation should survive in the new connector.
- **Sources Consulted**:
  - current `managed_oauth_selector.py`, quota logging/notifications and executor recovery logic
  - OpenAI SIWC account/session and errors/recovery documentation
- **Findings**:
  - OpenAI supports multiple registrations/profiles, but the documented usage/account relationship is explicit.
  - The old connector can rotate among managed accounts on auth/quota failures to maximize availability.
  - In an official subscriber-plan flow, silently hopping to a different subscriber when one plan reaches a limit obscures user identity, entitlement, and operator intent.
- **Implications**:
  - AIProxer may store multiple SIWC profiles, but a backend instance/request must use an explicitly selected or configured profile.
  - Automatic cross-subscriber round-robin/failover is not a default behavior of `openai-chatgpt-plan`.
  - Plan exhaustion is surfaced for the selected profile rather than causing hidden account rotation.

### Model Discovery and Routing Must Lose Codex Version Coupling

- **Context**: Identify the model-routing cleanup needed by a public `/v1/models` contract.
- **Sources Consulted**:
  - `src/connectors/openai_codex/catalog/`
  - `src/core/app/stages/codex_model_catalog.py`
  - `src/core/di/registrations/_backend/routing.py`
  - `src/core/services/configured_backend_model_enumerators.py`
  - model catalog maintenance scripts
- **Findings**:
  - Current Codex model discovery is a dedicated startup stage, uses the private catalog endpoint, maintains a shipped fallback snapshot, and uses Codex client-version compatibility.
  - The stage is globally registered even though this behavior exists only for the Codex family.
  - SIWC model discovery is ordinary account-scoped public `/v1/models` traffic.
- **Implications**:
  - New model discovery belongs to the SIWC profile/connector lifecycle, cached per profile.
  - It should integrate with generic model enumeration/routing and must not add another provider-specific application startup stage.
  - Once old connectors are removed, the Codex catalog stage, fallback resource, version resolver, enumerators, and scripts become deletion targets unless another live consumer is found during implementation.

### Brownfield Core Capability Gap Must Be Reconciled Here

- **Context**: New connector requires personal OAuth semantics, but project steering prohibits adding provider-name classification.
- **Sources Consulted**:
  - `.kiro/steering/product.md`, `structure.md`, `tech.md`
  - active `.kiro/specs/oauth-connectors-plugin-architecture/`
  - `src/core/domain/backend_capability_descriptor.py`
  - `src/connectors/oauth_detector.py`
  - `src/core/services/resilience/scope.py`
  - `src/connectors/__init__.py`
- **Findings**:
  - Steering and the active OAuth architecture spec define the desired direction: `BackendCapabilityDescriptor` should carry generic `is_oauth_based` / `requires_personal_auth` semantics.
  - The current checked code has not fully landed those fields yet; the descriptor still only contains protocol/tool/vision/json/context metadata.
  - Discovery and resilience still contain hardcoded `openai-codex` names and string heuristics.
  - Adding `openai-chatgpt-plan` to the same hardcoded lists would immediately violate current steering and create new cleanup debt.
- **Requirement repair made after gap analysis**:
  - Requirements 1.6 and 8 explicitly require capability-driven personal/OAuth classification.
  - The specification is self-contained: implementation must consume generic capability plumbing if already present at implementation time, otherwise land the minimum generic capability change required by this connector in this spec.
  - Legacy hardcodes are deleted during final teardown.
- **Implications**:
  - This work does not wait for another spec and does not duplicate the entire OAuth-plugin project; it owns only the generic capability pieces required to prevent a new provider-name branch.

### Context Compaction and Continuation Restrictions Are Legacy-Specific

- **Context**: Determine whether old session-level restrictions should be inherited.
- **Sources Consulted**:
  - `docs/user_guide/features/context-compaction.md`
  - old `openai-codex` documentation
  - continuation code and HTTP full-replay tests
  - SIWC preview continuation rules
- **Findings**:
  - Current AIProxer disables history compaction for a session after the legacy Codex backend is used.
  - SIWC HTTP does not support `previous_response_id`; it expects the necessary input replayed in the request.
  - This does not imply that AIProxer's own safe history compaction must be disabled; it only requires that the final replay be semantically sufficient.
- **Implications**:
  - The new connector uses full replay from AIProxer's canonical session/history path and does not inherit Codex continuation coordinators or the blanket context-compaction disablement.
  - Existing generic context correctness tests remain the guardrail.

### App-Server Was Considered and Intentionally Retired

- **Context**: OpenAI documents an official SIWC-compatible Codex app-server path, so decide whether `openai-codex-app-server` should survive the migration.
- **Findings**:
  - App-server is a useful way to embed the Codex *agent runtime*, but it is not a transparent model backend.
  - The target AIProxer use case is arbitrary external harnesses bringing their own prompts, tools, and execution loops.
  - Retaining app-server would leave an overlapping Codex-specific backend family and its workspace/subprocess/catalog/test surface after the new direct connector is proven.
- **Decision**:
  - Do not modernize app-server as part of the final architecture. Keep it only during the pre-gate coexistence period, then remove it with the other legacy connectors.
- **Implications**:
  - This avoids a harness-inside-a-harness path and satisfies the user's requirement for a final migration without a follow-up cleanup spec.

### Human Testing Is a Required Migration Barrier, Not Optional QA

- **Context**: Some SIWC behavior can only be demonstrated against a real eligible account and third-party harnesses.
- **Findings**:
  - Mocked OAuth/JWKS/SSE tests can prove protocol handling but cannot prove current account eligibility, real dynamic client registration, actual model visibility, or third-party harness compatibility.
  - The historical connector accumulated client-specific workarounds because private behavior differed from assumptions; deleting it solely on unit-test confidence would repeat that failure mode.
- **Implications**:
  - Tasks include a blocking operator/human acceptance milestone.
  - Legacy deletion tasks explicitly depend on human sign-off and cannot be marked complete by automated CI.
  - Required evidence includes a real SIWC login, `/v1/models`, `response.completed`, at least two harness/frontend paths, tool round trips, restart/profile reuse, forced token refresh, and wire-capture inspection proving absence of Codex emulation.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
|---|---|---|---|---|
| In-place rewrite of `OpenAICodexConnector` | Gradually replace private endpoint/auth/prompt behavior inside the old class | Fewer initial file additions | Very high risk of retaining hidden Codex assumptions; difficult coexistence; test suite conflates old/new | Rejected |
| New `openai-chatgpt-plan` connector based on public Responses, coexist, validate, then delete legacy | Independent SIWC package and profile services; old connectors remain until human gate | Clean boundary, easy A/B comparison, explicit rollback before cutover, much smaller final code | Temporary duplication during proving phase | **Selected** |
| New direct connector but retain modernized Codex app-server permanently | Direct path for general clients plus Codex agent runtime | Keeps app-server use cases | Leaves Codex-specific runtime/maintenance surface and conflicts with full-retirement objective | Rejected |
| Implement SIWC as an external optional OAuth plugin | Move all ChatGPT-plan behavior out of core repo | Strong packaging isolation | First-party OpenAI backend would become optional; adds packaging/dependency complexity; not required for requested migration | Rejected for this feature |
| Reuse legacy Codex managed OAuth/account pool as SIWC store | Adapt existing account selector/storage classes | Reuses code | Wrong identity model and encourages silent cross-account quota rotation; legacy dependencies remain | Rejected |

## Design Decisions

### Decision: Introduce `openai-chatgpt-plan` as a New Backend ID

- **Selected Approach**: New first-party connector package with no inheritance/import dependency on `OpenAICodexConnector`.
- **Rationale**: Makes old/new behavior independently testable and makes eventual legacy deletion mechanical rather than entangled.
- **Trade-off**: Users must migrate configuration and reauthorize; legacy aliases are intentionally not retained after cutover.

### Decision: Build on Public Responses Infrastructure

- **Selected Approach**: Reuse `OpenAIResponsesConnector` / `OpenAIConnector.responses()` transport semantics through a narrow SIWC specialization and request-policy layer.
- **Rationale**: Public `/v1/responses` is the official upstream contract and AIProxer already has mature streaming/capture/cancellation logic there.
- **Constraint**: Do not let generic `openai-responses` request flexibility bypass SIWC preview restrictions.

### Decision: Use a Connector-Local SIWC Policy Layer

- **Selected Approach**: After normal frontend/canonical projection, enforce SIWC-specific instructions, field support, forced upstream streaming, `store:false`, tool/input restrictions, and no HTTP continuation ID.
- **Rationale**: SIWC is a provider/auth profile of Responses, not a reason to fork frontend translators.
- **Rule**: If a defect is generic to Responses semantics, fix it in the generic translator/projector and test it there instead of adding a harness-specific SIWC workaround.

### Decision: Fresh SIWC Profiles, No Legacy Credential Migration

- **Selected Approach**: New host/profile store with issued client ID, OIDC subject, validated identity, scopes, token set, expiry and registration metadata. Existing Codex credentials do not auto-migrate.
- **Rationale**: A Codex CLI token does not establish the registration/scopes/host relationship required by SIWC.

### Decision: Authlib/OIDC Facilities for Token Validation

- **Selected Approach**: Prefer the repository's existing `authlib` dependency and standards-based OIDC/JWKS validation rather than custom JWT signature code.
- **Rationale**: Reduces security-sensitive bespoke implementation.
- **Constraint**: Tests must pin issuer/audience/nonce/expiry/signature failure behavior and mock discovery/JWKS deterministically.

### Decision: Explicit Profile Binding, No Automatic Subscriber Rotation

- **Selected Approach**: One selected profile per backend instance/request context. Multiple saved profiles are supported, but switching is explicit.
- **Rationale**: Preserves identity/entitlement transparency and avoids turning personal subscriptions into an opaque pooled quota source.

### Decision: Capability-Driven Personal OAuth Integration

- **Selected Approach**: `openai-chatgpt-plan` declares generic OAuth/personal-auth capabilities. If the generic descriptor/discovery changes from the active OAuth architecture spec are not present at implementation time, this migration lands the minimal generic support itself.
- **Rationale**: The new backend must not be added to hardcoded OAuth/resilience lists that are already recognized technical debt.

### Decision: Public, Per-Profile Model Discovery With No Fallback Snapshot

- **Selected Approach**: Cache `/v1/models` per profile and invalidate on profile/authorization changes.
- **Rationale**: The service is account-specific; a static Codex snapshot can advertise entitlements the selected profile does not have.

### Decision: HTTP/SSE First, No SIWC-Specific WebSocket Continuation Requirement

- **Selected Approach**: Implement the documented HTTP Responses SIWC flow first and rely on explicit input replay. Do not recreate `openai-codex-v2` continuation/WebSocket lineage merely for parity.
- **Rationale**: The user asked for simplification and official flow; adding a second transport before the direct path is proven would reproduce unnecessary complexity.
- **Note**: Generic OpenAI Responses WebSocket capability may evolve independently; this spec does not require a SIWC-only transport fork.

### Decision: Human Acceptance Is the Irreversible-Cutover Gate

- **Selected Approach**: Legacy connector deletion cannot start until the required real-service test matrix has explicit human sign-off.
- **Rationale**: It is the only reliable way to verify current account authorization plus real harness behavior before deleting the proven fallback.

### Decision: Delete the Entire Legacy Codex Family After Gate

- **Selected Approach**: Remove `openai-codex`, `openai-codex-v2`, `openai-codex-app-server` plus dead support code/resources/config/tests/scripts and core branches.
- **Rationale**: This is an alpha/brownfield product migration where keeping compatibility aliases/private endpoints is more harmful than requiring an explicit reauthorization/config migration.
- **No follow-up**: Residual legacy references are audited and resolved in this same implementation plan.

## Brownfield Gap Analysis and Requirements Repair

The initial product intent ("new clean connector, then remove old after human proof") was expanded after source inspection. The following gaps were incorporated into `requirements.md` before design finalization:

1. **Capability gap**: current core still has name-based OAuth/personal classification. Requirement 1.6 and Requirement 8 now require generic capability integration and prohibit a new name hardcode.
2. **Request-contract gap**: existing generic Responses projection can emit fields SIWC preview rejects. Requirement 6 now owns a strict policy/filter contract.
3. **Prompt-projection gap**: canonical chat/system semantics do not automatically become legal SIWC Responses semantics. Requirement 5 defines a deterministic high-priority instruction projection and a negative requirement against Codex wrapping.
4. **Success-state gap**: upstream HTTP must always stream, even for downstream non-streaming clients. Requirements 6 and 7 require accumulation only after terminal `response.completed`.
5. **Credential-model gap**: old managed Codex account files cannot be treated as SIWC registrations. Requirements 2 and 3 require new registration/profile semantics and fresh authorization.
6. **Routing/catalog gap**: old Codex startup stage and fallback catalog are incompatible with account-scoped public discovery. Requirement 4 and final removal requirements own that migration.
7. **Retirement-scope gap**: app-server, warmup, context-compaction exceptions, CLI/scripts and live hardcodes were not limited to the primary connector package. Requirement 11 explicitly covers all of them.
8. **Evidence gap**: automated tests cannot prove the real account/harness contract. Requirement 10 makes empirical human validation a hard dependency for teardown.

After these repairs, the requirements set covers introduction, coexistence, proof, cutover, teardown, documentation and residual verification without a required follow-up spec.

## Risks & Mitigations

- **SIWC preview changes during implementation** — Keep SIWC policy isolated behind typed connector-local contracts; re-check official docs immediately before implementation and real-service testing; do not encode private fallback behavior.
- **OAuth security defect** — Use standards-based OIDC/JWKS validation, PKCE/state/nonce, serialized refresh and atomic token persistence; add negative tests for every security invariant.
- **Generic translator loses harness instruction ordering** — Preserve provenance in request projection, add native Responses and translated-frontend contract tests, and use human wire inspection before cutover.
- **Forced upstream streaming breaks downstream non-stream responses** — Reuse existing Responses stream accumulator and require terminal completion semantics in automated tests.
- **Model routing sees stale/wrong profile catalog** — Cache by profile identity/registration and invalidate on selection/reauthorization.
- **Partial capability architecture has not landed** — Implement only the generic descriptor/discovery/resilience seam required by this connector within this spec; never fall back to a new provider-name hardcode.
- **Legacy cleanup removes a helper still used elsewhere** — Perform import/reference analysis before deleting each shared-looking helper and run project-wide tests plus residual-reference audit.
- **Human gate becomes ceremonial** — Tasks define concrete evidence and explicitly forbid commencing legacy deletion until human sign-off is recorded.
- **Rollback after teardown** — Before the human gate, route back to the legacy connector. After accepted cutover and deletion, rollback is a Git revert/release rollback, not a permanent legacy runtime fallback.

## References

### External
- [Sign in with ChatGPT for open-source apps](https://developers.openai.com/cookbook/articles/sign-in-with-chatgpt)
- [SIWC token sharing for open-source apps](https://developers.openai.com/siwc/token-sharing-open-source/)
- [SIWC sign-in and dynamic registration](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)
- [SIWC models and inference](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference)
- [SIWC preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations)
- [SIWC token reference](https://developers.openai.com/siwc/token-sharing-open-source/token-reference)
- [SIWC errors and recovery](https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery)
- [SIWC Codex app-server integration](https://developers.openai.com/siwc/token-sharing-open-source/codex-app-server)

### Repository
- `.kiro/steering/product.md`
- `.kiro/steering/structure.md`
- `.kiro/steering/tech.md`
- `.kiro/steering/testing.md`
- `.kiro/specs/oauth-connectors-plugin-architecture/`
- `.kiro/specs/responses-api-frontend-compliance/`
- `src/connectors/_openai_codex_connector.py`
- `src/connectors/_openai_codex_v2_connector.py`
- `src/connectors/openai_codex_app_server.py`
- `src/connectors/openai_codex/`
- `src/connectors/openai_responses.py`
- `src/connectors/openai.py`
- `src/core/domain/translators/responses/request.py`
- `src/core/domain/backend_capability_descriptor.py`
- `src/connectors/oauth_detector.py`
- `src/core/services/resilience/scope.py`
- `src/core/app/stages/codex_model_catalog.py`
- archived `.kiro/specs/archive/openai-codex-connector-god-object-refactoring/`
- archived `.kiro/specs/archive/codex-connector-refactoring-follow-up/`
