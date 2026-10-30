# Implementation Plan

- [x] 1. Establish canonical session contracts and configuration

- [x] 1.1 Extend canonical session references and session-denial errors
  - Add proxy-owned `SessionID` and raw `ResumeToken` fields to `pkg/lipapi.SessionRef` while preserving `ClientSessionID`, `ContinuityKey`, and `ALegID` as hints/correlation fields.
  - Add typed canonical session-denial errors for missing principal, invalid authority, owner mismatch, expired resume, workspace denial, policy unavailable, storage unavailable, and mandatory audit failure.
  - Document that session-like values from clients, backends, or remote LLMs are correlation hints only unless validated by proxy-owned secure-session state.
  - Done when unit tests prove existing call validation remains compatible and new errors expose public-safe codes without raw token data.
  - _Requirements: 1.2, 1.3, 1.4, 1.9, 1.10, 1.11, 3.1, 3.2, 12.1, 12.2, 12.3, 12.7_
  - _Boundary: Canonical contracts_

- [x] 1.2 Add secure-session configuration and validation
  - Add typed secure-session config for enablement, store mode, resume window, token fingerprint key, audit durability, redaction defaults, diagnostics paths, and non-durable warning behavior.
  - Add table-driven validation tests for invalid durable config, missing token key, invalid resume window, incompatible audit settings, and disabled secure-session defaults.
  - Done when config loading fails fast for unsafe secure-session settings and sample config documents non-durable vs durable behavior.
  - _Requirements: 6.1, 6.5, 7.4, 11.1, 11.6, 13.4_
  - _Boundary: Config_
  - _Depends: 1.1_

- [x] 1.3 Update SDK session views for authoritative session state (P)
  - Expose authoritative session id, client hint, A-leg id, workspace id, resume eligibility, and active treatment labels in SDK-visible session views without exposing raw resume tokens.
  - Update existing view tests and session opener fixtures to assert `SessionID` comes from authoritative session state when available.
  - Done when plugins can observe session metadata needed for policy diagnostics while raw resume authority remains unavailable.
  - _Requirements: 1.4, 4.7, 10.4, 11.5_
  - _Boundary: SDK views_
  - _Depends: 1.1_

- [x] 2. Build secure-session core types, crypto, and error mapping

- [x] 2.1 Define secure-session domain types
  - Create `internal/core/securesession/domain` types for session ids, token fingerprints, principals, workspaces, client hints, policy metadata, usage totals, transcript items, audit items, turn ids, activity sources, summaries, and read/query options.
  - Keep domain types pure: no imports from runtime, HTTP, SQL/SQLite, diagnostics, frontend/backend plugins, or SDK packages.
  - Include typed fields for transcript-enabled status, effective treatment, stricter-policy resolution, resume eligibility, B2BUA A-leg binding, attempt trace, attempt settings, attempt outcome, and per-attempt accounting metadata.
  - Done when package-local tests can construct records and summaries without using `any` or untyped maps for core invariants.
  - _Requirements: 1.10, 2.6, 4.1, 4.6, 4.7, 5.6, 6.1, 6.4, 6.5, 6.9, 7.1, 8.1, 9.1, 10.1, 11.1, 12.1, 12.7, 14.1, 14.8_
  - _Boundary: SecureSession domain_
  - _Depends: 1.1_

- [x] 2.2 Implement concurrent-safe ID and token generation
  - Implement `crypto/rand`-backed session id and resume token generation plus HMAC-SHA-256 token fingerprints keyed by configured secret material.
  - Mix trusted entropy material such as principal id, trusted agent identity digest, and first-message digest only as domain-separation input; uniqueness must not depend on timestamps or message content.
  - Done when high-concurrency tests create many sessions in the same clock-resolution window with distinct ids and no raw tokens persisted in generated records.
  - _Requirements: 1.1, 1.5, 1.6, 1.7, 1.8, 3.4, 3.7_
  - _Boundary: SecureSession crypto_
  - _Depends: 2.1_

- [x] 2.3 Implement session error mapping helpers (P)
  - Map internal secure-session errors to canonical `lipapi` session-denial categories with public-safe messages and internal diagnostic reasons.
  - Add tests proving unknown, invalid, and wrong-owner cases share non-enumerating public behavior while preserving internal denial categories.
  - Done when runtime and frontend packages can classify secure-session failures without string matching.
  - _Requirements: 3.2, 3.5, 12.1, 12.2, 12.3, 12.5, 12.7, 13.5, 13.7_
  - _Boundary: SecureSession errors_
  - _Depends: 1.1, 2.1_

- [x] 3. Implement secure-session store contracts and memory store

- [x] 3.1 Define the secure-session store interface and contract tests
  - Add the consumer-owned store interface in `internal/core/securesession/app`, covering create, load by id, load by resume fingerprint, load by A-leg id, touch activity, append/update attempt trace, append transcript, add per-attempt usage, append audit, summaries, transcript reads, audit reads, and store health primitives used by manager-owned readiness checks.
  - Keep the interface business-shaped and domain/app typed; do not expose `database/sql`, SQLite driver, HTTP, diagnostics, provider SDK, or transport types through the port.
  - Add reusable contract tests for uniqueness, owner/workspace persistence, A-leg lookup, B-leg attempt lookup, transcript-disabled metadata, usage summaries by session/user/workspace/backend attempt, attempt status summaries, and non-enumerating missing lookups.
  - Done when memory and future SQLite implementations can run the same contract test suite.
  - _Requirements: 2.5, 2.6, 4.6, 4.7, 5.6, 6.1, 6.5, 6.6, 6.7, 6.8, 6.9, 7.7, 8.1, 8.3, 9.4, 9.5, 9.6, 10.5, 10.7, 11.5, 11.7, 14.1, 14.6, 14.8_
  - _Boundary: SecureSession app store port_
  - _Depends: 2.1_

- [x] 3.2 Implement the concurrent in-memory secure-session store
  - Implement race-safe storage in `internal/core/securesession/adapters/memory` for records, token fingerprints, A-leg index, turns, attempt traces, transcripts, usage records, audit records, summaries, and readiness state.
  - Return a concrete adapter type from the package constructor; do not define an adapter-side interface just because the adapter implements the app-owned port.
  - Preserve explicit empty slices for returned summaries/transcripts/audits and avoid returning `nil` lists in JSON-facing query results.
  - Done when the store contract suite passes against the in-memory implementation and `go test -race` passes for the package.
  - _Requirements: 1.8, 2.5, 4.6, 6.6, 6.7, 6.8, 7.7, 8.4, 9.2, 9.5, 10.5, 14.1, 14.8_
  - _Boundary: SecureSession memory store adapter_
  - _Depends: 3.1_

- [x] 3.3 Add store fakes for manager and runtime tests (P)
  - Provide small test fakes for secure-session store failures, mandatory readiness failures, post-output recorder failures, and B2BUA store interactions.
  - Done when manager/runtime tests can force each denial and recorder failure path deterministically without real SQLite.
  - _Requirements: 7.3, 9.5, 12.6_
  - _Boundary: Test infrastructure_
  - _Depends: 3.1_

- [x] 4. Implement durable SQLite secure-session storage

- [x] 4.1 Add SQLite schema and migrations for secure sessions
  - Create secure-session tables in `internal/core/securesession/adapters/sqlite` for sessions, turns, attempt traces, transcript, usage, and audit records with unique constraints on session id and token fingerprint plus indexes for owner, workspace, A-leg, B-leg, resolved backend/model, and resume lookup.
  - Add migration tests for fresh database creation and idempotent reopen behavior.
  - Done when opening an empty SQLite database creates all secure-session tables and indexes without disturbing existing continuity tables.
  - _Requirements: 2.6, 6.1, 6.3, 6.5, 6.6, 6.7, 6.8, 7.7, 8.1, 8.2, 8.6, 9.4, 10.2, 11.7_
  - _Boundary: SecureSession SQLite store adapter_
  - _Depends: 3.1_

- [x] 4.2 Implement SQLite CRUD, query, and readiness operations
  - Implement create/load/touch/attempt trace/transcript/usage/audit/summary/readiness behavior against the SQLite schema using context-aware database calls and explicit transaction boundaries.
  - Keep SQL and transaction handles inside the SQLite adapter; app/domain code must see only the app-owned store port and domain values.
  - Support A-leg lookup for diagnostics and resume lineage without allowing A-leg alone to authorize user-facing resume.
  - Done when the shared store contract suite passes against SQLite.
  - _Requirements: 2.6, 2.7, 5.6, 6.1, 6.5, 6.6, 6.7, 6.8, 6.9, 7.7, 8.2, 8.3, 9.4, 9.5, 10.2, 14.1, 14.6, 14.8_
  - _Boundary: SecureSession SQLite store adapter_
  - _Depends: 4.1_

- [x] 4.3 Add SQLite restart and concurrent update tests
  - Verify restart restores owner binding, last activity, workspace, policy metadata, token fingerprints, lineage binding, attempt traces, transcript metadata, and usage summaries.
  - Verify concurrent session creation and concurrent touch/update flows keep unique ids and consistent last-activity state.
  - Done when package tests cover restart survival and concurrent writes without race or uniqueness failures.
  - _Requirements: 1.5, 1.6, 2.6, 6.7, 7.2, 7.5, 10.7_
  - _Boundary: SecureSession SQLite store adapter_
  - _Depends: 4.2_

- [x] 5. Implement secure-session manager policy

- [x] 5.1 Implement new-session creation with B2BUA A-leg binding
  - Create sessions with authoritative session id, resume token fingerprint, owner binding, workspace binding, policy metadata, first turn id, and a B2BUA A-leg created through an app-owned lineage port implemented by the existing store seam.
  - Add manager tests proving client-supplied ids/hints do not become authoritative and new sessions receive distinct IDs under concurrent creation.
  - Done when `BeginTurn` returns a session context containing a proxy-owned session id, raw resume token response metadata, and stored A-leg binding.
  - _Requirements: 1.1, 1.2, 1.3, 1.5, 1.8, 2.1, 3.4, 5.1, 10.1, 11.1, 12.4_
  - _Boundary: SecureSession app manager_
  - _Depends: 2.2, 3.2, 3.3_

- [x] 5.2 Implement resume validation and fixation rejection
  - Validate resume token fingerprint, authoritative session id, owner principal, resume window, persisted owner identity, and stored A-leg binding before returning session context.
  - Reject guessed `ALegID`/`ContinuityKey`, malformed tokens, transferred tokens, missing principal, owner mismatch, expired sessions, and unavailable persisted ownership without leaking other users' session existence.
  - Done when manager tests prove every invalid resume case rejects before exposing session contents or A-leg authority.
  - _Requirements: 2.2, 2.3, 2.4, 2.7, 3.1, 3.2, 3.3, 3.6, 3.7, 6.4, 6.6, 7.3, 7.7, 12.2, 12.3_
  - _Boundary: SecureSession app manager_
  - _Depends: 5.1_

- [x] 5.3 Implement workspace validation and policy metadata resolution
  - Enforce workspace match policy, fail closed when workspace resolution is required but unavailable, and preserve explicit no-workspace metadata when absent.
  - Resolve per-session treatment metadata including transcript capture status, redaction, full logging, routing hints, mandatory audit, and stricter effective treatment when session metadata conflicts with global policy.
  - Cover unbounded resume policy by proving sessions without a maximum resume window are not rejected solely because of inactivity age.
  - Done when manager tests show workspace-denied resumes reject, no-workspace sessions expose explicit metadata, and stricter treatment wins over weaker session metadata.
  - _Requirements: 6.5, 10.1, 10.2, 10.3, 10.4, 10.6, 11.1, 11.2, 11.3, 11.5, 11.6, 11.7_
  - _Boundary: SecureSession app manager_
  - _Depends: 5.2_

- [x] 5.4 Split policy metadata implementation into focused checkpoints
  - Verify policy metadata implementation is decomposed into workspace binding, redaction/full-logging/audit flags, and stricter-effective-treatment tests before coding proceeds past manager policy work.
  - Done when each policy behavior has a separate table-driven test case and no single test helper hides all policy branches.
  - _Requirements: 10.1, 10.2, 10.3, 10.4, 10.6, 11.1, 11.2, 11.5, 11.6, 11.7_
  - _Boundary: SecureSession app manager validation_
  - _Depends: 5.3_

- [x] 5.5 Implement per-session routing treatment and first-turn continuity rules
  - Apply stored per-session routing settings to eligible resumed turns while preserving capability negotiation, route planning, first-request semantics, and no-retry-after-output rules.
  - Add tests proving first-request routing is consumed once per session continuity context and resumed turns do not re-trigger first-request behavior.
  - Done when route planning receives effective session routing metadata without allowing clients to override protected treatment fields.
  - _Requirements: 5.4, 11.2, 11.3, 11.4, 12.6_
  - _Boundary: SecureSession app manager, routing driving adapter_
  - _Depends: 5.4_

- [x] 5.6 Implement mandatory readiness and turn outcome handling
  - Add pre-backend readiness checks for mandatory durable storage and audit policy, and add `FinishTurn` outcome recording for success, pre-output denial, surfaced failure, and post-output recorder failure.
  - Define a single manager-owned checklist for pre-output mandatory gates so executor and recorder do not duplicate gate ownership.
  - Done when tests show mandatory readiness failures reject before A-leg/B-leg execution and turn outcomes are recorded for success and failure paths.
  - _Requirements: 4.4, 4.5, 5.3, 6.2, 9.5, 12.6, 12.7_
  - _Boundary: SecureSession app manager_
  - _Depends: 5.5_

- [x] 6. Integrate secure-session gating into the executor

- [x] 6.1 Add executor dependencies and disabled-mode compatibility
  - Add secure-session app manager and recorder use-case dependencies to the executor; secure BeginTurn is the only prepare path (legacy continuity-only prepare was removed).
  - Done when runtime tests cover secure-session-only prepare and fakes can inject manager/recorder dependencies.
  - _Requirements: 7.4, 12.6_
  - _Boundary: Runtime driving adapter_
  - _Depends: 5.1_

- [x] 6.2 Reorder submit preparation around principal, workspace, and secure-session gate
  - Resolve trusted principal and workspace before secure-session authorization, translate them into app/domain values, then call `BeginTurn` before trusting `ALegID` or `ContinuityKey`.
  - Populate `call.Session.ALegID` from the secure-session record and strip raw resume token from backend-facing call copies.
  - Keep the dependency on the manager-owned readiness checklist so pre-output mandatory gates have one owner and are not duplicated in the executor.
  - Treat backend-provided session-like headers or metadata as provider correlation data only, never as inputs that can overwrite proxy-owned session state.
  - Done when forged continuity ids without valid session authority never reach B2BUA resolution or backend opening in secure-session mode.
  - _Requirements: 1.9, 1.10, 1.11, 2.2, 2.4, 3.1, 3.3, 3.6, 3.8, 3.9, 10.2, 10.6, 12.6_
  - _Boundary: Runtime driving adapter_
  - _Depends: 6.1, 5.6_

- [x] 6.3 Publish authoritative execution views and denial diagnostics
  - Build `execctx.Views.Session` from authoritative session state instead of `ClientSessionID`, and record session-denial categories for authorized diagnostics.
  - Add tests proving invalid session requests fail before route planning/backend open and expose only protocol-safe error categories to upstream callers.
  - Done when session views include authoritative session id, A-leg id, workspace, labels, resume eligibility, and no raw token.
  - _Requirements: 1.4, 2.5, 3.5, 4.7, 10.4, 12.1, 12.7, 13.5_
  - _Boundary: Runtime driving adapter, execution context_
  - _Depends: 6.2_

- [x] 6.4 Bind session lineage to B2BUA attempts
  - Ensure every opened B-leg attempt is associated with the validated secure session, turn id, A-leg id, and surfaced/swallowed outcome.
  - Add runtime tests for pre-output replacement attempts and post-output committed attempts under secure-session context.
  - Done when operators can identify the backend attempt that produced surfaced output for a secure session.
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 9.6_
  - _Boundary: Runtime driving adapter, B2BUA lineage adapter_
  - _Depends: 6.3_

- [x] 6.5 Capture per-attempt backend, model, route, and settings metadata
  - Snapshot requested model or alias, resolved backend/model, route source, route reason, A-leg id, B-leg id, attempt sequence, and execution settings when a backend attempt is opened.
  - Include settings that affect execution behavior such as timeout, temperature, max tokens, reasoning effort, tool summary, streaming mode, and safe backend option digest where available.
  - Done when runtime tests prove manual model changes across turns and dynamic routing/failover attempts produce distinct immutable attempt trace records.
  - _Requirements: 6.1, 6.2, 6.3, 6.4_
  - _Boundary: Runtime driving adapter, attempt trace app use case_
  - _Depends: 6.4, 3.1_

- [x] 6.6 Capture per-attempt outcome, status, and debug-safe failure details
  - Record binary success/failure, surfaced/swallowed state, HTTP status, provider status, error category, timeout classification, debug-safe reason, and end timestamp for each backend attempt.
  - Done when tests prove successful, failed, timed-out, swallowed, and surfaced attempts each have separate status records without relying on the final user-visible response alone.
  - _Requirements: 6.5, 6.7, 10.8, 14.8_
  - _Boundary: Runtime driving adapter, attempt trace app use case_
  - _Depends: 6.5_

- [x] 7. Implement transcript, usage, activity, and audit recording

- [x] 7.1 Record accepted client input and turn transcript entries
  - Append accepted user messages, tool responses, request metadata, turn id, sequence numbers, transcript-enabled status, and redaction state immediately after secure-session gate success.
  - Done when tests reconstruct accepted client input order from transcript records and sessions with transcript capture disabled expose explicit metadata.
  - _Requirements: 4.1, 4.2, 4.6, 4.7, 6.2, 9.1_
  - _Boundary: SecureSession app recorder_
  - _Depends: 6.3_

- [x] 7.2 Record post-hook stream events, usage deltas, and remote last activity
  - Record remote LLM response events after response/tool hooks, preserve relative ordering, update last activity for remote events, and aggregate usage deltas with unavailable markers for absent provider data.
  - Associate usage, billing, cost, and cache metadata with the specific B-leg attempt that incurred it, even if the attempt is failed or swallowed.
  - Done when stream tests prove recorder output matches client-visible event order and usage summaries include session/user/workspace/backend attempt dimensions where known.
  - _Requirements: 4.2, 4.3, 4.5, 6.6, 6.7, 6.8, 6.9, 7.3, 9.1, 9.2, 9.3, 9.5, 9.6_
  - _Boundary: SecureSession app recorder, runtime stream driving adapter_
  - _Depends: 7.1, 6.6_

- [x] 7.3 Split recorder stream behavior into focused checkpoints
  - Verify stream recording is decomposed into event-order preservation, last-activity touch, usage aggregation, and unavailable-metadata handling before audit work begins.
  - Ensure user-visible usage remains protocol-compatible while operator/billing rollups include every submitted backend attempt.
  - Done when each checkpoint has a focused test and the usage summary test covers session, user, workspace, and backend attempt dimensions where known.
  - _Requirements: 4.2, 4.3, 6.8, 6.9, 9.1, 9.2, 9.5, 9.6_
  - _Boundary: SecureSession app recorder validation_
  - _Depends: 7.2_

- [x] 7.4 Record provider session-like metadata only as correlation data
  - Capture backend/provider conversation ids, request ids, and session-like response headers only in explicit provider-correlation transcript or audit fields when configured.
  - Done when tests prove backend-returned session-like values appear only as non-authoritative metadata and never overwrite `SessionID`, resume token fingerprints, owner binding, workspace binding, or A-leg binding.
  - _Requirements: 1.10, 1.11, 3.8, 3.9, 4.3, 9.1_
  - _Boundary: SecureSession app recorder, backend metadata adapter boundary_
  - _Depends: 7.3_

- [x] 7.5 Implement audit serialization, redaction, and raw access restrictions
  - Serialize audit records for session contents, proxy decisions, B2BUA recovery, surfaced/swallowed attempts, redaction treatment, and full-logging metadata.
  - Include per-attempt backend/model, route decision, execution settings, status, usage, billing, cost, and cache metadata in audit records subject to redaction and authorization policy.
  - Record rejected fixation and forgery attempts as operator-visible security evidence without storing sensitive request contents unless policy explicitly allows it.
  - Restrict raw or sensitive payload audit records to explicitly authorized audit access while general audit records use configured redaction.
  - Done when audit tests show redacted general records, raw access denial by default, and explicit full-logging metadata.
  - _Requirements: 3.5, 6.1, 6.3, 6.4, 6.5, 6.6, 6.7, 10.1, 10.2, 10.3, 10.4, 10.6, 10.7, 10.8, 12.5_
  - _Boundary: SecureSession app recorder, audit adapter boundary_
  - _Depends: 7.4_

- [x] 7.6 Implement mandatory recorder failure semantics
  - Surface mandatory recorder failures according to pre-output vs post-output rules: pre-output failures reject before backend open when detected; post-output failures are committed-attempt terminal failures without silent replacement.
  - Add tests for optional failure logging, mandatory pre-output denial, mandatory post-output surfaced failure, and no inherited-session continuation after expired/failed sessions.
  - Done when mandatory recorder failures never trigger silent B-leg replacement or reuse expired session contents.
  - _Requirements: 6.6, 9.5, 12.6_
  - _Boundary: SecureSession app recorder, runtime stream driving adapter_
  - _Depends: 7.5_

- [x] 8. Implement frontend session metadata and error mapping

- [x] 8.1 Add shared frontend helpers for session carriers and canonical errors
  - Add shared helper patterns or small per-frontend utilities for reading secure session id/resume token carriers, writing response metadata, and classifying secure-session errors.
  - Done when frontend packages can map session errors without duplicating raw-token logging or inconsistent public messages.
  - _Requirements: 1.4, 3.1, 12.1, 12.2, 12.3, 12.5_
  - _Boundary: Frontend adapters_
  - _Depends: 1.1, 2.3_

- [x] 8.2 Implement OpenAI Responses frontend session support
  - Decode protocol-legal secure session metadata, preserve client-provided conversation ids as hints, and encode proxy-issued resume metadata on successful new sessions.
  - Map invalid, expired, not-owned, and not-resumable session denials into legal OpenAI Responses error shapes without leaking other users' session existence.
  - Done when OpenAI Responses handler/encoder tests cover new-session metadata, valid resume metadata, and non-enumerating denial errors.
  - _Requirements: 1.2, 1.4, 3.2, 12.1, 12.2, 12.3, 12.4, 12.5_
  - _Boundary: OpenAI Responses frontend_
  - _Depends: 8.1, 6.3_

- [x] 8.3 Implement legacy OpenAI frontend session support (P)
  - Add legacy OpenAI-compatible request/response handling for secure session metadata using protocol-legal headers or fields.
  - Done when legacy OpenAI tests cover create/resume metadata and session-denial mapping separately from OpenAI Responses tests.
  - _Requirements: 1.4, 12.1, 12.2, 12.4, 12.5_
  - _Boundary: Legacy OpenAI frontend_
  - _Depends: 8.1, 6.3_

- [x] 8.4 Implement Anthropic frontend session support (P)
  - Add Anthropic-compatible request/response handling for secure session metadata and denial errors without changing backend semantics.
  - Done when Anthropic handler/encoder tests cover create/resume metadata and non-enumerating session-denial responses.
  - _Requirements: 1.4, 12.1, 12.2, 12.4, 12.5_
  - _Boundary: Anthropic frontend_
  - _Depends: 8.1, 6.3_

- [x] 8.5 Implement Gemini frontend session support (P)
  - Add Gemini-compatible request/response handling for secure session metadata and denial errors without changing backend semantics.
  - Done when Gemini handler/encoder tests cover create/resume metadata and non-enumerating session-denial responses.
  - _Requirements: 1.4, 12.1, 12.2, 12.4, 12.5_
  - _Boundary: Gemini frontend_
  - _Depends: 8.1, 6.3_

- [x] 8.6 Verify cross-frontend session error consistency
  - Compare OpenAI Responses, legacy OpenAI, Anthropic, and Gemini session-denial mappings for invalid, expired, owner-mismatch, and missing-principal cases.
  - Done when a shared parity test or per-frontend matrix proves supported frontends return protocol-legal but semantically consistent session errors.
  - _Requirements: 12.1, 12.2, 12.3, 12.5_
  - _Boundary: Frontend parity validation_
  - _Depends: 8.2, 8.3, 8.4, 8.5_

- [x] 8.7 Add backend metadata trust-boundary regression tests
  - Add a stub backend or backend adapter fixture that returns session-like headers and provider conversation ids during streaming and non-streaming responses.
  - Done when tests prove frontend-visible or backend-returned session headers are never accepted as authoritative proxy session IDs and can only be surfaced as configured correlation metadata.
  - _Requirements: 1.10, 1.11, 3.8, 3.9_
  - _Boundary: Backend adapter boundary tests_
  - _Depends: 7.4, 8.6_

- [x] 9. Implement operator diagnostics and authorization

- [x] 9.1 Add operator authorization and redaction seams for session diagnostics
  - Define a diagnostics authorization seam in the diagnostics driving adapter that can adapt existing shared-secret protection initially but still enforces redaction mode, raw audit denial by default, and non-enumerating unauthorized lookups.
  - Done when unit tests show unauthorized transcript/audit access is denied without exposing session existence and authorized redacted access returns no raw payloads.
  - _Requirements: 9.3, 9.7, 13.2, 13.3, 13.7_
  - _Boundary: Diagnostics driving adapter authorization_
  - _Depends: 7.5_

- [x] 9.2 Add secure-session diagnostics handlers
  - Implement session summary, session detail, transcript, and by-A-leg lookup handlers using secure-session app query use cases/ports and diagnostics authorization.
  - Keep HTTP request/response types, status codes, and redaction transport formatting out of secure-session app/domain packages.
  - Include owner, workspace, last activity, resume eligibility, policy metadata, usage totals, per-attempt backend/model/status/settings/accounting summaries, lineage summary, transcript-enabled status, and effective policy in authorized summaries.
  - Done when HTTP tests cover summary filtering by session/user/workspace, by-A-leg lookup with redaction, per-attempt summaries, and empty result behavior.
  - _Requirements: 9.5, 10.8, 11.5, 14.1, 14.2, 14.4, 14.5, 14.6, 14.8_
  - _Boundary: Diagnostics HTTP driving adapter_
  - _Depends: 9.1, 3.2_

- [x] 9.3 Add diagnostics non-enumeration and audit access tests
  - Add tests for wrong owner, unknown session id, unknown A-leg id, expired session, raw audit denial, and authorized redacted transcript retrieval.
  - Done when diagnostics responses do not reveal whether another user's session exists and raw audit access is unavailable unless explicitly authorized.
  - _Requirements: 3.5, 9.7, 12.3, 13.3, 13.7_
  - _Boundary: Diagnostics HTTP tests_
  - _Depends: 9.2_

- [x] 10. Wire secure sessions into runtime bundle and operations

- [x] 10.1 Construct memory-backed secure-session manager in runtime bundle
  - Open the memory secure-session store adapter from config, construct generator/app manager with a no-op recorder placeholder, and inject dependencies into the executor for early vertical-slice testing.
  - Runtimebundle always wires secure-session defaults (memory store when omitted); explicit secure_session.enabled: false is rejected at validation.
  - Done when runtimebundle tests prove default and memory-enabled configurations build secure-session dependencies (sqlite still requires explicit durable inputs).
  - _Requirements: 7.4, 7.5, 12.6_
  - _Boundary: Runtime bundle_
  - _Depends: 3.2, 6.1_

- [x] 10.2 Wire durable stores and full recorder into runtime bundle
  - Open the SQLite secure-session store adapter when configured, append closers, replace the no-op recorder with the full app recorder, and enforce startup validation for durable/audit prerequisites.
  - Done when runtimebundle tests prove SQLite-enabled secure sessions build with durable storage and full recorder dependencies.
  - _Requirements: 7.1, 7.2, 7.3, 8.4, 9.2, 9.5_
  - _Boundary: Runtime bundle_
  - _Depends: 4.2, 7.6, 10.1_

- [x] 10.3 Update sample configuration and operator documentation
  - Document secure-session enablement, memory vs SQLite durability, resume window behavior, token key requirements, audit/redaction settings, diagnostics paths, and non-durable limitations.
  - Done when `config/config.yaml` and relevant docs show secure-session defaults and durable examples without implying continuity IDs are ownership proof.
  - _Requirements: 6.1, 6.4, 7.4, 11.5, 13.4_
  - _Boundary: Configuration documentation_
  - _Depends: 10.2_

- [x] 10.4 Add secure-session logs and metrics (P)
  - Emit structured logs and metrics for create, resume, denial categories, storage failures, mandatory audit failures, transcript append failures, and last-activity touch latency.
  - Redact token material and hash session/user identifiers where appropriate for logs.
  - Done when metrics/log tests verify denial category visibility without raw session authority leakage.
  - _Requirements: 3.5, 12.7, 13.5_
  - _Boundary: Observability_
  - _Depends: 6.3, 7.6_

- [x] 11. Add end-to-end integration coverage

- [x] 11.1 Test valid session create and resume across frontend, runtime, and backend stub
  - Add composed HTTP tests that create a session, capture proxy-issued resume metadata, resume as the same principal/workspace, and verify the same stored A-leg binding and session lineage are used.
  - Done when at least one frontend path proves create/resume works end-to-end with a stub backend and no raw resume token reaches the backend.
  - _Requirements: 1.4, 2.2, 5.1, 5.5, 6.2, 11.2, 13.4_
  - _Boundary: Cross-boundary integration_
  - _Depends: 8.2, 10.1_

- [x] 11.2 Test invalid, forged, and expired resume paths end-to-end
  - Add composed HTTP tests for missing principal, wrong owner, guessed A-leg, guessed continuity key, malformed token, expired resume window, workspace mismatch, and policy unavailable.
  - Done when every invalid path rejects before backend open and returns protocol-legal non-sensitive errors.
  - _Requirements: 2.3, 2.4, 3.2, 3.3, 3.6, 6.4, 10.3, 11.6, 12.1, 12.2, 12.3, 12.6_
  - _Boundary: Cross-boundary integration_
  - _Depends: 11.1_

- [x] 11.3 Test durable restart and resume behavior end-to-end
  - Add SQLite-backed tests that create a session, close/reopen runtime storage, resume within the window, and reject when required secure-session ownership state is missing.
  - Done when restart tests prove owner binding, workspace, policy metadata, last activity, usage, and lineage visibility survive proxy restart.
  - _Requirements: 2.6, 6.7, 7.1, 7.2, 7.3, 7.5, 7.6, 7.7, 10.7_
  - _Boundary: Cross-boundary integration_
  - _Depends: 11.1, 4.3, 10.2_

- [x] 11.4 Test streaming lineage, usage, audit, and recorder failure behavior
  - Add stream tests for pre-output B2BUA recovery, post-output committed failures, usage propagation, transcript ordering, audit redaction, per-attempt backend/model/status/accounting, mandatory pre-output readiness failure, and mandatory post-output recorder failure.
  - Split test cases by behavior cluster so lineage, attempt trace, usage, audit, and mandatory recorder failure assertions fail independently.
  - Done when stream tests prove no silent replacement after output and operators can identify surfaced vs swallowed attempts plus backend/model/status/accounting evidence in session records.
  - _Requirements: 4.3, 4.4, 4.5, 5.2, 5.3, 5.4, 5.5, 6.1, 6.3, 6.5, 6.6, 6.7, 6.8, 9.2, 9.6, 10.8, 13.6_
  - _Boundary: Cross-boundary streaming integration_
  - _Depends: 7.6, 10.2, 8.7_

- [x] 12. Add performance, concurrency, and race validation

- [x] 12.1 Add memory store and generator race tests
  - Run high-concurrency create/resume/touch scenarios for the memory store and ID generator under the race detector.
  - Done when `go test -race ./internal/core/securesession` passes and proves no duplicated ids or data races.
  - _Requirements: 1.5, 1.6, 1.8_
  - _Boundary: Concurrency validation_
  - _Depends: 3.2, 5.1_

- [x] 12.2 Add recorder overhead benchmark smoke
  - Add benchmark smoke for stream event recording with transcript/usage/audit enabled and with capture disabled.
  - Done when benchmark output documents bounded overhead and no unbounded allocation growth for representative event streams.
  - _Requirements: 4.2, 6.3, 8.2, 9.1_
  - _Boundary: Performance validation_
  - _Depends: 7.6_

- [x] 12.3 Add SQLite concurrent resume and touch tests
  - Exercise concurrent resume, last-activity touch, attempt trace append/update, usage append, and transcript append against SQLite using deterministic fake clocks.
  - Done when SQLite tests prove transaction consistency, attempt updates remain attached to the correct B-leg, and last-activity state remains monotonic for accepted events.
  - _Requirements: 6.1, 6.5, 6.6, 7.2, 7.3, 7.7, 8.4, 9.4_
  - _Boundary: Durable concurrency validation_
  - _Depends: 4.3, 7.2_

- [x] 13. Final quality gates and traceability review

- [x] 13.1 Run focused secure-session test suite
  - Run all new package tests for `internal/core/securesession`, `internal/core/runtime`, `internal/core/diag`, frontend session tests, and SQLite store tests.
  - Done when focused tests pass and failures, if any, are fixed without weakening requirements.
  - Evidence: `go test -parallel=8 -timeout=10m` on `./internal/core/securesession/...`, `./internal/core/runtime/...`, `./internal/core/diag/...`, `./internal/plugins/frontends/openairesponses/...`, `./internal/plugins/frontends/sessionwire/...`, `./internal/plugins/frontends/parity/...`, `./internal/stdhttp/...` (green).
  - _Requirements: 1.1, 2.2, 3.1, 4.1, 5.1, 6.1, 7.1, 8.1, 9.1, 10.1, 11.1, 12.1, 13.1_
  - _Boundary: Verification_
  - _Depends: 11.4, 12.3, 8.6_

- [x] 13.2 Run architecture, precommit, and quality checks
  - Run architecture tests, precommit-tagged runtime/QA tests, and repository quality checks relevant to core package boundaries and config drift.
  - Done when checks prove secure-session core does not import concrete plugins/provider SDKs and config/docs remain tidy.
  - Evidence: `make quality-checks`, `make test-precommit-extra`, and full `make qa` (includes `go test -tags=precommit ./...`, `make lint`, `go tool govulncheck`) all green; lint fixes applied where `make qa` surfaced errcheck/gofumpt/paralleltest/modernize in secure-session tests and `execctx/submit_views.go`.
  - _Requirements: 7.5, 12.5_
  - _Boundary: Verification_
  - _Depends: 13.1_

- [x] 13.3 Complete requirement traceability and operator evidence review
  - Review `tasks.md`, tests, diagnostics, and docs to ensure every requirement id has implementation and validation coverage.
  - Done when all secure-session requirements are mapped to delivered code/tests and any residual limitations are documented for operators.
  - Evidence: Requirement groups R1–R14 remain mapped in `design.md` (Requirements Traceability table). Completed tasks in this file carry `_Requirements:_` tags through 12.x; Req. 13 (protocol-neutral feedback) and Req. 14 (operator visibility) are covered by frontend/session tests, `internal/core/securesession/adapters/diag`, `internal/stdhttp` diagnostics tests, and tasks 8.x/10.x/11.x. Residual: strict race coverage is Linux CI / `scripts/race-check.sh` (see `docs/release-gates.md`); Windows `make test-race` is intentionally a no-op. Wiring smoke: `go run ./cmd/lipstd` with `server.address: ":0"` reached `listening` (default `:8080` may already be in use locally).
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 1.9, 1.10, 1.11, 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 3.8, 3.9, 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 4.7, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 6.7, 6.8, 6.9, 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 7.7, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7, 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 10.1, 10.2, 10.3, 10.4, 10.5, 10.6, 10.7, 10.8, 11.1, 11.2, 11.3, 11.4, 11.5, 11.6, 11.7, 12.1, 12.2, 12.3, 12.4, 12.5, 12.6, 12.7, 13.1, 13.2, 13.3, 13.4, 13.5, 13.6, 13.7, 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8_
  - _Boundary: Verification_
  - _Depends: 13.2_
