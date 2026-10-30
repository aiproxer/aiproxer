# Requirements Document

## Introduction

Go-LIP already normalizes model-emitted tool-call lifecycle items into `lipapi.ToolEvent` before tool policy and tool-reactor processing. Consumers currently receive the tool-call ID, tool name when present, and argument deltas, but they must interpret raw harness-specific tool names themselves.

The proxy needs a small protocol-neutral classification helper that converts common coding-agent tool names into a coarse tool category and a boolean indicating whether the named tool can **potentially mutate the local filesystem**. The derived metadata is intended to make later policy, auditing, and feature code simpler; this feature itself shall not make allow/deny decisions.

The helper must remain deliberately simple. Classification is based only on the tool name. It does not inspect JSON arguments, shell command text, schemas, descriptions, provider identity, client identity, or execution results. Because canonical streaming fragments can omit the tool name after the start event, Go-LIP must preserve the derived classification across one tool call's lifecycle by `ToolCallID`.

## Boundary Context

- **In scope:** a small canonical category enum/result; deterministic exact-name aliases covering the surveyed major coding-agent harnesses; a conservative local-filesystem-mutation boolean; additive enrichment of `lipapi.ToolEvent`; request-local correlation of name-less tool lifecycle fragments; reclassification after effective tool-name rewrites; focused unit/runtime tests.
- **Out of scope:** parsing tool arguments or shell commands; proving that a particular invocation did or did not mutate files; user-configurable aliases; provider-specific classifiers; dynamic/plugin registration; filesystem sandboxing; allow/deny policy; audit persistence; metrics/logging changes; generic `lipapi.Event` fields; frontend/backend/connector wire changes; databases; network calls; background workers; new third-party dependencies.
- **Primary ownership:** protocol-neutral classification contract belongs with canonical tool-event semantics in `pkg/lipapi`; lifecycle correlation belongs in the existing core runtime tool-event path.
- **Compatibility boundary:** existing `toolpolicy.Policy` and `hooks.ToolReactor` method signatures remain unchanged because they already consume `lipapi.ToolEvent` by value.
- **Safety interpretation:** `MayMutateLocalFS` means the tool family has the capability to cause local filesystem mutation. It is not evidence that a specific invocation mutated the filesystem.

## Requirements

### Requirement 1: Stable Tool Category Classification

**Objective:** As a Go-LIP feature author, I want common coding-agent tool names normalized into a small stable taxonomy, so that downstream code does not need harness-specific name checks.

#### Acceptance Criteria

1.1. When a non-empty tool name is classified, the system shall trim surrounding whitespace and compare the name case-insensitively.

1.2. The system shall expose exactly the following coarse categories for this feature: `file_read`, `file_search`, `os_command`, `file_edit`, `file_remove`, `web_access`, and `unknown`.

1.3. The classifier shall return one and only one category for every input name.

1.4. The classifier shall classify the following known file-read aliases as `file_read`: `read`, `read_file`, and `notebook_read`, including case variants.

1.5. The classifier shall classify the following known file-search/discovery aliases as `file_search`: `grep`, `find`, `glob`, `ls`, `search_files`, `list_files`, `list_code_definition_names`, `semantic_search`, and `codebase_search`, including case variants.

1.6. The classifier shall classify the following known OS-command/terminal aliases as `os_command`: `bash`, `execute_command`, `exec`, `exec_command`, `shell_command`, `terminal`, `process`, `write_stdin`, `background_process`, `interactive_terminal`, and `powershell`, including case variants.

1.7. The classifier shall classify the following known file-edit/create aliases as `file_edit`: `edit`, `write`, `replace_in_file`, `write_to_file`, `write_file`, `patch`, `apply_patch`, `notebook_edit`, and `notebookedit`, including case variants.

1.8. The classifier shall classify explicit removal-only aliases `delete_file`, `remove_file`, `delete_directory`, and `remove_directory` as `file_remove`; patch tools shall remain `file_edit` even though their patch grammar can delete files.

1.9. The classifier shall classify the following read-oriented network aliases as `web_access`: `web.run`, `web_search`, `websearch`, `web_fetch`, `webfetch`, `web_extract`, and `x_search`, including case variants where applicable.

1.10. The classifier shall classify `browser`, `browser_action`, and the surveyed Hermes browser names (`browser_back`, `browser_cdp`, `browser_click`, `browser_console`, `browser_dialog`, `browser_get_images`, `browser_navigate`, `browser_press`, `browser_scroll`, `browser_snapshot`, `browser_type`, `browser_vision`) as `web_access`.

1.11. When a name is empty or is not in the exact known alias set, the classifier shall return `unknown` rather than inferring a category from arbitrary prefixes, suffixes, substrings, schemas, descriptions, or provider identity.

### Requirement 2: Conservative Local-Filesystem Mutation Metadata

**Objective:** As a policy or feature author, I want a simple conservative flag indicating whether a tool family can mutate the local filesystem, so that I can distinguish clearly read-oriented tools from potentially mutating capabilities without parsing tool arguments.

#### Acceptance Criteria

2.1. When a tool is classified as `file_read` or `file_search`, the system shall set `MayMutateLocalFS` to `false`.

2.2. When a tool is classified as `os_command`, `file_edit`, or `file_remove`, the system shall set `MayMutateLocalFS` to `true`.

2.3. When a tool is a read-oriented web search/fetch/extract alias listed in Requirement 1.9, the system shall set `MayMutateLocalFS` to `false`.

2.4. When a tool is an interactive browser/browser-action alias listed in Requirement 1.10, the system shall set `MayMutateLocalFS` to `true` because name-only classification cannot prove that an automation action cannot download, save, or otherwise create local artifacts.

2.5. When the category is `unknown`, the system shall set `MayMutateLocalFS` to `true` so an unfamiliar tool is never falsely asserted to be filesystem-safe.

2.6. The system shall keep `MayMutateLocalFS=true` for every OS command/terminal alias regardless of the invocation arguments; for example, an `exec_command` call that happens to run `cat`, `ls`, `rg`, or `git status` shall still be potentially mutating.

2.7. The system shall not inspect, parse, or execute tool arguments, shell command strings, schemas, descriptions, or results when deriving the flag.

2.8. The derived boolean shall be documented and tested as a potential-capability hint, not as proof of actual filesystem mutation and not as a security boundary by itself.

### Requirement 3: Enrich Canonical ToolEvent Lifecycle Items

**Objective:** As a tool policy/reactor consumer, I want every processed `lipapi.ToolEvent` to carry category and mutation metadata, including lifecycle fragments that omit the raw tool name.

#### Acceptance Criteria

3.1. The canonical `lipapi.ToolEvent` contract shall expose the derived tool category and `MayMutateLocalFS` boolean additively without changing existing tool-policy or tool-reactor method signatures.

3.2. When a `tool_call_started` event has a non-empty tool name, the system shall classify that name before the corresponding `ToolEvent` is presented to tool policies/reactors.

3.3. When a later `tool_call_args_delta` or `tool_call_finished` event has the same `ToolCallID` but no tool name, the system shall attach the classification remembered from the current lifecycle for that ID.

3.4. When a name-less tool event has no prior remembered classification for its `ToolCallID`, the system shall attach the conservative `unknown` / `MayMutateLocalFS=true` result rather than failing the stream.

3.5. When multiple tool calls are interleaved, the system shall correlate classification independently by `ToolCallID` and shall not leak metadata between IDs.

3.6. After the finished event for a tool call has completed tool-event processing, the system shall remove its remembered classification so a later reuse of the same ID cannot inherit stale state.

3.7. When the owning stream resets, is replaced, or terminates, the system shall discard all remaining classification lifecycle state with that stream.

3.8. The lifecycle correlator shall be request/stream-local and shall not introduce process-global mutable state, persistence, background goroutines, or synchronization beyond what the existing single-`Recv` stream contract requires.

### Requirement 4: Keep Classification Consistent With Tool-Name Rewrites

**Objective:** As a maintainer, I want derived metadata to follow the effective tool name after existing rewrite hooks, so that classification can never contradict the tool call that downstream code sees.

#### Acceptance Criteria

4.1. When a completed-tool-call finalizer emits a rewritten lifecycle with a new tool name, normal classification shall use that rewritten name without requiring finalizer-specific classification logic.

4.2. When a tool reactor returns `ToolRewrite` or `ToolReplace` with a non-empty `ToolName`, the hook pipeline shall recompute category and mutation metadata from that effective name before a later reactor observes the event.

4.3. When a tool reactor rewrites the same `ToolCallID` but omits `ToolName`, the pipeline shall preserve the current effective classification rather than requiring the reactor to copy derived metadata fields manually.

4.4. When a reactor replacement changes `ToolCallID` and omits the tool name, the replacement shall receive the conservative `unknown` / `MayMutateLocalFS=true` classification rather than inheriting another logical call's classification.

4.5. Reactor-provided category or mutation-field values shall not override the category derived from a non-empty effective tool name.

4.6. If a later generic response-part hook changes a tool event's non-empty `ToolName`, the runtime shall refresh the remembered lifecycle classification from the final emitted name so subsequent name-less fragments for that ID remain consistent; classification need not be added to generic `lipapi.Event` or exposed to response-part hooks.

### Requirement 5: Preserve Minimal Architecture and Existing Behavior

**Objective:** As a maintainer, I want this capability implemented as a small helper rather than a framework, so that maintenance cost stays proportional to the simple requirement.

#### Acceptance Criteria

5.1. The pure name classifier shall have no dependency on provider SDKs, frontend/backend packages, runtime configuration, network access, filesystem access, databases, or external packages.

5.2. The implementation shall use a static exact-name mapping/switch owned in one place; it shall not introduce a service registry, DI container, reflection registry, plugin registration contract, configurable rule engine, or reloadable classification table.

5.3. The implementation shall not add provider/harness-specific classification branches to frontends, backends, or executable connectors.

5.4. The implementation shall not add classification fields to generic `lipapi.Event`, provider wire DTOs, request contracts, tool definitions, or backend-plugin ABI solely for this feature.

5.5. The implementation shall not change routing, failover, capability negotiation, tool-call repair semantics, completion gates, stream framing, or the existing no-retry-after-output rule.

5.6. The implementation shall not deny, rewrite, swallow, execute, or otherwise change a tool call merely because of its category or mutation flag.

5.7. Adding a newly recognized tool name in the future shall require only a focused exact-alias mapping/test change unless a genuinely new semantic requirement appears.

### Requirement 6: Deterministic TDD and Regression Coverage

**Objective:** As a maintainer, I want the classifier and lifecycle rules proven by focused tests, so that future alias additions or hook changes cannot silently weaken the metadata contract.

#### Acceptance Criteria

6.1. Before implementation, tests shall define the full alias matrix, case/whitespace normalization, category values, and expected mutation flag.

6.2. Tests shall cover unknown and empty names and prove the conservative `unknown/true` fallback.

6.3. Tests shall cover a name-bearing start followed by name-less delta and finish events and prove stable classification across the lifecycle.

6.4. Tests shall cover at least two interleaved tool-call IDs with different categories and prove independent correlation and finish cleanup.

6.5. Tests shall cover reactor same-ID rewrite with omitted name, reactor rename to a different category, replacement to a different ID without a name, and finalizer-generated renamed lifecycle behavior.

6.6. Tests shall prove that OS command classification does not depend on command contents and that patch tools remain `file_edit/true` even when deletion is possible.

6.7. Focused `pkg/lipapi`, `internal/core/hooks`, and `internal/core/runtime` tests shall pass without requiring network access, provider credentials, or external services.

6.8. Repository quality/architecture checks shall confirm the feature adds no forbidden provider dependency or unnecessary cross-package coupling.
