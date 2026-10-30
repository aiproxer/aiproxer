# Design Document

## Overview

This design implements issue #387 as an **early HTTP ingress access-control layer** for Go-LIP. It deliberately separates two lifetime domains:

1. an immutable **generation-scoped enforcement policy** used by the standard data-plane handler graph;
2. a **process-scoped country database service** that owns MMDB readiness, local/versioned files, and optional managed DB-IP Lite updates.

The central architectural rule is that GeoIP rejects traffic before general request instrumentation/authentication/frontend/runtime work without creating a parallel reload/control-plane architecture.

Brownfield baseline: `ca43dde919f4d53716a98bf53ffb57bd61560607`.

This revision incorporates the original brownfield design amendments plus review hardening for enforceable policy immutability, fixed abuse bounds, repeated forwarding-header handling, crash-durable LKG publication/recovery, exact `check-config` ownership, source-specific startup behavior, DB-IP Lite response-body ownership, and updater-versus-shutdown serialization.

## Goals

- Reject configured geographic/IP traffic before auth, frontend decode, routing, DB, billing, and model work.
- Preserve exact `deny_allow` / `allow_deny` class-precedence semantics.
- Support IPv4/IPv6 exact addresses and CIDRs.
- Make direct peer authoritative by default and forwarded addresses safe only through explicit trusted-proxy configuration.
- Use a local concurrent Country MMDB reader on the hot path.
- Manage MMDB updates automatically without compromising availability when an LKG exists.
- Hot reload pure policy through existing immutable generations.
- Keep database/updater lifecycle process-owned and restart-classified.
- Preserve current management-listener, auth-attribution, and in-flight generation semantics.
- Keep denial observability bounded and outside general hostile-traffic logging.
- Keep `check-config` deterministic, non-publishing, and network-independent.

## Non-Goals

- City/ASN/VPN/threat-intelligence policy.
- PROXY protocol or CDN-specific real-IP headers.
- WAF/firewall orchestration.
- Per-request GeoIP network service calls.
- Mandatory per-IP caching.
- Management-listener GeoIP enforcement.
- Retroactive disconnection of existing SSE/WebSocket sessions.
- Changes to frontend wire schemas, backend plugins, or auth peer attribution.
- Distributed updater coordination.

## Normative V1 Resource Limits

These are security contracts, not tuning suggestions. The implementation SHALL define one shared lower-level constant set and reuse it from config validation, the HTTP resolver, updater tests, and benchmarks.

| Limit | V1 value | Ownership |
|---|---:|---|
| aggregate selected forwarding-header bytes | 16 KiB | fixed HTTP security constant; not operator-configurable in v1 |
| forwarding hops | 32 | fixed HTTP security constant; not operator-configurable in v1 |
| managed MMDB download bytes | 128 MiB | fixed updater safety constant; not operator-configurable in v1 |
| one managed update/acquisition operation timeout | 2 minutes | fixed updater safety constant; not operator-configurable in v1 |
| default managed update interval | 24 hours | config default |
| minimum managed update interval | 6 hours | config validation |
| maximum managed update interval | 168 hours | config validation |
| periodic jitter | ±10% of configured interval | scheduler constant |

Zero, negative, overflowed, or out-of-range values SHALL be rejected during static validation when a value is operator-configurable. Internal fixed limits SHALL not be projected as mutable integers that a generation caller can override.

## Architecture

### HTTP placement

Current standard handler runtime order is effectively:

```text
SecurityHeaders
  -> DownstreamServer
    -> OuterRecovery
      -> OTelHTTP?
        -> PromHTTP?
          -> Trace + RequestID
            -> AccessLog
              -> InnerRecovery
                -> TransportAuth
                  -> RouteMux
```

The target becomes:

```text
SecurityHeaders
  -> DownstreamServer
    -> OuterRecovery
      -> GeoIPIngress?        # omitted entirely when disabled
        -> OTelHTTP?
          -> PromHTTP?
            -> Trace + RequestID
              -> AccessLog
                -> InnerRecovery
                  -> TransportAuth
                    -> RouteMux
```

In `stackHTTPHandler` composition order, build OTel/general Prometheus first, then wrap that handler with GeoIP if enabled, then apply `outerRecoveryMiddleware`, `DownstreamServerMiddleware`, and security headers.

This location preserves outer panic containment/security headers while ensuring denied traffic does not reach general OTel/HTTP Prometheus/request-ID/access-log/auth/frontend/runtime work.

### Lifetime split

```text
Process lifetime
┌──────────────────────────────────────────────────────────────┐
│ ProcessServices                                             │
│  └─ GeoIPDatabaseService                                   │
│      ├─ active CountryLookup reader/version                 │
│      ├─ LKG/versioned files + manifest                      │
│      ├─ updater root context + scheduler                    │
│      ├─ in-flight update lifecycle fence                    │
│      ├─ readiness/status                                    │
│      └─ bounded metrics                                     │
└──────────────────────────────────────────────────────────────┘
                    │ non-owning CountryLookup
                    ▼
Generation N                       Generation N+1
┌─────────────────────────┐       ┌─────────────────────────┐
│ immutable GeoIP Policy  │       │ immutable GeoIP Policy  │
│ immutable resolver cfg  │       │ immutable resolver cfg  │
│ GeoIP middleware?       │       │ GeoIP middleware?       │
│ rest of handler graph   │       │ rest of handler graph   │
└─────────────────────────┘       └─────────────────────────┘
```

A generation never closes/reconfigures the process reader/updater. Process shutdown closes the GeoIP service only under existing host/process-service ownership after request generations are retired.

## Components and Dependency Direction

### `internal/core/geoip`

Pure policy/domain package.

Responsibilities:

- order, reason, decision value types;
- normalized immutable country/address rule sets;
- Apache-compatible class precedence;
- safe decision-plan compilation/short-circuit metadata;
- narrow country lookup port.

Conceptual contracts deliberately do **not** expose mutable collections:

```go
package geoip

type Order uint8
const (
    OrderDenyAllow Order = iota + 1
    OrderAllowDeny
)

type ruleClass struct {
    countries frozenCountrySet // unexported, compiler-owned backing memory
    prefixes  []netip.Prefix   // unexported, never returned directly
}

type Policy struct {
    order              Order
    allow              ruleClass
    deny               ruleClass
    needsCountryLookup bool
}

func Compile(...) (*Policy, error)              // deep-copies/owns all backing data
func (p *Policy) NeedsCountryLookup() bool       // scalar/read-only accessor
func (p *Policy) Evaluate(addr netip.Addr, lookup CountryLookup) Decision

type CountryLookup interface {
    LookupCountry(netip.Addr) (country string, found bool, err error)
}

type Decision struct {
    Allow  bool
    Reason Reason // finite enum only
}
```

`Policy` has no mutator and no accessor returns a backing map/slice. The compiler copies input collections before publication. Once returned, every reachable object used by request evaluation is read-only by construction, so sharing a `*Policy` across a generation cannot mutate admission decisions or introduce collection races.

Core imports no `net/http`, GeoIP database implementation, logger, Prometheus, runtimebundle, or root stdhttp.

### `internal/stdhttp/contract`

This package remains the exact cycle-neutral composition boundary between `runtimebundle` and root `stdhttp`.

Add a data-only security projection; do **not** make `runtimebundle` import `internal/stdhttp/geoip`.

The resolver contract carries only semantic source/trust information. Fixed abuse limits come from shared constants and are not caller-overridable fields:

```go
type GeoIPResolverConfig struct {
    Source         ClientIPSource
    TrustedProxies []netip.Prefix // defensively copied
}

type GeoIPSecurityInput struct {
    Policy   *coregeoip.Policy
    Lookup   coregeoip.CountryLookup
    Resolver GeoIPResolverConfig
    Observer GeoIPObserver
}

type GeoIPObserver interface {
    Decision(reason coregeoip.Reason, allow bool)
}

type HTTPSecurityInput struct {
    // existing fields...
    GeoIP GeoIPSecurityInput
}
```

The contract imports only stdlib/lower-level core types. Slices are defensively copied using the same pattern as other contract projections. `Policy == nil` means no gate is installed. A whole `ProcessServices`, `any`, service locator, or middleware closure must not cross this contract.

### `internal/stdhttp/geoip`

HTTP adapter.

Responsibilities:

- direct `RemoteAddr` parsing;
- bounded XFF parsing;
- bounded RFC 7239 `Forwarded` parsing;
- trusted-proxy chain resolution;
- middleware using `contract.GeoIPSecurityInput`;
- generic 403 rendering.

It consumes the cycle-neutral contract and core policy but owns all HTTP/header semantics.

### `internal/infra/geoip`

Driven infrastructure adapter / process service.

Responsibilities:

- open/verify Country MMDB;
- decode only required `country.iso_code` semantics;
- own synchronized active reader;
- maintain versioned files and LKG manifest;
- managed DB-IP Lite update checks/downloads;
- transactional crash-durable publication/retirement;
- updater cancellation and close serialization;
- readiness/status;
- close/cleanup lifecycle.

Recommended implementation dependencies:

- `github.com/oschwald/maxminddb-golang/v2` (reads the DB-IP Lite MMDB and any compatible Country MMDB)
- standard library `net/http` for the DB-IP Lite static download (no `geoipupdate` client, no account/key)

DB-IP/provider client types never cross into core/stdhttp contracts.

## Configuration Model

Extend `AccessConfig` with a focused `GeoIP` subtree. Semantic model:

```go
type AccessConfig struct {
    Mode  string      `yaml:"mode"`
    GeoIP GeoIPConfig `yaml:"geoip"`
}

type GeoIPConfig struct {
    Enabled  bool              `yaml:"enabled"`
    Order    string            `yaml:"order"`
    Allow    GeoIPRuleConfig   `yaml:"allow"`
    Deny     GeoIPRuleConfig   `yaml:"deny"`
    ClientIP GeoIPClientConfig `yaml:"client_ip"`
    Database GeoIPDBConfig     `yaml:"database"`
}
```

Recommended YAML:

```yaml
access:
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
      source: direct            # direct | x_forwarded_for | forwarded
      trusted_proxies: []
    database:
      source: managed           # managed | local
      edition: dbip-country-lite
      directory: /var/lib/lip/geoip
      local_path: ""             # local source only
      update:
        enabled: true            # managed source only
        interval: 24h
```

Validation:

- omitted GeoIP block => disabled/no process service;
- enabled requires valid `order`;
- country values normalize uppercase and validate against an ISO-3166 alpha-2 set;
- CIDR/exact addresses parse with `net/netip` during static compilation;
- prefixes normalize via `Masked()`;
- forwarded source requires non-empty trusted proxies;
- `managed`/`local` fields are mutually consistent;
- local source rejects all managed updater settings and never requires credentials (DB-IP Lite needs none);
- managed update interval defaults to 24h and is valid only in `[6h,168h]`;
- request-header/hop, download-byte, and operation-timeout limits are fixed shared constants, not YAML knobs in v1;
- credentials are process secrets, not ordinary reloadable YAML.

DB-IP Lite requires no account, API key, or credential; the managed download uses the documented public HTTPS release URL pattern and no secret environment variables. Implementation must record the exact static URL pattern and checksums from the operator documentation.

## Static Compilation vs Runtime Readiness

This is a hard two-phase contract.

### Phase A — exact static validation entry point

The existing command path is normative:

```text
cmd/lipstd.runCheckConfigCommand
  -> runtimebundle.ValidateStructural
      -> static/effective config validation only
```

GeoIP static validation SHALL be invoked from `runtimebundle.ValidateStructural` through a focused pure helper in the config/core layer. It SHALL NOT call `BuildHost`, compile/publish a request generation, construct or activate `ProcessServices`, open/acquire an MMDB, instantiate the managed updater client, or perform external network I/O.

The same pure helper is reused by normal startup/reload candidate preparation to:

1. validate config shape/source-mode consistency;
2. normalize/validate countries;
3. parse/normalize CIDRs/trusted proxies;
4. compile immutable `coregeoip.Policy` and resolver values;
5. determine `NeedsCountryLookup` from the decision plan;
6. classify reload/restart paths.

`check-config` validates configured local-path syntax/source consistency but does not require the referenced MMDB to be opened as a live process resource. Existing structural-validation behavior remains non-listening, non-publishing, and independent of provider network availability.

### Phase B — serving activation readiness

Only normal serving process construction/candidate publication performs live readiness checks:

- if policy disabled: no gate, no lookup requirement;
- if enabled and `NeedsCountryLookup=false`: gate may operate without MMDB;
- if enabled and `NeedsCountryLookup=true`: process-owned lookup must already be provisioned and ready or candidate/startup fails.

Reload candidate compilation must never start/download/reconfigure a process service. There is no hidden compile-purpose global and no duplicated validation implementation.

## Reload Classification

Refactor current broad `classifyAccess` field handling.

| Field | v1 disposition | Reason |
|---|---|---|
| `access.mode` | restart-required | preserve existing deployment posture |
| `access.geoip.enabled` | reloadable | generation wrapper presence |
| `access.geoip.order` | reloadable | immutable policy |
| allow/deny countries | reloadable | immutable policy |
| allow/deny CIDRs | reloadable | immutable policy |
| client IP source | reloadable | immutable resolver |
| trusted proxies | reloadable | immutable resolver |
| database source | restart-required | process service |
| directory/local path | restart-required | process file lifecycle |
| edition | restart-required | reader/updater contract |
| update enabled/interval | restart-required | process scheduler lifecycle |
| credential source | restart-required | process secret/client construction |

Existing mixed-change all-or-nothing rejection remains unchanged.

## Process Service Construction

At normal process startup, branch explicitly by database source.

### Local source

1. run static GeoIP compile/validation;
2. construct the process service only when a local source is configured;
3. open and verify **only** the configured local MMDB path;
4. never instantiate the managed updater, read any credentials, scan managed LKG versions, or make an acquisition/network request;
5. if startup policy needs country lookup and the local DB is not ready, fail normal serving startup.

### Managed source

1. run static GeoIP compile/validation;
2. construct the process service and recover a verified retained LKG if available;
3. if startup policy is enabled + needs country lookup + no LKG is ready, make one bounded managed acquisition attempt;
4. fail normal serving startup if required readiness is still absent;
5. start the periodic updater only when managed updates are enabled.

If enforcement is disabled while database configuration exists, either source may remain provisioned (and managed mode may keep updating) for later pure-policy enable reload. This is process work only; the disabled request path has no wrapper.

## Candidate Generation Composition

During a normal serving candidate build:

1. static policy compilation succeeds using the same pure helper used by `ValidateStructural`;
2. `configreload.Classify` has rejected process-resource changes;
3. serving readiness checks the existing `ProcessServices.GeoIP` only when required;
4. runtimebundle creates a defensive `contract.GeoIPSecurityInput` containing immutable policy/resolver, non-owning `CountryLookup`, and bounded observer;
5. candidate security group carries that projection;
6. `ComposeStandardHTTP` installs `stdhttp/geoip` middleware iff `Policy != nil`/enabled.

No generation owns updater goroutines, files, credentials, MMDB close, or mutable policy state.

## Policy Evaluation Algorithm

Compile countries into private immutable sets and prefixes into private normalized slices.

Conceptual evaluation:

```text
addr = addr.Unmap()
allowCIDR = allow.prefixContains(addr)
denyCIDR  = deny.prefixContains(addr)

if order == deny_allow and allowCIDR:
    allow(cidr_allow)           # final allow phase already matched
if order == allow_deny and denyCIDR:
    deny(cidr_deny)             # final deny phase already matched

if compiled plan proves country cannot affect result:
    decide from CIDR flags + order default

country, found, err = lookup(addr)
if err:
    deny(lookup_error)
allowCountry = found && allow.countryContains(country)
denyCountry  = found && deny.countryContains(country)

allowMatch = allowCIDR || allowCountry
denyMatch  = denyCIDR || denyCountry
apply exact order truth table
```

Reason selection must be deterministic and finite; do not expose literal rule/IP/country values through the reason enum.

## Client-IP Resolution

### Shared forwarding-input contract

For the configured authoritative forwarding header, use `Header.Values` (or equivalent access to every field instance), not `Header.Get`/first-value behavior.

Multiple `X-Forwarded-For` or multiple `Forwarded` field instances are legal list fragments for this resolver: concatenate their values in received field order with a conceptual comma separator and parse them as **one ordered chain**. The 16 KiB byte limit applies to the aggregate selected field values plus separators, and the 32-hop limit applies after flattening all field instances. An attacker cannot evade limits by splitting data across repeated fields.

If both XFF and `Forwarded` are present, only the explicitly configured source is authoritative; the other header is ignored.

### Direct mode

- extract host from `RemoteAddr` using robust host:port handling;
- accept host-only forms used by Go/test servers;
- parse with `netip.ParseAddr`;
- `Unmap()`;
- reject hostname/non-IP values;
- ignore all forwarding headers without parsing them.

### Trusted XFF

If direct peer is untrusted: ignore XFF and use direct peer.

If direct peer is trusted:

1. aggregate all XFF field instances and enforce 16 KiB before unbounded allocation/work;
2. parse at most 32 comma-separated hops across the aggregate chain;
3. reject empty/invalid authoritative elements rather than silently skip them;
4. normalize addresses;
5. walk chain right-to-left, treating direct peer as trusted terminal hop;
6. return first non-trusted address;
7. fail closed if none is unambiguous.

### RFC 7239 `Forwarded`

Implement only robust extraction of the ordered `for=` chain needed for client resolution:

- aggregate every `Forwarded` field instance in received order under the same 16 KiB limit;
- support quoted values and bracketed IPv6;
- respect comma-separated elements and parameter syntax;
- enforce the same 32-hop bound after flattening;
- reject `unknown`/obfuscated/malformed authoritative values when they prevent unambiguous authority;
- do not generalize into unrelated Forwarded metadata processing.

Auth peer attribution remains untouched/direct.

## MMDB Reader Service and Shutdown Lifecycle

Conceptual state:

```go
type Service struct {
    readerMu sync.RWMutex
    active   *readerVersion

    lifecycleMu sync.Mutex
    closed      bool
    updateCtx   context.Context
    cancel      context.CancelFunc
    updateWG    sync.WaitGroup
    // scheduler/status
}
```

Lookup holds `readerMu.RLock` through required field decode. Candidate download/open/Verify happens without the reader writer lock.

### Update registration/publication fence

Every startup acquisition or periodic update is an owned operation:

1. acquire `lifecycleMu`; if `closed`, reject; otherwise `updateWG.Add(1)` and capture the service update context; release;
2. perform download/write/open/Verify under the cancellable operation context;
3. immediately before durable manifest publication, reacquire `lifecycleMu`; if closed/cancelled, abort and retire the candidate;
4. while the publication fence is held, complete the durable manifest commit and the short non-I/O active-reader swap; then release;
5. `defer updateWG.Done()` after all candidate/response resources are retired.

`Close` SHALL:

1. acquire `lifecycleMu`, atomically set `closed=true`, cancel the updater root context, and release the lock;
2. stop the scheduler and wait for `updateWG` without holding `lifecycleMu`;
3. after all update/acquisition operations have returned, acquire the reader writer lock, detach the active reader, and release;
4. close the detached reader/files and finish bounded cleanup.

Therefore no manifest or active-reader publication can begin after closed state is established, and `Close` never closes a reader/file while an update or lookup can still publish/use it. Shutdown races are required tests, not an implementation detail.

`Reader.Close` must never race any operation using that reader.

## Managed DB-IP Lite Update Client and Response Ownership

Download the DB-IP Lite database over public HTTPS from the documented static release URL pattern (`https://download.db-ip.com/free/dbip-country-lite-YYYY-MM.mmdb.gz`) using the standard library `net/http` client with an injected bounded transport/timeout. Do not scrape release artifacts, hard-code ad-hoc URLs beyond the documented pattern, copy protocol logic, or shell out to a subprocess.

The adapter owns every non-nil HTTP response body, including unchanged-response no-op bodies. Ownership rules:

- always close a non-nil body exactly once;
- when the server signals no change (HTTP 304 or a matching precomputed checksum), close the body without treating it as database data;
- when a changed database is returned, gunzip and consume it through the 128 MiB bounded writer, then close it before candidate verification/publication;
- on local write/validation/publication failure, close the response body and every candidate resource; where safe and still within the operation/size bounds, drain to EOF before close so transport reuse is not accidentally defeated;
- on hard byte-limit or context-timeout breach, cancel/close immediately rather than perform an unbounded drain.

Repeated unchanged checks and repeated successful/failed changed updates must show no body/file/goroutine leak.

The published MD5/SHA1 checksum is upstream change detection only, not cryptographic authenticity. Candidate trust comes from the public HTTPS download path plus strict MMDB verification/type validation before durable publication.

Every managed check/acquisition uses a 2-minute context timeout and a 128 MiB maximum database body. Periodic scheduling defaults to 24h with ±10% jitter and accepts only configured intervals from 6h through 168h.

## Transactional Crash-Durable LKG Publication

### Update flow

```mermaid
sequenceDiagram
    participant T as Update Timer
    participant U as GeoIP Updater
    participant M as DB-IP Downloader
    participant FS as Versioned Files/Manifest
    participant R as Active Reader

    T->>U: begin owned update(ctx)
    U->>M: GET(static URL, If-Modified-Since/checksum)
    alt unchanged
        M-->>U: HTTP 304 / matching checksum + body
        U->>U: close body
        U-->>T: unchanged metric
    else changed
        M-->>U: bounded gzipped MMDB body
        U->>FS: bounded write + durable candidate commit
        U->>U: gunzip/consume/close response body
        U->>U: open + Verify + expected Country type
        U->>U: enter lifecycle publication fence; reject if closed
        U->>FS: crash-durable atomic LKG manifest commit
        alt manifest commit fails
            U->>U: close candidate reader; retain old active/LKG
        else manifest committed
            U->>R: short-lock non-I/O active reader/version swap
            U->>U: leave publication fence
            U->>U: close retired reader after pre-swap lookups drained
            U->>FS: GC obsolete retired versions
            U-->>T: updated metric
        end
    end
```

### File layout

Managed directory concept:

```text
geoip/
  active.json
  dbip-country-lite.<hash>.mmdb
  dbip-country-lite.<old>.mmdb
  .download-<random>.tmp
  .active-<random>.tmp
```

### Durable candidate protocol

A candidate version uses a unique final filename; the active mapped database file is never replaced in place.

On Unix-like platforms:

1. write the bounded candidate temp file;
2. `fsync` the candidate data and close it;
3. atomically rename within the managed directory to its unique version filename and `fsync` the parent directory;
4. open/Verify the final version;
5. write the temporary manifest, `fsync` it, close it, atomically replace `active.json` in the same directory, then `fsync` the parent directory.

On Windows:

1. write the bounded candidate temp file, flush it with the platform write-through primitive, and close it before rename/open;
2. publish the unique version filename without replacing an open mapped file;
3. write/flush/close the temporary manifest;
4. replace `active.json` through a narrow file adapter using a same-directory atomic/write-through metadata-replacement primitive (for example `MoveFileExW` with replace-existing/write-through semantics or an equivalent proven primitive).

The implementation SHALL NOT fall back to delete-then-rename, copy-then-delete, in-place manifest truncation, or another non-atomic publication path on any supported platform. If the required atomic/durable primitive is unavailable or fails, publication fails, the previous manifest/LKG stays authoritative, and the candidate is not activated.

Manifest fields are limited to version/edition/checksum/timestamps/path basename; never credentials or arbitrary path traversal.

### Why manifest-before-reader

Manifest publication is the fallible durable commit step. A failure must leave the old active/LKG untouched. The in-memory pointer swap under `readerMu` is deliberately a non-I/O/non-failing commit step after durable selection and while still inside the lifecycle publication fence.

If the process crashes after manifest commit but before in-memory swap, restart validates and loads the committed verified candidate. While live, requests continue using the old in-memory reader until the tiny swap step completes.

### Safe reader swap

1. verified candidate + durable manifest already exist;
2. acquire `readerMu.Lock()` (waits for all prior RLock lookups);
3. replace active pointer;
4. release lock;
5. close retired reader; no lookup can still hold it because writer acquisition drained prior readers and post-swap readers see the new pointer;
6. GC retired file after close.

If implementation uses reference counting/RCU instead, it must prove the same close invariant with race tests. Prefer `RWMutex` until benchmarks show need.

## Restart Recovery

### Managed source

At startup:

1. parse the manifest strictly; require a basename-only target inside the managed directory plus expected edition/checksum metadata;
2. open/Verify the manifest target and validate expected Country semantics/checksum;
3. if manifest is missing/invalid, scan only retained managed version filenames, order candidates by modification time descending with basename lexical order as deterministic tie-break, and select the first version that fully verifies;
4. repair/commit `active.json` through the **same crash-durable manifest protocol** before publishing the recovered reader;
5. if no retained version verifies, remain unready; only an enabled country-dependent managed startup may make the one bounded acquisition attempt, otherwise serving activation that requires lookup fails.

Stale temp files are never candidates for recovery.

### Local source

Local mode does not use the managed manifest/version scan and never performs managed acquisition. It opens only `local_path`; if an enabled country-dependent startup requires lookup and that file is missing/invalid, startup fails.

## Denial Contract

All gate failures/denials use one bounded protocol-agnostic response:

```text
HTTP 403 Forbidden
Content-Type: text/plain (or equivalent existing generic-safe standard)
Body: "Forbidden\n" or similarly generic bounded text
```

Do not reveal IP, country, rule, order, header, proxy chain, database status, or upstream failure. No frontend-specific renderer is invoked because frontend identification is intentionally downstream.

## Observability

Integrate with the existing process metrics bundle/registry; observer is a narrow contract projected into each generation.

Suggested metric semantics (final names follow repository conventions):

- `lip_geoip_decisions_total{decision,reason}`
- `lip_geoip_update_total{result}`
- `lip_geoip_database_ready`
- `lip_geoip_database_age_seconds`

Finite reason classes only, e.g. `cidr_allow`, `cidr_deny`, `country_allow`, `country_deny`, `default_allow`, `default_deny`, `client_ip_error`, `lookup_error`.

No IP/CIDR/header/license-key labels. Country is omitted as a default metric label. Denied hostile requests intentionally do not enter normal access log/general OTel/general HTTP metrics. Per-denial logging is off by default; any diagnostic must be bounded/rate-limited. Updater state transitions/failures use bounded operational logs and metrics with secret redaction.

## Failure Model

| Failure | Behavior |
|---|---|
| invalid static config | reject startup/candidate/check-config |
| malformed direct peer | 403 `client_ip_error` |
| malformed/oversized authoritative forwarded chain | 403 `client_ip_error` |
| country absent | normal no-country match |
| active MMDB lookup/decode error | 403 `lookup_error` |
| normal serving enable without required ready lookup | reject candidate/startup |
| `check-config` without live lookup/network | static validation only; no process resource creation/publication |
| local source missing/invalid when required | fail serving readiness; no managed network fallback |
| managed initial acquisition fails and no LKG | fail enabled country-dependent normal startup |
| periodic update fails with LKG | retain LKG; bounded telemetry |
| corrupt/oversized candidate | reject candidate DB; retain old active/LKG |
| candidate durable flush/rename fails | retain old manifest/active LKG; do not activate candidate |
| manifest durable/atomic replacement fails | retain old manifest/active LKG; do not activate candidate |
| shutdown starts during update | cancel; wait; reject any publication that has not already entered fenced commit |
| panic inside gate | outer recovery contains it |

## Security Considerations

### Trusted address boundary

Forwarding metadata has no authority unless the immediate peer is explicitly trusted. Trust list/source mode reload atomically with policy. Repeated header fields are aggregated under one byte/hop budget; they do not create a limit bypass.

### Secrets

DB-IP Lite requires no account, API key, or credential; there are no database secrets to manage and no secret environment variables. Local mode never performs network acquisition. If a future managed source requires credentials, they SHALL be process secrets, never included in request contexts, manifest, status, metrics, debug summary, or logs.

### GeoIP limitations

Documentation must state that VPN/proxy/relay/mobile networks and database lag can cause false positives/negatives. GeoIP is defense in depth, not identity, citizenship, sanctions, or legal-compliance proof.

### Abuse bounds

- 16 KiB aggregate selected forwarding-header bytes;
- 32 forwarding hops;
- no DNS;
- no request network/filesystem;
- no unbounded per-IP cache;
- no per-denial normal log;
- 2-minute managed operation timeout;
- 128 MiB managed download maximum;
- managed interval `[6h,168h]`, default 24h ±10% jitter;
- strict candidate MMDB validation;
- closed-state publication fence prevents shutdown resurrection.

## Brownfield Compatibility

### Management plane

The process-owned reload management listener remains outside `ComposeStandardHTTP` and is not wrapped. Its existing loopback/dedicated-token trust model remains the recovery path after a bad data-plane policy candidate.

### Generation pinning

New policy applies to newly admitted requests/connections routed through the new generation. Existing SSE/WebSocket/in-flight work remains pinned to its original generation and is not actively revoked.

### Authentication

Auth `PeerIP` remains direct `RemoteAddr`; GeoIP's forwarded resolver is private to the gate.

### Frontends/backends

No DTO/connector changes. Allowed traffic reaches the unchanged downstream handler graph; denied traffic never identifies a frontend.

### No parallel runtime mechanisms

No new file watcher, config reload endpoint, service locator, mutable global policy, or feature stage.

## Testing Strategy

### RED pure policy/config contracts

- both complete order truth tables;
- overlapping country/CIDR; Moscow-office exception;
- unknown country vs lookup error;
- IPv4/IPv6/mapped IPv4;
- exact address host-prefix conversion;
- invalid countries/prefixes/source combinations;
- immutable-policy compile tests proving caller-owned input mutations cannot affect a published policy;
- `NeedsCountryLookup` and no-lookup short circuits;
- reload/restart field classification;
- exact `runCheckConfigCommand -> runtimebundle.ValidateStructural` no-network/no-ProcessServices/no-publication behavior;
- exact normative resource-limit validation.

### Client-IP tests/fuzzing

- direct host:port/IPv6/host-only;
- untrusted peer spoofing XFF/Forwarded;
- trusted one/multi-hop chain;
- attacker-prepended values;
- repeated XFF fields and repeated `Forwarded` fields preserving aggregate order;
- split-across-fields aggregate byte/hop limit breaches;
- quoted/bracketed RFC values;
- unknown/obfuscated/malformed values;
- parser fuzzing for panic/allocation safety.

### Middleware-order integration

Spies must prove denied request never reaches OTel, general HTTP metrics, trace/request-ID, normal access log, auth provider, frontend mux/decode, or runtime/model/DB fake. Also prove outer recovery and global security/server headers still wrap the response.

### MMDB/updater

- local valid/invalid DB and proof local startup never constructs updater/network client;
- managed LKG startup and bounded no-LKG acquisition;
- repeated unchanged update with every response reader closed;
- repeated changed update with reader consumed/closed on success and failures;
- timeout/transport failure;
- oversized/truncated/corrupt candidate;
- candidate data flush/version publication failure;
- manifest temp flush/atomic replacement/parent-directory durability failure;
- reader swap under concurrent lookup with `-race`;
- `Close` racing download, write, Verify, manifest commit boundary, and reader publication;
- no post-close publication/resource resurrection;
- restart manifest validation, deterministic retained-LKG recovery, manifest repair, and no-valid-LKG behavior;
- stale temp/old-version cleanup;
- Unix and Windows durable file-lifecycle implementations.

### Reload

- valid policy atomic change;
- invalid candidate preserves active;
- enable/disable wrapper presence;
- enable without required process lookup fails only in normal serving activation;
- DB/updater changes restart-required;
- `access.mode` behavior preserved;
- in-flight generation remains pinned.

### Performance

Benchmarks:

- disabled baseline (wrapper absent);
- enabled CIDR-only;
- enabled Country MMDB lookup;
- XFF/RFC Forwarded resolution including repeated fields;
- representative prefix scaling.

Do not add trie/cache complexity without benchmark evidence.

## Requirement Traceability

| Requirement | Primary design owner |
|---|---|
| R1 | middleware placement + wrapper omission |
| R2 | core order/evaluator + immutable representation |
| R3 | static `netip` compiler/matcher |
| R4 | CountryLookup semantics |
| R5 | direct resolver |
| R6 | trusted forwarded resolver + repeated-field/aggregate limits |
| R7 | MMDB driven adapter |
| R8 | source-specific process readiness/LKG/provisioning |
| R9 | managed updater + crash-durable publication + close fence |
| R10 | generation/process split + classifier |
| R11 | generic ingress 403 |
| R12 | bounded observer/metrics/log policy |
| R13 | normative hard bounds/races/benchmarks |
| R14 | data/management plane + generation compatibility |
| R15 | config/static compiler/`ValidateStructural`/docs |

## Migration and Delivery

No state/data migration is required. Existing configs without `access.geoip` behave exactly as before: no GeoIP process service, no wrapper, no lookup.

Implementation is TDD-first and must preserve architecture guardrails. The task plan sequences contract tests before production implementation, then process/generation composition, then updater durability/shutdown certification, then race/cross-platform/performance/release gates.