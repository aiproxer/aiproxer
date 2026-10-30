# Design Validation Review

## Review Method

The design was validated as a brownfield canonical-tool-event change against:

- root `AGENTS.md` and `.kiro/AGENTS.md`;
- `.kiro/steering/structure.md`, `api-standards.md`, and `testing.md`;
- repository `main` at `dcd5f398eef9aabbb0816a99eb42070355f232ca`;
- `pkg/lipapi.ToolEvent`, `Event`, and tool-event merge behavior;
- public tool-policy and tool-reactor SDK interfaces;
- tool-reactor validation/chaining behavior;
- runtime receive/tool-event ordering;
- completed-tool-call finalizer rewrites;
- representative OpenAI-compatible lifecycle emission;
- the single-`Recv` stream concurrency contract;
- all acceptance criteria in final `requirements.md` and findings in `gap-analysis.md`.

Any design that required provider-specific tagging, argument parsing, generic-event expansion, configurable registries, new synchronization, or stale derived metadata was treated as a scope/correctness failure.

## Round 1: Stateless Helper Review

### Assessment

**Decision: NO-GO**

The first concept was only `ClassifyToolName(name)` called on each event independently. That is attractive but does not satisfy the actual canonical stream.

### Critical Issue 1: Normal name-less fragments would lose classification

**Concern:** Existing OpenAI-compatible streams may emit the tool name only on `tool_call_started`. Later `tool_call_args_delta` and `tool_call_finished` items can legally omit it.

**Impact:** Policies/reactors would see correct metadata on start and `unknown/true` on later fragments of the same call, making the augmentation internally inconsistent.

**Resolution:** Add one private request-local `ToolCallID -> classification` lifecycle map in `retryRecvStream`. Populate it from a non-empty name, inherit it on name-less fragments, and clean it at finish/reset.

**Traceability:** Requirements 3.2–3.8; Design D5, D6, D10.

### Critical Issue 2: Adding metadata to generic Event would over-expand the contract

**Concern:** An alternative fix was to store classification on every `lipapi.Event` so adapters could repeat it.

**Impact:** This would broaden codecs, continuation, traffic, frontend/backend, and connector surfaces solely to transport data that can be derived centrally.

**Resolution:** Keep generic `Event` unchanged. Put additive metadata on the focused `ToolEvent` contract and correlate names in the core runtime.

**Traceability:** Requirements 3.1, 5.3–5.4; Design D1, Components and Change Surface.

## Round 2: Rewrite and Safety Review

### Assessment

**Decision: NO-GO**

Lifecycle correlation solved missing names, but a naive cache still allowed stale metadata after hook mutations and ambiguous safety semantics.

### Critical Issue 1: Reactor rename could leave contradictory derived metadata

**Concern:** Tool reactors can return a different `ToolName`. If category/bool were copied from the incoming event, a renamed `exec_command` could still say `file_read/false`.

**Impact:** Downstream reactors/features could rely on metadata that contradicts the effective name.

**Resolution:** Derived metadata becomes core-authoritative. Every non-empty rewritten name is reclassified before the next reactor. Same-ID name-less rewrites inherit current derived metadata; changed-ID/name-less replacement falls back to `unknown/true`.

**Traceability:** Requirements 4.2–4.5; Design D6, D7.

### Critical Issue 2: Existing reactors should not need boilerplate copying

**Concern:** Existing tests and typical reactors construct fresh `ToolEvent` values containing only the fields they modify. Requiring them to copy `ToolName`, category, and bool would turn an additive contract into a broad migration.

**Impact:** Existing correct reactors could accidentally zero the new fields and require unnecessary churn.

**Resolution:** Same-ID name-less rewrites preserve current derived metadata centrally. No reactor signature or mandatory copy behavior changes.

**Traceability:** Requirement 4.3; Design D7.

### Critical Issue 3: Unknown/shell/browser mutation semantics could create false safety

**Concern:** Marking unknown tools false, trying to detect read-only shell invocations, or assuming all `web_access` tools are harmless would overstate what a name-only classifier knows.

**Impact:** The bool could be misused as a safety assertion.

**Resolution:** Unknown and OS-command tools are always `true`. Read-oriented web lookup/fetch names are false; multipurpose/interactive browser names are conservatively true. No argument/action parsing is introduced.

**Traceability:** Requirements 2.1–2.8; Design D3, D4 and classification table.

## Round 3: Late Hook and Lifecycle Review

### Assessment

**Decision: PASS after one small hardening adjustment**

### Finding: Response-part hook can rename after tool-reactor reconciliation

**Concern:** General response-part hooks run after the tool-event path. A hook could change generic `Event.ToolName`, then a later name-less fragment would inherit the pre-hook category.

**Resolution:** After response hooks, observe any final non-empty tool name and refresh the source lifecycle cache before emission. Do not expose classification fields to response hooks or add them to generic `Event`.

**Traceability:** Requirement 4.6; Design D9.

This is a small observation step, not a new hook contract.

## Requirements Traceability Review

**Decision: PASS**

- All requested categories are represented, including a dedicated removal category.
- The alias set covers the previously surveyed coding-agent tool names and case variants.
- The local-FS boolean is conservative and explicitly potential-capability metadata.
- Name-less streaming fragments preserve lifecycle classification.
- Unknown fragments fail safe without failing the stream.
- Interleaved IDs remain independent and state is cleaned at lifecycle boundaries.
- Finalizer/reactor/late response-hook renames cannot leave stale cached classification.
- The implementation remains informational and does not alter tool execution.
- No generic Event, provider wire contract, plugin ABI, persistence, config, registry, or dependency change is needed.

## Minimality Review

**Decision: PASS**

The final design adds only:

1. `ToolCategory` constants;
2. `ClassifyToolName(string) (ToolCategory, bool)`;
3. two fields on `ToolEvent`;
4. one small private runtime map helper;
5. small integration/reconciliation calls at existing tool lifecycle boundaries;
6. tests.

Explicitly rejected as unnecessary:

- `Classifier` interface;
- classifier constructor/service injection;
- config schema;
- plugin registration;
- global alias registry;
- provider/harness detector;
- command parser;
- regex/fuzzy classifier;
- persistent cache;
- mutex/goroutine;
- external package.

This satisfies the repository rule that the smallest correct diff wins.

## SOLID / Go Architecture Review

### Single Responsibility — PASS

- `pkg/lipapi` helper: name -> derived metadata only.
- runtime helper: lifecycle correlation only.
- hook bus adjustment: keep derived metadata coherent after existing mutations.

No component takes on tool execution or policy authority.

### Open/Closed — PASS for intended scope

The category contract is stable while a new known alias requires one switch/test addition. A configurable extension system is intentionally not introduced until there is an actual requirement for user-defined classification.

### Interface Segregation — PASS

No new interface is introduced. Existing policy/reactor interfaces continue accepting the same `ToolEvent` type.

### Dependency Inversion — PASS / Not Applicable Beyond Existing Seams

The feature is pure canonical logic plus core-local state. There is no driven infrastructure dependency to abstract.

## Canonical Boundary Review

**Decision: PASS**

- Category names are provider-neutral functional categories.
- No provider SDK/import is needed.
- Harness-specific raw names exist only as exact compatibility aliases inside the classifier.
- `pkg/lipapi.Event` and provider/front-end wire DTOs remain unchanged.
- The public addition is limited to the already-public focused `ToolEvent` contract and category/helper used by its consumers.

## Streaming and Concurrency Review

**Decision: PASS**

- Streaming remains the primary path.
- Lifecycle state is keyed by active source `ToolCallID` only.
- State is scoped to one `retryRecvStream` and is not shared between requests.
- Existing single-`Recv` ownership means no new synchronization is necessary.
- Finish and inner-stream replacement/reset cleanup prevent stale ID reuse.
- No goroutine or blocking operation is introduced.

## Tool Policy / Security Semantics Review

**Decision: PASS with explicit limitation**

`MayMutateLocalFS` is a conservative hint, not an enforcement boundary. Existing ordering remains unchanged: tool policy executes before tool reactors. A reactor can therefore rename a tool after an earlier policy decision, exactly as it can today.

This spec does **not** reorder policy/reactor execution or claim the bool alone makes policy decisions secure. If a future security feature needs to authorize the post-rewrite effective operation, that is a separate policy-ordering requirement and must be designed explicitly.

## Retry/Failover and Protocol Review

**Decision: PASS**

The classifier does not affect candidate selection, backend opening, commitment, retry/failover, completion gates, output settlement, frontend encoding, or the no-retry-after-output invariant. It is metadata-only processing on events already in the canonical client-facing path.

## Testability Review

**Decision: PASS**

The design is directly testable without mocks or network:

- table-driven pure classifier tests;
- existing ToolEvent projection tests;
- existing hook-bus reactor tests;
- focused runtime stream tests using canonical events/fixed streams.

No integration service or credential is required.

## Final Spec Coherence Review

The final requirements, gap analysis, research, design, and implementation tasks were cross-checked for scope and traceability.

**Final decision: GO**

No unresolved P0/P1 design issue remains. The spec is ready for implementation after normal project approval. The PR should remain spec-only; no Go implementation is part of this workflow.
