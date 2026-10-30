# Research and Brownfield Gap Analysis

## Summary

- **Feature**: `agent-loop-explicit-completion-protocol`
- **Discovery Scope**: Brownfield extension of `agent-loop-guard` plus the smallest generic runtime/SDK seam needed for a proxy-owned model control tool.
- **Key Findings**:
  - The existing ALG already treats normalized explicit completion as strong evidence and already recognizes `attempt_completion`; the new strategy should extend that fact model rather than create a second terminal system.
  - Naively appending a hidden tool through current request/tool-catalog hooks is unsafe because client `ToolChoice` remains authoritative and current tool policy executes before late reactors can swallow a tool event.
  - Current completion gates buffer the whole response until `response_finished`; using them for the preferred strategy would violate the repository's streaming-first product contract.
  - A valid design needs a narrow generic proxy-control-tool projection/interception seam at the candidate/response boundaries, while ALG remains the concrete policy owner and terminal-decision core remains the only terminal/continuation owner.
  - The initial product idea required repair for legitimate user-input stops: absence of `attempt_completion` cannot distinguish premature stop from an intentional request for user input. The repaired default is one hidden protocol-repair turn and then allow an unmarked stop rather than loop or silently invoke the legacy verifier.

## Brownfield Gap Analysis

### Baseline ALG policy and terminal platform

**Sources consulted**
- `.kiro/specs/archive/agent-loop-breach-prevention/{requirements,research,design,tasks}.md`
- `.kiro/specs/archive/terminal-decision-feature-extension/`
- `internal/plugins/features/agentloopguard/{config.go,provider.go,causepolicy,progress,verifier}`
- `internal/standardplugins/features_install.go`
- `pkg/lipsdk/terminaldecision`

**Findings**
- ALG is already a removable concrete provider behind the generic terminal-decision platform. It does not own terminal publication, B-leg opening, continuation settlement, or steering lifecycle.
- Current enabled ALG has one policy path: cause/safety classification, optional trusted explicit-completion bypass, otherwise detached semantic verifier, then progress/no-progress policy and a continuation intent.
- Current defaults are verifier role `loop_guard`, 4-second timeout, 3 semantic continuations, and no-progress limit 2.
- Existing configuration has no strategy selector. Therefore changing the meaning of `enabled: true` with no new field would silently change brownfield behavior.

**Requirement repair**
- Added explicit mutually exclusive strategies.
- Omitted strategy on an existing enabled configuration remains legacy `semantic_verifier` behavior.
- New examples/documentation prefer explicit `strategy: attempt_completion`; no hidden default flip is allowed.
- Strategy-specific contradictory fields must fail candidate generation validation rather than create an accidental hybrid.

### Existing explicit-completion canonical fact

**Sources consulted**
- `pkg/lipapi/explicit_completion.go`
- `internal/core/runtime/terminal_decision_evidence.go`
- `internal/plugins/features/agentloopguard/provider.go`

**Findings**
- Canonical explicit-completion aliases already include `attempt_completion` (primary) and `attempt_complete`.
- `lipapi.HasExplicitCompletion` intentionally requires a completed matching tool call and tool result; call-only/in-progress/malformed evidence does not count.
- ALG's current `trust` policy short-circuits clean-stop verification when the normalized explicit-completion fact is true.

**Implication**
- The preferred protocol should produce the same generic trusted completion fact from a proxy-local control action rather than add an ALG-only terminal flag.
- Proxy-owned provenance must remain separate from the visible tool name so a client-created same-name tool cannot be hijacked.

### Tool catalog and ToolChoice authority

**Sources consulted**
- `pkg/lipapi/call.go`, `pkg/lipapi/parts.go`
- `pkg/lipsdk/toolcatalog/filter.go`
- backend protocol encoders and `pkg/lipsdk/backendplugin/types.go`

**Findings**
- `Call.Tools` and `Call.ToolChoice` are canonical client/request semantics. Tool choice can be `auto`, `none`, `any`, a required named tool, and can carry an allowed-tools subset.
- `toolcatalog.Filter` is defined as a remove/annotate stage, not an authority for privileged tool injection.
- Backend/plugin invocation currently exposes one ordinary tool catalog and one ordinary tool-choice control; there is no sideband for model-visible/client-hidden control tools.
- Adding `attempt_completion` to a request with `none`, a forced client tool, or a constrained allowed set would broaden the model's choices and silently weaken client semantics.

**Requirement repair**
- Proxy-owned injection is eligible only when the final candidate supports tools and the client tool choice is compatible with additive hidden control capability. V1 is conservative: unset/default-auto or explicit auto with no incompatible allowed subset.
- Incompatible choices make the preferred protocol inactive for that candidate. They do not trigger the legacy verifier automatically.

### Response tool ordering and late-swallow risk

**Sources consulted**
- `internal/core/runtime/response_pipeline_observations.go`
- `internal/core/extensions/tool_policy.go`
- `internal/core/hooks/tool.go`
- `pkg/lipsdk/toolpolicy`, `pkg/lipsdk/hooks`

**Findings**
- Model tool lifecycle currently reaches tool policy before tool reactors.
- A reactor can swallow an event, but by that point ordinary policy has already observed the model-emitted tool.
- A proxy-owned local completion action must never be interpreted as an executable client tool or depend on every client tool policy allowing it.

**Design consequence**
- Interception must occur at a generic trusted proxy-control-tool seam before ordinary tool policy/reactors for a call whose ownership was established during candidate projection.
- Name matching alone is not authority.

### Whole-response completion gates are the wrong mechanism

**Sources consulted**
- `pkg/lipsdk/completion`
- `internal/core/runtime/response_pipeline.go` (`completionGatedEmit`)

**Findings**
- Current completion gates buffer canonical events until `EventResponseFinished` before running the gate chain, subject to an overflow fail-live path.
- Installing such a gate for every preferred ALG turn would delay ordinary text/tool streaming and conflict with the product steering that streaming is primary.

**Decision**
- Rejected. The new mechanism may buffer only the proxy-owned control call/arguments and a pending completion result; ordinary response events continue streaming.

### Session opener and conversation-view steering

**Sources consulted**
- `pkg/lipsdk/session/opener.go`
- `docs/conversation-view.md`
- `pkg/lipsdk/steering`, `internal/core/conversationprojection`

**Findings**
- `session.Opener` returns session-label upserts only; it is not a model-instruction channel.
- Persistent steering is an established client-hidden/model-visible mechanism, but using durable A-leg state merely to remember a static completion rule is unnecessary state/lifecycle cost.
- Remote LLM APIs are request-based. A rule registered “once at session start” still has to be materially present in each backend-effective request unless the upstream provider itself owns durable session instructions.

**Requirement repair**
- Interpret the product intent as one logical session rule, not one physical one-shot payload. V1 materializes the same byte-stable control instruction on each eligible candidate B-leg without adding it to conversation history or accumulating copies.
- No new durable session table/state is introduced solely for this rule.

### Item-authoritative versus legacy call authority

**Sources consulted**
- `pkg/lipapi/call.go`
- `docs/conversation-view.md`

**Findings**
- `Call` supports legacy message/instruction authority and item authority; item-authoritative calls reject simultaneous legacy `Messages`/`Instructions`.
- Therefore feature code must not directly append a legacy `Instruction` blindly.

**Design consequence**
- The generic control-tool projection stage must inject the model instruction in an authority-neutral way, using canonical projection helpers that preserve either request authority and never create conflicting representations.

### Legitimate user-input stop is indistinguishable from missing completion by marker alone

**Context**
The original idea says: if a terminal appears without the completion tool, suppress it and tell the worker either to continue or call the completion tool. The protocol instruction also correctly says not to assume missing user input/permission.

**Gap**
A worker that correctly asks the user a question must terminate without `attempt_completion`; marker absence alone cannot tell that apart from a premature stop. A pure marker protocol cannot independently verify semantics without reintroducing the semantic verifier.

**Alternatives considered**
1. Add an `ask_followup_question` control tool as a second protocol primitive.
2. Add a status field to `attempt_completion`.
3. Use prose/question-mark heuristics.
4. Fall back to the semantic verifier.
5. Allow one protocol-repair turn, then allow a second unmarked stop.

**Selected repair**
- V1 keeps the familiar one-tool ABI unchanged and selects option 5.
- Default `max_protocol_reprompts = 1`.
- First eligible unmarked terminal receives the hidden protocol-repair continuation.
- If that continuation again ends unmarked, the default policy allows the stop. The worker therefore gets one chance either to continue real work, report completion correctly, or restate a legitimate user-input request.
- Operators may select a small bounded value greater than one, with immutable total cap and no-progress protection.
- There is no implicit semantic-verifier fallback in this strategy because the user explicitly requires only one strategy to be active.

### Adjacent active specification overlap

**Sources consulted**
- `.kiro/specs/b-leg-path-virtualization/design.md`
- `.kiro/specs/large-payload-streaming-fast-path/`
- `.kiro/specs/high-concurrency-performance-hardening/`

**Findings**
- `b-leg-path-virtualization` is already implementation-ready and changes candidate request shaping plus completed tool-call finalization immediately before ordinary tool policy/reactors.
- The explicit-completion protocol needs the same broad neighborhood but a different responsibility: proxy-owned tool projection and early interception.
- Large-payload work makes it especially undesirable to introduce whole-response buffering.

**Implication**
- Implementation tasks include a mandatory current-main revalidation of candidate-open and tool-call ordering after any of those specs land. The design does not freeze today's exact line/file layout as permanent architecture.

## External Prior Art and Model-Familiarity Research

### Cline

**Sources**
- `https://github.com/cline/cline/blob/main/apps/vscode/src/core/prompts/responses.ts`
- `https://github.com/cline/cline/blob/main/sdk/ARCHITECTURE.md`

**Findings**
- Cline's established prompt tells the worker: if the user's task is complete, use `attempt_completion`; if more information is needed, use the user-input path; otherwise continue working.
- Current Cline SDK uses `submit_and_exit` but explicitly documents it as the SDK analog of original Cline's `attempt_completion`.

### Roo Code

**Source**
- `https://github.com/RooCodeInc/Roo-Code/blob/main/src/core/tools/AttemptCompletionTool.ts`

**Findings**
- Current Roo still defines `AttemptCompletionTool` with exact tool name `attempt_completion`.
- Historical/current interface has required `result: string` and optional `command?: string`.
- The implementation treats `result` as completion-result content and completion as a task lifecycle signal.

### Kilo Code

**Sources**
- `https://github.com/Kilo-Org/kilocode/blob/main/packages/kilo-vscode/src/legacy-migration/sessions/lib/parts/parts-util.ts`
- Kilo legacy session/migration fixtures in the current repository.

**Findings**
- Kilo migration code recognizes legacy `tool_use` entries named `attempt_completion` and preserves `input.result` as completion text.

### Selected model-facing ABI

The common high-value prior is the name plus `result` field, not Roo's optional shell-command convenience. AIProxer therefore freezes:

```json
{
  "type": "function",
  "name": "attempt_completion",
  "description": "Call this only when all work requested by the user for the current task is complete. The result must concisely summarize the completed work. Do not call this while requested work remains that you can continue without additional user input.",
  "parameters": {
    "type": "object",
    "properties": {
      "result": {
        "type": "string",
        "description": "Concise final result summarizing the work that has been completed."
      }
    },
    "required": ["result"],
    "additionalProperties": false
  }
}
```

The hypothesis that model familiarity improves compliance is plausible and testable, not an assumed training guarantee. The implementation should compare protocol compliance telemetry/fixtures against renamed-tool controls if maintainers later want empirical confirmation; the product contract does not depend on claiming any particular provider's training corpus.

## Architecture Pattern Evaluation

| Option | Strengths | Risks / Limitations | Verdict |
|---|---|---|---|
| Keep verifier only | Already implemented and conservative | Extra inference latency/cost; semantic disagreement; does not exploit explicit finish prior | Preserve as legacy strategy |
| Replace verifier with prose-only “continue” | Very small | No explicit completion evidence; scope invention/loops | Rejected |
| Append `attempt_completion` in request transform/tool catalog | Reuses existing planes | Can violate ToolChoice; no trusted provenance; item-authority concerns | Rejected |
| Inject tool then swallow in ordinary reactor | Minimal response change | Ordinary tool policy sees it first; client policy may deny/execute semantics | Rejected |
| Completion gate converts tool result | Existing replacement API | Buffers whole response to terminal | Rejected |
| Persistent steering store for static rule | Existing hidden/model-visible lifecycle | Unnecessary durable state and mutation lifecycle for a fixed per-B-leg rule | Rejected for V1 |
| Preferred tool + semantic verifier fallback | Potentially strongest recall | Violates mutually-exclusive-strategy requirement; complex attribution | Rejected |
| Generic candidate-local control-tool seam + ALG protocol strategy | Explicit, streaming-preserving, removable, no normal auxiliary call | Requires narrow SDK/runtime extension and careful ToolChoice/capture rules | Selected |

## Design Decisions

### Decision: Strategy enum with brownfield-safe implicit legacy behavior

- **Context**: Current configs have only `enabled` plus verifier fields.
- **Selected approach**: add `strategy: attempt_completion | semantic_verifier`; explicit new guidance uses `attempt_completion`; omitted strategy on existing enabled config resolves to `semantic_verifier`.
- **Rationale**: preferred future behavior without a silent runtime migration.
- **Trade-off**: the recommended strategy is not the implicit behavior of old configuration.

### Decision: No hybrid fallback

- **Context**: A verifier fallback could handle ambiguous user-input cases.
- **Selected approach**: strategy selection is exclusive. Preferred mode never invokes the semantic verifier; legacy mode never injects the proxy control tool.
- **Rationale**: matches the explicit product requirement and avoids odd cross-policy loops/cost attribution.

### Decision: One missing-signal repair by default

- **Context**: marker absence cannot distinguish unfinished work from a legitimate user-input stop.
- **Selected approach**: one hidden reprompt by default; a second unmarked stop is allowed.
- **Rationale**: gives the worker a self-correction opportunity while keeping the protocol bounded without a second classifier.
- **Trade-off**: a worker can still abandon work after the repair turn; this is an intentional false-negative bias rather than an unbounded hidden loop.

### Decision: Candidate-local, non-accumulating projection

- **Context**: upstream APIs are request based, and direct one-time prompt mutation is not durable remote state.
- **Selected approach**: the generic seam projects the same stable instruction/tool onto each eligible backend-effective candidate call; it never appends copies to A-leg history.
- **Rationale**: deterministic, reload-safe, cache-friendly, no new persistence.

### Decision: Conservative ToolChoice eligibility

- **Context**: proxy injection must not broaden client restrictions.
- **Selected approach**: V1 proxy-owned injection activates only under default/auto tool choice with no incompatible allowed subset and a tool-capable candidate.
- **Rationale**: explicit capability failure is better than silent semantic degradation.

### Decision: Trusted provenance, not reserved-name authority

- **Context**: clients may already expose `attempt_completion`.
- **Selected approach**: request-local preparation records whether a specific definition is proxy-owned. Only calls correlated to that prepared capability are locally consumed.
- **Rationale**: avoids tool-name hijacking and preserves native Cline/Roo/Kilo clients.

### Decision: Reuse normalized explicit-completion evidence

- **Context**: terminal decision already understands `ExplicitCompletion`.
- **Selected approach**: a valid proxy-local completion action contributes an internal correlated completion fact to terminal evidence; no client-visible synthetic tool exchange is required.
- **Rationale**: minimizes terminal-decision schema/policy churn and keeps one completion vocabulary.

### Decision: Surface `result` only when it does not duplicate committed assistant text

- **Context**: Cline/Roo treat `result` as the final answer, but an arbitrary model may emit prose before the completion call.
- **Selected approach**: if no meaningful assistant text committed, hold the local result as pending final assistant text and release it through the normal canonical response owner if the terminal is accepted. If text already committed, use the result as completion evidence/summary and do not duplicate it automatically.
- **Rationale**: preserves streaming and no-rollback behavior.

## Risks and Mitigations

- **Model fails to use familiar tool** — one bounded hidden repair, telemetry, legacy verifier remains separately selectable.
- **Model falsely self-certifies incomplete work** — explicit completion is self-attestation, not independent proof; operators needing independent semantic checking can select the legacy strategy. Do not run both concurrently.
- **Legitimate user-input request gets reprompted** — default exactly one repair then allow unmarked stop.
- **ToolChoice semantic corruption** — strict activation eligibility; never broaden `none`, forced tool, or allowed subset.
- **Client same-name tool collision** — provenance-based ownership; never intercept by name alone.
- **Streaming regression** — no whole-response completion gate; only control-call fragments/pending result are held.
- **Control call reaches client** — intercept before ordinary tool policy/reactor/client release; architecture tests prove non-leakage.
- **Malformed/oversized args** — bounded control-call assembler and strict one-field JSON parser; never fall through to client execution.
- **Post-commit failure** — preserve existing no-replay/no-failover rule.
- **Adjacent spec drift** — implementation starts with current-main seam revalidation, especially `b-leg-path-virtualization`.
- **Prompt-cache churn** — fixed tool definition and byte-stable instruction; no per-turn dynamic data in the base prefix.

## References

- `.kiro/specs/archive/agent-loop-breach-prevention/` — existing ALG policy and invariants.
- `.kiro/specs/archive/terminal-decision-feature-extension/` — generic terminal/continuation platform.
- `docs/conversation-view.md` — A-leg versus B-leg visibility and stable hidden-content principles.
- `.kiro/steering/product.md` — streaming-first, canonical, small-kernel product contract.
- `.kiro/steering/routing-and-orchestration.md` — output commitment/recovery and terminal ownership.
- `.kiro/steering/structure.md` — feature/core placement rules.
- `.kiro/steering/testing.md` — TDD and extension/runtime certification requirements.
- `.kiro/specs/b-leg-path-virtualization/design.md` — active adjacent candidate/tool-finalization work requiring revalidation.
- Cline, Roo Code, and Kilo Code upstream sources listed above — `attempt_completion` prior art.
