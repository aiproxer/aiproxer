# Research & Design Decisions

## Summary

- **Feature**: `openai-responses-reasoning-preservation`
- **Discovery Scope**: Extension of shipped `reasoning-output-preservation` into OpenAI Responses fidelity
- **Primary handoff**: `EchoesVault/daily/2026-07-19.md`
- **Historical specs (read-only)**: `.kiro/specs/archive/reasoning-output-preservation/`, `.kiro/specs/archive/reasoning-preservation-e2e-validation/`
- **Spec state**: artifacts generated; **not approved**; `ready_for_implementation: false`
- **Key Findings**:
  - Dialect ID `openai.responses.reasoning_item.v1` exists; stream capture cannot populate it from provider output.
  - `EventReasoningOpaqueDelta` is Anthropic-only; must not be overloaded.
  - Feature `observer` maps bare `EventReasoningDelta` to `openai.chat.reasoning_text.v1` — progressive Responses deltas would duplicate a Chat part beside a Responses exact part unless forbidden or presentation-tagged and ignored.
  - `Collected` retains only reasoning text today.
  - Mapper ignores Responses reasoning stream events; replay encoder still allows text/summary fallback.
  - Backend always streams; frontend nonstream collects the same canonical stream (no second BE nonstream path).
  - SDK `openai-go/v3 v3.43.0`: output `ResponseReasoningItem` uses `respjson.Field` presence; input `ResponseReasoningItemParam` uses `param.Opt[string]` for `encrypted_content` with `param.Null[string]()` for JSON null; `ToParam()` is unsafe for exact replay (fields may be absent).

## Research Log

### Gap vs shipped feature

- **Sources**: EchoesVault page, durable plan, feature plugin, parent/E2E specs.
- **Findings**: Chat/Anthropic paths largely proven; Responses missing ingest, FE fidelity, stateful harness, precise 2x2 expectations.
- **Implications**: Active spec required; parent matching/TurnStore/inert remain.

### SDK v3.43.0 presence and param mapping

- **Sources**: `go doc` on `responses.ResponseReasoningItem`, `ResponseReasoningItemParam`, `packages/param.Opt`, `param.Null`, `respjson.Field`.
- **Findings**:
  - Output fields: `id` required string; `summary` required array; `type` constant `reasoning`; `content` optional array; `encrypted_content` nullable string; `status` optional enum; presence via `JSON.<Field>`.
  - Input: `EncryptedContent param.Opt[string]` with `omitzero`; `param.Null[string]()` marshals JSON `null`; omit when unset; `NewOpt` for values.
  - `ToParam()` explicitly warns param fields may not be present — unsuitable for exact opaque replay.
- **Implications**:
  - Canonical store holds allowlisted Opaque JSON with semantic presence (absent/null/value).
  - Replay prefers adapter-owned raw JSON insertion of stored Opaque into request `input`, or explicit Opt/Null/omit mapping — never silent null↔absent coercion, never `ToParam()` for exact items.
  - If a stored presence form cannot be emitted, fail-closed unrepresentable/state_error (content-safe), not lossy rewrite.

### Progressive delta duplication hazard

- **Sources**: `internal/plugins/features/reasoningpreservation/observer.go` (`EventReasoningDelta` -> Chat text dialect).
- **Findings**: Emitting bare progressive deltas plus terminal exact part would store Chat text + Responses exact for one provider item.
- **Decision**: See design — presentation-only tagging or terminal-only emission; observer never captures Chat parts from Responses presentation deltas.

### Ordering / output_index

- **Context**: `output_item.done` timing vs later text/tool events.
- **Decision**: Key assembly by `output_index`; test provider/ref assumption that index i completes before index > i content; otherwise bounded reorder buffer; never rewrite already-emitted canonical events; unresolvable hole after downstream content commit => stream terminal error, no retry, no artifact.

### Failure timing vs retry

- **Context**: Parent invariant — no retry/failover after first output.
- **Decision**: After any content-class event is released downstream, envelope failure is stream-terminal + discard pending artifact — not candidate replacement language.

### Transform vs ParamsForCall

- **Sources**: feature `on_unrepresentable` / `on_state_error`; BE `ParamsForCall` after transform.
- **Decision**: Restore/transform owns dialect representability and envelope structural validation when injecting; invalid stored envelope => `on_state_error` (configured reject/log_skip) with content-safe errors. ParamsForCall remains last-line fail-closed if invalid reaches it; never summary fallback; never leak opaque bytes in client-visible errors.

### 2x2 matrix semantics

| Cell | Positive meaning | Negative / limits |
|------|------------------|-------------------|
| Chat FE / Chat BE | Same-dialect Chat text capture/restore (existing) | — |
| Responses FE / Responses BE | Exact Responses capture/restore/replay | — |
| Chat FE / Responses BE | Capture Responses exact from BE; Chat FE may not expose opaque to client; client typically omits reasoning; restore injects Responses exact for Responses BE; anchors from Chat-visible non-reasoning | Not a claim that Chat FE round-trips opaque items to the client |
| Responses FE / Chat BE | Positive only for **Chat** dialect captured from Chat BE if Responses FE can legally present Chat reasoning text (not as fake Responses exact item) | Responses-dialect artifact + Chat BE candidate => reject/log_skip (no conversion) |
| Cross-dialect route change | — | Dedicated reject/log_skip proofs; not counted as positive 2x2 cells |

### Nonstream

- Backend Open is always streaming; frontend nonstream = collect same canonical stream. Tasks must not invent a backend nonstream ingest path.

## Design Decisions (locked for approval review)

1. Exact opaque only; no summary/text fallback success.
2. Terminal dialect-aware exact-part event; do not overload Anthropic opaque delta.
3. Presence: semantic exactness in allowlisted Opaque JSON; adapter raw-JSON or Opt/Null/omit replay; fail-closed if unrepresentable.
4. Progressive Responses deltas are not bare Chat-capturable deltas (see design).
5. Ordering by `output_index` with buffer/assumption/fail-closed rules above.
6. Mixed 2x2 cells have asymmetric positive criteria; cross-dialect is negative-only.
7. Durability out of scope; unmatched complete no-op.

## Remaining human approval items (not ambiguity)

1. Approve requirements + design in `spec.json` (implementation still blocked).
2. Optional product preference: terminal-only emission vs presentation-tagged progressive deltas (both specified; implementation may start terminal-only).

## References

- Durable plan: `EchoesVault/daily/2026-07-19.md`
- SDK pin: `github.com/openai/openai-go/v3 v3.43.0`
- Code: `pkg/lipapi/events.go`, `pkg/lipapi/reasoning.go`, `internal/plugins/features/reasoningpreservation/observer.go`, `internal/plugins/backends/openairesponses/map_events.go`, `invoke.go`, `internal/plugins/frontends/openairesponses/`
