# Requirements Document

## Introduction

Go-LIP needs one reusable extension seam for decisions made while a backend terminal is still provisional. The seam must let an optional feature inspect a canonical terminal candidate and return a bounded decision intent, while the core remains the only authority that holds or publishes the A-side terminal, settles B-side attempts, and starts continuation work. The seam is intended to host Agent Loop Guard and future terminal-decision policies without putting feature-specific policy into `internal/core`.

The extension is brownfield. Existing immutable generations, request/attempt terminal ownership, stream recovery, canonical continuation, secure-session authority, HTTP authentication, billing, routing, B2BUA continuity, and process shutdown remain authoritative. The extension adds only the narrow generic capability and its generic policy-control surface.

## Boundary Context

- **In scope**: one exclusive provisional-terminal provider per generation; one core terminal decision chokepoint; core-owned transactional continuation; immutable activation and withdrawal; process-owned bounded secure-session policy; generic client/operator policy endpoints; lifecycle, failure, observability, architecture, and simplification gates.
- **Out of scope**: Agent Loop Guard's verifier/classifier wording and feature policy; provider SDK behavior; a generic workflow/effect runtime; a second retry engine; durable policy persistence; changes to billing, routing, B2BUA, or secure-session authentication semantics.
- **Adjacent expectations**: `agent-loop-breach-prevention` supplies the concrete ALG feature provider and depends on this spec. Existing conversation-view steering is the canonical hidden-control mechanism used by core continuation; this spec does not create a second hidden-content authority.
- **Boundary ownership**: stable provider contract in `pkg/lipsdk`; generic core decision/continuation and process policy in existing core/runtime composition; generic HTTP adapters in standard HTTP composition; no provider-specific branches in core.
- **Revalidation triggers**: terminal ownership, generation publication/withdrawal, continuation materialization, secure-session identity/authorization, endpoint authentication, or the `FeatureBundle` contract changes.

## Requirements

### Requirement 1: Exclusive Generic Provider Contribution

**Objective:** As a feature author, I want one generic provisional-terminal provider seam so that a feature can supply policy without creating a provider-specific core branch.

#### Acceptance Criteria

1.1. **When** an enabled feature contributes a provisional-terminal decision provider, the generation shall accept that provider as one generic contribution with a stable identity.

1.2. **If** more than one enabled feature contributes a provisional-terminal provider, generation compilation shall fail before publication and shall identify the conflicting provider identities without selecting an order or silently dropping one.

1.3. **Where** no provider is contributed, the generation shall preserve the existing terminal and stream behavior with no extra provider call.

1.4. **When** a provider is invoked, the system shall supply canonical terminal evidence, bounded request identity, output-commit state, and the frozen request policy snapshot without provider SDK or raw transport types.

1.5. **The** provider shall return only a bounded allow-stop, continue-intent, or controlled-failure decision; it shall not claim a terminal, mutate the client stream, open a backend, or mutate a request snapshot directly.

### Requirement 2: Single Core Terminal Decision Chokepoint

**Objective:** As a runtime maintainer, I want every logical terminal to cross one core-owned boundary so that provisional decisions cannot leak through an alternate finish path.

#### Acceptance Criteria

2.1. **When** any backend attempt produces a logical A-side terminal candidate, the core shall route it through one decision chokepoint before final A-side terminal publication, including normal finish, transport-derived finish, limit, refusal, and provider-error outcomes.

2.2. **While** a candidate is under provider decision, the system shall keep the candidate provisional and shall not emit an A-side final marker, equivalent end-of-response signal, or duplicate terminal.

2.3. **When** cancellation, refusal/content filtering, authority denial, or another explicitly non-recoverable outcome is authoritative, the chokepoint shall preserve that outcome and shall not permit a provider to invent continuation authority.

2.4. **When** a B-side attempt is swallowed for a continuation, the attempt shall settle exactly once while the A-side logical request remains open until the chokepoint claims one final outcome.

2.5. **If** output has been committed, the chokepoint shall never classify continuation as retry or failover of the committed attempt.

### Requirement 3: Core-Owned Transactional Continuation

**Objective:** As a feature author, I want continuation to be executed by the core so that all extensions preserve routing, authority, billing, lineage, and stream safety.

#### Acceptance Criteria

3.1. **When** a provider returns a valid continue intent, the core shall validate its bounds, authority, canonical trajectory reference, and protocol eligibility before any new backend work.

3.2. **When** continuation is requested, the core shall prepare and open the next B-side leg through normal routing, authority, billing, and B2BUA paths and atomically publish it as the current attempt before settling the prior B-side attempt.

3.3. **When** the continuation leg is atomically published, the core shall settle the prior B-side attempt exactly once and keep the same A-side logical request open while exposing only legal canonical events from the current leg.

3.4. **If** materialization, admission, routing, backend open, steering placement, or protocol legality fails before publication, the core shall deactivate partial steering, leave the original B-side attempt unsettled, and finalize the original B1 candidate/request normally while preserving committed output; if B1 settlement reports an error after B2 publication, the core shall retain B2 as current, emit bounded diagnostic evidence, and shall not fabricate rollback of B2.

3.5. **When** hidden control content is required, the core shall use the existing canonical non-forwardable steering/conversation-view authority; direct appending to `Call.Messages` or `Call.Items` and secondary hidden fields shall be rejected by architecture tests.

3.6. **While** a continuation transaction is active, cancellation and terminal close shall be cancellation-aware; a losing continuation shall not publish output or leave an owned goroutine, lease, overlay, or attempt unsettled.

### Requirement 4: Immutable Generation Activation and Withdrawal

**Objective:** As an operator, I want extension changes to use normal generation lifecycle so that in-flight requests cannot observe live mutation.

#### Acceptance Criteria

4.1. **When** configuration enables, disables, or changes the provider, the system shall compile and validate a candidate immutable generation before publication.

4.2. **When** a generation is published, requests admitted to it shall retain its provider and policy projection for their lifetime; later reloads shall not rebind that published request.

4.3. **When** a generation is withdrawn, the runtime shall stop new admission, quiesce dependents, drain retained requests and continuation work, and close generation-owned resources in the established order.

4.4. **If** candidate compilation, provider construction, or validation fails, the last-good published generation shall remain serving and no partially constructed provider shall escape without cleanup.

4.5. **When** a provider is disabled, new requests shall observe no provider while requests pinned to the old generation complete under the old immutable snapshot.

### Requirement 5: Process-Owned Bounded Secure-Session Policy

**Objective:** As a client and operator, I want independent tri-state policy controls so that terminal decisions can be enabled or disabled for a secure session without changing generation composition.

#### Acceptance Criteria

5.1. **When** a policy override is set for an authenticated secure-session/A-leg scope, the process shall retain separate client and operator tri-state values: `unset`, `enabled`, or `disabled`.

5.2. **When** the effective policy is computed, any explicit `disabled` value shall win over every `enabled` value; when no disable exists, an explicit enable shall win over `unset`; when both are unset, the generation default shall apply.

5.3. **While** the process is running, the policy store shall enforce configured entry, key, and value bounds and shall reject new keys at capacity without changing existing entries.

5.4. **When** policy state is read or changed, the system shall bind it to secure-session authority and the target A-leg scope; an unauthenticated or unauthorized caller shall not read or change another scope.

5.5. **The** policy store shall not persist raw session credentials or conversational content and shall expose only bounded identifiers and state needed for diagnostics.

### Requirement 6: Policy Lifecycle and Next-Request Snapshot

**Objective:** As an operator, I want policy overrides to survive reloads predictably without changing an in-flight request or surviving a process restart unexpectedly.

#### Acceptance Criteria

6.1. **When** configuration is reloaded or the provider is disabled and later re-enabled, process-owned client/operator overrides shall remain available with their bounded revisions until explicitly cleared or the process restarts.

6.2. **When** the process restarts, the new process shall start with an empty policy store unless a future explicitly approved durable policy feature is present; this extension shall not add durable policy storage.

6.3. **When** a request is admitted, the runtime shall take one effective-policy snapshot before terminal decision evaluation; later policy writes shall affect only a subsequent request.

6.4. **When** a policy write races a request snapshot or another write, serialized key-boundary writes shall define one deterministic revision order; the request shall observe either the old or new complete snapshot, never a mixed client/operator state, and no update shall be lost.

6.5. **If** a new key would exceed the configured policy-store capacity, the write shall be rejected with a stable capacity error and shall not mutate any existing entry; existing values remain until explicitly cleared or process restart.

### Requirement 7: Generic Client and Operator Policy Endpoints

**Objective:** As a client or operator, I want generic controls for the terminal-decision policy so that controls are reusable by any provider and do not expose ALG-specific routes.

#### Acceptance Criteria

7.1. **When** an authenticated client calls `GET`, `PUT`, or `DELETE /v1/lip/session/features/{feature_id}`, the system shall resolve only the current authoritative secure session, allow a bounded read, accept exactly `{"enabled": true|false}` for `PUT`, and interpret `DELETE` as client inherit for that feature; missing client authority shall return 403.

7.2. **When** an authorized operator calls `GET`, `PUT`, or `DELETE /admin/session-features/{session_id}/{feature_id}` under existing admin authentication, the system shall validate the authoritative target session, return bounded effective/client/operator state, accept exactly `{"enabled": true|false}` for `PUT`, and interpret `DELETE` as operator inherit.

7.3. **The** generic policy surfaces shall use provider-neutral resource names and payloads; route names, schemas, and diagnostics shall not contain Agent Loop Guard or another concrete provider name.

7.4. **If** a feature is unknown or unregistered, the endpoint shall return 404; if client authority is missing, it shall return 403; invalid bodies/fields, capacity rejection, store failure, and unauthorized or invalid targets shall use stable bounded 4xx/5xx API errors without changing policy state.

7.5. **When** the policy store is unavailable or closing, the endpoint shall fail closed and shall not report a successful override that was not committed.

7.6. **When** an endpoint GET returns, the bounded response shall identify the feature state, source/actor state as applicable, and revision, and shall never contain `applies_from`; **when** a PUT or DELETE succeeds, the response shall additionally contain `applies_from: next_request` and shall not imply mutation of an in-flight request.

#### Endpoint Contract and Error Mapping

| Condition | Status | Error code/headers | Mutation |
|---|---:|---|---|
| Unsupported method | 405 | `method_not_allowed`, `Allow` | None |
| Wrong media type | 415 | `unsupported_media_type` | None |
| Oversized body | 413 | `body_too_large` | None |
| Malformed, empty, wrong-shape, or unknown-field PUT | 400 | `invalid_request` | None |
| Unauthenticated client | 401 | `unauthorized` | None |
| Authenticated client without authoritative secure session | 403 | `secure_session_required` | None |
| Unknown or unregistered feature | 404 | `feature_not_found` | None |
| Unauthenticated operator when distinguished upstream authentication fails | 401 | `unauthorized` | None |
| Diagnostics shared-secret mismatch | 403 | `forbidden` | None |
| Authenticated operator lacking target authorization | 403 | `forbidden` | None |
| Authorized operator with invalid target session | 404 | `session_not_found` | None |
| Policy key capacity reached | 409 | `policy_capacity` | None |
| Policy store absent or closing | 503 | `policy_unavailable` | None |

Client GET responses contain only bounded `feature_id`, `available`, `client_state`, `effective_enabled`, and `revision`, and never contain `applies_from`; successful client PUT/DELETE responses additionally contain `applies_from: next_request`. Operator responses additionally contain `operator_state` with the same GET-versus-successful-mutation rule. DELETE sets the calling actor's state to inherit. PUT and DELETE carry no request-side expected revision; `revision` is response and internal linearization evidence only. All errors are provider-neutral and mutate nothing.

### Requirement 8: Failure Schedules and Exactly-Once Outcomes

**Objective:** As a maintainer, I want deterministic outcomes for races and partial failures so that the generic seam cannot leak terminals or resources.

#### Acceptance Criteria

8.1. **If** a provider times out, returns an error, panics at its boundary, or returns malformed/unknown data, the core shall normalize the result to an allowed final terminal (and bounded diagnostic) without starting hidden continuation.

8.2. **When** client cancellation races provider completion, continuation admission, or backend open, cancellation shall win before any uncommitted continuation output and the logical request shall terminalize once.

8.3. **When** generation withdrawal races an admitted provider decision, the pinned request shall either complete under its immutable generation or fail through the established close path; no new request shall use the withdrawn provider.

8.4. **If** continuation admission or open fails before B2 publication, the core shall deactivate partial steering, shall not pre-settle B1, and shall finalize the original B1 candidate/request normally; if B1 settlement reports loss/error after B2 publication, B2 shall remain current and the core shall report the settlement issue without fabricating rollback.

8.5. **If** a candidate generation fails after acquiring a provider or policy resource, construction shall unwind owned resources in reverse order and leave the last-good generation unchanged.

8.6. **When** client/operator policy writes race each other or a request snapshot, serialized key-boundary writes and the effective-state rule shall prevent lost updates and mixed snapshots; no request-side expected revision or revision validation shall be used.

### Requirement 9: Observability, Privacy, and Bounds

**Objective:** As an operator, I want to diagnose terminal decisions and policy changes without exposing sensitive conversation data or allowing unbounded work.

#### Acceptance Criteria

9.1. **When** the chokepoint evaluates a candidate, telemetry shall expose bounded cause, provider identity, action, outcome, and failure reason codes.

9.2. **When** continuation or policy control spans A-leg and B-leg work, diagnostics shall preserve existing trace, lineage, authority, and billing relationships.

9.3. **The** system shall not place prompts, response bodies, tool arguments, secrets, raw policy payloads, or unbounded identifiers in metric labels or default logs.

9.4. **While** provider evaluation, continuation, policy entries, and endpoint payloads are active, configured deadlines, size limits, attempt limits, and cardinality bounds shall be enforced.

### Requirement 10: Architecture Guardrails and Single Ownership

**Objective:** As a maintainer, I want the extension to remain narrow so that future features cannot recreate the rejected generic runtime designs.

#### Acceptance Criteria

10.1. **The** core terminal decision path shall not import a concrete feature provider, provider SDK, provider-name switch, or feature-specific instruction vocabulary.

10.2. **The** implementation shall use explicit construction and registration and shall not add Go native plugin loading, reflection-based registration, a service locator, a DI container, or a generic effect/inverse-effect runtime.

10.3. **The** system shall have one logical owner for each physical provider, policy store, continuation transaction, steering overlay, and terminal claim; duplicated close or lookup authorities shall fail architecture review.

10.4. **When** architecture ratchets run, they shall detect a provider slice/chain replacing the exclusive field, a second terminal claim path, request-hot-path policy lookup, live generation mutation, direct hidden-content append, or provider-specific core branch.

### Requirement 11: ROI and Simplification Gates

**Objective:** As a project owner, I want measurable evidence before accepting a new extension seam so that abstraction cost does not exceed the lifecycle and maintenance benefit.

#### Acceptance Criteria

11.1. **When** implementation changes are proposed, the project shall record a deterministic baseline for terminal claim sites, existing feature contribution fields, concrete ALG references in core, policy owners, and continuation cleanup paths.

11.2. **When** the platform is implemented, the target evidence shall show one exclusive provider contribution, one core terminal chokepoint, zero concrete ALG policy branches in core, one process policy owner, and no new generic runtime owner.

11.3. **If** characterization does not demonstrate a material ownership/change-surface simplification, the project shall reject or narrow the abstraction rather than keep speculative infrastructure.

11.4. **When** implementation is declared ready, a simplification review shall compare concepts added, concepts removed, ownership paths, request-hot-path work, and measurable operation counts; unresolved net complexity is a no-go.

### Requirement 12: Removable Concrete Feature and Spec Dependency

**Objective:** As a platform maintainer, I want concrete Agent Loop Guard policy removable from core so that the generic seam remains reusable and independently testable.

#### Acceptance Criteria

12.1. **When** the Agent Loop Guard feature is disabled or not installed, the platform shall build and run without its verifier, classifier, instruction text, or provider-specific packages.

12.2. **When** the concrete feature is removed from the feature registry, the generic core terminal/continuation/policy paths shall remain compilable and preserve no-provider behavior.

12.3. **The** platform and concrete feature specifications shall declare a dependency from the feature provider to this platform contract, with platform tasks completed before provider integration tasks begin.

12.4. **When** a feature-specific behavior is proposed for the generic seam, the change shall be rejected unless it can be expressed through the provider-neutral contract and passes the architecture, failure-schedule, ROI, and simplification gates.
