# Research and Design Discovery

## Research Scope

This discovery supports the brownfield design for `tool-call-classification`. The requested behavior is intentionally narrow: classify model-emitted tool calls by tool name, attach a coarse category to the tool lifecycle information consumed by Go-LIP tool policies/reactors, and expose a conservative boolean indicating whether the named tool can potentially mutate the local filesystem.

Repository baseline: `main` at `dcd5f398eef9aabbb0816a99eb42070355f232ca`.

External reference set: current default branches of OpenAI Codex, Pi, Cline, OpenCode, Hermes Agent, OpenClaw, Kilo Code, and the supplied Claude Code source reconstruction. The survey was used only to derive stable tool-name aliases; no external runtime dependency or provider-specific integration is required.

No new third-party dependency is required.

## External Harness Tool-Name Survey

The survey produced the following practical alias families. Matching can be case-insensitive, so PascalCase variants such as `Read`, `Grep`, `Bash`, `Edit`, `Write`, `WebFetch`, and `WebSearch` do not require separate implementation branches.

| Category | Observed names |
|---|---|
| File read | `read`, `read_file`, `notebook_read` |
| File search / discovery | `grep`, `find`, `glob`, `ls`, `search_files`, `list_files`, `list_code_definition_names`, `semantic_search`, `codebase_search` |
| OS command / terminal | `bash`, `execute_command`, `exec`, `exec_command`, `shell_command`, `terminal`, `process`, `write_stdin`, `background_process`, `interactive_terminal`, `powershell` |
| File edit / create | `edit`, `write`, `replace_in_file`, `write_to_file`, `write_file`, `patch`, `apply_patch`, `notebook_edit`, `notebookedit` |
| Dedicated file removal | No normal dedicated removal primitive was exposed by the surveyed harnesses; deletion is usually performed through patch tools or shell execution. |
| Web lookup / fetch | `web.run`, `web_search`, `websearch`, `web_fetch`, `webfetch`, `web_extract`, `x_search` |
| Browser automation | `browser`, `browser_action`, and Hermes `browser_*` tools such as `browser_click`, `browser_navigate`, `browser_snapshot`, and `browser_type` |

Two observations matter for the local-filesystem mutation flag:

1. A generic command tool must be considered potentially mutating even when a particular invocation happens to run a read-only command such as `cat`, `rg`, or `git status`. This feature classifies by **tool name only** and does not inspect arguments.
2. `patch` / `apply_patch` belong to the file-edit category even though their patch grammars can delete files. The category describes the tool family, while `MayMutateLocalFS=true` captures the safety-relevant property.

## Key Repository Findings

### 1. `lipapi.ToolEvent` is the existing focused tool-policy boundary

`pkg/lipapi/tool_event.go` defines a small canonical projection for the three tool-call lifecycle kinds (`started`, `args_delta`, `finished`). `pkg/lipsdk/toolpolicy.Policy` and `pkg/lipsdk/hooks.ToolReactor` already consume this type directly.

This is the smallest useful augmentation surface. Adding classification to `ToolEvent` makes it available to existing policy/reactor consumers without changing their method signatures and without adding provider-specific fields to request or backend APIs.

### 2. Generic canonical `lipapi.Event` should remain unchanged

`pkg/lipapi.Event` already carries `ToolCallID` and `ToolName` for generic streaming transport. The requested categorization is derived metadata for tool-processing consumers, not provider wire evidence. Adding fields to the generic event would broaden cloning, validation, codec, continuation, traffic, and frontend surfaces with no present consumer need.

Design consequence: keep classification on `ToolEvent` and in a tiny runtime lifecycle helper. Do not change provider/front-end mappings merely to populate derived fields.

### 3. A stateless per-event classifier is insufficient in the brownfield stream

Current OpenAI-compatible streaming code commonly emits:

- `EventToolCallStarted` with `ToolCallID` and `ToolName`;
- later `EventToolCallArgsDelta` with the ID and argument fragment but no name;
- `EventToolCallFinished` with the ID but no name.

This is legal under the current canonical event contract and is not limited to one provider family.

If classification were derived independently from each incoming event, the start event would be classified correctly while later lifecycle events would degrade to `unknown`. To satisfy consistent tool-event enrichment, the runtime needs one tiny per-stream map from `ToolCallID` to the last derived classification.

### 4. The runtime already has a single choke point before tool policies/reactors

`internal/core/runtime.dispatchClientFacingEvent` converts a generic event with `lipapi.ToolEventFromEvent`, then `handleToolEventPath` runs tool policy and tool reactors before merging any reactor output back into the generic event.

This is the correct integration point for lifecycle enrichment. It avoids changes in every backend/connector and gives every existing tool policy the same normalized metadata.

### 5. Tool names can change after backend mapping

There are two existing rewrite paths:

- completed-tool-call finalizers can rewrite a tool name and synthesize a replacement lifecycle before the normal tool-event path;
- tool reactors can return `ToolRewrite` or `ToolReplace` with a different `ToolName`.

Classification must therefore describe the **effective current tool name**, not the original provider name. A reactor must not be able to leave stale or contradictory derived metadata by changing `ToolName` while copying an old category.

Existing reactor tests also show a common pattern where a rewrite constructs a fresh `ToolEvent` and omits an unchanged `ToolName`. The new derived fields must not force every existing reactor to copy them manually.

### 6. `retryRecvStream` does not need synchronization for classification state

The stream contract explicitly states that one goroutine calls `Recv`; concurrent `Recv` is unsupported. A small map owned by `retryRecvStream` therefore needs no mutex and no goroutine. It is lifecycle state analogous to other request-local stream bookkeeping, not a global cache or service.

### 7. General response-part hooks run after tool reactors

A response-part hook can theoretically change the generic `Event.ToolName` after the tool-policy/reactor phase. Classification does not need to be exposed to general response hooks, but the runtime can cheaply observe the final non-empty tool name before emission and refresh its per-ID classification so a later name-less fragment does not inherit stale state.

This keeps the feature coherent without expanding `lipapi.Event`.

## Chosen Minimal Architecture

The feature consists of only two concepts:

1. **Pure name classifier** — normalize a name with trim + case-fold and return `{Category, MayMutateLocalFS}` from a static exact-name table/switch.
2. **Per-stream lifecycle enricher** — remember classification by `ToolCallID` so name-less delta/finish events inherit the start classification; clear the entry at finish and on stream reset/teardown.

There is no interface, constructor graph, configuration, provider detector, command parser, policy engine, persistence layer, database table, background goroutine, or external dependency.

## Classification Taxonomy

The canonical category set is deliberately coarse:

- `file_read`
- `file_search`
- `os_command`
- `file_edit`
- `file_remove`
- `web_access`
- `unknown`

`file_remove` is retained as a first-class category for explicit removal-only tool names, even though the surveyed major harnesses generally delete through `patch`/`apply_patch` or shell execution. A tiny obvious exact-name set such as `delete_file`, `remove_file`, `delete_directory`, and `remove_directory` can map there without introducing fuzzy matching.

### Local-filesystem mutation posture

| Category / family | `MayMutateLocalFS` | Rationale |
|---|---:|---|
| Known file read | `false` | Read-only filesystem access by contract/name. |
| Known file search/discovery | `false` | Search/list operations are read-only. |
| OS command / terminal | `true` | Arbitrary commands or continuing shell sessions can mutate files. |
| File edit/create | `true` | Direct mutation capability. |
| Dedicated file removal | `true` | Direct deletion capability. |
| Web lookup/fetch/extract | `false` | Read-only network retrieval tools. |
| Interactive browser automation | `true` | Coarse name-only classification cannot prove the browser action cannot download/save or otherwise cause local artifacts. |
| Unknown | `true` | Conservative fallback avoids falsely declaring an unfamiliar tool safe. |

The boolean means **can potentially mutate**, not “did mutate.” It is informational metadata and must not be interpreted as execution evidence.

## Matching Rules

- Trim surrounding whitespace.
- Lowercase for matching.
- Use exact aliases only.
- Do not parse namespaces beyond exact known names such as `web.run`.
- Do not infer category from arbitrary substrings (`read_*`, `delete_*`, etc.).
- Do not inspect tool arguments, JSON schemas, shell command text, provider identity, or advertised descriptions.
- Unknown names return `CategoryUnknown` and `MayMutateLocalFS=true`.

Exact matching is intentionally less clever: it is deterministic, reviewable, cheap, and cannot silently misclassify unrelated custom/MCP tools because their names happen to contain a keyword.

## Lifecycle Enrichment Rules

For each canonical tool event entering the policy/reactor path:

1. If `ToolName` is non-empty, classify it and remember the result under `ToolCallID`.
2. If `ToolName` is empty and the ID is known, attach the remembered classification.
3. If neither name nor remembered state exists, attach `unknown/true`.
4. Run tool policy and reactors with the enriched event.
5. On a rewrite/replace with a non-empty effective name, recompute derived metadata from that name before the next reactor observes it.
6. On a same-ID rewrite that omits the name, preserve the current derived classification rather than accepting zero/stale reactor-authored derived fields.
7. On a replacement that changes ID and supplies no name, use `unknown/true` for the new logical call.
8. Remember the final effective classification for subsequent fragments.
9. If a later response-part hook changes the generic event's non-empty tool name, refresh the remembered classification from that final name.
10. On a finished lifecycle, remove the ID entry after processing; clear all entries when the stream state is reset/closed.

Multiple concurrently interleaved tool-call IDs remain independent.

## Rejected Alternatives

### Provider/backend-specific tagging

Rejected because tool names are client/harness conventions, not provider semantics. Duplicating tables in connectors would create drift and Cartesian maintenance.

### Parsing tool arguments or shell command contents

Rejected because the requirement explicitly asks for a simple potential-capability boolean. Parsing arbitrary shells/tool schemas would turn a tiny classifier into a security analyzer and still could not prove absence of mutation.

### Configurable classification registry

Rejected for the current scope. No user-configurable aliases or plugin registration were requested. A config surface would add validation, reload, ownership, persistence, and documentation concerns disproportionate to the feature.

### Global map/cache

Rejected. Classification is pure; only lifecycle correlation requires state, and that state belongs to the request-local stream that already owns the tool lifecycle.

### Adding fields to generic `lipapi.Event`

Rejected for this scope. Tool policies/reactors are the requested processing consumers and already use `ToolEvent`. Keeping generic stream events unchanged avoids unnecessary codec/frontend/backend churn.

## Research Conclusion

The repository already provides the correct seams. The implementation can remain very small: one canonical enum/result/helper, two additive fields on `ToolEvent`, and one request-local map integrated at the existing tool-policy/reactor choke point. The only brownfield complexity worth preserving in the specification is lifecycle correlation for name-less fragments and reclassification after tool-name rewrites.
