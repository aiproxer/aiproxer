# Requirements Document

## Introduction

Go-LIP shall simplify the upstream request-to-attempt execution pipeline by replacing overlapping mutable state carriers and pointer-out mutation with explicit private phase products, one route-progress authority, typed candidate outcomes, and one attempt-side-effect transaction boundary. The implementation shall preserve current security, accounting, billing, routing, extension, parallel/interleaved, B2BUA and backend behavior.

This is architecture simplification tranche 2 and is implementation-ordered after `turn-recv-terminal-ownership-simplification`. Tranche 1 establishes the downstream receive/terminal ownership seam; this specification simplifies how an incoming canonical request reaches that seam. The two specifications are intentionally reviewable independently and shall not be collapsed into one repository-wide refactor.

The current `Executor.Execute` high-level flow is intentionally retained. The target is the state topology beneath that orchestration: `preparedRequest`, `routePlanState`, `attemptOpenParams`, `attemptOpenResult`, context-carried business mirrors, and the mixed candidate planning/admission/open transaction.

## Boundary Context

- In scope: secure request preparation phase boundaries, request-lifetime pre-stream ownership, explicit typed prepared-turn facts, route facts versus route progress, initial/retry pipeline convergence, candidate planning/evaluation/admission outcomes, failure-history/error precedence, attempt-side-effect ownership/rollback, parallel/interleaved winner semantics, and handoff to tranche 1's downstream attempt owner.
- Out of scope: selector grammar redesign, sticky selector normalization, billing-domain redesign, secure-session-domain redesign, extension SDK redesign, backend-plugin ABI changes, OpenResponses protocol state-machine work, generation feature-surface projection cleanup, or generic pipeline/DI infrastructure.
- Existing authorities remain: secure-session manager, B2BUA store/lifecycle, request/attempt usage authority, billing services/stores, routing selector/planner, capability/transport negotiation, extension snapshot/hooks, model registry/catalog, interleaved state store, backend interfaces, and terminal ownership from tranche 1.

## Requirement 1: Prerequisite, Baseline, and State-Flow Evidence

1.1. Implementation shall begin only after the `turn-recv-terminal-ownership-simplification` implementation has established and certified a downstream ownership seam for immutable request facts and opened-attempt ownership; exact private type names are not a cross-spec contract.
1.2. Before production changes, document the current authoritative and mirrored state carried by `lipapi.Call`, request context, `preparedRequest`, `routePlanState`, `attemptOpenParams`, `attemptOpenResult`, and stream assembly.
1.3. Add a deterministic state-flow inventory showing every explicit assignment/projection among those carriers and every business fact recovered from `context.Context` for core decisions.
1.4. Add characterization tests for the exact preparation/order boundaries: authoritative secure-session/A-leg establishment, route-override snapshot, secret guard, frontend metering ingress, request-authority admission, submit/pre-request stages, route planning, billing exposure authorization, candidate opening, and downstream handoff.
1.5. Add characterization for initial-open and recv-replacement parity, including route progress, attempt budget, TTFT state, affinity, error history, interleaved cycle/memo state and BillingCallID continuity.
1.6. Add characterization for no-eligible-candidate error precedence across transport rejection, capability rejection, admission error, context-limit exhaustion, transform exclusions and parallel failure.
1.7. Add characterization for candidate-attempt transforms, panic isolation, capability/transport negotiation, model facts, B-leg/authority allocation, backend-open failure and cleanup.
1.8. Add characterization for parallel groups and interleaved thinker/executor routing, including loser cleanup and winner-only pending memo/route transition commit.
1.9. Record current `attemptOpenParams` pointer-out fields and direct callers as architecture debt that the final implementation must eliminate.
1.10. Final evidence shall compare before/after state carriers, projection assignments, pointer-out mutations, ownership/rollback boundaries and affected production surface; if material simplification is not demonstrated, implementation shall stop or re-scope.

## Requirement 2: Preserve the Explicit Executor Orchestration and Public Behavior

2.1. `Executor.Execute` shall remain a compact explicit orchestration shell rather than becoming a generic stage runner or regaining detailed preparation/open logic.
2.2. The externally observable ordering and results of request validation, security, request admission, routing, billing, candidate negotiation, backend opening, streaming and terminal behavior shall remain compatible.
2.3. No public configuration field, CLI flag, SDK API, backend-plugin ABI field, selector syntax, provider-specific branch or protocol field shall be required by this refactor.
2.4. Existing hooks, transforms, route hints, tool policies, traffic/evidence surfaces and extension ordering shall remain compatible.
2.5. Existing safety/panic boundaries around extension and backend/candidate operations shall remain effective and shall not be bypassed by new phase types.
2.6. Existing trace/diagnostic spans and decision evidence may be reorganized internally but shall retain equivalent semantic attribution and failure visibility.
2.7. No generic pipeline engine, service locator, dependency injection container, reflection dispatcher, actor framework or universal mutable `TurnState` shall be introduced.
2.8. Private concrete phase products/functions are preferred; interfaces require an existing external port or genuine substitution/testing need.
2.9. The refactor shall not change request concurrency, retry counts, TTFT/idle behavior, output commitment, billing/authority safety, or canonical request/event semantics.
2.10. The implementation shall remain compatible with all current supported frontends/backends and retained conformance/TCK/parity suites.

## Requirement 3: Establish One Authoritative Identity-Bound Turn Phase

3.1. Introduce an explicit private phase product for the request after proxy-authoritative identity/session/workspace establishment and before later routing/backend activity.
3.2. The phase shall establish a stable trace/call ID and a cloned working canonical call whose authoritative session/A-leg fields come from secure-session/B2BUA state rather than client-forged values.
3.3. Principal and request scope resolution shall remain available to later policy/extension stages through typed facts and compatible context projection.
3.4. Workspace resolution and its fail-open/fail-closed policy shall remain at the current security boundary and its result shall become an explicit phase fact.
3.5. Secure-session `BeginTurn` shall continue to precede submit and later request stages that depend on authoritative continuity.
3.6. A-leg fetch/continuity information shall be bound to the authoritative secure-session result and shall not be independently re-derived by later phases from client input.
3.7. Route-override authority shall be snapshotted at the current deliberate point, including the existing snapshot barrier/testing semantics, and carried as a typed fact to route planning.
3.8. Base decision-evidence/session/workspace views needed by later extension stages shall derive from the identity-bound facts without becoming a second business authority.
3.9. The identity-bound phase product shall not claim request-authority, billing exposure, route candidate or backend-attempt facts that do not exist yet.
3.10. Context values shall remain a compatibility/propagation projection; later core business decisions shall not need to rediscover authoritative identity/session/workspace facts from arbitrary context values.

## Requirement 4: Make Request Admission/Preparation a Typed Ownership Boundary

4.1. Request-wide metering ingress shall remain captured at its current semantic point before submit mutation where existing accounting requires it.
4.2. Request authority/concurrency admission shall occur exactly once and remain released on any later pre-stream failure according to current behavior.
4.3. Submit hooks, traffic capture, secret-guard and later request-wide transform/policy stages shall retain current ordering and call mutation semantics.
4.4. BillingCallID shall remain allocated once per incoming logical invocation and carried across retries, failover alternatives, parallel B-legs and interleaved continuations according to existing billing rules.
4.5. A-leg lifecycle scope shall start at the current safe point and shall be canceled/ended if ownership is not successfully handed to downstream execution.
4.6. Foreground keep-warm interruption (`BeginRealTurn`) shall retain its current ordering relative to authoritative A-leg establishment and later route/backend work.
4.7. Replace the mutable `streamReturned` cleanup condition with an explicit package-private pre-stream ownership guard/transfer operation or equivalent single-owner mechanism.
4.8. The guard shall own only integration cleanup required before downstream handoff; it shall not replace B2BUA, request authority or other domain owners with a generic resource ledger.
4.9. The final prepared-turn phase shall expose an immutable baseline request and stable owner/fact references required for routing/open and downstream execution, while facts established only after billing exposure/candidate admission remain represented at their actual later phase.
4.10. Preparation shall not require a universal partially initialized state object whose valid fields depend on temporal comments or nil checks.
4.11. Any context projection produced during preparation shall derive from authoritative typed facts/owners and shall not become a competing mutable copy.

## Requirement 5: Separate Immutable Route Facts From Mutable Route Progress

5.1. Route compilation shall produce explicit immutable route-execution facts distinct from mutable per-request attempt/recovery progress.
5.2. Immutable route facts shall include the compiled selector, failover requirement set, request-size estimate, affinity identity, deterministic RNG source/configuration and configured attempt/TTFT limits as applicable.
5.3. Mutable route progress shall own exclusions, attempt/TTFT consumption, session `[first]` state, failure history, context-limit/transform/parallel failure evidence, and interleaved cycle/retry state that changes as attempts proceed.
5.4. Initial open and recv replacement shall share the same mutable route-progress authority rather than reconstruct different copies from `routePlanState` and stream fields.
5.5. The implementation shall converge with the recovery ownership established by tranche 1; it shall not create a second persistent recovery state object with mirrored values.
5.6. Route facts/progress shall preserve existing affinity, preferred-candidate, first-request, weighted, parallel and thinker-cycle semantics.
5.7. Route-override snapshot facts from Requirement 3 shall be applied without live re-read at later candidate attempts.
5.8. Request-size/model/native-view behavior shall remain request-bound and shall not consult a newer generation unexpectedly.
5.9. Route state types shall remain private implementation details and shall not become a public routing SDK model.
5.10. This specification shall not alter selector grammar or independently rewrite `routing.ExpandFailoverGroups`/sticky selector semantics.

## Requirement 6: Eliminate `attemptOpenParams` and Pointer-Out Outcome Mutation

6.1. The final implementation shall delete `attemptOpenParams`; renaming it or wrapping it inside another universal input bag does not satisfy this requirement.
6.2. No attempt-pipeline input type shall contain pointer fields whose purpose is to let callees mutate caller-owned rejection/error/progress state as output parameters.
6.3. Candidate planning/open operations shall accept cohesive typed inputs: prepared-turn facts/owners, route facts/progress, current retry mode and the minimal attempt-lifecycle services they require.
6.4. Candidate rejections/exclusions shall return explicit typed outcomes containing the updated failure/progress evidence required for subsequent candidate planning.
6.5. Introduce one typed failure-history/diagnostic value or equivalent that preserves capability, transport, admission, context-limit, transform-exclusion and parallel failure information without six independent pointer channels.
6.6. The final no-eligible error shall preserve current precedence and public error mapping using that explicit failure history.
6.7. Initial open and recv-phase replacement shall invoke the same typed pipeline rather than manually assembling parallel parameter sets.
6.8. Retry-only differences shall be represented as explicit mode/progress facts, not by duplicating the candidate pipeline.
6.9. Tests shall prevent reintroduction of pointer-out mutation or a similarly broad mutable attempt bag in the affected path.
6.10. Removal of `attemptOpenParams` shall delete associated translation/forwarding code from stream assembly/replacement rather than leaving permanent adapters.

## Requirement 7: Make Candidate Planning and Evaluation Explicit Typed Outcomes

7.1. Candidate planning shall return one candidate or bounded parallel group plus the pending route/interleaved transition facts needed if that plan becomes authoritative.
7.2. Planning shall preserve sticky-affinity preference/fallback, weighted/parallel/first/interleaved behavior and deterministic ordering without duplicating selector interpretation.
7.3. Candidate evaluation shall clone the immutable baseline call for the attempt and apply interleaved shaping at the same semantic point as today.
7.4. Candidate attempt transforms shall retain current extension ordering, exclusion semantics, service/state access and observable diagnostics/evidence.
7.5. Candidate evaluation shall resolve the backend and candidate/model facts needed for capability/transport/admission without making those facts request-global.
7.6. Capability and transport negotiation, context/output/token eligibility, admission diagnostics and panic isolation shall preserve current semantics and safety boundaries.
7.7. Accepted evaluation shall return a typed evaluated/admitted candidate containing only facts necessary to open the attempt; rejected evaluation shall return a typed rejection/failure outcome.
7.8. Candidate evaluation is not required to be pure; any side effect shall remain phase-owned and must not be confused with resource ownership transferred to an attempt transaction.
7.9. Pending interleaved memo/cycle/route transitions produced during shaping/planning shall not become authoritative until the selected/opened attempt wins the relevant commit boundary.
7.10. No candidate evaluation type shall become a new provider/protocol-specific compatibility matrix or public ABI object.
7.11. Rejected candidates shall update route progress explicitly and continue/fail according to current attempt budget and error rules.

## Requirement 8: Give B-Leg/Authority/Backend Open One Attempt Transaction Boundary

8.1. Introduce one package-private attempt transaction/owner for side effects created while turning an evaluated candidate into an opened attempt.
8.2. The transaction shall own attempt authority/reservation, B-leg allocation/registration, backend open/managed stream and attempt-local cleanup until successful downstream handoff.
8.3. If authority admission, B-leg setup, backend open, observer/evidence work or later pre-handoff processing fails, the transaction shall terminalize/release/close exactly those resources it owns according to current semantics.
8.4. Successful handoff shall transfer ownership exactly once to the downstream attempt owner established by tranche 1; upstream cleanup shall become inert after transfer.
8.5. Failed or losing transactions shall not leak B-legs, authority reservations, backend streams, lifecycle scopes, pending memo effects or billing evidence.
8.6. Attempt/B-leg logging and diagnostics shall retain correct trace/A-leg/B-leg/candidate attribution.
8.7. Backend `Open`/execute and candidate admission safety/panic boundaries shall remain intact.
8.8. The transaction shall not become a generic resource framework or replace existing B2BUA/usage-authority/terminal domain owners.
8.9. Billing/usage authority ordering shall remain compatible with recent convergence, including no duplicate reservation/settlement or premature request closure.
8.10. A successful opened-attempt result shall be complete enough for downstream ownership without requiring the downstream façade to reconstruct B-leg/candidate/authority fields individually.
8.11. `attemptOpenResult` may be replaced by a stronger typed handoff, but duplicate partial result bags shall not remain after migration.

## Requirement 9: Preserve Parallel and Interleaved Winner/Loser Semantics

9.1. Every parallel arm shall have independent attempt-transaction ownership for B-leg, authority, backend stream and cleanup.
9.2. Parallel winner selection, handicap timing, cancellation and error aggregation shall remain behaviorally equivalent.
9.3. Losing arms shall terminalize/rollback exactly once and shall not leave an authority reservation, live stream or B-leg lifecycle leak.
9.4. Pending interleaved memo updates, cycle/route progress and other winner-only transitions shall commit only for the authoritative winning attempt.
9.5. A losing or failed arm shall not consume/commit memo budget or route continuity as if it had won.
9.6. Thinker/executor candidate roles and suppression rules shall remain unchanged.
9.7. Parallel-group failure shall continue to contribute its aggregated causal error to final no-eligible precedence.
9.8. Sticky affinity and candidate preference behavior across parallel/weighted alternatives shall remain unchanged.
9.9. Existing interleaved state store/wrapper architecture remains authoritative; this spec changes pipeline ownership/handoff only.
9.10. Race/scheduling tests shall cover parallel winner publication versus loser cleanup and pending transition commit.

## Requirement 10: Make Context a Projection and Downstream Handoff Explicit

10.1. Core pipeline phase products and owners shall be authoritative for identity, admission, route and attempt facts used in business decisions.
10.2. `context.Context` shall remain the authority for cancellation/deadline propagation and a compatibility carrier for tracing, principal/scope/session views, extension evidence, model/native views and other existing SDK seams.
10.3. Context projections shall be produced from current typed facts at explicit boundaries rather than treated as an independent mutable database.
10.4. Later pipeline stages shall not rely on arbitrary caller context to recover a business fact that is already part of the prepared turn/route/attempt state.
10.5. The final successful attempt-open boundary shall hand one complete opened-attempt ownership package plus required immutable request facts/progress to the downstream seam established by tranche 1.
10.6. During migration, a narrow compatibility adapter may target the downstream seam, but no permanent field-by-field reconstruction into a God object is permitted.
10.7. The integration contract with tranche 1 shall be semantic and lifetime-based; this spec shall not require exact private type names from an unmerged implementation.
10.8. Initial execution and recv-phase replacement shall both reach the same downstream attempt handoff path.
10.9. Pre-stream request ownership shall transfer exactly once at successful downstream handoff; failure before transfer shall execute the prepared-turn guard cleanup.
10.10. No request/attempt business state shall be simultaneously authoritative in a phase product and independently mutable in context.

## Requirement 11: Structural Simplification, TDD, and Architecture Ratchets

11.1. Add architecture tests proving `attemptOpenParams` is absent and pointer-out mutation is not reintroduced in attempt pipeline inputs.
11.2. Add a state-carrier/projection inventory gate tracking the number of overlapping phase bags, field-copy assignments, context business re-reads and translation layers in the affected path.
11.3. Add an ownership/transaction gate proving one authority for prepared request ownership, route progress, candidate failure history and pre-handoff attempt resources.
11.4. Remove obsolete `preparedRequest` fields, `routePlanState` fields/types, `attemptOpenResult` partial shapes, forwarding helpers and context mirrors as stronger phase products become authoritative.
11.5. `preparedRequest` may be replaced or retained in a materially smaller/frozen form; preserving it as a broad partially initialized temporal bag is a NO-GO.
11.6. The affected production state-handoff/parameter-translation surface shall have a net deletion. Net growth is a default NO-GO unless final design review demonstrates a material invariant reduction that cannot be expressed more simply.
11.7. File count and individual file length are not primary acceptance metrics; review shall prioritize authoritative representations, state-copy paths, temporal initialization, pointer-out mutation, rollback boundaries and change blast radius.
11.8. No architecture, routing, billing, secure-session, accounting, interleaved, conformance, race or protocol test shall be weakened/removed to make the refactor pass.
11.9. Run targeted race/scheduling coverage for preparation ownership transfer, parallel transactions, attempt handoff/rollback, route-progress mutation and downstream integration on supported platforms.
11.10. Run all affected runtime, routing, billing, usage-authority/accounting, secure-session, extension, interleaved, backend, frontend/protocol and repository quality/parity gates.
11.11. Final review shall compare the before/after state-flow graph and issue GO/NO-GO based on actual structural simplification, not movement of code into more types.
11.12. Selector grammar normalization, sticky traversal convergence, generation feature-surface projection cleanup and other deferred architecture candidates shall remain outside the implementation diff except for documentation references.
