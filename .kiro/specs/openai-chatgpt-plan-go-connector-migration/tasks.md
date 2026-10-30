# Implementation Plan

## 1. Establish the independent connector boundary

- [x] 1.1 Add the independently buildable `openai-chatgpt-plan` connector module
  - Create the connector module, plugin command, service skeleton, manifest template, release metadata and parity suite without importing `connectors/codex/internal` or root `internal` packages.
  - Export the direct inference factory `openai-chatgpt-plan`; reserve the app-server factory for Phase 7.
  - The manifest export declares `execution_class: inference`, `access_scope: local_only` and `credential_mode: oauth_user`; the runtime `Describe()` descriptor must match, and the parity suite must assert that agreement against the packaged manifest template.
  - No connector-local single-user/server-mode check: the factory simply declares its posture and stays absent from the host-owned multi-user approval registry, which is the default deny.
  - Observable completion: the backend-plugin host discovers/configures the new factory and `GOWORK=off` build/test succeeds without a root-module dependency on the connector.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.7, 1.8, 1.9, 1.10, 14.7, 14.11, 14.12_
  - _Boundary: backend plugin / composition root_
  - _Depends: none_
  - _Validation: GOWORK=off go test ./... (new module) && make quality-checks_

- [x] 1.2 Add coexistence staging without changing legacy behavior (P)
  - Add testkit/manifest staging for the new connector while leaving existing Codex staging untouched.
  - Observable completion: tests discover old and new connector factories in one plugin root with distinct ids and no aliasing.
  - _Requirements: 1.6, 12.1_
  - _Boundary: tests / config wiring_
  - _Depends: 1.1_
  - _Validation: go test ./internal/testkit/backendplugin/..._

## 2. Build SIWC host/profile storage and authorization

- [x] 2.1 Implement stable host-id persistence
  - Create/load a protected host record and atomic first-start path.
  - Prefer a JWK-thumbprint URI if safe/practical; otherwise use a persisted UUID URI.
  - Observable completion: restart and concurrent-first-start tests resolve one stable, non-PII host id; profile import cannot overwrite it.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6_
  - _Boundary: backend plugin / state adapter_
  - _Depends: 1.1_
  - _Validation: go test -race ./internal/siwc/... (new module)_

- [x] 2.2 Define the SIWC profile record and protected atomic profile store
  - Persist validated subject, issued client id, granted scopes, token set, expiry, optional retained identity hints, local label and reauth status.
  - Reuse `oauthcred` protected-file/redaction primitives where clean; do not widen its generic token schema with OpenAI-only fields.
  - Observable completion: round-trip/version/permissions/symlink/corruption/atomic-replace tests pass.
  - _Requirements: 2.11, 2.12, 3.6, 14.1_
  - _Boundary: backend plugin / state adapter_
  - _Depends: 2.1_
  - _Validation: go test ./internal/siwc/..._

- [x] 2.3 Implement dynamic first registration and returning-account authorization
  - Generate state/nonce/PKCE, select one loopback `redirect_uri`, run the callback listener, use `dynamic_agent_client` only for first registration, send stable host id and product agent name, and retain the returned issued client id.
  - Carry the exact selected `redirect_uri` through the pending authorization state and reuse it byte-for-byte during code exchange; never reconstruct or substitute scheme, host, port, or path.
  - Returning authorization uses the saved issued client id and validates subject before credential replacement.
  - Observable completion: fake auth integration proves exact parameters and exact callback-URI reuse; state mismatch/cancellation/timeout/missing issued-client paths fail safely; a mutated/reconstructed exchange URI is rejected by the test server; `dynamic_agent_client` is never stored as the issued id.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.8, 2.14_
  - _Boundary: backend plugin / driving adapter + app orchestration_
  - _Depends: 2.2_
  - _Validation: go test ./internal/siwc/... ./cmd/aip-backend-openaichatgptplan/..._

- [x] 2.4 Implement OIDC/JWKS and granted-scope validation
  - Validate signature, issuer, audience, expiry, nonce and stable subject; require direct plan-use scope before profile activation.
  - Bound/cache JWKS safely and reject unknown key/algorithm.
  - Observable completion: positive/negative token fixtures pass without raw-token logging.
  - _Requirements: 2.6, 2.7, 2.8, 2.12, 14.1, 14.6_
  - _Boundary: backend plugin / domain policy + driven adapter_
  - _Depends: 2.3_
  - _Validation: go test ./internal/siwc/..._

- [x] 2.5 Implement serialized rotating refresh, explicit profile selection and logout
  - Refresh with issued client id/resource; persist replacement refresh token atomically; classify terminal vs transient failures.
  - Provide list/select/add/reauthorize/logout operations; do not quota-rotate across registrations.
  - Logout resolves the OIDC discovery `revocation_endpoint`, attempts form-encoded refresh-token revocation with the issued client id, retries network/5xx failures with bounded backoff while the refresh token is retained, then distinguishes confirmed remote revocation from local-only sign-out when confirmation cannot be obtained.
  - Observable completion: concurrent refresh calls provider once per profile; terminal rotating-token failure requires reauth; profile switching is explicit; logout tests cover HTTP 200 confirmation, already-invalid token success semantics, retryable failure, and local completion with an explicit unconfirmed-revocation result.
  - _Requirements: 2.9, 2.10, 2.13, 2.14, 8.3, 8.5_
  - _Boundary: backend plugin / app orchestration + state adapter_
  - _Depends: 2.4_
  - _Validation: go test -race ./internal/siwc/..._

## 3. Implement account-specific public model inventory

- [x] 3.1 Add the authenticated public `/v1/models` client and parser
  - Authenticate with the selected profile token, retain list-visible rows, preserve slug/display metadata and bound response bodies.
  - Observable completion: reference-server tests prove exact public path/header and visibility filtering.
  - _Requirements: 7.1, 7.2, 7.3, 7.7, 14.2_
  - _Boundary: backend plugin / driven adapter_
  - _Depends: 2.5_
  - _Validation: go test ./internal/service/... ./internal/siwc/..._

- [x] 3.2 Make inventory profile-aware and eliminate Codex catalog assumptions from the new module
  - Key/invalidate cache by registration identity; never use another profile's catalog.
  - Do not invoke `codex debug models`, Codex binary resolution, client-version gates, private catalog parsing or old shipped snapshots.
  - Observable completion: two-profile tests prove isolation and static architecture checks find no old catalog dependency in the new module.
  - _Requirements: 7.4, 7.5, 7.6_
  - _Boundary: backend plugin / query seam_
  - _Depends: 3.1_
  - _Validation: go test ./internal/service/... + architecture grep/test_

## 4. Implement SIWC Responses projection and preview policy

- [x] 4.1 Add failing canonical-to-wire fixtures before implementation
  - Cover instructions, system/developer/user/assistant history, function calls/results, multimodal input, tool choice, reasoning option, and downstream streaming/non-streaming modes.
  - Assert absence of Codex prompt fallback, `<user_instructions>`, private headers and private endpoints.
  - Observable completion: fixtures define the target wire contract and fail against an unimplemented projector.
  - _Requirements: 4.8, 4.9, 5.1, 5.2, 5.3, 5.4, 5.6, 6.5, 6.6, 6.8_
  - _Boundary: backend plugin / tests_
  - _Depends: 1.1_
  - _Validation: go test ./internal/responses/..._

- [x] 4.2 Implement deterministic instruction/input/tool projection
  - Map `Call.Instructions` to `instructions`; fold explicit system conversation text into legal instruction representation; preserve legal developer/user/assistant/tool chronology and call ids.
  - Preserve supported image/file inputs.
  - Project ordinary canonical `Call.Items` message and tool content lawfully, beyond the message-authority corpus task 4.1 pinned: task 8.2 owns only optional reasoning replay, and task 8.1 owes the integration consumers. Task 4.1's fixtures do not certify every canonical form.
  - Observable completion: fixtures pass and generated JSON contains no Codex identity prompt/glue.
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 6.5, 6.6_
  - _Boundary: backend plugin / driven adapter_
  - _Depends: 4.1_
  - _Validation: go test ./internal/responses/..._

- [x] 4.3 Implement the centralized SIWC preview request policy
  - Force `store:false` and `stream:true`; reject or deliberately omit every documented unsupported field according to explicit canonical semantics.
  - Reject unsupported hosted tools and HTTP `previous_response_id` before network execution.
  - Observable completion: table tests cover every documented unsupported field/tool plus positive minimal/tool/multimodal requests.
  - _Requirements: 4.3, 6.1, 6.2, 6.3, 6.4, 6.7, 6.8, 6.9_
  - _Boundary: backend plugin / domain policy_
  - _Depends: 4.2_
  - _Validation: go test ./internal/responses/..._

## 5. Implement public stream-only Responses execution

- [x] 5.1 Implement public `/v1/responses` HTTP/SSE client
  - Use selected profile bearer token and public base URL; always request upstream streaming; bound request/error/SSE bodies and preserve cancellation.
  - Observable completion: reference server observes only the public endpoint and standard bearer auth, for both downstream delivery modes.
  - _Requirements: 4.1, 4.2, 4.3, 4.6, 4.8, 4.9_
  - _Boundary: backend plugin / driven adapter_
  - _Depends: 4.3, 2.5_
  - _Validation: go test ./internal/responses/..._

- [x] 5.2 Normalize Responses SSE into canonical events and require terminal completion
  - Map text/tool/reasoning/usage/completion/failure events; EOF without terminal is failure.
  - Preserve no-retry-after-visible-output semantics through the managed stream.
  - Observable completion: fixtures prove `response.completed` is the only success terminal and committed output cannot be replayed transparently.
  - _Requirements: 4.4, 4.5, 4.7, 6.5, 8.1, 14.2_
  - _Boundary: backend plugin / driven adapter_
  - _Depends: 5.1_
  - _Validation: go test ./internal/responses/..._

- [x] 5.3 Wire the direct factory Execute path and downstream non-stream collection
  - Use one streaming provider path for both downstream modes; do not add a provider non-stream request path.
  - Verify cancellation and accounting sideband integration.
  - Observable completion: host integration returns a downstream non-stream result while provider capture proves upstream `stream:true`.
  - _Requirements: 4.3, 4.6, 4.7, 8.7_
  - _Boundary: backend plugin / app orchestration_
  - _Depends: 5.2_
  - _Validation: go test ./... (new module) + targeted backendplugin host integration_

## 6. Implement SIWC error and usage semantics

- [x] 6.1 Add structured admission/stream error classification
  - Preserve status/code/request id and bounded diagnostics; classify unsupported capability, route, invalid user/context, temporary unavailable, usage exhausted and revocation/refresh outcomes.
  - Observable completion: provider fixtures produce deterministic typed errors without secret leakage.
  - _Requirements: 8.1, 8.2, 8.4, 8.5_
  - _Boundary: backend plugin / domain policy_
  - _Depends: 5.2_
  - _Validation: go test ./internal/responses/... ./internal/siwc/..._

- [x] 6.2 Prove no automatic cross-profile/model recovery
  - Usage exhaustion terminates selected profile; remove any temptation to reuse PlanTypeHint/GPT downgrade/x-codex recovery semantics.
  - Observable completion: usage-limit fixtures produce one selected-profile provider attempt and no silent model/profile substitution.
  - _Requirements: 2.13, 8.3, 8.6_
  - _Boundary: backend plugin / app orchestration_
  - _Depends: 6.1_
  - _Validation: go test ./internal/service/..._

- [x] 6.3 Integrate completed-stream usage with existing accounting evidence
  - Emit provider usage once through current sideband contracts; do not invent API-dollar price/rating for subscription-funded usage.
  - Observable completion: accounting tests receive one deduplicated usage evidence record with non-secret local profile provenance.
  - _Requirements: 8.7, 8.8_
  - _Boundary: backend plugin / accounting sideband_
  - _Depends: 5.2_
  - _Validation: go test ./... (new module) + accounting contract tests_

## 7. Add official SIWC-backed Codex app-server — deferred

Tasks 7.1–7.3 are deferred, not completed, to [#1633](https://github.com/aiproxer/aiproxer-enterprise/issues/1633) in the `Splitted OSS / Enterprise repos` milestone. The entries below are historical handoff, not executable tasks or closeout blockers for this direct-inference spec. Retain the legacy app-server and its dependencies.

- **Deferred 7.1:** Add failing exact launch/provider tests first
  - Pin public base URL, token env key, Responses wire API, `requires_openai_auth=false`, `supports_websockets=false`, truthful clientInfo name/title/version and secret-safe environment.
  - Also pin the manifest/descriptor posture: `execution_class: agent_runtime`, `access_scope: local_only`, `credential_mode: oauth_user`, matching the runtime descriptor.
  - Observable completion: tests define official SIWC app-server launch and fail against legacy assumptions.
  - _Requirements: 9.1, 9.2, 9.3, 9.4, 9.8, 9.9, 9.10, 14.11, 14.12_
  - _Boundary: backend plugin / tests_
  - _Depends: 2.5_
  - _Validation: go test ./internal/appserver/..._

- **Deferred 7.2:** Implement `openai-chatgpt-plan-app-server`
  - Reuse selected SIWC token; launch Codex with documented public Responses provider; preserve agent-runtime execution class/workspace/process lifecycle.
  - Keep the factory `local_only` and `oauth_user`, and keep it out of the host-owned multi-user approval registry. Do not add a connector-local single-user check.
  - Observable completion: fake app-server receives exact provider config and factory descriptor remains `agent_runtime` with the declared local-only/oauth-user posture.
  - _Requirements: 9.1, 9.2, 9.3, 9.4, 9.8, 9.9, 9.10, 14.11, 14.12_
  - _Boundary: backend plugin / agent-runtime driven adapter_
  - _Depends: 7.1_
  - _Validation: go test ./internal/appserver/... ./internal/service/..._

- **Deferred 7.3:** Implement token-renewal child restart and thread resume
  - On replacement access token, restart child, initialize and `thread/resume` saved thread through documented RPC.
  - Observable completion: fake multi-turn app-server integration continues across token rollover without elevating thread id into core authority.
  - _Requirements: 9.5, 9.6, 9.7_
  - _Boundary: backend plugin / app orchestration_
  - _Depends: 7.2_
  - _Validation: go test -race ./internal/appserver/..._

## 8. Revalidate reasoning/context without private Codex compaction

- [x] 8.1 Prove full-history SIWC HTTP correctness without private continuation
  - Multi-turn canonical calls replay required history, tool trajectories and long context; HTTP never sends `previous_response_id`.
  - Observable completion: correctness survives connector restart because no provider response-id/checkpoint state is required.
  - _Requirements: 6.3, 10.1, 10.2, 10.6_
  - _Boundary: backend plugin / tests_
  - _Depends: 5.3_
  - _Validation: go test ./internal/responses/..._

- [x] 8.2 Revalidate exact reasoning replay as an optional public capability
  - Add deterministic fixtures and an opt-in live probe for exact Responses reasoning items/encrypted content.
  - If unsupported, advertise reduced replay support and remain on public canonical history; no private fallback.
  - Observable completion: result is explicit in capabilities/tests and neither branch invokes private endpoint.
  - Result: exact replay remains unproven and unadvertised; the optional raw provider probe was not run and does not certify production replay.
  - _Requirements: 10.4, 10.5_
  - _Boundary: backend plugin / capability negotiation + tests_
  - _Depends: 8.1_
  - _Validation: go test ./... (new module) + environment-gated live probe_

- [x] 8.3 Keep generic context compaction outside the SIWC provider adapter
  - Revalidate existing canonical context features where semantically valid; do not implement `/responses/compact` or advertise `codex.responses.compaction.v2`.
  - Observable completion: descriptors/parity tests expose no private Codex compaction dialect on the new connector.
  - _Requirements: 10.1, 10.3, 10.7_
  - _Boundary: backend plugin / capability negotiation_
  - _Depends: 8.1_
  - _Validation: go -C connectors/openaichatgptplan test . ./internal/service/... && make parity-checks_

## 9. Reconcile coding-harness compatibility

- [x] 9.1 Prove baseline harness-shaped requests with `codex-client-compat` disabled
  - Exercise OpenCode-shaped and a materially different harness-shaped request through frontend→canonical→new connector, including tools.
  - Observable completion: both complete canonical tool-capable streams without client-name prompt bridges.
  - _Requirements: 5.7, 11.1, 11.2_
  - _Boundary: tests / feature boundary_
  - _Depends: 5.3_
  - _Validation: targeted frontend + backendplugin integration tests_

- [x] 9.2 Classify every current `codex-client-compat` behavior
  - Produce a test-backed keep/generalize/remove matrix; never copy `openai_codex.*` keys into the new backend.
  - Move only truly provider-neutral fixes to generic seams.
  - Observable completion: every current bridge/extension has one explicit disposition backed by tests or live evidence.
  - Assessment: `docs/research/codex-client-compat-disposition.md`; configured instance ID `openai-codex` still activates the legacy hook regardless of connector kind.
  - _Requirements: 11.2, 11.3, 11.4, 11.5, 11.6_
  - _Boundary: feature plugin / research-to-code reconciliation_
  - _Depends: 9.1_
  - _Validation: go test ./internal/plugins/features/codexclientcompat/... + targeted frontend tests_

## 10. Automated conformance gate for the new connector

- [x] 10.1 Certify SIWC auth/profile security
  - Run positive/negative auth, permissions, refresh and focused fuzz tests as appropriate; scan logs/captures for credential leakage. Follow current repository verification policy.
  - Observable completion: no leaked token material and deterministic failure classes.
  - Evidence is limited to offline fixtures and scans. Existing JWKS/OIDC fuzz-oracle false positives are tracked in #1654; no arbitrary callback coverage or universal no-leak claim is made.
  - _Requirements: 2.4, 2.6, 2.9, 2.10, 2.12, 14.1, 14.5, 14.6_
  - _Boundary: tests / security_
  - _Depends: 2.5_
  - _Validation: go test ./internal/siwc/... + targeted fuzz runs_

- [x] 10.2 Certify direct Responses/inventory wire contracts
  - Run exact-path/header/body/SSE/model-list tests and negative assertions for private Codex fields; include downstream non-stream/upstream stream.
  - Observable completion: test server observes only auth endpoints, `/v1/models`, and `/v1/responses` for the direct connector.
  - _Requirements: 4.1, 4.3, 4.8, 5.1, 6.1, 7.1, 14.2, 14.3_
  - _Boundary: tests / contract_
  - _Depends: 3.2, 5.3, 6.3_
  - _Validation: go test ./... (new module)_

- [x] 10.3 Certify direct plugin ABI and architecture boundaries
  - Stage the real direct executable through backend-plugin host tests and run import/dependency checks; preserve the existing app-server. SIWC app-server certification is deferred to #1633.
  - Observable completion: the direct factory negotiates correctly, module builds independently, root/core boundary stays clean, and the legacy app-server remains usable.
  - Offline evidence stages the production executable for negotiation/configuration and a test-only reference executable for successful execution. The module suite skips the opt-in live probe and two helper entrypoints. Connector-specific parity and host tests were run separately; `make parity-checks` does not cover this connector.
  - _Requirements: 1.1, 1.3, 1.4, 14.4, 14.7_
  - _Boundary: tests / architecture_
  - _Depends: 5.3_
  - _Validation: GOWORK=off go test ./... (new module) && make quality-checks && make parity-checks_

## Deferred / Out-of-Plan

Tasks 11.1–14.3 are deferred, not completed and not executable in this direct-inference slice. Resume the direct-inference items through [#1654](https://github.com/aiproxer/aiproxer-enterprise/issues/1654) after `aiproxer-complete-rebranding`, revalidating scope, evidence and paths against the then-current tree. Tasks 13.4–13.5 stay deferred to [#1633](https://github.com/aiproxer/aiproxer-enterprise/issues/1633), and this list does not revive the app-server work. No live or human gate below has run or passed; nothing here authorizes cutover, legacy removal, spec completion or archiving.

### 11. Human empirical acceptance gate — deferred

- [ ] 11.1 Complete a real SIWC registration/profile lifecycle
  - Use an eligible Plus/Pro account and record only non-secret evidence; verify issued client id, direct scope, stable host id, restart persistence and real/safely accelerated refresh.
  - Observable completion: human records PASS tied to tested commit/build, OS and plan class.
  - _Requirements: 12.1, 12.2, 12.11_
  - _Boundary: human / live acceptance_
  - _Depends: 10.1, 10.2, 10.3_
  - _Validation: operator evidence_

- [ ] 11.2 Validate public inventory and direct inference in real use
  - List account models; complete downstream streaming and non-stream requests; capture sanitized evidence for `store:false`, `stream:true`, public endpoints and absence of private Codex headers/prompt.
  - Observable completion: both delivery modes reach `response.completed` through public `/v1/responses`.
  - _Requirements: 12.3, 12.4, 12.8_
  - _Boundary: human / live acceptance_
  - _Depends: 11.1_
  - _Validation: operator evidence + sanitized wire capture_

- [ ] 11.3 Validate real coding harnesses and client-side tool loops
  - Run OpenCode and one materially different harness; execute a function/custom tool and multi-turn continuation; keep `codex-client-compat` disabled for baseline.
  - Observable completion: both harnesses complete useful multi-turn/tool tasks without Codex identity prompt injection.
  - _Requirements: 5.8, 11.7, 12.5, 12.6, 12.7_
  - _Boundary: human / live acceptance_
  - _Depends: 11.2_
  - _Validation: operator evidence_

- [ ] 11.4 Validate direct quota/error behavior
  - Exercise a controlled usage-limit/error condition or closest deterministic supported equivalent; confirm no silent profile rotation.
  - SIWC app-server live acceptance is deferred to #1633; it is not a gate for this direct-inference migration.
  - Observable completion: human records PASS for controlled direct quota/error semantics.
  - _Requirements: 12.9_
  - _Boundary: human / live acceptance_
  - _Depends: 11.3_
  - _Validation: operator evidence_

- [ ] 11.5 Record explicit migration approval
  - Aggregate Tasks 11.1–11.4 into a concise non-secret acceptance record tied to tested commit.
  - App-server acceptance is deferred to #1633; preserve the existing app-server and its dependencies. This direct-inference gate does not authorize their replacement or removal.
  - This checkbox is the hard dependency for every destructive task below.
  - Observable completion: project owner/human operator marks the migration gate PASS.
  - _Requirements: 12.1, 12.11_
  - _Boundary: release governance_
  - _Depends: 11.1, 11.2, 11.3, 11.4_
  - _Validation: explicit human approval_

### 12. Cut over operator-facing configuration after the gate — deferred

- [ ] 12.1 Make the new connector the documented ChatGPT-plan route
  - Update docs/examples/install/release guidance to `openai-chatgpt-plan`; distinguish direct inference from the retained legacy app-server, and document preview restrictions and OSS/local-hosted scope. Do not present the deferred SIWC app-server as implemented.
  - Document the security boundary as credential ownership, not a provider-terms blanket: a same-user local/self-hosted ChatGPT-plan deployment is supported, while a shared `multi_user` deployment rejects the connector. State that the rejection comes from the generic host gate plus the host-owned approval registry and that no configuration override exists.
  - Observable completion: current operator docs no longer recommend `~/.codex/auth.json` or private `backend-api` for subscription inference.
  - _Requirements: 13.1, 14.9, 14.10, 14.13_
  - _Boundary: docs / config wiring_
  - _Depends: 11.5_
  - _Validation: docs/config tests + grep_

- [ ] 12.2 Reconcile `codex-client-compat` using automated/live evidence
  - Remove private-Codex prompt bridges/extensions and backend targeting no longer needed; preserve only behavior justified by Task 9.2/11 evidence.
  - Observable completion: OpenCode + second-harness regression tests pass on the new connector with no `openai_codex.*` dependency.
  - _Requirements: 11.3, 11.4, 11.5, 11.6, 11.7_
  - _Boundary: feature plugin / tests_
  - _Depends: 11.5, 9.2_
  - _Validation: go test ./internal/plugins/features/codexclientcompat/... + targeted integration_

### 13. Remove the legacy private Codex implementation — deferred

- [ ] 13.1 Remove `openai-codex` private HTTP factory and transport
  - Delete factory/export/manifest/release/config paths plus private backend-api HTTP/WS, Codex CLI OAuth refresh, private headers, conversation/session transport affinity and default Codex identity instruction only where not required by the retained app-server; preserve shared code and prove the app-server still works.
  - Observable completion: no production code can construct or route to the legacy direct factory.
  - _Requirements: 13.1, 13.3, 13.8, 13.10_
  - _Boundary: backend plugin_
  - _Depends: 11.5, 12.1_
  - _Validation: go test ./connectors/codex/... during deletion + repository grep_

- [ ] 13.2 Remove obsolete account-pool, downgrade, quota and verbosity-turn code
  - Delete unreachable managed-Codex pool, plan hints/model downgrade, quota-header storage, session affinity/turn bumps and stale config/docs; prune dependencies used only there.
  - Observable completion: no supported factory references these types/fields and static/dead-code checks are clean.
  - _Requirements: 13.4, 13.9_
  - _Boundary: backend plugin / docs_
  - _Depends: 13.1_
  - _Validation: go test ./connectors/codex/... && make quality-checks_

- [ ] 13.3 Remove private native Codex context/compaction machinery
  - Delete `/responses/compact`, `codex.responses.compaction.v2`, private checkpoint/continuation state and compaction accounting sideband when no supported factory uses them.
  - Keep archived historical specs as evidence, not production obligations.
  - Observable completion: no supported descriptor/call site exposes the private compaction dialect.
  - _Requirements: 10.1, 10.3, 10.7, 13.6_
  - _Boundary: backend plugin / tests_
  - _Depends: 13.1, 8.3_
  - _Validation: repository grep + make parity-checks_

- **Deferred 13.4:** Replace/remove legacy `openai-codex-app-server` — [#1633](https://github.com/aiproxer/aiproxer-enterprise/issues/1633)
  - Remove old factory id after the SIWC app-server is proven; reuse/delete shared ACP helpers according to remaining callers; keep no permanent old-auth alias.
  - Observable completion: only the SIWC-backed app-server factory is production-visible.
  - _Requirements: 13.2, 13.5_
  - _Boundary: backend plugin / manifest_
  - _Depends: 11.5, 7.3, 12.1_
  - _Validation: connector parity suite + manifest/release gate_

- **Deferred 13.5:** Remove or archive the old `connectors/codex` module — [#1633](https://github.com/aiproxer/aiproxer-enterprise/issues/1633)
  - If no supported production factory remains, delete module/command/manifest/release metadata/replaces/packaging. Move any genuinely generic helper to the correct support module first rather than keeping a mostly dead connector.
  - Observable completion: package/release inventory contains only the new SIWC connector for ChatGPT-plan/Codex-plan integration.
  - _Requirements: 13.1, 13.2, 13.5, 13.9, 13.10_
  - _Boundary: backend plugin / packaging_
  - _Depends: 13.2, 13.3, 13.4_
  - _Validation: connector release gates + GOWORK=off builds_

### 14. Final repository-wide verification and closure — deferred

Tasks 13.4 and 13.5 are historical handoff to #1633, not closeout blockers or authorization to remove the retained legacy app-server. Direct cleanup must preserve its production dependencies; classify its necessary references as intentional.

- [ ] 14.1 Run residual-reference and architectural cleanup audit
  - Search production code/config/docs/tests for legacy ids, private Codex endpoints, `codex_cli_rs`, `chatgpt-account-id`, old Codex OAuth client id, obsolete `openai_codex.*` extensions, private compaction dialect and default Codex identity prompt.
  - Classify remaining hits as intentional archived/historical evidence or remove them.
  - Also confirm no connector-local single-user/server-mode check was introduced: no import of host `internal` packages, no locally declared multi-user approval/eligibility authority, and no `allow_*_in_multi_user` configuration knob.
  - Observable completion: zero unexplained production references remain and the connector subtree contains no local access-mode decision.
  - _Requirements: 13.7, 13.8, 14.7, 14.13_
  - _Boundary: repository-wide verification_
  - _Depends: 13.1, 13.2, 13.3_
  - _Validation: scripted grep/architecture check_

- [ ] 14.2 Run release-quality gates
  - Run new connector tests, root unit suite, parity, quality checks, connector release gates and appropriate wide QA under current repository verification policy; recheck logs/captures for SIWC secret leakage.
  - Observable completion: deterministic required gates pass and Task 11 live evidence remains tied to the release lineage.
  - _Requirements: 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.8_
  - _Boundary: tests / release governance_
  - _Depends: 14.1_
  - _Validation: make quality-checks && make test-unit && make parity-checks && make qa plus connector release gates_

- [ ] 14.3 Final documentation/release review
  - Ensure current docs state supported deployment scope, direct-vs-retained-legacy-app-server semantics, profile security, preview limitations, route migration/removal and reauthorization behavior.
  - Observable completion: no current operator guide recommends the retired private direct-inference route; documentation for the retained legacy app-server remains accurate.
  - _Requirements: 14.9, 14.10_
  - _Boundary: docs / release governance_
  - _Depends: 14.2_
  - _Validation: docs review + link/config checks_

## Implementation Notes

- Task 1.1: adding a new connector module breaks existing repository gates that enumerate connectors, even though the root module gains no dependency on it. A new `connectors/<name>/go.mod` must be registered in `.github/dependabot.yml` (the `dependency-sync` workflow runs `scripts/tidy-all-modules.sh --check` on any `connectors/**/go.mod` change), in `internal/providerprofiles/expected_inventory_test.go`'s `preExistingConnectors`, and in `internal/testkit/contract/backend/terminal_connector_matrix_test.go`'s `connectorTerminalMatrix` (use the existing `connectorDeferredRow()` helper for a connector whose terminal concerns are not yet proven). Validate every task with the full root suite plus `scripts/tidy-all-modules.sh --check`: a connector-scoped validation set passes while these root gates are red.
- Task 1.1: the host's inventory aggregation fail-softs on any `ListModels` load failure and reports an `unavailable` discovery, so an unimplemented inventory must return an explicit error. Returning a successful empty catalog is read by the host as a fetched catalog that found no models, which hides the degradation.
- Task 1.1: an unavailable operation must not report fetched provenance. The host maps a zero-model success to `empty` and a load failure to `unavailable`, so a not-yet-implemented inventory has to travel the error path to stay distinguishable.
- Task 1.2: one discovery root can hold several connectors with no discovery, catalog, trust or adapter change. Discovery already scans a root's directory entries for every `*.backendplugin.json` and gives each a distinct safe id, and the catalog only conflicts on a duplicate `plugin_id`. A coexistence root therefore needs distinct plugin ids and distinct manifest FILE names, not new host machinery.
- Task 1.2: the legacy `openai-codex` HTTP export's `Configure` needs one host-delivered `api_token` slot that only the composition root's static-inference provisioning lane supplies, so that export is provable at resolution level from a testkit staging root. The credential-free legacy app-server export configures for real from the same root.
- Task 2.1: a configured state path is an operator's SPELLING, not a file identity. The operating system resolves a path component by component and applies `..` AFTER following a symlink, while `filepath.Clean`/`filepath.Dir` apply `..` BEFORE it, so the two disagree wherever a symlink or a `..` is involved. Resolve the spelling once to a physical location and make load, parent protection, publication and durability all use that one location; `filepath.Clean` is not a substitute, because it silently changes which record the path names.
- Task 2.1: durability has three separate obligations, and satisfying one does not satisfy the others. The bytes must be flushed before the name becomes reachable; the name's own directory entry must be flushed, and a directory fsync persists the entries INSIDE it, not that directory's entry in its parent, so the ancestor chain must be flushed too; and every path that ACKNOWLEDGES the result must confirm the durability itself, because the process that published it may be the one whose sync later fails. A path-determined confirmation (the same chain derived from the record path, not from what this call happened to create) is what makes that hold between independent processes.
- Task 2.1: narrow a decoded code point to a byte and you can alias a legitimate name character onto a separator - `Я` (U+042F) and `į` (U+012F) both have a low byte of `0x2f`, which is `/`. Trim path separators as BYTES, never as runes.
- Task 2.1: `connector-support/oauthcred/store.go` has the same path-identity defect class (a lexical `filepath.Dir` for the temp file and the flush, while `os.Rename` publishes into the OS-resolved directory; `Delete` flushes `filepath.Dir` too). It was left untouched here as out of scope, but five connectors use it, so it needs its own issue. Task 2.2 must reuse the SIWC location contract rather than copy that pattern.
- Task 2.2: a JSON parse error is not universally value-free. `time.Time.UnmarshalJSON` includes the rejected string in its error, so wrapping it with `%w` put credential material into a support-facing error. The credential parsing boundary must CLASSIFY the failure - a malformed document, a wrong type, an invalid timestamp - and never wrap a value-bearing parser cause.
- Task 2.2: an identifier that becomes a file name must be injective on a case-insensitive filesystem, or two accepted ids share one file and the second save destroys the first registration's only copy of its refresh token. A canonical lowercase ASCII id makes every accepted id's file name already case-folded, so distinct ids cannot collide; a differently cased id is REFUSED, never lowercased into an existing key.
- Task 2.2: fmt only dispatches to a `Formatter` it can reach through an interface, and it handles some verbs first. `%T` is safe, but `%w` (non-error operand) and `%p` (non-pointer operand) are rendered through the raw struct path, and a record held in an unexported field or supplied as a non-interfaceable `reflect.Value` is rendered raw for an ordinary `%v` too. A secret-bearing type needs a Formatter AND a documented set of paths that bypass it; at an operational sink, prefer a structural summary over formatting the record.
- Task 2.3: four boundary rules decide whether a connector handles an external value correctly. **COLLECT vs SAMPLE** — every arrived value is collected, or the input is refused; never sample the first and drop the rest (`url.Values.Get` returns only the first value, and `r.URL.Query()` silently discards `url.ParseQuery`'s error, so a field with a bad escape or a raw semicolon vanishes). **CHECK vs ASSUME** — an external outcome is observed, never assumed (a detached launch that cannot resolve its executable still exits successfully, so the executable is resolved before detaching). **AUTHORITY vs COPY** — a value that decides attribution comes from the authority, not from a copy (a profile's recorded `host_id` is provenance; the host record is the authority). **REDACT BEFORE BOUND** — redaction runs before the length bound, or a secret crossing the bound keeps a visible prefix.
- Task 2.3: a JSON parse error is not value-free. `time.Time.UnmarshalJSON` includes the rejected string in its error, so a credential placed in a malformed `expiry` reached a support-facing error until the decode boundary classified the failure instead of wrapping the cause.
- Task 2.3: refusing a malformed OAuth callback narrows no client. RFC 6749 §3.1 forbids repeating a request parameter, so a conforming callback carries one value per parameter and the refusal never fires for it; the vision's "no leakage" line is absolute and outranks the interoperability preference here.
- Task 2.3: a security regression is only evidence if it DISCRIMINATES. Restore the pre-fix file wholesale through a `go test -overlay` (a fragment breaks cross-file symbols) and show the test fails; a test that still passes with the leak restored proves nothing.
- Task 2.4: **ARRIVED-BUT-UNUSABLE IS NOT ABSENT.** Absence is the one state that grants latitude — it enables a fallback, skips a check, or keeps a stale authority alive — so a value that arrived but is unusable must be refused, never treated as omission. Read every member of every provider document in three states (absent / invalid / usable), and read security-bearing members by their EXACT JSON name: a typed decoder matches struct tags case-insensitively, so a token carrying both `aud` and `AUD` can pass an audience check.
- Task 2.4: a test must fail for the rule it names. Check that the fixture is not refused for a different reason — a repeated key, a malformed document, or a missing field — and that removing the rule under test makes the case ACCEPT rather than merely change its error class. A test that cannot fail for the reason it names manufactures confidence, and it should be deleted with the argument rather than kept.
- Task 2.4 (deferred to task 2.5): a discovery failure still makes one request per attempt, with no backoff inside the renew window. The sign-in and refresh recovery policy belongs to the next slice; a backoff there would make a sign-in fail for a fixed period after a transient failure.
- Task 2.4 (follow-ups, non-blocking): N1 — the token response's integrality check runs AFTER the decimal-to-float conversion, so a value such as `3600.0000000000001` rounds to 3600 and is accepted; check mathematical integrality from the retained JSON number text (including exponent syntax) before the float conversion, and keep the JWT's fractional NumericDate semantics separate. N2 — the malformed-JWK rejection fixture can pass for missing or unusable material; seed a valid sibling, mutate only the named field, and prove that skipping that malformed candidate fails the test. N3 — inherited task-2.3 debt: the token-exchange body read truncates at the bound instead of detecting limit+1 the way the discovery and JWKS reads do; detect limit+1 and return a constant oversize failure.
- Task 4.1: a correct VALUE is not a renderable LITERAL. The exact-decimal comparison moved the decimal point with the exponent, so the integer part kept the leading zero it was written with: `0.5e2` became `050`, which is not a valid JSON number, and the comparison could not render the document at all — the rule rejected two documents that denote one value. Strip the redundant leading zeros of the INTEGER part only (the fraction's leading zeros are significant: `0.001e1` is `0.01`, and `0.001` stays `0.001`), keep a single `0` when the integer part is zero, and canonicalise the signed zero AFTER that step, so `0` and `-0` stay one value. A normaliser must be checked for the validity of what it EMITS, not only for the value it computes.
- Task 4.1: keep the auth strict reader and the wire-contract oracle independent. Reconsider connector-local internal extraction only when a real consumer demonstrates shared parsing behaviour; preserve constant auth errors versus developer-facing duplicate/path diagnostics. Decimal comparison is a separate test-only responsibility. If a later reuse is genuinely cross-connector, design's `connector-support/openaicompat` is an allowed generic-support home, not a mandatory destination for this connector-local concern. Never place SIWC policy in `pkg/aipapi` or generic core.
- Task 2.5: the owner ruled the withdrawal question as option A. A cancelled caller whose FINAL revocation attempt gets a completely-read nonconfirming response (for example HTTP 503) keeps the saved credential and receives an error. A confirmed HTTP 200 keeps local completion. Withdrawal is consulted only at the nonconfirming classification sites, not as a blanket check after every response.
- Task 2.5: a directory-flush failure after a successful rename is not a failed publication. Publication visibility, durability confirmation and supersession are three separate facts. An unconfirmed commit asserts neither "cleared" nor "NOT marked" unless that state was established, and recovery never treats the connector's own just-published replacement as a superseding credential.
- Task 2.5 (follow-up, non-blocking): the logout result can contradict a confirmed revocation when clearing publishes but its durability is unconfirmed (`RemoteRevocationConfirmed=true` alongside guidance that revocation was not confirmed). Track a logout-result hardening change: keep confirmed remote facts, distinguish clearing publication from its durability, and add the directory-flush regression. Setting a boolean without deciding that durability contract is not sufficient.

- Task 3.1: a refusal is proven only by a test that fails when the refusal is replaced by a silent drop. A visible row without a usable slug or display name, and a visible row with an absent slug, must each fail with no partial list. Absent `display_name` stays accepted because it is optional.
- Task 3.1: a bound is proven at ACQUISITION, not after it. A test that rejects an oversize body after `io.ReadAll` cannot detect an unbounded read. The refused answer is closed unread: the earlier deferred drain was removed because it spent bytes the connector had no use for, and that removal is itself asserted.

- Task 3.2: never stamp a response with a credential generation observed AFTER the fetch. The cache entry carries the provenance of the token that was SENT; when the post-read record disagrees, the in-flight answer is returned live and nothing is published, so the next call fetches with the replacement. Stamping with post-read identity poisons not only overlapping calls but the next non-overlapping call too.
- Task 3.2: an expired entry is not a fallback answer. The failed-read test must first prove a genuine cache hit (one provider request) before the TTL advance, or it proves ordinary error propagation and not refusal to serve the expired catalog.
- Task 3.2 (follow-up, non-blocking): an older-generation reader can unconditionally forget a concurrent valid entry (inventory.go reconciliation). That only loses a cache hit; the next call fetches with current credentials. An optional conditional delete could avoid the extra work.

- Task 4.2: a tool result has TWO carriers and the call has ONE authority. Literal `Part.Text` (what `aipapi.ProjectItemsToLegacyView` emits) and JSON `Part.Content` are both preserved; two populated carriers must decode to the same output or the call is refused. With complete canonical identity (`ToolCallID`+`ToolName`), `Content` is argument data verbatim — never inspect it as an envelope first; a `type` member inside arguments is an ordinary tool parameter.
- Task 4.2: normalization is for CHECKS, never for rewriting one side of a join key. Canonical ids permit surrounding whitespace and the legacy projection copies them exactly, so the projector must emit the authored bytes on every path (item, message-authority, envelope, Chat Completions fallback) or refuse atomically. Trimming the call id while the result keeps its bytes orphans the answer.
- Task 4.2 (follow-up, non-blocking): the nested Chat Completions `function.name` padding is not pinned (a trim-only mutant survives; production returns the authored name by inspection). Optional hardening: one padded nested-name table case asserting the exact emitted name and result join.

- Task 4.3: FORCE/REFUSE/OMIT is a triage, not a dial. `store:false`/`stream:true` are forced constants read by both projector and guard; continuations without history, hosted tools and wire `previous_response_id` are refused with zero Admission before network; generation options, cache hints and protocol extensions are only OMITTED, and every omission is returned in `Admission.Omissions` (default posture) or refused (strict posture). A silent drop is never an omission.
- Task 4.3: round-trip metadata is not a protocol requirement. The policy mirrors the canonical frontend-metadata key set locally (the authority stays unexported in core); the parity test pins every NAMED form, and an additive authority change needs a manual mirror update — it is not auto-discovered.
- Task 4.3: policy wraps projection, it does not rewrite it. `AdmitCall` clones and changes only continuation/cache carriers before invoking `ProjectCall`; inherited projector refusals propagate unchanged (no PolicyError wrapper for uniformity).

- Task 5.1: a transport error is not a diagnostic string. Generic body-read failures are flattened to the constant `ErrStreamUnreadable` (EOF, cancellation, closed-stream and size sentinels keep identity); the raw cause is neither wrapped nor retained, because `net/http` propagates provider-controlled text (e.g. a malformed trailer echoing the bearer) through that path.
- Task 5.1: the raw seam dispatches on field PRESENCE, not payload. A `data` field contributes its newline even when empty; a colon-less line is a field with an empty value. Frame-local state AND per-frame accounting reset at every blank separator; the whole-stream cap is independent.
- Task 5.1 (follow-up, non-blocking): N1 — close identity (`Next` after explicit `Close` → `ErrStreamClosed`) and name reset into a subsequent UNNAMED frame are not directly pinned by repository tests (judge probes pass; regression-strength gap only). Optional table cases.
- Task 5.1 (follow-up, non-blocking): S2 — successful SSE responses still discard the request-id header. Capture the sanitized request id in the raw stream (or another transport-owned envelope) in 5.2/6.1 before closing the error surface.

- Task 5.2: only `response.completed` with a usable resource is success. Terminals must be JSON objects; null/non-object/resource-less terminals are `malformed_event`; `incomplete`/`failed`/`cancelled`/stream errors are typed failures; EOF is returned ONLY after the completed terminal. A failure is atomic: the first error is terminal and the never-returned pending queue is discarded.
- Task 5.2: tool identity is provisional until committed. An explicit `call_id` re-keys an UNSTARTED provisional item-id state (buffered arguments migrate, no duplicate starts); a started identity never changes; a competing `call_id` or adoption into a finished target is contradictory. Dedup is by full part identity (item + carrier + content/summary index), not item alone.
- Task 5.2: error diagnostics carry provider code/message (top-level first, then nested, then resource) redacted-before-bound. The transport envelope keeps the sanitized request id (5.1 S2 resolved); F2 notes the contradiction Code is a local discriminator, which 6.1 must distinguish from provider recovery codes.
- Task 5.2 (follow-up, non-blocking): S1 — permitted adoption (unstarted provisional into an ACTIVE already-started target) lacks a positive repository regression; an over-refusal mutant survives the suite while failing full-stream behavior. Optional active-target buffered-adoption fixture.
- Task 5.2 (follow-up, non-blocking): the six deferred observations stand (status-vs-error precedence, sentinel metadata, `call_id`-on-argument legacy fixtures, nameless-start repair, unindexed-part fallback, three-tier precedence table) plus F1 (argument-completion lifecycle for `arguments.done` vs `tool_call_finished`).

- Task 5.3: bind admission to the credential, not the moment. An attempt resolves the selection ONCE and hands that bound record down: admission, token acquisition and the pre-I/O re-validation (usability, direct plan-use scope, grant generation) all name the SAME identity, and the transport never re-reads mutable selection (`OpenStreamForAttempt`). Post-open ProfileID comparison is too late — the forbidden request already happened.
- Task 5.3: a test-only endpoint is not a config knob. The shipped production configuration has NO endpoint key (`responses_base_url` is refused by name); production always addresses the documented public resource. Reference routing lives only in a test-only constructor + staged executable path that production Configure cannot reach. Loopback constrains location, not execution context.
- Task 5.3 (follow-up, non-blocking): S1 — the accounting `DedupeKey` names the local profile, not one provider charge. Owned by 6.3: prove per-attempt/provider-response identity with local profile provenance (two-attempt contract proof), without inventing money logic.
- Task 5.3 (follow-up, non-blocking): S3 — the grant-generation re-validation is correct but lacks a discriminating repository regression (removing only the generation-equality refusal leaves the suite green). One focused acquisition-boundary generation-mismatch case with zero provider I/O.

- Task 6.1: SOURCE is who reported the failure, not who answered the HTTP call. Provider-sourced means the provider's error document/status/code; a connector judgement of provider bytes (200 non-SSE, unreadable 200 refresh body, refused redirect, raw transport failure, local admission refusal) is `local` with observed status/request-id preserved and an EMPTY provider code. Verified at every seam: Responses answers, refresh outcomes, and the S1 zero-value comment.
- Task 6.1: precedence is outcome-first, cancellation-second. Typed classes (already-classified, caller-cancelled, typed 2.5 refresh outcome) beat untyped nested deadlines; a bare 429 is retryable throttling, never invented plan exhaustion — only the NAMED plan-limit code is non-retryable `usage_exhausted`. Raw stream failures keep envelope provenance (request/profile ids) under the constant sentinel.
- Task 6.1: admission is closed structurally. `ProjectionError`/`ErrCallNotRepresentable` → local `unsupported_capability` (reason preserved); malformed canonical input → local `invalid_user_context`. No string parsing, no local detail in provider diagnostics. FYI limits kept: success-status OAuth error documents and normalized unknown-code placeholders are future-slice concerns, not this task's contract.

- Task 6.2: exhaustion is a verdict on ONE attempt, not a routing hint. The named plan-limit outcome terminates the selected registration's attempt after exactly one provider request (selected credential, selected model) with a terminal typed failure. No same-profile retry, no rotation to a saved registration, no fallback model. A later attempt is the HOST's decision; this connector never substitutes silently.
- Task 6.2: the typed class is local to the returned error (J1). The gRPC/host-visible terminal stays the generic non-retryable terminal — requirements 2.13/8.3/8.6 demand stopping without substitution, not a structured host code. Whether a future mapping needs existing-field use or a new contract is undecided and out of scope.
- Task 6.2 (follow-up, non-blocking): J2 — pin the wire stop signal in the committed test (observed failure terminal has a non-nil, non-retryable PluginError). Judge-probed green; the committed assertion is missing.
- Task 6.2 record correction (J3): the static scan holds 23 markers across six families (not 24 as the implementer artifact states); it is a lexical tripwire over production files (comments included, renames may evade) — extend markers only with actual retirement work.

- Task 6.3: the charge is the PROVIDER's correlation, not the diagnostic. Accounting keys use the full redacted identity (`RequestIdentity`, before the 128-rune display bound); the display form stays truncated. Encodings are namespaced by source (`req:` provider, `attempt:` fallback, `hash:` digest-of-namespaced-segment), so literal spellings of another encoding cannot collide; redelivery keeps a stable key under the 256-byte bound.
- Task 6.3: one completed stream, one record. Emission requires `ResponseStatus == "completed"` with counter presence; usage accumulates additively (last-nonzero total, unioned presence). The identity_source marker is an operator log (never key material); identification is not delivery (the buffer owns acceptance). No money logic anywhere: counts only.
- Task 6.3 (post-approval, applied directly): F1 — the emission log claimed "emitted"; reworded to "identified" with the buffer-acceptance caveat (gating would need an SDK API change, out of scope). F2 — committed a beyond-display-bound secret case for the full identity; a no-redaction mutant fails it with `bearer_leaked=true`. Redaction is redact-before-bound; sanitization (controls/whitespace) also applies.
