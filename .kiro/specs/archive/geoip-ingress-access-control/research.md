# Research & Design Decisions

## Summary

Issue #387 is technically straightforward at the policy level but security-sensitive at four boundaries: **middleware placement, client-IP trust, database lifecycle, and runtime reload ownership**. The recommended implementation deliberately avoids treating GeoIP as an ordinary LLM feature plugin.

The design target is:

```text
HTTP connection
  -> security headers
  -> downstream server policy
  -> outer recovery
  -> GeoIP ingress gate
  -> OpenTelemetry HTTP
  -> general Prometheus HTTP
  -> trace/request ID
  -> access log
  -> inner recovery
  -> transport auth
  -> frontend mux/decode
  -> runtime/routing/model
```

The gate uses one immutable generation-scoped policy plus one process-owned local Country MMDB service.

## Research Scope

Reviewed:

- Go-LIP current middleware/composition/reload/process-service architecture at `ca43dde919f4d53716a98bf53ffb57bd61560607`.
- Apache 2.2 `mod_authz_host` `Order` semantics because #387 explicitly requests `DENY,ALLOW` and `ALLOW,DENY` behavior.
- RFC 7239 `Forwarded` trust/security considerations.
- nginx recursive real-IP trust behavior as a mature reference for reverse-proxy chain evaluation.
- Go `net/netip` normalization and IPv4-mapped IPv6 behavior.
- Country MMDB guidance (DB-IP Lite and compatible MMDB sources).
- `github.com/oschwald/maxminddb-golang/v2` reader behavior/API (reads the DB-IP Lite MMDB and any compatible Country MMDB).
- DB-IP Lite static HTTPS download acquisition (no `geoipupdate` client, no account/key).
- HTTP status semantics for 403 and 451.

## Source Notes

Primary references:

- Apache `mod_authz_host` legacy order semantics: https://httpd.apache.org/docs/2.2/en/mod/mod_authz_host.html
- RFC 7239 `Forwarded`: https://www.rfc-editor.org/rfc/rfc7239.html
- nginx real IP module: https://nginx.org/en/docs/http/ngx_http_realip_module.html
- Go `net/netip`: https://pkg.go.dev/net/netip
- DB-IP Lite download: https://db-ip.com/db/download/ip-to-country-lite
- DB-IP Lite licensing (CC BY 4.0): https://creativecommons.org/licenses/by/4.0/
- DB-IP IP to Country Lite: https://db-ip.com/db/ip-to-country-lite
- maxminddb Go reader: https://github.com/oschwald/maxminddb-golang
- HTTP Semantics RFC 9110: https://www.rfc-editor.org/rfc/rfc9110.html
- HTTP 451 RFC 7725: https://www.rfc-editor.org/rfc/rfc7725.html
- DB-IP Lite license/attribution: https://db-ip.com/db/lite.php

## Decision 1: Use an Early `stdhttp` Ingress Gate

### Alternatives considered

1. Generic `pre_request_admission` feature stage.
2. Transport-auth provider.
3. Frontend-specific wrappers.
4. Dedicated `stdhttp` ingress middleware.

### Decision

Use option 4.

### Rationale

The existing `pre_request_admission` stage occurs after canonical request shaping and is intentionally part of the LLM feature pipeline. By that point the proxy has already paid costs that #387 wants to avoid. Transport auth is also too semantically narrow: GeoIP denies before identity and should not be coupled to authentication credentials/renderers.

Frontend-specific wrappers duplicate policy and would create protocol Cartesian growth. A single HTTP ingress wrapper before general instrumentation/auth/routes is the narrowest common choke point.

The gate remains inside outer recovery and global server/security wrappers so blocked responses remain safe and consistent.

## Decision 2: Omit the Wrapper Entirely When Disabled

A per-request `if !enabled { next }` is cheap, but LIP already builds immutable handler generations. Omitting the wrapper from disabled generations is simpler and proves the requested fast path structurally:

- no GeoIP resolver invocation;
- no lookup call;
- no policy call;
- no hidden global enabled flag.

Runtime reload from disabled to enabled simply publishes a newly compiled handler graph.

## Decision 3: Separate Policy Lifetime From Database Lifetime

### Generation-owned

- enabled flag;
- order;
- allow/deny countries;
- allow/deny address/prefix rules;
- client-IP source;
- trusted-proxy prefixes.

### Process-owned

- active MMDB reader;
- local/versioned files and LKG metadata;
- managed update client;
- update loop/timer/jitter;
- readiness/status;
- reader publication/retirement.

This mirrors ADR 0008: immutable request generations are swapped; process services survive reload. It avoids opening/closing MMDB readers for every config generation and lets policy reload independently of updater lifecycle.

## Decision 4: Preserve Apache Rule-Class Semantics, Not Firewall First-Match Semantics

Apache's old `Order` directive evaluates two classes with a defined last class winning. The requested behavior is therefore represented as two explicit values:

| Matches | `allow_deny` | `deny_allow` |
|---|---|---|
| allow only | allow | allow |
| deny only | deny | deny |
| both | deny | allow |
| neither | deny | allow |

This is ideal for exceptions. Example:

```yaml
order: deny_allow
deny:
  countries: [RU]
allow:
  cidrs: [203.0.113.64/27]
```

An office address geolocated to RU matches both classes and is allowed because allow is the second phase.

Within one class, rule ordering is irrelevant.

## Decision 5: Compile IP Rules With `net/netip`

Use `netip.Addr`/`netip.Prefix` rather than legacy `net.IP`:

- parse at config compile time;
- normalize exact addresses to host prefixes;
- normalize prefixes via `Masked()`;
- normalize request addresses with `Addr.Unmap()`.

The last point matters because IPv4-mapped IPv6 addresses are distinct from ordinary IPv4 prefix matching unless normalized.

No DNS names are accepted; DNS would make a security hot path mutable/network-dependent.

For typical operator rule counts, a linear prefix scan is preferable to a new trie dependency. Add benchmarks before optimizing.

## Decision 6: Country Means `country.iso_code`

Use the actual geolocation country field. Do not fall back to registered-country because the latter describes registration and can differ from the address's geolocation.

Three lookup results remain distinct:

1. country found;
2. no country record (`found=false`);
3. lookup/decoder failure (`error`).

No-country is normal policy input. Lookup failure under an enabled country-dependent policy is fail-closed.

## Decision 7: Direct Peer Is the Default Security Authority

Go-LIP auth currently intentionally uses `RemoteAddr` and ignores forwarded headers. GeoIP keeps `direct` as its default.

Reverse-proxy deployments need an opt-in trusted-proxy mode. The safe algorithm is:

1. parse immediate direct peer;
2. if direct peer is not in configured trusted-proxy prefixes, ignore forwarding header and use direct peer;
3. if trusted, parse the configured header with fixed size/hop limits;
4. append/consider direct peer and walk right-to-left;
5. discard trusted hops;
6. select first non-trusted address;
7. fail closed on malformed/ambiguous authoritative input.

This is conceptually aligned with nginx `real_ip_recursive` and RFC 7239's warning that forwarding metadata has no inherent trust.

GeoIP must not change auth attribution as a side effect. A later auth design may reuse a generic trusted-client-address facility only through an explicit migration.

## Decision 8: Use Local MMDB Only on the Request Path

Do not call remote GeoIP web services per request. A local Country MMDB provides:

- bounded in-process lookup latency;
- no per-request network dependency;
- no request-driven quota exposure;
- no fail-open question caused by remote outage;
- materially lower attack amplification.

Recommended implementation dependency: `github.com/oschwald/maxminddb-golang/v2` (reads the DB-IP Lite MMDB and any compatible Country MMDB).

Expose only a narrow internal port, conceptually:

```go
type CountryLookup interface {
    LookupCountry(netip.Addr) (country string, found bool, err error)
}
```

The HTTP/core policy does not know GeoIP database record or reader types.

## Decision 9: No Mandatory Per-IP Cache

The issue asks to cache expensive lookups to avoid backend spam. With local MMDB there is no request-time backend. The important optimization is **one long-lived concurrent reader**, not open-per-request.

An unbounded `map[IP]country` is actively undesirable because hostile clients can drive unique-key growth. V1 therefore has no mandatory per-IP cache. If profiling later proves decoding expensive, any cache must be bounded and invalidated/versioned with the active MMDB generation.

## Decision 10: Download DB-IP Lite Over Public HTTPS (No Account/Key)

DB-IP Lite is published as a static, keyless MMDB download at a documented monthly URL pattern, e.g. `https://download.db-ip.com/free/dbip-country-lite-YYYY-MM.mmdb.gz`.

Relevant behavior:

- the file is served over public HTTPS with no account, API key, or authentication;
- the download page publishes MD5/SHA1 checksums and the release month, enabling change detection without a proprietary metadata protocol;
- the archive is gzip-compressed and must be gunzipped before MMDB validation;
- the checksum is an update/change token, not a cryptographic authenticity primitive.

Use the standard library `net/http` client with an injected bounded transport/timeout to fetch the documented URL pattern, rather than maintaining copied endpoint/protocol logic or shelling out to a subprocess. Trust is based on the public HTTPS download path plus structural/semantic MMDB validation before publication. No `geoipupdate` client or credential is required.

## Decision 11: Transactional, Windows-Safe MMDB Publication

Do not replace the active MMDB file in place.

Recommended lifecycle:

```text
update check/download
  -> bounded temp/versioned file
  -> complete write/close
  -> open candidate reader
  -> Verify / database type validation
  -> short publication lock
       swap active reader/version
     unlock
  -> retire old reader after pre-swap lookups drain
  -> delete obsolete old version only after close
```

Versioned filenames avoid corruption becoming active and avoid Windows mapped-file replacement constraints.

A simple service-level RWMutex is sufficient unless benchmarks show otherwise:

- lookup holds RLock through decode;
- candidate download/open/verify occurs outside lock;
- publisher takes Lock only for pointer/version swap;
- acquiring the write lock proves pre-existing readers have drained before the old reader is closed.

## Decision 12: LKG and Startup Failure Semantics

- Load/validate existing LKG first.
- If enabled country-dependent policy has no usable DB in managed mode, make one bounded startup acquisition attempt.
- If still unavailable, fail startup/candidate publication.
- After a valid LKG is active, periodic update failures retain it and do not make allowed traffic depend on managed-update connectivity.
- An address with no country record is not an update/readiness failure.

A process may provision/update the MMDB while enforcement is disabled if database configuration exists, enabling a later pure-policy reload without restart. If no database service was configured at startup, enabling a country-dependent policy requires restart/provisioning.

## Decision 13: Keep `check-config` Offline

Existing `check-config` shares generation compilation semantics, but config validation must remain deterministic and suitable for CI/offline use.

It should validate:

- policy order;
- country codes;
- addresses/prefixes;
- trusted-proxy/source consistency;
- database mode/source field consistency;
- update interval/bounds;
- reload/restart classification.

It must not download/update a database or require provider connectivity. Runtime activation separately verifies process-resource readiness.

## Decision 14: 403 Is the Correct Denial Status

GeoIP access policy is an authorization/access-control decision, so use generic `403 Forbidden`.

Do not use `451 Unavailable For Legal Reasons`; RFC 7725 is intended for legal-demand blocking, which is not what this feature expresses.

The body must not reveal country, IP, rule, database, or trust-chain details.

## Decision 15: Dedicated Bounded Observability

Because the gate intentionally sits outside general tracing/HTTP metrics/access logging, denied hostile requests should not create those artifacts.

Add narrow process metrics, e.g. conceptually:

```text
lip_geoip_decisions_total{decision,reason}
lip_geoip_update_total{result}
lip_geoip_database_ready
lip_geoip_database_age_seconds
```

No IP/CIDR/header labels. Avoid country labels as a default cardinality/privacy choice; operators can infer aggregate denial posture from finite reasons and updater health.

Per-denial logging is off by default. Updater state changes/failures can use bounded operational logs with no credentials.

## Decision 16: Do Not Route Infrastructure Maintenance Through LLM Feature Stages

The updater is process infrastructure, not auxiliary model work and not a FeatureBundle lifecycle. It should have an explicit owned goroutine/timer under the GeoIP process service and stop through normal `ProcessServices.Close` ownership.

This keeps the feature independent from request-generation reload and prevents infrastructure scheduling from acquiring model/agent-oriented semantics.

## Configuration Shape Recommendation

Final field names can follow config conventions, but the semantic shape should remain close to:

```yaml
access:
  mode: multi_user
  geoip:
    enabled: true
    order: deny_allow

    deny:
      countries: [BY, CN, IR, RU]
      cidrs: []

    allow:
      countries: []
      cidrs:
        - 203.0.113.64/27
        - 2001:db8:1234::/48

    client_ip:
      source: direct             # direct | x_forwarded_for | forwarded
      trusted_proxies: []

    database:
      source: managed           # managed | local
      edition: dbip-country-lite
      directory: /var/lib/lip/geoip
      local_path: ""             # used only for source: local
      update:
        enabled: true            # managed only
        interval: 24h
```

DB-IP Lite requires no account, API key, or credential; the managed download uses the documented public HTTPS static URL pattern and no secret environment variables. Implementation must record the exact static URL pattern and checksums from the operator documentation.

## Expected Package Boundaries

Recommended, subject to import-guardrail validation:

```text
internal/core/geoip/
    policy.go
    compile.go
    ports.go

internal/stdhttp/geoip/
    middleware.go
    client_ip.go
    forwarded.go

internal/infra/geoip/
    service.go
    mmdb.go
    updater.go
    files.go

internal/core/config/
    geoip model + validation

internal/core/configreload/
    explicit GeoIP field classification

internal/infra/runtimebundle/
    process service ownership + generation projection

internal/stdhttp/contract/
    narrow security composition capability

internal/infra/metrics/
    bounded GeoIP metrics
```

Do not force exact filenames if a smaller layout follows current repository conventions better. The hard rule is dependency direction: core policy must not import HTTP/GeoIP database implementation; `stdhttp` must not own updater lifecycle; infrastructure must not own policy semantics.

## Security Threat Checklist

Implementation review should explicitly test:

- spoofed XFF/Forwarded from untrusted direct peers;
- trusted proxy chain with attacker-prepended values;
- malformed and very large headers;
- IPv4-mapped IPv6 rule bypass attempts;
- policy default misunderstandings;
- country unknown vs lookup error confusion;
- corrupt/truncated/oversized update payloads;
- updater secret leakage;
- race between lookup and reader replacement;
- Windows active-file replacement;
- unique-source cache/memory exhaustion;
- log/metric cardinality abuse;
- config reload enabling enforcement without a ready process service.

## Deferred Extensions

Possible future additions, intentionally not required by #387:

- PROXY protocol source address;
- Cloudflare/AWS/Azure/GCP vendor-specific validated headers;
- City/ASN/VPN/threat-intelligence predicates;
- management-listener GeoIP policy;
- operator-defined denial body;
- distributed/fleet update coordination;
- per-IP bounded cache if profiling proves useful;
- active revocation of already admitted long-lived connections.
