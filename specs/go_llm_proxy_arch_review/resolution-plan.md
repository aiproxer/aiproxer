# Resolution plan — aiproxer architecture de-slopification roadmap

Generated: 2026-07-07

## Planning principles

This plan assumes **no behavior change** unless explicitly called out. The goal is to reduce future maintenance cost while preserving the current product behavior and architectural strengths.

Guiding constraints:

1. Preserve canonical `pkg/aipapi` and `pkg/aipsdk` contracts.
2. Preserve explicit composition; do not introduce DI containers, reflection registries, global mutable registries, or `init()` registration.
3. Preserve streaming-first behavior and no-retry-after-output semantics.
4. Prefer same-package file/function extraction before adding new packages.
5. Add architecture tests before large refactors where practical.
6. Keep refactors small enough to review and bisect.
7. Do not rename architecture for aesthetics; change package boundaries only when ownership becomes clearer.

---

## Phase 0 — Baseline and safety rails

**Goal:** Make architectural mass visible before refactoring.

**Risk:** Low

**Expected duration:** Small/medium

### Task 0.1 — Generate architecture metrics report

> **Status: Completed.** `scripts/arch-report.go` and `internal/archtest/hexagonal_migration_baseline_test.go` produce deterministic JSON + Markdown output via `make arch-report`. Reports package/file line counts, import fan-out/fan-in, exported symbol counts, and hexagonal baseline classifications.

Acceptance criteria:

- ~~`make arch-report` or equivalent produces deterministic Markdown/JSON output.~~ **Done** — `make arch-report` outputs JSON + Markdown.
- ~~Report does not fail CI initially.~~ **Done** — report is advisory.
- ~~Output highlights `runtimebundle`, `pluginreg`, `stdhttp`, and `runtime.Executor` hotspots.~~ **Done** — hotspot table includes all four.

### Task 0.2 — Add critical-file warning budgets

> **Status: Completed.** `internal/archtest/critical_files.go` defines `CriticalFileBudgets` (single source of truth for both the guardrail test and arch-report). Budgets lock post-refactor sizes for `executor.go` (150), `build.go` (200), `options.go` (200), `standard_table.go` (320), `reg.go` (320), `server.go` (300).

Acceptance criteria:

- ~~Budgets are initially warnings or generous limits.~~ **Done** — machine-checked via `TestCriticalFileLineBudgets`.
- ~~Any future budget increase must include a short rationale.~~ **Done** — rationale comments in `critical_files.go`.
- ~~The check is not noisy enough to block useful development immediately.~~ **Done** — generous headroom.

### Task 0.3 — Convert `extract` baseline entries into actionable backlog

> **Status: Completed.** `testdata/architecture/hexagonal_migration_baseline.json` now includes `role`, `backlog`, `retired_exceptions`, and `status` fields. All former `extract`/`exception` entries have been reclassified to `aligned` or retired. `TestHexagonalMigrationBaseline` requires zero `exception` entries.

Acceptance criteria:

- ~~Every `extract` or `exception` classification has a linked task or plan item.~~ **Done** — all reclassified to `aligned` or retired.
- ~~The baseline stops being only a snapshot and becomes a controlled migration register.~~ **Done** — baseline includes role/backlog/status.

---

## Phase 1 — Low-risk cleanup and canonical path simplification

**Goal:** Remove obvious transitional indirection and reduce reading cost before deeper refactors.

**Risk:** Low/medium

### Task 1.1 — Make `runtimebundle.Build + stdhttp.RunWithRuntime` the canonical path

> **Status: Completed.** `cmd/aipstd` uses `runtimebundle.BuildBootstrap` → `stdhttp.RunWithRuntime` as the single explicit build path. No duplicate runtime assembly pass exists.

Acceptance criteria:

- ~~`cmd/aipstd` uses one explicit build path.~~ **Done** — `BuildBootstrap` + `RunWithRuntime`.
- ~~Tests use the same path or clearly named test helpers.~~ **Done** — test helpers use `Build` / `BuildBootstrap`.
- ~~No duplicate runtime assembly pass exists in production startup.~~ **Done**.

### Task 1.2 — Remove or deprecate `stdhttp.BuildExecutor`

> **Status: Completed.** Both `stdhttp.BuildExecutor` and `runtimebundle.BuildExecutor` were deleted. The canonical path is now `runtimebundle.Build` directly.

`internal/stdhttp/wire.go` was a thin wrapper around `runtimebundle.BuildExecutor` (also now deleted).

Steps (done):

1. Searched all call sites.
2. Replaced internal call sites with `runtimebundle.Build`.
3. Deleted both wrappers — no stable internal compatibility seam was needed.

Acceptance criteria:

- ~~One fewer redundant wiring API.~~ Two fewer: both `stdhttp.BuildExecutor` and `runtimebundle.BuildExecutor` removed.
- No change in runtime behavior.

### Task 1.3 — Split `stdhttp/server.go` by concern without changing package

> **Status: Completed.** `server.go` split into `handler.go`, `middleware.go`, `mount_diagnostics.go`, `mount_metrics.go`, `mount_admin.go`, `mount_securesession.go`, `mount_frontends.go`. Same exported API, middleware order, routes, and shutdown behavior.

Acceptance criteria:

- ~~Same exported API.~~ **Done**.
- ~~Same middleware order.~~ **Done** — preserved by split.
- ~~Same mounted routes.~~ **Done**.
- ~~Same shutdown/closer behavior.~~ **Done**.
- ~~`server.go` becomes primarily listener lifecycle and `RunWithRuntime`.~~ **Done** — ~206 lines, budget 300.

### Task 1.4 — Add route/middleware preservation tests if missing

> **Status: Completed.** Route/middleware preservation tests added across `mount_test.go`, `control_plane_mount_test.go`, `cancel_test.go`, and frontend-specific integration tests.

Acceptance criteria:

- ~~Refactor has test coverage for route/middleware behavior.~~ **Done**.
- ~~No route accidentally becomes public/unprotected.~~ **Done** — shared-secret protection tested.

---

## Phase 2 — Runtimebundle decomposition

**Goal:** Keep `runtimebundle.Build` as a stable facade but make internals composable and inspectable.

**Risk:** Medium/high because startup wiring is broad.

### Task 2.1 — Introduce explicit build context/result structs

> **Status: Completed.** Build decomposed into focused build units: `build_executor.go`, `build_observability.go`, `build_security.go`, `build_model.go`, `build_persistence.go`, `build_extensions.go`. Each accepts config/inputs and returns sub-runtime values.

Acceptance criteria:

- ~~Closer handling remains LIFO and error-safe.~~ **Done** — `closerStack` preserves LIFO disposal.
- ~~Startup error paths still dispose resources.~~ **Done** — error paths dispose in reverse order.
- ~~Existing tests continue to pass.~~ **Done**.

### Task 2.2 — Extract observability and HTTP client construction

> **Status: Completed.** Observability/HTTP client construction extracted into `build_observability.go`.

Acceptance criteria:

- ~~All HTTP-client wrapping remains behaviorally identical.~~ **Done**.
- ~~Tracing and Prometheus behavior unchanged.~~ **Done**.
- ~~Build body shrinks.~~ **Done** — `build.go` reduced to ~158-line orchestrator.

### Task 2.3 — Extract auth/security runtime construction

> **Status: Completed.** Auth/security construction extracted into `build_security.go` and `secure_session.go`.

Acceptance criteria:

- ~~Auth startup failures preserve current error messages as much as practical.~~ **Done**.
- ~~Local/noop and remote auth behavior unchanged.~~ **Done**.
- ~~Backend local-only access-scope checks remain centralized.~~ **Done**.

### Task 2.4 — Extract model catalog/registry runtime

> **Status: Completed.** Model catalog construction extracted into `modelcatalog_attach.go` and `build_model.go`.

Acceptance criteria:

- ~~Model inventory refresh behavior unchanged.~~ **Done**.
- ~~Cache behavior unchanged.~~ **Done**.
- ~~Closers registered correctly.~~ **Done**.

### Task 2.5 — Extract persistence runtime

> **Status: Completed.** Persistence construction extracted into `build_persistence.go` and `secure_session.go`.

Acceptance criteria:

- ~~SQLite/Postgres/memory behavior unchanged.~~ **Done**.
- ~~TTL/max-leg semantics unchanged.~~ **Done**.
- ~~Secure-session diagnostics store remains available to `Built`.~~ **Done**.

### Task 2.6 — Extract extension runtime snapshot building

> **Status: Completed.** Extension runtime snapshot building extracted into `build_extensions.go` and `build_feature_hooks.go`.

Acceptance criteria:

- ~~Snapshot remains immutable per build generation.~~ **Done**.
- ~~Feature-bundle surfaces are cloned/merged as before.~~ **Done** — `MergeFeatureSurface` uses `MergeBundles`/`Append`.
- ~~Traffic, usage, policy observer composition unchanged.~~ **Done**.

### Task 2.7 — Extract executor construction

> **Status: Completed.** Executor construction extracted into `build_executor.go`. All fields now pass through `ExecutorConfig` grouped structs — `NewExecutor` is a strong invariant boundary with no post-construction mutation.

Acceptance criteria:

- ~~Executor receives the same fields/values.~~ **Done** — all fields via `ExecutorConfig`.
- ~~Error paths still dispose resources.~~ **Done**.
- ~~`Build` reads as orchestration over build units.~~ **Done** — `build.go` is ~158-line orchestrator.

### Task 2.8 — Normalize `BuildOptions`

> **Status: Completed.** `BuildOptions` grouped into `Startup`, `Infra`, `Auth`, `Extensions`, `Policy`, `Diagnostics`, `Testing` sub-structs in `options.go`.

Acceptance criteria:

- ~~Internal build units consume grouped options.~~ **Done**.
- ~~Tests clarify production vs testing overrides.~~ **Done**.
- ~~No behavior change.~~ **Done**.

---

## Phase 3 — Split registry from standard distribution bundle

**Goal:** Make `pluginreg` a true registry package and move standard bundle ownership elsewhere.

**Risk:** Medium/high because standard plugin registration touches many adapters.

### Task 3.1 — Define target package ownership

> **Status: Completed.** **Option A** chosen: `internal/pluginreg` (registry type), `internal/standardplugins` (standard bundle tables/install), `internal/featurebundle` (feature merge surface).

Acceptance criteria:

- ~~Architecture docs updated with the chosen ownership.~~ **Done** — `docs/architecture.md`, EchoesVault pages updated.
- ~~`pluginreg` is explicitly described as registry only.~~ **Done** — package doc and `architecture-guardrails.md` reflect this.

### Task 3.2 — Move standard bundle tables out of `pluginreg`

> **Status: Completed.** `standard_table.go` and `*_install.go` files moved to `internal/standardplugins/`. `pluginreg` retains only the registry type. `standardplugins/types.go` alias trampoline removed; all references qualified with `pluginreg.X`.

Acceptance criteria:

- ~~Concrete plugin imports no longer live in the narrow registry package.~~ **Done**.
- ~~Standard bundle install still happens explicitly from `cmd/aipstd` or standard composition root.~~ **Done** — `standardplugins.InstallStandardBundleOn`.
- ~~Architecture tests updated to allow concrete plugin imports only in the standard bundle package.~~ **Done**.

### Task 3.3 — Move frontend mount functions closer to frontend ownership

> **Status: Completed.** Each frontend package exposes `Mount(mux, opts)`. Standard bundle tables in `standardplugins/standard_table.go` register the mount functions.

Acceptance criteria:

- ~~Mount paths are protocol-owned.~~ **Done** — `frontopenairesponses.Mount`, etc.
- ~~Repeated handler field wiring is reduced.~~ **Done**.
- ~~No generic abstraction unless it clearly reduces code.~~ **Done**.

### Task 3.4 — Move feature-bundle merge/migration helpers

> **Status: Completed.** `FeatureFactoryFromHooks` deleted. All 13 bundled features return `aipfeature.FeatureBundle` directly. `internal/core/hooks` import removed from `standardplugins` and `featurebundle`. Feature merge lives in `internal/featurebundle/merge_surface.go`.

Acceptance criteria:

- ~~Registry package does not import `internal/core/hooks` solely for migration wrapping.~~ **Done** — bridge fully retired.
- ~~New feature plugins use native `FeatureBundle` factories.~~ **Done**.
- ~~Hook bridge has an explicit retirement target.~~ **Done** — bridge deleted; see `docs/feature-bridge-retirement-checklist.md`.

### Task 3.5 — Narrow or retire `pluginreg` exception baseline

> **Status: Completed.** `pluginreg`, `featurebundle`, `runtimebundle`, and `internal/core/extensions` all reclassified to `aligned`. Zero `exception` entries remain. `runtimebundle` has `role: composition_root`.

Acceptance criteria:

- ~~`pluginreg` classification moves from `exception` to `aligned`.~~ **Done** — `aligned`.
- ~~Retirement trigger is either satisfied or narrowed.~~ **Done** — all retired.
- ~~Tests enforce the new boundary.~~ **Done** — `TestHexagonalMigrationBaseline` requires zero exceptions.

---

## Phase 4 — Executor collaborator extraction

**Goal:** Keep the executor API stable while reducing internal complexity.

**Risk:** High because runtime behavior is subtle.

### Task 4.1 — Add characterization tests around `Execute`

> **Status: Completed.** `executor_characterization_matrix_test.go` maps all 14 listed scenarios to existing tests. Tests lock behavior before and during the executor refactor.

Acceptance criteria:

- ~~Tests lock behavior before refactor.~~ **Done** — characterization matrix covers all 14 scenarios.
- ~~Refactor can be reviewed as mechanical extraction.~~ **Done**.

### Task 4.2 — Group executor fields

> **Status: Completed.** Grouped structs introduced: `CoreRuntime`, `RoutingRuntime`, `SecurityRuntime`, `AccountingRuntime`, `ObservabilityRuntime`, `ExtensionRuntime`, `InterleavedRuntime`. All embedded in `Executor`. `ExecutorConfig` groups all dependencies; `NewExecutor(cfg)` is the constructor. ~76 test files migrated from `&Executor{...}` literals to `NewTestExecutor` + options.

Acceptance criteria:

- ~~Field count in `Executor` decreases or is grouped semantically.~~ **Done** — 7 grouped structs, mutex fields only on `Executor`.
- ~~Runtimebundle executor construction becomes clearer.~~ **Done** — all fields via `ExecutorConfig`.
- ~~Tests setting fields are migrated to builders/helpers.~~ **Done** — `internal/testkit/executor_builder.go`.

### Task 4.3 — Extract request preparation

> **Status: Completed.** `prepareRequest` extracted as a named method in `executor.go`. Handles validation, snapshot attachment, secure-session begin-turn, submit hooks, A-leg preparation, context enrichment.

Acceptance criteria:

- ~~`Execute` delegates preparation to a named component.~~ **Done** — `e.prepareRequest(ctx, call)`.
- ~~Existing error wrapping remains stable enough for tests/operators.~~ **Done**.

### Task 4.4 — Extract route planning setup

> **Status: Completed.** `buildRoutePlan` + `routePlanState` extracted into `executor_route_plan.go`. Handles selector aliasing, default backend, attempt/TTFT budgets, session routing state, excluded set, request-size estimate, affinity key, route preferences.

Acceptance criteria:

- ~~Route setup can be unit-tested without backend opens.~~ **Done** — `executor_route_plan.go` is independently testable.
- ~~`Execute` loop starts with a compact planning state object.~~ **Done** — `routePlanState`.

### Task 4.5 — Extract attempt opening

> **Status: Completed.** `openInitialAttempt` extracted into `executor_open_loop.go`. Contains tryPlanOpenOnce + B-leg register logic. Receives planning state, returns opened stream or error.

Acceptance criteria:

- ~~Capability negotiation and context-limit behavior unchanged.~~ **Done**.
- ~~B-leg lifecycle behavior unchanged.~~ **Done**.
- ~~Parallel/race/failover behavior unchanged.~~ **Done**.

### Task 4.6 — Extract stream assembly

> **Status: Completed.** `assembleExecutorStream` extracted into `executor_assemble_stream.go`. Handles retry stream creation, accounting tracker, recovery policy, secure-turn propagation, interleaved hidden/visible wrapping.

Acceptance criteria:

- ~~`Execute` ends by delegating to stream assembly.~~ **Done** — `e.assembleExecutorStream(prep, plan, out)`.
- ~~Stream behavior unchanged.~~ **Done**.
- ~~New assembler tests cover wrapper selection and stream state.~~ **Done**.

### Task 4.7 — Add executor file budget

> **Status: Completed.** `executor.go` budget set to 150 lines in `CriticalFileBudgets`. Current size: ~118 lines (delegate-only `Execute` + helpers).

Acceptance criteria:

- ~~New code cannot casually re-bloat `executor.go`.~~ **Done** — `TestCriticalFileLineBudgets` enforces 150-line cap.
- ~~Budget changes require rationale.~~ **Done** — rationale in `critical_files.go`.

---

## Phase 5 — Core boundary tightening

**Goal:** Prevent `internal/core` from becoming a bucket for everything cross-cutting.

**Risk:** Medium

### Task 5.1 — Classify existing core packages

> **Status: Completed.** `docs/core-boundaries.md` created with the full classification table.

Acceptance criteria:

- ~~New contributors have clear guidance.~~ **Done** — `docs/core-boundaries.md`.
- ~~Ambiguous packages are identified.~~ **Done**.

### Task 5.2 — Add core admission checklist

> **Status: Completed.** Core admission checklist added to `docs/core-boundaries.md` and `docs/architecture-guardrails.md`.

Acceptance criteria:

- ~~Architecture review becomes repeatable.~~ **Done**.
- ~~Core additions include a short justification.~~ **Done**.

### Task 5.3 — Add package-doc rule for new core packages

> **Status: Completed.** Architecture test enforces `doc.go` or package comment for `internal/core/*` packages.

Acceptance criteria:

- ~~New core package explains its boundary.~~ **Done** — enforced by archtest.
- ~~Package purpose is reviewable without reading all code.~~ **Done**.

---

## Phase 6 — Feature extension platform cleanup

**Goal:** Finish migration from hook-era concepts to explicit feature bundle seams.

**Risk:** Medium

### Task 6.1 — Inventory hook-only feature factories

> **Status: Completed.** `docs/feature-bridge-retirement-checklist.md` created with full inventory. All 13 bundled features migrated to native `FeatureBundle`.

Acceptance criteria:

- ~~A Markdown checklist exists.~~ **Done**.
- ~~Each item has migrate/keep/drop decision.~~ **Done** — all migrated.

### Task 6.2 — Convert bundled features to native `FeatureBundle`

> **Status: Completed.** All 13 bundled features return `aipfeature.FeatureBundle` directly. `FeatureFactoryFromHooks` deleted.

Acceptance criteria:

- ~~`FeatureFactoryFromHooks` usage drops.~~ **Done** — usage is zero (deleted).
- ~~Tests confirm feature registration and behavior unchanged.~~ **Done**.

### Task 6.3 — Retire or quarantine legacy bridge

> **Status: Completed.** `FeatureFactoryFromHooks` deleted. `internal/core/hooks` import removed from `standardplugins` and `featurebundle`. Hexagonal baseline narrowed (both reclassified to `aligned`).

Acceptance criteria:

- ~~Registry package no longer imports core hooks for legacy bridging.~~ **Done**.
- ~~Architecture baseline narrows accordingly.~~ **Done** — `aligned` classification.

---

## Phase 7 — Governance and long-term maintenance

**Goal:** Keep architecture from regressing as features and enterprise overlays grow.

**Risk:** Low

### Task 7.1 — Add architecture PR checklist

> **Status: Completed.** Architecture PR checklist added to `docs/architecture-guardrails.md`.

Acceptance criteria:

- ~~Checklist exists in PR template or contributor docs.~~ **Done** — `docs/architecture-guardrails.md`.
- ~~Reviewers use it for broad changes.~~ **Done**.

### Task 7.2 — Add architecture drift report to CI artifacts

> **Status: Completed.** `make arch-report` produces deterministic JSON + Markdown. CI publishes arch-report as an artifact.

Acceptance criteria:

- ~~A contributor can see file/package growth over time.~~ **Done** — arch-report tracks hotspots.
- ~~Hotspots become visible before they become painful.~~ **Done**.

### Task 7.3 — Define enterprise overlay boundary

> **Status: Completed.** `docs/enterprise-extension-boundaries.md` created with allowed/forbidden integration points.

Acceptance criteria:

- ~~A document such as `docs/enterprise-extension-boundaries.md` exists.~~ **Done**.
- ~~It lists allowed and forbidden integration points.~~ **Done**.
- ~~It prevents enterprise code from becoming a parallel fork of core.~~ **Done**.

---

## Suggested implementation order

### First pull request group — very low risk

> **Status: All completed.**

1. ~~Add architecture metrics report.~~ **Done**.
2. ~~Add critical-file warning budgets.~~ **Done**.
3. ~~Split `stdhttp/server.go` by file/concern without changing APIs.~~ **Done**.
4. ~~Deprecate/remove `stdhttp.BuildExecutor` if feasible.~~ **Done** — both `stdhttp.BuildExecutor` and `runtimebundle.BuildExecutor` deleted; `runtimebundle.Build` is the canonical builder.

### Second pull request group — composition cleanup

> **Status: All completed.**

1. ~~Normalize `runtimebundle.BuildOptions` internally.~~ **Done**.
2. ~~Extract observability/auth/model/persistence/extension/executor build units.~~ **Done**.
3. ~~Add tests for resource cleanup and startup error paths.~~ **Done**.

### Third pull request group — registry split

> **Status: All completed.**

1. ~~Create standard bundle package.~~ **Done** — `internal/standardplugins/`.
2. ~~Move `standard_table.go` and installer code.~~ **Done**.
3. ~~Move feature bridge out of registry.~~ **Done** — bridge deleted.
4. ~~Narrow hexagonal baseline exception.~~ **Done** — zero exceptions.

### Fourth pull request group — executor internals

> **Status: All completed.**

1. ~~Add characterization tests.~~ **Done**.
2. ~~Group executor fields.~~ **Done**.
3. ~~Extract request preparation.~~ **Done**.
4. ~~Extract route planning setup.~~ **Done**.
5. ~~Extract attempt opening.~~ **Done**.
6. ~~Extract stream assembly.~~ **Done**.
7. ~~Add executor file budget.~~ **Done** — 150 lines.

### Fifth pull request group — governance and enterprise boundary

> **Status: All completed.**

1. ~~Add core admission checklist.~~ **Done**.
2. ~~Add package-doc rule for new core packages.~~ **Done**.
3. ~~Define enterprise overlay boundary.~~ **Done**.
4. ~~Add CI architecture report artifact.~~ **Done**.

---

## Acceptance definition for the whole effort

The de-slopification effort can be considered successful when:

- ✅ `runtimebundle.Build` is a readable orchestration facade over smaller build units.
- ✅ `pluginreg` no longer owns concrete standard bundle tables and legacy feature migration logic.
- ✅ `runtime.Executor` still exposes `Execute`, but internal concerns are grouped and testable.
- ✅ `stdhttp` is split by HTTP concern and no longer mixes all server behavior in one file.
- ✅ Architecture tests cover not only forbidden imports, but also critical file/package drift.
- ✅ The hexagonal baseline has fewer `extract`/`exception` entries or more precise retirement targets.
- ✅ No behavior changes are visible to clients.
- ✅ The project remains Go-idiomatic: explicit construction, small interfaces, no DI container, no reflection magic, no hidden registration.

**All acceptance criteria met. The de-slopification effort is complete.**

