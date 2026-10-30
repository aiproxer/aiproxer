# Runtime corrections: branch-local validation

## Authority and limits

The owner approved both narrow implementation tasks, their separate test-proportionality exceptions, and archival of **only this spec** after integration validation so the bank-fix commit can satisfy the repository lifecycle rule. This is a branch-local archive, not the ordinary post-merge closeout. No merge, PR closure, broader-spec archival or resource cleanup is authorized.

**No merged-main baseline exists for these corrections.** Remote CI/race evidence, foundation reconciliation, customer charging and commercial readiness are not certified. The broader commercial and model-policy drafts remain unapproved and outside this worktree's slice.

## Implementation and review

- Predecessor: `a27ae0babea556ce4e492c0f0f802ec794e03836` (#1299), not an independently verified main baseline.
- Role registration and real-executor regressions: `4e2d588fec42a80fd711d3620b9d8d41789def6b`; independently approved after the 494-line test exception and final simplification.
- Bank retry predicate and deterministic shared regression: included in the commit carrying this archive; independently approved after the separate 141-line test exception. The reviewed production/test tree had fingerprint `38f70aea040564023b2fd6e6ee37f0eca499654c00e0f303bfb05642f141db8c` before task-state documentation changed.
- Both task reviews reproduced behavioral pre-change failures and fixed-source success. Bank proof executed on SQLite and fresh-schema PostgreSQL, including five repetitions per engine. Role proof used the real worker, background scheduler, runtime executor and recording backend, with distinct call identities and exact terminal call/leg records. Recording billing seams do not prove customer debits.

## Cross-task integration evidence

The final pre-archive integration tree at HEAD `4e2d588fe` had fingerprint `064fac95dda7d2d6fca1d36c794d7f7eed8c5e4f94814e8bb0c4663cc7ac5e47`. Source-bound logs and reviews are retained outside the repository in the controller's handoff inventory.

| Check | Result |
|---|---|
| Fresh eight-package tests: billing, metering, auxiliary runtime, runtime, memory store/feature, featurehost and runtimebundle | Passed |
| Build `cmd/lipstd` into external scratch; execute the built binary with `--help` | Passed |
| Fresh-schema PostgreSQL and SQLite memory contracts | Passed |
| Role and bank pre-change production overlays | Expected behavioral failure; fixed counterparts passed |
| Eight-package vet and formatting | Passed |
| Seven acceptance criteria and design boundary mapping | Covered; no new public contracts or money logic |
| Generic spec lifecycle check before moving the all-checked active spec | Expected failure requiring this authorized archive, not an implementation failure |

The composed phase-2 path uses the actual store's patched `ClaimBanks` before auxiliary inference and publication, proving the two fixes integrate rather than only passing isolated unit tests. The bank regression also proves claimability at the scheduled deadline.

Post-archive `go test -count=1 ./...`, `make docs-check`, generic `kirocheck`, and the spec-tool/lifecycle tests passed on the archive's pre-commit tree (fingerprint `f471f61a1804b26fa3d530dafa6ca8d744b1c1e8079fcf132cfbb9a1eedd09fe`). This paragraph records those executions; final committed-head evidence remains in the controller's handoff inventory. An unsupported legacy `make kiro-spec-check` registry target is not reported as passing; generic `kirocheck` is the applicable checker for this spec.

## Deferred obligations

- Transplant the two role constants and list memberships into the carved usage-evidence package when the open-core billing split lands.
- Keep the pre-existing positional bank-candidate skip as a separate follow-up; no scheduler redesign is included.
- Reconcile the foundation/spec revert and the still-open predecessor stack before main delivery.
- Preserve the separate financial-certification and model-policy approval gates; these corrections do not satisfy them.
