# Design Validation Review

## Review Method

The design was validated against:

- `.kiro/AGENTS.md`;
- product, API, routing/orchestration, structure, technology, and testing steering;
- ADR 0007;
- current `internal/core/toolcallrepair` and runtime assembler code;
- all current repair fixtures, tests, fuzz targets, and benchmarks;
- the reviewed DeepSeek-Reasonix history sanitizer and agent recovery path;
- every acceptance criterion in `requirements.md`;
- brownfield gaps in `gap-analysis.md`.

The review used three rounds. Each round could return NO-GO. Findings were incorporated before the next round.

## Round 1

### Assessment

**Decision: NO-GO**

The first draft copied too much of Reasonix's permissive history behavior.

### Critical Issue 1: Arbitrary malformed input could become `{}`

**Concern:** A fallback object can be accepted by a no-argument/defaulted tool.
**Impact:** Invalid output becomes executable behavior with no evidence of model intent.
**Resolution:** Remove all fallback-document behavior. No repair plan means unrepairable.
**Traceability:** 6.7, 2.12
**Evidence:** `design.md` — Non-Goals; Failure Handling.

### Critical Issue 2: Dangling colon became unconditional `null`

**Concern:** `null` can be permitted without being intended.
**Impact:** The proxy invents a business value.
**Resolution:** Insert only an existing deterministic fill source: `const`, one-element `enum`, or `default`. Type-derived null is prohibited. An explicit `default` is a policy-selected value, not proof that alternatives are invalid.
**Traceability:** 3.3, 3.4, 3.7, 6.8
**Evidence:** `design.md` — Pending Top-Level Value Repair.

### Critical Issue 3: Trailing escape deletion changed emitted data

**Concern:** Deleting `\` changes paths, regexes, commands, and strings.
**Impact:** A syntactically repaired call may carry a different argument.
**Resolution:** Incomplete escape and Unicode escape remain unrepairable.
**Traceability:** 4.6, 6.7
**Evidence:** `design.md` — Tail Analyzer; Refusals.

### Critical Issue 4: Agent retry/schema feedback crossed the proxy boundary

**Concern:** Go-LIP does not own the model loop or tool executor.
**Impact:** The design would require fabricating tool results or modifying later prompts.
**Resolution:** Exclude transcript repair and agent retry guidance. Repair remains pre-delivery canonical finalization.
**Traceability:** 1.1, 1.2, 1.8, 1.10
**Evidence:** `design.md` — Boundary Commitments.

## Round 2

### Assessment

**Decision: NO-GO**

The unsafe Reasonix fallbacks were removed, but classifier, validation, and observability contracts remained under-specified.

### Critical Issue 1: Naive terminal-comma deletion accepted missing values

**Concern:** Removing the last comma and testing `json.Valid` can transform `{,` to `{}` or `[,]` to `[]`.
**Impact:** Missing-value syntax is erased.
**Resolution:** Track grammar expectation and accept a comma only in object/array `comma-or-end` state after a complete value.
**Traceability:** 2.1, 2.6, 2.12, 4.2
**Evidence:** `design.md` — Grammar State; Terminal-Comma Repair.

### Critical Issue 2: Nested pending-value inference was underdesigned

**Concern:** Incomplete nested paths require a second structural parser and exact schema-location tracking.
**Impact:** Duplicate keys, arrays, normalized names, refs, and branches could resolve incorrectly.
**Resolution:** Restrict V2 to an exact final top-level property. Nested paths are deferred.
**Traceability:** 3.1, 3.2, 3.8
**Evidence:** `design.md` — Pending Top-Level Value Repair.

### Critical Issue 3: Pending property normalization composed two speculative repairs

**Concern:** Resolving a misspelled/inexact pending key before valid JSON creates multiple syntax/name hypotheses.
**Impact:** Candidate uniqueness becomes harder to prove.
**Resolution:** Pending property must exactly match root `properties`. Existing name normalization remains available only after a valid candidate exists for other complete properties.
**Traceability:** 3.2, 3.8, 4.4, 4.8
**Evidence:** `design.md` — Schema Resolution.

### Critical Issue 4: The first draft changed `CompleteJSONSuffix`

**Concern:** Existing tests and direct callers depend on append-only prefix preservation.
**Impact:** A frozen V1 primitive would silently change semantics.
**Resolution:** Keep it unchanged and add a separate analyzer invoked only after it fails.
**Traceability:** 1.3, 5.2
**Evidence:** `design.md` — Interaction With V1.

### Critical Issue 5: Candidate-byte invariants conflicted with later schema repair

**Concern:** Existing schema repair may rename, insert, or remove properties after a tail candidate is built.
**Impact:** A byte-preservation claim over published `ArgsJSON` would contradict the existing engine.
**Resolution:** Apply one-byte/append-only invariants to the private syntax candidate. The published result may differ further only through the existing bounded deterministic schema-repair pipeline; no second speculative syntax edit is allowed.
**Traceability:** 2.5, 2.10, 3.9, 3.12
**Evidence:** `design.md` — Candidate versus Published Result.

### Critical Issue 6: Empty-schema syntax-only behavior contradicted unconditional compiled validation

**Concern:** The current engine returns `syntaxOnlyOutcome` for an empty schema before schema compilation.
**Impact:** A requirement that every rewrite use compiled validation would silently change established behavior.
**Resolution:** Non-empty-schema rewrites require compiled post-validation. Empty-schema append-only/comma syntax-only rewrites require valid JSON and strict shape preflight. Pending-value repair always requires a non-empty compilable schema.
**Traceability:** 2.9, 5.4, 5.8
**Evidence:** `design.md` — Empty Schema.

### Critical Issue 7: Combined repairs lacked reason-code precedence

**Concern:** The public result carries one reason code, while a tail repair may be followed by existing schema repair.
**Impact:** Fixture expectations and diagnostics would be unstable.
**Resolution:** Existing no-tail V1 reasons remain unchanged. Append-only/comma paths publish `syntax_repaired`; pending paths publish the selected fill reason, even when later schema repair occurs.
**Traceability:** 1.5, 2.11, 3.11, 3.12, 8.5
**Evidence:** `design.md` — Primary Reason Matrix.

## Round 3

### Traceability Review

- 94 acceptance criteria were enumerated.
- Every criterion maps to at least one implementation task.
- Every task names requirements, design rules, boundary, dependencies, and validation.
- Existing V1 behavior is characterized before implementation.
- Positive and refusal cases are both executable requirements.
- Reasonix-only agent/history behavior is explicitly excluded.
- No public/canonical contract is widened.

### Architecture Review

- Core ownership is preserved.
- No adapter imports the engine.
- Streaming remains the primary path.
- The existing finalizer and assembler remain the only lifecycle rewrite seam.
- No new route, retry, B2BUA, capability, or session semantics are introduced.
- The schema compiler/cache remains the single schema authority.
- `CompleteJSONSuffix` remains a stable append-only primitive.

### Safety Review

- Exactly one comma can be deleted in a private syntax candidate.
- Comma deletion requires a complete preceding value.
- Pending fill is exact, top-level, final, and selected by existing deterministic-fill policy.
- No partial literals, escapes, fallback documents, scalar coercion, or type-derived null.
- Duplicate-key inputs may be provisionally classified but are rejected by shape preflight before materialization or publication.
- Non-empty-schema results are compiled-schema post-validated; empty-schema syntax-only results retain valid-JSON and shape-preflight safeguards.
- fail-open returns exact originals.
- diagnostics do not contain arguments, inserted values, or schema bodies.

### Performance Review

- Valid JSON does not invoke the new classifier.
- Existing append-only repairs succeed before the new classifier.
- New work is one bounded linear scan with no regex or backtracking.
- Pending-value schema compile reuses the existing cache and compiled handle.
- Same-host repeated `benchstat` evidence must show no statistically significant valid-pass-through regression greater than 5% in time/op and no increase in allocs/op.
- Near-limit elapsed time is recorded as evidence; byte/depth/operation bounds remain the gate.

### Testing Review

- RED characterization precedes implementation.
- Fixtures cover each positive class and unsafe near-neighbor.
- Fuzz properties apply byte-delta/append-only invariants to private syntax candidates and schema-validity invariants to published rewrites.
- Runtime tests cover exact replay and lifecycle isolation.
- Conformance remains canonical and protocol-agnostic.
- Linux race evidence is required before completion.

## Design Strengths

- Adopts Reasonix's useful terminal coverage without importing its history-replay risk profile.
- Reuses Go-LIP's stronger deterministic schema machinery.
- Makes the one deletion exception precise and testable.
- Limits the pending-value scope to the common top-level tool-argument case.
- Adds no configuration or public API surface.
- Preserves exact rollback and a single repair pipeline.

## Final Assessment

**Decision: GO FOR DESIGN READINESS**

No critical design gap remains. Implementation verification has not occurred and all implementation tasks remain pending.

## Implementation Gate

Implementation shall begin only after maintainers set `approvals.requirements.approved`, `approvals.design.approved`, and `approvals.tasks.approved` to `true` and set `ready_for_implementation` to `true` in `spec.json`.

The first implementation phase must add failing characterization and refusal tests. No production behavior may change before those contracts and the implementation plan are reviewed.
