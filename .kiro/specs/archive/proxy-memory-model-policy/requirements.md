# Requirements Document: proxy-memory-model-policy

> **Status: approved.** The owner approved this scope and enabled the remaining tasks ("Proceed with all remaining tasks!", 2026-10-21). `spec.json` records the approval, the owner context and the date. The sibling commercial spec is approved in the same round. This document does not authorize merges or closures, a new money engine, a new ledger or money arithmetic.
>
> **Why a separate spec.** `proxy-codex-memory` is at its owner-approved shrink-only ceiling, and the combined commercial plus model-policy contract does not fit one spec's budget. This spec owns the per-role model control, the operator inventory and locks, the compiled-target enforcement and the settings surface; the sibling spec owns the roles, the economics and the fencing.
>
> **Dependency: satisfied.** The background phases used to be rejected before the backend open. The role registration is **implemented and reviewed** in the archived spec `.kiro/specs/archive/proxy-memory-runtime-corrections` (role registration `4e2d588fe`), so the cases in this spec have a subject: the phases execute on the real path. The archived work is not to be redone.
>
> **Base and land.** This slice is implemented on the branch's own base `0b2d339ee`, which carries the inherited unshipped memory feature and the archived runtime corrections. A rebase onto `main` is **not** a precondition, and the later land stays gated on the foundation reconciliation (#1287) and the owner's merge authorization.

## Project Description

Who has the problem: a tenant who wants the background memory work to use the model and effort they choose, and an operator who wants to say which models may be chosen and which fields may not be changed at all.

Current situation: phase 1 and phase 2 have one operator-wide route selector each and hard-coded reasoning efforts, so a tenant has no say and an operator has no inventory and no lock. A model alias or a route query parameter resolves to a different target than the operator wrote, and checks that do not exist yet would be applied to the wrong thing. The reasoning effort travels as a canonical option that each provider supports or not, and the runtime currently strips it silently when the backend lacks the capability. The operator's inventory is therefore a **policy** statement, never provider-verified support: the backend capability set carries a **boolean** reasoning capability and the model catalog a **tri-state** per-model fact, and neither states which effort **values** a provider supports.

What should change: each phase is controlled independently, the operator can declare an allowed inventory per phase and lock individual fields, the checks run against the **final compiled actual target**, an unsupported effort is refused instead of downgraded, and a tenant can read, change and revoke its own settings over an authenticated, owner-scoped surface that persists in both engines.

## V1 Slice

- **Delivers**: per-phase backend/model/effort control with a per-phase operator inventory; per-field operator locks that a write cannot change and no runtime path can bypass, including a lock introduced after an override existed; enforcement against the final compiled actual target, including alias and query resolution; an explicit refusal for an unsupported reasoning effort; an authenticated `GET`/`PUT`/`DELETE` settings surface that is classified before the per-scope item route and is owner-scoped to tenant and principal; settings that persist in SQLite and PostgreSQL and are consistent across instances; and an effective-settings freeze at claim time, before the billable dispatch.
- **First consumer**: the same Proxy Memory feature that already runs phase 1 and phase 2.
- **Safe degradation**: an out-of-inventory value, a locked field, a not-installed model, an unsupported effort and an unservable compiled target are refused explicitly and counted. Nothing is silently substituted or downgraded, and a settings write never affects an already running job.

## Deferred

- Value-level reasoning-effort support: a public effort-support ABI that states which effort values a provider carries. Owner ruling D1 defers it to a follow-up dependency; this spec enforces only the backend's boolean reasoning capability and the model catalog's tri-state per-model fact, and converts the runtime's negotiated strip to an explicit, counted refusal.
- Per-project (not per-tenant/principal) settings. Follow-up: TBD.
- Settings for other feature planes (for example a per-tenant reasoning default for ordinary traffic). Follow-up: TBD.
- An operator dashboard for the settings and the effective targets. Follow-up: TBD.
- Automatic least-cost selection among the allowed inventory. Follow-up: TBD.
- A per-tenant budget or quota for the background work. Follow-up: TBD.
- The money side of the background work. Follow-up: `.kiro/specs/proxy-memory-commercial/`.

## Boundary Context

- **In scope**:
  - the per-phase target configuration, the per-phase allowed inventory and the per-field locks;
  - the effective-target resolution, the compiled actual target and the capability enforcement against it;
  - the refusal rules for an out-of-inventory value, a locked field, a not-installed model and an unsupported effort;
  - the persisted, owner-scoped settings and their authenticated resource;
  - the freeze of the effective settings per claimed job and the reload re-derivation of defaults and locks;
  - the acceptance matrix in this document, with its local and remote certification gates.
- **Out of scope**:
  - any money, ledger, rating, settlement, price or account concept;
  - the roles, the economics and the fencing, which are specified in `.kiro/specs/proxy-memory-commercial/`;
  - the frozen `billing-financial-safety-contracts` spec;
  - the approved `proxy-codex-memory` requirements, design and tasks (unchanged);
  - the implementation of the corrective stack in #1299, which is still open and is not extended by this slice.
- **Adjacent expectations**:
  - the feature package may not import core implementations, so the final compiled target is resolved through a **feature-owned port that the composition root binds**;
  - the settings live in the **existing isolated feature persistence** of the proxy-memory family, not in the core continuity database and not on its single connection or its mandatory pool;
  - the money logic stays in the **Enterprise** tree, which may reuse the existing monetary host integration and financial policy; this slice adds no ledger, no pricing, no rating and no settlement, and the open-source tree never imports `enterprise/`;
  - the implementation runs on the branch's own base `0b2d339ee`, which carries the inherited unshipped memory feature; a rebase onto `main` is not a precondition, and the land is gated on the foundation reconciliation (#1287) and the owner's merge authorization;
  - operators run the local CI equivalents while remote CI is blocked, and keep the local and remote evidence distinct.
- **Boundary ownership**:
  - the feature plugin owns the policy domain, the settings contract and the effective-target resolution;
  - the composition root owns the generation-bound port binding;
  - infrastructure owns the persisted settings;
  - the standard HTTP layer owns the settings resource mount.
- **Revalidation triggers**:
  - the routing selector compilation and the alias resolver;
  - the canonical capability negotiation and the negotiated downgrades;
  - the per-backend capability sets and the model catalog facts;
  - the route-parameter merge that can override a call's options;
  - the `proxy-memory` persistence family schema;
  - the mount contract inventory for the standard HTTP layer.

## Requirements

### Requirement 1: Per-phase targets, per-phase inventory and per-field locks
**Objective:** As a tenant, I want to choose the backend, model and reasoning effort of each background phase, and as an operator, I want to say which choices exist and which fields are not mine to change, so that quality and cost are controllable and cannot be bypassed.

#### Acceptance Criteria
1. Phase 1 and phase 2 shall be governed independently, each with its own target of **backend, model and reasoning effort** — the three fields a user may choose — and each with its own **allowed inventory** of backends, models and reasoning efforts, so that one owner may use a different model for extraction and for consolidation and a different effort for each. The route selector is **not** a fourth user-facing field: it is **derived** by the proxy from the operator's default route and the chosen backend and model, so it cannot become a cost or policy bypass. Every phase shall declare its **default target**, its **lock set** and its **allowed inventory** explicitly: the configuration is rejected when any of the three is missing, and an **empty inventory never means "allow everything"** — it means the phase accepts no user-selectable value for that field until the operator declares the allowed ones.
2. The operator shall be able to lock a field **per phase and per field** (backend, model and reasoning effort independently). A lock shall (a) reject a write that changes the locked field, (b) be enforced at runtime so that no request path bypasses it, and (c) win over an override that already existed when the lock was introduced, so the stale override does not survive.
3. A per-owner override shall change only the fields it names; every field it does not name shall keep the operator default, and the effective value of a field shall be resolved as **locked operator value, else permitted owner value, else operator default**.
4. A value outside the phase's allowed inventory, a model that is not installed in the deployment, and a reasoning effort the final compiled actual target does not support shall each be **refused explicitly**, never silently substituted, downgraded or ignored. The refusal on the write path is primary; a value that became invalid after a narrowing reload is refused at dispatch time, explicitly and counted. Because the effort values are **provider-specific**, the allowed efforts are declared **per phase and per target**, and the enforcement of the chosen effort against the actual target is the compiled-target capability check: the backend's boolean reasoning capability and the model catalog's per-model tri-state fact must prove the capability, and a requested effort is refused explicitly and counted when that capability is false **or unknown** — an unproven capability is never treated as support. A negotiated downgrade that would strip the effort is converted to an explicit, counted refusal for proxy-memory calls, never served as a downgrade. This spec adds **no public effort-support ABI**; value-level effort support is a **deferred follow-up dependency** (owner ruling D1), and no closed global list of effort values is assumed.
5. The checks shall run against the **final compiled actual target** — the derived selector compiled after model-alias resolution and per-leg query parameters — so that neither an alias nor a query parameter can point the call at a target the inventory, a lock or a capability check does not cover. The recorded usage and the quote shall describe the actual target that served the call.

### Requirement 2: Settings surface, persistence and freeze point
**Objective:** As a tenant, I want my phase settings to persist across restarts and instances and to be visible and revocable by me, so that I control my own background spend.

#### Acceptance Criteria
1. The proxy shall persist the owner's **overrides** — the unprojected per-field values the owner set, not the projected effective values — per tenant and principal, in SQLite and in PostgreSQL, with identical semantics, and a second instance shall read the same overrides. The **effective** value of every field shall be **recomputed on read and on reload** from the operator defaults, the locks and the stored overrides, so a changed default or a new lock takes effect immediately. The owner key shall hold no project, so the settings are global for that owner and apply to every project of that owner; forgetting a bank shall not delete the owner's overrides. The settings shall live in the **existing isolated feature persistence** of the proxy-memory family, never in the core continuity database or its connection pool.
2. The authenticated settings resource shall support read, update and delete of the caller's own settings, shall answer another tenant's settings exactly as it answers an unknown caller, and shall never expose an account identity. It shall be classified **before** the per-scope item route, so a settings request can never be parsed as a scope identifier, and the memory tool endpoint shall expose no settings mutation.
3. The **effective** settings shall be computed and **frozen** when a job is **claimed**, before its billable dispatch: that frozen snapshot is what the job runs with. A preference change between queueing and dispatch applies to a job claimed afterwards, while an already admitted, running job keeps the snapshot it was admitted with. The worker queue and the auxiliary scheduler queue are different queues and shall not be conflated.
4. A configuration reload shall re-derive the operator defaults and the locks, recompute the effective values from the stored overrides, and a lock in force before the reload shall still apply after it, while an override that depended on a removed default is masked rather than silently re-applied. Rows written before dependency context existed carry no provenance and keep the existing explicit, counted refusal instead of masking; a valid rewrite establishes context. No history is backfilled (owner-ratified unreleased-feature migration limitation, 2026-10-22).

## Acceptance Matrix — User Control Cases (U1–U12)

The twelve cases are the owner's exact list. Every case is proven against the real path with the real backend triple (backend, model and canonical options) through the production background scheduler → runtime executor → backend open, with the normal billing composition, and never with a mocked background client. Each row names the requirement that governs it and the gate that proves it.

| ID | Case | Expected observable outcome | Requirement | Evidence | Gate |
|---|---|---|---|---|---|
| U1 | Two **different users choose different models** | Each user's background work runs against their own selected model; neither user's selection affects the other | 1.1, 2.1 | real backend triple per user | local integration, two users |
| U2 | One user uses **model A for extraction and model B for consolidation** | The two phases dispatch to the two different models | 1.1 | two real backend triples, one owner | local integration, per-phase |
| U3 | A user sets a **custom reasoning effort for each phase** | Each phase carries its own effort; the effort reaches the provider as a canonical option | 1.1, 1.4 | real backend triple per phase | local integration, per-phase effort |
| U4 | A user overrides **only part** of a phase target | The overridden fields change; every other field keeps the operator default | 1.3 | real backend triple, partial override | local unit plus integration |
| U5 | A **lock** is in force | A write that changes a locked field is rejected, and a runtime path that tries to bypass the lock is refused too | 1.2 | real backend triple, write and runtime attempt | local unit plus integration |
| U6 | A **lock is introduced after** an override already existed | The lock wins: the effective target returns to the locked value, and the stale override does not survive | 1.2 | real backend triple, lock after override | local integration, ordering |
| U7 | The selected model is **not installed, or not allowed** | The write or the dispatch is refused explicitly; the proxy does not silently substitute another model | 1.4 | real backend triple, absent and out-of-inventory model | local unit plus integration |
| U8 | The selected **reasoning effort is unsupported** by the target | The request is refused explicitly; the effort is never silently downgraded | 1.4 | real backend triple, unsupported effort | local integration, refusal |
| U9 | An **alias or a query parameter** points elsewhere | The inventory, lock and capability checks run against the final compiled actual target, so neither bypasses the control; the recorded usage describes the actual target that served the call | 1.5 | real backend triple, alias and query | local integration, compiled target |
| U10 | The operator **reloads** the configuration | The defaults and the locks are re-derived, and a lock in force before the reload still applies after it | 1.2, 2.4 | real backend triple across a reload | local integration, reload |
| U11 | Settings **persist in SQL and PostgreSQL** across **multiple instances** | Both engines store the same settings with the same semantics, and a second instance reads the same effective settings | 2.1 | shared store, two instances | local store contract, both engines |
| U12 | Preferences change **between queueing and dispatch** | A job claimed after the change uses the new preferences, frozen before its billable dispatch; an already admitted, running job keeps the target it was admitted with | 2.3 | real backend triple, change between queue and claim | local integration, freeze timing |

## Certification Gates

- **Local gates** (required before a merge request): the focused package tests without `-race`; the SQLite and PostgreSQL-direct store contract for every changed persistence path; the composed-generation integration for every user-control case; the Requirement 11.5 convergence and overlay measurement; the local lint and vet equivalents; and the spec gate. The task-local development-host policy file is used for every command.
- **Remote gates** (owned by remote CI): the race lanes. They are currently blocked by the billing block on GitHub Actions and are expected to resume after 31 October 2026. A local run is never presented as a race result.
- **Not claimed by this spec**: live-provider certification, cross-node consistency certification, and any merger or closure of the related issues. The feature remains unshipped and the land stays gated on the foundation reconciliation (#1287). A user-control case whose guarantee this spec requires is never marked "not claimed" to reach green: an unresolved case **blocks** the readiness of this slice and the missing capability is named. Certification runs **last**, after the features are implemented.
