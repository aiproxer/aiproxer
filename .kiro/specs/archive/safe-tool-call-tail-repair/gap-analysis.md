# Brownfield Gap Analysis

## Scope and Method

This review compares the requirements in `requirements.md` against Go-LIP `main` at commit `11e7a1f70116b6a86c08097aa45ab76f9de85f7b` and the current DeepSeek-Reasonix `main-v2` implementation reviewed at commit `22e18cafc0dfe27f192e8e303ffae0a0bc548eb0`.

The review follows the brownfield workflow required by `.kiro/AGENTS.md`:

1. identify current assets and ownership;
2. map every requirement area to existing code;
3. identify implementation and compatibility gaps;
4. compare viable approaches;
5. remediate unsafe or underspecified requirements before design;
6. record revalidation triggers for runtime, schema, and protocol boundaries.

No runtime code was executed during specification generation. Findings are based on static source, tests, fixtures, ADRs, and current repository steering.

## Existing Go-LIP Assets

### Canonical runtime seam

Reusable assets:

- `internal/core/runtime/tool_call_assembler.go` buffers completed native tool-call lifecycle events only when finalizers are installed;
- ordered `toolcall.Finalizer` execution with panic/error isolation;
- exact-original replay for pass, fail-open, overflow, malformed finalizer output, and duplicate lifecycle conditions;
- canonical rewrite synthesis before frontend encoding;
- per-stream/B-leg isolation.

### Repair engine

Reusable assets:

- `internal/core/toolcallrepair/engine.go` resolves exact or uniquely normalized tool names;
- `CompleteJSONSuffix` performs minimal append-only closure;
- `preflightArgsJSON` and `jsonshape` enforce bounded bytes, depth, tokens, members, UTF-8, and duplicate-name rejection;
- offline schema compilation and validation with bounded digest LRU;
- ordered JSON materialization;
- deterministic property normalization, `const`, single-valued `enum`, `default`, and `additionalProperties: false` repair;
- mandatory final schema validation;
- context cancellation and stable reason codes.

### Standard feature and verification

Reusable assets:

- default-on standard distribution injection with explicit opt-out;
- YAML config limits and fail-open/fail-closed policy;
- high-signal fixture corpus under `testdata/tool-call-repair`;
- unit, concurrency, fuzz, hardening, allocation, benchmark, runtime, frontend/backend conformance, and dogfood coverage;
- ADR 0007 and performance/release documentation.

## Relevant Reasonix Assets

Reasonix contributes three distinct concepts:

1. **History sanitation** in `internal/provider/provider.go`:
   - closes truncated arguments;
   - removes trailing commas;
   - inserts `null` after a dangling colon;
   - removes a dangling escape;
   - falls back to `{}`;
   - repairs tool-call/result pairing for replay.
2. **Agent retry guidance** in `internal/agent/execute_one.go`:
   - after malformed-JSON execution failure, echoes the tool schema back to the model.
3. **Explicit MCP alias resolution** in `internal/tool/tool.go`:
   - resolves known registry aliases and rejects ambiguity.

Only the terminal-comma idea is directly transferable to a live generic proxy without changing ownership. Pending-value completion is safe only when constrained by Go-LIP's existing deterministic schema-fill rules. History pairing, agent retry prompts, and permissive fallbacks belong to an agent/session owner, not the canonical completed-call finalizer.

## Requirement-to-Asset Map

| Requirement area | Existing asset | Gap | Classification |
|---|---|---|---|
| 1 V1 compatibility | assembler, engine, ADR, fixtures | Must freeze existing behavior while adding a non-append-only exception | Partial |
| 2 terminal comma | Reasonix removes final comma; Go-LIP scanner explicitly refuses it | Need value-aware terminal classification and one-byte removal | Missing |
| 3 pending value | Go-LIP can fill a missing property after valid JSON parsing | Invalid `{"key":` cannot reach schema repair; must constrain to exact top-level property | Missing |
| 4 bounded classifier | current scanner tracks strings/delimiters only | Need grammar expectation/completed-value state without a second parser | Missing |
| 5 engine integration | schema cache/preflight/post-validation exist | Need candidate ordering and schema-before-syntax path only for pending value | Partial |
| 6 security | strong limits and privacy tests exist | New one-byte deletion and invalid-prefix analysis need adversarial proof | Partial |
| 7 protocol integration | conformance and canonical lifecycle exist | New cases must be added without adapter branches | Partial |
| 8 evidence | fixtures/fuzz/bench infrastructure exists | New class-specific fixtures, fuzz properties, and benchmark rows needed | Partial |

## Brownfield Gaps

### G-01: The append-only scanner intentionally rejects the main transferable Reasonix case

`json_scanner_test.go` explicitly freezes `{"a":1,` as refused. The existing scanner cannot remove a terminal separator, so appending delimiters alone can never make the input valid.

**Requirement remediation:** preserve `CompleteJSONSuffix` unchanged and add a separate tail-repair path rather than weakening its append-only contract.

### G-02: Removing a comma based only on final-byte position is unsafe

Inputs such as `{,`, `[,]`, `{"a":,`, or `[1,,` can become syntactically valid if a comma is naively removed and containers are closed. That would erase evidence of a missing value.

**Requirement remediation:** require that the terminal comma follow a lexically complete value in the active container.

### G-03: Existing schema fill happens too late for `{"key":`

The engine cannot parse ordered JSON or enter `repairObject` until arguments are syntactically valid. A deterministic schema value cannot currently help complete the invalid tail.

**Requirement remediation:** allow schema lookup during syntax candidate construction only for the narrow pending-value class.

### G-04: Nested pending paths would require a second full JSON parser

Safely resolving `{"outer":{"mode":` requires exact structural path tracking across incomplete JSON, schema branch resolution, duplicate handling, and property normalization. That is substantially more complex and higher risk than the common top-level tool-argument case.

**Requirement remediation:** V2 is restricted to an exact final top-level property. Nested pending values are deferred.

### G-05: Reasonix's unconditional `null` is not semantically safe

A schema may permit `null` without making it the intended or only value. A live call could acquire a business meaning the model never emitted.

**Requirement remediation:** `null` is inserted only when explicitly selected by `const`, single-valued `enum`, or `default`.

### G-06: Reasonix's `{}` fallback is unsafe for execution

An arbitrary malformed call may map to a valid no-argument tool invocation, changing an invalid instruction into executable behavior.

**Requirement remediation:** no generic fallback object is permitted.

### G-07: Deleting a dangling escape changes data

Removing a final backslash may alter a path, regex, command, or encoded string. Appending an escape is also ambiguous because the missing byte could have been `"`, `n`, `u`, or another escape code.

**Requirement remediation:** all incomplete escape and Unicode-escape tails remain unrepairable.

### G-08: Existing public reason codes are sufficient only with explicit precedence

Terminal comma is a syntax repair. Pending deterministic value already maps naturally to `const_inserted`, `enum_inserted`, or `default_inserted`. Combined tail and schema repair still needs one deterministic primary reason.

**Requirement remediation:** avoid widening `pkg/lipsdk/toolcall`; freeze `syntax_repaired` for append-only/comma paths and the selected fill reason for pending-value paths, even when a later bounded schema repair is also required. Existing no-tail V1 reason behavior remains unchanged.

### G-09: Candidate integration must not leak partial mutation

The engine currently returns original arguments on unrepairable outcomes. A new multi-stage syntax candidate could accidentally escape after one repair but before post-validation.

**Requirement remediation:** candidates stay local until shape preflight succeeds. Non-empty-schema results additionally pass existing deterministic schema repair and final compiled-schema validation. Empty-schema append-only/comma syntax-only rewrites retain the current JSON-validity and shape-preflight exception.

### G-10: Exact-original replay depends on keeping the old scanner contract

Some callers and tests use `CompleteJSONSuffix` directly and assert append-only prefix preservation. Changing it to remove commas would silently break a frozen contract.

**Requirement remediation:** introduce a new classifier/candidate builder; do not redefine `CompleteJSONSuffix`.

### G-11: Invalid JSON cannot use ordinary duplicate-key preflight directly

The tail classifier examines bytes before a valid candidate exists. It may provisionally classify such bytes, but duplicate-name authority remains with `preflightArgsJSON`.

**Requirement remediation:** duplicate-member candidates are rejected by preflight before ordered materialization, schema repair, or publication; the lexical classifier does not need a second duplicate-name implementation.

### G-12: Protocol adapters already have the correct abstraction

All supported frontends/backends consume canonical lifecycle events. Adding adapter-local fixes would duplicate policy and violate steering.

**Requirement remediation:** implementation stays in `internal/core/toolcallrepair` and existing runtime finalization.

### G-13: The default feature already has a complete rollback

Operators can explicitly disable `tool-call-repair`. Adding a second mode or several booleans would increase configuration surface without providing a stronger safety boundary.

**Requirement remediation:** safe-tail behavior is part of existing `conservative` mode; no new config key is required.

### G-14: Existing tests are broad but lack terminal grammar properties

Current fuzzing protects V1 engine invariants, but it does not freeze “comma must follow complete value” or “pending value must be exact top-level deterministic property.”

**Requirement remediation:** add dedicated classifier fuzz properties and positive/negative fixtures.

## Implementation Options

### Option A: Port Reasonix's History Sanitizer

**Approach**

- remove terminal commas;
- append `null` after dangling colons;
- delete dangling escapes;
- fall back to `{}`;
- repair transcript pairing.

**Advantages**

- broad recovery coverage;
- simple behavior already used by Reasonix.

**Disadvantages**

- designed for provider-safe history replay, not live execution;
- invents values and may execute a different tool operation;
- crosses into agent/session ownership;
- weakens Go-LIP's deterministic and post-validated policy.

**Disposition:** Rejected.

### Option B: Add Terminal-Comma Repair Only

**Approach**

- classify a comma after a complete value;
- remove it;
- use existing suffix closure and schema pipeline.

**Advantages**

- highest-confidence transferable improvement;
- small implementation;
- no schema-before-syntax path.

**Disadvantages**

- leaves a useful schema-determinable truncation shape unresolved;
- does not exploit Go-LIP's stronger existing deterministic-fill machinery.

**Disposition:** Safe viable subset, but incomplete.

### Option C: Bounded Safe-Tail Classifier

**Approach**

- keep append-only V1 first;
- classify only terminal comma after complete value or exact top-level pending value;
- for pending value, reuse current deterministic fill;
- route every candidate through existing preflight, schema repair, and post-validation;
- reject every other malformed tail.

**Advantages**

- captures the highest-value safe improvements;
- preserves one engine and one policy boundary;
- no provider/agent coupling;
- no new config or public action;
- explicitly testable refusal surface.

**Disadvantages**

- requires a more precise lexical state machine;
- pending-value path changes engine ordering;
- needs extensive hostile-input tests.

**Disposition:** Preferred.

### Option D: Add Explicit Tool Aliases

**Approach**

- widen tool definitions or finalizer metadata with aliases similar to Reasonix MCP bindings.

**Advantages**

- could repair provider/MCP naming variants.

**Disadvantages**

- Go-LIP currently has no canonical alias metadata;
- would widen public contracts and provider/cache surfaces;
- separator normalization already handles common safe variants;
- value is lower than terminal argument recovery.

**Disposition:** Deferred to a separate spec if concrete alias failures justify a contract.

## Required Design Decisions

1. Keep `CompleteJSONSuffix` unchanged.
2. Add one bounded terminal-tail classifier.
3. Try append-only completion before new repairs.
4. Permit exactly one comma deletion and only after a complete value.
5. Restrict pending values to an exact final top-level property.
6. Reuse only `const`, single-valued `enum`, and `default`.
7. Use existing reason codes and finalizer actions with explicit primary-reason precedence for combined repairs.
8. Keep candidates private until full post-validation.
9. Add no config key and no adapter branch.
10. Treat broader Reasonix behavior as explicit non-goals.

## Complexity and Risk

- **Effort: M** — one core package, runtime/conformance fixtures, and documentation.
- **Risk: Medium** — malformed-input grammar and the one-byte deletion exception require careful proof.
- **Risk controls:** narrow classes, no nested pending path, single linear scan, current limits, duplicate rejection after candidate construction, exact-original fail-open, mandatory post-validation, fuzzing, and class-specific negative fixtures.

## Requirements Gap Review Result

The requirements were corrected to:

- preserve the direct append-only scanner contract;
- reject comma removal unless a complete value precedes it;
- restrict pending-value completion to exact top-level properties;
- insert `null` only when explicitly schema-determined;
- exclude dangling escapes, partial literals, `{}` fallback, aliases, transcript repair, and agent retries;
- reuse existing public reason codes with explicit combined-repair precedence and reuse existing config;
- require no-partial-mutation and full post-validation.

No unresolved requirement gap remains. Proceed to design with Option C.
