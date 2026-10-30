# Final release-gate results

**Certified implementation SHA:** `a5a2d375c767b3dad8225de0879f5a6c6f4b1ee5`
**Reviewed baseline:** `efe4624909cea318c7211d5cb3734059d3210802`
**Evidence update:** documentation/artifacts only; production and benchmark source are byte-identical to the certified implementation.

## Exact-implementation gates

| Gate | Result | Evidence |
| --- | --- | --- |
| `make test` | PASS | exact local head `a5a2d375`; includes focused architecture, cleanup, and integration regressions |
| `make lint` | PASS | exact local head; `0 issues.` |
| `make arch-report` | PASS | exact head; convergence total 18832 vs baseline 19642, delta **-810** |
| Remote QA | PASS | exact implementation head; PostgreSQL integration, unit tests, golangci-lint, govulncheck |
| CodeQL actions | PASS | exact implementation head |
| CodeQL Go | PASS | exact implementation head |
| CodeQL JavaScript/TypeScript | PASS | exact implementation head |
| Complete performance matrix | PASS | baseline `efe46249` vs final `a5a2d375`, 10 isolated ABBA samples each |

GitHub status checks are mutable remote state and are intentionally read from PR #205 rather than frozen here. Any evidence-only successor commit must rerun the repository-required PR checks; because it changes no Go/config/build source, the implementation and benchmark certification remains scoped to the byte-identical `a5a2d375` source tree.

## Performance gate

Required benchmark surfaces:

- candidate compilation;
- BuildHost;
- successful reload;
- no-op reload;
- Manager Acquire/Release;
- Manager publication;
- generation dispatch.

All seven have exactly 10 baseline and 10 final samples. Candidate compilation improves by **19.68% elapsed time**, **15.44% bytes**, and **12.56% allocations**, satisfying the strict 10% requirement. Acquire/Release and dispatch are statistically unchanged; BuildHost and both reload paths improve.

Manager publication regresses by 1.804 microseconds and five allocations per successful configuration publication. The maintainer explicitly approved this reload-only cost because it launches required asynchronous manager-owned retirement and does not affect request dispatch.

Raw results, the exact baseline overlay, protocol, checksums, and approval are committed beside this file in `bench-final-notes.md`, `bench-final-runA.txt`, `bench-final-runB.txt`, `benchstat-final.txt`, and `benchmark-baseline-overlay.patch`.

## Notes

- No provider credentials or live provider calls were used.
- Benchmark capture ran on two pinned CPUs with no concurrent build/test workload.
- The separate AI security-review workflow failure was an unsupported-model infrastructure error and produced no analysis or finding; it is not a QA or CodeQL failure.
- This log supersedes the earlier PR D4 table tied to `e8019859`, including its obsolete full-lint failure and pre-fix package-only follow-up.
