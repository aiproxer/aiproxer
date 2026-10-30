# Implementation Plan

## Execution and Delivery Context
- Seven bounded leaf tasks, each estimated at 1–3 hours and carrying its own RED→GREEN tests. All tasks have passed independent review after review-driven repairs; whole-spec review supports all 21 criteria and local integration gates pass. Remote CI/race certification and delivery remain separate obligations.
- Rebaseline shared source and the adjacent lifecycle/financial/performance work before editing. Use the design file plan; no public contract, new registry, worker, schema, or paid-wire activation.
- Implementation was explicitly authorized by the user. The implementation base is `fa4bd2d5`, including the archived lifecycle correction; its presence does not replace the executed HTTP replacement proof. Delivery slicing and independent acceptance remain required.
- Existing harnesses/configuration suffice; no foundation scaffolding task is needed. Tasks 1/2/3 form the independent wave: proof-profile files, OpenAI handler/encoder files, and composition/stamp tests are separate. Test ownership is assigned below; shared harness edits are serialized before dispatch or remove the parallel marker. No shared scratch mutation.
- Default sequencing after that wave is 4 → 5 → 6 → 7. Task 7 additionally requires the adjacent lifecycle replay-handoff repair; it is not satisfied by the existence/approval of that spec. Coordinate the shared runtime source before task 4, and do not run task 4 concurrently with lifecycle runtime edits.
- Before coding and after accepted tasks: `make dev-delivery-plan DEV_BASE=<implementation-base> DEV_CONSUMER='existing certified streaming frontends and executor'`. Refresh actual production/test/file counts; split if growth exceeds repository slice budgets rather than omitting required proof.
- Tests observe serving behavior with literal expected values and a claim/source/mutant table. Focused local tests and coherent affected-package/contract gates are required; race evidence is remote GitHub CI only. No task requires local race, a live provider, or a new database.
- The maintainer explicitly granted an exception to the approximate 2× test/production addition budget for this feature after independent review measured 3,897 test additions against 584 production additions (6.67×). This exception covers proportionality only, not source-file limits, correctness, review evidence, hooks, or CI requirements.

### Parallel Test Ownership
- Task 1 owns existing `internal/plugins/frontends/{openairesponses,openailegacy,openresponses}/profile_test.go`, `frontendpipe/proofmechanics_test.go`, `frontendpipe/candidate_assessment_test.go`, and `frontendpipe/large_payload_proof_bound_characterization_test.go` (all beneath `internal/plugins/frontends/`).
- Task 2 owns new adjacent `internal/plugins/frontends/openairesponses/response_clock_parity_test.go` and `internal/plugins/frontends/openailegacy/response_clock_parity_test.go`; reuse existing test helpers read-only, do not edit task 1 files or shared conformance harnesses during the parallel wave.
- Task 3 owns `internal/infra/runtimebundle/build_large_body_assessor_test.go`, `large_payload_host_characterization_test.go` in that package, and existing runtime/largebody stamp tests. Later runtime tasks wait for this wave; no frontend test edits in task 3.

## Tasks
- [x] 1. Keep certified metadata retention inside one before-write budget
  - First reproduce nested role/type/status/control-string bypasses and raw-capture post-write overruns at the smallest profile/bounded-writer seam. Cover exact limit, limit+1, escaped/chunk-split bytes, trimmed whitespace, and cumulative small items after buffer resets.
  - Budget all retained metadata using existing leaf mechanics, keeping streamed prompt content and the independently bounded small-content cache separate. Preserve final fact validation and semantic identity.
  - Integrate with the existing frontend fallback: one held decode admission, zero wire backend opens, and one canonical execution or established decode rejection. Do not alter source/stream cleanup ownership.
  - Done: all three profiles refuse oversize metadata before retaining the excess; valid large text still proves equivalent canonical identity and same-permit fallback remains correct. Run existing allocation ratchets and focused parser fuzz where practical; record any unexecuted tagged gate.
  - _Requirements: 1.1, 1.2, 1.3, 1.4_
  - _Boundary: Frontend certified proof integration and existing metadata budget leaf_
  - _Depends: none_
  - _Validation: go test -count=1 ./internal/plugins/frontends/frontendpipe ./internal/plugins/frontends/openairesponses ./internal/plugins/frontends/openailegacy ./internal/plugins/frontends/openresponses_

- [x] 2. (P) Apply the configured OpenAI response clock without changing identity
  - First reproduce timestamp divergence with a fixed clock through eligible streaming handlers and option-aware non-stream writers receiving selected encode options. Separately prove unchanged deterministic-only convenience-writer behavior; do not treat those helpers as owning an executor clock. Preserve nil/zero/default behavior relative to canonical encoding.
  - Select creation time once at the existing frontend option seam and reuse it throughout the selected response writer; retain direct deterministic-only helper behavior.
  - Done: Responses and Chat creation fields use literal fixed time 1700000000 in both representations; an advancing clock is sampled once per response. Identity seeds, IDs, session/cancellation carriers and framing remain unchanged; non-stream production wire assessment still declines.
  - Do not modify OpenResponses timestamp policy, proof-profile files, shared pipeline ownership, or introduce another clock source.
  - _Requirements: 5.1, 5.2, 5.3, 5.4_
  - _Boundary: OpenAI Responses and Chat response encoding_
  - _Depends: none_
  - _Validation: go test -count=1 ./internal/plugins/frontends/openairesponses ./internal/plugins/frontends/openailegacy_

- [x] 3. (P) Isolate accepted proof by runtime composition incarnation
  - First build two independent production assessor/executor compositions against the same populated or absent economic publisher; show foreign acceptance must fail before session mutation or upstream open. Independently disagree only the candidate-domain identity and prove the same rejection, so both conditions in acceptance are exercised.
  - Create one bounded fresh composition identity at construction and reuse it across existing acceptance/domain/executor projections; remove economic publication and global fallback sequence from that identity mechanism.
  - Done: foreign accepted proof is terminal with zero BeginTurn, zero provider opens and zero canonical fallback, while a pinned old composition still accepts its own proof after replacement publication. Economic refresh and failed-candidate rollback leave active/pinned identity and behavior unchanged.
  - Extend existing composition/pin/rollback fixtures; do not reserve/predict host publication numbers or widen public options/lifecycle APIs.
  - _Requirements: 2.1, 2.2, 2.3, 2.4_
  - _Boundary: Runtime composition acceptance integration and existing stamp validation_
  - _Depends: none_
  - _Validation: go test -count=1 ./internal/infra/runtimebundle ./internal/core/largebody; focused composition/stamp runtime tests; make dev-contract-check_

- [x] 4. Supply authoritative bound-session views to shared routing
  - First reproduce the fresh-session affinity defect through production proof assessment and execution for both ignore and fail-closed policies; distinguish genuine missing identity from a lost handoff.
  - Freeze complete resolved principal, scope, bound session, and workspace before routing; valid view availability must not depend solely on principal presence. Never fabricate authority from body fields.
  - Done: the created authoritative session is the observed affinity key and drives expected backend selection; genuine absence retains existing policy. Frozen views survive bare/replacement contexts and parallel attempt projection without reading new request facts.
  - Preserve secure-session preparation/classification, fresh-session eligibility, affinity policy, and single route/receive owners. Shared runtime file overlap prevents parallel execution with task 5 or lifecycle changes.
  - _Requirements: 3.1, 3.2, 3.3, 3.4_
  - _Boundary: Core admitted request facts and routing integration_
  - _Depends: 1, 2, 3_
  - _Validation: focused fresh-session affinity and receive-fact regressions; go test -count=1 ./internal/core/runtime; make dev-contract-check_

- [x] 5. Freeze trusted economic lineage and bind successful wire provider streams
  - First prove trusted store resolution once per eligible request, including empty-result absence, and successful provider binding before downstream consumption using the existing canonical and wire acquisition consumers.
  - Transfer the stamped store value and marker alongside existing billing identity; share the current successful-open binding projection rather than duplicate it or create a new admission owner.
  - Done: each successful non-nil stream receives one current B-leg binding with trusted request/call/store/sequence facts; canonical behavior is unchanged, error/nil opens are not rebound, and replacements/parallel legs share frozen request lineage but have distinct attempt identities.
  - Keep exposure admission and unsupported-feature blockers intact; no provider interpretation, funding, rating, journal, or terminal-policy change. Evidence forwarding/terminal integration completes in tasks 6/7, not by changing this task's binder oracle.
  - _Requirements: 4.1, 4.2, 4.4, 4.5_
  - _Boundary: Core economic fact stamping and shared successful acquisition_
  - _Depends: 4_
  - _Validation: focused store/binding and existing canonical terminal-evidence tests; go test -count=1 ./internal/core/runtime; make dev-contract-check_

- [x] 6. Preserve optional provider evidence through local wire adapters
  - First reproduce native/rich/legacy evidence hiding through the actual two-adapter chain; include enabled, explicitly disabled, no-source and native-only streams so newly exposed rich methods cannot shadow the native drain.
  - Delegate existing evidence capabilities without new storage or drain ownership. Keep rich coverage/reason records unchanged; lift native observations only when no rich source exists. Preserve explicit accounting state and established absent-evidence behavior; missing sidebands never trigger canonical re-execution.
  - Done: the two-adapter chain forwards each provider-owned batch once, retains rich partial/unsupported coverage, and preserves disabled/no-source behavior without hiding native observations or legacy evidence. Close/Cancel and queues remain owned by existing components.
  - Keep focused regression tests at the local adapter boundary; do not add provider-family policy or change shared consumer precedence to compensate for an incorrect adapter.
  - _Requirements: 4.3, 4.4, 4.5_
  - _Boundary: Core wire evidence adapter delegation_
  - _Depends: 5_
  - _Validation: focused adapter rich/native/legacy/accounting-state tests; go test -count=1 ./internal/core/runtime; make dev-contract-check_

- [x] 7. Integrate trusted wire evidence with serving acquisition and receive replacement
  - Integrate the bound provider stream and capability-preserving adapter chain into the actual successful-open path and existing live/checkpoint/terminal consumers, repairing any missing call-site projection without another owner. Extend the existing provider-parser/serving fixtures as part of this wiring, not a new test framework.
  - Done: a real Responses provider parser and existing terminal sink receive literal native quantities 10 input/2 output/12 total with one trusted per-attempt attribution; first/pre-read and late evidence reach existing consumers once. Cancellation, parallel losers and permitted pre-output replacement keep sibling attribution separate; rich partial/unsupported coverage remains unchanged.
  - Include the production HTTP frontend → assessor/executor → receive-replacement sentinel only after the lifecycle replay-handoff fix is available. Prove fresh replay replacement before content, zero canonical re-execution, and no replacement after content. A backend-Open-only retry or an unexecuted HTTP sentinel does not satisfy done.
  - Run coherent complete affected package suites, existing architecture/contract and applicable allocation gates while integrating the delegation change. Preserve Close/Cancel/terminal owners; no live charging or scale claim follows.
  - _Requirements: 1.3, 2.3, 3.4, 4.3, 4.4, 4.5_
  - _Boundary: Core wire evidence adapters and explicit frontend/runtime/provider-parser serving integration_
  - _Depends: 6, large-payload-lifecycle-correctness task 1 replay/source handoff repair integrated into the implementation base_
  - _Validation: go test -count=1 ./internal/core/runtime ./internal/infra/runtimebundle ./internal/core/largebody ./internal/plugins/frontends/frontendpipe ./internal/plugins/frontends/openairesponses ./internal/plugins/frontends/openailegacy ./internal/plugins/frontends/openresponses; make dev-contract-check; existing applicable allocation ratchets; remote CI race evidence_

## Task Plan Review
All 21 criteria map to tasks; integration is part of implementation tasks, never a standalone test task. The shared runtime tasks remain ordered, and the external lifecycle prerequisite is explicit. Independent proof, clock, and composition work is isolated by file responsibility; all task completion conditions observe behavior, not helper existence. Independent task-graph review passed after one repair/recheck cycle.

## Completion Status
All seven tasks are `[x]` and merged on `main`.

- [x] Task 1 — certified metadata retention inside one before-write budget. Merged in #1148 (`40dd1bdc`).
- [x] Task 2 — configured OpenAI response clock without changing identity. Merged in #1148.
- [x] Task 3 — accepted proof isolated by runtime composition incarnation. Merged in #1148.
- [x] Task 4 — authoritative bound-session views supplied to shared routing. Merged in #1148.
- [x] Task 5 — trusted economic lineage frozen and successful wire provider streams bound. Merged in #1148.
- [x] Task 6 — optional provider evidence preserved through local wire adapters. Merged in #1148.
- [x] Task 7 — trusted wire evidence integrated with serving acquisition and receive replacement, including the production HTTP frontend → assessor/executor → receive-replacement sentinel after the lifecycle replay-handoff repair. Merged in #1148.
- [x] Requirements 1.1–5.4 implemented; each criterion proven by a test whose failure mode is tied to the behavior.
- [x] Corrective round merged: `a0c912cb` closed independent proof and parity review findings on the same branch before squash-merge.

Merged baseline: `40dd1bdcbad55ea8e4a6a46cc54b4c6860468b72` (#1148). PR checks green at merge. Race evidence remains remote-only; no live charging or scale claim follows. Deferred workstreams in `requirements.md` remain deferred.
