# Execute error classification (shared frontends)

Repository-wide error conventions: [`error-handling.md`](error-handling.md).

Bundled HTTP frontends map executor failures through [`internal/plugins/frontends/execerr`](../internal/plugins/frontends/execerr/execerr.go) to a small set: **reject** (4xx-class) vs **internal** (5xx-class). Protocol-specific status text and bodies stay inside each frontend adapter.

For **internal** (5xx) outcomes, the wire `Message` is a fixed, non-revealing string (`internal error`); the original error is retained on the `Outcome` for structured **server-side** logging. Frontends must log `out.Err` at error severity when present and must not echo arbitrary executor/upstream strings in JSON error bodies.

## Streaming responses: explicit in-band failures

Once execution opens a stream, the SSE adapters send final headers promptly.
OpenAI Responses publishes `response.created` immediately so clients can learn
the ID and cancel even if the backend never emits an event. Delaying the final
headers would also break clients with response-header deadlines; informational
1xx responses do not satisfy those deadlines.

Failures during receive or event encoding therefore retain HTTP 200 but emit a
classified, client-safe terminal error: Chat Completions sends an error data
frame followed by `[DONE]`, Anthropic sends `event: error`, OpenAI Responses
sends `response.failed` with the response ID and sequence number, and Gemini
sends a Google-shaped error chunk. The original error is returned to the
pipeline for error-level logging. This applies before content, after lifecycle
or usage events, and after partial output, including the OpenAI wire lane.

No successful finish is fabricated on failure. Disconnected/cancelled clients
and failed transport writes cannot be guaranteed delivery of an error frame.
Cleanup errors after a successful terminal are logged, not followed by a second
terminal. Pre-execution errors still use ordinary classified HTTP errors.

Terminal failure frames are written and flushed **before** synchronous backend
cleanup, so a blocked Close does not delay client notification. The pump still
closes exactly once and joins cleanup errors with the original failure.

Each adapter maps the classified outcome into its own vocabulary: for example,
429 preserves quota/rate-limit semantics, and Gemini uses RESOURCE_EXHAUSTED.
Responses resource-error codes are distinct from generic API error types: use
`rate_limit_exceeded` for 429 and the schema-valid `server_error` fallback for
generic refusals without an exact resource-error equivalent, retaining their
classified safe message. Never put `invalid_request_error` in ResponseError.code.

Pre-request keepalives use 102 only while Execute is pending. `net/http` sends
those headers immediately; flushing would prematurely commit a final 200.

## Richer shared kinds (deferred)

Future work may add more shared kinds (for example: cancellation, upstream unavailable, quota exhaustion, hook mutation) **without** moving protocol strings into `internal/core`. Any expansion should:

1. extend the shared classifier in `execerr` only,
2. keep wire encoding in `internal/plugins/frontends/<name>/`,
3. add regression tests per new kind before broad use.

No additional shared kinds are required for stage-three closure; this note records the decision to defer.
