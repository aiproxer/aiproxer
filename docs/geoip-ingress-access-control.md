# GeoIP ingress access control

AIProxer can optionally reject HTTP data-plane requests using a local Country MMDB plus exact IP/CIDR exceptions. This is an early transport gate: denied requests receive a generic `403 Forbidden` before authentication, frontend decoding, routing, persistence, model execution, general HTTP tracing, metrics, or access logging.

GeoIP is approximate defense in depth. VPNs, proxies, relays, mobile networks, database lag, and misconfigured forwarding boundaries can produce false positives or negatives. It is not identity verification, proof of citizenship, sanctions screening, or legal-compliance evidence. Keep authentication, rate limiting, firewall/WAF, and identity policy enabled.

The `client_ip` trust configuration documented below is shared with the default-on [ingress self-defense](ingress-self-defense.md) gate, which reuses it verbatim. Fixed country/IP/CIDR allow/deny policy stays exclusively here; self-defense owns only bounded adaptive quarantine, deterministic impossible-path rejection, and generic client-address resolution failure.

## Policy examples

The policy order is exactly one of `deny_allow` or `allow_deny`. Within each class, countries and CIDRs are sets; configuration order does not matter.

```yaml
access:
  geoip:
    enabled: true
    order: deny_allow
    deny:
      countries: [BY, CN, IR, RU]
    allow:
      countries: []
      cidrs: [203.0.113.64/27, 2001:db8:1234::/48]
    client_ip:
      source: direct
    database:
      source: local
      local_path: /var/lib/aip/geoip/dbip-country-lite.mmdb
```

`deny_allow` is useful for a deny list with exceptions: a request matching both classes is allowed. For example, to deny Russia while allowing an office subnet:

```yaml
access:
  geoip:
    enabled: true
    order: deny_allow
    deny: {countries: [RU]}
    allow: {cidrs: [198.51.100.64/27]}
```

An allow-list posture uses `allow_deny`; the default is deny and a deny match wins when both classes match:

```yaml
access:
  geoip:
    enabled: true
    order: allow_deny
    allow: {countries: [CA, DE, GB, US]}
    deny: {cidrs: [198.51.100.7]}
```

Exact IPv4/IPv6 addresses are accepted in `cidrs` and are compiled as `/32` or `/128`. Hostnames and DNS rules are not accepted.

## Client address trust

The default `client_ip.source: direct` uses the literal accepted connection peer from `RemoteAddr` and ignores `Forwarded` and `X-Forwarded-For`.

Only configure forwarded sources when the immediate reverse proxy is explicitly trusted:

```yaml
access:
  geoip:
    client_ip:
      source: x_forwarded_for # or forwarded
      trusted_proxies:
        - 192.0.2.0/24
        - 2001:db8:ffff::/48
```

If the direct peer is outside these prefixes, forwarding headers are ignored. If it is trusted, AIProxer parses the selected header as one bounded chain (including repeated header fields), walks from right to left, discards trusted hops, and selects the first non-trusted address. Malformed, ambiguous, obfuscated, oversized, and overlong authoritative chains fail closed. The other forwarding-header family is ignored. Authentication peer attribution remains based on the direct `RemoteAddr`.

## Database sources and lifecycle

`database.source` is either:

- `local`: AIProxer opens only the configured `local_path`; it never constructs the managed updater or performs network acquisition.
- `managed`: AIProxer owns the DB-IP Lite versioned database directory and public HTTPS update lifecycle. Updates use bounded operations and retain the last-known-good reader when a download or validation fails.

The request path uses one long-lived, concurrency-safe reader. It performs no filesystem open, download, DNS lookup, or MMDB parsing. AIProxer does not require an account, API key, or database credential for DB-IP Lite, and the database is not bundled in the binary.

DB-IP Lite attribution: **DB-IP Lite, © DB-IP.com / Eris Networks S.A.S., licensed under CC BY 4.0**. See <https://creativecommons.org/licenses/by/4.0/> and <https://db-ip.com/db/lite.php>. Operators are responsible for complying with the license and keeping managed data current. For fleets, use deployment-level coordination or staggered schedules to avoid synchronized downloads.

Managed update intervals default to 24 hours and must be between 6 and 168 hours. Forwarding input is bounded to 16 KiB and 32 hops; one managed operation is bounded to two minutes and 128 MiB.

## Reload and recovery

Pure request policy fields are generation-reloadable:

- `enabled`, `order`, allow/deny countries and CIDRs;
- `client_ip.source` and `trusted_proxies`.

Database source, edition, directory/local path, and updater settings are restart-required because they own process resources. Mixed candidates are rejected atomically. A valid policy is compiled and published as an immutable generation; in-flight SSE/WebSocket work remains pinned to its existing generation.

A process-owned database service may be warm while enforcement is disabled, but a reload cannot create a missing service. Enabling a country-dependent policy therefore requires a ready process-owned reader. Disabling enforcement removes the request wrapper and performs no request-side address resolution or lookup.

`check-config` validates GeoIP syntax, country codes, CIDRs, source consistency, update bounds, and reload classification without opening an MMDB, downloading data, binding a listener, or contacting a provider. The data-plane gate does not wrap the separate process-owned management listener, so the existing loopback/dedicated-token recovery path remains available after an invalid candidate.

All denials use the same bounded body, `Forbidden\n`, with status 403. Responses never reveal the client IP, country, rule, forwarding topology, database state, or policy order.
