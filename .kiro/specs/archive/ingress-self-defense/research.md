# Research and Brownfield Gap Analysis

## Summary

- **Feature**: `ingress-self-defense`
- **Source tracker**: GitHub issue #650, `feat(security): Add basic self-defense measures`
- **Repository baseline**: `main` at `0bc2c5519d16e36acd46b6e05d2e41d24d0a28a5`
- **Discovery scope**: minimal high-ROI v1, full brownfield analysis
- **Complexity**: M
- **Risk**: Medium
- **Selected direction**: add a small early standard-HTTP self-defense gate plus one bounded process-owned adaptive state service. Reuse the #387 GeoIP client-address resolver and forwarding trust configuration, observe actual transport-auth outcomes, keep valid authentication usable behind shared IPs, and deliberately defer every external feed/WAF/general scoring concern.

The implementation can remain much smaller than the full idea inventory in #650. The codebase already has the hardest reusable seams: trusted client-address resolution, early ingress middleware placement, immutable generation reload, process-service ownership, transport-auth chaining, bounded HTTP/server limits, and process metrics. The missing work is narrowly the hostile-request classifier, adaptive state, and auth-aware coordination between those existing seams.

## Source Set

### Project sources reviewed

- GitHub issue #650, including the researched recommendations and appended minimal v1 roadmap
- `AGENTS.md`
- `.kiro/AGENTS.md`
- `.kiro/steering/product.md`
- `.kiro/steering/tech.md`
- `.kiro/steering/structure.md`
- `.kiro/steering/testing.md`
- `.kiro/settings/templates/specs/*`
- `.kiro/specs/archive/geoip-ingress-access-control/*`
- `.kiro/specs/archive/authentication-architecture-refactor/*`
- `internal/stdhttp/middleware.go`
- `internal/stdhttp/request_plane.go`
- `internal/stdhttp/mount.go`
- `internal/stdhttp/contract/http_input.go`
- `internal/stdhttp/contract/route_claim.go`
- `internal/stdhttp/contract/route_registry.go`
- `internal/stdhttp/geoip/client_ip.go`
- `internal/stdhttp/geoip/middleware.go`
- `internal/stdhttp/auth/middleware.go`
- `internal/stdhttp/auth/adapter.go`
- `internal/core/config/access_auth_model.go`
- `internal/core/config/geoip.go`
- `internal/core/configreload/policy.go`
- `internal/infra/runtimebundle/candidate_compile.go`
- `internal/infra/runtimebundle/compile_generation.go`
- `internal/infra/runtimebundle/process_services.go`
- `internal/infra/runtimebundle/process_services_types.go`
- `internal/infra/runtimebundle/geoip_process.go`
- `internal/infra/metrics/bundle.go`
- `internal/infra/metrics/geoip_prom.go`
- `internal/archtest/core_ownership.go`

No external service contract is required by the minimal v1. The broader external-feed research in #650 remains intentionally deferred.

## Brownfield Gap Analysis

| Concern | Current repository state | Gap / required disposition |
|---|---|---|
| Client-IP trust | #387 already exposes bounded `ResolveClientIP`, direct-peer default, trusted XFF/Forwarded chains, IPv4-mapped normalization | Reuse exactly; do not create another forwarding parser or independent trust list |
| Early ingress placement | `stackHTTPHandler` already places GeoIP outside general OTel/HTTP metrics/request ID/access log/auth/routes but inside outer recovery/global server policy | Add self-defense immediately inside GeoIP and outside general instrumentation/auth so deterministic probe/quarantine rejections stay cheap |
| Fixed IP/CIDR access | `access.geoip` already supports exact IP/CIDR allow/deny and precedence semantics without requiring country lookup when only CIDRs decide | Do not duplicate hard allow/deny in self-defense; add only adaptive exemptions |
| Auth outcome authority | `stdhttp/auth.Middleware` has the actual provider-chain result and only delegates after the full chain succeeds | Add a narrow internal observer; never infer auth outcome from downstream final HTTP status |
| Credential presence | `PolicyProvider` knows configured header names and handler kind; arbitrary `httpauth.Provider` implementations may not | Add a private conservative probe. Unknown/credential-free provider means “may authenticate”, never early IP deny |
| Process lifecycle | `ProcessServices` owns long-lived process resources and closes after generations retire | Own one bounded in-memory adaptive store here; no persistence or goroutine lifecycle is needed |
| Immutable generations | candidate compilation validates beside active and publishes a complete handler graph | Generation owns immutable self-defense policy; process state is shared/non-owning |
| Reload classification | typed field-by-field reload/restart classification already exists | Request policy fields reload; state capacity/TTL restart because they size process-owned state |
| Disabled fast path | GeoIP omits its wrapper structurally when disabled | Self-defense disabled generation must omit both ingress gate and auth observation work |
| Management recovery | management reload/status listener is intentionally outside `ComposeStandardHTTP` | Preserve this boundary exactly |
| Metrics | process metrics bundle already owns bounded Prometheus collectors | Add only three finite-label/self-defense metrics; no IP/path labels |
| Route ownership | frontends expose normalized `RouteClaim`s validated before mount | Add regression proof that the fixed impossible-path set does not collide with standard routes; do not build a generic reserved-route framework in v1 |
| Existing resource defenses | HTTP timeouts, body caps and decode admission already exist | No duplicate rate/body/server-limit implementation in this spec |

## Brownfield Requirements Repairs

The gap pass changed the initial issue/minimal-roadmap wording in several material ways.

### Repair 1: remove duplicate static self-defense allow/deny CIDRs

The issue roadmap proposed `allow_cidrs` / `deny_cidrs` inside `access.self_defense`. That would duplicate the already implemented `access.geoip` fixed address policy and create a new precedence problem: does a self-defense allow override GeoIP deny, or vice versa?

**Repair:** v1 adds only `adaptive.exempt_cidrs`. Fixed country/IP/CIDR access remains exclusively under existing `access.geoip`. An adaptive exemption cannot override GeoIP denial and cannot make an impossible path valid.

### Repair 2: successful authentication is a reset, not an IP trust cache

The original concept included a recent-success TTL/trusted-IP memory. That creates a second class of long-lived source trust and weakens future detection on recycled/shared addresses.

**Repair:** a successful full provider chain clears the exact address's adaptive hostile entry. It does not create a trusted entry. Later hostile behavior starts fresh and impossible-path rejection remains deterministic.

### Repair 3: quarantine cannot safely suppress every credential-bearing brute-force attempt

The strongest product principle is that a bad actor sharing a public IP must not lock out a legitimate user. Therefore a quarantined address cannot be blindly denied before auth whenever the request might still present a valid credential or use a credential-free/custom auth provider.

**Repair:** early quarantine rejection is permitted only for requests that the active provider chain can positively classify as unable to authenticate as presented. Any unknown/custom/credential-free possibility goes through auth. This means v1 intentionally does **not** claim to protect an expensive remote auth backend from all credential brute-force traffic. It still eliminates deterministic exploit probes and credential-free/no-credential repeat noise, while all invalid-auth traffic remains blocked before frontend/model work by the existing auth chokepoint.

### Repair 4: count only unauthenticated 401 outcomes

Treating every auth-layer failure as hostile would misclassify authorization/entitlement failures and server/provider faults.

**Repair:** adaptive auth evidence is limited to terminal `401` outcomes before any accepted principal. `403`, `5xx`, provider errors, and principal-established chains are not counted.

### Repair 5: no prompt/body or generic query scanning

AI clients legitimately send exploit strings, shell commands, SQL, HTML and security research in prompt bodies. Generic WAF-style body scanning directly conflicts with the low-false-positive goal.

**Repair:** v1 impossible-path matching uses only request-target path structure required by the fixed matcher. Prompt/canonical body content and arbitrary query values are outside scope.

### Repair 6: separate generation policy from process-state sizing

`max_entries` and `state_ttl` control the shared process store, while thresholds/backoff/enabled flags are request policy.

**Repair:** capacity/TTL are startup-fixed/restart-required in v1; enabled/path toggle/failure threshold/window/quarantine durations/exempt CIDRs are immutable-generation reloadable. This preserves the repository's process/generation ownership model.

The repaired requirements pass the requirements gate: criteria are observable and testable, no criterion depends on a threat-feed vendor, database schema, or public API expansion, and the shared-IP safety constraint is explicit rather than implied.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Verdict |
|---|---|---|---|---|
| Full embedded WAF/CRS | Generic request inspection engine | Broad signature surface | High false-positive risk for AI prompts; large dependency/config surface; far beyond v1 | Rejected |
| Feature plugin | Optional self-defense through generic feature planes | Existing feature host machinery | Too late for cheapest ingress rejection; auth/HTTP-specific; unnecessary platform abstraction | Rejected |
| New core ingress-defense domain + stdhttp adapter | Put provider/protocol-neutral quarantine state machine under `internal/core/ingressdefense`; HTTP adaptation remains in stdhttp | Correct policy ownership; directly analogous to existing core GeoIP kernel policy | Requires one core-ownership census/architecture-test update | **Selected** |
| Pre-auth IP blacklist | Fail2Ban-style unconditional quarantine before auth | Very cheap blocked requests | Violates shared NAT/VPN safety principle | Rejected |
| Put adaptive policy/state in infrastructure | HTTP matcher in stdhttp, bounded state machine under `internal/infra`, immutable config in core config | Avoids a new core package | Makes infrastructure own product security policy, contrary to steering | Rejected |

## Design Decisions

### Decision: reuse `access.geoip.client_ip` as the v1 address-trust source

- **Context**: self-defense needs canonical source IP even when country filtering is disabled.
- **Alternatives considered**: duplicate self-defense client-IP config; migrate both features to a new shared `access.client_ip`; reuse current GeoIP resolver config.
- **Selected approach**: compile/reuse `GeoIPClientConfig` and `geoip.ResolveClientIP` for self-defense regardless of whether the GeoIP country/CIDR policy wrapper is active.
- **Rationale**: zero new trust boundary and smallest change. A future config cleanup may rename/extract the shared concept, but that is not required for v1 correctness.
- **Trade-off**: the operator-facing config name is GeoIP-flavored even though another ingress feature consumes it.

### Decision: self-defense policy is immutable-generation data; adaptive state is process-owned

Request policy consists of enabled flag, impossible-path toggle, auth failure threshold/window, initial/max quarantine and adaptive-exempt CIDRs. The process service owns capacity, TTL and mutable entries. Generation handlers borrow the state service; they never close or resize it.

This mirrors the already-proven GeoIP split between generation policy and process service without importing its MMDB/updater complexity.

### Decision: no state goroutine or timer

Expiration is lazy on lookup/mutation plus bounded eviction at capacity. No per-entry or sweeper goroutine is required for the v1 contract. This avoids lifecycle work and makes restart/close trivial.

### Decision: strong probe hit is one offense

A matched impossible path is deterministic request rejection and contributes one offense transition immediately. It does not require an arbitrary numeric scoring system. Repeated offenses reuse the same exponential quarantine mechanism as thresholded auth-failure windows.

### Decision: authentication observer lives at the real provider-chain chokepoint

`stdhttp/auth.Middleware` already knows whether a principal was accepted, whether a later provider rejected, and the terminal response status. Add an optional internal self-defense hook there rather than wrapping the final `ResponseWriter` and guessing from HTTP output.

Success is recorded only after the full provider chain completes and immediately before delegating to the route mux. A qualifying auth failure is recorded only on terminal 401 with no prior principal.

### Decision: conservative early credential probe is private and fail-open-to-auth

Introduce a private optional interface in the stdhttp auth integration, analogous to the existing private auth-success matcher seam. It returns only whether the provider chain can prove that this request cannot authenticate as presented.

- local API-key policy with no configured API-key header value may report definitely-no-credential;
- credential-bearing local API-key requests report may-authenticate;
- local no-op, remote/custom providers, and providers without the interface report may-authenticate/unknown;
- chain result is definitely-no-credential only when every relevant provider supports that conclusion.

No secret is copied into self-defense state or diagnostics.

### Decision: impossible-path matcher stays small and static

The matcher owns a short audited exact/prefix/traversal set such as `.env`, `.git`, WordPress, phpMyAdmin/Adminer, PHPUnit and selected obvious CGI probes. It does not become a regex DSL, signature downloader, CVE database, or Nuclei interpreter.

The implementation must normalize safely and regression-test against all standard frontend route claims. The list can be extended in later ordinary maintenance after a false-positive review.

### Decision: fixed hard network policy remains GeoIP

Self-defense does not reimplement IP/CIDR deny/allow. Existing GeoIP executes first when both gates are enabled. `adaptive.exempt_cidrs` prevents only adaptive state mutation/quarantine and never overrides hard denial or impossible-path rejection.

### Decision: response classes are 404 for impossible paths and 429 for proven early quarantine rejection

A generic 404 hides the presence of the honeypath detector. A generic 429 accurately represents temporary adaptive refusal. Neither response includes rule/source/penalty details. Existing auth responses remain unchanged when a request is allowed to reach authentication.

## Default V1 Policy

These are implementation defaults, not protocol constants:

| Setting | Default |
|---|---:|
| `enabled` | `true` |
| `impossible_paths` | `true` |
| `adaptive.auth_failures` | `5` |
| `adaptive.window` | `1m` |
| `adaptive.initial_quarantine` | `1m` |
| `adaptive.max_quarantine` | `2h` |
| `adaptive.state_ttl` | `24h` |
| `adaptive.max_entries` | `100000` |
| `adaptive.exempt_cidrs` | empty |

Validation should keep values finite and operationally sane. Recommended v1 validation bounds: auth failures `2..100`; window `1s..1h`; initial quarantine `1s..1h`; max quarantine `initial..24h`; state TTL `1m..7d`; max entries `1024..1_000_000`. These bounds prevent accidental zero/unbounded behavior while retaining operator flexibility.

## Adaptive State Model

A source entry needs only:

- normalized `netip.Addr` key;
- current failure-window start and failure count;
- offense level;
- quarantine deadline;
- last hostile activity timestamp.

No successful-auth timestamp is stored because success deletes/reset the hostile entry. No raw path is retained because impossible-path matches are a finite reason, not a per-path history.

Quarantine duration is deterministic exponential backoff:

`min(initial_quarantine * 2^(offense_level-1), max_quarantine)`

The implementation must saturate safely rather than overflow `time.Duration`.

## Middleware Ordering Decision

Target outer-to-inner order:

```text
SecurityHeaders
  -> DownstreamServerMiddleware
  -> outerRecovery
  -> GeoIP (if enabled)
  -> SelfDefense ingress gate (if enabled)
  -> OTel HTTP (optional)
  -> general Prometheus HTTP (optional)
  -> trace/request ID
  -> access log
  -> inner recovery
  -> transport auth + SelfDefense outcome observer (if enabled)
  -> route mux/frontend/runtime
```

The self-defense gate attaches the already-resolved normalized source address to request context for its paired auth observer so forwarding headers are parsed once per request. GeoIP remains outside and therefore wins on hard denial.

## Design Validation

### Validation concern 1: runtimebundle must not import concrete stdhttp implementation

**Risk:** a process state type or matcher owned directly by `internal/stdhttp/selfdefense` could force runtimebundle to import root stdhttp and violate the existing composition direction.

**Repair:** put the provider/protocol-neutral policy and bounded mutable state machine in a small `internal/core/ingressdefense` package; carry only a narrow lifecycle-free projection through `internal/stdhttp/contract`; keep path matching, responses and request-context adaptation in `internal/stdhttp/selfdefense`. Runtimebundle imports core/contract, never concrete root stdhttp.

**Verdict:** resolved.

### Validation concern 2: core ownership must satisfy the repository kernel test

**Risk:** adding a top-level core package without explicit admission would violate `internal/archtest/core_ownership.go`, while moving the state machine into infrastructure would make infrastructure own product security policy.

**Repair:** classify `internal/core/ingressdefense` as a kernel invariant: it is provider/protocol neutral, remains required in the default standard distribution when optional feature plugins are absent, imports no HTTP/provider SDK, and owns only bounded source-defense state transitions. Add the corresponding architecture-test rationale as part of implementation. YAML decoding/normalization remains in existing `internal/core/config`. No public SDK contract is added.

**Verdict:** resolved.

### Validation concern 3: existing GeoIP resolver projection is currently tied to active GeoIP policy

**Risk:** `buildStandardHTTPInput` currently constructs `GeoIPSecurityInput` only when `cand.security.geoip.Policy()` is non-nil. Self-defense still needs the same resolver configuration when GeoIP enforcement is disabled.

**Repair:** candidate compilation continues compiling `GeoIPConfig`; standard HTTP projection separately supplies the already-compiled resolver source/trusted prefixes to self-defense whenever self-defense is enabled. It does not require a GeoIP country database or GeoIP policy wrapper.

**Verdict:** resolved.

### Validation concern 4: dynamic quarantine can become a shared-IP lockout

**Risk:** a straightforward pre-auth `if quarantined { 429 }` violates the strongest product requirement.

**Repair:** use the conservative provider-chain credential probe. Unknown or potentially successful auth always reaches the normal auth chain. Successful full auth clears state. Early 429 is only allowed for proven no-credential/no-credential-free-auth cases.

**Verdict:** resolved, with the explicit trade-off that arbitrary credential-bearing brute-force can still reach auth.

### Validation concern 5: duplicate hard CIDR policy creates contradictory precedence

**Risk:** adding `self_defense.allow_cidrs/deny_cidrs` would overlap `access.geoip` and create two network policy authorities.

**Repair:** remove them from v1. Keep only adaptive exemptions; fixed hard policy remains GeoIP.

**Verdict:** resolved.

### Validation concern 6: process state and reloadable state policy can drift

**Risk:** changing state capacity/TTL through generation reload cannot atomically resize/redefine a process-owned store without extra lifecycle machinery.

**Repair:** classify `max_entries` and `state_ttl` as restart-required; all pure request-policy fields are reloadable. Process state survives policy reload and is cleared only on auth success, TTL/eviction, or process shutdown.

**Verdict:** resolved.

### Validation concern 7: disabled configuration must remain a real fast path

**Risk:** always wrapping auth/middleware with no-op checks adds permanent hot-path cost.

**Repair:** the generation compiler projects nil/disabled self-defense input; `stackHTTPHandler` omits the ingress wrapper and calls the existing auth middleware without observer/probe behavior. The process state holder may exist as an empty lazily allocated process resource so runtime enablement remains possible, but disabled requests perform zero feature work.

**Verdict:** resolved.

### Validation concern 8: route false positives

**Risk:** a future change to a standard frontend could claim one of the built-in impossible paths.

**Repair:** add a generation/standard-registration regression that enumerates current standard frontend route claims against the fixed matcher. Do not build a generic reserved-route framework in v1; if a legitimate collision appears, the matcher set must be repaired before merge.

**Verdict:** resolved for the standard distribution scope defined by this spec.

### Design validation verdict

**GO.** The repaired design reuses existing ingress trust, auth and lifecycle chokepoints; preserves management recovery and immutable generation semantics; avoids duplicate hard IP policy and public SDK expansion; bounds all attacker-keyed state; and makes the unavoidable shared-IP/auth trade-off explicit rather than claiming impossible zero-cost pre-auth blocking.

## Risks and Mitigations

- **Shared-IP false positives** — valid/unknown authentication is always allowed to run; successful auth clears adaptive state.
- **Memory DoS by unique IP churn** — hard entry cap, bounded entry shape, lazy expiry/eviction, no path strings.
- **Lock contention at high concurrency** — sharded or equivalently bounded fine-grained state synchronization; no lock held across auth/frontend/network work.
- **Forwarding spoof** — reuse the proven #387 resolver exactly.
- **Security oracle** — generic 404/429; no exact penalty/rule details; normal auth semantics preserved.
- **False positive from AI content** — no body/canonical content inspection.
- **Config/lifecycle complexity creep** — capacity/TTL startup-fixed, no persistence/feed/updater subsystem.

## Implementation Risk and Effort

- **Effort: M (roughly 3-5 focused contributor days)** — small algorithm, but it crosses config/reload, process ownership, stdhttp ordering, transport-auth observation, metrics and adversarial tests.
- **Risk: Medium** — the main risks are shared-IP safety, auth-chain semantics and bounded concurrent state rather than algorithmic complexity.

## Revalidation Items at Implementation Time

1. Reconfirm the exact `stackHTTPHandler` order and GeoIP wrapper location on implementation-time `main`.
2. Reconfirm `httpauth.Provider` chain semantics and `PolicyProvider` handler-kind/header access before adding the private credential probe.
3. Reconfirm the current standard frontend route-claim inventory before freezing the built-in impossible-path set.
4. Reconfirm GeoIP client-IP resolver behavior and bounds; reuse rather than fork it.
5. If concurrent performance work changes ProcessServices or metrics composition before implementation, preserve the ownership invariants rather than stale file names.

## References

- GitHub issue #650 — product scope, research and minimal implementation roadmap
- `.kiro/specs/archive/geoip-ingress-access-control/` — existing early-ingress/client-IP/reload/process-lifecycle precedent
- `.kiro/specs/archive/authentication-architecture-refactor/` — transport-auth/provider-chain ownership precedent
- `.kiro/steering/structure.md` — package ownership and dependency direction
- `.kiro/steering/testing.md` — TDD, fake-clock and lifecycle/concurrency evidence rules