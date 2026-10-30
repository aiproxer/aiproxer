# V1 code review — go-llm-interactive-proxy

Date: 2026-04-21
Repository reviewed: `matdev83/go-llm-interactive-proxy` (`main`)
Review artifact: `v1_code_review.md`

## Scope and method

I reviewed the implementation from the perspective of the v1 Go-core architecture goals and the distinctive LIP behaviors that were meant to survive the rewrite.

Reviewed surfaces:

- bootstrap and composition: `cmd/lipstd/*`, `internal/stdhttp/*`
- canonical contracts and limits: `pkg/lipapi/*`, `pkg/lipsdk/*`
- orchestration core: `internal/core/runtime/*`, `internal/core/routing/*`, `internal/core/hooks/*`, `internal/core/b2bua/*`, `internal/core/config/*`
- representative protocol adapters:
  - frontends: `internal/plugins/frontends/openailegacy/*`, `internal/plugins/frontends/openairesponses/*`
  - backends: `internal/plugins/backends/openailegacy/*`, `internal/plugins/backends/openairesponses/*`
- repo shape, config, QA posture, and Kiro artifacts

I also used local code metrics on the downloaded review subset. In that subset the main hot spots are:

- `internal/core/runtime/executor.go`
- `internal/plugins/frontends/openairesponses/decode.go`
- `internal/plugins/frontends/openairesponses/encode.go`
- `internal/plugins/frontends/openailegacy/decode.go`
- `internal/core/b2bua/store.go`

This is already a real implementation, not a scaffold.

---

## Executive summary

The good news first: v1 is a legitimate foundation. The codebase already captures the right strategic direction compared to the Python system:

- canonical request / event contracts exist
- streaming is the primary execution path
- B2BUA-like recoverable pre-output failover is implemented in the executor
- routing selectors support failover and weighted branches with first-request steering
- provider integrations are isolated behind protocol adapters
- deterministic IDs/timestamps/clocks were added, which is excellent for tests and replayability
- the repo already has meaningful QA posture: unit, integration, fuzz, lint, and CI surfaces

My overall assessment is:

**v1 is buildable and worth continuing, but it is not yet “architecturally honest”.**

Several abstractions exist in the SDK/config layer, yet the actual standard distribution is still wired through hardcoded switchboards. The core execution path is also semantically correct in the main happy path, but it still carries a few correctness traps that will become painful once stage-two features start landing.

If the current issues are fixed, the project is in a strong position to move into stage two. If they are not fixed, stage two risks rebuilding a milder version of the old Python coupling problem.

---

## What is already strong

### 1. The canonical core idea is real, not aspirational

`pkg/lipapi` is a solid starting point. The canonical call, capabilities, stream events, output-commit rules, route-parameter merge rules, and bounded collectors are all in place. This is the right center of gravity for cross-API translation.

### 2. The B2BUA / failover rule is mostly implemented correctly

The executor correctly distinguishes pre-output recoverable failure from post-output surfaced failure. Once output is committed, it does not silently switch backends. That is the right rule and it protects user-visible stream coherence.

### 3. Determinism was treated seriously

Deterministic clocks, call tokens, and stable fallback IDs make this repo much easier to test and reason about than the Python original.

### 4. Hook seams exist early

Submit hooks, request-part hooks, response-part hooks, and tool reactors are already present in v1. Even though I am critical of their current composition semantics, the existence of these seams this early is a major positive.

### 5. The repo is test-minded

The repo shape, README, and per-adapter package layout show a meaningful QA intent rather than a hand-wavy promise of testing later.

---

## Priority findings

I am splitting findings into four buckets:

- **C0 — correctness and semantic traps**
- **A1 — architectural truthfulness gaps**
- **P1 — protocol coverage gaps**
- **L2 — lower-priority maintainability / polish issues**

The assumption for the stage-two spec pack is that all C0 and A1 items are fixed first.

---

## C0 — correctness and semantic traps

### C0.1 — capability downgrades and request mutations are sticky across retries

**Files:**

- `internal/core/runtime/executor.go`
- `pkg/lipapi/capabilities.go`
- `internal/core/hooks/parts.go`

**Problem**

`Executor.Execute` copies the incoming call into `work`, then mutates `work` in place when:

- capability negotiation returns a downgrade (`ApplyNegotiatedDowngrades`)
- request-part hooks mutate the canonical call
- route query params are merged into generation options

That mutated `work` is then stored on `retryRecvStream.call`.

When a backend later fails recoverably before output, `tryReplacementIteration` clones `*s.call`, which means it starts from an already-mutated request.

This creates two classes of bug:

1. **Capability downgrades are not attempt-local.**
   If attempt 1 strips reasoning because backend A lacks it, and backend A then fails before output, attempt 2 against backend B may never receive the original reasoning options even if backend B supports them.

2. **Request hooks can compound across retries.**
   A request hook intended to run once per attempt may instead run repeatedly on top of earlier mutations.

**Why this matters**

This breaks the central promise of cross-backend retry: every attempt should start from the same logical client request, plus explicit per-attempt derivations.

**Required fix**

Treat the client request as an immutable baseline.

Recommended shape:

- `BaselineCall` or equivalent immutable request snapshot
- derive a fresh `AttemptCall` per backend attempt
- apply negotiation downgrades to the attempt copy only
- apply request-part hooks to the attempt copy only
- never persist a downgraded/mutated attempt as the baseline for later retries

**Regression tests to add**

- first backend lacks reasoning, fails pre-output, second backend supports reasoning → second backend sees original reasoning options
- request hook appends a marker once per attempt → marker is not duplicated across retries unless the hook explicitly chooses to do so

---

### C0.2 — hook metadata contracts exist, but the executor passes empty metadata

**Files:**

- `pkg/lipsdk/hooks/submit.go`
- `pkg/lipsdk/hooks/parts.go`
- `pkg/lipsdk/hooks/toolreactor.go`
- `internal/core/runtime/executor.go`

**Problem**

The SDK defines useful metadata fields:

- `TraceID`
- `ALegID`
- `BLegID`
- `AttemptSeq`

But the executor currently invokes hook chains with zero-valued metadata:

- `sdk.PartMeta{}`
- `sdk.ToolMeta{}`
- submit hooks get `nil` metadata, then `RunSubmit` creates an empty meta struct

So the hook APIs advertise attempt-aware behavior, but the runtime does not actually provide that context.

**Why this matters**

This blocks exactly the kind of advanced LIP features that stage two needs:

- attempt-aware request rewrites
- per-session / per-A-leg policies
- tool-reactor correlation
- deterministic diagnostics and audit trails

**Required fix**

Populate hook metadata consistently from the executor:

- submit phase: `TraceID`
- request hooks: `TraceID`, `ALegID`, and when applicable current `BLegID` / `AttemptSeq`
- response hooks and tool reactors: full attempt metadata

Also define the exact phase semantics in docs and tests.

**Regression tests to add**

- feature hook receives expected `TraceID`, `ALegID`, `BLegID`, `AttemptSeq`
- metadata changes correctly when the executor switches from one B-leg to another

---

### C0.3 — `routing.max_attempts` is dead configuration today

**Files:**

- `internal/core/config/model.go`
- `internal/core/config/loader.go`
- `internal/core/runtime/executor.go`

**Problem**

`Routing.MaxAttempts` is decoded and defaulted, but the executor never consults it.

At runtime the system will keep retrying until candidates are exhausted, regardless of configured attempt cap.

**Why this matters**

This is a correctness issue, not just a polish item. Operators will assume `max_attempts` is enforced.

**Required fix**

Track logical attempt count per request and stop planning/opening new B-legs when the configured cap is reached.

This limit should count both:

- open-time recoverable failures
- recv-time recoverable failures that force replacement

**Regression tests to add**

- selector with many eligible candidates and `max_attempts=2` never creates more than 2 B-legs
- exhausted `max_attempts` returns deterministic surfaced error and attempt records show correct final outcome

---

### C0.4 — continuity/store config is mostly declarative, not operational

**Files:**

- `internal/core/config/model.go`
- `internal/stdhttp/wire.go`

**Problem**

`ContinuityConfig` currently exposes only `in_memory`, but `BuildExecutor` always constructs `b2bua.NewMemoryStore(...)` with zero-valued options.

That means:

- `continuity.in_memory` is effectively dead config
- no persistent store can be selected
- store TTL / max-legs settings are not configurable through runtime config even though the memory store supports them

**Why this matters**

B2BUA continuity is core product behavior. It must not be hardcoded behind an incidental in-memory implementation.

**Required fix**

Make store selection and store options part of actual runtime composition.

Minimum fix for v1 stability:

- wire TTL / max-legs / clock-independent config into memory store construction
- fail startup on invalid continuity config

Stage-two fix:

- pluggable continuity / attempt store backends
- durable local default (SQLite recommended)

---

### C0.5 — model-only route selectors parse successfully but are not executable

**Files:**

- `internal/core/routing/parser.go`
- `internal/core/runtime/executor.go`

**Problem**

The parser allows selectors like:

- `gpt-4o-mini`

That yields a `Primary` with empty backend.

The executor then does:

- `be, ok := e.Backends[c.Primary.Backend]`

which will fail with unknown backend `""`.

**Why this matters**

The syntax and runtime contract disagree. That creates user-facing ambiguity.

**Required fix**

Choose one of these and enforce it consistently:

1. **Reject model-only selectors in v1/v2**
2. **Resolve model-only selectors through a configured default backend / policy**

Do not leave it as a runtime surprise.

---

### C0.6 — request body limits are not enforced in the safest way

**Files:**

- `internal/plugins/frontends/openailegacy/handler.go`
- `internal/plugins/frontends/openairesponses/handler.go`
- analogous other frontend handlers

**Problem**

Handlers use `io.ReadAll(io.LimitReader(...))` instead of `http.MaxBytesReader`.

This means oversized request bodies are only rejected incidentally (often as invalid JSON), not explicitly as “request entity too large”.

**Why this matters**

This weakens operational hardening and makes error behavior less deterministic.

**Required fix**

Use `http.MaxBytesReader` and return a clean 413-style error when the body exceeds the configured limit.

Also centralize the limit so all frontend handlers share the same source of truth.

---

## A1 — architectural truthfulness gaps

### A1.1 — plugin registrations exist, but composition is still hardcoded in multiple switchboards

**Files:**

- `cmd/lipstd/hooks_compose.go`
- `cmd/lipstd/wiring.go`
- `internal/stdhttp/wire.go`
- `internal/stdhttp/mount.go`
- `internal/core/config/registrations.go`
- `pkg/lipsdk/contracts.go`
- `pkg/lipsdk/registration.go`

**Problem**

The repo already has:

- plugin registrations from config
- registration validation
- plugin kinds (`frontend`, `backend`, `feature`)

But actual bundle construction still happens through explicit imports and `switch p.ID` code.

This means the config/SDK layer implies pluggability, while the real standard distribution is still manually composed.

**Why this matters**

This is the main architectural risk in the repo. If stage two adds more features without fixing this, the project will drift back toward the same central wiring hell that plagued the Python codebase.

**Required fix**

Move to a real registry/factory model:

- frontend factory registry
- backend factory registry
- feature factory registry
- lifecycle tracking for started plugins

The bundle may still be statically linked, but construction must flow through registries, not hand-maintained switches.

---

### A1.2 — frontend config rows and enabled flags are not actually driving mounts

**Files:**

- `internal/core/config/model.go`
- `internal/stdhttp/mount.go`
- `config/config.yaml`

**Problem**

The config includes `plugins.frontends`, each with `id`, `enabled`, and `config`.

But `MountBundledFrontends` unconditionally mounts all bundled frontend handlers.

So frontend plugin rows are currently declarative only; they do not control actual runtime surface exposure.

**Why this matters**

This is another truthfulness gap between declared architecture and actual behavior.

**Required fix**

Frontend mounts must be registry-driven and config-driven, just like backends should be.

At a minimum:

- disabled frontends must not be mounted
- plugin-private mount config should be decoded through frontend factories
- the standard bundle should no longer hardcode mount table in `internal/stdhttp/mount.go`

---

### A1.3 — feature plugin composition is also hardcoded, and feature config payloads are ignored

**Files:**

- `cmd/lipstd/hooks_compose.go`
- `config/config.yaml`

**Problem**

Feature registrations are inspected, but composition is still a switch on hardcoded no-op feature IDs.

The `config` payload for feature rows is not decoded or passed anywhere meaningful.

**Why this matters**

Stage two will add real request/response altering hooks and real tool reactors. This code path needs to be fixed before those features land.

**Required fix**

Feature plugins must have real factories that:

- receive opaque config payloads
- return one or more hook implementations and optional lifecycle handles
- are assembled through registry-based bundle composition

---

### A1.4 — plugin lifecycle abstraction exists but is unused

**Files:**

- `pkg/lipsdk/plugin/lifecycle.go`
- `internal/core/runtime/app.go`
- `internal/stdhttp/run.go` equivalent (`server.go` / `Run`)

**Problem**

The SDK has a `Lifecycle` interface, but the standard distribution does not actually start/stop plugin lifecycles.

`runtime.App.Start()` currently only logs hook-chain lengths.

**Why this matters**

As soon as plugins own resources such as:

- persistent stores
- background health probes
- wire-capture sinks
- policy refreshers

lifecycle handling becomes mandatory.

**Required fix**

Make lifecycle a first-class part of bundle composition.

The composition root should:

- construct plugins
- start them in dependency-safe order
- stop them during shutdown in reverse order

---

### A1.5 — backend capability negotiation is too static for model-varying providers

**Files:**

- `internal/core/runtime/executor.go`
- `pkg/lipapi/capabilities.go`
- backend `plugin.go` files

**Problem**

`runtime.Backend` currently exposes one static capability set per backend instance.

That is too coarse for providers where capability varies by model or route target.

**Why this matters**

Static backend-level capabilities can produce both:

- false accepts
- false rejects

especially once stage two introduces richer protocol coverage and broader model catalogs.

**Required fix**

Move from static backend-instance caps to model-aware / candidate-aware capability description.

Examples:

- `Describe(candidate, call) CapabilityDecision`
- provider-level static caps + optional model-specific override table

---

## P1 — protocol coverage gaps

### P1.1 — OpenAI Chat frontend rejects assistant tool-call history

**File:** `internal/plugins/frontends/openailegacy/decode.go`

**Problem**

The decoder explicitly rejects assistant messages containing:

- `tool_calls`
- `function_call`

This means the frontend cannot faithfully accept many valid chat-completions conversations that include prior tool use.

**Why this matters**

This blocks real continuation scenarios and weakens cross-API compatibility.

**Required fix**

Extend canonical history representation and/or frontend decoding so prior tool-call history can round-trip in the supported subset.

---

### P1.2 — OpenAI Responses frontend only accepts message input items

**File:** `internal/plugins/frontends/openairesponses/decode.go`

**Problem**

`parseInputItem` accepts only `type == "message"` (or empty).

That excludes valid item forms such as function call / function call output style history entries.

**Why this matters**

The Responses API is more than a plain message array. Restricting it to message items limits compatibility and makes tool-driven multi-turn behavior harder.

**Required fix**

Add support for the required v2 subset of Responses input item types and map them losslessly into canonical history where possible.

---

### P1.3 — protocol helper logic is already duplicating across adapters

**Files:**

- OpenAI frontend/backend adapter packages

**Problem**

Multiple adapters duplicate helper logic for:

- instruction joining
- file-data extraction from canonical parts
- image/file helper parsing
- tool-choice conversion patterns

**Why this matters**

Some duplication is fine, but repeated translation helpers will become drift risk once protocol coverage expands.

**Required fix**

Create small, protocol-scoped shared helper packages where duplication is already clearly structural.

Do **not** centralize everything into a giant translation package. Keep helpers narrow and scoped.

---

## L2 — lower-priority maintainability / polish issues

### L2.1 — health-path fallback is inconsistent

`config.LoadFile` defaults diagnostics health path to `/healthz`, while `stdhttp.Run` falls back to `/health` when the field is empty.

This is minor, but it is the kind of config drift that becomes annoying later.

### L2.2 — Gemini is mounted on `/` as a catch-all

This works for now because more specific routes win, but it makes future surface growth more fragile than necessary.

### L2.3 — default wire models are hardcoded in the HTTP bundle layer

Useful for v1, but stage two should move provider/model defaults into registry metadata or routing policy config.

---

## Recommended fix order

This is the order I would use before or while starting stage two.

### Step 1 — fix execution semantics

1. Make logical request baseline immutable
2. Make attempt derivation explicit
3. Populate hook metadata
4. Enforce `routing.max_attempts`
5. Reject or resolve model-only selectors consistently

### Step 2 — make bundle composition honest

1. Introduce frontend/backend/feature factory registries
2. Move `cmd/lipstd` to registry-driven construction
3. Remove switch-based plugin composition from `stdhttp` / `cmd/lipstd`
4. Respect frontend enabled flags and plugin config
5. Wire plugin lifecycles

### Step 3 — fix continuity/routing operational seams

1. Make continuity store selection/config real
2. Add configurable memory-store options now
3. Prepare persistent store abstraction for stage two
4. Wire health/circuit-breaker state into routing policy in stage two

### Step 4 — close protocol gaps that block real usage

1. OpenAI Chat assistant tool-call history
2. OpenAI Responses non-message input items
3. add golden tests for cross-protocol tool history

### Step 5 — harden frontend request handling

1. replace `LimitReader` with `MaxBytesReader`
2. unify request size limit source of truth
3. add explicit 413 behavior tests

---

## Regression test matrix I strongly recommend

These should become non-negotiable before stage-two execution accelerates.

### Core orchestration tests

- downgrade on attempt 1 does not leak into attempt 2
- request-part hook mutation does not compound unexpectedly across retries
- request-part hooks see correct attempt metadata
- response-part hooks see correct attempt metadata
- tool reactors see correct attempt metadata
- `max_attempts` is enforced for open-time and recv-time recoverable failures
- model-only selectors either reject early or resolve deterministically

### Composition tests

- disabled frontend plugin is not mounted
- disabled backend plugin cannot be selected
- disabled feature plugin contributes no hooks
- plugin factories are used end-to-end instead of switchboards
- plugin lifecycle start/stop order is deterministic

### Store / B2BUA tests

- continuity store options from config are honored
- A-leg/B-leg metadata survives retry / failover chains
- attempt lineage is persisted in order

### Protocol tests

- OpenAI Chat history with assistant tool calls decodes and re-encodes correctly in supported subset
- OpenAI Responses `function_call_output`-style history decodes correctly
- cross-API tool history goldens for Chat ↔ Responses ↔ Anthropic subset

### HTTP hardening tests

- oversized body returns 413-equivalent error contract
- truncated/oversized JSON does not produce misleading invalid-json behavior

---

## Bottom line

This repo is on the right track.

The most important sentence in this review is:

> **Fix the semantic and composition truthfulness gaps before you add many more features.**

Once that is done, the Go rewrite has a strong chance of avoiding the Python codebase’s architectural gravity well.

---

## Follow-up instructions for the execution agent

Use this exact sequence:

1. Fix C0.1, C0.2, C0.3, C0.5, and C0.6 first.
2. Then replace switch-based bundle composition with real registries/factories.
3. Make frontend config and lifecycle real.
4. Wire continuity/store config honestly.
5. Only then begin stage-two delivery work.

When implementing those fixes:

- do not patch around the issues locally in handlers or adapters
- fix them at the architectural seam where they originate
- add regression tests before each fix
- preserve the current small-package feel; do not re-centralize logic into a new god object

---

## Assumption carried into stage-two specs

The accompanying stage-two Kiro spec pack assumes that the above issues are fixed.

Stage two therefore focuses on:

- honest plugin-driven composition
- persistent continuity and attempt stores
- routing policy engine with health / circuit breaker semantics
- richer protocol fidelity
- real feature plugins for submit hooks, request/response altering, and tool reactors
