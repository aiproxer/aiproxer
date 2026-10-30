# Development gates and fixture performance

The first five improvements below were measured against `bb3aa523`. Later
sections record the owner-callback and fast staged-hook measurements. Runtime
code is unchanged.

## Changes

1. `make qa` delegates lint to its comprehensive `lint` prerequisite once.
   Preliminary quality checks retain formatting, generated output, dependency
   and repository-policy checks. Standalone quality checks still run lint.
2. Changed/staged local lint selects changed packages and transitive production,
   internal-test and external-test consumers within each affected module. Shared
   public contracts, testkit, core configuration, dependency files and lint-policy
   changes force complete module lint. Discovery errors fail the command.
   Unscoped lint and CI retain comprehensive coverage. A clean changed-mode
   invocation retains the root-module gate; documentation-only work needs no lint.
3. Store fixtures restore a SQLite database image created by real migrations,
   rather than replaying the full CREATE-statement set per fixture. Each fixture
   retains its private database, shared-memory pool and connection pragmas.
   Existing databases still take the real restart/upgrade path. An independent
   migration comparison verifies schema equivalence, and a two-connection test
   checks pool visibility and foreign-key enforcement.
4. Real host fixtures reuse immutable metering schema bytes in private
   files. Host construction, journal posting, close/reopen and ownership assertions
   remain intact. The two R5 relay fixtures use WAL/NORMAL, consistent with the
   existing fixture policy; they prove process reopen, not power-loss durability.
5. Package/test concurrency was measured separately. Native defaults remain the
   fastest tested setting. `AIP_TEST_PACKAGES` optionally controls package workers;
   `AIP_TEST_PARALLEL` continues to control parallel tests within each binary.

## Windows measurements

Go 1.26.6, Windows/amd64, Ryzen 7 5800X, 16 logical processors. Commands ran
sequentially with stable warm compiler/module caches. Fresh test execution used
`-count=1`; no cache was cleared. These single full-suite observations describe
this machine and do not predict GitHub runner performance.

The matched original uses a Go source overlay in the same worktree. New test
files are empty package stubs in that overlay, so the baseline retains the old
fixture implementations without including the new regression tests.

| Fresh root suite, `-p=16 -parallel=16` | Original | Candidate |
| --- | ---: | ---: |
| Command wall time, including compilation/startup | 84.1 s | 80.1 s |
| First-to-last test JSON event | 79.2 s | 75.2 s |
| Billingstore package | 49.4 s | 43.9 s |
| Runtimebundle package | 54.5 s | 50.7 s |

All 360 package results passed on both sides. The observed wall reduction is
4.8%. Package times overlap and include contention; unchanged packages also vary.
The sum of package durations increased from 447.4 to 466.1 seconds, so this pair
is not evidence of a proportional reduction in total CPU work.

| SQLite schema fixture benchmark, 30 iterations per repetition | Original | Candidate |
| --- | ---: | ---: |
| Warm repetition 2 | 7.003 ms | 0.559 ms |
| Warm repetition 3 | 6.941 ms | 0.543 ms |

Warm schema materialization is roughly 12.5 times faster. The first repetition
includes one-time template construction and is excluded from that comparison.
The focused R5C2b/R5C3a relay tests fell from 1.37/1.47 seconds to 0.08/0.14
seconds. The four-test host group remained around 4.2 seconds because its longer
R82 scenarios still set the critical path.

| Candidate root suite concurrency | Command wall time | Result |
| --- | ---: | --- |
| 16 package workers / 16 parallel tests | 72.4 s | Passed |
| 8 / 8 | 89.4 s | Passed |
| 4 / 8 | 107.1 s | Passed |
| 16 / 8 | 91.8 s | Passed |

These are separate sequential observations, not averaged benchmark results.
Lowering the default would have regressed this host. Leave the optional package
budget unset unless measurements on the target machine justify an override.

## Connector fixture executable size

The two staged connector-fixture build helpers pass `-ldflags=-w` when building
their real executable fixtures. Private copies and executable hash checks are
unchanged; production build flags are unchanged. On Go 1.26.6/Linux/amd64 with
`GOMAXPROCS=2`, two matched pairs in ABBA order of the ordinary runtimebundle
suite used `-tags=precommit -ldflags=-w` test binaries and warmed the six fixed
fixture build actions. Each run used `-test.parallel=2`, excluded
only `TestRuntimebundle_NoCompleteOwnerCallbackEscapes`, and passed all 983
reported top-level tests. The fixture checks and unprofiled frozen-generation
leak check remained enabled and passed.
The suite also exercises the shared helper through dogfood local-stub staging.

| Mean of two fresh suite runs | Baseline | `-w` fixture builds | Change |
| --- | ---: | ---: | ---: |
| Wall time | 42.727 s | 38.896 s | −9.0% |
| Process CPU | 53.514 s | 47.701 s | −10.9% |

Peak RSS was higher and variable for the candidate, so this is not a memory
improvement claim. A representative Codex fixture shrank from 34,587,590 to
29,279,324 bytes (−15.35%). Build-time observations were unstable and are not
claimed as a result.

## Local lint scope

The selector regression fixtures use real Git repositories and Go import graphs.
They cover transitive/test-only consumers, independent modules, untracked work,
shared-contract fallback, documentation-only work, staged isolation, and discovery
failure. An alternate Git index measures a leaf `tools/devcheck` edit without
changing the actual staged work. Both shell adapters must select that package.

The QA orchestration check observes two lint invocations on the original Makefile
and one comprehensive invocation plus preliminary policy checks on the candidate.
The comprehensive lint command and analyzer set are retained.

| Native PowerShell lint command | Wall time |
| --- | ---: |
| Whole root, first call after fixture/tool edits | 109.7 s |
| Identical whole-root warm repeat | 5.7 s |
| Selected leaf, actual comment edit | 4.3 s |
| Whole root with the same leaf edit, after scoped lint | 5.7 s |
| Bash selected leaf adapter, warm | 4.0 s |

The 109.7-second first call and 4.3-second leaf call are different invalidation
scopes and must not be presented as a 25-fold speedup. With the current warm
analyzer cache, the measured leaf improvement is modest. The selector avoids
unrelated package analysis when those packages lack cached results; its benefit
depends on dependency fan-out and cache state. Broad test-consumer relationships
can legitimately expand an internal-package edit to most of the module.

## Linux archtest measurement

`MeasureWaveMirrors` now reuses the sorted all-wave findings when
`ActiveMigrationWave == Wave5c_Residual`; if those thresholds diverge, it keeps
the original active-wave scan. The before binary was built at `e1e18da4`, and
the candidate at `7e8c35f7` with this change. `internal/archtest`, `go.mod` and
`go.sum` were unchanged between those bases. Both matching precompiled binaries
used `-ldflags=-w` and ran from the same pinned worktree root, which `repoRoot`
uses to find the checkout. Environment: Linux/amd64, Go 1.26.6,
`GOMAXPROCS=2`.

Three interleaved runs used `-test.v`,
`-test.run='^TestExtensionPlanesBaselineGeneration_Determinism$'`,
`-test.count=1`, `-test.parallel=2` and `-test.timeout=8m`:

| Median per run | Before | After |
| --- | ---: | ---: |
| Wall time | 3.025 s | 1.616 s (-46.6%) |
| CPU time | 3.184 s | 1.704 s (-46.5%) |
| Peak RSS | about 94–96 MiB | about 94–96 MiB |

The mirror-measurement, report-formatting, baseline-determinism and baseline-
artifact tests passed. This focused test result does not establish a full-suite
speedup.

## Verification

- Fresh Windows default root suites passed for all four concurrency settings and
  both matched source variants.
- `make qa` passed with `AIP_VERIFY_MODULE_CACHE=1`: generated output, formatting,
  module verification, tagged root tests/architecture, all 42 mandatory lint
  modules, vulnerability scanning and static release/compliance checks. The log
  contains exactly one comprehensive lint invocation.
- Focused QA/selector tests passed, both lint adapters executed the selected
  package, and invalid package budgets failed on Windows and Bash.
- Linux race checks passed for the schema clone/capture, private host fixture,
  R5 restart and selector regression tests in the three affected packages.
- `aipstd` build and `--help` smoke passed.

Configured external PostgreSQL services and the complete native multi-OS release
matrix were not run locally. These results do not certify remote cache behavior
or the paused historical Windows cost comparison.

## Owner-callback architecture gate

The precommit owner-callback check builds an import-reachability graph for the root
module and then performs typed loads for Linux/amd64 and Windows/amd64, both
with CGO disabled. The graph pass needs
package metadata and imports, so it no longer requests `NeedCompiledGoFiles`,
which makes `go list` enter the build-action pipeline. Typed loads retain
`NeedCompiledGoFiles` for both OS configurations; the sentinels and inventory
checks are unchanged. This removes graph-load build-action work without removing
a validation check.

Preliminary measurements used Go 1.26.6 on a 2-CPU `agent-dev`, with precompiled
precommit binaries built with matching flags (`-ldflags=-w`), a fresh scanner per run, and
retained caches. The first pair is priming-sensitive; the two warm pairs show
broad variation, so these observations do not establish a stable overall gate
speedup.

| Matched precommit binary run | Before | After |
| --- | ---: | ---: |
| 1, priming-sensitive | 61.007 s | 8.056 s |
| 2 | 8.069 s | 4.791 s |
| 3 | 11.328 s | 13.617 s |

The original cold first gate took 583.833 s; that run is separate and is not a
comparable before measurement. Separate gate phase timings show the graph phase
lower after the metadata-only load change: 2.83–4.11 s with
`NeedCompiledGoFiles`, versus 0.84–2.27 s without it. In two metadata-only
all-module traces, `go list` with `NeedCompiledGoFiles` and exports disabled took
45.7/47.8 s; without that flag it took 1.33/1.40 s.

The focused test can be rerun with:

```sh
go test -count=1 -tags=precommit -ldflags=-w -run '^TestRuntimebundle_NoCompleteOwnerCallbackEscapes$' ./internal/infra/runtimebundle
```

The gate wall measurements ran the already compiled test binaries separately
from compilation. Logs and binaries were kept outside the worktree under
`/home/mateusz/.cache/qa-package-speed`. This result is local evidence only; it
does not predict CI timings. The measured two-OS required-export union is 2263.8 MiB. A private 3072 MiB
bounded snapshot dropped 542 required artifacts (274.3 MiB), so this slice adds
no cache lane or budget change. The existing 4096 MiB owner lane remains intact.


## Fast staged runtimebundle commits

A staged runtimebundle file previously triggered the complete owner-callback
scan as part of its package tests. That scan discovers importers across the root
module and type-checks Linux and Windows. The fast hook now follows the existing
CI split: it excludes only `TestRuntimebundle_NoCompleteOwnerCallbackEscapes`,
for the root runtimebundle package. The positive/negative aggregate, candidate
assembly and Windows-overlay fixture tests still run; these use small stub
packages rather than the production module graph. Full pre-commit mode and
ordinary scoped test commands retain the complete gate.

The dedicated CI owner job uses `-count=1`: child `go list` discovery can find a
new importer without changing the linked runtimebundle test binary or the
parent's previously recorded test-cache inputs. Compiler/export caches remain
enabled. The graph pass also uses the metadata-only mode described above.

Two matched complete `scripts/hooks/pre-commit` pairs used the same harmless
staged runtimebundle comment, identical working sources, retained caches and
fresh test execution through `GOFLAGS=-count=1`. Both sides include build, vet,
package tests and lint. A separate 233.627 s baseline warm-up is excluded.

| Complete hook observation | Before | After |
| --- | ---: | ---: |
| Pair 1 | 156.651 s | 321.912 s |
| Pair 2 | 671.989 s | 234.700 s |

All four hooks passed. The shared 2-CPU host had concurrent Go jobs, slot waits
and indexing; these wall observations do **not** establish a stable whole-hook
speedup. The removed owner test itself took 14.32 s and 45.42 s in the before
runs and did not execute afterward. Its separate cold 583.833 s observation
above explains the scope risk, but is not a matched cold-hook comparison.

Fresh verification passed for devcheck (including persisted/quoted GOFLAGS,
quarantine merging, subtest skips and exact-name matching), both shell scope
regressions, and the retained runtimebundle tests including the unprofiled leak
check. A temporary unrelated package declaring a Windows-only callback over
`*runtimebundle.Host` was rejected by the complete gate; removing it restored a
fresh passing gate. This verifies that metadata-only discovery retains
Windows importer coverage. Benchmark and mutant logs are retained outside the
worktree under `/home/mateusz/.cache/qa-package-speed`.
