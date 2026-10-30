# Requirements Document: proxy-memory-runtime-corrections

> **Status: approved (narrow scope).** The owner approved exactly this scope — the two auxiliary roles with their real-executor proof, and the bank-claim retry gate with its deterministic interleaving. `spec.json` records the approval, the quoted user context and the approval date. The broader drafts (`.kiro/specs/proxy-memory-commercial/`, `.kiro/specs/proxy-memory-model-policy/`) remain **unapproved** and are not ratified by this document.

## Project Description

Who has the problem: every operator of Proxy Memory, because two runtime defects make the feature inert or unsafe.

Current situation:

- **The background phases never run.** The feature stamps the auxiliary roles `proxy_memory_phase1` and `proxy_memory_phase2` onto its requests, but the closed-world workload allowlist knows only two other roles. The projection therefore fails, and the failure is **fatal**: it is unconditional in the runtime's request-authority admission — before the branch that tolerates a missing request coordinator, so it fires on a usage-only host too — and it happens while the request is being prepared, **before** the billing call identity is stamped. Composed effect today: no billing call identity, no submit, **no backend open**, no terminal usage record, and only a bounded `phase1_failed` warning.
- **The bank claim ignores the retry gate.** The candidate query filters on the next attempt time, but the conditional update revalidates only the dirty flag and the lease. A late failure from a previous generation can release the lease and schedule a backoff in the gap between the two statements, and the stale update then leases the bank early, increments the attempt counter and ignores the scheduled backoff.

What should change: the two roles are registered in the closed list, and a composed run proves that both phases reach the backend through the real scheduler, the real executor and a real backend, with a distinct billing call identity and separate terminal call and leg records per phase; and the bank claim re-checks the retry gate inside its conditional update, proven by a deterministic interleaving on both supported engines.

## V1 Slice

- **Delivers**: two registered auxiliary roles; a composed production-path proof of both phases (one backend open per phase, a distinct billing call identity per phase, one terminal call record and one terminal leg record per phase with the exact role); unknown roles still rejected with zero backend opens; the bank claim's retry-gate re-check with a deterministic interleaving on SQLite and PostgreSQL-direct.
- **First consumer**: the existing Proxy Memory feature, on any deployment.
- **Safe degradation**: the composed proof fails loudly if a phase does not open the backend; an unknown role keeps failing closed before the backend is opened; the bank claim skips a candidate that left the window instead of leasing it with a stale candidacy.

## Deferred

Everything outside the approved narrow scope stays unapproved and is specified in the broader drafts, which the owner has **not** ratified:

- the explicit operator sponsorship mode, the funding admission, the all-attributable per-leg charging, the exactly-once terminal handoff and the account freeze: `.kiro/specs/proxy-memory-commercial/` (unapproved).
- the pending first-turn decision closure (test-only composition pin, no production hardening): `.kiro/specs/proxy-memory-commercial/` Requirement 3 (unapproved).
- the per-role model control, the per-field locks, the allowed inventory, the settings surface, the cross-engine overrides and the freeze at claim: `.kiro/specs/proxy-memory-model-policy/` (unapproved).
- The Agent Loop Guard verifier role, which the same closed list rejects: different feature, out of scope.
- The pre-existing positional lost-race skip in the bank claim: the session claim uses an exact skip list; the difference is a follow-up, not part of this slice.

## Boundary Context

- **In scope**:
  - the two role constants and their two entries in the closed allowlist, in the current base file;
  - the composed real-path proof of both phases and the focused seam proof that an unknown role opens the backend zero times;
  - the bank claim's retry-gate predicate and the deterministic interleaving case on both engines.
- **Out of scope**:
  - any new public SDK type, port or contract: both changes use **existing** ports and existing test seams;
  - any money, ledger, rating, settlement, account, sponsorship or mode change;
  - any model, lock, inventory or settings change;
  - the pending first-turn decision, which is a **test-only** follow-up in the unapproved broader draft;
  - the approved `proxy-codex-memory` scope, which is unchanged.
- **Adjacent expectations**:
  - the open-source tree never imports the Enterprise tree;
  - the open-core split is in flight on a feature branch: the allowlist is carved there to the usage-evidence package, so the same addition must be transplanted when the split lands. That coordination is **not** a blocker for this slice, which is written against the **current base** file.
  - the local CI equivalents stand in for the remote race lanes while remote CI is blocked, and the two are kept distinct.
- **Boundary ownership**:
  - the role allowlist is owned by the core billing package (or its carved successor);
  - the bank claim SQL is owned by the proxy-memory store in the infrastructure zone;
  - the composed proof lives in the featurehost composition, which is the only place that already composes the real scheduler, the real executor and a backend.
- **Revalidation triggers**:
  - the auxiliary role allowlist and its successor package after the split;
  - the request-authority admission and the request-preparation order;
  - the bank claim SQL in the proxy-memory store.

## Requirements

### Requirement 1: Auxiliary role registration and phase execution
**Objective:** As an operator, I want the two background phases to run and to be attributable to closed, registered auxiliary roles, so that memory work exists and is visible instead of being rejected before the backend is opened.

#### Acceptance Criteria
1. The closed workload allowlist shall contain exactly the two roles `proxy_memory_phase1` and `proxy_memory_phase2`, in addition to the roles it already contains, and shall keep rejecting every other role — including a user-supplied, suffixed or case-shifted spelling of the two — before the backend is opened.
2. A composed run with the **real** background scheduler, the **real** runtime executor and a recording backend shall observe exactly one backend open per phase, a **distinct billing call identity per phase**, and one terminal call record and one terminal leg record per phase, each carrying the auxiliary class and that phase's exact role.
3. The composed proof shall use the production path — the process background scheduler bound to the real executor, the real feature worker and a real backend — and shall not use a mocked background client; it shall also observe the trusted detached lineage of each phase.
4. The change shall be confined to the allowlist file of the current base, plus the feature-side drift pin, and shall introduce no new public type, port or contract; the addition shall be transplanted to the carved package when the open-core split lands, without blocking this slice.

### Requirement 2: Bank claim retry gate
**Objective:** As an operator, I want the bank claim to honour the scheduled retry time under concurrency, so that a late failure from a previous generation cannot cause an early, duplicate consolidation.

#### Acceptance Criteria
1. The bank claim's conditional update shall re-check the retry gate, so a bank whose next attempt time was scheduled in the gap between the candidate read and the update is not claimed.
2. A deterministic interleaving shall prove the re-check on SQLite and on a fresh-schema PostgreSQL lane, with no sleeps and no polling: the claim is not taken, and the bank's lease owner, lease expiry, next attempt time and attempt counter are unchanged.
3. The fix shall add no scheduler redesign: the candidate query, the worker, the backoff and the attempt counter stay as they are, the lease-fencing predicate and the dialect-neutral statement stay untouched, and the pre-existing positional lost-race skip stays a follow-up.
