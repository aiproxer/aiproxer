# Brownfield Requirements Gap Analysis

## Scope and Method

This analysis compares the final requirements for `tool-call-classification` against repository `main` at `dcd5f398eef9aabbb0816a99eb42070355f232ca`.

The review covers:

- canonical streaming events and tool-event projection in `pkg/lipapi`;
- tool-policy and tool-reactor SDK contracts in `pkg/lipsdk`;
- tool-reactor mutation validation/merge behavior in `internal/core/hooks`;
- the client-facing receive path in `internal/core/runtime`;
- completed-tool-call assembly/finalizer rewrites;
- representative OpenAI-compatible stream mappings where later fragments omit `ToolName`;
- current stream concurrency/lifecycle guarantees;
- Kiro steering for canonical ownership, streaming-first behavior, TDD, and minimal architecture.

Classifications:

- **Missing** — required capability does not exist.
- **Partial** — reusable machinery exists but does not satisfy the requirement alone.
- **Constraint** — existing public/runtime behavior constrains the design.
- **Risk** — brownfield behavior can make seemingly simple implementation incorrect unless explicitly covered.

## Current Assets Worth Preserving

### ToolEvent already centralizes tool-policy consumption

`pkg/lipapi.ToolEventFromEvent` is the canonical projection used immediately before tool policy/reactor processing. Both `pkg/lipsdk/toolpolicy.Policy` and `pkg/lipsdk/hooks.ToolReactor` already consume `lipapi.ToolEvent` by value. This provides a focused additive metadata seam without introducing new hook signatures.

### Generic Event already carries the source evidence needed for classification

`lipapi.Event` carries `ToolCallID` and `ToolName` when producers have them. Classification can be derived inside the core tool-processing path; generic event codecs do not need new fields.

### Runtime has one tool-policy/reactor choke point

`retryRecvStream.dispatchClientFacingEvent` converts each applicable event to `ToolEvent`, then `handleToolEventPath` runs policies and reactors. This prevents provider-specific integration and naturally covers live backend events plus finalized/replayed tool lifecycles.

### Stream concurrency is already single-consumer

`retryRecvStream` explicitly disallows concurrent `Recv`. A request-local classification map can therefore remain a plain map with no mutex or goroutine.

### Existing rewrite paths are explicit

Completed-call finalizers synthesize a new complete lifecycle after a rewrite, and tool reactors return typed pass/rewrite/replace/swallow decisions. The feature can rederive metadata at these existing boundaries instead of creating a new mutation layer.

## Gap Register

| ID | Severity | Class | Current finding | Required disposition |
|---|---:|---|---|---|
| G-01 | P0 | Missing | No canonical tool category or local-FS mutation hint exists. | Add one small enum/result/helper and additive `ToolEvent` fields. |
| G-02 | P0 | Partial | `ToolEventFromEvent` is centralized but currently copies only ID/name/args. | Perform initial name classification there or immediately at the runtime enrichment boundary. |
| G-03 | P0 | Risk | OpenAI-compatible delta/finished events commonly omit `ToolName`. | Keep per-stream `ToolCallID -> classification` lifecycle state. |
| G-04 | P0 | Risk | Tool reactors can rewrite/replace `ToolName`, while existing reactors often omit an unchanged name in fresh `ToolEvent` literals. | Rederive on non-empty rename; preserve current classification for same-ID name-less rewrites. |
| G-05 | P0 | Risk | A replacement can change `ToolCallID`; blindly inheriting state would cross logical calls. | Different-ID/no-name replacement becomes conservative `unknown/true`. |
| G-06 | P1 | Risk | Generic response-part hooks run after tool reactors and may mutate `Event.ToolName`. | Observe a final non-empty emitted name and refresh lifecycle state; do not expand generic `Event`. |
| G-07 | P1 | Constraint | `ToolEvent` is a public canonical type used by SDK interfaces. | Keep the change additive and avoid changing interface method signatures. |
| G-08 | P1 | Constraint | Generic `lipapi.Event` is used broadly by streams, codecs, traffic, continuation, and frontends. | Do not add derived classification fields to it for this feature. |
| G-09 | P0 | Risk | Treating unknown names as non-mutating would create false safety. | Unknown must be `unknown/true`. |
| G-10 | P0 | Risk | Parsing shell commands to refine the bool would turn a name classifier into an incomplete command analyzer. | OS-command family remains `true` regardless of arguments; inspect names only. |
| G-11 | P1 | Risk | Browser tools vary from read-like snapshots to actions that can trigger downloads/artifacts. | Keep one `web_access` category and conservatively mark interactive browser automation `true`. |
| G-12 | P1 | Constraint | Surveyed harnesses normally delete through patch or shell, not a dedicated removal tool. | Keep `file_remove` as a category with a tiny exact generic removal alias set; patch remains `file_edit/true`. |
| G-13 | P1 | Constraint | Project rules favor smallest correct diff and no speculative seams. | No service interface, config, registry, persistence, provider detector, or new dependency. |
| G-14 | P1 | Partial | Existing tests cover ToolEvent projection/reactor rewrites but not derived metadata or lifecycle correlation. | Add table-driven classifier tests plus focused hook/runtime lifecycle tests first. |

## Requirements Review Round 1

### Initial interpretation

The first pass treated classification as a purely stateless helper applied independently to each canonical tool event.

### Finding R1-A: Stateless classification loses metadata on normal stream fragments

**Problem:** Existing adapters may provide the name only on `tool_call_started`. Subsequent argument deltas and the finish event can carry only `ToolCallID`. A stateless helper would classify the start correctly and later events as unknown.

**Remediation:** Final Requirement 3 now requires a tiny request-local lifecycle correlator keyed by `ToolCallID`, with finish/reset cleanup and independent interleaved IDs.

### Finding R1-B: A generic Event field would be unnecessary surface expansion

**Problem:** One way to carry classification would be to add it to every generic `lipapi.Event`, but that would touch broad stream/codec/continuation surfaces even though tool policies/reactors already use a focused projection.

**Remediation:** Boundary Context and Requirement 5 now explicitly keep generic `lipapi.Event` unchanged and place derived metadata on `ToolEvent` only.

## Requirements Review Round 2

### Finding R2-A: Mutation bool needs conservative semantics

**Problem:** A shell/exec tool can run either read-only or mutating commands, and an unknown tool can do anything. Attempting to infer actual behavior from the name or arguments would create false precision.

**Remediation:** Requirement 2 defines the bool as potential capability. Every OS-command tool and unknown tool is always `true`; no arguments are inspected.

### Finding R2-B: Browser automation cannot be accurately split by one coarse category

**Problem:** Web-search/fetch tools are read-only network retrieval, while multipurpose browser tools may trigger downloads or saved artifacts. Treating all `web_access` names identically for the bool would either overstate simple search or understate browser automation.

**Remediation:** Category remains `web_access`, while Requirement 2 distinguishes read-oriented lookup/fetch aliases (`false`) from interactive browser automation aliases (`true`). No second browser category or action parser is introduced.

### Finding R2-C: File removal category would otherwise be unusable

**Problem:** The surveyed harnesses largely perform deletion via patch/shell, so an alias table derived strictly from their dedicated primitives would leave `file_remove` with no direct name.

**Remediation:** Requirement 1.8 adds only four explicit generic removal-only aliases (`delete_file`, `remove_file`, `delete_directory`, `remove_directory`). It does not add fuzzy `delete_*` matching. `patch`/`apply_patch` remain `file_edit/true`.

## Requirements Review Round 3

### Finding R3-A: Tool reactor rename can stale or contradict metadata

**Problem:** If metadata is enriched before reactors and a reactor renames the tool, copying the original category would make the event internally contradictory. Conversely, existing reactors often construct fresh events without copying unchanged fields.

**Remediation:** Requirement 4 makes derived metadata authoritative from the effective name: non-empty names are reclassified, same-ID name-less rewrites inherit current derived metadata, and different-ID/name-less replacements get `unknown/true`.

### Finding R3-B: Late response hook rename can affect the next name-less fragment

**Problem:** The general response-part hook runs after the tool reactor path. If it changes `Event.ToolName`, a later name-less fragment could otherwise inherit the pre-hook classification.

**Remediation:** Requirement 4.6 requires the runtime to refresh lifecycle state from a final non-empty emitted tool name. Classification still does not become a generic Event field or response-hook contract.

## Requirements Quality Gate

The final `requirements.md` was checked against the requested scope and brownfield findings.

**PASS**

- Category taxonomy is explicit and bounded.
- Alias behavior is deterministic and exact-name based.
- `MayMutateLocalFS` semantics are conservative and do not claim execution evidence.
- Normal name-less stream fragments are covered.
- Tool-name rewrites cannot leave stale classification.
- Unknown tools fail safe to `true` without blocking execution.
- Public API impact is additive to an existing focused canonical type.
- No provider-specific changes, argument parsing, dynamic registry, configuration, persistence, new dependency, or policy engine is required.
- The feature remains informational and does not alter routing, tool execution, or retry semantics.

No unresolved requirements gap remains that requires broadening the feature.
