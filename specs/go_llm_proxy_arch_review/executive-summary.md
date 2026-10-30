# Executive summary — aiproxer architecture review

Generated: 2026-07-07

## Bottom line

`aiproxer` already has a strong architectural foundation for a Go proxy/control-plane project. The most valuable design choices are already present:

- canonical protocol-neutral `pkg/aipapi` request/event contracts;
- stable plugin SDK contracts in `pkg/aipsdk`;
- official protocol/provider implementations placed under `internal/plugins` rather than inside the runtime core;
- explicit standard-distribution assembly through `cmd/aipstd`, `internal/pluginreg`, `internal/infra/runtimebundle`, and `internal/stdhttp`;
- automated architecture tests that enforce important boundaries such as no concrete plugin imports from core, no provider SDK leakage into public contracts, no `init()` registration in standard bundle paths, and line-count budgets for high-risk trees.

The project is **not architecturally broken**. The current risk is more subtle: the codebase is entering the stage where composition and orchestration packages can quietly become gravitational centers. If unchecked, `runtimebundle`, `pluginreg`, `stdhttp`, and `runtime.Executor` can grow into “god composition” / “god orchestration” zones while the repo still appears hexagonal on paper.

## High-value conclusion

Do **not** rewrite the project. Do **not** rename folders into generic Clean Architecture buckets. The repository already has a Go-idiomatic, project-specific architecture. The correct move is a set of targeted de-slopification refactors that preserve behavior while reducing coupling and cognitive load.

The main architectural goal should be:

> Keep `pkg/aipapi` and `pkg/aipsdk` as stable contracts, keep concrete plugins outside core, and aggressively prevent the standard-distribution assembly layer from becoming a second core.

## Priority risks

| Priority | Risk | Why it matters |
| --- | --- | --- |
| P0 | `internal/infra/runtimebundle` is too broad | It assembles auth, control plane, continuity, secure sessions, model catalog/registry, accounting, HTTP clients, executor dependencies, and extension snapshot wiring. It is already classified as `extract` in the hexagonal baseline. |
| P0 | `internal/pluginreg` mixes registry, standard bundle, installers, security metadata, feature migration wrappers, and auth renderers | Its current name suggests a narrow registry, but the package has become the home for many standard-distribution concerns. That invites more unrelated plugin-adjacent behavior to accumulate there. |
| P0 | `runtime.Executor` has too many reasons to change | It owns routing, attempt lifecycle, secure-session preparation, extension snapshot usage, accounting hooks, stream recovery, interleaved-thinking wrappers, diagnostics, and metrics. The single entry point is good; the internal shape is becoming too dense. |
| P1 | `internal/stdhttp/server.go` is doing too much | It mounts diagnostics, metrics, admin endpoints, model catalog, control-plane query routes, frontend routes, middleware stack, server lifecycle, shutdown, and convenience build path. |
| P1 | Guardrails are strong but coarse | Line budgets and dependency exclusions are valuable, but they do not yet prevent per-file gravity wells, overly broad option bags, or package role drift. |

## Recommended next sequence

1. **Make the standard build path unambiguous.** Prefer `runtimebundle.Build(...)` followed by `stdhttp.RunWithRuntime(...)`. Keep any compatibility wrapper only as a thin deprecated convenience or retire it if unused.
2. **Add finer-grained architecture checks.** Keep current line budgets, but add critical-file budgets, direct-import allowlists with retirement triggers, and a generated architecture report for reviewers.
3. **Split `runtimebundle` by responsibility behind the same public facade.** Keep `Build` stable, but move internal construction into named build units with small input/output structs.
4. **Shrink `pluginreg` to a true registry.** Move standard bundle tables/installers and feature bundle merging into a separate standard-distribution package or a clearly named sub-area.
5. **Extract collaborators from `runtime.Executor`.** Preserve `Execute(ctx, call)` but move preparation, route planning, attempt opening, and stream assembly into cohesive internal components.
6. **Refactor `stdhttp` by mounting concern.** Keep the package if needed, but split diagnostics/admin/frontend/middleware/listener responsibilities into focused files and smaller functions.

## Expected impact

| Refactor area | Expected benefit |
| --- | --- |
| Runtimebundle decomposition | Lower startup/composition cognitive load; easier enterprise overlay; fewer accidental cross-concern edits. |
| Pluginreg slimming | Cleaner standard bundle ownership; easier support for custom/enterprise bundles without contaminating registry semantics. |
| Executor collaborator extraction | Easier reasoning about no-retry-after-output, secure-session gates, route planning, and accounting behavior independently. |
| Stdhttp cleanup | Less risk that operator/admin routes and protocol frontends become tangled. |
| Better guardrails | Prevents regression into a “looks clean but feels unmaintainable” architecture. |

## What not to do

- Do not introduce a DI container.
- Do not use Go `plugin` loading for v1.
- Do not move everything into `domain`, `ports`, `adapters`, `services` directories.
- Do not create interfaces only for tests or symmetry.
- Do not split into multiple Go modules yet unless provider SDK dependency isolation becomes a real build/distribution problem.
- Do not hide standard plugin registration behind `init()`, global registries, or lazy `sync.Once` registration.

