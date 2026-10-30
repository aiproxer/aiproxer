# Implementation Plan: proxy-memory-runtime-corrections

> **Executor notes (read first):**
> - **Approved narrow scope.** The owner approved exactly this: the two auxiliary roles with their real-executor proof, and the bank-claim retry gate with its deterministic interleaving. The broader contracts (sponsorship, funding, charging, the pending-decision closure, the model policy and the settings) are **unapproved** and are out of scope here.
> - TDD: write the listed tests first and run them to see them fail (RED), then implement (GREEN), then refactor. Keep RED evidence per `docs/agent-handoffs.md`.
> - Two production files at most, and no new public type, port or contract.
> - Never log or label metrics with project paths, principal IDs, A-leg IDs, prompts or memory text.
> - No local `-race`: race evidence is remote-owned. Run the focused non-race checks with the task-local development-host policy file.
> - **No implementation is piled into the still-open #1299 stack.** This slice ships on its own.
> - Work from the current base (the allowlist file lives in the core billing package on this base). The open-core split is in flight on a feature branch and carves that allowlist to the usage-evidence package: the same addition must be transplanted when the split lands. That is coordination, **not** a blocker for this slice, and it is not part of it.

- [x] 1. Auxiliary role registration and phase execution

- [x] 1.1 Register the two roles and prove both phases execute on the real path
  - In the core billing package's workload identity file, add the two role constants (`proxy_memory_phase1`, `proxy_memory_phase2`) and add both to the two closed lists in that file: the switch that projects an auxiliary role and the switch that validates a workload identity (design §1). Change nothing else in that file: the struct, the JSON shape and every other function stay untouched.
  - Keep the feature constants `phase1Role` and `phase2Role` as the feature-side source and pin them against the billing constants with one test, so the two lists cannot drift.
  - Add the composed proof in the featurehost composition test package (design §2): replace **only** the mocked background client with the real background scheduler bound to the real runtime executor, and the stub backend with a recording backend registered under the configured auxiliary backend identifiers. The recording backend declares the capabilities the phase calls need — the structured-output capability is a hard requirement, streaming is required.
  - Provide the executor's exported billing seams (the identity bundle, the credit gate, the exposure admission and the terminal usage sink) so the terminal records are observable.
  - RED first, asserting:
    - exactly **one** backend open per phase, counted at the backend, with the trusted role read from the encoded lineage;
    - a private child A-leg distinct from the parent, with an internal service scope;
    - a **distinct** billing call identity per phase;
    - one terminal call record and one terminal leg record per phase, each with the auxiliary class and that phase's exact role;
    - the control case: an allowlisted role opens the backend exactly once, and an **unknown** role is rejected and opens the backend **zero** times;
    - the drift pin: the feature role constants equal the billing constants.
  - **Done when** the composed test passes with the real scheduler and executor, the focused seam case is mirrored in the core runtime package (without importing the feature package), the diff touches one production file plus tests, and the new tests fail on the pre-change source and pass after it.
  - _Requirements: 1.1, 1.2, 1.3, 1.4_
  - _Boundary: core (the allowlist file) and tests (featurehost composition, core runtime seam)_
  - _Depends: none_
  - _Validation: `go test ./internal/core/billing/ ./internal/core/runtime/ ./internal/standardplugins/featurehost/ -count=1`_

- [x] 2. Bank claim retry gate

- [x] 2.1 Re-check the retry gate inside the bank claim
  - In the proxy-memory store's bank claim, add the retry-gate predicate and its argument to the conditional update, mirroring the session claim (design §3). The lease-fencing predicate and the dialect-neutral statement stay untouched, and the candidate query, the worker, the backoff and the attempt counter are unchanged: no scheduler redesign.
  - Keep the lost-race handling: a bank that left the candidate window in the gap is skipped like a lost race, never leased with a stale candidacy.
  - RED first, in the shared store contract: a deterministic interleaving that schedules the bank's backoff in the gap between the candidate read and the update, then asserts the bank is not claimed and that its lease owner, lease expiry, next attempt time and attempt counter are unchanged. Park the claim statement with a query hook that runs **before** the statement takes a connection, so the competing write lands in the gap deterministically on a single-connection SQLite as well as on a fresh-schema PostgreSQL lane — no sleeps, no polling.
  - **Done when** the interleaving case is green on both engines, the other claim cases are unchanged, and the case fails on the pre-change source.
  - _Requirements: 2.1, 2.2, 2.3_
  - _Boundary: infrastructure (proxy-memory store) and its shared contract test_
  - _Depends: none_
  - _Validation: `go test ./internal/infra/proxymemorystore/ -count=1`_

## Implementation Notes

- This plan, with `requirements.md`, `design.md` and `research.md` of the same spec, is **approved** for this narrow scope; `spec.json` records the approval, the quoted user context and the date.
- The controller checks task boxes only after independent approval and fresh verification.
- Task 1.1 has an owner-approved proportionality exception for 494 added test lines versus nine added production lines; it does not authorize larger tests or any broader billing/model-policy scope.
- Task 2.1 has a separate owner-approved exception for 141 added test lines versus ten added production lines, including explanatory comments; the complete narrow slice adds 635 test lines. Neither exception authorizes future growth or broader contracts.
- The approved `proxy-codex-memory` scope is untouched, and the broader drafts remain unapproved; nothing in this slice depends on them.
- No task in this plan edits `billing-financial-safety-contracts`, any Enterprise package, or the open #1299 stack.

## Completion Status

Both implementation tasks passed independent review and source-bound integration validation. The owner authorized this branch-local archive to satisfy the completed-spec lifecycle gate before the bank-fix commit. Evidence and limitations are recorded in `validation.md`; no merged-main, remote-CI or commercial-certification claim is made.
