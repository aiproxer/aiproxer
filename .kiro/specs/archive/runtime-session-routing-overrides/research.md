# Research and Design Discovery

## Research Scope

This discovery supports the brownfield design for `runtime-session-routing-overrides`. It focuses on the current Go-LIP request/continuity/routing lifecycle, the safest concurrency boundary for runtime mutation, reuse of existing admin/security patterns, and avoidance of a second routing implementation.

Repository baseline: `main` at `294fa587b902fa0989adab8ad0a16f6ab001c33e`.

No new third-party runtime dependency is required.

## Key Repository Findings

### 1. Canonical route input is intentionally opaque

`pkg/lipapi/call.go` defines:

- `RouteIntent{Selector string}`;
- `Call.Route RouteIntent`.

The API layer does not understand selector grammar. `pkg/lipapi/limits.go` caps `Route.Selector` at `MaxRouteSelectorBytes = 64 * 1024`. This is the right contract to preserve: admin state should provide an alternate selector source, not add parsed routing concepts to `lipapi`.

### 2. Route compilation is already centralized

`internal/core/runtime/executor_route_plan.go` currently:

1. trims `prep.baseline.Route.Selector`;
2. resolves configured aliases;
3. calls `routing.Parse`;
4. applies model-only backend defaulting;
5. binds native model IDs;
6. derives affinity identity;
7. loads A-leg interleaved state;
8. builds one `routePlanState` for the request.

This path is the only correct place to interpret admin selectors. A separate admin parser, pre-expanded route object, or preselected backend/model would diverge from normal behavior.

### 3. The route plan lifetime already gives B-leg isolation

The parsed selector, attempt budget, TTFT budget, session routing state, exclusions, affinity identity and interleaved state are held in request-local `routePlanState` / retry stream state. Failover and interleaved continuation reuse that state instead of rebuilding from a mutable global selector.

Design consequence: snapshot mutable override state **before** `buildRoutePlan`; after that, do not read override state again anywhere in the B-leg lifecycle. This directly satisfies “do not disturb ongoing B-legs.”

### 4. Authoritative A-leg resolution happens before planning

`internal/core/runtime/executor_prepare_secure.go` calls secure-session `BeginTurn`, receives the proxy-owned A-leg, fetches it from the B2BUA store, populates the secure session view, runs submit/pre-routing stages, and finally clones `work` into the baseline used by route planning.

This provides a clean linearization window:

- before A-leg authority: override cannot be safely selected;
- after A-leg authority but before route planning: override can be snapshotted once;
- after route-plan construction: override must be immutable for this turn.

### 5. Mutation stages create a precedence hazard

The secure prepare path currently runs submit hooks, tool-catalog filters, request transforms, pre-request handlers and route hinting before freezing the baseline. Some brownfield stages can mutate the canonical call.

If admin override were written into `work.Route.Selector` only once immediately after A-leg resolution, a later request mutation could overwrite it and existing client-input evidence could misattribute the admin route to the client. The chosen design therefore snapshots override state early but keeps the client/work call intact through existing pre-routing mutation/evidence stages. Immediately before route hinting, runtime clones an effective routing call and **asserts the frozen admin selector on that copy**; that effective call becomes the routing baseline.

### 6. Route hints are advisory, not authoritative

Routing steering explicitly describes route hints as advisory preferences. Reusing `routehint` for an admin override would be semantically wrong: a preference may influence candidate order, whereas the user story requires replacing the selector string itself. The feature belongs to core routing authority, not a route-hint plugin.

### 7. Dynamic routing state is already A-leg state

`internal/core/b2bua.ALegRecord` stores `WeightedFirstConsumed`. `internal/core/runtime/interleaved_open.go` loads/stores thinker cycle state through optional `b2bua.InterleavedStateStore`; memo scope is the A-leg ID. `internal/core/interleavedthinking.MemoState` records `SourceSelector`, but current shaping does not use route changes as an automatic reset trigger.

Design consequence: set/replace/clear must not invent reset behavior. `[first]`, `[thinker]`, memo injection and affinity should behave exactly as they do when a client changes its own selector between turns.

### 8. Base continuity interface is a compatibility boundary

`internal/core/b2bua.Store` is intentionally mirrored by `pkg/lipsdk/continuity.Store`; `internal/core/continuity/sdkwrap.go` bridges them, and contract tests guard drift. Adding operator-specific methods to `b2bua.Store` would force a public SDK change.

Preferred design: a focused optional/internal `RouteOverrideStore` contract implemented by the standard memory/Bun continuity stores. The base continuity interface remains unchanged.

### 9. Continuity stores already provide the correct durability classes

- `b2bua.MemoryStore`: mutex-protected A-leg state with TTL/max-leg eviction.
- `internal/core/continuity/bunstore`: SQLite/PostgreSQL-backed A-leg/B-leg/attempt state and schema migrations.

Override state should follow these same lifecycles. A process-local side map would create orphan/lifetime problems and would not propagate through a shared PostgreSQL store.

### 10. Process services outlive request generations

`internal/infra/runtimebundle.ProcessServices` owns `Continuity b2bua.Store` once per process. Runtime reload publishes immutable `GenerationRuntime` values for new admissions while in-flight work retains its generation.

This exactly matches the feature's two authorities:

- mutable per-A-leg override state: process continuity;
- selector interpretation (aliases/default backend/registry/catalog): the generation that admits the turn.

Stored overrides therefore remain raw and are reinterpreted under each new generation rather than copied into generation state.

### 11. Existing admin/security patterns are reusable

`internal/stdhttp/admin/*` already hosts operator handlers. `diag.WrapDiagnosticsProtect` requires `X-LIP-Diagnostics-Secret` when configured, and `config.ValidateProtectedDiagnosticsPosture` rejects protected surfaces on non-loopback binds without a sufficient secret. Token-accounting and control-plane admin mounts demonstrate narrow service injection and opt-in exposure.

The config-reload management listener has stronger dedicated management posture, but it is intentionally specialized (`internal/stdhttp/admin/configreload`). Turning it into a general management plane would be a broader architectural project. This spec keeps scope bounded by using the existing protected standard-admin pattern.

### 12. A-leg discovery already exists

Secure-session diagnostics expose session summaries including A-leg IDs and a `by-a-leg` lookup. The mutation API can therefore require the authoritative A-leg ID directly and avoid accepting client session hints as write authority.

## External Research

### Go concurrency memory model

The Go Memory Model requires concurrent mutable state to be serialized with synchronization primitives (or otherwise made data-race-free) and documents synchronization ordering for mutexes/atomics. This supports a simple design: memory-store override state changes occur under the store mutex; each turn obtains a copied immutable value under the same synchronization boundary. No lock-free multi-field selector/revision structure is justified.

Source: https://go.dev/ref/mem

### HTTP method semantics

RFC 9110 defines PUT and DELETE as idempotent methods. That maps well to the operator API:

- `PUT /admin/routing-overrides/{a_leg_id}` means “make this resource's active selector equal to this value”;
- `DELETE /admin/routing-overrides/{a_leg_id}` means “make this override inactive.”

The state-changing revision advances only when the effective resource state changes, so repeating an identical PUT or DELETE is idempotent at the feature level.

Source: https://www.rfc-editor.org/rfc/rfc9110

## Chosen Architecture Pattern

**Pattern:** A-leg-scoped mutable control resource + immutable per-turn snapshot + existing routing compiler.

This is a small application-service seam, not a new framework:

- **Domain state:** active/inactive selector override and monotonic revision bound to A-leg.
- **Application orchestration:** validate current-generation selector, atomically replace/clear state, snapshot once per turn, reassert selector authority before route hinting/planning.
- **Driving adapter:** protected admin HTTP GET/PUT/DELETE.
- **Driven adapters:** in-memory and Bun continuity stores.
- **Composition root:** process services create the override service from continuity; each generation injects its route validator and runtime reader into appropriate adapters/executor.

## Design Decisions

### D1 — A-leg is the only routing-override anchor

Use authoritative A-leg ID as the key. Do not key by TCP/WebSocket connection, frontend instance, client hint, backend, B-leg, or trace ID.

### D2 — Override state is revisioned and can be inactive

State contains at minimum:

- `ALegID`;
- `Active`;
- raw normalized `Selector` when active;
- monotonically increasing `Revision`;
- `UpdatedAt`.

A set/replace/clear that changes effective state increments revision atomically. Repeated identical PUT/DELETE is a no-op with the current revision.

### D3 — One snapshot per logical turn

After the authoritative A-leg is known, read the complete override state once and copy it into request-local preparation state. No B-leg/retry/interleaved path reads mutable override state.

### D4 — Store raw selector, not expanded alias/AST

The write path preflights against the current generation for fast operator feedback, but the durable authority is the raw selector. Every later turn runs its generation's normal alias/parser/default/backend binding path. This preserves ordinary behavior across config reload.

### D5 — Admin selector has final selector-source precedence

The active snapshot is asserted on the cloned effective-routing call immediately before route hinting/baseline freeze. This prevents a brownfield request mutation from accidentally undoing the administrator's explicit route replacement while preserving the original work call for client evidence. It does **not** bypass capability, catalog, auth, security, accounting, usage/concurrency or backend-admission rules.

### D6 — Keep dynamic route state untouched

Override lifecycle does not reset `[first]`, `[thinker]`, memo or affinity state. It changes selector authority only.

### D7 — Optional continuity store contract, not base SDK expansion

Add an internal/focused `RouteOverrideStore` seam. Standard memory and Bun stores implement it. `pkg/lipsdk/continuity.Store` remains unchanged.

### D8 — Process-owned service, generation-owned validation

The mutation/state service is process-scoped because persistence is process continuity. Selector preflight uses a narrow validator from the currently active generation so aliases/default backend/registry are current at write time.

### D9 — Protected opt-in admin HTTP resource

Default endpoint shape:

- `GET /admin/routing-overrides/{a_leg_id}`;
- `PUT /admin/routing-overrides/{a_leg_id}` with `{"selector":"..."}`;
- `DELETE /admin/routing-overrides/{a_leg_id}`.

The exact prefix remains typed config and must be collision-validated/mounted like other admin surfaces. It is disabled by default and protected using existing operator posture.

### D10 — Durable state follows A-leg lifecycle

Memory state lives inside/alongside the A-leg state and is evicted with it. Durable state uses A-leg referential ownership and cascade/transaction semantics. No orphan override may survive A-leg deletion/replacement.

### D11 — Write validation performs no execution

Preflight checks bound/UTF-8/JSON and current-generation selector acceptance, but cannot allocate B-legs, call providers, start connectors, consume tokens, mutate `[first]`, affinity or thinker state, or create sessions.

### D12 — Diagnostics carry source/revision, not selector cardinality

Protected GET may return raw selector. Runtime diagnostics record source=`admin_override` plus revision; normal logs use action/revision/digest/length, and metrics stay low-cardinality.

## Rejected Alternatives

### Mutate an in-flight `routePlanState`

Rejected because it breaks snapshot semantics, can change failover/race candidates mid-turn, and risks post-output routing violations.

### Re-read override before every B-leg

Rejected because a failover B-leg could use a different selector revision than the original B-leg of the same logical turn.

### Store the parsed selector AST or expanded alias

Rejected because it creates persistence/versioning problems and makes admin routes behave differently from client routes after alias/default/backend changes.

### Put override state on immutable generation runtime

Rejected because admin mutations would either mutate published generation objects or require generation rebuild/reload for every operator action.

### Implement as request-transform/route-hint feature plugin

Rejected because route hints are advisory and request transforms are plugin mutation seams, not core routing authority. The user story requires an authoritative per-session control resource with strict in-flight semantics.

### Add methods to base `b2bua.Store` / public `pkg/lipsdk/continuity.Store`

Rejected as unnecessary public compatibility expansion. Use an optional focused contract.

### Generalize config-reload management server now

Rejected because it broadens a feature into management-plane architecture work. A later spec can converge operator listeners if there is a broader requirement.

## Key Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Override update changes a failover B-leg inside an active turn | Snapshot once before route planning; never re-read in B-leg code. |
| Hook/request transform overwrites admin selector | Reassert frozen admin selector at final route-authority boundary. |
| Durable store races produce torn selector/revision | One transaction/mutex mutation with monotonic revision. |
| Reload changes alias meaning | Store raw selector; reinterpret per generation; fail normally if no longer valid. |
| Public SDK compatibility churn | Optional/internal store seam. |
| Stale state after A-leg deletion | A-leg-owned memory state / FK cascade or transactional delete. |
| Cross-process stale cache | Authoritative store read per turn; no indefinite local cache. |
| Selector leaks into logs/metrics | Raw only in protected GET; digest/length elsewhere. |
| `[thinker]`/`[first]` state reset accidentally | Explicit non-reset requirements + characterization tests. |
| Admin validation causes provider work | Side-effect-free route preflight contract and tests with upstream probes. |

## Implementation Investigation Items

These are implementation-level details, not unresolved architecture choices:

1. Choose exact migration naming/table shape for Bun (separate one-to-one override table is preferred over widening the hot A-leg row if benchmarks/schema simplicity support it).
2. Select the narrowest generation-owned selector preflight API that can validate aliases/default backends without opening attempts.
3. Determine whether existing route-trace DTO can carry `selector_source`/`override_revision` additively or whether a small dedicated decision entry is cleaner.
4. Verify current stdhttp route-collision tests cover the new configurable admin prefix; add them if not.
5. Add deterministic test barriers around the snapshot linearization point so race scenarios are reproducible rather than timing-based.
