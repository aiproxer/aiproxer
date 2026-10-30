# Requirements Document

## Introduction
The LLM Interactive Proxy translates Anthropic Messages API traffic through a canonical event stream. Extended-thinking responses include a cryptographic signature on each thinking content block that downstream clients must replay, unmodified, when continuing a multi-turn conversation with tool use. The proxy currently drops this signature end-to-end, so downstream Anthropic clients cannot validly replay thinking blocks and the Anthropic API rejects the continuation. This feature preserves the Anthropic thinking signature through the canonical event stream for streaming Anthropic-to-Anthropic pass-through, without fabricating signatures for reasoning synthesized from non-Anthropic backends and without changing non-streaming or non-Anthropic behavior.

## Boundary Context
- **In scope**: preserving the Anthropic thinking signature for streaming Anthropic-to-Anthropic pass-through; carrying the signature through the canonical event stream; emitting it on the downstream Anthropic streaming wire (the `signature` field on the thinking `content_block_start` and a `signature_delta` event before `content_block_stop`); per-block signature handling for multiple interleaved thinking blocks.
- **Out of scope**: non-streaming Anthropic responses (the non-stream path intentionally omits thinking blocks); fabricating or inventing signatures for reasoning synthesized from non-Anthropic backends; other providers' reasoning wire shapes; changing which thinking content is emitted.
- **Adjacent expectations**: relies on the Anthropic SDK exposing `SignatureDelta` on the upstream stream; relies on the canonical event sequence contract (content-class events require a started message frame); must not change output-commit/failover semantics or the no-retry-after-first-output invariant.
- **Boundary ownership**: backend plugin (Anthropic protocol adapter) + canonical contracts (`pkg/lipapi`) + frontend plugin (Anthropic). Not core orchestration.
- **Revalidation triggers**: streaming behavior, canonical event contract, capability negotiation, parity (Anthropic frontend x Anthropic backend).

## Requirements

### Requirement 1: Upstream signature capture
**Objective:** As an operator running Anthropic-to-Anthropic pass-through, I want the proxy to capture the thinking signature from the upstream Anthropic stream, so that it is not lost before encoding.

#### Acceptance Criteria
1. When the upstream Anthropic stream emits a signature delta for a thinking content block, the LLM Interactive Proxy shall capture the signature value.
2. When the upstream Anthropic stream emits a thinking delta followed by a signature delta, the LLM Interactive Proxy shall preserve both the thinking text and the signature.
3. The LLM Interactive Proxy shall not synthesize or invent a signature when the upstream stream provides none.

### Requirement 2: Canonical signature carrier
**Objective:** As a maintainer of the canonical event model, I want the signature to travel through the canonical event stream as a distinct signal, so that frontends can choose to emit it without overloading reasoning text.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall carry the thinking signature on the canonical event stream as a signal distinct from reasoning text.
2. When a signature signal is present on the canonical event stream, the LLM Interactive Proxy shall accept it only after a message frame has started.
3. The LLM Interactive Proxy shall bound the size of the signature signal on the canonical event stream.
4. The LLM Interactive Proxy shall not treat the signature signal as user-visible output content for failover commitment purposes.

### Requirement 3: Downstream signature emission (streaming)
**Objective:** As a downstream Anthropic client, I want the streaming response to include the thinking signature, so that I can replay thinking blocks in later turns.

#### Acceptance Criteria
1. When the proxy emits a thinking content block start on the downstream Anthropic streaming wire, the LLM Interactive Proxy shall include the `signature` field on that block.
2. When a signature was captured for a thinking block and the block is closing, the LLM Interactive Proxy shall emit a `signature_delta` event before the `content_block_stop` event for that block.
3. When no signature was captured for a thinking block, the LLM Interactive Proxy shall omit the `signature_delta` event for that block.
4. The LLM Interactive Proxy shall emit the `signature_delta` event on the same content block index as the thinking block it finalizes.

### Requirement 4: Multiple interleaved thinking blocks
**Objective:** As a downstream Anthropic client, I want each thinking block to carry its own signature, so that multi-turn extended thinking with interleaved tool use round-trips correctly.

#### Acceptance Criteria
1. When a response contains more than one thinking block, the LLM Interactive Proxy shall associate each signature with the thinking block it was captured for.
2. When a thinking block closes and a later thinking block opens, the LLM Interactive Proxy shall emit the `signature_delta` for the first block before opening the second and shall emit the second block's signature when it closes.

### Requirement 5: Isolation of non-Anthropic and non-stream paths
**Objective:** As an operator, I want only Anthropic streaming pass-through to be affected, so that other frontends and non-streaming responses behave unchanged.

#### Acceptance Criteria
1. Where a frontend is not the Anthropic streaming frontend, the LLM Interactive Proxy shall ignore the signature signal with no change to that frontend's wire output.
2. When encoding a non-streaming Anthropic response, the LLM Interactive Proxy shall not introduce a thinking block or a signature.
3. The LLM Interactive Proxy shall not change the reasoning text, tool call, or media content emitted by any frontend as a result of carrying the signature signal.

### Requirement 6: Canonical sequence validation
**Objective:** As a maintainer, I want the canonical event sequence contract to recognize the signature signal, so that streams containing it validate correctly.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall accept a canonical event stream that contains the signature signal after a started message frame.
2. If the signature signal appears before a message frame has started, the LLM Interactive Proxy shall reject the stream as invalid.
3. The LLM Interactive Proxy shall not accumulate the signature signal into non-streaming reasoning text aggregation.
