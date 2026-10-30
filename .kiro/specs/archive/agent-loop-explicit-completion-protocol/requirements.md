# Requirements Document

## Introduction

Agent Loop Guard (ALG) currently prevents supported premature agent-loop termination by using a bounded independent semantic verifier and, when justified, a hidden continuation intent. This specification adds a second ALG strategy based on an explicit model-facing completion protocol: on an eligible B-leg, the proxy makes the familiar `attempt_completion` tool available only to the remote worker model and instructs that model to call it when the requested work is complete. A candidate terminal without the expected completion signal receives a bounded hidden protocol-repair continuation instead of an immediate A-side terminal.

The explicit completion protocol is the preferred strategy for new deployments because it removes the normal-path auxiliary verifier call and converts completion into an explicit worker/proxy handshake. The existing semantic-verifier strategy remains supported as a mutually exclusive legacy alternative. Operators may disable ALG entirely. The two strategies must never run concurrently for one generation/request.

This is a brownfield extension of the existing `agent-loop-guard` feature and the generic terminal-decision/continuation platform. It must preserve the existing single terminal owner, no-post-commit replay rule, A-leg/client tool authority, streaming-first behavior, and concrete-feature removal boundary.

## Boundary Context

- **In scope**: mutually exclusive ALG strategy selection; preferred `attempt_completion` completion protocol; exact model-facing tool contract; safe B-leg-only protocol activation; proxy-owned completion evidence; bounded missing-signal continuation; preservation of the current semantic-verifier strategy; compatibility, observability, and regression coverage; the smallest generic SDK/runtime seam required to expose and consume a proxy-owned model control tool without leaking it to the A-leg.
- **Out of scope**: replacing terminal ownership, routing, B2BUA, billing, secure-session authority, existing pre-output transport recovery, provider-specific wire logic, a general workflow engine, arbitrary local tool execution, or making all client tool-choice combinations compatible with hidden proxy tools.
- **Adjacent expectations**: existing terminal-decision and conversation-view semantics remain authoritative; active work touching candidate-open/tool-finalization ordering (including `b-leg-path-virtualization`) must be revalidated before implementation.
- **Boundary ownership**: ALG owns strategy/configuration and completion policy; a generic SDK/runtime extension owns proxy-control-tool projection/interception; core remains provider/feature neutral and keeps terminal/continuation authority.
- **Revalidation triggers**: canonical tool/tool-choice semantics, candidate-open ordering, tool-call assembly/finalization, terminal-decision evidence, continuation transaction, conversation projection, backend-plugin invocation shape, or output-commit semantics change.

## Requirements

### Requirement 1: Mutually Exclusive Strategy Selection and Compatibility

**Objective:** As an operator, I want one explicit ALG strategy at a time so that completion policies cannot race or produce contradictory recovery decisions.

#### Acceptance Criteria

1.1. **Where** ALG is disabled, the proxy shall construct neither the explicit-completion protocol nor the semantic verifier and shall preserve no-provider terminal behavior.

1.2. **When** ALG is enabled with strategy `attempt_completion`, the proxy shall use the explicit completion protocol and shall not construct, invoke, or fall back to the semantic verifier.

1.3. **When** ALG is enabled with strategy `semantic_verifier`, the proxy shall preserve the existing bounded semantic-verifier behavior and shall not inject or consume a proxy-owned `attempt_completion` tool.

1.4. **If** configuration attempts to enable or configure both strategies for concurrent use, the proxy shall reject the candidate generation before publication with a bounded configuration error.

1.5. **Where** an existing ALG configuration enables the feature but omits the new strategy selector, the proxy shall preserve the pre-spec semantic-verifier behavior for backward compatibility.

1.6. **Where** documentation, generated examples, or new operator guidance recommends an ALG strategy, the proxy project shall recommend explicit `strategy: attempt_completion` while identifying implicit legacy selection as compatibility behavior rather than the preferred configuration.

1.7. **When** a strategy-specific field is supplied under the other strategy in a way that could imply mixed behavior, configuration validation shall reject it rather than silently ignore it.

### Requirement 2: Stable `attempt_completion` Model Contract

**Objective:** As an agent user, I want the worker model to see a familiar completion primitive so that models already exposed to Cline/Roo/Kilo-style agents are more likely to follow the protocol correctly.

#### Acceptance Criteria

2.1. **When** the preferred protocol is active on a B-leg, the model-facing tool name shall be exactly `attempt_completion`.

2.2. **When** the proxy defines its injected `attempt_completion` tool, its input schema shall contain exactly one required property named `result` whose value is a string, shall reject additional properties, and shall not expose a `command` parameter.

2.3. **When** the tool description is rendered, it shall state that the tool is called only after all work requested by the user for the current task is complete and that `result` summarizes the completed work.

2.4. **When** the protocol instruction is rendered, it shall tell the model to continue concrete in-scope work that can proceed without new user input, call `attempt_completion` only when complete, and never treat the protocol as new user intent, approval, permission, or scope expansion.

2.5. **When** further progress genuinely requires user input, permission, credentials, clarification, or a choice, the protocol instruction shall tell the model to request that input normally rather than invent it or call completion falsely.

2.6. **The** tool name, required `result` property, and no-`command` contract shall be treated as a stable model-facing ABI and shall not be operator-renamable in this version.

2.7. **The** base protocol instruction shall be byte-stable across equivalent turns and shall not contain timestamps, trace IDs, attempt IDs, counters, or other per-turn volatile data.

### Requirement 3: B-Leg-Only Protocol Visibility and Provenance

**Objective:** As a client integrator, I want the completion protocol to remain transparent so that existing agent clients need no code changes and cannot spoof proxy-owned completion authority.

#### Acceptance Criteria

3.1. **When** the proxy injects the protocol, the injected tool definition and base protocol instruction shall be visible to the selected B-leg model but shall not be added to A-leg/client truth, client-visible history, or frontend tool catalogs.

3.2. **When** the model emits a call to a proxy-injected `attempt_completion`, the proxy shall identify ownership from trusted request-local provenance established by the injection step rather than from tool name alone.

3.3. **If** a client independently declares a tool named `attempt_completion` or another recognized explicit-completion alias, the proxy shall not relabel that client-owned tool as proxy-owned or intercept it solely because of its name.

3.4. **When** a client-owned explicit-completion tool completes through its ordinary client/tool-result path, existing normalized explicit-completion evidence shall remain usable by ALG without changing client ownership.

3.5. **When** a proxy-owned completion call is emitted, its tool lifecycle shall not become client-visible and shall not be offered to ordinary client tool execution.

3.6. **When** the preferred strategy is removed or disabled in a newly admitted generation, no new request shall receive proxy-owned completion-tool provenance or protocol instruction.

### Requirement 4: Safe Protocol Activation and Client Tool-Choice Preservation

**Objective:** As a client, I want my declared tool constraints preserved so that an optional loop guard cannot silently weaken tool-selection semantics.

#### Acceptance Criteria

4.1. **When** the selected backend/model cannot represent callable tools, the proxy shall leave the explicit-completion protocol inactive for that B-leg and shall not emulate the tool through prose parsing.

4.2. **When** the client tool choice is unset/default-auto or explicitly auto and no incompatible allowed-tool subset is present, the proxy may activate the completion protocol if all other compatibility checks pass.

4.3. **When** the client explicitly forbids tools, requires a named client tool, requires an arbitrary tool from a client-constrained set, or otherwise supplies a tool-choice constraint that hidden tool injection would broaden, the proxy shall not activate the proxy-owned completion tool and shall not weaken the client constraint.

4.4. **If** a proxy-owned tool-name collision or another ambiguous ownership condition is detected, the proxy shall fail the protocol activation conservatively for that B-leg rather than hijack a client tool.

4.5. **When** protocol activation is ineligible, the preferred ALG strategy shall not silently invoke the legacy semantic verifier as a fallback.

4.6. **When** protocol activation is ineligible and no trusted explicit completion fact exists, ALG shall preserve a conservative final outcome instead of inventing a completion signal or a new tool authority.

4.7. **When** a candidate-specific backend changes because of retry/race/failover before commitment, protocol activation shall be recomputed from that candidate's actual capabilities and final backend-effective call.

### Requirement 5: Streaming-Preserving Proxy Control-Tool Handling

**Objective:** As an interactive user, I want normal assistant output and ordinary tools to keep streaming while the proxy handles only its own completion call privately.

#### Acceptance Criteria

5.1. **While** no proxy-owned completion call is active, ordinary response text, reasoning, media, usage, warnings, client tool calls, and other canonical events shall retain existing streaming behavior and shall not be buffered until response completion solely because this strategy is enabled.

5.2. **When** a proxy-owned completion call starts, the proxy shall divert only that call's lifecycle from the client path and shall bound the retained completion-call arguments.

5.3. **When** non-proxy tool calls are observed, existing tool-call finalization, tool policy, reactor, and client-release behavior shall remain authoritative.

5.4. **When** a proxy-owned completion call is claimed, it shall be consumed before ordinary client tool policy/reactor execution so that client tool policy cannot accidentally execute or reinterpret the proxy-local control action.

5.5. **If** the proxy-owned completion call is malformed, oversized, incomplete, ambiguous, or fails feature-local validation, the proxy shall not expose that call to the client as a fallback tool invocation.

5.6. **If** completion-protocol handling fails after client-visible output has committed, the proxy shall preserve the committed output and shall not replay or fail over the committed attempt.

5.7. **The** preferred strategy shall not depend on whole-response completion-gate buffering as its normal execution mechanism.

### Requirement 6: Trusted Completion Signal and Final Result

**Objective:** As a user, I want a valid completion call to end the task cleanly and return a useful final result without duplicate output.

#### Acceptance Criteria

6.1. **When** a proxy-owned `attempt_completion` call completes with strict valid JSON containing a non-empty bounded `result`, the proxy shall record a trusted normalized explicit-completion fact for the current logical response.

6.2. **When** the proxy records that fact, it shall represent a completed proxy-local control action for evidence purposes without publishing a synthetic client tool call/result pair.

6.3. **When** no meaningful client-visible assistant text has yet been committed for the logical response, a valid completion `result` shall be eligible to become the final client-visible assistant text before the accepted terminal.

6.4. **When** meaningful client-visible assistant text has already been committed, the proxy shall not duplicate the same logical answer merely to surface the `result`; the `result` may remain completion evidence/summary only.

6.5. **When** a valid explicit-completion fact reaches ALG under the preferred strategy, ALG shall allow the corresponding otherwise-safe terminal without an auxiliary semantic verifier call.

6.6. **If** a completion signal is followed by contradictory model output or another unresolved non-control tool boundary before the terminal candidate, the proxy shall not treat the earlier signal as unconditional authority to discard or overwrite later canonical state.

6.7. **When** completion result text is surfaced, normal frontend encoding, secure-session recording, customer-visible usage reconstruction, traffic observation, and terminal publication shall observe it through existing canonical release ownership rather than a feature-specific side channel.

### Requirement 7: Missing-Signal Protocol Repair

**Objective:** As an unattended agent user, I want an accidental clean stop without the completion signal to receive one bounded chance to self-correct instead of terminating the A-leg loop immediately.

#### Acceptance Criteria

7.1. **When** the preferred protocol was active for the candidate B-leg, no trusted completion signal was observed, and an otherwise recoverable clean terminal is proposed, ALG shall suppress that terminal through the existing terminal-decision contract and request a bounded hidden continuation.

7.2. **When** such a continuation is created, its control text shall state that the prior turn ended without the required completion signal, direct the worker to call `attempt_completion` if all requested work is complete, otherwise continue only remaining in-scope work, and prohibit invention or scope expansion.

7.3. **When** the worker needs user input rather than autonomous work, the repair text shall permit it to make the required user-facing request and end normally without falsely claiming completion.

7.4. **When** the first protocol-repair continuation itself ends without a completion signal, the default policy shall allow the new terminal rather than forcing an unbounded sequence of repair turns.

7.5. **Where** an operator configures more than one protocol reprompt within the supported bound, ALG shall enforce both a fixed total reprompt cap and no-progress protection; progress shall never reset the immutable total cap.

7.6. **If** the protocol was not active for the candidate, ALG shall not create a missing-signal continuation merely because a completion marker is absent.

7.7. **When** the platform rejects continuation admission, placement, authority, protocol legality, or lifecycle work, ALG shall accept the platform's conservative final outcome and shall not create a second continuation authority.

### Requirement 8: Transport, Cancellation, and Side-Effect Safety

**Objective:** As a user, I want the new protocol to improve loop continuity without weakening existing retry and side-effect guarantees.

#### Acceptance Criteria

8.1. **When** a transport failure occurs before meaningful output commitment, existing pre-output recovery shall remain authoritative and the completion protocol shall not add a competing replay budget.

8.2. **When** a post-output interruption occurs after a B-leg on which the completion protocol was active and no completion signal was observed, ALG may request a new continuation leg from retained canonical trajectory but shall never replay the committed attempt.

8.3. **When** completed client tool calls/results are retained before an interruption, continuation shall preserve them and shall not request their re-execution solely because the later stream failed.

8.4. **If** incomplete client tool arguments, opaque provider state, or another unsafe boundary prevents safe continuation, ALG shall stop conservatively.

8.5. **When** the client cancels the request, the proxy shall never convert absence of `attempt_completion` into automatic continuation.

8.6. **When** refusal, content filtering, or another authoritative non-recoverable terminal cause occurs, the completion protocol shall not reinterpret it as missing completion work.

8.7. **The** new strategy shall preserve the existing rule that no transparent retry, failover, or race substitution occurs after first client-visible output commitment.

### Requirement 9: Legacy Semantic-Verifier Strategy Preservation

**Objective:** As an existing operator, I want the current verifier-based ALG behavior to remain available so that adoption of the new protocol is reversible and does not force a migration.

#### Acceptance Criteria

9.1. **When** strategy `semantic_verifier` is selected, the current cause/evidence projection, verifier timeout/error behavior, explicit-completion trust policy, progress/no-progress policy, and continuation semantics shall remain behaviorally compatible unless a separately justified bug fix is required.

9.2. **When** the legacy strategy evaluates an eligible clean stop without trusted explicit completion, it shall continue to use the existing detached bounded verifier rather than the new completion protocol.

9.3. **When** the legacy strategy sees trusted normalized explicit completion under its existing trust policy, it shall retain the existing ability to bypass semantic verification.

9.4. **When** the preferred strategy is selected, verifier role/timeout requests and auxiliary verifier usage shall be absent from the execution path.

9.5. **When** either strategy is selected, both shall continue to rely on the same generic terminal owner and continuation transaction rather than implement separate terminal publication paths.

9.6. **When** regression tests compare pre-spec legacy configuration with explicit `strategy: semantic_verifier`, observable decisions shall match for the certified acceptance matrix.

### Requirement 10: Configuration Bounds, Reload, and Lifecycle

**Objective:** As an operator, I want strategy changes to obey immutable generation semantics and remain bounded under reload.

#### Acceptance Criteria

10.1. **When** the preferred strategy is explicitly selected, the default maximum missing-signal protocol reprompts shall be one.

10.2. **If** an operator configures a protocol-reprompt bound outside the supported finite range, generation compilation shall fail before publication.

10.3. **When** configuration reload changes ALG strategy, newly admitted requests shall use the newly published generation while in-flight requests shall remain pinned to the strategy and control-tool provenance admitted with their original generation.

10.4. **When** a candidate generation containing invalid mixed-strategy configuration fails, the last-good generation shall remain active and unchanged.

10.5. **When** ALG is withdrawn from the feature registry, generic core/runtime behavior shall remain usable without concrete ALG imports, provider-name branches, or stale control-tool state.

10.6. **The** preferred protocol shall require no new durable per-session database state solely to remember that the tool/instruction was advertised; activation state shall remain bounded to the admitted request/attempt unless existing continuity facts already own it.

### Requirement 11: Observability, Privacy, and Diagnostics

**Objective:** As an operator, I want to understand protocol behavior without leaking prompts, tool results, or identifiers into telemetry.

#### Acceptance Criteria

11.1. **When** protocol activation is attempted, telemetry shall be able to distinguish bounded outcomes such as active, backend-tools-unsupported, incompatible-tool-choice, tool-name-collision, malformed-control-call, completion-observed, reprompted, reprompt-exhausted, and protocol-inactive.

11.2. **When** a completion call is handled, metric labels and structured reason codes shall not contain the `result` text, tool arguments, prompts, raw A-leg/B-leg IDs, secrets, or other unbounded content.

11.3. **When** the preferred strategy runs, usage/accounting shall continue to attribute upstream model usage to the real B-leg and shall not manufacture provider usage for the local control-tool handling itself.

11.4. **When** the legacy verifier strategy runs, its existing auxiliary usage/trace lineage shall remain separately attributable as before.

11.5. **When** the proxy surfaces completion `result` text to the client, that text shall be treated as ordinary client-visible assistant content for existing recording/redaction policy; hidden protocol instructions remain backend-visible and shall not be treated as secrets.

### Requirement 12: Architecture and Acceptance Gates

**Objective:** As a maintainer, I want the feature to remain removable, streaming-first, and compatible with adjacent runtime work.

#### Acceptance Criteria

12.1. **The** implementation shall keep concrete ALG policy outside `internal/core` and shall not add provider-name or feature-name switches to generic terminal, tool, or stream logic.

12.2. **The** smallest new generic proxy-control-tool contract, if required, shall be usable without importing ALG and shall not become a DI container, generic service locator, arbitrary local-tool runtime, or alternate terminal engine.

12.3. **When** architecture tests run, they shall prove that proxy-owned completion-tool events cannot reach A-leg/client tool execution and that disabling/removing ALG leaves the generic control-tool seam inert.

12.4. **When** streaming tests run, they shall prove ordinary text and non-control tool events are released incrementally rather than held until terminal solely because the preferred strategy is enabled.

12.5. **When** the acceptance matrix runs, it shall cover: valid completion-only response, visible text then completion, clean stop without completion, second unmarked stop after repair, user-input-needed stop, client cancellation, refusal/filter, pre-output transport failure, post-output interruption, completed client tool/result retention, malformed/oversized completion args, incompatible tool choice, unsupported backend tools, client-owned completion tool, reload between strategies, and legacy verifier parity.

12.6. **Before** implementation is declared complete, the change shall revalidate candidate-open/tool-finalization ordering against any landed implementation of `b-leg-path-virtualization` and other active specs that modify the same seams.

12.7. **When** the implementation is declared ready, focused feature/SDK/runtime/architecture tests and the repository's applicable quality/test/QA gates shall pass, with environment-specific limitations reported rather than silently skipped.
