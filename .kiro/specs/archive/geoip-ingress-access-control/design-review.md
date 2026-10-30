# Design Validation Review

## Review Method

The design was validated as a brownfield HTTP-security/process-lifecycle change against:

- root and `.kiro` repository guidance;
- Go-LIP `main` at `ca43dde919f4d53716a98bf53ffb57bd61560607`;
- `internal/stdhttp/middleware.go` and normal access logging;
- `internal/stdhttp/request_plane.go` / `internal/stdhttp/contract` dependency direction;
- auth peer attribution (`RemoteAddr`, no forwarded-header trust);
- immutable generation compilation and atomic runtime reload (ADR 0008);
- `ProcessServices` ownership and close semantics;
- current `configreload.Classify` typed field classification;
- `cmd/lipstd.runCheckConfigCommand -> runtimebundle.ValidateStructural` structural-validation boundary;
- process-owned management listener separation;
- current metrics composition/registry model;
- all reconciled requirements and `gap-analysis.md`;
- DB-IP Lite / GeoIP database lifecycle constraints documented in `research.md`.

The review treats security bypass, fail-open behavior, mutable published policy, import cycles, hidden request-path/network I/O, non-durable LKG activation, publication-after-close, and broken reload/management recovery semantics as NO-GO findings.

A second review-hardening pass was performed after CodeRabbit's PR review. The review comments are treated as untrusted findings and were independently checked against the actual spec/repository boundaries before amendment.

## Round 1: Middleware Placement and Scope

### Assessment

**Decision: PASS**

The proposed position is the earliest useful request-handler boundary that still preserves global security headers/server policy and outer panic containment.

It correctly bypasses on denial:

- general OTel HTTP instrumentation;
- general Prometheus HTTP middleware;
- request/trace ID setup;
- normal access log;
- transport auth;
- frontend parsing/routes;
- runtime/routing/DB/model work.

The design also correctly limits v1 to the standard data-plane generation. The separate process-owned reload management listener remains outside the gate.

### Guardrail

An implementation that wraps the process-level management server/common listener instead of the canonical standard data-plane generation must return to design review.

## Round 2: HTTP Contract Dependency Direction

### Initial assessment

**Decision: NO-GO pending clarification**

The initial design left open whether `runtimebundle` might import the concrete HTTP GeoIP adapter to carry resolver configuration.

### Resolution

The final design makes the composition contract exact:

- `internal/core/geoip` owns `Policy`, `CountryLookup`, decision/reason values;
- `internal/stdhttp/contract` owns a cycle-neutral data-only `GeoIPSecurityInput` and resolver source/trusted-prefix projection;
- `internal/stdhttp/geoip` consumes that contract and owns HTTP parsing/middleware;
- `runtimebundle` projects process lookup + compiled generation policy without importing `internal/stdhttp/geoip`.

Fixed abuse bounds are shared lower-level constants rather than arbitrary `int` fields passed through the contract.

**Final decision: PASS.**

## Round 3: `check-config` vs Live Readiness

### Initial assessment

**Decision: NO-GO pending clarification**

A generic "static mode" or generation compiler flag would be too ambiguous because existing `check-config` already has a concrete non-public structural path.

### Brownfield evidence

`cmd/lipstd.runCheckConfigCommand` calls `runtimebundle.ValidateStructural`. The existing `ValidateStructural` contract explicitly excludes process services, generation compile/publication, plugin activation, runtime tracing, and provider network I/O.

### Resolution

The final design makes that existing path normative:

```text
runCheckConfigCommand
  -> runtimebundle.ValidateStructural
      -> focused pure GeoIP static validation/compiler helper
```

GeoIP static validation on this path may parse/configure pure values only. It cannot:

- call `BuildHost`;
- construct/activate `ProcessServices`;
- compile/publish a request generation;
- open/acquire a live GeoIP database;
- instantiate or call the managed updater;
- depend on provider network availability.

Normal serving startup/reload preparation reuses the same pure helper and adds a separate activation-readiness gate over already process-owned resources.

Tasks 1.4 and 7.3 now test this exact entry point.

**Final decision: PASS.**

## Round 4: Policy Immutability

### Post-review assessment

**Decision: NO-GO until representation was hardened**

The earlier conceptual `RuleClass` exposed an exported `map` and `[]netip.Prefix`. A comment saying "immutable after compile" is not sufficient in Go: callers retaining or receiving those collections could mutate a published generation or introduce a race.

### Resolution

The final design requires:

- unexported rule-class and policy backing collections;
- compiler-owned deep copies of all input maps/slices;
- no mutator and no accessor returning mutable backing memory;
- scalar/read-only methods such as `NeedsCountryLookup` and `Evaluate` only.

Tasks 1.2, 2.1, 7.1, and 9.2 include immutability/race certification.

**Final decision: PASS.**

## Round 5: Client-IP Trust and Resource Bounds

### Assessment

**Decision: PASS after hardening**

Direct-peer default and explicit trusted-proxy recursion remain appropriate. The second pass made two previously implicit security contracts normative.

### Fixed V1 limits

One shared contract defines:

- 16 KiB aggregate selected forwarding-header bytes;
- 32 forwarding hops;
- 128 MiB managed database download;
- 2-minute managed acquisition/update operation timeout;
- update interval default 24h, accepted 6h-168h, periodic jitter ±10%.

Header/hop/download/operation limits are fixed internal v1 constants; only the update interval is operator-configurable.

### Repeated forwarding-header fields

Using only a first/header-get value is ambiguous and can bypass bounds. The final contract uses every selected header field instance, preserves received field order, and treats repeated instances as one flattened list. Byte and hop limits apply across the aggregate. The non-selected forwarding-header family is ignored.

Tasks 3.2, 3.3, and 10.1 include repeated-field and split-across-fields limit tests/fuzzing.

### Security invariants retained

- forwarded headers ignored for an untrusted direct peer;
- right-to-left trust evaluation;
- no leftmost-XFF shortcut;
- malformed authoritative chain fails closed;
- IPv4-mapped IPv6 normalized;
- auth attribution remains direct-peer based.

**Final decision: PASS.**

## Round 6: MMDB Publication Transaction and Crash Durability

### Initial assessment

**Decision: NO-GO pending ordering fix**

The first draft listed reader swap before LKG manifest persistence, contradicting the LKG failure contract.

### First resolution

Manifest commit was moved before the in-memory reader swap.

### Post-review finding

**Decision: NO-GO until durability semantics were made normative**

"Atomic replacement where supported" still allowed unsafe implementations. A process crash or platform-specific replacement fallback could leave a torn/absent manifest after the candidate had been considered durable.

### Final resolution

The design now requires a narrow platform file adapter and an explicit crash-durability protocol.

On Unix-like systems:

1. bounded candidate write;
2. candidate `fsync` + close;
3. same-directory unique-version rename + parent-directory `fsync`;
4. verify the final version;
5. manifest temp write + `fsync` + close;
6. same-directory atomic manifest replacement + parent-directory `fsync`.

On Windows:

- candidate/manifest writes are flushed with platform write-through semantics and closed before publication;
- the active mapped DB is never overwritten;
- `active.json` uses a same-directory atomic/write-through metadata-replacement primitive behind the file adapter;
- delete-then-rename, copy-delete, and in-place truncation are forbidden fallbacks.

If any required durability/atomic primitive fails, the old manifest and active LKG remain authoritative and the candidate is not activated.

### Restart recovery

Managed restart now:

- strictly validates the manifest target/edition/checksum/basename;
- if missing/invalid, scans only retained managed version files;
- orders by modification time descending with basename lexical tie-break;
- selects the first fully verified candidate;
- repairs the manifest through the same durable protocol before reader publication;
- remains unready when no candidate verifies, allowing only the separately bounded managed startup acquisition when policy requires it.

Local mode never participates in managed manifest scanning/acquisition.

Tasks 5.3 and 5.5 carry the full Unix/Windows fault matrix.

**Final decision: PASS.**

## Round 7: Reader Concurrency and Updater-vs-Close Safety

### Initial reader assessment

The short reader `RWMutex` design remains sufficient:

- lookup holds `RLock` through MMDB decode;
- update download/open/Verify does not hold the writer lock;
- reader publication acquires `Lock`, draining earlier lookups;
- post-swap lookups cannot acquire the retired reader.

### Post-review lifecycle finding

**Decision: NO-GO until process shutdown was serialized with updater publication**

The earlier design said the scheduler stops through `ProcessServices.Close` but did not prevent an already-running update from verifying/publishing after close started, potentially resurrecting readers/files after shutdown.

### Resolution

The final service contract contains:

- lifecycle mutex + `closed` state;
- process-owned updater root context/cancel;
- in-flight update wait group;
- registration of every startup acquisition/periodic update before work starts;
- a lifecycle publication fence immediately before manifest commit;
- fenced durable manifest commit + non-I/O active-reader swap;
- `Close` sets closed/cancels first, releases the lifecycle lock, waits updates, then detaches/closes the active reader.

`Close` never waits while holding the lifecycle mutex. Any update that has not entered fenced commit when closed is established must abort; no late manifest/reader publication can occur afterward.

Tasks 5.4, 7.2, and 10.1 test shutdown during download, write, Verify, pre-manifest publication, and reader publication.

**Final decision: PASS.**

## Round 8: DB-IP Lite Download Response Ownership

### Post-review assessment

**Decision: NO-GO until ownership was explicit**

The updater abstraction did not state who closes a non-nil HTTP response body, especially for unchanged responses. Repeated update checks could therefore leak bodies/transports/resources.

### Resolution

The adapter owns every non-nil HTTP response body:

- unchanged response: close exactly once;
- changed response: consume through the bounded writer to EOF and close before verify/publication;
- ordinary local failure: drain only while still within size/time bounds, then close;
- hard limit/timeout: cancel/close immediately, never unbounded drain;
- all candidate files/readers are retired on every failure path.

Tasks 5.1 and 5.2 add repeated unchanged/changed and failure-path ownership tests.

**Final decision: PASS.**

## Round 9: Source-Specific Startup and Network Isolation

### Post-review assessment

**Decision: PASS after task/design clarification**

The requirements already distinguish managed and local sources, but task 5.4 could be read as applying LKG recovery/acquisition generically.

The final design/tasks explicitly branch:

- **local:** open only configured `local_path`; no managed manifest scan, credentials, updater construction, or network acquisition;
- **managed:** validate/recover managed LKG; make one bounded startup acquisition only when enabled country-dependent readiness requires it; schedule updates only when configured.

This prevents an implementation agent from turning a local-file deployment into an unexpected network client.

## Round 10: Reload/Process Ownership

### Assessment

**Decision: PASS**

The policy/process split is aligned with ADR 0008 and current `ProcessServices`:

- pure policy fields reloadable;
- database/updater lifecycle fields restart-required;
- generation owns no updater goroutine/file/reader close;
- process service may stay warm while enforcement is disabled;
- enabling a country-dependent generation requires already provisioned readiness;
- mixed candidate changes reject atomically.

The design identifies the brownfield `classifyAccess` trap: the current broad restart classification must be split by field path so adding `GeoIP` does not silently make policy reload impossible.

## Round 11: Policy Correctness and Lookup Short-Circuiting

### Assessment

**Decision: PASS**

The explicit Apache truth table is testable and does not depend on rule ordering within a class.

Safe final-phase short circuits remain:

- `deny_allow` + allow-CIDR match may allow immediately;
- `allow_deny` + deny-CIDR match may deny immediately;
- country lookup may be skipped only when the compiler proves it cannot affect the result.

`NeedsCountryLookup` is a property of the immutable compiled decision plan, not a mutable request heuristic.

## Round 12: Cache and Resource-Abuse Model

### Assessment

**Decision: PASS**

Not requiring a per-IP cache remains the safer design. One persistent local MMDB reader removes backend/file-open repetition. An attacker-keyed cache would add memory-DoS and DB-version invalidation complexity.

Any future cache must be bounded and benchmark-justified.

## Round 13: Observability and Denial Rendering

### Assessment

**Decision: PASS**

Denied hostile traffic intentionally bypasses normal access logging/general request spans and is represented by dedicated finite-label GeoIP metrics.

Generic 403 is correct at this early protocol-agnostic boundary. Pulling frontend error renderers upward would couple the security gate to routing/DTO concerns and defeat early rejection.

## SOLID Review

**Decision: PASS**

- **Single Responsibility:** policy, HTTP resolver, database lifecycle, config, metrics, and composition are distinct.
- **Open/Closed:** alternate `CountryLookup` adapters/new deliberate client-IP sources can be added without changing policy semantics.
- **Liskov:** fake/local/MMDB lookup implementations share found/not-found/error semantics.
- **Interface Segregation:** request gate sees policy/lookup/resolver/observer, not updater/files/credentials.
- **Dependency Inversion:** core owns ports; DB-IP/GeoIP database stays infrastructure-only.

## Hexagonal Architecture Review

**Decision: PASS.**

- domain: policy/value objects;
- driving adapter: HTTP request/client-address resolver;
- driven adapter: MMDB/database update/files;
- composition root: runtimebundle/process services;
- cycle-neutral `stdhttp/contract` DTO;
- no global locator;
- no backend/frontend pairwise implementation.

## Requirements Traceability Review

**Decision: PASS.**

Every Requirement 1-15 has at least one design owner and planned verification category. Review hardening does not change product scope; it makes existing security/lifecycle requirements deterministic enough to implement and test.

## Required Design Amendments — Final Status

Original brownfield validation amendments:

1. cycle-neutral `GeoIPSecurityInput` — **DONE**;
2. static validation vs serving readiness — **DONE**;
3. manifest-before-reader publication — **DONE**.

Post-review hardening amendments:

4. enforceable immutable policy backing state — **DONE**;
5. fixed resource constants + repeated-header semantics — **DONE**;
6. crash-durable Unix/Windows publication + deterministic restart recovery — **DONE**;
7. exact `runtimebundle.ValidateStructural` check-config integration — **DONE**;
8. updater/Close publication fence — **DONE**;
9. explicit response-body ownership — **DONE**;
10. local-vs-managed startup isolation — **DONE**.

## Final Validation Decision

**PASS FOR MAINTAINER REVIEW.**

No unresolved architectural blocker identified by either brownfield validation or the subsequent review-hardening pass remains in the normative design/task plan. This is **not** maintainer approval to implement: `spec.json` intentionally keeps approvals and `ready_for_implementation` false.