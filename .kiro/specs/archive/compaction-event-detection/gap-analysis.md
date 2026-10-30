# Brownfield Requirements Gap Analysis

## Result

**PASS after requirements corrections.** Current Go-LIP has the required canonical request, A-leg continuity, explicit compact operation, compaction output item, feature composition, and retry-stream integration seams. No database or provider-specific implementation is needed.

## Gaps and Corrections

### 1. There is no universal compaction wire operation
Go-LIP models explicit `context.compaction`, while most agents compact through ordinary utility LLM calls or local history rewrites.

**Correction:** use the ordered evidence hierarchy `protocol_strict` -> versioned `signature_strict` -> conservative `history_heuristic`. Generic summarization language never suffices.

### 2. A recognized request may never execute
Matching during request preparation would falsely report starts for requests later rejected by routing/admission/open.

**Correction:** inspect the effective post-transform baseline, but emit `started` only after the first upstream B-leg opens.

### 3. One compaction can issue several LLM calls
Pi/OpenClaw may split, Gemini verifies a snapshot, and Aider may recursively summarize.

**Correction:** add A-leg-scoped transaction modes (`single`, `series`, `completion-only`) and deduplicate to one logical start/completion pair.

### 4. Local-only compaction needs cross-request memory
A request-local object cannot compare subsequent turns; durable database state is disproportionate.

**Correction:** keep bounded process-owned in-memory fingerprints keyed by authoritative `ALegID`. They survive hot generation reload but are correctness-independent and may disappear on restart.

### 5. FeatureBundle has no typed compaction subscriber
Existing observer composition is suitable but `traffic.Observer` exposes body/leg concepts listeners do not need.

**Correction:** add `pkg/lipsdk/compaction.Observer`, a FeatureBundle slice, merge/snapshot wiring, and a small fail-open dispatcher carrying metadata only.

### 6. Prompt privacy needs an explicit invariant
Signature/heuristic matching must temporarily inspect text, but listeners should not receive it.

**Correction:** persist only counts, timestamps, bounded SHA-256 semantic hashes, rule/transaction metadata; never prompt excerpts or raw bodies.

### 7. Two native request controls are not currently canonical
The pinned OpenResponses request model does not currently represent Codex V2 `compaction_trigger`, and no canonical carrier was found for Hermes `context_management` / `compact_threshold`.

**Correction:** do not add partial support here. Accepting a control merely to detect it without forwarding its semantics would be a silent downgrade. Add detection when a separate protocol-compatibility change represents those controls correctly. Existing compaction output remains strict completion evidence.

### 8. Completion cannot always be proven
Some implementations expose a distinctive summarizer request but no unique installed-summary marker.

**Correction:** allow start-only transactions. Emit `completed` only for canonical compact output/terminal, a strict post marker, or the high-precision history heuristic.

### 9. Existing final-stream observation has stronger failure semantics
Compaction listeners are telemetry only and must never fail execution.

**Correction:** do not overload FinalStreamObserver. Observe final selected canonical releases through a dedicated fail-open compaction dispatcher.

## Brownfield Compatibility Matrix

| Subsystem | Change |
|---|---|
| `pkg/lipapi` | consume existing operation/item/traversal; no detection-only fields |
| secure session / A-leg | consume authoritative ID only |
| request pipeline | analyze effective baseline; emit after open |
| routing/backend | no decision changes |
| retry stream | observe every final released event once |
| FeatureBundle | additive observer slice |
| ProcessServices | one concrete detector instance |
| DB/B2BUA schemas | none |
| provider/frontends | no signature branches |

## Required Invariants

1. canonical and provider-neutral matching;
2. authoritative A-leg scope;
3. start only after real upstream open;
4. completion only when evidence warrants it;
5. observer fail-open behavior;
6. no content retention;
7. no unsupported protocol semantics;
8. bounded process state with no background worker.
