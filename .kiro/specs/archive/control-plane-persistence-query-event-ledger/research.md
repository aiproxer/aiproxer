# Implementation Gap Analysis: control-plane-persistence-query-event-ledger

Requirements status: generated, not approved in `spec.json` at analysis time. Gap analysis proceeds because it can inform requirements review and design.

## Executive Summary

The codebase already contains most raw facts required by the feature, but they are split across several domain-specific stores and observer seams. Secure-session storage has sessions, attempts, usage, audit, summaries, readiness checks, and diagnostics; B2BUA continuity has A-leg/B-leg lineage; token accounting has ledger persistence; auth and policy decisions have safe observer/event DTOs. The major gap is a unified, query-ready control-plane evidence model that records stable event identities, applies cross-session filters, exposes bounded continuation, and preserves safe scope attribution consistently across all subsystems.

The feature is feasible without provider SDK imports, client protocol changes, or canonical stream changes. The safest design path is likely hybrid: add a new control-plane event/query capability that consumes existing lifecycle facts through narrow seams, while preserving existing secure-session diagnostics and observer contracts. Directly expanding secure-session or token-accounting stores alone would be faster but risks coupling unrelated lifecycle domains and making future admin/reporting features harder.

## Current State Investigation

### Existing Assets

| Area | Existing assets | Current capability | Gap relevance |
| --- | --- | --- | --- |
| Principal/scope attribution | `pkg/lipsdk/scope/view.go`, `pkg/lipsdk/scope/value.go`, `internal/core/execctx`, `internal/core/runtime/scope_resolver.go` | Safe, presence-aware `PrincipalScopeView`; distinguishes unknown vs known-empty values; excludes secrets by construction. | Strong foundation for query identity dimensions, but existing durable stores mostly persist legacy owner/workspace fields rather than the full scope view. |
| Auth events | `pkg/lipsdk/auth/events.go`, `internal/core/auth/events.go`, `internal/infra/authevent/sink.go`, `internal/infra/runtimebundle/auth_events.go` | Safe `AuthDecisionEvent` and `SessionStartEvent`; dispatcher can fail open or fail closed; default sink logs only safe fields and claim keys. | Events are sink/log oriented, not durably queryable; no event identity/cursor; session-start event lacks full scope snapshot. |
| Policy/admission evidence | `pkg/lipsdk/policydecision/record.go`, `pkg/lipsdk/policydecision/observe.go`, `internal/core/extensions/decision_evidence.go` | Normalized `Record` includes trace, A-leg, B-leg, attempt seq, stage, provider, outcome, effect, reason, visibility, scope, annotations; observer errors are isolated. | Excellent DTO seam for event capture; no durable observer adapter; no query API; no stable persisted ordering. |
| Attempt lifecycle evidence | `internal/core/extensions/attempt_evidence.go`, `internal/core/runtime/attempt_stream.go`, `internal/core/b2bua/store.go`, `pkg/lipapi.AttemptRecord` | Runtime records B2BUA attempts; secure-session stores richer attempt traces/outcomes/accounting; policy evidence can project attempt failures. | Attempt facts are fragmented between lightweight continuity rows and rich secure-session rows; cross-session attempt query is missing. |
| Secure-session store | `internal/core/securesession/app/ports.go`, `internal/core/securesession/domain/types.go` | `app.Store` persists sessions, attempt trace/outcome, transcript, audit, usage, summaries, readiness; `SessionUsageRollup` optional extension. | Richest existing asset; still session-shaped, legacy owner/workspace focused, and not a general event ledger. |
| Secure-session diagnostics | `internal/core/securesession/adapters/diag/handlers.go`, `internal/stdhttp/server.go` | HTTP diagnostics list/detail/transcript/audit/by-A-leg; protected by diagnostics shared-secret; bounded limit for some reads. | Useful compatibility surface; filters limited to owner/workspace/session/A-leg; no generalized control-plane filters, cursor model, or unsupported-filter reporting. |
| Secure-session durable schemas | `internal/core/securesession/adapters/bunstore/20250426000000_securesession_baseline.go`, related migrations | SQLite/Postgres Bun schemas for sessions, attempt traces, usage, audit, transcript; indexes for session, owner, workspace, B-leg, backend/model. | Schema has many needed fields, but stores raw usage JSON, lacks full scope columns, lacks policy/auth event tables, lacks cross-domain event ordering. |
| B2BUA continuity | `internal/core/b2bua/store.go`, `internal/core/continuity/bunstore/store.go`, `internal/infra/runtimebundle/continuity_open.go` | A-leg creation/resolution, B-leg allocation, attempt lineage, interleaved state; memory and durable Bun stores. | Strong A/B-leg authority; attempt rows are not broad enough for operator query views and lack scope/session/policy/usage dimensions. |
| Token accounting ledger | `internal/core/tokenaccounting/ledger`, `internal/infra/tokenaccounting/ledgerstore/store.go`, `internal/infra/runtimebundle/token_accounting.go` | Memory or durable Bun ledger; records request, attempt, backend, model, usage plane, token counts, metadata, unavailable/failure reasons. | Durable usage source exists but only queries by request/attempt; no scope/session filters or aggregates; no event-level cursor; no direct operator usage query surface. |
| Traffic and usage observers | `pkg/lipsdk/traffic/observe.go`, `pkg/lipsdk/usage/observe.go`, runtime emission in `attempt_stream.go` | Observers carry trace, A-leg/B-leg, principal/session/backend/frontend/model, safe scope, and usage/raw capture metadata. | Good optional capture seams; observer chains are not persistence/query stores; usage observer chain returns errors unlike policy chain, so non-interference semantics need review. |
| Runtime composition | `internal/infra/runtimebundle/options.go`, `internal/infra/runtimebundle/build.go`, `internal/infra/runtimebundle/built.go` | Explicit BuildOptions for auth sinks, policy observers, traffic/usage observers, raw capture, secure-session store; Built exposes stores/admin handles. | Natural wiring point for a new control-plane recorder/query service; no current `ControlPlane` config/runtime object/readiness surface. |
| HTTP/operator surfaces | `internal/stdhttp/server.go`, `internal/core/diag`, `internal/stdhttp/admin/tokenaccounting` | Diagnostics and admin endpoints are mounted behind existing config/security posture checks. | A future query endpoint can fit here, but this spec does not require admin UI; design should distinguish internal query port from optional HTTP diagnostic adapter. |
| Tests | `internal/core/securesession/storecontract`, `internal/core/b2bua/*contract*`, `internal/infra/tokenaccounting/ledgerstore/store_test.go`, runtime evidence tests, archtests | Strong contract-test pattern, env-gated Postgres tests, architecture guardrails. | Need new contract tests for event ledger/query semantics, redaction, pagination, non-interference, and compatibility with existing diagnostics. |

### Architectural Patterns and Constraints

- Public contracts belong in `pkg/lipsdk/` only when external plugins or future feature consumers need stable access; core implementation belongs under `internal/core/` and adapters under `internal/infra/` or domain adapter packages.
- Store interfaces are consumer-owned and use `context.Context` first; durable adapters hide SQL/Bun details behind ports.
- Runtime wiring is explicit in `internal/infra/runtimebundle`; no DI containers, reflection registries, globals, or hidden startup mutation.
- Streaming-first and no-retry-after-first-output semantics are hard constraints; evidence recording must not create a second execution path or alter stream ordering.
- Diagnostics and privileged visibility are composition-root decisions; request/front-end paths must not enable privileged policy evidence.
- Persistence changes need explicit migration/review evidence; schema mutation should not be implicit outside adapter startup migrations.
- Query adapters for operator/reporting flows are permitted and preferred when repository-shaped write ports would hide read intent.

## Requirement-to-Asset Map

| Requirement | Existing support | Gap classification | Notes |
| --- | --- | --- | --- |
| Req 1: Control-plane event capture | Auth events, secure-session store, B2BUA attempts, token ledger, policy observer, traffic/usage observers. | Missing / Fragmented | Facts exist, but not as one consistent event ledger with stable event IDs/order, common redaction states, and unified correlation. |
| Req 2: Cross-session query behavior | Secure-session `Summary`, `ListAttemptEvidence`, token ledger list-by-request/attempt, diagnostics handlers. | Missing | Queries are session/request shaped; no filters across full scope dimensions, model/backend/time/outcome/reason; no cursor continuation or unsupported-filter reporting. |
| Req 3: Correlation and consistency | Trace/A-leg/B-leg/attempt seq appear in runtime, B2BUA, secure-session, token ledger, policy records. | Constraint / Partial | Correlation exists but field authority differs by subsystem; design must define precedence and avoid contradictory rollups. |
| Req 4: Scope attribution/privacy | `PrincipalScopeView`, safe auth DTOs, policy visibility gating, traffic redactors, raw capture sink policy. | Partial / Missing | Safe attribution exists in memory/observer DTOs; durable schemas mostly do not persist full scope presence; privileged/redacted/unavailable state is inconsistent. |
| Req 5: Runtime non-interference and streaming safety | Policy observers fail open; token ledger has `LedgerWriteRequired`; secure-session mandatory recording paths exist; no-retry invariant tested. | Constraint / Partial | Need recorder failure mode policy per lifecycle stage; asynchronous/outbox designs must preserve ordering and shutdown; usage observer error behavior must be audited. |
| Req 6: Retention/redaction lifecycle | Secure-session redaction profile and diagnostics redaction defaults; memory store TTL for continuity; no general retention engine. | Missing / Unknown | No generic retention job, expired markers, redacted field-state vocabulary, or aggregate-vs-detail distinction for control-plane queries. |
| Req 7: Readiness/failure visibility | Secure-session `CheckReadiness`, startup fail-closed checks, diagnostics health, runtimebundle startup errors. | Partial | No control-plane capability status (`ready/degraded/unavailable/disabled`), query error classification, or recorder backlog/failure status. |
| Req 8: Compatibility with existing diagnostics/stores | Existing diagnostics and observers are independent seams; Built exposes secure-session store and token admin. | Constraint | Design must preserve existing endpoints/observers and avoid forcing current observers to understand the new ledger. |
| Req 9: Explicit exclusions | Existing code separates policy engines, billing enforcement, UI, provider plugins. | Constraint | Query/event foundation must not become billing/rate limiting/policy UI or provider telemetry forwarding. |

## Missing Capabilities

### Data and Contract Gaps

- No canonical control-plane event type covering auth/session/attempt/usage/policy/audit with event identity, event time, source subsystem, correlation fields, scope, redaction state, and payload-summary fields.
- No stable query DTOs for session summaries, attempt history, usage rows/aggregates, policy/audit evidence, unsupported filters, bounded pages, or continuation tokens.
- No common redaction/unavailable/expired/privileged field-state vocabulary across auth, policy, usage, audit, and query rows.
- No persisted full `PrincipalScopeView` dimensions in secure-session, continuity, or token-accounting schemas; current durable identity fields are incomplete for tenant/organization/project/department/cost center/credential filters.
- No generic event identity or deterministic cursor that spans multiple event categories and backing stores.

### Persistence and Query Gaps

- Secure-session store is rich but session-centered; token ledger is request/attempt-centered; continuity is lineage-centered; auth/policy evidence is observer/log-centered.
- Durable schemas lack auth event and policy decision tables, cross-event index strategy, query-bounds enforcement, and explicit unsupported-filter handling.
- Existing token ledger lacks cross-session usage grouping by scope/session/backend/model/time/accounting plane.
- Existing secure-session attempt evidence can return chronological attempts for one session, but not cross-session attempt history by backend/model/outcome/reason/time/scope.
- Existing diagnostics endpoints use offset-like `AfterSeq` for transcript/audit and simple `limit`, not a stable cross-query continuation contract.

### Runtime and Non-Interference Gaps

- There is no central recorder that can accept evidence from auth, session, runtime attempts, token accounting, policy decisions, and audit without changing request outcomes.
- Mandatory recording is only partially represented by auth event fail-closed, secure-session mandatory recording, and token ledger required writes; there is no unified policy for which lifecycle stages can fail closed before upstream work.
- No explicit backpressure/queue/shutdown owner exists for asynchronous control-plane recording if design chooses decoupled writes.
- Recorder failure visibility currently depends on per-subsystem logs/errors; no central degraded-state diagnostics.

### Security and Privacy Gaps

- Existing safe DTOs prevent raw secret fields, but a unified persistence layer still needs field classification rules and tests to prevent raw payload/header/token storage.
- `raw_usage_json` exists in secure-session usage storage; design must decide whether it is allowed, summarized, redacted, or excluded from default control-plane query results.
- Traffic `RawCaptureSink` is explicitly privileged; control-plane query defaults must avoid treating raw capture as normal evidence.
- Current auth log sink includes safe attributes but not full scope, and custom sinks remain responsible for redaction beyond dispatcher sanitization.

### Operational Gaps

- No `control_plane` config group or capability readiness object exists.
- No operator query endpoint exists for the new cross-session views; design can define internal query ports first and optionally mount diagnostics later.
- No retention/redaction worker pattern exists for this domain; lifecycle processing needs an owner, scheduling, cancellation, and deterministic tests if implemented now.
- No documented migration path from existing fragmented data to a unified query view.

## Implementation Approach Options

### Option A: Extend Existing Components

Extend secure-session store/diagnostics, token ledger, auth event sinks, and policy observers directly until each can answer the required control-plane queries.

**Likely files/modules to extend**

- `internal/core/securesession/app/ports.go`
- `internal/core/securesession/domain/types.go`
- `internal/core/securesession/adapters/bunstore/*`
- `internal/core/securesession/adapters/diag/handlers.go`
- `internal/infra/tokenaccounting/ledgerstore/*`
- `internal/core/auth/events.go`
- `pkg/lipsdk/policydecision/*`
- `internal/infra/runtimebundle/*`

**Trade-offs**

- Pros: reuses existing tables and handlers; fastest for session and usage views; fewer new packages at first.
- Pros: can piggyback on secure-session readiness, migrations, and diagnostics authorization.
- Cons: high risk of bloating secure-session and token-accounting responsibilities beyond their domain.
- Cons: auth/policy events still need durable tables or custom sinks; cross-domain pagination and filter consistency are awkward.
- Cons: future admin/reporting features may inherit fragmented query logic.

**Fit**

Feasible for a narrow MVP, but weak fit for the requirement that evidence not remain fragmented.

### Option B: Create New Control-Plane Ledger and Query Components

Add a dedicated control-plane capability with its own event contracts, recorder port, durable adapters, query service, readiness status, and optional diagnostic/admin adapter. Existing subsystems emit or bridge evidence into it.

**Likely new components**

- `pkg/lipsdk/controlplane` or `pkg/lipsdk/evidence` for stable event/query DTOs if feature/plugin consumers need public contracts.
- `internal/core/controlplane` for event model, recorder/query ports, validation, redaction state, pagination, and readiness semantics.
- `internal/infra/controlplane/ledgerstore` for memory/Bun durable adapters and migrations.
- `internal/infra/runtimebundle/control_plane.go` for config-driven wiring and observer/sink fan-out.
- `internal/stdhttp/admin/controlplane` or diagnostics adapter if HTTP query exposure is in scope for design.

**Integration points**

- Auth dispatcher can fan out safe auth/session events to a recorder sink.
- Policy decision observer can be implemented by the control-plane recorder adapter.
- Runtime attempt/token accounting/secure-session paths can emit normalized event summaries at existing record boundaries.
- Secure-session diagnostics can remain unchanged while query service optionally reads existing store data for compatibility.

**Trade-offs**

- Pros: clean separation; purpose-built query and pagination model; easiest to protect privacy and future admin/reporting consumers.
- Pros: avoids turning secure-session into a general control-plane database.
- Cons: more design work; duplicate/derived data risk; must define consistency and transaction/outbox semantics carefully.
- Cons: needs schema, retention, readiness, and contract-test surface from scratch.

**Fit**

Strong fit for long-term requirements, especially query readiness and future enterprise consumers.

### Option C: Hybrid Projection Layer Over Existing Stores Plus New Event Ledger

Introduce a small dedicated control-plane query/recorder capability, but initially project from existing stores where they are authoritative and add new durable event tables only for facts that lack a store today, such as auth decisions and policy decisions.

**Combination strategy**

- Define one control-plane event/query contract and readiness model.
- Reuse secure-session store for session/attempt/audit/session-usage views where it already has authoritative rows.
- Reuse token ledger for raw usage rows where request/attempt evidence exists, while adding scope/session indexes through new event projections when needed.
- Add durable auth/policy event recording through observer/sink adapters.
- Add a bounded query service that composes these sources and reports unsupported filters explicitly.

**Trade-offs**

- Pros: lower initial migration risk; preserves current diagnostics and stores; adds unified query behavior incrementally.
- Pros: avoids duplicating all secure-session data immediately while still creating a coherent control-plane contract.
- Cons: query composition across stores is harder; stable pagination across heterogeneous sources needs careful design.
- Cons: source-of-truth boundaries must be explicit to prevent contradictory query results.

**Fit**

Strong brownfield fit. Likely best analysis-informed design starting point, but design must decide which evidence is authoritative in each view.

## Integration Challenges

1. **Stable cross-source pagination**: Existing stores order by session activity, attempt sequence, transcript/audit sequence, or table identity. A unified continuation token must be stable without requiring unbounded joins.
2. **Scope persistence**: Full `PrincipalScopeView` includes presence-aware values and safe maps. Durable storage needs a representation that supports filters without losing unknown vs known-empty semantics.
3. **Authority precedence**: Secure-session, continuity, token ledger, usage observer, and policy record may all describe related facts. Design must define which source wins and how partial evidence is represented.
4. **Non-interference**: Best-effort recorder failures must not affect streaming; mandatory recording must fail only before protected upstream work; post-output write failures must not trigger retries.
5. **Retention/redaction**: Deleting or redacting detailed records while preserving aggregates requires explicit record states and tests, not just SQL deletes.
6. **Migration and compatibility**: Existing secure-session diagnostics must keep their fields and behavior; new query views must not force current observers or stores to change at once.
7. **Security posture**: Query/admin exposure must reuse diagnostics trust boundaries and avoid leaking SQL/provider/raw infrastructure errors to clients.
8. **Testing matrix**: Memory, SQLite, and Postgres behavior likely need store contracts, runtime composed tests, and archtests for dependency boundaries.

## External Dependency Research

No new external dependency appears necessary for the gap-analysis phase. The repository already uses Bun, `database/sql`, and `modernc.org/sqlite`, and has Postgres-capable Bun store patterns. Design may still need focused research on:

- Cursor-token encoding and tamper handling using only stdlib primitives.
- SQLite/Postgres index strategy for scope JSON vs columnized scope dimensions.
- Whether existing Bun dialect features are sufficient for portable retention/redaction migrations and aggregate queries.

## Complexity and Risk

- Effort: XL (2+ weeks) -- the feature crosses auth, secure-session, B2BUA continuity, token accounting, policy evidence, runtime streaming, config, persistence, diagnostics, and test contracts.
- Risk: High -- the main risks are cross-source consistency, privacy regression, stream non-interference, and schema/query complexity rather than unknown libraries.

## Research Needed for Design Phase

- Decide whether the stable control-plane contract belongs in `pkg/lipsdk/controlplane` for future feature plugins or stays internal for now.
- Decide authoritative source per query view: secure-session rows, continuity rows, token ledger rows, new event ledger rows, or composed projections.
- Decide the event identity/cursor model, including ordering across event categories and stores.
- Decide scope storage shape: fully columnized fields, JSON plus generated indexes, or hybrid columns for filterable dimensions with JSON for maps/presence.
- Decide mandatory recording configuration granularity and exact fail-closed points before upstream work begins.
- Decide retention/redaction semantics for details vs aggregates and whether lifecycle processing is in this spec or deferred behind explicit APIs.
- Decide query exposure: internal query service only, diagnostics/admin HTTP endpoint, or both.
- Decide how to treat `raw_usage_json` and privileged raw-capture references in default query results.
- Decide test strategy for cross-store consistency and pagination under SQLite/Postgres without slow external dependencies in the default suite.

## Design Phase Recommendations

- Prefer Option C as the design baseline: introduce a coherent control-plane contract and query service while reusing existing authoritative stores where safe.
- Keep existing secure-session diagnostics, token ledger APIs, auth event sinks, usage observers, traffic observers, and policy observers backward compatible.
- Add a dedicated recorder/query seam rather than expanding secure-session into a generic control-plane database.
- Treat scope attribution, redaction state, unsupported filters, and continuation tokens as contract-first design items with tests before adapter work.
- Keep HTTP/admin exposure optional and protected by existing diagnostics posture until a later admin/reporting spec defines broader UX.

---

# Design Discovery Update

## Summary

- **Feature**: `control-plane-persistence-query-event-ledger`
- **Discovery Scope**: Complex Integration / Extension
- **Key Findings**:
  - Existing auth, policy, usage, secure-session, B2BUA, and token-accounting paths already expose safe evidence seams, but none provides a unified event identity, availability state, or cross-session query contract.
  - Existing Bun, SQLite, Postgres, runtimebundle, storecontract, and diagnostics patterns are sufficient; no new third-party dependency is needed.
  - The selected design uses a dedicated control-plane contract and core query/recorder service, with adapters/decorators around existing seams to avoid bloating secure-session or token-accounting ownership.

## Research Log

### Runtime Integration Seams
- **Context**: Design needs to capture events without changing runtime outcomes.
- **Sources Consulted**: `internal/infra/runtimebundle/options.go`, `internal/infra/runtimebundle/build.go`, `internal/infra/runtimebundle/auth_events.go`, `internal/stdhttp/server.go`, `pkg/lipsdk/policydecision/observe.go`, `pkg/lipsdk/usage/observe.go`.
- **Findings**:
  - Runtimebundle already owns explicit construction and can wrap auth sinks, policy observers, usage observers, secure-session stores, and B2BUA stores.
  - Policy observer chains ignore child errors; auth event dispatch can fail open or fail closed by existing config; usage observers can return errors and require careful adapter behavior.
  - stdhttp already mounts protected diagnostics/admin handlers behind diagnostics posture.
- **Implications**:
  - Control-plane recording belongs in runtimebundle-composed adapters, not in provider/front-end paths.
  - Recording policies must respect each existing seam's failure semantics and fail closed only before protected upstream work.

### Storage and Query Patterns
- **Context**: Requirements need durable event identity, filters, pagination, retention, and readiness.
- **Sources Consulted**: `internal/core/securesession/app/ports.go`, `internal/core/securesession/adapters/bunstore/*`, `internal/core/continuity/bunstore/*`, `internal/infra/tokenaccounting/ledgerstore/*`, `internal/core/securesession/storecontract/contract.go`.
- **Findings**:
  - Bun migrations, SQLite/Postgres DDL, readiness checks, closers, and optional Postgres tests already have established patterns.
  - Existing secure-session and token-accounting stores cannot satisfy full control-plane query requirements without overloading their domains.
  - Store contract tests are the preferred validation shape for adapter parity.
- **Implications**:
  - Add a new control-plane store port plus memory/Bun adapters and contract tests.
  - Store safe scope filter dimensions as explicit queryable fields while preserving full safe scope for result reconstruction.

### Query Contract Shape
- **Context**: Later admin/reporting/budget consumers need stable query behavior without knowing backing sources.
- **Sources Consulted**: requirements, gap analysis, `pkg/lipsdk/scope`, secure-session diagnostics query behavior.
- **Findings**:
  - Existing diagnostics are session-shaped; new queries need bounded pages, opaque continuation, unsupported-filter reporting, and disabled-vs-empty distinction.
  - `PrincipalScopeView` already solves safe attribution and unknown-vs-known-empty semantics.
- **Implications**:
  - Define `pkg/lipsdk/controlplane` as the stable event/query/status contract.
  - Keep query consumers store-agnostic and expose evidence state/source limitations explicitly.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| Extend existing stores | Add query/event features to secure-session, token ledger, auth sinks, and policy observers | Fast for narrow session/usage views | Bloats existing domains; pagination and filter semantics remain fragmented | Rejected as primary design |
| Dedicated ledger | New control-plane event store and query service owns all evidence projections | Clean contract and future consumer story | More schema and adapter work; duplicate projection risk | Strong long-term fit |
| Hybrid projection | New control-plane contract/service with adapters over existing source seams and dedicated event store for query-ready evidence | Fits brownfield seams, preserves compatibility, avoids domain bloat | Requires authority/source-state rules and careful dedupe | Selected design baseline |

## Design Decisions

### Decision: Public SDK Contract for Control-Plane Evidence
- **Context**: Requirements 9.1-9.5 need future feature consumers to use query results without knowing backing stores.
- **Alternatives Considered**:
  1. Internal-only core DTOs.
  2. Add types to existing auth/policy/usage packages.
  3. New `pkg/lipsdk/controlplane` package.
- **Selected Approach**: Add `pkg/lipsdk/controlplane` for event, detail, query, page, cursor, status, and stable error DTOs.
- **Rationale**: Keeps contracts additive, protocol-neutral, and available to feature plugins without leaking core/storage details.
- **Trade-offs**: Adds a new public package that must be kept minimal and versionable.
- **Follow-up**: Public surface tests must pin package exports and secret-safe fields.

### Decision: Runtimebundle-Composed Source Adapters
- **Context**: Evidence must be captured without modifying provider/front-end paths or changing existing observers.
- **Alternatives Considered**:
  1. Add recording calls directly throughout runtime methods.
  2. Replace existing observers/stores.
  3. Wrap/fan-out existing seams in runtimebundle.
- **Selected Approach**: Runtimebundle constructs control-plane source adapters for auth, policy, usage, secure-session, and B2BUA seams.
- **Rationale**: Preserves explicit wiring and keeps source semantics owned by existing components.
- **Trade-offs**: Decorator behavior and fail-closed points need strong tests.
- **Follow-up**: Add compatibility tests for existing observers and diagnostics.

### Decision: Dedicated Store Port with Memory and Bun Adapters
- **Context**: Requirements demand stable event identity, pagination, filters, retention, and readiness.
- **Alternatives Considered**:
  1. Query existing stores directly.
  2. Persist all evidence in secure-session tables.
  3. Add a dedicated control-plane store.
- **Selected Approach**: Add a consumer-owned control-plane `Store` port with memory and Bun-backed adapters.
- **Rationale**: Enables cross-session query semantics without distorting secure-session/token-accounting stores.
- **Trade-offs**: Projection duplicates selected source facts; source keys and availability states must prevent contradictions.
- **Follow-up**: Contract tests must cover dedupe, pagination, unsupported filters, and retention.

### Decision: Build Opaque Cursor and Query Bounds In-House
- **Context**: No existing dependency is needed for bounded continuation and query-shape binding.
- **Alternatives Considered**:
  1. Add a cursor/token library.
  2. Use plain offset pagination.
  3. Use stdlib-encoded opaque cursor tied to event ordering and query shape.
- **Selected Approach**: Build cursor encoding with stdlib primitives in the core query service.
- **Rationale**: Avoids dependency growth and prevents unstable offset behavior under append-only event streams.
- **Trade-offs**: Implementation must be carefully validated and fuzzed.
- **Follow-up**: Add cursor tamper/shape-mismatch tests and fuzzing where practical.

## Risks & Mitigations

- Cross-source contradiction risk - mitigate with source event keys, explicit source/availability states, and contract tests.
- Privacy regression risk - mitigate with SDK field review, normalizer validation, secret fixture tests, and default redaction.
- Streaming regression risk - mitigate with runtime non-interference tests around post-output failures and pre-work mandatory failures.
- Query performance risk - mitigate with required bounds, indexes, max page sizes, and store contract tests.
- Scope semantics loss - mitigate with presence-aware scope columns and round-trip tests for unknown vs known-empty values.

## References

- `.kiro/steering/product.md` - control-plane product promise and evidence expectations.
- `.kiro/steering/tech.md` - Go, persistence, runtime composition, streaming, and startup posture rules.
- `.kiro/steering/structure.md` - package ownership and where to change code.
- `.kiro/steering/routing-and-orchestration.md` - B2BUA lineage and no-retry-after-output invariants.
- `.kiro/steering/api-standards.md` - canonical/protocol boundary and provider SDK leakage rules.
