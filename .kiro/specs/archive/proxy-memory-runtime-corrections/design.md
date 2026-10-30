# Design Document: proxy-memory-runtime-corrections

> **Status: approved (narrow scope).** Section numbers (§N) are referenced from `tasks.md`. Both changes use existing ports and existing test seams; no new public type, port or contract is introduced.

## Overview

Two small, source-confirmed corrections to the Proxy Memory runtime:

1. the two auxiliary roles the feature already sends are missing from the closed workload allowlist, which aborts both background phases before the backend is opened and before any billing call identity is minted;
2. the bank claim's conditional update omits the retry gate that its own candidate query and the session claim both carry, so a late failure can cause an early claim.

The first correction is one production file plus a composed proof. The second is two lines plus one deterministic contract case.

### Goals
- Both phases execute on the real path, with distinct billing call identities and separate terminal call and leg records per phase (Requirement 1).
- Unknown roles keep failing closed with zero backend opens (Requirement 1.1).
- The bank claim honours the scheduled retry time under a deterministic interleaving on both engines (Requirement 2).

### Non-Goals
- Money, sponsorship modes, funding, charging policy, accounts: `.kiro/specs/proxy-memory-commercial/` (unapproved).
- The pending first-turn decision, its test-only closure pin and the model-policy surface: the unapproved broader drafts.
- Any new public SDK type, port or contract.
- The Agent Loop Guard verifier role, which the same allowlist rejects.
- The pre-existing positional lost-race skip in the bank claim.

## Boundary Commitments

### This Spec Owns
- The two role constants and their two entries in the closed allowlist of the **current base** file.
- The composed real-path proof of both phases, and the focused seam proof that an unknown role opens the backend zero times.
- The bank claim's retry-gate predicate and the deterministic interleaving case on both engines.

### Out of Boundary
- The approved `proxy-codex-memory` scope, which is unchanged.
- Every concern listed under Non-Goals.

### Allowed Dependencies
- The core billing package keeps its existing dependencies; the change is two constants and two list entries.
- The proxy-memory store keeps its existing dependencies; the change is one predicate and one argument.
- The featurehost composition test uses the real scheduler, the real executor and a recording backend, and must not import the feature package into core or core into the feature.
- The open-source tree never imports the Enterprise tree.

### Revalidation Triggers
- The allowlist file and its carved successor after the open-core split.
- The request-authority admission and the request-preparation order.
- The bank claim SQL in the proxy-memory store.

## Architecture

### Seams Reused
| Seam | Use | Requirement |
|---|---|---|
| The closed workload allowlist in the core billing package | the two closed lists that admit the two roles | 1.1 |
| The runtime's request-authority admission, and the workload projection it calls | the point where an unregistered role aborts the call before the backend is opened | 1.1 |
| The request-preparation order (identity preparation, then the billing call identity stamp) | why the rejection is fatal, and why the identity is minted only for an admitted operation | 1.2 |
| The process background scheduler, the generation-bound executor runner, the runtime executor and the backend open | the production path the composed proof drives | 1.2, 1.3 |
| The terminal usage sink and the independent leg append | where the per-phase call record and leg record are observed | 1.2 |
| The proxy-memory store's two-step claim, and the session claim's complete predicate revalidation | the precedent the bank claim must mirror | 2.1, 2.3 |
| The shared store contract suite, run on SQLite and on a fresh-schema PostgreSQL lane | the existing test port for the claim behaviour | 2.2 |
| The featurehost sentinel composition | the existing composition the proof reuses, with only the background client and the backend replaced | 1.3 |

### New Seams
None. Both corrections use existing ports and existing test seams; the composed proof reuses the featurehost composition rather than introducing a new harness framework.

### Project Boundary Questions (AIProxer)
- **Core-owned or plugin-owned?** The allowlist is core (a content-free correlation label); the store change is infrastructure; the proof is a test in the featurehost composition.
- **New canonical concept?** No.
- **Streaming-first preserved?** Yes; nothing about the event path changes.
- **Provider SDK leakage?** None. **No retry post-output?** Unchanged.

## Components and Interfaces

### §1 The role registration
- **File:** the core billing package's workload identity file, on the **current base**.
- **Change:** two constants for `proxy_memory_phase1` and `proxy_memory_phase2`, added to the two closed lists in that file — the switch that projects an auxiliary role and the switch that validates a workload identity. Nothing else in the file changes: the struct, the JSON shape and every other function stay untouched. A workload role is a content-free label, so no money logic is introduced.
- **Split coordination (not a blocker):** the open-core split is in flight on a feature branch and carves this allowlist to the usage-evidence package, keeping a forwarder alias behind. The same addition must be transplanted when the split lands. This slice is written against the current base file and does not wait for the split.
- **Drift pin:** the feature constants stay the feature-side source, and one test pins them against the billing constants, so the two lists cannot drift.

### §2 The composed execution proof
- **Home:** the featurehost composition test package, which already composes the process, the generation, the real feature worker, the background client, the runtime executor and a backend.
- **Fixture change:** the mocked background client is replaced by the **real** background scheduler bound to the **real** runtime executor, and the stub backend is replaced by a recording backend registered under the configured auxiliary backend identifiers. The recording backend declares the capabilities the phase calls actually need — the structured-output capability is a hard requirement and streaming is required — so the call is not downgraded on the way in.
- **Assertions:** exactly one backend open per phase, counted at the backend; the trusted detached lineage of each phase; a **distinct** billing call identity per phase; one terminal call record and one terminal leg record per phase, each with the auxiliary class and that phase's exact role; and, in the focused seam case, an allowlisted role opening the backend exactly once while an **unknown** role is rejected and opens the backend **zero** times.
- **Why the composition and not a unit test:** the unit level cannot host the full proof, because core may not import the feature and the feature may not import core. The featurehost composition is the only place that already owns all the pieces.

### §3 The bank claim retry gate
- **File:** the proxy-memory store's bank claim.
- **Change:** the conditional update gains the same retry-gate predicate its candidate query already carries, plus that predicate's argument. The lease-fencing predicate and the dialect-neutral statement stay untouched, and the candidate query, the worker, the backoff and the attempt counter are unchanged: there is no scheduler redesign (Requirement 2.1, 2.3).
- **Lost-race handling:** a bank that left the candidate window in the gap is skipped like a lost race, never leased with a stale candidacy.
- **Proof:** one contract case, added to the shared store contract so it runs on SQLite and on a fresh-schema PostgreSQL lane. The interleaving is deterministic and sleep-free: a query hook that runs **before** the statement acquires a connection parks the claim statement, so the competing write lands in the gap — which works on a single-connection SQLite and on PostgreSQL alike. The case asserts that the bank is not claimed and that its lease owner, lease expiry, next attempt time and attempt counter are unchanged.
- **Residual (follow-up):** the lost-race skip in the bank claim is positional, so a candidate that left the window can make the offset skip an eligible row in the same call; the row stays dirty and is claimed by a later tick. The session claim uses an exact skip list. Pre-existing, and not part of this slice.

## Error Handling
- An unregistered role keeps aborting the call **before** the backend is opened and before any billing call identity is minted; the empty-identity outcome belongs to the empty-role case, which is admitted.
- The bank claim treats a candidate that left the window as a lost race and continues, exactly as it does today for another instance winning the row.

## Security Considerations
- The registered roles are a **closed set of known labels**. Registering them makes the two phases work; it says nothing about who supplied a request, and no unspoofability is claimed.
- The composed proof writes no user content into logs, metrics or labels.

## Testing Strategy
- **Real path, everywhere:** the composed proof drives the real scheduler, the real executor and a real backend; only the provider's content is stubbed. The proof never uses a mocked background client.
- **Deterministic concurrency:** the bank-claim case uses a query hook, not sleeps or polling, and runs on both engines.
- **Proportionality:** one composed integration plus one focused seam case for the roles; one contract case for the claim.
- **Certification:** the local non-race package tests, the shared store contract on SQLite and PostgreSQL-direct, the spec gate and the local lint and vet equivalents. The race lanes remain remote-owned, and a blocked remote job is reported as blocked, never as green.

## Requirements Traceability

| Requirement | Component | Section |
|---|---|---|
| 1.1 | Role registration and the focused seam case | §1, §2 |
| 1.2 | Composed execution proof | §2 |
| 1.3 | Composed proof fixture and the real path | §2 |
| 1.4 | One-file scope and the split transplant | §1 |
| 2.1 | Bank claim predicate | §3 |
| 2.2 | Deterministic interleaving contract case | §3 |
| 2.3 | No scheduler redesign; residual follow-up | §3 |
