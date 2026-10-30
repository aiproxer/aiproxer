# ADR 0007: Canonical tool-call repair (V1 + reviewed safe-tail extension)

## Status

Accepted (issue [#152](https://github.com/aiproxer/aiproxer-enterprise/issues/152)). Phase 2 landed the finalizer seam and core per-B-leg assembler. Phase 3 landed bounded offline schema compilation/validation and catalog indexing in `internal/core/toolcallrepair`. Phase 4 landed the deterministic `Engine.Repair` implementation. Phase 5 packaged the standard `tool-call-repair` feature plugin with bootstrap injection and opt-out. Phase 6 added adversarial, leakage, concurrency, fuzz, and panic-isolation hardening. Phase 7 added frontend/backend matrix conformance and an adapter dependency gate. Phase 8 wires repair fuzz/bench into `make test-fuzz` / `make bench`, documents operator-facing latency as `benchstat` evidence (not wall-clock asserts in the parallel unit suite), and adds a dogfood truncated-args example.

## Context

The reviewed safe-tail extension preserves the V1 ownership and rollback model while covering two bounded terminal truncations that remain safe for immediate execution: one terminal comma after a complete JSON value, and one exact final top-level property whose value is selected by an existing deterministic schema fill.

Upstream models sometimes emit native structured tool calls whose argument JSON is truncated, mis-keyed, or missing only schema-determinable values. Repair must stay at the canonical tool-event layer (after backend translation, before frontend encoding) so every wire frontend benefits once. `ToolReactor` is one-event-in/one-event-out and cannot replace a completed call with an ordered started/args/finished lifecycle, so V1 adds a purpose-built completed-call finalizer seam rather than overloading reactors or completion gates.

## Decision

### Scope

- Repair **native structured tool calls only** (`tool_call_started` / `tool_call_args_delta` / `tool_call_finished`).
- Effective catalog is `retryRecvStream.baseline.Tools` after catalog filtering and request shaping.
- Core owns bounded per-B-leg buffering and exact original replay on pass / fail-open / overflow.
- Deterministic repair only; every rewrite is post-validated against the effective schema.

### Decision matrix

| Safe-tail situation | Action |
|---|---|
| Terminal comma after a complete object member/array element | Delete exactly that comma, append required closers, shape-preflight, and post-validate |
| Exact final root property with `const`, one-element `enum`, or `default` | Append the selected JSON value and required closers, shape-preflight, and post-validate |
| Dangling colon without deterministic fill, partial literal/number/string, nested pending value, unsafe comma | Unrepairable; preserve original bytes under fail-open |

The safe-tail classifier is one bounded linear scan. It never falls back to `{}`, inserts type-derived `null`, deletes escapes, coerces scalars, or searches multiple candidates. `CompleteJSONSuffix` remains append-only and runs first.

| Situation | Action |
|---|---|
| Args JSON valid + schema-valid for exact tool name | Pass: replay exact original buffered event bytes/sequence |
| Truncated/unterminated JSON completable by minimal append-only suffix | Rewrite after post-validation |
| Tool name / property name uniquely matches after normalization | Rewrite name/key only on **exactly one** match |
| Missing property with schema `default`, `const`, or single-valued `enum` | Insert that value only |
| Unknown property and schema has `additionalProperties: false` | Remove that property only |
| Ambiguous normalized name/key, invented business value needed, unsafe schema, overflow, validator failure | Unrepairable (no partial mutation) |
| Scalar type mismatch (e.g. string `"1"` vs number) | **Unrepairable in V1** — scalar coercion is disabled |

The tool-name-correction slice ([#997](https://github.com/aiproxer/aiproxer-enterprise/issues/997)) extends the finalizer vocabulary with the additive `ActionSteer`: a finalizer that recognises a completed call core should replace returns `ActionSteer` with a `SteerRequest` (hidden instruction, per-turn correction budget, reasoning allowance). Core's steer gate may decline; accepted and declined steers both continue the finalizer chain exactly as a pass, so later finalizers, including mandatory decisions, still run, and only acceptance additionally marks the attempt as withholding. A missing or invalid steer request, a steer request carrying a tool name or argument bytes, or a steer attached to any other action is unusable and takes the existing unusable-result fallback.

### Engine vs policy

- Engine outcomes are `pass`, `rewrite`, or `unrepairable` (classification only).
- Fail-open pass-through vs fail-closed reject is finalizer/runtime policy over `unrepairable`, not an engine `pass`.

### Name normalization

`NormalizeASCIIName`:

1. lowercase ASCII letters (`A-Z` → `a-z`);
2. remove only ASCII whitespace, `'_'`, and `'-'`;
3. preserve all other bytes unchanged.

Rewrite a tool or property name only when normalization yields exactly one catalog/schema match. Never use edit-distance or multi-candidate fuzzy matching.

### Finalizer ordering

1. Core buffers original lifecycle events per tool-call ID until `tool_call_finished` (or overflow/cancel/EOF/replace).
2. On completion, run ordered `toolcall.Finalizer` chain under existing panic/error isolation.
3. Emit `pass` as exact original replay; `rewrite` as synthesized started + repaired args delta(s) + finished; `reject` as configured typed error.
4. Every emitted event still passes tool policy, tool reactors, response-part hooks, completion gates, traffic observation, and frontend encoding.
5. Finalization occupies the existing `tool_event_reaction` inventory stage; it does not invent a new legal pipeline stage.

Core passes Finalize a defensive copy of argument bytes; exact original events remain core-owned and are not part of the public `CompletedCall`.

### Fail-open / fail-closed

- Default `on_unrepairable: pass_through`: replay exact originals.
- Optional `on_unrepairable: error`: fail closed with a typed stream error.
- Cap overflow or fail-open finalizer error: immediately replay buffered originals for that call and switch it to transparent pass-through.

### Buffering implications

- Zero buffering work when no finalizers are installed, feature disabled, or the call has no tools.
- Bound argument assembly by `max_args_bytes` (default 64 KiB).
- Clear attempt-local state on finish, B-leg replacement, cancellation, EOF, and `Close`.
- Concurrent requests with identical tool-call IDs remain isolated per B-leg.

### Schema dialect / offline behavior

- Validate offline only; remote `$ref` / network / filesystem resolution is prohibited.
- Unsupported or unsafe schemas skip speculative mutation (syntax-only where safe, otherwise pass-through with a stable reason code).
- Diagnostics may carry tool-name hash or bounded catalog name, schema digest, byte counts, action, and reason code — **never** raw argument or schema payloads.

### Standard configuration

Structured validation paths and their rendered form are bounded to 256 runes. Errors and reject reasons expose stable classifications, never instance values, tool names, schema bodies, or external-reference URLs. Existing runtime finalizer diagnostics remain authoritative; V1 does not add a feature-global metrics registry or a new SDK observer solely for repair counters.

- Enablement is `config.PluginConfig.Enabled` / `aipsdk.Registration.Enabled` only (not plugin-private YAML).
- Absent `tool-call-repair` row in the **standard** distribution means inject enabled defaults.
- Explicit disabled row opts out (suppresses injection/enablement).
- No auto-injection into custom/minimal bundles.
- Duplicate feature IDs remain a configuration error via existing registration validation.

### Non-goals (V1)

- Text / XML / Markdown / pseudo-tag tool-call inference
- Business-value guessing for missing required fields
- Fuzzy / edit-distance matching with multiple candidates
- Auxiliary LLM repair calls
- Remote schema fetches
- Scalar coercion (explicitly deferred)
- Provider- or frontend-local repair branches
- Process-global plugin buffering state

## Consequences

- The ADR, fixtures, engine contracts, `retryRecvStream`/Execute sequences, and standard injection behavior are frozen by tests.
- `pkg/aipsdk/toolcall` exposes the completed-call finalizer contract; FeatureBundle and runtime snapshot wiring install it explicitly.
- Phase 3 adds schema validation (`ValidateArgsAgainstSchema`) with offline compile, rejecting external loaders, and a bounded digest LRU.
- Phase 4 implements the deterministic repair engine (`Engine.Repair`).
- Phase 5 packages/registers `tool-call-repair`, injects an enabled standard config row when absent via `EnsureToolCallRepairInConfig`, and keeps opt-out via any explicit matching features row (instance ID or factory kind). Under plain-bool `PluginConfig.Enabled`, omitting `enabled: true` zeros to false and is therefore an explicit disabled opt-out that suppresses injection (not a `StandardDistributionRequirements` mandatory entry). The feature package is YAML-only; `standardplugins` constructs the core `toolcall.Finalizer` adapter and contributes `ToolCallFinalizationMaxArgsBytes` on `FeatureBundle`.
- Phase 6 locks payload non-leakage, bounded structured errors, adversarial fail-closed behavior, concurrent cache/engine reuse, and native fuzz invariants. Runtime panic/error isolation continues to replay exact buffered originals.
- Phase 7 runs the standard repair finalizer through every tools-viable bundled frontend/backend conformance cell and prohibits protocol adapters from importing the engine or feature implementation.
- Phase 8 performance posture: keep allocation/semantic-bound unit checks; publish `Benchmark*` via `make bench` and Tier-1 fuzz via `make test-fuzz`; do **not** encode absolute wall-clock latency thresholds in `go test` (parallel CI/hardware noise). Millisecond-scale goals are verified with controlled `benchstat` comparisons.
- DoS hardening via `internal/core/jsonshape` (`encoding/json.Decoder.Token`): request envelopes keep historical duplicate-member acceptance (`RejectDuplicateNames=false`, depth 128, default 8 MiB / configurable); tool schema (256 KiB / depth 32 / 4096 nodes / 1024 members) and tool args (64 KiB default / depth 64) reject duplicates. Schema `MaxArrayElems` is `min(MaxNodes, profile cap)`. Overall HTTP request-body caps are **independent** of tool schema/args limits. Engine public reasons stay sparse: cancel → `canceled`, unsupported → `schema_unsupported`, other schema shape/limit/malformed → `schema_invalid`. `Engine.RepairContext` propagates cancellation as `OutcomeUnrepairable` + `canceled` without a Go error; originals are preserved. Step 3 locks preflight-before-materialize on all four frontend ServeHTTP paths (`reqbody` → `jsonguard.Preflight` → Decode*), schema/args/repair candidates (including `repairArgsJSON` and post-`CompleteJSONSuffix` candidates), with archtest call-order gates.
- Step 5 ordered-args parser decision: retain the recursive `parseOrderedJSON` representation after preflight guarantees depth ≤ 64 (args) / ≤ 32 (schema). Duplicate rejection, key order, and `json.Number` spellings stay. An iterative token-stack builder is a future profiling-triggered optimization only — Windows `-benchtime=10x` evidence (not Linux baselines / not linearity claims) shows depth-64 parse ~8 µs and mixed ~60 KiB parse ~0.6 ms versus full `Engine.Repair` ~2 ms on the same payload (`docs/performance-checks.md`). No custom grammar scanner.
- Observable compatibility note: tool lifecycle events may be held until `tool_call_finished`; valid originals are replayed unchanged with no extra model round trip.
- Safe-tail compatibility note: the private terminal-comma candidate changes exactly one original byte; the private pending-value candidate is append-only. Published output may receive only existing bounded schema repairs after candidate preflight. Existing feature disablement remains a complete rollback.
