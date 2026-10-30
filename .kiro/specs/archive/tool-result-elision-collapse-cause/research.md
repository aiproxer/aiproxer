# Research

Evidence gathered on `origin/main` (worktree `feat-tool-result-elision-collapse-cause`) for the design in `design.md`.

## 1. The diagnostic's structural inputs have no production publisher

```
rg -n "ObserveSeamDigests|NoteStructuralCause" --type go -g '!*_test.go'
internal/infra/metrics/prompt_cache_collapse.go:164  (doc comment)
internal/infra/metrics/prompt_cache_collapse.go:170  (definition)
internal/infra/metrics/prompt_cache_collapse.go:233  (doc comment)
internal/infra/metrics/prompt_cache_collapse.go:248  (definition)
```

Only test files call them. The only cause ever published today is the configuration generation:

```
rg -n "NoteConfigGeneration" --type go -g '!*_test.go'
internal/infra/runtimebundle/candidate_compile.go:233  ps.Metrics.PromptCacheCollapse.NoteConfigGeneration()
```

The detector is live in production: `internal/infra/runtimebundle/candidate_compile.go:167` chains it into the usage
observer chain.

## 2. The cause whitelist rejects anything that is not already a constant

`internal/infra/metrics/prompt_cache_collapse.go:170-184`: an unknown label is ignored, and a note for a key that does
not exist yet is dropped. The second behaviour is asserted by
`internal/infra/metrics/prompt_cache_collapse_test.go:308` ("note for an absent key is ignored"), so it is intended.

## 3. The key is created by the usage path

`internal/infra/metrics/prompt_cache_collapse.go:358-390` (`keyState`) creates the key. Its callers are `observe()`
(from `OnUsage`) and `ObserveSeamDigests`. `NoteStructuralCause` only reads the map.

## 4. The healthy branch clears the note

`internal/infra/metrics/prompt_cache_collapse.go:322-334`: a healthy sample sets `s.healthyAt`, `s.healthyGen` and clears
`s.note`. `attribute()` (`:342-353`) prefers the note over the configuration generation and the keep-warm expiry.

## 5. The elision transform runs once per request, before candidate selection

- `internal/plugins/features/toolresultelision/transform.go:18` — `TransformOrder = 900`.
- `internal/core/extensions/request_transform.go:60` — the request-wide stage runs the chain.
- `internal/core/runtime/executor_prepare_secure.go:683` — `RunRequestTransformStage` runs in request preparation, before
  route planning and candidate attempts.

## 6. The stage runner owns the commit boundary

`internal/core/extensions/decision_runner.go:44-72` (`runBoundedCall`): with a non-zero budget the runner clones the
live call synchronously, runs the provider against the clone in a bounded goroutine, and commits the clone back only when
`!res.TimedOut && !res.ParentCanceled`. A worker that keeps running after the deadline therefore mutates a discarded
clone: the live request keeps neither the change nor anything written on it.

`internal/core/extensions/attempt_transform.go:88` uses the same helper for the candidate attempt stage.

## 7. Why the A-leg identifier is not a per-request identity

`internal/core/securesession/app/manager.go:275-340` (`beginResume`): a resumed secure session loads the existing record
and returns it, keeping the record's A-leg, while allocating a new `TurnID`. Two different client turns of one resumed
session therefore share an A-leg identifier.

## 8. Why the trace identifier is not a per-request identity

`internal/core/runtime/executor_prepare_secure.go:435-439`: when the request carries no id, the trace id is derived from
the call by `diag.StableCallID` (`internal/core/diag/stable.go:28-36`). Two identical requests therefore share a trace
id.

## 9. Why the session is not a per-request identity

The usage event's session identity is the canonical call's `Session.CorrelationID()` (`pkg/lipapi/call.go:40-45`), while
the publication side sees a session view (`pkg/lipsdk/session/view.go`). The authoritative session id can be bound later
in the request, and the per-event and terminal usage paths do not derive the session identically.

## 10. The leg is the identity that holds

`internal/core/runtime/response_pipeline_observations.go:469-479`: the usage event carries the leg's own `BLegID`, plus
the `ALegID`, `SessionID`, `BackendID` and `Model` of the key. A label that rides that sample is bound to exactly one
request.

## 11. The carrier options and why they were rejected

- `lipapi.Call.Extensions` (`pkg/lipapi/call.go:90`) is forwarded to the backend as `ext.<key>`
  (`pkg/lipsdk/backendplugin/invocation_meta.go:141-152`), so a marker there becomes wire-visible.
- `lipapi.Call.SemanticExtensions` participates in protocol requirements
  (`pkg/lipapi/requirements.go:329-334` adds `CapabilityOpaqueExtensions` and an extension requirement), so a carrier
  there would change backend admission.
- `lipapi.Message.Metadata` is proxy-owned and `json:"-"`, but it is per message and the elision may target items
  (`el.Loc.IsItem`), and `lipapi.Item` has no metadata carrier.
- `lipapi.Invocation` is proxy-owned and `json:"-"`, but it describes the invocation, not the request shaping.
- `lipapi.Call` already carries proxy-owned, non-wire fields (`MaxPendingWireEvents`), so one more bounded label is the
  smallest carrier that covers both canonical shapes.

## 12. Cost of the digest feed (why it is deferred)

`ObserveSeamDigests` expects one SHA-256 digest per request message, computed by the caller. Producing them means
hashing the whole model-bound request on every request — O(prompt bytes) per request — for a diagnostic that fires only
after five consecutive cold legs. The collapse diagnostic has thresholds but no enable switch
(`internal/core/config/prompt_cache_collapse.go`), so there is no gate to put the cost behind today. The digest feed
therefore needs its own cost decision and configuration gate, and it is not a prerequisite for the cause path.
