# Design Document: claude-agent-model-bridge

## Overview

This slice adds the optional `claudeagentmodel` backend connector. A Go plugin instance supervises one private Node sidecar per conversation. The sidecar drives the official, unmodified `@anthropic-ai/claude-agent-sdk` using the operator's installed Claude Code login, converts one canonical step into SDK events, and exposes the caller's canonical tool inventory through a transient SDK-owned MCP server.

V1 covers real text streaming (S1), the deferred tool-call loop for the existing OpenAI Chat and Anthropic Messages frontends (S2), and cross-client continuation for OpenAI Responses ordered items and Gemini name-only requests (S3). The connector never executes a caller tool, never uses a static model or tool registry, and never rewrites an existing native transcript.

### Goals

- A disabled-by-default, local-only connector that streams runtime-discovered Claude text with honest identity, usage and errors.
- A reversible per-step tool bridge that returns client tool calls, parks the native SDK turn, and resumes it exactly once from the client's next HTTP request.
- An explicit lifecycle boundary: HTTP response completion, parked pending state, cancellation and process retirement are distinct actions.

### Non-Goals

- Restart-durable parked sessions, cross-node coordination, multi-user sharing/rotation, remote hosting, parallel tool calls, general MCP passthrough, structured outputs, reasoning replay, multimodal tool output, billing/economics.
- Certification of a broad public subscription claim; maintainer legal/policy sign-off remains a release gate (S4).

## Boundary Commitments

### This Spec Owns

- Connector identity, strict configuration, local single-user gates, credential isolation and the owned sidecar process tree.
- Native SDK model discovery, text/usage event mapping and effective-model disclosure.
- Canonical tool validation, reversible native naming, the per-step transient MCP server, deferred invocation correlation and canonical tool-call events.
- Parked-turn continuation proof for an authoritative proxy session and for ID-preserving stateless Chat/Anthropic requests.
- The connector's closed manifest, release metadata, disabled example and operator documentation.

### Out of Boundary

- `pkg/aipapi`, `pkg/aipsdk` ABI changes, core routing/failover/B2BUA, frontend wire encoders/decoders and billing.
- Autonomous agent semantics, native builtin tools, client tool execution, external MCP servers and any client-specific feature branch.
- Provider cost/entitlement accounting and claims that subscription usage equals billed API cost.

### Allowed Dependencies

- `pkg/aipapi` canonical call/event/tool types and `pkg/aipsdk/backendplugin` `Service`, `ConfiguredInstance`, `ForwardExecute`, `CallFromInvocation` and existing capability/minor negotiation.
- Existing frontends only as unchanged consumers of canonical events.
- `connector-support/` only for a small shared helper if sibling `internal` duplication cannot be avoided; the connector never imports another connector's `internal` package.
- Node 18.14 or newer (tested with Node 22), the pinned official Claude Agent SDK and the operator-installed Claude Code CLI. The root module never depends on the connector or Node.

### Revalidation Triggers

Canonical item/tool/event contracts, backend-plugin ABI or capability negotiation, frontend tool wire forms, connector manifest/release packaging, process-tree isolation, or a pinned SDK/CLI behavior change.

## Architecture

### Seams Reused

- `backendplugin.Service`/`ConfiguredInstance` for Describe, Configure, Resolve, ListModels, Execute and Close.
- `backendplugin.ForwardExecute` + `CallFromInvocation` so a tool-call response can finish normally while the connector-owned turn stays parked.
- `aipapi.Call`/`NormalizedItems`/`HasItemAuthority` and the existing event kinds; no new canonical concept.
- Existing OpenAI Chat and Anthropic frontends for `tool_calls`/`tool_use` encoding and tool-result decoding.
- Closed manifest, `release.yaml`, per-instance process sharing and the existing conformance/package tooling.

No new seam is introduced: the connector is the driven adapter, and the ephemeral MCP server is private sidecar behavior.

### Project Boundary Questions (AIProxer)

- **Core-owned or plugin-owned?** Plugin-owned; core remains provider-agnostic and only routes/streams.
- **New canonical concept?** No. Only existing `Call`, items, tool definitions and events.
- **Streaming-first preserved?** Yes; non-streaming is collection by the existing frontend/core path.
- **Provider SDK leakage?** The official SDK lives only in the connector sidecar; no Go provider SDK and no root import.
- **No retry after first client-visible output?** The connector never retries or fails over after commitment; terminality follows `ForwardExecute`.
- **Secure-session/diagnostics/startup posture?** Yes: local-only, single principal, secret-free bounded diagnostics and one owned process tree.

### Runtime Topology

```mermaid
sequenceDiagram
  participant C as Client harness
  participant F as AIProxer frontend
  participant E as claudeagentmodel engine
  participant S as Node sidecar (one per conversation)
  participant K as Claude Agent SDK/CLI
  C->>F: Chat or Anthropic request with tools
  F->>E: canonical Call
  E->>S: start(model, instructions, messages, tools)
  S->>K: one isolated SDK query step
  K-->>S: text / complete tool_use
  S-->>E: canonical-shaped NDJSON events
  E-->>F: response_started ... tool_call_finished, tool stop
  Note over E,S: HTTP stream ends; native SDK turn remains parked
  C->>F: next request with tool result
  F->>E: canonical Call with tool result
  E->>S: validated tool_result (exactly once)
  S->>K: release deferred MCP handler
  K-->>S: continuation
  S-->>E: events
  E-->>F: next canonical response
```

### Process and HTTP Ownership

- The Go engine owns a bounded per-instance session manager. A conversation owns exactly one sidecar child and one serialized native turn; other conversations are independent.
- The sidecar is launched with inherited stdio only (no TCP listener). Its script path, Node flags, SDK options, environment and MCP endpoints are connector-owned, never client-supplied.
- One ordered writer and one ordered reader own the NDJSON pipe. Queues, message size, UTF-8, JSON depth and stderr retention are bounded; stderr is redacted.
- `ForwardExecute` closes the per-HTTP event reader when the caller stream ends. That close **does not** kill a legitimate parked sidecar. Only explicit cancel, client failure, pending TTL, idle reap, shutdown/reload or process death retires the turn.
- Cancellation before commitment drains the owned SDK query; cancellation after a committed tool call cannot be retried and must not release the pending result later.

State machine: `NEW -> INITIALIZING -> READY -> RUNNING`; `RUNNING -> WAITING_RESULTS` after a committed tool call; `WAITING_RESULTS -> RESUMING -> RUNNING` after one validated result; `RUNNING -> COMPLETED`; any state may move to `CANCELING -> CLOSED` or `FAILED/EXPIRED -> CLOSED`. A waiter is never evicted as if its tool had completed.

### Continuation Proof

Primary lookup is `(configured instance, host-authenticated local principal, host-validated ProxyOwnedSessionID)` when the host injected the negotiated proxy-owned session ID. The connector verifies request model/route, generation, pending IDs, tool-schema snapshot and the observed canonical transcript prefix.

Without an authoritative session the connector accepts only stateless continuation with an ordered visible-prefix fingerprint matching the assistant tool-call prefix. Exact-ID fronts (Chat, Anthropic, Responses items) require exactly one pending call with the exact opaque `tool_call_id`. Name-only fronts (Gemini) require exactly one pending call with that original name, the exact canonical argument digest and the same ordered prefix; the name is never treated as an opaque id and no synthetic id is created. Two same-name pending conversations, an identical request prefix in two sessions, unknown IDs or names, missing/reordered/duplicated history and changed arguments refuse with `continuation_mismatch` or `ambiguous_continuation`; expiration and consumption are typed from ID-keyed and (name, ordered prefix, argument digest)-keyed tombstones, so a common tool name cannot shadow an unrelated conversation. A tool ID, tool name or client session header alone is never authority.
A Responses item continuation is accepted only when the host materialized the parent trajectory. A missing or unavailable materialized parent, or an unresolved `item_reference`, refuses with `history_unavailable`. A present materialized trajectory whose visible prefix was altered (reordered, omitted or argument-tampered) refuses with `continuation_mismatch`; it never releases the parked call. An omitted tool catalogue on a continuation retains the parked snapshot with a `tool_snapshot_retained` warning (strict mode refuses) instead of silently changing the step's tool surface.

## File Structure Plan

### New Files

- `connectors/claudeagentmodel/go.mod`, `go.sum` — independent connector module.
- `connectors/claudeagentmodel/release.yaml`, `manifest/template.backendplugin.json` — closed optional export.
- `connectors/claudeagentmodel/cmd/aip-backend-claudeagentmodel/main.go` — gRPC plugin entrypoint.
- `connectors/claudeagentmodel/internal/service/service.go` — descriptor and instance wiring.
- `connectors/claudeagentmodel/internal/service/config.go` — strict typed configuration and gates.
- `connectors/claudeagentmodel/internal/service/engine.go` — model discovery, execution and session-manager coordination.
- `connectors/claudeagentmodel/internal/bridge/process.go` — sidecar spawn, NDJSON reader/writer, stderr and process tree.
- `connectors/claudeagentmodel/internal/bridge/session.go` — per-conversation state machine and deferred invocation.
- `connectors/claudeagentmodel/internal/bridge/tools.go` — validation, reversible naming and per-step snapshot.
- `connectors/claudeagentmodel/internal/bridge/continuation.go` — authority, prefix fingerprint and result release.
- `connectors/claudeagentmodel/internal/bridge/events.go` — sidecar-to-canonical event mapping.
- `connectors/claudeagentmodel/sidecar/{package.json,package-lock.json,bridge.mjs,session.mjs,tool-server.mjs}` — pinned SDK sidecar and in-process MCP server.
- `config/examples/claude-agent-model.yaml` — disabled operator example.
- `docs/claude-agent-model.md` — operator/security/rollback documentation.

### Modified Files

- `.kiro/steering/roadmap.md` — park goal 3 while this feature occupies the third active goal slot.
- No root Go module, ABI, frontend or essential backend file changes.

## Components and Interfaces

| Component | Package | Intent | Requirements |
|-----------|---------|--------|--------------|
| Configuration/gates | `internal/service` | strict parse, consent and credential posture | 1.1–1.5 |
| Service/instance | `internal/service` | descriptor, discovery, profile and Execute | 2.1–2.4 |
| Engine/session manager | `internal/service` | per-conversation ownership and bounds | 4.1, 4.5 |
| Sidecar process | `internal/bridge` | private NDJSON lifecycle | 1.4, 4.5, 5.2 |
| Tool catalogue | `internal/bridge` | validation and reversible names | 3.1, 3.2, 3.6 |
| Deferred invocation | `internal/bridge` + sidecar | park/release exactly once | 3.3–3.7 |
| Event mapping | `internal/bridge` | canonical text/tool/usage/error events | 2.3, 2.4, 5.1 |
| Continuation | `internal/bridge` | authority and prefix proof | 4.1–4.4 |

### Configuration and Gates

`ParseConfigYAML` accepts only:

```go
type Config struct {
	Executable           string
	CredentialSource     string // host_login (default) | api_key
	AdminPolicy          string // refuse (default) | honor
	DefaultWorkspace     string
	MaxRuntimes          int
	TurnWaitSeconds      float64
	PendingTTLSeconds    float64
	IdleTimeoutSeconds   float64
	StrictSemantics      bool
}
```

Unknown fields, extra YAML documents, non-absolute `default_workspace`, unsupported executable/script/env switches, invalid credential/admin values and out-of-range bounds reject before process or network I/O. The sidecar script and SDK are bundled, not configurable. `admin_policy: honor` needs explicit operator consent and passes only the existing host local-only policy.

Credential source `host_login` uses the installed CLI's own login; the connector never reads, copies, prints or forwards OAuth material. `api_key` is an explicit operator secret from `ConfigureRequest.Secrets["api_key"]` and is handed only to the sidecar's isolated environment. `CLAUDE_CODE_DISABLE_CLAUDE_MDS`, `CLAUDE_CODE_DISABLE_AUTO_MEMORY`, `DISABLE_AUTO_COMPACT` and `ENABLE_TOOL_SEARCH=false` are set; `CLAUDE_CODE_SIMPLE` is cleared; any `ANTHROPIC_BASE_URL` pointing at this proxy is removed to prevent recursion.

### Descriptor and Capability Posture

`Describe` uses protocol major 1 and negotiates the latest supported minor, advertising `FeatureOrderedItems`, `FeatureProxyOwnedSessionID`, `FeatureCancellationHandshake` and `FeatureReasoningEffortValues`. `Streaming` and `Tools` are true; the static factory summary advertises `OrderedItems` for the ordered-item ABI and keeps `ItemReferences` false. `ParallelToolCalls`, `StructuredOutputs`, `Vision`, `Documents` and `ReasoningReplay` stay false. The resolved execution profile keeps message authority by default: the host projects legacy Chat/Anthropic calls without changing the S2 corridor, and a host that supplies ordered items directly is handled by the same parked loop. Advertising ordered items in the resolved profile would project legacy Chat calls to items, where the current root projection drops a Chat JSON tool-result `Part.Content`; that root fix is a separate slice. `Resolve` returns model-specific discovered capabilities; graded effort facts are published only when the pinned SDK reports exact model effort choices and the existing feature is negotiated, otherwise `nil` (Unknown).

The manifest export declares `execution_class: agent_runtime`, `access_scope: local_only`, `process_sharing: per_instance`, `credential_mode: oauth_user`, `experimental: true`, with no static token contract. Under the default `safe` policy it is not composed into failover/race/weighted chains; direct routing remains. Multi-user admission stays host-registry-denied.

### Engine, Discovery and Execution

`Configure` builds an immutable instance; `Resolve`/`ListModels` probe or use a bounded discovery snapshot, expose the exact model IDs reported by the pinned SDK, mark one default only when the SDK names one, and surface discovery failure via error/`ErrorCode` rather than a static list. A request for an unknown or stale model rejects before upstream I/O.

`Execute` calls `ForwardExecute` and opens a `ManagedEventStream` backed by one SDK step. The stream maps canonical request data into a sidecar `start`, forwards canonical events as they arrive, and emits `EventResponseFinished` with a tool-use stop after a committed tool call. The HTTP reader's `Close` detaches only that reader; the session manager retains the parked turn.

### Sidecar Protocol and SDK Settings

Versioned NDJSON messages carry `kind`, `request_id`, `sequence`, bounded `model`, `tools`, `content`, `tool_call_id`, `tool_result` and `error`. Go rejects unknown kinds, out-of-order sequences, oversized frames, invalid UTF-8 and malformed JSON as typed protocol errors, then retires the child.

The sidecar opens one SDK `query` per conversation (an async prompt stream fed one admitted canonical step at a time) with `settingSources: []`, `settings: {disableAllHooks:true}`, `strictMcpConfig:true`, `skills: []`, `tools: []`, `allowedTools: ['mcp__aiproxer__*']`, `permissionMode:'dontAsk'`, `verbatimPrompts:true` where the pinned SDK supports it, and exactly one `aiproxer` MCP server. A new user turn is fed only after a clean completion; a parked tool turn is resumed by the deferred MCP result, never by a second prompt. Client text is never elevated to system; the supported SDK system prompt carries only client-owned instructions, and any separate developer role that cannot be represented yields `instruction_role_degraded` or a strict reject. The unchanged stable prefix is reused across turns; changed earlier history never mutates a live native transcript and instead produces an explicit replay/restart degradation or strict reject.

### Tool Catalogue and Reversible Names

For each step, the connector validates the canonical `[]aipapi.ToolDef`, rejects duplicate original names, blank/oversized/control names, missing or invalid JSON schemas and unrepresentable schemas before inference. Native names are `mcp__aiproxer__<short>`: `<short>` is the original name only when it uses `[A-Za-z0-9_-]` and fits 64 characters; otherwise invalid characters become `_`, the value is truncated for an underscore plus 10 hex SHA-256 characters of the full original UTF-8 name, and collision-free uniqueness is verified. Original names, descriptions and schemas are preserved; only the native name is transformed. Duplicate originals or hash/native collisions reject the whole catalogue.

### Deferred Tool Correlation

The SDK `PreToolUse` hook captures the native `tool_use_id`, native name and current binding and creates exactly one pending entry **before** the MCP handler may wait. `mcp.tools/call` only awaits a deferred result; it never executes a command, file/network operation or plugin handler. Only complete, structurally valid arguments produce `EventToolCallStarted`/`EventToolCallArgsDelta`/`EventToolCallFinished`; the task is not emitted before its ID and JSON are proven. V1 permits one outstanding call per step; a second call produces `unsupported_parallel_tool_calls` and a safe retirement/rollback. `ToolChoice:none` advertises no tools. Forced/required choice either rejects under strict semantics or records an explicit downgrade, never a false enforcement claim.

The next HTTP request supplies the exact result. The connector validates authority, ID, name, arguments digest, model/route/tool snapshot and transcript prefix, atomically marks `RESUMING`, and writes the result to the sidecar exactly once. The sidecar returns the caller's exact text/JSON content to the MCP handler with the MCP delivery flag `isError:false`. That flag is the MCP call envelope, not a preserved caller-tool business status: the connector never asserts caller success either.

Tool error status is not representable in this slice: `aipapi.PartToolResult`/`ToolResultItem` carry no error-status field, and the unchanged Anthropic decoder discards `is_error`. The connector therefore preserves content only, never infers failure from content, and does not widen the public ABI for this. Error-status propagation needs its own scoped carrier/frontend change with owner approval.

### Canonical Event Mapping

| SDK/sidecar observation | Canonical event |
|---|---|
| step start | `EventResponseStarted`, `EventMessageStarted` |
| assistant text delta | `EventTextDelta` |
| one complete validated tool call | `EventToolCallStarted`, `EventToolCallArgsDelta`, `EventToolCallFinished` |
| usage presence | `EventUsageDelta` with unknown-vs-zero preserved and no invented cost/cache/quota |
| explicit degradation | `EventWarning` |
| ordinary terminal / tool boundary | `EventResponseFinished` with honest stop reason |

A `model_refusal_fallback` produces a visible effective-model change; if exact identity cannot be guaranteed the next turn is refused. `supersedes` never leaves unmarked committed text as authoritative. Rate-limit/plan exhaustion is a typed pre-output failure where identified, and post-output errors stay terminal without retry.

## Error Handling

Capability and configuration problems reject before upstream I/O with bounded typed messages. Missing/old CLI, signed-out login, runtime identity mismatch, organization policy denial, disconnected MCP, malformed sidecar output, SDK timeout and process death produce bounded redacted errors; a secret canary never appears in errors, logs, stderr or argv. Errors after the first committed event are non-retryable and carry `OutputCommitted`; the connector never fails over or emits a second terminal. Pending expiry surfaces `continuation_expired` explicitly on a later request.

## Testing Strategy

Follow Test Proportionality in `.kiro/steering/testing.md`; use a fake SDK-shaped sidecar and table-driven cases, not fabricated SDK output.

- Unit: config/gate tables, credential canaries, tool-name reversibility and collisions, schema/oversize rejects, event/usage mapping, continuation authority/prefix/duplicate/expiry cases, item match/reorder/omit/duplicate/item-reference, name-only unique/duplicate/argument-mismatch, and name-keyed tombstones.
- Process: spawn, ordered NDJSON, malformed/oversized frames, stderr redaction, cancel-before/after-tool, TTL/idle reap, runtime bound, close/reload and descendant reaping with a process-tree fixture.
- Integration: full gRPC/HTTP Chat and Anthropic paths, streaming and collected, exact IDs, text and JSON results, error-looking content preserved as content, blocked native `Read`/shell, no connector-side execution, no duplicate release; OpenAI Responses item loops with materialized previous-response proof/refusal and Gemini name-only loops with duplicate-name ambiguity and cross-session isolation. A projected Responses-to-message fixture proves the execution-profile projection path.
- Regression: `claudeagentacp`, `claude_code_hybrid`, backend-plugin conformance, root module isolation and an unchanged disabled example that spawns nothing.
- Red first for every task; S2 integration is the proof required before `Tools:true`, and the S3 item/name fixtures prove cross-client continuation before claiming that behavior.
