# Implementation Plan

## Execution Contract
This plan is generated and auto-approved for specification purposes only; implementation is not authorized by the generation request. Every task is pending. Begin only after separate implementation authorization, on the dedicated task worktree rather than `main`.

- **Delivery slice**: One PR based on `55e5d4412834a1948c418ac1923cca3c25576019` or a recorded successor baseline after sibling reconciliation.
- **First consumer**: Standard-host census/summary and concrete serving authority assessment; generation substrate and consumer ship together.
- **Budget**: Four sequential leaf tasks, each approximately 1-3 hours. Tests are embedded in the task that changes behavior; no separate certification task.
- **Dependencies**: No unconditional lifecycle/proof/assessment-spec prerequisite. Reconcile already-landed sibling changes before editing shared symbols; preserve the concrete production gate and generation-token owner.
- **Preflight**: Confirm clean/owned task branch and current source/order/mask baseline, approved requirements/design, development readiness, and any shared-file edits. Use an ext4 scratch `TMPDIR` verified by `make dev-doctor`. Before coding run `make dev-delivery-plan DEV_BASE=<recorded-base> DEV_CONSUMER='standard large-body eligibility compiler and authority gate'`; refresh after each accepted task. Read `docs/development-iteration.md` for scoped gates.
- **TDD**: Record claim/source/mutant before changes. Characterize existing outcomes; observe new missing invariants RED, then GREEN. Reuse existing witnesses and keep test additions proportional. Do not implement sibling repairs to make this slice appear green.

## Tasks

- [x] 1. Derive serving plane metadata from the existing ordered declaration inventory
  - Extend existing build-time generation to emit the core-local count, ordered identifiers and default access classes, preserving the current 28 diagnostic positions, enum mapping and 32-bit capacity.
  - Replace the handwritten serving catalog definitions and seed the standard census from the emitted metadata; retain compiler validation and bounded unfamiliar-ID diagnostics.
  - Add small generator fixtures for a new declaration, class propagation, invalid membership/access, stable order, capacity boundary and overflow. Characterize current bit positions with an independent baseline fixture; exercise shuffled, missing, duplicate and unknown runtime census inputs.
  - **Done**: A valid appended synthetic declaration produces a serving catalog entry without a second handwritten eligibility entry; malformed/over-capacity input rejects; all existing census/blocker outcomes remain unchanged. The new generated artifact is produced through the emitter, not maintained by hand.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 2.1, 2.3, 2.4, 4.3, 4.5_
  - _Boundary: Explicit integration — internal build-time generator and core standard-census consumer; no public SDK change or runtime manifest access_
  - _Depends: None; preflight baseline and sibling reconciliation above_
  - _Validation: `go test ./internal/featureplanegen ./pkg/lipsdk/feature ./internal/core/largebody -count=1` with ext4 TMPDIR_

- [x] 2. Make the existing generation and currency workflow own both artifacts
  - Extend default regeneration and read-only checking to the SDK and core outputs; preserve legacy explicit SDK-output behavior and require an explicit second destination for custom two-output fixtures.
  - Validate/render all selected outputs before installation; reuse per-file atomic writing and retain nonzero failure on incomplete installation rather than promise a multi-file transaction.
  - Extend existing quality selection and generic architecture currency/path reporting to include the core artifact. Reconcile the existing report baseline through explicit update tooling only if required.
  - Include scratch CLI fixtures for invalid input leaving both targets unchanged, stale/missing core rejection, read-only checks, custom destination isolation and installation failure. Include the existing quality fake-Go selector and architecture report fixtures; wire the CLI fixture into the documented verification command for this task.
  - **Done**: Default regeneration followed by `-check` covers both files; a stale core artifact fails; explicit legacy SDK output does not touch repository core output; SDK bytes remain unchanged on the recorded manifest baseline.
  - _Requirements: 1.1, 1.2, 1.6, 4.5_
  - _Boundary: Explicit integration — existing generator command, quality selector and generic generated-currency reporting; no new bespoke architecture gate_
  - _Depends: 1_
  - _Validation: Generator `-check`; `bash scripts/test-generate-feature-planes.sh`; `bash scripts/test-quality-checks.sh`; `go test ./internal/featureplanegen ./internal/archtest -run 'PlaneGenerator|Eligibility|ManifestStatus|ExtensionPlanes' -count=1` with ext4 TMPDIR_

- [x] 3. Apply the same hook and narrow-port policy to independent defensive facts
  - Replace duplicated defensive hook/port expressions with existing pure mask helpers evaluated against the current census, not the earlier summary alone.
  - Preserve seal/pin/summary ordering, protected typed-authority weakening, custom-port unknown/blocker/name rules, all fixed port bits and the existing conditional exemptions. Correct misleading response-hook documentation without accepting response-only occupancy.
  - Embed table regressions with a clean sealed summary and independently blocking current census: four hooks, narrow-port conditions/exemptions and unavailable two-phase capability. Keep exact expected masks/reasons independent of the shared implementation. Reuse custom/protected-plane and seal/generation cases.
  - **Done**: Every existing hook/narrow-port truth-table result matches in both stages; altered current facts still decline despite a clean summary; positive exemption controls remain nonblocking; reason precedence and conservative support are unchanged.
  - _Requirements: 2.2, 2.3, 2.4, 3.1, 3.2, 3.3, 3.4, 4.5_
  - _Boundary: Core large-body serving compiler and concrete authority gate; do not revive or remove sibling-owned assessment adapters_
  - _Depends: 1, 2_
  - _Validation: `go test ./internal/core/largebody -count=1` with ext4 TMPDIR; focused runtime large-body authority tests; `make dev-contract-check`_

- [x] 4. Integrate the derived catalog with production occupancy while preserving early rejection
  - Add or extend a bounded real production-builder sentinel using frozen plane contributions and hook occupancy; verify occupied response-only policy blocks and a metadata-only positive control remains available for assessment.
  - Prove admitted metadata isolation from later public descriptor/list mutation and rejected candidate compilation with existing freeze/publication seams, restoring any test-only global mutations without parallel execution.
  - Preserve typed core-port unknown-field census behavior, paid admission/custom-billing blockers, traffic projection and secure-recorder exemption. Any production adjustment is limited to using the derived census correctly; no generation-token or execution-owner repair.
  - Attach existing static reason/zero-allocation/no-capture and stock-host/frontend witnesses to this integration change. Verify unsupported/default-off compositions never open a wire backend and body-admission limits stay unchanged.
  - **Done**: The standard serving builder consumes declaration-derived metadata and real occupancy; admitted decisions remain stable; blocker/default-off requests retain canonical selection with no capture/scanner or wire-open effects. Complete applicable delivery gates and record source-bound evidence, not a claimed performance improvement.
  - _Requirements: 1.1, 2.1, 2.2, 2.4, 2.5, 4.1, 4.2, 4.3, 4.4, 4.5_
  - _Boundary: Explicit integration — standard composition, existing runtime authority and representative frontend consumer; canonical/replay/execution ownership unchanged_
  - _Depends: 1, 2, 3_
  - _Validation: Complete generator/SDK/largebody suites; focused runtime/runtimebundle/frontendpipe selections named in research.md; generator `-check`; CLI/quality and existing architecture currency checks; `make dev-contract-check`; applicable complete delivery gates and remote CI per docs/development-iteration.md. No local race execution._

## Deferred / Out-of-Plan
The Deferred list and three sibling owners in `requirements.md`/`research.md` are not tasks in this plan. No PR publication, merge, spec archive, external issue creation or feature-completion claim is authorized here.

## Completion Status

- [x] All four implementation tasks delivered in #1178 (`1374603f85e4c13577186af9d7cb67bbbf95bdc1`); per-task commits afc535a9e (1), dd0340829 (2), 9e5374747 (3), 3dccc51b4 (4).
- [x] Post-merge repair #1191 (`82c13f5a4`) migrated the nine retired `AssessAuthorityGate` test call sites after the parallel #1179 retirement; test-only, no production behavior change.
- [x] The generation-time statements above ("Every task is pending", the `validation.md` holds) describe the spec-only phase; implementation was subsequently delivered and merged, so this bundle has no residual implementation work.
