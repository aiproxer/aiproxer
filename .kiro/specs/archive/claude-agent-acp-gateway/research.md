# Research: claude-agent-acp-gateway

## Adapter evidence (certified 0.89.1 bytes, offline cache)
- Gateway is a client-capability-gated auth method: offered only when `clientCapabilities.auth._meta.gateway === true`; methods `gateway` and `gateway-bedrock`.
- `authenticate` handling: for `methodId` `gateway`/`gateway-bedrock`, payload `_meta.gateway` may be omitted (historical no-op) or must be an object with a valid absolute http(s) `baseUrl` and optional `headers` object. On success it stores the request as a provider override read by every later session; auth status becomes kind `gateway` with the base URL host as detail.
- Provider override environment: resets provider vars and sets `ANTHROPIC_BASE_URL` + injected headers, with token/bypass placeholders so the CLI skips its normal login/credential checks. Comment warns that an unchecked payload can silently turn the `--hide-claude-auth` guard off — hence our validation happens before any process exec.
- Session-scoped `providers/set` uses the same provider-config shape but is out of scope for V1.

## Pre-implementation connector baseline (historical)
- `AuthEnv` supports exactly three sources with a single required secret each; `handshake.go` already carries `AuthenticateParams`/`ClientCapabilities` fields to extend.
- Effective identity admission (`effectiveidentity.go`) currently requires `provider == "firstParty"` and source matching; gateway support requires new extension bytes and a new accepted tuple — a recertification trigger, matching the spec's Boundary note.
- `--hide-claude-auth` is only appended for `api_key`; gateway must not add it.

## Pre-implementation questions (resolved by the certification below)
- Exact CLI-facing behavior when both a gateway override and an inherited first-party credential exist (adapter resets provider vars; the stub-gateway fixture must prove no first-party credential is sent upstream).
- Whether the adapter reports a distinguishable provider string for gateway in the fresh session observation the identity extension reads (tests currently expect `firstParty`); the extension update pins the observed values before admission tables are finalized.

## Sequencing
- Split from #1185; sequence after #1193 MCP and before/parallel-to #1194 v2 (independent). macOS certification (#1192) remains environment-blocked.
- No billing/pricing changes: gateway usage accounting stays adapter-reported; enterprise keeps money logic.

## Real-runtime certification (2026-10-16, task 4.1)

Offline pins (operator-installed bytes): adapter 0.89.1, Agent SDK 0.3.293,
bundled CLI 2.1.293, Node 22.23.3 on Linux/amd64 glibc. Changed project sidecar
(bootstrap.mjs, identity.mjs, models.mjs) recomputed after the review repairs:
`5a6c3c5c…f702`, `06081860…afc3`, `b7e0d5d3…120d`; sidecar closure
`45a7abfd3c48095a7c82d5ea4b6dc9f96b8322fe147249dae80526f923ff2093`; combined
tree+sidecar `993f346e7b4aa9a1901d5bffce360f29277e9460a898dc40c4924159ee272371`.
The runtime-tree digest is unchanged. `extension.go` and
`docs/claude-agent-acp-backend.md` now pin the observed profile only.

Stub gateway (project-owned test harness, loopback `http://127.0.0.1`, host home
excluded) demonstrated, against the exact shipped bytes:

- initialize carries `clientCapabilities.auth._meta.gateway: true`; the adapter
  offers the `gateway`/`gateway-bedrock` methods.
- `authenticate {methodId: "gateway", _meta: {gateway: {baseUrl, headers}}}`
  succeeds (result `{}`); `_auth/status_update` becomes `{"kind":"gateway",
  "label":"Custom model gateway", detail:<host>}`.
- With the connector's session meta minus `allowedProviders`, `session/new`
  succeeds and `_golip/effective_identity_v1` issues the one-use tuple with
  `credentialSource: "gateway"`; a replayed tuple refuses before input.
- `session/prompt` streamed a full turn through the stub gateway; the gateway
  received the configured custom header and no first-party credential values
  outside the sidecar's deliberate bypass placeholder.
- Notebook the CLI-side spawn gate: the configured (pre-auth) CLI `auth status
  --json` reports `loggedIn:false, authMethod:"none", apiProvider:"firstParty"`
  and exits 1; the Go identity gate treats exactly this logged-out shape as the
  source's negative precondition and the session-bound observation above as the
  only per-turn authorization.
- Review-repair reruns (2026-10-17) re-establish the same rows with the hardened
  sidecar: the authenticate latch claims synchronously (concurrent attempts
  cannot both pass; failures burn the one-time right), `methodId` must be
  exactly `gateway`, and admission compares the stored override against the
  captured payload INCLUDING every header name/value (changed headers refuse).
  The committed `connectors/claudeagentacp/certgateway/` harness reproduces the
  full row set in-repository via
  `LIP_GATEWAY_CERT_RUNTIME=<pinned install> go -C connectors/claudeagentacp test ./certgateway/...`
  when that env names the certified install (skips otherwise).
- Leakage scan over the certification run's artifact tree and captured driver/
  sidecar output: no header value outside the stub's own request log. Unreadable
  files and walk errors fail the scan. Go-side redaction tests cover AuthEnv
  formatting, prepared argv/env and errors.
- Review-repair policy deltas now pinned: configured `Authorization` headers
  reject at configuration (the runtime's bypass token replaces that name on
  the wire); gateway responses that match the Anthropic plan taxonomy map to
  generic upstream failures, never `plan_exhausted`; every `authenticate` RPC
  failure names gateway remediation; the startup and resolved-profile
  disclosures name the validated gateway host (`gateway/<host>`).
- Remediation certification uses the production Go `AdapterResolver.Resolve`
  and `ResolveIdentityExtension` gates before running the driver: the complete
  dependency tree, Node binary, adapter/CLI versions and staged shipped sidecar
  must match the pins. A same-version but different-tree cached install was
  rejected; the original certified offline install passed all eight driver rows.
  This exercises Go startup admission, not the complete Go service streaming path.
  The driver executes that verified staged sidecar, using the admitted absolute
  Node executable; its child reuses `process.execPath`, without another PATH lookup.
  The real CLI's logged-out status also passes through `IdentityGate.Check` and
  the production process-tree runner. Only a sole normal exit-1 cause is tolerated;
  independent joined wait/cleanup failures refuse, even with valid logged-out JSON.
- Eight required driver rows: `initialize_advertised_gateway`,
  `authenticate_accepted_exact_payload`, `session_created_gateway_session`,
  `admission_ticket_gateway_issued`, `gateway_routed_streamed_turn`,
  `streamed_text_delta_with_stub_content`, `replayed_ticket_refused`, and
  `post_refusal_observation_refused`. The harness verifies exact provisioned
  `X-Route` and bypass `Authorization` on every model POST, accepts the CLI's
  `?beta=true` query string, rejects first-party `X-Api-Key` on any request,
  and checks streamed `gateway-turn-ok` content rather than turn completion alone.
  The CLI may issue a second model request for session-title generation; this
  request uses the same gateway credentials, while `HEAD /api/hello` is uncredentialed.
  Certification explicitly rejects `X-Route` and `Authorization` on that preflight
  (including empty values) and rejects unexpected auxiliary method/path pairs.
- Routing is covered at its owning seams: the connector's
  `TestClaudeSpec_AuthenticationRemediationAndOutputBoundary` verifies the real
  gateway `-32000` classifier and output-phase eligibility. The host's
  `TestPreOutputAuthFailure_OnlyExplicitRouteCanReachAlternate` verifies that the
  resulting canonical error can invoke an alternate only when the route explicitly
  supplies one. Connector tests do not import host-internal packages. The
  no-fallback guarantee is per instance, not route-wide; requirement 4.2 keeps
  generic pre-output failover.
- Pin behavior observed during research (kept as constraints): sidecar
  `authenticate` is admitted exactly once, only pre-session, only with the
  connector-owned URL; logout/mutation stays refused; the session-level account
  shape for a gateway session is the first-party placeholder
  (`tokenSource: "ANTHROPIC_AUTH_TOKEN"`), so classification alone never
  authorizes gateway prompts; the CLI refuses a custom endpoint whose
  `allowedProviders` pin is not vouched, so the gateway session meta omits
  `allowedProviders` while keeping every other managed lock.
- Windows/macOS/WSL gateway certification and `gateway-bedrock` remain
  uncertified and rejected per the spec's deferred list.

Re-run harness (external scratch, volatile): an Anthropic-protocol stub
gateway on loopback plus a stdio ACP driver driving the shipped sidecar with
the pinned adapter root, loopback port and gateway base URL via the
documented child-environment name.
