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