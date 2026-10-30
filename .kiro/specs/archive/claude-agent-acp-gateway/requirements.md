# Requirements Document

## Project Description (Input)
Support the `claudeagentacp` adapter's `gateway` authentication method as a fourth explicit credential source (follow-up to issue #705; split from #1185). Today the connector supports exactly `api_key`, `subscription_token` and `host_login`, never falls back between sources, and admits prompts only with a fresh first-party identity observation. The adapter (verified 0.89.1 bytes) offers a `gateway` method to clients advertising `clientCapabilities.auth._meta.gateway`; `authenticate` stores `_meta.gateway {baseUrl, headers}` as a provider override read by every later session, redirecting API traffic to the operator's custom Anthropic-protocol gateway with the CLI's normal login checks skipped by design.

- **Who has the problem:** operators who front Anthropic-protocol inference with their own authenticated gateway and want the Claude agent runtime to route through it, without AIProxer silently falling back to a Claude login or API key.
- **Current situation:** the source does not exist; the effective-identity admission expects a first-party provider observation; gateway headers are credentials that must never reach argv, logs or diagnostics.
- **What should change:** an explicit `credential_source: gateway` with operator-provided base URL and secret-provisioned headers, no fallback, gateway-specific identity admission with recertified sidecar evidence, endpoint policy (https, or loopback http), certification-gated enablement, and honest billing disclosure (usage flows through the operator's gateway; no Claude plan or API billing claim).

## V1 Slice
- **Delivers:** `credential_source: gateway` on `claude-acp` instances. Config carries the non-secret `gateway.base_url` and `gateway.headers` names whose values are provisioned as secrets; the connector advertises the gateway capability only for this source, calls `authenticate` with `methodId: "gateway"` and the operator payload, and admits prompts only with a fresh observation that the applied provider is the configured gateway and no first-party credential is active. No fallback to any other source; no login UI; failures name the operator remediation.
- **First consumer:** a single-user loopback deployment with an operator-owned gateway endpoint.
- **Safe degradation:** picker rejects literal secrets in YAML; missing secret refs reject pre-exec; endpoint policy violations reject; unknown evidence refuses prompts without admitting input; the adapter's `gateway-bedrock` variant and `providers/set` stay out of scope.

## Deferred
- `gateway-bedrock` protocol variant and non-Anthropic gateway protocols.
- Session-scoped `providers/set` overrides and any dynamic provider switching.
- OAuth or interactive authentication flows for gateways (static headers only in V1).
- Gateway usage-accounting integration beyond authoritative adapter reports; no pricing/rating logic in this slice.
- Windows/macOS certification of the gateway-enabled profile.

## Boundary Context
- **In scope**: the fourth credential source (config, secret refs, capability advertisement, `authenticate` call, environment construction), gateway identity admission with sidecar recertification, endpoint/header policy, error remediation, disclosure, docs and a disabled example.
- **Out of scope**: changing existing three sources, fallback behavior, provider mutation after startup, header value generation, gateway-side implementation, changes to other connectors.
- **Adjacent expectations:** the operator owns and operates the gateway, its endpoint and its credentials; AIProxer never generates or refreshes them.
- **Trust model**: unchanged local single-user trust; gateway headers are operator secrets handled like existing credentials.
- **Boundary ownership**: backend plugin (`connectors/claudeagentacp`) plus the connector-owned ESM identity extension (new bytes ⇒ recertification) and additive `connector-support/acp` seams if needed.
- **No-fallback boundary (requirements 1.1, 2.4 and 4.2):** credential-source immutability belongs to each connector instance, not the host's route. Pre-output `-32000` retains host failover eligibility, so an explicitly configured second candidate can be a separate first-party instance. Operators requiring gateway-only traffic must keep sequential and parallel route candidates gateway-only. This slice does not add route-wide credential affinity or alter generic failover policy.
- **Revalidation triggers**: adapter gateway/auth method changes; SDK account-info shape; identity extension changes; secret provisioning changes.

## Requirements

### Requirement 1: Explicit fourth source with fail-closed configuration
**Objective:** As an operator, I want an explicit gateway credential source, so that routing through my gateway is a deliberate, validated choice.

#### Acceptance Criteria
1. The connector shall accept `credential_source: gateway` alongside the existing three sources and shall never fall back from it, or to it, on any failure.
2. Gateway configuration shall require a non-secret `gateway.base_url` (https, or http only for loopback hosts; no userinfo) and a bounded list of header names whose values are provisioned as secrets; literal header values in configuration shall reject.
3. The connector shall reject the instance pre-exec when a referenced header secret is missing, when the URL violates policy, or when the header list is malformed; no partial configuration may reach a session.
4. Disabled, removed or drifted gateway instances shall not resolve secrets, launch processes, or receive sessions.

### Requirement 2: Wire-correct, no-fallback authentication
**Objective:** As an operator, I want the gateway handshake performed exactly as the adapter defines it, so that no Claude credential silently takes over.

#### Acceptance Criteria
1. Only for `credential_source: gateway`, the connector shall advertise `clientCapabilities.auth._meta.gateway` and call `authenticate` with `methodId: "gateway"` and `_meta.gateway {baseUrl, headers}` exactly as the adapter validates it; other sources shall advertise neither.
2. The connector shall never pass `--hide-claude-auth`, set Claude credential variables, or otherwise combine the gateway source with any other credential source.
3. Header values shall never appear in argv, logs, diagnostics, evidence or error text; attempts to format prepared configuration shall remain redacted.
4. If `authenticate` is rejected or the override cannot be established, the connector shall fail the instance with a safe diagnostic naming the gateway remediation, never retrying with another source.

### Requirement 3: Gateway identity admission and honest billing posture
**Objective:** As an operator, I want prompt admission to verify the gateway is actually in effect, so that billing and routing are predictable.

#### Acceptance Criteria
1. Before each user prompt is admitted, the connector shall obtain a fresh, session-query-bound observation that the applied provider is the configured gateway (adapter-reported gateway identity matching the configured base URL host) and that no first-party Claude credential is active; unknown, stale or conflicting evidence shall refuse without admitting input.
2. The connector-owned identity sidecar shall be updated, certified and bound for the gateway source; its bytes change therefore require a new certification record.
3. Disclosure and diagnostics shall state that usage flows through the operator's gateway and make no Claude plan or Anthropic API billing claim; plan-exhaustion classification shall not be attributed to Anthropic plans for this source.

### Requirement 4: Isolation, endpoint policy and lifecycle
**Objective:** As an operator, I want gateway deployments isolated like every other source, so that no new leak or orphan path exists.

#### Acceptance Criteria
1. Gateway instances shall keep the existing owned-root, environment allowlist, administrator-policy and process-tree behavior; the adapter's provider-override environment construction remains adapter-owned and unmodified by the connector.
2. Server-initiated gateway errors shall map through existing classification with bounded redacted diagnostics; pre-output eligibility and post-output terminality shall be unchanged.
3. Cancel, idle reap, reconfigure and shutdown shall terminate owned processes exactly as today; no gateway-specific lifecycle owner is introduced.

### Requirement 5: Certification, disclosure and regression safety
**Objective:** As the maintainer, I want evidence-gated enablement, so the source ships only with demonstrated behavior.

#### Acceptance Criteria
1. Enabling `credential_source: gateway` shall require a recorded certification against the exact adapter/runtime/extension profile using a project-owned stub gateway fixture demonstrating: capability advertisement, successful `authenticate`, gateway-routed streaming turn, identity admission pass and refusal rows, and secret non-leakage in logs/argv/evidence.
2. Startup and resolved-profile disclosure shall name the gateway posture and host (never header values) alongside the existing credential/identity fields.
3. Operator documentation shall cover configuration, secret provisioning, endpoint policy, no-fallback and no-billing-claim statements; a disabled example shall demonstrate the source.
4. The existing three sources, deny/allow postures and other connectors shall remain covered by unchanged tests.

## Out-of-Scope Assumptions Worth Naming
- The adapter's `gateway` method requires no AIProxer-side protocol beyond the documented `authenticate` payload; the fixture verifies this before wiring.
- Gateway endpoints are operator-owned; AIProxer tests never call real third-party gateways.
