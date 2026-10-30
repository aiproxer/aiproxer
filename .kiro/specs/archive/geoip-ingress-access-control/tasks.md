# Implementation Plan

Implementation is TDD-first. Every production task follows RED → GREEN → refactor. Keep changes localized to the security/config/composition seams identified in `design.md`; do not introduce a new generic extension stage, backend/frontend-specific GeoIP logic, or a parallel reload system.

Each sub-task contains no more than five concrete actions and includes dependency, validation, and requirement traceability.

## Shared V1 Limits

Tests and production code SHALL consume one shared contract for these normative limits rather than duplicating magic numbers:

- aggregate selected forwarding-header bytes: **16 KiB**;
- forwarding hops: **32**;
- managed MMDB download: **128 MiB**;
- one managed acquisition/update operation: **2 minutes**;
- managed update interval: default **24h**, allowed **6h-168h**, periodic jitter **±10%**.

Header/hop/download/operation limits are fixed internal v1 safety constants; only the managed update interval is operator-configurable.

## 1. Freeze Policy, Configuration, and Reload Contracts With RED Tests

- [x] 1.1 Freeze exact `deny_allow` / `allow_deny` semantics
  - Add RED table tests for allow-only, deny-only, both, and neither under both policy orders.
  - Add overlap fixtures where country deny + office allow CIDR produces the expected second-phase override.
  - Freeze deterministic finite decision reasons without literal IP/country/rule payloads.
  - Add no-country versus lookup-error tests with distinct outcomes.
  - _Boundary: core domain tests_
  - _Depends: none_
  - _Validation: `go test ./internal/core/geoip/...`_
  - _Requirements: 2.1-2.7, 4.5-4.7, 11.1-11.2, 12.7_

- [x] 1.2 Freeze IP/CIDR normalization, immutability, and decision-plan contracts
  - Add RED tests for IPv4, IPv6, exact-address host prefixes, `Prefix.Masked()`, and IPv4-mapped IPv6 `Unmap()` behavior.
  - Reject malformed CIDRs/addresses and hostnames without DNS; cover duplicate/overlapping prefixes independent of rule order within a class.
  - Assert a compiled policy defensively owns all country/prefix backing data: mutating caller-owned maps/slices after compile cannot change decisions or race readers.
  - Assert `NeedsCountryLookup`/short-circuit behavior never calls lookup when the compiler proves CIDR/default outcome is final.
  - _Boundary: core domain/compiler tests_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/core/geoip/...`; targeted `go test -race`_
  - _Requirements: 2.7, 3.1-3.7, 7.7-7.8, 8.9, 13.1-13.3_

- [x] 1.3 Freeze GeoIP config model and normative resource validation
  - Add RED YAML/config tests for absent/disabled, valid deny/allow lists, invalid order/country/CIDR, and managed/local source one-of semantics.
  - Require forwarded sources to have non-empty valid trusted-proxy prefixes and local source to reject all managed updater settings.
  - Pin the shared v1 limits: 16 KiB aggregate forwarding input, 32 hops, 128 MiB download, 2m operation timeout, and managed interval default/range/jitter contract.
  - Prove managed mode requires no credentials/account and local mode performs no network acquisition.
  - _Boundary: core config tests_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/core/config/...`_
  - _Requirements: 4.1-4.2, 6.1, 6.5, 9.4-9.5, 13.4, 13.12-13.13, 15.1-15.3_

- [x] 1.4 Freeze reload classification and exact network-free `check-config` path
  - Add RED classifier tests proving `access.mode` stays restart-required, pure GeoIP policy/resolver fields reload, and DB/updater lifecycle fields are restart-required with atomic mixed-change rejection.
  - Extend the existing `runCheckConfigCommand -> runtimebundle.ValidateStructural` contract tests so GeoIP static compile is exercised there and never calls `BuildHost`, constructs `ProcessServices`, publishes a generation, opens/acquires MMDB data, or contacts the database provider.
  - Require normal startup/reload preparation to reuse the same pure GeoIP validation/compiler helper while serving readiness remains a separate gate.
  - Preserve existing unrelated reload and `ValidateStructural` behavior.
  - _Boundary: config reload / structural-validation tests_
  - _Depends: 1.3_
  - _Validation: `go test ./internal/core/configreload/... ./internal/infra/runtimebundle/... ./cmd/lipstd/...`_
  - _Requirements: 10.1-10.9, 14.5, 15.4-15.6_

## 2. Implement the Pure GeoIP Policy Core

- [x] 2.1 Implement `internal/core/geoip` value types and immutable compiler
  - Add `Order`, finite `Reason`, `Decision`, narrow `CountryLookup`, and private rule-class/policy backing state.
  - Compile country codes and `netip.Prefix` rules once; deep-copy/own backing collections and expose no mutable map/slice accessor or mutator after publication.
  - Compute the safe decision plan/`NeedsCountryLookup` from rule semantics.
  - Keep the package free of HTTP, GeoIP database implementation, logger, Prometheus, runtimebundle, and frontend/backend imports.
  - _Boundary: core domain_
  - _Depends: 1.1, 1.2_
  - _Validation: `go test ./internal/core/geoip/...`; architecture import tests_
  - _Requirements: 2.1-2.7, 3.1-3.7, 4.1-4.7, 7.2, 7.7, 8.9_

- [x] 2.2 Implement deterministic policy evaluation
  - Implement CIDR class matching, safe final-phase short circuits, optional country lookup, and exact Apache truth-table resolution.
  - Treat `found=false` as ordinary no-country input and lookup error as typed fail-closed evidence.
  - Normalize request addresses with `Unmap()` before all matches/lookups.
  - Preserve finite decision reasons suitable for bounded metrics and generic denial rendering.
  - _Boundary: core domain_
  - _Depends: 2.1_
  - _Validation: `go test ./internal/core/geoip/...`_
  - _Requirements: 2.1-2.7, 3.4, 4.3-4.7, 7.6-7.8, 11.1, 12.7_

## 3. Build the HTTP Client-Address Security Boundary

- [x] 3.1 Implement direct-peer resolution
  - Add `internal/stdhttp/geoip` direct `RemoteAddr` parsing with host:port, IPv6, and host-only test/server forms.
  - Parse only literal IPs with `netip`, apply `Unmap()`, and reject hostnames/invalid values without DNS.
  - In direct mode ignore XFF/`Forwarded` completely.
  - Keep existing auth `peerIPFromRemoteAddr` behavior untouched.
  - _Boundary: HTTP driving adapter_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/stdhttp/geoip/... ./internal/stdhttp/auth/...`_
  - _Requirements: 5.1-5.5, 14.7_

- [x] 3.2 Implement bounded trusted X-Forwarded-For resolution
  - Read every `X-Forwarded-For` field instance via `Header.Values`/equivalent, flatten in received order, and enforce the shared 16 KiB aggregate limit before unbounded work plus the 32-hop limit after flattening.
  - Ignore XFF authority entirely when the immediate peer is untrusted; otherwise perform strict right-to-left trusted-hop evaluation.
  - Reject malformed/empty/over-limit authoritative chains instead of skipping ambiguity or trusting leftmost input.
  - Cover IPv4/IPv6, attacker-prepended values, multiple trusted proxies, repeated header fields, and split-across-fields limit breaches.
  - _Boundary: HTTP security adapter_
  - _Depends: 3.1_
  - _Validation: `go test ./internal/stdhttp/geoip/...`_
  - _Requirements: 6.1-6.3, 6.5-6.7, 13.4, 13.6_

- [x] 3.3 Implement bounded RFC 7239 `Forwarded` resolution
  - Read every `Forwarded` field instance, flatten in received order under the same aggregate byte/hop limits, and parse only ordered `for=` authority.
  - Support quoted values and bracketed IPv6 with the same trusted-peer/right-to-left model as XFF.
  - Reject malformed/ambiguous/obfuscated/`unknown` authoritative values when they prevent safe resolution; ignore non-selected forwarding header families.
  - Add repeated-field table tests and fuzz targets with panic/allocation/bounds assertions.
  - _Boundary: HTTP security adapter_
  - _Depends: 3.1, 3.2_
  - _Validation: `go test ./internal/stdhttp/geoip/...`; targeted fuzz smoke tests_
  - _Requirements: 6.1-6.8, 13.4, 13.6_

## 4. Implement the Local MMDB Process Service

- [x] 4.1 Add the MMDB `CountryLookup` adapter
  - Add `internal/infra/geoip` using `github.com/oschwald/maxminddb-golang/v2` behind the core `CountryLookup` port.
  - Open/verify an expected Country MMDB and decode only `country.iso_code`; do not fall back to registered-country.
  - Reuse one concurrency-safe active reader rather than opening per request.
  - Add fixtures for found, absent-country, decode/error, incompatible/corrupt database cases.
  - _Boundary: driven infrastructure adapter_
  - _Depends: 2.2_
  - _Validation: `go test ./internal/infra/geoip/...`_
  - _Requirements: 4.3-4.7, 7.1-7.6, 13.1-13.3_

- [x] 4.2 Implement synchronized active-reader publication
  - Add service-owned reader-version state with lookups holding a read lease/`RLock` through decode.
  - Open/Verify candidate readers outside the reader writer lock and swap only through a short non-I/O publication critical section.
  - Prove old readers are never closed while outstanding lookup operations can still use them.
  - Add high-concurrency lookup/swap tests and targeted `go test -race` coverage.
  - _Boundary: process service / concurrency_
  - _Depends: 4.1_
  - _Validation: `go test -race ./internal/infra/geoip/...`_
  - _Requirements: 7.5, 9.9, 13.5, 13.9_

- [x] 4.3 Implement local-source readiness and status
  - Implement operator-owned local MMDB loading that opens only the configured `local_path` and owns no updater/managed LKG scan.
  - Fail normal serving readiness when an enabled country-dependent policy requires a missing/corrupt/incompatible local file.
  - Prove local source never reads any credentials or performs network acquisition, including startup recovery paths.
  - Expose bounded readiness/version/age status with no client data/secrets while `check-config` remains structural only.
  - _Boundary: process service / status_
  - _Depends: 4.1, 1.4_
  - _Validation: `go test ./internal/infra/geoip/... ./internal/infra/runtimebundle/...`_
  - _Requirements: 8.1, 8.5-8.9, 12.5-12.6, 15.4-15.6_

## 5. Implement Managed DB-IP Lite Updates and Last-Known-Good Storage

- [x] 5.1 Implement the DB-IP Lite download client with explicit response-body ownership
  - Download the DB-IP Lite MMDB over public HTTPS from the documented static URL pattern (`https://download.db-ip.com/free/dbip-country-lite-YYYY-MM.mmdb.gz`) using the standard library `net/http` client with an injected bounded transport/timeout; no `geoipupdate` client, no account/key, no process-owned credentials.
  - Use the published checksum or `If-Modified-Since`/HTTP 304 as upstream change detection; treat the checksum only as change detection, not cryptographic authenticity.
  - Own and close **every** non-nil HTTP response body, including unchanged responses (HTTP 304 / matching checksum); changed bodies must be gunzipped and consumed through the bounded writer and closed before verification/publication.
  - Add repeated unchanged/changed/transport-failure/timeout tests proving response bodies and transports/resources are not leaked.
  - _Boundary: driven update adapter_
  - _Depends: 4.1_
  - _Validation: `go test ./internal/infra/geoip/...`_
  - _Requirements: 9.1-9.5, 13.12, 15.3_

- [x] 5.2 Implement bounded versioned download and candidate validation
  - Stream changed responses to same-directory temporary/versioned files with the shared 128 MiB/2m limits; never overwrite the active MMDB in place.
  - On ordinary local write failure, drain only while still within byte/time bounds then close; on hard limit/timeout cancel and close immediately.
  - Flush/close the candidate before open/Verify and validate expected Country semantics before durable publication.
  - Add fault-injection tests for truncated/oversized/corrupt/wrong-type/disk-failed candidates and response-reader cleanup on each exit.
  - _Boundary: process file lifecycle_
  - _Depends: 4.2, 5.1_
  - _Validation: `go test ./internal/infra/geoip/...`_
  - _Requirements: 9.5-9.8, 13.9_

- [x] 5.3 Implement crash-durable manifest-before-reader publication and recovery
  - Add a narrow file adapter implementing the design's Unix `fsync`/same-directory rename/parent-dir `fsync` protocol and Windows atomic/write-through equivalent; prohibit delete/copy non-atomic fallback.
  - Publish a verified unique-version candidate durably, then atomically/durably commit the secret-free LKG manifest before the non-I/O active-reader swap; any durability/publication failure preserves the previous manifest/active LKG.
  - On managed restart, strictly validate the manifest target and deterministically scan retained versions newest-first with the specified tie-break when the manifest is missing/invalid, repairing the manifest through the same durable protocol before activation.
  - Add crash/fault tests for candidate flush/rename, manifest write/flush/replace/parent-dir durability, invalid manifest/target/checksum, deterministic fallback, and no-valid-LKG outcome.
  - _Boundary: process persistence/lifecycle_
  - _Depends: 4.2, 5.2_
  - _Validation: `go test -race ./internal/infra/geoip/...`; Unix + Windows targeted file-adapter tests_
  - _Requirements: 8.4, 9.6-9.11, 13.5, 13.9_

- [x] 5.4 Implement source-specific startup and updater/Close serialization
  - Branch startup explicitly: local loads only `local_path` with zero managed/network/credential activity; managed validates manifest/LKG and may make one bounded acquisition only when enabled country-dependent readiness requires it.
  - Add service closed state, updater root context/cancel, and in-flight update wait group; every acquisition/update registers before work and rejects when closed.
  - Fence durable manifest commit + active-reader publication against `Close`; shutdown sets closed/cancels first, waits active updates without holding the lifecycle lock, then closes active readers/files.
  - Add deterministic race tests for shutdown during download, candidate write, Verify, pre-manifest publication, and active-reader publication, proving no post-close publication or resource resurrection.
  - _Boundary: process lifecycle_
  - _Depends: 5.1-5.3_
  - _Validation: `go test -race ./internal/infra/geoip/...`; `go.uber.org/goleak` where repository conventions use it_
  - _Requirements: 8.2-8.8, 9.1, 9.4, 9.7, 9.9-9.12_

- [x] 5.5 Certify Windows-safe active-file and crash-durability lifecycle
  - Prove no active/mapped MMDB file is overwritten/deleted before reader close; all candidate DBs use unique version names.
  - Test the Windows manifest adapter's atomic/write-through replacement and its failure behavior; no delete-then-rename/copy fallback is permitted.
  - Cover stale temp cleanup, deterministic retained-version recovery, and cleanup after service restart/close without touching a valid active LKG.
  - Run the same publication failure matrix on supported Unix and Windows CI so durability semantics are equivalent even if primitives differ.
  - _Boundary: process file lifecycle / cross-platform_
  - _Depends: 5.3, 5.4_
  - _Validation: Windows CI targeted package tests + standard Unix package tests_
  - _Requirements: 9.8-9.11, 13.9_

## 6. Add Bounded GeoIP Observability

- [x] 6.1 Add process metrics and observer adapter
  - Extend existing metrics bundle/registry with finite-label GeoIP decision/update/readiness/age metrics following repository naming conventions.
  - Implement the narrow `contract.GeoIPObserver` projection used by generation middleware.
  - Prohibit IP, CIDR, raw header, license key, and arbitrary strings as metric labels.
  - Add cardinality tests enumerating the complete allowed decision/update label space.
  - _Boundary: observability adapter_
  - _Depends: 2.2, 4.3_
  - _Validation: `go test ./internal/infra/metrics/... ./internal/infra/geoip/...`_
  - _Requirements: 12.1-12.2, 12.5-12.7_

- [x] 6.2 Add bounded operational logging without denial spam
  - Add only lifecycle/update status logs needed for LKG selected/update failed/recovered/updated with safe bounded fields.
  - Do not add one normal log entry per GeoIP denial and do not route denials through normal access logging.
  - Add tests/guards that database secrets (none for DB-IP Lite) and raw forwarding headers are absent from logs/status/diagnostics.
  - Keep any optional denial security diagnostic disabled or rate-limited by explicit bounded policy.
  - _Boundary: observability/security_
  - _Depends: 5.4, 6.1_
  - _Validation: focused logging/diagnostic tests_
  - _Requirements: 12.3-12.6_

## 7. Compose Process Ownership and Generation Security Projection

- [x] 7.1 Extend the cycle-neutral HTTP security contract
  - Add data-only `GeoIPResolverConfig`, `GeoIPSecurityInput`, and observer contract under `internal/stdhttp/contract` using only stdlib/lower-level core dependencies.
  - Project only source + defensively copied trusted proxies; fixed forwarding bounds remain shared internal constants rather than unconstrained caller-set `int` fields.
  - Prove `runtimebundle` does not import `internal/stdhttp/geoip` or root stdhttp and no import cycle/service locator is introduced.
  - Add compile/architecture guardrail tests for the dependency direction and immutable policy projection.
  - _Boundary: composition contract_
  - _Depends: 2.1, 3.1, 6.1_
  - _Validation: `go test ./internal/stdhttp/contract/... ./internal/archtest/...`_
  - _Requirements: 2.7, 7.2, 10.2, 13.4, 14.5-14.6_

- [x] 7.2 Add GeoIP service to `ProcessServices` with shutdown ownership
  - Construct `internal/infra/geoip.Service` once from startup-fixed DB configuration; permit warm managed provisioning while enforcement is disabled and create no service when no DB source exists.
  - Give generations only non-owning lookup/status capability; they cannot Close/reconfigure the service.
  - Preserve host close ordering so request generations retire before `ProcessServices` closes GeoIP; GeoIP Close then cancels updater work, waits all registered acquisitions/publications, and closes reader/files.
  - Add `ProcessServices` lifecycle tests where shutdown races each controlled updater phase and prove no close-before-use, late publication, goroutine, or file-handle leak.
  - _Boundary: composition root / lifecycle_
  - _Depends: 4.3, 5.4, 6.1_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./internal/infra/geoip/...`_
  - _Requirements: 1.7, 8.1-8.8, 9.9, 10.4, 10.7, 10.9, 14.5_

- [x] 7.3 Compile/project generation-scoped policy while preserving `ValidateStructural`
  - Wire the pure GeoIP static compiler into the existing `runtimebundle.ValidateStructural` path used by `runCheckConfigCommand`; it must not construct `ProcessServices`, `BuildHost`, or publish a generation.
  - Reuse that pure compiler in normal startup/reload preparation, with existing process-service readiness checked only in normal serving activation.
  - Build a defensive `GeoIPSecurityInput` for enabled serving generations and zero/nil projection for disabled generations; reject country-dependent enable when the existing process lookup is not ready.
  - Add tests proving `check-config` never acquires/open/downloads GeoIP data or requires provider network while serving activation still fails closed on missing required readiness.
  - _Boundary: composition root / structural + generation compiler_
  - _Depends: 1.4, 7.1, 7.2_
  - _Validation: `go test ./internal/infra/runtimebundle/... ./cmd/lipstd/...`_
  - _Requirements: 1.3-1.4, 8.1, 8.7-8.9, 10.1-10.9, 15.4-15.6_

## 8. Integrate the Early HTTP Gate

- [x] 8.1 Implement generic GeoIP middleware and 403 renderer
  - Build `internal/stdhttp/geoip` middleware from cycle-neutral input; resolve client IP, evaluate policy, and delegate allowed traffic unchanged.
  - Return one bounded generic 403 for policy denial, client-IP resolution failure, or fail-closed lookup error without frontend-specific rendering.
  - Record only bounded GeoIP observer decisions and reveal no IP/country/rule/proxy/database detail.
  - Add unit tests proving no downstream handler call on denial/error and unchanged pass-through on allow.
  - _Boundary: HTTP driving adapter_
  - _Depends: 2.2, 3.1-3.3, 7.1_
  - _Validation: `go test ./internal/stdhttp/geoip/...`_
  - _Requirements: 5.1-6.8, 11.1-11.5, 12.1-12.4_

- [x] 8.2 Install the gate at the exact standard middleware boundary
  - Modify `stackHTTPHandler` so enabled GeoIP wraps inside outer recovery/global server-security middleware and outside OTel/general Prometheus/request-ID/access-log/auth/routes.
  - Omit the wrapper entirely when `GeoIPSecurityInput.Policy` is nil/disabled.
  - Add order spies proving denied traffic never reaches each expensive/noisy inner layer while security/server headers and recovery remain effective.
  - Preserve the separate management listener and existing `ComposeStandardHTTP` ownership boundary.
  - _Boundary: standard HTTP composition_
  - _Depends: 7.3, 8.1_
  - _Validation: `go test ./internal/stdhttp/...`_
  - _Requirements: 1.1-1.6, 12.3, 13.7, 14.1-14.2, 14.6_

- [x] 8.3 Certify auth/frontend/runtime non-regression
  - Prove allowed direct traffic retains current auth `PeerIP` attribution and forwarded GeoIP mode does not rewrite auth metadata.
  - Prove no official frontend/backend needs GeoIP-specific DTO/connector branches.
  - Verify denied traffic causes zero auth-provider calls, frontend decode, route/runtime/model/DB fake calls, and zero normal access-log/general trace/general HTTP metric events.
  - Run focused standard frontend/auth contract suites for allowed requests.
  - _Boundary: brownfield integration tests_
  - _Depends: 8.2_
  - _Validation: focused `go test ./internal/stdhttp/... ./internal/plugins/frontends/...`_
  - _Requirements: 1.1, 5.4, 13.7, 14.6-14.7_

## 9. Complete Reload, Generation-Pinning, and Management-Plane Certification

- [x] 9.1 Implement explicit GeoIP reload classification
  - Refactor `classifyAccess` at field granularity, keeping `access.mode` restart-required.
  - Mark pure policy/resolver paths reloadable and DB/updater lifecycle paths restart-required exactly as `design.md` specifies.
  - Preserve existing sorted/compacted mixed-candidate failure semantics and bounded restart-required diagnostics.
  - Add regression tests for configs without GeoIP and unrelated access/auth behavior.
  - _Boundary: config reload_
  - _Depends: 1.4, 1.3_
  - _Validation: `go test ./internal/core/configreload/...`_
  - _Requirements: 10.1-10.5, 14.5, 15.1_

- [x] 9.2 Certify runtime enable/disable and immutable generation behavior
  - Add end-to-end reload tests from disabled→enabled→policy changed→disabled with request assertions against each published generation.
  - Prove invalid candidates and enable-without-required-ready-lookup leave active generation unchanged.
  - Prove disabled generations contain no gate/lookup invocation while a pre-provisioned process DB service may continue its own lifecycle.
  - Prove existing in-flight/pinned request or long-lived transport continues on its acquired immutable policy generation and cannot observe later policy backing-state mutation.
  - _Boundary: runtime reload integration_
  - _Depends: 7.3, 8.2, 9.1_
  - _Validation: `go test ./internal/infra/runtimehost/... ./internal/infra/runtimebundle/... ./internal/stdhttp/...`_
  - _Requirements: 1.3-1.7, 2.7, 8.7-8.8, 10.1-10.9, 14.3-14.4_

- [x] 9.3 Certify management-plane recovery isolation
  - Add a test/proof that data-plane GeoIP policy does not wrap the process-owned reload management listener.
  - Preserve existing loopback/dedicated-token management authorization and route ownership.
  - Demonstrate a data-plane deny-all candidate does not remove the existing management recovery path.
  - Document that management-plane GeoIP would require a separate explicit feature/design.
  - _Boundary: management/data-plane integration_
  - _Depends: 8.2, 9.2_
  - _Validation: focused management/reload contract tests_
  - _Requirements: 14.1-14.2_

## 10. Performance, Security, Documentation, and Final Gates

- [x] 10.1 Add security/property/fuzz/race regression suite
  - Add adversarial fixtures for forwarding spoofing/repeated fields/mapped-address bypass/overlapping policies/malformed or oversized inputs/corrupt DBs and unique-source memory pressure.
  - Fuzz forwarding parsers and pure rule compiler/evaluator boundaries with the fixed byte/hop/input limits.
  - Run targeted race tests for lookup/update/reload/**Close** interaction, including each controlled publication phase, and verify no goroutine/resource leak.
  - Add secret/log/metric cardinality assertions for attacker-controlled inputs.
  - _Boundary: cross-cutting security tests_
  - _Depends: 3.3, 5.4, 6.2, 9.2_
  - _Validation: focused fuzz smoke tests; `go test -race` on affected packages_
  - _Requirements: 6.5-6.7, 9.5-9.12, 12.2-12.6, 13.3-13.9_

- [x] 10.2 Benchmark and profile the hot path before optional optimization
  - Benchmark baseline/disabled stack proving wrapper absence, enabled CIDR-only decisions, Country MMDB lookup, XFF/Forwarded resolution including repeated fields, and representative prefix counts.
  - Record allocation/latency results in implementation PR notes or benchmark docs and compare against the existing standard HTTP baseline.
  - Do not add a prefix trie or per-IP cache unless profiles show a material bottleneck and the optimization remains strictly bounded/version-safe.
  - Add performance regression thresholds only where stable enough for the repository's CI environment.
  - _Boundary: performance validation_
  - _Depends: 8.2, 10.1_
  - _Validation: focused `go test -bench` packages + profiling as needed_
  - _Requirements: 7.5, 13.1-13.3, 13.10-13.11_

- [x] 10.3 Add operator/security documentation and examples
  - Document deny-list, allow-list, Russia+office exception, direct peer, trusted XFF/Forwarded, managed update, and local-MMDB examples.
  - Document process-vs-generation reload semantics, warm provisioning while disabled, restart-required DB changes, exact `check-config` structural behavior, and management-plane exclusion.
  - Document DB-IP Lite CC BY 4.0 attribution, licensing/update obligations (no account/key required), multi-replica quota/distribution considerations, crash-durable LKG recovery, and local-source no-network behavior.
  - Warn clearly that GeoIP is approximate defense in depth and not identity/citizenship/sanctions proof.
  - _Boundary: operator/security docs_
  - _Depends: 9.3, 10.2_
  - _Validation: docs examples parsed by config tests where practical_
  - _Requirements: 9.4, 13.12-13.13, 14.1-14.7, 15.7-15.8_

- [x] 10.4 Add DB-IP Lite attribution to documentation
  - Add a clear attribution note (DB-IP Lite, © DB-IP.com / Eris Networks S.A.S., licensed under CC BY 4.0) to the operator/security documentation and to any generated/example configuration or README referenced by the GeoIP feature.
  - State that the database is fetched over public HTTPS with no account/key and that the binary does not bundle it.
  - Add a docs test/guard that the attribution text is present wherever GeoIP database usage is documented.
  - _Boundary: operator/security docs_
  - _Depends: 10.3_
  - _Validation: docs examples parsed by config tests where practical; manual doc review_
  - _Requirements: 13.13_

- [x] 10.5 Run architecture, quality, cross-platform, and release gates
  - Run affected package tests, `go test -race`, config/reload/management integration suites, architecture guardrails, updater-close tests, and Unix/Windows crash-durability file-lifecycle tests.
  - Run repository standard formatting/lint/vet/security/vulnerability/release gates required by `AGENTS.md` and current CI.
  - Verify no GeoIP database client type leaks into core/stdhttp contract, no mutable policy collection crosses generation boundaries, and no backend/frontend compatibility matrix is added.
  - Review final implementation diff for disabled wrapper omission, fail-closed errors, durable LKG retention, bounded telemetry/resources, no secret leakage, no post-close publication, and no management-plane lockout.
  - _Boundary: final certification_
  - _Depends: all prior tasks_
  - _Validation: repository standard release gates + targeted cross-platform CI_
  - _Requirements: 1.1-15.8_
## Completion Status

- [x] All implementation tasks completed and verified on `origin/main` @ `b763a772`
- [x] Task completion gate satisfied: every checkbox is `[x]` (geoip 33/33, non-forwardable 20/20, runtime 24/24, request tranche via typed pipeline)
- [x] Implementation PR(s) merged:
  - PR #408 `feat(geoip): enforce ingress access control with durable DB-IP lifecycle` — head `9b8902d167c36958d718cb856eda86b8a72d2fa1` merged as `3a5f7272b79769e5eb121e1d9dc02aa95e7fac94` on 2026-08-19T22:11:25Z — https://github.com/matdev83/go-llm-interactive-proxy/pull/408
- [x] Required focused tests and architecture gates pass on merged baseline (see PR CI)
- [x] No successor-only removals falsely marked complete; successor scope documented as deferred (see spec project_description)
- [x] `spec.json` updated: `phase=completed`, `completed=true`, `ready_for_implementation=false`, `updated_at=2026-08-23T22:00:00+02:00`
