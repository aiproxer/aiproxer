# Design Document: proxy-memory-model-policy

> **Status: approved.** The owner approved this scope and enabled the remaining tasks on 2026-10-21; `spec.json` records the approval, the owner context and the date. Section numbers (§N) are referenced from `tasks.md`.
>
> **Naming caveat.** Names of new types and fields are **provisional placeholders** until the source authority is confirmed. The port below mirrors an existing, source-backed precedent rather than inventing a framework.
>
> **Base.** Implemented on the branch's own base `0b2d339ee`, which carries the inherited unshipped memory feature and the archived runtime corrections (`4e2d588fe`, `0b2d339ee`). A rebase onto `main` is not a precondition; the land stays gated on the foundation reconciliation (#1287) and the owner's merge authorization. Certification runs last, after the features are implemented.

## Overview

Proxy Memory's two background phases today carry one operator-wide route selector each and a hard-coded reasoning effort. This design gives each phase its own governed triple (backend, model, reasoning effort), an operator inventory and per-field locks, and a tenant override; it resolves the checks against the **final compiled actual target**; and it exposes an authenticated, owner-scoped settings surface whose effective values are frozen per claimed job.

Two structural facts shape the design:

- **The feature cannot resolve the target itself.** The alias resolver, the default backend and the known-backend set live on the runtime executor, which is built **after** the feature host compiles. The feature package may not import core implementations. So the feature needs a **narrow, feature-owned port that the composition root binds**, exactly like the route-override admin's selector validator.
- **The capability facts are coarser than the policy.** The backend capability set carries a **boolean** reasoning capability, and the model catalog carries a **tri-state** per-model reasoning fact (unknown / unsupported / supported). There is **no provider-specific effort-value fact** anywhere. So an operator-declared inventory can constrain what a user may choose, but it must **not** be presented as provider-verified support, and the verification of a chosen effort needs a runtime capability surface that does not exist yet.

### Goals
- Per-phase, per-field governance: defaults, a per-phase inventory, per-field locks, and a per-owner override (Requirement 1).
- Enforcement against the final compiled actual target, and an explicit refusal instead of a silent downgrade (Requirement 1.4, 1.5).
- An authenticated, owner-scoped, cross-engine settings surface with a correct freeze point (Requirement 2).

### Non-Goals
- Money, ledger, rating, settlement, account concepts.
- A generic selection framework, a new route-compiler, or a new capability vocabulary.
- A new model catalog, a new alias resolver, or any change to the canonical capability negotiation.
- The roles, the economics and the fencing, which belong to `.kiro/specs/proxy-memory-commercial/`.

## Boundary Commitments

### This Spec Owns
- The per-phase target configuration, the per-phase allowed inventory, the per-field locks and their precedence.
- The effective-target resolution and the compiled-actual-target enforcement, through a feature-owned port bound by the composition root.
- The persisted, owner-scoped settings, their service and their authenticated resource.
- The freeze of the effective settings per claimed job and the reload re-derivation of defaults and locks.

### Out of Boundary
- The canonical call and event contracts, the routing grammar, the alias resolver, the capability negotiation and the model catalog, all of which are consumed as they are.
- The approved `proxy-codex-memory` scope and the sibling commercial spec.

### Allowed Dependencies
- The feature package `internal/plugins/features/proxymemory` may import `pkg/lipapi`, `pkg/lipsdk/...`, the stdlib, `gopkg.in/yaml.v3` and Prometheus. It must not import `internal/core/...`, `internal/infra/...` or `internal/standardplugins/...`.
- Only `pkg/lipapi` is importable from a feature for capabilities: `Negotiate`, `ApplyNegotiatedDowngrades` and the capability set. `internal/core/routing`, `internal/core/capabilities` and `internal/core/modelcatalog` are core implementations and are **not** importable from the feature.
- The composition root may import both the core routing/model facts and the feature package, and binds them behind the feature-owned port.
- The infra package `internal/infra/proxymemorystore` may import the feature package, `internal/bunmigrate`, `github.com/uptrace/bun` and its dialects.

### Revalidation Triggers
- The routing selector compilation, the alias resolver and the route-parameter merge.
- The canonical capability negotiation and the negotiated downgrades.
- The per-backend capability sets and the model catalog reasoning facts.
- The `proxy-memory` schema and the mount contract inventory.

## Architecture

### Seams Reused
| Seam | Use | Requirement |
|---|---|---|
| `routing.CompileSelector` (trim → alias resolve → parse → defaulting) | the final compiled target, applied by the composition root | 1.5 |
| `routing.AliasResolver.Resolve` (first matching pattern wins) | the alias resolution that the compiled target must include | 1.5 |
| `routing.ExpandFailoverGroups` and `AttemptCandidate` | the candidate expansion of the compiled target | 1.5 |
| the route-parameter merge, applied per attempt after the candidate is chosen | the query parameters that can override the call's options, **including the reasoning effort** | 1.5, 1.4 |
| `pkg/lipapi` capability negotiation, the capability set, and the negotiated downgrades | the capability check and the two places where the effort is stripped today | 1.4, 1.5 |
| the per-backend static capabilities and their dynamic resolution | the boolean reasoning capability of a backend | 1.4 |
| the model catalog's per-model tri-state reasoning fact and the effective-capability resolution | the per-model reasoning fact and its intersection with the backend capabilities | 1.4 |
| the attempt evidence that records backend, model and reasoning effort | the evidence that a real run produced the expected target | 1.5, U9 |
| `internal/core/routeoverride` (a command-service port plus a selector-validator port) and its generation-bound binding in the composition root | the **precedent** for a narrow, feature-owned port bound by the composition root | 1.5 |
| the admin HTTP surface and its mount behind the request-plane access-auth stack | the **precedent** for an authenticated admin resource | 2.2 |
| `internal/infra/proxymemorystore` | the persisted settings | 2.1 |

### New Seams, each tied to a requirement
- **A feature-owned target-compiler port** (Requirement 1.5): the feature asks for "the final compiled actual target and the capabilities it can serve"; the composition root answers using the generation's aliases, default backend, known backends, the model catalog and the canonical capability negotiation. This is the same shape as the route-override admin's selector validator, bound in the same place, and it is **not** a generic selection framework.
- **One persisted settings table** in the existing `proxy-memory` persistence family, keyed by tenant and principal only.
- **One settings resource** in the standard HTTP layer, classified before the per-scope item route.

### Project Boundary Questions (AIProxer)
- **Core-owned or plugin-owned?** Plugin-owned policy (targets, inventory, locks, overrides, settings), with one narrow core-facing port bound by the composition root.
- **New canonical concept?** No. The canonical call keeps its options; the effort stays a canonical generation option.
- **Streaming-first preserved?** Yes. The phases are auxiliary collections, unchanged.
- **Provider SDK leakage?** None. **No retry after first output?** Unchanged.
- **Secure-session posture?** Unchanged; the settings never cross a client boundary.

## Components and Interfaces

### §1 The target-compiler port (the smallest reachable seam)
The feature cannot compile the final target itself: the alias resolver, the default backend and the known-backend set live on the runtime executor, which is constructed after the feature host compiles, and the feature may not import core implementations.

- The **feature** owns the port, with the shape of the route-override admin's selector validator: a single method that takes the phase's target and returns the final compiled primary plus the capabilities that target can serve, or an error.
- The **composition root** binds it, next to the route-override validator binding, from the generation's executor: the alias resolver, the default backend, the known-backend map with its static capabilities and their dynamic resolution, the model catalog's per-model facts, and the canonical capability negotiation.
- The port is **narrow and generation-bound**: it is rebuilt per generation, so a reload re-derives it (Requirement 2.4). It is not a registry, not a resolver chain and not a framework.
- The compiled target must include the **route parameters** that the runtime merges per attempt after the candidate is chosen, because those parameters can override the call's options, the reasoning effort included. A check that ignores them would validate a target that never serves the call (Requirement 1.5, U9).

### §2 Capability facts, and what may not be claimed
- **Owner-approved D1 scope:** task 2.2 requires proof of reasoning capability from the backend boolean and applicable model-catalog tri-state fact; false or unknown capability refuses a requested effort explicitly and observably. The generic internal downgrade-guard extension converts negotiated effort stripping at both runtime sites into a counted refusal for governed Proxy Memory calls only; an absent guard preserves ordinary runtime behavior. Value-level effort support and its public ABI are deferred, not completion dependencies of this narrowed task. Earlier pending-surface descriptions below concern that deferred value-level work, not permission to treat unknown reasoning capability as support.
- The backend capability set carries a **boolean** reasoning capability. The model catalog carries a **tri-state** per-model reasoning fact. **Neither** states which effort *values* a provider supports.
- Therefore: an operator-declared inventory constrains what a user may choose, and it is a **policy** statement. It must never be presented as provider-verified support, and no closed list of effort values may be claimed to be supported everywhere.
- The two places where a chosen effort is currently **stripped** today are the pre-open candidate admission and the post-request transform, both of which apply the negotiated downgrades. A downgrade is logged, but it is invisible to the feature, invisible in the feature's metrics, and it would silently violate an operator's lock on the effort. This is why the effort check is a **refusal** and not a warning (Requirement 1.4, U8).
- The verification of the chosen effort against the actual target needs a **runtime capability surface that does not exist yet**: the per-provider effort values are not in the capability set and not in the model catalog. That is a **dependency to be confirmed and, if needed, extended or reused** — this design does not invent a vocabulary, and it does not claim a closed global list (marked *pending evidence*).
- **Refusal scope (owner decision, 2026-10-27).** The core's generic pre-open guard refuses one **attempt**: on the sequential open path that refusal ends the operation (an open error is terminal, so there is no failover to another candidate), and in a parallel round it fails that arm only — every arm passes the same guard before it opens, so a sibling that serves was checked too. The core adds **no parallel-round coordination** for the guard. Whole-operation refusal for the memory phases comes from the feature instead: a configuration that declares the model policy must give each phase a **single-target route**, and the feature binds its guard only when the policy is declared, so ordinary traffic and undeclared deployments run with no guard at all.
- The composed test fixture must **declare the capabilities it actually supports** and assert that the backend's options remain frozen: the current sentinel backend declares only streaming and tools, so it strips every requested effort. That is a property of the fixture, **not** evidence that production backends strip the effort.

**The pre-open invariant (task 2.2).** The compiled-target check runs before the dispatch, but the runtime may still change the attempt afterwards — a request-part hook may rewrite it, and the runtime's own negotiated downgrade may strip the reasoning effort. The same target and the same reasoning effort are therefore **re-asserted at the provider-open chokepoint**, after every mutating transform and before any provider I/O, through a **generic, internal pre-open guard** the composition root binds and the feature owns through a feature-owned port. The guard recomputes the frozen expectation from the hook-immune baseline and the actual candidate's route parameters, so it needs no second compilation and adds no provider round trip. A divergence is an **explicit refusal**: the attempt closes as never started and, on the single-target route a declared policy requires, the operation does not fall back to another candidate (see *Refusal scope* above for a parallel arm). The canonical open path (ordinary, parallel arm and B2BUA continuation) is covered; the wire fast path carries no canonical call and is not reachable for a governed auxiliary call.

**The per-value effort support (task 2.2, internal half).** The runtime capability surface for effort values is a **per-candidate provider query** (`execbackend.Backend.ResolveEffortSupport`, internal) whose answer is a tri-state — supported, unsupported, or unknown. The root never carries a closed global list of effort values, and an operator inventory is never provider-verified support. A *supported* answer accepts, an *unsupported* answer refuses explicitly before the provider open, and an *unknown* answer does not refuse on the value but is counted and logged as unverified. No in-process provider owns a per-value vocabulary today, so every in-process answer is *unknown*; the executable-connector half needs the additive public ABI of the follow-up slice and stays **pending owner approval**.

### §3 Per-phase targets, inventory, locks and precedence
- Each phase has its own target of **backend, model and reasoning effort** — the three fields a user may choose — and its own allowed inventory of backends, models and efforts. Every phase **declares** its default target, its lock set and its allowed inventory: the configuration is rejected when one is missing, and an **empty inventory never means "allow everything"**. The effort values are **provider-specific**, so the allowed efforts are declared per phase and per target, and the enforcement of the chosen effort needs a runtime capability surface that must be confirmed or extended before the slice is complete. The **route selector is derived, not chosen**: the proxy builds it from the operator's default route and the selected backend and model, so it is never a fourth override and never a way to reach a target the inventory, the locks or the capability checks do not cover. An absent backend or model keeps the operator's default; an absent effort keeps today's value; an empty inventory means "no user-selectable override", not "everything allowed".
- **Locks** are per phase and per field. A locked field rejects a write, is enforced at runtime, and wins over an override that already existed when the lock was introduced. The stale override does not survive, and the effective value falls back to the locked operator value.
- **Precedence** per field: locked operator value, else permitted owner value, else operator default. A partial override changes only the fields it names.
- **Refusals** are explicit and counted, on the write path first (an out-of-inventory value, a not-installed model, an unsupported effort) and at dispatch time for a value that became invalid after a narrowing reload. Nothing is silently substituted or downgraded (Requirement 1.2, 1.3, 1.4, U4, U5, U6, U7, U8).
- **No generic framework.** The policy is a small, closed domain in the feature: two phases, three fields, one inventory and one lock set per field.
- **The installation fact (task 2.2).** The not-installed refusal uses the deployment's **frozen registry publication**: a model is *installed* when a frozen row names it as the backend's canonical or native id, *confirmed absent* when the backend's inventory is known (a fresh enumeration) and the model is not in it, and *unknown* otherwise — a cached publication, an empty discovery whose freshness the status does not prove, an unavailable discovery, a missing record, or a canonical registered under another backend. A *confirmed absent* model is refused before the provider open; an *unknown* state is counted and logged. The publication is a snapshot, so a model installed after the last refresh is refused until the next refresh; that window is the operator's registry refresh cadence, and it is logged and counted, never hidden. The operator's allowed inventory remains a policy constraint and is never presented as installation.
- **The operator's strict switch (task 2.2).** `models.strict` selects the strict policy for the two facts the deployment does not prove (the installation and the per-value effort support): the default serves them explicitly degraded, counted and logged, and the strict policy refuses. It is an operator setting, not a fourth owner-selectable field: it adds no target, no model and no effort to the owner-facing surface, and it never widens what an owner may choose.

### §4 Overrides, effective values and the freeze point
- **Stored = overrides.** The store keeps the owner's **unprojected per-field overrides**, not the projected effective values. The effective value of each field is **recomputed** from the operator defaults, the locks and the stored overrides, on every read and on every reload, so a changed default or a newly introduced lock takes effect at once. The stored row therefore stays minimal and never goes stale (Requirement 2.1, 2.4).
- **Ownership and isolation.** The overrides are stored per tenant and principal, with **no project** in the key, so they are global for that owner and apply to every project of that owner. Forgetting a bank does **not** delete the owner's overrides.
- **Persistence location.** The overrides live in the **existing isolated feature persistence** of the proxy-memory family, never in the core continuity database: the continuity database has a single connection and one writer, and a settings write on it would hold the connection the mandatory core path needs. The isolated feature database is already the home of the memory tables, so the settings inherit its owner predicates, its durability rules and its database-parity registration.
- **Surface.** The service exposes read, update and delete. Every error that is not the caller's own settings answers exactly like an unknown caller, and no response carries an account identity. The resource is `GET`, `PUT` and `DELETE /v1/lip/memory/settings`, classified **before** the per-scope item route, behind the same request-plane access-auth stack as the rest of the memory surface: authentication first, then eligibility, then routing. The memory tool endpoint exposes no settings mutation (Requirement 2.2).
- **The freeze point.** The effective values are computed and frozen when a job is **claimed**, before its billable dispatch; that frozen snapshot is what the job runs with. A preference change between queueing and dispatch therefore applies to a job claimed afterwards, while an already admitted, running job keeps the snapshot it was admitted with. The worker queue and the auxiliary scheduler queue are different queues and are not conflated (Requirement 2.3, U12).
- **Reload.** A reload re-derives the defaults and the locks and recomputes the effective values from the stored overrides; a lock in force before the reload still applies, and an override that depended on a removed default is masked (Requirement 2.4, U10).

## File Structure Plan

| Area | Files | Responsibility |
|---|---|---|
| Policy domain | `internal/plugins/features/proxymemory/policy.go` (new), `config.go` (modified) | the phase targets, the per-phase inventory, the per-field locks, the precedence and the effective-target resolution (§3) |
| Target port | `internal/plugins/features/proxymemory/settings.go` (new) — the feature-owned port and the compiled-target type | the port the feature consumes (§1) |
| Port binding | `internal/infra/runtimebundle/proxy_memory_policy.go` (new) | the generation-bound binding of the port, next to the route-override validator binding (§1) |
| Generation input | `internal/standardplugins/featurehost/inputs.go` (modified) and `proxy_memory.go` (modified) | hand the bound port to the feature; compile the policy (§1, §4) |
| Settings persistence | `internal/plugins/features/proxymemory/settings.go` (the store contract), `internal/infra/proxymemorystore/settings.go` (new) and the baseline migration — in the **existing isolated feature persistence**, never the core continuity database | the persisted, owner-scoped **overrides** (§4) |
| Settings resource | `internal/stdhttp/proxymemoryhttp/handler.go` (modified) | the route classification and the three handlers (§4) |
| Worker | `internal/plugins/features/proxymemory/worker.go` (modified) | freeze the effective settings at claim time (§4) |

## Budget and Architecture Gates

- The **Requirement 11.5** net shrinkage floor is `-791` and the measured convergence on this worktree is `-793`: **two lines of headroom**, not a free budget. The affected surfaces are `internal/infra/runtimebundle`, `internal/infra/runtimehost`, `internal/stdhttp`, `cmd/lipstd` and `pkg/lipruntime`.
- The **"Proxy memory" path overlay** is measured at 614 against a cap of 639, so 25 lines are available for the overlay-credited paths; the overlay table is extensible by path marker and overlays must stay pairwise disjoint.
- This spec adds a **new file in `internal/infra/runtimebundle`**, which is an affected surface and is **not** currently overlay-credited. The honest accounting: the new file must be **credited by extending the existing "Proxy memory" path overlay marker** — not by a new, overlapping overlay — so its lines are credited and the convergence delta stays where it is; the overlay cap is then **re-pinned with the measured value** in the same slice, and the pairwise-disjointness gate must keep passing. If the measured value exceeds the available headroom, the excess goes to the owner as a **separate budget approval**. No slice may assume the remaining 639 lines are free, and no budget marker may be widened to make a delta disappear.
- The settings resource in the standard HTTP layer is an affected surface and is overlay-credited; its lines are expected to exceed the remaining overlay room, so the same re-pin applies.
- The dbparity catalog is the authoritative persistence inventory: a new table needs its own entry and a parity capability.

## Error Handling
- **Refused write**: an out-of-inventory value, a locked field, a not-installed model or an unsupported effort is rejected with a bounded, content-free error naming the offending field; the stored settings are unchanged.
- **Refused dispatch**: a value that became invalid after a narrowing reload is refused at dispatch time, counted, and the job falls back to the ordinary failure path.
- **Port failure**: when the target-compiler port is unavailable or errors, the phase is refused explicitly and counted; it is never dispatched against an unchecked target.
- **Settings store failure**: a background job falls back to the operator default, explicitly and counted; the settings resource answers with a content-free 500.

## Security Considerations
- The settings are owner-scoped: a read, update or delete for another owner answers exactly like an unknown caller, and the resource is behind the same access-auth stack as the rest of the memory surface.
- The allowed inventory is closed, and a lock cannot be bypassed by a request path, an alias or a query parameter, because the checks run against the final compiled actual target and the selector is derived, never chosen.
- No settings response ever carries an account identity, a model credential or a client-extensible field.
- A masked or lock-introduced override can never survive and silently redirect a phase.
- **A closed list of known classifications is not unspoofability.** The registered auxiliary roles and the declared sponsor keys are a bounded vocabulary that says which labels are *known*; they say nothing about who supplied a request. The authority for a background leg comes from the **trusted boundary** — the operator's configuration and the account the host's identity seam resolves and freezes — and the design claims no forgery guarantee that the current request-populated carriers cannot support.

## Testing Strategy
Test proportionality: one table per decision, one integration test per seam, and the existing store contract suite for the new persistence.

- **Real path, everywhere.** Every case runs the real backend triple (backend, model, canonical options) through the production background scheduler → runtime executor → backend open, with the normal billing composition. Only the provider's **content** is stubbed. **A mocked background client may not prove any case.**
- **Fixture honesty**: the recording backend declares the capabilities it actually supports, and the test asserts that the **backend's options remain frozen** — the requested effort, the model and the backend as compiled. A fixture that declares fewer capabilities than production would otherwise hide the very behaviour under test.
- **U1–U12**: the twelve user-control cases as integrations and unit tables, each with the assertions named in the matrix; U11 runs on SQLite and PostgreSQL-direct with two instances; U12 drives the change between the queue and the claim.
- **Regressions**: the queued and durable decision paths, the phase defaults, the strict configuration decode, and the existing feature and store contracts.
- **Certification**: local non-race runs, the SQLite plus PostgreSQL-direct store contract, the composed-generation integration, the Requirement 11.5 and overlay measurement, and the spec gate. The race lanes remain remote-owned.

## Requirements Traceability

| Requirement | Components | Sections |
|---|---|---|
| 1.1 | Per-phase targets and per-phase inventory | §3 |
| 1.2 | Per-field locks and their precedence over existing overrides | §3 |
| 1.3 | Partial override and field precedence | §3 |
| 1.4 | Explicit refusals: inventory, not installed, unsupported effort | §2, §3 |
| 1.5 | Final compiled actual target, alias and query coverage | §1, §2 |
| 2.1 | Override persistence, cross-engine, owner-global, forget-safe, isolated store | §4 |
| 2.2 | Settings resource, auth order, route classification, no MCP mutation | §4 |
| 2.3 | Freeze at claim, before the billable dispatch | §4 |
| 2.4 | Reload re-derivation and masking | §1, §4 |

## Slice Boundaries and Dependencies

- **Slice A — policy domain** (tasks 1.1, 1.2): the per-phase targets, the per-phase inventory, the per-field locks and the precedence, in the feature only. No core, no composition root, no money.
- **Slice B — compiled target** (tasks 1.3, 1.4): the feature-owned port, the composition-root binding, and the enforcement against the final compiled actual target, including the route-parameter coverage. It depends on Slice A and on the **runtime capability surface** for the effort value, which is a *pending evidence* dependency.
- **Slice C — settings** (tasks 2.1, 2.2): the persisted settings, the service, the resource and the freeze at claim. It depends on Slice A.
- **Slice D — certification** (task 3.1): the U1–U12 run, with the local and remote gates kept distinct, and the Requirement 11.5 and overlay re-pin.
- **Foundation dependency and base**: implemented on the branch's own base `0b2d339ee` (inherited unshipped memory feature, archived runtime corrections). A rebase onto `main` is **not** a precondition and would drop that feature; the later land is gated on the **foundation reconciliation** (#1287) and the owner's merge authorization. The corrective stack in #1299 stays open and receives nothing from this slice.
- **Cross-spec dependency: satisfied.** The role registration is implemented and archived in `.kiro/specs/archive/proxy-memory-runtime-corrections` (`4e2d588fe`), so the background phases execute and the cases here have a subject. The archived work is not to be redone.

## Open Questions and Pending Evidence

- The **runtime capability surface for effort values**: the per-provider effort values are not in the backend capability set and not in the model catalog, so the verification of a chosen effort needs an existing surface to reuse or a narrow extension to add. *Pending evidence; the slice that needs it is Slice B.*
- Whether the effort check may be satisfied by the operator's inventory plus the tri-state model fact alone. *Owner decision; this design does not assume it.*
- The **status of the open-core split** for the model catalog and the capability facts. The implementer checks the **current** split branch before writing code and does not assume the money domain has moved. *Checked locally at `b4587e66`: only the workload-identity allowlist is carved to the usage-evidence package; the money files are still in the open-source billing package.*
