# Research Notes

## Scope

The feature detects coding-agent session compaction from proxy-observable request/response flow. The primary source scan covers Codex, Pi, Cline, OpenCode, Hermes Agent, OpenClaw, KiloCode, and the supplied Claude Code source snapshot; Gemini CLI, Roo Code, Aider, and Crush are retained as additional coverage.

## Go-LIP Brownfield Findings

Go-LIP already has protocol-neutral compaction semantics: `lipapi.OperationContextCompaction` identifies explicit compact operations and `lipapi.ItemKindCompaction` represents compaction output. `lipapi.NormalizedItems`, `WalkCallItems`, and `WalkCallTexts` provide one canonical traversal path for signatures and fingerprints.

Secure request preparation resolves authoritative A-leg continuity before request transforms, pre-request handling, routing, and backend open. The effective `baseline` is therefore the correct request representation to analyze, but a start event should be emitted only after an upstream B-leg actually opens.

The final retry stream is the response integration point because it owns failover, hook/gate output selection, and final canonical events. Detector state that compares multiple turns must outlive generation reloads, so process-owned in-memory state keyed by authoritative `ALegID` is the appropriate lifetime.

Feature listeners should use a typed, non-mutating `pkg/lipsdk/compaction.Observer` rather than raw traffic observation. Events contain derived correlation/evidence only, never prompt bodies.

## Detection Evidence

Three evidence classes are sufficient:

1. `protocol_strict`: canonical protocol semantics directly prove compaction.
2. `signature_strict`: a versioned, deterministic conjunction of implementation markers identifies a compaction utility call or installed summary.
3. `history_heuristic`: a conservative same-A-leg history rewrite proves a likely local-only compaction when no strict signal exists.

Strict signatures must require multiple shape/role/text markers; generic words such as "summarize" or a no-tools request are never sufficient.

## Agent Rule Families

| Rule | Proxy-visible evidence | Mode |
|---|---|---|
| `protocol.context_compaction.v1` | opened explicit compact operation; released compaction item / successful terminal | single |
| `codex.local_checkpoint.v1` | `CONTEXT CHECKPOINT COMPACTION` request; stable later handoff prefix | single |
| `pi_openclaw.compaction_summary.v1` | context-summarizer system marker + `<conversation>` + structured checkpoint; fixed later `<summary>` carrier | series |
| `cline.agentic_compaction.v1` | continuation-note summary request; later `Context summary:` | single |
| `cline.basic_compaction_post.v1` | later `<SYSTEM_NOTICE>` + `Earlier context was compacted` | completion-only |
| `opencode.anchored_summary.v1` | `<conversation>` + anchored-summary instruction + Objective/Work State template | single |
| `opencode.custom_compaction_history.v1` | custom prompt followed by fixed `The following is the conversation history:` delimiter | single |
| `hermes.local_compaction_post.v1` | `[CONTEXT COMPACTION — REFERENCE ONLY]` | completion-only |
| `hermes.legacy_compaction_post.v1` | `[CONTEXT SUMMARY]:` | completion-only |
| `kilocode.anchored_summary.v1` | fixed Objective/Important Details/Work State/Next Move/Relevant Files template | single |
| `claude_code_2026_03.compaction.v1` | TEXT-ONLY/no-tools preamble plus detailed compaction sections; later continuation-from-previous-conversation prefix | single |
| `gemini_cli.state_snapshot.v1` | state-snapshot generation/verification sequence | series |
| `roo_code.condense.v1` | fixed summarization-only system operation + no-tools wording | single |
| `aider.chat_summary.v1` | fixed programming-summary prompt + `# USER/# ASSISTANT`; later previous-conversation prefix | series |
| `crush.session_summary.v1` | fixed context-preservation system prompt + required section family | single |
| generic local compaction | large same-A-leg rewrite with retained recent tail | completion-only heuristic |

A completion-only rule never invents a historical start. A series rule coalesces multiple utility requests into one logical transaction.

## Generic Heuristic

Only emit inferred completion when all conditions hold: same authoritative A-leg; substantial previous context; reduction of at least both an absolute and relative threshold; at least two recent semantic item hashes survive in order; and a meaningful older prefix disappears or is replaced. New/reset sessions and unrelated short requests must not match.

Stored fingerprints contain token/item counts, bounded SHA-256 semantic hashes, and timestamps only. Source prompt/tool-result text is discarded after hashing. State uses lazy TTL/max-entry eviction and no background worker.

## Important Compatibility Gap

Current Go-LIP supports compaction output and explicit compact operations, but its pinned OpenResponses request model does not currently represent Codex V2 `compaction_trigger`, and no current canonical carrier exists for Hermes `context_management` / `compact_threshold` controls. This spec does not accept those controls merely to detect them: doing so without forwarding their semantics would create a silent protocol downgrade. Their transport/capability support belongs in a separate compatibility change; the detector can add a canonical rule once such controls are represented correctly.

## Rejected Alternatives

- Agent hooks: violate proxy-only observation and require per-agent setup.
- Raw HTTP body matching: duplicates frontend parsing and creates protocol Cartesian products.
- LLM classifier: adds latency, cost, nondeterminism, and privacy egress.
- Durable detector database: disproportionate for ephemeral observability state.
- Injecting derived lifecycle into `lipapi.Event`: conflates upstream model events with proxy-derived observations.
