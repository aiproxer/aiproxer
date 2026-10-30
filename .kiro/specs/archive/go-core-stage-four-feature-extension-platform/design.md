# Design — Go core reimplementation stage four: advanced feature extension platform

Spec name: `go-core-stage-four-feature-extension-platform`

## Design goals

1. Preserve the Go rewrite’s small-core discipline.
2. Make the extension surface broad enough that advanced Python LIP behaviors can migrate later **without more core edits**.
3. Keep routing, capability checks, continuity, and streaming semantics core-owned.
4. Keep auth, policy, mutation, capture, memory, and steering logic plugin-owned.
5. Prevent the new feature platform itself from becoming a disguised god object.

---

## Why this stage matters now

The Go repo already has:

- a runnable `cmd/lipstd` standard distribution
- `internal/stdhttp` server wiring
- official frontend/backend plugin composition
- continuity store wiring
- a feature hook bus
- current hook interfaces for submit, request-part, response-part, and tool-reactor plugins

That is enough to keep moving for simple plugins.

It is **not enough** for the Python proxy’s broader “workflow improver” features, because many of them require request-wide context, private auxiliary model calls, bounded buffering, route roles, state, or traffic/capture seams that do not exist today as first-class contracts.

If we skip this stage and start migrating those features directly, the Go core will slowly absorb:

- workspace detection logic
- auth logic
- special-case request mutation
- special-case response buffering
- custom bookkeeping for each feature
- ad hoc capture/usage code
- per-feature session memory

That is exactly the path back to the Python maintainability trap.

---

## Current state to preserve

The current implementation already expresses several healthy ideas:

- `pkg/lipapi` canonical contracts
- `pkg/lipsdk/hooks` stable hook interfaces
- `internal/core/hooks.Bus` ordering and failure semantics
- `pluginreg.BuildFeatureHooks(...)` which merges enabled feature plugins into stable hook chains
- explicit standard-server wiring in `internal/stdhttp`
- core-owned routing and continuity

Those should be **extended**, not replaced by a magical framework.

---

## Feature classes we must be ready for

The Python LIP feature set spans several distinct classes.

### 1. Request/context shaping

Examples:

- auto append first prompt
- dynamic outbound rewrite
- stale tool-output compaction
- dynamic tool-output compression
- ProxyMem recall/injection
- secret redaction before upstream send
- context-window enforcement
- user-submit hooks

### 2. Tool policy and workspace safety

Examples:

- allowed/disallowed tools
- dangerous command protection
- file sandboxing
- project-root discovery
- command steering (pytest full suite, inline Python, test reminders)
- tool-event dynamic rewrites

### 3. Response shaping and gated control

Examples:

- inbound response rewrite
- think-tag fixups
- auto-continue/proceed cleanup
- quality verifier with inline recall
- future whole-response replacement policies

### 4. Auxiliary orchestration

Examples:

- verifier model calls
- future memory summarization calls
- auxiliary routing roles
- request branching with lineage

### 5. Identity and access

Examples:

- SSO-based user authentication
- principal-aware policy
- per-user statistics
- tenant-aware routing or policy later

### 6. Observability and evidence

Examples:

- four-leg usage accounting
- usage statistics generation
- session text capture
- CBOR wire capture
- redacted logging/export

These are **not one extension point**.
They are at least six different architectural concerns.

---

## Architectural target

### High-level shape

```text
Client
  |
  v
+------------------------------+
| stdhttp transport            |
| - request ID / trace         |
| - transport auth plugins     |
+--------------+---------------+
               |
               v
+------------------------------+
| frontend plugin              |
| - decode client protocol     |
| - encode client protocol     |
+--------------+---------------+
               |
               v
+--------------------------------------------------------------+
| core execution pipeline                                      |
|                                                              |
| 1. session open / identity attach                            |
| 2. submit hooks                                              |
| 3. tool catalog filters                                      |
| 4. request shapers / part hooks                              |
| 5. route hint providers                                      |
| 6. core route planner                                        |
| 7. attempt loop                                              |
| 8. response event hooks                                      |
| 9. tool reactors                                             |
| 10. completion gates                                         |
| 11. egress observers                                         |
+------------------+----------------------+--------------------+
                   |                      |
                   v                      v
        +---------------------+   +---------------------------+
        | backend plugin      |   | plugin services           |
        | - upstream invoke   |   | - state store             |
        | - canonical stream  |   | - workspace resolver      |
        +---------------------+   | - aux request client      |
                                  | - traffic/capture sinks   |
                                  +---------------------------+
```

### The key idea

The runtime becomes a **stage runner** over a stable set of typed extension points.

The core knows:

- when each stage runs
- what data is visible there
- whether the stage may mutate, reject, or only observe
- which services may be used

The core does **not** know:

- what “dangerous git command protection” means
- how quality verification is prompted
- how ProxyMem stores or summarizes memory
- how secrets are detected
- how session transcripts are formatted
- which tool names a particular operator wants to block

That knowledge stays in plugins.

---

## The extension model

## 1. Replace “feature hooks only” with a richer feature bundle

### Current model

Today a feature factory returns roughly:

- hook config
- lifecycles

### Target model

A feature factory returns a typed `FeatureBundle` (name indicative, exact Go type may differ):

```go
type FeatureBundle struct {
    SubmitHooks          []hooks.SubmitHook
    RequestPartHooks     []hooks.RequestPartHook
    ResponsePartHooks    []hooks.ResponsePartHook
    ToolReactors         []hooks.ToolReactor

    SessionOpeners       []session.Opener
    ToolCatalogFilters   []toolcatalog.Filter
    RequestTransforms    []request.Transform
    RouteHintProviders   []routehint.Provider
    CompletionGates      []completion.Gate
    TrafficObservers     []traffic.Observer
    CaptureSinks         []traffic.CaptureSink
    WorkspaceResolvers   []workspace.Resolver
    Lifecycles           []plugin.Lifecycle
}
```

The bundle contract should also carry explicit schema or version metadata so the SDK can add new extension-point classes without breaking existing feature plugins.

If a feature plugin does not participate in a given extension stage, its bundle simply leaves that slice empty and the runtime treats that stage as absent for that plugin.
No stage-specific fallback behavior is invented in core for missing handlers.

For the standard HTTP distribution, transport-specific extension types live **outside core** in a transport SDK package, for example:

```go
type HTTPFeatureBundle struct {
    AuthProviders []httpauth.Provider
    AdminMounts   []httpplugin.AdminMount
}
```

This preserves the transport/core separation.

### Why this is better than a generic `interface{}` bag

Because every extension point has:

- a documented stage
- a documented mutation scope
- a documented failure policy
- a typed context object
- explicit test coverage

The core stage runner owns the legal stage list and ordering.
Plugins may attach only to those documented stages; they do not create ad hoc runtime stages of their own.

That is what prevents “feature system” from turning into a loophole for uncontrolled coupling.

---

## 2. Introduce typed execution context views

The feature SDK must expose narrow views, not core objects.

### Principal view

```go
type PrincipalView struct {
    ID          string
    DisplayName string
    Roles       []string
    Claims      map[string]string
}
```

The exact fields may differ, but the principle is:

- generic identity
- no HTTP types
- no auth-provider-specific structs

### Session view

```go
type SessionView struct {
    SessionID   string
    ALegID      string
    IsNew       bool
    Labels      map[string]string
}
```

### Attempt view

```go
type AttemptView struct {
    TraceID     string
    BLegID      string
    AttemptSeq  int
    BackendID   string
    RouteRole   string
}
```

### Workspace view

```go
type WorkspaceView struct {
    ProjectRoot string
    DirtyTree   bool
    Markers     []string
    Labels      map[string]string
}
```

These views are read-only to plugins except through the extension point that is allowed to resolve or annotate them.

Shared services exposed to plugins should follow the same rule: capability-specific interfaces only, never a general-purpose service locator or direct access to mutable core structs.

---

## 3. Introduce a session-open stage

Some features are awkward if the first place they can run is “submit hook”.

Examples:

- auto-append first prompt
- project root discovery
- session-scoped state initialization
- future principal/session labels

### Stage contract

`SessionOpener` runs after identity is known but before request shaping.

Responsibilities:

- initialize plugin-scoped session state
- resolve workspace metadata if needed
- annotate session labels/metadata
- decide if the request is the first meaningful user turn in the session

This stage must **not** call providers directly.
If it needs model work, it must use the auxiliary-request service.

Identity enters this stage only through a canonical principal view produced by transport-auth integration.
The transport layer is responsible for translating transport-native auth results into that stable principal contract before transport-agnostic stages execute.

---

## 4. Separate tool catalog filtering from tool-event reaction

The Python feature set proves these are different concerns.

### Tool catalog filtering

Runs before the backend sees the request.
Responsibilities:

- remove disallowed tool definitions
- annotate filtered tools
- reconcile `tool_choice`

Examples:

- allow/block tool names
- persona-/principal-specific tool catalogs
- “read only” mode

This stage must complete before backend translation and tool-choice reconciliation are finalized so downstream adapters consume the post-policy tool set rather than raw pre-filtered tool definitions.

### Tool reactor

Runs when tool-use events appear in the canonical response/tool lifecycle.
Responsibilities:

- pass
- rewrite
- swallow
- replace

Examples:

- dangerous command protection
- sandbox policy
- pytest full-suite steering
- tool-output steering or rewrite
- command reminders

Keeping these separate avoids trying to use one surface for two different jobs.

Both contracts remain provider-agnostic.
Core and stable SDK packages must not depend on provider SDK event types to express tool policy or tool-reaction behavior.

---

## 5. Add request-wide shaping in addition to part hooks

Per-part hooks are too small for some future behaviors.

Examples that need a whole-call or history-aware view:

- stale tool-output compaction
- dynamic tool-output compression
- ProxyMem context injection
- first-prompt file append
- model-wide or phase-wide request rewrites
- secret redaction across the request

### Target

Add a request-wide transform stage with a contract like:

```go
type Transform interface {
    Handle(ctx context.Context, call *lipapi.Call, meta RequestMeta, svc Services) error
}
```

This is distinct from `SubmitHook`:

- `SubmitHook` is early, allowed to reject, good for annotations and coarse control
- `RequestTransform` is for canonical-call mutation with full visibility over messages/history/tools/options

`RequestPartHook` stays for localized, low-level per-part mutation.

This request-wide shaping stage is intentionally distinct from transport-auth and session-opening stages so whole-request mutation does not depend on transport-specific preprocessing or session bootstrap side effects.

---

## 6. Keep response event hooks for local changes, add completion gates for whole-response control

### Response event hooks

Good for:

- small text/event normalization
- per-event metadata annotation
- simple stream-safe rewrites

Examples:

- think-tag cleanup
- header/annotation tweaks
- narrow event normalization

### Completion gates

Required for:

- quality verifier
- inline recall on verifier steer
- future whole-response policy decisions
- future rewrite/classifier flows based on the full completion

### Completion-gate contract

A completion gate may request bounded buffering before first output.
Then it receives a `BufferedCompletion` plus services.
It returns a typed decision such as:

- `pass_original`
- `buffer_and_decide`
- `replace_with_alternative`
- `reject`
- `replay_original`
- `live_passthrough_fail_open`

Crucially:

- a completion gate runs before any downstream user-visible content is committed
- once the first user-visible content is emitted, gates may no longer replace that response
- gates are the only place where same-request auxiliary-model calls may influence the final visible completion

This preserves streaming correctness.

The decision model should be encoded as a typed outcome enum or result rather than loose strings at runtime, so pass-through, buffered decision, replacement, rejection, replay, and fail-open behavior remain explicit and testable.

---

## 7. Introduce an auxiliary-request client/service

This is the most important new service.

Without it, each advanced feature would invent its own side channel for subrequests and routing.

### Why we need it

Required by:

- quality verifier
- inline steering recall
- future ProxyMem summarization
- future route classifiers / rewrite helpers
- future policy classifiers

### Contract sketch

```go
type AuxRequest struct {
    Role              string   // verifier, memory, rewrite, primary, ...
    Visibility        string   // private, internal-debug, public
    ParentTraceID     string
    ParentALegID      string
    ParentBLegID      string
    DisablePlugins    []string // loop guard / scoped suppression
    Call              lipapi.Call
}

type AuxClient interface {
    Collect(ctx context.Context, req AuxRequest) (lipapi.Collected, error)
    Stream(ctx context.Context, req AuxRequest) (lipapi.EventStream, error)
}
```

### Rules

1. Plugins do not select backends directly. They request a role/hint.
2. Core routing decides how that role maps to a backend candidate set.
3. Auxiliary requests have lineage and are observable/capturable as auxiliary, not primary, traffic.
4. Recursive self-triggering is blocked by policy.
5. Auxiliary requests may be hidden from the client while still visible to observability plugins.

The aux client exposed to plugins is intentionally narrow.
Plugins do not receive direct access to runtime executors, backend clients, or provider-specific SDK objects.

This also gives a clean home for “routing of auxiliary requests”.

---

## 8. Introduce a plugin state service

State must be a service, not a reason to keep adding fields to continuity or the call model.

### Contract sketch

```go
type Scope string
const (
    ScopeRequest   Scope = "request"
    ScopeSession   Scope = "session"
    ScopePrincipal Scope = "principal"
    ScopeGlobal    Scope = "global"
)

type StateStore interface {
    Get(ctx context.Context, scope Scope, ns, key string, out any) (found bool, err error)
    Put(ctx context.Context, scope Scope, ns, key string, value any, ttl time.Duration) error
    Delete(ctx context.Context, scope Scope, ns, key string) error
    InspectTTL(ctx context.Context, scope Scope, ns, key string) (ttl time.Duration, found bool, err error)
}
```

The exact method set may differ, but the design should preserve narrow typed operations for read, write, delete, and expiry-aware inspection without exposing storage backend internals to feature plugins.

### Why separate state matters

Examples:

- “first full-suite pytest attempt” memory
- verifier counters and cooldowns
- cached workspace/project-root result
- per-session steering memory
- ProxyMem metadata
- future tool-reactor suppressions

The core should not gain per-feature storage structures for any of these.

---

## 9. Introduce workspace resolution as a shared service

The Python proxy’s safety features repeatedly depend on:

- project root
- repo markers
- dirty tree / VCS state
- path-boundary policy

The Go design must not let each safety plugin rediscover that differently.

### Design

A `WorkspaceResolver` chain runs early and yields a `WorkspaceView`.
Later plugins consume that view.

Possible derived metadata:

- `project_root`
- `root_detected_by`
- `repo_dirty`
- `allowed_parent_access`
- `workspace_kind`

This lets later plugins implement:

- sandboxing
- dangerous command protection
- path steering
- repo-safety policies

without knowing how discovery itself works.

---

## 10. Introduce four-leg traffic observation and capture

The Python usage system explicitly distinguishes:

- client to proxy
- proxy to backend
- backend to proxy
- proxy to client

The Go platform should make this a first-class extension seam, not a later patch.

### Traffic observer

For redacted / structured observation:

```go
type Leg string
const (
    LegCTP Leg = "client_to_proxy"
    LegPTB Leg = "proxy_to_backend"
    LegBTP Leg = "backend_to_proxy"
    LegPTC Leg = "proxy_to_client"
)
```

Observers receive:

- leg
- trace/session/attempt/backend/frontend/principal identifiers
- protocol
- content type
- structured or redacted body representation
- timing / size metadata

General observers are explicitly non-mutating.
If a component needs control behavior or privileged raw access, it uses a separate contract.

### Capture sink

Privileged sink for raw bytes when needed, e.g. CBOR wire capture.

This must be explicitly separate so raw access does not silently leak to general observers.

The same separation applies to enforcement-style sinks: published traffic contracts distinguish passive observers from control-capable capture or enforcement components.

### What this enables later

- usage accounting with verbatim vs mutated comparisons
- CBOR wire capture
- session text capture
- statistics aggregation
- audit/debug evidence

### Per-leg observation pipeline contract

| Leg | Privileged raw capture point | Redaction point | Structured observer point | Semantic meaning |
|---|---|---|---|---|
| client -> proxy | immediately after transport receipt, before frontend decode | after decode into canonical request and before general observation export | decoded request before request-shaping mutation | original client intent as received by the proxy |
| proxy -> backend | immediately before backend send, after backend request encoding | after final request shaping / tool filtering / route planning and before general export | final canonical request after mutation, before backend encoding | mutated upstream request selected for backend execution |
| backend -> proxy | immediately after backend bytes arrive, before backend decode | after decode into canonical events and before general export | canonical backend event stream before response hooks, tool reactors, and completion gates | original backend response before proxy-side mutation |
| proxy -> client | immediately before client write, after frontend egress encoding | after final completion-gate / response-mutation decisions and before general export | final client-visible canonical output after all mutation and gating, before egress encoding | what the proxy intentionally emits downstream |

For every leg, privileged raw capture happens before redaction, and general observers run only on redacted or structured views.
This ordering is what makes verbatim-vs-mutated comparisons and privilege boundaries auditable rather than implicit.

---

## 11. Define redaction boundaries

The feature set includes both capture and redaction.

That means the platform must choose, explicitly, which surfaces see raw content.

### Model

```text
raw boundary frame
    |
    +--> privileged capture sinks (opt-in)
    |
    v
redaction stage
    |
    +--> general observers / stats / transcript exporters
```

This gives a safe default:

- general observer plugins get redacted or structured data
- only explicitly privileged capture plugins get raw bodies
- diagnostics can reveal which plugins are privileged

That is essential for future secret-redaction work.

---

## 12. Keep routing core-owned, but add route hints and route roles

Some future plugins will need to say:

- “this is a verifier call”
- “this is a memory summarization call”
- “this request would prefer the cheap classifier backend”
- “this primary request carries a routing annotation”

The routing planner must stay the single source of truth.

### Design

Add a `RouteHintProvider` extension point that can annotate:

- route role
- preferred selector hint
- required capability tags
- budget hints

The core route planner then resolves those hints against:

- configured selectors
- health
- capability compatibility
- failover rules

Route hints remain advisory only.
They express plugin intent but never guarantee backend selection or bypass capability, policy, eligibility, or recovery logic owned by the core runtime.

This preserves the small-core principle:
plugins influence intent; core chooses execution.

---

## 13. Transport auth stays in `stdhttp`, not in core

We do need a place for SSO/authn/authz work.

We do **not** want HTTP auth concepts mixed into canonical call handling.

### Target

A transport-specific SDK package under `pkg/lipsdk/transport/http` (name indicative) defines:

- auth provider
- principal mapper
- optional admin mount contract

`stdhttp` runs those before frontend decode and passes a generic `PrincipalView` downstream.

This keeps:

- transport auth in transport layer
- protocol decode in frontend plugins
- orchestration in core
- policy logic in feature plugins

---

## 14. Diagnostics and inventory must surface extension truth

The platform will only stay maintainable if people can see what is installed.

### Inventory should expose

- feature plugin instance ID
- extension point classes it participates in
- ordering
- failure mode
- privileged capabilities:
  - raw capture access
  - auxiliary-request access
  - auth-provider role
  - completion-gate role

This is a maintainability feature, not a nice-to-have.

---

## 15. Brownfield rollout and hook compatibility strategy

This stage must land in an already-working codebase.
That means the design needs an incremental adoption path, not a flag-day rewrite.

### Migration strategy

1. Preserve the current hook bus and hook interfaces as the first compatibility layer.
2. Introduce `FeatureBundle` assembly in parallel with existing hook-chain construction.
3. Adapt current hook-only plugins into bundle form mechanically during registration or composition.
4. Move new seams into stage runners one concern at a time.
5. Retire compatibility shims only after the richer extension surface is proven by tests and reference plugins.

### Required guardrails

- unchanged frontend plugins, backend plugins, routing components, and existing feature plugins must preserve behavior while the new seams are introduced
- wrapper or adapter layers are acceptable for migration
- unrelated provider, routing, and transport packages must not require broad rewrites merely to adopt the new extension seams
- the current hook bus remains a supported registration path during migration so feature logic is not duplicated in competing extension systems

This keeps the rollout feasible while preserving the architectural target.

---

## 15B. Reload-friendly architecture assumptions

Dynamic runtime config reload is a separate future effort, not part of stage four.
However, the extension platform should avoid choices that would make reload support awkward or unsafe later.

### Assumptions for this stage

- runtime composition should be able to produce an immutable execution snapshot containing the active feature bundles, stage runners, and service bindings
- each request should execute against one stable snapshot for its full lifetime even if operators later swap configuration
- stage assembly, plugin wiring, and shared service facades should avoid hidden process-wide mutable globals
- plugin lifecycle hooks should be designed so future activation, deactivation, or replacement can be modeled explicitly rather than through ambient singleton state

### What this does not mean yet

This stage does not design:

- operator-triggered config reload APIs
- rollback policy for invalid config snapshots
- in-flight swap semantics across listeners, routes, or continuity stores
- reloadability guarantees for every backend, transport, or storage integration

The goal here is only to keep the extension architecture compatible with a later dedicated runtime-reload spec.

---

## 16. Package layout target

A concrete package proposal:

```text
pkg/lipsdk/
  hooks/                 # existing; preserved
  feature/               # feature bundle + common metadata
  session/               # session opener contracts
  request/               # request-wide transforms
  toolcatalog/           # tool definition filters
  routehint/             # route hint providers
  completion/            # buffered completion gates
  traffic/               # observers + privileged capture sinks
  state/                 # plugin state-store interface
  aux/                   # auxiliary request service
  workspace/             # resolver + workspace view
  transport/httpauth/    # stdhttp auth contracts

internal/core/
  extensions/            # stage runner / chain assembly
  execctx/               # principal/session/attempt/workspace context plumbing
  auxreq/                # runtime aux-request implementation
  state/                 # state store implementations
  traffic/               # observer fanout + redaction pipeline
  workspace/             # shared workspace resolver plumbing

internal/stdhttp/
  auth/                  # transport auth integration
  inventory/             # extension inventory output

internal/plugins/features/
  ... future real plugins ...
```

This layout keeps the extension surface explicit and layered.

---

## 17. Failure modes and safety rules

### Deterministic intra-stage ordering

Every extension stage uses the same ordering rule unless a stage-specific contract explicitly narrows it further:

1. explicit stage priority or order field (ascending)
2. plugin instance or bundle ID (ascending, stable)
3. registration sequence tie-break (ascending)

This gives one normative ordering contract for hooks, openers, transforms, filters, route hints, reactors, gates, observers, and capture sinks.
The same order must appear in diagnostics and inventory output so runtime behavior is reviewable.

### Hooks / transforms

Preserve current `fail_open` vs `fail_closed` semantics where they make sense.

For pre-backend mutation stages, the default runner behavior should remain compatible with the current hook bus: stage contracts declare whether handler errors fail closed, fail open with passthrough, or only drop that plugin's output while preserving the rest of the stage.

### Stage failure matrix

- transport auth: default fail-closed when an enabled auth provider errors; request may be rejected or challenged but not silently treated as authenticated
- session open / workspace resolution: default fail-open for optional enrichers, fail-closed only when the plugin or route policy marks the metadata as required
- submit hooks / tool catalog filters / request transforms: preserve explicit fail-open vs fail-closed semantics compatible with the current hook bus
- route hint providers: fail-open by dropping the hint and continuing with core-owned routing
- response part hooks / tool reactors: preserve existing hook-bus semantics with deterministic ordering and explicit rejection or rewrite outcomes
- completion gates: use the typed gate-outcome model; gate failure defaults to original completion or documented fail-open passthrough unless policy requires rejection
- traffic observers / capture sinks: fail-open only; they may emit diagnostics but must not take down the request path

### Observers

Default to fail-open.
They may log or emit diagnostics, but should not take down the request path unless explicitly configured and documented.

### Completion gates

Need their own typed decision/failure model because they are control points.

Suggested rules:

- verifier or rewrite gate error -> fail open to original completion unless policy says otherwise
- buffer overflow -> fail open to live passthrough
- no captured text / incomplete state -> replay original completion, do not invent behavior

### Capture sinks

Must never mutate request execution.
They are evidence writers, not policy engines.

---

## 18. Rules to prevent architectural backsliding

1. No future advanced feature may be added directly into core once a matching seam exists.
2. New feature requests must first answer: “Which existing seam should this use?”
3. If no seam fits, add or revise the seam first — do not special-case the feature.
4. Feature plugins may depend on stable SDK packages and shared services only.
5. Feature plugins must not import concrete backends, frontends, or core executor internals.
6. Auxiliary requests must use the shared aux-request service only.
7. Captures/statistics must use traffic/capture seams only.
8. Auth must enter through transport auth only.
9. Workspace safety features must use workspace resolution only.
10. State-bearing features must use the plugin state store only.

These rules are as important as the code.

---

## 19. Reference proof plan and stage-exit criteria

Stage four is not complete when the infrastructure exists on paper.
It is complete only when a small proof set demonstrates that the new seams are sufficient for future feature migration without more feature-specific core edits.

### Minimum proof set

| Proof plugin | Required seam(s) | What it proves | Minimum validation |
|---|---|---|---|
| session-start append proof | session opener + submit/request transform | first-turn session enrichment and request shaping can be implemented through the published seams | tests cover first-turn detection, deterministic ordering, and no core-specific feature branch |
| tool-policy proof | tool catalog filter + tool reactor + principal/workspace views | tool exposure and attempted tool-use enforcement are separate and both plugin-owned | tests cover tool removal, tool-choice reconciliation, blocked or rewritten tool events, and provider-agnostic contracts |
| workspace-safety proof | workspace resolver + tool reactor + state store | shared workspace metadata can drive safety behavior without rediscovering filesystem state per feature | tests cover resolver reuse, read-only workspace view, and state-backed policy memory |
| traffic-observer proof | traffic observer + capture sink + redaction stage | four-leg observation and privileged raw capture work with explicit boundaries | tests cover each leg's raw capture point, redacted observer point, and privilege separation |
| completion-gate auxiliary proof | completion gate + aux client + route roles + state store | bounded buffering, auxiliary lineage, and final-response control work without violating streaming invariants | tests cover replace/replay/pass-through outcomes, no change after first output, lineage visibility, and loop guards |

### Stage-exit gate

The stage should not be considered complete until all proof plugins satisfy these conditions:

1. the proof plugin is implemented using only the published seam plus composition-root registration
2. no new feature-specific branch is added to core orchestration for that proof
3. tests assert the relevant invariant for that seam, not only the happy path
4. diagnostics and inventory reveal the plugin's stage occupancy and privileged capabilities where applicable

### “No more core edits” validation path

The practical proof goal is not just that one plugin works.
It is that a second plugin of the same class could be added later by editing plugin code, tests, and registration only.
If a proof plugin still requires core logic unique to that feature class, the stage is not extension-complete yet.

---

## 20. Mapping the Python feature inventory to the new seams

| Python/LIP feature class | Primary seam(s) |
|---|---|
| auto append first prompt | session opener + submit/request transform |
| dynamic outbound rewrite | request transform + request part hook |
| dynamic inbound rewrite | response part hook or completion gate |
| stale tool-output compaction | request transform |
| dynamic tool-output compression | request transform + aux client later if summarization is used |
| dangerous command protection | tool reactor + workspace view + state store |
| sandboxing | workspace resolver + tool reactor + tool catalog filter as needed |
| allowed/disallowed tools | tool catalog filter + tool reactor |
| project-root discovery | workspace resolver |
| pytest/full-suite steering | tool reactor + state store |
| inline Python / test reminder steering | tool reactor + state store |
| ProxyMem recall/injection | session opener + request transform + aux client + state store |
| quality verifier | completion gate + aux client + state store |
| SSO auth | stdhttp transport auth + principal propagation |
| usage accounting / stats | traffic observer |
| session text capture | traffic observer or capture sink |
| CBOR wire capture | privileged capture sink |
| secret redaction | redaction stage + request/response transforms |
| routing of auxiliary requests | aux client + route roles + route hint provider |

This table is the practical heart of the stage.

---

## 21. Why this design avoids the Python trap

Because it forces every advanced behavior to answer four questions explicitly:

1. **At which stage does it run?**
2. **What may it see?**
3. **What may it mutate or observe?**
4. **Which stable service may it use?**

The Python project became hard to change partly because those answers were often implicit.
Stage four makes them architectural contracts.
