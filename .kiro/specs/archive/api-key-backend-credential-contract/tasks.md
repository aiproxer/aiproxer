# Implementation Plan

## Delivery constraints

**Brownfield, contract-completion umbrella.** Every discovered gap G01–G19 in research.md belongs to these 12 leaf tasks. Each task must be completed by an independently reviewable implementation slice (split an oversized task into successive small PRs without dropping its acceptance criteria). Follow .kiro/steering/delivery.md: no new test matrix, no standalone test-only task, no weakening of architectural boundaries. Keep stock Go LIP configuration, plugin API and non-secret doc examples in sync with each slice. The final umbrella is done only after all tasks/required checks close.

## Tasks

- [x] 1. Establish closed credential-class and environment-namespace registration (foundation)
  - Extend SDK/manifest parser and validated descriptor comparison with a **versioned nonsecret static-key contract**, unique provider root, secret slot and selection mode; keep nonstatic v1 compatibility but fail closed on unclassified static-key exports.
  - Enumerate all built-in/profile/executable kinds, including every Ollama Cloud export, and make approved same-provider aliases explicit. Each new API-key connector is rejected without its registration contract.
  - _Requirements: 1.3, 1.4, 4.4, 5.1, 5.3_
  - _Boundary: SDK/public contract; manifest/catalog validation; host security profiles_
  - _Depends: none_
  - _Validation: go test ./pkg/lipsdk/backendplugin/... ./internal/infra/backendplugins/manifest/... ./internal/archtest -run 'Manifest|Descriptor|BackendPlugin'_

- [x] 2. Replace both hard-coded numbered-key scanners with one policy-driven host resolver (P)
  - Add nonsecret upstream_credentials max_env_index (default 20, bounded 1..256) and quota_switch_retry_after (default 1h). Implement deterministic sparse scanning, slot de-duplication, explicit root_1 rejection, value-size budget and secret-free errors; delete Alibaba persistent Windows environment override.
  - Integrate resolver via authorized candidate snapshot rather than request-time os.Getenv; include collision and failed-reload tests.
  - _Requirements: 1.1, 1.2, 1.5, 2.5_
  - _Boundary: core config; composition root; infrastructure resolver_
  - _Depends: 1_
  - _Validation: go test ./internal/standardplugins ./internal/core/config ./internal/infra/runtimebundle -run 'Env|APIKey|Credential|Reload'_

- [x] 3. Remove literal-static-credential configuration schemas and fallbacks
  - Strip API key/token/PAT/private-key fields from built-in/plugin-owned config structs and nested credential YAML, including interpolation aliases. Central path-aware validation rejects legacy fields before opaque plugin YAML delivery and reports field path + env root only.
  - Remove static-key auth.json/dotenv/credential-file fallback and keep OAuth/workload credential stores behind explicitly classified different modes. Migrate examples and negative decoder tests as each owner changes.
  - _Requirements: 2.1, 2.2, 2.4, 5.4_
  - _Boundary: core config; backend/plugin decoders; connector-private config schemas_
  - _Depends: 1, 2_
  - _Validation: go test ./internal/core/config ./internal/standardplugins ./internal/archtest -run 'Config|APIKey|Forbidden|Credential'_

- [x] 4. Add a separately authorized static-inference secret-provisioning lane (P)
  - Preserve existing agent_runtime/local_only capture gate; add static API-key inference capture from host-only approved namespace roots with complete pre-lookup security validation.
  - Deliver bounded numbered slots through existing authenticated ConfigureRequest.Secrets, never child env/argv/YAML; bind credentials into physical resource identity and prove candidate rollback, multi-user approval, lost-peer denial and generation cleanup.
  - _Requirements: 2.3, 2.5, 4.2, 5.3_
  - _Boundary: infrastructure runtimebundle/backendplugins; secure composition and resource ownership_
  - _Depends: 1, 2_
  - _Validation: go test ./internal/infra/runtimebundle ./internal/infra/backendplugins/... -run 'Provision|Capture|Activation|Identity|MultiUser'_

- [x] 5. Implement one ordered quota-aware credential coordinator and SDK slot decoder
  - Extend pkg/credpool to distinguish a transient same-key short throttle from long quota exhaustion; configure strict Retry-After >1h threshold; parse HTTP dates and deltas safely; keep first-key preference, auth-invalid and bounded attempts with injected clock.
  - Supply connector-support/SDK validation of canonical ordered Configure slots without protobuf/ABI expansion. Prove missing/bad/overflow hints, concurrent requests, cancellation, secret-free snapshots and no automatic rotation on a 30-second 429.
  - _Requirements: 3.1, 3.2, 3.3, 3.5, 4.4_
  - _Boundary: pkg/credpool; backend adapters; SDK connector support_
  - _Depends: 1, 2_
  - _Validation: go test -race ./pkg/credpool ./internal/plugins/backends/openaicred ./pkg/lipsdk/backendplugin/..._

- [x] 6. Migrate official built-in API-key families to env-only quota policy (P)
  - Move OpenAI legacy/Responses, Anthropic, Gemini and Alibaba Token Plan through the new resolver and coordinator. Remove accepted literal YAML keys/structured key metadata and old fallback constructors; preserve model inventory, streaming and existing core failover semantics.
  - Include HTTP 401/429 with short/long/unknown Retry-After and Gemini RetryInfo in local emulators; provider-specific 403/402 must not silently rotate.
  - _Requirements: 2.1, 3.1, 3.2, 3.3, 4.1_
  - _Boundary: internal/standardplugins; internal/plugins/backends provider adapters_
  - _Depends: 3, 5_
  - _Validation: go test ./internal/standardplugins ./internal/plugins/backends/openailegacy ./internal/plugins/backends/openairesponses ./internal/plugins/backends/anthropic ./internal/plugins/backends/gemini_

- [x] 7. Complete compatible and profile family key selection (P)
  - Replace compatmode.FirstAPIKey-only execution in OpenResponses and SystemOne, including streaming/compact/nonstreaming/model inventory, with the shared selector; converge custom OpenAI/Anthropic families and embedded provider-profile roots.
  - Verify different project/account roots stay isolated, intentional provider aliases remain explicit, and no source file or literal config key is accepted.
  - _Requirements: 1.4, 3.1, 3.4, 4.1, 4.3, 4.4_
  - _Boundary: internal/plugins/backends/{compatmode,openresponsescompat,systemonecompat,openaicompat,anthropic}; provider profiles_
  - _Depends: 3, 5_
  - _Validation: go test ./internal/plugins/backends/openresponsescompat ./internal/plugins/backends/systemonecompat ./internal/plugins/backends/openaicompat ./internal/standardplugins -run 'Credential|APIKey|429|Profile'_

- [x] 8. Reconcile optimized large-body wire retries with safe replay
  - Add a replayability proof/factory at the existing largebody wire boundary; when exact bytes can safely be replayed before any output/effect, use the same credential coordinator on 429/auth-invalid.
  - When request input is consumed/non-replayable or effect/output may have started, update future credential state but return safely without retry, runaway buffering or double provider execution; validate cancel/close and B-leg ownership.
  - _Requirements: 3.4, 3.5, 4.3_
  - _Boundary: internal/core/largebody wire contract; internal/plugins/backends/openaicompat; core routing unchanged_
  - _Depends: 5, 6_
  - _Validation: go test ./internal/core/largebody ./internal/plugins/backends/openaicompat ./internal/core/runtime -run 'Wire|Replay|LargeBody|Credential'_

- [x] 9. Convert external direct API-key connectors: OpenRouter, NVIDIA, Hugging Face, CommandCode, OpenCode and Ollama Cloud (P)
  - In independently reviewable connector-specific PRs, use host/SDK numbered slots, remove all literal YAML api_key fields, OpenCode auth.json and CommandCode interpolation/direct os.Getenv; give CommandCode exports separate canonical roots and correct Ollama Cloud manifest posture.
  - Keep OpenCode Go/Zen scopes distinct, provider-specific errors and existing transport behavior. Every export must pass module-local secure Configure and quota/no-file tests; do not log secrets.
  - _Requirements: 1.3, 2.1, 2.2, 4.1, 4.2, 4.5, 5.1_
  - _Boundary: connectors/{openrouter,nvidia,huggingface,commandcode-openai,commandcode-anthropic,opencode,ollama}; connector-support_
  - _Depends: 3, 4, 5_
  - _Validation: go test ./connectors/openrouter/... (from module); similarly each affected connector module, plus tools/backendplugin contract checks_

- [x] 10. Convert external token/PAT API providers: Azure key-mode, Cloudflare, Cohere, Databricks, Infomaniak, Replicate, Snowflake and watsonx (P)
  - Adapt key exchange and bearer/PAT provider modes to host-issued ordered slots with provider-appropriate quota classifiers, shared env roots, explicit quota domains and retained secure Configure-only startup.
  - Keep Azure Entra/OAuth variants and token refresh under their authenticating key isolated; add per-module short/long/unknown 429 checks and verify no YAML static credential values.
  - _Requirements: 2.1, 2.3, 3.1, 3.2, 4.1, 4.2, 4.4_
  - _Boundary: listed connectors modules and their existing auth/token provider seam_
  - _Depends: 3, 4, 5_
  - _Validation: go test in each affected connector module; run module-local contract/parity suites, not root-wide Cartesian test matrix_

- [x] 11. Harden mode-edge connector schemas and session admission (P)
  - Remove static key-bearing YAML/file fallbacks from Cursor SDK, Codex HTTP static token, SAP AI Core service-key and optional-auth local Ollama/llama.cpp/LM Studio/vLLM; classify AWS/OCI/Vertex workload sources separately without breaking provider-managed credential chains.
  - For Cursor SDK, bind env key per safe agent-session admission and never replay after effects; confirm maintained standalone artifact migration before declaring release closure. For Codex subscription/managed OAuth, no indiscriminate quota pooling; validate manifest class and access scope explicitly.
  - _Requirements: 2.1, 2.2, 2.4, 3.4, 4.5, 5.4_
  - _Boundary: relevant connector-specific configuration/auth, manifest and standalone connector release contract_
  - _Depends: 3, 4, 5_
  - _Validation: go test in each affected module; manifest posture/side-effect admission tests and standalone Cursor SDK package certification_

- [x] 12. Enforce future connector onboarding and close the entire negative-contract census
  - Complete host admission of v2 declared static contracts and add/extend existing generic architecture/QA and release checks that discover each manifest export, schema field and static-key source path. Include a deliberately violating future connector fixture proving missing namespace, literal YAML key, unauthorized file fallback and manifest/Describe mismatch are rejected.
  - Complete **all G01–G19** coverage matrix and operator migration examples across built-in, compatible/profile, wire, external inference and static-exception paths; use fast in-memory/httptest contracts, focused race checks and active generation rollback. Verify every code slice's own tests and full release gates without inserting new central connector-specific switches.
  - _Requirements: 1.5, 2.1, 2.3, 2.5, 3.2, 3.4, 3.5, 4.1, 4.3, 4.5, 5.1, 5.2, 5.3, 5.4, 5.5_
  - _Boundary: host/manifest registration enforcement; existing internal/archtest + internal/qa; tools/backendplugin; operator docs_
  - _Depends: 1–11_
  - _Validation: go run ./tools/kiro/kirocheck; go test ./internal/archtest ./internal/qa/... ./internal/infra/runtimebundle ./pkg/credpool; make quality-checks; affected parity-checks and release-grade connector module CI_

## Dependency / parallelization review

- **Serial foundation:** 1 -> 2, then 3 / 4 / 5 along validated edges; don't turn on strict runtime admission before the contract and all intended consumers have a migration path.
- **Parallel implementation waves:** 6 and 7 may run concurrently after 3+5; 9, 10, 11 may run concurrently after 3+4+5 in separate connector-module PRs. 8 follows the relevant wire/pool seam. 12 closes the envelope after all required functionality is implemented, with early gate scaffolding allowed during 1.
- **No hidden prerequisites:** A standalone Cursor SDK artifact outside this repository is a required release handoff; provider credentials for live tests are optional, not substitutes for deterministic hermetic test proofs.
- **Review checkpoints per PR:** State changed files, source SHA, run smallest relevant checks, demonstrate a failing pre-fix test and a passing post-fix test, check status, and close every known gap rather than declaring partial success.

## Final cross-artifact review

- **Requirements coverage:** 1.1–5.5 assigned to tasks 1–12; each G01–G19 in research maps to at least one acceptance criterion, and every audited connector family has an owner.
- **Brownfield correctness:** Existing plugin secure channel, agent-local eligibility and core pre-output policy retained; no provider imports into core or new ad hoc child environment readers.
- **Future proofing:** Mandatory declared contract + strict host validation + SDK slot decoder + connector schemas + negative in-tree gate, not documentation-only enforcement.
- **Design validation repairs propagated:** Transient throttle pinning, safe wire replay, trusted inference provisioning and non-key auth-mode distinctions appear in requirements, design and tasks.
- **Spec readiness:** GO for implementing the 12 small slices. No production code is changed by the spec-only PR.

## Implementation Notes

- Task 1: CompareDescriptorToManifest has no production caller yet; confirm activation-time wiring lands in task 4/12.
- Task 2: registered growth-style archtest overlay for the +18 counted composition lines (cap sized for tasks 4/5/8); NEVER use git stash in this container (shared ref) — use worktree-local patches. Task-2 FYI for task 7: OpenResponses/SystemOne still resolve custom roots via process env at factory-build rather than the snapshot.
- Task 3: reviewer-probed auth_token/bearer_token/credential/env:-scheme negatives committed as repo tests via followup + confirmation review (review2); post-approval test-only delta requires fresh reviewer confirmation (stale-artifact rule enforced by handoff tool).
- Task 4: reviewer suggestion deferred to task 12 (activation Describe runs post-Configure; secrets reach a drifted-but-authenticated peer before rejection — design-silent on ordering, disclosed).
- Task 5 follow-up: baselined additive slot-decoder surface in ABI contract (task 1 set the precedent); branch now carries ~119 cumulative *.go files vs origin/main — delivery must go as per-task slice PRs, not one PR.
- Task 8: runtime binding limited to single open site (zero routing-semantic change, verified); 3 runtime harness failures were tasks-3/6 debt (literal-key fixtures) — fixed in follow-up 5fb24e5e before this commit.
- Task 9: connector operator docs with legacy YAML examples deferred to task 12 (owns cross-cohort operator migration examples); live-probe legacy env names are test-only.
- Task 11: reviewer-suggested managed-OAuth fail-closed tests committed via followup + confirmation review (review2); followup artifact reclassified behavioral true->false by controller local repair (pure test-addition, no RED-able behavior change; validator accepts nonbehavioral with verification receipts).
- Task 12: implementer BLOCKED overruled by reviewer — sole quality-checks failure is the branch-cumulative size gate (278 *.go vs 100 at 23dc0ba9; slice adds zero counted files), delivery-owned (per-task slice PRs). Fixed stale cursor-sdk precedence claim as controller docs micro-fix. Umbrella implementation complete; delivery + closeout remain.

## Completion Status

- All 12 leaf tasks checked `[x]` with independent reviewer APPROVED artifacts and recorded verification receipts (execution index in controller scratch).
- Delivered as 12 per-task slice PRs #1126, #1130, #1133, #1139, #1141, #1149, #1153, #1156, #1160, #1161, #1162, #1164 (squash-merged; heads re-verified on merged main).
- Deferred scope stays deferred per requirements.md Deferred section (vault/KMS, cross-node quota sync, OS sandboxing, quota introspection APIs) — no V1 code for those exists.
- Known accepted limitation (documented, test-pinned): activation Describe runs post-Configure; see `docs/backend-plugins/operator.md` trust-boundary paragraph.
