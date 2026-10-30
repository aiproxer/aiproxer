# Requirements — Go core reimplementation stage four: advanced feature extension platform

Spec name: `go-core-stage-four-feature-extension-platform`

## Goal

Prepare the Go proxy architecture to support the **advanced, UX-improving proxy features** that made the Python LIP powerful for agentic coding workflows, while **preventing those features from leaking responsibilities back into the core**.

This stage is intentionally **not** a one-shot migration of every Python-only feature.

It is the stage where the Go codebase must become **extension-complete** enough that the following classes of features can be added later as plugins **without editing the proxy core again**:

- context and request shaping
- tool policy and tool-call steering
- project/workspace safety enforcement
- auxiliary-model workflows such as quality verification
- traffic observation, accounting, and capture
- authentication and user identity integration
- session-scoped and cross-session memory helpers

## Stage-four thesis

The current Go codebase already has the right starting direction:

- a runnable standard distribution server
- explicit frontends and backends
- canonical request/event contracts
- routing and continuity
- a feature hook bus with submit hooks, request-part hooks, response-part hooks, and tool reactors

That is enough for **simple** feature plugins.

It is **not yet enough** for the broader Python LIP feature family, because many of those behaviors need at least one of the following that do not yet exist as first-class extension seams:

- authenticated user/principal context
- session-start and workspace-resolution hooks
- request-wide or history-wide transformation stages
- tool catalog filtering before the model sees tool definitions
- completion-wide interception with buffering and replacement
- auxiliary internal requests with lineage and routing roles
- namespaced plugin state with TTL
- four-leg traffic observation and capture
- redaction boundaries for observers and captures

If we start migrating advanced Python features before those seams exist, the Go rewrite will drift back toward the same coupling and maintainability trap that triggered the rewrite in the first place.

---

## In scope

- define the **full extension surface** needed for future advanced proxy features
- preserve and absorb the current hook bus into a richer stage-aware extension model
- introduce typed plugin-facing request/session/principal/workspace context views
- introduce plugin-scoped state and auxiliary-request services
- introduce four-leg traffic observation / capture seams
- introduce completion-gate seams for features that need whole-response control
- introduce transport-auth seams for the standard HTTP distribution
- prove the new seams with a small set of reference plugins
- keep the core provider-agnostic and plugin-independent

## Out of scope

- implementing the full production version of every advanced Python feature
- adding new vendor API flavors
- UI / admin console work
- dynamic loading with Go's native `plugin` package
- out-of-process plugin hosting
- broad protocol-fidelity expansion unrelated to the extension platform

---

## Functional requirements

### R1 — the standard feature plugin model must expand from “hook config” to a typed extension bundle

The current feature plugin model must be generalized so a feature plugin can register multiple kinds of extension points, not just the current hook-chain types.

#### Acceptance criteria

- the stable SDK exposes a typed `FeatureBundle` (or equivalent) rather than only a merged hook config
- the bundle contract is versionable so new extension point types can be added without breaking existing plugin registrations
- the existing hook types remain first-class and are carried inside that bundle
- the standard distribution can build enabled feature plugins without the core importing concrete feature packages
- adding a future feature plugin does not require editing executor/core orchestration code
- if a feature plugin declares no handlers for a given stage, the runtime treats that stage as absent for that plugin without changing behavior for other plugins

### R2 — the execution pipeline must have explicit, documented extension stages

The runtime must expose named extension stages with documented order and mutation scope.

At minimum the pipeline must distinguish:

1. transport authentication / identity attachment (standard HTTP layer)
2. session open / session context resolution
3. submit-time request enrichment / rejection
4. tool catalog filtering
5. request-wide shaping and history/context shaping
6. route hinting
7. attempt lifecycle hooks / observers
8. stream event mutation
9. tool event reaction
10. completion gating / buffering / replacement
11. traffic observation and capture
12. egress encoding

#### Acceptance criteria

- stage order is documented and covered by tests
- each stage specifies whether it may mutate, reject, observe, or replace
- deterministic ordering is preserved within each stage (order, ID, registration tie-break)
- the core runtime remains small and stage-driven instead of feature-driven
- the core runtime owns the legal extension stages and their order, and plugins attach only to documented stages rather than inventing ad hoc runtime stages

### R3 — plugin-facing context must be explicit and narrow

Plugins must receive typed views over request/session/principal/workspace data, not core internals.

#### Acceptance criteria

- plugins do not receive core config structs, backend instances, DI containers, or transport globals
- plugin-facing metadata includes only stable views such as trace IDs, attempt lineage, session identity, principal identity, workspace info, and annotations
- official provider SDK types never appear in feature plugin SDK contracts
- the canonical `lipapi.Call` stays provider-agnostic and is not polluted with transport-only concerns
- plugin-facing context views and services are exposed through narrow read-only or capability-specific contracts rather than general-purpose service locators or mutable core structs

### R4 — the architecture must support transport-layer authentication and principal propagation

The standard HTTP distribution must support future SSO and authn/authz features without pushing HTTP auth logic into the core runtime.

#### Acceptance criteria

- `stdhttp` exposes a transport-auth extension seam that can authenticate incoming requests and attach a generic principal/claims object
- the core runtime sees only a generic principal view, not HTTP-specific details
- auth plugins can reject, challenge, or annotate requests before frontend decode as appropriate for the transport
- frontends do not each reimplement authentication policy
- transport-auth extensions translate transport-native identity results into a stable canonical principal contract before transport-agnostic stages execute

### R5 — the architecture must support session opening and workspace/project-root resolution as shared services

Features such as dangerous-command protection, sandboxing, and session-start enrichers need shared session and workspace knowledge.

#### Acceptance criteria

- the runtime exposes a session-opening seam that can initialize or annotate session-scoped context before request shaping proceeds
- the runtime exposes a workspace-resolution seam that can determine project root and related safety metadata
- workspace information is cached and made available through a read-only workspace view
- project-root discovery is not embedded inside individual future safety features
- tool safety features can depend on workspace metadata without importing filesystem logic from core internals

### R6 — the architecture must support plugin-scoped state with namespace and TTL

Many advanced Python features depend on state that is request-scoped, session-scoped, principal-scoped, or global.

Examples:

- first-attempt vs second-attempt steering
- quality-verifier counters
- remembered tool-steering state
- cached workspace/project-root resolution
- ProxyMem-related metadata

#### Acceptance criteria

- the runtime exposes a stable state-store service for feature plugins
- state is namespaced by plugin
- state supports TTL and explicit scope selection (`request`, `session`, `principal`, `global`)
- feature authors do not need new core tables or custom core fields for each new feature
- the state service is testable with deterministic fakes
- the state service exposes narrow typed operations for read, write, delete, and expiry-aware inspection without exposing storage backend internals to feature plugins

### R7 — the architecture must support auxiliary internal requests with lineage and routing roles

Advanced features such as quality verifier, future ProxyMem summarization, and steering/rewrite helpers need private internal subrequests.

#### Acceptance criteria

- the runtime exposes an auxiliary-request client/service to feature plugins
- auxiliary requests use the canonical request model and canonical event stream
- each auxiliary request carries lineage linking it to the parent request/attempt
- auxiliary requests carry an explicit route role (for example: `verifier`, `memory`, `rewrite`, `primary`)
- routing remains core-owned; plugins may request a role or hint, not directly bypass routing policy
- recursion/loop guards exist so a plugin cannot accidentally trigger itself indefinitely
- no auxiliary flow may alter already-emitted downstream content
- the auxiliary-request service is exposed as a narrow plugin capability rather than direct access to runtime executors, backend clients, or provider-specific SDK objects

### R8 — the architecture must support completion-wide gates, not only per-event mutation

Some advanced behaviors require holding or buffering an entire completion before deciding what the client should see.

Examples:

- quality verifier with inline recall
- future response-wide cleanup or policy gates
- future completion replacement driven by auxiliary calls

#### Acceptance criteria

- a completion-gate extension point exists in the SDK/runtime
- a gate can request bounded buffering before first client-visible output
- a gate can pass through, replace, reject, or replay a completion according to a typed decision contract
- memory and buffering limits are explicit and enforced
- overflow behavior is deterministic and documented (typically fail-open/live passthrough)
- streaming semantics remain correct and test-covered
- typed gate outcomes distinguish pass-through, buffered decision, replacement, rejection, and replay behavior

### R9 — the architecture must support two-layer tool policy: tool catalog filtering and tool event reaction

The Python feature set includes both “do not expose certain tools to the model” and “block/rewrite specific attempted tool calls”.

#### Acceptance criteria

- a tool catalog filter stage exists before backend translation
- tool catalog filters can remove or annotate tool definitions and reconcile tool-choice fields
- the existing tool reactor stage remains available for tool-event enforcement/rewrite
- model/agent/principal/workspace/session metadata can influence tool policy without coupling tool policy to core
- a future “allowed/disallowed tools” plugin can be implemented without core edits
- tool catalog filtering completes before backend translation and tool-choice reconciliation so downstream adapters consume the post-policy tool set
- tool-event reaction contracts remain provider-agnostic and do not require provider SDK event types in core or SDK packages

### R10 — the architecture must support four-leg traffic observation and capture

The Python proxy tracks and captures both original and mutated traffic at four measurement points:

- client to proxy
- proxy to backend
- backend to proxy
- proxy to client

This must become a first-class plugin seam in Go so future usage accounting, session text captures, and CBOR wire captures do not get baked into the core.

#### Acceptance criteria

- the runtime and standard distribution expose four-leg traffic observation/capture seams
- observers can correlate frames with trace ID, session, attempt, backend instance, frontend, and principal
- the design supports separate inbound/outbound token accounting and verbatim vs mutated comparisons
- CBOR wire capture, text transcript capture, and usage statistics can be implemented as plugins using those seams
- observers are explicitly categorized as mutating vs non-mutating; general observers cannot silently become control logic
- published observation contracts distinguish non-mutating observers from control-capable capture or enforcement sinks

### R11 — the architecture must support redaction boundaries

The system must make it explicit which extension points may see raw traffic and which only receive redacted/structured views.

This is required so future secret-redaction and capture features do not become a security footgun.

#### Acceptance criteria

- the design distinguishes general observers from privileged capture sinks
- a redaction seam exists before general observation/export
- secret-redaction features can run without needing to modify core logging/capture code
- privileged raw-access surfaces are opt-in and diagnosable

### R12 — request and response mutation must remain explicit and layered

The Python proxy includes outbound rewrites, inbound rewrites, auto-appended first prompts, and other shaping features.
The Go platform must support these without collapsing back into an all-knowing core request processor.

#### Acceptance criteria

- submit-time whole-call mutation remains supported
- request-wide shaping and per-part mutation remain separate concerns
- response event mutation remains separate from completion-wide gates
- future outbound and inbound rewrite plugins can be implemented without changing backend/frontend code
- the mutation order between session-start appenders, request shapers, part mutators, and completion gates is documented and deterministic
- request-wide shaping remains distinct from transport-auth and session-opening stages so whole-request mutation does not depend on transport-specific preprocessing

### R13 — routing hints must remain advisory and core-owned routing must remain authoritative

Advanced plugins may need to influence where a request should go, especially for auxiliary requests, but routing logic must not be duplicated inside plugins.

#### Acceptance criteria

- plugins can provide route hints or route roles
- the routing planner makes the final selection
- capability checks and health checks remain core responsibilities
- weighted routing, failover, and B2BUA-like pre-output recovery remain correct with plugin-provided hints
- route hints are advisory only and do not guarantee backend selection or bypass capability, policy, or eligibility checks owned by the core runtime

### R14 — plugin inventory and diagnostics must show the active extension surface

As the number of plugin seams grows, operators and developers must be able to see which feature plugins are installed, what stages they occupy, and whether any are privileged.

#### Acceptance criteria

- diagnostics/inventory output lists enabled feature plugins and their extension-point classes
- order and failure mode are visible where applicable
- privileged capabilities (for example raw capture access or auxiliary-request ability) are visible
- the inventory helps reviewers verify that advanced logic is not hidden in core code

### R15 — the current hook bus must be preserved but absorbed into the richer platform

The existing submit/request-part/response-part/tool-reactor model is a good start and must not be thrown away.

#### Acceptance criteria

- current hook interfaces remain supported
- they are modeled as part of the richer extension platform rather than a competing mechanism
- existing noop/reference feature plugins continue to work or migrate mechanically
- the extension platform upgrade has a documented migration path from the current feature factory shape
- existing hook-only feature plugins can keep registering through the current bus during migration without duplicating feature logic in a parallel extension mechanism

### R16 — reference plugins must prove the seams before real feature migration starts

This stage is only successful if it proves that the new architecture really can host future advanced features.

#### Acceptance criteria

At least the following reference plugins (or equivalent proofs) exist by the end of the stage:

- a session-start / submit plugin proving auto-append-style behavior
- a tool-policy plugin proving tool catalog filtering plus tool-event enforcement
- a workspace-aware safety plugin proving workspace resolution + tool safety context
- a traffic observer or capture plugin proving four-leg observation
- a completion-gate plugin proving auxiliary-request + buffered replacement flow

The goal is not production feature depth.
The goal is proof that those feature classes can land **without more core edits**.

---

## Quality requirements

### Q1 — maintainability is the primary success metric

This stage is successful only if it reduces the probability that future feature work recreates Python-style coupling and monolith growth.

#### Acceptance criteria

- the design introduces reusable seams instead of per-feature exceptions
- the spec explicitly forbids pushing new advanced feature logic into core packages once the seam exists

### Q2 — the core must stay small, provider-agnostic, and layered

#### Acceptance criteria

- core packages do not import concrete feature plugins
- core packages do not import official provider SDKs
- transport auth stays in transport/server layer
- provider-aware logic stays in backend/frontend plugins
- advanced UX/security/observability behavior stays in feature plugins

### Q3 — no “god pipeline” may emerge

The next request processor must not become a new giant switchboard.

#### Acceptance criteria

- stage runners are split by concern
- files and functions stay within documented size/complexity budgets
- tests assert behavior at stage boundaries

### Q4 — TDD is mandatory

Every new seam in this stage must land behind tests first.

#### Acceptance criteria

- new extension seams are validated through unit or composed integration tests without real provider network dependencies
- tests cover stage ordering, mutation boundaries, rejection behavior, buffering behavior, routing influence, and lineage preservation where those concerns apply
- deterministic fakes or fixtures exist where state, time, randomness, or buffering affect behavior
- tests are treated as the authority for extension-stage behavior rather than narrative documentation alone

### Q5 — migration to future features must be deliberate

The stage must document how later feature work should choose the correct seam rather than invent a new one.

#### Acceptance criteria

- the design includes a feature-to-seam mapping table
- plugin authors have a clear place to attach new advanced behaviors

### Q6 — brownfield adoption must be incremental

This stage must be adoptable on top of the existing codebase without a flag-day rewrite of unrelated packages.

#### Acceptance criteria

- the extension-platform rollout can proceed incrementally on top of the existing hook bus and runtime pipeline
- when a new seam supersedes a hook-only pattern, the platform provides a compatibility path or adapter so existing behavior remains stable during migration
- unchanged frontend plugins, backend plugins, routing components, and existing feature plugins preserve behavior while the new seams are introduced
- wrapper or adapter layers may be added for migration, but the stage does not require broad rewrites of unrelated provider, routing, or transport packages merely to adopt the new seams

### Q7 — extension seams should remain compatible with future dynamic config reload

This stage does not implement runtime config reload, but it should avoid architectural choices that would make later reload support unnecessarily hard.

#### Acceptance criteria

- extension-platform composition should be able to bind against an immutable runtime configuration snapshot rather than process-wide mutable globals
- a request should be able to execute against a stable plugin/bundle/stage configuration for the lifetime of that request even if runtime configuration changes later
- feature-plugin lifecycles and stage assembly should avoid hidden global mutable state that would prevent safe future activation, deactivation, or replacement during runtime
- this stage may document reload-friendly assumptions, but it shall not expand scope into full dynamic config reload semantics, operator APIs, or hot-reload orchestration
