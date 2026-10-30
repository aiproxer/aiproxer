# Design Document: claude-agent-acp-gateway

## Overview
`credential_source: gateway` joins the three existing sources with identical fail-closed composition. The connector advertises the adapter's gateway capability only for this source, performs the `authenticate` handshake with an operator-owned endpoint, and admits prompts only with fresh evidence that the gateway override is actually applied. Secrets are provisioned like existing credentials (opaque, peer-delivered), never literals.

## Configuration
```yaml
credential_source: gateway
gateway:
  base_url: https://claude-gw.example.com   # https; http only for loopback
  headers:                                  # names only; values are secrets
    - name: X-Route
```
Secrets: one opaque secret per header name, delivered through the existing instance-scoped secret mechanism (the current single-secret slots generalize to named refs; the per-source required-secret check stays). Validation: URL parse, https-or-loopback, no userinfo; ≤16 headers, names matching the host secret-reference grammar `^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`, values provided only via secret refs; literal values in YAML reject using the existing "no literal secrets" machinery. The certified runtime supplies its own bypass authorization, so configured `Authorization` headers reject; provision a supported custom header instead.

## Wire contract (verified 0.89.1 bytes)
- Client advertisement: `initialize.clientCapabilities.auth._meta.gateway = true`.
- Authenticate: `{methodId: "gateway", _meta: {gateway: {baseUrl, headers: {name: value}}}}`; the adapter validates baseUrl (absolute http(s)) and headers (object), stores the request as a provider override read by every later session, and reports auth status kind `gateway` with the base URL host as detail.
- Adapter environment: reset + redirect (`ANTHROPIC_BASE_URL`, header injection) with token/bypass placeholders so the CLI skips its normal login checks; connector does not set `--hide-claude-auth` for this source and sets no Claude credential variables.

## Handshake wiring
`AuthEnv.HandshakeProfile()` gains a gateway variant: `SkipAuthenticate` stays false only when the source requires the call, `ClientCapabilities` gains the gateway capability, and `AuthenticateParams` carries the payload built from config + secrets. Built after `mergeHandshakeProfile` consumes client extensions (client-supplied gateway keys are not accepted; no new client key). `PreparedSpawn` redaction extends to header values (formatting tests assert absence).

## Identity admission
The connector-owned ESM extension (`identity.mjs`) is updated to recognize gateway identity: the fresh session-bound observation must report the gateway provider (auth status kind `gateway`/provider override active) whose host matches the configured `base_url`, with no first-party credential signal. `ValidatePromptAdmission` accepts exactly that tuple; first-party tuples under the gateway source (and gateway tuples under other sources) refuse. Extension bytes change ⇒ new digest ⇒ new certification record bound in the same way as the current pinned digest; an uncertified combination rejects pre-exec.

## Billing honesty
The gateway is operator-owned: disclosure states usage flows through the gateway; no Claude plan or API billing claim; `plan_exhausted` classification is not produced for this source (a gateway's own quota errors surface through the generic RPC classification with bounded diagnostics). No pricing/rating logic is added (enterprise-owned).

## Lifecycle and errors
No new lifecycle owner: owned roots, allowlisted environment and process-tree behavior are unchanged; gateway errors map through existing classification. The adapter-side provider override lives and dies with the process, so kill/reap/close semantics need no changes; tests assert no gateway state outlives the process.

No-fallback is instance-local, as clarified in requirements: the host may select an explicitly configured alternate before output, including another credential source. Gateway-only traffic requires gateway-only sequential and parallel candidates. A logged-out startup probe may tolerate only a sole normal exit-1 cause, never an independent joined wait or cleanup failure.

## Certification
A project-owned stub gateway fixture (test-only) exercises: capability advertisement, successful `authenticate`, one streamed gateway-routed turn, admission pass/refuse rows (wrong host, first-party tuple, stale evidence), and leakage scans over argv/logs/evidence. Certification binds the new extension digest; enabling constants update only for the observed bytes.

## Testing strategy
Red-first per task: config/validation tables; gate composition; handshake payload tests over a fake adapter capturing `authenticate`; redaction tests; admission acceptance/refusal tables with synthetic extension responses; stub-gateway integration turn; docs/example checks; regression suites for the three existing sources and other connectors.

## Risks and mitigations
- Header leakage → secret-ref-only values, redaction tests at every formatting surface.
- Silent fallback to first-party → admission refuses without a fresh gateway observation; `--hide-claude-auth` never combined.
- Endpoint policy bypass → https/loopback validation pre-exec; userinfo rejected.
- Extension drift → digest bound to certification; mismatch rejects.
