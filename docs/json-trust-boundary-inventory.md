# JSON Trust-Boundary Inventory

This inventory records the first implementation wave for issue #160. It classifies JSON materialization by provenance and first resource boundary, rather than requiring every `encoding/json` call to use one wrapper.

## Policy Owners

| Boundary | Owner | Required order | Policy |
| --- | --- | --- | --- |
| Public inference HTTP request | `internal/plugins/frontends/frontendpipe` + `internal/core/jsonshape` | bounded body read -> shape preflight -> decode admission -> protocol decode | `RequestEnvelopeLimits`; duplicate names remain accepted for wire compatibility |
| Tool schema | `internal/plugins/features/toolcallrepair/repair` + `internal/core/jsonshape` | schema byte/shape preflight -> schema compilation/materialization | `ToolSchemaLimits`; duplicate names rejected |
| Tool arguments | `internal/plugins/features/toolcallrepair/repair` + `internal/core/jsonshape` | argument byte/shape preflight -> ordered materialization/repair | `ToolArgumentsLimits`; duplicate names rejected |
| Protected admin mounts | `internal/jsonbody` + the mounted handler | bounded body read -> shape preflight (exactly one document) -> typed decode -> domain validation | 64 KiB; trailing documents rejected; payload-safe `400`/`413` mapping |
| Protected token accounting | `internal/jsonbody` + token accounting adapter | bounded body read -> shape preflight (exactly one document) -> typed decode -> application service | configured cap, default 1 MiB; unknown fields remain allowed for the rich canonical call contract; canceled requests return 408 `request_canceled` without calling the service |
| Protected keepwarm mutation | `internal/jsonbody` + keepwarm adapter | bounded body read -> shape preflight (exactly one document) -> typed decode -> policy service | configured cap, default 64 KiB; empty body and connection-died wrapped-EOF remain accepted for the existing command contract |
| Protected route override | routeoverride adapter | bounded body read -> typed decode -> exactly one document -> domain validation | existing handler policy retained; it remains the reference for strict fixed-shape admin JSON |
| OpenResponses request | `internal/plugins/protocols/openresponses` + `internal/core/jsonshape` | request byte limit -> shared shape preflight -> protocol typed/raw decode -> semantic validation | OpenResponses depth and request-size settings; duplicate names rejected; request-size/depth/shape overflows map to structured `limit_exceeded` (`request_size`=413, `item_depth`/`request_shape`=400); extension semantics remain protocol-owned |
| OpenCode non-stream Anthropic/Gemini response | connector-local `readNonStreamResponse` | `max+1` bounded read -> typed decode | 8 MiB successful-response cap; error snippets remain separately capped at 4 KiB |
| OpenCode SSE responses | connector-local `decodeSSEFrame` + scanner | bounded line scanner -> per-frame byte cap -> per-frame decode | scanner line cap `maxSSEFrameLineBytes` (prefix + payload cap + LF/CRLF terminator); 1 MiB payload cap with `errSSEFrameTooLarge`; the stream is not preflighted as one document |

## First-Wave Findings

### Fixed

- Protected admin command bodies no longer use an unbounded `json.Decoder(r.Body)`. They are bounded, shape-preflighted, and reject trailing JSON before the mounted handler mutates state.
- Token accounting and keepwarm use the shared HTTP JSON policy while retaining their deliberate unknown-field and empty-body compatibility choices. Token accounting decodes with the request context (no offline-decode seam); canceled requests short-circuit to 408 `request_canceled`.
- OpenResponses no longer owns a second generic token walker for UTF-8, duplicate names, depth, or trailing data. Those checks use `internal/core/jsonshape`; protocol-specific discriminators and semantic validation remain local. Wire-mapping deltas are intentional and covered by `errors_mapping_test.go`: depth overflows are now structured `item_depth` `LimitExceededError` (wire `code: limit_exceeded`) instead of a plain invalid-request, and envelope shape-limit hits (tokens/string/key/number) map to param `request_shape` (previously accepted); `request_size` keeps its existing 413 `payload_too_large_error` mapping.
- OpenCode successful non-stream Anthropic and Gemini response bodies are read with an 8 MiB `max+1` bound before typed materialization.
- OpenCode SSE frames are decoded under a named 1 MiB per-frame cap (`decodeSSEFrame` / `errSSEFrameTooLarge`) instead of an implicit scanner limit; both streams fail loudly on violation instead of skipping or materializing the frame.

### Explicitly not migrated mechanically

- Public frontend request decoders: their shared pipeline and architecture call-order gate already establish the correct boundary.
- Nested `json.RawMessage` values whose parent is already bounded and preflighted, unless the nested value has a stricter contract.
- Secret redaction and other feature-specific JSON transformations: their recursive materialization is domain behavior, not a generic structural guard.
- Tests, fixtures, embedded data, database-owned state, and trusted/offline configuration paths.
- Executable connector modules importing `internal/core/jsonshape`: connector isolation is preserved; connector-local or `connector-support` bounds are used instead.

## Audit Classification Rules

A future JSON materialization site should be assigned one of these classifications before changing it:

- `safe-by-parent`: the first trusted boundary already imposes the applicable byte/shape policy;
- `needs-byte-cap`: a provider, subprocess, or HTTP body can be buffered without a hard cap;
- `needs-shape-preflight`: generic structure is materialized before the applicable bounded profile;
- `needs-contract-strictness`: byte/shape bounds exist but exactly-one-document, unknown-field, duplicate-key, or semantic policy is accidental;
- `intentional-transform`: the decoder is part of a domain-specific redaction, normalization, or protocol transformation;
- `trusted/offline`: the source is controlled and has a separate corruption/configuration policy.

The inventory is intentionally not a count of `json.Unmarshal` calls. A decoder is only a security gap when its provenance and resource boundary demonstrate one.

## Verification Evidence

Focused regression coverage lives in:

- `internal/jsonbody/jsonbody_test.go`;
- `internal/stdhttp/admin/tokenaccounting/handler_test.go`;
- `internal/stdhttp/admin/keepwarm/handler_test.go`;
- `internal/plugins/protocols/openresponses/phase7_quality_gate_test.go`;
- `connectors/opencode/internal/upstream/json_response_test.go`;
- `connectors/opencode/internal/upstream/sse_frame_test.go`.

Architecture ownership is checked by `internal/archtest/json_trust_boundary_test.go`. The inventory should be extended when new public/admin/provider/connector JSON boundaries are introduced; it is not a reason to add a repository-wide JSON wrapper.
