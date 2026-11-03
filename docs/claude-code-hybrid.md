# Claude Code CLI hybrid routing (experimental)

The Anthropic frontend has a disabled-by-default, loopback-only mode for keeping
native Claude requests on the CLI's original Anthropic login while sending exact
custom model aliases to configured credential-owning backends. It arms **only**
when both `User-Agent: claude-cli/...` and `x-app: cli` identify Claude Code CLI.
These spoofable hints recognize a client; they are not authentication. Other
clients keep the ordinary canonical frontend behavior.

## Configuration

This example uses MiniMax M2.7 through OpenRouter, with the existing OpenAI Chat
adapter. Set `OPENAI_API_KEY` in the **proxy process only** to the OpenRouter key;
never replace the CLI's Anthropic login with that key. Other credential-owning
backends can be selected with their exact configured single-leaf route.

```yaml
server:
  address: "127.0.0.1:8080"
  write_timeout: "6m"
access:
  mode: single_user
auth:
  handler: local_noop
  required_level: none
continuity:
  store: memory
plugins:
  frontends:
    - id: anthropic
      enabled: true
      config:
        expose_aip_usage_extensions: false
        claude_code_hybrid:
          enabled: true
          external_models:
            - model: aiproxer-reviewer
              route: minimax:minimax/minimax-m2.7
              require_subagent: true
  backends:
    - id: minimax
      kind: openai-legacy
      enabled: true
      config:
        base_url: https://openrouter.ai/api/v1
        models:
          source: inline
          items:
            - canonical_id: minimax/minimax-m2.7
              native_id: minimax/minimax-m2.7
  features:
    # Explicit opt-outs: aipstd otherwise injects these defaults.
    - id: tool-call-repair
      enabled: false
    - id: reasoning-output-preservation
      enabled: false
```

V1 refuses global model aliases, route-override administration, durable continuity,
enabled canonical feature plugins other than the three standard no-ops, accounting
or metering, mandatory durable session audit/workspace policy, strict reasoning
attempt policy, and fail-closed tool-reactor policy. Native opaque traffic cannot
honor those policies. Do not disable a required policy to use this mode; use the
ordinary canonical frontend instead. Ingress auth, self-defense, tracing, and
security headers remain in place.

Runtime composition separately rejects hybrid mode when the process owns a
monetary binding, including storeless Enterprise bindings without reports/admin
mounts. This check runs before startup or reload candidate composition; a rejected
reload does not replace the active generation. HTTP admin mounts or capability
declarations are not used to infer billing presence.

Create `.claude/agents/reviewer.md` in the client project:

```markdown
---
name: reviewer
description: Review code using the configured external model
model: aiproxer-reviewer
tools: Read
---
Review the requested code and report findings.
```

Launch the stock CLI with **only its API base URL changed**:

```sh
ANTHROPIC_BASE_URL=http://127.0.0.1:8080 claude
```

Keep the original Claude login. Do not introduce an AIProxer
`ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY`, use `--bare` (which disables OAuth),
change the main model to the external alias, or globally replace Haiku/Sonnet.

## Dispatch and limitations

- Exact external aliases select only their configured single `backend:model`
  leaf. Duplicate/native-colliding aliases, composites and unknown backends fail
  configuration. Unknown request models and client route headers (including
  configured aliases) fail before upstream I/O. `require_subagent` requires
  `x-claude-code-agent-id` and refuses a contradictory main request class.
- Original auth, cookies, proxy credentials, OAuth/beta and client control headers
  are absent from the fresh canonical external request. The configured backend
  supplies its credential. External tool results are projected into canonical
  tool-role messages in original order. Inline worker `system` messages retain
  their system role and original history position, without being hoisted into
  top-level instructions. The ordinary Anthropic Messages frontend admits the
  same shape so stock worker histories keep their positions; downstream backend
  contracts determine fidelity: Chat and Responses preserve the in-place role,
  Anthropic Messages and Gemini reject it before upstream I/O, and Bedrock and
  the commandcode-anthropic connector lift it to the top-level system field,
  preserving the text but not its mid-history position. The full history is
  retained; each external request opens a fresh proxy session rather than
  accepting body or header resume authority, preventing inherited routing
  overrides. Proxy-side cross-turn continuity is not provided for these workers
  in V1.
- Native `claude-*` requests, main or subagent, go only to
  `https://api.anthropic.com/v1/messages`, with original JSON bytes, one original
  first-party credential, explicitly safe protocol headers, and opaque response
  bytes (including SSE/thinking signatures). Redirects/environment proxies are
  disabled. Request bodies default to 8 MiB (the existing server setting can
  override this); native exchanges have a five-minute lifetime and observe client
  cancellation and generation retirement. Standard composition requires
  `server.write_timeout` of at least **6 minutes**, allowing ingress/drain
  headroom around that exchange budget. The ordinary 120-second default is
  incompatible and is rejected when hybrid mode is enabled. Direct mounts must
  configure an equivalent downstream server write budget; any client or outer
  reverse-proxy timeout can still impose a shorter limit.
- Native traffic skips canonical guardrails, usage/cost accounting and canonical
  traffic capture. Logs carry `canonical_processing=false`; responses carry
  `X-AIP-Canonical-Processing: false`. No proxy usage or cost is invented.
- Only `POST /v1/messages`, optionally the CLI's proven fixed `?beta=true`, is
  admitted. Arbitrary queries, absolute URLs and non-loopback Host/peer addresses
  are refused. Case variants of the top-level `model` key (including a second
  `MODEL`/`Model` alongside `model`) are also refused before native or external
  I/O, preventing case-insensitive canonical decoding from changing the admitted
  model identity. Token counting, model discovery and other native operations are
  not implemented. External capabilities remain those of the chosen backend and
  the existing canonical adapter, not native Claude capabilities.

## Evidence and acceptance status

Claude Code **2.1.310** was probed against a local emulator with a synthetic OAuth
credential and isolated client state. Only the base URL changed provider routing:
the main agent emitted `claude-sonnet-4-6`, the custom worker emitted exactly
`aiproxer-reviewer`, then the main model remained unchanged. All observed operations
were `POST /v1/messages?beta=true`. `x-claude-code-agent-id` appeared on the worker,
not the main request; no gateway request-class hints or mandatory auxiliary
operation were observed. CLI identity predicates matched `claude-cli/` and `cli`.
The CLI warned that the custom model was unrecognized, but emitted it unchanged.
No real credentials were captured by this probe.

Observed request header **names**, not values:
`accept`, `accept-encoding`, `anthropic-beta`,
`anthropic-dangerous-direct-browser-access`, `anthropic-version`, `authorization`,
`connection`, `content-length`, `content-type`, `host`, `user-agent`, `x-app`,
`x-claude-code-agent-id` (worker only), `x-claude-code-session-id`,
`x-stainless-arch`, `x-stainless-lang`, `x-stainless-os`,
`x-stainless-package-version`, `x-stainless-retry-count`, `x-stainless-runtime`,
`x-stainless-runtime-version`, `x-stainless-timeout`.

Focused tests exercise real standard HTTP/auth/self-defense/canonical/backend
composition, external streaming and a tool-result continuation with backend-owned
synthetic auth, credential/header canaries, native byte fidelity/errors, refusals,
and cancellation/retirement. This is local evidence, not provider certification.

**Live acceptance passed for the tested combination:** Claude Code **2.1.310**,
native **`claude-haiku-5-5`** using the existing `claude.ai` Pro login, and external
**`minimax/minimax-m2.7` through OpenRouter**. Only the CLI's base URL changed; no
new client auth variable was supplied. The worker called `Read`, received the
synthetic file marker as a tool result, and returned `EXTERNAL_TOOL_ROUNDTRIP_OK`
through the native main agent. Both real external requests returned HTTP 200.
A local forwarding recorder observed header names, backend-credential equality
and role order only, not credential values or private prompt contents. It
forwarded real provider responses, not emulated completions. Recorded history
retained `system, user, system`, followed by `assistant, tool` on continuation.
The external requests contained only the configured backend credential and no
original Anthropic/control headers. A separate real-CLI emulator roundtrip also
passed. The initial 429 and subsequent inline-system HTTP 400 are no longer the
observed blockers on this combination.

This is a bounded live acceptance result, not certification of every Claude
model, CLI version, provider, native operation or canonical capability. The mode
remains opt-in and experimental, with the limitations above.
