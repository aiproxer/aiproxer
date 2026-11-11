# Requirements Document — 1569 Effort-Values ABI

## Introduction

Proxy-Memory model policy has an internal per-value query (`execbackend.Backend.ResolveEffortSupport`) and a pre-open guard, but no public ABI lets providers publish graded reasoning-effort facts. Executable connectors cannot answer; in-process backends answer unknown. This slice adds a narrowly scoped, backward-compatible ABI on the existing model-specific `ResolveProfile` RPC and wires it to the existing core resolver, with one in-process and one executable real producer.

## V1 Slice

- **Delivers**: providers declare per-model graded effort values; proxy-memory guard refuses unsupported values pre-open and stops counting supported values as unverified.
- **First consumer**: Proxy-Memory model policy (`EffectiveEffortSupport` → pre-open guard) via executable adapter + OpenAI Responses backend + Claude Agent ACP connector.
- **Safe degradation**: missing/invalid/unnegotiated/failed facts answer Unknown; Unknown is served-and-counted (non-strict) or refused (strict), never treated as supported/unsupported.

## Deferred

- Global effort vocabulary, normalization/ceilings (#462) — follow-up: TBD.
- General model-profile/capability-aware routing (#495) — follow-up: TBD.
- Operator UI, broad provider coverage, vendor API probing, cross-instance caching, new diagnostics/ratchets, memory settings/locks/quotes changes — follow-up: TBD.

## Requirements

### Requirement 1: Versioned public ABI on ResolveProfile

**Objective:** As a connector author, I want a versioned optional field on the existing model-specific `ResolveProfile` response, so that I can publish graded effort facts without a new RPC or a global vocabulary.

#### Acceptance Criteria

1. When a peer negotiates protocol minor >= 11 with feature `reasoning_effort_values_v1`, the system shall carry `ReasoningEffortValuesV1{model_id, supported, exhaustive}` on `ResolvedProfile`.
2. If the peer negotiates minor < 11 or lacks the feature, the system shall omit/ignore the field and keep legacy `ResolveProfile` working with Unknown.
3. Where a declaration is present, the system shall validate exact nonempty model ID (max 256 bytes), bounded strings (<=32 values, <=64 bytes each), no blanks/controls/duplicates, valid UTF-8; invalid input shall be rejected as unproven (Unknown), never Unsupported.
4. Where `Resolve(ctx,nil)` is called, the system shall leave the new field absent.

### Requirement 2: Provider-owned tri-state semantics

**Objective:** As a policy consumer, I want exact-match per-value answers, so that Unknown stays the safe default and no global list is invented.

#### Acceptance Criteria

1. When the declaration names the exact requested model and the value is in `supported`, the system shall answer Supported.
2. When the declaration names the exact requested model, is valid, has `exhaustive=true`, and omits the value, the system shall answer Unsupported.
3. When the declaration is missing, partial with `exhaustive=false`, mismatched model, failed/expired discovery, unavailable RPC, older peer, or ungraded binary toggle, the system shall answer Unknown.
4. While mapping, the system shall compare effort strings exactly and case-sensitively with no normalization, and shall never leak provider bodies or model/user strings into diagnostic labels.
5. The system shall not set `exhaustive=true` without provider evidence that the list is the complete accepted graded input set.

### Requirement 3: Executable adapter wiring

**Objective:** As a runtime operator, I want the executable adapter to expose the negotiated declaration through the existing core callback, so that the guard sees real facts.

#### Acceptance Criteria

1. When the session negotiates the feature at minor >= 11, the adapter shall set `Backend.ResolveEffortSupport` to a closure querying the existing `session.Resolve` for the backend-facing candidate; otherwise the callback shall stay nil.
2. When resolving, the system shall query the final compiled native model after alias/native-model resolution and route-query merge, require exact model match, and use metadata-only `ResolveProfile` with caller context timeout.
3. If the RPC errors, cancels, times out, or returns malformed data, the system shall answer Unknown with no trial inference or background process.
4. While resolving, the system shall not borrow another model's profile, cache across models/credentials/generations, or import provider SDKs into core.

### Requirement 4: Real producers and guard integration

**Objective:** As a proxy-memory owner, I want one in-process backend and one executable connector publishing honest facts, so that Supported/Unsupported reach the actual pre-open guard.

#### Acceptance Criteria

1. When OpenAI Responses backend serves an exact documented native model on the official `https://api.openai.com/v1` endpoint, the system shall answer Supported for declared values and Unsupported for exhaustively excluded values; unknown models/aliases/compatible endpoints shall answer Unknown.
2. When the Claude Agent ACP connector has a validated, current discovery snapshot proving graded values for the exact native model, `Resolve(modelID)` shall publish them read-only; contradictory binary metadata, failed-refresh staleness, or missing facts shall publish nothing, and a valid refresh shall restore them.
3. When the guard sees Supported, the governed phase shall preserve effort, invoke provider Open, and not increment `model_policy_unverified_effort`.
4. When the guard sees Unsupported, the governed phase shall refuse before provider Open with the existing bounded error.
5. When the guard sees Unknown, the system shall keep existing semantics: non-strict serves with one unverified event, strict refuses, ordinary traffic unchanged.
