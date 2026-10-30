# Brownfield Design Validation

## Verdict

**GO.** The design is compatible with current Go-LIP boundaries and is ready for task decomposition. No requirement/design correction remains after this validation pass.

## Validation Checklist

### Core vs plugin ownership — PASS

The detector belongs to core because it correlates authoritative A-leg state across requests and must observe runtime open/release seams. Consumers remain feature plugins through a narrow `compaction.Observer`; no concrete provider plugin owns the detector.

### Canonical model neutrality — PASS

Rules inspect `lipapi.Call`, normalized items/text, `OperationContextCompaction`, and `ItemKindCompaction`. No Anthropic/OpenAI/Gemini SDK type enters core detection. Upstream prompt signatures are represented as versioned canonical text/shape matchers, avoiding frontend/backend Cartesian branches.

### Streaming-first architecture — PASS

Response detection observes final canonical events selected by the existing retry stream. There is no non-streaming side channel and no buffering of full responses. Explicit compact terminal completion is recognized through the same stream lifecycle.

### Retry/failover invariants — PASS

A start is created at logical request/A-leg scope only after an actual backend open. Later B-leg retries cannot create new logical transactions. Detection never requests retry or changes no-retry-after-output behavior.

### Request integration — PASS

The effective canonical `baseline` is already available after request-wide/pre-request shaping. Inspecting it avoids raw-client spoof differences caused by proxy transforms. Deferring event emission until backend open prevents false starts for locally rejected requests.

### Response integration — PASS with implementation constraint

Current `retryRecvStream` has several release branches (live, completion-gate drains, tool-finalizer drains, recovery drains). Implementation must centralize detector observation at the final release seam or route all returns through one helper; attaching only to `handleRecvSuccess` is insufficient.

### Observer failure semantics — PASS

A dedicated fail-open observer avoids the stronger failure semantics of final-stream observer sessions. Observer errors/panics are isolated, callbacks are synchronous and ordered, and no mutation result exists.

### Process lifetime — PASS

Cross-request heuristic/transaction state must survive generation replacement; `ProcessServices` is the existing process-owned lifetime. Database durability is neither required nor desirable because state affects observations only.

### Concurrency — PASS

Concurrent A-legs/turns require synchronization. One detector map mutex is justified. State transitions happen under the mutex; callbacks happen after unlock. No per-A-leg goroutine/channel is needed.

### Privacy — PASS

Rule matching may transiently read canonical text, but durable in-memory detector state contains only hashes/counts/timestamps/transaction metadata. Listener events contain no content. This is materially narrower than exposing traffic observations.

### Signature maintainability — PASS

Rules use versioned IDs and explicit match functions/table entries. There is no regex DSL, dynamic registry, provider branching, or plugin-configured classifier. Updating an upstream harness rule is a localized test/table change.

### Heuristic precision — PASS

The heuristic requires same A-leg, substantial previous history, dual absolute/relative reduction, retained recent semantic tail, and older-prefix replacement. It emits completion only. This deliberately accepts false negatives instead of mislabeling resets.

### Protocol compatibility boundary — PASS

The design does not attempt partial support for Codex V2 `compaction_trigger` or Hermes `context_management`. Detection-only admission of unsupported controls would be a semantic bug. Their eventual canonical support can add strict detector rules without redesign.

## Design-to-Requirement Trace

| Requirement | Design coverage |
|---|---|
| R1 lifecycle contract | SDK Phase/Evidence/Event, no fabricated completion |
| R2 subscription | FeatureBundle observer + fail-open dispatcher |
| R3 protocol strict | explicit operation/item + open/terminal rules |
| R4 agent signatures | static versioned rule matrix on canonical traversal |
| R5 local heuristic | bounded same-A-leg semantic fingerprints |
| R6 transactions | single/series/completion-only state machine |
| R7 privacy/lifetime | ProcessServices, hashes only, TTL/max bounds |
| R8 regression safety | open/release seams, TDD/race/architecture gates |

## Simplification Review

The design deliberately rejects four tempting additions:

1. **No numeric confidence score.** Evidence class communicates epistemic quality without arbitrary calibration.
2. **No new StageID.** Subscription is observational and fits the existing extension composition model.
3. **No durable detector store.** Restart loss affects telemetry only, not session correctness.
4. **No protocol-control scope creep.** Unsupported compaction request controls require their own compatibility work.

No service interface is introduced for the concrete detector; one concrete process-owned type is enough.

## Implementation Risks to Pin With Tests

- a recognizable compact request that fails before upstream open;
- duplicate starts during failover or multi-call summary series;
- response release paths bypassing detector observation;
- a reset mistaken for local compaction;
- strict post marker plus heuristic producing duplicate completion;
- rule near-misses containing generic summarization language;
- hot generation replacement losing per-A-leg state;
- observer panic/error affecting execution;
- detector lock held while calling listeners.

## Final Gate

Design validation is **GO** subject to TDD-first implementation and the invariant that this feature remains observational only.
