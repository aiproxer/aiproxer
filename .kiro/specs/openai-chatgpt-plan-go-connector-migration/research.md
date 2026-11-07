# Research & Design Decisions

---
**Purpose**: Capture brownfield investigation of the current Go connector, reusable AIProxer infrastructure, and the September 2026 OpenAI SIWC contract that drives the migration.
---

## Summary

- **Feature**: `openai-chatgpt-plan-go-connector-migration`
- **Discovery Scope**: Complex brownfield connector replacement and protocol migration
- **Repository baseline**: `aiproxer/aiproxer-enterprise`, `main`, inspected 2026-09-30
- **Key findings**:
  - The Go implementation is an external executable backend plugin.
  - `connectors/codex` exports two semantically different factories: private direct Codex inference and Codex app-server agent runtime.
  - The private inference stack still targets `chatgpt.com/backend-api/codex`, impersonates Codex CLI, and contains private OAuth/header/WebSocket/catalog/native-context assumptions.
  - Canonical request translation is central, and client-family glue is mostly a feature plugin (`codex-client-compat`) rather than backend/core logic.
  - OpenAI's new SIWC contract turns direct ChatGPT-plan inference into a public Responses integration with OAuth authorization. A new executable connector is cleaner than preserving the private Codex engine shape.
  - Existing `connector-support/oauthcred` and `connector-support/openaicompat` offer reusable primitives, but neither is a drop-in implementation of SIWC dynamic registration or its always-stream/preview policy.
  - Existing private Codex native compaction must not be carried forward without a documented public contract.

## Research Log

### AIProxer architecture and plugin boundary

**Sources consulted**
- `.kiro/steering/product.md`, `tech.md`, `structure.md`, `testing.md`
- `pkg/aipsdk/standard_bundle.go`
- `connectors/codex/manifest/template.backendplugin.json`
- `connectors/codex/release.yaml`
- `internal/testkit/backendplugin/stage_codex.go`

**Findings**
- Frontends decode to `pkg/aipapi`; backends consume canonical calls. Pairwise frontend/backend translation is intentionally prohibited.
- Streaming is the primary lifecycle; non-streaming is collection over canonical events.
- Optional provider integrations live in separately buildable executable connectors behind the backend-plugin gRPC ABI.
- `connectors/codex` is already externalized and excluded from the root mandatory connector bundle.

**Implications**
- SIWC belongs in a new external connector, not root core or a new canonical type.
- Provider OAuth/profile state stays connector-owned.
- Reusable support changes are acceptable only when provider-neutral.

### Current direct Codex implementation

**Sources consulted**
- `connectors/codex/internal/codex/config.go`
- `headers.go`, `oauth.go`, `managed_oauth.go`, `plugin.go`, `attempt.go`, `ws.go`
- `payload.go`, `payload_input.go`, `request_policy.go`

**Findings**
- Default direct endpoint is `https://chatgpt.com/backend-api/codex`.
- OAuth defaults are Codex-CLI-specific, including the historical Codex client id and token endpoint.
- Requests identify themselves as `codex_cli_rs` and add private Codex headers (`version`, conversation/session ids, Codex task type, account id, beta headers).
- Connector state includes managed account selection/rotation, rate-limit state, plan hints/model downgrade, turn-count verbosity heuristics, WebSocket continuation, and native context/compaction.
- `payload.go` still defines a fallback Codex identity instruction: `You are Codex...`.
- `payload_input.go` folds system messages into `instructions` and skips them from Codex `input`, so little prompt glue remains.

**Implications**
- The useful reusable concepts are canonical history/tool mapping and bounded streaming normalization, not the private auth/headers/session state machine.
- The new connector must have no fallback Codex identity prompt.

### Current factory/service packaging

**Sources consulted**
- `connectors/codex/internal/service/{kind,service,config,execute,inventory}.go`
- `connectors/codex/cmd/aip-backend-codex/main.go`

**Findings**
- One plugin id, `com.aiproxer.backend.codex`, exports:
  - `openai-codex` — inference execution class.
  - `openai-codex-app-server` — agent-runtime execution class.
- Host interaction is canonical and gRPC-based; the connector reconstructs provider-local execution state.
- Current extracted-service config surface is narrower than some legacy documentation, revealing stale docs/config assumptions that should be cleaned during migration.

**Implications**
- Manifest, release metadata, parity tests, testkit staging, config examples and docs must migrate together.
- The new official connector can cleanly be a separate plugin id/module.

### Native context, reasoning continuity and compaction

**Sources consulted**
- `.kiro/specs/archive/openai-codex-native-compaction/*`
- `connectors/codex/internal/codex/native_context_*`
- service capability/dialect advertisement

**Findings**
- AIProxer currently has sophisticated private Codex context support: exact reasoning replay, encrypted reasoning, checkpoint state, private Responses Compaction V2, WebSocket continuation and provider accounting sideband.
- HTTP correctness can fall back to full exact history; response-id continuation is an optimization.
- The private compaction endpoint/dialect is not part of OpenAI's new documented SIWC contract.

**Implications**
- The new connector must not call private `/responses/compact` or advertise `codex.responses.compaction.v2`.
- Baseline correctness uses public full-history replay.
- Exact public reasoning replay is an optional capability to revalidate independently; failure must narrow capability, not trigger a private fallback.

### Client compatibility feature

**Sources consulted**
- `internal/plugins/features/codexclientcompat/*`
- `docs/openai-codex-backend.md`

**Findings**
- OpenCode/Pi/Droid/Hermes bridges are feature-owned, which is architecturally healthier than backend-specific client switching.
- The feature targets literal backend id `openai-codex` and sets `openai_codex.*` extensions for old Codex request workarounds.
- It also contains some potentially provider-neutral tool-history/guard behavior.

**Implications**
- Do not simply retarget this feature to `openai-chatgpt-plan`.
- First prove normal Responses-capable harnesses work without the feature.
- Classify each existing behavior as remove / keep feature-owned / generalize to a provider-neutral seam, based on tests and live evidence.

### Existing reusable connector support

**Sources consulted**
- `connector-support/oauthcred/*`
- `connector-support/openaicompat/*`
- MiniMax OAuth connector login flow
- root OpenAI Responses backend for behavioral reference

**Findings**
- `oauthcred` already solves protected token-file storage, serialized refresh, terminal quarantine, PKCE helpers and redaction.
- Its generic token record cannot safely represent SIWC issued client id, validated subject, host id, granted scopes and retained identity hints.
- `openaicompat` supports external connector-safe Responses HTTP/SSE, but today chooses provider streaming based on downstream delivery mode and its generic request builder forwards fields SIWC currently rejects.

**Implications**
- Reuse generic security primitives, not the generic token schema as the SIWC domain model.
- Keep SIWC preview policy and profile metadata connector-local.
- Extract generic transport helpers only when they remain provider-neutral; otherwise use a small SIWC-specific Responses adapter.

### OpenAI official SIWC contract

**Official sources**
- `https://developers.openai.com/siwc/token-sharing-open-source`
- `/sign-in`
- `/profiles-and-sessions`
- `/models-and-inference`
- `/preview-limitations`
- `/codex-app-server`
- `/errors-and-recovery`
- `/token-reference`

**Findings**
- Initial OSS registration starts with `dynamic_agent_client`, stable `ext_agent_host_id`, and the real agent name. The callback returns a user/workspace-bound issued client id, which is saved and reused.
- Code exchange/refresh use the documented accounts OAuth endpoint; no client secret or partner API key is used.
- The direct plan-use scope must be validated.
- Public model inventory is `GET /v1/models` with the selected access token.
- Direct inference is `POST /v1/responses`; OpenAI explicitly instructs integrations not to use ChatGPT `backend-api`.
- HTTP plan-use requests require `store:false`, `stream:true`, full context in `input`, and no HTTP `previous_response_id`.
- `instructions`/developer messages are accepted; explicit system input messages are rejected.
- A defined set of fields and hosted tool classes is unsupported in preview.
- Plan usage errors may arrive as stream-terminal failures and should not silently switch billing/account paths.
- Official Codex app-server uses the same SIWC access token, public Responses provider configuration, stdio locally and HTTP/SSE upstream; token renewal is app-owned and requires restart + `thread/resume`.

**Implications**
- The current private direct connector is obsolete for the intended subscription-backed use case.
- App-server remains useful but should move to official SIWC provider semantics.

### Hosted web search and end-to-end declaration carriage

**Context**
The owner objective is a working default Codex session, not a connector-only policy that is unreachable because the shared OpenAI Responses frontend rejects `web_search` before routing. The old policy guard refused every non-function/custom declaration, and the frontend never delivered a hosted declaration to it. This section traces the existing carriage seams before selecting the smallest end-to-end boundary.

**Sources consulted**
- `https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations`
- `https://developers.openai.com/api/docs/guides/tools-web-search`
- `https://developers.openai.com/api/reference/resources/responses/methods/create`
- Codex CLI request-shape reports: `https://github.com/openai/codex/issues/26234`, `https://github.com/openai/codex/issues/31875`

**Findings: the wire contract**
- The preview page says: "Supported tools: Group function/custom tools in namespaces or supply them through `additional_tools` input items. Web search remains subject to model and account/workspace policy." Its unsupported list is image generation, file search, Code Interpreter, native computer use, hosted MCP/connectors and `tool_search`; `programmatic_tool_calling` is not accepted in top-level `tools`.
- The web-search guide documents the hosted declaration types `web_search` and the legacy `web_search_preview`; the Responses reference also names the versioned `web_search_2025_08_26` and `web_search_preview_2025_03_11`.
- A hosted search produces a model-authored `web_search_call` output item whose action may be `search`, `open_page` or `find_in_page`. The canonical item model has no equivalent.
- `namespace` is a declaration that groups function/custom tools. It is not a web-search declaration, and a member's name alone does not establish whether it is caller-executed or hosted.
- Codex CLI sends `web_search` as a top-level tool, and also sends `namespace` declarations (and, on some versions, `additional_tools` input items) for MCP/skills. The web-search carriage therefore cannot by itself make every Codex request work.

**Findings: existing carriage seams**
- `internal/plugins/frontends/openairesponses/decode.go` currently rejects every top-level tool whose `type` is neither `function` nor absent; it never reaches a connector.
- `aipapi.Call.Extensions` is the existing generic residual-metadata map. The backend-plugin bridge copies each entry into `Invocation.SafeMetadata` under `ext.<key>` (bounded to 64 KiB per value and 64 fields) and `CallFromInvocation` restores it, so an extension can reach the connector without a new ABI field.
- `aipapi.DeriveProtocolRequirements` treats every non-internal `Call.Extensions` key as `CapabilityOpaqueExtensions` plus an exact `ExtensionRequirement{Namespace: "call", Type: key}`. `MatchRequirements` rejects a candidate that does not advertise that exact extension **before provider I/O**. A backend that lacks the extension therefore fails explicitly; it does not silently serve the request without web search.
- `CapabilitySummary.OpaqueExtensions` and `ResolvedProfile.DialectSupport.ExtensionTypes` let a connector advertise the generic capability and the exact extension type. The SIWC connector currently advertises neither.
- `SemanticExtensions` is a separate negotiated residual carrier that requires the semantic-extensions ABI minor/feature and is rejected by the SIWC projector. Using it would require a larger ABI/contract change and is not the smallest seam.
- `ItemKindExtension` would change item authority/trajectory and require an ordered-item projection change. A new canonical `ToolDef`/hosted-tool field would be a public `pkg/aipapi` contract addition and would violate requirement 6.8. Frontend branching on backend kind would move routing policy into the wrong owner. A connector-specific frontend would duplicate the Responses frontend.
- `internal/testkit/backendplugin/stage_openai_chatgpt_plan_harness_requests_test.go` already drives the real OpenAI Responses frontend → core executor → staged SIWC executable → local reference provider. The end-to-end proof extends that harness; it does not add a new test plane.

**Owner decision (2026-11-06): SIWC-only omission**
- The frontend preserves the declaration type for admission, but only the SIWC connector applies omission. Every other backend rejects the exact extension at admission; no global/frontend/core omission exists.
- SIWC strict posture rejects before projection/network.
- Model-authored web-search history is never dropped, fabricated, or reordered; unrepresentable history is refused.
- `namespace`/`additional_tools` carriage remains separate and is required for Codex requests that send them.

**Selected boundary**
- The frontend preserves only the closed table `web_search`, `web_search_preview`, `web_search_2025_08_26`, `web_search_preview_2025_03_11` from the top-level `tools` array, as at most `aipapi.MaxTools` type strings (carrier value ≤64 KiB) under the opaque extension key `openairesponses.web_search_declarations`. Type strings only: no declaration attributes, filters, user location, bodies, or conversation content. Function tools stay canonical; hosted declarations are not flattened into function tools; a function/custom tool named `web_search` is not carried; every other hosted type still fails decode; unknown/new variants fail closed.
- The key is deliberately not frontend round-trip metadata, so the existing extension-requirement admission applies. Only the SIWC connector advertises the exact extension and applies omission; every other backend rejects at admission. Runtime admission reads `ResolvedProfile.Capabilities`, so the SIWC connector must return `OpaqueExtensions: true` there and the exact `ResolvedProfile.DialectSupport.ExtensionTypes` entry; `FactoryDescriptor.StaticCapabilities.OpaqueExtensions` mirrors it. `Describe` alone is not admission authority. The policy consumes the carrier before projection and keeps its policy-local omission/strict behavior.
- Empty, malformed, unknown, or over-bound carrier entries are policy refusals classified as unsupported capability, not invalid user context.
- The key literal is defined privately in both modules and pinned by a staged frontend→host→SIWC test with the real `Resolve` profile. No `pkg/aipapi`/`pkg/aipsdk` field or switch is added; generic core sees only an opaque exact extension. This is the requirement 6.8 boundary.
- The `openairesponses` large-payload/`CompileProof` fast path also parses `tools`; it must detect hosted web-search types from the raw `tools` bytes **before** `parseTools` and decline to canonical processing. Because 4.3 changes that shared `parseTools`, 4.3 owns the guard and its `frontendpipe`/`openaicompat` validation; 5.4 owns runtime `Resolve` advertisement and end-to-end admission/omission proof. The proof must show raw-order decline, enable the fast path, and show an ordinary request still takes it in a wire-capable harness (not SIWC). It must not rely on SIWC currently lacking a `WireBackend`.
- The behavior change for other backends is explicit: a request carrying the carrier is rejected at admission unless the backend declares the exact extension. It no longer fails at frontend decode, but it is never silently served without web search. The owner decision records this non-supporting-backend rejection; it changes the error surface but is never silent.
- Current `origin/main` is `64b1df490`; `#1738` changed `openairesponses` function-call history decoding and the SIWC input projection but left `parseTools` unchanged. The carrier work must layer on `#1738` and preserve canonical function-call identity and exact argument bytes.

**Preserved behavior**
- The omission applies only to declarations. Removing the carrier leaves every input item untouched. An input item carrying model-authored search history that cannot be preserved exactly is refused before provider execution, and an upstream `web_search_call` remains a terminal `unrepresentable_output_item` failure. Neither path drops, fabricates, or reorders history.
- Hosted web-search execution is out of scope: the declaration is omitted or refused, not forwarded, so the connector never advertises a search capability it cannot map to canonical events.
- A hosted `tool_choice` remains a frontend decode rejection; this slice carries declarations only. A Codex request that forces a hosted web-search choice is not claimed.
- `namespace` and `additional_tools` carriage remain separate. They are not treated as web-search synonyms, and a default Codex request that sends them still fails until that slice is approved.

## Brownfield Gap Repair

Codebase investigation added requirements that were not obvious from the initial migration idea:

1. Keep the new connector external and separately buildable.
2. Model "always stream upstream" explicitly because AIProxer supports downstream non-stream delivery.
3. Migrate the app-server factory in the same spec because it shares current packaging and OpenAI now documents its SIWC form.
4. Retire private native compaction instead of preserving it by inertia.
5. Reconcile `codex-client-compat` rather than renaming its target.
6. Include backend-plugin manifests, testkit staging, release metadata and dependency cleanup.
7. Use explicit profile selection rather than legacy quota-driven multi-account pooling.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Verdict |
|---|---|---|---|---|
| Refactor `connectors/codex/internal/codex` in place | Replace auth/URLs while preserving engine | Smaller initial diff | Carries obsolete WS/pool/prompt/compaction state and obscures final cleanup | Reject |
| Add a third factory inside `connectors/codex` | Add SIWC beside old kinds | Reuses executable plumbing | Couples official connector to legacy module/dependencies; harder deletion | Reject for direct path |
| New connector module | New executable plugin with SIWC profiles + public Responses | Clean boundary, easy coexistence/removal, aligns with architecture | Some initial mapping duplication | **Selected** |
| Put SIWC in root OpenAI Responses backend | Reuse mature adapter | Small wire diff | Violates provider-auth isolation and root dependency discipline | Reject |
| Leave old app-server untouched | Only replace direct inference | Less work | Leaves duplicate/private auth model permanently | Reject |
| New direct connector + SIWC app-server factory | Shared SIWC profile, distinct execution classes | Matches OpenAI and AIProxer semantics | More migration/test work | **Selected** |

## Design Decisions

### New plugin/module rather than in-place mode
- **Selected**: provisional module `connectors/openaichatgptplan`, plugin id `com.aiproxer.backend.openai-chatgpt-plan`.
- **Factories**: `openai-chatgpt-plan` and `openai-chatgpt-plan-app-server`.
- **Rationale**: official direct inference is a public Responses provider, not "Codex HTTP v3". Clean separation makes legacy deletion mechanical.

### Connector-owned SIWC profile record
- Store validated subject, issued client id, host id, scopes, token set, expiry, optional retained id token/hints, local label and reauth state.
- Reuse `oauthcred` primitives where clean, but do not enlarge its generic record into an OpenAI schema.

### SIWC-specific request projector/policy
- A connector-local projector makes `store:false`, `stream:true`, system/instruction handling, unsupported fields/tools and full-history HTTP behavior explicit.
- `openaicompat` may supply genuinely generic bounded helpers, but SIWC names/policies remain out of that support module.

### Explicit profile selection; no subscription pooling
- Multiple registrations may exist, but quota exhaustion for one is terminal for that selected profile.
- This replaces old API-key-like managed-account rotation semantics.

### Official SIWC app-server remains a separate agent runtime
- It reuses the selected SIWC profile token and launches Codex with documented public Responses provider settings.
- Token rollover restarts the child and resumes thread state through RPC.

### Private Codex compaction is retired
- New direct correctness rests on public full-context replay.
- Exact reasoning replay is revalidated as an optional public Responses capability.

### Destructive cleanup is human-gated
- Old and new connectors coexist until browser registration, real model listing/inference, tool loops and real harnesses are proven by a human operator.

## Risks & Mitigations

- **SIWC preview changes** — centralize policy and link official docs in tests/docs.
- **OIDC/dynamic-registration security errors** — PKCE/state/nonce/JWKS negative tests and bounded callback lifetime.
- **Rotating refresh races** — per-profile serialization + atomic replacement.
- **Host-id churn** — persist once; test restart/concurrent creation/import behavior.
- **Generic support pollution** — keep provider-specific policy/profile metadata in the connector.
- **Loss of native compaction/reasoning continuity** — measure quality separately; never retain private endpoint as fallback.
- **App-server refresh loses thread** — explicit restart + documented `thread/resume` test.
- **Harness regression** — baseline OpenCode + second harness with `codex-client-compat` disabled before removing old bridges.
- **Residual stale config/docs** — repository-wide final grep is a required implementation task.

## References

- OpenAI SIWC overview: https://developers.openai.com/siwc/token-sharing-open-source
- Registration/sign-in: https://developers.openai.com/siwc/token-sharing-open-source/sign-in
- Accounts/sessions: https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions
- Models/inference: https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference
- Preview limitations: https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations
- Codex app-server: https://developers.openai.com/siwc/token-sharing-open-source/codex-app-server
- Errors/recovery: https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery
- Token reference: https://developers.openai.com/siwc/token-sharing-open-source/token-reference