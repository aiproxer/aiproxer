# Research

## Summary
This is an extension to an existing system (Anthropic-to-Anthropic pass-through). Discovery was light and focused on integration points: the Anthropic SDK surface for `SignatureDelta`, the canonical event contract extension points, and the Anthropic frontend thinking-block wire shape. No external architectural research was required beyond confirming the SDK and wire shapes already in use.

## Research Log

### Anthropic SDK surface
- `github.com/anthropics/anthropic-sdk-go@v1.55.0` exposes `anthropic.SignatureDelta` as a `ContentBlockDelta` variant (`message.go:5582`) with a `.Signature string` field, symmetric to `anthropic.ThinkingDelta` (`.Thinking`). The streaming `ContentBlockDeltaEvent.Delta.AsAny()` switch already handles `TextDelta`, `InputJSONDelta`, `ThinkingDelta`; adding `SignatureDelta` is a one-case extension.
- Implication: no SDK upgrade or workaround is needed; capture is straightforward.

### Canonical event contract extension points
- `pkg/lipapi/events.go` defines `Event`, `EventKind` constants, `ValidateEventSequence` (two call sites grouping content-class events that require `sawMessage`), `ValidateEventEnvelope` (size bounds), and `CollectWithLimits` (aggregation). `pkg/lipapi/output_commit.go` lists output-committing kinds.
- A new kind must be registered in both validators (the `default` rejects unknown kinds) and may be a no-op in `CollectWithLimits`. It must NOT be added to `OutputCommitted` (signature is metadata, not committing content).
- Implication: the change is additive and localized to `pkg/lipapi`.

### Anthropic frontend wire shape
- Real Anthropic streaming thinking block: `content_block_start` carries `{"type":"thinking","thinking":"","signature":""}`; `thinking_delta` events carry text; `signature_delta` (`content_block_delta` with `delta.type="signature_delta"`, `delta.signature`) arrives near the end; then `content_block_stop`.
- The current frontend emits only `type` on start and skips `signature_delta`. Adding `Thinking`/`Signature` fields on the start struct and a `signature_delta` emission in `closeThinkingBlock` matches the wire.
- Non-stream Anthropic (`WriteNonStreamJSON`) intentionally omits thinking blocks (parity test asserts reasoning must not appear), so the signature fix is stream-only.

## Architecture Pattern Evaluation
- Chosen: additive canonical carrier + adapter capture/emit. Rejected: overloading `EventReasoningDelta` with a `Signature` field (muddies text vs metadata and complicates collector reasoning aggregation). The new kind keeps semantics separate and lets non-Anthropic frontends ignore it via existing `default` switch cases.

## Risks
- Multiple interleaved thinking blocks: mitigated by resetting the per-block `thinkingSignature` on each `closeThinkingBlock`.
- Synthesized reasoning (non-Anthropic source): no signature event arrives, so no `signature_delta` is emitted; no fabrication.
- Contract drift: the new kind is registered in both validators and documented in `output_commit.go` so future `nextOutIdx`-style invariants stay explicit.
