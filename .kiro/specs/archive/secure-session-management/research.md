# Gap Analysis: secure-session-management

## Status Note

- Requirements are generated but not yet approved in `.kiro/specs/archive/secure-session-management/spec.json:6`.
- This analysis is still useful for design because the project is a brownfield codebase with existing continuity, auth, and diagnostics behavior.

## Current State Investigation

### Existing Assets

| Area | Existing asset | Evidence |
| --- | --- | --- |
| Canonical session envelope | `lipapi.SessionRef` already carries `ClientSessionID`, `ContinuityKey`, and `ALegID` on every canonical call | `pkg/lipapi/call.go:18` |
| B2BUA continuity | Core continuity manager resolves sessions in order `ALegID -> ContinuityKey -> new` | `internal/core/continuity/manager.go:35` |
| Opaque server-generated leg ids | A-leg and B-leg ids are generated with `crypto/rand` and hex encoding | `internal/core/b2bua/ids.go:9` |
| Attempt lineage | Canonical `AttemptRecord` already captures B-leg, A-leg, seq, backend, model, outcome, and reason | `pkg/lipapi/lineage.go:15` |
| Durable continuity storage | SQLite store persists A-legs, B-legs, and attempts; memory store supports TTL and max legs | `internal/core/continuity/sqlitestore/store.go:76`, `internal/core/b2bua/store.go:22` |
| Auth context | HTTP auth middleware produces a trusted principal in request context on success | `internal/stdhttp/auth/middleware.go:32` |
| Runtime views | Executor snapshots principal, session, attempt, and workspace into execution context | `internal/core/runtime/executor_prepare.go:42`, `internal/core/execctx/views.go:16` |
| Session open extension seam | `session.Opener` can enrich session labels before submit hooks run | `pkg/lipsdk/session/opener.go:10`, `internal/core/extensions/session_open.go:16` |
| Workspace resolve seam | Runtime already resolves per-request workspace context | `internal/core/runtime/executor_prepare.go:77` |
| Traffic capture | Capture metadata already includes trace, A-leg, principal, and session id | `internal/core/runtime/executor_prepare.go:106`, `pkg/lipsdk/traffic/emit.go:45` |
| Operator attempt diagnostics | Existing diagnostics endpoint can load attempt rows by `a_leg_id` | `internal/core/diag/attempts.go:37` |

### Dominant Patterns and Constraints

- Core owns orchestration and continuity; adapters should not duplicate session/security policy.
- Public contracts are small and protocol-neutral; provider SDKs must not leak into core.
- Streaming is the primary path; non-streaming behavior is derived from the stream path.
- Current extension seams are explicit and mostly fail-open for workspace/session-open behavior.
- Read/query adapters are acceptable for diagnostics and operator flows, so session-summary reads can be introduced without forcing repository-shaped abstractions.
- Hexagonal boundary discipline should treat secure-session lifecycle as a core application use case: pure session invariants in a domain package, orchestration and outbound ports in an app package, and SQLite/diagnostics/runtime/frontend concerns as adapters.

### Integration Surfaces

- `pkg/lipapi.SessionRef` is the current ingress surface for session-related client hints.
- `b2bua.Store` and `continuity.Manager` are the natural persistence and resolution seams.
- `execview.PrincipalView` is the current trusted user identity carrier.
- `diag` HTTP handlers are the natural place for operator-visible session queries.
- Frontend plugins and protocol error mappers are the natural place to surface protocol-legal session denials.

## Requirement-to-Asset Map

| Requirement area | Existing asset | Gap tag | Gap analysis |
| --- | --- | --- | --- |
| Server-owned session identity | Opaque A-leg generation exists | Missing | The current authoritative continuity path is still resumable by `ALegID` or client-provided `ContinuityKey`; there is no distinct server-issued resumable session token or authoritative secure session id separate from routing continuity. |
| Concurrent uniqueness guarantees | `crypto/rand` id generation exists for A-legs/B-legs | Constraint | Random leg IDs are a strong starting point, but the current contract does not define session-level uniqueness semantics, concurrency guarantees, or whether secure session ids and A-leg ids are the same concept. |
| User binding and isolation | Trusted principal can exist in context | Missing | Stored continuity records do not include owner identity, and continuity resolution does not compare requested session ownership against the principal. |
| Fixation / forgery resistance | Auth middleware is fail-closed; continuity ids are opaque | Missing | There is no proxy-issued resume proof, no binding between secure session authority and trusted auth identity, and no anti-enumeration-specific denial taxonomy. |
| Full transcript semantics | Canonical calls/events exist; traffic capture exists | Missing | There is no durable ordered session transcript store that captures interleaved user messages, model outputs, tool calls, and tool responses as a single session artifact. |
| B2BUA-aware lineage | Attempt records and B-leg allocation are already present | Partial | Attempt lineage is strong, but it is not yet tied to a richer session object, surfaced transcript, or operator summary model. |
| Resume window from last activity | A-leg `LastSeenAt` and memory TTL exist | Missing | Current TTL is operational eviction, not policy-based resume authorization with user-visible denial and remote-event activity updates. |
| Restart-safe durability | SQLite persists continuity and attempts | Partial | Durable continuity exists, but not the full secure-session bundle: owner, workspace, treatment metadata, transcript, usage aggregates, audit references, resume policy state. |
| Usage accounting | Providers can emit usage events | Missing | No session-scoped durable accounting model or query surface exists for inbound/outbound/cached tokens and billing/cost metadata. |
| Auditing / serialization | Traffic transcript feature and raw capture seams exist | Partial | Observability capture exists, but there is no first-class session audit record with mandatory-vs-optional policy behavior and operator retrieval semantics. |
| Workspace association | Workspace is resolved per request | Missing | Workspace is not durably attached to the session or enforced during resume. |
| Per-session policy metadata | Session openers can set labels | Partial | Labels are not a secure persisted policy snapshot; current seams do not define authorization, protection, or guaranteed application on later turns. |
| Protocol-neutral errors | Frontends already map errors to protocol responses | Partial | A dedicated session error taxonomy is missing, especially for expired/not-owned/not-resumable/forged cases with non-enumerating behavior. |
| Operator visibility | Attempt diagnostics endpoint exists | Missing | No session summary, transcript access, usage summary, resume eligibility view, or authorization-aware audit retrieval exists. |

## Requirements Feasibility Analysis

### Technical Needs Implied by Requirements

- A durable secure-session record distinct from or layered over current A-leg continuity.
- Session owner binding to trusted principal identity.
- Resume authorization rules that validate ownership, policy, and inactivity window before execution.
- A consistent definition of session “last activity” that includes both accepted client input and remote LLM output.
- Durable transcript model for ordered turn contents and tool events.
- Session-scoped usage and billing/cost aggregation.
- Audit serialization and redaction-aware persistence.
- Workspace binding and validation at resume time.
- Protected per-session treatment metadata that survives later turns.
- Operator query surfaces with authorization and anti-enumeration behavior.

### Missing Capabilities

- No stored `owner_principal_id` or equivalent in the current continuity schema.
- No server-issued secure resume credential separate from user-controlled hints.
- No persisted session object containing workspace, metadata, transcript, usage, audit links, or policy snapshot.
- No resume-window enforcement path returning explicit protocol-legal denials.
- No operator session summary loader interface.
- No canonical error classification dedicated to secure session failures.

### Constraints from Existing Architecture

- Continuity is core-owned, so secure session authorization should stay near `internal/core/continuity` / `internal/core/runtime` rather than in each frontend.
- Existing fail-open workspace/session-open seams are unsafe defaults for security-critical session policy unless guarded or moved earlier in the lifecycle.
- Because `lipapi.SessionRef` is already public and stable, introducing new secure-session semantics must be careful about backward compatibility and meaning of existing fields.
- B2BUA semantics already depend on A-leg/B-leg records, so any new session model must align with rather than replace attempt lineage.

### Complexity Signals

- Security-sensitive identity binding across multiple frontends and diagnostics surfaces.
- New persistence model with migration implications.
- User-visible denial semantics across protocol families.
- Potential need to split “routing continuity” from “secure resumable session authority.”

## Implementation Approach Options

### Option A: Extend Existing Continuity Components

**When to consider**: Keep a single continuity/session persistence center in core.

**Which files/modules to extend**
- `internal/core/b2bua/store.go`
- `internal/core/continuity/manager.go`
- `internal/core/continuity/sqlitestore/store.go`
- `internal/core/runtime/executor_prepare.go`
- `pkg/lipapi/call.go`
- `internal/core/diag/*`

**How it would work**
- Expand A-leg records to include owner identity, resume state, workspace, policy metadata, and references to transcript/usage/audit artifacts.
- Keep A-leg as the authoritative secure session id or extend it with a mapped secure session id.
- Add resume checks into continuity resolution or executor preparation before request execution proceeds.

**Trade-offs**
- ✅ Leverages existing B2BUA and SQLite/memory store patterns.
- ✅ Keeps session and continuity ownership clearly in core.
- ✅ Minimizes new conceptual seams.
- ❌ Risks overloading A-leg continuity with too many responsibilities.
- ❌ Harder to separate “routing continuity handle” from “secure resumable session authority.”
- ❌ Schema evolution becomes broader and more invasive.

### Option B: Create a New Secure Session Component Beside Continuity

**When to consider**: Distinguish secure user session ownership from B2BUA continuity internals.

**Rationale for new creation**
- Secure session management has broader responsibility than current continuity rows.
- Transcript, usage, workspace, audit, and policy metadata form a richer domain than A-leg storage alone.

**Possible new components**
- `internal/core/session/` for secure-session lifecycle, ownership validation, resume policy, and session summary queries.
- New store interface for secure session records, with continuity store linked through A-leg ids.
- New diagnostics query adapter for session summaries and transcript access.

**Trade-offs**
- ✅ Cleaner separation between secure session product semantics and B2BUA mechanics.
- ✅ Easier to model richer metadata and operator queries.
- ✅ Allows continuity store to stay focused on lineage and attempt sequencing.
- ❌ Requires careful interface design and mapping between session ids and A-leg ids.
- ❌ More files and concepts to introduce.
- ❌ Higher short-term integration effort.

### Option C: Hybrid Approach

**When to consider**: Preserve existing continuity store but add a secure-session layer on top.

**Combination strategy**
- Keep `b2bua.Store` responsible for A-leg/B-leg allocation and attempt lineage.
- Add a new secure-session record keyed by a proxy-issued session id and linked to current A-leg continuity.
- Use runtime preparation to resolve secure session authority first, then continuity, then execution context.
- Reuse traffic/diag/query patterns for audit and operator reads.

**Phased implementation path**
- Phase 1: introduce secure session authority, owner binding, and resume window checks.
- Phase 2: add durable workspace/policy metadata and operator session summaries.
- Phase 3: add durable transcript, usage accounting, and audit serialization/redaction behaviors.

**Trade-offs**
- ✅ Best alignment with current brownfield assets and new requirements.
- ✅ Reduces risk of bloating continuity rows beyond their current responsibility.
- ✅ Supports incremental delivery and migrations.
- ❌ Requires disciplined coordination across two related persistence models.
- ❌ Design needs to define authoritative linkage and failure semantics carefully.

## Research Needed for Design Phase

1. Should the secure session id be the A-leg id, a separate mapped id, or a signed resume token referencing a stored session row?
2. What minimum principal identity shape is stable enough for durable owner binding across auth providers?
3. How should session last activity be updated on streaming downstream events without inflating hot-path persistence cost?
4. Should transcript persistence store canonical calls/events, a normalized turn model, or references into traffic capture artifacts?
5. What redaction boundary applies between general operator diagnostics, compliance audit access, and raw capture?
6. How should session-denial errors map into each supported frontend protocol without leaking session existence?
7. Should memory-store mode support all secure-session semantics or only a reduced non-durable subset with explicit operator warnings?

## Effort and Risk

- **Effort:** L — The feature spans identity, persistence, runtime gating, diagnostics, transcript/accounting, and protocol error mapping.
- **Risk:** High — The main risks are security regressions, ambiguous authority between client hints and server-owned session identity, and cross-cutting changes to hot request paths.

## Recommendations for Design Phase

- Preferred direction to evaluate first: **Option C (Hybrid Approach)**.
- Key decisions to resolve in design:
  - authoritative session identifier model
  - owner-binding persistence shape
  - separation of secure-session state from B2BUA continuity rows
  - transcript/accounting/audit storage granularity
  - operator query authorization model
- Preserve current strengths:
  - core-owned continuity/orchestration
  - existing attempt lineage
  - protocol-specific error rendering at the frontend edge
  - explicit seams for query/read adapters and optional feature plugins
- Apply hexagonal constraints during implementation:
  - define secure-session ports in the app package that consumes them
  - keep SQL, HTTP, SDK, plugin, and diagnostics types out of secure-session domain/app contracts
  - let runtime/frontend/diagnostics translate into app commands instead of owning policy

---

# Design Discovery Addendum: secure-session-management

## Summary
- **Feature**: `secure-session-management`
- **Discovery Scope**: Complex Integration / Extension
- **Key Findings**:
  - The hybrid approach remains the best fit: a new secure-session layer should own user-bound resume authority while existing B2BUA stores continue to own attempt lineage.
  - Runtime gating must happen before backend attempts and before client-supplied A-leg or continuity identifiers are trusted for resume.
  - Standard-library cryptography is sufficient for session ID generation, resume-token fingerprints, and constant-time secret comparison.

## Research Log

### Existing Runtime and Store Integration
- **Context**: Design needed concrete integration points for secure session authorization.
- **Sources Consulted**: `internal/core/runtime/executor.go`, `internal/core/runtime/executor_prepare.go`, `internal/core/runtime/attempt_stream.go`, `internal/core/b2bua/store.go`, `internal/core/continuity/manager.go`, `internal/infra/runtimebundle/build.go`.
- **Findings**:
  - `Executor.Execute` delegates pre-backend setup to `prepareSubmitAndALeg` before route planning and B-leg opening.
  - B2BUA A-leg resolution currently accepts `ALegID` or `ContinuityKey` before any secure owner check.
  - `retryRecvStream.Recv` is the correct place to record post-hook, client-visible stream events and usage deltas.
  - Runtime bundle wiring already constructs stores and executor dependencies explicitly.
- **Implications**:
  - Add a secure-session manager to executor dependencies.
  - Move or wrap A-leg resolution behind secure-session authorization when secure sessions are enabled.
  - Keep B2BUA store unchanged for attempt sequencing and add secure-session linkage around it.

### Session Security Practices
- **Context**: Requirements demand fixation resistance, concurrent-safe uniqueness, and user-bound session authority.
- **Sources Consulted**: OWASP session-management guidance, NIST high-entropy secret principles, Go standard library crypto packages.
- **Findings**:
  - Server-side session stores with random bearer tokens are preferred for revocation and owner binding.
  - Session identifiers and resume tokens must come from `crypto/rand`, not timestamps or `math/rand`.
  - Store token fingerprints, not raw bearer tokens; use HMAC-SHA-256 with a deployment secret for fingerprints.
  - Unknown, invalid, expired, and wrong-owner tokens need non-enumerating public errors.
- **Implications**:
  - Design uses a generated session ID plus separate resume token/fingerprint.
  - First-message digest and trusted agent identity can be mixed into generator material, but uniqueness does not depend on them.
  - Frontends must redact raw resume tokens from logs, backend payloads, and traffic capture by default.

### Diagnostics and Operator Visibility
- **Context**: Requirements include summaries, transcript access, audit visibility, and non-enumerating denials.
- **Sources Consulted**: `internal/core/diag/attempts.go`, `internal/core/diag/auth.go`, `internal/stdhttp/server.go`.
- **Findings**:
  - Diagnostics are already mounted centrally and can be protected with a shared secret.
  - Existing attempt diagnostics are B2BUA-centric and do not enforce session owner/redaction semantics.
- **Implications**:
  - Add session-specific query handlers with the same protection wrapper initially.
  - All transcript/audit reads must pass through secure-session redaction and authorization rules.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| Extend B2BUA store | Put owner, workspace, transcript, and usage directly on A-leg storage | Fewest new components | Overloads B2BUA, blurs continuity vs ownership authority | Not preferred |
| Separate secure-session subsystem | New core session domain with its own store and manager | Cleanest security boundary | More integration work and two linked stores | Viable |
| Hybrid secure-session over B2BUA | New secure session owns user/session state and links to B2BUA A-leg | Preserves B2BUA and adds clear authority boundary | Requires careful linkage and transaction design | Selected for design |

## Design Decisions

### Decision: Separate Authoritative Session Identity from B2BUA Continuity
- **Context**: Requirements state continuity identifiers must remain traceable but not become ownership proof.
- **Alternatives Considered**:
  1. Use A-leg ID as session ID.
  2. Create a distinct secure session ID linked to A-leg ID.
- **Selected Approach**: Create a distinct secure session record with a proxy-owned session ID and stored A-leg binding.
- **Rationale**: Keeps B2BUA lineage semantics stable while preventing guessed or leaked A-leg IDs from authorizing resume.
- **Trade-offs**: Adds a mapping to maintain, but reduces security ambiguity.
- **Follow-up**: Verify diagnostic lookups by A-leg always apply secure-session authorization/redaction.

### Decision: Store Resume Token Fingerprints Only
- **Context**: Resume tokens are bearer secrets.
- **Alternatives Considered**:
  1. Store raw resume tokens.
  2. Store HMAC fingerprints of resume tokens.
- **Selected Approach**: Store only HMAC-SHA-256 fingerprints keyed by deployment secret.
- **Rationale**: Reduces blast radius of persistence or log exposure and uses standard library crypto.
- **Trade-offs**: Requires key management through configuration; lost key invalidates existing tokens.
- **Follow-up**: Design config validation for required token key when durable secure sessions are enabled.

### Decision: Record Client-Visible Stream Events After Hooks
- **Context**: Transcript/audit must match user-visible behavior and preserve tool/event order.
- **Alternatives Considered**:
  1. Record raw backend events before hooks.
  2. Record post-hook events returned to frontend encoders.
- **Selected Approach**: Record post-hook events in `retryRecvStream.Recv` after response/tool hooks.
- **Rationale**: The session transcript should reflect what the user saw, while raw backend capture can remain an audit artifact when configured.
- **Trade-offs**: Debugging raw provider behavior relies on separate traffic/audit capture.
- **Follow-up**: Ensure usage accounting still records provider usage deltas even if redaction changes transcript payloads.

## Risks & Mitigations
- Security regression from trusting old `ContinuityKey` semantics — mitigate by gating resume through secure-session manager before B2BUA resolution when enabled.
- Hot-path persistence overhead on streaming events — mitigate with bounded batching/coalescing for last-activity updates and paged transcript storage.
- Token/key misconfiguration — mitigate with startup validation and clear operator diagnostics.
- Cross-protocol inconsistency — mitigate with shared canonical session error categories and frontend-specific tests.
- Transcript/audit diagnostics exposing user data through a shared-secret-only path — mitigate with an explicit operator-authorization seam, redaction-by-default behavior, and non-enumerating denials for content endpoints.
- Mandatory recorder failures after visible output conflicting with no-silent-replacement semantics — mitigate by validating mandatory prerequisites before backend open where possible and surfacing post-output failures as committed-attempt terminal failures.

## References
- OWASP Session Management Cheat Sheet — session ID entropy, fixation prevention, and cookie/token handling guidance.
- NIST SP 800-63B — high-entropy secret handling principles.
- Go `crypto/rand`, `crypto/hmac`, `crypto/sha256`, `crypto/subtle` package documentation — standard-library primitives used by design.
