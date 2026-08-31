# Backend Overview

The LLM Interactive Proxy supports multiple backend providers, allowing you to route requests to different LLM services while maintaining a consistent front-end API. This flexibility enables you to choose the best provider for your use case, switch providers without changing client code, and implement failover strategies.

## Supported Backends

Backend IDs are the `type:` values in YAML and the `backend_type` carried on requests. **Core connectors** live in this repository and are always import-registered. **OAuth plugin connectors** ship in the sibling package **`llm-interactive-proxy-oauth-connectors`** and register when you install the optional extra, for example `pip install "llm-interactive-proxy[oauth]"` (see `pyproject.toml` optional dependency `oauth`).

### Core connectors (this repository)

| Backend ID | Provider | Authentication | Best For |
|------------|----------|----------------|----------|
| `openai` | OpenAI | API Key | Production applications, standard OpenAI models |
| `openai-responses` | OpenAI | API Key | Same credentials as OpenAI; targets `/v1/responses` for structured outputs (see [OpenAI backend](openai.md#openai-responses-backend)) |
| `openai-codex` | OpenAI (ChatGPT / Codex CLI) | Local OAuth token | ChatGPT login instead of an API key |
| `openai-codex-app-server` | OpenAI (Codex CLI `app-server --stdio`) | Local OAuth token | Local Codex agent over stdio (native app-server JSON-RPC, not ACP); requires `codex` on PATH or `CODEX_BIN`; local/single-user only |
| `anthropic` | Anthropic | API Key | Claude via the standard Anthropic API |
| `alibaba-token-plan-intl` | Alibaba Cloud Model Studio Token Plan (International) | API Key (`ALIBABA_TOKEN_PLAN_API_KEY`) | Anthropic-compatible agent traffic with dynamic plan-specific model discovery |
| `gemini` | Google Gemini | API Key | Metered API usage, production apps |
| `gemini-cli-acp` | Google Gemini (ACP via Gemini CLI) | Local OAuth token | Sub-agents and tooling via Gemini CLI |
| `cursor-cli-acp` | Cursor (ACP via Cursor CLI `agent acp`) | Local Cursor login (`agent login`); optional `CURSOR_API_KEY` discovery fallback | Cursor-hosted models through the official CLI; requires `agent` on PATH or `CURSOR_AGENT_BIN` |
| `eve-acp` | Vercel Eve (ACP via `eve acp`) | Local Eve runtime | Agent runtime execution with native ACP protocol over stdio; requires `eve` on PATH or `EVE_BINARY` |
| `gemini-cli-cloud-project` | Google Gemini (GCP) | OAuth + GCP project | Enterprise / team billing on Vertex-style flows |
| `openrouter` | OpenRouter | API Key | Many third-party hosted models behind one API |
| `nvidia` | NVIDIA (NIM / OpenAI-compatible) | API Key (`NVIDIA_API_KEY`) | NVIDIA integrator or self-hosted NIM |
| `runinfra` | Runinfra (runinfra.ai / OpenAI-compatible) | API Key (`RUNINFRA_API_KEY`) | runinfra.ai hosted models |
| `zenmux` | ZenMux | API Key | OpenAI-compatible ZenMux router |
| `zai` | ZAI | API Key | Zhipu / Z.ai |
| `zai-coding-plan` | ZAI Coding Plan | API Key | Coding-plan SKU / workflows |
| `kimi-code` | Kimi | API Key | Kimi For Coding (OpenAI-compatible) |
| `opencode-go` | OpenCode Go | API Key | OpenCode Go with internal OpenAI/Anthropic-style routing |
| `minimax` | Minimax | API Key | Minimax models |
| `internlm` | InternLM | API Key (rotation supported) | InternLM with optional key rotation |
| `ollama` | Ollama | None (local) | Local and remote models via Ollama |
| `hybrid` | Virtual (two backends) | Inherits from sub-backends | Two-phase reasoning + execution |

### OAuth plugin connectors (`llm-interactive-proxy-oauth-connectors`)

These entry points are defined in the sibling repo’s `pyproject.toml` under `[project.entry-points."llm_proxy_backends"]`. They are **not** present unless the optional package is installed.

| Backend ID | Provider | Authentication | Best For |
|------------|----------|----------------|----------|
| `antigravity-oauth` | Google Gemini (Antigravity) | Antigravity token | Internal / debugging (Gemini-shaped traffic) |
| `cline` | Cline | Local OAuth token | Internal development and compatibility testing |
| `gemini-oauth-auto` | Google Gemini (CLI) | Multi-account OAuth | Automatic account rotation across Google logins |
| `gemini-oauth-plan` | Google Gemini (CLI) | OAuth | Google One / paid CLI tier |
| `gemini-oauth-free` | Google Gemini (CLI) | OAuth | Free-tier CLI usage |
| `kiro-oauth-auto` | Amazon Kiro / Q Developer | Self-managed OAuth | Kiro streaming via local OAuth tokens |
| `opencode-zen` | OpenCode Zen | OAuth | OpenCode Zen API (distinct from `opencode-go`) |
| `qwen-oauth` | Alibaba Qwen (CLI) | Local OAuth token | Qwen CLI OAuth |

## Agent Client Protocol (ACP) backends

The `gemini-cli-acp` and `cursor-cli-acp` backends spawn a local agent subprocess for each pooled workspace/session key (see connector implementation for pooling). After each **completed chat turn** (assistant response finished), the proxy schedules termination of that subprocess if it stays **idle** for `stale_acp_agent_kill_idle_seconds` (default **3600** seconds = 60 minutes). When you send another message or reuse the same pooled agent, the pending timer is **cancelled**; after the next completed turn, a **new** idle timer is scheduled.

The Cursor `/v1/responses` compatibility path is an exception: each Responses turn uses an isolated subprocess and is reaped immediately after completion or cancellation. Chained Responses turns replay the stored visible transcript into a fresh process.

The proxy completes a fail-soft model-catalog refresh before startup readiness. This refresh does not create agent conversations, select a workspace, or start an authentication flow. Cursor instances are enumerated with `agent --list-models`; configured `cursor_cli_extra_args` and `cursor_api_endpoint` are applied to that command exactly as they are applied to ACP startup, and `cursor_model_discovery_timeout_seconds` controls its subprocess deadline. Codex App Server instances reuse the catalog discovered by the existing `codex debug models` startup stage. Gemini CLI ACP and Agy CLI ACP do not currently expose an equivalent safe catalog command, so they advertise only the exact top-level `models` entries in their backend-instance configuration. Empty or failed instance catalogs are omitted without hiding successful instances, and `/v1/diagnostics` reports each configured instance's discovery source, status, model count, and error code.

Local-agent catalog routes are instance-pinned, for example `agy-cli-acp.project:google/gemini-3.5-flash-high`, so project-bound instances cannot collapse into generic round-robin routing. A Codex discovery fallback snapshot advertises only `openai/auto`; discovered model slugs are advertised only when live Codex catalog discovery succeeds.

### Cursor CLI ACP as a Codex custom provider

`cursor-cli-acp` has a dedicated `/v1/responses` projection path for text-only Planner and Advisor agents. Configure Codex without changing its root provider:

```toml
[model_providers.lip_local]
name = "Local LLM Interactive Proxy"
base_url = "http://127.0.0.1:8000/v1"
wire_api = "responses"
requires_openai_auth = false
```

Use an instance-pinned route returned by `GET /v1/models`, for example `cursor-cli-acp.default:cursor/glm-5.2-max`. Every Cursor route contains the full backend instance name, so multiple project-bound instances are never collapsed into a generic round-robin selector. The catalog is sourced from the installed Cursor CLI's `agent --list-models` output; when `cursor_api_endpoint` is configured, discovery uses the same `-e <endpoint>` option as the ACP subprocess. Cursor-native CLI IDs such as `cursor-grok-4.5-high` are exposed as `cursor/grok-4.5-high` while the exact CLI ID is retained for process startup.

Auth policy is cookie-first: ACP children prefer `agent login` credentials and strip inherited `CURSOR_API_KEY` / `CURSOR_AUTH_TOKEN` when cookie auth is usable. Discovery tries cookie-only env first, then retries with a discovery key when `--list-models` fails authentication (some CLI builds require a key for listing even though login cookies work for ACP/`agent status`). The discovery key comes from process env `CURSOR_API_KEY` / `CURSOR_AUTH_TOKEN` when set, otherwise from the `apiKey` field in Cursor's local login store (`%APPDATA%\Cursor\auth.json` on Windows). When the catalog is empty but cookie or key auth is still usable, requested models are allowed via deterministic CLI-id mapping (`cursor/foo` → `cursor-foo`). When auth is unusable, requests fail with `cursor_cli_auth_unavailable`. When the catalog is populated, unadvertised models still fail with `cursor_model_unavailable`. The connector never substitutes another advertised model.

This compatibility path is deliberately **not an Executor provider**. Cursor ACP is an agent runtime whose native tool activity cannot currently be translated into Codex-owned Responses function calls. Responses-projected turns use a separate subprocess launched with `--mode ask`; requests containing `tools`, `tool_choice`, or `parallel_tool_calls` fail with `provider_limitation`, and ACP permission requests are rejected. That is not a no-tools guarantee: live acceptance testing showed Cursor's ask mode executing a built-in Edit File operation without issuing a permission request. Historical `function_call` and matching `function_call_output` items retain their `call_id` during conversation projection, but Cursor cannot originate a Codex-governed tool round trip through this path. Use it for Planner/Advisor roles only with a dedicated disposable workspace or an external OS/container sandbox; do not point it at a valuable project workspace on the assumption that ask mode is read-only.

Workspace selection is project-specific rather than request-driven. Set `workspace_path` to an existing readable absolute path in a dedicated backend instance, or set `CURSOR_CLI_WORKSPACE` before starting the proxy. Relative paths, an implicit proxy working directory, prompt-derived paths, and per-request `project_dir` / `workspace_path` / `cwd` / `project` overrides are rejected. Create a separate constrained backend instance for each project; standard Responses requests cannot safely select arbitrary host paths.

Legacy `/v1/chat/completions` requests retain the existing session workspace behavior: when no static Cursor workspace is configured, initialization succeeds and ACP model discovery is deferred until the request pipeline supplies the validated absolute `session.state.project_dir`. This dynamic legacy behavior does not apply to `/v1/responses`.

Every Cursor Responses turn receives a fresh, isolated ACP subprocess. The in-process Responses session store carries the complete visible text transcript for `previous_response_id` replay, so continuation does not depend on keeping the original Cursor process alive. The store is process-local and does not cross worker or proxy-process boundaries.

For model names ending in an effort suffix such as `-xhigh` or `-max`, that suffix is authoritative. Omit `reasoning.effort`, or send the identical suffix value. A contradictory effort fails explicitly and a matching value is validation-only; it is not forwarded as a second effort selector.

### `openai-codex-app-server` (Codex native app-server, not ACP)

`openai-codex-app-server` is a sibling local-agent backend that reuses the same pooling / request-lock / cancellation / idle-reap / stale-kill / shutdown lifecycle as the ACP backends, but speaks Codex's **native app-server JSON-RPC 2.0 protocol** over stdio (not ACP). It launches Codex equivalent to:

```
codex --dangerously-bypass-approvals-and-sandbox --search app-server --stdio
```

and performs the strict handshake (`initialize` -> `initialized` -> `thread/start` -> `turn/start`), streams `item/agentMessage/delta` into OpenAI SSE `delta.content`, surfaces reasoning as visible `Thinking:` blocks, emits compact progress summaries for plan/command/file activity (never raw command stdout, full diffs, or secrets), and ends each stream with `data: [DONE]` on `turn/completed`. URI params map as: `reasoning_effort` -> `turn/start.effort`, `verbosity` -> spawn-time `-c model_verbosity=...` (process restart when it changes), and the model selector -> `thread/start.model` / `turn/start.model` (the optional `openai/` prefix is stripped; `model: "auto"` omits the model fields so Codex uses its configured default).

**Safety posture (local/single-user only — not suitable for production multi-user exposure):** the primary posture is Codex's bypass mode (no sandbox, no approval prompts). If approval server-requests still arrive, the connector auto-accepts known approval methods (`execCommandApproval`, `applyPatchApproval`, `item/commandExecution/requestApproval`, `item/fileChange/requestApproval`, `item/permissions/requestApproval`) and **fails closed** for interactive user-input requests that cannot be answered headlessly (`item/tool/requestUserInput`, `mcpServer/elicitation/request`, and any unknown server-request method). Every auto-approval is logged at INFO with method, workspace, model, and a sanitized summary. Because it uses local personal Codex auth, it is treated as an OAuth connector and is **not loaded in Multi User Mode**. Like the other local-agent backends, it requires a usable workspace directory (session `project_dir` or request `project_dir`/`workspace_path`/`cwd`/`project`).

This idle cleanup is **enabled by default**. To disable it:

- CLI: `--disable-stale-acp-agent-kills`
- Environment: `DISABLE_STALE_ACP_AGENT_KILLS=true`
- Configuration file: `disable_stale_acp_agent_kills: true`

To change the idle delay:

- CLI: `--stale-acp-agent-kill-idle-seconds <seconds>`
- Environment: `STALE_ACP_AGENT_KILL_IDLE_SECONDS=<seconds>`
- Configuration file: `stale_acp_agent_kill_idle_seconds: <seconds>`

**psutil** is a required runtime dependency (declared in `pyproject.toml`). Before terminating a child, the proxy uses it to verify the OS process is still the same one it spawned (creation time and, when available, executable path), so an unrelated process that reused the PID is not killed. The code also has a defensive import fallback: if `psutil` cannot be imported at runtime, idle-kill falls back to the subprocess handle only (weaker).

Precedence: **CLI** overrides **environment** overrides **configuration file**. INFO-level logs describe when a kill is scheduled, cancelled, or executed.

## Frontend APIs

The proxy exposes multiple frontend APIs where clients connect. Each frontend implements a different LLM provider's API specification.

For detailed frontend API documentation, see the [Frontend Overview](../frontends/overview.md):

- [OpenAI Chat Completions](../frontends/openai-chat-completions.md) - `/v1/chat/completions`
- [OpenAI Responses API](../frontends/openai-responses.md) - `/v1/responses`
- [Anthropic Messages](../frontends/anthropic.md) - `/anthropic/v1/messages`
- [Google Gemini v1beta](../frontends/gemini.md) - `/v1beta/models`

## Choosing a Backend

When selecting a backend, consider:

- **Cost**: API key-based backends typically charge per token, while OAuth-based backends may have subscription or free tier limits
- **Performance**: Different providers have different latency and throughput characteristics
- **Model Availability**: Each provider offers different models with varying capabilities
- **Authentication**: Choose between API keys (simpler) or OAuth (may offer free tiers)
- **Use Case**: Some backends are optimized for specific tasks (e.g., `zai-coding-plan` for coding)
- **Tooling Model**: Some CLI-mediated backends are better suited for specialized sub-agents than for acting as the main general-purpose coding agent for the whole session

## Configuration

Backends are configured through environment variables and the proxy configuration file:

### Basic Setup

```bash
# Set API keys for the backends you want to use
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."
export ALIBABA_TOKEN_PLAN_API_KEY="sk-sp-..."
export GEMINI_API_KEY="AIza..."
export OPENROUTER_API_KEY="sk-or-..."
export NVIDIA_API_KEY="..."
export ZENMUX_API_KEY="..."
export ZAI_API_KEY="..."
export KIMI_API_KEY="..."
export MINIMAX_API_KEY="..."
export INTERNAI_API_KEY="..."

# For GCP-based Gemini
export GOOGLE_CLOUD_PROJECT="your-project-id"
```

### Starting the Proxy

```bash
# Start with a specific default backend
python -m src.core.cli --default-backend openai

# Or specify in config file
python -m src.core.cli --config config/config.yaml
```

### Config File Example

```yaml
# config.yaml
backends:
  openai:
    type: openai
  anthropic:
    type: anthropic
  gemini:
    type: gemini

default_backend: openai
```

## Switching Backends

You can switch backends dynamically during a session using in-chat commands:

```
!/backend(anthropic)
!/model(claude-3-5-sonnet-20241022)
```

Or use one-off commands for a single request:

```
!/oneoff(openrouter:qwen/qwen3-coder)
```

## Backend-Specific Documentation

For detailed configuration and usage information for each backend, see:

**Core**

- [OpenAI and OpenAI Responses](openai.md) (`openai`, `openai-responses`)
- [OpenAI Codex](openai-codex.md) (`openai-codex`)
- [Anthropic](anthropic.md)
- [Alibaba Token Plan (International)](alibaba-token-plan-intl.md) (`alibaba-token-plan-intl`)
- [Gemini](gemini.md) (API keys, CLI OAuth variants, `gemini-cli-acp`, and `gemini-cli-cloud-project`)
- **Cursor CLI ACP** (`cursor-cli-acp`): same idea as Gemini CLI ACP but via Cursor’s `agent acp` CLI; install and log in with `agent login` (preferred for ACP). Optional `CURSOR_API_KEY` can help model discovery on CLI builds that reject cookie-only `--list-models`. Ensure `agent` is on `PATH` or set `CURSOR_AGENT_BIN`. There is no separate backend guide page yet.
- **Vercel Eve ACP** (`eve-acp`): Native Agent Client Protocol backend via `eve acp`; executes Eve as an agent runtime with internal tool loops, streamed reasoning, and process-per-session isolation. Ensure `eve` is on `PATH` or configure `eve_executable` / `EVE_BINARY`.
- [OpenRouter](openrouter.md)
- [NVIDIA](nvidia.md)
- [ZAI](zai.md)
- [Kimi Code](kimi-code.md)
- [OpenCode Go](opencode-go.md)
- [Ollama](ollama.md)
- [InternLM](internlm.md)
- [MiniMax](minimax.md)
- [ZenMux](zenmux.md)
- [Hybrid backend](../features/hybrid-backend.md) (`hybrid`)

**OAuth plugin (`llm-interactive-proxy-oauth-connectors`)**

- [Antigravity OAuth](antigravity-oauth.md)
- [Cline](cline.md)
- [Gemini OAuth Auto](gemini-oauth-auto.md) (`gemini-oauth-auto`; overview also in [Gemini backends](gemini.md))
- [Kiro OAuth Auto](kiro-oauth-auto.md)
- [OpenCode Zen](opencode-zen.md)
- [Qwen OAuth](qwen.md)
- [Gemini OAuth plan / free](gemini.md) (`gemini-oauth-plan`, `gemini-oauth-free`)

**Extensibility**

- [Custom Backends](custom-backends.md)

## Related Features

- [Model Name Rewrites](../features/model-name-rewrites.md) - Transform model names dynamically
- [Hybrid Backend](../features/hybrid-backend.md) - Use two models in sequence
- [URI Model Parameters](../features/uri-model-parameters.md) - Specify parameters in model strings
