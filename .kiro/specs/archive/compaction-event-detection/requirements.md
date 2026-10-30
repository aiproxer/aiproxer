# Requirements Document

## Introduction

Go-LIP shall detect session compaction from proxy-observable canonical flow and expose derived lifecycle events to feature listeners. Detection must not depend on agent hooks, provider SDK types, raw HTTP parsing in core, or an LLM classifier, and must never alter routing, prompts, responses, retries, accounting, or client framing.

Evidence priority is: canonical protocol semantics; versioned deterministic agent signatures; conservative same-A-leg history heuristic. Detection inspects the effective canonical request associated with the authoritative A-leg and canonical response events actually released by the retry stream.

## Boundary Context

- In scope: started/completed observations, FeatureBundle subscription, existing canonical compaction signals, surveyed-agent signatures, bounded A-leg state, local-compaction heuristic, multi-call coalescing, tests.
- Out of scope: agent hooks, traffic mutation/policy, durable event storage, configurable regex rules, LLM classification, general agent identification, and new protocol support for unsupported `compaction_trigger`/`context_management` controls.
- Ownership: `pkg/lipsdk/compaction`, `internal/core/compactiondetect`, process-lifetime wiring, and existing request/stream orchestration seams.

## Requirement 1: Typed Compaction Lifecycle

1.1. Define exactly `started` and `completed` phases.
1.2. Events shall carry phase, evidence class, versioned rule ID, transaction ID, trace ID, authoritative A-leg ID, available B-leg/attempt/session correlation, and occurrence time.
1.3. Evidence classes shall be `protocol_strict`, `signature_strict`, and `history_heuristic`.
1.4. Events shall contain no prompt/response/tool-result/raw-body/encrypted-compaction content.
1.5. Completion-only evidence shall never invent a historical start.
1.6. If completion cannot be proven, a transaction may legitimately emit only `started`.

## Requirement 2: Safe Feature Subscription

2.1. `FeatureBundle` shall accept zero or more `compaction.Observer` contributions.
2.2. Merge order and frozen-generation wiring shall follow existing observer conventions.
2.3. Observer errors/panics shall be isolated and fail-open; they shall never fail the request.
2.4. Observers are non-mutating and return no replacement/decision.
2.5. Add no mutating StageID, FailureMode, background queue, or listener goroutine.
2.6. With no observers, current behavior remains unchanged with negligible dispatch overhead.

## Requirement 3: Canonical Protocol-Strict Detection

3.1. Effective `OperationContextCompaction` is a protocol-strict start candidate.
3.2. Emit `started` only after an upstream B-leg successfully opens; local rejection/no route/open failure emits nothing.
3.3. Released `ItemKindCompaction` emits protocol-strict `completed` once.
3.4. Successful terminal explicit compact operation may complete if no earlier compaction item did so.
3.5. Retry/failover B-legs for one logical request shall not duplicate starts/transactions.
3.6. A configured server compaction threshold alone is not a compaction event.
3.7. Do not accept/drop currently unsupported `compaction_trigger`, `context_management`, or equivalent controls merely for detection.

## Requirement 4: Versioned Coding-Agent Signatures

4.1. Match canonical roles/items/text via shared `lipapi` traversal helpers, not wire DTOs.
4.2. Each rule has a stable versioned `RuleID` and a distinctive conjunction of shape/marker constraints; generic `summarize`/no-tools text alone never matches.
4.3. Initial rules cover Codex, Pi/OpenClaw, Cline agentic/basic-post, OpenCode default/custom, Hermes current/legacy post, KiloCode, the supplied March-2026 Claude Code snapshot, Gemini CLI, Roo Code, Aider, and Crush.
4.4. Indistinguishable harnesses shall not be assigned an invented identity.
4.5. Customizable prompt portions may vary if stable implementation-owned conjunctions remain.
4.6. Start signatures emit only after successful B-leg open.
4.7. Recognized installed-summary/post markers emit `completed` once and close the matching transaction.
4.8. Updating one signature shall require one focused rule/table/test change, not provider-adapter changes.

## Requirement 5: Conservative Local-Only Heuristic

5.1. Compare successive successfully opened requests only within one authoritative A-leg.
5.2. Require all: substantial prior context; material absolute and relative size reduction; at least two recent semantic tail fingerprints surviving in order; and meaningful older-history disappearance/replacement.
5.3. Ambiguous transitions emit nothing; precision is preferred over recall.
5.4. New A-leg, known reset/fork, or unrelated fresh short requests shall not match from token reduction alone.
5.5. Heuristic evidence may emit only `completed/history_heuristic`, never retroactive start.
5.6. Strict post evidence suppresses duplicate heuristic completion.
5.7. Size estimation is deterministic/local and performs no provider/network call.

## Requirement 6: Multi-Call Transaction Coalescing

6.1. Rules shall declare `single`, `series`, or `completion-only` behavior.
6.2. Pi/OpenClaw, Gemini, Aider, and other explicit series rules shall reuse one active A-leg transaction across matching utility subcalls and suppress repeated starts.
6.3. Transaction ID shall be stable for the logical compaction without random global mutable state.
6.4. Completion emits once even if several strict/post signals appear.
6.5. An ordinary request may close an unprovable series transaction silently; never fabricate completion.
6.6. If one request completes an old transaction and starts a new one, emit old `completed` before new `started`.
6.7. Stale transactions expire and cannot suppress later compactions indefinitely.

## Requirement 7: Bounded Private Process State

7.1. State shall be process-owned and keyed by authoritative A-leg ID so hot generation replacement does not lose it.
7.2. Do not persist detector state or change B2BUA/secure-session schemas.
7.3. Store only counts, timestamps, bounded SHA-256 semantic hashes, and transaction metadata; discard source text after matching/hashing.
7.4. Apply explicit max-entry/inactivity bounds with lazy eviction and no cleanup goroutine.
7.5. Concurrent turns/A-legs shall be race-safe with minimal synchronization.
7.6. Process restart may lose detection state without affecting session correctness.
7.7. Listener payloads and ordinary logs shall not include matched prompt excerpts.

## Requirement 8: Runtime Preservation and TDD

8.1. Write RED tests for the event contract, rule matrix, transaction behavior, and heuristic thresholds before implementation.
8.2. Do not modify `lipapi.Call`/`lipapi.Event` solely to expose observations.
8.3. Detection/listener failures shall not affect routing, failover/no-retry-after-output, completion gates, tool policy/reactors, billing/accounting, or secure-session outcomes.
8.4. Observe every canonical event actually released by `retryRecvStream`, including gate/finalizer/recovery drains, exactly once.
8.5. Prove a signature-looking request that never opens upstream emits no start.
8.6. Cover the surveyed rule matrix, near-miss negatives, series coalescing, strict/heuristic dedupe, expiry, concurrent A-legs, and generation-reload continuity.
8.7. Focused plus repository quality/architecture/race checks shall pass without network credentials.
8.8. Final review shall remove unnecessary framework/configuration/persistence/protocol-specific core parsing/background work.
