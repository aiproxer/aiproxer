# Implementation Plan

## 1. Freeze V1 compatibility and add RED safe-tail contracts

- [x] 1.1 Add fixture-level terminal-comma contracts
  - Add positive object, array, nested-container, string-punctuation, literal, number, and whitespace cases.
  - Add explicit refusals for `{,`, `[,]`, `[1,,`, `{"a":,`, partial literals, mismatched closes, comma-in-string, and trailing garbage.
  - Preserve all existing fixture outputs and exact pass-through expectations.
  - Observable completion: fixture tests fail only for the new positive cases and pass for all new refusal cases.
  - _Requirements: 1.3, 1.4, 1.5, 2.1–2.12, 8.1, 8.2, 8.4_
  - _Design rules: D2, D4, D7, D13, D15_
  - _Boundary: core repair contract fixtures/tests_
  - _Depends: none_
  - _Validation: go test ./internal/core/toolcallrepair/..._

- [x] 1.2 Add fixture-level pending-value contracts
  - Add positive exact top-level `const`, single-enum, and default cases, including explicit null supplied by those keywords.
  - Add refusals for absent deterministic value, nested path, normalized/misspelled key, partial scalar/string, ambiguous branch, external ref, and missing schema.
  - Assert correct existing reason codes and final schema validity.
  - Observable completion: current engine fails the positive cases without changing unsafe refusals.
  - _Requirements: 3.1–3.14, 6.4, 6.5, 6.8, 8.1, 8.3, 8.4_
  - _Design rules: D5, D6, D7, D9, D15_
  - _Boundary: core repair contract fixtures/tests_
  - _Depends: none_
  - _Validation: go test ./internal/core/toolcallrepair/..._

- [x] 1.3 Add RED tail-classifier tables and invariant helpers
  - Freeze expectation-state transitions, completed-scalar recognition, final non-whitespace handling, closers, maximum depth, invalid UTF-8, escapes, and cancellation.
  - Assert at most one class, explicit refusal of incomplete numbers/literals, and no raw payload in structured errors.
  - Keep direct `CompleteJSONSuffix` append-only tests unchanged.
  - Observable completion: the new classifier contract is executable before implementation.
  - _Requirements: 1.3, 4.1–4.10, 6.1, 6.2, 6.11, 6.12, 8.5_
  - _Design rules: D2, D3, D10, D14, D15_
  - _Boundary: core lexical policy tests_
  - _Depends: 1.1, 1.2_
  - _Validation: go test ./internal/core/toolcallrepair/..._

- [x] 1.4 Add RED engine/runtime sequencing tests
  - Prove append-only repair runs before safe-tail classification.
  - Prove candidates never escape before shape preflight; require compiled-schema final validation for non-empty schemas and the existing JSON-validity/shape-preflight exception for empty-schema syntax-only rewrites.
  - Cover fail-open exact originals, fail-closed reject, panic/error isolation, overflow, cancellation, interleaved IDs, and lifecycle synthesis.
  - Observable completion: tests define the full finalizer behavior before production changes.
  - _Requirements: 1.1, 1.2, 1.4, 1.6–1.10, 5.1–5.12, 7.1–7.5, 7.8, 7.10, 8.6_
  - _Design rules: D1, D8–D13, D15_
  - _Boundary: core engine and runtime finalizer tests_
  - _Depends: 1.1, 1.2_
  - _Validation: go test ./internal/core/toolcallrepair/... ./internal/core/runtime/..._

## 2. Implement the bounded tail analyzer and terminal-comma repair

- [x] 2.1 Implement the private bounded JSON-tail grammar scanner
  - Track object/array expectation, strings, escapes, exact literals, JSON numbers, completed values, and open-container closers.
  - Enforce byte/depth bounds and cancellation without regex, recursion, backtracking, or unbounded copies.
  - Return exactly one structural classification or none.
  - Observable completion: task 1.3 tables pass while `CompleteJSONSuffix` behavior remains byte-for-byte unchanged.
  - _Requirements: 4.1–4.10, 6.3, 6.11, 6.12_
  - _Design rules: D2, D3, D7, D10, D14_
  - _Boundary: internal/core/toolcallrepair lexical policy_
  - _Depends: 1.3_
  - _Validation: go test ./internal/core/toolcallrepair/..._

- [x] 2.2 Implement one-byte terminal-comma candidate construction
  - Require final non-whitespace comma in object/array comma-or-end state.
  - Build a private syntax candidate that removes exactly that byte, preserves all other bytes/whitespace, appends closers, enforces max size, and requires valid JSON.
  - Reject every unsafe near-neighbor without attempting a second syntax edit.
  - Observable completion: terminal-comma fixture and byte-delta tests pass.
  - _Requirements: 2.1–2.8, 2.11, 2.12, 6.6, 6.7_
  - _Design rules: D3, D4, D7, D8, D10_
  - _Boundary: internal/core/toolcallrepair syntax candidate builder_
  - _Depends: 2.1_
  - _Validation: go test ./internal/core/toolcallrepair/..._

- [x] 2.3 Integrate terminal-comma repair into `Engine.RepairContext`
  - Keep append-only completion first.
  - Route the comma candidate through strict argument preflight; use current schema validation/repair and mandatory final validation for non-empty schemas, while preserving the existing empty-schema syntax-only path.
  - Publish `syntax_repaired` as the primary reason even when a later schema repair is needed; preserve original bytes on every failure.
  - Observable completion: engine and runtime tests from 1.1/1.4 pass with no V1 regression.
  - _Requirements: 1.4–1.7, 2.8–2.11, 5.1–5.12, 6.9, 6.11_
  - _Design rules: D1, D8–D13_
  - _Boundary: core repair engine_
  - _Depends: 2.2, 1.4_
  - _Validation: go test ./internal/core/toolcallrepair/... ./internal/core/runtime/..._

- [x] 2.4 Add terminal-comma fuzz and adversarial hardening
  - Assert deterministic classification, exact one-byte deletion in the private syntax candidate, bounded suffix, valid accepted candidate, applicable final-schema validity, and no panic/leak.
  - Seed hostile deep/wide/near-limit/UTF-8/duplicate/escape/partial-token cases.
  - Observable completion: focused fuzz campaigns and hardening tests pass.
  - _Requirements: 4.10, 6.1–6.12, 8.7_
  - _Design rules: D3, D4, D7–D10, D14_
  - _Boundary: tests_
  - _Depends: 2.3_
  - _Validation: go test ./internal/core/toolcallrepair/... && go test -run=^$ -fuzz=FuzzJSONTail -fuzztime=30s ./internal/core/toolcallrepair_

## 3. Implement schema-determined top-level pending-value repair

- [x] 3.1 Extract/reuse deterministic fill behind a pending-property resolver
  - Resolve only the effective root object schema and exact `properties` entry.
  - Reuse current local-ref/single-branch and `const`/single-enum/default precedence/materialization.
  - Return no value for type-derived null, multi-branch selection, normalized key, additional-property schema, external ref, or inference.
  - Observable completion: resolver unit tests prove one deterministic result or refusal.
  - _Requirements: 3.2–3.8, 5.4, 5.5, 6.4, 6.8_
  - _Design rules: D5–D7, D11_
  - _Boundary: internal/core/toolcallrepair schema policy_
  - _Depends: 1.2, 2.1_
  - _Validation: go test ./internal/core/toolcallrepair/..._

- [x] 3.2 Implement append-only pending-value candidate construction
  - Accept only the final colon of an exact top-level property with no value bytes.
  - Build a private syntax candidate that appends the deterministic JSON value and required closer without deleting or changing original bytes.
  - Enforce max size, JSON validity, and strict argument shape preflight.
  - Observable completion: positive const/enum/default and append-only invariants pass.
  - _Requirements: 3.1–3.4, 3.8–3.11, 3.13, 3.14, 4.5, 4.6_
  - _Design rules: D3, D5–D8, D10_
  - _Boundary: internal/core/toolcallrepair syntax/schema candidate builder_
  - _Depends: 3.1_
  - _Validation: go test ./internal/core/toolcallrepair/..._

- [x] 3.3 Integrate pending-value repair with compiled-schema reuse
  - Compile once when the pending class requires schema and reuse the compiled schema for validation.
  - Continue through existing deterministic schema repair only after candidate preflight.
  - Implement and fixture an explicit primary-reason matrix: existing no-tail V1 repairs keep current reasons; append-only/comma paths use `syntax_repaired`; pending paths use the selected `const_inserted`, `enum_inserted`, or `default_inserted`, even when later schema repair also occurs.
  - Publish only after final validation; preserve exact originals on all failures.
  - Observable completion: `engine_contract_test.go` fixtures assert every pure and combined reason-code outcome, and pending-value engine/runtime tests pass.
  - _Requirements: 3.9–3.12, 5.4–5.11, 6.9–6.11_
  - _Design rules: D6, D8–D11, D13_
  - _Boundary: core repair engine and schema cache_
  - _Depends: 3.2, 1.4_
  - _Validation: go test ./internal/core/toolcallrepair/... ./internal/core/runtime/..._

- [x] 3.4 Add pending-value fuzz, cache, and hostile-schema tests
  - Fuzz exact top-level key extraction and append-only candidate behavior.
  - Cover invalid/unsupported/oversized schemas, local-ref cycles, multi-branch ambiguity, conflicting keywords, cache concurrency, and cancellation.
  - Assert inserted values never appear in diagnostics.
  - Observable completion: fuzz, hardening, cache, and concurrency suites pass.
  - _Requirements: 3.5–3.8, 4.7–4.10, 6.1–6.12, 8.7, 8.8_
  - _Design rules: D3, D5–D10, D14_
  - _Boundary: tests_
  - _Depends: 3.3_
  - _Validation: go test ./internal/core/toolcallrepair/... && go test -run=^$ -fuzz=FuzzPendingRootValue -fuzztime=30s ./internal/core/toolcallrepair_

## 4. Prove canonical integration, rollback, and operator clarity

- [x] 4.1 Extend the tools-capable frontend/backend conformance matrix
  - Add canonical terminal-comma and pending-value cases to every tools-viable bundled cell.
  - Record unsupported/tools-inviable cells explicitly.
  - Assert streaming/non-streaming equivalence and adapter ignorance.
  - Observable completion: parity matrix passes without frontend/backend repair imports.
  - _Requirements: 1.8, 7.1–7.8, 7.10_
  - _Design rules: D1, D12, D13_
  - _Boundary: conformance tests_
  - _Depends: 2.3, 3.3_
  - _Validation: make parity-checks_

- [x] 4.2 Extend local dogfood and standard feature regression coverage
  - Add deterministic local-stub examples for both repair classes.
  - Prove standard injection enables the behavior and existing explicit opt-out returns exact malformed originals.
  - Prove no new YAML key, mode, or default drift.
  - Observable completion: dogfood and standard-plugin tests pass in enabled and disabled configurations.
  - _Requirements: 1.9, 1.10, 7.9, 8.13, 8.14_
  - _Design rules: D11–D13_
  - _Boundary: standard distribution tests/config examples_
  - _Depends: 4.1_
  - _Validation: go test ./internal/standardplugins/... ./internal/stdhttp/... ./cmd/lipstd/..._

- [x] 4.3 Harden diagnostics and failure isolation
  - Extend reason/action diagnostics tests for both classes without raw arguments, inserted values, schema bodies, or user-controlled labels.
  - Re-run panic, finalizer error, cancellation, EOF, overflow, duplicate lifecycle, and interleaving matrices.
  - Observable completion: all failures remain typed, bounded, and exact-original safe.
  - _Requirements: 1.6, 1.7, 6.1–6.12, 7.3–7.5, 8.6_
  - _Design rules: D10, D13, D14_
  - _Boundary: core diagnostics/runtime tests_
  - _Depends: 2.3, 3.3_
  - _Validation: go test ./internal/core/toolcallrepair/... ./internal/core/runtime/..._

- [x] 4.4 Amend ADR 0007 and operator/performance documentation
  - Document the V2 safe-tail matrix, exact one-byte comma exception, deterministic pending-value boundary, reason-code reuse, refusal cases, rollback, and performance evidence procedure.
  - Explicitly contrast live repair with Reasonix history sanitation and keep broader behavior out of scope.
  - Observable completion: docs accurately match executable fixtures and no V1 append-only claim remains misleading.
  - _Requirements: 8.12–8.14_
  - _Design rules: D2, D4–D7, D11, D14_
  - _Boundary: documentation_
  - _Depends: 4.2, 4.3_
  - _Validation: make docs-check_

## 5. Certify concurrency, performance, and release readiness

- [x] 5.1 Run and harden race/concurrency coverage
  - Exercise shared cache with identical/distinct schemas, comma/pending mixes, cancellation, and concurrent B-leg streams.
  - Add no goroutines or global mutable repair state.
  - Observable completion: strict Linux race run is green or an explicit blocker is recorded without claiming success.
  - _Requirements: 6.10, 8.8, 8.11_
  - _Design rules: D3, D10, D11, D13_
  - _Boundary: concurrency tests_
  - _Depends: 4.3_
  - _Validation: make test-race_

- [x] 5.2 Publish controlled benchmark evidence
  - Benchmark valid pass-through, V1 append-only, terminal comma, pending const/default, and near-limit unrepairable cases.
  - Compare repeated baseline/final samples with `benchstat`.
  - Record allocations, bytes, cache posture, platform, and any reviewed exception.
  - Observable completion: same-host repeated `benchstat` samples show no statistically significant valid-pass-through regression greater than 5% in time/op and no increase in allocs/op; near-limit cases remain within documented byte/depth/operation bounds, with elapsed time recorded only as evidence.
  - _Requirements: 8.9, 8.10_
  - _Design rules: D3, D8–D11_
  - _Boundary: benchmarks/evidence_
  - _Depends: 2.4, 3.4_
  - _Validation: make bench_

- [x] 5.3 Run full fuzz and quality gates
  - Run registered repair fuzz smoke, focused longer tail fuzz campaigns, formatting, build, vet, architecture, unit, tagged conformance, and vulnerability/lint gates required by repository policy.
  - Verify no protocol adapter imports the engine and no new public/canonical dependency edge appears.
  - Observable completion: all mandatory gates pass at the exact implementation head or limitations are recorded truthfully.
  - _Requirements: 1.8, 4.10, 6.12, 8.7, 8.11_
  - _Design rules: D1, D3, D12, D14_
  - _Boundary: repository release gates_
  - _Depends: 4.4, 5.1, 5.2_
  - _Validation: make quality-checks && make test && make test-fuzz && make parity-checks_

- [x] 5.4 Complete traceability and spec closeout
  - Verify every acceptance criterion has implementation/test evidence.
  - Record exact commands, platform/race scope, fuzz durations, benchmark comparison, conformance cells, and known limitations.
  - Update `spec.json` only after implementation and human approvals; archive only when all tasks and release evidence are complete.
  - Observable completion: the implementation can be reviewed without inference or overstated evidence.
  - _Requirements: 8.1–8.14_
  - _Design rules: D15_
  - _Boundary: spec/release evidence_
  - _Depends: 5.3_
  - _Validation: repository Kiro spec checks and manual traceability review_
