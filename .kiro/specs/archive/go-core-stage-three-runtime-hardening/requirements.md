# Requirements — Go core reimplementation stage three: runtime hardening and instance identity

Spec name: `go-core-stage-three-runtime-hardening`

**Lifecycle:** implementation-complete; archived under `.kiro/specs/archive/go-core-stage-three-runtime-hardening/`
(see `spec.json`).

## Goal

Turn the current stage-two Go rewrite into a **production-correct, instance-addressable, explicitly owned standard distribution** without expanding the feature surface prematurely.

Stage three is intentionally **not** a “build the first server app” stage.

A standard server/binary already exists. The correct next step is to harden the architecture so the project does not drift back toward the maintainability trap that motivated the rewrite away from Python.

## Stage-three thesis (historical)

At spec authoring time, the implementation had the right ingredients, but several concerns were only partly
true. **Stage three closed that gap:** bundle composition is explicit (`pluginreg` + `runtimebundle`), runtime
identity is instance-aware, continuity retention semantics are store-explicit, production wiring injects real
clock/RNG/transport defaults, routing health and observation are wired on the standard path, and architecture
tests guard against drift. Optional follow-ons (richer error taxonomy, breaker admin UI, SQLite pruning) are
**out of scope** for this spec; track them in new specs if needed.

---

## In scope

- split plugin/adapter kind from configured instance identity
- make the standard bundle explicitly assembled instead of `init()`-registered
- make runtime resource ownership complete and explicit
- inject production-correct clock/RNG/transport defaults
- wire health-aware routing and route observers for real
- align continuity retention semantics across memory and durable modes
- harden the standard server distribution around tracing, shutdown, and operational correctness
- preserve small-core discipline and plugin separation

## Out of scope

- new API flavors
- advanced tool-reactor implementations
- auth / multi-user / tenancy features
- web UI / admin console
- out-of-process plugins
- native Go `plugin` loading
- broad protocol-fidelity expansion beyond what is needed for the hardening work

---

## Functional requirements

### R1 — configured plugin instances must have their own identity, separate from adapter kind

The system must distinguish:

- adapter/plugin kind (the bundled factory, e.g. `openai-responses`)
- configured runtime instance identity (e.g. `openai-primary`, `openai-failover`)

#### Acceptance criteria

- configuration supports multiple backend rows using the same adapter kind
- each configured backend instance has a unique runtime identity
- route selectors and diagnostics target runtime instance identity, not adapter kind
- plugin validation rejects duplicate instance identities within a plugin family
- plugin validation does **not** reject multiple instances of the same adapter kind

### R2 — the standard distribution bundle must be explicitly assembled

The standard bundle must be defined through an explicit compile-time bundle definition, not through package import side effects.

#### Acceptance criteria

- standard bundle factories are defined in an explicit bundle package or table
- no `init()`-driven registry installation is required for the standard distribution
- tests can build a minimal or custom bundle without importing the full standard distribution
- bundle composition remains static-linking based

### R3 — runtime ownership of resources must be complete

One runtime owner must assemble and own:

- continuity store
- backend clients/transports
- executor
- route observers / trace buffers
- lifecycle-aware feature plugins
- HTTP server dependencies

#### Acceptance criteria

- every opened resource has a defined owner and shutdown path
- durable continuity store handles are closed on shutdown
- shutdown order is deterministic and documented
- standard HTTP serving does not build hidden runtime resources outside the owner

### R4 — production composition must inject real clock and entropy by default

Deterministic runtime fallbacks are allowed for tests and harnesses, not for the standard production binary.

#### Acceptance criteria

- standard production composition injects a real wall clock
- standard production composition injects a non-deterministic RNG / entropy source
- deterministic clocks/RNG remain easy to inject in tests
- weighted routing in production wiring is not fixed-seed deterministic
- attempt timestamps and frontend response timestamps are wall-clock based in standard production wiring

### R5 — routing health and observability must become real runtime behavior

The standard distribution must wire routing-health and observation seams as real runtime behavior, not placeholder fields.

#### Acceptance criteria

- unhealthy candidates can be excluded by a real configured health source
- route decisions can be observed through a standard observer path
- route traces and route logs report runtime instance identity
- health behavior is testable without provider SDKs

### R6 — continuity retention semantics must be explicit and consistent

Continuity configuration must not imply capabilities that only one store actually honors.

#### Acceptance criteria

- retention semantics are either:
  - supported consistently for memory and SQLite, or
  - explicitly store-specific in config/schema/docs
- operator intent is not silently ignored
- durable continuity does not grow without a documented policy
- continuity store behavior is covered by tests for both store modes

### R7 — standard backend transport configuration must be explicit

Backend instances must not rely on package-global HTTP clients as their operational transport.

#### Acceptance criteria

- the standard bundle owns HTTP client / transport creation
- backends can be configured with instance-scoped transport settings as needed
- shared transport reuse is possible when appropriate
- provider adapter code does not have to construct its own global client defaults

### R8 — request correlation must be independent from diagnostics endpoints

Basic request correlation should always exist in the standard server distribution.

#### Acceptance criteria

- request IDs / trace IDs are present even when diagnostics endpoints are disabled
- diagnostics enablement only controls diagnostics/admin endpoints
- logs and route observation can correlate by request/trace identity

### R9 — frontend error behavior should be normalized through shared runtime error kinds

Frontends should not independently invent error-mapping policy for the same runtime failure classes.

#### Acceptance criteria

- runtime/frontend shared code exposes at least a shared classifier for common execute failure classes, starting with reject vs internal failures
- frontends map those shared error kinds consistently to their protocol-specific response shapes
- frontend-specific mapping logic is small and codec-focused
- if richer shared kinds such as unavailable / timeout / cancellation are added later, they extend the shared classifier rather than reintroducing per-frontend duplication

### R10 — small-core discipline must remain enforceable

Stage three must actively prevent architectural drift back toward Python-style hidden complexity.

#### Acceptance criteria

- core packages remain provider-agnostic
- core packages do not import bundled plugins
- complexity/file-size budgets are documented and enforced in CI or architecture tests
- no single new package becomes the new orchestration monolith

---

## Quality requirements

### Q1 — maintainability takes priority over breadth

Stage three is successful only if it reduces future change cost, not if it merely adds more code.

#### Acceptance criteria

- the stage reduces hidden dependencies and ownership ambiguity
- the stage enables future routing/server work with fewer core edits

### Q2 — migration from stage-two config must be deliberate

The move to instance identity must not create a silent config break.

#### Acceptance criteria

- a documented migration path exists from old `id`-only config rows
- migration behavior is deterministic and tested
- ambiguous old configs fail clearly

### Q3 — TDD remains mandatory

Every architectural change in stage three must land behind tests first.

#### Acceptance criteria

- new behavior is introduced through failing tests
- regression tests encode the code-review findings from stage two
- no architectural seam is added without tests that prove why it exists
