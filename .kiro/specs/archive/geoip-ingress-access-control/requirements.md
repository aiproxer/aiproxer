# Requirements Document

## Introduction

Go-LIP needs an optional transport-level GeoIP access-control layer that can reject obviously unwanted network traffic before the request reaches expensive or noisy parts of the proxy. The motivating deployment has a known geographic user footprint and wants country-level blocking with precise IP/CIDR exceptions, while preserving runtime policy reload and automatic GeoIP database maintenance.

The feature is an **HTTP ingress security control**, not an LLM routing/admission policy. Its value depends on running before authentication, frontend parsing, routing, persistence, and model work. It is defense in depth: it reduces attack surface and resource consumption, but does not replace authentication, rate limiting, network firewalls, WAFs, or identity policy.

Brownfield gap analysis was performed against `main` commit `ca43dde919f4d53716a98bf53ffb57bd61560607`. This revision incorporates the resulting management-plane, generation-pinning, database-provisioning, `check-config`, and cache-mechanism clarifications.

## Boundary Context

### In scope

- Optional country-based allow/deny policy for the canonical standard HTTP data plane.
- Exact Apache-style `deny_allow` and `allow_deny` rule-class precedence.
- IPv4 and IPv6 exact-address/CIDR allow/deny overrides.
- Direct-peer client-IP resolution by default.
- Explicit trusted-reverse-proxy resolution for `Forwarded` or `X-Forwarded-For`.
- Local Country MMDB lookup on the request path.
- Process-owned last-known-good MMDB lifecycle and optional managed DB-IP Lite updates.
- Runtime reload of pure enforcement policy through the existing immutable generation mechanism.
- Fail-closed behavior for unsafe address resolution and lookup failures.
- Bounded-cardinality observability, hard parser/resource bounds, race/concurrency coverage, and benchmarks.
- Deterministic offline/static configuration validation integrated with existing `check-config` behavior.

### Out of scope

- City, subdivision, ASN, ISP, organization, anonymity/VPN, or threat-intelligence policy.
- Per-request remote GeoIP web-service calls.
- OS firewall, nftables/iptables, cloud security-group, CDN, or WAF rule management.
- PROXY protocol support.
- CDN/vendor-specific client-IP headers.
- DNS/hostname rules.
- Retroactive termination of already accepted long-lived streams after policy reload.
- Automatic application of data-plane GeoIP policy to the separate process-owned reload management listener.
- A distributed database-update coordinator for multi-replica fleets.
- A mandatory per-IP lookup cache.
- Changing existing authentication peer-attribution semantics.

### Boundary ownership

- **Pure policy:** focused core package; owns rule normalization and decisions, imports no HTTP or GeoIP database implementation.
- **HTTP ingress adapter:** `stdhttp`; owns request peer/header parsing, generic 403 response, and middleware placement.
- **GeoIP database adapter:** infrastructure package; owns MMDB reader/open/verify/swap, on-disk LKG, and managed database update transport.
- **Lifecycle/composition:** process service owns database/updater; immutable request generation owns compiled policy and HTTP wrapper presence.
- **Configuration:** core config model with explicit reload classification.
- **Observability:** existing process metrics infrastructure with bounded labels only.

## Glossary

- **Direct peer:** IP address extracted from the accepted connection's `http.Request.RemoteAddr`.
- **Resolved client IP:** address against which CIDR and country policy is evaluated after the configured trust algorithm.
- **Trusted proxy:** proxy hop whose address is explicitly present in operator-configured trusted-proxy CIDRs.
- **Country lookup:** local mapping from a normalized IP address to `country.iso_code` from the active MMDB.
- **LKG:** last-known-good structurally validated MMDB version retained for continued service when an update fails.
- **Policy generation:** immutable request-plane generation compiled by the existing runtime reload architecture.
- **Managed database:** MMDB maintained by the proxy through the documented DB-IP Lite static download (no account/key/credentials).
- **Local database:** operator-managed MMDB file read by the proxy without automatic download ownership.
- **Provisioned database service:** process-owned country lookup constructed at process startup even if request enforcement is currently disabled.

## Requirement 1: Early Ingress Enforcement and Disabled Fast Path

**Objective:** As an operator, I want blocked traffic rejected before expensive proxy processing and disabled GeoIP to impose effectively no request-path cost.

### Acceptance Criteria

1.1. WHEN GeoIP enforcement is enabled for a request-plane generation, THE standard HTTP data plane SHALL evaluate GeoIP admission before general OpenTelemetry HTTP tracing, general HTTP Prometheus instrumentation, request/trace-ID middleware, normal access logging, transport authentication, frontend decoding, routing, persistence access, billing, or model execution.

1.2. THE GeoIP gate SHALL remain inside the standard outer panic-recovery boundary and inside global downstream/security response middleware so a denial still receives normal server/security response headers.

1.3. WHEN GeoIP enforcement is disabled, THE composed generation SHALL omit the GeoIP request wrapper rather than invoking a disabled policy on every request.

1.4. WHEN enforcement is disabled, THE request path SHALL perform no GeoIP client-address resolution beyond work already performed by unrelated middleware, no country lookup, and no GeoIP policy evaluation.

1.5. THE feature SHALL NOT be implemented through the canonical LLM `pre_request_admission` stage or another post-decode feature hook.

1.6. Existing behavior for allowed traffic SHALL remain unchanged except for the bounded cost of enabled client-IP resolution, policy evaluation, and local country lookup when required.

1.7. A configured process-owned database service MAY load/update GeoIP data while request enforcement is disabled; this SHALL NOT weaken the zero-work request-path contract in 1.3-1.4.

## Requirement 2: Ordered Allow/Deny Policy Semantics

**Objective:** As an operator, I want deterministic Apache-style rule-class precedence so broad country rules and narrow exceptions compose predictably.

### Acceptance Criteria

2.1. THE configuration SHALL support exactly two v1 policy orders: `deny_allow` and `allow_deny`.

2.2. FOR `deny_allow`, THE engine SHALL evaluate deny-class matches and then allow-class matches, with allow winning when both classes match and allow as the default when neither class matches.

2.3. FOR `allow_deny`, THE engine SHALL evaluate allow-class matches and then deny-class matches, with deny winning when both classes match and deny as the default when neither class matches.

2.4. A rule-class match SHALL be true when either its configured country set matches the resolved country or one of its IP/CIDR rules contains the resolved client IP.

2.5. THE engine SHALL NOT use first-match firewall semantics where rule ordering within a class changes the result.

2.6. A CIDR exception SHALL therefore be able to override a broad country rule according to the selected second-phase precedence, including denying country `RU` while allowing a Moscow office CIDR under `deny_allow`.

2.7. THE compiled policy SHALL be immutable for the lifetime of one request-plane generation.

## Requirement 3: IPv4/IPv6 Address and CIDR Rules

**Objective:** As an operator, I want exact-address and subnet exceptions for both IP families without request-time parsing or DNS.

### Acceptance Criteria

3.1. THE configuration SHALL accept IPv4 and IPv6 CIDR prefixes in both allow and deny classes.

3.2. THE configuration SHALL accept exact IPv4/IPv6 addresses and compile them as host prefixes (`/32` or `/128`).

3.3. DURING configuration compilation, THE implementation SHALL parse rules with `net/netip`, normalize network prefixes with `Masked()`, and reject malformed addresses/prefixes.

3.4. BEFORE policy matching, THE resolved client address SHALL be normalized with IPv4-mapped IPv6 addresses unmapped to canonical IPv4 form.

3.5. THE policy SHALL NOT perform DNS resolution and SHALL reject hostname rules.

3.6. Request-time matching SHALL operate only on precompiled immutable address/prefix values.

3.7. Implementations MAY short-circuit an outcome that can no longer be changed by country evaluation, but SHALL preserve the truth table in Requirement 2.

## Requirement 4: Country Rule Semantics

**Objective:** As an operator, I want country rules to mean geolocated country consistently and not silently switch to network-registration geography.

### Acceptance Criteria

4.1. THE configuration SHALL accept ISO-3166 alpha-2 country codes and normalize them to uppercase at compile time.

4.2. INVALID country-code syntax SHALL reject configuration before publication; implementations SHOULD validate against a maintained ISO-3166 alpha-2 set rather than only checking string length.

4.3. THE lookup adapter SHALL map policy country only from the MMDB `country.iso_code` semantic field.

4.4. THE implementation SHALL NOT silently fall back to `registered_country.iso_code` when `country.iso_code` is absent.

4.5. WHEN an address has no country record, THE lookup SHALL report `found=false` rather than an infrastructure error.

4.6. A no-country result SHALL produce no country-class match and SHALL therefore follow CIDR matches and the selected policy order's ordinary default.

4.7. Country policy SHALL remain independent of vendor-specific record structs through a narrow internal country-lookup port.

## Requirement 5: Direct Client-IP Resolution by Default

**Objective:** As a security administrator, I want attacker-controlled forwarding headers ignored unless I explicitly establish a trusted proxy boundary.

### Acceptance Criteria

5.1. THE default client-IP source SHALL be `direct` and SHALL derive the address from `http.Request.RemoteAddr`.

5.2. IN direct mode, THE GeoIP feature SHALL ignore `Forwarded`, `X-Forwarded-For`, and vendor-specific forwarding headers.

5.3. IF `RemoteAddr` cannot be parsed as a valid normalized IP address, THEN an enabled GeoIP gate SHALL fail closed with a generic denial.

5.4. GeoIP client-IP resolution SHALL NOT alter the existing transport-authentication peer attribution contract as a side effect.

5.5. THE resolved address parser SHALL support ordinary Go host:port forms and host-only test/server forms without performing hostname resolution.

## Requirement 6: Explicit Trusted Reverse-Proxy Resolution

**Objective:** As an operator behind a reverse proxy, I want GeoIP to evaluate the real client while preventing clients from spoofing forwarding headers.

### Acceptance Criteria

6.1. THE configuration MAY select `x_forwarded_for` or RFC `forwarded` as the client-IP source only together with at least one explicitly configured trusted-proxy CIDR.

6.2. WHEN the immediate direct peer is not trusted, THE resolver SHALL ignore the configured forwarded header and use the direct peer.

6.3. WHEN the immediate peer is trusted, THE resolver SHALL parse a bounded forwarding chain, conceptually include the direct peer, walk hops from right to left, discard trusted hops, and select the first non-trusted IP as the client.

6.4. THE resolver SHALL correctly handle IPv4 and IPv6 forms required by the selected forwarding syntax, including quoted/bracketed IPv6 in RFC `Forwarded`.

6.5. THE resolver SHALL enforce fixed limits on accepted forwarding-header bytes and hop count before performing unbounded work or allocation.

6.6. IF the configured authoritative forwarding header is malformed, ambiguous, exceeds bounds, or yields no usable client address, THEN the gate SHALL fail closed rather than trust a less-authoritative attacker-controlled value.

6.7. THE feature SHALL NOT trust the leftmost XFF address merely because a header exists.

6.8. PROXY protocol and CDN/vendor-specific headers SHALL remain out of scope for this spec.

## Requirement 7: Local MMDB Country Lookup

**Objective:** As an operator, I want request-time geolocation to be local, fast, concurrency-safe, and independent of an external service.

### Acceptance Criteria

7.1. Country lookup on the request path SHALL use a long-lived local MMDB reader and SHALL perform no request-triggered network call.

7.2. THE core policy SHALL depend on a narrow interface equivalent to `LookupCountry(netip.Addr) (country string, found bool, err error)` and SHALL NOT import GeoIP database implementation types.

7.3. THE standard infrastructure adapter SHALL support a Country MMDB sufficient to decode `country.iso_code`, including `dbip-country-lite` and compatible Country editions selected by validated configuration.

7.4. THE adapter SHALL open and structurally validate a candidate MMDB before it becomes active.

7.5. THE active reader SHALL be safe for concurrent lookup and SHALL be reused rather than opened per request.

7.6. IF the active reader reports an unexpected lookup/decode error after enforcement has been activated, THEN the request SHALL fail closed.

7.7. IF a compiled policy has no country rules whose result could affect the decision, THEN the request path SHALL avoid MMDB lookup.

7.8. Long-lived local-reader reuse, rather than a request-triggered backend or per-IP cache, SHALL be the baseline mechanism satisfying the issue's requirement to avoid repeated expensive GeoIP backend work.

## Requirement 8: Database Readiness, Provisioning, and Last-Known-Good Behavior

**Objective:** As an operator, I want enforcement never to silently run without trustworthy country data, while transient update outages do not take down a proxy with a valid database.

### Acceptance Criteria

8.1. BEFORE publishing an enabled GeoIP request generation whose compiled policy can require country lookup, THE composition/runtime boundary SHALL verify that a usable country-lookup service is ready.

8.2. IF enforcement is enabled at startup and no valid LKG database exists, THEN managed mode SHALL make one bounded initial acquisition attempt before serving the enabled request plane.

8.3. IF no valid database is available after the bounded startup attempt, THEN startup or candidate publication SHALL fail rather than silently fail open.

8.4. ONCE a valid LKG exists, later update-network failures SHALL retain the existing reader and SHALL NOT make otherwise valid allowed requests depend on managed-update network availability.

8.5. A local database source SHALL fail startup/candidate readiness when its configured file is absent, unreadable, corrupt, or semantically incompatible and country lookup is required by an enabled generation.

8.6. THE process service SHALL expose bounded readiness/status information without exposing credentials or per-client data.

8.7. IF database service configuration is present while enforcement is disabled, THE process MAY provision/load/update the service so a later pure-policy reload can enable GeoIP without process restart.

8.8. IF no process-owned database service was configured at startup, THEN a later reload that enables a country-dependent policy SHALL reject candidate publication; adding/changing the process-owned database source requires restart under v1 lifecycle classification.

8.9. A policy containing only decisive CIDR rules that provably never needs country lookup MAY operate without MMDB readiness, but the compiler SHALL prove that property rather than fail open dynamically.

## Requirement 9: Managed Automatic Database Updates

**Objective:** As an operator, I want the proxy to keep managed GeoIP data current without cron or manual file replacement.

### Acceptance Criteria

9.1. Managed mode SHALL support automatic periodic database update checks owned by the proxy process.

9.2. THE standard updater SHALL download the DB-IP Lite database over public HTTPS from the documented static release URL pattern using the standard library HTTP client with an injected bounded transport, rather than scraping release artifacts, hard-coding ad-hoc download URLs beyond the documented pattern, or shelling out to an external updater executable.

9.3. THE updater SHALL use the current database checksum/update token only for upstream change detection and SHALL NOT treat MD5 as a cryptographic authenticity guarantee.

9.4. THE configured/default update cadence SHALL be conservative, bounded, and jittered to avoid synchronized fleet bursts; v1 defaults SHALL target approximately daily checks rather than minute-scale polling.

9.5. EVERY download/check SHALL use a bounded context timeout and bounded response/database size.

9.6. A changed database SHALL be written to a non-active temporary/versioned path, fully read/closed as required, opened and verified as a valid expected Country MMDB, and only then published as active.

9.7. ANY network, authentication, disk, parse, verification, or publication failure SHALL leave the previous LKG reader/file active and unchanged.

9.8. THE updater SHALL NOT overwrite the currently open/memory-mapped database file in place.

9.9. Publication SHALL be safe under concurrent lookups and SHALL not close an old reader while a lookup is still using it.

9.10. THE implementation SHALL use a Windows-safe file lifecycle based on versioned files/atomic metadata rather than assumptions that an open mapped file can be replaced or deleted.

9.11. THE process SHALL garbage-collect obsolete validated versions only after they are no longer active/in use, while retaining enough LKG state for restart recovery.

9.12. Update failures SHALL produce bounded operational telemetry and SHALL NOT create a per-request log storm.

## Requirement 10: Runtime Policy Reload and Lifecycle Ownership

**Objective:** As an operator, I want country/CIDR policy changes applied without proxy restart while lifecycle-heavy database configuration remains explicit and safe.

### Acceptance Criteria

10.1. `enabled`, `order`, allow/deny countries, allow/deny IP/CIDR rules, client-IP source, and trusted-proxy CIDRs SHALL be reloadable through the existing versioned runtime configuration mechanism.

10.2. A reload SHALL compile and validate the complete immutable GeoIP policy beside the active generation and publish it only with the candidate generation's existing atomic-swap semantics.

10.3. Invalid GeoIP policy changes SHALL reject the candidate atomically and SHALL leave the active generation unchanged.

10.4. Database source/provider, storage location, database edition, and managed-update enablement/cadence settings SHALL initially be classified restart-required when they change process-owned resources.

10.5. THE config-reload classifier SHALL explicitly classify every GeoIP field; no new GeoIP subtree SHALL bypass typed reload/restart policy.

10.6. Enabling GeoIP by reload SHALL fail candidate publication if the compiled policy needs country lookup and the process-owned country lookup is not provisioned and ready.

10.7. Disabling GeoIP by reload SHALL publish a generation without the ingress wrapper while leaving process-owned resources subject to their configured lifecycle.

10.8. A request SHALL use one immutable GeoIP policy generation for its admission decision.

10.9. New generations SHALL NOT mutate process-owned reader/updater configuration as a side effect of pure-policy reload.

## Requirement 11: HTTP Denial and Information-Disclosure Contract

**Objective:** As a security administrator, I want rejected traffic to receive a stable generic response that does not reveal rule internals.

### Acceptance Criteria

11.1. A GeoIP policy denial, unsafe client-IP resolution, or fail-closed lookup error SHALL return HTTP `403 Forbidden`.

11.2. THE response body SHALL be generic and SHALL NOT reveal the resolved country, client IP, matching rule/CIDR, trusted-proxy topology, database state, or policy order.

11.3. THE feature SHALL NOT use HTTP `451 Unavailable For Legal Reasons` unless a separate future feature implements denial specifically due to a legal demand.

11.4. Denial SHALL occur without invoking frontend-specific error renderers because the gate precedes frontend identification/decoding.

11.5. Global security/server response middleware outside the gate SHALL continue to apply their response contract.

## Requirement 12: Observability Without Hostile-Traffic Log Spam

**Objective:** As an operator, I want enough bounded telemetry to know the control is working without turning hostile traffic into high-cardinality metrics or noisy logs.

### Acceptance Criteria

12.1. GeoIP SHALL expose dedicated process metrics for bounded decision outcomes and database/update health.

12.2. Metrics SHALL use only finite labels such as decision/reason/result classes and SHALL NOT label by source IP, CIDR text, arbitrary header value, raw country supplied by a client, or other attacker-controlled high-cardinality strings.

12.3. GeoIP-denied requests SHALL bypass the normal access-log middleware and general request tracing/HTTP metrics by virtue of the early gate placement.

12.4. The gate MAY emit a bounded/rate-limited security diagnostic, but SHALL NOT emit one unbounded normal log line per denied hostile request by default.

12.5. Database updater failures/recovery SHALL be observable through bounded metrics and bounded operational logs that redact credentials.

12.6. Diagnostic/status surfaces SHALL never expose database credentials/license keys or raw forwarding headers.

12.7. Recommended decision-reason dimensions SHALL remain finite (for example `cidr_allow`, `cidr_deny`, `country_allow`, `country_deny`, `default_allow`, `default_deny`, `client_ip_error`, `lookup_error`) rather than recording literal rule values.

## Requirement 13: Resource Bounds, Security, and Performance Gates

**Objective:** As a proxy operator, I want the security layer itself resistant to resource abuse and demonstrably cheap enough for the hot path.

### Acceptance Criteria

13.1. THE request path SHALL perform no filesystem open, database download, remote HTTP call, DNS query, or configuration/CIDR parsing.

13.2. V1 SHALL NOT require a per-IP lookup cache; an implementation MAY add a strictly bounded cache only if profiling demonstrates value and invalidation is tied safely to active database version.

13.3. An unbounded attacker-keyed IP/cache map SHALL NOT be introduced.

13.4. Forwarding-header parsing SHALL have fixed byte/hop bounds and SHALL avoid quadratic parsing behavior.

13.5. Country lookup publication and concurrent request lookups SHALL pass targeted race testing.

13.6. Unit/property/fuzz coverage SHALL include malformed `RemoteAddr`, XFF, RFC `Forwarded`, IPv4, IPv6, mapped IPv4, CIDR boundaries, duplicate/overlapping rules, and both policy-order truth tables.

13.7. Integration tests SHALL prove that a denied request does not reach auth, frontend decode, routing, persistence/model fakes, normal access logging, general HTTP metrics, or OTel middleware.

13.8. Reload tests SHALL prove valid policy changes affect newly admitted requests atomically, invalid candidates preserve the old policy, enable requires a ready lookup when needed, and disable removes request-path GeoIP work.

13.9. Updater tests SHALL cover unchanged, successful, timeout, authentication failure, truncated/oversized/corrupt MMDB, disk failure, concurrent swap, restart LKG recovery, and cleanup ordering.

13.10. Benchmarks SHALL separately measure disabled composition/request path, enabled CIDR-only decisions, enabled country lookup, trusted-proxy resolution, and rule-set scaling.

13.11. The implementation SHALL document expected performance bounds and SHALL not introduce a more complex prefix-tree/cache data structure without benchmark evidence.

13.12. DB-IP Lite requires no account, API key, or credential; there are no database secrets to manage. If a future managed source requires credentials, they SHALL come from an existing secure secret/environment convention, SHALL NOT be embedded in diagnostics/config dumps, and SHALL NOT be logged.

13.13. The repository/binary SHALL NOT bundle the DB-IP Lite database; operator setup/documentation SHALL give DB-IP Lite attribution under its CC BY 4.0 license (copyright + license reference) and cover its data-source update obligations.

## Requirement 14: Brownfield Plane and Generation Compatibility

**Objective:** As a Go-LIP maintainer, I want GeoIP to fit existing listener and generation boundaries without creating a parallel control plane or changing unrelated transport semantics.

### Acceptance Criteria

14.1. V1 GeoIP enforcement SHALL wrap the canonical standard **data-plane generation** produced by `ComposeStandardHTTP` and SHALL NOT automatically wrap the separate process-owned runtime-config management listener.

14.2. The existing management listener's loopback/dedicated-token trust model SHALL remain unchanged by this feature; applying GeoIP to management traffic requires a separate explicit design.

14.3. WHEN a new GeoIP policy generation is published, newly admitted requests/connections SHALL use the new generation according to existing runtime-host selection semantics.

14.4. Requests and long-lived SSE/WebSocket work already pinned to an older generation SHALL continue on that generation and SHALL NOT be retroactively terminated solely because a later GeoIP policy would deny their source.

14.5. GeoIP SHALL NOT add a parallel file watcher, reload API, configuration store, service locator, or global mutable policy pointer outside the existing generation/process-service architecture.

14.6. GeoIP SHALL NOT require changes to backend plugins/connectors or frontend protocol DTOs/error renderers.

14.7. Existing auth `PeerIP` attribution derived from direct `RemoteAddr` SHALL remain unchanged unless separately redesigned outside this spec.

## Requirement 15: Configuration Validation and Operator Contract

**Objective:** As an operator, I want invalid policy rejected deterministically before publication and configuration validation to work without live provider connectivity.

### Acceptance Criteria

15.1. THE configuration model SHALL place GeoIP under the deployment access/security configuration and SHALL validate policy order, countries, CIDRs, client-IP source, trusted-proxy requirements, database source mode, update bounds, and mutually exclusive source fields.

15.2. THE implementation SHALL support an explicit v1 database source distinction equivalent to `managed` versus `local`, where managed mode owns updates and local mode treats the file as operator-owned.

15.3. Managed mode SHALL require no account, API key, or credential; the DB-IP Lite feed is fetched over public HTTPS without authentication, so no credential appears in reloadable policy fields or diagnostics.

15.4. `check-config` SHALL validate all static GeoIP syntax/semantics and reload classification without contacting the database provider or performing any database download/update.

15.5. `check-config` MAY structurally validate an explicitly available local MMDB path, but absence of external network connectivity SHALL NOT make an otherwise static configuration check fail.

15.6. Runtime startup/candidate activation SHALL perform the separate lookup-readiness check required by Requirement 8; static config validity SHALL NOT be confused with live resource readiness.

15.7. Documentation SHALL include examples for deny-list posture, allow-list posture, country deny plus office CIDR exception, direct-peer deployment, trusted reverse-proxy deployment, managed updates, local MMDB, runtime reload, and recovery from an invalid candidate.

15.8. Documentation SHALL warn that GeoIP is approximate defense in depth and can be bypassed by VPN/proxy/relay infrastructure; it SHALL NOT be presented as identity or sanctions-compliance proof.
