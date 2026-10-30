# Backend plugin operator guide

Install, trust, diagnose, upgrade, roll back, and remove **executable** optional backend connectors without rebuilding `cmd/aipstd`. Hybrid composition is [ADR 0008](../adr/0008-hybrid-backend-connector-plugins.md). Authoring details remain in [`authoring.md`](authoring.md).

**Trust posture (operator):** an installed connector runs as a separate process behind approved local IPC and digest checks. That **process isolation is not a malicious-code sandbox**. Treat installed plugins as **trust-equivalent to code you chose to run as the proxy service account**. The dedicated threat model and accepted controls are in [`threat-model.md`](threat-model.md) (`make backend-plugin-security-checks`).

**Activation ordering (accepted limitation):** the host delivers an export's `Configure` secrets over the authenticated channel before comparing the runtime `Describe` descriptor against the packaged manifest. A drifted-but-authenticated peer therefore receives its secrets before the host rejects the mismatch and releases the candidate. Activation fails closed, but secrets were already disclosed to that authenticated peer: do not interpret fails-closed as never-disclosed. Reordering would not strengthen the boundary — the peer is already the digest-verified, trusted-root artifact running as the proxy service account — so the host documents this ordering and always fails the activation closed with a secret-free error instead of claiming pre-delivery verification.

There is **no runtime download** of plugins. Operators (or installers) place artifacts on disk; AIProxer only discovers and launches what is already present under trusted roots.

## Package layouts: minimal vs curated-full

| Profile | Contents | Typical use |
|---|---|---|
| **minimal** | Standard `aipstd` binary layout only — **no** optional connector executables | Essentials-only deployments; add plugins later |
| **curated-full** | Structurally discovers every `connectors/*/release.yaml` whose `profiles` include `full` (not a maintained name list) and stages digests + manifests | Dogfood, offline bundles, curated appliance images |

Commands (repo root):

```bash
make package-minimal PACKAGE_DEST=.aip-package-staging/minimal
make package-full PACKAGE_DEST=.aip-package-staging/full
make package-plugin-smoke
go run ./tools/backendplugin/package_plugins -profile full -dest .aip-plugins/full
```

Each staged tree includes `ACCESS.txt` (ownership posture metadata) and `package-index.json`. See fixtures [`examples/operator/package-index.minimal.json`](examples/operator/package-index.minimal.json) and [`examples/operator/package-index.full.json`](examples/operator/package-index.full.json).

## Platform install directories and permissions

Default **machine-scoped** plugin roots (installer/admin owned; proxy account **read + execute** only):

| Platform | Default plugin root |
|---|---|
| Linux | `/opt/AIProxer/plugins` |
| macOS | `/Library/Application Support/AIProxer/plugins` |
| Windows | `%ProgramFiles%\AIProxer\plugins` |

Guidance:

- **Linux/macOS:** directories `0755` or tighter; plugin executables `0755` (or `0555`); manifests `0644`. Prefer root/admin ownership; the proxy UID/GID should not be able to rewrite digests or executables.
- **Windows:** Administrators (or installer SID) modify; service/user SID **Read & execute**. Avoid granting the proxy account write/modify on the plugin tree. Named-pipe ACL enforcement is host-owned; do not widen pipe DACLs for convenience.
- Packagers may inject another installation-owned default (for example `/usr/libexec/AIProxer/plugins`); the runtime never guesses mutable per-user locations unless **`development_mode`** is on with **explicit** `paths`.

Copy one plugin directory (manifest + `bin/` + digest metadata) into a trusted root. Removing that directory uninstalls that artifact; other plugins remain. No root Go rebuild required.

## Discovery, trust, and closed manifests

```yaml
plugins:
  backend_discovery:
    enabled: true
    paths:
      - /opt/AIProxer/plugins
    strict: true
    development_mode: false
  backends: []
```

| Field | Operator meaning |
|---|---|
| `enabled` | When false, optional connectors are not discovered |
| `paths` | Explicit trusted roots (non-recursive install directories containing `*.backendplugin.json`) |
| `strict` | Fail closed on discovery/layout errors |
| `development_mode` | Allows only the explicit `paths` you list; still **no** implicit home-directory plugin root |

**Closed manifest:** unknown JSON fields fail closed. Required shape matches [`examples/operator/closed-manifest.backendplugin.json`](examples/operator/closed-manifest.backendplugin.json):

```json
{
  "schema": "aiproxer.backendplugin.manifest/v1",
  "plugin_id": "com.aiproxer.backend.localstub",
  "version": "0.1.0",
  "build_id": "REPLACE_BUILD_ID",
  "executable": "bin/aip-backend-localstub",
  "sha256": "REPLACE_SHA256",
  "protocol_major": 1,
  "protocol_min_minor": 0,
  "protocol_max_minor": 0,
  "platforms": [{"os": "linux", "arch": "amd64"}],
  "exports": [
    {
      "kind": "local-stub",
      "credential_mode": "none",
      "access_scope": "any",
      "process_sharing": "per_instance"
    }
  ]
}
```

**Digest / exact artifact / private staging:** the host verifies `sha256`, binds a **private staged** copy of the executable, and launches those exact bytes — not a mutable path-only trust. Staging is cleaned on shutdown/upgrade paths exercised by packaging smoke tests.

**Configured-missing:** an enabled backend whose kind is not built-in and not discovered fails closed at composition (inspect/check-config surface the gap). Installed but **unconfigured** plugins stay inactive (no process launch).

Validated configs:

- [`config/examples/plugin-operator-minimal.yaml`](../../config/examples/plugin-operator-minimal.yaml) — discovery off / essentials + optional stub after single-plugin install
- [`config/examples/plugin-operator-full-discovery.yaml`](../../config/examples/plugin-operator-full-discovery.yaml) — curated-full discovery path
- [`examples/operator/discovery-development.yaml`](examples/operator/discovery-development.yaml)
- [`examples/operator/discovery-production.yaml`](examples/operator/discovery-production.yaml)

## Approved IPC, peer auth, and secrets

- Hosts use **approved secure local IPC** profiles (platform-specific; Windows uses a host-provided pipe such as `AIP_PLUGIN_CHANNEL_PIPE`). Unauthorized local peers cannot negotiate/configure.
- **Peer-authentication failure:** doctor/configure stops; **connector credentials are never sent** after channel/peer failure.
- Secrets arrive only in authenticated configure payloads — not via unprotected process-environment bootstrap. Do not put provider API keys into plugin launch env.
- **Local-only** connectors (`access_scope` / local-only posture) are rejected when `access.mode: multi_user`. Keep them on single-user loopback deployments.

## Static API-key migration (env-only)

Provider API keys come only from the host process environment. Literal
`api_key`, `api_keys`, `credentials`, `access_token`, `api_token`, `pat` or
`private_key` values in `plugins.backends[*].config` fail closed at
`check-config` with the offending field path and the required environment root
(never the value), for example:

```text
openairesponses backend config: forbidden config key "config.api_key"
(static credentials must come from host environment root OPENAI_API_KEY;
never put values in config)
```

Migrate each row:

1. Delete the literal secret fields from YAML (including `${...}`
   interpolation and `file:`/`vault:`/`dotenv:`/`env:` references, which are
   rejected the same way).
2. Export the numbered environment variables (`ROOT`, `ROOT_2`, …).
3. For custom compatible rows, set the nonsecret `api_key_env_var_root`.
4. Run `check-config` — validation fails until every secret leaves YAML.

Built-in (before → after):

```yaml
# BEFORE (rejected): literal key in config
plugins:
  backends:
    - id: openai-primary
      kind: openai-legacy
      config:
        api_key: PASTE_KEY_HERE
```

```yaml
# AFTER (accepted): no secret in config; host reads OPENAI_API_KEY
plugins:
  backends:
    - id: openai-primary
      kind: openai-legacy
```

```sh
export OPENAI_API_KEY="<operator key>"
```

External executable connector (before → after):

```yaml
# BEFORE (rejected)
plugins:
  backends:
    - id: router
      kind: openrouter
      config:
        api_key: PASTE_KEY_HERE
```

```yaml
# AFTER (accepted): host reads OPENROUTER_API_KEY
plugins:
  backends:
    - id: router
      kind: openrouter
```

```sh
export OPENROUTER_API_KEY="<operator key>"
```

Custom compatible (before → after):

```yaml
# BEFORE (rejected)
plugins:
  backends:
    - id: provider123
      kind: custom-openai-legacy-compatible
      config:
        backend_prefix: provider123
        base_url: https://api.provider123.example/v1
        api_key: PASTE_KEY_HERE
```

```yaml
# AFTER (accepted)
plugins:
  backends:
    - id: provider123
      kind: custom-openai-legacy-compatible
      config:
        backend_prefix: provider123
        base_url: https://api.provider123.example/v1
        api_key_env_var_root: PROVIDER123_API_KEY
```

Static-exception paths (OAuth-user, workload identity, no-auth local, agent
session admission) keep their classified modes but obey the same rule: no
literal provider secret ever appears in AIProxer YAML, and rotation across
identities is never assumed unless the contract declares it.

## Multi-user eligibility: connector posture **and** a host-owned approval

`access.mode: multi_user` is a shared-service deployment: unrelated downstream principals multiplex through one proxy. Two independent things must both hold before any backend may run in that mode, and the **most restrictive** source wins.

| Requirement | Declared by | Enforced |
|---|---|---|
| `access_scope: any` | connector manifest export | host, at composition |
| `credential_mode` is not `oauth_user` and not `unknown` | connector manifest export | host, at composition |
| `execution_class: inference` (agent runtimes are never shared) | connector manifest export | host, at composition |
| Explicit entry in the **host-owned approval registry** | `internal/standardplugins/multi_user_backend_policy.go` | host, at composition |

The registry lives in the **host/distribution tree, never in `connectors/<name>`**. Connector packaging cannot add itself to it, and no connector can read the effective access mode (architecture guards reject both an import of host internals and a locally declared multi-user authority).

**Absence from the registry is denial.** A brand-new connector that declares `access_scope: any` + `credential_mode: static` + `execution_class: inference` — even a real agent runtime that misclassifies itself — is rejected in a multi-user deployment. Promoting a factory is an explicit, separately reviewed change to the registry file, which is a privilege grant and should be treated as such (`CODEOWNERS` / branch protection).

The generic invariants behind this are:

```text
execution_class: agent_runtime  => access_scope: local_only   # documented product policy
credential_mode: oauth_user     => access_scope: local_only   # personal/subscription credentials
manifest validation rejects credential_mode: oauth_user with any access scope other than local_only
```

An approval can never *widen* a posture: a factory that is approved but whose packaged manifest says `local_only`, `agent_runtime`, `oauth_user`, or an undeclared credential is still denied in a multi-user deployment.

The manifest, not the runtime descriptor, is the host's security-policy authority. Connector parity tests keep `FactoryDescriptor.AccessScope` / `CredentialMode` in agreement with the packaged manifest so a disagreement is a diagnostic failure, not a silent divergence. The access decision itself is **never** made inside `Describe()` or `Configure()`.

### What "personal credential" means here

AIProxer's boundary is about **credential ownership under the current principal model**, not about a blanket provider-terms rule:

- **User-scoped / personal / subscription credentials** — the credential belongs to one human's account or device. Every such factory is `access_scope: local_only`; `credential_mode` then records *how* it authenticates and is not the same for all of them:
  - Agent runtimes and local-device backends are `local_only` with `credential_mode: static` or `none`, because they read the local user's own CLI login, device state, or agent process rather than an upstream OAuth profile. This covers the ACP family, Cursor SDK under its current contract, and both Codex exports (`openai-codex` is `static`/`inference`; `openai-codex-app-server` is `none`/`agent_runtime`).
  - The subscription-OAuth connectors are `local_only` **and** `credential_mode: oauth_user`, because the effective credential is a user OAuth session: `gitlab-duo`, `minimax-oauth`, `nous-portal`, `qwen-oauth`, `xai-oauth`, plus a future ChatGPT-plan / SIWC profile.
  - In both groups the *scope* is what denies shared use, and `local_only` alone already rejects a `multi_user` deployment. OpenAI explicitly supports ChatGPT-plan usage in a **same-user** self-hosted VM; what AIProxer refuses is any of these credentials becoming an **operator-wide credential that unrelated downstream principals can multiplex**. There is no pooling or subscriber failover across different users' accounts.
- **Legitimate shared-service identities** — provider API keys, workload identity from the runtime environment, and provider service accounts are ordinary multi-user-safe credentials. A backend that uses one of those, runs remote provider inference, and is explicitly registered may be approved.

**There is no escape hatch.** No `allow_local_only_in_multi_user` (or equivalent) configuration exists, and none should be added. A future shared agent backend must be a distinct, reviewed factory with a provider-supported service identity, explicit per-principal credential binding, and an explicit exception — not a reclassified existing connector.

### When multi-user rejects a backend

The failure is a typed, actionable composition error carrying **instance** and **factory** context, raised at startup / `check-config` / reload candidate compilation **before** backend construction, connector `Dial`/`Configure`, or child-process launch. A configured-but-unapproved backend fails the whole candidate; it is never silently disabled or downgraded.

Remediate by choosing one of:

1. **Run a single-user loopback deployment** — `access.mode: single_user` on an explicit loopback listener (`127.0.0.1`, `::1`, `localhost`). Note that `single_user` is about the *listener*, not about auth: a single-user loopback deployment may still use an external auth handler.
2. **Use a credential type intended for shared service operation** — a provider API key, workload identity, or service account on a backend that is explicitly approved for `multi_user`.

Discover which kinds the current distribution approves:

```bash
go run ./cmd/aipstd inspect --config CONFIG
```

## Inspect and doctor

```bash
go run ./cmd/aipstd check-config --config CONFIG
go run ./cmd/aipstd inspect --config CONFIG
go run ./cmd/aipstd doctor --config CONFIG --instance INSTANCE_ID
```

| Command | Launches plugins? | Meaning |
|---|---|---|
| `check-config` | No | Validates YAML + composition readiness |
| `inspect` | No | Built-in vs discovered kinds, versions, conflicts, configured-missing, activation needed |
| `doctor --instance ID` | **Only that** configured instance | Handshake / secure-channel / peer checks; never all discovered plugins |

Inspect states operators commonly see: discovered, configured, missing kind, manifest invalid, digest mismatch, builtin collision, local-only rejected. Doctor failures on peer/channel leave no credential exposure to the plugin.

### Connector diagnostics and the diagnostic budget

The host drains every connector's stdout and stderr for the whole process
lifetime, so a connector that logs cannot block on a full pipe. The drained
bytes are discarded: they never reach a client and they never reach the proxy
log. A connector that reports a degradation has to say so on the canonical
event stream, not only in its own log output.

Draining is not free, so every drained byte is charged against a per-generation
diagnostic budget. A connector that outruns the budget is terminated and its
generation is dropped. The budget is the reported cause of that generation:
an activation that is still being configured fails with
`connector_output_limit_exceeded`, and a generation that was already served
reports the same reason when its activation is cleaned up. A connector that
floods its own log stream therefore stops serving instead of being allowed to
keep the host busy.

## Compatibility, upgrade, rollback, uninstall

**Compatibility:** host and plugin negotiate protocol major/minor from the manifest. Incompatible major versions fail before configure.

**Atomic upgrade:**

1. Stage the new artifact beside the live tree (or into a versioned directory).
2. Verify digest/`package-index.json`.
3. Atomically replace the published plugin directory (packaging uses staging + publish).
4. Point discovery at the new root if you use versioned roots — see [`config/examples/plugin-operator-upgrade.yaml`](../../config/examples/plugin-operator-upgrade.yaml) and [`examples/operator/upgrade-candidate.yaml`](examples/operator/upgrade-candidate.yaml):

```yaml
plugins:
  backend_discovery:
    enabled: true
    development_mode: true
    strict: true
    paths:
      - .aip-plugins/upgrade-candidate/localstub
```

5. `check-config` + `inspect`; optional `doctor --instance …`.

**Rollback:** keep the previous published directory; retarget `backend_discovery.paths` (or restore the prior atomic publish) — [`config/examples/plugin-operator-rollback.yaml`](../../config/examples/plugin-operator-rollback.yaml), [`examples/operator/rollback-previous.yaml`](examples/operator/rollback-previous.yaml). No `aipstd` rebuild.

```yaml
plugins:
  backend_discovery:
    enabled: true
    development_mode: true
    strict: true
    paths:
      - .aip-plugins/previous/localstub
```

**Uninstall / cleanup:** delete the plugin install directory; remove or disable its `plugins.backends` rows. Locked source artifacts and private staging must not remain after tested shutdown/upgrade (packaging smoke covers staged cleanup). Unrelated plugins keep working.

## Routing Execution Composition Policy

By default, AIProxer applies a **safe** routing execution composition policy (`routing.execution_composition_policy: safe`). Under this policy:
- Backends classified as `agent_runtime` (such as ACP agent connectors, Cursor SDK agents, or OpenAI Codex App-Server) and backends with `unknown` execution class **cannot** be mixed into composite routing selectors (failover `|`, parallel/race `!`, weighted `^`, or thinker hybrid chains) with other backends.
- Direct routing to any backend (e.g. `acp:claude-3-7-sonnet`) is always permitted.
- Pure inference composition (e.g. `openai:gpt-4o|anthropic:claude-3-5-sonnet`) is fully permitted.

To explicitly permit mixed agent runtime and inference composition at operator risk, set:

```yaml
routing:
  execution_composition_policy: unrestricted
```

> [!WARNING]
> In `unrestricted` mode, failover or parallel execution against agent runtimes may trigger duplicate side-effects (e.g. tool execution, file edits, git commands) across multiple backends or retries.

## Cloudflare AI Gateway REST Connector

The Cloudflare external backend connector (`kind: cloudflare`) integrates AIProxer with the [Cloudflare AI Gateway REST API](https://developers.cloudflare.com/ai-gateway/usage/rest-api/).

Key characteristics:
- **Cloudflare AI Gateway REST API:** Requests are dispatched to `{api_origin}/client/v4/accounts/{account_id}/ai/v1` (production default origin: `https://api.cloudflare.com`). The account-scoped path is deterministically constructed by the connector.
- **Responses preferred:** ambiguous or OpenResponses traffic routes to the `/responses` endpoint. Chat completions routes to `/chat/completions`.
- **Not Workers AI:** this connector is an external HTTP backend target for Cloudflare AI Gateway REST endpoints, not an in-worker or Workers AI SDK binding.
- **Not `/compat`:** the deprecated `/compat` endpoint is forbidden for ordinary calls.
- **Not ACP:** the connector is an `execution_class: inference` backend, not an Agent Client Protocol runtime.
- **Gateway selection:** when `gateway_id` is configured, each request passes `cf-aig-gateway-id: <gateway_id>`. When omitted, the header is not sent.
- **Credentials:** API tokens must be supplied via `ConfigureRequest.Secrets` as canonical `api_token[_N]` slots (host env `CLOUDFLARE_API_TOKEN[_N]`). Literal tokens in configuration YAML are strictly forbidden and rejected.
- **Model inventory:** `ListModels` queries GET `/models` and filters results to Responses-capable text and coding models, omitting embedding, rerank, image, and audio models.

### Configuration Example

```yaml
plugins:
  backends:
    - id: cloudflare-gateway
      kind: cloudflare
      config:
        account_id: "your-cloudflare-account-id"
        gateway_id: "my-gateway"
```

## Azure OpenAI / AI Foundry REST Connector

The Azure OpenAI external backend connector (`kind: azure-openai`) integrates AIProxer with Azure OpenAI and Azure AI Foundry endpoints. A single `azure-openai` connector kind covers both Azure OpenAI and Azure AI Foundry v1, as Foundry v1 shares the same OpenAI-compatible resource/deployment REST surface.

Key characteristics:
- **Base URL construction:** Outgoing requests target `{endpoint}/openai/v1` or `https://{resource_name}.openai.azure.com/openai/v1`. The `/openai/v1` path is deterministically formed by the connector.
- **Responses preferred & Hard-negative:** Ambiguous or OpenResponses traffic routes to the `/responses` endpoint and **never falls back to chat completions** (hard-negative). Chat completions traffic explicitly routes to `/chat/completions`.
- **API Version:** The required `api_version` configuration (e.g. `2024-10-21`) is injected as an HTTP query parameter (`?api-version=<version>`) on every outgoing call.
- **Dual credential modes:**
  - `credential_mode: api_key` (default): sends `api-key: <key>` header (no `Authorization` header). The API key is supplied via `ConfigureRequest.Secrets` (`api_key`).
  - `credential_mode: entra`: authenticates via Microsoft Entra ID credential chain (`DefaultAzureCredential` or workload identity/service principal using optional typed `tenant_id` and `client_id`; client secrets are provider-managed via the token provider, never typed connector YAML fields). Sends `Authorization: Bearer <token>` header (no `api-key` header). Tokens are resolved dynamically per request and never persisted to YAML, diagnostics, or descriptors. Never paste a static JWT bearer token into configuration.
- **Not `/compat`:** The `/compat` endpoint is forbidden for ordinary calls.
- **Not ACP:** The connector is an `execution_class: inference` backend, not an Agent Client Protocol runtime.
- **Deployments:** A required `deployments` mapping (`<deployment-name>: <underlying-model>`). Azure routes inference requests by deployment name rather than model ID. At least one deployment is required.
- **Model inventory:** `ListModels` maps deployed models to canonical IDs prefixed with `azure-openai/` and filters out non-Responses models (embeddings, rerank, audio, image).

### Configuration Example

```yaml
plugins:
  backends:
    - id: azure-openai-eastus
      kind: azure-openai
      config:
        resource_name: "my-openai-resource"
        api_version: "2024-10-21"
        credential_mode: "api_key"
        deployments:
          my-gpt-4o: gpt-4o
          my-gpt-4o-mini: gpt-4o-mini
```

## Snowflake Cortex REST Connector

The Snowflake Cortex external backend connector (`kind: snowflake-cortex`) integrates AIProxer with [Snowflake Cortex REST APIs](https://docs.snowflake.com/en/user-guide/snowflake-cortex/cortex-overview).

Key characteristics:
- **Base URL construction:** Outgoing requests target `https://{account}.snowflakecomputing.com/api/v2/cortex/v1`. The `/api/v2/cortex/v1` path is deterministically formed by the connector from the typed `account` identifier.
- **Not `/compat`:** The `/compat` endpoint is forbidden for ordinary calls.
- **Not ACP:** The connector is an `execution_class: inference` backend, not an Agent Client Protocol runtime.
- **Role header:** When optional `role` is configured, outgoing requests include `X-Snowflake-Role: <role>`. When omitted, the header is not sent.
- **Credentials:** Programmatic Access Tokens (PAT) or JWTs must be supplied via `ConfigureRequest.Secrets` as canonical `api_token[_N]` slots (host env `SNOWFLAKE_CORTEX_PAT[_N]`). Literal tokens in configuration YAML are strictly forbidden and rejected.
- **Responses preferred & Hard-negative:** Ambiguous or OpenResponses operations route to `/responses` and never fall back to chat completions. Chat completions operations route to `/chat/completions`.
- **Model inventory:** `ListModels` maps Snowflake-hosted foundation models (e.g. `mistral-large2`, `llama3.3-70b`, `snowflake-arctic`, `deepseek-r1`) to canonical IDs prefixed with `snowflake-cortex/`, filtering out non-coding/non-language models (embeddings, rerank, audio, image).

### Configuration Example

```yaml
plugins:
  backends:
    - id: snowflake-cortex-primary
      kind: snowflake-cortex
      config:
        account: "xy12345.us-east-1"
        role: "cortex_user_role"
```

## Databricks AI REST Connector

The Databricks AI external backend connector (`kind: databricks-ai`) integrates AIProxer with [Databricks AI Gateway](https://docs.databricks.com/en/large-language-models/ai-gateway.html) OpenAI-compatible endpoints.

Key characteristics:
- **Base URL construction:** Outgoing requests target `https://{host}/ai-gateway/mlflow/v1`. The host is normalized (stripping schemes and trailing slashes) and the gateway path `/ai-gateway/mlflow/v1` is deterministically appended by the connector.
- **Default model-service name:** When optional `serving_endpoint` is configured, it serves as the default target model for the mlflow `model` field when the invocation model is omitted or unversioned. When the invocation supplies an explicit model, that model is used. No custom routing headers are injected.
- **Not `/compat`:** The `/compat` endpoint is forbidden for ordinary calls.
- **Not ACP:** The connector is an `execution_class: inference` backend, not an Agent Client Protocol runtime.
- **Credentials:** Databricks workspace Personal Access Tokens must be supplied via `ConfigureRequest.Secrets` as canonical `api_token[_N]` slots (host env `DATABRICKS_API_TOKEN[_N]`). Literal tokens in configuration YAML are strictly forbidden and rejected.
- **Responses preferred & Hard-negative:** Ambiguous or OpenResponses operations route to `/responses` and never fall back to chat completions. Chat completions operations route to `/chat/completions`.
- **Model inventory:** `ListModels` maps Databricks serving endpoints and foundation models (e.g. `databricks-dbrx-instruct`, `databricks-meta-llama-3-3-70b-instruct`) to canonical IDs prefixed with `databricks-ai/`, filtering out non-coding/non-language models (embeddings, rerank, audio, image).

### Configuration Example

```yaml
plugins:
  backends:
    - id: databricks-gateway
      kind: databricks-ai
      config:
        host: "adb-123.azuredatabricks.net"
        serving_endpoint: "my-endpoint"
```

## Infomaniak AI REST Connector

The Infomaniak AI external backend connector (`kind: infomaniak-ai`) integrates AIProxer with [Infomaniak AI](https://developer.infomaniak.com/docs/api) OpenAI-compatible endpoints.

Key characteristics:
- **Base URL construction:** Outgoing requests target `https://api.infomaniak.com/2/ai/{product_id}/openai/v1`. The required `product_id` is supplied in configuration (accepting an integer or numeric string).
- **Chat default:** In contrast to other providers, Infomaniak AI documents standard chat completions (`/chat/completions`) and does not document a `/responses` endpoint. Ambiguous and default traffic routes to `/chat/completions`. Explicit Responses traffic routes to `/responses` and fails closed if unsupported.
- **Not `/compat`:** The `/compat` endpoint is forbidden for ordinary calls.
- **Not ACP:** The connector is an `execution_class: inference` backend, not an Agent Client Protocol runtime.
- **Credentials:** API keys must be supplied via `ConfigureRequest.Secrets` as canonical `api_key[_N]` slots (host env `INFOMANIAK_API_KEY[_N]`). Literal tokens in configuration YAML are strictly forbidden and rejected.
- **Model inventory:** `ListModels` queries GET `/models` from the constructed product base and maps models to canonical IDs prefixed with `infomaniak-ai/`, filtering out non-coding/non-language models (embeddings, rerank, audio, image).

### Configuration Example

```yaml
plugins:
  backends:
    - id: infomaniak-ai-primary
      kind: infomaniak-ai
      config:
        product_id: 103281
```

## Google Vertex AI REST Connector

The Google Vertex AI external backend connector (`kind: vertex`) integrates AIProxer with Google Vertex AI generative model endpoints using the native `generateContent` and `streamGenerateContent` REST protocol.

Key characteristics:
- **Constructed URL:** Requests target `https://{location}-aiplatform.googleapis.com/v1/projects/{project}/locations/{location}/publishers/{publisher}/models/{model}:generateContent` (or `:streamGenerateContent?alt=sse` for streaming). If location is `global`, the origin is `https://aiplatform.googleapis.com`. The default publisher is `google` when omitted.
- **Native generateContent contract:** Uses native Vertex/Gemini `contents` / `candidates` payloads instead of forcing models through OpenAI compatibility endpoints or gateways.
- **Distinct from Gemini API-key backend:** This connector is specifically for Google Cloud Vertex AI (`kind: vertex`) using OAuth2 / Bearer authorization, distinct from the in-process Gemini API-key backend (`kind: gemini` at `generativelanguage.googleapis.com`).
- **Not ACP:** The connector is an `execution_class: inference` backend, not an Agent Client Protocol runtime.
- **Credentials:** Supports Google Application Default Credentials (ADC) or explicit service account credentials via `ConfigureRequest.Secrets` (`service_account_json`). Literal secrets or private keys in configuration YAML are strictly forbidden and rejected.
- **Model inventory:** `ListModels` queries the Model Garden catalog via `GET /v1beta1/publishers/{publisher}/models` (default publisher `google`) without embedding project or location in the inventory path, and maps models to canonical IDs prefixed with `vertex/`, dropping embeddings, image generation, video, and audio models.

## Amazon SageMaker REST Connector

The Amazon SageMaker external backend connector (`kind: sagemaker`) integrates AIProxer with Amazon SageMaker model deployments using the AWS SDK v2 with SigV4 request signing.

Key characteristics:
- **SigV4 signing:** All runtime invoke requests are signed with AWS Signature Version 4 (SigV4) using standard AWS credentials.
- **Runtime invocation:** Directly targets SageMaker Runtime `InvokeEndpoint` (unary) at `/endpoints/{endpoint_name}/invocations`. Provider streaming (`InvokeEndpointWithResponseStream`) is intentionally not used; streaming delivery is served by collecting the unary response.
- **Frozen v1 inference contract:** Requires `inference_contract: hf-text-generation`. Requests send `{"inputs": "<user text>", "parameters": {"max_new_tokens": ...}}` and parse `{"generated_text": "..."}` or `[{"generated_text": "..."}]` responses into canonical text delta events. Unsupported roles or tools fail closed rather than silently drop semantics.
- **Distinct from AWS Bedrock:** This connector is specifically for Amazon SageMaker custom and jumpstart endpoint deployments (`kind: sagemaker`), completely distinct from the in-process Amazon Bedrock backend (`kind: bedrock`).
- **Not ACP:** The connector is an `execution_class: inference` backend, not an Agent Client Protocol runtime.
- **Credentials:** Supports default AWS credential chain (environment variables, AWS shared credentials/config, IAM roles) via `NewProduction()`, or explicit static credentials via `ConfigureRequest.Secrets` (`aws_access_key_id`, `aws_secret_access_key`, optional `aws_session_token`). Literal secrets or access keys in configuration YAML are strictly forbidden and rejected.
- **Model inventory:** `ListModels` exposes only the configured `endpoint_name` as `sagemaker/{endpoint_name}` (no control-plane enumeration). Executing an unconfigured endpoint fails closed. **Required IAM permission:** `sagemaker:InvokeEndpoint` on the configured endpoint; no SageMaker control-plane permissions are needed.

## OCI Generative AI REST Connector

The Oracle Cloud Infrastructure (OCI) Generative AI external backend connector (`kind: oci-generative-ai`) integrates AIProxer with OCI Generative AI inference service via native HTTP request signing.

Key characteristics:
- **Constructed chat URL:** Requests target `https://inference.generativeai.{region}.oci.oraclecloud.com/20231130/actions/chat`.
- **OCI HTTP signing:** All requests are signed using OCI HTTP Signatures (`Authorization: Signature ...`) with an RSA private key. Bearer authorization is not used.
- **Native GENERIC chat contract:** Sends documented OCI `ChatDetails` with `chatRequest.apiFormat: "GENERIC"` using on-demand or dedicated serving modes. Responses parse `chatResponse` choices and text contents into canonical text events. Unsupported message roles or tools fail closed rather than silently drop semantics.
- **Not OpenAI-compatible:** Operates directly against native OCI Generative AI inference action endpoints, not the OCI `/openai/v1` compatibility layer.
- **Not ACP:** The connector is an `execution_class: inference` backend, not an Agent Client Protocol runtime.
- **Credentials:** Supports OCI standard credential providers (file / instance principal) via `NewProduction()`, or explicit static credentials via `ConfigureRequest.Secrets` (`private_key` PEM, optional `passphrase`, `tenancy_ocid`, `user_ocid`, `fingerprint`). Literal private keys or tokens in configuration YAML are strictly forbidden and rejected.
- **Model inventory:** `ListModels` queries the Generative AI management API at `https://generativeai.{region}.oci.oraclecloud.com/20231130/models?compartmentId={compartmentId}`, dropping non-language models (embed, rerank, image) and mapping models to canonical IDs prefixed with `oci-generative-ai/`.

## IBM watsonx.ai REST Connector

The IBM watsonx.ai external backend connector (`kind: watsonx`) integrates AIProxer with IBM watsonx.ai regional machine learning endpoints using native chat and text generation REST APIs.

Key characteristics:
- **Constructed regional ML URL:** Requests target `https://{region}.ml.cloud.ibm.com/ml/v1/text/chat?version={api_version}` (or `_stream` for streaming). Text generation targets `/ml/v1/text/generation` and `/ml/v1/text/generation_stream`. Deployed models target `/ml/v1/deployments/{id}/text/chat`.
- **IBM Cloud IAM API key exchange and refresh:** Production credentials use IBM Cloud IAM (`POST https://iam.cloud.ibm.com/identity/token` with `grant_type=urn:ibm:params:oauth:grant-type:apikey&apikey={apikey}`), caching and proactively refreshing Bearer tokens before expiry.
- **Native watsonx contract:** Direct integration with native IBM watsonx chat and text generation payloads. Not OpenAI-compatible (`/ml/v1/openai` or `/v1/chat/completions`), not ACP, and not LiteLLM.
- **Project XOR Space scope:** Exactly one of `project_id` or `space_id` must be configured; configuring neither or both fails closed.
- **Credentials:** API key is supplied via `ConfigureRequest.Secrets` (`api_key` or `apikey`). Literal secrets or API keys in configuration YAML are strictly forbidden and rejected.
- **Model inventory:** `ListModels` queries foundation model specs at `GET /ml/v1/foundation_model_specs` and ready deployments at `GET /ml/v4/deployments`, dropping withdrawn/deprecated and non-language models (embed, rerank, image) and mapping models to canonical IDs prefixed with `watsonx/` or `watsonx/deployment/`.

## SAP AI Core REST Connector

The SAP AI Core external backend connector (`kind: sapaicore`) integrates AIProxer with SAP AI Core Generative AI Hub deployments using OpenAI-compatible chat completion endpoints.

Key characteristics:
- **Deployment-routed inference URL:** Requests target `POST {AI_API_URL}/v2/inference/deployments/{deploymentId}/chat/completions`. The deployment ID is resolved per request (`sapaicore/{deployment-id}` or default `deployment_id` from configuration).
- **Service key parsing and OAuth:** Production credentials require a standard SAP BTP service key passed connector-locally via `ConfigureRequest.Secrets` (`service_key` JSON containing `clientid`, `clientsecret`, `url`, `serviceurls.AI_API_URL`). The connector performs OAuth2 client credentials exchange (`POST {url}/oauth/token`) and caches Bearer tokens with proactive refresh before expiry. Service key JSON and `clientsecret` are never exposed in diagnostics or error logs.
- **Resource group enforcement:** Every AI API call (both inference and model inventory) applies the required `AI-Resource-Group` header matching `resource_group`.
- **Chat completions only:** Reuses compatible transport strictly for OpenAI-compatible chat completions (`inference_contract: openai-chat`). Responses operations, orchestration pipelines (`.../v2/completion`), and embeddings fail closed.
- **Not ACP:** The connector is an `execution_class: inference` backend, not an Agent Client Protocol runtime.
- **Model inventory:** `ListModels` queries `GET {AI_API_URL}/v2/lm/deployments` with `AI-Resource-Group` and Bearer token, enumerating deployments in `RUNNING` state and mapping them to canonical IDs prefixed with `sapaicore/`.

## Cohere REST Connector

The Cohere external backend connector (`kind: cohere`) integrates AIProxer with Cohere's native v2 chat endpoint using native request and response mapping.

Key characteristics:
- **Native v2 chat endpoint:** Requests target `POST https://api.cohere.com/v2/chat` with native payload structure (`model`, `messages`, `stream`, optional `max_tokens`). Not OpenAI Chat completions, not LiteLLM, and not legacy Cohere v1 (`/v1/chat`).
- **Bearer API key credentials:** Production credentials require an API key passed via `ConfigureRequest.Secrets` (`api_key` or `token`) sent as `Authorization: Bearer {api_key}`. Literal secrets in configuration YAML are strictly forbidden and rejected. Error logs and diagnostics never expose the API key.
- **Native streaming and content parsing:** Streaming requests use `/v2/chat` with `stream: true` and parse Cohere v2 SSE chunks (`content-delta`, `message-end`). Unary responses parse string or content-block array structures losslessly.
- **Fail closed on unsupported semantics:** Supported roles are `system`, `user`, and `assistant`. Tools, non-text parts (vision, images, files), and Responses operations fail closed.
- **Not ACP:** The connector is an `execution_class: inference` backend, not an Agent Client Protocol runtime.
- **Model inventory:** `ListModels` queries `GET https://api.cohere.com/v1/models?endpoint=chat`, dropping non-language models (embed, rerank, image, audio) and mapping models to canonical IDs prefixed with `cohere/`.

## Replicate REST Connector

The Replicate external backend connector (`kind: replicate`) integrates AIProxer with Replicate language-model prediction endpoints using native prediction lifecycle orchestration.

Key characteristics:
- **Explicit prediction lifecycle:** Creates predictions via `POST https://api.replicate.com/v1/models/{owner}/{name}/predictions`. Connects directly to SSE stream (`urls.stream`) when streaming is requested, or polls `urls.get` until a terminal status (`succeeded`, `failed`, `canceled`, `aborted`) is reached. Context cancellation immediately aborts local I/O and issues `POST urls.cancel` to cancel the remote prediction.
- **Frozen v1 inference contract:** Requires `inference_contract: prompt-text`. Concatenates user text parts into `input.prompt`. Tools, vision/non-text parts, non-user roles, and Responses operations fail closed.
- **Not OpenAI-compatible, not ACP, not LiteLLM:** Direct integration with native Replicate model prediction endpoints without translation gateways or Chat Completions compatibility layers.
- **Bearer API token credentials:** Production credentials require an API token passed via `ConfigureRequest.Secrets` as canonical `api_token[_N]` slots (host env `REPLICATE_API_TOKEN[_N]`) sent as `Authorization: Bearer {token}`. Literal secrets in configuration YAML are strictly forbidden and rejected. Error logs and diagnostics never expose the token.
- **Model inventory:** `ListModels` exposes only the configured `owner/name` as `replicate/{owner}/{name}`, verified against `GET /v1/models/{owner}/{name}` (404 fails closed). Executing a model different from the configured model fails closed.

## GitLab Duo REST Connector

The GitLab Duo external backend connector (`kind: gitlab-duo`) integrates AIProxer with GitLab Duo and the Duo Agent Platform (DAP) inference endpoints using GitLab's direct-access token exchange protocol.

**Multi-user deployment:** ships `access_scope: local_only` and `credential_mode: oauth_user`, because its upstream credential is one human user's OAuth session rather than an operator, workload, or service-account credential. See [multi-user eligibility](#multi-user-eligibility-connector-posture-and-a-host-owned-approval).

Key characteristics:
- **Direct-access token exchange:** Requests exchange an authorized GitLab Personal Access Token (PAT) or OAuth session token with the GitLab instance (`POST /api/v4/ai/third_party_agents/direct_access`, accepting 201 Created or any 2xx response) with documented feature flags to obtain a short-lived JSON Web Token (JWT) and required routing headers for the AI Gateway. Entitlement denials (403) fail closed immediately without retry loops.
- **AI Gateway inference targets:** Routes agentic chat inference to GitLab AI Gateway endpoints at `POST /ai/v1/proxy/anthropic/v1/messages` (with `anthropic-beta: context-1m-2025-08-07`) and `POST /ai/v1/proxy/openai/v1/chat/completions`. An optional `ai_gateway_url` override can be configured to point to an enterprise private AI gateway.
- **Truthful client identification:** All HTTP requests truthfully report user agent identifying `aiproxer` and the GitLab Duo connector without impersonating OpenCode or editor plugins.
- **Authentication & OAuth lifecycle:** Supports static PATs via `ConfigureRequest.Secrets` (`pat` or `token`) or file-backed OAuth sessions via `oauth_token_file` using the shared `oauthcred` pattern module. Expired OAuth tokens refresh proactively via `POST /oauth/token`; terminal refresh failures quarantine the stored token record to protect downstream accounts. Self-managed GitLab instances strictly require `oauth_client_id`. Literal secrets or tokens in configuration YAML are strictly forbidden and rejected.
- **Model inventory and lifecycle scoping:** When a `root_namespace_id` or `project_path` is configured, dynamic discovery queries GitLab GraphQL `aiChatAvailableModels` at `POST /api/graphql` on every call (no lifetime in-memory cache). Successful discovery is authoritative: exactly the discovered model refs are advertised. Discovery queries and project lookups fail closed on non-200 responses so the host runtime can apply its cached-enumeration fallback; request cancellation is preserved without fallback. When neither namespace source is configured, GraphQL discovery is skipped and the documented static agentic chat models (`duo-chat-haiku-4-5`, `duo-chat-sonnet-4-5`, `duo-chat-opus-4-5`) are returned as the explicit no-discovery fallback.
- **Strictly an inference connector (Not ACP, No repository tools):** Focuses strictly on model execution and completions. Requests specifying ACP operations (`agent_control`) or non-inference repository management tools (`gitlab_mr_*`, `gitlab_issue_*`, `gitlab_pipeline_*`, repository actions) fail closed with clear out-of-scope errors.

## Nous Portal REST Connector

The Nous Portal external backend connector (`kind: nous-portal`) integrates AIProxer with Nous Portal subscription gateway and Nous inference endpoints using scoped OAuth JSON Web Tokens (JWT).

**Multi-user deployment:** ships `access_scope: local_only` and `credential_mode: oauth_user`, because its upstream credential is one human user's subscription session rather than an operator, workload, or service-account credential. See [multi-user eligibility](#multi-user-eligibility-connector-posture-and-a-host-owned-approval).

Key characteristics:
- **Scoped JWT subscription gateway:** Connects to Nous Portal (`https://portal.nousresearch.com`) and Nous Inference API (`https://inference-api.nousresearch.com/v1`). Mints and refreshes scoped `inference:invoke` JWTs from stored OAuth refresh tokens via `POST /api/oauth/token`. Legacy opaque session-keys or static API keys are supported when provided via secrets.
- **Truthful client identity:** Truthfully reports User-Agent `aiproxer/0.1.0 (nous-portal)` across all Portal and Inference requests. Never sends Hermes client tags (`client=hermes-client-*`), never claims Hermes, and forbids hardcoded Hermes client IDs in production constructor paths.
- **Authentication & OAuth lifecycle:** File-backed OAuth credentials manage refresh tokens via `oauthcred` with 0600 file permissions and 120s skew margin. Strictly requires configured `oauth_client_id` for OAuth flows. Terminal refresh errors (`invalid_grant`, `refresh_token_reused`, 4xx) quarantine stored credentials and prevent refresh replay. Literal secrets in configuration YAML are strictly forbidden and rejected.
- **Entitlement access control:** HTTP 403 Forbidden responses from the inference API indicate entitlement or credit limits and fail closed immediately without entering token refresh loops. Transient HTTP 401 Unauthorized responses trigger token refresh once and retry.
- **Dynamic model catalog:** Queries `GET /models` on the inference endpoint to dynamically discover available models, prefixing canonical model IDs as `nous-portal/{id}` while preserving vendor slugs (e.g. `nous-portal/anthropic/claude-sonnet-4.6`). Discovery queries fail closed on non-200 responses.
- **Inference execution:** Provides OpenAI-compatible Chat completions (`/chat/completions`) for streaming and non-streaming requests. Out-of-scope services (such as Tool Gateway, browser automation, audio/TTS, or ACP) are not supported.

## xAI Subscription OAuth Connector

The xAI Subscription OAuth external backend connector (`kind: xai-oauth`) integrates AIProxer with xAI's subscription service using standard OAuth 2.0 / OIDC credentials and the xAI OpenAI-compatible Chat API.

**Multi-user deployment:** ships `access_scope: local_only` and `credential_mode: oauth_user`, because its upstream credential is one human user's subscription session rather than an operator, workload, or service-account credential. Use the catalog-driven API-key `xai` profile for shared deployments. See [multi-user eligibility](#multi-user-eligibility-connector-posture-and-a-host-owned-approval).

Key characteristics:
- **Distinct from API-key xAI profile:** Operates as a distinct backend plugin (`kind: xai-oauth`, plugin ID `com.aiproxer.backend.xaioauth`) separate from the catalog-driven API-key `xai` profile.
- **OIDC discovery & subscription token refresh:** Performs standard OIDC discovery against `https://auth.x.ai/.well-known/openid-configuration` (or a configured issuer URL) to locate the token endpoint (`https://auth.x.ai/oauth2/token`), exchanging OAuth refresh tokens for short-lived access tokens via `grant_type=refresh_token`.
- **Chat family only (No Responses):** Strictly routes inference to the standard Chat Completions endpoint (`POST /chat/completions`). OpenAI Responses operations (`/v1/responses` or `OperationOpenAIResponses`) are not used or supported for xAI OAuth, ensuring predictable streaming behavior and compatibility.
- **Truthful client identity:** Truthfully reports User-Agent `aiproxer/0.1.0 (xai-oauth)` across all auth and inference requests. Never sends Hermes or Grok CLI client tags, never claims Hermes or Grok CLI identity, and forbids hardcoded Hermes client IDs in production constructor paths.
- **Entitlement access control:** HTTP 403 Forbidden responses from the inference API indicate subscription tier or feature entitlement denials and fail closed immediately without entering token refresh loops. Transient HTTP 401 Unauthorized responses trigger token refresh once and retry.
- **Authentication & OAuth lifecycle:** File-backed OAuth credentials manage refresh tokens via `oauthcred` with 0600 file permissions and 60s skew margin. Strictly requires configured `oauth_client_id` for OAuth flows. Terminal refresh errors (`invalid_grant`, `unauthorized_client`, 4xx) quarantine stored credentials and prevent refresh replay. Literal secrets in configuration YAML are strictly forbidden and rejected. `xai-oauth` is `pre-provisioned-refresh-only`: initial browser/device login is not implemented (no documented public third-party authorization endpoint exists, so reimplementing xAI's first-party login would be private-interface scraping); provision the token file via xAI's official authorization channels first. `Configure` fails closed with an actionable error until a usable credential exists.
- **Dynamic model catalog:** Queries `GET /models` on the inference endpoint (`https://api.x.ai/v1`) to dynamically discover available Grok models, prefixing canonical model IDs as `xai-oauth/{id}` (e.g. `xai-oauth/grok-2`). Discovery queries fail closed on non-200 responses.

## Qwen Portal OAuth Connector

The Qwen Portal OAuth external backend connector (`kind: qwen-oauth`) integrates AIProxer with Alibaba's Qwen Portal subscription service using standard OAuth 2.0 PKCE / refresh tokens and OpenAI-compatible Chat completions at `https://portal.qwen.ai/v1`.

**Multi-user deployment:** ships `access_scope: local_only` and `credential_mode: oauth_user`, because its upstream credential is one human user's subscription session rather than an operator, workload, or service-account credential. Use the catalog-driven `alibaba`/DashScope API-key profile for shared deployments. See [multi-user eligibility](#multi-user-eligibility-connector-posture-and-a-host-owned-approval).

Key characteristics:
- **Distinct from Alibaba/DashScope API-key profiles:** Operates as a distinct external backend plugin (`kind: qwen-oauth`, plugin ID `com.aiproxer.backend.qwenoauth`) separate from catalog-driven `alibaba` and DashScope API-key profiles.
- **Wire request adaptations:** Applies 5 connector-local request adaptations on outbound HTTP Chat payloads:
  - Normalizes string message content into typed text parts (`[{"type": "text", "text": ...}]`).
  - Preserves image URL objects (`{"type": "image_url", "image_url": ...}`).
  - Injects `cache_control: {"type": "ephemeral"}` on the last part of the system message.
  - Sets top-level `vl_high_resolution_images: true` for vision-language models.
  - Places session metadata at top-level `body["metadata"]` rather than nested inside `extra_body`.
- **Chat family only (No Responses):** Strictly routes inference to Chat Completions (`POST /chat/completions`). OpenAI Responses operations (`/v1/responses` or `OperationOpenAIResponses`) are explicitly rejected.
- **Truthful client identity:** Truthfully reports User-Agent `aiproxer/0.1.0 (qwen-oauth)` across all requests. Never sends Hermes or Qwen CLI client tags, never claims Hermes or Qwen CLI identity, and forbids hardcoded Hermes client IDs in production constructor paths.
- **Authentication & OAuth lifecycle:** File-backed OAuth credentials manage refresh tokens via `oauthcred` with 0600 file permissions and 120s skew margin. Strictly requires configured `oauth_client_id` for OAuth flows. Refresh tokens exchange against `https://chat.qwen.ai/api/v1/oauth2/token`. Terminal refresh errors (`invalid_grant`, `unauthorized_client`, 4xx) quarantine stored credentials and prevent refresh replay. Literal secrets in configuration YAML are strictly forbidden and rejected. `qwen-oauth` is `pre-provisioned-refresh-only`: initial browser/PKCE login belongs to Qwen's first-party surfaces and is not reimplemented here (reimplementing that consumer login would be private-interface scraping); provision the token file via Qwen's official authorization channels first. `Configure` fails closed with an actionable error until a usable credential exists.
- **Entitlement access control:** HTTP 403 Forbidden responses from the inference API indicate subscription tier or quota denials and fail closed immediately without entering token refresh loops. Transient HTTP 401 Unauthorized responses trigger token refresh once and retry.
- **Dynamic model catalog:** Queries `GET /models` on the inference endpoint (`https://portal.qwen.ai/v1`) to dynamically discover available Qwen models, prefixing canonical model IDs as `qwen-oauth/{id}` (e.g. `qwen-oauth/qwen-coder-plus`). Discovery queries fail closed on non-200 responses.

## MiniMax OAuth Connector

The MiniMax OAuth external backend connector (`kind: minimax-oauth`) integrates AIProxer with MiniMax's subscription service using standard OAuth 2.0 PKCE / refresh tokens and Anthropic Messages inference endpoints at `https://api.minimax.io/anthropic` (global) or `https://api.minimaxi.com/anthropic` (China).

**Multi-user deployment:** ships `access_scope: local_only` and `credential_mode: oauth_user`, because its upstream credential is one human user's subscription session rather than an operator, workload, or service-account credential. Use the catalog-driven API-key `minimax` profile for shared deployments. See [multi-user eligibility](#multi-user-eligibility-connector-posture-and-a-host-owned-approval).

Key characteristics:
- **Distinct from API-key MiniMax profile:** Operates as a distinct external backend plugin (`kind: minimax-oauth`, plugin ID `com.aiproxer.backend.minimexoauth`) separate from catalog-driven API-key `minimax` profiles. It strictly rejects `MINIMAX_API_KEY` configuration.
- **Anthropic Messages wire transport:** Routes all inference requests via the Anthropic Messages protocol (`POST /v1/messages`) with `anthropic-version: 2023-06-01` and `Authorization: Bearer {token}`. Streaming uses Anthropic Server-Sent Events (SSE) message and content block deltas. OpenAI Responses operations (`/v1/responses` or `OperationOpenAIResponses`) are explicitly rejected.
- **Regional routing:** Defaults to global endpoints (portal `https://api.minimax.io`, inference `https://api.minimax.io/anthropic`). Supports China mainland region (`region: cn`, `region: china`, or `region: minimax-cn`) routing to portal `https://api.minimaxi.com` and inference `https://api.minimaxi.com/anthropic`. Endpoints can also be overridden explicitly via `portal_base_url` and `inference_base_url`.
- **Truthful client identity:** Truthfully reports User-Agent `aiproxer/0.1.0 (minimax-oauth)` across all auth and inference requests. Never sends Hermes client tags, never claims Hermes identity, and forbids hardcoded Hermes client IDs in production constructor paths.
- **Authentication & OAuth lifecycle:** File-backed OAuth credentials manage refresh tokens via `oauthcred` with 0600 file permissions and 60s skew margin. Strictly requires configured `oauth_client_id` for OAuth flows. Device/browser code acquisition via `POST /oauth/code` and polling via `POST /oauth/token` with `grant_type: urn:ietf:params:oauth:grant-type:user_code`. Initial login is operator-driven: `StartLogin` acquires and displays the verification URI + user code, `LoginSession.Complete` polls for approval and persists the minted credential via the existing credential store (clearing any prior quarantine). `Configure` fails closed with an actionable run-login-first error when no usable credential exists. Terminal refresh errors (`invalid_grant`, `refresh_token_reused`, 4xx) quarantine stored credentials and prevent refresh replay. Stored tokens can be re-established upon fresh login. Literal secrets in configuration YAML are strictly forbidden and rejected.
- **Entitlement access control:** HTTP 403 Forbidden responses from the inference API indicate subscription tier or quota denials and fail closed immediately without entering token refresh loops. Transient HTTP 401 Unauthorized responses trigger token refresh once and retry.
- **Dynamic model catalog:** Queries `GET /v1/models` on the inference endpoint to dynamically discover available models, prefixing canonical model IDs as `minimax-oauth/{id}`. Successful enumeration is authoritative: only discovered models are advertised. Empty success advertises empty. All failures (missing token provider, token errors, non-200 responses, decode errors, transport errors) fail closed with an explicit error so the host runtime can apply its cached-enumeration fallback; the connector keeps no static model list. Request cancellation is preserved without fallback.

## Troubleshooting

| Symptom | Likely cause | Action |
|---|---|---|
| Unsafe execution composition error | Mixed `agent_runtime` / `unknown` backend in composite route selector | Use direct routing for agent runtimes, or set `routing.execution_composition_policy: unrestricted` if intended |
| Kind missing in inspect | Artifact not under trusted `paths`, or discovery `enabled: false` | Install manifest+bin; fix `paths`; re-run inspect |
| Unknown field / invalid manifest | Closed schema violation | Fix manifest; unknown keys are rejected |
| Digest mismatch | File rewritten after package | Re-package; do not hand-edit binaries |
| Peer/channel failure in doctor | IPC/ACL/profile mismatch | Fix install permissions/ACLs; do not disable peer checks |
| Configured-missing fail-closed | Enabled backend kind not discovered | Install artifact or disable the row |
| Local-only rejected | `access.mode: multi_user` | Single-user loopback or different connector |
| User OAuth credential rejected | `credential_mode: oauth_user` in `access.mode: multi_user` | Single-user loopback, or a shared-service credential backend |
| Backend not approved for multi-user | Factory absent from the host-owned approval registry (including a brand-new connector) | Single-user loopback, or request an explicit registry change; there is no config override |
| Manifest `oauth_user` + `access_scope: any` rejected | Contradictory manifest semantics | Declare `access_scope: local_only` |
| Development path ignored | `development_mode: false` with only loose paths | Set `development_mode: true` **only** for explicit lab paths |
| Wanted “download plugin” | Unsupported | Package offline; copy artifacts — **no runtime download** |

## Related

- [`authoring.md`](authoring.md) — connector authors
- [`docs/dogfood-local.md`](../dogfood-local.md) — no-key stub workflow
- [`EchoesVault/pages/backend-connector-plugins.md`](../../EchoesVault/pages/backend-connector-plugins.md)
