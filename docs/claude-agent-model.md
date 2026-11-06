# Claude Agent SDK model connector (`claudeagentmodel`)

**Status: experimental, opt-in, local-only, single-user. S1 text streaming, S2 deferred tool loop, S3 cross-client continuation and S4 release acceptance are implemented. The artifact oracle is approved and the maintainer legal/policy sign-off is recorded for the narrow envelope below; this is not a broad subscription-backend claim (see [release acceptance status](#release-acceptance-status)).** The connector streams text from the operator's own Claude Code login through the official, unmodified Claude Agent SDK and exposes only the caller's own tool catalogue through a transient, per-step SDK MCP server. It never executes a caller tool and is not a subscription-sharing feature.

The connector is optional and disabled by default. It runs as the local OS user and is not a sandbox.

## Supported envelope

| Caller frontend | Client tool shape | Follow-up continuity | Status |
|---|---|---|---|
| OpenAI Chat Completions | `tools[].function` | exact `tool_call_id` plus ordered visible prefix | supported, streaming and collected |
| Anthropic Messages | `tools[]{name,input_schema}` | exact `tool_use.id` plus ordered visible prefix | supported, streaming and collected |
| OpenAI Responses | ordered `function_call` / `function_call_output` items | exact `call_id`; host-materialized parent only | supported for item authority; legacy `openairesponses` previous-response unsupported |
| Google Gemini | `functionDeclarations` / `functionCall` | unique original name, exact argument digest and `PrefixByName` proof | supported name-only; ambiguity refuses |

V1 permits one outstanding tool call per step and never executes the caller's tool. Parallel calls, native Claude builtins, external MCP servers, multimodal tool results, structured outputs, reasoning replay and a durable parked turn are not supported; each refusal is typed or warned rather than hidden.

## Requirements

- Linux/amd64 (the first certified profile).
- Node 18.14 or newer on `PATH`; the packaged sidecar is tested with Node 22.
- An installed, unmodified Claude Code CLI at version 2.1.248 or newer.
- The operator's own login through `claude auth login`. The connector never accepts an OAuth token from a request, copies login material, or prints credentials.
- The locked sidecar dependencies (`cd <plugin>/sidecar && npm ci`) before enabling a packaged install.

## Install

Build and stage the closed connector outside the repository:

```sh
go run ./tools/backendplugin/package_plugins -root . -profile full \
  -select claudeagentmodel -dest /absolute/staging
```

Install the complete `claudeagentmodel/` tree into a trusted discovery directory; do not move only the binary. The packaged layout is:

```text
claudeagentmodel/
  bin/aip-backend-claudeagentmodel
  plugin.backendplugin.json
  sidecar/{bridge.mjs,session.mjs,tool-server.mjs,package.json,package-lock.json}
```

Packaging does not ship `node_modules`. The operator installs the locked closure once in the packaged sidecar directory, then keeps it read-only:

```sh
(cd /opt/aip/plugins/claudeagentmodel/sidecar && npm ci)
```

`npm ci` is the only supported install command: it consumes the checked-in lock instead of resolving new ranges. Re-run certification after any lock, SDK, CLI or Node upgrade. The root `aipstd` module never depends on Node, the connector or the Claude SDK.

## Configuration

Use the disabled example in [`config/examples/claude-agent-model.yaml`](../config/examples/claude-agent-model.yaml). The connector accepts only this typed subtree:

| Field | Default | Meaning |
|---|---|---|
| `executable` | required | Absolute path to the Claude Code CLI. |
| `default_workspace` | host home | Absolute existing directory used as the SDK `cwd`. |
| `credential_source` | `host_login` | `host_login` or `api_key` (opaque `api_key` Configure secret). |
| `admin_policy` | `refuse` | `honor` is required to enable the connector; it accepts potential organisation/server policy. |
| `max_runtimes` | `8` | Bounded live sidecars per configured instance, 1–64. Discovery and every conversation both consume one slot; at the bound a new conversation gets a typed capacity failure and a waiting conversation is never evicted as if its tool had completed. |
| `turn_wait_seconds` | `30` | Bound on waiting for a free runtime slot and for one conversation's single turn slot. It is not an inference deadline. |
| `pending_ttl_seconds` | `1800` | Bound on a committed tool call waiting for its caller result. |
| `idle_timeout_seconds` | `300` | Bound on a released continuation that never produces a terminal. |
| `strict_semantics` | `false` | Reject forced tool choice, generation-control and instruction/history degradations instead of disclosing them. |

Unknown keys, relative paths, out-of-range values, and extra YAML documents reject before a process starts. The sidecar script, Node flags, SDK options, MCP endpoints, and request payload cannot select commands or environment.

### Enabled local single-user example

The checked-in example is disabled. Enabling is an explicit operator decision: loopback only, `access.mode: single_user`, absolute paths, `admin_policy: honor`, and no credential value in YAML. `honor` acknowledges that organisation/server-delivered Claude policy remains authoritative; it is not a bypass.

```yaml
server:
  address: "127.0.0.1:18082"
access:
  mode: single_user # never expose this connector from a multi-user deployment
plugins:
  backend_discovery:
    enabled: true
    paths: [/opt/aip/plugins/claudeagentmodel]
    instances:
      claude-model:
        kind: claudeagentmodel
        execution_env: [HOME, PATH, TMPDIR]
  backends:
    - id: claude-model
      kind: claudeagentmodel
      enabled: true
      config:
        executable: /opt/claude/bin/claude # absolute installed Claude Code CLI
        default_workspace: /home/operator/workspace # absolute existing directory
        credential_source: host_login # use the operator's native `claude auth login`
        admin_policy: honor # explicit local single-user trust
        max_runtimes: 8
        turn_wait_seconds: 30
        pending_ttl_seconds: 1800
        idle_timeout_seconds: 300
        strict_semantics: false
```

No OAuth token, API-key value, or sidecar switch belongs in this file. The snippet above uses the operator's native `host_login`.

An optional `api_key` source uses the existing host secret-reference mechanism. The YAML carries only the reference; the host resolves the value from its environment at Configure:

```yaml
plugins:
  backend_discovery:
    instances:
      claude-model-api-key:
        kind: claudeagentmodel
        secret_refs:
          api_key: {env: ANTHROPIC_API_KEY}
        execution_env: [HOME, PATH, TMPDIR]
```

The matching backend entry selects `credential_source: api_key`; `ANTHROPIC_API_KEY` must already exist in the host environment and is never written into YAML. The manifest declares only `oauth_user`; `api_key` is an operator-selected alternative, not a certified second manifest mode.

## Behaviour

- `Describe` advertises streaming, tools, runtime discovery, the ordered-item ABI feature (`FeatureOrderedItems`; static `OrderedItems`) and the existing exact-Responses carrier. `ItemReferences`, `ParallelToolCalls`, `StructuredOutputs`, `Vision`, `Documents` and `ReasoningReplay` stay false. The resolved execution profile keeps message authority: the host projects item inputs through that corridor, and the connector also handles direct ordered-item invocations. This preserves Gemini's name-only correlation without inventing opaque call IDs.
- `ListModels`/`Resolve` use the exact models reported by the installed SDK; there is no static fallback list. Discovery, missing login, and runtime identity failures are explicit.
- A single user message is sent as-is. Multi-message history, tool calls and tool results are replayed as one labeled user transcript with a `history_replay_degraded` warning; developer instructions become system text with `instruction_role_degraded`. `strict_semantics: true` rejects both. No assistant turn is invented.
- The SDK runs with its first-party Claude Code system-prompt preset; the client's instructions are appended, never substituted for that preset. Only the recognized client environment element (`<env>` with a working directory or workspace root, plus its immediate date/environment preamble lines) is preserved at the start of the user prompt with `environment_context_relocated`, because Claude Code already owns environment context in the system prompt. Client instructions sharing that system block stay in the system append. Strict mode rejects the relocation. Substituting a bare client system prompt for the preset makes subscription inference ineligible as third-party application usage, so it is not a supported mode.
- Inline system/developer messages from worker harnesses remain at their original transcript positions, with `inline_instruction_degraded`; they are never hoisted ahead of model-authored history. Strict mode rejects that loss of native instruction priority. Client prompt-cache hints are carried across the existing exact-Responses ABI and produce `prompt_cache_key_unsupported` (or a strict reject), because the SDK controls caching itself; the hint value is never logged.
- `reasoning_effort` is forwarded as the SDK `effort` option only when the discovered model advertises that level. An unsupported level produces `reasoning_effort_degraded`. Other admitted generation controls (`max_output_tokens`, `temperature`, non-plain `response_mime_type`) produce `generation_controls_degraded`; strict mode rejects them before a turn starts. Under strict mode a forwarded effort is also reported as `reasoning_effort_forwarded`, because the SDK/model may apply a different effective level.
- A fallback notice names the requested and effective models in its visible warning. An SDK `assistant.supersedes` replacement after committed text is a non-retryable `superseded_output_committed` failure rather than a silent success.
- Usage is reported only when the SDK reports it; unknown is distinct from zero. The connector never invents cost, cache counters, quota, or subscription entitlement.
- Native SDK tools, skills, hooks, agents, terminal, and external MCP discovery are disabled. A native tool attempt fails closed with no filesystem or network effect.
- Child stderr is drained to EOF. No raw stderr window is retained or exposed (its edge can cut through a credential); diagnostics report only a bounded byte-count status.
- Sidecar failure codes map to connector-owned, bounded client messages. Raw SDK exception/error prose, prompts and request payloads are never forwarded. Warning text is still redacted at the Go boundary with configured secrets and bearer patterns. An exact third-party extra-usage rejection maps to `third_party_extra_usage` and a non-retryable `permission_denied`; a pre-output backend permission denial reaches clients as HTTP 403 with the constant `backend permission denied`, while a committed or retryable failure keeps its existing stream/transport classification. The operator attempt record retains the sanitized connector cause.
- A child exit before `response_finished` is a typed protocol failure, not a successful empty or truncated completion.
- The private sidecar process group is owned by the connector; shutdown, cancellation, leader exit, and failures retire the whole group, including SDK descendants.

## Tool envelope

The caller's canonical tool catalogue for one step is validated before any upstream I/O: unique non-blank names, bounded descriptions, and structurally valid JSON object schemas. Validation walks the admitted vocabulary recursively (`type`, `properties`, `required`, `items`, `prefixItems`, `contains`, `propertyNames`, `additionalProperties`, `$defs`, `if`/`then`/`else`, `dependentSchemas`, `dependentRequired`, `dependencies`, `anyOf`/`oneOf`/`allOf`, `not`, `enum`, `const` and typed annotation/numeric keywords).

The admitted dialect is deliberately closed and enforced:

- The schema root must be `{"type":"object", ...}` because the installed MCP client rejects any other root `inputSchema.type`; empty, scalar and union roots are refused during admission.
- `type` is a known JSON Schema type name or a non-empty list of unique names. `required` allows `[]` but rejects duplicate entries. `multipleOf` must be a number strictly greater than zero. Numeric, non-negative integer, string and boolean keywords must carry the matching JSON type. `dependencies` values are lists of property names or schema objects.
- Boolean schemas (`true`/`false`) are not admitted as schema positions; keywords such as `additionalProperties` accept a boolean value directly.
- `pattern` and `patternProperties` are refused as unsupported schema keywords. Their values are ECMAScript regular expressions; Go's RE2 parser is not a faithful validator (it rejects valid lookahead), and this slice does not half-validate them. The schema bytes are never rewritten and the keyword is never silently dropped.
- `$ref` and unknown keywords are refused explicitly, and schema nesting beyond the supported depth rejects the catalogue.

The connector never repairs or truncates a schema. A request whose trailing tool result is accompanied by other content is replayed, and a duplicate occurrence of the committed tool call id is refused with `continuation_mismatch`.

Each admitted tool is published to the model as `mcp__aiproxer__<short>`:

- `<short>` is the original name when every character is `[A-Za-z0-9_-]` and the full model-visible name (prefix + short) is at most 64 bytes; the short budget is therefore 49 bytes.
- Otherwise invalid characters become `_`, the value is truncated, and an underscore plus ten hex characters of the SHA-256 of the full original name is appended. Mapping is deterministic and reversible; a native-name collision rejects the whole catalogue.
- The original name, description and schema bytes are preserved. Only the native name is transformed.

Each fresh request opens its own SDK query; a parked continuation resumes that same query, and a clean-completed conversation starts a new query for the next request because the connector does not persist a native transcript. When the model proposes one complete tool call, the connector:

1. captures the native `tool_use_id`, name and exact arguments in `PreToolUse` and records exactly one pending entry before the MCP handler may wait;
2. emits `tool_call_started` / `tool_call_args_delta` / `tool_call_finished` with the original caller name and exact argument JSON;
3. ends the HTTP response with a tool-use stop while the native SDK query stays parked;
4. refuses a second outstanding call (`unsupported_parallel_tool_calls`).

The MCP handler only awaits a deferred result. It never runs a command, file operation, network call or plugin handler.

### Cross-client continuation

- **OpenAI Responses.** Item-authority calls carry `function_call` and `function_call_output` items with their exact `call_id`. The connector digests the ordered item trajectory, matches the trailing `function_call_output` to the one committed tool call, and refuses a reordered, padded, omitted or duplicated item history. `item_reference` is not resolved by this connector: a continuation that carries one refuses with `history_unavailable` instead of guessing the referenced content. A `previous_response_id` continuation is accepted only when the host materialized the parent trajectory. A missing or unavailable materialized parent refuses with `history_unavailable`; a present materialized history whose prefix was altered (reordered, omitted or argument-tampered) refuses with `continuation_mismatch`. The `openairesponses` frontend used by Codex at `/v1/responses` carries the function-call identity at the top level with the argument document in the part content; continuations captured from older builds keep the former envelope-in-content carrier. Both carriers preserve the original `call_id`, name and arguments for the same ordered prefix proof. Assistant `output_text` returned by that frontend can be replayed on the next request.
- **Google Gemini.** `functionCall` has no distinct native ID and `functionResponse` identifies the call by name only. The connector therefore requires exactly one outstanding pending call with that original name, the exact canonical argument digest, and a matching ordered `PrefixByName` fingerprint. It never invents a call id and never writes the name into the native opaque-ID slot. Two pending conversations with the same name refuse with `ambiguous_continuation`; a trusted `ProxyOwnedSessionID` may select one of them. Serial same-name calls are supported when the visible prefix selects exactly one occurrence.
- **Tombstones.** Expiry and consumption are recorded against the native call ID and against the name-only continuation identity: original name, ordered prefix and exact argument digest. A late or repeated result returns `continuation_expired` or `continuation_consumed`, while an unrelated live call with the same name but a different prefix is unaffected. A genuinely advanced serial call has a different prefix and is served; a new conversation with an identical prefix is refused as ambiguous without a trusted session binding.
- **Omitted tools.** A continuation that omits its tool definitions reuses the parked catalogue snapshot and reports `tool_snapshot_retained`; `strict_semantics: true` refuses instead. A different non-empty catalogue remains a `continuation_mismatch`.

### Continuing a parked turn

The next HTTP request releases exactly one pending call and never re-runs it. The connector requires:

- the host-validated `ProxyOwnedSessionID` header when present (client session hints and User-Agent remain diagnostics only);
- the exact opaque `tool_call_id` from the assistant tool call, or for a name-only Gemini frontend the unique pending original name plus the exact canonical argument digest;
- the same configured instance, resolved model, and tool-catalogue snapshot (an omitted catalogue may retain the parked snapshot as described above);
- an ordered fingerprint of the client-visible transcript up to and including the assistant tool call, including any assistant text the client saw before it and, for item authority, the exact ordered items.

Only the final result group can be the new continuation: earlier completed tool results are replayed history, so a sequential loop with several calls is supported while exactly one call may be outstanding. A request whose trailing result is accompanied by other content (before it, in the same message, or after it) is served as a **new** conversation turn: the client-visible history is replayed in source order with `history_replay_degraded` and `continuation_replayed` warnings instead of resuming a native turn, so no text is dropped and the parked call is not released. An ordered-item trajectory instead refuses a padded/reordered suffix with `continuation_mismatch`, because replaying it could reorder model-authored content. A request whose trailing tool result does not match a pending call is refused with `continuation_mismatch`, `ambiguous_continuation`, `continuation_expired`, `continuation_consumed`, `continuation_failed`, or `history_unavailable` for an unresolved item reference or unavailable materialized parent, and the parked turn is not released. The named refusal also covers a committed call echoed under a non-assistant role.

`ToolChoice:none` exposes no tools. A forced or required choice cannot be enforced by the pinned SDK MCP path, so it produces the `tool_choice_degraded` warning (or a strict reject); a tool-choice subset produces `tool_choice_allowed_subset_degraded`. An explicit `ToolChoice:none` on a continuation is not an omitted catalogue: when the parked native turn admitted tools, the connector refuses the continuation before consuming the pending call, because the live SDK query cannot unregister its MCP catalogue.

### Tool results: representable content only

The canonical tool-result boundary (`aipapi.PartToolResult` / `ToolResultItem`) has no error-status field, and this slice does not add a public ABI field. The connector therefore preserves the caller's exact result text and structured JSON content (a Chat JSON string result and the same Anthropic text result normalize to the same content history), returns it to the deferred MCP handler with an explicit `isError:false`, and never infers failure from result strings or asserts success on the caller's behalf.

Anthropic `is_error` is **not** propagated as MCP error status: the current Anthropic frontend decoder discards it before the connector sees the result. A client that needs error semantics must express them in the result content or in a later slice with an approved generic carrier. This is the normative contract recorded in `requirements.md` 3.7 and `design.md` ("Deferred Tool Correlation"); no ABI expansion is made here.

### Lifecycle and TTL

`Close()` on the HTTP event stream after a committed tool call detaches only that reader; the parked native turn stays alive. Explicit cancel, client failure, pending TTL expiry, idle timeout, shutdown/reload or process death retire the conversation exactly once.

- `pending_ttl_seconds` (default 1800) bounds a committed tool call waiting for its caller result. A later result receives `continuation_expired`; the parked process is retired and never released twice.
- `idle_timeout_seconds` (default 300) bounds a released continuation that never reaches a terminal. The idle callback is ownership-conditional: it cannot close a newer turn.
- `max_runtimes` (default 8, maximum 64) bounds live sidecars per configured instance, including discovery. At the bound a new conversation gets a typed capacity failure; a waiting conversation is never evicted as if its tool had completed.
- `turn_wait_seconds` (default 30) bounds waiting for the single turn slot, not inference.
- Cancellation before commitment drains the query. Cancellation after a committed tool call cannot be retried and must not release the pending result later.
- Parked turns do not survive a proxy restart, reload or process death. Descendants are reaped with the owned process group on Linux.

### Errors and redaction

Sidecar failure codes map to connector-owned, bounded client messages. Raw SDK exception/error prose, prompts, request payloads, result bodies and stderr are never forwarded. Child stderr is drained to EOF and only a bounded byte-count status is retained, because a raw window edge can cut through a credential. Warning text is redacted at the Go boundary with configured secrets and bearer patterns.

Missing or old Node/CLI, signed-out login, runtime identity mismatch, organization policy denial, transient MCP disconnection, malformed sidecar JSON, rate-limit/plan exhaustion and caller timeout are distinct paths. A stream that fails before `response_finished` returns exactly one typed terminal error; it never becomes a successful truncated completion and never emits a second terminal.

An `authentication_failed` observation identifies the failing SDK invocation,
not the operator's account-wide login state. Compare a direct, prompt-bounded
CLI invocation with the connector under the same OS user, executable, `HOME`
and `CLAUDE_CONFIG_DIR`. Investigate environment differences and native CLI
refresh failures before prescribing re-login. Model discovery alone proves
neither successful inference nor a missing login. Do not inspect or copy tokens
into proxy configuration or attach raw authentication stores to diagnostics.

For operator-side rejection details, enable the existing attempt diagnostics on
the local host:

```yaml
diagnostics:
  enabled: true
  attempts_path: "/admin/attempts"
```

The attempt record retains the sanitized connector failure class and summary;
the client may receive only `capability rejected`. Correlate the A-leg ID from
the host log when a rejected response has no `X-AIP-A-Leg-Id` header. Do not
enable raw request or tool-payload logging to diagnose a rejection.

### Codex function-only compatibility mode

Codex's server-native `web_search` and `namespace` tool declarations are not
canonical caller-executed function definitions. They are not silently dropped,
flattened, or advertised as supported. For Codex CLI 0.166.0, use a dedicated
provider/profile with those optional tools explicitly disabled:

```toml
model = "claude-model:anthropic/sonnet"
model_provider = "aiproxer"
web_search = "disabled"

[model_providers.aiproxer]
name = "AIProxer caller-function tools"
base_url = "http://127.0.0.1:8080/v1"
wire_api = "responses"

[features]
multi_agent = false
goals = false
view_image = false
browser_use = false
in_app_browser = false
apps = false
```

Keep the harness's own sandbox and permission settings. Its function tools
(including `exec_command`) still execute only in Codex, not in the connector.
This is an explicit reduced tool envelope, not stock-default Codex feature
parity or support for server-native tools. No Claude credential belongs in the
Codex client configuration; the loopback host owns its native login boundary.

Claude Code acceptance likewise uses an explicit `--tools Read --allowedTools
Read` profile, not its full default catalogue. Claude Code 2.1.316's default
`Workflow`/`Monitor` schemas contain the unsupported JSON Schema `pattern`
keyword and are refused before inference. Tool-catalogue narrowing is a client
choice, never silent connector filtering; the Read-only loop does not prove
stock-default Claude Code compatibility.

## Credential posture and manifest metadata

The closed export declares `credential_mode: oauth_user`, `access_scope: local_only`, `process_sharing: per_instance` and `execution_class: agent_runtime`, matching the default `host_login` path.

`credential_source: api_key` is an optional operator-selected alternative: the secret arrives only through `ConfigureRequest.Secrets["api_key"]` and becomes a single environment variable for the private sidecar. The manifest schema carries one `credential_mode` value, so it does not describe that alternative; neither mode permits shared or remote use, and the host still enforces the `oauth_user => local_only` rule. The manifest declares the default `oauth_user` source only; it does not certify `api_key` as a second manifest mode.

### Multi-user denial

The closed export is `local_only`, `oauth_user`, `per_instance`, `agent_runtime` and experimental. The standard host multi-user policy does not approve this factory, and the connector has no config switch that can add it to a shared deployment. A multi-user host rejects the connector even if an operator writes `admin_policy: honor`; that setting only accepts the local single-user trust decision. Do not run this connector as a shared subscription pool, a remote service or an account-rotation backend.

## Known limitations

- One outstanding tool call per step; parallel tool calls are refused, not hidden.
- Chat, Anthropic Messages, legacy and item-authority OpenAI Responses calls, and Gemini name-only calls are supported. Unresolved `item_reference` items are refused; the connector does not fetch or reconstruct referenced items. The backend-plugin ABI does not carry `Call.PreviousResponseID`; Responses continuation therefore relies on the host-materialized trajectory and its provenance. A previous-response request without materialized history is `history_unavailable`.
- The resolved execution profile deliberately keeps message authority. Static ordered-item ABI support is not a promise that every frontend can be projected to an ID-requiring item carrier: Gemini name-only tool calls have no opaque call ID to supply. Host-level protocol support is proved by HTTP continuations through the message corridor, not by flipping that profile flag.
- A Gemini name-only continuation is accepted only when exactly one pending call has that name and the exact argument/prefix proof matches. Duplicate same-name pending calls, or identical prefixes in two stateless sessions, refuse with `ambiguous_continuation` unless a trusted `ProxyOwnedSessionID` selects one.
- Tool results are content-only (see above): exact caller text and structured JSON are preserved and no status is synthesized. Anthropic `is_error` is not propagated. Structured JSON sent as an object may be re-encoded with canonical key order; its fields are unchanged. A multimodal tool result is refused only when the caller sends canonical non-text parts (`ToolResultItem.Parts`); a Chat JSON result array is passed through as JSON content, not interpreted as image/file content.
- A tool result accompanied by other content is replayed in source order with explicit warnings instead of resuming the parked query (ordered-item trajectories instead refuse); the parked call then expires at `pending_ttl_seconds`.
- A model shape that authors text after a `tool_use` block in the same step is refused with `unsupported_post_tool_text`. Genuine assistant text authored before the tool block may already have been emitted as prefix content; the turn then fails terminally, no tool call is emitted, and the native turn is retired. The connector neither reorders nor retracts that prefix.
- A client that changes its tool catalogue, model, route or the assistant tool-call prefix while a turn is parked receives an explicit refusal; an omitted catalogue retains the parked snapshot with a warning.
- Parked turns do not survive a proxy restart.

## Verification

```bash
(cd connectors/claudeagentmodel && GOWORK=off go test ./... -count=1)
(cd connectors/claudeagentmodel/sidecar && npm ci && node --test *.test.mjs)
go test -tags=integration ./tools/backendplugin/ -run '^TestPackage_ClaudeAgentModelArchiveDeployment$' -count=1
```

The exact installed-artifact gate is environment-only and sends no user prompt. Verify mode compares the selected candidate with the committed approved identity; it is not the candidate-recording mode:

```sh
AIP_CLAUDE_MODEL_CERT_RUNTIME=/opt/aip/plugins/claudeagentmodel \
AIP_CLAUDE_MODEL_CERT_CLI=/opt/claude/bin/claude \
AIP_CLAUDE_MODEL_CERT_EXPECT=/absolute/approved-identity.json \
AIP_CLAUDE_MODEL_CERT_OUT=/absolute/private/observed-evidence.json \
  env GOWORK=off go -C connectors/claudeagentmodel test ./internal/service -run '^TestCertification_InstalledBundle$' -count=1 -v
```

`-count=1` is required: installed bytes and child processes are external inputs that Go test caching cannot observe, so a cached PASS is not evidence.

It checks the Go binary hash against the manifest, the manifest identity and posture, every sidecar companion and lock, the installed dependency-tree hash and SDK package, Node/CLI versions and hashes, production `resolveSidecar`/`probeRuntime`, the sidecar hello identity, and the installed Go executable's real gRPC `Describe`/`Configure`/`ListModels` execution. The discovered model catalogue is account/runtime data: it must be non-empty and unique, is written to private evidence, and is not compared with the approved identity. It sends no user prompt and requires no model completion. A set verify environment fails closed on any artifact/runtime/closure mismatch; only all-unset environment skips it. Candidate recording is a separate command (`AIP_CLAUDE_MODEL_CERT_RECORD=1` with `AIP_CLAUDE_MODEL_CERT_OUT`) and never claims approval.

The synthetic fake sidecar proves configuration, discovery, streaming, redaction, protocol bounds, the parked/resume tool loop and cleanup without a token. `production-wiring.test.mjs` spawns the real sidecar entrypoint with only the external SDK `query` replaced, and drives the real session/broker/MCP glue across a result release. The sidecar tests exercise the real in-process MCP server surface (raw JSON Schema publication, deferred handler, name/argument correlation, denial of native builtins). `sdk-surface.test.mjs` characterizes the pinned SDK declaration and the installed CLI (`PreToolUse` native id, `tools: []`/`settings`/`strictMcpConfig`/`permissionMode`, the sdk MCP instance boundary, and the 2.1.248 `verbatimPrompts` minimum) offline; a pinned-version mismatch fails with a precise message instead of a guessed workaround.

`TestStagedAcceptance_FrontendToolLoop` builds the real host (`aipstd`) and connector executables, stages them with the SDK-shaped fixture at the model boundary, and drives OpenAI Chat, Anthropic Messages, legacy OpenAI Responses (Codex wire), item-authority OpenResponses and Gemini over HTTP and gRPC. Streaming and collected loops replay the actual assistant prefix and return text and JSON results. The S3 suites add ordered-item matching (match/reorder/omit/duplicate/item-reference), materialized previous-response acceptance and `history_unavailable` refusal, a projected Responses-to-message continuation, Gemini name-only loops with duplicate-name ambiguity and cross-session isolation, two serial same-name loops, omitted-tool retention, and an ordered-item ABI invocation. A real-account smoke is optional, private, and must not log prompts or credentials.

## Release acceptance status

The packaging test packages and installs the closed artifact, parses the strict manifest and package index, launches the installed binary over gRPC, checks descriptor/manifest parity, sidecar presence, `local_only` posture and host multi-user denial, and removes the optional connector to prove ACP still describes itself.

Private acceptance has exercised the live model through Chat, Anthropic,
legacy Responses, item-authority Responses and Gemini, each streaming and
collected, with caller-executed tool results and unchanged assistant replay.
Real Claude Code (Read-only profile) and Codex (function-only profile) also
completed their own tool/result loops over their natural frontend protocols.
This is not a harness-by-frontend Cartesian certification or stock-default tool
parity. A separate private real-account text smoke completed normally.

Live acceptance ran on the #1698 merge head. Current `main` differs by the
#1702 advisory lint modernization, the #1713 Go toolchain pin and the #1714
root `x/crypto` removal; no new live tool-loop run was performed on this exact
head. A fresh candidate was recorded from `f1266f3b6` on 2026-11-03, verified
against the exact installed bundle, and approved as the committed oracle in
#1715 (merged as `f9a42bf9f`). That approval binds artifact/runtime/closure
identity; it does not re-run the live matrix.

The maintainer legal/policy sign-off is recorded below. It is a policy decision
about this connector's narrow envelope, not a legal opinion and not a broad
public subscription-backend claim. The final-head live-acceptance gap above is
a documented limitation of this release, not a hidden claim of complete
harness-by-frontend coverage.

See [`connectors/claudeagentmodel/certification/README.md`](../connectors/claudeagentmodel/certification/README.md) for the exact artifact record and runbook. Verify mode requires `AIP_CLAUDE_MODEL_CERT_EXPECT` pointing at the committed [`approved-local-candidate.json`](../connectors/claudeagentmodel/certification/approved-local-candidate.json); that oracle pins artifact/runtime/closure identity, not the dynamic model catalogue. Record mode writes a candidate and never claims approval. A green synthetic suite is not a live subscription certification.

## Rollback and policy

Rollback is operator-local and has no core migration:

1. Set `enabled: false` for the instance (or remove its `backends` entry), then restart/reload the host. A disabled connector spawns no sidecar and performs no discovery.
2. Remove or archive the `claudeagentmodel/` plugin directory from the discovery path. The package test proves removing this optional connector leaves the separately packaged `claudeagentacp` artifact and its descriptor intact; `claude_code_hybrid`, other backends and the root host are not part of this connector's process or configuration ownership.
3. Keep the operator-installed sidecar `node_modules` for audit, or delete it with the plugin directory. Reinstalling requires the same reviewed `package-lock.json` and `npm ci`.

The connector fails closed in multi-user deployments and is excluded from default multi-backend composition as an `agent_runtime` export.

Subscription use here is personal and experimental. The personal-use premise is that an end user may sign in to the unmodified Claude Code with their own plan; that premise is attributed to Anthropic's Claude Code terms, not asserted as a legal conclusion by this project. Operators must verify the applicable Anthropic terms for their own account and plan before enabling this connector. This project does not resell, share or represent that allowance as a provider API entitlement. Never place OAuth tokens in YAML, argv, logs or the proxy; the login remains in the native Claude CLI.

### Maintainer legal/policy sign-off

Recorded 2026-11-03 for the `claudeagentmodel` release:

> As maintainer, I record the legal/policy sign-off for `claudeagentmodel`: it ships as an experimental, opt-in, local-only, single-user connector that uses the operator's own Claude Code login. It must not share, pool, resell, or rotate subscriptions; it must not represent subscription usage as provider cost or entitlement; and it makes no broad public subscription-backend claim. Operators must verify Anthropic's terms for their own account and plan. This is a policy decision, not a legal opinion.

The sign-off is limited to that envelope. It does not approve multi-user or
remote hosting, subscription pooling, account rotation, resale, or any claim
that a consumer plan is a provider API entitlement. Engineering availability and
synthetic tests are not this approval.
