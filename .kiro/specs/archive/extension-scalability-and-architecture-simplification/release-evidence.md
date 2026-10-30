# Phase 7 Release Evidence

> **Snapshot status:** Final certification is green for the current worktree. Phases 1–7 and Task 7.4 are complete, with the Windows race limitation recorded explicitly and no standalone full-protocol compliance run claimed.

This is implementation evidence for the exact working-tree head, not a changelog. It records commands and environmental limitations honestly; an unrun or failing gate is not represented as passed.

## Head and Scope

- Baseline SHA: `95089eb4b74d5cf8d062f238a1121124ce0da878` (checked-in Cartesian-only inventory baseline).
- Implementation head: `git rev-parse HEAD` is `e87c7e5e43f5e73bbc27eb56307ed79b0bd027cf`, with the complete implementation represented by `git diff 95089eb4b74d5cf8d062f238a1121124ce0da878` plus the worktree diff; the final commit follows and no commit was created by this phase.
- Accepted unrelated worktree changes preserved exactly and not staged: `scripts/check-adhoc-goroutines.ps1`, `scripts/check-adhoc-goroutines.sh`.
- Change-surface report commands: `go run ./internal/archtest/tools/changesurface/cmd -json` for the worktree and `go run ./internal/archtest/tools/changesurface/cmd -base 95089eb4b74d5cf8d062f238a1121124ce0da878 -json` for the full implementation diff.
- The worktree-only report is intentionally not branch blast-radius evidence. The baseline report currently reports `backendplugin-abi=25`, `canonical-contract=8`, `core-routing-runtime=26`, `shared-composition=29`, `provider-profile-data=5`, `extension-owned-production=113`, `generated=1`, `tests-reference=364`, `docs-spec=85`, and `other=51`; it includes canonical/core/ABI/shared changes from Phases 1-6. Profile-only fixtures still require zero shared boundaries.

## Deterministic Gates

The following completed successfully at the evidence capture head:

| Gate | Command / subjects | Result |
|---|---|---|
| Architecture and change-surface | `go test -count=1 ./internal/archtest/...`; change-surface package tests; synthetic Contoso/Fabrikam and profile-only fixtures | PASS |
| Architecture RED ratchet | `go test -count=1 -tags=architecture_red -run 'TestRED_' ./internal/archtest` | PASS; no current debt detected |
| Profile scale | `go test -count=1 ./internal/providerprofiles/... ./internal/testkit/scale/...`; 1,000 profiles, profile `provider-1000`, four families, pure compiler/no-activation surface | PASS |
| Core/frontend/backend/connector TCKs | `internal/testkit/contract/...`, `pkg/lipsdk/backendplugin/contracttest`, backend-plugin host/conformance packages | PASS |
| Protocol and adapter suites | `go test -count=1 ./internal/testkit/conformance/... ./internal/plugins/frontends/... ./internal/plugins/backends/... ./internal/standardplugins/... ./internal/pluginreg/... ./internal/stdhttp/contract/...` | PASS |
| Documentation | `make docs-check`; docs contract tests in `internal/archtest` | PASS |
| Quality/build | `make quality-checks` | PASS; formatting, build, vet, goroutine allowlist, hot-path check, architecture tests |
| Parity | `make parity-checks` | PASS; TCKs, retained protocol parity, connector module checks, bounded evidence |
| Shrinkage and affected surface | checked-in deletion gate and change-surface report | PASS; 82.3% legacy Cartesian-only deletion and affected-surface delta `-6178` lines |

The exact phase-specific deterministic gates above are the closure basis for Tasks 7.1-7.4. The report is machine-readable and human-readable, path-normalized for Windows, handles rename records, and distinguishes generated/test/reference breadth from shared-boundary coupling. The pure profile scale evidence does not claim an observational process/network counter; that boundary is covered by the no-activation composition tests.

## Full-Repository Evidence

The final repository gates were rerun and passed with these exact commands:

1. `make quality-checks` — PASS.
2. `make parity-checks` — PASS.
3. `make test` — PASS.
4. `make lint` — PASS.
5. `make qa` — PASS.
6. `make docs-check` — PASS.
7. `git diff --check` — PASS.
8. `make test-fuzz` — PASS in 92.8 seconds.
9. `make test-race` — unsupported on Windows; the target reported the actual platform limitation rather than producing race evidence.

The OpenResponses compliance result is the static gate run by `make qa`: it passed after the required documentation reference was added. The standalone full `make test-openresponses-compliance` protocol run was not run and is not claimed here. `make parity-checks` remains the recorded protocol-owned parity scope.

An initial gate attempt encountered a transient Go cache/disk environmental condition. After the cache/disk condition was cleared, the final reruns above passed; the transient attempt is not treated as a code or release-gate failure.

## Measurements and Residuals

- Legacy Cartesian deletion: `82.3%`, measured by the checked-in deletion gate against `internal/testkit/conformance/testdata/baseline_cartesian_inventory.json`.
- Affected-surface line delta: `-6178` lines in the reviewed affected surface; the change-surface report remains the source of truth for classification.
- Scale and certification evidence: the synthetic `1,000`-profile proof, bounded real-stack sentinel, frontend/core/backend/connector TCK evidence, and protocol-owned parity evidence are present and green.
- Wall-clock evidence: `make test-fuzz` completed in `92.8` seconds. No aggregate wall-clock duration is fabricated.
- Environmental limitation: `make test-race` is unsupported on Windows in this worktree; no race pass is claimed, and Linux CI remains the supported race environment.
- No commit, staging, worktree creation, or push was performed.
