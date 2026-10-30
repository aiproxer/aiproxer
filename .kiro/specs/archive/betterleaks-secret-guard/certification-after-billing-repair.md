# BetterLeaks Secret Guard Certification After Billing Repair

> Historical certification: this result applies only to the revisions recorded below. The current disposition is in [validation-report.md](validation-report.md), with subsequent repairs in `review-remediation.md` (excluded from the open-source export). The earlier GO does not authorize merging PR #726.

## Verification Result

- STATUS: VERIFIED
- CLAIM_TYPE: FEATURE_GO
- REVIEW_VERDICT: APPROVED
- VALIDATION_DECISION: GO
- COMPLETION_VERIFICATION: VERIFIED for implementation-branch certification
- TASK: 9.1
- SOURCE_COMMIT: daa38a0ba9d87f10f78aea7a451dd2380ca524c7
- EVIDENCE_SCOPE: Current unit, quality, parity, QA, broad race, build, and CLI smoke; unchanged billing, support, runtime, and architecture race lanes from 22d7b25bc7a2d8d13d47b65458ad47a1c6a70179.

## Repairs and preserved contracts

Task 9.2 replaces process-wide stack observations contaminated by unrelated runtime activity with isolated per-depth measurements of fresh retained Rate goroutines. Both automatic GC triggers are controlled, collections during measurement are rejected, and child evidence is complete, bounded, and fail closed. The original 65,536-byte depth-spread limit, depths 200/800/3200/8192, fingerprints, allocation checks, and solver coverage remain intact. Independent ordinary small-frame recursion around the actual Rate call exceeded the unchanged limit. No production billing code changed.

Task 9.3 relocates four unchanged harness regression tests to the existing integration-tagged helper file. The certification population assertion and canonical sweep selector are unchanged.

Task 9.4 gives two spool worker tests private in-memory SQLite through existing Config.DB injection. Assertions, one-second/five-second deadlines, repeated Start calls, and claim configuration remain intact. Caller-owned database cleanup follows spool cleanup. File-backed durability/restart tests and production spool code are unchanged. The original full-suite timeout cause remains unproven; the current complete gate result establishes the resulting suite status.

Task 9.5 replaces unsynchronized test-poller job-ID recording with atomic storage and a zero-safe accessor. All three ID assertions and the concurrent attach/clear scenario remain intact. Production reasoning preservation is unchanged.

Each repair received independent approval. Current Linux focused/package race, lint, and vet evidence for task 9.5 is retained as review-9.5-*-linux.log and review-9.5-provenance.json under the 22d7b25b evidence directory.

## Current canonical gates

The native clone betterleaks-cert-22d7b25b was updated cleanly to the source commit above. Commands ran sequentially as UbuntuOld/ciuser, UID 1001, using Go 1.26.6, GCC 11.4.0, GolangCI-Lint 2.12.2, and buf 1.66.0. Scripts preserved failing exit codes and stopped on failure.

Evidence directory: betterleaks-cert-daa38a0b-evidence/.

| Command | Exit | Evidence |
| --- | ---: | --- |
| make test-unit | 0 | 01-unit.log |
| make quality-checks | 0 | 02-quality.log |
| make qa | 0 | 03-qa.log |
| bash scripts/race-check.sh --strict --lane broad | 0 | 04-race-broad.log |
| make parity-checks | 0 | 05-parity.log |
| go build -o <evidence>/lipstd ./cmd/lipstd | 0 | 06-build.log |
| <evidence>/lipstd --help | 0 | 07-smoke.log |

commit.txt, changed-paths.txt, exits.txt, and final-status.txt record the source, scope, exits, and clean clone. Quality uses the repository's preferred configured linter. Only opt-in local module-cache verification remains skipped. QA delegates standalone compilation/vet to its full tagged test pass; this is the canonical runner's documented behavior.

## Full race coverage and source identity

The complete make test-race invocation at 22d7b25b exited 1 because its broad lane found the subsequently repaired shared test-poller race. The same invocation completed the other four lanes successfully:

| Unchanged lane | Result | Duration |
| --- | --- | ---: |
| Billing, including maximum-depth integration certificate | PASS | 3410.166 s |
| Exhaustive support-agreement | PASS | 520.416 s |
| Durable runtime | PASS | 62.642 s |
| Architecture, including tools | PASS | 102.800 s for the principal package |

Their complete evidence is betterleaks-cert-22d7b25b-evidence/09-make-test-race.log. Between 22d7b25b and daa38a0b, the only Go changes are the two reasoning-preservation test files in the broad lane. No production, billing, runtime, architecture, module, or build configuration changed. The current canonical broad-lane exit 0 therefore completes applicable full race coverage without repeating unchanged expensive lanes. This report does not claim that a single current-SHA invocation of the all-lane command exited 0.

Final artifact checks also passed: make docs-check, go test ./tools/kiro/speccheck, and git diff --check. No unchecked or blocked implementation task remains. The source certificate is unchanged by the documentation-only completion commit.

## Prior failures and supplemental checks

At 80261328, make test-unit exited 1 with:

- TestSpoolStaleDeliveryIsReclaimedAndExactlyOneWorker: calls = 0, want one worker delivery.
- TestSpoolWakeDrainsCommittedBacklog: committed backlog was not drained by the append wake.
- TestQAFastPreflight_BillingSchemaCertification: four new TestDR helper tests changed the pinned certification-file population.

At 22d7b25b, unit, configured quality, parity, and QA passed, while the broad race lane reported concurrent writes at pollTestPoller.Poll, compression_attempt_poll_test.go:30. Both original traces and gate failures remain in their separate evidence directories; current passing results supersede them.

Standalone staticcheck@latest reported 722 repository-wide findings. That supplemental fallback does not apply the preferred GolangCI-Lint configuration and nolint directives. The unchanged repository-configured mandatory lint gate passed with GolangCI-Lint 2.12.2; no lint baseline, suppression, or gate was modified to obtain that result. Windows race remains unavailable; applicable race verification ran on Linux.

## Scope and delivery

The existing independent integration assessment maps all 73 criteria across 10 requirement sections with no implementation gap. Production implementation and its accepted bounded performance tradeoffs are unchanged by these repairs. See performance-assessment.md and benchmark-report.md for remaining upstream matcher CPU and JSON allocation costs; no latency SLO is asserted.

This is implementation-branch certification. GitHub CI, PR submission, merge, merged-main verification, and spec archiving are not established by these local gates.
