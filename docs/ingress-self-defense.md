# Ingress self-defense

AIProxer ships a small, **default-on** defense in front of the standard HTTP data plane. It rejects request targets that cannot plausibly belong to an AI proxy, tracks bounded per-source behaviour, and applies temporary exponential quarantine to repeated unauthenticated access attempts. It is deliberately *not* a WAF and not a threat-intelligence system.

It runs as an early transport gate: a refusal happens before authentication, frontend decoding, routing, persistence, model execution, general HTTP tracing, and access logging. It applies only to the standard data-plane handler graph. The separate process-owned management/recovery listener is not wrapped, so the existing loopback or dedicated-token recovery path always stays available.

## Default posture and explicit opt-out

Omitting `access.self_defense` is identical to the documented defaults below. A normally exposed proxy therefore gets basic ingress self-defense with no extra setup.

Opt out with an explicit boolean:

```yaml
access:
  self_defense:
    enabled: false          # removes the gate, the matcher, adaptive-state lookups and auth observation
```

Keep only the adaptive half, or only the deterministic path half, with the other explicit flag:

```yaml
access:
  self_defense:
    impossible_paths: false  # bypass the fixed matcher; adaptive quarantine still applies
```

A disabled generation is a structural fast path: no gate is installed, the process performs no request-side address resolution or state lookup, and the transport-auth chain is exactly the unobserved chain it was before the feature existed.

## Shared client-address trust

Self-defense resolves the source address with the **shared `access.geoip.client_ip`** configuration: the same direct-peer default, the same `source: x_forwarded_for` / `source: forwarded` selection, and the same `trusted_proxies` prefixes and parser bounds documented in [GeoIP ingress access control](geoip-ingress-access-control.md). There is deliberately no second self-defense forwarding-trust setting and no second trust boundary. If the immediate peer is not trusted, forwarding headers are ignored and the direct peer address is used; a malformed, ambiguous, oversized or overlong trusted chain fails closed with a generic refusal.

Self-defense needs no GeoIP country database, so a deployment with country/CIDR enforcement switched off still receives the shared forwarding-trust boundary.

## Adaptive exemptions

`adaptive.exempt_cidrs` is an **allowlist** of sources exempt from adaptive state and quarantine. It is never a ban list:

- It suppresses adaptive state mutation and quarantine only.
- It never overrides an existing `access.geoip` hard denial.
- It never makes a matched impossible-path request valid; an exempt source still receives the generic `404`.
- It never creates prefix-keyed, subnet, ASN, or country state.

```yaml
access:
  self_defense:
    adaptive:
      exempt_cidrs: [198.51.100.0/24, 2001:db8:1234::/48]
```

## Defaults and validation

| Field | Default | Accepted range | Reload class |
|---|---:|---|---|
| `enabled` | `true` | boolean | reload |
| `impossible_paths` | `true` | boolean | reload |
| `adaptive.auth_failures` | `5` | `2`..`100` | reload |
| `adaptive.window` | `1m` | `1s`..`1h` | reload |
| `adaptive.initial_quarantine` | `1m` | `1s`..`1h` | reload |
| `adaptive.max_quarantine` | `2h` | `>= initial_quarantine`, at most `24h` | reload |
| `adaptive.state_ttl` | `24h` | `1m`..`168h` | restart |
| `adaptive.max_entries` | `100000` | `1024`..`1000000` | restart |
| `adaptive.exempt_cidrs` | empty | normalized IPv4/IPv6 prefixes | reload |

Durations are Go duration strings, so `state_ttl` is written `168h` and **not** `7d`: the loader's duration parser has no day unit. The same applies to every other duration field.

`aipstd check-config` validates this syntax, the durations, the entry bounds, the CIDRs, and the cross-field constraint above. It does not construct the process state table, bind a listener, download anything, or perform network I/O.

## Reload and restart

Request policy is generation-reloadable: `enabled`, `impossible_paths`, `auth_failures`, `window`, `initial_quarantine`, `max_quarantine`, and `exempt_cidrs`. A valid candidate is compiled and published as an immutable generation; in-flight SSE/WebSocket work stays pinned to the generation it started on, so a reload only affects requests admitted to the new generation.

`state_ttl` and `max_entries` size the single process-owned adaptive table shared by every generation, so they are **restart-required**. A candidate that mixes reloadable changes with restart-required process-resource changes is rejected atomically: nothing is published and the last-good generation keeps serving with its existing state. Adaptive state survives a successful policy-only reload and is disposed only with the process.

Restart-required is decided on the **compiled** limits, not on how the YAML is spelled, so it agrees with the defaults above. Omitting the whole `access.self_defense` block, spelling the documented default out (`state_ttl: 24h`, `max_entries: 100000`), and spelling an equivalent duration (`1440m` is the same instant as `24h`) all produce identical process limits, and none of them forces a restart. Only a candidate whose compiled limits actually differ is rejected.

## Configured paths are literal routes

An operator-supplied data-plane path — the OpenResponses `base_path`, and the `diagnostics`, `observability.metrics`, secure-session, model-diagnostics and protected operator mount paths — must be a **literal** route, and validation rejects anything that is not one:

- ServeMux pattern syntax is rejected: `{name}`, `{name...}` and `{$}` mean something to the router that a literal path cannot express.
- `?` and `#` are rejected: a decoder splits them off as query and fragment material, and this layer's normalizer deliberately cuts at a decoded `?`, so a path containing one is never the same value for the operator, the router and the normalizer.
- `*`, `\`, `%`, an empty path segment, a `.` or `..` segment, and control or whitespace characters are rejected.

The reason is structural, not stylistic: the router and this layer both reason about literal paths, so a value that means something else to either of them would make a legitimate route unreachable. An operator who wants a literal character writes the literal character.

This applies to **operator input only**. The bundled frontends' own route claims may use ServeMux wildcards — the OpenAI-Responses frontend claims `/v1/responses/{id}/cancel` — and the carve reproduces the router's segment grammar for those, matching `{name}` against one non-empty segment, `{name...}` against the remaining segments, and `{$}` against the end of the path.

An invariant in `internal/archtest` requires that every operator path the validators **accept** registers a path that this layer's normalizer cannot move. That is the property that prevents a route from being shadowed, and it holds independently of any individual metacharacter.

## Configured routes are never shadowed

The fixed matcher is a set of audited commodity-probe prefixes, but the data-plane path surface is operator-configurable: the OpenResponses `base_path` and the `diagnostics`, `observability.metrics`, secure-session, model-diagnostics and protected operator mount paths all accept **any** normalized absolute non-root path. Those two spaces overlap, so some legitimate configurations name a path inside a probe family.

A published route is therefore authoritative: the matcher never refuses a route this proxy actually serves. Each generation collects the method/path pairs its configuration *could* publish, and composition then resolves them against the real router that was just built. Ownership and match semantics are the **router's** answers, not a reading of the configuration:

- A configured path whose feature is disabled is not mounted, so it owns nothing and carves nothing.
- A literal registration is exact: it owns its own method/path pair and nothing below it. OpenResponses' `POST <base>/responses`, the diagnostics and metrics paths, and the accounting admin path are all exact.
- A trailing-slash registration is a subtree and owns everything below it. The pprof mount and the secure-session and control-plane query prefixes are subtrees.
- Comparison is **case-sensitive**, like the router. `/WP-ADMIN/responses` is not the route `/wp-admin/responses`, so it stays a probe.
- The route's method is part of ownership. A route registered for `POST` is not owned for `DELETE`.

The carve is a routing decision only, and it is bounded by the router:

- A probe **beside** a published route, in the same family, is still refused and still scores. `/wp-admin/healthz` does not make `/wp-admin/adminer.php` reachable, and it does not make `/wp-admin/responses/.env` reachable either, because an exact registration owns no subtree.
- A request that reaches a published route is not counted as an impossible-path probe, so a legitimate client cannot quarantine itself out of a valid configuration.
- The router's fallback is never ownership. Every unrouted path resolves to some fallback handler, so a fallback pattern would otherwise carve the entire data plane; both the bare root and the empty pattern are excluded.

If the projection is ever absent, the matcher keeps its full pre-carve behavior: refusing a published route is never the safe default to *rely* on, but silently opening the families would be worse.


## Generic responses

Refusals are generic and identical for every triggering request, so they never disclose a matched rule, a source address, a quarantine deadline, an offense level, a score, or any other security internal:

- An impossible-path request receives a generic `404`. A generic 404 also hides the presence of the matcher itself.
- An active quarantine that can positively prove the request cannot authenticate receives a generic `429`. No `Retry-After` hint is sent.
- A source address that cannot be resolved into a usable identity receives a generic `403`.

Credential-bearing and unknown-auth traffic may still **reach authentication** while an address is quarantined, so a legitimate user behind a shared public IP, VPN exit, carrier NAT, or corporate proxy is never locked out by another actor on the same address. Requests that reach authentication keep the existing authentication response semantics unchanged; self-defense never turns an authorization denial or a provider failure into a hostile verdict.

## Successful authentication is a reset, not a trust cache

A completed, successful full authentication chain clears that exact source address's accumulated adaptive state. That is a **reset**: it is not a trusted-IP entry, it expires nothing into permanence, and later hostile behaviour starts from a fresh offense level. A prior success never exempts a later impossible-path request and never overrides fixed `access.geoip` policy.

## Observability

Three bounded process metrics describe the feature, with a single finite label:

| Metric | Type | Labels |
|---|---|---|
| `aip_self_defense_denials_total` | counter | `reason` |
| `aip_self_defense_quarantine_transitions_total` | counter | `reason` |
| `aip_self_defense_state_entries` | gauge | none |

`reason` is one of `impossible_path`, `auth_failure_threshold`, `active_quarantine`, `client_ip_error`, or the finite `unknown` bucket. The metrics never label by source IP, CIDR text, request path, query, header, User-Agent, credential, principal, country, or any other attacker-controlled value, and the entry gauge reads the live process-owned state, so it reflects inserts, bounded eviction and successful-auth clears immediately. Inactivity expiry is **lazy**: it happens on the next state lookup or mutation for that source, so a source that has passed `state_ttl` and is never touched again stays counted until something looks it up. Read `aip_self_defense_state_entries` as allocated capacity in use, not as a count of currently hostile sources.

Metrics are not authoritative. A deployment with Prometheus scraping disabled projects no observer at all, and every security decision, state mutation, and response is identical. Per-request hostile-event **logging** is disabled: the refusals above carry no rule, address, or penalty detail, and the proxy adds no hostile-request log line by default.

## Interaction with fixed access policy

Fixed country, IP, and CIDR allow/deny policy remains exclusively under [`access.geoip`](geoip-ingress-access-control.md) and stays the authoritative network-access policy. Self-defense does not introduce a second general-purpose static IP/CIDR engine. When both are enabled, a GeoIP hard denial happens before any self-defense behaviour for the same request. `adaptive.exempt_cidrs` affects adaptive state and quarantine only and can never override a hard denial.

## Deliberate non-goals for v1

- No external reputation feed, threat-intelligence client, or updater framework.
- No WAF, OWASP rule set, regex rule DSL, or request body/query-content inspection. Prompt and body content is never a reason to refuse a request on an otherwise legitimate proxy route.
- No persistent or distributed ban state. The adaptive state is process-local and in-memory; a restart forgets it.
- No automatic subnet escalation. One source address is never widened into a `/24`, `/64`, ASN, or country quarantine.
- No volumetric L3/L4 DDoS protection, and no claim that every credential-bearing brute-force attempt is blocked before authentication.

## See also

- [GeoIP ingress access control](geoip-ingress-access-control.md) — the shared `client_ip` trust and the fixed network policy.
- [Runtime configuration reload](runtime-config-reload.md) — generation publication, pinning, and restart-required fields.
- [`config/config.yaml`](../config/config.yaml) — the commented and default-value reference block.
