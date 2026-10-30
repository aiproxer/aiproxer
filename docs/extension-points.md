# Extension points

The extension platform lets advanced proxy behavior live in plugins without widening core business logic. Core owns stage order and canonical validation. Plugins provide narrow handlers through `pkg/aipsdk` and are assembled into a `feature.FeatureBundle`.

## Legal pipeline

The canonical stage IDs are defined in `pkg/aipsdk/feature/stages.go` and surfaced through diagnostics inventory.

| Order | Stage ID | Owner | Role | Primary SDK surface |
| --- | --- | --- | --- | --- |
| 1 | `transport_authentication` | stdhttp / auth providers | reject | `pkg/aipsdk/transport/httpauth`, `pkg/aipsdk/auth` |
| 2 | `session_open` | core + feature plugins | mutate | `pkg/aipsdk/session.Opener` |
| 3 | `secret_guard` | core + feature plugins | mutate/reject | `pkg/aipsdk/secretguard.Guard` (after BeginTurn, before FE checkpoint) |
| 4 | `submit_request` | core hook bus + feature plugins | mutate/reject | `pkg/aipsdk/hooks.SubmitHook` |
| 5 | `tool_catalog_filter` | feature plugins | mutate | `pkg/aipsdk/toolcatalog.Filter` |
| 6 | `request_wide_shaping` | feature plugins + brownfield hooks | mutate | `pkg/aipsdk/request.Transform`, request-part hooks |
| 7 | `pre_request_admission` | feature plugins | reject | `pkg/aipsdk/prerequest.Handler` |
| 8 | `route_hinting` | feature plugins advise, core decides | observe | `pkg/aipsdk/routehint.Provider` |
| 9 | `candidate_attempt_transform` | feature plugins | mutate/reject | `pkg/aipsdk/request.AttemptTransform` |
| 10 | `attempt_lifecycle` | core | observe | attempt lineage, route observers, diagnostics |
| 11 | `stream_event_mutation` | core hook bus + feature plugins | mutate | response-part hooks |
| 12 | `tool_event_reaction` | core hook bus + feature plugins | mutate/reject | `pkg/aipsdk/toolpolicy.Policy`, then `hooks.ToolReactor` |
| 13 | `completion_gating` | feature plugins | replace | `pkg/aipsdk/completion.Gate` |
| 14 | `final_stream_observation` | feature plugins | observe | `pkg/aipsdk/response.StreamObserverFactory` |
| 15 | `traffic_observation` | feature plugins | observe | `traffic.Observer`, `usage.Observer`, `RawCaptureSink`, `Redactor` |
| 16 | `egress_encoding` | frontend adapters | mutate | frontend encoders |

`attempt_lifecycle` and `egress_encoding` are legal inventory stages even though feature bundles do not own extension planes for them.

## Seam taxonomy

Use the narrowest seam that matches the behavior:

| Need | Use | Do not use |
| --- | --- | --- |
| Accept/reject transport identity | `transport/httpauth` provider and auth renderer | canonical request hooks |
| Add first-session metadata | `session.Opener` | frontend-specific request parsing |
| Rewrite the whole canonical call | `request.Transform` | route planner or backend adapter branches |
| Rewrite individual request/response parts | request-part or response-part hooks | provider SDK payload mutation in core |
| Filter exposed tools | `toolcatalog.Filter` | tool reactors alone |
| Allow/deny observed tool-call lifecycle events before reactors | `toolpolicy.Policy` | skipping catalog shaping or relying only on reactors |
| Enforce observed tool calls/results | `hooks.ToolReactor` | frontend-specific stream logic |
| Prefer a route/model role | `routehint.Provider` | direct backend selection from plugin code |
| Inspect or replace a full completion | `completion.Gate` | buffering in frontend encoders |
| Record traffic | `traffic.Observer` | mutation hooks |
| Record canonical usage deltas | `usage.Observer` | mutation hooks |
| Capture raw or canonical payloads | `traffic.RawCaptureSink` plus `traffic.Redactor` | ad hoc file writes in core |
| Store plugin state | `state.Store` through `state.BindPlugin` | package globals |
| Resolve workspace/project context | `workspace.Resolver` | reading process CWD inside arbitrary hooks |
| Call a verifier/model privately | `auxiliary.Client` | importing executor/backend internals |

Hooks mutate or decide. Observers record. Stores persist. Resolvers discover context. Auxiliary clients call other models or backends under core policy. Keep these concepts separate.

## Current implementation status

Implemented and wired in the standard runtime:

- submit hooks, request-part hooks, response-part hooks, tool-call policies, tool reactors;
- session openers;
- workspace resolvers;
- tool catalog filters;
- request transforms;
- route hint providers;
- completion gates;
- tool-call policies (`pkg/aipsdk/toolpolicy`), traffic observers, usage observers (`pkg/aipsdk/usage`), redactors, and raw capture sinks;
- lifecycle hooks during composition.

Partially available or intentionally conservative:

- `state.Store` exists as an SDK facade, but the default request snapshot uses `state.DisabledStore` unless a composition path binds a concrete store.
- `auxiliary.Client` exists as an SDK facade, but the default request snapshot uses `auxiliary.DisabledClient` until auxiliary execution is explicitly wired.
- Tool catalog filtering remains distinct from tool event reaction; policy (`toolpolicy.Policy`) runs immediately before tool reactors on the same stage runner ordering rules.

## Ordering and failure policy

Stage runners materialize handlers in stable order using each handler's `Order` value and ID tie-breaking where the SDK package provides sorting. Stage-specific failure mode controls fail-open versus fail-closed behavior. Fail-open errors are logged and counted; fail-closed errors reject the request or stream according to the stage.

Every mutation stage must preserve canonical validity. If a plugin needs to produce provider-specific payloads, that plugin belongs at the adapter edge, not in the canonical extension pipeline.

## Diagnostics inventory

Diagnostics inventory should show active feature plugins, legal stages, occupied stages, elevated privileges, auth providers, observers, redactors, raw capture sinks, completion gates, auxiliary-use flags, and lifecycle contributions. Treat inventory as an operator review surface for plugin capability and trust boundaries.

For the **`aipstd inventory`** CLI workflow over stub configs (no listener), see [`docs/dogfood-local.md`](dogfood-local.md).

## When to add a seam

Add a new SDK seam only when all are true:

- the behavior is needed by more than one feature or is a clear, recurring feature class;
- existing seams would overload responsibilities or require core/provider leakage;
- the interface can be narrow and provider-neutral;
- ordering, failure mode, timeout/cancellation, diagnostics, and revalidation semantics can be stated;
- tests can prove core imports no concrete plugin and plugins import no core internals.
