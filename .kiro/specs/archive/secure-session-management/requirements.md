# Requirements Document

## Introduction
Secure session management gives proxy operators a reliable way to preserve multi-turn LLM interaction state while preventing cross-user data exposure and session takeover. The feature defines user-visible and operator-visible behavior for server-owned session identity, authenticated user binding, B2BUA-aware continuity, resume limits, durable usage accounting, auditable session serialization, workspace association, and per-session policy metadata.

## Boundary Context (Optional)
- **In scope**: server-owned session identity, authenticated user isolation, resume authorization, inactivity-based resume limits, B2BUA attempt correlation, usage and cost accounting records, auditable transcript serialization, workspace association, policy-derived per-session treatment flags, and operator-visible session diagnostics.
- **Out of scope**: defining a new user authentication mechanism, provider-specific conversation history formats, billing-rate calculation logic, and implementation choices for persistence technology.
- **Adjacent expectations**: frontend protocols must receive legal protocol-specific errors, configured auth must provide a trustworthy user identity before resumable session access is allowed, configured storage must be treated as the source of durable session, usage, and audit evidence when durability is enabled, B2BUA continuity identifiers must remain traceable without becoming client-authoritative session ownership proofs, and headers or metadata received from clients or backends may support correlation but must not become authoritative session identity.

## Requirements

### Requirement 1: Server-Owned Session Identity
**Objective:** As a proxy operator, I want the proxy to own session identity, so that clients cannot choose or fixate durable session identifiers.

#### Acceptance Criteria
1. When a request starts a new resumable session, the LLM Interactive Proxy shall assign a globally unique opaque session identifier.
2. When a client supplies a client-side conversation identifier, the LLM Interactive Proxy shall treat it as a hint or correlation value and not as proof of session ownership.
3. If a client attempts to create or replace the authoritative session identifier, the LLM Interactive Proxy shall reject or ignore the client-supplied authority without attaching the request to another session.
4. The LLM Interactive Proxy shall expose the authoritative session identifier only through proxy-controlled response metadata or configured operator-visible records.
5. When multiple sessions are created concurrently for the same user or for different users, the LLM Interactive Proxy shall assign a distinct authoritative session identifier to each session.
6. If multiple sessions are created within the same clock-resolution window, then the LLM Interactive Proxy shall still assign distinct authoritative session identifiers.
7. The LLM Interactive Proxy shall not rely solely on timestamp precision to guarantee authoritative session identifier uniqueness.
8. The LLM Interactive Proxy shall ensure that two active sessions never share the same authoritative session identifier.
9. The LLM Interactive Proxy shall keep B2BUA continuity identifiers distinguishable from client-supplied session hints and from the authoritative session ownership proof.
10. When a frontend client, backend provider, or remote LLM supplies a session identifier, header, or metadata field, the LLM Interactive Proxy shall treat that value as correlation metadata only and not as authoritative session identity.
11. The LLM Interactive Proxy shall derive authoritative session identity and in-session handling from proxy-owned session state rather than from headers or metadata supplied by clients, backends, or remote LLMs.

### Requirement 2: User Binding and Isolation
**Objective:** As an end user, I want my session contents isolated from other users, so that another user cannot view or resume my conversation.

#### Acceptance Criteria
1. When a session is created, the LLM Interactive Proxy shall bind the session to the authenticated user identity for the request.
2. When a user attempts to resume a session, the LLM Interactive Proxy shall verify that the session belongs to that authenticated user before accepting the request.
3. If the authenticated user does not match the session owner, then the LLM Interactive Proxy shall reject the resume attempt without disclosing session contents.
4. If no trustworthy authenticated user identity is available and the request attempts to resume durable session state, then the LLM Interactive Proxy shall reject the resume attempt.
5. While processing a session, the LLM Interactive Proxy shall prevent session-scoped data, usage records, audit records, and policy metadata from being visible to other users.
6. When session ownership is persisted, the LLM Interactive Proxy shall persist enough owner identity information to enforce the same ownership decision after proxy restart.
7. If the persisted owner identity cannot be validated during resume, then the LLM Interactive Proxy shall reject the resume attempt rather than creating a new owner binding for existing session contents.

### Requirement 3: Fixation and Forgery Resistance
**Objective:** As a security operator, I want session references to resist fixation and forged headers, so that attackers cannot impersonate users or attach themselves to existing sessions.

#### Acceptance Criteria
1. When a request carries session-related headers or protocol fields, the LLM Interactive Proxy shall validate them against proxy-issued session authority before using them for resume.
2. If a session reference is malformed, expired, unrecognized, or not issued by the proxy, then the LLM Interactive Proxy shall reject the resume attempt with an informative non-sensitive error.
3. If a request carries forged user identity headers that are not trusted by configured authentication policy, then the LLM Interactive Proxy shall not use those headers to bind or resume a session.
4. The LLM Interactive Proxy shall prevent a newly created session from reusing an attacker-provided identifier as its authoritative session identifier.
5. The LLM Interactive Proxy shall record rejected fixation or forgery attempts in operator-visible security evidence without recording sensitive request contents unless configured policy allows it.
6. If a client knows or guesses a B2BUA continuity identifier without valid session authority, then the LLM Interactive Proxy shall reject the resume attempt without attaching the request to that continuity context.
7. When session authority is exposed to a client for future resume, the LLM Interactive Proxy shall ensure that the authority is bound to the authenticated user and cannot be transferred to another user without rejection.
8. If a backend response carries a session identifier or session-like header, then the LLM Interactive Proxy shall not use that value to create, replace, resume, or rebind the authoritative proxy session.
9. The LLM Interactive Proxy shall not forward backend-supplied session identifiers into proxy-owned session state unless an explicit non-authoritative correlation field is configured for audit or diagnostics.

### Requirement 4: Session Transcript Semantics
**Objective:** As a proxy operator, I want sessions to represent the full interleaved LLM interaction, so that replay, auditing, and policy decisions have complete context.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall represent a session as an ordered set of user messages, remote LLM responses, tool calls, and tool call responses.
2. When a session turn includes multiple content types or tool events, the LLM Interactive Proxy shall preserve their relative order in the session record.
3. When a backend emits assistant output, tool call events, usage events, warnings, or terminal errors, the LLM Interactive Proxy shall associate the events with the active session and turn.
4. If a turn fails before any client-visible output is produced, then the LLM Interactive Proxy shall keep enough evidence to explain the failure and any replacement attempt.
5. If a turn fails after client-visible output is produced, then the LLM Interactive Proxy shall record the surfaced failure as part of the session transcript.
6. When a session transcript is serialized, the LLM Interactive Proxy shall include enough turn and event ordering information to reconstruct the user-visible sequence and related tool interactions.
7. If transcript capture is not enabled for a session, then the LLM Interactive Proxy shall make that absence explicit in operator-visible session metadata.

### Requirement 5: B2BUA-Aware Session Continuity
**Objective:** As a proxy operator, I want session records to include B2BUA attempt lineage, so that one client leg can be traced across multiple backend legs.

#### Acceptance Criteria
1. When one client-visible turn opens a backend attempt, the LLM Interactive Proxy shall associate that backend leg with the session and client leg.
2. When pre-output recovery opens a replacement backend attempt, the LLM Interactive Proxy shall record each backend leg under the same session and client leg.
3. When a backend attempt is swallowed or surfaced, the LLM Interactive Proxy shall record the outcome and reason in session lineage.
4. If client-visible output has already begun for a backend leg, then the LLM Interactive Proxy shall not silently replace that leg with another backend leg for the same turn.
5. The LLM Interactive Proxy shall make session lineage sufficient for operators to identify which backend attempt produced surfaced output.
6. When session state is serialized or summarized, the LLM Interactive Proxy shall include the lineage relationship between the authoritative session, client-visible turn, A-leg, and B-leg attempts.

### Requirement 6: Per-Attempt Backend, Model, Status, and Accounting Traceability
**Objective:** As a proxy operator, I want each backend attempt within a session turn to carry backend, model, settings, status, and usage evidence, so that audit, debugging, and billing remain accurate even when attempts fail or are swallowed.

#### Acceptance Criteria
1. When a session turn opens a backend attempt, the LLM Interactive Proxy shall record the requested model or alias, resolved backend, resolved model, route source, route reason, A-leg identifier, B-leg identifier, and attempt sequence for that attempt.
2. When a user changes the requested model or model-affecting options between session turns, the LLM Interactive Proxy shall record the changed request metadata on the affected turn and attempts without rewriting prior attempt records.
3. When dynamic routing, weighted routing, failover, or alias resolution selects a backend or model, the LLM Interactive Proxy shall record the resolved backend/model and routing decision metadata separately for each backend attempt.
4. When a backend attempt uses execution settings, the LLM Interactive Proxy shall record a safe snapshot of settings that affect execution behavior, including known values such as temperature, max tokens, timeout, reasoning effort, tool settings, streaming mode, and backend-specific option summaries.
5. When a backend attempt succeeds, fails, times out, is swallowed, or is surfaced to the user, the LLM Interactive Proxy shall record both a binary success or failure state and a detailed status containing available HTTP status, provider status, error category, timeout classification, and debug-safe reason.
6. When a backend attempt emits usage, billing, cost, or cache metadata, the LLM Interactive Proxy shall associate that data with the specific backend attempt even if the attempt does not produce the final user-visible response.
7. If a backend attempt fails or is swallowed after the proxy submitted work to the remote LLM, then the LLM Interactive Proxy shall preserve any known usage, billing, cost, cache, status, and settings metadata for operator accounting and audit.
8. The LLM Interactive Proxy shall keep protocol/user-visible usage semantics separate from operator and billing accounting, so surfaced response usage can remain protocol-compatible while operator accounting includes every submitted backend attempt.
9. If a provider does not supply usage, billing, cache, HTTP status, provider status, or setting metadata for an attempt, then the LLM Interactive Proxy shall mark the missing fields as unavailable rather than inventing values.

### Requirement 7: Resume Window and Last Activity
**Objective:** As a proxy operator, I want sessions to expire for resume after inactivity, so that stale sessions cannot be resumed indefinitely.

#### Acceptance Criteria
1. Where a maximum resume time is configured for a session, the LLM Interactive Proxy shall calculate resume eligibility relative to the session's last activity time.
2. When the client sends a session request, the LLM Interactive Proxy shall update the session last activity time after accepting the request.
3. When the remote LLM sends a response event for a session, the LLM Interactive Proxy shall update the session last activity time.
4. If a user attempts to resume a session after the maximum allowed resume time has elapsed, then the LLM Interactive Proxy shall reject the request with a clear message that the session can no longer be resumed.
5. If no maximum resume time applies to a session, then the LLM Interactive Proxy shall not reject resume solely because of inactivity age.
6. When a session is rejected because its resume window expired, the LLM Interactive Proxy shall not create a replacement session that inherits the expired session contents.
7. Where durable session storage is enabled, the LLM Interactive Proxy shall preserve last activity time across restarts for resume-window enforcement.

### Requirement 8: Durable Session State and Restart Survival
**Objective:** As a proxy operator, I want durable sessions to survive proxy restarts, so that session continuity and evidence are not lost unexpectedly.

#### Acceptance Criteria
1. Where durable session storage is enabled, the LLM Interactive Proxy shall persist session identity, owner binding, last activity time, workspace association, policy metadata, transcript state, lineage, usage records, and audit references.
2. When the proxy restarts, the LLM Interactive Proxy shall restore durable sessions sufficiently to enforce user binding, resume windows, policy metadata, and lineage visibility.
3. If required durable session state is unavailable during resume, then the LLM Interactive Proxy shall reject the resume attempt instead of creating ambiguous access to prior session contents.
4. Where only non-durable session storage is configured, the LLM Interactive Proxy shall make the non-durable behavior visible to operators.
5. The LLM Interactive Proxy shall avoid reporting a session as resumable unless enough state is available to enforce its security and resume rules.
6. When durable storage is enabled, the LLM Interactive Proxy shall persist enough data to distinguish client-supplied session hints from proxy-owned session authority after restart.
7. If durable storage contains continuity lineage but lacks required secure-session ownership state, then the LLM Interactive Proxy shall not allow user-facing resume of that session.

### Requirement 9: Usage Accounting
**Objective:** As a proxy operator, I want session usage accounting, so that token consumption and billing evidence can be reported per session and user.

#### Acceptance Criteria
1. When a session turn is processed, the LLM Interactive Proxy shall associate inbound token usage, outbound token usage, cached output token usage, and available billing or cost metadata with the session.
2. When usage data is emitted by a backend, the LLM Interactive Proxy shall preserve the usage data in session accounting without changing the user-visible response semantics.
3. If a provider does not supply a usage or billing field, then the LLM Interactive Proxy shall mark that field as unavailable rather than inventing a value.
4. Where durable accounting is enabled, the LLM Interactive Proxy shall persist usage accounting so that it survives proxy restarts.
5. The LLM Interactive Proxy shall support operator-visible usage summaries by session, user, workspace, and backend attempt where the relevant dimensions are known.
6. The LLM Interactive Proxy shall derive session, user, and workspace usage totals as rollups from per-attempt accounting records rather than treating only the final surfaced response as the accounting source of truth.

### Requirement 10: Auditing and Session Serialization
**Objective:** As a compliance operator, I want sessions to be serializable for auditing, so that session contents and proxy treatment can be reviewed after execution.

#### Acceptance Criteria
1. When audit capture is enabled for a session, the LLM Interactive Proxy shall produce an ordered serializable audit record for session contents and related proxy decisions.
2. Where configured audit storage is enabled, the LLM Interactive Proxy shall persist session audit records through the configured storage path.
3. When redaction is enabled for a session, the LLM Interactive Proxy shall apply the configured redaction treatment before exposing general audit records.
4. Where full logging is enabled for a session, the LLM Interactive Proxy shall make the increased capture level explicit in operator-visible metadata.
5. If audit capture fails for a session where audit capture is mandatory, then the LLM Interactive Proxy shall reject or stop processing according to configured policy and surface an informative operator-visible reason.
6. When audit records include B2BUA recovery, the LLM Interactive Proxy shall identify swallowed and surfaced attempts without requiring raw backend payload access.
7. If audit records contain raw or sensitive payloads, then the LLM Interactive Proxy shall restrict those records to explicitly authorized audit access.
8. When audit records include backend attempts, the LLM Interactive Proxy shall include per-attempt backend/model, routing decision, execution settings, status, and accounting metadata subject to redaction and authorization policy.

### Requirement 11: Workspace Association
**Objective:** As a workspace user, I want sessions associated with workspaces, so that session history and policy can follow the correct project context.

#### Acceptance Criteria
1. When workspace context is available for a new session, the LLM Interactive Proxy shall associate the session with that workspace.
2. When a user resumes a session, the LLM Interactive Proxy shall verify the request workspace against the session workspace according to configured policy.
3. If the request workspace is not permitted for the session, then the LLM Interactive Proxy shall reject the resume attempt without exposing session contents.
4. Where a session has no workspace association, the LLM Interactive Proxy shall make that absence explicit in operator-visible session metadata.
5. The LLM Interactive Proxy shall support operator-visible session lookup or summaries by workspace where workspace information is known.
6. If workspace resolution fails for a request whose session policy requires workspace verification, then the LLM Interactive Proxy shall reject the resume attempt rather than fail open.
7. When durable session storage is enabled, the LLM Interactive Proxy shall preserve workspace association across proxy restarts.

### Requirement 12: Per-Session Policy Metadata
**Objective:** As a proxy operator, I want sessions to carry policy-derived treatment metadata, so that routing, logging, redaction, and advanced controls are consistent across turns.

#### Acceptance Criteria
1. When a session is created, the LLM Interactive Proxy shall attach configured or policy-derived treatment metadata to the session.
2. When a session is resumed, the LLM Interactive Proxy shall apply the stored treatment metadata before routing or exposing session contents.
3. If a client attempts to override protected treatment metadata without authorization, then the LLM Interactive Proxy shall reject or ignore the override according to configured policy.
4. Where per-session routing settings are present, the LLM Interactive Proxy shall apply them consistently across eligible turns while preserving capability and failure rules.
5. Where per-session redaction, full logging, or advanced proxy settings are present, the LLM Interactive Proxy shall make the active treatment visible to authorized operators.
6. When a per-session treatment setting is security-critical, the LLM Interactive Proxy shall fail closed if the setting cannot be loaded or validated during resume.
7. If session treatment metadata conflicts with current global safety policy, then the LLM Interactive Proxy shall apply the more restrictive effective treatment.

### Requirement 13: Protocol-Neutral User Feedback
**Objective:** As an API client user, I want session errors to be clear and legal for my frontend protocol, so that clients can react predictably.

#### Acceptance Criteria
1. When a session request is rejected, the LLM Interactive Proxy shall return an error in the legal shape for the active frontend protocol.
2. If a session is expired, not found, not owned by the user, or not resumable, then the LLM Interactive Proxy shall provide a clear non-sensitive reason.
3. If a rejection is caused by security policy, then the LLM Interactive Proxy shall avoid revealing whether another user's session exists.
4. When a new session is created successfully, the LLM Interactive Proxy shall provide enough protocol-appropriate metadata for authorized future resume where resume is allowed.
5. The LLM Interactive Proxy shall keep session error handling consistent across supported frontend protocols.
6. If a session denial happens before backend execution starts, then the LLM Interactive Proxy shall not open a backend attempt for that request.
7. When a session denial is returned, the LLM Interactive Proxy shall record the denial category for authorized operator diagnostics.

### Requirement 14: Operator Visibility and Controls
**Objective:** As a proxy operator, I want controlled visibility into session state, so that I can diagnose incidents without violating user isolation.

#### Acceptance Criteria
1. The LLM Interactive Proxy shall provide authorized operators with session summaries that include session identifier, owner identity, workspace, last activity time, resume eligibility, policy metadata, usage totals, and lineage summary where available.
2. When an operator requests a session transcript or audit record, the LLM Interactive Proxy shall enforce authorization and configured redaction before returning session contents.
3. If an operator is not authorized for a session, then the LLM Interactive Proxy shall deny access without exposing session contents.
4. Where session retention or resume policy changes, the LLM Interactive Proxy shall make the effective policy visible to authorized operators.
5. The LLM Interactive Proxy shall make security-relevant session events, including rejected resumes and owner mismatches, available for operator diagnostics.
6. When an operator requests session details by B2BUA continuity identifier, the LLM Interactive Proxy shall apply the same session authorization and redaction rules as requests by authoritative session identifier.
7. If a session lookup would reveal another user's session existence to an unauthorized requester, then the LLM Interactive Proxy shall return a non-enumerating denial.
8. The LLM Interactive Proxy shall provide authorized operators with per-attempt summaries that include attempted backend, attempted model, requested model or alias, route source, outcome, status, timing, settings summary, usage, billing, cache, and whether the attempt was surfaced or swallowed where available.
