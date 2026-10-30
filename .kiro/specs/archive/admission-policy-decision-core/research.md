# Implementation Gap Analysis: admission-policy-decision-core

Date: 2026-07-02

## Analysis Status

- Requirements are generated but not yet approved in `spec.json`; this gap analysis proceeds because it can inform design and possible requirement revisions.
- No external dependency research was needed. The feature is a brownfield contract/lifecycle/evidence change over existing extension, auth, scope, runtime, and frontend error surfaces.
- Core steering emphasizes a small policy-owning Go core, explicit plugin/SDK seams, canonical-in-the-middle translation, streaming-first execution, secure-session authority before backend work, and no provider or transport leakage into core contracts.

## Current State Investigation

### Existing assets

- Legal extension stage inventory already exists in `pkg/lipsdk/feature/stages.go`, including transport auth, session open, submit, tool catalog, request-wide shaping, pre-request admission, route hinting, attempt lifecycle, stream event mutation, tool event reaction, completion gating, traffic observation, and egress encoding.
- Core mirrors legal stage inventory and default failure policies in `internal/core/extensions/pipeline.go` and `internal/core/extensions/failure_policy.go`.
- `RequestRuntimeSnapshot` freezes extension chains and facades per request/build, including tool policies, request transforms, pre-request handlers, route hints, completion gates, traffic redactors, usage observer, and traffic observer (`internal/core/extensions/snapshot.go`).
- Stage runners already exist for request transforms, pre-request admission, tool policy, tool catalog filtering, route hints, and completion gates (`internal/core/extensions/request_transform.go`, `internal/core/extensions/pre_request.go`, `internal/core/extensions/tool_policy.go`, `internal/core/extensions/completion_run.go`).
- Existing SDK surfaces already represent several localized decision models: pre-request `Decision{Deny, DenyMessage, Annotations}`, tool policy `DecisionAllow/DecisionDeny`, tool reactor `ToolPass/ToolRewrite/ToolSwallow/ToolReplace`, and completion `OutcomePassOriginal/OutcomeReplace/OutcomeReplayOriginal/OutcomeReject`.
- Hook failure posture exists as `hooks.FailureMode` with fail-open/fail-closed behavior (`pkg/lipsdk/hooks/failure.go`), and stage runners already isolate extension panics through `internal/core/safety`.
- The control-plane scope foundation is present. `pkg/lipsdk/scope.PrincipalScopeView` carries safe attribution; `internal/core/execctx.Views` carries both authoritative scope and legacy principal projection; usage and traffic observer contracts include safe scope metadata (`pkg/lipsdk/scope/view.go`, `internal/core/execctx/views.go`, `pkg/lipsdk/usage/observe.go`, `pkg/lipsdk/traffic/observe.go`).
- Runtime invokes request transform and pre-request stages before route planning/backend attempt creation, then attaches secure-session views after BeginTurn (`internal/core/runtime/executor_prepare_secure.go`).
- Runtime invokes tool policy before tool reactors and emits scope-aware usage/traffic observations from the stream path (`internal/core/runtime/attempt_stream.go`).
- Frontend execution error classification distinguishes capability rejects, pre-request rejects, context-limit rejects, session denials, and internal failures (`internal/plugins/frontends/execerr/execerr.go`).

### Existing patterns and constraints

- Public plugin-facing contracts belong under `pkg/lipsdk/*`; core-owned orchestration and shared semantics belong under `internal/core/*`.
- New advanced behavior should extend the extension platform or feature plugins instead of adding provider/frontend branches to executor or adapters.
- Stage ordering is already core-owned and tested. New decision semantics should reuse or deliberately extend this legal pipeline rather than inventing a parallel stage taxonomy.
- The runtime is streaming-first. Completion gates buffer only bounded completion snapshots; normal non-streaming must remain collection over the canonical stream.
- Secure-session authority is before backend execution. Admission decisions must not let client hints bypass BeginTurn/resume validation.
- Frontends map executor errors to protocol-legal wire shapes. New policy-denial/error roots need stable classification so each frontend can render them safely.
- Observability must preserve bounded cardinality and avoid raw prompts, secrets, backend payloads, paths, and user-controlled strings in metric labels or high-volume logs.
- Existing hooks use per-hook failure modes and stage-level defaults, but evidence is mostly metrics/logs, not a unified policy-decision event model.

## Requirement-to-Asset Map

| Requirement | Existing assets | Gap classification | Notes |
| --- | --- | --- | --- |
| R1 Protocol-neutral decision vocabulary | Stage descriptors; localized decisions in prerequest, toolpolicy, hooks, completion | Missing / Partial | Multiple decision enums exist, but no shared outcome/reason/evidence vocabulary spans stages. |
| R2 Scope-aware decision context | `PrincipalScopeView`, `execctx.Views`, usage/traffic scope propagation | Partial | Scope exists, but many policy metas still expose only `PrincipalView`; decision-specific context does not consistently include scope/origin/parent details. |
| R3 Admission lifecycle coverage | Legal stages; request transform and pre-request before route planning; tool policy in stream; completion gates | Partial | Lifecycle positions exist, but no common admission decision facade expresses which outcomes are legal at each position. |
| R4 Deterministic policy composition | MaterializeSorted helpers; frozen snapshot order; stage runners | Partial | Ordering exists per surface, but cross-stage conflict semantics and shared evidence for multiple providers are missing. |
| R5 Client-safe outcomes and protocol compatibility | `execerr.ClassifyExecute`; session denial and pre-request rejection mapping | Missing / Partial | There is no stable policy denial/rejection error root or frontend classification for general policy outcomes. |
| R6 Failure, timeout, cancellation behavior | `FailureMode`; stage defaults; panic isolation; context checks in runners | Partial | Fail-open/fail-closed exists, but timeout budgets and unified malformed-decision handling are inconsistent across surfaces. |
| R7 Audit-safe decision evidence | auth events, session diagnostics, usage/traffic observers, extension metrics/logging | Missing / Partial | Decision evidence is not a first-class event correlated with scope, stage, provider id, outcome, reason, and attempt lineage. |
| R8 Routing, streaming, and continuity invariants | B2BUA/attempt lineage, no retry after output, completion gate outputCommitted behavior | Constraint / Partial | Core invariants exist; design must ensure new decisions cannot weaken them and must add explicit no-backend-attempt evidence for denied admission. |
| R9 Compatibility with existing extension surfaces | Existing SDK interfaces and reference plugins; feature bundle registration | Constraint / Partial | Must preserve old hook/plugin behavior while optionally projecting old outcomes into new evidence. |
| R10 Explicit scope exclusions | Requirements boundary and steering | Constraint | No concrete budget/rate/redaction/safety/provisioning/admin feature should be implemented in this spec. |

## Missing Capabilities

1. **Shared decision contract**: No single SDK/core contract represents stage, provider id, outcome, reason code, client-safe message/category, evidence visibility, and optional annotations across existing decisions.
2. **Stage legality matrix for decision outcomes**: Stage descriptors identify mutation roles, but no enforceable matrix says which shared outcomes are legal at each lifecycle position.
3. **Scope-rich policy metadata**: Current `prerequest.Meta`, `request.RequestMeta`, `toolpolicy.Meta`, `hooks.ToolMeta`, and completion metadata rely mainly on legacy principal/session/workspace views; they do not consistently carry `PrincipalScopeView`.
4. **Unified policy error taxonomy**: Existing errors cover capability reject, hook mutation, session denial, and pre-request rejection, but there is no general policy deny/failure/malformed-decision root for frontends and diagnostics.
5. **Decision evidence event model**: Extension stages log errors and emit metrics, but there is no structured policy decision event emitted for allow/deny/mutate/replace/skip/fail-open/fail-closed outcomes.
6. **Conflict-resolution semantics**: Existing runners define local chain behavior, but no common policy explains incompatible outcomes across multiple decision providers in one lifecycle position.
7. **Timeout budget semantics**: Requirements mention decision evaluation time budgets, but current stage contracts do not expose a shared budget model; some plugin implementations may use their own contexts/timeouts.
8. **Compatibility projection**: Existing decisions can be mapped to a shared vocabulary, but no adapter/projection layer currently records that mapping while preserving old behavior.

## Integration Challenges

- **Public SDK stability**: Adding fields or packages under `pkg/lipsdk` is possible, but existing plugins must continue compiling and behaving the same unless they opt in.
- **Avoiding a god policy package**: The feature wants shared semantics but must not centralize every concrete future enterprise policy into core.
- **Frontend-safe rendering**: A new policy denial root must be general enough for all frontends while keeping protocol-specific HTTP/SSE/error-body rendering at the frontend edge.
- **Streaming response policy**: Completion-gate and tool-event decisions happen after backend output may have begun. Design must preserve the no-transparent-failover-after-output invariant.
- **Evidence volume and sensitivity**: Emitting evidence for every allow/pass decision could be noisy. Design must choose default evidence granularity while satisfying auditability.
- **Auxiliary request provenance**: Scope can represent internal origin and parent trace id, but decision context must define how internal requests inherit or suppress policies without causing recursion.
- **Error vs decision semantics**: Today some denials are represented as errors. Design must decide whether shared decisions become errors only at surfacing boundaries or whether runners return both decision records and errors.

## Implementation Approach Options

### Option A: Extend Existing Stage Contracts In Place

Add shared decision/evidence fields directly to existing SDK metadata and outcomes for pre-request, tool policy, tool reactors, request transforms, and completion gates.

**Likely modules touched**
- `pkg/lipsdk/prerequest`, `pkg/lipsdk/toolpolicy`, `pkg/lipsdk/hooks`, `pkg/lipsdk/request`, `pkg/lipsdk/completion`
- `internal/core/extensions/*` stage runners
- `internal/plugins/frontends/execerr`
- Runtime call sites in `internal/core/runtime/*`

**Trade-offs**
- Pros: Minimal new concepts; follows existing extension surfaces; easy to update local tests.
- Pros: Existing stage runners remain the primary execution path.
- Cons: Repeats shared fields across packages; risks inconsistent behavior and docs; may bloat existing narrow contracts.
- Cons: Harder to add cross-stage evidence and conflict semantics without duplication.

**Effort/Risk**: M / Medium. Fastest path but higher long-term consistency risk.

### Option B: Create A New Shared Policy Decision SDK/Core Contract

Introduce a dedicated shared decision package/contract and have existing stage runners produce or project into that contract while preserving legacy plugin interfaces.

**Likely modules touched**
- New `pkg/lipsdk/policy` or similarly named SDK package for decision, reason, lifecycle position, evidence, failure behavior, and scope-aware context shapes.
- New or extended core helper under `internal/core/extensions` or `internal/core/policy` to validate stage/outcome legality and record evidence.
- Existing stage runners updated to convert current outcomes into shared decision records.
- `internal/plugins/frontends/execerr` updated to classify shared policy errors/denials.

**Trade-offs**
- Pros: Clean shared vocabulary; avoids duplicating reason/evidence semantics; future enterprise policies get one obvious contract.
- Pros: Existing interfaces can remain compatible through adapters/projection.
- Cons: New public surface requires careful naming and versioning; design must avoid overgeneralization.
- Cons: More upfront work and integration tests.

**Effort/Risk**: L / Medium. Best long-term fit, but requires disciplined scope control.

### Option C: Hybrid Compatibility Layer

Add a shared decision/evidence contract, but do not replace existing plugin interfaces in the first implementation. Existing runners keep their current inputs/outputs and emit shared decision records through a compatibility projection. New opt-in policy providers can use the shared contract later.

**Combination strategy**
- Keep current stage interfaces source-compatible.
- Add shared decision records and validation helpers.
- Project pre-request/tool-policy/tool-reactor/completion outcomes into shared evidence.
- Add a stable policy denial/failure error taxonomy for frontend mapping.
- Add scope to stage metadata where additive and safe.

**Trade-offs**
- Pros: Preserves compatibility while establishing the foundation required by later features.
- Pros: Gives design room to introduce first-class decision evidence without forcing a wholesale plugin migration.
- Pros: Aligns with the previous `control-plane-principal-scope` hybrid compatibility pattern.
- Cons: Two representations coexist temporarily; tests must prove projection consistency.
- Cons: New opt-in provider model may still need a follow-up spec.

**Effort/Risk**: L / Medium. Most balanced approach for brownfield adoption.

## Feasibility and Complexity

- Overall feasibility: High. The repository already has legal extension stages, deterministic ordering, fail-open/fail-closed handling, scope attribution, frontend error classification, usage/traffic observers, and rich tests.
- Estimated effort: L (1-2 weeks). The work crosses public SDK contracts, core extension runners, runtime integration, frontend error classification, and diagnostics/evidence tests.
- Risk: Medium. Main risks are public surface churn, evidence leakage, behavior regressions in existing hooks, and accidentally weakening streaming/failover invariants.
- Complexity signal: Cross-cutting contract and lifecycle integration, not external-service integration or complex algorithmic work.

## Recommendations For Design Phase

- Prefer **Option C** unless design finds that a smaller Option B can remain backward-compatible without migration risk.
- Define a minimal shared decision record first: lifecycle position, provider id, outcome, reason code, client-safe category/message, failure behavior, safe scope, trace/A-leg/B-leg/attempt ids, and evidence visibility.
- Keep existing plugin interfaces source-compatible in this spec; add additive scope fields to metadata where needed.
- Add a stable policy denial/failure/malformed-decision error taxonomy and update frontend classification without changing frontend wire success shapes.
- Define a stage/outcome legality table using existing legal pipeline stage ids instead of inventing new stage names.
- Emit decision evidence through bounded-cardinality structured events/logs/observers; avoid raw prompts, raw payloads, secrets, headers, and high-cardinality labels.
- Revalidate pre-output vs post-output behavior, especially completion gates, tool policies, and stream event mutation.

## Research Needed During Design

1. Decide the public package/name for shared decision contracts (`policy`, `decision`, or another non-conflicting name) without colliding with `internal/core/policy` routing/circuit concepts.
2. Decide whether decision evidence is emitted via existing usage/traffic observers, a new observer seam, structured logs, diagnostics records, or a combination.
3. Decide default evidence granularity for allow/pass outcomes to balance auditability and volume.
4. Decide how time budgets are configured or inherited for decision providers without adding concrete enterprise policy config in this spec.
5. Decide how policy denial/failure maps to each frontend's protocol-legal response shape and streaming terminal behavior.
6. Decide how auxiliary/internal requests inherit, suppress, or record policy decisions to avoid recursive policy loops.
7. Decide how much compatibility projection should be visible in SDK vs kept as core-internal evidence mapping.

## Files Most Likely Relevant In Design

- `pkg/lipsdk/feature/stages.go`
- `pkg/lipsdk/scope/view.go`
- `pkg/lipsdk/prerequest/handler.go`
- `pkg/lipsdk/request/transform.go`
- `pkg/lipsdk/toolpolicy/policy.go`
- `pkg/lipsdk/hooks/toolreactor.go`
- `pkg/lipsdk/completion/outcome.go`
- `pkg/lipsdk/completion/gate.go`
- `pkg/lipsdk/usage/observe.go`
- `pkg/lipsdk/traffic/observe.go`
- `internal/core/extensions/pipeline.go`
- `internal/core/extensions/failure_policy.go`
- `internal/core/extensions/pre_request.go`
- `internal/core/extensions/request_transform.go`
- `internal/core/extensions/tool_policy.go`
- `internal/core/extensions/completion_run.go`
- `internal/core/extensions/snapshot.go`
- `internal/core/execctx/views.go`
- `internal/core/runtime/executor_prepare_secure.go`
- `internal/core/runtime/attempt_stream.go`
- `internal/plugins/frontends/execerr/execerr.go`
- `internal/core/hooks/toolreactor_test.go`

---

# Design Discovery and Synthesis Update

Date: 2026-07-02

## Summary

- **Feature**: `admission-policy-decision-core`
- **Discovery Scope**: Extension
- **Key Findings**:
  - Existing legal extension stages, frozen runtime snapshots, and stage runners provide the right integration points.
  - Existing decisions are fragmented across pre-request, request transform, tool policy, tool reactor, and completion packages.
  - The prior principal-scope foundation supplies safe attribution for decision context and evidence.

## Research Log

### Extension Point Analysis
- **Context**: The design must add shared decision semantics without replacing current plugin APIs.
- **Sources Consulted**: `pkg/lipsdk/feature`, `pkg/lipsdk/prerequest`, `pkg/lipsdk/request`, `pkg/lipsdk/toolpolicy`, `pkg/lipsdk/hooks`, `pkg/lipsdk/completion`, `internal/core/extensions`, `internal/core/runtime`.
- **Findings**:
  - Legal stage IDs and mutation roles already exist and should be reused.
  - Stage runners already handle deterministic order, fail-open/fail-closed behavior, panic isolation, and metrics.
  - Runtime already places request-wide and pre-request stages before route planning and tool/completion stages on the streaming path.
- **Implications**: The design can add a projection/evidence layer instead of introducing a parallel pipeline.

### Error Classification Analysis
- **Context**: Policy outcomes must surface through frontend-legal errors.
- **Sources Consulted**: `pkg/lipapi/errors.go`, `internal/plugins/frontends/execerr/execerr.go`, frontend parity conventions.
- **Findings**:
  - Existing classification separates session denials, capability rejects, pre-request rejects, context-limit rejects, and internal errors.
  - There is no general policy denial/failure/malformed decision taxonomy.
- **Implications**: Add stable policy error roots in `pkg/lipapi` and classify them in `execerr` without moving rendering into core.

### Evidence Emission Analysis
- **Context**: Requirements need audit-safe decision evidence independent of usage/traffic observers.
- **Sources Consulted**: `pkg/lipsdk/usage`, `pkg/lipsdk/traffic`, extension metrics, scope contracts.
- **Findings**:
  - Usage and traffic observers now carry scope, but making them interpret policy semantics would mix concerns.
  - A dedicated observer seam keeps policy evidence separate and future control-plane storage can subscribe later.
- **Implications**: Add `policydecision.Observer` with no-op/chain implementations and keep usage/traffic observers unchanged.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| Extend existing contracts only | Add evidence fields to each hook package | Small initial diff | Duplicates semantics and weakens consistency | Rejected for long-term fragmentation risk |
| New mandatory policy interface | Replace existing stage APIs with shared decision providers | Clean future model | Breaks existing plugins and over-scopes migration | Rejected for compatibility |
| Hybrid compatibility projection | Add shared decision record and project legacy outcomes | Preserves behavior and establishes foundation | Requires projection consistency tests | Selected |

## Design Decisions

### Decision: Add `pkg/lipsdk/policydecision` For Shared Decision Evidence
- **Context**: Requirements need one vocabulary across multiple lifecycle stages.
- **Alternatives Considered**:
  1. Extend every existing hook package separately.
  2. Add one shared SDK package and project existing outcomes into it.
- **Selected Approach**: Add `pkg/lipsdk/policydecision` for shared records, context, legality descriptors, and observers.
- **Rationale**: This keeps public policy semantics stable without making any existing plugin migrate.
- **Trade-offs**: Adds a new SDK package, but avoids repeated decision fields across five existing packages.
- **Follow-up**: Verify public surface imports no internal packages or provider SDKs.

### Decision: Keep Existing Extension Interfaces Source-Compatible
- **Context**: Existing official and reference plugins consume current stage interfaces.
- **Alternatives Considered**:
  1. Require migration to a new `PolicyProvider` interface.
  2. Preserve current interfaces and add projection/evidence in core stage runners.
- **Selected Approach**: Preserve current interfaces for this spec.
- **Rationale**: Requirements explicitly require compatibility and no mandatory migration.
- **Trade-offs**: Legacy and shared decision representations coexist temporarily.
- **Follow-up**: Add projection tests for all current outcome types.

### Decision: Evidence Uses A Dedicated Observer Seam
- **Context**: Requirements require operator evidence without making usage/traffic observers interpret policy semantics.
- **Alternatives Considered**:
  1. Reuse usage observer.
  2. Reuse traffic observer.
  3. Add policy decision observer.
- **Selected Approach**: Add a dedicated policy decision observer with no-op and chain implementations.
- **Rationale**: Keeps concerns separate and lets later persistence/reporting specs subscribe cleanly.
- **Trade-offs**: Adds one more observer seam to runtime snapshots.
- **Follow-up**: Keep no-op overhead minimal.

### Decision: Stable Errors Live In `pkg/lipapi`
- **Context**: Frontends need protocol-neutral classification roots.
- **Alternatives Considered**:
  1. Keep errors private to core extensions.
  2. Put stable error roots in `pkg/lipapi`.
- **Selected Approach**: Add policy decision error roots to `pkg/lipapi`.
- **Rationale**: Existing frontend classification already depends on `lipapi` roots for canonical execution failures.
- **Trade-offs**: Expands public canonical error surface, but only for shared policy semantics.
- **Follow-up**: Keep error payloads safe and provider-neutral.

## Risks & Mitigations

- Public SDK surface sprawl — keep `policydecision` small and record-only; defer first-class policy providers.
- Evidence leakage — default records exclude raw prompts, payloads, headers, secrets, and unbounded values; tests verify this.
- Behavior regressions — projection happens after existing outcomes are computed and must not change runner return behavior.
- Stream invariant regressions — post-output tests verify no transparent retry or failover after policy outcomes.
- Observer overhead — no-op observer is default and record cloning occurs only on emission.

## References

- `.kiro/steering/product.md` — core product boundaries and plugin extensibility promise.
- `.kiro/steering/tech.md` — streaming, security posture, extension platform, and dependency rules.
- `.kiro/steering/structure.md` — package ownership and where to change code.
- `.kiro/steering/routing-and-orchestration.md` — no retry/failover after first visible output.
- `.kiro/steering/api-standards.md` — protocol/legal error mapping boundaries.
