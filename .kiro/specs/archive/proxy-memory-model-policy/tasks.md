# Implementation Plan: proxy-memory-model-policy

> **Executor notes (read first):**
> - **Approved.** The owner approved this scope and enabled the remaining tasks ("Proceed with all remaining tasks!", 2026-10-21); `spec.json` records the approval, the owner context and the date. This authorizes the **implementation** of the tasks below — it does not authorize a merge, a closure, a new money engine, a new ledger or money arithmetic.
> - **Dependency: satisfied.** The role registration is implemented and reviewed in the archived spec `.kiro/specs/archive/proxy-memory-runtime-corrections` (role registration `4e2d588fe`), so the background phases execute and the cases here have a subject. The archived work is not to be redone.
> - **Base and land.** Implement on this branch's own base `0b2d339ee`, which carries the inherited unshipped memory feature. A rebase onto `main` is **not** a precondition and would drop that feature; the land is gated on the foundation reconciliation (#1287) and the owner's merge authorization.
> - **Certification runs last**, after the features are implemented. A user-control case whose guarantee this spec requires is never marked "not claimed" to reach green: an unresolved case **blocks** this slice's readiness and the missing capability is named.
> - TDD: write the listed tests first and run them to see them fail (RED), then implement (GREEN), then refactor. Keep RED evidence per `docs/agent-handoffs.md`.
> - Never import `internal/core/...`, `internal/infra/...` or `internal/standardplugins/...` from `internal/plugins/features/proxymemory`; the archtest closure rule fails the build otherwise. The compiled target therefore comes through a feature-owned port that the composition root binds.
> - Never log or label metrics with project paths, principal IDs, A-leg IDs, prompts, memory text or account identities.
> - No money, ledger, rating or settlement in this slice.
> - Race evidence is remote-owned. Run focused non-race checks locally, with the task-local development-host policy file.
> - **No implementation is piled into #1299.** That stack is still open and receives nothing from this plan.
> - **Budget:** the new composition-root file is on an affected surface with only two lines of convergence headroom; it must be credited by extending the existing "Proxy memory" overlay marker, and the overlay cap is re-pinned with the measured value. If the measured value exceeds the headroom, it goes to the owner as a separate budget approval — no marker is widened to hide a delta.
> - **Slice order**: A (1.1, 1.2) → B (1.3, 1.4) → C (2.1, 2.2); D (3.1) certifies.

- [x] 1. Per-phase policy domain

- [x] 1.1 Per-phase targets with a per-phase allowed inventory
  - Extend the feature configuration with a target per phase over the three user-facing fields — **backend, model and reasoning effort** — and a **per-phase** allowed inventory of backends, models and efforts; extend the strict key set and the decoder accordingly (design §3). Every phase **declares** its default target, its lock set and its allowed inventory: the configuration is rejected when one is missing, and an **empty inventory never means "allow everything"**. The effort values are provider-specific, so the allowed efforts are declared per phase and per target.
  - The **route selector is derived**, not a fourth user field: the proxy builds it from the operator's default route and the selected backend and model, so it can never become a cost or policy bypass.
  - Defaults: an absent backend or model keeps the operator's default; an absent effort keeps today's value; an empty inventory means "no user-selectable override", not "everything allowed".
  - The effective value of a field is resolved as **locked operator value, else permitted owner value, else operator default**; the resolution is a small, closed domain in the feature — two phases, three fields — and not a generic framework.
  - RED first, in `config_test.go`: defaults applied; the per-phase fallback; an inventory rejection naming the offending field, per phase; an unknown effort token; the empty-inventory case; the field precedence; the derived selector is never taken from a client-facing field.
  - **Done when** the table passes and the existing configuration tests are unchanged.
  - _Requirements: 1.1, 1.3_
  - _Boundary: feature plugin (proxymemory)_
  - _Depends: none_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -run TestConfig -count=1`_

- [x] 1.2 Per-field locks, including a lock introduced after an override existed
  - Add a per-field, per-phase lock set to the operator configuration and the precedence rule in the effective-target resolution (design §3).
  - A locked field rejects a write that changes it, is enforced at runtime so no request path bypasses it, and wins over an override that already existed when the lock was introduced: the effective value returns to the locked value and the stale override does not survive.
  - RED first: a write to a locked field is rejected; a runtime attempt that tries to bypass the lock is refused; a lock introduced after an override existed wins and the stale override does not survive; the durable and permitted paths are unchanged.
  - **Done when** the tests pass and effective values and per-field lock flags are available to the authenticated settings projection. Task 3.1 exposes administrator defaults, user overrides, effective values and lock flags without account or credential information.
  - _Requirements: 1.2_
  - _Boundary: feature plugin (proxymemory) and the settings service it exposes_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ -count=1`_

- [x] 2. Compiled actual target

- [x] 2.1 Add the feature-owned target-compiler port and bind it in the composition root
  - Add the port to the feature, with the shape of the route-override admin's selector validator: it takes the phase's target and returns the final compiled primary plus the capabilities that target can serve, or an error (design §1).
  - Bind it in the composition root, next to the route-override validator binding, from the generation's executor: the alias resolver, the default backend, the known-backend map with its static capabilities and their dynamic resolution, the model catalog's per-model facts, and the canonical capability negotiation.
  - The port is narrow and **generation-bound**: it is rebuilt per generation, so a reload re-derives it. It is not a registry, not a resolver chain and not a framework.
  - The compiled target must include the route parameters the runtime merges per attempt after the candidate is chosen, because those parameters can override the call's options, the reasoning effort included.
  - RED first: the port is called with a phase target and returns the alias-resolved target; a query parameter that changes the effort is reflected in the compiled result; the port is rebuilt on a reload; the port is nil-safe and the feature refuses explicitly when it is unavailable.
  - **Done when** the tests pass, the feature imports no core implementation, and the port is bound from the generation's executor.
  - _Requirements: 1.5, 2.4_
  - _Boundary: feature plugin (port) and composition root (binding)_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ ./internal/infra/runtimebundle/ -count=1`_

- [x] 2.2 Enforce the inventory, the locks and the effort against the actual target
  - **Owner approval received and verified (2026-10-23):** the narrowed D1 implementation and Requirement 1.4/task amendments are approved; the reviewed slice, spec gate and focused package/vet gates pass. Value-level effort support and the public ABI remain deferred.
  - Run the inventory check, the lock check and the capability check against the **final compiled actual target** returned by the port, so neither an alias nor a query parameter can point the call at an unchecked target (design §2, §3).
  - A reasoning effort the actual target does not support is **refused explicitly**; the proxy never silently downgrades it. The two places where the negotiated downgrades strip the effort today are the pre-open candidate admission and the post-request transform: the check therefore happens before dispatch, not as a warning afterwards.
  - The operator inventory is a **policy** constraint and must never be represented as provider-verified support: the backend capability set carries a boolean reasoning capability and the model catalog a tri-state per-model fact, and neither states which effort values a provider supports. A requested effort is refused explicitly and counted when the actual target's reasoning capability is false **or unknown**: an unproven capability is never treated as support. The runtime's negotiated effort strip — at the pre-open candidate admission and at the post-request transform — is converted to an explicit, counted refusal for proxy-memory calls, never a downgrade. The value-level effort support is a **deferred follow-up dependency** (owner ruling D1): this task neither designs nor implements a public effort-support ABI, and it never invents a closed global list of supported values.
  - **Owner ruling D1 (2026-10-23, narrowed scope):** the public effort-support ABI is deferred; this task completes (1) the inventory, lock, alias and query checks against the compiled target, (2) the refusal of **any** requested effort whose reasoning capability is false or unknown, and (3) the negotiated strip conversion to an explicit, counted refusal for proxy-memory calls. No public ABI is part of this spec; value-level effort support is recorded in `requirements.md` as a deferred follow-up dependency.
  - The core guard is per attempt and adds no parallel-round coordination (design §2, owner decision 2026-10-27): the feature binds its guard only when the model policy is declared, and a declared policy requires a single-target route per phase, refused at configuration time.
  - RED first: an out-of-inventory model is refused; a not-installed model is refused; an unsupported effort is refused and **not** downgraded; an unknown reasoning capability is refused as well and **not** treated as support; a negotiated effort strip on a proxy-memory call is refused and counted at both downgrade sites; an alias that resolves outside the inventory is refused; a query parameter that moves the target outside the inventory is refused; the recorded usage and the attempt evidence describe the actual target that served the call.
  - **Done when** the tests pass and the composed fixture declares the capabilities it actually supports and asserts that the backend's options remain frozen.
  - _Requirements: 1.4, 1.5_
  - _Boundary: feature plugin (policy), with the core capability surfaces consumed as they are_
  - _Depends: 2.1_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ ./internal/standardplugins/featurehost/ -count=1`_

- [x] 3. Settings surface and freeze

- [x] 3.1 Persist the owner-scoped overrides and serve them over the authenticated resource
  - Add the settings contract to the feature, keyed by tenant and principal only, with no project in the key, so the overrides are global for that owner (design §4). The store keeps the **unprojected overrides**; the effective values are recomputed on read and on reload.
  - Add the overrides table to the **existing isolated feature persistence** of the proxy-memory family — never to the core continuity database, whose single connection and single writer must stay free for the mandatory core path — and the queries in the infra package; register the family's database-parity capability for it.
  - Add the service methods and the resource `GET`, `PUT` and `DELETE /v1/lip/memory/settings`. The route classifier checks the settings segment **before** the per-scope item route, behind the same access-auth stack as the rest of the memory surface: authentication first, then eligibility, then routing. The memory tool endpoint exposes no settings mutation. Forgetting a bank does not delete the owner's overrides.
  - Every error that is not the caller's own settings answers exactly like an unknown caller, and no response carries an account identity.
  - RED first: the store contract on SQLite and PostgreSQL-direct (write, read back the **overrides** rather than a projection, delete, two-principal isolation, no project in the key, forget-safety, two instances reading the same overrides); the HTTP tests for the three methods, the not-found equivalence, the auth-then-eligibility order, and the route classification that keeps `settings` out of the scope path; a test that a changed operator default changes the **effective** value while the stored override stays as the owner wrote it.
  - **Done when** the contract and HTTP tests pass on both engines and the dbparity catalog entry is updated.
  - _Requirements: 2.1, 2.2_
  - _Boundary: feature plugin, infrastructure (proxymemorystore), HTTP adapter (proxymemoryhttp)_
  - _Depends: 1.1, 1.2_
  - _Validation: `go test ./internal/infra/proxymemorystore/ ./internal/stdhttp/proxymemoryhttp/ -count=1`_

- [x] 3.2 Freeze the effective settings at claim time and re-derive them on reload
  - Compute the effective values from the defaults, the locks and the stored overrides, and freeze that snapshot when a job is claimed, before its billable dispatch; the job runs with the snapshot (design §4).
  - The worker queue and the auxiliary scheduler queue are different queues: the freeze belongs to the claim, not to the queue.
  - On a reload, re-derive the defaults and the locks and recompute the effective values from the stored overrides; a lock in force before the reload still applies, and an override that depended on a removed default is masked rather than silently re-applied.
  - RED first: a settings change between queueing and claiming changes the newly claimed job's target and leaves an already admitted, running job unchanged; a reload that removes a default masks the derived override and the lock still applies; the stored override itself is unchanged by either.
  - **Done when** the tests pass and the frozen target is visible in the dispatched request.
  - _Requirements: 2.3, 2.4_
  - _Boundary: feature plugin (proxymemory) and featurehost composition_
  - _Depends: 2.1, 3.1_
  - _Validation: `go test ./internal/plugins/features/proxymemory/ ./internal/standardplugins/featurehost/ -count=1`_

- [x] 4. Certification

- [x] 4.1 Run the user-control matrix and re-pin the measured budgets
  - Execute the twelve user-control cases (U1–U12) of `requirements.md` and record, per case, the command, the result and the gate. Every case runs the real backend triple through the production path with the normal billing composition; **a mocked background client may not prove a case**.
  - Re-measure the Requirement 11.5 convergence and the "Proxy memory" overlay, extend the existing overlay marker to credit the new composition-root file, and re-pin the overlay cap with the measured value. If the measured value exceeds the available headroom, stop and put the excess to the owner as a **separate budget approval**.
  - Local gates: the focused non-race package tests, the SQLite plus PostgreSQL-direct store contract, the composed-generation integration, `go run ./tools/kiro/kirocheck`, the local lint and vet equivalents, all with the task-local development-host policy file. Remote gates: the race lanes, currently blocked by the GitHub Actions billing block; a local run is never reported as a race result.
  - Not claimed: live-provider certification, cross-node consistency certification, and any merger or closure of the related issues.
  - **Done when** all twelve required cases pass against the implemented path and measured budgets identify their exact base. Missing required behavior blocks readiness; an explicit gap is not a passing case. Remote certification is reported separately and never invented.
  - _Requirements: all_
  - _Boundary: verification, measurement and reporting only_
  - _Depends: 2.2, 3.1, 3.2_
  - _Validation: `go run ./tools/kiro/kirocheck -root .` plus the recorded commands_

## Completion Status

- [x] All implementation tasks (1.1, 1.2, 2.1, 2.2, 3.1, 3.2, 4.1) are delivered on `main` through the replacement stack of #1389:
  - #1413 → `68b2e2c7` (1.1, 1.2)
  - #1414 → `9838b66b` (2.1)
  - #1566 → `866b38a5` (2.2, core guard)
  - #1567 → `2cf51843` (2.2, feature enforcement)
  - #1419 → `ab742678` (3.1)
  - #1420 → `04df06f0` (3.2, 4.1)
- [x] Owner decision 2026-10-27 (design §2, *Refusal scope*): the core guard is per attempt with no parallel-round coordination; a declared policy requires a single-target phase route and binds the guard only when declared.
- [x] Merged-main verification on `04df06f0`:
  - `go build ./...` passes;
  - non-race tests pass for proxymemory, proxymemorystore, runtimebundle, featurehost, proxymemoryhttp, core runtime, archtest and `cmd/lipstd`;
  - the PostgreSQL-direct `proxymemorystore` lane passes 78/78 on a fresh database.
- [x] Remote CI was billing-blocked; local CI equivalents are recorded on each PR. Race lanes are remote-owned.
- Deferred, not completed by this spec:
  - value-level reasoning-effort support and its public ABI (owner ruling D1) → #1569;
  - lazy gathering of the guard facts for ungoverned attempts → #1568;
  - the remaining Deferred items in `requirements.md` stay product backlog.

## Implementation Notes

- Task 4.1 is independently reviewed and controller-verified: all twelve user-control cases are proven against the real backend triple through the production background scheduler, runtime executor and `Backend.Open` with the normal billing composition. U1 asserts each owner's exact provider-open backend/model/route/canonical effort linked by B-leg to that owner's usage (owner-swap mutant fails); U2 asserts both actual phase opens with inverted operator defaults (wrong-provider mutant fails). U3–U12 retain per-phase efforts, partial-override defaults, lock refusal/ordering, absent/out-of-inventory refusal, unsupported-effort refusal, query/alias controls, reload generations, both-engine settings contract (SQLite plus PostgreSQL-direct, second instance, isolation, Forget safety, atomic rollback), and claim-time freeze. The Proxy memory overlay is re-measured at 1239 non-test lines against the re-pinned cap 1264 (marker set unchanged); convergence remains -791/floor -791. This certifies the U1–U12 matrix only. Public effort ABI (pending 2.2b leg), financial recovery, race/live-provider/cross-node/pooled-PostgreSQL certification, whole-feature GO and any remote operation are not claimed.
- Task 3.2 is independently reviewed and controller-verified: the effective target is frozen at worker claim (before request assembly and auxiliary submission); the restored composed proof reaches the real scheduler, runtime and `Backend.Open`, and later settings changes do not alter an admitted call. Removed-default masking uses an additive, both-engine five-column dependency context on the existing isolated settings table, written atomically with the raw overrides after validation; a changed inherited target masks the dependent effort to the operator default, the same inherited target with a narrowed inventory still refuses, and raw overrides plus the HTTP wire are unchanged. Legacy rows written before the context existed have no provenance and retain an explicit refusal; a valid rewrite establishes context (explicit unreleased-feature migration limitation). The literal task boundary extends into the existing task-3.1 store because the distinction must survive restart and shared instances. Slice: 21 Go paths, production +418/−90, tests +1072/−82 (2.56×; 2.11× excluding the restored 192-line composed prefix). Public effort ABI, U1–U12/full-feature certification and financial recovery are not claimed.

- Task 3.1 is independently reviewed and controller-verified: isolated owner-global raw overrides, authenticated GET/PUT/DELETE, separate administrator defaults/owner overrides/effective values/locks, and actual-target/factual validation before either phase is stored. SQLite and PostgreSQL-direct contracts, upgrade/rollback, route/native-binding regressions and focused QA/architecture pass. The measured overlay is 1239/cap 1261, convergence remains -791/floor -791, and the declared-base change count is 76 Go files. Claim-time adoption/freezing (3.2), public effort support (the pending 2.2b leg), U1-U12/full-feature certification and callback-panic recovery are not claimed.

- Task 2.1 has separate explicit owner approval for the measured Proxy memory overlay of 769 lines and cap 794 (25 lines of headroom; 130 above the former cap 639), with convergence −792 against the unchanged −791 floor. Independent review and fresh task-local checks cover the compiler, actual feature handoff, retained candidate-generation isolation, and cleanup on acquisition/disposal failures. The composed fixture does not publish, pin or retire generations; final model-policy enforcement, settings persistence, claim snapshots and U1–U12 certification remain pending.

- This plan is generated alongside `requirements.md` and `design.md` of the same spec, and all three are **approved** (owner, via the parent session, 2026-10-21). The task checkboxes stay unchecked by the spec convention and are checked by the implementer.
- The approved `proxy-codex-memory` slice is not reopened here, and no task is added to the open #1299 stack.
- No task in this plan **edits** the frozen `billing-financial-safety-contracts` spec. The **Enterprise** side is not off limits to *reuse*: it may reuse the existing monetary host integration and financial policy under the open-core model. What stays out of this plan is authoring **new** money logic — a new ledger, pricing, rating or settlement — and importing `enterprise/` from the open-source tree.
