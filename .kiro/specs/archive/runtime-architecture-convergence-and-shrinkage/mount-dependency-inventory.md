# Task 3.1 / 3.5 — stdhttp mount dependency inventory

Deterministic inventory of production HTTP mount helpers and composition roots
after Task 3.5 deleted the broad `runtimebundle.RequestPlane` compatibility
direction. Desired cohesive groups follow approved `design.md` names
(`StandardHTTPInput` / `HTTP*Input`).

Lifecycle/closer at every mount boundary: **none** (ownership stays in
generation/process composers; mounts must not accept closers).

## Desired composition groups (design § StandardHTTPInput)

| Group field | Type | Concern |
| --- | --- | --- |
| `Core` | `HTTPCoreInput` | execution / routing capability only |
| `Security` | `HTTPSecurityInput` | auth / session / authority surfaces only |
| `Operations` | `HTTPOperationsInput` | metrics / tracing / diagnostics / admin query only |
| `Models` | `HTTPModelInput` | catalog / inventory diagnostic surfaces only |
| `Frontends` | `HTTPFrontendInput` | executor / registry / default-route / frontend configuration only |

Prohibited in every group: `*runtimebundle.Built`, broad `runtimebundle.RequestPlane`,
resource ledger, generic closer list, `io.Closer`, close/shutdown callbacks,
host/coordinator lifecycle owner, generic dependency getter.

## Mount helpers

| Helper | Source | Historical Built fields (pre-3.2) | Desired group(s) + capability | Route(s) | Behavior test(s) | Lifecycle at mount |
| --- | --- | --- | --- | --- | --- | --- |
| `mountMetrics` | `internal/stdhttp/mount_metrics.go` / `mountMetricsInput` | `Built.Metrics` (Registry, HTTP) | `Operations`: Metrics bundle | configured metrics path (default `/metrics`) | `TestStackHTTPHandler_recoveredPanic_combinedMetricsAccessAndSafeBody`; `TestMountContract_NilOptionalCapabilitiesSkipMounts` | none |
| `mountDiagnostics` | `internal/stdhttp/mount_diagnostics.go` / `mountDiagnosticsInput` | `Built.Store`; `Built.SecretGuardInventory`; also `Exec`, `Reg`, `App`/`Registrations` (non-Built params) | `Operations`: Store, SecretGuardInventory, inventory/route-trace/pprof sinks; `Core`: Executor route-trace attach | health / attempts / inventory / route-trace / pprof (config paths) | `TestStandardMiddlewareMountParity_ComposeStandardHTTPRouteSetAndStack`; diagnostics suites under `internal/stdhttp` | none |
| `mountModelCatalogDiagnostics` | `internal/stdhttp/mount_diagnostics.go` / `diagnosticsMount` | `Built.CatalogRuntime` | `Models`: CatalogRuntime | `model_catalog.diagnostics_path` | `TestModelCatalogDiagnostics_*` | none |
| `mountModelInventoryDiagnostics` | `internal/stdhttp/mount_diagnostics.go` / `diagnosticsMount` | `Built.ModelRegistryRuntime` | `Models`: ModelRegistryRuntime | `model_inventory.diagnostics_path` | `TestModelRegistryStatusHandler_*`; `TestComposeStandardHTTP_openAIModelsAndModelRegistryDiagMounted` | none |
| `mountSecureSessionDiagnostics` | `internal/stdhttp/mount_securesession.go` / `mountSecureSessionDiagnosticsInput` | `Built.SecureSessionStore` | `Security`: SecureSessionStore | `secure_session.diagnostics_path_prefix` (`GET` base + `/`) | `TestSecureSessionDiagnostics_mount_matchesComposePattern` | none |
| `mountKeepwarmAdmin` | `internal/stdhttp/mount_admin.go` / `mountAccountingAdminInput` | `Built.KeepwarmAdmin`; `Built.KeepwarmAdminEnabled` | `Operations`: KeepwarmAdmin, authenticated diagnostics wrapper | fixed `/admin/keepwarm` | `TestKeepwarmAdminMountedWithDiagnosticsSecret` | none |
| `mountAccountingAdmin` | `internal/stdhttp/mount_admin.go` / `mountAccountingAdminInput` | `Built.TokenAccountingAdmin`; fallback `Built.Executor.AdminCountService` | `Operations`: TokenAccountingAdmin; `Core`: Executor admin-count fallback | `accounting.admin.path` | `TestTokenAccountingAdminMounted*`; `TestTokenAccountingAdminDisabledNotRegistered` | none |
| `mountAdminMounts` | `internal/stdhttp/mount_admin.go` / `adminMountsInput` | `Operations.AdminMounts`; registered protected operator handlers (task 4.2) | `Operations`: AdminMounts | registered literal `/admin/...` mount paths (duplicates/nesting/built-in collisions rejected) | `TestAdminMountsMountedAndProtected`; `TestAdminMountsRejectInvalidPaths`; `TestAdminMountsRejectOverlaps` | none |
| `mountControlPlaneQuery` | `internal/stdhttp/mount_admin.go` / `controlPlaneQueryMount` | `Built.ControlPlaneQueries`; `Built.ReadinessReport` | `Operations`: ControlPlaneQueries, ReadinessReport | `control_plane.query.path_prefix` (+ `/`) | `TestControlPlaneQuery_MountedWhenEnabledAndProtected`; `TestControlPlaneQuery_NotMountedWhenDisabled` | none |
| `mountAccountingAuthorityQuery` | `internal/stdhttp/mount_admin.go` / `accountingAuthorityQueryMount` | `Built.UsageAuthority`; `Built.ConcurrencyAuthority`; `Built.Executor.ConcurrencyProvider` | `Security`: UsageAuthority, ConcurrencyAuthority; `Core`: ConcurrencyProvider | `accounting.authority.query.path_prefix` (+ `/`, leases) | `TestAccountingAuthorityQuery*` (`authority_mount_test.go`) | none |
| `mountRouteOverrideAdmin` | `internal/stdhttp/mount_admin.go` / `routeOverrideAdminMount` | generation-bound override command handler | `Operations`: RouteOverrideAdmin | `routing.override_admin.path_prefix` (+ `/`) | `TestComposeStandardHTTP_routingOverrideAdminRequiresAccessAuth`; `TestComposeStandardHTTP_routingOverrideAdminNotOnFrontendPaths` | none |
| `MountBundledFrontends` | `internal/stdhttp/mount.go` / `MountBundledFrontendsInput` | _(no Built field today)_ — `Exec`, `Reg`, route/plugins/admission/traffic from caller | `Frontends`: Executor, Registry, DefaultRoute, RoutePrefixes, Plugins, DecodeAdmission, TrafficPorts, PreRequestKeepalive | frontend protocol routes (+ A-leg cancel via `mountALegCancel`) | `TestMountBundledFrontends_*`; `TestFrontendMountOptions_*`; `TestBundledFrontends_authRequired_*` | none |
| `MountBundledFrontendsLegacy` | `internal/stdhttp/mount.go` | delegates to `MountBundledFrontends` | `Frontends` (same) | same as bundled frontends | `TestMountBundledFrontends_*` | none |
| `mountALegCancel` | `internal/stdhttp/cancel.go` | _(no Built)_ — `Exec` only | `Frontends` / `Core`: Executor cancel | A-leg cancel prefix | frontend mount suites | none |
| `stackHTTPHandler` | `internal/stdhttp/middleware.go` / `stackHTTPInput` | `Built.HTTPAuthProviders` | `Security`: HTTPAuthProviders; Operations metrics via `HTTPProm` param; tracing from cfg | _(wraps entire mux)_ | `TestStandardMiddlewareMountParity_StackOrderObservables`; `TestMountContract_StackAuthBeforeInnerHandler`; `TestDownstreamServerMiddleware_*`; `TestOuterRecoveryMiddleware_*` | none |
| `mountProxyMemory` | `internal/stdhttp/proxy_memory_mount.go` | projected `Operations.ProxyMemory` handler (feature-host projection; no Built field) | `Operations`: ProxyMemory — proxy-memory REST + MCP surface; nil projection mounts nothing | fixed `/v1/lip/memory` + subtree `/v1/lip/memory/` | `TestComposeStandardHTTP_ProxyMemoryRoutesReachProjectedHandler`; `TestComposeStandardHTTP_ProxyMemoryUnmountedWithoutHandler` | none |

## Composition roots (consume mounts)

| Helper | Source | Current broad input | Desired after focused seam | Migration status | Behavior test(s) | Lifecycle at mount |
| --- | --- | --- | --- | --- | --- | --- |
| `prepareStandardHandler` | `internal/stdhttp/handler.go` | focused `StandardHTTPInput` only | already focused; closers owned by caller/generation | **strict Task 3.2 target** — focused composer/preparer; must accept `StandardHTTPInput` and no lifecycle owner | `TestComposeStandardHTTP_projectsWithoutBuiltRehydration`; `TestComposeStandardHTTP_diagnosticsHealthzMounted`; `TestComposeStandardHTTP_openAIModelsAndModelRegistryDiagMounted`; `TestStandardMiddlewareMountParity_*` | closers owned **above** mount boundary |
| `ComposeStandardHTTP` | `internal/stdhttp/request_plane.go` | `StandardHTTPInput` (+ explicit `cfg`/`log` params) | already the canonical `HandlerComposer` target | **canonical Task 3.4 composer** — the sole `runtimebundle.HandlerComposer` invoked by `CompileGeneration`/`GenerationCompiler`; RequestPlane compatibility composers are deleted (Task 3.5) | `TestComposeStandardHTTP_RouteConflictRejects`; `TestComposeStandardHTTP_ManagementRoutesNotMounted`; `TestComposeStandardHTTP_NilInputsRejected`; `TestStandardMiddlewareMountParity_*` | none at mount; generation owns resources |

Task 3.5 deleted `ComposeRequestPlane`, `standardHTTPInputFromRequestPlane`, broad
`runtimebundle.RequestPlane`, and `NewCompatRequestPlane`. Task 4.2 deleted
`runtimebundle.Built`, compatibility `runtimebundle.Build`, `NewStandardHandler`,
`standardHTTPInputFromBuilt`, `releaseBuiltResources`, `runClosers`, candidate
`Closers`, and `ResourceLedger.LegacyClosers`. `ComposeStandardHTTP` (via
`prepareStandardHandler`) is the sole composition root; `stdhttp.RunWithGenerationHost`
is the sole production serve path. Architecture gates prohibit reintroduction of
any deleted symbol or an equivalent structural shape.

## Middleware order (outer → inner; frozen)

`DownstreamServerMiddleware` → `outerRecoveryMiddleware` → optional OTel HTTP → optional Prometheus HTTP → `TraceMiddleware(RequestIDMiddleware)` → access log → `RecoveryMiddleware` → transport `auth.Middleware` → route mux.

Evidence: `TestStandardMiddlewareMountParity_StackOrderObservables`, `TestMountContract_StackAuthBeforeInnerHandler`, `TestMountContract_NilOptionalCapabilitiesSkipMounts`.
