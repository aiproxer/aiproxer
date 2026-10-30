# Implementation Gap Analysis: Authentication Architecture Refactor

Generated: 2026-04-24T17:08:09Z

## Scope and Status

This analysis compares the `authentication-architecture-refactor` requirements against the current Go codebase. The spec is in `requirements-generated` phase, and `spec.json` still has `approvals.requirements.approved: false`; this gap analysis proceeds because it can inform requirement revisions and the design phase.

## Current State Summary

### Existing Assets

- **Transport auth seam exists**: `pkg/lipsdk/transport/httpauth/provider.go:8` defines `Provider.Authenticate`, and `pkg/lipsdk/transport/httpauth/result.go:9` defines continue/principal/reject/challenge/annotate outcomes.
- **HTTP auth middleware exists**: `internal/stdhttp/auth/middleware.go:36` runs configured auth providers, propagates principals, and fails closed on provider errors.
- **Principal propagation exists**: `pkg/lipsdk/execview/context.go:10` carries `PrincipalView` through context, and `internal/core/runtime/executor_prepare.go:49` reads it before session-open, workspace, hooks, route hints, and views.
- **HTTP server stack exists**: `internal/stdhttp/server.go:46` stacks tracing/metrics/request ID/access log/recovery/auth/mux and serves with `cfg.Server.Address` at `internal/stdhttp/server.go:237`.
- **Runtime composition has auth injection hooks**: `internal/infra/runtimebundle/options.go:35` accepts `HTTPAuthProviders`, and `internal/infra/runtimebundle/build.go:199` copies them into `Built`.
- **Standard binary does not wire auth today**: `cmd/lipstd/main.go:88` builds runtime options without `HTTPAuthProviders`.
- **Typed config exists but lacks auth/access blocks**: `internal/core/config/model.go:17` defines `Config`, and `internal/core/config/model.go:122` defines `ServerConfig`; there is no access-mode or auth policy config.
- **Server default is unsafe for single-user requirement**: `internal/core/config/loader.go:53` defaults empty `server.address` to `:8080`, which means all interfaces in Go.
- **Validation exists but lacks posture validation**: `internal/core/config/validate.go:11` validates config, and `internal/core/config/validate.go:130` validates only server duration and queue fields.
- **Plugin registration exists**: `pkg/lipsdk/contracts.go:23` defines `Registration`, and `internal/core/config/registrations.go:9` maps config plugin rows to registrations.
- **Backend capabilities exist but not credential posture**: `pkg/lipapi/capabilities.go:5` models functional capabilities; it does not distinguish personal OAuth-user credentials from service credentials.
- **Secure-session subsystem exists separately**: `internal/core/runtime/executor.go:71` has secure-session fields, and `internal/core/runtime/executor_prepare.go:32` branches into secure preparation when active.
- **Session-open extension exists but is fail-open**: `pkg/lipsdk/session/opener.go:37` defines the opener seam, and `internal/core/extensions/session_open.go:16` runs it with fail-open semantics.
- **Diagnostics secret exists but is not user auth**: `internal/stdhttp/server.go:157` mounts diagnostics; `diagnostics.shared_secret` protects admin-style diagnostics, not frontend users.

### Dominant Patterns to Preserve

- Core config is typed and plugin-private config remains opaque.
- Composition is explicit through `cmd/lipstd`, `internal/pluginreg`, `internal/infra/runtimebundle`, and `internal/stdhttp`.
- Stable contracts live in `pkg/lipapi` and `pkg/lipsdk`; provider SDKs stay in backend plugins.
- Frontend adapters own protocol-specific error rendering and wire legality.
- Runtime execution and routing remain streaming-first and core-owned.
- New seams should be narrow and consumed where needed, not generic service-locator abstractions.

## Requirement-to-Asset Map

| Requirement | Existing Assets | Gap Classification | Notes |
|---|---|---|---|
| R1 Access Mode Selection | `internal/core/config` load/validate patterns | **Missing** | No `single_user` / `multi_user` config or effective-mode defaulting. |
| R2 Single-User Binding | `ServerConfig.Address`, `stdhttp.RunWithRuntime` | **Missing / Constraint** | Existing default `:8080` conflicts with loopback-only default. Need address parsing and loopback validation. |
| R3 Multi-User Binding/Auth | `httpauth.Provider`, middleware stack | **Partial / Missing** | Auth seam exists, but no config-driven required auth, no multi-user startup enforcement. |
| R4 Auth Policy Levels | `AuthenticationResult`, `PrincipalView` | **Missing** | Existing results can carry a principal, but no no-auth/API-key/API-key-plus-SSO policy model. |
| R5 Local No-Op Auth | Empty provider chain currently acts as passthrough | **Missing / Constraint** | Passthrough does not establish OS principal or emit events. Need explicit no-op auth, not absence of auth. |
| R6 Local API-Key Auth | `httpauth.Provider` can reject/challenge | **Missing** | No local API-key records, validation, redacted key identity, or config. |
| R7 Remote Auth Delegation | `HTTPAuthProviders` injection can host a remote provider | **Missing / Unknown** | No remote auth contract or client. Protocol choice and failure mapping remain design research. |
| R8 Auth Decision Events | Logging/metrics/tracing infrastructure | **Missing** | No dedicated auth decision event model, sink, or failure policy. |
| R9 Session-Start Events | secure-session, continuity, session-open stage | **Partial / Unknown** | Session identity concepts exist, but no event semantics or dedupe contract. Need define relationship to secure sessions and legacy continuity. |
| R10 OAuth-User Backend Eligibility | plugin config rows, registrations, backend factories | **Missing / Constraint** | Plugin config opacity means eligibility should be exposed as stable metadata, not parsed from private config. |
| R11 Config Validation/Feedback | `Validate`, `LoadFile`, logger startup | **Partial** | Validation framework exists; needs cross-field posture validation and startup observability. |
| R12 Protocol/Runtime Compatibility | stdhttp middleware before mux; executor principal propagation | **Partial / Constraint** | Existing stack can prevent backend execution before auth; protocol-legal challenge/reject may need frontend-aware mapping beyond generic HTTP responses. |
| R13 Security/Secret Handling | diagnostics protection, config validation, logging patterns | **Partial / Missing** | Existing redaction is scattered; auth-specific secret/fingerprint rules missing. |

## Feasibility Analysis

### Technical Needs Implied by Requirements

- New config fields for access mode, auth policy level, local API-key records, remote auth settings, and event failure policy.
- Effective defaults that are mode-aware, especially for `server.address`.
- Startup validation across server bind address, access mode, auth policy, auth handler selection, and backend eligibility.
- An explicit authentication service/provider path that establishes a principal even for local no-op mode.
- Local OS user identity resolution for Windows, Linux, and macOS.
- Local API-key validation with redacted/fingerprinted identity and no raw secret leakage.
- Remote auth delegation contract and request/decision model.
- Auth decision and session-start event models plus local no-op/local/remote sinks.
- Backend credential posture metadata for OAuth-user gating.
- Tests across config validation, middleware behavior, standard distribution wiring, plugin registration metadata, and event emission.

### Constraints and Integration Challenges

- **Default behavior must change carefully**: existing `:8080` default likely appears in docs/tests; single-user safety requires loopback default.
- **Middleware currently writes generic HTTP termination**: requirements call for legal frontend protocol errors, which may require richer error propagation or frontend-aware auth handling.
- **Session-open is fail-open**: it is not suitable as the primary audit/auth event path when event failure policy may block execution.
- **Plugin config opacity matters**: OAuth-user backend eligibility must not require core parsing plugin-private YAML.
- **Remote auth must not pull enterprise logic into OSS**: design needs a stable boundary and a local fallback story.
- **Secure-session is partially wired**: fields exist, but runtimebundle appears not to set secure-session manager fields by default; session-start events should avoid depending on unfinished wiring unless design explicitly phases it.
- **Requirements not yet approved**: if requirements change, especially around API-key-plus-SSO or session-start dedupe, design may need rework.

### Complexity Signals

- Access-mode config and loopback validation: straightforward but security-sensitive.
- Local no-op/API-key auth: medium complexity due to principal/event/secret handling.
- Remote auth delegation: integration-heavy, but can be stubbed behind a narrow contract.
- Session-start dedupe: potentially complex because current session identity differs between legacy continuity and secure-session modes.
- Protocol-legal auth challenge/reject: can become broad if each frontend must render auth errors differently.

## Implementation Approach Options

### Option A: Extend Existing Components

**Summary:** Add access/auth config to `internal/core/config`, build concrete `httpauth.Provider` implementations from config in `cmd/lipstd` or `runtimebundle`, and extend existing stdhttp middleware for events and auth levels.

**Files/modules likely extended**

- `internal/core/config/model.go`
- `internal/core/config/loader.go`
- `internal/core/config/validate.go`
- `cmd/lipstd/main.go`
- `internal/infra/runtimebundle/options.go`
- `internal/stdhttp/auth/middleware.go`
- `pkg/lipsdk/transport/httpauth/*`
- `pkg/lipsdk/contracts.go` or plugin registration metadata files for backend eligibility

**Compatibility assessment**

- Uses existing auth middleware and principal propagation.
- Minimal disruption to frontend and executor paths.
- Risk of turning `httpauth.Provider` into a large policy interface not originally designed for access-mode validation, session events, and remote auth.

**Trade-offs**

- Pros: fastest path; reuses established tests; smaller initial diff.
- Cons: higher risk of bloating `stdhttp/auth` and mixing transport parsing with auth policy, eventing, and remote delegation.

**Effort / Risk**

- Effort: **M** for basic access/local auth; **L** if remote auth and session events are included fully.
- Risk: **Medium** because requirements exceed the current provider abstraction.

### Option B: Create New Dedicated Auth and Access Components

**Summary:** Introduce dedicated access-mode and auth packages, keep `stdhttp/auth` as the HTTP adapter, and expose stable auth/event models separate from transport parsing.

**Candidate new components**

- `internal/core/accessmode` for mode normalization, bind validation, and posture checks.
- `internal/core/auth` for auth policy levels, decisions, principal/key identity, events, and local no-op/API-key services.
- `internal/infra/osidentity` for OS user inference.
- `internal/infra/remoteauth` or similar for a future **driven** adapter that implements the core `RemoteDecider` port (not a second public contract surface).
- SDK metadata additions for backend credential posture.

**Integration points**

- `internal/core/config` references core auth/access config structs or mirrors them in config package.
- `cmd/lipstd` / `runtimebundle` constructs auth service and wraps it into `httpauth.Provider` for `stdhttp`.
- `internal/stdhttp/auth` remains responsible for HTTP extraction and termination mapping.
- Plugin registration exposes backend credential posture through a stable contract.

**Compatibility assessment**

- Better matches steering: small core-owned policy, explicit composition, transport specifics at edge.
- More files and contracts, but clearer separation of access-mode validation from HTTP middleware.
- Easier to test access-mode, local API-key, remote delegation, and event sinks independently.

**Trade-offs**

- Pros: clean boundaries; easier future enterprise integration; avoids making `httpauth.Provider` a god interface.
- Cons: larger design effort; requires careful naming and package placement to avoid architecture churn.

**Effort / Risk**

- Effort: **L**.
- Risk: **Medium** due to new contracts and broad wiring, but lower long-term maintainability risk than Option A.

### Option C: Hybrid Phased Approach

**Summary:** Add new access/auth core concepts while reusing existing stdhttp provider wiring as the first transport adapter. Phase delivery from startup safety to full remote/session event support.

**Phase sketch**

1. Add access-mode config, loopback validation, and safe defaults.
2. Add explicit local no-op auth provider that establishes OS principal and emits basic auth events.
3. Add local API-key auth and secret redaction tests.
4. Add backend credential posture metadata and multi-user OAuth-user gating.
5. Add session-start event semantics, initially tied to existing session/continuity observations.
6. Add remote auth delegation behind a configurable client and fail-closed behavior.
7. Improve protocol-legal challenge/reject rendering if generic HTTP termination is insufficient.

**Compatibility assessment**

- Uses existing middleware and principal propagation for early value.
- Keeps design room for a future remote enterprise authority.
- Allows tests and docs to migrate from current `:8080` default in a controlled way.

**Trade-offs**

- Pros: balances safety and maintainability; supports incremental validation; avoids overcommitting remote protocol details too early.
- Cons: requires clear phase boundaries to avoid inconsistent interim states.

**Effort / Risk**

- Effort: **L** overall, with **S/M** slices possible for the first access-mode and local no-op milestones.
- Risk: **Medium**; biggest risks are session-start semantics and frontend-auth error rendering.

## Recommended Design Focus

This analysis does not make final implementation decisions, but the design phase should strongly consider **Option C**: a hybrid phased approach with new auth/access concepts and existing stdhttp middleware as the first adapter.

Key design decisions to resolve:

1. Whether access/auth config structs live directly in `internal/core/config` or are defined in a core auth/access package and embedded by config.
2. Whether auth events are best represented as a new event sink, structured logs, traffic observer extensions, or a combination.
3. How session-start dedupe is defined when secure sessions are disabled.
4. How frontend-protocol-legal auth challenges are produced without pushing auth policy into every frontend plugin.
5. Where backend credential posture metadata belongs in `pkg/lipsdk` without widening `pkg/lipapi` unnecessarily.
6. How much remote auth protocol shape belongs in this spec versus a later enterprise-integration spec.

## Research Needed for Design Phase

- **Remote auth protocol**: Compare ConnectRPC and gRPC for Go-to-Go auth decisions, local development, streaming compatibility, TLS/mTLS support, and generated-code footprint.
- **OS identity portability**: Validate `os/user.Current()` behavior on Windows, Linux, macOS, cross-compiled binaries, and service contexts; define fallback behavior.
- **Frontend auth error mapping**: Review how OpenAI Responses, legacy OpenAI-compatible, Anthropic, and Gemini clients expect authentication errors and whether generic HTTP challenge/reject is adequate.
- **Secret fingerprinting**: Decide acceptable fingerprint format for API-key correlation without making brute-force identification easier.
- **Session-start semantics**: Determine whether secure sessions should be required, recommended, or optional for multi-user audit correctness.
- **OAuth-user metadata**: Decide whether credential posture is declared by factory kind, backend factory metadata, registration metadata, or a backend capability-like contract.

## Test Strategy Implications

- Add table-driven config validation tests for mode/address/auth combinations.
- Add tests proving default single-user mode uses loopback instead of broad bind.
- Add middleware tests for explicit no-op principal creation and event emission.
- Add API-key tests for missing/invalid/valid keys and secret redaction.
- Add startup wiring tests for `cmd/lipstd` or runtimebundle effective auth provider setup.
- Add plugin registry/build tests for OAuth-user backend rejection in multi-user mode.
- Add secure/session event tests for dedupe and unknown-session outcomes.
- Add protocol-level tests that auth rejects do not open backends and do not corrupt streaming behavior.

## Overall Effort and Risk

- **Effort:** **L (1-2 weeks)** for a complete version covering all 13 requirement areas; can be sliced into smaller milestones.
- **Risk:** **Medium** because core seams exist, but remote auth, session-start events, and protocol-legal rejection behavior require careful design.
- **Primary implementation risk:** accidentally treating absence of auth as local no-op auth, which would fail to establish principals and audit events.
- **Primary product risk:** allowing broad binds or OAuth-user backend activation in multi-user mode due to incomplete startup validation.

## Gap Analysis Conclusion

The current codebase provides a good foundation: HTTP auth middleware, principal propagation, runtime composition hooks, typed config validation, plugin registration, and secure-session primitives already exist. The major gap is not low-level plumbing but a coherent policy layer: access-mode defaults and validation, explicit auth levels, event semantics, backend credential posture, and remote delegation behavior. The design phase should convert these into narrow contracts while preserving the existing small-core, plugin-first, streaming-first architecture.

---

# Design Discovery and Synthesis Addendum

Generated: 2026-04-24T17:15:04Z

## Summary

- **Feature**: `authentication-architecture-refactor`
- **Discovery Scope**: Extension with security-sensitive integration points.
- **Key Findings**:
  - The existing `httpauth.Provider` and stdhttp middleware are a useful transport adapter seam, but auth policy, eventing, and remote delegation should not be pushed into that provider contract.
  - Remote auth must be designed as an interface and configuration boundary only in this phase; no concrete ConnectRPC, gRPC, or HTTP client transport is included.
  - Session-start event emission belongs near runtime session resolution, not in the existing fail-open `session.Opener` stage.

## Research Log

### Remote auth transport deferral
- **Context**: User clarified that this stage should not include concrete transport handling for remote auth instrumentation.
- **Sources Consulted**: User clarification; Connect for Go documentation; existing `runtimebundle.BuildOptions` composition pattern.
- **Findings**:
  - ConnectRPC can later provide type-safe Go client/server code and gRPC compatibility, but choosing and wiring a concrete transport now would overfit this spec.
  - The OSS binary can expose remote auth configuration and fail startup when `remote` is selected without an injected `RemoteDecider` (core port).
- **Implications**:
  - `design.md` defines the `RemoteDecider` port and remote config validation, but no generated protobuf, ConnectRPC handler, gRPC client, or network package.

### OS identity for local no-op auth
- **Context**: Single-user mode must infer the user ID from the current OS session on Windows, Linux, and macOS.
- **Sources Consulted**: Go `os/user` package documentation.
- **Findings**:
  - `os/user.Current()` returns `Uid`, `Gid`, `Username`, `Name`, and `HomeDir`; it caches the first result.
  - Platform behavior and service contexts can fail or return low-quality identity data.
- **Implications**:
  - Local no-op auth uses an `OSIdentityProvider` abstraction with environment fallback and an explicit local fallback principal when OS lookup fails.

### Auth and session events
- **Context**: Requirements require auth decision events for every decision and session-start events for new proxy-recognized sessions.
- **Sources Consulted**: Existing `session.Opener`, secure-session manager, continuity A-leg resolution, and runtime executor prepare paths.
- **Findings**:
  - `session.Opener` is fail-open and unsuitable as the source of security-sensitive event guarantees.
  - Auth decisions occur before frontend request execution in stdhttp middleware; session identity becomes available later in executor preparation.
- **Implications**:
  - Auth decision events are emitted by the auth transport adapter; session-start events are emitted by runtime preparation after session resolution.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| Extend `httpauth.Provider` | Put policy, events, and remote behavior into existing transport provider | Small diff, familiar tests | Bloats transport seam and mixes HTTP with core auth policy | Rejected as primary pattern |
| Dedicated core auth plus stdhttp adapter | DTOs in `pkg/lipsdk/auth`, ports in `internal/core/auth`, stdhttp as driving adapter | Clear boundaries, future remote integration, testable local handlers | More files and upfront design | Selected |
| Concrete remote transport now | Add ConnectRPC or gRPC generated client/server now | End-to-end remote path exists early | Violates current scope clarification and adds dependency decisions too soon | Deferred |

## Design Decisions

### Decision: Separate auth policy from HTTP auth middleware
- **Context**: Existing middleware is transport-shaped and returns HTTP-specific results.
- **Alternatives Considered**:
  1. Extend `httpauth.Provider` directly.
  2. Define an auth service contract and adapt it to `httpauth.Provider`.
- **Selected Approach**: Keep **DTOs and event shapes** in `pkg/lipsdk/auth`; define **consuming ports** (`Authenticator`, `RemoteDecider`, `EventSink`) in `internal/core/auth`; implement local services and facades under `internal/core/auth`; adapt to stdhttp as the driving edge through a small adapter; wire default logging sinks from infra at the composition root.
- **Rationale**: Preserves stdhttp as driving adapter, keeps transport and `slog` out of core policy, and matches “ports live with consumers” for this repo’s core/plugin layout.
- **Trade-offs**: Adds a public SDK surface for DTOs that must stay stable; port names are internal except where re-exported for tests.
- **Follow-up**: Keep the initial SDK contract narrow and avoid provider-specific fields.

### Decision: Remote auth interface only
- **Context**: Future enterprise auth is out of repo and may use ConnectRPC or gRPC later.
- **Alternatives Considered**:
  1. Implement ConnectRPC client now.
  2. Implement gRPC client now.
  3. Define only the `RemoteDecider` port and configuration validation.
- **Selected Approach**: Define only the port and factory injection point; standard OSS fails startup if remote mode is configured without a supplied `RemoteDecider` implementation.
- **Rationale**: Satisfies configurability and boundary requirements without selecting transport prematurely.
- **Trade-offs**: Remote mode cannot function end-to-end until a later spec or enterprise integration supplies an implementation.
- **Follow-up**: Future remote transport spec must map its wire contract to the core `RemoteDecider` port.

### Decision: Session-start event source follows runtime session resolution
- **Context**: Middleware sees auth but not canonical session information.
- **Alternatives Considered**:
  1. Emit session-start events in auth middleware from raw HTTP inputs.
  2. Emit through fail-open `session.Opener`.
  3. Emit in executor preparation after continuity or secure-session resolution.
- **Selected Approach**: Add runtime session event emission after session resolution and before backend execution.
- **Rationale**: Uses the best available proxy session identity and avoids fail-open security behavior.
- **Trade-offs**: Session-start events are coupled to executor preparation flow and must support both legacy and secure-session paths.
- **Follow-up**: Tests must cover legacy, secure, unknown, and duplicate session outcomes.

## Risks & Mitigations

- Remote auth scope creep — mitigate by defining only the `RemoteDecider` port and startup behavior in this spec.
- Protocol-specific auth errors become too broad — mitigate by allowing frontend-aware error rendering without changing canonical LLM event contracts.
- Single-user default migration surprises operators — mitigate with explicit startup visibility and tests for loopback defaults.
- API key leakage — mitigate with typed redacted fingerprint values and log/event tests.

## References

- `internal/stdhttp/auth/middleware.go` — existing HTTP auth adapter seam.
- `pkg/lipsdk/execview/views.go` — existing principal view consumed by runtime and plugins.
- `internal/core/runtime/executor_prepare.go` — legacy session and principal preparation flow.
- `internal/core/runtime/executor_prepare_secure.go` — secure-session preparation flow.
- `pkg/lipsdk/session/opener.go` and `internal/core/extensions/session_open.go` — fail-open session opener seam to avoid for mandatory auth events.
- Connect for Go documentation — future remote transport option, explicitly deferred in this design.
- Go `os/user` documentation — basis for OS identity provider abstraction.
