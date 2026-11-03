# Research & Design Decisions: claude-agent-model-bridge

## Summary

- **Feature:** `claude-agent-model-bridge` (first consumer of issue #822: S1 text + S2 deferred tool loop).
- **Discovery Scope:** Extension of the optional backend-plugin system; no new research assignment. Sources below are the owner-approved handoff and pinned upstream references.
- **Key Findings:**
  - `claudeagentacp` is an autonomous agent runtime; its process pool, permission policy, ACP adapter and `agent_runtime` export posture are reference only. This connector must not reuse ACP session semantics.
  - The intended continuation split is transport-level: an HTTP response can end at a tool-call boundary while the connector-owned SDK query remains parked. `ForwardExecute`'s stream close must therefore be decoupled from process retirement.
  - The SDK's MCP invocation does not provide a trustworthy tool-use ID on its own; PreToolUse correlation plus one outstanding call is the V1 safe envelope. OpenCode's single mutable `next` ID is explicitly not copied.

## Research Log

### Owner handoff and baseline

- **Context:** Issue #822 is the owner-approved GO and contains the normative architecture/algorithm and slice/acceptance handoffs.
- **Sources Consulted:** issue #822 body and both attached comments; baseline `aiproxer/aiproxer@6c1f14d338d9c5e1ae3ca61de9d702eaa1ee1515` (2026-10-31).
- **Findings:** New optional connector `connectors/claudeagentmodel`; preserve `claudeagentacp`, `claude_code_hybrid` and essential tables; no public ABI expansion; each slice is a separate bounded PR; SDK API mismatch must be reported precisely rather than worked around.
- **Implications:** The spec is scoped to S1/S2 only. Responses/Gemini continuation, durability, multi-user, parallelism and certification are deferred.

### Upstream reference implementation

- **Context:** The owner handoff supplies verified OpenCode source paths so implementation does not repeat upstream research.
- **Sources Consulted:** OpenCode commit `f29e281322b35b525e337d12e84aedc7e129fd6d` (merged 2026-10-30, PR #58782, v2.2.3), especially `claude/query-options.ts`, `tools/server.ts`, `tools/hooks.ts`, `tools/names.ts`, `claude/session.ts`, `sync/fingerprint.ts`, `language-model/convert-claude-stream.ts`.
- **Findings:** Isolated `settingSources: []`/`tools: []`/MCP allowlist/`verbatimPrompts`; in-process MCP schema publication and a deferred pending invocation; PreToolUse ID capture and PostToolUse result correction; stable 64-character name+hash mapping; parked native process and transcript matching.
- **Implications:** Reuse the algorithms, not the wire types or the single mutable pending-ID shortcut. The connector keeps its own per-conversation session manager and canonical event mapping.

### Runtime and dependency identity

- **Context:** S1 must pin an independently tested SDK/CLI pair; ACP's versions are not certification for this connector.
- **Sources Consulted:** Local host observation: Node `v22.23.4`, npm `10.9.9`, Claude Code CLI `2.1.311`; npm registry latest `@anthropic-ai/claude-agent-sdk` `0.3.316`; ACP research baseline SDK `0.3.293` / CLI `2.1.293` from the handoff.
- **Findings:** The host can build and test a sidecar, but the exact SDK/CLI pair and its discovery/MCP API must be characterized in S1 against the locked package.
- **Implications:** `package-lock.json` pins the closure. If the pinned SDK differs from the assumed options or discovery API, S1 stops with a precise failing characterization instead of inventing options.

### Documentation and policy references

- **Sources Consulted:** `VISION.md`, root `AGENTS.md`, `.kiro/steering/delivery.md`, `docs/adr/0008-hybrid-backend-connector-plugins.md`, `docs/backend-plugins/{authoring,operator,unsupported}.md`, `docs/claude-agent-acp-backend.md`, `docs/claude-code-hybrid.md`, Anthropic legal/compliance guidance.
- **Findings:** Optional backends are closed manifest executables; agent-runtime exports are excluded from default multi-backend composition; local login use is personal/single-user; OAuth capture/forwarding is forbidden.
- **Implications:** Manifest posture is `agent_runtime`/`local_only`/`per_instance`/`experimental`; broad subscription claims require maintainer legal sign-off independent of engineering.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|---|---|---|---|---|
| Reuse ACP connector | Extend `claudeagentacp` with a model mode | Reuses process/credential code | Couples autonomous ACP semantics to raw inference; wrong ownership | Rejected by handoff |
| In-process Go SDK | Call an Anthropic Go SDK directly | No Node child | No official Claude subscription/Agent SDK login path; leaks provider SDK into core | Rejected |
| Private Node sidecar | One pinned SDK child per conversation, connector-owned | Official unmodified SDK, native login, isolated MCP | Process lifecycle and IPC complexity | Selected |
| Global sidecar | One shared SDK child with a session multiplexer | Fewer processes | Global tool-ID/pending state, cross-session risk | Rejected |

## Design Decisions

### Decision: Connector-owned parked session separate from the HTTP stream

- **Context:** `ForwardExecute` closes the upstream event stream when the downstream attempt ends; a tool-call response must not terminate the native SDK turn.
- **Alternatives Considered:** Keep the HTTP reader open (breaks protocol); copy the query into a global process (mixes sessions); kill and replay (loses native state and may duplicate effects).
- **Selected Approach:** The engine/session manager owns the sidecar and native turn. The managed event stream is a per-HTTP reader; `Close()` detaches only the reader. Explicit cancellation/error/TTL/shutdown retires the turn.
- **Rationale:** Preserves session continuity without fabricating history or re-executing tools.
- **Trade-offs:** More state and lifecycle tests; V1 does not survive proxy restart.
- **Follow-up:** Verify `Close()`-while-parked on the real pinned SDK in S2.

### Decision: PreToolUse correlation with one outstanding call

- **Context:** The SDK MCP call may not carry the native `tool_use_id`.
- **Alternatives Considered:** Copy OpenCode's single mutable `next` pointer (unsafe without proof); infer by name/order (ambiguous); global registry (cross-session risk).
- **Selected Approach:** PreToolUse creates exactly one pending entry with native ID/name/args digest and validates the current step binding before the handler waits. A second simultaneous call is an explicit unsupported-parallel failure.
- **Rationale:** Honest V1 capability without guessed correlation.
- **Trade-offs:** No parallel tool calls until a verified per-call channel exists.

### Decision: Stateless continuation requires ID plus ordered prefix proof

- **Context:** Chat/Anthropic preserve tool IDs; Gemini does not, and a bare ID is not authority.
- **Alternatives Considered:** Trust `x-session`/User-Agent (spoofable); match by tool name (ambiguous); replay the full transcript into a new query (loses native state, potential duplication).
- **Selected Approach:** Prefer host-validated proxy-owned session ID; otherwise require exact opaque ID, unique pending candidate, matching model/route/tool snapshot and ordered visible-prefix fingerprint. Gemini/Responses matching is deferred to S3.
- **Rationale:** Prevents cross-session resumption and duplicate release while keeping Chat/Anthropic in scope.
- **Trade-offs:** Stateless clients with changed/omitted history receive an explicit refusal, not a guess.

### Decision: Capability honesty over breadth

- **Context:** The SDK can expose autonomous-agent surfaces and fallback behavior that are not part of the client contract.
- **Selected Approach:** Native tools/agents/hooks/MCP discovery are disabled; only caller tools are exposed; `Tools` flips true only after S2 proof; reasoning/vision/structured output/parallel capabilities stay false; fallbacks and supersedes are visible.
- **Rationale:** VISION prohibits fabricated history, leakage and hidden degradation.
- **Trade-offs:** Narrower V1 and explicit rejections for unsupported semantics.

## Risks & Mitigations

- SDK/CLI API drift from pinned reference — characterize the locked package in S1 and stop on mismatch.
- Orphaned children or leaked credentials — one owned process tree, inherited stdio only, env allowlist, redacted bounded stderr, secret canaries.
- Duplicate or cross-session tool release — one pending entry, exact ID/prefix proof, atomic single release, duplicate/mismatch tests.
- Hidden native execution — `tools: []`, empty skills/hooks, strict MCP config, fixture proving `Read`/shell denial.
- Subscription-policy misunderstanding — disabled experimental example, explicit local single-user wording, maintainer legal sign-off before a broad claim.
- Restart/compaction losing native state — explicit replay/restart degradation or strict reject; durability deferred.

## Delivery evidence

- **Owner waiver for S2 tasks 6–10 (explicit decision).** On 2026-11-01 the owner was asked: **“Do you authorize accepting current-head verification in lieu of historical RED for S2 tasks 6–10 at the final reviewed head?”** and answered: **“Authorize S2 RED waiver.”** This is an explicit, attributable owner decision, not an inference from the general instruction “Fix all discovered issues then re-review.” The content-only tool-result contract recorded in `requirements.md` 3.7 and `design.md` (“Deferred Tool Correlation”) is part of the same reviewed direction.
- **S2 historical RED exception.** The S2 implementation predates the review workflow's implementation-artifact requirement, so no historical RED receipts exist for tasks 6–10 and none may be reconstructed retroactively. Accepted evidence is the current-head connector suite, the Node sidecar suite (including the real production-wiring ordering/release and real MCP root-schema characterizations), the staged host/frontend/gRPC acceptance proof, and the panel's own probes at the reviewed code head. Later slices keep the normal RED-then-GREEN artifact requirement.
- **Reviewed and accepted code head:** `16ef259f8851e31110a14db1a4848e923a5af82b` (the round-4 repair commit that closes R4-J1 and pins the earlier evidence commit). This document-only evidence commit records the explicit owner decision after that code head; it changes no connector code, and the code under acceptance is exactly `16ef259f`. The earlier `d0eba491` and `730f29dab` entries remain historical review inputs, not the accepted code head.

## References

- Issue #822: owner-approved GO, architecture/algorithm handoff (1/2), tasks/acceptance handoff (2/2).
- OpenCode `f29e281322b35b525e337d12e84aedc7e129fd6d`, PR #58782, v2.2.3.
- `pkg/aipsdk/backendplugin/interfaces.go`, `forward_execute.go`, `call_bridge.go`.
- `docs/adr/0008-hybrid-backend-connector-plugins.md`; `docs/backend-plugins/authoring.md`; `docs/backend-plugins/unsupported.md`.
- `docs/claude-agent-acp-backend.md`; `docs/claude-code-hybrid.md`.
- Anthropic Claude Code legal and compliance guidance: https://code.claude.com/docs/en/legal-and-compliance
