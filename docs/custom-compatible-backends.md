# Custom Compatible Backends

Built-in compatible backend modes let operators add OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages compatible providers without an external connector plugin. They are dependency-free protocol-family aliases registered in the essential bundle (`built_in_compatible` origin), not executable backend plugins.

Use them when a provider exposes a wire-compatible HTTP API and you do not need a dedicated connector (OAuth bridges, vendor SDKs, agent runtimes, or other non-trivial protocol extensions).

## When to use generic modes vs provider profiles vs external connectors

| Need | Use |
| --- | --- |
| Standard public/cloud provider (Groq, Together, DeepSeek, Kimi, etc.) | Standard **Provider Profiles** (`kind: provider-profile`) — see [Provider Profiles](provider-profiles.md) |
| OpenAI `/chat/completions`, `/responses`, or Anthropic `/v1/messages` against a custom/private base URL | Built-in `custom-*-compatible` kinds (this guide) |
| OpenRouter routing, attribution, and provider-specific headers | Dedicated **OpenRouter external connector** (`openrouter` kind under `connectors/openrouter/`) — not a generic compatible row |
| OAuth/user login, agent IPC, local process spawning, vendor SDK features | Matching external connector under `connectors/` |
| Local no-key stub for development | `local-stub` connector or dogfood examples |

Generic modes reuse the same adapters as native OpenAI/Anthropic essentials with validated endpoint, optional env credentials, shared tokenizer/admission/inventory policy, and canonical parity. They never launch a plugin process and never accept literal YAML secrets.

## Factory kinds

| Factory kind | Upstream API |
| --- | --- |
| `custom-openai-legacy-compatible` | OpenAI-compatible `/chat/completions` |
| `custom-openai-responses-compatible` | OpenAI-compatible `/responses` |
| `custom-anthropic-compatible` | Anthropic-compatible `/v1/messages` |

Each enabled row requires a unique runtime `id` and `config.backend_prefix`. The prefix is used for inventory and prefixed routing. It must not contain `/` or `:`, must not duplicate another enabled backend prefix, and must not collide with built-in or discovered connector prefixes.

## Credentials (environment only)

Literal credential fields in YAML are **rejected** for compatible modes:

- Forbidden: `api_key`, `api_keys`, `credentials`

Use `api_key_env_var_root` to reference numbered environment keys only:

- `ROOT`, then `ROOT_2`, `ROOT_3`, …

Example: `api_key_env_var_root: PROVIDER123_API_KEY` reads `PROVIDER123_API_KEY`, `PROVIDER123_API_KEY_2`, etc.

Omit `api_key_env_var_root` for unauthenticated endpoints (no auth headers are sent).

Restart or reload the process after changing environment variables; config validation does not read live env values into diagnostics output.

## Base URL rules

- Required absolute `http` or `https` URL
- No userinfo (`https://user:pass@host` is rejected)
- No URL fragments
- Host required; explicit ports preserved
- Path prefixes are preserved and joined deterministically for execution and inventory (`/models`, `/chat/completions`, `/responses`, `/v1/messages`, `/v1/models`)

`check-config` validates URLs structurally only; it does not perform DNS or upstream requests.

## Tokenizer and concurrency

Optional fields:

- `tokenizer`: `cl100k_base` or `o200k_base` (omit for default accounting behavior)
- `max_concurrent_requests`: positive per-instance limit (omit or `0` for default admission)

Each instance owns independent tokenizer, credential pool, admission, and inventory state.

## Model inventory

Default: remote discovery using the same endpoint and optional credentials as execution.

- OpenAI-compatible kinds: `<base_url>/models`
- Anthropic-compatible kind: `<base_url>/v1/models`

Override with static inventory:

```yaml
models:
  source: inline   # or file
  items:
    - canonical_id: provider123/deepseek-chat
      native_id: deepseek-chat
      display_name: Provider123 DeepSeek Chat
```

Static inline/file inventory takes precedence over remote discovery. Inventory refresh during serving is separate from execution concurrency unless common policy combines them.

## Diagnostics

`aipstd inventory`, `aipstd routes`, and `aipstd inspect` expose bounded `compatible_backends` rows:

- origin `built_in_compatible`
- factory kind, runtime id, prefix, sanitized endpoint identity
- auth configured yes/no (never secret values)
- tokenizer and concurrency policy
- inventory state (`static_inline`, `static_file`, `remote`, …)

Compatible rows never show plugin process, manifest, or digest fields.

## Examples

Runnable configs (validate with `go run ./cmd/aipstd check-config --config <path>`):

- `config/examples/provider-profiles-bulk.example.yaml` (standard provider profiles)
- `config/examples/custom-openai-legacy-compatible.yaml`
- `config/examples/custom-openai-responses-compatible.yaml`
- `config/examples/custom-anthropic-compatible.yaml`
- `config/examples/custom-compatible-no-auth.yaml`

## Example: OpenAI-compatible provider

```yaml
plugins:
  backends:
    - id: provider123
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: provider123
        base_url: https://api.provider123.example/v1
        api_key_env_var_root: PROVIDER123_API_KEY
```

## Example: Unauthenticated local gateway

```yaml
plugins:
  backends:
    - id: local-openai
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: local-openai
        base_url: http://127.0.0.1:8080/v1
```

## Example: Anthropic-compatible provider

```yaml
plugins:
  backends:
    - id: provider-anthropic
      kind: custom-anthropic-compatible
      enabled: true
      config:
        backend_prefix: provider-anthropic
        base_url: https://api.provider-anthropic.example
        api_key_env_var_root: PROVIDER_ANTHROPIC_API_KEY
```

## Migration from literal YAML secrets

If you previously used `api_key`, `api_keys`, or `credentials` under a compatible backend row:

1. Remove literal secret fields from YAML.
2. Export secrets to environment variables.
3. Set `api_key_env_var_root` to the env key prefix.
4. Run `aipstd check-config` — validation fails until secrets are removed from YAML.

Kinds, runtime ids, `backend_prefix` values, and route selectors remain stable; only secret placement changes.
