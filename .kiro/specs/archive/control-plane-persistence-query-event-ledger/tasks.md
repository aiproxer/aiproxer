# Implementation Plan

- [x] 1. Foundation: public contracts, core ports, validation, and configuration
- [x] 1.1 Define safe control-plane event and status contracts
  - Add stable event identity, category, evidence availability, redaction, visibility, correlation, source, typed detail, record-result, status, and error classification DTOs for future feature and operator consumers.
  - Preserve scope attribution through safe principal/scope snapshots that distinguish unknown fields from known-empty fields.
  - Contract tests demonstrate stable JSON shape, one-detail-per-event expectations, status classifications, and no transport, SQL, HTTP, frontend wire, or provider SDK dependencies.
  - _Requirements: 1.7, 1.8, 3.5, 4.1, 4.2, 4.3, 4.7, 7.1, 7.4, 9.1, 9.2, 9.3, 9.4, 9.5_
  - _Boundary: SDK/public event contract_
  - _Validation: go test ./pkg/lipsdk/controlplane_

- [x] 1.2 Define safe control-plane query and pagination contracts
  - Add session, attempt, usage, usage aggregate, policy/audit, raw event, filter, page, continuation, visibility, and unsupported-filter DTOs for bounded cross-session consumers.
  - Encode query contracts so consumers can request filters without knowing which diagnostic, observer, ledger, or store supplied each result.
  - Contract tests demonstrate bounded page metadata, opaque continuation values, unsupported-filter reporting, disabled capability responses, and safe default visibility.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 2.9, 3.5, 4.7, 7.4, 9.1, 9.4, 9.5_
  - _Boundary: SDK/public query contract_
  - _Validation: go test ./pkg/lipsdk/controlplane_

- [x] 1.3 Establish core errors, status state, and invariant validation
  - Add stable classifications for disabled, unavailable, degraded, invalid query, too broad, unsupported filter, and unsafe evidence outcomes.
  - Validate category/detail exclusivity, source identity, timing, correlation, redaction, availability, and safe-summary invariants before records can be persisted or returned.
  - Status tests show disabled, ready, degraded, and unavailable states expose bounded safe reason codes without raw infrastructure details.
  - _Requirements: 1.7, 1.8, 3.4, 3.5, 3.6, 4.4, 4.5, 4.6, 4.7, 5.2, 7.1, 7.2, 7.3, 7.4, 9.4_
  - _Boundary: core control-plane validation/status_
  - _Depends: 1.1, 1.2_
  - _Validation: go test ./internal/core/controlplane_

- [x] 1.4 Define the core event-store, clock, and identity ports
  - Add consumer-owned ports for append, query, retention, readiness, time, and identity generation without exposing SQL, Bun, HTTP, transport, or provider SDK types.
  - Keep store responsibilities aligned with recorder, query service, and retention controller needs rather than creating a generic repository layer.
  - Port tests or compile-time assertions prove memory and durable adapters can satisfy the same core-owned contract once implemented.
  - _Requirements: 1.7, 1.8, 2.6, 2.7, 6.1, 7.1, 9.5_
  - _Boundary: core control-plane ports_
  - _Depends: 1.1, 1.2, 1.3_
  - _Validation: go test ./internal/core/controlplane_

- [x] 1.5 Add typed control-plane configuration and startup validation
  - Add disabled-by-default recording, store, durable connection, query exposure, page bounds, time-window bounds, retention, redaction, required-category, and recording-policy configuration.
  - Reject invalid combinations such as enabled durable stores without connection settings, query exposure without a protected diagnostics posture, required recording without durable readiness, and max page sizes below defaults.
  - Config tests show disabled mode preserves current behavior, enabled mode produces explicit startup readiness decisions, and excluded enterprise features have no configuration surface.
  - _Requirements: 2.6, 2.9, 5.4, 5.5, 7.1, 7.4, 7.5, 7.6, 10.1, 10.2, 10.3, 10.4_
  - _Boundary: config validation_
  - _Validation: go test ./internal/core/config_

- [x] 2. Storage and query substrate
- [x] 2.1 Build the in-memory event store for deterministic local recording and tests
  - Append normalized events with stable event IDs, monotonic ordering, source-event-key dedupe, safe scope snapshots, and category-specific safe details.
  - Support bounded reads for events, sessions, attempts, usage, usage aggregates, and policy/audit evidence from the same recorded facts.
  - Store tests demonstrate empty result behavior, deterministic ordering, dedupe results, and unknown-vs-known-empty scope preservation.
  - _Requirements: 1.7, 1.8, 2.1, 2.2, 2.3, 2.4, 2.8, 3.1, 3.4, 3.5, 4.2, 4.3, 8.5, 9.1, 9.5_
  - _Boundary: driven adapter memory event store_
  - _Depends: 1.4_
  - _Validation: go test ./internal/infra/controlplane/ledgerstore_

- [x] 2.2 (P) Add durable SQLite and Postgres event-store migrations
  - Create the single append-only event table with columnized identity, timing, correlation, state, safe scope dimensions, bounded safe detail JSON, bounded safe scope JSON, and bounded safe summary JSON.
  - Add dialect-aware constraints and indexes for source dedupe, stable ordering, category/time, trace/session/A-leg/B-leg, backend/model, outcome/effect/reason, and filterable scope dimensions.
  - Migration tests prove SQLite migration succeeds locally, Postgres migration is gated by existing integration settings, and no per-category detail tables are required for the first implementation.
  - _Requirements: 1.7, 1.8, 2.5, 2.6, 4.2, 4.3, 4.4, 4.5, 6.1, 7.1, 7.6_
  - _Boundary: durable store migrations_
  - _Depends: 1.1, 1.2, 1.4_
  - _Validation: go test ./internal/infra/controlplane/ledgerstore -run 'TestSQLiteStore_migrations|TestSQLiteStore_noDetailTables'_

- [x] 2.3 Add durable append, scan, readiness, and store-contract coverage
  - Persist normalized events atomically with their safe detail and scope snapshots, returning stable record results for new and deduplicated source events.
  - Reconstruct SDK query DTOs from durable rows without leaking SQL, Bun, DSN, driver, or infrastructure error text into core contracts.
  - Shared contract tests pass for memory and SQLite stores, covering append, dedupe, readiness, safe scan mapping, ordering, and classified storage failures.
  - _Requirements: 1.7, 1.8, 2.1, 2.2, 2.3, 2.4, 3.1, 3.7, 4.4, 4.5, 7.1, 7.3, 8.2, 8.3, 9.5_
  - _Boundary: driven adapter durable event store_
  - _Depends: 2.2_
  - _Validation: go test ./internal/infra/controlplane/ledgerstore/contract ./internal/infra/controlplane/ledgerstore_

- [x] 2.4 Add store-level query filters, continuation, and unsupported-filter reporting
  - Apply supported filters for safe principal/scope dimensions, time range, backend, model, session, A-leg, B-leg, outcome, effect, visibility, and reason code.
  - Return bounded pages and continuation tokens that resume from the prior visible position without duplication or skipping under the same query shape.
  - Contract tests show unsupported filters are reported explicitly, broad queries are not silently widened, and disabled/unavailable states are distinct from empty result pages.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 2.9, 3.5, 3.6, 4.2, 6.3, 8.6, 9.4, 9.5_
  - _Boundary: driven adapter query seam_
  - _Depends: 2.3_
  - _Validation: go test ./internal/infra/controlplane/ledgerstore/contract ./internal/infra/controlplane/ledgerstore_

- [x] 2.5 Add retention and redaction store operations
  - Mark or prune records outside configured retention windows while preserving allowed safe correlation metadata and explicit expired/unavailable states.
  - Apply redaction profiles to safe details and privileged summaries without presenting aggregate evidence as detailed raw records.
  - Retention contract tests show repeated runs are idempotent, default query visibility returns redacted or summarized evidence, and in-flight runtime stores are not mutated.
  - _Requirements: 4.6, 4.7, 5.7, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 7.2_
  - _Boundary: driven adapter retention seam_
  - _Depends: 2.4_
  - _Validation: go test ./internal/infra/controlplane/ledgerstore/contract ./internal/infra/controlplane/ledgerstore_

- [x] 3. Core recording, normalization, query, and retention services
- [x] 3.1 Build safe scope flattening and reconstruction
  - Flatten safe principal, credential, tenant, organization, workspace, project, department, cost center, roles, safe claims, policy labels, origin, and parent trace fields into presence-aware storage/query values.
  - Clone and bound slices and maps at the boundary so query results cannot expose unsafe or mutable caller-owned data.
  - Unit tests prove unknown attribution, known-empty attribution, and safe-valued attribution round-trip distinctly through event normalization and query output.
  - _Requirements: 4.1, 4.2, 4.3, 4.7, 4.8, 9.1_
  - _Boundary: core scope flattener_
  - _Validation: go test ./internal/core/controlplane -run TestScopeFlattener

- [x] 3.2 Normalize authentication, session, and attempt evidence
  - Convert safe auth decisions, session lifecycle facts, and backend attempt facts into one validated event shape with shared trace, request, session, A-leg, B-leg, attempt sequence, backend, model, timing, output-surfaced, outcome, and failure classification fields where available.
  - Distinguish surfaced attempts from swallowed, failed, cancelled, losing, and post-output attempts without inventing unavailable identifiers.
  - Unit tests show normalized records preserve correlation and availability state and reject raw credentials, raw headers, raw payloads, and provider wire data.
  - _Requirements: 1.1, 1.2, 1.3, 1.6, 3.1, 3.2, 3.3, 3.6, 3.7, 4.4, 4.5, 5.3, 10.5, 10.7_
  - _Boundary: core event normalizer_
  - _Depends: 1.1, 1.3, 3.1_
  - _Validation: go test ./internal/core/controlplane -run 'TestNormalize(AuthDecision|SessionStart|SessionRecord|Attempt)'

- [x] 3.3 Normalize usage, policy, and audit evidence
  - Convert safe usage observations, accounting authority evidence, policy decisions, admission decisions, and audit facts into typed event details with token dimensions, accounting plane, decision stage, effect, visibility, reason code, redaction state, and safe action/result summaries.
  - Preserve observed, accounting-authoritative, unavailable, and failed-accounting states separately for usage consumers.
  - Unit tests show policy outcomes are never changed, raw usage JSON is not surfaced by default, privileged audit evidence is marked redacted or summarized for default visibility, and safe reasons remain queryable.
  - _Requirements: 1.4, 1.5, 1.6, 3.4, 3.5, 3.7, 4.4, 4.5, 4.6, 4.7, 4.8, 8.2, 9.2, 9.3_
  - _Boundary: core event normalizer_
  - _Depends: 1.1, 1.3, 3.1_
  - _Validation: go test ./internal/core/controlplane -run 'TestNormalize(Usage|UsageRecord|PolicyDecision|Audit)'

- [x] 3.4 Add recording policy and capability status behavior
  - Record valid events through the configured store when enabled and report disabled without touching source seams when disabled.
  - Treat best-effort recording failures as degraded operator-visible status while preserving request outcomes, and treat required pre-work failures as fail-closed before protected upstream work begins.
  - Unit tests prove post-output recording failures cannot request retry, failover, or replacement and non-streaming collection keeps the same correlation path.
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 7.1, 7.2, 7.3, 7.5, 7.6, 10.7_
  - _Boundary: core recorder service_
  - _Depends: 2.1, 3.2, 3.3_
  - _Validation: go test ./internal/core/controlplane -run TestRecorder

- [x] 3.5 Add bounded query service behavior
  - Validate query page size, time window, continuation shape, visibility, and filter support before store access.
  - Serve session, attempt, usage, aggregate usage, policy/audit, and raw-event views while distinguishing disabled capability, empty matches, unavailable evidence, expired evidence, redacted evidence, and unsupported capabilities.
  - Unit tests prove continuations are tied to query shape and visibility, too-broad queries fail stably, unsupported filters are reported, and query consumers do not need to know the source store or observer that supplied a result.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 2.9, 3.4, 3.5, 3.6, 6.3, 6.4, 7.1, 7.4, 9.1, 9.4, 9.5_
  - _Boundary: core query service_
  - _Depends: 2.4_
  - _Validation: go test ./internal/core/controlplane -run TestQueryService

- [x] 3.6 Add retention and redaction orchestration
  - Apply configured retention and redaction commands through the store with safe status updates and without mutating routing, policy, usage, or session outcomes for active requests.
  - Keep detailed records, summaries, privileged evidence, and aggregate usage views distinct after retention or redaction changes visibility.
  - Unit tests prove retention actions are idempotent, safe correlation remains available where allowed, and operation failures classify status without raw infrastructure leakage.
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 7.2, 10.7_
  - _Boundary: core retention controller_
  - _Depends: 2.5, 3.5_
  - _Validation: go test ./internal/core/controlplane -run TestRetention

- [x] 4. Source adapters for existing evidence seams
- [x] 4.1 Add authentication event fan-out into control-plane recording
  - Fan authentication decisions and session-start events to the existing auth sink and the control-plane recorder without requiring existing sinks to understand query capability.
  - Apply existing auth event delivery policy and control-plane required-pre-work policy only at safe pre-upstream points.
  - Adapter tests show auth/session events preserve trace and safe scope attribution, existing sink behavior remains unchanged, and recording failures are classified according to policy.
  - _Requirements: 1.1, 1.2, 3.1, 4.1, 5.2, 5.4, 8.4_
  - _Boundary: source adapter auth sink_
  - _Depends: 3.2, 3.4_
  - _Validation: go test ./internal/infra/controlplane/observers -run TestAuthSinkAdapter

- [x] 4.2 (P) Add policy and usage observer adapters
  - Record safe policy decisions, admission outcomes, and usage observations while leaving existing observer chains fail-open or failure-preserving as they are today.
  - Use deterministic source event keys and safe correlation fields so repeated observer delivery deduplicates without hashing raw payloads, headers, or tokens.
  - Adapter tests show policy outcomes are unchanged, usage observer behavior is preserved, unsupported or partial evidence is marked explicitly, and control-plane failures only degrade status unless required by an existing source policy.
  - _Requirements: 1.4, 1.5, 3.4, 3.5, 3.7, 5.2, 5.5, 8.2, 8.4, 8.5_
  - _Boundary: source adapter observers_
  - _Depends: 3.3, 3.4_
  - _Validation: go test ./internal/infra/controlplane/observers -run 'TestPolicyObserverAdapter|TestUsageObserverAdapter'

- [x] 4.3 Add secure-session store recording decorator
  - Decorate session create, activity touch, attempt trace, attempt outcome, usage addition, and audit append operations while delegating authoritative secure-session behavior to the existing store.
  - Record after delegate success when authoritative identifiers are known, except for configured pre-work guarantees that can safely fail closed before upstream work.
  - Decorator tests show existing session list, detail, transcript, audit, and by-A-leg diagnostics retain their safe fields while control-plane query records carry matching identifiers and redaction state.
  - _Requirements: 1.2, 1.3, 1.4, 1.6, 3.1, 3.4, 3.7, 5.1, 5.3, 6.6, 8.1, 8.5, 10.7_
  - _Boundary: source adapter secure-session store_
  - _Depends: 3.2, 3.3, 3.4_
  - _Validation: go test ./internal/infra/controlplane/observers -run TestSecureSession_

- [x] 4.4 (P) Add B2BUA attempt-lineage recording decorator
  - Decorate attempt recording so A-leg, B-leg, attempt sequence, route outcome, surfaced state, and losing/swallowed/failure states are projected into control-plane attempt evidence.
  - Preserve B2BUA allocation and continuity semantics; control-plane failures never change routing outcomes or attempt replacement behavior.
  - Decorator tests show A-leg/B-leg lineage matches existing continuity evidence and query consumers can distinguish surfaced attempts from non-surfaced attempts.
  - _Requirements: 1.3, 2.2, 3.1, 3.2, 3.3, 3.7, 5.1, 5.3, 8.3, 8.5, 10.7_
  - _Boundary: source adapter B2BUA store_
  - _Depends: 3.2, 3.4_
  - _Validation: go test ./internal/infra/controlplane/observers -run TestB2BUA_

- [x] 4.5 Verify adapter compatibility and non-interference across source seams
  - Exercise auth, policy, usage, secure-session, and B2BUA adapters together with best-effort, disabled, and required-pre-work policies.
  - Prove existing observers keep receiving their current events and existing stores keep their current semantics even when control-plane query capability is disabled, degraded, or unavailable.
  - Integration tests show source limitations are reported explicitly instead of widening query results or inventing missing evidence.
  - _Requirements: 3.4, 3.5, 3.6, 5.1, 5.2, 5.5, 7.5, 8.1, 8.4, 8.5, 8.6, 10.7_
  - _Boundary: adapter integration tests_
  - _Depends: 4.1, 4.2, 4.3, 4.4_
  - _Validation: go test ./internal/infra/controlplane/observers ./internal/core/securesession/..._

- [x] 5. Runtime and protected operator query integration
- [x] 5.1 Wire control-plane stores, recorder, queries, status, and closers into runtime construction
  - Build memory, SQLite, or Postgres stores from typed config and expose recorder, query, and status handles through the standard runtime bundle only when enabled.
  - Wrap auth, policy, usage, secure-session, and B2BUA seams without dropping operator-provided observers or existing diagnostics wiring.
  - Runtimebundle tests show disabled mode adds no source wrapping, enabled mode wires all configured seams, closers are owned by the bundle, and no provider telemetry or client-facing protocol responses receive control-plane records.
  - _Requirements: 5.1, 5.5, 5.7, 7.1, 7.5, 8.4, 10.5, 10.6, 10.7_
  - _Boundary: composition root runtimebundle_
  - _Depends: 1.5, 2.3, 3.4, 3.5, 4.5_
  - _Validation: go test ./internal/infra/runtimebundle_

- [x] 5.2 Add startup fail-closed and readiness behavior for enabled recording/query posture
  - Fail startup when required durable evidence, required pre-work recording, durable store readiness, retention setup, or protected query exposure cannot satisfy configured posture.
  - Report disabled, ready, degraded, and unavailable states with safe reason codes for recording, query, retention, redaction, and backing-capability failures.
  - Runtime tests prove raw DSNs, SQL, driver text, raw infrastructure errors, and secrets do not leak to client-facing protocols or unprotected HTTP responses.
  - _Requirements: 7.1, 7.2, 7.3, 7.5, 7.6, 10.5_
  - _Boundary: composition root startup posture_
  - _Depends: 5.1_
  - _Validation: go test ./internal/infra/runtimebundle ./internal/stdhttp_

- [x] 5.3 Add protected HTTP status and query routes
  - Mount status, sessions, attempts, usage, usage aggregate, policy-audit, and events routes only when control-plane query exposure and diagnostics shared-secret protection are explicitly configured.
  - Map request filters, page bounds, visibility, continuation, unsupported filters, disabled state, empty results, and stable error classifications to safe JSON responses.
  - HTTP tests show routes are absent when disabled, protected when enabled, return bounded pages, do not expose privileged raw evidence by default, and do not become client-facing LLM protocol responses.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 2.9, 4.6, 4.7, 5.5, 7.1, 7.4, 8.6, 9.1, 9.4, 10.4, 10.5_
  - _Boundary: driving adapter protected HTTP query_
  - _Depends: 3.5, 5.2_
  - _Validation: go test ./internal/stdhttp/admin/controlplane ./internal/stdhttp_

- [x] 5.4 Add runnable configuration cases for disabled, memory, durable, query, and retention modes
  - Keep default configuration disabled while adding explicit examples or fixtures for enabled memory recording, SQLite/Postgres durable recording, protected query exposure, required pre-work policy, and retention windows.
  - Startup/config tests prove the examples validate or fail closed exactly as intended and do not introduce billing, identity provisioning, policy-engine, web-admin, reporting-chart, marketplace, provider-forwarding, or historical-migration behavior.
  - Operators can run the standard distribution with existing config and observe unchanged behavior unless they explicitly enable control-plane settings.
  - _Requirements: 2.9, 5.4, 7.5, 7.6, 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 10.7_
  - _Boundary: config examples and startup fixtures_
  - _Depends: 5.2, 5.3_
  - _Validation: go test ./internal/core/config ./internal/infra/runtimebundle_

- [x] 6. End-to-end validation, compatibility, and guardrails
- [x] 6.1 Validate cross-session evidence capture and query flow end to end
  - Run a representative auth, secure-session, backend attempt, policy, usage, and audit flow through the standard runtime with control-plane recording enabled.
  - Query sessions, attempts, usage, usage aggregate, policy/audit, and raw events across sessions using principal/scope, time, backend/model, session, A-leg, B-leg, outcome, effect, visibility, and reason filters.
  - End-to-end tests show shared trace/session/A-leg/B-leg correlation, stable ordering, bounded pages, valid continuation, empty-result behavior, and source/availability context for partial evidence.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 3.1, 3.4, 3.5, 3.6, 3.7, 9.1, 9.2, 9.3, 9.5_
  - _Boundary: end-to-end control-plane tests_
  - _Depends: 5.4_
  - _Validation: go test ./internal/testkit/... ./internal/infra/runtimebundle -run TestControlPlane_

- [x] 6.2 Validate routing, failover, racing, and streaming non-interference
  - Exercise pre-output failover and parallel race paths that create surfaced, swallowed, failed, cancelled, and losing attempts.
  - Inject post-output recording failures and best-effort store failures while asserting canonical streaming order, non-streaming collection behavior, output commitment, and no-retry-after-first-output semantics remain unchanged.
  - Regression tests show mandatory pre-work recording failures happen before backend execution and best-effort/degraded recording failures never change already-surfaced client output.
  - _Requirements: 1.3, 3.2, 3.3, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 5.7, 10.7_
  - _Boundary: runtime streaming regression tests_
  - _Depends: 5.4_
  - _Validation: go test ./internal/core/runtime ./internal/testkit/conformance/...

- [x] 6.3 Validate compatibility with existing diagnostics, observers, and stores
  - Compare existing secure-session list, detail, transcript, audit, and by-A-leg diagnostics before and after enabling control-plane recording.
  - Verify token-accounting correlation, B2BUA lineage, auth event sinks, usage observers, traffic observers, and policy observers continue to work without consuming the new query capability.
  - Compatibility tests show shared identifiers, safe attribution, redaction state, and explicit source limitations are consistent across old diagnostics and new control-plane views.
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 10.6, 10.7_
  - _Boundary: compatibility regression tests_
  - _Depends: 5.4_
  - _Validation: go test ./internal/core/securesession/... ./internal/infra/tokenaccounting/... ./internal/core/b2bua/... ./internal/stdhttp_

- [x] 6.4 Validate privacy, redaction, retention, and exclusion guardrails
  - Scan stored events and HTTP query results from representative flows for raw bearer tokens, API keys, OAuth tokens, resume tokens, credential secrets, raw transport headers, raw request payloads, and raw response payloads.
  - Verify privileged, summarized, redacted, hashed, expired, unavailable, and unsupported states are explicit and safe values only are returned for roles, claims, policy labels, usage metadata, and audit labels.
  - Security regression tests show retention/redaction does not affect in-flight routing, policy, usage, or session outcomes and excluded enterprise features remain absent.
  - _Requirements: 4.4, 4.5, 4.6, 4.7, 4.8, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 10.1, 10.2, 10.3, 10.4, 10.5_
  - _Boundary: security and lifecycle regression tests_
  - _Depends: 5.4_
  - _Validation: go test ./internal/core/controlplane ./internal/infra/controlplane/... ./internal/stdhttp/admin/controlplane_

- [x] 6.5 Add architecture and quality guardrails for the control-plane feature
  - Add dependency checks proving SDK contracts do not import core, SQL, Bun, HTTP, frontend wire, backend plugin, or provider SDK packages, and core control-plane code does not import provider SDKs or concrete plugins.
  - Add guardrails showing no pairwise protocol translator, provider telemetry forwarding, hidden background worker, or client-facing LLM protocol response path was introduced by the feature.
  - Quality checks pass for formatting, vetting, architecture tests, focused unit suites, store contracts, and the control-plane regression set.
  - _Requirements: 5.1, 5.6, 5.7, 9.5, 10.5, 10.7_
  - _Boundary: architecture and QA tests_
  - _Depends: 6.1, 6.2, 6.3, 6.4_
  - _Validation: make quality-checks && go test ./internal/archtest ./internal/qa/...
