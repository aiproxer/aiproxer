# Brownfield Requirements Gap Analysis

## Scope and Baseline

This analysis validates the initial `requirements.md` against the Go-LIP brownfield architecture at `main` commit `ca43dde919f4d53716a98bf53ffb57bd61560607` and against issue #387's stated behavior.

Reviewed seams include:

- `internal/stdhttp/middleware.go`
- `internal/stdhttp/accesslog.go`
- `internal/stdhttp/auth/peer_ip.go`
- `internal/stdhttp/auth/adapter.go`
- `internal/stdhttp/request_plane.go`
- `internal/stdhttp/contract/http_input.go`
- `internal/core/config/model.go`
- `internal/core/config/access_auth_model.go`
- `internal/core/configreload/policy.go`
- `internal/infra/runtimebundle/candidate_compile.go`
- `internal/infra/runtimebundle/process_services_types.go`
- `docs/adr/0006-stage-four-extension-seam-map-and-migration.md`
- `docs/adr/0008-versioned-runtime-config-reload.md`
- `pkg/lipsdk/feature/stages.go`
- existing metrics/reload tests and composition guardrails.

## Executive Result

**Initial requirements are directionally correct but need brownfield-specific amendments before design.** No architectural blocker was found. The existing request-generation/process-service split is unusually well suited to the feature, but five contracts were under-specified:

1. the dedicated management listener must not accidentally inherit data-plane GeoIP policy;
2. in-flight long-lived transports must preserve existing generation pinning rather than be retroactively disconnected;
3. `enabled: false` must distinguish zero request-side work from optional pre-provisioned process-side database maintenance;
4. `check-config` must remain network-independent;
5. the initial cache wording must explicitly state that reader reuse/local MMDB satisfies the anti-backend-spam intent without forcing an attacker-keyed cache.

These findings are incorporated into the reconciled requirements revision after this analysis.

## Current-State Inventory

| Concern | Brownfield state | Gap / required disposition |
|---|---|---|
| HTTP middleware order | `stackHTTPHandler` places security/server wrappers outside recovery, then tracing/metrics/request-id/access-log/auth/routes | Add one explicit ingress wrapper between outer recovery and general tracing; do not reuse late LLM admission stage |
| Auth peer identity | auth intentionally derives peer from `RemoteAddr`; forwarded headers are explicitly not trusted | GeoIP may have its own opt-in trusted-proxy resolver; must not mutate auth semantics |
| Runtime reload | complete immutable request generation compiled beside active and atomically swapped | Pure GeoIP policy fits generation; classify every field explicitly |
| Process services | long-lived stores/metrics/etc. survive generation reload | MMDB/updater belongs here; generation must only hold a non-owning country-lookup capability |
| Management reload listener | process-owned and intentionally outside `ComposeStandardHTTP` | Data-plane GeoIP gate must not silently wrap/lock out management recovery path |
| Long-lived requests | generation-pinned; reload does not mutate in-flight request graph | New GeoIP policy applies to newly admitted requests, not retroactively to existing SSE/WebSocket streams |
| Access logging | inside proposed gate location | GeoIP denials naturally bypass normal access log, matching anti-spam requirement |
| General OTel/HTTP Prometheus | inside proposed gate location | Denials need dedicated bounded GeoIP metrics if visibility is desired |
| Access config | `AccessConfig` currently contains deployment `mode`; changes are restart-required as one `access` block | Split classification: preserve `access.mode` restart requirement, make pure `access.geoip` policy reloadable, resource fields restart-required |
| Feature stages | transport auth is `stdhttp`; `pre_request_admission` is later canonical LLM stage | GeoIP is transport ingress, not FeatureBundle/plugin stage |
| GeoIP database dependencies | none currently | Add implementation dependencies only during feature implementation; spec remains dependency-free |

## Gap 1: Data-Plane vs Management-Plane Scope

### Finding

`ComposeStandardHTTP` deliberately excludes config-reload/status management routes because the management listener is process-owned. The initial requirements said "standard HTTP data plane" but did not positively protect the management recovery path.

### Risk

A future implementer could wrap a process-level server or common mux and unintentionally GeoIP-block the authenticated/loopback management path. That could make a bad policy reload difficult to recover from and violate ADR 0008's ownership boundary.

### Requirement correction

Add an explicit compatibility requirement:

- v1 GeoIP enforcement applies to the canonical standard **data-plane** handler generation;
- the dedicated process-owned reload management listener retains its existing loopback/token trust policy and is not automatically governed by data-plane GeoIP;
- extending GeoIP to management is a separate explicit security decision.

**Disposition: REQUIREMENTS UPDATED.**

## Gap 2: Reload Semantics for Existing Long-Lived Traffic

### Finding

ADR 0008 pins in-flight work to the generation it acquired. OpenResponses WebSocket/SSE and other long-lived traffic should not have their middleware/policy graph changed underneath them.

### Risk

"Re-configure policy during runtime" could be interpreted as retroactively disconnecting existing connections from newly denied countries. Doing that would require a separate connection registry/revocation mechanism and contradict current generation semantics.

### Requirement correction

Clarify that:

- a reload changes admission for requests/connections that enter the newly published generation;
- already admitted in-flight work remains pinned to its original generation and is not retroactively terminated;
- retroactive connection revocation is out of scope.

**Disposition: REQUIREMENTS UPDATED.**

## Gap 3: Disabled Request Fast Path vs Warm Database Provisioning

### Finding

Issue #387 requires disabled mode to avoid GeoIP lookup cost. Runtime reload additionally requires operators to enable policy without restart. These goals conflict if "disabled" is interpreted as "the process must not even maintain/provision a database."

### Correct model

Two independent states are needed:

1. **request enforcement** — generation-owned; disabled means wrapper omitted and zero GeoIP request work;
2. **database service provisioning** — process-owned; if configured, it may load/update an MMDB even while enforcement is disabled so a future reload can enable policy immediately.

If the process starts with no GeoIP database source configured at all, a later reload that merely sets `enabled: true` must fail because creating new process-owned lifecycle infrastructure is restart-required.

### Requirement correction

Add explicit acceptance criteria for this distinction and for enable-on-reload readiness.

**Disposition: REQUIREMENTS UPDATED.**

## Gap 4: `check-config` Must Not Become an Updater/Network Operation

### Finding

ADR 0008 deliberately shares generation compilation with `check-config` in dry-run/rollback mode. Static validation should not unexpectedly download GeoIP data or require live provider connectivity.

### Risk

If policy compilation reaches through to the managed updater, `check-config` becomes non-deterministic and network-dependent, and CI/offline validation becomes fragile.

### Requirement correction

Specify:

- `check-config` validates syntax, country codes, CIDRs, source-mode consistency, bounds, and reload classification without network access;
- it must not trigger managed update/download;
- runtime enablement performs the separate readiness check against process-owned lookup state;
- an explicitly configured local file may be structurally checked when the runtime path is available, but static configuration validity cannot depend on external network availability.

**Disposition: REQUIREMENTS UPDATED.**

## Gap 5: Cache Requirement Should Preserve Intent, Not Mechanism

### Finding

The issue asks that "expensive geoip lookups should get cached to avoid spamming the related backend." The recommended design has no request-time GeoIP backend: one concurrent long-lived MMDB reader performs in-process memory-mapped lookups.

### Risk

Treating a cache as a literal acceptance criterion could lead to an unbounded `map[IP]country`, which is an attacker-controlled memory-growth primitive and adds invalidation complexity around MMDB updates.

### Requirement correction

Keep the observable intent:

- never open MMDB per request;
- never call a GeoIP network backend per request;
- reuse the long-lived reader;
- no unbounded IP cache;
- optional bounded cache only after profiling/benchmarks and database-version-safe invalidation.

This is a refinement, not a scope reduction.

**Disposition: REQUIREMENTS UPDATED.**

## Gap 6: Process Configuration Reload Classification Needs Field Granularity

### Finding

Current `classifyAccess` marks any `Access.Mode` change as `restart("access")`. Adding `GeoIP` under `AccessConfig` without refactoring classification could make all GeoIP policy changes restart-required, directly violating #387.

### Required disposition

The implementation must split classification by field path:

- `access.mode` remains restart-required;
- `access.geoip.enabled`, order, country/CIDR sets, client-IP source and trusted proxies are reloadable;
- database source/path/edition/update lifecycle and credential-source settings are restart-required initially.

Mixed candidates continue to fail atomically under existing reload semantics.

**Disposition: REQUIREMENTS ALREADY COVERED; DESIGN/TASK MUST NAME THE EXISTING CLASSIFIER.**

## Gap 7: Generic Denial Rendering Is Required by Placement

### Finding

The gate runs before frontend identification and transport auth. Existing frontend-specific auth renderers therefore cannot be used safely without pulling later responsibilities earlier.

### Required disposition

The ingress gate owns one protocol-agnostic 403 response and relies only on global outer security/server headers. No OpenAI/Anthropic/Gemini error-shape branching is required or desired.

**Disposition: REQUIREMENTS ALREADY COVERED.**

## Gap 8: Country Unknown vs Infrastructure Failure

### Finding

GeoIP databases legitimately have addresses for which a country is absent. This is different from a corrupt reader/decoder failure.

### Required disposition

Keep three states explicit:

- `found(country)` → country class can match;
- `not found` → normal no-country match, policy default/other CIDR rules decide;
- `error` → fail closed when enforcement requires lookup.

**Disposition: REQUIREMENTS ALREADY COVERED.**

## Gap 9: Safe Reader Replacement Must Respect Concurrency and Windows

### Finding

The standard MMDB implementation can memory-map files; closing/replacing a reader while concurrent lookups are in flight is unsafe, and Windows makes replacement/deletion of open mapped files especially problematic.

### Required disposition

The design must make publication explicit: validate new reader/file off-path, atomically swap under a short synchronization boundary, retire the old reader only after prior lookups drain, use versioned files rather than in-place overwrite, then garbage-collect.

**Disposition: REQUIREMENTS ALREADY COVERED; DESIGN VALIDATION MUST VERIFY lifecycle ownership.**

## Gap 10: Observability Placement Is Intentionally Asymmetric

### Finding

Putting the gate outside OTel/general HTTP metrics/access logging means blocked traffic is intentionally invisible to those generic layers.

### Required disposition

Dedicated GeoIP counters/status are required, with no IP/country labels and no per-denial access log. This is necessary to meet the original "avoid log spam" goal; it is not an observability regression.

**Disposition: REQUIREMENTS ALREADY COVERED.**

## Requirement Changes Applied After This Analysis

The reconciled requirements revision SHALL add:

- a Brownfield Compatibility / Plane Boundary requirement;
- explicit new-request versus in-flight reload semantics;
- explicit disabled-request-path versus pre-provisioned process-service semantics;
- an offline/deterministic `check-config` contract;
- a strengthened explanation that local-reader reuse supersedes a mandatory per-IP cache.

No initial requirement is removed. The changes make previously implicit brownfield constraints testable.

## Quality Gate

**Result after planned reconciliation: PASS.**

The feature can proceed to design once the above amendments are applied. There is no need for a new generic extension stage, no need to alter backend/frontend protocol contracts, and no need for a parallel runtime-reload mechanism.
