# Implementation Plan

- [x] 1. Establish interleaved state, config, and memo foundations
- [x] 1.1 Define shared interleaved route state values
  - Add role, cycle state, cycle entry, and memo reference value semantics used by routing, continuity, and runtime.
  - Include validation for empty state, stale selector state, cursor bounds, and JSON round-trip behavior.
  - Done when shared values can be created, validated, serialized, and compared without importing runtime or memo-processing packages.
  - _Requirements: 2.2, 2.3, 8.1, 8.4, 8.5_
  - _Boundary: Core shared value model_
  - _Validation: go test ./internal/core/interleavedstate_

- [x] 1.2 Add interleaved thinking configuration defaults and validation
  - Add disabled-by-default settings for instructions source, visibility mode, regular-turn budget, and memo size limits.
  - Reject invalid enabled settings before runtime construction can serve traffic.
  - Done when config tests show valid defaults, enabled valid config, and fail-closed invalid config.
  - _Requirements: 3.3, 3.5, 4.6, 10.1, 10.2, 10.5, 9.5_
  - _Boundary: Core config_
  - _Validation: go test ./internal/core/config_

- [x] 1.3 Add bounded memo state and memo store behavior
  - Add memo body metadata, budget counters, visibility flags, interrupted marker, and bounded storage keyed by memo reference.
  - Enforce memo size and scope isolation in the store contract.
  - Done when memo store tests prove put, get, update, delete, budget update, scope isolation, and size limit behavior.
  - _Requirements: 4.5, 4.6, 5.3, 5.4, 8.1, 8.2, 8.3, 11.3_
  - _Boundary: Interleaved memo state_
  - _Validation: go test ./internal/core/interleavedthinking_

- [x] 2. Add thinker selector grammar and planner behavior
- [x] 2.1 Parse and validate thinker annotations
  - Accept bare and true-valued thinker forms on weighted branches.
  - Reject false, empty, duplicate, misplaced, and first-plus-thinker forms with selector validation errors.
  - Done when parser tests cover every accepted and rejected annotation form.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7_
  - _Boundary: Core routing parser_
  - _Validation: go test ./internal/core/routing_

- [x] 2.2 Add the narrow thinker plus parallel executor hybrid selector form
  - Accept one thinker branch plus one non-thinker weighted branch whose target is a parallel executor group.
  - Reject multiple thinker branches, multiple embedded executor expressions, malformed placement, and thinker annotations inside the embedded parallel group.
  - Done when hybrid selector tests prove accepted shape and rejected invalid shapes without relaxing general weighted/parallel mixing.
  - _Requirements: 7.1, 7.4, 7.5_
  - _Boundary: Core routing parser_
  - _Validation: go test ./internal/core/routing_

- [x] 2.3 Implement thinker-aware weighted cycle selection
  - Build cycle entries by repeating non-thinker branches by effective weight and appending the thinker branch once.
  - Honor first-request steering before cycle advancement when no valid cycle state exists.
  - Suppress thinker candidates during continuation and return deterministic no-eligible-route outcomes when no executor candidate remains.
  - Done when planner tests prove cycle advancement, stale reset, first interaction, suppression, and no-eligible behavior.
  - _Requirements: 2.1, 2.3, 2.4, 2.5, 2.6, 7.2, 7.3, 7.6, 8.4, 8.5_
  - _Boundary: Core routing planner_
  - _Validation: go test ./internal/core/routing_

- [x] 2.4 Preserve existing routing behavior and scenario coverage
  - Add fuzz seeds and scenario registrations for thinker parsing and planning.
  - Extend regression coverage to prove selectors without thinker keep existing weighted, failover, parallel, first, health, and context-size behavior.
  - Done when existing routing tests and new non-interference tests pass together.
  - _Requirements: 10.6, 11.1, 11.5_
  - _Boundary: Core routing tests_
  - _Validation: go test ./internal/core/routing_

- [x] 3. Persist cycle state and memo references through continuity
- [x] 3.1 Extend in-memory A-leg continuity for thinker cycle and memo references
  - Store and fetch cycle state and memo reference with the A-leg record.
  - Preserve zero-value behavior for routes without thinker.
  - Done when memory store tests round-trip cycle/reference state and show empty state is harmless.
  - _Requirements: 2.2, 8.1, 8.4, 8.5, 11.3_
  - _Boundary: B2BUA memory store_
  - _Validation: go test ./internal/core/b2bua_

- [x] 3.2 Add durable continuity round-trip support for interleaved references
  - Persist small cycle/reference state in SQLite and bun-backed stores using bounded serialized state.
  - Keep durable schema changes explicit and backward-compatible for empty state.
  - Done when durable store tests round-trip cycle/reference state and existing continuity tests still pass.
  - _Requirements: 8.1, 8.2, 11.3_
  - _Boundary: Continuity stores_
  - _Validation: go test ./internal/core/continuity/...

- [x] 3.3 Preserve secure-session authority for interleaved state access
  - Ensure authorized turns can access cycle and memo reference state at the store/authority boundary, while denied turns cannot apply stored state.
  - Keep missing interleaved state equivalent to a new session for cycle purposes.
  - Done when secure-session and B2BUA tests show authority-gated state access; full runtime resume behavior is covered later by composed tests.
  - _Requirements: 8.2, 8.3, 8.4, 11.3_
  - _Boundary: Secure-session integration_
  - _Validation: go test ./internal/core/securesession/... ./internal/core/b2bua_

- [x] 4. Build canonical memo processing and call shaping
- [x] 4.1 Extract bounded thinker memos from canonical events
  - Capture complete memo wrapper blocks from text and reasoning deltas.
  - Produce bounded fallback memo content when wrapper blocks are absent or incomplete.
  - Preserve interrupted-stream metadata.
  - Done when memo tests cover block extraction, fallback, partial streams, limits, and interrupted state.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.6, 9.2, 9.5_
  - _Boundary: Memo Processor_
  - _Validation: go test ./internal/core/interleavedthinking_

- [x] 4.2 Sanitize visible thinker output
  - Strip memo wrapper tags from visible output.
  - Normalize visible thinker content as canonical reasoning deltas without provider-specific fields.
  - Done when sanitization tests show wrapper tags never appear as ordinary assistant content.
  - _Requirements: 6.2, 6.3, 9.1, 9.2, 9.4_
  - _Boundary: Memo Processor_
  - _Validation: go test ./internal/core/interleavedthinking_

- [x] 4.3 (P) Shape thinker requests before backend negotiation
  - Add configured thinker instructions to thinker candidate calls.
  - Suppress tools and tool-choice directives before capability checks and backend open.
  - Leave non-thinker calls unchanged.
  - Done when call shaping tests prove thinker calls validate without tools and executor calls are not mutated.
  - _Depends: 2.3_
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 9.3_
  - _Boundary: Call Shaper_
  - _Validation: go test ./internal/core/interleavedthinking_

- [x] 4.4 Inject memo context into executor requests
  - Inject valid memo state as planning context for executor candidates.
  - Decrement regular-turn budget only after injection, skip expired memo, and avoid duplicate equivalent memo content.
  - Suppress duplicate continuation injection when the memo was already visible to the client.
  - Done when call shaping tests prove injection, visible suppression, budget decrement, expiry, and dedupe behavior.
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 10.4_
  - _Boundary: Call Shaper_
  - _Validation: go test ./internal/core/interleavedthinking_

- [x] 5. Wire candidate-specific shaping and hidden continuation runtime
- [x] 5.1 Apply interleaved call shaping in the attempt-open path
  - Shape thinker and executor calls after route selection and before capability negotiation.
  - Persist cycle/reference updates at the same points that route selection and memo injection become authoritative.
  - Done when attempt-open tests show shaped calls are the ones used for negotiation and backend open.
  - _Depends: 2.3, 3.1, 4.3, 4.4_
  - _Requirements: 3.1, 3.2, 3.4, 5.1, 9.3_
  - _Boundary: Runtime attempt opening_
  - _Validation: go test ./internal/core/runtime_

- [x] 5.2 Implement hidden thinker continuation stream
  - Drain thinker events without surfacing them, capture and store memo state, then re-plan with thinker suppression and open an executor continuation.
  - Preserve one logical A-leg and record both B-leg attempts in lineage.
  - Done when runtime tests show hidden mode emits only executor output and stores the thinker memo.
  - _Depends: 5.1_
  - _Requirements: 4.3, 6.1, 6.4, 10.3_
  - _Boundary: Interleaved Stream Coordinator_
  - _Validation: go test ./internal/core/runtime_

- [x] 5.3 Handle hidden-mode recovery, no-eligible, and cancellation paths
  - Apply existing pre-output recovery to thinker failures before visible output.
  - Surface no-eligible executor outcomes when suppression leaves no executor candidate.
  - Forward client cancellation and stream close to the active thinker or executor stream.
  - Done when runtime tests cover thinker pre-output failure, no eligible continuation, and cancellation cleanup.
  - _Depends: 5.2_
  - _Requirements: 2.6, 6.5, 6.7_
  - _Boundary: Interleaved Stream Coordinator_
  - _Validation: go test ./internal/core/runtime_

- [x] 6. Add visible continuation and hybrid executor behavior
- [x] 6.1 Implement visible thinker continuation stream
  - Surface sanitized thinker reasoning deltas before executor output.
  - Treat surfaced thinker deltas as client-visible output for recovery decisions.
  - Done when visible-mode runtime tests show sanitized thinker output followed by executor output and no silent restart after visible output.
  - _Depends: 5.2_
  - _Requirements: 6.2, 6.3, 6.6, 9.1, 9.4_
  - _Boundary: Interleaved Stream Coordinator_
  - _Validation: go test ./internal/core/runtime_

- [x] 6.2 Execute hybrid parallel executor branches
  - When the selected weighted executor expression is parallel, run the existing parallel race unchanged.
  - Preserve winner selection, loser cancellation, lineage, and output commitment rules.
  - Done when runtime tests show hybrid thinker and hybrid parallel-executor paths both complete correctly.
  - _Depends: 2.2, 2.3, 5.2_
  - _Requirements: 7.2, 7.3, 7.6_
  - _Boundary: Runtime parallel integration_
  - _Validation: go test ./internal/core/runtime ./internal/core/routing_

- [x] 6.3 Validate frontend-visible reasoning legality
  - Add representative frontend encoder tests for visible thinker reasoning deltas.
  - Verify protocols either emit legal visible thinker output or fail with deterministic configuration/capability errors.
  - Done when visible-mode canonical streams encode legally across bundled frontend families covered by tests.
  - _Depends: 6.1_
  - _Requirements: 9.1, 9.4_
  - _Boundary: Frontend compatibility tests_
  - _Validation: go test ./internal/plugins/frontends/...

- [x] 7. Wire operator configuration, diagnostics, and scenario evidence
- [x] 7.1 Wire interleaved config through runtime construction
  - Pass validated interleaved settings into executor construction from runtime bundle config.
  - Keep behavior inert when disabled or when selectors do not contain thinker.
  - Done when runtime wiring tests show enabled config reaches the executor and disabled config preserves existing behavior.
  - _Requirements: 3.5, 10.1, 10.2, 10.5, 11.2_
  - _Boundary: Config and runtimebundle wiring_
  - _Validation: go test ./internal/core/config ./internal/infra/runtimebundle_

- [x] 7.2 Add bounded diagnostics for interleaved state transitions
  - Emit route role, phase transition, memo captured, memo injected, memo skipped, memo expired, and thinker suppressed diagnostics.
  - Exclude raw prompt text and memo content from logs and high-cardinality metrics.
  - Done when diagnostics tests or assertions prove state transitions are observable without memo body leakage.
  - _Depends: 5.2, 6.1_
  - _Requirements: 10.3, 10.4_
  - _Boundary: Runtime diagnostics_
  - _Validation: go test ./internal/core/runtime ./internal/core/diag/...

- [x] 7.3 Register spec-bundle evidence and migration notes
  - Register test-backed thinker scenarios for parser, planner, hidden continuation, visible continuation, hybrid routing, and state persistence.
  - Update migration notes alongside scenario references so operator-visible behavior matches the executable evidence.
  - Done when precommit scenario tests pass and migration notes describe supported selector forms, modes, memo behavior, continuation behavior, and Python differences.
  - _Requirements: 10.6, 11.5_
  - _Boundary: Spec bundle evidence and migration notes_
  - _Validation: go test -tags=precommit ./internal/core/routing ./internal/core/runtime_

- [x] 8. Complete cross-boundary validation and quality gates
- [x] 8.1 Validate hidden interleaved thinking end to end
  - Exercise selector parse, thinker selection, hidden memo capture, stored state, continuation executor selection, and final stream output in one composed runtime test.
  - Done when the composed test fails if any phase is skipped or state is not persisted.
  - _Depends: 7.1_
  - _Requirements: 2.1, 2.2, 3.1, 3.2, 4.1, 4.3, 5.1, 6.1, 8.1, 10.3_
  - _Boundary: Runtime integration tests_
  - _Validation: go test ./internal/core/runtime_

- [x] 8.2 Validate visible interleaved thinking end to end
  - Exercise visible thinker output, memo sanitization, memo storage, continuation, and final stream termination in one composed runtime test.
  - Done when the composed test proves wrapper tags are not surfaced and executor output follows thinker reasoning.
  - _Depends: 7.1_
  - _Requirements: 4.4, 5.2, 6.2, 6.3, 6.6, 9.1, 9.2, 9.4_
  - _Boundary: Runtime integration tests_
  - _Validation: go test ./internal/core/runtime_

- [x] 8.3 Validate session isolation and stale selector behavior
  - Prove memo state is not applied across unrelated sessions or denied resumes.
  - Prove stale cycle state resets without corrupting memo state.
  - Done when composed tests cover authorized resume, denied resume, unrelated session isolation, and selector-change reset.
  - _Depends: 7.1_
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5, 11.3_
  - _Boundary: Secure-session and runtime integration tests_
  - _Validation: go test ./internal/core/runtime ./internal/core/securesession/...

- [x] 8.4 Validate extension ordering and non-interference
  - Run existing request transforms, hooks, weighted routes, failover routes, parallel routes, and first-request steering alongside interleaved thinking coverage.
  - Done when regression tests prove selectors without thinker and disabled interleaved config behave exactly as before.
  - _Depends: 7.1_
  - _Requirements: 10.2, 11.1, 11.2, 11.4, 11.5_
  - _Boundary: Regression tests_
  - _Validation: go test ./internal/core/routing ./internal/core/runtime ./internal/core/extensions_

- [x] 8.5 Run final quality and parity validation
  - Run focused package tests for routing, continuity, runtime, config, frontends, and interleaved helpers.
  - Run repository quality checks and parity checks after focused tests pass.
  - Done when focused tests, quality checks, and parity checks complete without failures or skipped required coverage.
  - _Depends: 8.1, 8.2, 8.3, 8.4_
  - _Requirements: 9.5, 11.5_
  - _Boundary: Validation_
  - _Validation: make quality-checks && make parity-checks_
