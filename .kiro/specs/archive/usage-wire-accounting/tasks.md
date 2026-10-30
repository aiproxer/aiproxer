# Implementation Plan

## Delivery Constraints

Requirements, design, the three-producer amendment and tasks are approved. The
owner authorized implementation. Eleven leaves, about 1–3 hours each.
Each task includes its own TDD evidence; there is no standalone certification task.

Tasks 1–2 deliver prerequisite A from current origin/main in a dedicated worktree.
Task 3 delivers prerequisite B independently from origin/main in another worktree.
Task 4 blocks until both are delivered on main; merge requires separate owner
authorization. Do not bypass that barrier or author changes on main.

Use task-owned worktrees under the container, preserve unrelated work, and retain
exact base/head SHAs and command evidence through the existing handoff inventory.
Measure each PR with the delivery report. Tasks 4 and 9 recheck the whole
remediation PR's 40-file guideline and 5x test proportionality; reduce duplicated
proof or split rather than waive the budget. No local race execution.

- [x] 1. Select one client usage perspective across a turn
  - Start RED with fallback-only, first explicit selection, explicit zero,
    later fallback, and subsequent explicit-delta cases.
  - Cover top-level client events without scopes, scoped-over-shell precedence
    and all matching client scopes. Keep the exported event-local accessor's
    existing single-scope semantics; multi-scope accumulation remains private.
  - Reset usage counters, exclusion corrections and usage cost metadata only;
    preserve text, tools, warnings, terminal state and original events.
  - Preserve selection/correction state through clones and legacy interpretation
    for directly populated fallback collectors.
  - Done: selected client counters never include provider quantities, including
    after cloning or explicit zero. Run affected decision/parity consumers too.
  - _Boundary: Collector_
  - _Depends: none_
  - _Validation: focused canonical collector/clone tests; affected consumer suites_
  - _Requirements: 3.1, 3.2, 3.3, 3.4_

- [x] 2. Integrate perspective selection with actual executor output
  - Extend the existing real-executor fixture with reconstruction enabled and
    preserve assertions that provider and client usage precede the terminal.
  - Feed executed output to Chat and Responses in both modes; fix only existing
    selection/consumer integration, not financial or routing policy.
  - Done: prompt/completion equal selected client counters rather than a sum,
    and original provider evidence remains available unchanged.
  - Retain the selected perspective's existing reported-or-derived total rule;
    OpenAI sum totals belong to task 9. This prerequisite proves the shared
    collector seam; four-family parity also belongs to task 9.
  - _Boundary: Collector and executor-to-frontend integration_
  - _Depends: 1_
  - _Validation: focused runtime accounting regressions; affected family suites and contract gate_
  - _Requirements: 3.1, 3.2, 3.4, 5.2_

- [x] 3. (P) Preserve omitted provider totals across connector transport
  - Work independently in prerequisite B's current-main worktree, with no new
    exclusion fields or remediation-branch helpers.
  - First fail actual DTO-to-protobuf-bytes-to-decoded-DTO cases for absent,
    explicit-zero and reported nonzero totals.
  - Remove missing-total synthesis, retaining existing legacy nonzero-value
    compatibility and other quantities/presence. Update only expectations that
    asserted the fabricated total and explain the behavioral correction.
  - Done: missing total stays nil/absent and explicit zero stays present zero;
    a derived input/output sum never becomes a reported provider quantity.
  - _Boundary: Connector conversion_
  - _Depends: none_
  - _Validation: bridge/protobuf round-trip regressions; owning SDK suite_
  - _Requirements: 2.2, 2.3, 5.3_

- [x] 4. Reconcile delivered prerequisites into the remediation branch
  - Verify both prerequisites are available on main by merged content, not just
    ancestry after squash merges; otherwise report the delivery blocker.
  - Rebase PR #1325 in its dedicated worktree, preserving unrelated changes and
    recording the new base/head and semantic conflict resolutions.
  - Preserve usage selection, all existing correction-field resets and omitted
    totals in overlapping collector/clone/bridge edits. Independent flag meaning
    remains task 5's change, not a hidden partial conflict-resolution edit.
  - Done: both prerequisite regressions and existing PR regressions pass together
    on the reconciled head before new semantic edits. Refresh delivery budgets.
  - _Boundary: Delivery setup and semantic integration_
  - _Depends: 2, 3_
  - _Validation: prerequisite and existing PR regressions on rebased head; delivery report_
  - _Requirements: 2.2, 2.3, 3.1, 3.2_

- [x] 5. Interpret cache reads and writes independently
  - Restore read-only exclusion meaning and add the independent write declaration
    with a backward-compatible false default; correct the steering flag contract.
  - First fail fully inclusive, read-exclusive/write-inclusive, both-exclusive
    and write-only-exclusive projection cases.
  - Exercise differing per-delta declarations, explicit selection resetting both
    correction accumulators, and clone preservation.
  - Done: 400 input plus 600 excluded reads with 100 already-included writes
    yields 1000, as does 300 input with 600 reads and 100 writes both excluded.
  - _Boundary: Inclusion declarations and canonical collector_
  - _Depends: 4_
  - _Validation: canonical projection, mixed-delta, selection and clone tests_
  - _Requirements: 1.1, 1.2, 1.3, 3.3_

- [x] 6. Transport independent declarations through connector carriers
  - Add independent write fields to normal and sideband carriers using the
    design's additive protobuf numbers; keep read fields and defaults unchanged.
  - Extend SDK conversion, canonical bridging and sideband fingerprints without
    introducing a new plane, dependency or handshake.
  - Prove all four flag combinations through real protobuf serialization, absent
    and zero counters, and a write-only declaration change in deduplication.
  - Done: flags, native quantities and presence survive SDK round-trips. Use
    pinned generation and update the generic ABI baseline once in this task.
  - _Boundary: SDK carrier and protobuf integration_
  - _Depends: 5_
  - _Validation: SDK round-trip/sideband tests; proto-check; ABI baseline update and check_
  - _Requirements: 2.1, 2.2, 2.3, 5.3_

- [x] 7. Restore declarations at every host usage boundary
  - First expose metadata loss in normal stream, sideband and finalizer conversion
    with shared native counter cases and independent flag states.
  - Copy both declarations without altering quantities, presence, plane,
    provenance or deduplication identity.
  - Include actual protobuf conversion with omitted and explicit-zero totals.
  - Done: normal and finalized usage convey identical declared interpretation
    while retaining their original provider evidence.
  - _Boundary: Host adapter integration_
  - _Depends: 6_
  - _Validation: host normal-usage, accounting-evidence and finalizer regressions_
  - _Requirements: 2.1, 2.4, 5.3_

- [x] 8. Migrate existing exclusive-input producers
- [x] 8.1 Declare both exclusions for native Anthropic and Claude ACP
  - Extend existing producer fixtures with both declarations and unchanged native
    quantities/presence. Assert text events still carry neither declaration.
  - Annotate Claude usage in its existing classified stream and remove the
    redundant usage-only wrapper.
  - Done: native Anthropic and Claude usage explicitly retain their fully
    exclusive shape without rewriting input/output/cache counters.
  - _Boundary: Native and Claude ACP producer integration_
  - _Depends: 7_
  - _Validation: native mapper fixtures; Claude ACP module-local service suite_
  - _Requirements: 1.4, 2.4_

- [x] 8.2 Audit and migrate the three existing compatible producers
  - Audit GitLab Duo, MiniMax OAuth and CommandCode Anthropic write inclusion;
    retain the source/fixture basis for each conclusion in the PR description.
  - Declare excluded writes only where established, preserving independent flags
    through partial snapshots and existing non-stream annotations.
  - Extend module-local evidence fixtures for unchanged native quantities,
    presence and raw evidence. This instantiates task 5's projection contract,
    not a competing rule; no dependency upgrades, release or ACP #1324 work.
  - Done: all three producers explicitly communicate actual write inclusion and
    their focused module regressions pass without double-counting writes.
  - _Boundary: Existing compatible-producer migration integration_
  - _Depends: 8.1_
  - _Validation: three connector module-local producer suites_
  - _Requirements: 1.1, 1.2, 2.4_

- [x] 9. Encode protocol-correct selected usage in both response modes
  - First fail family cases for independent declarations, scoped/unscoped usage,
    unequal provider totals, missing totals and extension settings.
  - Chat/Responses derive wire total from inclusive input plus output without
    inventing a breakdown for total-only evidence. Remove the unmerged generic
    total helper and its old tests when replacing its last use.
  - Gemini preserves the reported total when it does not fall below the
    inclusive prompt plus output and otherwise reports that floor; fully
    exclusive Anthropic final usage remains unchanged.
  - Extend the bounded four-family, two-mode parity fixture with parsed final
    usage and observed terminals, not substring assertions. Correct the steering
    total contract with its implementation, without pending-status footers.
  - Done: ordinary corrections work with extensions off, OpenAI equality holds,
    and final quantities agree across modes. Recheck whole-PR file/test budgets;
    run ABI checks without a second baseline update.
  - _Boundary: Frontend projection and family integration_
  - _Depends: 8.2_
  - _Validation: family/parity suites; consumer compilation; dev-contract-check and ABI check_
  - _Requirements: 3.3, 4.1, 4.2, 4.3, 4.4, 5.1_

- [x] 10. Connect actual connector conversion to executor usage selection
  - Extend the existing adapter-package usage test where the private real host
    converter is accessible; do not export conversion solely for tests.
  - Compose a small real executor with its existing exported test constructor
    and a deterministic backend carrying the serialized/decoded connector event.
    Do not import private runtime test helpers or introduce a test framework.
  - Connect that executor to the exported Chat encoder with reconstruction on
    and off. Restrict integration fixes to approved conversion/selection seams.
  - Done: flags and truthful total presence survive the host hop; explicit client
    usage wins when supplied and provider fallback wins otherwise.
  - Keep four-family/mode parity in task 9 and finalizer cases in task 7; this
    task owns only the otherwise unproven connector-to-executor integration seam.
  - _Boundary: Connector-to-host-to-executor-to-client integration_
  - _Depends: 2, 3, 7, 9_
  - _Validation: focused adapter-package executor integration in both reconstruction configurations_
  - _Requirements: 2.2, 2.3, 3.1, 3.2, 5.2, 5.3_

## Completion Status

- [x] All tasks checked. Delivered in #1324 and #1325 (task notes carry the per-task evidence).
- [x] Verified on main `5b5c873c` (2026-10-29): full root suite `go test -tags=precommit,integration ./...` and `scripts/check-all-modules.sh` pass.
