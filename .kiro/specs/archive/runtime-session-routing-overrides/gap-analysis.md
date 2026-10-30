# Brownfield Requirements Gap Analysis

## Scope and Method

This analysis compares the final requirements for `runtime-session-routing-overrides` with repository `main` at `294fa587b902fa0989adab8ad0a16f6ab001c33e`.

The review covers:

- canonical route intent and size bounds in `pkg/lipapi`;
- selector aliases/parser/planner in `internal/core/routing`;
- request preparation and route-plan lifetime in `internal/core/runtime`;
- A-leg/B-leg continuity in `internal/core/b2bua`;
- durable continuity in `internal/core/continuity/bunstore`;
- public continuity mirror/wrapper in `pkg/lipsdk/continuity` and `internal/core/continuity/sdkwrap.go`;
- A-leg lifecycle and secure-session resolution;
- interleaved `[thinker]` state and weighted `[first]` state;
- process services, immutable generations, and reload ownership;
- existing diagnostics/admin HTTP composition and security posture;
- route/attempt diagnostics and secure-session A-leg lookup;
- current testing/architecture guardrails.

Classifications:

- **Missing** — required capability does not exist.
- **Partial** — reusable machinery exists but does not satisfy the requirement alone.
- **Constraint** — existing API/security/lifecycle behavior constrains the design.
- **Risk** — brownfield behavior can regress unless explicitly characterized.

## Current Assets Worth Preserving

### A-leg is already the correct session anchor

`internal/core/b2bua.ALegRecord` is the logical session row for routing/lineage, and the secure-session path resolves/fetches the authoritative A-leg before route planning. Client session IDs and A-leg carriers are hints until validated by secure-session authority. This gives the feature a stable proxy-owned key without trusting client-supplied routing-control identity.

### A route plan is already turn-local and reusable across B-legs

`internal/core/runtime.buildRoutePlan` reads `prep.baseline.Route.Selector`, resolves aliases, parses once, applies model-only backends, binds model IDs, loads dynamic session state, and returns a `routePlanState`. That selector/plan is then carried through retry/failover/interleaved paths. This is exactly the shape needed for non-interference: snapshot the override before building this state and never re-read override state from B-leg code.

### Full selector semantics already have one owner

`internal/core/routing` already owns aliases, failover, weighted routing, races, TTFT, affinity, `[first]`, `[thinker]`, model-only defaulting and planner state. The override should supply a selector string to this existing pipeline, not parse/expand/store a second representation.

### Dynamic routing state is already A-leg scoped

`WeightedFirstConsumed` is persisted on the A-leg. Interleaved-thinking state is persisted through the optional `b2bua.InterleavedStateStore`, and memo scope is the A-leg ID. The feature can preserve current semantics by changing selector authority only and leaving these state machines untouched.

### Process services survive immutable generation swaps

`runtimebundle.ProcessServices` owns continuity for the process lifetime while request-plane `GenerationRuntime` values are immutable and replaced on reload. This supports the desired split: mutable operator override state belongs with process-owned continuity; alias/parser/backends belong to the generation admitting each turn.

### Durable continuity already supports SQLite/PostgreSQL migrations

`internal/core/continuity/bunstore` already persists A-leg rows, B-leg sequence allocation, attempts, and optional interleaved state. A route-override table/columns can use the same migration/lifecycle infrastructure without adding a second database stack.

### Operator HTTP patterns already exist

`internal/stdhttp/admin/*`, `mount_admin.go`, `diag.WrapDiagnosticsProtect`, and `config.ValidateProtectedDiagnosticsPosture` provide established patterns for bounded protected operator endpoints. Secure-session diagnostics already expose `GET .../by-a-leg/{id}` and session summaries containing A-leg IDs, so administrators can identify the anchor without a new client protocol.

## Gap Register

| ID | Severity | Class | Effort | Current finding | Required disposition |
|---|---:|---|---:|---|---|
| G-01 | P0 | Missing | M | No A-leg routing-override state or mutation contract exists. | Add focused override state/store/service keyed by authoritative A-leg. |
| G-02 | P0 | Constraint | M | `b2bua.Store` is mirrored by public `pkg/lipsdk/continuity.Store`; adding base methods would force public contract/wrapper drift. | Prefer an optional/internal override-store seam implemented by standard continuity stores. |
| G-03 | P0 | Partial | S | Request preparation knows the authoritative A-leg before route planning, but does not snapshot operator route authority. | Insert one turn-level override snapshot after A-leg authority is established. |
| G-04 | P0 | Partial | S/M | `buildRoutePlan` parses the baseline selector once and retry streams reuse it, which naturally isolates B-legs. | Preserve this lifetime; forbid override reads from B-leg/retry code. |
| G-05 | P0 | Risk | M | Submit/request/pre-request stages can mutate the canonical call before route hinting and baseline freeze. | Define one final core selector-authority reassertion before route hinting/planning when override is active. |
| G-06 | P0 | Partial | S | Alias resolver/parser already support rich strings including dynamic modes. | Store raw bounded selector and feed the normal pipeline; do not store AST/expanded route. |
| G-07 | P0 | Missing | M | No admin route validator can prove current-generation acceptability without execution. | Add a narrow side-effect-free validation/preflight seam using the active generation's alias/default/registry view. |
| G-08 | P0 | Missing | M | Memory continuity has no override revision/state. | Add mutex-protected state under A-leg lifecycle with atomic replace/clear/snapshot. |
| G-09 | P0 | Missing | M | Bun continuity schema has no override state/revision. | Add migration and transactional latest-wins state tied to A-leg deletion. |
| G-10 | P0 | Constraint | M | Runtime generations are immutable; mutating executor/generation fields would violate reload ownership. | Keep override state process-owned and snapshot it per turn. |
| G-11 | P0 | Missing | M | No protected set/replace/get/clear HTTP adapter exists. | Add opt-in admin route with bounded DTOs and existing operator protection. |
| G-12 | P0 | Constraint | S/M | Secure-session diagnostic Store is deliberately read-only and has owner-scoped authorization semantics. | Do not mutate through diagnostics Store; keep admin command service separate. |
| G-13 | P1 | Partial | S | Secure-session diagnostics already resolve sessions by A-leg and expose A-leg IDs. | Reuse for discovery; do not invent client-hint mutation authority. |
| G-14 | P0 | Risk | M | A prior `[thinker]` memo and `[first]` consumed flag can survive selector changes. | Characterize and preserve normal client-selector-change semantics; override lifecycle must not reset them implicitly. |
| G-15 | P1 | Risk | M | Affinity state can also survive selector changes according to existing planner semantics. | Characterize direct/weighted/affinity transitions; do not add admin-specific cleanup. |
| G-16 | P0 | Missing | S/M | Route diagnostics do not identify admin selector source/revision. | Add bounded source/revision evidence while keeping attempt EffectiveModel unchanged. |
| G-17 | P1 | Risk | S | Raw selectors can contain unbounded/high-cardinality values and must not enter logs/metrics. | Return raw selector only on protected read; log digest/length/revision. |
| G-18 | P1 | Constraint | S | `lipapi.MaxRouteSelectorBytes` already caps client selectors at 64 KiB. | Reuse the same selector bound; separately cap admin JSON body. |
| G-19 | P0 | Risk | M | Shared PostgreSQL continuity could be used by multiple processes; process-local caching would make updates stale. | Read authoritative override once per turn from the continuity store; no indefinite local cache. |
| G-20 | P1 | Constraint | M | Data-plane admin surfaces require explicit posture on non-loopback binds. | Extend protected-surface validation and keep override admin disabled by default. |
| G-21 | P1 | Constraint | M | The separate config-reload management listener is specialized and generalizing it would broaden scope. | Use the established stdhttp protected-admin composition for this feature; defer management-server generalization. |
| G-22 | P0 | Risk | M | A selector can be valid when written but become invalid after alias/backend config reload. | Validate on write against current generation, store raw form, and fail normally on a later turn if a future generation no longer accepts it. |
| G-23 | P0 | Missing | M | No race tests cover concurrent admin mutations versus route admission. | Add deterministic barriers and race-detector coverage for snapshot/set/replace/clear ordering. |
| G-24 | P1 | Constraint | S | Public canonical and frontend/backend contracts do not need this operator concept. | Keep `pkg/lipapi.Call`, frontend decoders, and backend connectors unchanged. |

## Requirements Review Round 1

The first feature interpretation treated the change as a one-turn override. That failed the user story.

### Finding R1-A: Override lifetime was too short

**Problem:** A one-shot “next turn only” override would force the administrator to reapply the same cost-control decision on every client turn.  
**Remediation:** Requirements 1.2–1.4 now define a sticky A-leg override that remains authoritative across all later turns until explicitly replaced or cleared.

### Finding R1-B: In-flight boundary was ambiguous

**Problem:** “Next B-leg” could be misread as changing a failover B-leg inside an already admitted logical turn.  
**Remediation:** Requirement 3 now defines a single per-turn snapshot. All B-legs belonging to that turn use it; later admin mutations affect only turns whose snapshot happens after commit.

## Requirements Review Round 2

The sticky model still lacked operator reversibility and deterministic updates.

### Finding R2-A: Administrator could not change their mind safely

**Problem:** A write-once override does not support changing the model route as session cost/complexity changes.  
**Remediation:** Requirements 2.1–2.3 define replace/latest-wins semantics using monotonically ordered committed revisions.

### Finding R2-B: No way back to client routing

**Problem:** Session routing would remain permanently hijacked once overridden.  
**Remediation:** Requirements 2.4–2.7 define a revisioned clear/inactive state and restoration of the routing string supplied by each later client turn.

### Finding R2-C: Clear-by-delete loses ordering evidence

**Problem:** Physically deleting the only row makes it harder to prove a clear happened after an earlier set and complicates concurrent set/clear reasoning.  
**Remediation:** The requirements now model active/inactive state with revision. A state-changing clear is a newer committed state; repeated clear is idempotent.

## Requirements Review Round 3

Brownfield mapping exposed four additional correctness constraints.

### Finding R3-A: Override must survive route-changing request stages

**Problem:** Applying the override too early could let a later submit/request mutation overwrite it before planning.  
**Remediation:** Requirement 5 defines a final core selector-authority boundary before route hinting/planning and requires active admin authority to be reasserted there.

### Finding R3-B: Expanded aliases would freeze stale generation behavior

**Problem:** Storing an expanded selector would detach runtime override behavior from normal client route semantics after config reload.  
**Remediation:** Requirements 4.3–4.5 require storage of raw bounded selector input and interpretation by the generation admitting each turn.

### Finding R3-C: Putting override fields on the base continuity interface creates unnecessary public API churn

**Problem:** `pkg/lipsdk/continuity.Store` intentionally mirrors `b2bua.Store`; extending it only for operator control would broaden a stable SDK contract.  
**Remediation:** Requirement 7.7 prefers a focused optional/internal persistence seam.

### Finding R3-D: Dynamic selector state could be accidentally reset

**Problem:** Treating admin replace/clear as a new session would break `[first]`, `[thinker]`, and affinity semantics.  
**Remediation:** Requirement 6 explicitly preserves those A-leg state machines and demands characterization across selector changes.

## Implementation Approach Options

### Option A: Add override fields/methods directly to the base A-leg continuity record/store

**Advantages**
- Small conceptual surface.
- Fetching A-leg and override could be one operation.

**Disadvantages**
- Forces changes to public `pkg/lipsdk/continuity.Store`, wrappers, drift tests, and any external implementation.
- Mixes operator-only mutable policy with the minimum lineage continuity contract.
- Makes future override evolution part of a broad public compatibility surface.

**Assessment:** Rejected unless implementation proves the optional seam is impossible.

### Option B: Keep a process-local concurrent map keyed by A-leg

**Advantages**
- Minimal schema work.
- Easy atomic revisioning in one process.

**Disadvantages**
- Loses overrides across restart even when A-leg continuity is durable.
- Cannot work coherently across multiple processes sharing PostgreSQL continuity.
- Creates a second lifecycle/eviction authority and risks orphaned state.

**Assessment:** Rejected.

### Option C: Focused A-leg override contract implemented by continuity adapters

**Shape**
- Introduce a narrow core override state/store/service contract separate from the base public continuity Store.
- Make the in-memory A-leg store and Bun continuity store implement that optional contract.
- Construct a process-owned override service from the continuity store.
- Snapshot via the executor once per turn; expose commands through a protected stdhttp admin adapter.

**Advantages**
- Follows A-leg lifecycle/durability.
- Avoids public SDK breakage.
- Keeps route parsing/planning in its existing owner.
- Supports single-process memory and shared durable stores with the same semantics.
- Natural TDD seams for store, service, runtime snapshot and HTTP adapter.

**Disadvantages**
- Adds a small new core capability and schema migration.
- Requires explicit wiring through process/runtime composition.

**Assessment:** Preferred.

## Admin Surface Options

### Protected standard HTTP admin surface — preferred

Reuse `internal/stdhttp/admin`, opt-in config, diagnostics-style shared-secret protection, non-loopback posture validation, bounded request decode and narrow service injection. This is consistent with existing token-accounting/control-plane operator surfaces and does not require a client frontend.

### Extend the config-reload management listener — rejected for this spec

The listener has strong management auth/isolation, but its package/contracts are deliberately config-reload-specific. Generalizing it into a process management plane is a larger architecture change unrelated to the routing-control user story.

### Mutate through secure-session diagnostics — rejected

The diagnostic store is intentionally read-only and uses session-detail/transcript authorization semantics. Mixing commands into that surface would blur read/query and operator-write responsibilities.

## Complexity and Risk

- **Effort: L (1–2 weeks)** — multiple core/runtime/store/admin/test boundaries plus a durable schema migration, but all use established repository patterns.
- **Risk: Medium** — no new external dependency or routing algorithm, but timing/precedence mistakes could reroute an active turn or stale an override after reload. The per-turn snapshot and continuity-owned revision model bound that risk.

## Design Recommendations

1. Use Option C: a focused A-leg override state/store/service with standard continuity adapters.
2. Define one linearization point per mutation and one snapshot point per admitted turn.
3. Store raw normalized selector + active flag + revision + update time; never store the routing AST as authority.
4. Validate a mutation through a current-generation side-effect-free selector preflight, but reinterpret raw state on every later turn using that turn's generation.
5. Reassert admin selector authority immediately before route hinting/planning so request hooks cannot accidentally defeat it.
6. Keep B-leg/retry/interleaved code ignorant of mutable override state; it receives the frozen baseline/selector already used today.
7. Extend protected stdhttp admin composition, not client protocols and not provider adapters.
8. Add RED concurrency barriers before implementation to prove set/replace/clear/snapshot ordering and no in-flight mutation.
9. Preserve `[first]`, `[thinker]`, affinity, accounting, capability and no-retry state machines exactly as they behave for ordinary selector changes.
