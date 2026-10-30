# OpenAI Codex backend

The `openai-codex` backend connects to the ChatGPT Codex Responses API (`https://chatgpt.com/backend-api/codex/responses`). Route selectors use the `openai-codex` prefix, for example `openai-codex:gpt-5.5`.

## Enable

```yaml
plugins:
  backends:
    - id: codex
      kind: openai-codex
      enabled: true
      config:
        access_token: ""   # or api_key
        account_id: ""     # optional ChatGPT account header
```

## Credentials

This connector is single-user only. The standard distribution rejects it when `access.mode: multi_user` is enabled because its credentials are private user OAuth/ChatGPT material whether supplied through explicit tokens, `auth_json_path`, default Codex CLI discovery, or managed OAuth account files.

The rejection happens at composition, before backend construction or connector configuration, and it is not waivable: the factory is absent from the host-owned multi-user approval registry (see [`backend-plugins/operator.md`](backend-plugins/operator.md#multi-user-eligibility-connector-posture-and-a-host-owned-approval)), so it is denied twice over — by `access_scope: local_only` and by the missing approval. The boundary is credential ownership, not a claim that every provider forbids subscription-backed integration: the same user's ChatGPT-plan credential in a **single-user** deployment is fine; what AIProxer refuses is one user's credential becoming an operator-wide credential that unrelated downstream principals can multiplex. There is no configuration flag to allow local-only or user-OAuth backends in a multi-user deployment.

- YAML: `access_token` (preferred) or `api_key`, plus optional `api_keys` / `credentials`.
- Environment: `OPENAI_CODEX_ACCESS_TOKEN`, then numbered `_2`, `_3`, …; falls back to `OPENAI_CODEX_API_KEY` (+ `_N` variants) when access-token vars are unset.
- When neither `access_token` nor `auth_json_path` is set, the connector reads `~/.codex/auth.json` if present (Codex CLI default).

## Token file permissions

On Unix, the `auth.json` file and managed-OAuth account files in `managed_oauth_storage_path` must be owner-only (`0600`). Files readable or writable by group or other are rejected at load time with an error mentioning `group/other accessible`; fix with `chmod 600 <file>`. This mirrors the Codex CLI `auth.json` guard and fails closed on multi-user hosts. On Windows (ACL-based permissions, no meaningful Unix mode bits) this check is a no-op.

Symlinked account files inside the managed-OAuth storage directory are skipped during discovery, so a symlink planted in that directory cannot cause the proxy to read a target outside it. Use real files for managed accounts.

## Optional settings

| Field | Purpose |
| --- | --- |
| `base_url` | Default `https://chatgpt.com/backend-api/codex` |
| `auth_json_path` | Explicit Codex CLI `auth.json` path (overrides default discovery) |
| `refresh_token` | OAuth refresh token when used |
| `oauth_token_url` | OAuth token endpoint (default `https://auth.openai.com/oauth/token`) |
| `oauth_client_id` | OAuth client id (OpenAI Codex CLI default) |
| `account_id` | `ChatGPT-Account-Id` header |
| `default_reasoning_effort` | Default reasoning effort for requests |
| `default_verbosity` | Default verbosity (`low`, `medium`, or `high`) for requests |
| `transport` | `https` (default), `auto`, or `websocket` |
| `experimental_websocket` | Required opt-in for `transport: auto` or `transport: websocket` |
| `websocket_fallback_cooldown_seconds` | Auto-mode cooldown after a pre-output WebSocket failure (default 300) |
| `models` | Static model inventory (inline or file), same shape as other backends |
| `managed_oauth_enabled` | Load OAuth accounts from JSON files in `managed_oauth_storage_path` |
| `managed_oauth_storage_path` | Directory of `*.json` account files |
| `managed_oauth_accounts` | Account ids to use; empty or `all` uses every valid file |
| `managed_oauth_selection_strategy` | `first-available` (default), `round-robin`, or `session-affinity` |
| `managed_oauth_session_affinity_ttl_seconds` | Evict session→account bindings after this many seconds (0 = no TTL) |
| `managed_oauth_session_affinity_max_entries` | Max in-memory session affinity entries (0 = unlimited) |
| `managed_oauth_allow_auth_json_fallback` | Fall back to `auth_json_path` / default auth.json when no usable managed accounts |
| `rate_limit_fallback_seconds` | 429 cooldown when `Retry-After` is missing (default 60) |
| `gpt55_downgrade_disabled` | Disable the free-plan `gpt-5.5` downgrade recovery |
| `gpt55_downgrade_source_model` | Source model for downgrade detection (default `gpt-5.5`) |
| `gpt55_downgrade_target_model` | Target model for free-plan downgrade (default `gpt-5.4`) |
| `plan_type_hint` | Optional plan hint for proactive downgrade tests/local overrides |
| `early_session_verbosity_bump_disabled` | Disable the early-session verbosity bump (default `false`; set `true` to opt out) |
| `early_session_verbosity_bump_turns` | Number of leading per-conversation turns bumped to `high` verbosity (default `5`) |
| `mid_session_verbosity_bump_disabled` | Disable the periodic mid-session verbosity bump (default `false`; set `true` to opt out) |
| `mid_session_verbosity_bump_frequency` | Force `high` on every Nth turn after the early window (default `10`; must be greater than `early_session_verbosity_bump_turns` when the mid-session bump is enabled) |

Without `models`, the connector advertises the Codex model catalog owned by the
external `connectors/codex` module (`connectors/codex/internal/catalog`). When
`catalog_enabled` is true (default), the connector runs `codex debug models` at
Configure and re-runs it on every inventory load, parsing routable model slugs
(and per-model reasoning-effort settings); on discovery failure (or when
`catalog_enabled: false`) it falls back to a shipped embedded snapshot inside
that connector module. Caller cancellation is never masked by fallback: a
canceled load returns the context error. Local-only Codex connectors are rejected
by backend security policy in multi-user mode. No model slugs are hardcoded in
core. Configure catalog paths and the codex binary under the connector instance
`config:` block (`catalog_fallback_path`, `catalog_codex_binary_path` /
`executable`). The Codex app-server backend additionally advertises the `auto`
routing sentinel (the app-server resolves the actual model server-side).

> Backward-incompatible: legacy Codex slugs the CLI no longer advertises (e.g.
> `gpt-5.3-codex`, `gpt-5.1-codex`, `gpt-oss-120b`) are no longer in the
> built-in inventory. To keep them, ship a custom fallback JSON (same `codex
> debug models` format) and set `catalog_fallback_path` on the connector.

## Transport

The default transport is HTTPS/SSE. WebSocket support is experimental and only enabled when `experimental_websocket: true` is set. With that opt-in, `transport: auto` tries `wss://chatgpt.com/backend-api/codex/responses` first and falls back to HTTPS/SSE only if WebSocket fails before the first canonical event. After that first event, stream errors are surfaced and not retried. Use `transport: websocket` to fail instead of falling back during debugging. After a pre-output WebSocket failure, auto mode skips WebSocket for `websocket_fallback_cooldown_seconds` to avoid repeated retry latency.

## Native compaction accounting

Native Responses compaction is fail-closed unless the negotiated backend-plugin
ABI includes protocol minor 5 and the `accounting_evidence_sideband` capability.
Compaction usage is sent as host-only accounting evidence, not as a synthetic
canonical usage event, and is consumed by operator/provider accounting only.
The evidence is bounded and deduplicated by provider-charge key across open,
EOF/error/cancel, WebSocket first-event, and managed-account retry paths. Older
hosts safely use full history without making a billable compaction call.

## Client compatibility (OpenCode / Pi / Droid / Hermes)

OpenCode, Pi, Factory Droid, and Hermes Agent bridge prompts are **not** applied by the backend adapter. Enable the opt-in feature plugin instead:

```yaml
plugins:
  features:
    - id: codex-client-compat
      enabled: true
      config: {}
```

The request-part hook detects client markers from extensions, headers, prompts, and tool names, then mutates the canonical call for selected `openai-codex` backend attempts before backend translation. It no-ops for other backends. For Hermes-marked calls it also sets the `openai_codex.tool_strict=false` extension, which makes the Codex payload emit Responses tools with `strict=false` and defaults `parallel_tool_calls` to `true` when unset.

## Routing example

```yaml
routes:
  default: "openai-codex:gpt-5.5"
```

Bracket parameters such as `?reasoning_effort=high&verbosity=low` are supported in route selectors.

## Per-request routing

A client can override the configured default route per request by putting a full route
selector in the request body `model` field, with optional URI parameters:

```json
{ "model": "openai-codex:gpt-5.5?reasoning_effort=low&verbosity=high", "input": "ping" }
```

The `openai-codex:` prefix selects the backend, the model name selects any model (the
built-in inventory comes from the auto-discovered `codex debug models` catalog;
arbitrary model strings can still be routed even if not listed), and the `reasoning_effort`
URI parameter is converted into the canonical call options and then into the Codex payload
`reasoning.effort` field. An explicit `X-AIP-Route` header, when present, takes precedence
over the body `model`. A bare model name without a backend prefix still falls back to the
configured default route.

URI parameters are explicit routing directives and **override** any corresponding value
set elsewhere: `?reasoning_effort=xhigh&verbosity=high` wins over matching request-body
values and over `default_reasoning_effort`/`default_verbosity`. A parameter absent from the
selector leaves the other value in effect. The Codex HTTP payload emits verbosity as
`text.verbosity`.

## Early-session verbosity bump

The connector bumps `text.verbosity` to `high` for the first few turns of each
conversation so early responses carry more detail. The bump is enabled by default;
set `early_session_verbosity_bump_disabled: true` to opt out, and tune the window
with `early_session_verbosity_bump_turns` (default `5`).

A "turn" is one prompt plus one response round-trip with the Codex backend, counted
per conversation using the stable conversation identity order: continuity key,
then correlation id, then input-derived conversation id.
Tool-call continuations count as separate turns. Early- and mid-session bumps share
one in-memory, per-process turn counter (TTL-bounded and LRU-capped; not shared
across replicas). Turn numbers are reserved atomically when an open is prepared,
committed when that open succeeds, and released if that open ultimately fails, so
concurrent opens for the same conversation cannot observe the same turn number,
failed attempts do not consume slots, and idle TTL/eviction cannot reuse an
in-flight reservation.

The bump only applies when the request has no explicit per-request verbosity: an
explicit `?verbosity=` URI parameter or request-body `verbosity` option always wins,
even during the early window. After the configured number of turns, normal
resolution resumes (explicit per-request value, else `default_verbosity`).

## Mid-session verbosity bump

The connector also bumps `text.verbosity` to `high` on every configured
frequency turn after the early window, so mid-session responses periodically
carry more detail. The bump is enabled by default; set
`mid_session_verbosity_bump_disabled: true` to opt out, and tune the cadence
with `mid_session_verbosity_bump_frequency` (default `10`).

Mid-session bumps use the same shared per-conversation turn counter as the early-
session bump (atomic reserve on prepare, commit on success, release on failed open).
Tool-call continuations count as separate turns.

The bump only applies when the request has no explicit per-request verbosity:
an explicit `?verbosity=` URI parameter or request-body `verbosity` option
always wins, even on a frequency turn. When the mid-session bump is enabled,
the cadence must be greater than the early-session window length, so the
default `10` pairs with the default early window of `5` to produce hits at
turns `10`, `20`, `30`, and so on. When the mid-session bump is disabled, the
cadence value is ignored.

## Unsupported generation parameters

The Codex Responses API does not support `temperature`, `top_p`, or `max_output_tokens`.
Plain calls that set any of these fail at payload-build time with an explicit error.
The `openai_codex.ignore_unsupported_gen_params` canonical-call extension (bool, `true`)
opts in to dropping them instead — the `codex-client-compat` feature sets this for
detected compatibility clients (OpenCode, pi, Factory Droid, Hermes) so optional tuning
params are not forwarded upstream and do not fail the request. `reasoning_effort` and
`parallel_tool_calls` are honored.

## Model name normalization

Clients that use a `provider/model` namespace (for example OpenCode's `openai/gpt-5.4-mini`)
have the leading `openai/` prefix stripped before the model reaches the Codex upstream, which
rejects org-prefixed model names. A bare model name such as `gpt-5.4-mini` is sent unchanged.

## System messages

The Codex Responses API rejects `system`-role items in `input` ("System messages are not
allowed"). System content must be carried in the `instructions` field. The connector folds
system-role messages from the conversation into `instructions` (deduplicated against explicit
instructions, including the `codex-client-compat` bridge) and omits them from `input`, so
clients that send a system prompt (for example OpenCode) interoperate without a capability
mismatch.

## Tool schemas

The Codex Responses API requires function-tool parameter schemas to be
"strict-compatible" when sent with `strict:true`: every object must declare
`additionalProperties:false` and list all of its properties in `required`.
Clients that emit looser schemas (for example OpenCode's `apply_patch`, which
omits `additionalProperties`) would otherwise be rejected with
`invalid_function_parameters`. The connector inspects each tool schema and
sends `strict:false` for any schema that is not strict-compatible, while
keeping `strict:true` for strict-compatible and parameterless schemas. This is
a safe relaxation — it only disables strict validation and never causes an
upstream rejection. The Hermes compatibility bridge keeps its existing
`tool_strict:false` behavior (all tools relaxed).

## Tool call history

When a client sends a prior assistant tool call and its result back (for example
OpenCode following up after executing a tool), the Chat Completions frontend
encodes the assistant tool call as a `PartJSON` item in the Chat Completions
shape (`type:"function"` with a nested `function:{name,arguments}` object and
`id` as the call id). The connector translates that into a Codex Responses
`function_call` input item (using the Chat Completions `id` as the `call_id`)
and the matching `tool`-role result into a `function_call_output` item with the
same `call_id`, so the upstream sees a correctly linked call/output pair. The
`codex-client-compat` bridge recognizes Chat Completions-style tool calls when
matching tool results, so results that belong to a known call are preserved
rather than treated as orphaned.
