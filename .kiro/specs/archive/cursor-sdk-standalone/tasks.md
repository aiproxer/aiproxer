# Implementation Plan

## Task Format Note
- `(P)` means parallel-safe with peers under the same parent.
- `_Boundary_` names the design component/ownership seam.
- `_Depends_` declares non-obvious cross-group dependencies.
- `_Validation_` names the focused proof command for the task.

- [x] 1. Extraction baseline and publication prerequisites
- [x] 1.1 Generate exact migration inventory and record baseline SHA
  - Enumerate every active Cursor reference from the implementation baseline, with source hash, destination mapping, category, and planned deletion batch.
  - Record extraction baseline commit, SDK 1.0.23 and Undici 6.28.1 pins, and confirm destination repository name, provisioning, and publication permissions before external writes.
  - Write `.kiro/specs/cursor-sdk-standalone/migration-inventory.json` as migration evidence only.
  - Observable completion is a committed inventory that distinguishes dependency coupling, policy fixtures, and historical documentation.
  - _Requirements: 6.1, 6.6_
  - _Boundary: Public module baseline_
  - _Validation: git diff --check and inventory schema validation_

- [x] 1.2 Establish independently resolvable root and ACP module baselines
  - Publish/select a real downloadable root semver tag exposing existing public contracts without widening the API.
  - Publish `connector-support/acp` as nested-module tag `connector-support/acp/vX.Y.Z` pinned to that root tag, with sibling replacements removed in the tag content.
  - Verify from a temporary checkout with no adjacent host source that module resolution, download, verify, and build succeed.
  - Observable completion is recorded exact tag versions usable by the standalone plugin with `GOWORK=off`.
  - _Requirements: 2.2, 6.1_
  - _Boundary: Public module baseline_
  - _Validation: GOWORK=off go mod download, go mod verify, and go build ./..._

- [x] 1.3 Provision standalone plugin repository skeleton without moving behavior
  - Create the external repository layout, module path, license/provenance files, and independent Go plus Node verification workflows.
  - Pin only released dependencies; include no Cursor implementation source yet.
  - Verify clean-checkout module resolution without sibling checkouts or unpublished host packages.
  - Observable completion is an empty but buildable plugin repository that resolves only published contracts.
  - _Requirements: 2.1, 2.5_
  - _Boundary: Standalone connector_
  - _Validation: GOWORK=off go test ./... in an isolated checkout_

- [x] 2. Standalone connector with released dependencies
- [x] 2.1 (P) Relocate Go connector onto published contracts
  - Move command, provider adapter, lifecycle, diagnostics, protocol, fixtures, and Go tests while preserving structure beneath the old `connectors/cursorsdk` path.
  - Rewrite only module-relative imports and monorepo assumptions; retain `Service.Describe/Configure`, `ConfiguredInstance` behavior, opaque YAML config, authenticated secrets handling, and existing error mapping.
  - Forbid root `internal/` imports and copied generated ABI files.
  - Observable completion is a standalone module that builds and passes its relocated Go tests against released root and ACP versions.
  - _Requirements: 2.1, 2.2, 4.1, 5.1, 5.3_
  - _Boundary: Standalone connector Go_
  - _Validation: GOWORK=off go test ./... and public-only import audit_

- [x] 2.2 (P) Relocate SDK bridge with pinned toolchain baseline
  - Move bridge source, production npm lock, SDK fixtures/tests, and bridge scripts with SDK 1.0.23 and Undici 6.28.1 unchanged.
  - Preserve model discovery, text/reasoning/tool/warning/usage semantics, credential redaction, bounded diagnostics, sandbox validation, and readiness/version reporting.
  - Keep all Node tooling inside the plugin repository.
  - Observable completion is a relocated bridge that passes its existing hermetic suite under Node 22.22.3.
  - _Requirements: 2.1, 4.1, 5.3, 5.4, 6.2_
  - _Boundary: Standalone connector bridge_
  - _Validation: npm ci, npm test, and npm run typecheck_

- [x] 2.3 Wire plugin-local private companion resolution
  - Resolve the packaged default bridge through a direct plugin-local launcher path relative to the installed outer executable.
  - Preserve explicit `bridge_executable` overrides and existing direct-executable validation without shell, npm, global binary, or automatic download behavior.
  - Preserve user-supplied workspace and configuration semantics unchanged.
  - Observable completion is an installed-layout launch that uses the private companion by default while honoring explicit overrides.
  - _Requirements: 3.4, 4.1, 5.4_
  - _Boundary: Standalone connector_
  - _Depends: 2.1, 2.2_
  - _Validation: packaged-layout launch and override regression tests_

- [x] 3. Private runtime packaging and compatibility evidence
- [x] 3.1 Implement direct private Node launcher with owned cleanup
  - Build new `cmd/lip-cursor-sdk-bridge` launcher that locates the fixed private Node executable and bridge entrypoint, forwards protocol streams and exit status, and performs no shell/npm/download operations.
  - Establish launcher cleanup before resource escape, release partial acquisitions on startup failure, and supervise the runtime descendant under the existing process-tree policy on both POSIX and Windows paths.
  - Cover startup failure, shutdown during start, descendant termination, late settlement, and repeated close with deterministic tests plus Linux race evidence.
  - Observable completion is a tested launcher that starts, supervises, shuts down, and reaps the private runtime without leaking owned processes.
  - _Requirements: 3.4, 5.1, 5.2_
  - _Boundary: Private runtime package_
  - _Validation: GOWORK=off go test -race ./... for launcher and bridge lifecycle packages_

- [x] 3.2 Assemble native archives with trustworthy private layout
  - Build production JavaScript, stage the lockfile that pins the SDK, and ship NO third-party package code: the proprietary Cursor SDK is operator-provisioned, not redistributed.
  - Preserve manifest identity, `local_only`, `per_instance`, `agent_runtime`, static credentials, and existing supported Linux/Windows claims only where native tests pass.
  - Validate checksum coverage for shipped private files, protected install ownership, missing-companion failure, missing-SDK failure with remediation, paths containing spaces, and no global Node requirement.
  - Observable completion: installable native archives whose verification script reports exact files, checksums, runtime metadata, and SDK provisioning state.
  - _Requirements: 2.3, 2.4, 3.4, 3.5, 3.6_
  - _Boundary: Private runtime package_
  - _Depends: 2.3, 3.1_
  - _Validation: scripts/package-plugin and scripts/verify-package on native OS/arch_

- [x] 3.3 (P) Stop redistributing the SDK and require operator provisioning
  - Stop staging `private/bridge/node_modules/`; ship `package.json` + `package-lock.json` only, and record in the archive that the SDK is operator-provided and not redistributed.
  - Make a missing or version-mismatched provisioned SDK an explicit, actionable prerequisite failure naming the exact provisioning command; keep the existing no-shell/no-download posture.
  - Narrow the shipped checksum record to shipped files and state the provenance split: the plugin authenticates what it ships, the operator authenticates what they provisioned.
  - Add the installation/provisioning documentation for operators.
  - Observable completion is an archive with no third-party package code, a verifier that fails closed on an unprovisioned tree, and documented provisioning steps.
  - _Requirements: 2.4, 3.4, 3.5, 3.6_
  - _Boundary: Private runtime package_
  - _Depends: 3.2_
  - _Validation: archive content audit, unprovisioned and provisioned verification on native OS/arch_

- [x] 3.4 (P) Narrow manifest template, relocate stranded host scripts, inspect required CI checks
  - Narrow `manifest/template.backendplugin.json` to platforms the pipeline can natively assemble, so an unvalidated platform cannot be claimed.
  - Relocate the remaining host-only `scripts/test-cursor-sdk-{comparison-report,live,platform}.{sh,ps1}` to the plugin repository, so the cutover batches can delete them with an owner in place.
  - Inspect branch protection and required status contexts for the Cursor lane; replace the host-relevant invariant with the generic no-Node guard before any required check is retired.
  - Observable completion is a manifest that cannot overclaim, no orphaned Cursor scripts, and a recorded required-check disposition.
  - _Requirements: 2.4, 6.1_
  - _Boundary: Plugin certification_
  - _Depends: 3.2_
  - _Validation: manifest/template platform audit, script inventory, gh required-check inspection_

- [x] 3.5 Record packaging evaluation and release compatibility metadata
  - _Evidence: published `cursorsdk-v0.1.0`, immutable source `c403e28d1d5993068ecce798c78d45349fe45964`, release run `37819483634`. Independently checked published archive digests, compatibility metadata, SDK non-redistribution, provisioning instructions, and provenance attestations are recorded in `release-certification.json`. Windows requires a full installed `bridge_executable` path; Linux supports packaged default resolution. Host `v0.1.0` and root/ACP module pins remain unchanged._
  - Generate `compatibility.json` from validated release inputs with plugin/build/source identity, exact host/root/ACP/runtime versions and the REQUIRED (not bundled) SDK version, protocol range, platform evidence, tested host hashes, package verification results, external-Node requirement flag, SDK provisioning command, and an explicit non-redistribution statement.
  - Document the SEA versus private-runtime evaluation, including SDK loading, imports, metadata lookup, native assets, sandbox behavior, signatures, and platform limits, without widening the closed host manifest.
  - Publish tested per-OS installation instructions stating explicitly whether system Node is required and exactly how to provision the SDK with the shipped runtime.
  - Observable completion is a release whose compatibility and packaging decision can be audited without inspecting build logs.
  - _Requirements: 2.4, 3.5, 3.6, 6.4_
  - _Boundary: Plugin certification_
  - _Depends: 3.2, 3.3, 3.4_
  - _Validation: compatibility metadata schema check and installation rehearsal_

- [x] 3.6 (P) Make verification tractable and probes bounded
  - Replace the O(N²) recorded-digest resolution and per-file digest spawns in the shell verifier with a single indexed pass, and add bounded timeouts to the runtime and launcher probes so a replaced interactive binary fails instead of hanging.
  - This is a performance and robustness change only: it must not alter any verdict, and every trust check must remain intact.
  - Observable completion is a materially faster gate with probe timeouts proven to produce a finding rather than a hang.
  - _Requirements: 2.4, 3.4_
  - _Boundary: Private runtime package_
  - _Depends: 3.2_
  - _Validation: gate runtime before/after, unchanged verdicts across the existing adversarial cases_

- [x] 4. Plugin certification against public contracts
- [x] 4.1 Preserve SDK contract, stream, lifecycle, and diagnostics regressions
  - Retain baseline SDK fixtures and cover bridge tests, canonical execution, ordered events with single terminal outcome, session reuse/isolation and resource limits, capability errors, cancellation/shutdown bounds, generation fencing, redaction, health/readiness, and sandbox denial.
  - Keep macOS fake-bridge/lifecycle coverage as development evidence without claiming production Darwin support.
  - Report live provider scenarios separately with credentials; do not hide quota consumption in default verification.
  - Observable completion is a preserved regression suite that fails on semantic, lifecycle, or redaction drift.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 5.1, 5.2, 5.3, 5.4, 5.5, 6.2_
  - _Boundary: Plugin certification_
  - _Depends: 2.3_
  - _Validation: plugin Go tests, bridge tests, and public conformance suite_

- [x] 4.2 Certify real host install, trust, and optional activation
  - _Evidence: final release run `37819483634` certified published `cursorsdk-v0.1.0` against unchanged host `v0.1.0` on native Windows/amd64 and Linux/amd64. Both published host-cert reports contain 16 PASS lines (top-level test, intermediate companion-contract group, and 14 leaf cases), zero failures, and the adopted platform-specific companion contract. Discovery, trust, negotiation, inventory, canonical execution, capability refusal, inactive discovery, multi-user denial, and missing-resource failures were independently reviewed; see `release-certification.json`._
  - Verify trusted discovery, manifest identity, secure negotiation, inventory listing, canonical execution, explicit capability errors, inactive discovery, and default-deny multi-user behavior against a versioned host artifact without recompiling the host.
  - Verify missing plugin/runtime resources fail explicitly without automatic installation or provider fallback.
  - Observable completion is a certified plugin release installable through the existing mechanism with unchanged host binary.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 4.5, 6.1_
  - _Boundary: Plugin certification_
  - _Depends: 3.3, 4.1_
  - _Validation: real host install, inspect/doctor, negotiation, execution, and access-denial checks_

- [x] 5. Host decoupling and Node-independence guard
- [x] 5.1 Add host no-Node verification and active-reference regression
  - Add a no-Node lane that runs root build, unit/quality/comprehensive checks, minimal packaging, CLI startup/help, and non-Cursor runtime smoke where Node and npm are unavailable and fails if either tool is invoked.
  - Add a narrow QA regression over active build/workflow/module inventories for Cursor source paths and Node setup/install commands, excluding historical specs and generic examples.
  - Observable completion is a host check that proves standard development works without the Cursor toolchain.
  - _Requirements: 1.1, 1.2, 1.3, 6.3_
  - _Boundary: Host independence guard_
  - _Evidence: GitHub run `37323136994`, job `111807118483`, passed all nine steps on `ubuntu-latest` at repair commit `f1c504d5`. The lane reported no Node entry point reachable or invoked and uploaded artifact `11351686982`. The Linux admin-detector test's missing config import was repaired without changing its assertions._
  - _Validation: node-independence lane and internal/qa/node_independence_scope_test.go_

- [x] 5.2 Retire Cursor-specific host coupling after external certification
  - Remove family census entry, dedicated workflow/Dependabot/cache-lane coupling, Cursor Makefile/script targets and source-specific exceptions, Cursor examples/config assumptions, active documentation npm instructions, first-party inventory entries (`backend_prefix_inventory`, `multi_user_backend_policy`, `standard_bundle_posture`, `expected_inventory`, `catalog_population`, `backend_multi_user_policy`), and the `discovered_factories` absence guard.
  - Replace presence-based architecture tests with external-boundary/absence regressions while preserving generic trust, lifecycle, default-deny, and contract coverage; swap the Cursor-named authority fixture for a generic external-plugin fixture where practical and leave `cursorcliacp` references untouched.
  - Inspect required CI checks/rulesets first and replace the host-relevant invariant before retiring or unrequiring bridge checks.
  - Observable completion is a host tree with no active Cursor build/CI dependency and no blocked required job.
  - _Requirements: 1.4, 6.3_
  - _Boundary: Host independence guard_
  - _Depends: 4.2, 5.1_
  - _Evidence: merged PR #839 (`00c2ad775`) removed `.github/workflows/cursor-sdk-platform.yml` - the only host workflow that ran the bridge lane and produced the `bridge-node-tests` context - plus the Dependabot entry, the cache-lane registration, the Cursor Makefile and script targets, the eight `scripts/test-cursor-sdk-*` helpers, the first-party inventory entries, and the `discovered_factories` presence guard, and rewrote `internal/archtest/cursor_sdk_external_gates_test.go` as an external-boundary guard. PR #904 (`10b382ad7`) re-added the Dependabot entry by accident; this change removes it again. That removal is temporarily reversed while the module exists: `scripts/tidy-all-modules.sh --check` requires every discovered Go module to appear in the Dependabot directory list, so with `connectors/cursorsdk/go.mod` still in the tree the `/connectors/cursorsdk` entry is required, and merged PR #1341 (`67c21d9b6`) restored it. The exception covers the Dependabot entry only - the workflow, cache-lane, Makefile, script, inventory and absence-guard retirements of this task stand - and it lasts exactly as long as the module does, because task 6.2 deletes the entry in the same change that removes `connectors/cursorsdk/go.mod`. Required-check reconciliation: `main` carries no live required status checks (`repos/matdev83/go-llm-interactive-proxy/branches/main` reports `protected: false` with an empty context list), so no retired job can block a merge, and the replacement host invariant is the no-Node lane of task 5.1; the plugin records the same inspection in its `docs/host-required-checks.md`._
  - _Validation: make quality-checks, affected architecture/QA tests, and required-check inspection_

- [x] 6. Bounded cutover and migration verification
- [x] 6.1 Transfer test ownership and complete first bounded removal
  - Transfer all 59 Cursor Go test files and bridge verification ownership to certified external CI while retaining a completely buildable in-tree implementation fallback.
  - Remove in-tree Cursor-only workflow/QA expectations made obsolete by the transfer without weakening generic architecture rules.
  - Keep this change within the 100-Go-file gate and preserve generic contract coverage needed by the remaining implementation.
  - Observable completion is external ownership of Cursor tests with a still-buildable host fallback and passing host gates.
  - _Requirements: 1.4, 6.1, 6.2_
  - _Boundary: Host independence guard_
  - _Depends: 4.2, 5.2_
  - _Evidence: this change removes the in-tree Cursor test ownership: 71 Go files (60 `*_test.go` plus the 11 test-support files `internal/product/fakebridge/**`, `internal/product/comparison/**` and `internal/product/protocol/fixtures.go`), inside the 100-Go-file gate. The implementation fallback stays buildable - `go build ./...` and `go vet ./...` are clean in `connectors/cursorsdk` after `go mod tidy` dropped the test-only `testify` and `goleak` requirements - and the certified plugin project owns the equivalent suites, whose CI runs them (`verify.yml` lanes `go`, `go-windows`, `go-macos-dev`, `go-race` and `bridge-node`). The host expectations that the transfer made obsolete went with it: the retired `cursor-sdk-platform.yml` workflow and its `internal/qa/main_push_lane_scope_test.go` lane entry (PR #839), the two `scripts/fuzz-targets.tsv` rows whose fuzz targets were test functions, the `.golangci.yml` `paralleltest` exception for the deleted test path, the `scripts/check-adhoc-goroutines.{sh,ps1}` allowlist entry for the deleted fake-bridge harness, and the `internal/archtest/credential_source_gate_test.go` allowlist entries for the deleted harness, fixture loader, and comparison report. The shared fixture corpus under `internal/product/testdata/fixtures` stays until 6.2 because the bridge's TypeScript suites still read it._
  - _Validation: go test ./... scope, architecture/QA tests, and changed-file count audit_

- [x] 6.2 Remove remaining implementation and finalize host guards
  - Delete the remaining 66 Cursor Go files, module/release metadata, implementation assets, active enumerations, and host inventory/absence-guard updates within the remaining file budget, splitting further if host guard changes exceed budget.
  - Delete the `/connectors/cursorsdk` Dependabot directory entry in the same change that removes `connectors/cursorsdk/go.mod`. The module-synchronization check requires the entry for exactly as long as the module exists, so the entry restored by #1341 is a temporary state that this task closes.
  - Replace active in-tree build/installation instructions with the standalone plugin location while retaining historical archived specifications.
  - Observable completion is a host tree with no active Cursor source and final external-boundary regressions passing.
  - _Requirements: 1.4, 6.2, 6.6_
  - _Boundary: Host independence guard_
  - _Depends: 6.1_
  - _Evidence: this change removes the rest of the in-tree connector: 53 Go files plus the module metadata (`go.mod`, `go.sum`, `release.yaml`), the closed manifest template, the Node bridge tree and the shared fixture corpus, all under the 100-Go-file gate. It also deletes the `/connectors/cursorsdk` Dependabot entry in the same change, closing the temporary exception recorded by #1341/#1345. The host guards became absence regressions: `internal/archtest/cursor_sdk_external_gates_test.go` flips the module-present assertion to module-absent, and `internal/archtest/cursorsdk_boundaries_test.go` replaces the bridge-boundary scan with a host-wide guard that no `@cursor/sdk` import or require site exists anywhere in the host. The first-party enumerations that named the in-tree module are cleaned (`internal/providerprofiles/expected_inventory_test.go`, `internal/testkit/contract/backend/terminal_connector_matrix_test.go`, `internal/archtest/credential_source_gate_test.go`, `scripts/check-adhoc-goroutines.{sh,ps1}`), and the active build and installation instructions in `docs/cursor-sdk-backend.md`, `config/config.yaml`, `config/examples/cursor-sdk-experimental.yaml` and `README.md` now name the standalone plugin. The historical specifications stay archived. Two host-side registrations deliberately survive: the `cursorsdk` credential contract in `internal/infra/backendplugins/manifest/namespace.go` (the external plugin's manifest is validated against it) and the connector's billing-coverage entry in the sibling `billing-financial-safety-contracts` spec, which is retargeted at the external plugin rather than dropped._
  - _Validation: go test ./..., make test, git diff --check, the changed-file count audit, and `bash scripts/tidy-all-modules.sh --check`_

## Deferred / Out-of-Plan

- **Former task 6.3 (rehearse migration, rollback, and final host certification)** is deferred to issue #1404 by owner decision (2026-10-29) and is not part of this plan. It cannot be finished in this repository: it needs a host release carrying the v2 credential-contract ABI and a compatible `aiproxer-cursor-sdk` 0.2.0 release. The partial evidence stays in `cutover-rehearsal.json` (install half verified; compatibility half blocked on that ABI). The deferred scope is reduced to publishing plugin 0.2.0 against the next host release; the Windows certification and the rollback rehearsal are dropped unless a real user needs them.

## Completion Status

- [x] The spec's purpose is delivered: the standard host builds, tests and runs without Node or npm, and the Cursor SDK integration lives in the standalone `aiproxer-cursor-sdk` plugin (removal merged as `f1caa0eb` under the maintainer's temporary certification waiver; tasks 1–6.2 carry their evidence above).
- [x] Former task 6.3 is deferred to #1404 (see above); Cursor support is experimental and currently works only with host `v0.1.0` until plugin 0.2.0 ships.

## Implementation Notes

- **Current disposition:** tasks 3.5 and 4.2 are verified by the published release and final native certification in `release-certification.json`; task 5.1 is verified by the full no-Node CI run below. Tasks 5.2, 6.1 and 6.2 are now closed: 5.2 by merged PR #839, 6.1 by the in-tree test-ownership transfer (merged PR #1311), and 6.2 by the removal of the remaining in-tree implementation, merged as `f1caa0eb`, which also deleted the `/connectors/cursorsdk` Dependabot entry and closed the temporary exception recorded by #1341/#1345. That merge happened before a compatible standalone plugin release existed, under a maintainer-authorized temporary certification waiver; task 6.3 (operator migration and rollback rehearsal, final host certification) therefore remains the last open task, is not satisfied by the merged state, and is tracked by GitHub issue #1404 together with the host and plugin releases. The spec stays active and is not archived until #1404 closes. Windows requires an installed full `bridge_executable` path; Linux default resolution remains supported. Issue #701 cleanup is complete: the confirmed lock-holding CodeGraph process was stopped and `go-lip-701-local-only-security` was deleted, with absence verified. Its non-required scanning job failed before analysis with `CAPIError: 400 The requested model is not supported`, not a code finding.
- **Worktree recovery:** 8,199 tracked files were observed missing from the task checkout; the captured status/diff output is `tool_10c66ce32001Gzqka7eCqXOqWv`. They were restored from pushed commit `d2294180`, after checking available disk space. The restored checkout was clean before the import repair; the deletion cause remains unknown.

- **Host no-Node lane (task 5.1, implemented; full gate PENDING):** `tools/backendplugin/node_independence/` (Go tool, mirroring the `tools/backendplugin/isolated_root_qa` precedent) plus `scripts/check-node-independence.{sh,ps1}`, `.github/workflows/node-independence.yml`, `internal/qa/node_independence_scope_test.go` and `internal/stdhttp/admin_detection_linux_test.go`. The design's `scripts/check-node-independence.py` filename was rejected after inspection: a controlled Go base image has neither `python3` nor `make`, and the mandated command set is `make`-based, so a Python entry script would force the lane to provision an interpreter, which the same design forbids. **Isolation is real absence, not PATH order.** The lane probes Node availability (PATH resolution at one level, the conventional system roots at bounded depth, every home version-manager root, and the repository tree), then a minimal elevation builds a **mount-only** namespace - deliberately no user namespace, because mapping the caller to root would leave the surface running as EUID 0 - via `sudo -n unshare --mount --fork --propagation private`. A tiny root helper masks every discovered entry point by bind-mounting a non-executable file over it, then `setpriv --reuid=$SUDO_UID --regid=$SUDO_GID --clear-groups` drops straight back to the caller and the verification surface runs unprivileged inside the namespace. No host file is ever moved, so **namespace teardown is the restore**: no quarantine directory, no restore trap, no root-owned residue. The drop identity is taken only from `SUDO_UID`/`SUDO_GID`, which sudo derives from the real caller, and a helper that cannot see a non-zero sudo identity refuses to run. Both handoff inputs - the mask list and the caller's environment - are staged as owner-only `0600` files whose owner and mode the helper re-checks, because **sudo resets the environment**: an env-carried mask list arrives empty and looks exactly like a clean host, and a stripped environment silently repoints the Go caches. Execution as root is refused outright, and the EUID is re-asserted after isolation, because the host itself refuses to start as an administrative user; that detector is now covered against the real process credential instead of only a stub. Negative controls must fail: every masked absolute path and every bare name, requiring a *launch* failure rather than a nonzero exit. Positive controls must succeed: `go version` and `make --version`. PATH tripwires remain defence in depth. `full` set = root build, `make test-unit`, `make quality-checks`, `make test`, `make package-minimal`, `lipstd --version`/`--help`, `make parity-sentinel`, and **every pull request runs `full`** - a reduced PR subset was rejected because it would leave quality, packaging and comprehensive steps unproven until merge.
- **Four defects execution caught, all regression-covered:** `exec.Command` resolves via the *ambient* PATH and ignores `cmd.Env`, so a negative control silently ran the real `/usr/bin/node`; `WalkDir` does not descend a symlinked root, so an fnm multishell symlink hid three entry points until roots are `EvalSymlinks`-resolved; `unix.Exec` does not search `PATH`; and sudo's environment reset arrived as an empty mask list and an empty masked-path report. Each one produced a *passing* run until a control was tightened.
- **Executed here:** the tool suite passes on Windows and on Linux, and `TestRootHelperMasksAndDropsPrivileges` drives the real chain - `sudo -> unshare --mount -> root helper -> setpriv` - reporting `masked=2`, `euid=1003`, `control-ok`, which is the evidence that Node is unreachable by absolute path and by name while privileges are back with the caller. A full-set run under WSL as that unprivileged user reached `probe after isolation: none` with 24 entry points masked, all controls satisfied, and `root-build` exit 0.
- **Full no-Node evidence:** run `37323136994` completed all nine steps on a native Linux checkout, resolving the local WSL Git-pointer limitation. The no-node job passed in 15m28s; the final verdict was `9/9 steps passed, no Node entry point reachable or invoked`. Evidence artifact `11351686982` accompanies the run.
- **New CI lane costs (identified, not weakened):** a fast-preflight lane-registration rule forces any `setup-go` job onto a registered bounded lane, so `policy.json` gained `node-independence` (workflow `Node independence`, job `no-node`, `build_mib` 7168) and `go-cache-maintenance.yml` gained the `Node independence` retention trigger; `prune-go-caches.py`'s v3 patterns are lane-generic and needed no change. The lane is registered in `main_push_lane_scope_test.go` (`TestQAFastPreflight_MainPushLaneScopes`) and its `no-node` job reports `always() && !cancelled()` so an unrelated-PR bypass stays mergeable. `node-independence` **is** in `.PHONY` (Makefile line 1, target at line 737), and the archived windows-task-reliability design table was extended with both its classification row (`Linux-authoritative`, because no Windows host can provide the namespaces) and its 90m budget row, so the table stays complete rather than falling back to the weaker `test-quick`/`proto-check` precedent of omitting the target. The two tests that used to enforce this by name — `TestQAFastPreflight_AllGoWorkflowsUseSharedCachePolicy` and `TestWindowsTaskReliability_TargetTableComplete` — were deleted upstream by PR #780 along with the rest of the CI/Makefile text-pinning tests. The table rows and the `.PHONY` entry above are the surviving evidence, re-verified at `eef98f56`; nothing about the lane itself was weakened.
- **Published host and measured certification evidence:** PR #732 merged as `987e1f7d527cfd84cae61d294709538de8d03815`. Merged-main verification run `37252922811` and tag-push GoReleaser run `37256275733` passed. Host release https://github.com/matdev83/go-llm-interactive-proxy/releases/tag/v0.1.0 contains six platform archives and `checksums.txt`; downloaded bytes were checked against published checksums and attestation subjects, release-workflow provenance, contents, and build metadata. Windows/amd64 and Linux/amd64 binaries were executed; other architectures were inspected, not executed. Real-host plugin checks passed 13/13 on each of Windows/amd64 and Linux/amd64 with explicit companion paths. Linux default resolution was measured reachable; Windows default resolution was measured unreachable because the outer executable is copied into staging. This is measurement, not final plugin certification, and has nothing to do with Windows/arm64.
- **Latest merged plugin delivery:** PR #15 (`2915fee`, "test: record measured released-host compatibility without certification") landed the measured real-host gate and its documentation on plugin `main`: `host_release_certification_test.go`, `host_certification_record_test.go`, `certification_evidence_test.go`, plus `docs/certification.md` and the `docs/installation.md` section "Set `bridge_executable` explicitly on Windows". It measured host `v0.1.0` on both declared platforms (windows/amd64 digest `6a6f7462…`, linux/amd64 digest `ce52b7e3…`), 13/13 on each, and it deliberately records that a green default run is not host certification. Host release `v0.1.0` is unchanged by it. The measurement it records is the evidence behind the now-decided contract in requirements 3.5.
- **Completed plugin delivery:** task 3.6 merged in plugin PR #13 (`fa17c9b05a01cb9de89ba218f6272c064974f4a1`) with five CI lanes green. Task 4.1 merged in plugin PR #14 (`e144a52980da17caf167c44f8b0b2bbf7052d012`) with seven lanes green. The measured shared corpus is 10 executed scenarios and 16 hard negatives; live-provider evidence remains opt-in.
- **Tasks 6.2 / 6.3 disposition (after 5.2 and 6.1 closed):** final-release certification has satisfied 4.2, the host coupling is retired, and the Cursor test ownership now sits in the certified plugin project. What remains is the removal of the fallback in buildable slices and the operator-path rehearsal. The 100-changed-Go-file gate is an absolute limit, not a delivery slice budget: the in-tree module measured 124 Go files at `eef98f56` (59 test + 66 implementation) and now measures 53 Go files after the 6.1 transfer (71 Go files deleted, leaving the implementation and its module metadata), so 6.2 removes 53 Go files plus the module, its Dependabot entry, release, and bridge assets in one slice and 6.3 rehearses the operator path against the published plugin release. `cursorcliacp` is explicitly out of scope. The `billing-financial-safety-contracts` cross-spec obligation below must be resolved atomically with 6.2.
- **Refreshed cutover baseline and drift audit (documentation only):** see `migration-inventory-cutover.json`. The original extraction inventory `migration-inventory.json` is preserved byte-for-byte at its baseline commit `14cdd0e9`; the cutover file is additive evidence at `eef98f56`. Headline facts re-measured there: the connector tree is still 170 files / 125 Go (59 test, 66 implementation), every one of the 125 host Go files and all 45 non-Go files have a counterpart in the plugin at `2915fee` (zero missing transfers), and 55 plugin-only Go files are launcher, packaging and certification additions. Three host-side files moved ahead of the plugin on in-tree modernization and are lost when the host copy is deleted unless transferred: `internal/product/prompt.go` (`slices.Backward`), `internal/product/bridge_fault.go` (`errors.AsType`), `cmd/lip-backend-cursorsdk/main.go` (`serveNamedPipe` helper). The reverse direction is larger and needs no transfer back, because the plugin is the surviving owner: `internal/product/bridge_process.go` gained a bounded stderr-drain join and a single-owner `exitFault`, which repairs the lost-remediation defect recorded below and is plugin-only.

- **Cross-spec coordination obligation (discovered by the refresh, not resolved here):** the active sibling spec `.kiro/specs/billing-financial-safety-contracts` has open task **14.11** "Certify the cursorsdk connector end to end", which sets its working directory to `connectors/cursorsdk`. That spec did not exist at the extraction baseline, so the original inventory could not see it. It is **not** an enforced mechanical gate today: `run_task_gate.py --task 14.11` exits 2 on a missing `execution/task-manifest.json`, the whole `execution/` directory is absent, and `verify_coverage.py` never resolves `connectors/cursorsdk` — its only `Path` use is the `--spec` default, it compares spec-JSON `bids` against the hardcoded `EXPECTED_CONNECTORS` (which contains `cursorsdk`) plus `len(bids)==44`, and it will pass with a stale source reference. It also already exits 1 on the same missing manifest. So deleting the module will not turn any check red; the obligation is to the other spec's owner. Resolve it atomically with all three coverage artefacts — tasks.md 14.11, `coverage/universe.json` + `EXPECTED_CONNECTORS` + the `len(bids)` assertion in `tools/verify_coverage.py`, and `coverage/README.md` — before or with 6.2. Partial edits are worse than none, because nothing mechanical will catch the inconsistency. This refresh modified no file under that spec.
- **Inventory count hygiene (correction):** the 18 retain entries in `migration-inventory-cutover.json` are **not** 18 scan matches. They are 14 token-matched plus 4 carried by provenance or design authority with no Cursor reference at all (`.kiro/steering/tech.md`, `.kiro/steering/structure.md`, `docs/adr/0008-hybrid-backend-connector-plugins.md` return zero `cursor` lines; `AGENTS.md` mentions only the Cursor editor and `.cursor/skills`). The 9 archived spec trees are excluded from the scan by the scope pattern and appear because requirement 6.6 retains them. Separately, the token pattern under-reports by exactly one file: `docs/remote-ci-performance.md` names "the native ACP, Cursor, taskrunner and backend gates" in bare prose, which `cursorsdk|cursor[-_ ]sdk` does not match. The 45-figure in the earlier tasks.md note was a host sweep count from prose, unrelated to `migration-inventory.json`, where 45 is `connector_measurement.non_go_files`.

- **Module baselines (task 1.2):** published `v0.1.0-rc.1` for both root and ACP modules. The subsequently authorized root `v0.1.0` tag published the first public `lipstd` binary release. The plugin's existing root/ACP `v0.1.0-rc.1` pins remain real published module baselines; a final ACP tag is not an additional certification prerequisite.
- **Root tag causes repo-wide MVS drift (task 1.2):** publishing the root tag made every module that path-replaces ACP select `v0.1.0-rc.1`. `scripts/check-all-modules.sh` asserts `go mod tidy -diff`, so any module whose committed require line still says `v0.0.0` fails CI (6 connectors here). After changing a nested module's published require, expect a repo-wide require bump in every dependent module in the same PR.
- **Destination (task 1.3):** standalone repository is `https://github.com/aiproxer/aiproxer-cursor-sdk` (public, MIT), module `github.com/aiproxer/aiproxer-cursor-sdk`, scaffold head `1583c29b`. Keep it MIT; derived host-side code originates from an Apache-2.0 repository and PROVENANCE.md records that attribution.
- **`internal/pinnedcontracts` is a scaffold guard:** it exists so `go build`/`go test`/`go mod tidy -diff` are meaningful in an otherwise empty module and it fails if a `replace` is introduced. Task 2.1 should delete it once real code imports the public contracts.
- **Packaging merged (task 3.2):** plugin PR #9 merged `524bfd20` after THREE rejections plus a debugger round. Root cause of the loop (worth remembering for any future packaging work): the trust surface grew faster than its model — each round added an input that could decide a verdict (ambient CWD, then an explicit `--repo-root`), and each fix added surface instead of removing it. The break came from DELETING an option rather than documenting the hole. Rule: when a verifier input is a trust decision, remove it; when it is a build input, keep it.
- **Verifier trust boundary (settled):** neither verifier accepts a repository substitution — both resolve the repository from their own location. Packagers keep `--repo-root` (build-side). Checksums cover private files but are unsigned, so "attacker regenerates `checksums.sha256`" still verifies clean; the stated mitigation is protected install-root ownership plus the host's manifest digest for the outer process only.
- **Packaging performance debt (deliberately deferred):** `verify-package.sh` still resolves recorded digests by linear scan (O(N²)) and spawns `sha256sum` per file; one verification is ~31 s and the ubuntu gate needs `-timeout 25m` (11 min). Fix as its own reviewed PR — it is a performance change, not a trust change.
- **Packaging known limitations (recorded in plugin README):** verifier probes have no timeout (a replaced interactive binary hangs rather than fails), path comparison is byte-wise with no Unicode normalization (fails closed), the PowerShell ownership FAIL branch is driven end-to-end only where permission bits are readable, and the `.sh` packager cannot be validated on a Windows host under git bash.
- **Native platform evidence:** windows/amd64 + linux/amd64 are assembled and verified natively and CI-enforced via a `package` matrix on `windows-latest`/`ubuntu-latest`. windows/arm64 and linux/arm64 remain declared-not-assembled; darwin is not declared.
- **RELEASE BLOCKER resolved by decision (spec amended):** `@cursor/sdk` is proprietary and its platform package ships bundled `rg`/`cursorsandbox` whose license texts are not redistributed. The maintainer decided the plugin must NOT redistribute the SDK. The archive now ships `package.json` + `package-lock.json` and **no** `node_modules/`; the operator provisions the SDK once with the shipped private runtime's own bundled npm. Requirements 3.4/3.5/3.6 and the design's "Private runtime package" component were amended accordingly. Consequences to honor in implementation: the shipped checksum record covers shipped files only (the provisioned tree is operator-owned); the verifier's SDK check becomes a requirement (resolve at the pinned version via the shipped lockfile) rather than a digest check; provenance must state the runtime/bridge are project-attributable and the provisioned tree is operator-attributable.
- **Operator-provisioned SDK merged (task 3.3):** plugin PR #10 merged `c803d4b7`. Three rejections, all on shipped-text accuracy — never on packaging or trust logic. The recurring lesson: **license and provenance claims in shipped artifacts must be audited against the assembled bytes, not reasoned about.** Three separate false claims shipped this way ("no third-party package code at all" while shipping 1964 files of npm; "npm is MIT" when npm is Artistic-2.0 with deps on their respective terms; a coverage count that missed nested `node_modules`). Always re-derive license inventories and counts from a real archive before claiming them.
- **npm license facts (for future packaging work):** Node itself is MIT (`LICENSES/nodejs-LICENSE`), but bundled npm is **Artistic-2.0** and the packages npm bundles are "licensed on their respective license terms". npm's own license text ships at `private/node/node_modules/npm/LICENSE`; several bundled packages ship no license text upstream at all and are named (not staged) in the notice. Never attribute the npm tree to the Node MIT grant.
- **Provisioning command** (from the shipped runtime, no global toolchain): `cd <plugin>/private/bridge && ../node/node[.exe] ../node/<npm-cli> ci --omit=dev`. npm CLI paths differ per platform: `lib/node_modules/npm/bin/npm-cli.js` (linux tarball) vs `node_modules/npm/bin/npm-cli.js` (windows zip). npm specifically is required — `overrides` semantics differ per package manager and the Undici 6.28.1 baseline depends on it.
- **Trust split now explicit:** `checksums.sha256` covers shipped files only; verification runs with `--tree-state shipped|installed`; the verifier requires the provisioned SDK at the pinned version; provisioned-tree content authenticity is the operator's responsibility.
- **Pre-existing follow-up (connector-side), now resolved plugin-side:** `internal/product/bridge_process.go` snapshots `stderrBuf` in `waitProc` without joining the concurrent `readStderr` goroutine, so under load the connector can relay a bare exit status and lose the exact remediation text. `installed_layout_test.go:134` depends on that hop. Proven by mutation (delaying `readStderr` loses "not provisioned" + the provisioning command). The defect is still present in the in-tree host copy at `eef98f56`, and it stops mattering when 6.2 deletes that copy. The plugin repaired it in PR #12 (`7c804420b`) by joining the stderr reader under a bounded `bridgeObservationGrace` before stamping the exit fault, adding the single-owner `exitFault`/`terminalFaultLocked` reporting path and the `writeFailureResult` bridge, with coverage in `bridge_process_repair_test.go`, `bridge_process_exit_diag_test.go`, `bridge_process_final_test.go` and `bridge_process_test.go`. No transfer back is required; do not port the repair into the host copy that is about to be deleted.
- **Launcher merged (task 3.1):** plugin PR #8 merged `9e71f266`. Design updated: supervision is adopted on BOTH platforms (not POSIX exec-replace) because the spec's own late-settlement/repeated-close/Linux-race evidence is unobservable under exec-replace; two accepted residuals are recorded in design.md and README: a POSIX graceful close escalates against the direct child only, and the connector's identity-mismatch handle-only kill can strand the runtime (measured by `TestLauncherProcess_AbruptLauncherDeathByHandleAloneStrandsPrivateRuntime`).
- **Race on this host:** `go test -race` cannot initialize locally (ThreadSanitizer shadow-reservation error 87, reproduces on untouched packages). The plugin's `go -race (launcher and bridge lifecycle)` CI lane on ubuntu-latest is the race evidence — always confirm that lane before claiming concurrency evidence.
- **Task 3.2 must stage `private/bridge/bin/`** (now a design layout row): the launcher executes `private/bridge/bin/lip-cursor-sdk-bridge.js`, not `dist/main.js`. The rel-path constant lives in `package main` and cannot be imported by the packager, so 3.2 owns moving/consuming it.
- **Companion resolution (task 2.3):** plugin PR #7 merged `edaa8c40`. Default resolution = `os.Executable()` dir + `../private/bridge/lip-cursor-sdk-bridge[.exe]`, never CWD/PATH/npm. Two things to remember: (a) the shell/npm-wrapper guard applies only to an OPERATOR-supplied `bridge_executable`, never to the derived companion path — otherwise an install root containing `$`/`&` produced a misleading operator-facing error; (b) `os.Executable()` is used without `EvalSymlinks`, so decide before task 3.2 freezes the archive layout if a symlinked/junctioned install root matters.
- **Tasks 3.1/3.2 watch items:** the companion existence check is only `os.Stat` (a non-executable file is accepted at Configure and fails later at `fork/exec`); executability/checksum validation of private files belongs to 3.2.
- **Relocation merges (tasks 2.1/2.2):** plugin PR #1 (Go connector) merged `70eba5b2`, plugin PR #2 (SDK bridge) merged `6c050fc0`. Fidelity: 120/125 Go files byte-identical after module-path substitution (5 = gofmt import re-sorts + 2 path-depth edits); 32/33 bridge files SHA256-identical (only `bridge-node/README.md` differs).
- **Plugin CI pins:** use `npm exec --package=node@22.22.3 --package=npm@10.9.8 --call "npm ci && npm test && npm run typecheck"` in `bridge-node` to reproduce the Node lane locally; `npm exec` cannot `cd`, so pass the directory inside the command string.
- **Stranded host scripts:** only `scripts/test-cursor-sdk-live-bridge.{sh,ps1}` moved in 2.1. `test-cursor-sdk-comparison-report.{sh,ps1}`, `test-cursor-sdk-live.{sh,ps1}`, and `test-cursor-sdk-platform.{sh,ps1}` were still host-only — task 3.4 (plugin PR #11 `5face2c`) relocated them, so all eight now have a plugin owner and the host copies are plain cutover deletions. Verified at `eef98f56`: the plugin carries all eight and the host still carries all eight.
- **Minor doc drift (resolved):** `bridge-node/package.json` said "Project-owned" while `bridge-node/README.md` said "Plugin-owned". The plugin aligned both to plugin-owned wording; the in-tree host copy still carries the old strings and is deleted in 6.2, so nothing needs porting.
- **Pre-existing Windows flake (unrelated to this spec):** `connector-support/acp` `TestKillProcessTree_WindowsDescendants` fails intermittently with `Kill: exit status 255` under load; reproduced on the pre-change baseline, so it is not caused by module publication work. Hardening its `taskkill` assertion needs a separate authorized change.
- **Cleanup done:** the erroneous scaffold repo `matdev83/go-lip-cursorsdk` was deleted by the maintainer after task 1.3. Only `aiproxer/aiproxer-cursor-sdk` exists.
- **Bridge lane is live (task 2.2):** `.github/workflows/verify.yml` `bridge-node` job is enabled (no `if: false`), SHA-pinned checkout + setup-node 22.22.3, `cache-dependency-path: bridge-node/package-lock.json`, running `npm ci` → `npm test` → `npm run typecheck`. Dependabot gained an npm entry for `/bridge-node` with no `ignore` so SDK/Undici security updates still surface.
