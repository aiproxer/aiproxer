# V1 Code Review Status Audit

Date: 2026-04-21
Repository: `go-llm-interactive-proxy`
Source review: [`v1_code_review.md`](v1_code_review.md)

## Purpose

This document is a current-state audit of every issue raised in [`v1_code_review.md`](v1_code_review.md), based on direct inspection of the active code path and targeted test execution on the repository state reviewed in this session.

It is intended to answer one question precisely:

> Which review findings are already fixed, which are only partially addressed, and which remain open?

---

## Verification performed

### Code paths inspected

- `internal/core/runtime/executor.go`
- `internal/core/runtime/app.go`
- `internal/core/routing/model_only.go`
- `internal/stdhttp/wire.go`
- `internal/stdhttp/mount.go`
- `internal/pluginreg/*`
- `internal/plugins/frontends/*`
- `internal/plugins/backends/*`
- `internal/core/config/*`

### Focused tests executed

```bash
go test ./internal/core/runtime ./internal/core/routing ./internal/stdhttp ./cmd/lipstd ./internal/plugins/frontends/openairesponses ./internal/plugins/frontends/openailegacy ./internal/plugins/frontends/anthropic ./internal/plugins/frontends/gemini
go test ./internal/pluginreg ./internal/core/config
```

Result: passing.

---

## Status legend

- `Fixed`: the review concern is addressed in the active implementation, with supporting code and/or tests.
- `Partially fixed`: the implementation moved in the right direction, but the review concern is not closed end-to-end.
- `Open`: the issue still materially exists.
- `N/A / advisory`: maintainability or future-direction concern rather than a concrete defect; not necessarily expected to be fully closed in v1.

---

## Finding-by-finding audit

### C0.1 — capability downgrades and request mutations are sticky across retries

Status: `Fixed`

Reasoning:

- The executor now snapshots a post-submit immutable baseline via `baseline := lipapi.CloneCall(work)`.
- Each backend attempt derives a fresh call from that baseline with `attempt := lipapi.CloneCall(baseline)` or `attempt := lipapi.CloneCall(s.baseline)`.
- Negotiated downgrades and request-part hook mutations are applied to the attempt-local copy, not persisted as the retry baseline.

Evidence:

- [`internal/core/runtime/executor.go#L135`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L135)
- [`internal/core/runtime/executor.go#L138`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L138)
- [`internal/core/runtime/executor.go#L160`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L160)
- [`internal/core/runtime/executor.go#L425`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L425)
- [`internal/core/runtime/executor_test.go#L642`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor_test.go#L642)

Notes:

- This is one of the most important semantic fixes from the review.

---

### C0.2 — hook metadata contracts exist, but the executor passes empty metadata

Status: `Fixed`

Reasoning:

- Submit hooks now receive `SubmitMeta{TraceID: traceID}`.
- Request-part hooks now receive populated `TraceID`, `ALegID`, `BLegID`, and `AttemptSeq`.
- Response-part hooks and tool reactors receive fully populated per-attempt metadata through `recvHookMeta()`.

Evidence:

- [`internal/core/runtime/executor.go#L135`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L135)
- [`internal/core/runtime/executor.go#L214`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L214)
- [`internal/core/runtime/executor.go#L313`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L313)
- [`internal/core/runtime/executor_test.go#L564`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor_test.go#L564)
- [`internal/core/runtime/executor_test.go#L611`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor_test.go#L611)

---

### C0.3 — `routing.max_attempts` is dead configuration today

Status: `Fixed`

Reasoning:

- The executor now carries `MaxAttempts`.
- It derives an attempt budget with `effectiveMaxAttempts()`.
- Both initial opens and replacement opens consume from the same budget.
- Exceeding the cap returns `lipapi.ErrMaxRouteAttempts`.

Evidence:

- [`internal/core/runtime/executor.go#L51`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L51)
- [`internal/core/runtime/executor.go#L76`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L76)
- [`internal/core/runtime/executor.go#L152`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L152)
- [`internal/core/runtime/executor.go#L205`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L205)
- [`internal/core/runtime/executor.go#L462`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L462)
- [`internal/core/runtime/executor_test.go#L679`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor_test.go#L679)

---

### C0.4 — continuity/store config is mostly declarative, not operational

Status: `Largely fixed` (v1 scope)

Reasoning:

- Store construction is owned by [`internal/core/continuity/store.go`](internal/core/continuity/store.go): `OpenStore` is the composition factory; `stdhttp.BuildExecutor` calls `continuity.OpenStore` (not `b2bua.NewMemoryStore` directly).
- `continuity.ttl`, `continuity.max_legs`, and optional `continuity.store` (`memory` default via `LoadFile`) are validated; unknown store names fail startup deterministically.
- **Intentionally deferred (stage-two):** durable backends (`continuity.in_memory=false`, `store: sqlite`, etc.), registry-external store plugins, and restart-survival tests.

Evidence:

- [`internal/core/config/model.go`](internal/core/config/model.go) (`ContinuityConfig`, including `store`)
- [`internal/core/config/loader.go`](internal/core/config/loader.go) (normalizes `store` to `memory` when `in_memory: true`)
- [`internal/core/continuity/store.go`](internal/core/continuity/store.go) (`OpenStore`, `newMemoryStoreFromContinuity`)
- [`internal/stdhttp/wire.go`](internal/stdhttp/wire.go) (`continuity.OpenStore`)

Conclusion:

- The “hidden memory-only wiring” concern is addressed for v1; durable continuity remains explicitly out of scope until stage-two.

---

### C0.5 — model-only route selectors parse successfully but are not executable

Status: `Fixed`

Reasoning:

- Runtime now applies `routing.ApplyModelOnlyBackends(sel, e.DefaultBackend)`.
- If a model-only selector still cannot be resolved, execution fails early with `lipapi.ErrUnresolvedModelOnlySelector`.
- This removes the old empty-backend runtime surprise.

Evidence:

- [`internal/core/runtime/executor.go#L148`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L148)
- [`internal/core/runtime/executor.go#L150`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go#L150)
- [`internal/core/routing/model_only.go#L9`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/routing/model_only.go#L9)
- [`internal/core/runtime/executor_test.go#L718`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor_test.go#L718)

---

### C0.6 — request body limits are not enforced in the safest way

Status: `Fixed`

Reasoning:

- Bundled HTTP frontends now use the shared `reqbody.ReadAll(...)`.
- That helper uses `http.MaxBytesReader`.
- Handlers map oversize bodies to deterministic `413` behavior instead of relying on incidental JSON decode failure.
- There are targeted 413 tests for OpenAI Responses, OpenAI Chat, Anthropic, and Gemini frontends.

Evidence:

- [`internal/plugins/frontends/reqbody/body.go#L15`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/reqbody/body.go#L15)
- [`internal/plugins/frontends/openairesponses/handler.go#L35`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses/handler.go#L35)
- [`internal/plugins/frontends/openailegacy/handler.go#L35`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy/handler.go#L35)
- [`internal/plugins/frontends/anthropic/handler.go#L37`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/anthropic/handler.go#L37)
- [`internal/plugins/frontends/gemini/handler.go#L32`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/gemini/handler.go#L32)
- [`internal/plugins/frontends/openairesponses/handler_413_test.go#L13`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses/handler_413_test.go#L13)
- [`internal/plugins/frontends/openailegacy/handler_413_test.go#L13`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy/handler_413_test.go#L13)
- [`internal/plugins/frontends/anthropic/handler_413_test.go#L13`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/anthropic/handler_413_test.go#L13)
- [`internal/plugins/frontends/gemini/handler_413_test.go#L13`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/gemini/handler_413_test.go#L13)

---

### A1.1 — plugin registrations exist, but composition is still hardcoded in multiple switchboards

Status: `Largely fixed`

Reasoning:

- Active backend construction now flows through `pluginreg.BuildBackend(...)`.
- Active frontend mounting now flows through `pluginreg.MountFrontend(...)`.
- Feature hook composition now flows through `pluginreg.BuildFeatureHooks(...)`.
- Registries and registration helpers exist in `internal/pluginreg`.
- The old switchboard composition concern appears materially removed from the active standard path.

Remaining caveat:

- The registry is internal to the standard bundle, not yet exposed as a broader stable store/plugin factory surface. That is a stage-two completeness issue, not the original switchboard issue.

Evidence:

- [`internal/pluginreg/reg.go#L32`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/pluginreg/reg.go#L32)
- [`internal/stdhttp/wire.go#L24`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/stdhttp/wire.go#L24)
- [`internal/stdhttp/mount.go#L31`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/stdhttp/mount.go#L31)
- [`cmd/lipstd/main.go#L27`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/cmd/lipstd/main.go#L27)

---

### A1.2 — frontend config rows and enabled flags are not actually driving mounts

Status: `Fixed`

Reasoning:

- `MountBundledFrontends(...)` now iterates configured frontend rows and skips disabled entries.
- The actual mounting call is registry-driven per enabled row.
- Gemini is still ordered last as a catch-all, but only when enabled.

Evidence:

- [`internal/stdhttp/mount.go#L15`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/stdhttp/mount.go#L15)
- [`internal/stdhttp/mount.go#L20`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/stdhttp/mount.go#L20)
- [`internal/stdhttp/mount.go#L31`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/stdhttp/mount.go#L31)

---

### A1.3 — feature plugin composition is also hardcoded, and feature config payloads are ignored

Status: `Largely fixed` (v1 reference bundle)

Reasoning:

- Feature composition remains registry-driven (`pluginreg.BuildFeatureHooks`).
- **`submit-noop`** decodes YAML in the feature boundary (`submitnoop.DecodeHookConfig`): optional `order`, optional `lifecycle_probe`; unknown keys fail composition.
- **`parts-noop`** and **`tool-reactor-noop`** remain intentionally strict empty config (reference no-ops); invalid keys still fail loudly via `requireEmptyFeatureYAML`.

Evidence:

- [`internal/pluginreg/features_install.go`](internal/pluginreg/features_install.go)
- [`internal/plugins/features/submitnoop/config.go`](internal/plugins/features/submitnoop/config.go)
- [`internal/pluginreg/feature_yaml.go`](internal/pluginreg/feature_yaml.go)

Conclusion:

- The “payloads are silently ignored” concern is closed for the configurable reference feature; broader real feature plugins remain a stage-two product choice.

---

### A1.4 — plugin lifecycle abstraction exists but is unused

Status: `Fixed`

Reasoning:

- `runtime.App` now accepts `Lifecycles`.
- `App.Start(...)` starts them.
- `App.Shutdown(...)` stops them in reverse order.
- `cmd/lipstd/main.go` now passes lifecycle values returned from feature composition into `runtime.New(...)`.

Evidence:

- [`internal/core/runtime/app.go#L29`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/app.go#L29)
- [`internal/core/runtime/app.go#L79`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/app.go#L79)
- [`internal/core/runtime/app.go#L100`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/app.go#L100)
- [`cmd/lipstd/main.go#L27`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/cmd/lipstd/main.go#L27)
- [`cmd/lipstd/main.go#L43`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/cmd/lipstd/main.go#L43)

Remaining caveat:

- The current bundled feature plugins return no lifecycle handles, so lifecycle orchestration is real infrastructure with limited active use.

---

### A1.5 — backend capability negotiation is too static for model-varying providers

Status: `Fixed` (reference backends)

Reasoning:

- Runtime `Backend` uses `ResolveCaps` when set (`internal/core/runtime/executor.go`).
- OpenAI Responses, OpenAI Legacy, Anthropic, Gemini, Bedrock, and ACP bundled backends set `ResolveCaps` (ACP returns static caps via the hook for agent-wide surfaces).
- Model heuristics live next to each backend (`*caps.go` where applicable).

Evidence:

- [`internal/core/runtime/executor.go`](internal/core/runtime/executor.go)
- [`internal/plugins/backends/openairesponses/plugin.go`](internal/plugins/backends/openairesponses/plugin.go)
- [`internal/plugins/backends/openailegacy/plugin.go`](internal/plugins/backends/openailegacy/plugin.go)
- [`internal/plugins/backends/anthropic/plugin.go`](internal/plugins/backends/anthropic/plugin.go), [`internal/plugins/backends/anthropic/caps.go`](internal/plugins/backends/anthropic/caps.go)
- [`internal/plugins/backends/gemini/caps.go`](internal/plugins/backends/gemini/caps.go), [`internal/plugins/backends/bedrock/caps.go`](internal/plugins/backends/bedrock/caps.go)

Conclusion:

- Reference distribution rollout is complete; expanding catalog accuracy remains ongoing maintenance.

---

### P1.1 — OpenAI Chat frontend rejects assistant tool-call history

Status: `Fixed`

Reasoning:

- Assistant messages now accept `tool_calls` and `function_call`.
- Those payloads are preserved as canonical `PartJSON` parts on assistant history messages.
- There are direct decode tests for both forms.

Evidence:

- [`internal/plugins/frontends/openailegacy/decode.go#L150`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy/decode.go#L150)
- [`internal/plugins/frontends/openailegacy/decode.go#L164`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy/decode.go#L164)
- [`internal/plugins/frontends/openailegacy/decode_test.go#L185`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy/decode_test.go#L185)
- [`internal/plugins/frontends/openailegacy/decode_test.go#L206`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy/decode_test.go#L206)

Note:

- [internal/plugins/frontends/openailegacy/doc.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy/doc.go) now states assistant tool calls and legacy `function_call` payloads are supported, which aligns with the current decoder behavior.

---

### P1.2 — OpenAI Responses frontend only accepts message input items

Status: `Fixed`

Reasoning:

- Decoder now supports `message`, `function_call`, and `function_call_output` item types.
- Unsupported item types fail explicitly.
- Tests cover both additional supported forms.

Evidence:

- [`internal/plugins/frontends/openairesponses/decode.go#L160`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses/decode.go#L160)
- [`internal/plugins/frontends/openairesponses/decode.go#L168`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses/decode.go#L168)
- [`internal/plugins/frontends/openairesponses/decode_test.go#L110`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses/decode_test.go#L110)
- [`internal/plugins/frontends/openairesponses/decode_test.go#L318`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses/decode_test.go#L318)

---

### P1.3 — protocol helper logic is already duplicating across adapters

Status: `Fixed` (OpenAI frontends)

Reasoning:

- Shared helpers live in [`internal/plugins/frontends/openaiwire`](internal/plugins/frontends/openaiwire) (`MustJSON`, `ImagePartFromURL`, `FilePartFromBase64`); both OpenAI frontends import them.

Evidence:

- [`internal/plugins/frontends/openairesponses/decode.go`](internal/plugins/frontends/openairesponses/decode.go)
- [`internal/plugins/frontends/openailegacy/decode.go`](internal/plugins/frontends/openailegacy/decode.go)
- [`internal/plugins/frontends/openaiwire/parts.go`](internal/plugins/frontends/openaiwire/parts.go)

---

### L2.1 — health-path fallback is inconsistent

Status: `Fixed`

Reasoning:

- `config.LoadFile(...)` defaults health path to `/healthz`.
- `stdhttp.Run(...)` also falls back to `/healthz` when the field is empty.

Evidence:

- [`internal/core/config/loader.go#L19`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/config/loader.go#L19)
- [`internal/stdhttp/server.go#L31`](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/stdhttp/server.go#L31)

---

### L2.2 — Gemini is mounted on `/` as a catch-all

Status: `Fixed`

Reasoning:

- Gemini is registered on `/v1beta/` and `/v1beta1/` only (see `mountGemini`), avoiding a root catch-all.
- `MountBundledFrontends` still orders Gemini after other enabled frontends.

Evidence:

- [`internal/pluginreg/frontends_install.go`](internal/pluginreg/frontends_install.go)
- [`internal/stdhttp/mount_test.go`](internal/stdhttp/mount_test.go) (health path not shadowed when Gemini is the only frontend)

---

### L2.3 — default wire models are hardcoded in the HTTP bundle layer

Status: `Not fully assessed from the reviewed changes; likely still true in part`

Reasoning:

- The active path still injects `defaultRouteSelector` into mounted handlers from bundle wiring.
- The broader concern about provider/model defaults living in registry metadata or policy config still appears directionally valid.

Assessment:

- Treat as advisory architectural cleanup, not a blocked correctness item.

---

## Summary matrix

| Finding | Status | Short conclusion |
|---|---|---|
| C0.1 | Fixed | Baseline is immutable, attempts are derived fresh |
| C0.2 | Fixed | Hook metadata is populated correctly |
| C0.3 | Fixed | `max_attempts` is enforced |
| C0.4 | Largely fixed | `OpenStore` factory + `continuity.store`; durable/sqlite deferred |
| C0.5 | Fixed | Model-only selectors resolve or fail early |
| C0.6 | Fixed | Shared `MaxBytesReader` and 413 behavior are in place |
| A1.1 | Largely fixed | Active composition is registry-driven |
| A1.2 | Fixed | Frontend enablement now drives mounts |
| A1.3 | Largely fixed | `submit-noop` decodes config; other reference no-ops strict-empty |
| A1.4 | Fixed | Lifecycle start/stop is wired |
| A1.5 | Fixed | `ResolveCaps` on reference backends + focused caps tests |
| P1.1 | Fixed | Chat assistant tool-call history is accepted |
| P1.2 | Fixed | Responses non-message items are supported |
| P1.3 | Fixed | `openaiwire` shared helpers for OpenAI frontends |
| L2.1 | Fixed | Health-path fallback is consistent |
| L2.2 | Fixed | Gemini mounted under `/v1beta/` and `/v1beta1/` |
| L2.3 | Advisory / likely still true in part | Default placement concern remains |

---

## Bottom line

The answer to “were all review issues already properly addressed?” is:

> For **v1 / reference-bundle scope**, the actionable audit items are now closed or explicitly classified as **stage-two** (durable continuity, richer feature plugins, catalog depth).

**Still intentionally open / advisory**

- **L2.3** — default route injection and “defaults in wiring vs policy” (advisory architecture).
- **Durable continuity** — `continuity.in_memory=false` and non-`memory` `continuity.store` values remain unsupported until stage-two implements real backends.
- **Broader feature surface** — beyond reference `submit-noop` config, additional real features remain product work.

---

## Recommended interpretation for agent work

If a follow-on agent is asked whether stage-two can proceed, the correct answer is:

- Yes: proceed on durable store backends, expanded feature plugins, and catalog-driven caps—tracked as stage-two work, not as silent v1 gaps.

---

## Agent handoff plan execution (WP1–WP5)

Cross-reference: [`v1_code_review_agent_handoff_plan.md`](v1_code_review_agent_handoff_plan.md).

| WP | Title | Outcome |
|----|-------|---------|
| WP1 | Continuity store truthfulness | `continuity.OpenStore`, `continuity.store`, loader default; unsupported store → startup error |
| WP2 | Feature plugin honesty | `submit-noop` YAML (`order`, `lifecycle_probe`) + lifecycle tests |
| WP3 | Candidate-aware caps | `ResolveCaps` on Anthropic, Gemini, Bedrock, ACP (+ existing OpenAI) |
| WP4 | Protocol helper dedup | `internal/plugins/frontends/openaiwire` |
| WP5 | Docs / residuals | Package doc + audit updates; Gemini prefix mount + regression test |
