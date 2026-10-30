# Requirements Document

## Introduction

Go-LIP already repairs native structured tool calls at the canonical event layer after backend translation and before frontend encoding. The existing V1 engine is intentionally conservative: it can append the minimal suffix needed to close unterminated strings and containers, normalize uniquely matching tool/property names, insert schema-determined values, remove forbidden additional properties, and post-validate every rewrite against the effective tool schema.

That design leaves two common terminal truncation shapes unhandled:

1. a complete final object member or array element followed by a trailing comma; and
2. an explicit final top-level object property whose value was cut off immediately after the colon even though the existing deterministic-fill policy can select a bounded schema value.

DeepSeek-Reasonix demonstrates that broader JSON-tail handling can reduce model retry loops, but its history sanitizer also performs transformations that are not safe for a live proxy: arbitrary malformed input can become `{}`, a dangling colon can become `null`, and a trailing escape can be deleted. This specification adopts only the high-value transformations that can remain deterministic, bounded, and schema-validated.

The existing `tool-call-repair` feature remains the owner. No frontend, backend, provider, or agent-specific repair branch is introduced.

## Boundary Context

- **In scope**: native structured tool-call argument JSON; terminal-comma recognition; exact final top-level pending-value recognition; reuse of schema `const`, single-valued `enum`, and `default`; mandatory post-validation; cancellation, shape limits, diagnostics, fixtures, fuzzing, benchmarks, conformance, dogfood, and ADR updates.
- **Out of scope**: arbitrary malformed JSON recovery; fallback to `{}`; unconditional `null`; deletion or reinterpretation of string escapes; partial literals; scalar coercion; fuzzy/edit-distance names; nested pending-value path inference; transcript/tool-result pairing; agent retry prompts; auxiliary LLM repair calls; text/XML/Markdown tool-call inference; provider-local repair.
- **Boundary ownership**: `internal/core/toolcallrepair` plus existing runtime finalizer/assembler integration and the YAML-only standard feature package.
- **Canonical/public impact**: no new canonical request/event concepts and no new public finalizer action. Existing public reason codes are reused.
- **Revalidation triggers**: completed-call finalizer ordering; exact-original pass/fail-open replay; tool schema compilation; JSON shape limits; duplicate-member rejection; canonical tool lifecycle synthesis; frontend/backend tools matrix.

## Requirements

### Requirement 1: Preserve Conservative V1 Compatibility

**Objective:** As an operator, I want the repair expansion to preserve existing safe behavior and rollback semantics, so enabling the standard feature does not weaken current guarantees.

#### Acceptance Criteria

1.1 The implementation shall keep native structured tool calls as the only repair input.
1.2 The implementation shall retain the canonical completed-call finalizer location after backend translation and before frontend encoding.
1.3 The implementation shall keep `CompleteJSONSuffix` append-only and behavior-compatible for all existing callers and tests.
1.4 Valid arguments that are schema-valid shall replay the exact original buffered lifecycle bytes and sequence.
1.5 Existing V1 syntax, tool-name, property-name, deterministic-fill, and forbidden-additional-property repairs shall retain their current outcomes.
1.6 An unrepairable call shall retain the existing configurable fail-open pass-through or fail-closed reject policy.
1.7 Finalizer panic, finalizer error, overflow, cancellation cleanup, EOF cleanup, replacement, and duplicate lifecycle behavior shall retain exact-original safety.
1.8 No frontend or backend protocol adapter shall import or call the repair engine.
1.9 The standard feature shall remain enabled by standard-distribution injection and removable through the existing explicit opt-out.
1.10 The enhancement shall not require a new feature mode, provider switch, auxiliary model call, or network access.

### Requirement 2: Repair a Terminal Comma Only After a Complete Value

**Objective:** As an agent client, I want an otherwise complete tool argument cut immediately after a separator comma to execute without another model turn.

#### Acceptance Criteria

2.1 When the final non-whitespace byte is a comma outside a string and the comma follows one complete JSON value, the engine shall be able to remove exactly that comma and append only the required closing suffix.
2.2 The repair shall support a complete final object member.
2.3 The repair shall support a complete final array element.
2.4 The repair shall support nested open containers when the terminal comma itself belongs to a valid active object or array frame.
2.5 The private terminal-comma syntax candidate shall preserve every original byte except the one terminal comma and shall preserve original whitespace; a later published rewrite may differ further only through the existing deterministic schema-repair pipeline.
2.6 The repair shall reject a comma following an opening object/array delimiter, colon, or another comma.
2.7 The repair shall reject a comma inside a string, after a partial scalar token, after mismatched delimiters, or before non-whitespace trailing bytes.
2.8 The repair candidate shall pass argument shape preflight before materialization and schema repair.
2.9 With a non-empty effective schema, the final candidate shall validate against that compiled schema before rewrite; with an empty schema, syntax-only comma repair shall retain the existing JSON-validity and shape-preflight guarantees.
2.10 If the candidate does not validate and cannot be repaired by existing deterministic schema rules, the engine shall return unrepairable without emitting a partial candidate; no second speculative syntax edit is permitted.
2.11 A successful append-only or terminal-comma path shall publish `syntax_repaired` as the primary reason, including when a later bounded schema repair is also required.
2.12 Inputs such as `{,`, `[,]`, `{"a":,`, `[1,,`, `{"a":tru,`, and `{"a":1,}x` shall remain unrepairable.

### Requirement 3: Repair an Explicit Final Top-Level Pending Value Only From the Schema

**Objective:** As an agent client, I want a tool call cut immediately after a known top-level property colon to be completed when the existing deterministic-fill policy selects a bounded schema value.

#### Acceptance Criteria

3.1 The engine shall recognize only a root object whose final non-whitespace token is the colon of an explicit final top-level property.
3.2 The property name shall exactly match one entry in the effective root schema `properties`.
3.3 The engine shall insert a value only when the effective property schema supplies `const`, a single-valued `enum`, or an explicit `default`.
3.4 Existing deterministic-fill precedence and materialization rules shall be reused rather than duplicated.
3.5 Local `$ref` and single-branch `oneOf`, `anyOf`, or `allOf` handling may be reused only where the existing schema compiler/repair rules already consider the path deterministic.
3.6 Multi-branch selection, external references, unresolved references, conflicting constraints, or schemas requiring inference shall remain unrepairable.
3.7 The engine shall not infer `null` solely because `null` is permitted by `type`.
3.8 The engine shall not repair nested pending paths, normalized/misspelled pending property names, array positions, missing property names, or partial values in this version.
3.9 The private pending-value syntax candidate shall append the deterministically encoded value and required closing suffix without deleting or changing original bytes; a later published rewrite may differ further only through the existing deterministic schema-repair pipeline.
3.10 The candidate shall pass argument shape preflight and full effective-schema post-validation.
3.11 A successful pending-value path shall publish `const_inserted`, `enum_inserted`, or `default_inserted` as the primary reason according to the selected schema source, including when a later bounded schema repair is also required.
3.12 If another existing deterministic schema repair is needed after syntax completion, the engine may apply it only through the existing bounded repair pipeline, shall post-validate the combined result, and shall not replace the primary pending-value reason with a secondary schema-repair reason.
3.13 Inputs such as `{"mode":`, `{"recursive":`, or `{"options":` shall remain unrepairable when no deterministic schema value exists.
3.14 The engine shall never replace a partial literal such as `{"mode":"sa`, `{"count":1e`, or `{"enabled":t`.

### Requirement 4: Use a Bounded Terminal-Tail Classifier

**Objective:** As a maintainer, I want terminal defects classified without speculative parsing or search, so the feature remains predictable and resistant to hostile input.

#### Acceptance Criteria

4.1 The classifier shall perform one bounded linear scan over the argument bytes.
4.2 The classifier shall track string, escape, delimiter, container expectation, and completed-value state without regular expressions.
4.3 The classifier shall enforce the existing argument-byte limit and a fixed maximum scan depth no greater than the current JSON scanner bound.
4.4 The classifier shall return at most one repair class for an input.
4.5 The classifier shall distinguish append-only closable input, safe terminal comma, safe top-level pending value, and unrepairable input.
4.6 Invalid UTF-8, incomplete escapes, incomplete Unicode escapes, mismatched delimiters, or trailing garbage shall not receive a new repair plan; duplicate-member inputs may be provisionally classified but shall be rejected by argument preflight before ordered materialization or any published rewrite.
4.7 The classifier shall honor context cancellation during bounded work where the engine already exposes `RepairContext`.
4.8 The implementation shall not recursively enumerate candidates, backtrack over token interpretations, or combine multiple speculative syntax edits.
4.9 The classifier result shall contain only bounded structural metadata and shall not copy raw arguments into diagnostics.
4.10 Fuzzing shall assert no panic, no out-of-bounds access, bounded output, and deterministic classification for identical bytes.

### Requirement 5: Integrate Through the Existing Engine and Schema Pipeline

**Objective:** As a maintainer, I want the new repairs to reuse existing compilation, validation, cache, and policy boundaries instead of creating a second engine.

#### Acceptance Criteria

5.1 Exact tool resolution and unique normalized tool-name resolution shall remain unchanged.
5.2 The engine shall try the existing append-only completion before the new terminal-tail repairs.
5.3 Terminal-comma repair shall not require schema materialization before a syntactically valid candidate exists.
5.4 Pending-value repair shall require a compiled effective schema before value insertion.
5.5 Schema compilation shall continue to use the bounded digest LRU and offline-only reference policy.
5.6 Every generated candidate shall pass `preflightArgsJSON` before ordered parsing or schema repair.
5.7 Existing schema repair shall run only on a syntactically valid, preflighted candidate.
5.8 Every rewrite with a non-empty effective schema shall pass the existing compiled-schema validator after all syntax and schema changes; empty-schema syntax-only rewrites shall retain the existing JSON-validity and shape-preflight guarantees.
5.9 Failed candidate generation or validation shall leave the original tool name and argument bytes available for unrepairable policy mapping.
5.10 The engine shall not emit a rewrite when no semantic or syntax change occurred.
5.11 Candidate size growth shall remain within `max_args_bytes`; overflow shall map to `args_too_large`.
5.12 The finalizer and runtime assembler shall require no new action type or lifecycle event kind.

### Requirement 6: Preserve Security, Privacy, and Failure Isolation

**Objective:** As an operator, I want malformed calls contained without exposing their content or expanding denial-of-service risk.

#### Acceptance Criteria

6.1 Argument and schema payloads shall never be included in diagnostics, logs, metrics labels, or reject messages.
6.2 Errors shall continue to expose stable reason categories rather than instance values or schema bodies.
6.3 Duplicate JSON member names shall remain rejected by argument shape preflight.
6.4 Remote, filesystem, and network schema resolution shall remain prohibited.
6.5 Scalar coercion shall remain disabled.
6.6 Arbitrary deletion shall remain prohibited except for the one classified terminal comma.
6.7 The engine shall not delete a trailing escape, truncate a partial literal, or replace malformed input with `{}`.
6.8 A dangling colon shall not become `null` unless `null` is explicitly selected by `const`, a single-valued `enum`, or `default`.
6.9 Panic/error isolation in the finalizer chain shall continue to replay exact originals under fail-open behavior.
6.10 Concurrent engine/cache use shall remain race-safe.
6.11 Cancellation shall produce the existing `canceled` classification and shall not leak partially built candidates.
6.12 Adversarial tests shall cover very deep, very wide, oversized, invalid UTF-8, duplicate-key, and near-limit terminal-tail inputs.

### Requirement 7: Preserve Cross-Protocol and Streaming Semantics

**Objective:** As a client integrator, I want safe repair to work uniformly across supported tool-capable frontend/backend combinations.

#### Acceptance Criteria

7.1 The runtime shall continue buffering only tool lifecycle events for calls subject to installed finalizers.
7.2 Successful rewrites shall synthesize one started event, bounded argument delta event(s), and one finished event with the original call ID and message index.
7.3 Valid pass-through and fail-open outcomes shall replay the exact original event sequence.
7.4 Parallel or interleaved tool-call IDs shall remain isolated per B-leg stream.
7.5 A repair shall not alter first-visible-output commitment, retry eligibility, route planning, or B2BUA lineage.
7.6 Every tools-viable bundled frontend/backend conformance cell shall exercise at least one new safe-tail repair.
7.7 Unsupported or tools-inviable cells shall remain explicitly classified rather than silently omitted.
7.8 Streaming and non-streaming frontends shall observe equivalent final tool-call semantics.
7.9 Local dogfood shall include deterministic examples for terminal-comma and schema-determined pending-value repair.
7.10 Protocol adapters shall remain unaware of the repair class and receive only canonical replay or rewritten lifecycle events.

### Requirement 8: Verification, Performance, and Rollout Evidence

**Objective:** As a maintainer, I want executable evidence that the additional coverage is useful without weakening latency or safety.

#### Acceptance Criteria

8.1 Tests shall be written before behavior changes for each new repair and refusal class.
8.2 Contract fixtures shall include positive and negative terminal-comma cases.
8.3 Contract fixtures shall include positive `const`, single-enum, and default pending-value cases plus no-deterministic-value refusals.
8.4 Existing V1 fixture outcomes shall remain byte-for-byte stable unless the fixture is intentionally reclassified by this specification.
8.5 Unit tests shall cover classifier states, candidate construction, engine ordering, reason codes, and no-partial-mutation behavior.
8.6 Runtime tests shall cover finalizer chaining, fail-open, fail-closed, panic isolation, cancellation, overflow, interleaving, and exact replay.
8.7 Fuzz targets shall cover tail classification and full `Engine.RepairContext`.
8.8 Race tests shall cover shared schema cache and concurrent repair of identical and distinct calls.
8.9 Benchmarks shall separately measure valid pass-through, append-only truncation, terminal comma, deterministic pending value, and near-limit unrepairable input.
8.10 Controlled same-host repeated benchmarks analyzed with `benchstat` shall show no statistically significant valid-pass-through regression greater than 5% in time/op and no increase in allocs/op; near-limit elapsed time shall be recorded as evidence while byte, depth, and operation bounds—not an absolute wall-clock threshold—remain the pass/fail gate.
8.11 `make quality-checks`, focused unit tests, `make parity-checks`, `make test-fuzz`, and Linux race evidence shall be required before implementation completion.
8.12 ADR 0007 and operator/performance documentation shall describe the one-byte terminal-comma exception and the exact deterministic pending-value boundary.
8.13 No default-on claim shall be made for any broader Reasonix-style repair not specified here.
8.14 Disabling the existing `tool-call-repair` feature shall remain a complete rollback.
