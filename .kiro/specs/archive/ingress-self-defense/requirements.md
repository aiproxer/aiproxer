# Requirements Document

## Introduction

Ingress Self-Defense adds a minimal, default-on standard-HTTP data-plane defense against commodity Internet scanning and repeated unauthenticated access attempts. The feature is intentionally narrower than a WAF or threat-intelligence system: it rejects a small set of request targets that cannot plausibly belong to the standard AIProxer data plane, tracks bounded process-local hostile-source behavior, and applies temporary exponential quarantine without locking a legitimate authenticated user out merely because another actor shares the same public IP.

The v1 contract reuses the existing GeoIP ingress client-address trust semantics from #387, the existing transport-auth provider chain, immutable generation reload, process-service ownership, and bounded metrics infrastructure. It does not add external reputation feeds, body inspection, persistent/distributed ban state, ASN/subnet escalation, or a generic rule engine.

## Boundary Context

- **In scope**: standard HTTP data-plane impossible-path rejection; repeated unauthenticated-401 tracking; temporary exponential per-address quarantine; authentication-aware shared-address safety; bounded in-memory state; adaptive-exemption CIDRs; runtime-reloadable request policy; minimal bounded metrics.
- **Out of scope**: Spamhaus or other threat feeds; CrowdSec/Nuclei runtime signatures; GeoIP/ASN reputation scoring; generic WAF/OWASP CRS; prompt/body/query-content inspection; persistent or fleet-wide quarantine; automatic `/24`, `/64`, ASN, or country bans; volumetric DDoS/SYN defense; CAPTCHA/challenge UX; ML/LLM classification.
- **Adjacent expectations**: existing `access.geoip` remains authoritative for fixed country/IP/CIDR allow/deny policy and client-address trust configuration; existing authentication remains the authority for principal acceptance; server timeouts, body limits, and decode admission remain unchanged; the separate management/recovery listener remains outside the data-plane gate.
- **Boundary ownership**: typed base configuration and reload classification in existing core config; provider/protocol-neutral adaptive policy and bounded process-local state in `internal/core/ingressdefense`; request classification and response behavior in `internal/stdhttp`; process/generation wiring in `internal/infra/runtimebundle`; no new public SDK contract and no generic feature-plugin plane.
- **Optional hexagonal lens**: immutable policy and bounded adaptive state = core domain/process state; HTTP gate/auth observation = driving adapters; runtimebundle = composition root; metrics = driven observability adapter.
- **Revalidation triggers**: changes to stdhttp middleware order, transport-auth provider-chain semantics, GeoIP client-IP resolver/trusted-proxy semantics, ProcessServices ownership, config reload classification, or management-listener separation. Routing, model execution, canonical streaming, B2BUA, and provider adapters are not intentionally changed.

## Requirements

### Requirement 1: Default Posture and Scope
**Objective:** As an operator, I want basic ingress self-defense enabled safely by default, so that a normally exposed proxy rejects common hostile noise without extra setup.

#### Acceptance Criteria
1. When the standard distribution starts with `access.self_defense` omitted, the LLM Interactive Proxy shall use the documented v1 self-defense defaults with request enforcement enabled.
2. If the operator explicitly disables self-defense, then the data-plane request generation shall omit self-defense path matching, adaptive-state lookup, and auth-outcome recording for that feature.
3. While self-defense is enabled, the feature shall apply only to the standard HTTP data-plane handler graph and shall not wrap the separate process-owned management/recovery listener.
4. The self-defense v1 request path shall perform no DNS lookup, remote reputation query, feed download, database I/O, or other request-driven network I/O.
5. The feature shall not change canonical LLM request/event semantics, routing, retry/failover, B2BUA continuity, model selection, or provider behavior.

### Requirement 2: Canonical Client Address Trust
**Objective:** As a security administrator, I want self-defense to use the same trusted client-address semantics as existing GeoIP ingress control, so that attackers cannot choose their tracking identity with spoofed forwarding headers.

#### Acceptance Criteria
1. When self-defense resolves a source address, the LLM Interactive Proxy shall use the same direct-peer and trusted `X-Forwarded-For` / `Forwarded` resolution semantics and parser bounds already used by GeoIP ingress control.
2. If the immediate direct peer is not trusted for forwarding, then self-defense shall ignore forwarding headers and use the direct peer address.
3. When a configured trusted forwarding chain is malformed, ambiguous, oversized, overlong, or contains no untrusted client hop, the request shall fail closed before frontend decode or model work.
4. The LLM Interactive Proxy shall normalize IPv4-mapped IPv6 addresses consistently with existing GeoIP IP/CIDR matching before adaptive-state keying or exemption matching.
5. Self-defense address resolution shall not change transport-auth attribution semantics or cause authentication to trust forwarding headers that it does not already trust.

### Requirement 3: Conservative Impossible-Path Rejection
**Objective:** As an operator, I want obviously unrelated web exploit probes rejected before expensive proxy work, so that commodity scanners consume minimal resources and create little noise.

#### Acceptance Criteria
1. When a request target matches the built-in v1 impossible-path set, the LLM Interactive Proxy shall return a generic `404 Not Found` without running transport authentication, frontend decode, routing, persistence, or model execution.
2. The built-in v1 matcher shall inspect only normalized HTTP request-target path structure needed for its fixed path/prefix/traversal rules and shall not inspect LLM prompt bodies, canonical messages/items, tool arguments, SQL/code text, or arbitrary query values.
3. When impossible-path matching is disabled while adaptive auth-failure defense remains enabled, requests shall bypass only the impossible-path matcher and shall retain the adaptive authentication behavior.
4. When a non-exempt source hits an impossible path, the LLM Interactive Proxy shall record one strong hostile offense for that exact source address and shall start or escalate temporary quarantine according to the configured backoff policy.
5. When an adaptive-exempt source hits an impossible path, the request shall still receive the generic `404`, but the hit shall not create or escalate adaptive quarantine state.
6. The built-in impossible-path set shall have regression evidence that it does not overlap standard distribution frontend routes or the separate management/recovery surface.

### Requirement 4: Authentication-Failure Evidence and Quarantine
**Objective:** As an operator, I want repeated unauthenticated failures to cause temporary backoff, so that brute-force and blind probing traffic is throttled without permanent bans.

#### Acceptance Criteria
1. When the complete transport-auth provider chain terminates a request with `401 Unauthorized` before any accepted principal has been established, the LLM Interactive Proxy shall count one authentication-failure offense for the resolved source address.
2. The LLM Interactive Proxy shall not count `403` authorization/entitlement denials, `5xx` provider/system failures, provider errors, or a request that already established an accepted principal as authentication-failure evidence for IP quarantine.
3. When a source reaches the configured authentication-failure threshold within the configured failure window that begins with its first counted failure, the LLM Interactive Proxy shall start a temporary quarantine.
4. When the same source produces another qualifying offense after a prior quarantine cycle, the LLM Interactive Proxy shall increase the quarantine duration exponentially from the configured initial duration up to the configured maximum duration.
5. When the active quarantine deadline passes, the LLM Interactive Proxy shall stop treating the source as quarantined without operator action.
6. When hostile state has been inactive for the configured state TTL, the LLM Interactive Proxy shall expire that source state and its accumulated offense level.

### Requirement 5: Shared-Address Safety and Authentication-Aware Bypass
**Objective:** As a legitimate user behind shared NAT, VPN, carrier, or corporate proxy infrastructure, I want valid authentication to remain usable even if another actor sharing my public IP has triggered adaptive quarantine.

#### Acceptance Criteria
1. While an address is dynamically quarantined, the LLM Interactive Proxy shall reject a request before authentication only when it can positively determine that the presented request cannot authenticate through the configured provider chain and does not carry configured credential material.
2. If any active auth provider is credential-free, can authenticate the request as presented, or cannot safely answer the early credential-presence question, then the LLM Interactive Proxy shall allow the quarantined request to reach the normal transport-auth chain rather than deny it solely by source IP.
3. When a quarantined request completes the full auth provider chain successfully, the LLM Interactive Proxy shall allow the request to continue and shall clear the adaptive hostile state for that exact source address.
4. When a quarantined request reaches authentication and is rejected with a qualifying unauthenticated `401`, the LLM Interactive Proxy shall stop the request at authentication and shall update the adaptive hostile state without reaching frontend decode or model execution.
5. A prior successful authentication shall not permanently trust an address and shall not exempt later impossible-path requests from deterministic rejection.
6. The LLM Interactive Proxy shall never automatically widen one source-address offense into a `/24`, `/64`, ASN, country, or other aggregate network quarantine.

### Requirement 6: Bounded Ephemeral State
**Objective:** As an operator, I want adaptive defense state to remain cheaper than the traffic it rejects, so that hostile source churn cannot become a memory or goroutine denial of service.

#### Acceptance Criteria
1. The v1 adaptive state shall be process-local and in-memory only, with no database persistence, distributed coordination, per-address goroutine, or per-address timer.
2. The adaptive state shall enforce a configured maximum number of tracked source entries and a configured inactivity TTL.
3. When new hostile addresses arrive at the configured capacity, the state implementation shall remain bounded through deterministic bounded eviction or equivalent replacement rather than grow without limit or reject unrelated legitimate traffic globally.
4. Each source entry shall retain only normalized address identity, bounded counters/penalty state, and bounded timestamps needed for the v1 algorithm; it shall not retain raw credentials, principal identifiers, request bodies, prompt text, complete paths, headers, User-Agent values, or arbitrary attacker-controlled strings.
5. The state implementation shall remain safe under concurrent requests from the same and different source addresses and shall not serialize unrelated traffic behind long-held request-path locks.

### Requirement 7: Configuration, Validation, and Reload
**Objective:** As an operator, I want a small validated configuration surface that can adjust request policy at runtime without making process-owned state ownership ambiguous.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall provide finite validated v1 defaults for enabled state, impossible-path matching, authentication-failure threshold/window, initial/max quarantine, state TTL, maximum tracked entries, and adaptive-exemption CIDRs.
2. `check-config` shall validate self-defense syntax, durations, entry bounds, CIDRs, and cross-field constraints without constructing process state, binding a listener, or performing network I/O.
3. Changes to enabled state, impossible-path enablement, authentication-failure threshold/window, initial/max quarantine, and adaptive-exemption CIDRs shall be generation-reloadable and shall affect only requests admitted to the new generation.
4. Changes to process-state capacity or state TTL shall be restart-required in v1 because those values define process-owned adaptive-state resources shared across generations.
5. When a candidate self-defense configuration is invalid or mixes reloadable changes with restart-required process-resource changes, candidate publication shall fail atomically and the last-good generation/state shall remain active.
6. Adaptive state shall survive successful policy-only generation reloads and shall be disposed only with its owning ProcessServices lifecycle.

### Requirement 8: Interaction With Existing Fixed Access Policy
**Objective:** As an operator, I want self-defense to compose predictably with existing GeoIP/CIDR access control, so that v1 does not introduce a second conflicting hard-block policy.

#### Acceptance Criteria
1. Existing `access.geoip` country/IP/CIDR allow/deny policy shall remain the authoritative fixed network-access policy and shall be evaluated independently according to its existing semantics.
2. Self-defense v1 shall not introduce a second general-purpose static IP/CIDR allow/deny engine.
3. Adaptive-exemption CIDRs shall affect only adaptive state/quarantine behavior; they shall not override an existing GeoIP hard denial or make an impossible-path request valid.
4. Where both GeoIP and self-defense are enabled, GeoIP hard denial shall occur before self-defense behavior for the same request.
5. Self-defense shall reuse the existing GeoIP client-IP source/trusted-proxy configuration in v1 rather than create a second independently configurable forwarding-trust boundary.

### Requirement 9: Response Privacy and Bounded Observability
**Objective:** As an operator, I want enough telemetry to verify that self-defense works without exposing sensitive or attacker-controlled data or turning abuse into logging/metrics amplification.

#### Acceptance Criteria
1. When an impossible-path request is denied, the response shall be generic and shall not reveal the matched rule, source address, offense state, or security feature internals.
2. When an active adaptive quarantine is able to reject before authentication, the response shall use generic `429 Too Many Requests` semantics without disclosing the exact reputation score, offense level, or matched evidence.
3. The implementation shall expose bounded metrics equivalent to denial count by finite reason, quarantine-transition count by finite reason, and current adaptive-state entry count.
4. Metrics shall not label by source IP, CIDR text, path, query, header, User-Agent, credential, principal, country, or arbitrary attacker-controlled value.
5. Per-request hostile-event logging shall be disabled by default; operational logs, if any, shall be bounded to configuration/lifecycle/state anomalies and shall not contain raw secret or request-content material.
6. Self-defense diagnostics shall not create a credential-validity oracle beyond the existing authentication response semantics.

### Requirement 10: Minimal V1 Compatibility Contract
**Objective:** As a maintainer, I want the first implementation to solve the common cases without creating a framework that must be maintained before evidence shows it is needed.

#### Acceptance Criteria
1. The v1 implementation shall not add external reputation-feed clients, updater frameworks, generic scoring DSLs, WAF engines, body scanners, persistence schemas, or distributed ban-state contracts.
2. The v1 implementation shall not require a new public `pkg/lipapi`, `pkg/lipsdk`, or `pkg/lipruntime` contract solely for self-defense.
3. When LLM prompt bodies contain SQL injection strings, XSS payloads, shell commands, malware-analysis text, traversal strings, or exploit examples on otherwise legitimate proxy routes, self-defense shall not reject the request solely because of that body content.
4. Existing standard server timeouts, maximum request-body enforcement, decode admission, transport-auth semantics, and management-recovery posture shall remain effective and shall not be reimplemented by self-defense.
5. The implementation shall provide focused regression evidence for shared-NAT safety, forwarding-header spoof resistance, state bounds, no subnet escalation, route non-collision, and management-surface isolation before the feature is considered implementation-complete.