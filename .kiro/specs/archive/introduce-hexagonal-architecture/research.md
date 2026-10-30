# Research & Design Decisions

## Summary
- **Feature**: `introduce-hexagonal-architecture`
- **Discovery Scope**: Brownfield architectural formalization of an existing Go codebase
- **Key Findings**:
  - The repository already behaves like a ports-and-adapters system in several critical places.
  - The main gap is incomplete formalization and seam clarity, not the absence of architectural boundaries.
  - A hybrid approach is safer than a textbook rewrite: preserve the current package map, tighten only high-value seams, and add stronger guardrails.

## Research Log

### Composition roots and explicit wiring
- **Context**: Determine whether the system already has an explicit composition model worth preserving.
- **Sources Consulted**: `cmd/lipstd/main.go`, `internal/infra/runtimebundle/build.go`, `internal/stdhttp/server.go`
- **Findings**:
  - `cmd/lipstd/main.go` explicitly loads config, creates the registry, installs the standard bundle, constructs the runtime, and starts HTTP.
  - `internal/infra/runtimebundle/build.go` assembles the executor, continuity store, runtime snapshot, HTTP client, metrics, and transport-auth providers.
  - `internal/stdhttp/server.go` mounts diagnostics, frontends, and auth middleware at the transport edge.
- **Implications**: The design should preserve explicit composition roots and prevent hidden construction from leaking into the core.

### Core-owned orchestration already exists
- **Context**: Determine whether the core already owns the product-defining proxy behavior.
- **Sources Consulted**: `internal/core/runtime/executor.go`, `.kiro/steering/api-standards.md`, `.kiro/steering/routing-and-orchestration.md`
- **Findings**:
  - The executor already owns routing, capability negotiation, B2BUA continuity, attempt lineage, and backend-attempt orchestration.
  - Canonical-in-the-middle and streaming-first execution are already explicit project rules.
  - The core already rejects direct dependency on concrete frontend/backend/plugin packages through architecture tests.
- **Implications**: The design should treat `internal/core/*` plus `pkg/lipapi` as the effective application core and preserve those invariants.

### Weak seams are runtime-shaped, not fundamentally missing
- **Context**: Identify the main sources of coupling that still weaken a stricter hexagonal story.
- **Sources Consulted**: `internal/pluginreg/standard_table.go`, `internal/infra/runtimebundle/build.go`, `internal/archtest/extension_platform_boundaries_test.go`
- **Findings**:
  - `pluginreg` currently depends on `internal/core/runtime.Backend`, which works but is more runtime-shaped than an explicitly named application port.
  - Extension/runtime services are exposed through snapshot and bundle wiring details rather than a compact documented seam inventory.
  - Current guardrails verify major forbidden imports, but not yet the full intended rule of "adapters depend on stable core contracts, not runtime internals".
- **Implications**: The best design target is selective seam clarification and stronger verification, not package churn.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| A. Formalize current structure | Keep current package map and document roles more clearly | Lowest churn, preserves working semantics | Can leave some seams implicit | Good baseline, but may stop short of needed seam cleanup |
| B. Full textbook hexagonal rewrite | Create explicit `app`, `domain`, and `adapters` packages and migrate broadly | Clean narrative and visible package taxonomy | High churn, regression risk, interface ceremony | Poor fit for this repo now |
| C. Hybrid pragmatic hexagonal model | Keep package map, extract only high-value ports, expand guardrails | Best balance of clarity and stability | Requires disciplined scope control | **Selected approach** |

## Design Decisions

### Decision: Treat the current core package family as the application core
- **Context**: Requirements call for an application core, but the repository already expresses that role through `internal/core/*` and `pkg/lipapi`.
- **Alternatives Considered**:
  1. Introduce new `app` and `domain` packages immediately.
  2. Declare the existing core package family as the effective application core and formalize it.
- **Selected Approach**: Keep `internal/core/*` and `pkg/lipapi` as the effective application core.
- **Rationale**: This matches the real dependency shape and avoids package churn that does not reduce coupling.
- **Trade-offs**: The repo is less textbook on disk, but more truthful to the actual code.
- **Follow-up**: Add explicit ownership mapping and architecture tests that enforce the intended role.

### Decision: Extract only high-value ports
- **Context**: Some seams are already adequate; others are too runtime-shaped or too wiring-shaped.
- **Alternatives Considered**:
  1. Extract explicit ports for every major dependency class.
  2. Inventory seams and extract only where coupling or ownership is materially unclear.
- **Selected Approach**: Use a seam inventory and only extract or rename high-value ports.
- **Rationale**: Preserves the project's small-core discipline and avoids interface soup.
- **Trade-offs**: Some runtime-shaped seams may remain until a later phase justifies more work.
- **Follow-up**: Prioritize backend execution, continuity storage, auth/principal context, and selected extension services.

### Decision: Keep seams consumer-owned and local rather than introducing a repo-wide ports layer
- **Context**: A brownfield hexagonal refactor often drifts into generic `ports`, `interfaces`, or `services` packages that flatten ownership instead of clarifying it.
- **Alternatives Considered**:
  1. Create a central shared package for all new seam contracts.
  2. Place each seam near the consuming core capability or keep it as an already-adequate local contract.
- **Selected Approach**: Keep seams local to the consuming capability and avoid central architecture buckets.
- **Rationale**: This preserves bounded ownership, reduces accidental coupling, and avoids refactor churn that mostly renames things.
- **Trade-offs**: The resulting package map is less visually uniform, but it better matches how the code actually evolves.
- **Follow-up**: Review each proposed extraction against the owning capability before creating any new package.

### Decision: Do not force inbound interfaces or repository-shaped reads
- **Context**: In Go, driving adapters can often depend directly on concrete application services, and read-oriented flows may be clearer as query adapters than as aggregate repositories.
- **Alternatives Considered**:
  1. Require interfaces on both inbound and outbound sides for symmetry.
  2. Keep inbound seams concrete by default and allow dedicated query/read seams where they simplify the design.
- **Selected Approach**: Keep inbound seams pragmatic and allow query-specific read models.
- **Rationale**: This improves testability and maintainability without introducing interface ceremony or forcing diagnostics/reporting reads through write-oriented abstractions.
- **Trade-offs**: The architecture story is less uniform on paper, but the code remains easier to navigate and change.
- **Follow-up**: Encode this rule in requirements, design guidance, and tasks so later implementation work does not regress into symmetry-driven abstractions.

### Decision: Make the backend seam the first extraction target
- **Context**: The clearest concrete composition leak today is that `internal/pluginreg` and `internal/infra/runtimebundle` import `internal/core/runtime` mainly to name `runtime.Backend` and related helpers.
- **Alternatives Considered**:
  1. Start by splitting `RequestRuntimeSnapshot` and observer seams first.
  2. Start by extracting only the backend seam out of the main executor package and leave other seams alone until evidence justifies more work.
- **Selected Approach**: Extract the backend seam first.
- **Rationale**: It resolves a real inward dependency concern, has a small blast radius, and improves architecture clarity without changing product semantics.
- **Trade-offs**: Other seams remain somewhat broad for now, but they are not yet the highest-value problem.
- **Follow-up**: Pair the extraction with narrow architecture tests and executor non-regression coverage.

### Decision: Use architecture tests as the primary enforcement mechanism
- **Context**: The repo already relies on automated guardrails for important structural rules.
- **Alternatives Considered**:
  1. Rely mostly on design documentation and code review discipline.
  2. Expand `internal/archtest` and related checks to encode the target hexagonal rules.
- **Selected Approach**: Expand architecture tests and guardrails.
- **Rationale**: This project already values compiler- and test-enforced constraints over conventions alone.
- **Trade-offs**: Rules must stay focused on real ownership boundaries, not aesthetics.
- **Follow-up**: Add checks for seam ownership and dependency direction where the design introduces stronger expectations.

## Risks & Mitigations
- Textbook rewrite drift - Mitigate by explicitly forbidding package churn without a coupling benefit.
- Semantic regression in routing/streaming/continuity - Mitigate with non-regression tests around executor behavior.
- Interface overproduction - Mitigate with a port-justification rule for every new seam.
- Generic ports-layer sprawl - Mitigate by keeping seams near consuming capabilities and rejecting central interface buckets without a clear ownership need.
- Hidden exceptions - Mitigate with a migration classifier and explicit exception register.

## References
- `cmd/lipstd/main.go` - explicit composition root
- `internal/infra/runtimebundle/build.go` - runtime assembly and runtime snapshot publication
- `internal/stdhttp/server.go` - transport edge, diagnostics, and frontend mounting
- `internal/core/runtime/executor.go` - core-owned orchestration behavior
- `internal/pluginreg/standard_table.go` - registry-side coupling to runtime backend seam
- `internal/archtest/extension_platform_boundaries_test.go` - current mechanical boundary enforcement
- `AGENTS.md` - mission, architectural rules, and project guardrails
- `.kiro/steering/structure.md` - package ownership map
- `.kiro/steering/api-standards.md` - canonical-in-the-middle and streaming rules
- `.kiro/steering/routing-and-orchestration.md` - core-owned routing and B2BUA semantics
