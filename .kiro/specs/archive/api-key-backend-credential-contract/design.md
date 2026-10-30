# Design Document

## Overview

Implement one host-owned, environment-only static API-key contract for built-in, compatible/profile and executable backends. This is a **brownfield contract completion**, not a new generic authentication framework. An operator provides OPENAI_API_KEY, OPENAI_API_KEY_2, etc.; a candidate composition captures approved values, a provider-scoped ordered key selector handles quota/auth failures before any irreversible output/effect, and backends never read API keys from configuration files. An invalid candidate or unsafe retry fails closed without disturbing the active generation.

**Implementation delivery:** Twelve leaf tasks are intentionally independently reviewable work slices. No audit item is deferred. Each slice must respect .kiro/steering/delivery.md (~1,500 non-test Go lines / ~40 touched files per PR; do not override guards).

### Goals

- Deterministic bare + numbered environment discovery, default 20, configurable maximum, sparse numbering and explicit namespaces (1.1–1.5).
- No static provider secrets in any AIProxer backend YAML/JSON schemas, connector auth.json fallbacks or connector launch environments (2.1–2.5).
- First-key preference until quota-exhaustion threshold or auth invalidation, bounded short-throttle behavior, and pre-output/replay-safe rotation across all supported lanes (3.1–3.5).
- Full audited backend coverage, secure external inference provisioning, and automated future connector onboarding gates (4.1–5.5).

### Non-goals and exceptions

- Do not change ACP/login/OAuth refresh flow, customer-facing inbound auth.local_api_keys, AWS/OCI/Vertex workload-identity machinery, or arbitrary filesystem access by unsigned/untrusted binaries; static provider secrets in AIProxer connector config remain prohibited in every mode.
- Do not assume multiple keys are separate billable quota pools without provider evidence; do not introduce persisted, cross-host pool state or new provider SDK dependencies into core.
- Do not relax agent local_only gates or attempt an agent-session retry after a side effect. Cursor SDK and Codex static-subscription paths have explicit classifications below.

## Boundary commitments

| Owner | Owns | Does not own |
| --- | --- | --- |
| Config and composition (internal/core/config, internal/standardplugins, internal/infra/runtimebundle) | Nonsecret global policy, provider bindings, startup validation, immutable env capture and injected secrets | Provider HTTP taxonomy; secrets in YAML |
| SDK/manifest (pkg/lipsdk/backendplugin, internal/infra/backendplugins) | Versioned exported static-credential contract, bounded ordered SecretBundle slots, strict manifest/descriptor/posture checks | Provider auth SDK; raw provider env |
| Backend adapters and connector-support | Provider response classification, local attempt/replay boundary, safe key binding, optional auth-mode exceptions | Route-plan changes; core-specific state |
| pkg/credpool | Ordered credential state, short throttle versus quota cooldown, clock-aware eligibility/snapshot | Reading process env or files; provider-specific status parsing |
| internal/core/runtime | Existing inter-backend failover, B-leg lineage, cancellation and commitment | Per-provider key selection or secret inventory |
| internal/archtest, internal/qa, connector release tooling | Declarative future connector safeguards, negative fixtures, ABI/config parity | A separate parallel authentication implementation |

**Allowed dependencies:** Preserve hybrid ADR 0008, immutable runtime generations and SDK ABI isolation. No connector imports root internal packages. Root does not import optional connector modules. No provider-specific branches in the generic core.

## Concrete operator contract

Nonsecret top-level configuration only:

    upstream_credentials:
      max_env_index: 20               # inclusive: bare slot 1, suffixes _2.._N; bounds 1..256
      quota_switch_retry_after: 1h   # STRICTLY greater triggers long-limit advance
      unknown_429: do_not_rotate     # mandatory safe policy initially

For a custom compatible instance only, a nonsecret api_key_env_var_root may override its default provider namespace. Never put api_key, api_keys, credentials, access_token, api_token, PAT or private-key **values** anywhere in plugins.backends[*].config. Do not accept dollar-brace interpolation, file:/vault:/dotenv: secret paths, YAML aliases/merges carrying key values, or a legacy config-key shim. The config validator must emit field path + approved environment-root name, never the rejected value.

**Exact discovery:** Lookup root (slot 1), root_2 ... root_N without stopping at gaps; root_1 is invalid/misconfigured, not a second slot. Trim space, skip empty/missing slots, reject invalid secret bytes or oversized entries, deduplicate by value in stable priority order and keep numeric slot identity as nonsecret metadata. Limit N 1..256 and bound total captured bytes. Missing required keys reject backend composition, while explicitly no-auth local modes remain allowed.

**Namespace:** Factory metadata declares canonical provider credential domain separately from routing/backend_prefix. Shared OpenAI Chat/Responses may explicitly alias OPENAI_API_KEY. Different CommandCode exports use COMMANDCODE_OPENAI_API_KEY and COMMANDCODE_ANTHROPIC_API_KEY. Overlapping unrelated roots fail startup. Multiple instances sharing a root require explicit provider/quota-domain intent; custom profile roots are syntax-checked and collision-checked. One upstream target need not spawn per-key backend instances.

## Manifest and SDK admission contract

Introduce a **versioned v2 closed backend manifest** with one optional nonsecret credential_contract **per export**, required for static provider-secret consumers. Retain v1 for non-static/no-key/OAuth/workload exports; reject v1 static API-key export admission unless migrated to v2 (no permissive legacy fallback). V2 fields:

| Field | Example | Validation |
| --- | --- | --- |
| credential_contract.class | api_key, api_token, static_nonrotatable | Closed enum; consistent with existing credential_mode/access_scope/execution_class |
| credential_contract.source | host_env | Sole approved source for static keys; no file/yaml/child_env option |
| credential_contract.env_root | OPENROUTER_API_KEY | Uppercase constrained name, unique provider domain or explicit same-provider alias |
| credential_contract.slot | api_key or api_token | Closed typed SDK slot name; alias normalized before plugin delivery |
| credential_contract.selection | quota_pre_output / session_admission / no_rotation | Mode constrained by inference/agent and side-effect safety |
| credential_contract.quota_domain | named nonsecret provider/deployment scope | Prevent unrelated provider/project cooldown aliasing |

Keep original manifest process/access/credential posture gate. Validate **every export**, including Ollama Cloud's currently contradictory manifest/runtime descriptor. Compare runtime Describe metadata and declared export posture (at least mode, access, process-sharing, execution class; reject missing/extra exports); manifest-authored env binding remains host authority, never writable by the executable. The closed parser must reject unknown and secret-bearing fields. V2 is versioned independently of the transport protocol; **no new protobuf/Configure RPC is needed** for ordered credentials.

Use existing authenticated ConfigureRequest.Secrets Values with canonical names api_key, api_key_2, ... or api_token, api_token_2, ...; SDK supplies one typed decoder which validates order, length, uniqueness and total bytes. Map iteration order is never meaningful. No raw key in plugin ConfigYAML, argv, launch Env or diagnostics. No implicit per-export wildcard secret injection.

## Composition, secure handoff and reload

1. Validate strict config and all exported manifest/host approval metadata, namespace collisions, multi-user scope, execution class, mode, bounded N and policy **before** any env lookup, process launch or publication.
2. Capture required host process env through injected HostEnvironment.Lookup at authorized candidate build (no connector child os.Getenv). No dotenv loading or Windows persistent-registry override. Freeze a generation-bound ordered set; capture all indexes even when sparse. Fail atomically on invalid required key.
3. Preserve the existing local-only agent provisioning path; introduce a **separate explicitly authorized static-inference delivery lane** for approved export + allowed scope (including multi-user-safe service API keys). Never remove the local_only + agent_runtime condition from the original gate.
4. Reuse existing processhost secure peer-authentication and private Configure transport; copy only the selected export's validated slots after authentication. Keep default child env empty/minimal. Invalid peer/transport: zero secrets sent.
5. Include normalized credential-slot order, opaque secret fingerprints, namespace/quota domain and policy revision in backend physical-resource identity; no accidental resource reuse across credential snapshots. Candidate failure releases candidate resources; active and in-flight generation values remain unchanged.
6. Reload re-reads env **only** when compiling an authorized new candidate, not on each request. Process env edits alone do not silently mutate serving instances. In-flight attempts keep the admitted immutable pool; no orphaned goroutines or secret-containing metrics.

**Security boundary:** Config decode and host delivery enforce no AIProxer YAML secrets for first-party code. Architecture/AST rules forbid static API-key file fallbacks in in-tree connectors. Host cannot prove arbitrary third-party executable has no OS file read access without additional sandboxing; admit only trusted, certified manifest contracts and state this limitation accurately.

## Credential selection state machine

Per quota-domain + credential identity, use a deterministic priority list of opaque IDs (c0... assigned from ordered host slots). Keep no raw secret in IDs, log labels or exposed snapshots. Extend pkg/credpool with distinct states:

| Event / state | Selection decision |
| --- | --- |
| Start / success | First valid prioritized key stays active; no round-robin |
| 429 with trustworthy Retry-After > 1h (config threshold) or explicit verified long quota-exhaustion evidence | Quota-cool the selected credential until provider reset; advance to the next eligible key before output |
| 429 with valid Retry-After <= threshold | **Transient throttle on same selected key**, not quota-pool exclusion; bounded same-key wait if deadline permits, else return classified pre-output throttle; do not consume key 2 for future concurrent requests |
| 429 missing/invalid/past/overflow Retry-After | Conservative provider-defined bounded throttle on same key; no automatic rotation without a documented provider exhaustion signal |
| 401 or provider-confirmed credential invalid | Mark that credential auth-invalid; advance pre-output; 403/402 do not implicitly invalidate or rotate |
| 408/5xx, canceled, network error | Existing recoverability decision, not evidence for key exhaustion |
| Quota reset | Credentials become eligible after quota-cooldown expiry; deterministic preference re-evaluated; a previously invalid key remains invalid until reconfiguration |
| No eligible credential or deadline exhausted | Classified pre-output error to core; do not spin, retry blindly or create a new backend ID |

**Short-throttle invariant:** A transiently paused primary remains the priority owner; the selector cannot return secondary solely because the primary has a brief Retry-After. Concurrent requests use shared state safely with a mutex/clock injection and bounded admission rather than stampeding the secondary. Provider may return a per-model or org-wide 429; classifier explicitly marks scope and only switches if evidence supports independent credential quota. Preserve Retry-After HTTP-date and delta-seconds parsing and Gemini RetryInfo mapping.

**Pre-output invariant:** Each adapter uses one credential coordinator before its first canonical visible event. A key switch ends the failed attempt's provider resources before starting the next, with bounded attempts <= available credentials, correct cancellation and unchanged core B-leg identity/terminal ownership. No rotation after visible output or after agent/tool side effects that cannot safely be replayed.

## Optimized wire and other compatible paths

- Canonical streaming/non-streaming adapters use the same credential policy. OpenResponses backend.go/streaming.go/compact.go and SystemOne backend.go must stop taking FirstAPIKey at each call; wire key choice must be selected through the same pool.
- OpenAI wire_open.go currently acquires one key, then returns on classified 429. Reconcile with shared selection **only if the wire input can be reconstructed exactly**. The current body is an io.Reader; add an explicit replayable-source interface or bounded snapshot/reader factory owned by the largebody/wire boundary. Never assume an arbitrary consumed reader is seekable; never buffer unbounded payloads just to enable rotation.
- If upstream bytes may have produced irreversible effects, the first event is committed, replay is uncertain, or the reader cannot reconstruct, mark cooldown for future calls and return the classified error **without** another HTTP attempt. Prove cancellation/cleanup and no duplicate provider request. An optimized path must not claim unconditional equal replay behavior at the cost of at-most-once semantics.
- Backend-specific additional requests (model inventory, compact, token exchange, auxiliary call) must use selected credentials safely or report explicit no-rotation behavior; no hidden FirstAPIKey bypass in optional paths.

## Migration scope and application plan

| Cohort | Files / owners | Required change |
| --- | --- | --- |
| Built-in | internal/standardplugins/{keys,credentials,backends_*.go}; internal/pluginreg/keys.go | Remove literal key schema fields and YAML precedence; central env resolver; policy-injected pools; remove Alibaba Windows fallback |
| Generic/profile | internal/plugins/backends/{compatmode,openaicompat,anthropic,openresponsescompat,systemonecompat}; internal/standardplugins/provider_profile_binding.go | Shared env registry, full multi-key selection, inventory/compact/wire-safe coverage |
| Host/SDK | internal/infra/runtimebundle/backend_provisioning.go, discovered_factories.go, backend_resource_identity.go; internal/infra/backendplugins/{manifest,processhost}; pkg/lipsdk/backendplugin/manifest; pkg/credpool | Explicit inference credentials lane, v2 metadata, stable SecretBundle decoding and quota states |
| Lightweight external | connectors/{openrouter,nvidia,huggingface,commandcode-openai,commandcode-anthropic,opencode,ollama} | Env-only approved slots, one common pool adapter; delete YAML key fields/interpolation/auth.json; fix Ollama Cloud export |
| Auth API/PAT external | connectors/{azure,cloudflare,cohere,databricks,infomaniak,replicate,snowflake,watsonx} | Source from host-only secure slots in API-key modes, provider-specific quota classifiers/token-provider adapters |
| Mode-edge external | connectors/{cursorsdk,codex,sapaicore,llamacpp,lmstudio,vllm,vertex,oci,sagemaker}; standalone Cursor SDK release | Remove literal static secret/config file fallbacks; protect agent/session, subscription and workload/non-bearer exceptions; do not invent quota portability |
| Negative gates | internal/archtest, internal/qa, connector-support, tools/backendplugin, connector manifest/contract suites | Registration validation, build/manifest/source-schema inventory, first-party forbidden-source AST check, future connector negative fixtures |

**Migration posture:** Breaking YAML secret fields fail closed, without silent deprecated support. For old operator configs, produce secret-free field-specific remediation and a before/after env-only example. Existing inbound client auth config remains separately owned. Update README, docs/backend-plugins/{authoring,operator}.md, docs/custom-compatible-backends.md, provider profile docs and affected connector operator examples with each implementation slice; no separate docs-only task.

## Validation and traceability

| Component | Requirement IDs | Most important focused proof |
| --- | --- | --- |
| Config/namespace resolver | 1.1–1.5, 2.5 | Sparse slots, bounds, collision/shared alias, bad key without value leak, reload snapshot |
| Schema/secret guard | 2.1, 2.2, 2.4, 5.4 | Nested keys/interpolation/aliases, unknown fields, prohibited files, OAuth/workload preserved |
| Host provisioner / manifest | 2.3, 4.2, 5.1, 5.3 | Inference admitted, agent guard intact, authenticated delivery, hostile exporter and process env |
| Credential pool / adapters | 3.1–3.5, 4.4 | Short/long/missing 429 and auth statuses with fake clock/concurrency/cancellation |
| Compatible and optimized paths | 3.4, 4.1, 4.3 | OpenResponses/SystemOne/large-body replay and no-replay parity |
| Connector cohorts | 4.1, 4.2, 4.5 | Manifest parity each exported kind, no YAML, ordered keys, no unsafe agent replay |
| Admission + architecture QA | 5.1–5.5 | New mock connector without metadata fails; forbidden sources fail archtest; normal connector passes |

**Verification:** Unit/domain and per-module contract tests with httptest/fake clocks first; bounded host-plugin end-to-end proof including all export kinds; candidate rollback; test -race / CI for shared pools; go run ./tools/kiro/kirocheck for spec metadata and budgets; affected make quality-checks/parity-checks as needed. TDD per task; each task owns its tests rather than a standalone test program. No external provider keys or network for default tests.

## Brownfield design validation outcome

- **Security repaired:** No broadening of existing agent-only capture function; new inference lane runs after manifest and authorization checks.
- **Policy repaired:** Short 429 is transient pinning, not Pool.Acquire cooldown skip; invalid and unknown hints cannot exhaust all keys by accident.
- **Wire repaired:** Require explicit body replay proof and no side effects before any resend; otherwise no replay.
- **Coverage repaired:** No unowned OpenResponses/SystemOne or external connector branch; multi-export Ollama, Cursor standalone, Codex and local optional API-key modes assigned to tasks.
- **Future enforcement repaired:** Closed manifest v2 + SDK helper + host admission + existing generic architecture gate, not prose.
- **Final design gate:** GO for implementation planning. No unknown key-bearing auth route may be silently accepted; release closure requires the full cohort census and every negative proof.
