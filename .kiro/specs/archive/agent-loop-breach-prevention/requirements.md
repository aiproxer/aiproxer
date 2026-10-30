# Requirements Document

## Introduction

Agent Loop Guard (ALG) is the first concrete policy provider for the generic terminal-decision extension defined by `.kiro/specs/terminal-decision-feature-extension/`. It protects unattended agent loops from recoverable backend interruptions and suspiciously premature clean stops while preserving conservative user-authority boundaries.

This specification owns ALG policy: canonical cause/evidence classification, bounded semantic verification, progress/no-progress decisions, and the provider's bounded continuation intent and internal recovery content. The platform specification owns FeatureBundle registration, the single core terminal chokepoint, transactional continuation execution, conversation-view steering lifecycle, immutable generations, process policy, and generic client/operator endpoints. ALG must not recreate those authorities.

## Boundary Context

- **In scope**: an opt-in ALG feature provider; canonical terminal-cause/evidence classification; independent bounded completion verification; conservative verdict policy; bounded progress and continuation intent; provider-specific internal recovery wording; ALG-specific metrics and fixtures.
- **Out of scope**: FeatureBundle merge mechanics; terminal ownership/chokepoint; continuation admission/settlement; conversation-view storage/projection/reassertion; secure-session tri-state store; client/operator endpoints; routing, billing, B2BUA, stream-recovery transport algorithms, or generic lifecycle infrastructure.
- **Adjacent expectations**: The provider depends on the ready-to-implement `terminal-decision-feature-extension` spec and uses its provider-neutral contract. The platform executes every returned intent and remains the only owner of terminal publication and continuation side effects.
- **Boundary ownership**: concrete feature plugin/provider policy and its SDK-facing auxiliary adapter; no ALG implementation in `internal/core`.
- **Revalidation triggers**: provider contract, canonical continuation evidence, auxiliary request visibility/recursion controls, explicit completion facts, or platform terminal/steering transaction semantics change.

## Requirements

### Requirement 1: Opt-In ALG Provider and Compatibility

**Objective:** As an operator, I want ALG to be explicitly enabled so that existing deployments do not gain autonomous continuation unexpectedly.

#### Acceptance Criteria

1.1. **Where** ALG is disabled, the proxy shall preserve existing terminal, stream-recovery, continuation, and A-side publication behavior and shall not construct an ALG verifier or provider.

1.2. **When** ALG is enabled and the platform supplies an eligible provisional terminal candidate, ALG shall return a bounded provider-neutral decision through the platform contract rather than publishing a terminal itself.

1.3. **If** a candidate or state is outside ALG's supported evidence/continuation policy, ALG shall return an allow-stop decision or an otherwise conservative non-continuation result.

1.4. **When** the ALG feature is not installed or is removed from the registry, the generic platform shall remain usable without ALG-specific packages, configuration, or core branches.

### Requirement 2: Canonical Terminal-Cause and Evidence Classification

**Objective:** As a maintainer, I want ALG to use canonical facts so that policy is protocol- and provider-neutral.

#### Acceptance Criteria

2.1. **When** a candidate is evaluated, ALG shall distinguish clean normal completion, empty/near-empty completion, provider pause/deferred continuation, output/token limit, transport interruption before/after commitment, partial tool-call state, refusal/content filtering, client cancellation, and unknown state when canonical facts expose those distinctions.

2.2. **When** a transport interruption is classified, ALG shall use canonical output-commit state and shall not infer commitment from provider name or raw wire frames.

2.3. **When** evidence is projected, ALG shall include the bounded current user objective, relevant recent canonical trajectory, candidate assistant output, tool/action state, explicit completion fact, lineage reference, and logical continuation attempt number.

2.4. **If** a cause or evidence field cannot be classified safely, ALG shall return conservative non-continuation rather than create model work solely to resolve ambiguity.

2.5. **When** the candidate represents client cancellation, refusal/content filtering, or another explicit non-recoverable outcome, ALG shall not reinterpret it as unfinished agent work.

### Requirement 3: Pre-Output Transport Recovery Policy

**Objective:** As a user, I want transient failures before visible output handled by existing transport recovery rather than by a competing ALG retry loop.

#### Acceptance Criteria

3.1. **When** a transport failure occurs before meaningful output commitment, ALG shall return the platform decision that delegates to existing bounded pre-output recovery policy without adding a second retry budget.

3.2. **While** existing replay-safe recovery is in progress, ALG shall not authorize an intermediate A-side terminal or a semantic continuation.

3.3. **When** an eligible empty/near-empty clean completion is offered to the existing pre-output recovery path, ALG shall not require semantic verification before that path resolves.

3.4. **If** existing recovery is disabled or exhausted, ALG shall return a conservative final decision and shall not start hidden work after client cancellation.

### Requirement 4: Post-Output Interruption Safety

**Objective:** As a user, I want interrupted work resumed without replaying committed output or side effects.

#### Acceptance Criteria

4.1. **When** meaningful output is committed, ALG shall never return an intent that labels replay or failover of the committed attempt as safe.

4.2. **When** a post-output interruption has a canonically resumable trajectory and budget remains, ALG shall return a bounded continuation intent that refers to the retained trajectory and does not duplicate committed assistant output.

4.3. **When** a completed tool call and matching result are retained, ALG shall preserve that fact in its evidence and shall not request re-execution solely because the later stream failed.

4.4. **If** incomplete tool arguments, opaque provider state, or another unsafe state cannot be resumed through a normalized safe capability, ALG shall return non-continuation.

4.5. **If** post-output continuation is unavailable, protocol-ineligible, or exhausted, ALG shall return a final conservative decision while preserving committed output.

4.6. **When** the platform executes an ALG continuation intent, ALG shall not assume that the platform can undo external provider calls, emitted output, tool effects, or durable records.

### Requirement 5: Independent Semantic Completion Verification

**Objective:** As an unattended agent user, I want suspiciously premature clean stops checked by an independent bounded evaluator.

#### Acceptance Criteria

5.1. **When** an eligible clean normal completion has no unresolved tool boundary and ALG is enabled, ALG shall request one independent completion verdict before allowing continuation.

5.2. **When** the verifier concludes the requested work is complete, ALG shall return allow-stop without a continuation intent.

5.3. **When** the verifier identifies concrete unfinished work already requested by the user and executable without new input, ALG shall return a continuation intent.

5.4. **When** the next step needs user input, approval, permission, credentials, clarification, or a choice, ALG shall return allow-stop and shall not synthesize that response or authorization.

5.5. **When** the work is externally blocked, ALG shall return allow-stop rather than repeatedly asking the worker model to continue.

5.6. **If** verifier timeout, transport failure, malformed output, unknown verdict, missing objective, or uncertainty occurs, ALG shall return allow-stop.

5.7. **Where** a trusted normalized explicit completion fact is present, ALG shall honor the configured trust policy and bypass semantic verification for that clean stop while retaining transport-failure protection.

5.8. **Where** explicit completion is configured as evidence rather than trust, ALG shall pass it to the verifier without treating it as an unconditional continuation or stop override.

### Requirement 6: Bounded Conditional Continuation Intent

**Objective:** As a user, I want any automatic continuation constrained to my existing request and represented through the platform's safe transaction.

#### Acceptance Criteria

6.1. **When** ALG returns continuation, the intent content shall state that it is automated internal recovery and is not a new user request, approval, permission, or scope expansion.

6.2. **When** the original work is complete, the intent content shall direct the worker to end normally without inventing, repeating, broadening, optimizing, or discovering work.

6.3. **When** concrete unfinished work remains, the intent content shall constrain the worker to that existing work and the retained canonical safe point.

6.4. **When** further progress requires user input, approval, permission, credentials, clarification, or a choice, the intent content shall direct the worker to stop for the user rather than assume it.

6.5. **When** the platform receives the intent, ALG shall provide only bounded provider-neutral continuation fields and shall not append to canonical calls, claim terminals, open backends, or mutate conversation snapshots.

6.6. **When** multiple ALG continuation attempts occur, ALG shall retain the logical request's immutable maximum budget and shall not reset it merely because new output exists.

6.7. **If** the platform rejects intent bounds, authority, placement, protocol capability, or lifecycle admission, ALG shall accept the conservative final outcome and shall not retry through a second authority.

### Requirement 7: Progress, Recursion, and Cost Bounds

**Objective:** As an operator, I want ALG unable to create an unbounded hidden loop or uncontrolled verifier cost.

#### Acceptance Criteria

7.1. **When** ALG is enabled, it shall enforce a configured maximum number of semantic continuation intents per logical request.

7.2. **When** ALG invokes its verifier, it shall enforce a bounded timeout and shall normalize timeout to allow-stop.

7.3. **When** successive candidates repeat materially equivalent assistant output, tool/action/error state, verifier decision, or remaining objective without canonical progress, ALG shall return non-continuation at the configured no-progress limit.

7.4. **While** the verifier or an ALG policy evaluation is executing, ALG shall suppress ALG recursion for that internal operation.

7.5. **When** a budget or no-progress limit is reached, ALG shall return one final conservative decision and shall not leave the platform request waiting for another hidden leg.

### Requirement 8: Evidence and False-Positive Resistance

**Objective:** As a user, I want clean answers and user-owned next steps left alone rather than misclassified from superficial wording.

#### Acceptance Criteria

8.1. **When** the verifier evaluates a candidate, it shall consider the current user objective and relevant recent instructions together with candidate output and canonical tool/action trajectory.

8.2. **When** the candidate says requested work is complete and evidence does not contradict it, optional language such as “I can also…” shall not require continuation.

8.3. **When** the candidate asks the user a question or offers an optional next action, that wording alone shall not authorize continuation.

8.4. **When** a “Next steps” section assigns work to the user or recommends future work outside the request, it shall not authorize continuation.

8.5. **When** the candidate commits to an immediate in-scope action and the trajectory lacks evidence that it occurred, ALG shall be able to return concrete continuation intent.

8.6. **When** quoted or discussed text contains future-action wording but the assistant is not committing to that action, ALG shall not treat the quotation alone as unfinished work.

### Requirement 9: Feature Lifecycle and Platform Dependency

**Objective:** As a maintainer, I want ALG to activate and withdraw as a normal feature provider so that concrete policy can be removed without changing core architecture.

#### Acceptance Criteria

9.1. **When** ALG configuration is enabled, its provider shall be constructed through the platform's FeatureBundle contribution and shall not add an ALG branch to core terminal logic.

9.2. **When** ALG is disabled, reloaded, or removed, existing requests shall follow the platform's immutable generation semantics and no new request shall call a withdrawn ALG provider.

9.3. **When** the platform process policy snapshot is supplied, ALG shall consume that snapshot and shall not create a second client/operator policy store or endpoint.

9.4. **If** the platform contract is unavailable or incompatible, ALG feature construction shall fail before generation publication and shall not fall back to direct core integration.

9.5. **When** ALG is removed from the registry, all ALG verifier/classifier/instruction code shall be removable without leaving core imports, provider-name checks, or hidden terminal authorities.

### Requirement 10: ALG Observability and Acceptance Matrix

**Objective:** As an operator and maintainer, I want bounded evidence for ALG decisions and deterministic regressions for false stops and unsafe continuation.

#### Acceptance Criteria

10.1. **When** ALG evaluates a candidate, it shall emit bounded cause, verdict, action, continuation, no-progress, and failure reason evidence through existing observability seams.

10.2. **When** ALG runs a verifier or continuation, telemetry shall preserve A-leg/B-leg/trace relationships and existing usage/accounting attribution without putting content or secrets in labels.

10.3. **When** a clean stop follows an immediate promised in-scope action with no corresponding trajectory action, ALG shall be able to return continuation.

10.4. **When** a clean stop follows a complete answer, user-directed question, optional improvement, user-owned next steps, quoted future action, refusal, or filter, ALG shall return allow-stop.

10.5. **When** pre-output EOF, post-output interruption, completed tool/result retention, incomplete tool arguments, cancellation, verifier failure, no-progress, and budget exhaustion are exercised, ALG shall produce the conservative provider decisions required by this specification.

10.6. **When** the platform executes a valid ALG intent across supported frontends, no intermediate terminal or hidden control content shall become client-visible; protocol legality remains the platform's responsibility.

10.7. **When** architecture ratchets run, they shall prove zero direct call append, zero `turnTerminal.guardHidden` dependency, zero provider-specific core branch, and zero second ALG policy owner.

### Requirement 11: Scope and Review Gates

**Objective:** As a project owner, I want ALG implementation bounded to policy so that generic platform work remains reusable and reviewable.

#### Acceptance Criteria

11.1. **The** ALG implementation shall contain no Go native plugin loading, DI/container wiring, service locator, reflection registry, generic effect runtime, core provider-name switch, or duplicate terminal/continuation owner.

11.2. **When** ALG provider integration begins, the platform dependency's contract, terminal chokepoint, continuation transaction, policy snapshot, and generic endpoints shall have passed their approved task gates.

11.3. **When** ALG is declared ready, tests shall show that removing the concrete provider leaves generic platform behavior and no-provider compatibility intact.

11.4. **If** ALG-specific work would require changing the generic platform's ownership or adding a second authority, the work shall return to platform design rather than expand this specification.
