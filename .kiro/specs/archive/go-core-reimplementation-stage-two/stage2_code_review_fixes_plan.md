Here’s a complete hardening plan focused on addressing the reviewer’s concerns without letting the proxy core grow into a God object. The plan keeps orchestration in core, provider semantics in plugins, and composition explicit and testable.
Plan Goal
- Fix stage-two architectural risks before expanding feature scope.
- Preserve the repo’s intended shape from .kiro/steering/structure.md:5 and .kiro/steering/routing-and-orchestration.md:18: small core, registry/plugin-first edges, streaming-first orchestration, clear A-leg/B-leg ownership, and no provider SDK leakage into core.
Non-Negotiable Outcomes
- Core routes by backend instance identity, not plugin kind.
- Production runtime never falls back to deterministic test defaults.
- One explicit standard-bundle assembler owns resources, lifecycles, and shutdown order.
- Health/observer seams are either truly wired or removed from the standard path.
- Continuity semantics are explicit and consistent across store backends.
- Bundle composition is explicit, not hidden behind init() side effects.
Execution Order
- 1. Split plugin kind from runtime instance identity.
- 2. Introduce one explicit standard-bundle assembler/resource owner.
- 3. Inject real clock/RNG and shared runtime services in production wiring.
- 4. Enroll all closers/lifecycles into owned shutdown.
- 5. Finish routing health/observer wiring.
- 6. Make continuity retention semantics explicit and consistent.
- 7. Replace init()-driven registration with explicit bundle construction.
- 8. Only then resume feature expansion.
Phase 0: Architecture Guardrails
- Freeze new product features until F1/F2/F3 are closed; this matches the review’s highest-risk items in .kiro/specs/archive/go-core-reimplementation-stage-two/stage2_code_review.md:89.
- Define a short “stage-three hardening contract” before code changes: core owns orchestration only, plugins own provider semantics, composition roots own resources, and stable IDs must represent runtime instances.
- Treat any change that adds provider-specific logic to internal/core/* as out of bounds.
- Keep file-size pressure visible; if a file exceeds ~350-400 LOC, split by responsibility before adding more logic.
Phase 1: Identity Model Split
- Introduce distinct concepts in config and SDK:
  - kind or factory_id: selects plugin factory type such as openai-responses.
  - instance_id or name: identifies one configured runtime instance such as openai-primary.
- Update internal/core/config/model.go:66 so plugin rows can carry both factory kind and runtime instance ID.
- Update pkg/lipsdk/registration.go:50 and related contracts so duplicate validation keys by (kind, instance_id) or equivalent explicit structure, not just one ID.
- Update internal/core/config/registrations.go:16 to preserve both identities.
- Update internal/stdhttp/wire.go:25 so Executor.Backends is keyed by backend instance ID, not factory kind.
- Update selector semantics so route targets and diagnostics refer to backend instance identity.
- Keep plugin registration/factory lookup keyed by kind; keep runtime routing keyed by instance ID. This is the critical separation that keeps composition honest.
- Add an adapter layer if needed so existing plugin factories remain minimally changed.
Identity Acceptance Criteria
- Two backends of the same kind can coexist with different instance IDs.
- Route selectors can target both instances independently.
- Attempt lineage records show backend instance identity.
- Diagnostics and route traces expose instance identity, not only kind.
- Existing single-instance configs can be migrated mechanically.
Phase 2: Explicit Standard-Bundle Assembler
- Create a single standard-bundle composition root responsible for:
  - decoding validated config,
  - constructing runtime services,
  - opening stores/clients/transports,
  - building the executor,
  - mounting frontends,
  - exposing diagnostics,
  - managing shutdown order.
- Keep runtime.App lean; do not turn it into a mega-bootstrapper. Instead, add a dedicated bundle assembly package, e.g. internal/stdhttp/bundle or internal/infra/runtimebundle, that returns a small owned object.
- The owned object should contain only assembled runtime dependencies:
  - executor,
  - store(s),
  - server dependencies,
  - optional observers/traces,
  - lifecycle/closer lists.
- cmd/lipstd/main.go:35 should select a standard bundle explicitly, not piece together ownership across runtime.App and stdhttp.Run.
- stdhttp.Run should stop opening core resources itself; it should serve an already-assembled runtime.
Assembler Design Principles
- runtime.App remains about validated runtime state plus hook bus/lifecycles, not backend/store creation.
- stdhttp becomes HTTP serving/mounting code, not resource owner.
- Provider-specific HTTP clients stay outside core.
- The assembler can depend on internal/pluginreg, but core packages must not.
Phase 3: Real Production Defaults
- Remove deterministic production fallback behavior from the active standard path.
- Inject a real wall clock and nondeterministic RNG during standard-bundle assembly into runtime.Executor.
- Leave deterministic clock/RNG construction available only in tests, fuzz harnesses, and explicit testkit builders.
- Consider moving clock/entropy helpers into internal/infra/ to make “test-only deterministic” vs “production real” an explicit decision.
- Update frontend response timestamp behavior so WallClock() is always available in standard runtime; see the current dependency in internal/plugins/frontends/openairesponses/handler.go:90.
Production Defaults Acceptance Criteria
- Standard runtime always has non-nil real clock and real RNG.
- Test harness can still build deterministic executor cheaply.
- Weighted routing behaves nondeterministically in production wiring and deterministically in tests.
Phase 4: Resource Ownership and Shutdown
- Introduce a small internal lifecycle/closer registry owned by the standard bundle.
- All opened resources must register into one owner:
  - continuity store,
  - optional shared HTTP transports/clients,
  - route observer sinks,
  - any future durable buffers or metrics sinks.
- Define shutdown order explicitly:
  - stop accepting HTTP,
  - cancel/drain in-flight requests,
  - stop plugin lifecycles,
  - close stores/transports/observer sinks.
- Ensure SQLite store closure is guaranteed when internal/core/continuity/sqlitestore/store.go:85 is used.
- Prefer lightweight interfaces near the composition root, e.g. a local type closer interface { Close() error }, rather than broad abstractions in core.
- Keep shutdown ownership outside internal/core/runtime unless the resource is truly core-owned.
Phase 5: Routing Health and Observer Wiring
- Decide which routing seams are part of the standard distribution now:
  - CandidateHealth,
  - RouteObserver,
  - RouteTrace,
  - structured logging.
- If these are real standard features, wire them in the assembler and ensure they affect runtime behavior, not just type surfaces.
- CandidateHealth should come from a standard observer/health package or a small infra service, not from plugin code.
- RouteObserver should have a small no-op default and optional concrete observers for diagnostics/metrics.
- RouteTrace should remain an optional in-memory diagnostics sink, but it should be enrolled in the same ownership model.
- If a seam will not be operational in stage three, remove it from the standard path or clearly park it behind nil defaults and docs. Do not leave “implied behavior” hanging in the executor surface.
Phase 6: Continuity Semantics Cleanup
- Pick one continuity contract and make config match it:
  - preferred: ttl and max_legs are continuity semantics across both memory and SQLite,
  - fallback: those fields become memory-store-specific and SQLite gets explicit retention config of its own.
- If choosing store-agnostic semantics:
  - add retention support for SQLite,
  - define pruning strategy and execution point,
  - make it observable and testable.
- If choosing store-specific semantics:
  - split config into store-private blocks,
  - keep core continuity config generic,
  - fail config load when incompatible fields are used with the selected store.
- The simpler, more “architecturally honest” choice is likely store-specific config with a common minimal continuity contract, unless durable retention is essential now.
- Do not let internal/core/continuity/store.go:14 silently imply cross-store parity that does not exist.
Phase 7: Explicit Bundle Registration
- Replace internal/pluginreg/init.go:3 global side effects with explicit registration table construction.
- Create an explicit bundle definition, e.g. typed bundle metadata colocated with internal/pluginreg/register_standard.go (and *_install.go), that returns:
  - backend factories,
  - frontend mounts,
  - feature factories.
- cmd/lipstd should choose the standard bundle explicitly at compile time.
- pluginreg can remain a registry/factory helper layer, but the populated bundle should be provided directly, not hidden behind imports and init().
- This improves test isolation and makes custom or reduced bundles straightforward.
- Keep it compile-time and explicit; do not introduce reflection, DI containers, or dynamic plugin loading.
Phase 8: Shared Transport/Client Factory
- Add a shared transport/client factory owned by the standard bundle for ACP, Bedrock, and any future backends that need HTTP clients.
- Keep transport policy out of backend plugin internals where possible:
  - timeouts,
  - proxies,
  - pooling,
  - TLS knobs,
  - tracing wrappers,
  - user-agent.
- Backend plugins should accept an injected client/transport config and stay focused on canonical-to-provider mapping.
- Do not centralize provider semantics; centralize only shared operational plumbing.
Phase 9: Correlation and Diagnostics Separation
- Install request/trace correlation middleware unconditionally in standard HTTP serving.
- Let diagnostics.enabled control endpoints and extra admin surfaces, not core request tracing.
- Keep request ID generation and trace propagation in shared HTTP middleware, not duplicated in frontends.
- Ensure route trace and attempt diagnostics show backend instance IDs and attempt lineage consistently.
Suggested Package/File Impact
- internal/core/config/
  - split plugin identity fields,
  - tighten config validation,
  - add migration/defaulting behavior.
- pkg/lipsdk/
  - registration/requirement contracts,
  - possibly factory input structs that distinguish kind from instance identity.
- internal/stdhttp/
  - move assembly out of server.go,
  - keep server/mount code thin,
  - always install correlation middleware.
- internal/core/runtime/
  - minimal surface adjustments for instance identity and injected services,
  - avoid expanding executor responsibilities.
- internal/core/continuity/
  - align retention semantics,
  - expose only store-neutral contracts to core callers.
- internal/core/continuity/sqlitestore/
  - add retention/pruning or document/store-specific behavior explicitly,
  - ensure closure is enrolled.
- internal/pluginreg/
  - replace implicit registration with explicit standard bundle construction,
  - keep registry/factory logic small and testable.
- internal/infra/
  - likely home for real clock, entropy, shared transport/client factory, and possibly closer helpers.
- internal/plugins/backends/*
  - accept injected clients/config where needed,
  - avoid owning global transport defaults.
Testing Plan
- Identity
  - configure two backend instances of the same kind and prove both are independently routable.
  - validate duplicate detection rejects duplicate instance IDs within a kind or bundle rules as intended.
- Production defaults
  - standard bundle executor has real clock/RNG injected.
  - deterministic test builder remains available and stable.
- Resource ownership
  - SQLite store closes on shutdown.
  - shutdown order does not leak active resources.
- Routing seams
  - candidate health exclusions affect planner behavior in the standard bundle.
  - route observers and route traces receive instance-identity-aware decisions.
- Continuity semantics
  - retention behavior is explicit and tested for the selected store contract.
  - invalid config combinations fail early and deterministically.
- Bundle composition
  - standard bundle registration works without init().
  - reduced/custom test bundles can be assembled explicitly.
Recommended Milestone Breakdown
- Milestone 1: Identity correction
  - config/schema split,
  - SDK registration updates,
  - backend map/routing updates,
  - diagnostics identity updates.
- Milestone 2: Explicit runtime ownership
  - standard-bundle assembler,
  - real clock/RNG injection,
  - closer/lifecycle registry,
  - stdhttp simplification.
- Milestone 3: Routing/runtime truthfulness
  - health/observer wiring,
  - unconditional correlation middleware,
  - route-trace/observer integration.
- Milestone 4: Continuity/store hardening
  - retention semantics decision,
  - SQLite retention or config split,
  - shutdown/cleanup proofs.
- Milestone 5: Bundle explicitness and transport cleanup
  - remove init() registration,
  - explicit standard bundle,
  - shared transport/client factory.
Risks to Watch
- Do not let the identity split leak provider-specific naming into core canonical contracts.
- Do not move backend/store assembly into runtime.Executor; keep executor orchestration-only.
- Do not create a “super assembler” that knows too much business logic; it should only own composition and resource lifetime.
- Do not over-abstract transport or lifecycle interfaces unless there are at least two clear consumers.
- Do not break selector compatibility without a deliberate migration path.
Decision Points To Settle Before Implementation
- Preferred naming: kind + instance_id vs factory_id + name.
- Continuity policy: truly store-agnostic retention or store-specific config.
- Where to house standard-bundle assembler: internal/stdhttp/bundle vs internal/infra/runtimebundle.
- Whether route health in stage three is a real standard feature or deferred seam.
My recommendation:
- Use kind + instance_id.
- Treat retention as store-specific unless you are ready to implement SQLite pruning now.
- Put the assembler in a small dedicated composition package outside core.
- Wire route health/observer now, because the executor surface already advertises those seams.