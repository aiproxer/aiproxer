# ADR 0005: Architecture guardrails and complexity budgets

## Status

Accepted (stage three runtime hardening).

## Context

Stage two review ([`stage2_code_review.md`](../../.kiro/specs/archive/go-core-reimplementation-stage-two/stage2_code_review.md)) warned that registry-driven composition is healthier than `switch` wiring, but **global mutable registries** and **hidden `init()`** registration still create long-term drift risk. Stage three adds explicit assembly (`NewRegistry` + `InstallStandardBundleOn(reg)` + `runtimebundle.Build` with `PluginRegistry`) but still needs **measurable** guardrails so the core and composition roots do not grow without review.

## Decision

1. **Complexity budgets** — Non-test line counts are capped for these trees (see [`internal/archtest/guardrails_test.go`](../../internal/archtest/guardrails_test.go) for authoritative numbers):
   - `internal/core`
   - `internal/pluginreg`
   - `internal/stdhttp`
   - `internal/infra/runtimebundle`

2. **No `init()` in the standard bundle registration path** — Production registration code under `internal/pluginreg`, `internal/standardplugins`, and `cmd/aipstd` must not use `func init()`. The standard binary should own an explicit `*pluginreg.Registry` (`NewRegistry` + `InstallStandardBundleOn(reg)` + validation) and thread that registry into `runtimebundle.Build` / HTTP wiring. Composition roots and wiring layers must not depend on package-level default registry state (see ADR 0001 and `internal/archtest`). Test-only `init()` in `*_test.go` remains allowed.

3. **Core import boundary** — `internal/core` must not depend on `internal/plugins/...` (enforced by `go list` dependency tests in `internal/core/runtime`).

4. **Regression checklist** — Stage-two “must-fix” items F1–F10 are tracked in [`.kiro/specs/archive/go-core-stage-three-runtime-hardening/stage2_regression_checklist.md`](../../.kiro/specs/archive/go-core-stage-three-runtime-hardening/stage2_regression_checklist.md) with pointers to tests and code.

## Consequences

- CI / `make quality-checks` can fail when layers grow beyond budget; bump budgets intentionally when needed.
- New contributors have a single place describing boundaries and how they are enforced.
