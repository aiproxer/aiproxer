# V1 Code Review Agent Handoff Plan

Date: 2026-04-21
Related audit: [`v1_code_review_status_audit.md`](v1_code_review_status_audit.md)
Source review: [`v1_code_review.md`](v1_code_review.md)

## Purpose

This document turns the remaining review gaps into concrete agent-ready work packages.

It is optimized for delegation:

- each work package has a narrow scope
- each package defines ownership boundaries
- each package has explicit acceptance criteria
- each package includes validation commands
- each package avoids overlapping write scopes where practical

---

## Current conclusion

The original review is **not fully closed**.

The remaining work is concentrated in five areas:

1. store truthfulness and durable continuity plumbing
2. real feature plugin factories and config decoding
3. full candidate-aware capability rollout
4. protocol helper deduplication
5. doc/test cleanup around stale claims and remaining low-priority bundle concerns

---

## Recommended execution order

1. `WP1` Store truthfulness
2. `WP2` Feature plugin honesty
3. `WP3` Candidate-aware capability rollout
4. `WP4` Protocol helper deduplication
5. `WP5` Docs and cleanup follow-through

Reasoning:

- `WP1` and `WP2` close the remaining architecture-truthfulness gaps.
- `WP3` improves routing/capability correctness before broader model expansion.
- `WP4` is lower-risk refactoring once behavior is stable.
- `WP5` cleans up stale documentation and low-priority residuals.

---

## Work package status table

| ID | Title | Priority | Review findings covered | Suggested ownership |
|---|---|---|---|---|
| WP1 | Continuity Store Truthfulness | High | C0.4 | runtime/config/store agent |
| WP2 | Real Feature Plugin Factories | High | A1.3 | plugin/features agent |
| WP3 | Candidate-Aware Capabilities Rollout | High | A1.5 | backend/caps agent |
| WP4 | Protocol Helper Deduplication | Medium | P1.3 | frontend/adapters agent |
| WP5 | Stale Docs and Residual Cleanup | Medium | P1.1 doc drift, L2.2, L2.3 | docs/integration agent |

---

## WP1 — Continuity Store Truthfulness

### Goal

Make continuity/store configuration operationally honest instead of memory-store-only wiring hidden behind config fields.

### Why this matters

The current runtime now honors `ttl` and `max_legs`, but store selection is still hardcoded:

- `continuity.in_memory=false` does not select another store
- there is no store registry/factory path
- durable continuity is not active in the runtime composition root

### Scope

In scope:

- define or finalize a store factory seam
- move store creation out of direct `b2bua.NewMemoryStore(...)` hardcoding
- preserve in-memory store support with validated `ttl` and `max_legs`
- support explicit startup failure for invalid or unsupported store config
- if SQLite work is intended in this phase, wire it through the same composition seam

Out of scope:

- broad routing refactors unrelated to store selection
- unrelated plugin registry redesign

### Primary files

- [internal/stdhttp/wire.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/stdhttp/wire.go)
- [internal/core/config/model.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/config/model.go)
- [internal/core/config/loader.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/config/loader.go)
- [internal/core/b2bua/store.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/b2bua/store.go)
- [internal/pluginreg/reg.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/pluginreg/reg.go)

### Acceptance criteria

1. Runtime composition no longer directly hardcodes `b2bua.NewMemoryStore(...)` as the only store path.
2. In-memory options remain honored and validated.
3. Unsupported store selection fails explicitly and deterministically at startup.
4. If durable store selection is in scope, it is constructed through the same factory path.
5. Diagnostics and runtime use the configured store instance, not a shadow copy.

### Minimum tests

1. valid memory config with `ttl` and `max_legs` is honored
2. invalid `continuity.ttl` fails startup
3. unsupported store selection fails startup with a clear error
4. if SQLite is added: data survives process restart in a focused test

### Validation commands

```bash
go test ./internal/stdhttp ./internal/core/config ./internal/core/b2bua ./internal/core/continuity
```

### Suggested agent prompt

```text
Close the remaining continuity/store truthfulness gap from .kiro/specs/archive/go-core-reimplementation-stage-two/v1_code_review_status_audit.md.

Scope:
- own internal/stdhttp/wire.go, internal/core/config/*, internal/core/b2bua/*, and any new store-factory wiring
- do not refactor unrelated routing or frontend code
- preserve current memory-store behavior while removing direct memory-store-only hardcoding from the active composition path

Deliver:
- code changes
- focused tests
- short summary of whether durable store support was implemented or explicit startup failure was retained
```

---

## WP2 — Real Feature Plugin Factories

### Goal

Make feature plugins operationally real: factories should decode opaque config payloads and return actual configured hooks/lifecycles rather than ignoring config.

### Why this matters

The registry seam is present, but bundled feature factories still discard config and instantiate static no-op hooks. That means the architecture is only partially honest.

### Scope

In scope:

- decode feature `yaml.Node` payloads in feature factories
- make at least one real configurable feature plugin per supported family if already expected by spec direction
- return lifecycle handles where appropriate
- preserve deterministic ordering semantics

Out of scope:

- executor semantics already fixed in C0.1/C0.2/C0.3
- unrelated backend capability work

### Primary files

- [internal/pluginreg/features_install.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/pluginreg/features_install.go)
- [internal/pluginreg/reg.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/pluginreg/reg.go)
- [internal/plugins/features](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/features)
- [cmd/lipstd/main.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/cmd/lipstd/main.go)

### Acceptance criteria

1. Bundled feature factories no longer ignore config payloads by default.
2. Feature config decoding happens inside the feature factory/plugin boundary, not in core config structs.
3. Hook composition remains registry-driven.
4. Lifecycle values, when returned, are started/stopped by the existing app lifecycle machinery.
5. Tests cover at least one non-trivial config-driven feature factory path.

### Minimum tests

1. enabled feature with config payload produces the expected hook behavior
2. invalid feature config fails composition cleanly
3. disabled feature contributes no hooks
4. lifecycle start/stop executes for a feature that returns lifecycle handles

### Validation commands

```bash
go test ./cmd/lipstd ./internal/pluginreg ./internal/plugins/features/...
```

### Suggested agent prompt

```text
Close the remaining feature-plugin honesty gap from .kiro/specs/archive/go-core-reimplementation-stage-two/v1_code_review_status_audit.md.

Scope:
- own internal/pluginreg/features_install.go, internal/pluginreg/reg.go, and bundled feature plugin code
- do not change unrelated frontend/backend packages
- preserve registry-driven composition

Deliver:
- config-aware feature factories
- focused tests for enabled/disabled/invalid-config behavior
- lifecycle integration if a feature returns lifecycle handles
```

---

## WP3 — Candidate-Aware Capabilities Rollout

### Goal

Finish the rollout of candidate-aware capability negotiation beyond the OpenAI backends.

### Why this matters

The runtime seam now supports `ResolveCaps(...)`, but only OpenAI backends use it. Static capability declarations on model-varying providers still risk false accepts and false rejects.

### Scope

In scope:

- review Anthropic, Gemini, Bedrock, ACP capability behavior
- implement `ResolveCaps(...)` where capability varies by model/candidate
- keep negotiation deterministic and pre-upstream

Out of scope:

- unrelated routing planner redesign
- tool-history frontend changes

### Primary files

- [internal/core/runtime/executor.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/core/runtime/executor.go)
- [internal/plugins/backends/anthropic/plugin.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/backends/anthropic/plugin.go)
- [internal/plugins/backends/gemini/plugin.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/backends/gemini/plugin.go)
- [internal/plugins/backends/bedrock/plugin.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/backends/bedrock/plugin.go)
- [internal/plugins/backends/acp/plugin.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/backends/acp/plugin.go)

### Acceptance criteria

1. Providers with model-varying capability behavior expose `ResolveCaps(...)`.
2. Capability negotiation remains pre-upstream and deterministic.
3. Tests prove at least one model-specific accept/reject or downgrade path per affected provider family where applicable.
4. Static `Caps` remains acceptable only where capability is genuinely backend-wide.

### Minimum tests

1. one candidate-aware capability test for Anthropic if model variance exists
2. one candidate-aware capability test for Gemini if model variance exists
3. no regression in OpenAI capability negotiation

### Validation commands

```bash
go test ./internal/core/runtime ./internal/plugins/backends/anthropic ./internal/plugins/backends/gemini ./internal/plugins/backends/bedrock ./internal/plugins/backends/acp ./internal/plugins/backends/openairesponses ./internal/plugins/backends/openailegacy
```

### Suggested agent prompt

```text
Close the remaining candidate-aware capability rollout gap from .kiro/specs/archive/go-core-reimplementation-stage-two/v1_code_review_status_audit.md.

Scope:
- own capability descriptors in backend plugin packages plus any focused runtime tests
- do not redesign the executor; use the existing ResolveCaps seam
- static Caps is acceptable only where provider capability truly does not vary by target model

Deliver:
- ResolveCaps adoption where justified
- focused regression tests
- short note listing which providers remain static intentionally and why
```

---

## WP4 — Protocol Helper Deduplication

### Goal

Remove the already-visible structural duplication across protocol adapters without creating a giant translation package.

### Why this matters

This is a maintainability risk, not an urgent correctness bug. It should be handled after the truthfulness items are closed.

### Scope

In scope:

- extract small protocol-scoped shared helper functions
- deduplicate OpenAI frontend helper logic first
- preserve existing behavior exactly

Out of scope:

- changing canonical semantics
- broad adapter redesign

### Primary files

- [internal/plugins/frontends/openairesponses/decode.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openairesponses/decode.go)
- [internal/plugins/frontends/openailegacy/decode.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy/decode.go)

### Acceptance criteria

1. Shared helper logic is factored into a narrow package or file with a clear scope.
2. There is no behavior change in existing frontend decode tests.
3. The refactor reduces duplication without introducing a god translation utility.

### Minimum tests

1. existing frontend decode suites stay green
2. add focused helper tests if extracting behavior warrants them

### Validation commands

```bash
go test ./internal/plugins/frontends/openairesponses ./internal/plugins/frontends/openailegacy
```

### Suggested agent prompt

```text
Address the protocol-helper duplication called out in .kiro/specs/archive/go-core-reimplementation-stage-two/v1_code_review_status_audit.md.

Scope:
- own only the OpenAI frontend adapter helper layer unless you find a clearly better narrow boundary
- preserve exact behavior
- avoid introducing a central giant translation package

Deliver:
- narrow deduplication refactor
- green adapter tests
- brief note on what was intentionally not centralized
```

---

## WP5 — Stale Docs and Residual Cleanup

### Goal

Bring docs and low-priority residuals back into sync with the actual implementation.

### Why this matters

One concrete documentation error already exists: the OpenAI Chat frontend doc still claims assistant tool-call history is rejected even though decode support is implemented. Low-priority bundle cleanup items also remain.

### Scope

In scope:

- update stale package docs
- document remaining partial/open review items accurately
- optionally reduce low-priority residual fragility if the changes are small and low-risk

Out of scope:

- large behavior changes that belong in WP1–WP4

### Primary files

- [internal/plugins/frontends/openailegacy/doc.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/plugins/frontends/openailegacy/doc.go)
- [.kiro/specs/archive/go-core-reimplementation-stage-two/v1_code_review_status_audit.md](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/.kiro/specs/archive/go-core-reimplementation-stage-two/v1_code_review_status_audit.md)
- [internal/pluginreg/frontends_install.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/pluginreg/frontends_install.go)
- [internal/stdhttp/mount.go](/C:/Users/Mateusz/source/repos/go-llm-interactive-proxy/internal/stdhttp/mount.go)

### Acceptance criteria

1. Package docs no longer contradict actual behavior.
2. Remaining residual items are clearly classified as open, partial, or intentionally deferred.
3. If low-risk cleanup for Gemini catch-all or default-route defaults is performed, it includes tests and no behavioral regression.

### Validation commands

```bash
go test ./cmd/lipstd ./internal/stdhttp ./internal/plugins/frontends/openailegacy
```

### Suggested agent prompt

```text
Perform the documentation and residual-cleanup pass described in .kiro/specs/archive/go-core-reimplementation-stage-two/v1_code_review_status_audit.md.

Scope:
- own stale docs first
- only take on low-risk code cleanup if it is clearly separable and test-backed
- do not start store or capability work from this package

Deliver:
- doc corrections
- optional small residual cleanup with tests
- final note listing what remains intentionally deferred
```

---

## Parallelization guidance

Safe parallel split:

1. `WP1` can run independently.
2. `WP2` can run independently if it avoids store/config file overlap beyond feature rows.
3. `WP3` can run independently from `WP1` and `WP2`.
4. `WP4` should wait until `WP2` and `WP3` are not touching the same adapter files.
5. `WP5` should run last or only on clearly separate docs.

Suggested first wave:

- Agent A: `WP1`
- Agent B: `WP2`
- Agent C: `WP3`

Suggested second wave:

- Agent D: `WP4`
- Agent E: `WP5`

---

## Completion checklist

- [x] `WP1` merged or explicitly deferred with written rationale *(v1: `OpenStore` + `store: memory`; SQLite/durable deferred — see audit “C0.4”)*
- [x] `WP2` merged or explicitly deferred with written rationale *(config-driven `submit-noop` + lifecycle probe tests)*
- [x] `WP3` merged or explicitly deferred with written rationale *(ResolveCaps on reference backends)*
- [x] `WP4` merged or explicitly deferred with written rationale *(`openaiwire`)*
- [x] `WP5` merged or explicitly deferred with written rationale *(docs + Gemini prefixes + audit)*
- [x] this handoff plan updated if scope changes materially during execution

---

## One-line briefing for the next orchestrator

Use [`v1_code_review_status_audit.md`](v1_code_review_status_audit.md) as the source of truth for current status, and assign agents against `WP1` through `WP5` in this file rather than assuming the original review is already fully closed.

