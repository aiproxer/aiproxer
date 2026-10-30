# Implementation Plan

- [x] 1. Repair and certify source inode ownership
- [x] 1.1 Complete the source ownership integration and essential regressions — 6–9 hours; one indivisible T1 integration unit.
  - Implement the source owner/borrow lifecycle, positive ext4-driver detection, stable candidate and reopened-target validation, and provenance-aware atomic classification while preserving unsupported-Linux startup plus existing Windows and non-Linux adapters.
  - Carry takeable owner slots through bootstrap rollback, one-shot effective loading and checks, coordinator construction, candidate outcomes, effective no-op, committed publication, Apply, and Host.Close; isolate post-swap Manager and PhasePublish failures and include finalization in the shutdown idle predicate.
  - Add the essential AC-01–11 regressions, wire the mandatory tagged certification into Linux quality gates and the Ubuntu test variant, and add focused native identity/reload tests to the existing Windows variant; preserve portable checks, the existing hook, and the 100 modified-Go-file limit.
  - T1 is complete only when the source owner and every consumer are integrated, mandatory hooks pass, supported-filesystem certification passes, and relevant consumers pass. The 1–3 hour checkpoints for source primitives, all consumers, and shutdown/gates are internal checkpoints, not standalone normal commits.
  - _Boundary: Explicit integration across internal/infra/configsource, internal/infra/runtimehost, internal/infra/runtimebundle, the test-only ownership fixtures in internal/core/config/reload_strict_effective_contract_test.go and internal/stdhttp/admin/configreload/self_defense_management_isolation_test.go plus cmd/lipstd/reload_signal_adapter_unix_test.go and internal/stdhttp/config_reload_soak_test.go; measured secondary LOC-budget accounting in internal/archtest, scripts/configsource-certify.sh, scripts/quality-gate.sh, and the Ubuntu and Windows test variants in .github/workflows/ci.yml_
  - _Validation: bash scripts/configsource-certify.sh; go test ./internal/infra/configsource/... ./internal/infra/runtimehost/... ./internal/infra/runtimebundle/..._
  - _Requirements: 1.1, 1.2, 2.1, 2.2, 2.3, 3.1, 3.2, 3.3, 4.1, 4.2, 4.3, 5.1, 5.2, 5.3, 6.1, 6.2, 6.3, 7.1, 7.2, 7.3, 7.4, 8.1, 8.2, 8.3, 8.4, 9.1, 9.2, 9.3, 10.1, 10.2, 11.1, 11.2, 11.3_

- [x] 1.2 Add adversarial race and cleanup coverage — 2–3 hours.
  - Use deterministic channel barriers to test initially-idle and active shutdown finalization, concurrent WaitForIdle, deadlines, callback panic, and final borrower release.
  - Inject ownership-transfer, close, StartPublished, PhasePublish, and post-adoption coordinator failures; assert one close, cached error, Published truth, a live matching source baseline, no start retry, cleared publishing state, and later quiesce/close without deadlock.
  - Cover only acceptance behavior already satisfied by T1 and keep every essential test non-optional; use no timing sleeps or mirror-only tests.
  - Observable completion: the focused race suite passes and demonstrates cleanup at each ownership boundary without changing the committed baseline.
  - _Boundary: Adversarial tests across internal/infra/configsource, internal/infra/runtimehost, and internal/infra/runtimebundle; minimal mandatory tagged-fault discovery/execution wiring in scripts/configsource-fault-check.sh, scripts/quality-gate.sh, and the existing Ubuntu ext4 CI step_
  - _Depends: 1.1_
  - _Validation: go test -race -count=1 ./internal/infra/configsource/... ./internal/infra/runtimehost/... ./internal/infra/runtimebundle/..._
  - _Requirements: 5.2, 5.3, 7.1, 7.3, 7.4, 8.1, 8.2, 8.3, 8.4, 9.1, 9.2, 9.3, 10.2_

- [x] 1.3 Complete platform and delivery certification — 1–3 hours.
  - Require a passing native Windows identity/reload CI result for the source tree supplied by 1.1/1.2; run Windows/macOS package builds, portable tests, and the final tagged Linux certification and race suite. A draft prerequisite PR carrying the reviewed earlier commits provides the native executor before this task completes; cross-compilation alone does not complete it.
  - Confirm the local Linux gate requires explicit writable supported TMPDIR without host mount provisioning, and the Ubuntu test job alone creates and removes its RUNNER_TEMP ext4 loop fixture.
  - Document the ext4-driver/proc-visibility boundary and operator TMPDIR precondition; review bounded diagnostics and confirm normal precommit and source-change gates remain active.
  - Change only documentation and verification records in this task. Any concrete code or workflow defect returns to the integration owner for a reviewed normal repair commit and fresh native evidence, so there is no uncommitted native-code change hidden behind a passing earlier CI revision.
  - Observable completion: the platform checks pass, the mandatory Linux test cannot skip or silently miss either certification case, and documentation matches the tested support boundary.
  - _Boundary: Platform adapters, scripts/configsource-certify.sh, scripts/quality-gate.sh, .github/workflows/ci.yml, and operator-facing support documentation_
  - _Depends: 1.1, 1.2_
  - _Validation: bash scripts/configsource-certify.sh; go test -race -count=1 -tags=configsource_cert ./internal/infra/configsource/...; GOOS=windows GOARCH=amd64 go build ./internal/infra/configsource/... ./internal/infra/runtimehost/... ./internal/infra/runtimebundle/...; GOOS=darwin GOARCH=amd64 go build ./internal/infra/configsource/... ./internal/infra/runtimehost/... ./internal/infra/runtimebundle/..._
  - _Requirements: 1.1, 2.1, 2.2, 3.1, 3.2, 3.3, 11.1, 11.2, 11.3_
