# Enterprise extension boundaries

This document defines where enterprise features may attach to the OSS core and which integration points are forbidden. It prevents enterprise code from becoming a parallel fork of core.

Enterprise binaries attach to the same converged runtime as OSS `aipstd`: **one process runtime / `ProcessServices`**, **one generation runtime**, **one private-field host** from `runtimebundle.BuildHost` (shutdown via `Host.Close`; Manager-owned generation retirement), and **one reload contract** in `pkg/aipsdk/configreload`. Candidate assembly is not a public API. Do not invent a second host, bootstrap path, or legacy options adapter. Options use the non-money authority seams documented below plus the optional attachment seams; monetary behaviour is attached explicitly. The final ownership move is not finished: the legacy in-tree money composition is still part of this tree and still backs the stock command, so enterprise code attaches the same behaviour through the seams below while that in-tree path still exists.

## Allowed integration points

Enterprise features attach through these stable, documented seams:

| Seam | Package | How enterprise attaches |
| --- | --- | --- |
| Plugin SDK facades | `pkg/aipsdk/*` | Register feature plugins (`FeatureBundle`), backend adapters, or frontend mounts through `pkg/aipsdk` contracts. Enterprise binaries compose via public `pkg/aipruntime.Build` with `aipruntime.Options`. |
| Composition-root options | `pkg/aipruntime.Options` / `runtimebundle.ProductionOptions` | Use canonical non-money authority registration fields on public `aipruntime.Options`: `RequestRegistrations`, `AttemptRegistrations`, and `ConcurrencyRegistration` (plus observers/metering), and the optional attachment seams `UsageLimits`, `AdminMounts`, `EnterpriseCapabilities`, and `MeteringObservationSink`. Internal distributions use `runtimebundle.ProductionOptions` via `BuildHostInput.Production` into `runtimebundle.BuildHost`. Grouped `BuildOptions` sub-structs remain for focused internal composition helpers, not as a `BuildHost` parameter. |
| Control-plane ports | `internal/core/controlplane` | Implement control-plane `Store`, `QueryService`, `RetentionController`, or `Recorder` interfaces for enterprise persistence, audit, or search. |
| Hook bus | `internal/core/hooks` | Register enterprise submit/part/tool hooks through the standard hook bus. Prefer `FeatureBundle` over raw hooks for new code. |
| Secure-session authority | `internal/core/securesession/app` | Inject enterprise `Manager`, `Store`, or `GateRecording` implementations for custom session authority. |

## Forbidden integration points

Enterprise code MUST NOT:

- **Edit `runtime.Executor` directly** — add fields, modify `Execute`, or import `internal/core/runtime` to change orchestration behavior. New orchestration concerns belong behind hooks, extensions, or composition-root options.
- **Import deep runtime internals** — `internal/core/runtime`, `internal/core/stream`, `internal/core/leglifecycle`, or `internal/core/lineage` are not extension seams; they are use-case orchestration owned by the OSS core.
- **Fork the composition root** — enterprise binaries should call public `pkg/aipruntime.Build` with `aipruntime.Options` (observers, metering, canonical `RequestRegistrations` / `AttemptRegistrations` / `ConcurrencyRegistration`), not reimplement assembly. Internal distributions that already import `runtimebundle` compose via `runtimebundle.BuildHost` with registration-only `BuildHostInput` / `ProductionOptions` — not `BuildOptions`, which is a separate composition bag and is not accepted by `BuildHost`.
- **Bypass architecture guardrails** — the hexagonal baseline, line budgets, and import-boundary tests apply to enterprise code equally. Enterprise packages must not import `internal/core` packages beyond the allowed seams above.
- **Introduce global state or hidden registration** — no `init()` registration, package-level registries, `sync.Once` singletons, or reflection-based plugin loading.

## Typical enterprise attachment patterns

| Enterprise feature | Attach via |
| --- | --- |
| Audit/search | Control-plane `Store` + `QueryService` interfaces; traffic observers via `aipruntime.Options.TrafficObservers` (or `BuildHostInput.Production.TrafficObservers`). |
| Monetary behaviour | Attach one explicit `pkg/aipsdk/billing.Binding` through `aipruntime.BuildWithBilling`; attach usage-limit admission, settlement, release, and usage application through `aipruntime.Options.UsageLimits`, and protected operator routes through `aipruntime.Options.AdminMounts`. Do not add money fields to public `aipruntime.Options`. The in-tree composition that predates these seams still exists and is being replaced by them; enterprise code must not depend on it. Enterprise recipe (moves to the Enterprise tree): [`docs/billing-host-composition.md`](billing-host-composition.md). Do not attach a token-accounting ledger as a second monetary path. |
| SSO / user provisioning | Internal composition helpers using `runtimebundle.BuildOptions.Auth` (`RemoteDecider`, `OSIdentity`, `AuthEventSink`) — not currently a `aipruntime.Options` field and not a `BuildHost` parameter. |
| Custom routing policy | Internal `BuildOptions.Extensions` / `BuildOptions.Policy` helpers where those fields are wired; prefer FeatureBundle hooks for new code. |
| Custom backends | Backend plugin via `pkg/aipsdk` factory; register on a custom `*pluginreg.Registry` before public/internal Build. |

## Reviewer guidance

When reviewing enterprise-adjacent PRs, check:

- [ ] Does the change use only allowed integration points?
- [ ] Does the change avoid importing deep runtime internals?
- [ ] Does the change keep provider/protocol details out of core?
- [ ] Does the change avoid new global state, init registration, and lazy singletons?
- [ ] Does the change keep canonical streaming as the primary execution path?
