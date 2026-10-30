# Design Validation Review

## Review Method

The revised design was reviewed in three passes against:

- `.kiro/AGENTS.md`;
- API, routing, structure, testing, and design steering;
- current direct Codex and reasoning-preservation code;
- current OpenAI Codex source behavior;
- all acceptance criteria in `requirements.md`; and
- brownfield constraints introduced by PR #235;
- the approved default-on/explicit-opt-out policy and `CodexHarnessHeadroomV1` budget rules.

Each pass was allowed to return NO-GO. Findings were applied to the artifacts before the next pass.

## Round 1

### Assessment

**Decision: NO-GO**

The initial revision correctly added reasoning continuity but still treated its storage as potentially connector-owned and overemphasized cross-turn response-ID chaining.

### Critical Issue 1: Connector-local reasoning capture cannot identify the surfaced winner

**Concern:** The connector receives output before core failover/race/gate ownership is settled.  
**Impact:** Losing or swallowed attempt reasoning could be restored into a later request.  
**Resolution:** Automatic reasoning state remains owned by `reasoning-output-preservation`, whose final-stream observer commits only successful surfaced output. The connector owns only wire conversion and compacted checkpoints.  
**Traceability:** 3.1, 3.2, 10.13  
**Evidence:** `design.md` — Ownership Decisions; Reasoning Feature Integration.

### Critical Issue 2: Cross-turn `previous_response_id` was incorrectly elevated to the primary design

**Concern:** Codex CLI creates a fresh model client session per Codex turn and uses response IDs mainly for incremental WebSocket requests inside that turn.  
**Impact:** Designing durable correctness around response IDs would diverge from Codex's exact-item history and create expiry/store coupling.  
**Resolution:** Exact reasoning/compaction item replay is the durable baseline. Existing WebSocket continuation remains optional optimization; no automatic HTTP chain is added.  
**Traceability:** 8.1–8.9  
**Evidence:** `design.md` — Response-Chain and Transport Semantics.

### Critical Issue 3: Compaction could still consume an unaugmented client transcript

**Concern:** The connector could not prove the reasoning-preservation transform was eligible before compacting.  
**Impact:** A checkpoint could omit restorable private reasoning and lock in a lower-quality context window.  
**Resolution:** The eligible transform sets a bounded internal continuity marker. Required mode skips compaction without it. Compaction history is built after restoration.  
**Traceability:** 3.4, 3.10–3.11, 5.4, 6.2  
**Evidence:** `design.md` — Internal Continuity Marker; NativeContextCoordinator.

## Round 2

### Assessment

**Decision: NO-GO**

The ownership model was correct, but two request/history details remained underspecified.

### Critical Issue 1: Encrypted reasoning remained conditional on explicit effort

**Concern:** Current connector request construction asks for encrypted reasoning only when a reasoning object is already present.  
**Impact:** First turns or default-effort turns could produce no replay artifact even though continuity is enabled.  
**Resolution:** A continuity-marked attempt always sends a valid reasoning object and `reasoning.encrypted_content`; explicit effort overrides remain authoritative, otherwise model/default controls are used.  
**Traceability:** 2.1–2.3  
**Evidence:** `design.md` — RequestPolicy.

### Critical Issue 2: Normal no-tools projection could destroy native compaction history

**Concern:** Existing safe generation behavior converts historical calls/outputs to text when tools are absent.  
**Impact:** Compaction would lose causal action structure and reason over a degraded transcript.  
**Resolution:** Add a separate exact native history builder before normal projection. It preserves reasoning, structured calls, outputs, placements, and pair identities; invalid exact history bypasses compaction.  
**Traceability:** 4.2–4.7, 6.2  
**Evidence:** `design.md` — Exact Native History; ExactHistoryBuilder.

### Critical Issue 3: The retained-message policy was narrower than current Codex behavior

**Concern:** The original design retained only recent user context.  
**Impact:** Useful non-final agent state retained by Codex could be lost, reducing behavioral parity.  
**Resolution:** Adopt a bounded Codex-aligned predicate for user/developer/system and non-final agent items, with final-answer exclusion and per-agent cap.  
**Traceability:** 6.7–6.9  
**Evidence:** `design.md` — ReplacementBuilder.

## Round 3

### Traceability Review

- 113 acceptance criteria were enumerated.
- Every criterion has at least one implementation task.
- Every component maps to requirements.
- Every task names boundary, dependency, and validation.
- PR #235 functionality is treated as a characterized prerequisite rather than duplicated implementation.
- Default-on behavior, explicit opt-outs, and rollback are explicit.
- Quality evidence is required to report observed behavior, not to defer the approved default or claim unmeasured gains.
- Spark and GPT-5.x fallback ceilings, safe triggers, and override validation are explicit.

### Architecture Review

- Provider semantics stay in the connector.
- Surfaced reasoning ownership stays in the feature observer.
- No new canonical item kind or core routing branch is introduced.
- The internal marker uses the existing extension seam and is prevented from upstream serialization.
- Streaming remains primary.
- No retry occurs after visible output.
- Managed account isolation and response-chain reset are explicit.
- External API uncertainty is isolated behind pre-output fallback/cooldown and live tests; operators retain explicit opt-out.

### Safety Review

- No ciphertext logging or semantic inspection.
- Exact account/model/session binding.
- Marker spoofing addressed by deleting client-supplied value and setting it only internally.
- Full reasoning-complete history remains fallback.
- Connector-local losing-attempt capture is prohibited.
- Process-local bounds, cooldown, defensive copies, and close semantics are defined.

### Quality Review

- Four-mode ablation is specified.
- Coding-task quality metrics are defined.
- ARC findings are not extrapolated without evidence.
- One-time compaction cost and break-even are included.
- Quality/failure evidence is required to report observed behavior accurately; it is not a separate gate for the approved default-on policy.

## Design Strengths

- The design now mirrors Codex CLI's durable exact-item history rather than assuming response-ID persistence.
- It reuses mature winner-owned reasoning preservation and confines provider-specific compaction to the adapter.
- It separates exact compaction history from ordinary no-tools generation projection.
- It provides a reversible, explicitly opt-out rollout and a falsifiable quality evaluation.

## Final Assessment

**Decision: GO**

No critical requirement, ownership, ordering, security, transport, context-budget, or evaluation gap remains. The remaining unknowns concern live ChatGPT Codex endpoint behavior, and each has a bounded environment-gated test plus pre-output fallback and explicit opt-out containment.

## Implementation Gate

Implementation should proceed from the human-approved synchronized `requirements.md`, `design.md`, and `tasks.md` in `spec.json`. New or changed default-on/headroom tasks remain unchecked until implemented; completed historical checkboxes remain historical evidence. The first implementation work must characterize PR #235 behavior and add failing integration contracts before changing runtime behavior.
