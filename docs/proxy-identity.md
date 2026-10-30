# Proxy identity (A-leg / B-leg)

AIProxer presents a controlled product identity on selected HTTP carriers. This is
**not** an arbitrary header tunnel: only User-Agent (B-leg), OpenRouter app
attribution headers (B-leg, OpenRouter only), and the Server response header
(A-leg) are in scope.

Config sample comments: [`config/config.yaml`](../config/config.yaml).
Types and validation: `internal/core/identity`. Wiring: `internal/standardplugins`,
`internal/plugins/backends/httpidentity`, `internal/stdhttp` (Server middleware).

## A-leg vs B-leg

| Leg | Direction | Carrier | Where applied |
| --- | --- | --- | --- |
| A-leg | Proxy -> client | HTTP `Server` response header | `stdhttp` stack-wide (success, stream/SSE, protocol errors, auth denials, recovery 500s, not-found) |
| B-leg | Proxy -> backend | HTTP `User-Agent` | Approved hosted connectors via `httpidentity` transport wrap |
| B-leg | Proxy -> OpenRouter | `HTTP-Referer`, `X-OpenRouter-Title` | OpenRouter backend only |

Client request `Server` is ignored for A-leg identity. Downstream `server` mode
`passthrough` is rejected at validation.

Proxy vs B2BUA nuance: failover and parallel races allocate distinct B-legs.
Each attempt uses that backend instance's **effective** upstream policy. A-leg
`Server` stays independent of which upstream won. Identity does not change the
"no transparent retry/failover after first client-visible output" rule.

## Modes

| Mode | Upstream User-Agent | OpenRouter app URL/title | Downstream Server |
| --- | --- | --- | --- |
| `proxy` (default when omitted) | Product name `aiproxer` | Product GitHub URL / product title | Product name `aiproxer` |
| `passthrough` | Call path: accepted client UA, else omit. Background/inventory (no call-path client context): product identity | Call path: accepted client extension values, else omit. Background/inventory: product defaults | **Forbidden** |
| `custom` | Exact `value` (bounded, control-char safe) | Exact `value` | Exact `value` |
| `drop` | Omit header | Omit that attribution header | Omit `Server` |

Inventory / non-Open call paths on approved connectors never forward client
passthrough identity; they resolve as product User-Agent (or drop/custom when
configured).

## Compatibility default change

Omitting `identity:` now defaults to **AIProxer product proxy identity**. That is an
intentional break from older OpenRouter client-first / empty-header behavior.
Operators who relied on forwarding client User-Agent or empty OpenRouter
attribution must set `mode: passthrough` or `mode: drop` explicitly.

## Precedence

1. Global `identity.upstream.*` defaults (`proxy` when mode empty).
2. Backend nested `plugins.backends[].config.identity.*` **pointer merge**:
   omit field = inherit global; explicit `drop` is not inherit.
3. OpenRouter legacy `static_referer` / `static_title` (per-backend only):
   - Apply when the matching **nested backend** `identity.openrouter.app_*`
     field is absent (factory sets `LegacyAppURL` / `LegacyAppTitle`; empty
     `FieldPolicy.Mode` is not a legacy signal — it means `proxy`).
   - **Override** global `identity.upstream.openrouter.*` for that carrier
     (legacy wins; startup does not fail).
   - Fail closed only when `static_*` is set together with the matching
     **nested backend** `identity.openrouter.app_*` field.
4. Flat `identity.app_url` / `identity.app_title` under backend config are rejected.

## Eligible and excluded connectors

**Eligible** for User-Agent policy wrap (and OpenRouter attribution where
applicable):

`openai-responses`, `openai-legacy`, `anthropic`, `gemini`, `bedrock`,
`openrouter`, `nvidia`, `huggingface`

**Excluded** (keep vendor/local agent identity; no AIProxer User-Agent wrap / no
OpenRouter attribution injection):

- OAuth / Codex: `openai-codex`, `openai-codex-app-server`
- ACP family: `acp`, `cursorcliacp`, `geminicliacp`, `agycliacp`
- OpenCode: `opencode-go`, `opencode-zen`
- Local: `ollama`, `ollama-cloud`, `llamacpp`, `lmstudio`, `vllm`, `local-stub`
- Custom compatible rows: `custom-openai-legacy-compatible`,
  `custom-openai-responses-compatible`, `custom-anthropic-compatible`

Allowlist is locked in `internal/archtest/identity_transport_boundaries_test.go`.

## OpenRouter attribution (current Go headers)

On OpenRouter B-leg attempts only:

- App URL -> `HTTP-Referer`
- App title -> `X-OpenRouter-Title` (modern header; not legacy `X-Title`)

See OpenRouter app attribution docs:
[https://openrouter.ai/docs/app-attribution](https://openrouter.ai/docs/app-attribution).

Non-OpenRouter approved backends never emit those attribution headers from AIProxer
identity policy.

A-leg capture of OpenRouter attribution headers (`HTTP-Referer`,
`X-OpenRouter-Title`, legacy `X-Title`) is currently performed only by the
OpenAI Responses and OpenAI Legacy frontends. Anthropic and Gemini frontends
capture User-Agent for B-leg passthrough but do not lift OpenRouter app
attribution headers into call extensions; routing Anthropic/Gemini A-leg traffic
to an OpenRouter backend with `app_url`/`app_title` `passthrough` therefore
omits those headers unless another path supplies extensions.

## Security and privacy

- Custom values are length-bounded and reject control characters (CR/LF).
- Passthrough User-Agent is revalidated (`AcceptClientUserAgent`); invalid or
  missing values on a call path omit the header rather than forwarding unsafe material.
- Do not log raw client User-Agent as a high-cardinality metric label.
- Identity carriers are not a path for credentials or session tokens.

## HTTP semantics (brief)

- [RFC 9110 User-Agent](https://www.rfc-editor.org/rfc/rfc9110.html#name-user-agent)
  describes the request product token sent toward origins (here: B-leg).
- [RFC 9110 Server](https://www.rfc-editor.org/rfc/rfc9110.html#name-server)
  describes the response product token toward clients (here: A-leg).

## Examples

Product defaults (explicit; same as omitting `identity:`):

```yaml
identity:
  upstream:
    user_agent: { mode: proxy }
    openrouter:
      app_url: { mode: proxy }
      app_title: { mode: proxy }
  downstream:
    server: { mode: proxy }
```

Custom A-leg Server, drop B-leg User-Agent globally, OpenRouter title custom:

```yaml
identity:
  upstream:
    user_agent: { mode: drop }
    openrouter:
      app_title:
        mode: custom
        value: "My Gateway"
  downstream:
    server:
      mode: custom
      value: "ExampleGateway/1"
```

Backend override (nested under that backend's `config`):

```yaml
plugins:
  backends:
    - id: openrouter
      enabled: true
      config:
        identity:
          user_agent:
            mode: custom
            value: "MyORClient/2"
          openrouter:
            app_url:
              mode: custom
              value: "https://example.com/my-proxy"
            app_title:
              mode: drop
```

Passthrough client User-Agent on approved connectors (frontend captures into
`aipapi.Invocation.ClientUserAgent`; executor attaches on Open):

```yaml
identity:
  upstream:
    user_agent: { mode: passthrough }
```

## Migration from static OpenRouter fields

1. Prefer nested backend `config.identity.openrouter.app_url` / `app_title`
   (`custom` or `proxy`), **or** set global `identity.upstream.openrouter.*`
   after removing legacy fields.
2. **Global migration:** remove backend `static_referer` / `static_title` first.
   While `static_*` remains, it overrides global OpenRouter policy for that
   carrier (no startup conflict).
3. **Nested backend migration:** set `config.identity.openrouter.app_*` and
   remove the matching `static_*`. Configuring both for one carrier fails closed
   at startup.
4. Expect modern `X-OpenRouter-Title` on the wire, not `X-Title`.
5. Re-check defaults: product proxy identity is now the omit-default when no
   `static_*` remains.

Committed reconstruction fixture:
[`testdata/identity/global_openrouter_override.yaml`](../testdata/identity/global_openrouter_override.yaml).

## Implementation notes (vs early #147 plan sketch)

These shapes are intentional and locked by tests:

- Canonical A-leg capture uses `aipapi.Invocation.ClientUserAgent` (`json:"-"`), not a nested `ClientIdentity` struct.
- Backend overrides nest OpenRouter fields under `config.identity.openrouter.*`. Flat `identity.app_url` / `identity.app_title` are rejected.
- A-leg `Server` is enforced by a thin commit-time `ResponseWriter` wrapper in `internal/stdhttp` that preserves `http.Flusher` and `Unwrap` for streaming / `ResponseController`.
- Passthrough User-Agent: call path with missing/invalid client UA **omits** the header; background/inventory (no call-path marker) uses **product** identity.
