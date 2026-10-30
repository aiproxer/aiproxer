# Design Validation Review

## Review Method

The design was validated as a brownfield routing/continuity/admin change against:

- root `AGENTS.md` and `.kiro/AGENTS.md`;
- `.kiro/steering/routing-and-orchestration.md`, `structure.md`, `api-standards.md`, `testing.md`;
- repository `main` at `294fa587b902fa0989adab8ad0a16f6ab001c33e`;
- `pkg/lipapi.Call.RouteIntent` and selector size limits;
- `internal/core/runtime` request preparation, route planning, retry and interleaved continuation;
- `internal/core/routing` aliases/parser/planner/dynamic modes;
- `internal/core/b2bua` memory continuity and public SDK mirror constraints;
- `internal/core/continuity/bunstore` SQLite/PostgreSQL durability;
- process service / immutable generation reload ownership;
- secure-session A-leg authority and diagnostics;
- existing protected admin HTTP patterns;
- all acceptance criteria in `requirements.md` and gaps in `gap-analysis.md`.

The validation used four rounds, including an external-review remediation pass. Any unresolved in-flight correctness, selector-semantic duplication, public-contract churn, persistence inconsistency, or admin-security issue returned NO-GO and forced design remediation.

## Round 1

### Assessment

**Decision: NO-GO**

The first architecture concept correctly used A-leg state but still coupled the feature too tightly to the base continuity interface and left the concurrency cut under-specified.

### Critical Issue 1: Extending the base continuity Store would force unnecessary public SDK churn

**Concern:** `b2bua.Store` is mirrored by `pkg/lipsdk/continuity.Store` and wrapper/contract tests. Adding override methods directly would turn an operator-only feature into a public persistence compatibility change.  
**Impact:** External/custom continuity implementations would need new methods even when override administration is unused.  
**Resolution:** Introduce a focused optional/internal `routeoverride.Store` capability implemented by standard memory/Bun continuity stores; leave the base Store unchanged.  
**Traceability:** 7.7, 10.5–10.6.  
**Evidence:** Design D8, Components and Interfaces.

### Critical Issue 2: Reading mutable override state per B-leg would violate the user story

**Concern:** If retry/failover/interleaved code re-read current override state, a single logical turn could switch models/routes after an admin update.  
**Impact:** Active requests could change candidate sets mid-turn, defeat consistent failover requirements, and create hard-to-audit costs.  
**Resolution:** Define one linearizable override snapshot immediately after A-leg authority and before route planning; B-leg/retry code never reads mutable override state.  
**Traceability:** 3.1–3.7.  
**Evidence:** Design D3, Set/Replace and Clear sequence diagrams.

### Critical Issue 3: Putting mutable overrides on immutable generations conflicts with reload ownership

**Concern:** A generation-local map would either mutate published generation objects or require rebuild/reload for each admin action.  
**Impact:** Violates current runtime architecture and could lose/duplicate state across generation swaps.  
**Resolution:** Keep state in process-owned continuity and interpret it with the generation admitting each turn.  
**Traceability:** 7.4–7.6.  
**Evidence:** Design D10, Runtime Reload flow.

## Round 2

### Assessment

**Decision: NO-GO**

The concurrency model was correct, but the first integration draft risked corrupting audit semantics and accidentally creating a second route-validation behavior.

### Critical Issue 1: Mutating the only `work` call early would misattribute admin routing as client input

**Concern:** Current CTP traffic capture and secure-session client-turn recording use the prepared work call. If the admin selector replaced it immediately after A-leg resolution, operator changes could appear as if submitted by the client.  
**Impact:** Audit/debugging would lose the distinction between client intent and proxy routing authority.  
**Resolution:** Preserve the client/work call for existing evidence. After pre-request mutation, deep-clone an effective routing call, apply the frozen admin selector to that copy, run route hinting, and freeze the routing baseline from it.  
**Traceability:** 5.1–5.5, 9.1–9.4.  
**Evidence:** Design D4–D5 and Runtime Integration order.

### Critical Issue 2: Admin validation could drift from or execute the real routing engine

**Concern:** A bespoke parser would diverge; a full dry-run candidate plan could mutate `[first]`/affinity/thinker state or perform provider/model work.  
**Impact:** PUT could have side effects or accept/reject syntax differently from real turns.  
**Resolution:** Extract/reuse a pure alias/parse/default-backend structural compiler helper used by both route planning and admin preflight. Full request-dependent eligibility remains deferred to the real turn.  
**Traceability:** 4.1–4.8, 5.6, 8.7.  
**Evidence:** Design D6, D9, Selector Preflight Design.

### Critical Issue 3: Alias expansion at write time would freeze stale generation semantics

**Concern:** Persisting the expanded selector would make a runtime override behave differently from a client route after config reload.  
**Impact:** Operators could not rely on aliases/config updates taking normal effect, and stale backend identities could survive indefinitely.  
**Resolution:** Store raw normalized selector; validate against the current generation for feedback; reinterpret under each later admitting generation. Fail normally if a future generation no longer accepts it.  
**Traceability:** 4.3–4.5, 7.5.  
**Evidence:** Design D6 and Runtime Reload flow.

### Critical Issue 4: Override lifecycle risked resetting dynamic routing state

**Concern:** Treating set/replace/clear as a new routing session could reset `[first]`, thinker memo/cycle, affinity or B-leg sequence.  
**Impact:** Advanced selectors would behave differently through the admin surface than when supplied directly by clients.  
**Resolution:** Override lifecycle changes selector authority only; all existing A-leg dynamic state remains untouched and is covered by characterization tests.  
**Traceability:** 6.1–6.6.  
**Evidence:** Design D7, Advanced Routing Characterization.

## Round 3

### Requirements Traceability Review

**Decision: PASS**

- Sticky lifetime until replace/clear is explicit.
- Latest committed mutation is revisioned and deterministic.
- Clear restores the current later client turn's selector, not a historical snapshot.
- One immutable override state is bound per logical turn.
- All B-legs of that turn use one route plan/revision.
- Rich selector syntax is reused through the existing compiler.
- Reload preserves process state but updates selector interpretation for new turns.
- `[first]`, `[thinker]`, affinity and no-retry behavior remain existing core semantics.
- Memory and durable persistence follow A-leg lifecycle.
- Admin surface is opt-in/protected and does not trust client hints for mutation authority.
- Raw selector is protected/high-cardinality data and does not enter metrics/logs by default.
- Public canonical and continuity SDK base contracts remain unchanged.

### SOLID Review

**Single Responsibility — PASS**

- routeoverride domain/store/service owns override state only;
- routing package remains selector semantic owner;
- runtime owns turn snapshot/substitution;
- admin handler owns HTTP translation/security integration;
- continuity adapters own persistence.

**Open/Closed — PASS**

- New selector syntax automatically works through existing routing parser/planner without changes to override storage/API.
- New frontend/backend plugins do not need override-specific code.

**Liskov Substitution — PASS**

- Memory and Bun stores run the same override-store semantic contract.
- The effective selector behaves like the same selector supplied by a client on the same A-leg.

**Interface Segregation — PASS**

- Base `b2bua.Store`/public SDK remains narrow.
- `routeoverride.Reader`, `Store`, and selector validator are focused capabilities.

**Dependency Inversion — PASS**

- Admin HTTP depends on a command service, not routing internals/providers.
- Runtime depends on a narrow reader.
- Store adapters implement core-owned contracts.

### Hexagonal Boundary Review

**Decision: PASS**

- Domain state is internal/core and provider-neutral.
- Driving admin adapter is isolated from client frontends.
- Driven store adapters reuse continuity infrastructure.
- Composition remains explicit through process/generation roots.
- No generic service locator, reflection registry, or provider import is introduced.

### Concurrency Review

**Decision: PASS**

The design has two explicit linearization boundaries:

1. **Mutation commit:** mutex/transaction changes complete state + revision atomically.
2. **Turn snapshot:** one complete state is copied after A-leg authority.

After snapshot, the routing baseline/AST is request-local. No notification, callback, channel, watcher, polling loop or active-turn mutation is required. This is simpler and safer than attempting to push admin changes into live executions.

### Routing Semantics Review

**Decision: PASS**

- raw selector is authoritative data;
- current generation owns alias/parse/default/backend interpretation;
- real request owns capability/catalog/authority admission;
- route hint remains advisory;
- admin selector source is final before planning;
- no selector grammar fork exists.

### Dynamic Routing Review

**Decision: PASS**

The design correctly treats set/replace/clear as selector changes on the same A-leg, not new sessions. Existing `[first]`, `[thinker]`, memo and affinity state are retained. Tests compare admin-selected routes against equivalent client-selected routes to prevent admin-only semantics.

### Persistence and Reload Review

**Decision: PASS**

- Memory state follows MemoryStore eviction.
- Durable state is A-leg-owned and cascade/transaction cleaned.
- ProcessServices owns state across generation swaps.
- No generation-local copy is authoritative.
- Shared durable stores are read per turn rather than indefinitely cached.

### Security Review

**Decision: PASS**

- Admin feature disabled by default.
- A-leg is proxy authority; client hints cannot mutate state.
- Existing protected-admin secret posture is reused.
- Raw selector appears only on protected response; logs/metrics stay bounded.
- Mutation validation opens no backend and consumes no model usage.
- Unknown/invalid selector writes are atomic failures.

### Brownfield Compatibility Review

**Decision: PASS**

No-override execution preserves current behavior. The implementation is additive:

- no `pkg/lipapi.Call` field;
- no frontend/backend/connector change required;
- no base public continuity Store expansion;
- legacy A-leg rows read as inactive revision 0;
- config omitting override admin remains valid;
- in-flight generation/stream behavior is unchanged.

### Testing Review

**Decision: PASS**

The design demands RED contracts before production changes and includes deterministic barrier-based tests for the central race:

- T1 snapshots rev N;
- admin commits rev N+1 or clear;
- T1 later opens/retries B-legs with rev N;
- T2 snapshots rev N+1.

It also covers durable restart, config reload (including PUT-after-reload generation binding), disabled-surface enforcement, A-leg liveness, delete/recreate mutation races, fixed admin media/status/DTO semantics, cross-frontend A-leg continuity, selector forms, post-output non-interference, admin protection and race detector execution.

## Round 4: External Review Remediation

### Assessment before remediation

**Decision: NO-GO (temporary)**

A post-publication CodeRabbit review correctly identified several contracts that were directionally intended but still under-specified. The design-readiness decision was therefore treated as conditional until the following items were made explicit.

### Finding 1: Generation-current admin validation needed an admission/binding contract

**Resolution:** The admin handler/service is generation-scoped and is composed into each `GenerationRuntime` HTTP handler. The existing stable `runtimehost.GenerationDispatcher` leases one active generation per HTTP request, so admin requests admitted after publish use the new generation's validator/config without a process-global service swap. A dedicated PUT-after-reload test is required.

### Finding 2: HTTP exposure and runtime enforcement were accidentally conflated

**Resolution:** Standard memory/Bun continuity always wires the focused runtime reader independently of `routing.override_admin.enabled`. The flag controls only protected command-route exposure. A persisted override remains enforced while the endpoint is disabled, and a regression test proves this.

### Finding 3: Admin HTTP wire behavior had unresolved choices

**Resolution:** Inactive responses omit `selector`; revision-0 responses also omit `updated_at`, while cleared tombstones retain update time. PUT requires media type `application/json` (valid parameters allowed); missing/malformed/unsupported media types return 415 before decode. Malformed/invalid selector input uses 400, body overflow 413, unknown A-leg 404, store unavailable 503.

### Finding 4: Override-store liveness effects were vague

**Resolution:** Successful Snapshot/Get/Replace/Clear operations refresh A-leg `LastSeenAt` in both memory and durable standard stores, matching existing continuity read/mutation semantics. Idempotent operations refresh A-leg liveness without changing override revision/update time.

### Finding 5: Delete/recreate races needed deterministic contract coverage

**Resolution:** Store tests now require both linearization orders for deletion or continuity-key recreation racing Replace/Clear. Mutation-before-delete is cleaned up; delete/recreate-before-mutation returns not-found; no orphan state commits and a new A-leg cannot inherit old state.

### Revalidation

**Decision: PASS**

The requirements, design, and task plan now resolve every external-review correctness/security contract above. The implementation approvals and `ready_for_implementation` remain false; this review only restores design readiness.

## Final Assessment

**Decision: GO FOR DESIGN READINESS**

The final design satisfies the sticky/latest-wins/clear user story while preserving Go-LIP's existing routing and B2BUA ownership. Its key simplification is to make the override a small A-leg state resource and freeze it into the already-request-local route plan rather than trying to mutate live execution. That avoids a second routing engine, avoids public API churn, and gives precise concurrency semantics.

No implementation is authorized by this review.

## Implementation Gate

Implementation shall begin only after maintainers explicitly:

1. set `approvals.requirements.approved` to `true`;
2. set `approvals.design.approved` to `true`;
3. set `approvals.tasks.approved` to `true`;
4. set `ready_for_implementation` to `true` in `spec.json`.

TDD order is mandatory: failing contracts/tests first, production implementation second.
