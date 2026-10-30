# Implementation Plan

## Execution and Delivery Context

- Seven bounded leaf tasks, each estimated at 1–3 hours and carrying its own RED→GREEN tests; all checkboxes remain open.
- Single corrective slice against the current approved implementation base. No public surface beyond five corpus rows; no canonical, core, billing, or transport change.
- Tasks 1 and 2 are parallel-safe (contract metadata versus selector rules, separate files). Tasks 3–7 sequence after them; task 7 closes the slice with architecture and traceability gates.
- Existing harnesses suffice; no foundation scaffolding task is needed. Tests observe certification behavior with literal expected values. Focused local tests only; race evidence is remote CI. This document does not authorize implementation.
- Before coding: rebaseline shared source and adjacent terminal-semantics work. After accepted tasks: focused contract gates plus `go run ./tools/kiro/kirocheck`.

## Tasks

- [x] 1. (P) Add the five versioned terminal scenarios to the corpus metadata
  - First reproduce the gap: capable-connector certification executes no terminal-semantics scenario from the shared corpus.
  - Append exactly the five designed rows with IDs, features, transports, prerequisites, and expected evidence; keep every pre-promotion row byte-identical and IDs unique.
  - Done: corpus schema and JSON round-trip checks pass with the five new IDs present and no existing row changed.
  - _Requirements: 1.1, 1.2, 1.3, 1.4_
  - _Boundary: SDK public contract metadata_
  - _Depends: none_
  - _Validation: go test -count=1 ./pkg/lipsdk/contract_

- [x] 2. (P) Gate the terminal scenarios through capability and transport selection
  - First reproduce unselected or mis-selected terminal scenarios for tools-capable, streaming-capable, and minimal subjects.
  - Apply the designed capability requirements so capable subjects select positive and incapable subjects select negative exactly once; preserve existing frontend-kind allowance for every pre-promotion scenario.
  - Done: selector fixtures show the exact positive/negative split per subject kind with full corpus coverage and no dropped IDs.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5_
  - _Boundary: shared scenario selector_
  - _Depends: none_
  - _Validation: go test -count=1 ./internal/testkit/contract/semantic_

- [x] 3. Execute the terminal scenarios in the connector TCK runner
  - First reproduce TCK runs that skip the new scenarios or record them without frame and terminal evidence.
  - Drive each terminal scenario through the host boundary with per-scenario invocation frames; record terminal, usage-observed, and error-observed evidence; record incapable subjects as executed hard negatives with no upstream activation.
  - Done: capable reference-connector certification executes all five with zero failures; incapable subjects show executed negatives only.
  - _Requirements: 3.1, 3.2, 3.4_
  - _Boundary: connector contract test runner_
  - _Depends: 1, 2_
  - _Validation: go test -count=1 ./pkg/lipsdk/backendplugin/contracttest_

- [x] 4. Re-certify existing connectors with no new failures
  - First capture the pre-promotion certification artifacts of the reference connectors as the comparison baseline.
  - Re-run certification after promotion and prove exact corpus coverage with zero failures; any failure is triaged as slice-owned and repaired here.
  - Done: every previously certified connector validates green against the extended corpus.
  - _Requirements: 3.3_
  - _Boundary: connector certification evidence_
  - _Depends: 3_
  - _Validation: go test -count=1 ./pkg/lipsdk/backendplugin/contracttest; certification artifact validation_

- [x] 5. Anchor matrix cells to corpus IDs without moving builders
  - First reproduce matrix cells for promoted concerns that name no shared corpus scenario.
  - Reference the corresponding corpus ID from each clean-text, clean-tool-call, output-limit, upstream-error, and usage-reported cell in both matrices; keep every covering test and builder where it is.
  - Done: both matrices execute fully green with all cells anchored and no builder moved into the contract package.
  - _Requirements: 4.1, 4.2, 4.3_
  - _Boundary: test-only matrix registries_
  - _Depends: 1_
  - _Validation: go test -count=1 ./internal/testkit/contract/frontend ./internal/testkit/contract/backend_

- [x] 6. Prove in-process TCK selection is drift-free for existing subjects
  - First reproduce any positive/negative drift on fixed frontend, backend-family, and core subject fixtures after promotion.
  - Assert pre-promotion scenarios keep their exact selection per fixture and only the five new IDs appear as additions.
  - Done: traceability and release-readiness checks pass unchanged for existing subjects.
  - _Requirements: 2.5, 3.3_
  - _Boundary: in-process TCK harnesses_
  - _Depends: 2, 4_
  - _Validation: go test -count=1 ./internal/testkit/contract/..._

- [x] 7. Close with architecture and traceability gates
  - First reproduce the slice's gate status: corpus budget, import direction, and stale-reference checks.
  - Prove no core import on concrete plugins, provider SDKs, or test-only registries; prove kirocheck green and every requirement ID covered by the tasks above.
  - Done: architecture guards, corpus checks, and kirocheck all pass with no scope beyond the five rows and their runner branches.
  - _Requirements: 4.4_
  - _Boundary: architecture guards and spec metadata_
  - _Depends: 5, 6_
  - _Validation: go run ./tools/kiro/kirocheck; affected architecture-guard packages_

## Completion Status

- [x] All seven tasks delivered in #1118 (`24ee257e9`), the user-authorized implementation of issue #1106 ("Closes #1106").
- [x] Task evidence on merged main:
  - Task 1: five additive corpus rows (`terminal-clean-text`, `terminal-clean-tool-call`, `terminal-output-limit`, `terminal-upstream-error`, `terminal-usage-reported`) - `pkg/lipsdk/contract/scenarios.go` with round-trip tests.
  - Task 2: capability/transport selection for the new IDs - `internal/testkit/contract/semantic/semantic_contract_test.go`.
  - Task 3: connector TCK executes all five against the reference Fake - `pkg/lipsdk/backendplugin/contracttest/host_adapter_test.go`, `internal/testkit/backendplugin/fake.go`.
  - Task 4: connector re-certification (Azure + cohere suites green; remaining connectors covered by CI).
  - Task 5: matrices anchor every concern cell to its corpus ID - `internal/testkit/contract/frontend/terminal_semantics_matrix_test.go`, `internal/testkit/contract/backend/terminal_semantics_matrix_test.go`.
  - Task 6: in-process TCK selection drift-free - `internal/testkit/contract/backend/selection_contract_test.go`, `internal/testkit/contract/core/selection_contract_test.go`, `internal/testkit/contract/core/tck_test.go`, `internal/testkit/contract/frontend/selection_contract_test.go`, `internal/testkit/contract/frontend/standard_frontends_test.go`.
  - Task 7: `go run ./tools/kiro/kirocheck` green (task validation, re-run at closeout).
- [x] The generation-time notes above (spec-only context, unchecked tasks, approval tracked separately in PR #1110) describe the spec-authoring phase; the implementation was separately user-authorized and merged, so this bundle has no residual implementation work.
