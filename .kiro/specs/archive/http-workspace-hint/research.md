# Research

## Summary
Stock HTTP has no canonical workspace carrier. The authorized live workspace proof needs a narrow shared ingress seam, not arbitrary client extensions or a connector workaround. User authorized both remaining blocker fixes and resuming task 3.2; independent design review approved this slice before implementation.

## Existing Owners
- `pkg/lipsdk/httpheaders.go`: explicit LIP header aliases/defaults; `internal/core/config/http_headers.go` validates aliases, but existing validation is intra-field only. The new workspace field needs cross-field collision checks.
- `sessionwire/session_input.go`: neutral session-carrier owner; stock ordinary decoders carry headers in `DecodeOptions`, bounded profiles in existing `frontendpipe.ProofInput.Headers`.
- `connector-support/acp/workspace.go`: already recognizes `workspace_path`, owns native filesystem validation and precedence.
- `connector-support/acp/runtime_pool.go`: `RuntimeKey` includes workspace, model and client session. No new pool state is needed.
- `pkg/lipsdk/workspace/context.go`: pinned context view is separate from call extension hints. This slice does not rewrite that authority or claim it reaches ACP.

## Decisions
Use `X-LIP-Workspace` and one shared helper. Reject invalid nonempty hints with static 400; absent/whitespace-only hints remain inert. Use a separate prerequisite specification to keep the Claude feature's 12 historical task IDs unchanged. No new `lipapi` field, arbitrary body extension passthrough, ACP-specific ingress header, OpenRouter-owned parser, new session binding or host chdir.

## Synthesis and Review
Reuse existing header, materialization and workspace owners. The independent gate confirmed all five families have ordinary and bounded header carriers, and folded in precise alias collision, capture-once materialization, parser-only drive/UNC scope and existing 400-writer constraints. Live proof remains owned by Claude task 3.2; implementation and synthetic tests alone cannot complete it.
