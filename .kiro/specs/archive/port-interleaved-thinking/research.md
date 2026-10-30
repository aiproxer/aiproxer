# Gap Analysis: port-interleaved-thinking

## Context

Requirements are generated but not yet approved. This analysis proceeds because gap validation can inform requirement revisions before design.

The feature requests Python LIP interleaved thinking parity in Go LIP: `[thinker]` selector syntax, thinker-aware weighted cycles, thinker prompt/tool behavior, memo capture, hidden/visible continuation, memo injection, session continuity, parallel executor hybrid behavior, diagnostics, and non-interference for existing routes.

## Current State Investigation

### Existing Assets

- `internal/core/routing/selector.go` defines the selector AST: `Selector`, `FailoverAlt`, `Primary`, `Weighted`, `WeightedBranch`, and `Parallel`.
- `internal/core/routing/parser.go` parses route selectors and prefix annotations for `[weight]`, `[first]`, context limits, TTFT timeouts, and parallel handicaps.
- `internal/core/routing/weighted.go` implements `pickWeighted`, including first-request steering via `WeightedBranch.IsFirst` and `AttemptCandidate.MarkedFirst`.
- `internal/core/routing/planner.go` defines `SessionRoutingState`, `PlanOptions`, `AttemptCandidate`, and `ExpandFailoverGroups` for health/exclusion-aware planning.
- `internal/core/runtime/executor.go` parses the selector, resolves aliases, creates `SessionRoutingState` from `ALegRecord.WeightedFirstConsumed`, opens one planned candidate or parallel group, and returns a `retryRecvStream`.
- `internal/core/runtime/executor_open_attempt.go` performs candidate open, capability negotiation, hook stages, B-leg creation, and `[first]` consumption persistence.
- `internal/core/runtime/attempt_stream.go` and `internal/core/streamrecovery` own receive-phase recovery and the no-silent-retry-after-output policy.
- `pkg/lipapi/call.go` already has `Call.Instructions`, `Call.Messages`, `Call.Tools`, `Call.ToolChoice`, and `Call.Extensions` for request shaping.
- `pkg/lipapi/events.go` already has `EventReasoningDelta`, `EventTextDelta`, validation, and collection limits.
- `pkg/lipsdk/feature/bundle.go` exposes feature contribution points: request transforms, completion gates, response hooks, traffic observers, raw capture sinks, and lifecycle hooks.
- `pkg/lipsdk/request/transform.go` exposes request-wide transforms with `RequestMeta` and feature services.
- `pkg/lipsdk/completion/gate.go` exposes completion gates over bounded buffered completions.
- `internal/core/b2bua/store.go` and continuity stores persist A-leg/B-leg continuity, including current `[first]` state through `WeightedFirstConsumed`.
- `internal/core/securesession` owns authoritative session begin/resume policy before upstream execution.
- `internal/plugins/features/` contains official feature plugins and examples for registration/config/test shape.

### Existing Constraints and Patterns

- Routing selector semantics are core-owned product behavior.
- Request/response mutation should live behind hooks or extension stages, not inside provider adapters.
- Runtime orchestration owns backend attempts and continuation-like behavior; feature plugins should not directly open backends.
- Provider SDK and wire types must stay out of `pkg/lipapi`, `pkg/lipsdk`, and `internal/core`.
- Streaming is primary; non-streaming is collection over canonical events.
- The no-retry-after-client-visible-output rule is a hard invariant.
- Continuity and secure-session state must respect proxy-owned session authority.
- Tests are expected before implementation, with routing, streaming, continuity, and extension interactions treated as high-value targets.

### Existing Tests and Documentation

- `internal/core/routing/parser_test.go` has `[first]` and Python parity-style selector tests.
- `internal/core/routing/planner_test.go` covers weighted behavior, `[first]`, retry-path handling, context constraints, and deterministic RNG.
- `internal/core/routing/parser_parallel_test.go` covers parallel parsing and invalid weighted/parallel mixing.
- `internal/core/runtime/executor_characterization_test.go` covers `[first]` persistence.
- `internal/core/b2bua/store_test.go` and continuity store tests cover state persistence patterns.
- `docs/python-to-go-migration.md` currently mentions routing parity areas and should be updated when `[thinker]` lands.
- `docs/feature-migration-map.md` is the migration anchor for Python-era features.
- `.kiro/steering/routing-and-orchestration.md` does not yet mention thinker semantics; design should decide whether the feature materially updates routing steering.

## Requirement-to-Asset Map

| Requirement | Existing Assets | Gap Type | Gap Summary |
| --- | --- | --- | --- |
| 1. Selector syntax and validation | `parser.go`, `selector.go`, parser tests | Missing | No `[thinker]` annotation, no true/false value parsing, no single-thinker validator, no `[first]+[thinker]` rejection. |
| 2. Thinker-aware weighted cycle | `weighted.go`, `planner.go`, `SessionRoutingState` | Missing | No cycle state, deterministic thinker sequence, suppression flag, or no-eligible-non-thinker path. |
| 3. Thinker request behavior | `lipapi.Call`, request transforms, executor open path | Missing / Constraint | Request model can support instructions/tool suppression, but selection metadata and timing relative to capability negotiation need design. |
| 4. Memo capture/extraction | canonical events, collectors, completion gates | Missing | No memo extractor, no tag stripping, no interrupted memo metadata, no bounded thinker recorder. |
| 5. Executor memo injection | `request.Transform`, `Call.Instructions`, `Call.Messages` | Missing / Constraint | Existing transform seam can inject context, but memo retrieval and dedupe semantics are absent. |
| 6. Hidden/visible continuation | executor stream flow, stream recovery policy, lifecycle coordinator | Missing / High Risk | Runtime currently returns one stream after opening one candidate/group; thinker then executor continuation is a new orchestration pattern. |
| 7. Parallel hybrid parity | `Parallel`, `Weighted`, parser rejection for `^` + `!` mix | Missing / Constraint | Current grammar rejects mixed weighted/parallel in one arm; hybrid parity requires a narrow grammar/model extension or equivalent representation. |
| 8. Session continuity/resume | B2BUA store, secure-session manager, continuity stores | Missing / Constraint | No thinker memo/cycle fields or store methods; authority path exists but needs integration. |
| 9. Protocol-neutral streaming | canonical event stream, frontend encoders for reasoning/text | Constraint / Unknown | Reasoning/text events exist across frontends, but visible mode legality and tag stripping across protocols need design validation. |
| 10. Config/diagnostics | feature plugin config patterns, routing diagnostics, logs | Missing / Constraint | No interleaved-thinking config, prompt asset, or bounded diagnostics for memo transitions. |
| 11. Non-interference | existing route tests, extension ordering tests | Constraint | Additive path must avoid changing existing selectors without `[thinker]`; requires focused regression coverage. |

## Missing Capabilities

1. Parser support for `[thinker]` forms and validation.
2. AST metadata to mark thinker branches and possibly hybrid embedded executor branches.
3. Planner support for thinker cycle sequence, stored cursor, selector/sequence invalidation, and thinker suppression.
4. Candidate metadata carrying thinker/executor role and selector identity into runtime.
5. Persisted interleaved thinking state: cycle state, memo state, metadata, injection budget, and interrupted flag.
6. Thinker request shaping: instructions injection and tool suppression before capability negotiation and backend open.
7. Memo extraction over canonical stream events, including hidden buffering, visible sanitization, fallback memo, limits, and interruption handling.
8. Continuation orchestration that can run thinker B-leg followed by executor B-leg within one A-leg while preserving cancellation, attempt lineage, and no-retry-after-output semantics.
9. Memo injection into executor turns with dedupe and regular-turn budget behavior.
10. Operator configuration and docs for prompt source, visibility mode, memo size, and regular-turn budget.
11. Diagnostic events/logging for selection, memo state transitions, continuation, and suppression without leaking raw memo content.

## Integration Challenges

- **Streaming-first continuation**: Hidden mode naturally buffers or drains the thinker stream before executor output. Visible mode emits thinker output before executor output. Both must preserve legal frontend stream framing and terminal events.
- **Output commitment**: Visible thinker output may commit the A-leg before executor starts; continuation failure cannot be hidden by failover in ways that violate existing recovery rules.
- **Capability negotiation order**: Thinker tool suppression must happen before the selected thinker backend is negotiated, otherwise capability checks may see tools that will not be sent.
- **Feature extension ordering**: Memo injection and other request transforms must be deterministic when multiple feature plugins are active.
- **Session authority**: Memo/cycle state must never be applied after secure-session denial or across unrelated sessions.
- **Hybrid grammar**: Current parser intentionally rejects weighted/parallel mixing in one arm. Python hybrid parity is the largest selector-language mismatch.
- **State placement**: B2BUA continuity, secure-session records, and plugin state stores all exist; design must choose which state is authoritative for cycle/memo behavior without duplicating authority.
- **Diagnostics privacy**: Memo content is model-generated and potentially sensitive; operator observability must avoid raw prompt/memo leakage in high-cardinality logs/metrics.

## Implementation Approach Options

### Option A: Extend Existing Components In Place

Extend `routing`, `runtime`, `b2bua.Store`, and existing feature transform/gate seams directly.

**Modules likely extended**:
- `internal/core/routing/*`
- `internal/core/runtime/*`
- `internal/core/b2bua/store.go`
- continuity store implementations
- `pkg/lipapi` helper functions if needed
- `pkg/lipsdk/request` and feature registration only where current seams are sufficient

**Trade-offs**:
- Good fit for selector parsing, planner state, attempt metadata, and continuity fields.
- Minimizes new package boundaries.
- Risks bloating executor and stream receive code with a distinct multi-leg workflow.
- Harder to isolate memo extraction and continuation tests if everything lands inside existing large runtime files.

**Feasibility**: Viable for parser/planner/store extensions; weak for full continuation and memo extraction because responsibilities are distinct.

### Option B: Create New Components

Create dedicated interleaved-thinking components for memo extraction, memo state, request shaping, and continuation orchestration, integrating through existing core/runtime and feature seams.

**Possible new components**:
- A routing helper for thinker-cycle sequence and state evaluation.
- A runtime continuation coordinator for thinker B-leg -> executor B-leg flow.
- A memo extraction/sanitization package over canonical events.
- An official feature plugin for config, prompt loading, memo injection, and diagnostics.
- A typed state model for thinker memo/cycle persistence.

**Trade-offs**:
- Better isolation for complex behavior and tests.
- Keeps existing executor and routing files smaller if integrated through narrow calls.
- Requires careful seam design to avoid over-abstracting or letting plugins own backend execution.
- More files and explicit wiring.

**Feasibility**: Strong for memo extraction, config, prompt behavior, and continuation orchestration support; still requires core extensions for routing/state metadata.

### Option C: Hybrid Approach

Extend existing core-owned surfaces only where the feature changes core product semantics, and add new focused components for the distinct interleaved-thinking workflow.

**Likely split**:
- Extend `routing` for `[thinker]`, validation, cycle planning, suppression, and candidate role metadata.
- Extend continuity/session stores with typed thinker state or a narrow state reference.
- Add a new runtime coordinator for thinker continuation rather than embedding all logic in `Executor.Execute`.
- Add a focused memo extractor/sanitizer over canonical events.
- Add an official feature plugin/config surface for prompt loading and memo injection if existing SDK seams can carry the needed context.

**Trade-offs**:
- Balances parity with boundary clarity.
- Keeps core ownership for routing/orchestration while preserving plugin-owned request mutation where appropriate.
- Requires design discipline around state authority and extension ordering.
- Most consistent with steering, but has the broadest planned surface.

**Feasibility**: Best overall fit for full parity.

## Effort and Risk

- **Effort**: XL (2+ weeks). The feature spans selector grammar, planning, runtime stream orchestration, stores, secure-session interaction, request mutation, memo extraction, config, diagnostics, docs, and tests.
- **Risk**: High. The main risks are visible-mode streaming legality, no-retry-after-output preservation, hybrid weighted/parallel grammar, and state authority across secure sessions and continuity stores.

## Recommendations for Design Phase

1. Prefer the hybrid approach: core extensions for route semantics and attempt orchestration, new focused components for memo extraction/sanitization and continuation flow, feature plugin for operator-facing config and request shaping where viable.
2. Design state authority explicitly before coding: decide whether cycle and memo state live directly in continuity A-leg/session records, an SDK state store keyed by authoritative session, or a split model.
3. Define stream commitment semantics for hidden and visible mode early. In particular, specify what happens if visible thinker output has been emitted and the continuation executor fails.
4. Define the exact hybrid selector grammar. The current Go grammar rejects weighted/parallel mixing, so parity requires a deliberate grammar extension and tests.
5. Validate whether existing `request.Transform` metadata can identify thinker/executor turns and access memo state. If not, design the smallest SDK metadata extension.
6. Validate whether completion gates are sufficient for memo capture or whether runtime-level stream wrapping is required for hidden/visible continuation. Hidden continuation likely requires runtime ownership.
7. Keep memo extraction provider-neutral by operating on canonical events only.
8. Include architecture tests or import checks if new packages risk core importing concrete feature plugins.

## Research Needed

- Confirm the best storage location for memo state under secure-session authority and durable continuity.
- Confirm legal visible thinker output representation for each bundled frontend protocol.
- Confirm how current completion gates interact with streaming event emission and whether they can participate before output is surfaced.
- Confirm exact Python hybrid selector examples that must be accepted, including precedence and normalization. Requirements now state the user-visible shape as one thinker branch plus one non-thinker executor expression that is itself a parallel group, but design still needs grammar precision.
- Confirm whether `[first=1|yes|true]` compatibility should remain out of scope or be revisited while adding `[thinker=...]` value parsing.

## Suggested Validation Targets

- `go test ./internal/core/routing`
- `go test ./internal/core/b2bua ./internal/core/continuity/...`
- Focused runtime tests under `internal/core/runtime`
- Feature plugin tests under the chosen `internal/plugins/features/...` package
- `make quality-checks`
- `make parity-checks` after cross-protocol visible/hidden stream behavior is implemented

---

# Design Discovery and Synthesis

## Summary

- **Feature**: `port-interleaved-thinking`
- **Discovery Scope**: Complex Integration
- **Key Findings**:
  - Existing routing, canonical event, continuity, and extension seams are strong enough to host the feature without provider-specific contracts.
  - Full parity requires runtime-level continuation orchestration; existing completion gates alone cannot own hidden thinker drain followed by executor opening.
  - The safest design is hybrid: extend core-owned routing and continuity semantics, add focused interleaved-thinking helpers, and keep provider translation at backend edges.

## Research Log

### Routing and Selector Integration
- **Context**: `[thinker]` changes selector syntax and weighted planning behavior.
- **Sources Consulted**: `internal/core/routing/selector.go`, `parser.go`, `weighted.go`, `planner.go`, `.kiro/steering/routing-and-orchestration.md`.
- **Findings**:
  - `[first]` is already represented on `WeightedBranch` and consumed through `SessionRoutingState`.
  - Current weighted branches only target primaries, while hybrid parity requires one weighted branch to target a parallel executor expression.
  - Existing parser deliberately rejects weighted and parallel mixing; the hybrid must be a narrow exception, not a broad operator-mixing relaxation.
- **Implications**: Add typed thinker metadata and a constrained embedded executor target rather than weakening general selector validation.

### Runtime Continuation Flow
- **Context**: Thinker turns require a B-leg followed by an executor B-leg under one A-leg.
- **Sources Consulted**: `internal/core/runtime/executor.go`, `executor_open_attempt.go`, `attempt_stream.go`, `internal/core/streamrecovery`.
- **Findings**:
  - Current `Execute` returns a `retryRecvStream` after opening one candidate or parallel group.
  - Runtime already owns B-leg lifecycle, attempt budgets, lineage, and pre-output recovery.
  - Completion gates operate after bounded buffering and cannot by themselves open the continuation executor attempt.
- **Implications**: Add a runtime-owned interleaved stream wrapper/coordinator that can drain or surface thinker events, store memo state, and open a continuation executor attempt.

### Request Mutation and Memo Capture
- **Context**: Thinker turns need instructions/tool suppression; executor turns need memo injection.
- **Sources Consulted**: `pkg/lipapi/call.go`, `pkg/lipapi/events.go`, `pkg/lipsdk/request/transform.go`, `internal/core/extensions/request_transform.go`.
- **Findings**:
  - Canonical `Call` has enough structure for instructions, messages, tools, and tool choice.
  - Canonical events already distinguish reasoning and text deltas.
  - Candidate-specific mutation must occur after route selection and before capability negotiation, which is later than request-wide transforms.
- **Implications**: Interleaved request shaping is runtime-owned and candidate-aware. It should use canonical call/event helpers, not provider wire logic.

### State and Authority
- **Context**: Cycle state and memo state must survive session turns without bypassing secure-session authority.
- **Sources Consulted**: `internal/core/b2bua/store.go`, continuity stores, `internal/core/securesession` steering and types.
- **Findings**:
  - B2BUA A-leg continuity already stores first-request routing state.
  - Secure-session BeginTurn validates or creates authoritative session context before backend execution.
  - Storing memo/cycle state on A-leg continuity keeps it behind the same authority gate and avoids plugin-private state lookup before authorization.
- **Implications**: Add typed interleaved-thinking state to A-leg continuity records and store implementations. Do not store raw resume tokens or provider wire payloads.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
| --- | --- | --- | --- | --- |
| Extend existing components | Add thinker logic directly to routing, runtime, and stores | Lowest file count, familiar patterns | Bloats executor and mixes memo concerns into stream recovery | Rejected as the full design because continuation and extraction are distinct responsibilities |
| New isolated feature plugin | Implement most behavior in a feature plugin | Clear feature packaging | Plugins must not open backends or own core routing semantics | Rejected because routing and continuation are core-owned product behavior |
| Hybrid core orchestration plus focused helpers | Core owns selector, planning, continuation, and continuity; helpers own memo extraction and shaping | Fits steering, testable boundaries, no provider leakage | More explicit seams and store changes | Selected |

## Design Decisions

### Decision: Core Owns Thinker Routing and Continuation
- **Context**: `[thinker]` affects route planning and creates multiple B-legs under one A-leg.
- **Alternatives Considered**:
  1. Feature plugin owns the whole workflow.
  2. Runtime owns the workflow with helper packages.
- **Selected Approach**: Runtime and routing core own selector semantics, candidate role, continuation orchestration, attempt budgets, and B-leg lineage.
- **Rationale**: Backend opening, recovery, and output commitment are core product guarantees.
- **Trade-offs**: The core grows, but the growth is bounded to orchestration semantics and provider-neutral helpers.
- **Follow-up**: Add architecture tests if new packages risk importing concrete feature plugins.

### Decision: Split Cycle State and Memo Body Storage
- **Context**: Cycle cursor and memo state need secure-session-bound persistence without bloating B2BUA lineage rows with memo text.
- **Alternatives Considered**:
  1. Store all cycle and memo body state directly on A-leg continuity.
  2. Store only cycle state and memo references on A-leg continuity, with memo body in a bounded core memo store.
  3. Store all state only in plugin state keyed by session.
- **Selected Approach**: Store thinker cycle state and memo references in A-leg continuity; store bounded memo body and metadata in a core-owned memo store keyed by authoritative session or A-leg scope.
- **Rationale**: A-leg continuity already backs routing state and is reached after secure-session authority is established, while separating memo body avoids bloating lineage rows and durable schemas.
- **Trade-offs**: Adds one small state seam, but keeps B2BUA records compact and memo retention easier to bound.
- **Follow-up**: Validate durable store migration shape and memo store lifetime policy in tasks.

### Decision: Memo Handling Uses Canonical Events Only
- **Context**: The feature must work across frontend/backend families.
- **Alternatives Considered**:
  1. Provider-specific extraction paths.
  2. Canonical event extraction and sanitization.
- **Selected Approach**: Extract and sanitize memo text from `EventReasoningDelta` and `EventTextDelta` after backend adapters normalize provider output.
- **Rationale**: This preserves canonical-in-the-middle and avoids provider SDK leakage.
- **Trade-offs**: Provider-specific hidden fields not represented canonically are ignored until adapters emit canonical events.
- **Follow-up**: Add fixtures for reasoning and text deltas across representative backends.

### Decision: Visible Mode Emits One Logical Canonical Stream
- **Context**: Visible thinker output plus executor output must remain legal for frontends.
- **Alternatives Considered**:
  1. Forward thinker and executor lifecycle events verbatim.
  2. Normalize both phases into one logical response stream.
- **Selected Approach**: In visible mode, surface sanitized thinker deltas as reasoning deltas inside the same logical response, suppress duplicate lifecycle events, and finish with the executor terminal event.
- **Rationale**: Frontend encoders already understand canonical reasoning deltas; duplicate response lifecycles would be protocol-risky.
- **Trade-offs**: Visible thinker content is represented as reasoning context rather than raw backend text.
- **Follow-up**: Validate frontend encoders for OpenAI Responses, legacy OpenAI, Anthropic, and Gemini.

## Risks & Mitigations

- **Visible stream commitment risk** — Treat any surfaced thinker delta as client-visible output; after that point continuation failures surface rather than silently fail over.
- **Hybrid grammar drift** — Keep the exception narrow: one thinker branch plus one embedded non-thinker parallel executor expression.
- **Memo privacy risk** — Store bounded memo text only in continuity state and exclude raw memo content from high-cardinality logs and metrics.
- **Memo state coupling risk** — Store only compact memo references in B2BUA continuity and keep memo body in a bounded core memo store.
- **Dependency direction risk** — Keep shared role, cycle, and memo-reference value types in a pure `internal/core/interleavedstate` package so routing, B2BUA, runtime, and helpers can depend on the value model without import cycles.
- **Store migration risk** — Add typed JSON state where durable store schemas can evolve with minimal column churn.
- **Extension ordering risk** — Candidate-specific interleaved shaping runs in runtime after route selection; existing request transforms remain deterministic and unchanged.

## References

- `.kiro/steering/product.md` — product identity and core-vs-plugin ownership.
- `.kiro/steering/tech.md` — streaming, plugin, config, and dependency constraints.
- `.kiro/steering/structure.md` — package ownership map and file placement rules.
- `.kiro/steering/routing-and-orchestration.md` — route planning, B2BUA, and no-retry-after-output invariants.
- `.kiro/steering/api-standards.md` — canonical request/event and frontend/backend adapter rules.
- `.kiro/steering/testing.md` — TDD and high-value test targets.
