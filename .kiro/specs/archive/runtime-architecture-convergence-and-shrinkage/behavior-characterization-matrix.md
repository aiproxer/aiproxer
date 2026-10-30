# Behavior Characterization Matrix — Deletion Seams

**Task:** 1.3 — Add behavior characterization matrix for deletion seams
**Feature:** `runtime-architecture-convergence-and-shrinkage`
**Inventory source:** [`baseline/migration-inventory.json`](./baseline/migration-inventory.json) (Task 1.1, reviewed SHA `efe4624909cea318c7211d5cb3734059d3210802`)
**Evidence class:** **byte/behavior compatibility** unless marked **architecture-only**

This matrix is auditable: every production deletion seam/caller from the inventory is named, mapped to exact tests (or proven dead), and linked to the later deletion/migration task. Comment-only inventory hits are ignored unless they encode a migration constraint.

---

## Legend

| Column | Meaning |
| --- | --- |
| Seam | Inventory concept / production symbol |
| Production path | File(s) with non-comment production roles |
| Behavior tests | Exact `Test*` names and paths |
| Evidence | `behavior` = observable runtime contract; `arch` = structure/declaration only |
| Delete/migrate | Later task that removes or replaces the seam |

---

## 1. Production deletion seams (inventory → tests → task)

### 1.1 `runtimebundle.Built` (broad dependency bag)

| Production path | Role | Behavior tests | Evidence | Delete/migrate |
| --- | --- | --- | --- | --- |
| `internal/infra/runtimebundle/built.go` | declaration | `TestInitialGeneration_LegacyServeStillBuildsBuilt` (`internal/infra/runtimebundle/initial_generation_test.go`); `TestProcessServices_BuildCompatibilityRetainsAggregateCleanup` (`process_services_test.go`) | behavior | **4.2** delete Built |
| `internal/infra/runtimebundle/build.go` | construction | `TestBuild_*` suite (`build_test.go` and sibling Build callers); `TestBootstrapCompatibility_BuildPartialCleanupOnCandidateFailure` | behavior | **4.1→4.2** |
| `internal/infra/runtimebundle/bootstrap_plan.go` | type/field usage (legacy Built serve path) | `TestBuildBootstrap_serveSetsBuiltExecutor` (`bootstrap_plan_test.go`); `TestInitialGeneration_LegacyServeStillBuildsBuilt` | behavior | **5.5** / **4.2** |
| `internal/infra/runtimebundle/bootstrap_host.go` | field_usage (`Built=nil` in generation-host mode) | `TestInitialGeneration_BootstrapPublishesGenerationOne` | behavior | **5.5** (dual product removal) |
| `internal/infra/runtimebundle/terminal_work.go` | type_usage | covered via Build/terminal-work suites (`terminal_work_test.go`) | behavior | **4.2** |
| `internal/stdhttp/{handler,middleware,mount_*,server}.go` | Built mount params | `TestNewStandardHandler_*` / `TestMount*` / `TestTokenAccountingAdminMounted*` (`mount_test.go`, `server_identity_stack_test.go`, `control_plane_mount_test.go`, `authority_mount_test.go`); **Task 1.3:** `TestStandardMiddlewareMountParity_ComposeRequestPlaneRouteSetAndStack`, `TestStandardMiddlewareMountParity_StackOrderObservables` | behavior | **3.2→3.5→4.2** |
| `internal/stdhttp/request_plane.go` | compatibility_adapter fields via `requestPlaneAsBuilt` | `TestComposeRequestPlane_RouteConflictRejects`, `TestComposeRequestPlane_ManagementRoutesNotMounted`; **Task 1.3 mount parity** | behavior | **3.5** / **4.4** |

**Comment-only / non-caller (ignored as callers):** `generation_pin_tracker.go`, `build_extension.go`, `build_model.go`, `candidate_compile.go`, `generation_bundle.go`, `options.go`, `runtimebundle/request_plane.go`, `resource_ledger.go`, `runtimehost/{generation,request_binding,request_plane}.go`, `stdhttp/route.go`.

### 1.2 Compatibility `Build` (`runtimebundle.Build` / `lipruntime.Build`)

| Production path | Role | Behavior tests | Evidence | Delete/migrate |
| --- | --- | --- | --- | --- |
| `internal/infra/runtimebundle/build.go` | declaration | Build suite + `TestBootstrapCompatibility_*` | behavior | **4.2** |
| `internal/infra/runtimebundle/bootstrap_plan.go:209` | **sole production caller** of `runtimebundle.Build` | `TestInitialGeneration_LegacyServeStillBuildsBuilt`; `TestBuildBootstrap_serveSetsBuiltExecutor`; `TestBootstrapCompatibility_BuildPartialCleanupOnCandidateFailure` | behavior | **4.1→4.2** / **5.2** |
| `pkg/lipruntime/build.go` | public `lipruntime.Build` declaration (calls `BuildBootstrap`+`AttachReloadHost`, not `runtimebundle.Build` directly) | `TestBuildCompatibility_ValidExampleReady`, `TestBuildCompatibility_StrictFixturesReject`; `TestBuild_*` / `TestReloadFacade_*` / `TestClose_*` | behavior | **8.1** thin facade; **5.2** BuildHost |

### 1.3 `stdhttp.RunWithRuntime`

| Production path | Role | Behavior tests | Evidence | Delete/migrate |
| --- | --- | --- | --- | --- |
| `internal/stdhttp/server.go` | **declaration only** | `TestRunWithRuntime_*` (`server_test.go`); `TestRunWithRuntime` posture (`runwithruntime_posture_test.go`); `TestRegistryRequired` caller path | behavior (test-only callers) | **4.3** |

**Proven dead in production:** `rg -n 'RunWithRuntime\(' --glob '*.go' --glob '!*_test.go'` → sole hit is the declaration in `server.go`. All other production inventory hits are **comment_mention**. Serve uses `BuildBootstrap` + `AttachReloadHost` + `RunWithGenerationHost`.

### 1.4 `RequestPlane` + `requestPlaneAsBuilt`

| Production path | Role | Behavior tests | Evidence | Delete/migrate |
| --- | --- | --- | --- | --- |
| `internal/infra/runtimebundle/request_plane.go` | declaration | `TestComposeRequestPlane_*`; compile/reload generation suites | behavior | **3.3–3.5** narrow capabilities |
| `internal/infra/runtimebundle/compile_generation.go` (+ provider resolver, reload_host) | code_reference | `TestCompileGeneration_*`, `TestInitialGeneration_*` | behavior | **3.4** |
| `internal/infra/runtimehost/{generation,dispatcher,executor,lease,lifecycle_worker,coordinator}.go` | code_reference | runtimehost generation/dispatcher suites; no-drop certs | behavior | **3.3** / **7.x** |
| `internal/stdhttp/request_plane.go` | composer + **`requestPlaneAsBuilt` declaration+caller** | `TestComposeRequestPlane_RouteConflictRejects`, `TestComposeRequestPlane_ManagementRoutesNotMounted`; **Task 1.3:** `TestStandardMiddlewareMountParity_ComposeRequestPlaneRouteSetAndStack` | behavior | **3.5** delete `requestPlaneAsBuilt` |
| `pkg/lipruntime/build.go` | code_reference (composer wire) | public Build/Close/reload suites | behavior | **8.1** |

**Search:** `rg -n 'requestPlaneAsBuilt\(' --glob '*.go' --glob '!*_test.go'` → declaration + single call site in `stdhttp/request_plane.go` only.

### 1.5 `AttachReloadHost`

| Production path | Role | Behavior tests | Evidence | Delete/migrate |
| --- | --- | --- | --- | --- |
| `internal/infra/runtimebundle/reload_host.go` | declaration | `TestAttachReloadHost_UsesStartupFixedStreamRecoverySnapshot` | behavior | **5.5** |
| `cmd/lipstd/command.go` | production caller (serve) | `TestSignalReload_*` / serve bootstrap tests; rollback `TestServeStartupRollback_*` | behavior | **5.2→5.5** |
| `pkg/lipruntime/build.go` | production caller (public Build) | `TestBuild_BindsCoordinatorAndStableExecutorView`; `TestReload_*` | behavior | **5.2→5.5** |

**Comment-only:** `serve_rollback.go`, `bootstrap_plan.go` (ignored as callers).

### 1.6 Duplicate reload contracts / `reload_map`

| Production path | Role | Behavior tests | Evidence | Delete/migrate |
| --- | --- | --- | --- | --- |
| `internal/core/configreload/model.go` (+ history) | canonical internal vocabulary | configreload / runtimehost reload suites | behavior | retain; **2.1–2.2** migrate consumers |
| `pkg/lipruntime/reload.go` | mirrored public types | `TestReloadFacade_*` (`reload_facade_external_test.go`) | behavior (byte-compatible categories) | **2.3** aliases |
| `pkg/lipruntime/reload_map.go` | field-for-field mapping | `TestReloadFacade_ExactCategoryMapping` | behavior | **2.3** delete map |

### 1.7 Deprecated public `Options` + `normalize`

| Production path | Role | Behavior tests | Evidence | Delete/migrate |
| --- | --- | --- | --- | --- |
| `pkg/lipruntime/options.go` | deprecated field declarations | `TestBuild_Legacy*` / registration compose suites | behavior | **8.2–8.4** |
| `pkg/lipruntime/normalize.go` | conversion | same + `TestBuild_PublicOnlyOptions` | behavior | **8.2–8.4** |

**Comment-only:** `pkg/lipruntime/build.go` mention (ignored as caller).

---

## 2. Required area coverage (startup → shutdown)

| Area | Existing characterization | Task 1.3 gap fill | Delete/migrate context |
| --- | --- | --- | --- |
| Standard startup | `TestBuildBootstrap_*`, `TestInitialGeneration_BootstrapPublishesGenerationOne`, `TestBootstrapCompatibility_*`, `cmd/lipstd` serve gate tests | — (covered) | **5.2** BuildHost |
| Legacy `Build` | `TestInitialGeneration_LegacyServeStillBuildsBuilt`, Build suites, `TestBootstrapCompatibility_BuildPartialCleanupOnCandidateFailure` | — | **4.2** |
| Generation startup/publication | `TestInitialGeneration_*`, `TestCompileGeneration_*` | — | **3.4** / **7.x** |
| HTTP mounts + middleware | scattered `TestMount*` / `TestStackHTTPHandler_*` / `TestComposeRequestPlane_*` | **`TestStandardMiddlewareMountParity_ComposeRequestPlaneRouteSetAndStack`**, **`TestStandardMiddlewareMountParity_StackOrderObservables`** | **3.1–3.5** |
| Reload host attachment | `TestAttachReloadHost_*`, `TestBuild_BindsCoordinatorAndStableExecutorView`, SIGHUP/API reload suites | — | **5.5** / **6.x** |
| Public facade + legacy Options | `TestBuildCompatibility_*`, `TestBuild_*`, `TestReloadFacade_*`, registration compose | **`TestCapabilityReporting_DogfoodFacadeHostSnapshot`** (+ existing Phase55 executable capability) | **8.1–8.4** |
| Validation / check-config | `TestCheckConfigCompatibility_*`, `TestRunCommand_checkConfig_*` | **`TestCheckConfig_NonPublicNoListenAndPrivateCleanup`** (wraps `cleanupCheckConfigBootstrap` on command-owned result) | **5.4** ValidateDistribution |
| Shutdown / cleanup ownership | `TestClose_Idempotent`, `TestClose_HonorsDeadlineWithoutPrematureProcessClose`, `TestServeStartupRollback_*`, `TestJoinInitialFailureCleanup_*` | **`TestClose_RetryAfterOwnedCloserErrorExactlyOnceProcess`**, **`TestBootstrapPartialCleanup_ComposeFailureClosesOwnersOnce`** | **7.4** / **8.1** |

---

## 3. Task 1.3 gap characterization (why each was missing)

| Gap | New / extended test | Why genuinely missing before |
| --- | --- | --- |
| Middleware/mount parity | `TestStandardMiddlewareMountParity_ComposeRequestPlaneRouteSetAndStack` (`internal/stdhttp/middleware_mount_parity_test.go`); `TestStandardMiddlewareMountParity_StackOrderObservables` (`middleware_mount_parity_stack_test.go`) | Mounts and stack behaviors existed piecemeal; no single freeze of request-plane route set + outer Server policy + recovery observables for Built replacement |
| Partial bootstrap cleanup | `TestBootstrapPartialCleanup_ComposeFailureClosesOwnersOnce` + strengthened `TestInitialGeneration_CompileFailureRollsBackProcessServices` (assert `ShutdownTracing == nil`) | Process close was asserted; **no-double-close tracing handoff** was not locked |
| Public Close retry | `TestClose_RetryAfterOwnedCloserErrorExactlyOnceProcess` (`pkg/lipruntime/close_retry_characterization_test.go`) | Deadline retry existed (`TestClose_HonorsDeadlineWithoutPrematureProcessClose`); **owned closer error → retry with Process exactly-once** was not |
| Capability reporting | `TestCapabilityReporting_DogfoodFacadeHostSnapshot` (`pkg/lipruntime/capability_reporting_characterization_test.go`); also references `TestPhase55_FacadeExposesExecutableGenerationWithoutInternalTypes` | Phase55 covers injected economics; dogfood facade Ready/Snapshot/attachment flags/closed vocabulary were not frozen together |
| check-config non-public | `TestCheckConfig_NonPublicNoListenAndPrivateCleanup` (`cmd/lipstd/check_config_nonpublic_test.go`) | Compatibility/exit-text tests existed; **no-listen + command-owned private cleanup** was not |

### check-config residual (do not guess away)

Current `runCheckConfigCommand` uses `BootstrapServe` + `ComposeRequestPlane`, which **publishes generation 1 then retires** it. Task **5.4** (`ValidateDistribution`) must remove manager publication. Task 1.3 freezes: no data-plane listen, and `cleanupCheckConfigBootstrap` (package-private seam) is invoked exactly once on the **command-owned** bootstrap result leaving no open generations / closed process. The test wraps that seam (not `t.Parallel`; restores via `t.Cleanup`) rather than re-running `BuildBootstrap`. It does **not** claim unpublished dry-run yet.

---

## 4. Proven-dead production paths

| Symbol | Deterministic search | Result |
| --- | --- | --- |
| `RunWithRuntime(` | `rg -n 'RunWithRuntime\(' --glob '*.go' --glob '!*_test.go'` | Declaration in `internal/stdhttp/server.go` only; no production callers |
| `runtimebundle.Build(` | inventory + `rg` on production files | Single caller: `bootstrap_plan.go` legacy Built path; public `lipruntime.Build` does not call it |
| Comment-only Built/RunWithRuntime/Options mentions | inventory `comment_mention` role | Not migration callers; deletion gates must key off code roles |

---

## 5. Architecture-only vs behavior

| Item | Class |
| --- | --- |
| Mount route set, middleware Server/recovery observables, Close retry, bootstrap cleanup, check-config no-listen/cleanup, facade capability snapshot | **behavior** |
| Inventory role tags, package fan-in/out, line budgets (Task 1.2), upcoming Task 1.4 RED gates | **architecture-only** (not substituted for behavior locks here) |

---

## 6. Validation command (Task 1.3)

```bash
go test ./cmd/lipstd/... ./internal/stdhttp/... ./internal/infra/runtimebundle/... ./pkg/lipruntime/... \
  -run 'Compatibility|Standard|Bootstrap|Close|Capability|CheckConfig'
```
