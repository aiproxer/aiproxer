# Requirements Document

## Introduction

Go LIP maintainers need Python LIP's interleaved thinking (`[thinker]`) functionality ported into the Go proxy. Operators currently have dynamic routing, weighted selectors, `[first]` steering, canonical streaming, feature plugin seams, and continuity state in the Go repo, but they cannot configure a weighted thinker branch that records planning output, resumes with an executor branch, and injects the captured memo into later turns. This feature shall provide Python-era interleaved thinking parity while preserving Go LIP's protocol-neutral behavior, streaming-first execution, explicit session authority, and clear operator diagnostics.

## Boundary Context

- **In scope**: `[thinker]` selector syntax and validation, thinker-aware weighted routing cycles, first-turn interaction, thinker suppression, thinker prompt behavior, tool suppression for thinker turns, memo capture and extraction, hidden and visible continuation flows, memo injection into executor turns, session continuity across requests, parallel executor hybrid behavior, diagnostics, and parity documentation.
- **Out of scope**: improving the quality of thinker prompts beyond parity, adding provider-specific planner semantics, changing client-facing protocol contracts unrelated to interleaved thinking, adding external model sub-calls outside the configured route, and changing existing `[first]` behavior except where parity with `[thinker]` explicitly requires interaction rules.
- **Adjacent expectations**: existing routing, streaming, secure-session, continuity, request mutation, response mutation, and feature registration behavior must remain compatible for routes that do not use `[thinker]`.
- **Revalidation triggers**: routing selector syntax, weighted/parallel routing behavior, streaming output legality, continuation semantics, secure-session state persistence, diagnostics, and feature plugin configuration.

## Requirements

### Requirement 1: Thinker Selector Syntax and Validation

**Objective:** As an operator, I want to mark one weighted route branch as a thinker branch, so that the proxy can run an interleaved planning turn as part of dynamic routing.

#### Acceptance Criteria

1. When a selector contains `[thinker]` on a weighted branch, the Go LIP proxy shall accept the selector and treat that branch as thinker-annotated.
2. When a selector contains `[thinker=1]`, `[thinker=yes]`, or `[thinker=true]` on a weighted branch, the Go LIP proxy shall accept the selector and treat that branch as thinker-annotated.
3. If a selector contains `[thinker=0]`, `[thinker=no]`, `[thinker=false]`, `[thinker=]`, or an unrecognized `[thinker=VALUE]` form, the Go LIP proxy shall reject the selector with a validation error.
4. If a weighted selector contains more than one thinker-annotated branch, the Go LIP proxy shall reject the selector with a validation error.
5. If a branch is annotated with both `[first]` and `[thinker]`, the Go LIP proxy shall reject the selector with a validation error.
6. If `[thinker]` appears outside a weighted selector context, the Go LIP proxy shall reject the selector with a validation error.
7. When a selector combines `[thinker]` with supported branch annotations such as weights and context-size constraints, the Go LIP proxy shall preserve each annotation's observable routing meaning.

### Requirement 2: Thinker-Aware Weighted Routing Cycle

**Objective:** As an operator, I want thinker branches to participate in a deterministic weighted cycle, so that executor requests and thinker requests alternate predictably according to configured weights.

#### Acceptance Criteria

1. When a weighted selector includes one thinker branch and one or more non-thinker branches, the Go LIP proxy shall select branches from a cycle formed by repeating each non-thinker branch according to its effective weight and then including the thinker branch once.
2. When a thinker-aware weighted cycle advances, the Go LIP proxy shall persist enough session state for the next request in the same session to continue from the next cycle position.
3. When the stored cycle state does not match the current selector or branch sequence, the Go LIP proxy shall reset the cycle for the current selector.
4. When no valid stored cycle state exists and first-request steering is active, the Go LIP proxy shall honor a valid `[first]` branch before starting normal thinker-cycle advancement.
5. While thinker selection is suppressed for a continuation executor request, the Go LIP proxy shall exclude thinker branches from eligibility for that continuation.
6. If all non-thinker branches are ineligible while thinker selection is suppressed, the Go LIP proxy shall surface a deterministic no-eligible-route outcome.

### Requirement 3: Thinker Turn Request Behavior

**Objective:** As an operator, I want thinker turns to receive planning instructions and avoid tool execution, so that thinker models produce a memo instead of acting as the executor.

#### Acceptance Criteria

1. When a thinker branch is selected, the Go LIP proxy shall add the configured thinker instructions to the backend request in a way that is visible to the selected model.
2. When a thinker branch is selected, the Go LIP proxy shall suppress tool availability and tool-choice directives for that thinker backend request.
3. If thinker instructions are configured but cannot be loaded or resolved, the Go LIP proxy shall fail the thinker turn before upstream execution with an operator-visible error.
4. When a non-thinker branch is selected, the Go LIP proxy shall not add thinker instructions or suppress tools because of the interleaved thinking feature.
5. Where interleaved thinking is disabled or not configured for the route, the Go LIP proxy shall preserve existing request behavior.

### Requirement 4: Thinker Memo Capture and Extraction

**Objective:** As an operator, I want the proxy to capture the thinker model's planning memo, so that later executor requests can use the latest thinker output as context.

#### Acceptance Criteria

1. When a thinker turn produces output containing a `<proxy_thinker_memo>...</proxy_thinker_memo>` block, the Go LIP proxy shall store the block content as the thinker memo for the session.
2. When a thinker turn produces output without a complete memo block, the Go LIP proxy shall store a bounded fallback memo derived from the thinker output.
3. When a thinker turn streams output, the Go LIP proxy shall capture memo content without requiring a separate non-streaming execution path.
4. If a thinker turn is interrupted after partial output, the Go LIP proxy shall preserve captured memo information and mark the stored memo as interrupted.
5. When stored memo metadata is available, the Go LIP proxy shall preserve observable metadata including source route identity, selected backend/model identity, request identity, creation time, visibility mode, injection count, and remaining regular-turn budget.
6. The Go LIP proxy shall bound stored memo size according to configured limits and avoid storing unbounded model output.

### Requirement 5: Executor Memo Injection

**Objective:** As an operator, I want executor turns to receive the captured thinker memo, so that regular models can use the thinker's planning context.

#### Acceptance Criteria

1. When a non-thinker executor turn starts and a valid thinker memo is available for the session, the Go LIP proxy shall inject the memo into the executor request as planning context.
2. When the memo has already been made visible to the client for the same continuation, the Go LIP proxy shall avoid duplicating that memo into the continuation executor request when configured behavior requires suppression.
3. When the stored memo has a remaining regular-turn budget, the Go LIP proxy shall decrement that budget only after a memo is injected into an executor request.
4. When the stored memo has no remaining regular-turn budget, the Go LIP proxy shall stop injecting that memo into executor requests.
5. If an executor request already contains equivalent thinker memo context, the Go LIP proxy shall avoid injecting a duplicate copy.

### Requirement 6: Hidden and Visible Continuation Flows

**Objective:** As a client user, I want a request that selects a thinker branch to continue to an executor branch, so that interleaved planning improves the final response without breaking streaming behavior.

#### Acceptance Criteria

1. When hidden thinker mode is active and a thinker branch is selected, the Go LIP proxy shall capture the thinker response without surfacing thinker content to the client and then continue with an executor branch in the same logical client request.
2. When visible thinker mode is active and a thinker branch is selected, the Go LIP proxy shall surface client-legal thinker output before continuing with an executor branch in the same logical client request.
3. When visible thinker output contains memo wrapper tags, the Go LIP proxy shall prevent the wrapper tags from being exposed as ordinary assistant content.
4. When a continuation executor request is issued after a thinker turn, the Go LIP proxy shall suppress thinker branch selection for that continuation.
5. If the thinker turn fails before client-visible output begins, the Go LIP proxy shall apply existing pre-output recovery semantics.
6. If client-visible output has begun, the Go LIP proxy shall not silently fail over or restart in a way that violates the no-retry-after-output guarantee.
7. When the client cancels the request, the Go LIP proxy shall cancel active thinker and continuation work for that logical request.

### Requirement 7: Thinker With Parallel Executor Hybrid Parity

**Objective:** As an operator, I want Python-era selectors that combine a thinker branch with a parallel executor group in one weighted selector to behave equivalently in Go, so that existing routing configurations can migrate.

#### Acceptance Criteria

1. When a selector expresses one thinker branch and one non-thinker executor expression that is itself a parallel group, the Go LIP proxy shall accept the selector if it is otherwise valid.
2. When the hybrid selector chooses the thinker branch, the Go LIP proxy shall run the thinker flow and then continue to an eligible executor branch with thinker selection suppressed.
3. When the hybrid selector chooses the parallel executor expression, the Go LIP proxy shall run the parallel executor race according to existing parallel routing semantics.
4. If the hybrid selector includes more than one thinker branch, more than one embedded executor expression, or a thinker annotation inside the embedded parallel group, the Go LIP proxy shall reject it with a validation error before upstream execution.
5. If the hybrid selector is malformed or contains unsupported annotation placement, the Go LIP proxy shall reject it with a validation error before upstream execution.
6. While executing a hybrid parallel race, the Go LIP proxy shall preserve existing winner selection, loser cancellation, and output commitment rules.

### Requirement 8: Session Continuity and Resume Behavior

**Objective:** As an operator, I want interleaved thinking state to survive session turns and supported resume paths, so that thinker cycles and memos remain consistent across an ongoing conversation.

#### Acceptance Criteria

1. When a session continues after a thinker-aware request, the Go LIP proxy shall restore the stored thinker cycle state and memo state for that session.
2. When a session is resumed through an authorized resume path, the Go LIP proxy shall preserve interleaved thinking state subject to the same authority checks as other session state.
3. If session authority is denied, the Go LIP proxy shall not expose or apply stored thinker memo or cycle state to the denied request.
4. When interleaved thinking state is unavailable for a request without an authoritative session, the Go LIP proxy shall behave as a new session for thinker-cycle purposes.
5. When selector changes make stored interleaved thinking state stale, the Go LIP proxy shall ignore stale cycle state without corrupting stored memo state.

### Requirement 9: Protocol-Neutral Streaming and Canonical Behavior

**Objective:** As a client user, I want interleaved thinking to work across supported frontend and backend families, so that the feature does not depend on one provider protocol.

#### Acceptance Criteria

1. When interleaved thinking is active, the Go LIP proxy shall preserve legal streaming and non-streaming responses for the selected frontend protocol.
2. When backend providers emit reasoning, thinking, content, or text deltas, the Go LIP proxy shall use protocol-neutral behavior for memo capture and client output decisions.
3. If a selected backend lacks a required capability for the effective thinker or executor request, the Go LIP proxy shall fail explicitly before upstream execution where capability validation is available.
4. Where a protocol cannot legally expose visible thinker output, the Go LIP proxy shall use a client-legal representation or fail with a deterministic configuration/capability error.
5. The Go LIP proxy shall not expose provider-specific wire fields or provider SDK concepts as required client configuration for interleaved thinking.

### Requirement 10: Operator Configuration and Diagnostics

**Objective:** As an operator, I want interleaved thinking to be configurable and observable, so that I can enable, troubleshoot, and audit it safely.

#### Acceptance Criteria

1. Where interleaved thinking is configured, the Go LIP proxy shall allow operators to configure thinker instructions, visibility mode, regular-turn memo budget, and memo size limits.
2. When interleaved thinking is not configured, the Go LIP proxy shall keep existing routing and request behavior unchanged for selectors without `[thinker]`.
3. When a thinker branch is selected, the Go LIP proxy shall make the selected thinker route and continuation route observable through existing routing and attempt diagnostics.
4. When a memo is captured, injected, skipped, expired, or suppressed, the Go LIP proxy shall make the state transition observable to operators without exposing raw prompt or memo content in high-cardinality diagnostics.
5. If interleaved thinking configuration is invalid, the Go LIP proxy shall fail closed before serving affected traffic.
6. The Go LIP proxy shall document supported selector forms, visible and hidden modes, memo behavior, continuation behavior, and migration differences from Python LIP.

### Requirement 11: Backward Compatibility and Non-Interference

**Objective:** As an operator, I want existing routes and features to keep working, so that enabling this port does not regress unrelated traffic.

#### Acceptance Criteria

1. While a selector does not contain `[thinker]`, the Go LIP proxy shall preserve existing weighted, failover, parallel, `[first]`, health, and context-size routing behavior.
2. While interleaved thinking is disabled for a deployment, the Go LIP proxy shall not mutate requests, responses, or session state because of interleaved thinking.
3. If interleaved thinking is enabled for one route, the Go LIP proxy shall not apply thinker memo state to unrelated sessions or unrelated selector identities.
4. When existing feature hooks or request transforms are active, the Go LIP proxy shall preserve deterministic extension ordering and produce a deterministic outcome for interleaved thinking interactions.
5. The Go LIP proxy shall provide regression coverage for routing, streaming, continuity, and feature-extension interactions introduced by interleaved thinking.
