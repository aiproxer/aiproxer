# Implementation Plan

- [x] 1. Establish auth and backend security contracts
- [x] 1.1 Define protocol-neutral auth decision DTOs and core ports
  - In `pkg/lipsdk/auth`, add handler kinds, required levels, decision outcomes, challenge data, principal/device identity snapshots, and `InboundCallMeta` (no `net/http` types).
  - In `internal/core/auth`, add consuming ports: `Authenticator`, `RemoteDecider` (remote auth is a core consumer-side port only in this spec).
  - Include API-key-plus-SSO outcome semantics so a recognized device/app key can still produce an unmet SSO challenge or denial.
  - The `pkg/lipsdk/auth` package compiles without importing HTTP, provider SDK, enterprise transport, or canonical LLM event packages.
  - _Requirements: 1.7, 3.4, 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 7.1, 7.2, 7.3, 7.4, 7.6, 12.5, 13.5_
  - _Boundary: Public SDK DTOs + `internal/core/auth` ports_

- [x] 1.2 Define auth decision and session-start event DTOs
  - Add non-secret event models in `pkg/lipsdk/auth` for auth decisions and session starts, including access mode, auth level, frontend identity, outcome, reason code, principal, device identity, and session certainty fields.
  - Define the core `EventSink` port in `internal/core/auth` (methods take SDK event DTOs) and `EventFailurePolicy` without durable storage or remote transport assumptions in the DTOs.
  - The event contracts make raw bearer tokens, API keys, SSO tokens, resume proofs, and personal OAuth tokens impossible to represent as first-class event fields.
  - _Requirements: 5.5, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7, 9.1, 9.2, 9.3, 9.5, 9.6, 9.7, 13.1, 13.2, 13.3_
  - _Boundary: Public SDK event DTOs + core `EventSink` port_

- [x] 1.3 Define backend credential posture metadata
  - Add backend credential posture values for static credentials, workload credentials, OAuth-user credentials, and unknown classification.
  - Extend plugin registration metadata so backend factories can declare credential posture without exposing plugin-private configuration values.
  - Contract tests verify default or unknown posture behavior is explicit and visible to registry validation code.
  - _Requirements: 10.1, 10.2, 10.4, 10.5, 10.7, 11.5_
  - _Boundary: BackendSecurityProfile_

- [x] 1.4 Add SDK contract documentation and compile-time guard tests
  - Document public auth and backend security contract intent, non-goals, and secret-handling expectations.
  - Add package-local tests or compile-time assertions proving the new SDK contracts can be consumed without internal packages.
  - The package boundary is verifiably stable: `pkg/lipsdk` does not import concrete core, stdhttp, provider SDK, or enterprise implementation packages.
  - _Requirements: 7.6, 10.2, 12.5, 13.1, 13.2_
  - _Boundary: Public SDK Contracts_

- [x] 2. Add access mode and auth configuration validation
- [x] 2.1 Add typed access and auth configuration fields
  - Add config fields for access mode, auth handler kind, required auth level, local API-key records, event failure policy, and remote auth selection placeholders.
  - Preserve plugin-private backend configuration opacity while making access/auth posture visible to validation.
  - The config model can represent default single-user local-noop, multi-user local API-key, and remote-auth placeholder scenarios.
  - _Requirements: 1.1, 1.2, 3.2, 4.1, 6.1, 6.6, 7.5, 7.6, 8.5, 11.1_
  - _Boundary: ConfigValidation_

- [x] 2.2 Implement access mode normalization and listener classification
  - Normalize empty access mode to `single_user` and reject unknown mode values with clear validation errors.
  - Classify configured listener addresses as loopback, broad, non-loopback, or malformed for IPv4 and IPv6 cases.
  - Unit tests cover empty address, `127.0.0.1`, `127.0.0.0/8`, `::1`, `:8080`, `0.0.0.0`, `[::]`, hostnames, and malformed host/port values.
  - _Requirements: 1.1, 1.2, 1.5, 2.1, 2.2, 2.4, 2.5, 2.6, 3.1, 11.1, 11.2_
  - _Boundary: AccessMode_

- [x] 2.3 Implement access/auth posture validation matrix
  - Validate single-user loopback-only binding, multi-user required authentication, and no-auth/noop restrictions.
  - Keep backend credential posture eligibility out of this validation task so backend rules remain owned by the registry validation boundary.
  - Table tests verify invalid combinations fail startup validation and permissive interpretations are not chosen for conflicting settings.
  - _Requirements: 1.3, 1.4, 1.6, 2.3, 2.4, 2.5, 3.2, 3.3, 4.2, 5.1, 11.1, 11.2, 11.3_
  - _Boundary: AccessMode, ConfigValidation_

- [x] 2.4 Apply mode-aware config defaults and startup-effective values
  - Replace broad listener defaulting with safe loopback-effective defaulting for single-user mode.
  - Preserve explicit broad binds only when multi-user mode and required authentication are configured.
  - Tests show omitted access mode starts as single-user, omitted single-user address becomes loopback, and explicitly broad single-user configs fail.
  - _Requirements: 1.2, 2.2, 2.3, 2.5, 11.4, 11.6_
  - _Boundary: ConfigValidation_

- [x] 3. Build auth and session event dispatch
- [x] 3.1 Implement the default operator-visible event sink
  - Provide a default structured-log `EventSink` **implementation** in an infra or runtimebundle package (uses `log/slog` or existing logging helpers there), registered at the composition root; core `EventDispatcher` depends only on the `EventSink` port, not on `slog` directly.
  - Support explicit disabled/custom sink handling according to configuration while keeping event records non-secret.
  - Tests verify default event records are observable and the disabled/custom state is explicit rather than silent.
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.7, 9.2, 9.3, 13.2, 13.3_
  - _Boundary: EventDispatcher_

- [x] 3.2 Implement event dispatcher failure policy
  - Apply best-effort and fail-closed event failure policies for auth decision and session-start event delivery.
  - Ensure protected requests fail before backend execution when fail-closed event delivery fails.
  - Tests verify failure policy behavior without depending on log text or external services.
  - _Requirements: 8.5, 11.3, 12.2, 13.2_
  - _Boundary: EventDispatcher_

- [x] 3.3 Add event redaction regression tests
  - Test auth decision and session-start event payloads using configured secrets and token-like values.
  - Verify raw API keys, bearer values, SSO tokens, resume proofs, and personal OAuth tokens do not appear in emitted events or default log attributes.
  - The tests fail if any secret fixture string is present in event output.
  - _Requirements: 8.6, 9.6, 13.1, 13.2, 13.3, 13.5_
  - _Boundary: EventDispatcher_

- [x] 4. Implement local and remote-delegated auth services
- [x] 4.1 Implement OS identity lookup for local no-op auth
  - Resolve current OS user identity through a testable provider with Windows, Linux, and macOS-friendly fallback inputs.
  - Produce an explicit fallback principal when OS user data is unavailable and make that fallback observable to the operator.
  - Tests cover normal lookup, environment fallback, and unknown local fallback without requiring real OS account changes.
  - _Requirements: 5.2, 5.3, 5.4, 13.4_
  - _Boundary: OSIdentityProvider_

- [x] 4.2 Implement explicit local no-op authenticator
  - Allow credential-free requests only when no-op is selected and permitted by access posture validation.
  - Establish a non-empty principal for each accepted request using the OS identity provider.
  - Tests prove empty auth configuration is not treated as anonymous pass-through and local no-op emits an allowed decision.
  - _Requirements: 1.7, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6_
  - _Boundary: AuthService_

- [x] 4.3 Implement local API-key authenticator and redacted key identity
  - Validate local API-key records at startup and reject records missing principal or key identity fields.
  - Authenticate presented bearer keys with constant-time comparison and establish principal plus device/key identity on success.
  - Tests cover missing key, unknown key, valid key, invalid record validation, and redacted fingerprint output.
  - _Requirements: 4.3, 4.5, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 13.1, 13.2, 13.3, 13.4_
  - _Boundary: AuthService_

- [x] 4.4 Implement auth policy orchestration and remote decision abstraction
  - Route `InboundCallMeta` to local no-op, local API-key, or `RemoteDecider` based on validated config.
  - Support API-key-plus-SSO challenge/deny semantics through a `RemoteDecider` fake or stub without implementing remote transport.
  - Tests verify remote allow, remote deny, remote challenge, remote unavailable fail-closed, unusable remote decisions, and unmet SSO challenge behavior.
  - _Depends: 3.1, 3.2_
  - _Requirements: 3.4, 3.5, 3.6, 4.1, 4.2, 4.4, 4.5, 4.6, 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 7.7, 12.2, 13.5_
  - _Boundary: AuthService_

- [x] 5. Adapt auth decisions to HTTP and protocol-safe errors
- [x] 5.1 Implement HTTP auth request extraction and principal propagation
  - Convert incoming HTTP request metadata into protocol-neutral auth requests and call the configured auth service.
  - Attach established principals to the request context through the existing principal view mechanism on allow decisions.
  - Tests verify accepted requests reach the inner handler with principal context and rejected requests do not reach it.
  - _Depends: 4.4_
  - _Requirements: 3.4, 3.5, 3.6, 5.2, 6.2, 7.2, 8.1, 8.2, 12.2, 12.4_
  - _Boundary: HTTPAuthAdapter_

- [x] 5.2 Add renderer-aware termination support to the HTTP auth path
  - Add the minimal stdhttp/httpauth extension needed for auth decisions to carry safe rendered status, headers, content type, and body.
  - Preserve existing middleware provider-chain behavior for continue, principal, reject, challenge, and annotate outcomes.
  - Tests verify terminal auth responses are written once and existing safe response header behavior remains intact.
  - _Requirements: 3.5, 6.3, 7.3, 12.2, 12.6, 13.2, 13.4_
  - _Boundary: HTTPAuthAdapter, AuthErrorRenderer_

- [x] 5.3 Implement default auth error renderer
  - Render denied and challenged auth decisions into generic safe HTTP responses for surfaces without protocol-specific rendering.
  - Preserve challenge headers where safe and avoid existence-revealing secret details in response bodies.
  - Tests verify missing key, invalid key, SSO required, and remote unavailable categories produce safe client-visible responses.
  - _Requirements: 3.5, 4.6, 6.3, 7.3, 7.7, 12.6, 13.2, 13.4_
  - _Boundary: AuthErrorRenderer_

- [x] 5.4 Add optional frontend auth error renderer metadata
  - Allow frontend mount metadata to opt into protocol-specific auth error bodies without owning auth policy.
  - Keep default rendering active when frontend identity cannot be selected safely before mux dispatch.
  - Tests verify renderer selection, fallback rendering, and no backend execution for rendered auth failures.
  - _Requirements: 3.5, 6.3, 7.3, 12.2, 12.5, 12.6, 12.7, 13.2_
  - _Boundary: AuthErrorRenderer, Public SDK Contracts_

- [x] 5.5 Add stdhttp auth adapter integration tests
  - Exercise local no-op, local API-key, remote stub allow, remote stub deny, and challenge flows through the stdhttp middleware stack.
  - Verify auth decision events are emitted before execution and backend/frontend handlers are skipped for reject or challenge outcomes.
  - The integration suite proves request authentication is enforced before backend attempts can open.
  - _Depends: 3.2, 4.4, 5.3_
  - _Requirements: 3.4, 3.5, 3.6, 5.5, 6.3, 6.4, 7.3, 7.4, 8.1, 8.2, 8.4, 12.2_
  - _Boundary: HTTPAuthAdapter, EventDispatcher_

- [x] 6. Emit session-start events from runtime preparation
- [x] 6.1 Implement legacy session-start event emission
  - Emit session-start events after legacy continuity/session identity is resolved and before backend execution continues.
  - Include principal, access/auth policy context, frontend identity when available, A-leg/session identifiers, and certainty information.
  - Tests verify a new legacy session emits one event with non-secret session fields.
  - _Depends: 3.1_
  - _Requirements: 9.1, 9.2, 9.3, 9.5, 9.6, 9.7, 12.4_
  - _Boundary: SessionStartEmitter_

- [x] 6.2 Add secure-path session-start emission when secure sessions are already active
  - Emit session-start events from the secure preparation path only when that path is already active through existing runtime state.
  - Do not complete or expand secure-session runtimebundle wiring as part of this task.
  - Tests verify secure-path new/resume event behavior using in-package fakes or existing secure-session test harnesses.
  - _Depends: 6.1_
  - _Requirements: 9.1, 9.3, 9.4, 9.7, 12.4_
  - _Boundary: SessionStartEmitter_

- [x] 6.3 Add session dedupe and unknown-certainty coverage
  - Prevent duplicate session-start events for requests recognized as belonging to the same existing session start.
  - Emit explicit unknown or reduced-certainty outcomes when stable session identity cannot be established.
  - Tests cover existing legacy session, secure resume, missing session identity, and redaction of resume proofs.
  - _Depends: 6.1, 6.2_
  - _Requirements: 9.4, 9.5, 9.6, 9.7, 13.1, 13.2_
  - _Boundary: SessionStartEmitter_

- [x] 7. Enforce backend credential posture eligibility
- [x] 7.1 Store backend security profiles in the registry
  - Record backend credential posture alongside backend factory registration and expose lookup for validation.
  - Preserve existing backend factory construction behavior and duplicate registration checks.
  - Tests verify registered profiles are returned by factory key and unknown factories produce clear errors.
  - _Depends: 1.3_
  - _Requirements: 10.1, 10.2, 11.5_
  - _Boundary: BackendSecurityProfile_

- [x] 7.2 Declare credential posture for bundled backend factories
  - Assign static, workload, OAuth-user, or unknown posture for each bundled backend factory according to current credential behavior.
  - Ensure non-OAuth-user backends remain eligible for multi-user mode when otherwise valid.
  - Tests verify every bundled backend factory has an explicit posture declaration or intentional unknown classification.
  - _Depends: 7.1_
  - _Requirements: 10.1, 10.2, 10.5, 10.6, 11.5_
  - _Boundary: BackendSecurityProfile_

- [x] 7.3 Validate enabled backend posture against access mode
  - Reject enabled OAuth-user backends in multi-user mode before frontend traffic is accepted.
  - Apply conservative multi-user behavior for `CredentialUnknown` unless the backend is explicitly classified as eligible.
  - Tests verify multi-user rejection identifies the incompatible backend instance without secrets, single-user allows OAuth-user backends, and invalid backends are not silently disabled or rerouted.
  - _Depends: 2.3, 7.2_
  - _Requirements: 10.3, 10.4, 10.5, 10.6, 10.7, 11.1, 11.2, 11.3, 11.5_
  - _Boundary: BackendSecurityProfile, ConfigValidation_

- [x] 8. Wire auth architecture into runtime composition
- [x] 8.1 Add runtimebundle auth build options
  - Add build options for `RemoteDecider`, `EventSink`, OS identity provider, and auth error renderer.
  - Build configured auth providers from validated config while keeping concrete remote transport out of the OSS standard binary.
  - Tests verify runtimebundle returns the expected provider set and fails clearly when required injected interfaces are missing.
  - _Depends: 2.4, 3.2, 4.4, 5.3_
  - _Requirements: 1.7, 3.2, 3.3, 7.5, 7.6, 8.5, 11.1, 11.2, 11.3_
  - _Boundary: RuntimeBundleAuthBuilder_

- [x] 8.2 Wire single-user local-noop defaults in the standard binary
  - Configure the standard distribution so omitted access/auth config starts with single-user loopback and explicit local no-op auth.
  - Startup output makes effective access mode, listener, auth handler, and required level observable to the operator.
  - Integration tests verify no anonymous pass-through path remains in the standard runtime default.
  - _Depends: 8.1_
  - _Requirements: 1.2, 2.2, 5.1, 5.2, 5.5, 5.6, 11.4_
  - _Boundary: RuntimeBundleAuthBuilder, ConfigValidation_

- [x] 8.3 Wire multi-user local API-key mode
  - Configure standard runtime composition for multi-user deployments with local API-key records.
  - Verify authenticated requests execute with principal/device context and unauthenticated requests are rejected before backend execution.
  - Integration tests cover valid multi-user local API-key startup and invalid no-auth multi-user startup.
  - _Depends: 8.1_
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 4.3, 6.1, 6.2, 6.3, 6.4, 11.1_
  - _Boundary: RuntimeBundleAuthBuilder_

- [x] 8.4 Wire remote mode as interface-only startup behavior
  - Support selecting remote auth in config only when a `RemoteDecider` is injected by the composition root.
  - Ensure the OSS standard binary does not create ConnectRPC, gRPC, HTTP, or generated remote auth clients in this spec.
  - Tests verify remote mode without an injected `RemoteDecider` fails startup and injected fakes drive allow/deny/challenge decisions.
  - _Depends: 8.1_
  - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 7.7, 11.2, 13.5_
  - _Boundary: RuntimeBundleAuthBuilder_

- [x] 9. Update sample config and migration coverage
- [x] 9.1 Update sample configuration scenarios
  - Add sample config entries for default single-user local-noop, multi-user local API-key, and remote-auth interface-only selection.
  - Document safe loopback defaults and broad bind requirements through config comments rather than prose-only docs.
  - The sample config demonstrates default event visibility or explicitly disabled/custom event delivery without raw secrets.
  - _Requirements: 1.2, 2.2, 3.1, 3.2, 4.1, 6.1, 7.5, 8.7, 11.4, 13.2_
  - _Boundary: ConfigValidation_

- [x] 9.2 Add migration and default behavior regression tests
  - Verify existing configs that omit access mode become single-user and omit server address become loopback-effective.
  - Verify explicit broad binds require multi-user mode and required authentication.
  - Tests prove unsafe broad listener defaults are not accepted silently in single-user mode.
  - _Requirements: 1.2, 2.2, 2.3, 2.5, 11.3, 11.4, 11.6_
  - _Boundary: ConfigValidation_

- [x] 10. Complete end-to-end validation and architecture guards
- [x] 10.1 Add bundled frontend auth rejection compatibility tests
  - Exercise each bundled frontend surface with missing/invalid credentials where authentication is required.
  - Verify responses use legal frontend-compatible auth error bodies or safe default rendering when no specific renderer is available.
  - Tests prove backend execution is not started for frontend auth rejects or challenges.
  - _Depends: 5.5, 8.3_
  - _Requirements: 3.5, 3.6, 6.3, 6.4, 7.3, 12.2, 12.6, 13.4_
  - _Boundary: HTTPAuthAdapter, AuthErrorRenderer_

- [x] 10.2 Add authenticated streaming compatibility regression tests
  - Run an authenticated request through existing streaming paths and verify canonical event ordering, cancellation, and error framing remain unchanged.
  - Include principal context in the execution path without adding provider-specific auth data to canonical LLM events.
  - Tests compare authenticated success behavior against existing unauthenticated baseline fixtures or stubs.
  - _Depends: 8.3_
  - _Requirements: 12.1, 12.3, 12.4, 12.5, 12.7_
  - _Boundary: HTTPAuthAdapter, RuntimeBundleAuthBuilder_

- [x] 10.3 Add full-path secret leakage regression tests
  - Seed API-key, bearer, SSO, resume-proof, and OAuth-like secret fixtures into auth and session flows.
  - Verify startup errors, request errors, logs, diagnostics, auth events, session events, and frontend responses do not contain raw secret values.
  - The regression suite fails on any raw secret fixture appearing in captured outputs.
  - _Depends: 3.3, 5.5, 6.3, 8.3_
  - _Requirements: 6.5, 8.6, 9.6, 13.1, 13.2, 13.3, 13.4, 13.5_
  - _Boundary: EventDispatcher, AuthErrorRenderer, SessionStartEmitter_

- [x] 10.4 Update architecture and quality guard coverage
  - Add or update architecture tests ensuring core auth packages do not import provider SDKs, concrete enterprise packages, or concrete plugins.
  - Verify auth/session events remain outside canonical LLM event contracts and backend plugin config remains opaque except for credential posture metadata.
  - `go test ./...` and relevant architecture/QA package tests pass with the new auth boundaries in place.
  - _Depends: 1.4, 7.3, 8.4_
  - _Requirements: 7.6, 10.2, 11.5, 12.5, 12.7_
  - _Boundary: Public SDK Contracts, BackendSecurityProfile_
