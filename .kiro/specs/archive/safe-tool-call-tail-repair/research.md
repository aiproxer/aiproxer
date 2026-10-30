# Research Notes

## Research Objective

Identify the highest-value tool-call repair ideas in DeepSeek-Reasonix that can be adopted by Go-LIP without weakening the conservative live-execution guarantees established by ADR 0007.

## Sources Reviewed

### Go-LIP

Base reviewed: `main` commit `11e7a1f70116b6a86c08097aa45ab76f9de85f7b`.

Primary files:

- `docs/adr/0007-canonical-tool-call-repair.md`
- `internal/core/toolcallrepair/engine.go`
- `internal/core/toolcallrepair/json_scanner.go`
- `internal/core/toolcallrepair/json_scanner_test.go`
- `internal/core/toolcallrepair/schema_repair.go`
- `internal/core/toolcallrepair/shape_guard.go`
- `internal/core/toolcallrepair/schema_compile.go`
- `internal/core/toolcallrepair/schema_cache.go`
- `internal/core/toolcallrepair/finalizer.go`
- `internal/core/runtime/tool_call_assembler.go`
- `pkg/lipsdk/toolcall/finalizer.go`
- `testdata/tool-call-repair/*`
- current fuzz, hardening, conformance, dogfood, and benchmark tests.

### DeepSeek-Reasonix

Reviewed `main-v2` commit `22e18cafc0dfe27f192e8e303ffae0a0bc548eb0`.

Primary files:

- `internal/provider/provider.go`
- `internal/provider/provider_test.go`
- `internal/agent/execute_one.go`
- `internal/agent/repeat_failure_guard.go`
- `internal/tool/tool.go`.

## Finding 1: The Two Projects Repair at Different Ownership Layers

Go-LIP repairs the current native structured call before the frontend/client receives it. A successful rewrite can therefore become executable immediately.

Reasonix's broad `closeTruncatedJSON` behavior is primarily applied to stored conversation history before provider replay. The corresponding tool result usually already exists or a placeholder is inserted. That is a lower-risk context-repair use case than changing a fresh live instruction.

Reasonix also improves a later model retry by including the tool schema after malformed-JSON execution failure. Go-LIP is a protocol proxy and does not own the agent's retry loop.

**Consequence:** copy mechanisms only when they remain safe for immediate execution and belong at the canonical finalizer layer.

## Finding 2: Go-LIP V1 Is Stronger for Live Repair

Go-LIP already provides capabilities not present in Reasonix's generic execution path:

- exact and unique normalized tool resolution;
- bounded offline JSON Schema compilation;
- deterministic property normalization;
- deterministic `const`, single-valued `enum`, and `default` insertion;
- forbidden additional-property removal;
- mandatory final schema validation;
- configurable fail-open/fail-closed policy;
- exact original lifecycle replay;
- shape/duplicate/UTF-8 limits;
- panic, cancellation, concurrency, fuzz, and conformance hardening.

**Consequence:** the new work should extend syntax candidate construction, not replace the engine.

## Finding 3: Terminal Comma Is the Highest-Value Safe Transfer

Reasonix repairs:

```json
{"path":"/tmp",
```

to:

```json
{"path":"/tmp"}
```

Go-LIP explicitly refuses this shape because `CompleteJSONSuffix` is append-only.

The transformation can be safe when all of the following hold:

1. the comma is the final non-whitespace byte;
2. it is outside a string;
3. it follows a complete value in the active object/array;
4. exactly that comma is removed;
5. required closers are appended;
6. the candidate passes shape preflight and final schema validation.

The complete-value rule is essential. Without it, `{,` could become `{}` and `[,]` could become `[]`.

## Finding 4: A Dangling Colon Is Safe Only With a Deterministic Schema Value

Reasonix maps `{"a":` to `{"a":null}`.

That is unsafe as a generic live repair. However, Go-LIP already defines a conservative deterministic-fill set:

- `const`;
- one-element `enum`;
- explicit `default`.

For a top-level exact property, the engine can append the selected value and closers without deleting any emitted data:

```json
{"mode":
```

with schema:

```json
{"properties":{"mode":{"const":"safe"}}}
```

becomes:

```json
{"mode":"safe"}
```

**Consequence:** reuse the existing fill function and reason codes. Do not add type-derived `null`, branch selection, scalar coercion, or business-value guessing.

## Finding 5: Incomplete Escapes Remain Ambiguous

Reasonix removes a final backslash before closing a string. That changes the emitted string.

Appending another backslash is not reliably better: the missing byte may have been `"`, `n`, `t`, `u`, or another escape.

Paths, regexes, shell commands, and source snippets make this especially risky.

**Consequence:** incomplete escapes and Unicode escapes stay unrepairable.

## Finding 6: Fallback `{}` Is Incompatible With Go-LIP's Safety Model

A no-argument tool or a tool with defaults may accept `{}`. Converting arbitrary garbage to `{}` turns an invalid model output into a potentially valid operation with no evidence that the model intended that operation.

**Consequence:** no fallback document.

## Finding 7: Explicit Alias Resolution Is Not Ready for This Spec

Reasonix can resolve MCP aliases because its registry owns canonical bindings, raw names, visible names, server names, package names, and capability IDs.

Go-LIP's canonical `ToolDef` contains only name, description, and parameters. Introducing aliases would require a public/canonical metadata decision and cache/prompt-shape analysis.

**Consequence:** keep current exact and normalized unique matching. Defer aliases.

## Finding 8: Existing Reason Codes Are Sufficient Only With Explicit Precedence

The public result carries one reason code, so combined syntax/schema repairs need a deterministic primary-reason contract:

| Path | Primary reason |
|---|---|
| existing V1 name-only repair | existing `tool_name_normalized` |
| existing V1 schema-only repair | existing first schema-repair reason |
| append-only or terminal-comma candidate, with or without later schema repair | `syntax_repaired` |
| pending `const`, with or without later schema repair | `const_inserted` |
| pending one-element enum, with or without later schema repair | `enum_inserted` |
| pending `default`, with or without later schema repair | `default_inserted` |

Secondary schema repairs remain observable through the final validated payload and tests, not by replacing the primary tail-repair reason. A new public reason code would expose implementation mechanics without changing policy.

**Consequence:** keep `pkg/lipsdk/toolcall` unchanged and freeze the precedence matrix in fixtures.

## Finding 9: `CompleteJSONSuffix` Is a Frozen Useful Primitive

Tests assert:

- valid input is copied;
- unterminated strings and containers are closed;
- existing invalid prefix bytes are never removed;
- trailing comma, pending value, incomplete escape, mismatched close, and trailing garbage are refused.

Changing this function would make direct callers lose the append-only guarantee.

**Consequence:** add a new tail analyzer and candidate builder after append-only completion fails.

## Finding 10: Terminal Comma Needs Grammar State, Not Only Delimiter State

The current scanner can identify strings and balanced delimiters but not whether a comma follows a complete value.

The new scanner needs expectation state:

- object expects key/end;
- object expects colon;
- object expects value;
- object expects comma/end;
- array expects value/end;
- array expects comma/end.

Completed-scalar recognition also matters:

- strings and closed containers are complete;
- `true`, `false`, and `null` require exact completion;
- numbers require valid JSON number termination;
- partial tokens must be rejected.

**Consequence:** implement one bounded lexical grammar scanner rather than regex or remove-and-hope logic.

## Finding 11: Top-Level Pending Value Is the Correct Initial Boundary

Most tool arguments are root JSON objects with named parameters. Restricting to the final top-level property captures common cases such as:

```json
{"path":"/tmp","recursive":
```

without requiring incomplete nested-path reconstruction.

Nested paths would require carrying exact schema location through arrays, objects, normalized names, refs, and branch constructs.

**Consequence:** nested pending values are a future profiling/evidence-triggered extension, not V2 scope.

## Finding 12: Candidate Ordering Can Preserve the Existing Engine

Recommended order:

1. enforce cancellation and `max_args_bytes`;
2. resolve the exact or uniquely normalized tool;
3. if JSON is valid, continue existing validation;
4. try `CompleteJSONSuffix`;
5. if that fails, classify the terminal tail;
6. build a terminal-comma candidate, or compile the non-empty schema once and build a deterministic pending-value candidate;
7. shape-preflight the candidate, including duplicate-member rejection;
8. for a non-empty schema, validate and, if needed, run existing deterministic schema repair;
9. preflight and post-validate the final non-empty-schema result;
10. for an empty schema, permit only existing append-only or terminal-comma syntax-only rewrites after JSON validity and shape preflight;
11. publish a rewrite only after the applicable checks succeed.

Byte-preservation invariants apply to the private syntax candidate. The published `ArgsJSON` may differ further only through the existing bounded deterministic schema-repair pipeline; no second speculative syntax edit is allowed. This keeps a single engine and preserves the current empty-schema early-return behavior.

## Finding 13: No New Configuration Is Needed

The feature already has:

- standard default injection;
- explicit feature disablement;
- conservative-only mode;
- byte/schema limits;
- fail-open/fail-closed policy.

The proposed classes are narrower than existing property removal and deterministic insertion and remain post-validated.

**Consequence:** safe-tail repair becomes part of `conservative`; full feature disablement remains rollback.

## Finding 14: Performance Should Remain Linear and Bounded

The tail analyzer adds one scan only for invalid JSON after append-only completion fails. Valid calls retain the current fast path.

Expected cost:

- valid/schema-valid: unchanged except no new work;
- append-only truncation: unchanged before new classifier;
- terminal comma: one additional O(n) scan and one bounded copy;
- pending value: scan plus existing cached schema compile/lookup and one encoding;
- near-limit malformed: bounded O(n), no search/backtracking.

**Consequence:** benchmark each class separately with same-host repeated samples. Require no statistically significant valid-pass-through regression greater than 5% in time/op and no increase in allocs/op. Record near-limit elapsed time as evidence, but keep byte/depth/operation bounds—not an absolute wall-clock threshold—as the gate.

## Selected Repair Matrix

| Input class | Action |
|---|---|
| valid JSON | existing validation/repair |
| unterminated string/container, append-only closable | existing V1 completion |
| terminal comma after complete value | remove one comma, append closers, post-validate |
| exact final top-level property + schema const | append const, append closers, post-validate |
| exact final top-level property + single enum | append enum value, append closers, post-validate |
| exact final top-level property + default | append default, append closers, post-validate |
| dangling colon without deterministic value | unrepairable |
| nested pending property | unrepairable |
| partial scalar/string/escape | unrepairable |
| arbitrary malformed input | unrepairable |
| multiple plausible repairs | unrepairable |

## Resolved Design Questions

1. **Owner:** existing core repair engine.
2. **Scanner contract:** keep `CompleteJSONSuffix` unchanged.
3. **Comma mutation:** exactly one terminal comma after complete value.
4. **Pending value:** exact top-level property only.
5. **Value sources:** `const`, one-element `enum`, `default`.
6. **Null:** only when one of those sources explicitly yields null.
7. **Schema branches:** only existing deterministic single-branch/local-ref behavior.
8. **Reason codes:** reuse existing codes with tail-repair precedence over later secondary schema repairs.
9. **Configuration:** no new key.
10. **Protocol changes:** none.
11. **Fallback:** exact-original pass/reject policy.
12. **Evidence:** fixtures, fuzz, race, conformance, dogfood, benchmarks, ADR.
