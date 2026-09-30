# OpenAI ChatGPT Plan Backend (`openai-chatgpt-plan`)

Official ChatGPT Plus/Pro subscription path using Sign in with ChatGPT (SIWC)
and the public OpenAI Responses API.

This backend is **not** the API-key `openai` / `openai-responses` backends, and
it is **not** the legacy private Codex family (`openai-codex*`). Legacy Codex
backends remain temporarily available until the empirical acceptance gate
succeeds; they are pending retirement.

## What it uses

- Authorization: Sign in with ChatGPT (dynamic client registration + PKCE)
- Inference: `POST https://api.openai.com/v1/responses`
- Models: per-profile `GET https://api.openai.com/v1/models`
- Upstream inference always sends `store=false` and `stream=true`

## Profile management

```bash
./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py list
./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py add --profile-id primary
./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py show primary
./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py reauthorize primary
./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py refresh primary
./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py signout primary
./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py remove primary
./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py export primary --output profile.json
./.venv/Scripts/python.exe scripts/manage_openai_chatgpt_plan_profiles.py import profile.json --profile-id primary
```

`list` / `show` never print access, refresh, or ID tokens. `export` is an
explicit secret-bearing operation. Import never overwrites the destination
host's `ext_agent_host_id`.

## Configuration

Environment overrides use the `OPENAI_CHATGPT_PLAN_*` namespace only (no
`OPENAI_CODEX_*` aliases):

```bash
export OPENAI_CHATGPT_PLAN_PROFILE_ID=primary
export OPENAI_CHATGPT_PLAN_PROFILES_PATH=var/openai_chatgpt_plan/profiles
export OPENAI_CHATGPT_PLAN_HOST_STATE_PATH=var/openai_chatgpt_plan/host.json
export OPENAI_CHATGPT_PLAN_OAUTH_CALLBACK_PORT=1455
export OPENAI_CHATGPT_PLAN_MODEL_CATALOG_TTL_SECONDS=300
```

YAML example:

```yaml
backends:
  openai_chatgpt_plan:
    type: openai-chatgpt-plan
    extra:
      chatgpt_plan:
        profile_id: primary
        profiles_path: var/openai_chatgpt_plan/profiles
        host_state_path: var/openai_chatgpt_plan/host.json
        oauth:
          callback_port: 1455
        model_catalog:
          ttl_seconds: 300
```

## Current SIWC limitations

Official SIWC preview currently rejects several Responses fields and hosted
tools (for example temperature/top_p/metadata and image generation / file
search / Code Interpreter). Explicit unsupported requests raise a typed
provider-limitation error. Incidental unsupported defaults are stripped.

## Troubleshooting

| State | Meaning | Action |
|---|---|---|
| `missing_plan_scope` | Plan-use scope not granted | Reauthorize and grant `chatgpt.tokens.use.direct` |
| `needs_reauth` | Refresh token terminal | `reauthorize` the selected profile |
| `signed_out` | Local tokens cleared | `reauthorize` |
| ambiguous profile | Multiple profiles, no explicit selection | Set `profile_id` or pass explicit selection |
| model discovery unavailable | Temporary `/v1/models` failure | Retry later; no Codex catalog fallback |

## Distinctions

| Backend | Auth | Protocol |
|---|---|---|
| `openai` / `openai-responses` | API key | Public OpenAI APIs |
| `openai-chatgpt-plan` | SIWC ChatGPT plan | Public Responses + `/v1/models` |
| `openai-codex*` (legacy) | Old Codex OAuth / private endpoints | Pending retirement after acceptance gate |
