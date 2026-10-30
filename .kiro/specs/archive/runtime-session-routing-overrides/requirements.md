# Requirements Document

## Introduction

Go-LIP currently derives each logical request's route from the canonical `lipapi.Call.Route.Selector`, then resolves aliases, parses the selector, applies model-only defaults, evaluates dynamic selector state, negotiates capabilities, and opens one or more B-legs. The authoritative client session is anchored by the core-owned A-leg. One logical turn may consume several B-legs because of failover, parallel routing, TTFT behavior, or interleaved `[thinker]` execution.

Proxy operators need a runtime control that can replace a selected session's client-supplied routing string without disconnecting the client, cancelling work, or rebuilding the proxy. The override must be **sticky**: after an administrator sets it, every later turn on that A-leg uses it until an administrator replaces it with a newer override or clears it. Replacement is latest-wins. Clearing restores normal behavior for later turns, using the routing string supplied by the client/agent on each such turn.

The critical concurrency invariant is turn-level snapshot isolation. An admitted logical turn snapshots exactly one override revision (or the absence of an override) after the authoritative A-leg is known. That immutable routing authority is then used by every B-leg belonging to that turn, including failover/race/recovery/interleaved B-legs. An administrator mutation committed after the snapshot cannot alter that turn; it becomes visible to the next turn whose snapshot occurs after the mutation commits.

## Boundary Context

- **In scope:** A-leg-scoped runtime route-override state; set/replace/get/clear operator operations; latest-wins revision semantics; turn-level immutable snapshots; full existing selector-language support; validation through normal routing semantics; memory and durable continuity persistence; config-generation reload compatibility; protected admin HTTP surface; bounded diagnostics/audit metadata; concurrency/race coverage; explicit interaction with `[first]`, `[thinker]`, affinity, failover, parallel, TTFT and route hints.
- **Out of scope:** automatic cost-based model selection; budgets or policy engines that autonomously create overrides; per-B-leg overrides inside an already admitted turn; retroactively rerouting/cancelling active B-legs; client-facing APIs for setting overrides; provider-specific routing syntax; changing selector grammar; changing alias rules; changing capability negotiation; changing failover/retry semantics; changing secure-session authority; changing accounting/usage/concurrency authority; building a GUI; scheduling/TTL expiry of overrides; principal-wide/global override rules.
- **Primary ownership:** core owns override authority and routing integration; continuity adapters own persistence; `internal/stdhttp/admin` owns the operator HTTP adapter; frontends/backends remain unaware of the feature.
- **Canonical boundary:** runtime admin state is not part of the client canonical request model and shall not be added to `pkg/lipapi.Call` or forwarded to providers.
- **Persistence boundary:** override durability follows the A-leg continuity backend: process-memory continuity is process-lifetime state; SQLite/PostgreSQL continuity preserves active/inactive revision state across process restart while the A-leg exists.
- **Revalidation triggers:** A-leg continuity store contracts, route-plan construction, pre-routing mutation order, selector aliases/parser/planner, interleaved-thinking state, generation reload, secure-session diagnostics/admin security posture, and streaming/failover non-interference.

## Requirements

### Requirement 1: Sticky A-Leg-Scoped Routing Override

**Objective:** As a proxy administrator, I want to replace the routing string for one active client session, so that all later turns use an operator-selected route without requiring client cooperation.

#### Acceptance Criteria

1.1. When an administrator activates a routing override for an existing A-leg, the system shall associate the override with that A-leg rather than with a connection, frontend protocol, B-leg, backend, process request, or client-supplied session hint.

1.2. While an A-leg has an active routing override, every later logical turn that snapshots that active revision shall use the override as its authoritative route selector instead of the route selector supplied by the client/agent for that turn.

1.3. While an active override remains unchanged, the system shall continue applying it to subsequent turns for the same A-leg until an administrator replaces or clears it.

1.4. When the same authoritative session is resumed through a different supported frontend or transport while retaining the same A-leg, the system shall continue to apply the active A-leg override.

1.5. The system shall not require the client to reconnect, restart its coding agent, change its model setting, or receive a proxy restart for an already-enabled admin surface to apply an override.

1.6. The system shall not create a second client-visible session identity or new A-leg merely because an override is set, replaced, read, or cleared.

### Requirement 2: Replace, Latest-Wins, and Clear Semantics

**Objective:** As a proxy administrator, I want to change my routing decision repeatedly and remove it later, so that runtime control remains reversible and the latest administrative intent wins.

#### Acceptance Criteria

2.1. When an administrator replaces an active override with a different valid routing string, the system shall commit a newer A-leg override revision and make that routing string authoritative for turns that snapshot after the commit.

2.2. When multiple override mutations target the same A-leg, the system shall serialize committed state transitions so that each observed state has one monotonically increasing revision and the latest committed state is authoritative for subsequent snapshots.

2.3. If two administrator mutations race, the system shall produce a deterministic total order of committed revisions; a turn shall observe either the complete earlier revision or the complete later revision, never a torn/mixed selector state.

2.4. When an administrator clears an active override, the system shall commit an inactive state newer than the previously active revision and subsequent turns shall stop using the prior override.

2.5. While the override state is inactive after a clear, each later turn shall use the routing selector currently supplied by that turn's client/agent, subject to the same existing non-admin mutation, alias, parsing, routing and policy behavior as a session that never had an override.

2.6. If an administrator clears an A-leg that already has no active override, the clear operation shall be idempotent and shall leave the effective state inactive.

2.7. If an administrator sets the same normalized selector that is already active, the operation shall be idempotent with respect to effective routing state and shall not create an unbounded sequence of semantically identical revisions.

2.8. When a set/replace/clear operation succeeds, the admin surface shall return the resulting A-leg override state, including whether it is active and its current revision, so an operator can determine which mutation committed.

### Requirement 3: Turn-Level Snapshot Isolation and In-Flight Non-Interference

**Objective:** As an operator and client user, I want runtime changes to affect future turns only, so that active streams, failovers, and tool cycles are never disturbed mid-turn.

#### Acceptance Criteria

3.1. When a logical turn resolves its authoritative A-leg, the system shall snapshot one complete override state before route planning for that turn.

3.2. While a turn is using a snapshotted override state, every B-leg opened for that turn shall use the same effective selector and override revision, including B-legs opened later because of pre-output failover, parallel races, TTFT fallback, or interleaved-thinking continuation.

3.3. When an administrator commits a new override while a turn is already using an older snapshot, the system shall not mutate that turn's route-plan AST, candidate set, baseline call, retry stream, B-leg lineage, backend stream, or connection.

3.4. When an administrator clears an override while a turn is in flight, the system shall not revert any B-leg belonging to that turn to the client selector.

3.5. When a set/replace/clear operation has committed and returned success, any later turn whose override snapshot begins after that commit shall observe the resulting revision/state.

3.6. The system shall not cancel, close, drain, restart, migrate, or recreate an in-flight B-leg merely because override state changes.

3.7. The existing rule prohibiting transparent failover after the first client-visible output shall remain unchanged by route overrides.

3.8. The test suite shall prove race-free set/replace/clear/read/turn-snapshot behavior under the Go race detector where supported.

### Requirement 4: Full Existing Routing-String Semantics

**Objective:** As a proxy administrator, I want an override to accept anything the proxy normally accepts as a model routing string, so that the admin feature does not create a reduced routing language.

#### Acceptance Criteria

4.1. While an override is active for a turn, the system shall send its selector through the same alias-resolution, selector parsing, model-only defaulting, native-model binding, capability negotiation, candidate health, affinity, attempt-budget, and backend-open pipeline used by an equivalent client-supplied selector.

4.2. The admin surface shall accept direct model/backend routes, model-only routes, configured aliases, ordered failover, weighted routing, parallel races, supported route parameters, TTFT controls, affinity controls, `[first]`, `[thinker]`, and other selector forms accepted by the current routing parser/planner.

4.3. The system shall store the administrator's bounded normalized **raw selector string**, not a pre-expanded alias result or preselected backend/model, so later turns execute normal routing semantics rather than a second routing implementation.

4.4. When alias configuration changes through a successful runtime generation reload, a stored override shall be interpreted by the alias resolver and routing configuration of the generation that admits each later turn, exactly as a client selector would be.

4.5. If a previously valid stored override becomes invalid or unresolvable under a later runtime generation, the affected later turn shall fail through the normal route-validation/planning error path rather than silently falling back to the client selector or an older expanded route.

4.6. The admin write path shall validate selector size and route acceptability using the routing semantics of the runtime generation that admits that admin request, without opening a backend, allocating a B-leg, starting a provider process, or consuming model usage; an admin write admitted after a successful generation publish shall therefore use the newly published generation rather than a retired generation validator.

4.7. The maximum stored override selector size shall not exceed the canonical `lipapi.MaxRouteSelectorBytes` bound, and admin request-body limits shall remain bounded independently of client request-body limits.

4.8. While an override is active, the system shall not bypass hard capability requirements, model-catalog eligibility, transport negotiation, authentication, accounting, usage/concurrency authority, security policy, or other core admission rules that an equivalent client route must satisfy.

### Requirement 5: Override Precedence Without a Second Routing Engine

**Objective:** As a maintainer, I want one authoritative selector source per turn, so that admin control is reliable without duplicating routing logic.

#### Acceptance Criteria

5.1. While a turn has an active override snapshot, the final selector supplied to core route planning shall be the snapshotted admin selector rather than the client-provided selector.

5.2. The system shall apply the active override at a core-owned routing-authority boundary after the A-leg is authoritative and before route hinting/route planning can derive candidate choices from a different selector.

5.3. If a pre-routing request mutation attempts to replace `Route.Selector` while an active admin override is authoritative for the turn, the system shall reassert the snapshotted admin selector before route hinting and route planning.

5.4. The route-hint stage shall remain advisory candidate preferences and shall not replace or clear an active admin selector.

5.5. When no active override is snapshotted, the current client/hook/route-hint behavior shall remain byte-for-byte/semantically compatible with existing routing behavior.

5.6. The implementation shall reuse the existing `internal/core/routing` alias/parser/planner and shall not introduce an admin-specific selector grammar, backend switch, provider branch, or frontend-specific routing path.

### Requirement 6: Preserve Existing Session-Dynamic Routing State

**Objective:** As an operator, I want advanced selectors to retain their normal A-leg semantics, so that override lifecycle operations do not reset or fork existing dynamic routing state machines.

#### Acceptance Criteria

6.1. When an override containing `[first]` is activated on an A-leg, the selector shall observe the existing `WeightedFirstConsumed` state for that A-leg rather than treating override activation as a new session.

6.2. When an override is set, replaced, or cleared, the system shall not reset `WeightedFirstConsumed`.

6.3. When an override contains `[thinker]`, the selector shall use the existing A-leg-scoped interleaved-thinking state and memo machinery rather than a separate admin-specific state store.

6.4. When an override is set, replaced, or cleared, the system shall not delete or reset interleaved-thinking state; any memo/cycle behavior shall follow the same rules that apply when a client changes its selector between turns.

6.5. When an override is set, replaced, or cleared, the system shall not delete or reset session/client affinity state; later planning shall apply the existing affinity semantics to the effective selector.

6.6. The implementation shall characterize selector changes across direct, weighted, failover, race, `[first]`, `[thinker]`, and affinity cases to prove that admin substitution changes only selector authority and does not fork planner state machines.

### Requirement 7: Continuity-Aligned Persistence and Lifecycle

**Objective:** As an operator, I want override state to follow the selected session's continuity lifecycle, so that runtime behavior is predictable across reloads and supported restarts.

#### Acceptance Criteria

7.1. The in-memory continuity implementation shall retain override state under the same A-leg lifecycle/eviction boundary as the A-leg and shall remove it when that A-leg is removed; successful override Snapshot/Get/Replace/Clear operations shall follow existing A-leg access semantics by refreshing `LastSeenAt`, while override `Revision`/`UpdatedAt` change only when the effective override state changes.

7.2. The SQLite/PostgreSQL continuity implementation shall persist active/inactive override state and revision atomically with A-leg ownership so that a surviving A-leg retains its override state across process restart; successful override Snapshot/Get/Replace/Clear operations shall refresh A-leg `LastSeenAt` consistently with the existing durable continuity access contract, while idempotent override operations shall not churn override `Revision`/`UpdatedAt`.

7.3. When an A-leg is deleted, replaced because of continuity-key recreation, or otherwise removed by existing continuity lifecycle rules, its override state shall not survive as an orphan that can apply to a different future A-leg.

7.4. When runtime configuration reloads, the system shall not copy override state into immutable generation objects; generations shall read the process-owned continuity authority at the per-turn snapshot boundary.

7.5. When a new generation is published, in-flight turns shall retain their already-snapshotted override revision and new turns shall read current A-leg override state using the new generation's routing semantics.

7.6. Where multiple process instances share the same supported durable continuity database, override reads shall not rely on an unbounded process-local cache that can keep subsequent turns on stale revisions.

7.7. The feature shall not require changing the base public `pkg/lipsdk/continuity.Store` contract solely to add operator-only mutable state; a focused optional/internal persistence seam shall be preferred unless implementation evidence proves a public contract is necessary.

### Requirement 8: Protected Runtime Admin Operations

**Objective:** As a proxy administrator, I want a bounded authenticated runtime API to inspect, replace, and clear session overrides safely.

#### Acceptance Criteria

8.1. Where routing-override administration is not explicitly enabled, the standard distribution shall expose no mutation endpoint for this feature, shall not implicitly clear or deactivate an already-persisted override, and shall continue to read/enforce already-persisted override state through the standard continuity-backed runtime reader.

8.2. Where routing-override administration is enabled, the standard distribution shall provide protected operations to get current override state, set/replace the selector for an A-leg, and clear the override for an A-leg.

8.3. The write target shall be an authoritative proxy A-leg ID; client-supplied session hints shall not be accepted as authority for mutating another session's routing state.

8.4. If the target A-leg does not exist, the admin operation shall fail with a bounded not-found response and shall not create a new A-leg or latent override.

8.5. The admin surface shall reuse the repository's operator-security posture and shall require the configured operator secret/strong protection whenever the existing non-loopback rules require it.

8.6. The admin request decoder shall enforce method, supported JSON content type, body-size, selector-size, JSON-shape, and unknown-field bounds before mutation, rejecting missing/unsupported PUT media types before JSON decoding.

8.7. If a selector mutation is invalid, the admin service shall fail atomically: the previous override state/revision shall remain authoritative and no partial state shall be stored.

8.8. The admin set/replace/clear operations shall not depend on any concrete frontend or backend package and shall not forward admin requests through a client LLM protocol handler.

8.9. The API shall support unconditional last-committed-wins replacement; optimistic compare-and-swap preconditions may be added later but shall not be required for the base user story.

### Requirement 9: Diagnostics, Auditability, and Secret-Safe Output

**Objective:** As a proxy administrator, I want to know whether a turn was affected by an override and which revision applied, so that routing/cost investigations remain explainable.

#### Acceptance Criteria

9.1. The protected override-state read operation shall return the A-leg ID, active/inactive state, current revision, update time when the revision is non-zero, and the raw selector only when the override is active and the caller is authorized.

9.2. When a turn is executed, bounded routing diagnostics shall make it possible to determine whether an admin override was active and which override revision was snapshotted without requiring raw prompt content.

9.3. The B-leg attempt lineage shall continue to record the actual backend/effective model selected after normal routing, regardless of whether the selector source was client or admin.

9.4. The structured mutation logs/audit records shall distinguish set/replace/clear outcomes and revision but shall not emit the raw selector into ordinary logs by default; a digest/length or protected admin response is sufficient.

9.5. The metrics labels shall not contain raw selector strings, A-leg IDs, model-route expressions, or other unbounded/high-cardinality values.

9.6. The admin errors shall not leak provider credentials, config secrets, raw resume tokens, or unrelated session data.

### Requirement 10: Brownfield Compatibility, Testing, and Architectural Guardrails

**Objective:** As a maintainer, I want the feature to integrate with current Go-LIP boundaries and TDD conventions, so that runtime control does not destabilize routing or grow protocol-specific maintenance cost.

#### Acceptance Criteria

10.1. The implementation shall begin with RED characterization/contracts for current no-override routing, A-leg continuity, set/replace/clear state ordering, and in-flight non-interference before production behavior is changed.

10.2. When the target A-leg has no active override, existing client requests shall behave identically to current routing behavior regardless of whether the admin surface is enabled.

10.3. The frontend plugins shall continue only to decode client routing strings into canonical `RouteIntent`; they shall not learn about or implement admin overrides.

10.4. The backend plugins/connectors shall continue to receive the attempt-local canonical call selected by core; they shall not read override state or admin metadata.

10.5. The `pkg/lipapi` contract shall remain protocol-neutral and shall not gain an admin-override field, revision, HTTP DTO, or operator authorization concept for this feature.

10.6. The core shall not import provider SDKs or concrete plugins, and the admin adapter shall depend on a narrow core service/port rather than internal routing implementation details where a seam is required.

10.7. The non-streaming behavior shall remain collection over the canonical stream; no separate override execution path shall be introduced.

10.8. The test suite shall cover memory and durable-store behavior, A-leg liveness refresh, delete/recreate-versus-mutation races, admin HTTP protection/validation and fixed wire semantics, generation reload including PUT-after-reload validation, disabled-admin-surface enforcement of persisted state, multiple frontends sharing one A-leg, update/clear races, failover/race B-legs, post-output non-retry, `[first]`, `[thinker]`, affinity, alias changes, invalidation after reload, and restoration of current client routing after clear.

10.9. The implementation shall run focused routing/runtime/store/admin tests plus architecture, unit, race where practical, and repository quality gates appropriate to the touched boundaries before the spec can be marked complete.

10.10. The implementation workflow shall not treat any implementation task as authorized until `requirements`, `design`, and `tasks` approvals and `ready_for_implementation` are explicitly set in `spec.json`.
