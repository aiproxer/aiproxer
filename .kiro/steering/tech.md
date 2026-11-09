# Technology Stack (Steering)

## Technology Policy

Steering records technology **choices and constraints**, not dependency/version snapshots.

- The Go language/toolchain version is whatever is pinned by `go.mod`; do not copy the number here.
- Exact dependency versions live in module files.
- Prefer the Go standard library unless an external dependency materially reduces complexity, risk, or protocol burden.
- Technology-specific code belongs at adapter/composition edges; core and public contracts remain technology-light.

## Core Technology Choices

- **HTTP and context**: standard `net/http` and `context` semantics are the baseline.
- **Structured logging**: use `log/slog`-compatible structured logging; never emit secrets/raw sensitive prompts by default.
- **Serialization**: standard JSON semantics are authoritative unless a wire adapter requires a provider codec. Preserve explicit presence/null distinctions where protocol behavior depends on them.
- **Configuration**: typed core configuration plus raw/typed plugin-owned subtrees; optional feature configuration is decoded by the owning feature.
- **Persistence**: domain contracts are separated from Bun/SQL adapters; supported relational engines must preserve equivalent logical behavior.
- **Observability**: metrics/traces/logs must have bounded cardinality and secret-safe attributes.
- **Testing**: stdlib testing/httptest first; use targeted helpers such as goroutine-leak checks where lifecycle ownership warrants them.

## Runtime Composition Invariants

- **Explicit composition**: constructors and composition roots wire dependencies. Do not add DI containers, reflection-driven service registries, or request-time service locators.
- **No dynamic `init()` registration/state**: package initialization must not perform service registration, runtime discovery, mutable process setup, or hidden dependency construction.
- **Generated immutable metadata exception**: deterministic generated package initialization may capture/freeze canonical immutable metadata when it is mechanically generated, architecture-tested, and does not perform dynamic discovery/registration.
- **Single Host ownership**: Host construction owns process resources, immutable generation management, reload coordination, and shutdown. `Host.Close` is the process shutdown coordinator.
- **Immutable generations**: configuration reload compiles a candidate and publishes a new immutable request plane for new admissions.
- **Publication isolation**: candidate compilation/validation must not mutate active process-visible behavior. Publication-only side effects run through the publication lifecycle after the generation becomes active.
- **Hybrid backend model** ([ADR 0008](docs/adr/0008-hybrid-backend-connector-plugins.md)): essential adapters may be linked in-process; optional backends with independent dependency/runtime needs use executable connectors under `connectors/` over the versioned SDK ABI. Native Go `plugin` loading is forbidden.

## Dependency and SDK Isolation

- Provider SDKs stay inside provider backend adapters/connectors.
- Public `pkg/aipapi`, `pkg/aipsdk`, and `internal/core` do not import provider SDKs.
- The root module must not absorb optional connector module dependencies.
- Environment credential resolution for in-process essentials belongs to composition; connectors own their connector-local credential/config behavior.
- Provider/profile inventory belongs to registries/manifests, not hard-coded steering prose.

## Concurrency & Context Standards

- `context.Context` is the first parameter for external/I/O operations where cancellation/deadline propagation matters.
- Do not store request contexts in long-lived structs.
- Do not replace a request context with `context.Background()` in the middle of request work.
- Every goroutine/channel has an explicit owner and cancellation/close path.
- Avoid ad-hoc per-request goroutines when the same work can remain synchronous or use an existing owned scheduler/pool.
- Publish shared state only after it is fully initialized.
- Never retry/failover after client-visible output commits the attempt.
- Concurrency-sensitive lifecycle changes require race-focused tests and clean terminal/cleanup ownership.

## Optional attachment and defaults

- Money handling reaches the runtime through attachment ports; the runtime surface carries no money implementation of its own.
- Operational exposure, immutable usage evidence, customer rating/settlement, and provider cost accounting are distinct concepts.
- An attached implementation may authorize exposure before upstream execution; stream receive code must not perform rating, journal posting, or balance mutation.
- Public `pkg/aipruntime.Options` remains non-money: it carries the canonical registrations and the optional attachment seams, never a monetary composition.
- A host that needs monetary behaviour attaches it explicitly through the public composition entry point; stock/public composition must not infer or auto-open monetary state from YAML.
- An unattached port keeps its no-op default: the usage-limit port admits every request.
- The money implementation and the usage-authority implementation live in the Enterprise tree (`enterprise/`); this tree keeps only the public contracts, the port payloads, the binding adapter, the neutral usage-limit port, and the provider quota window. The stock command composes no billing: a pricing configuration fails validation and names the Enterprise capability. Importing a plugin does not remove the rules or the monetary algorithms that an attached implementation brings with it.
- Missing authoritative pricing/catalog state fails closed at the stage that requires it rather than inventing defaults.
- Attempt order used by financial logic must come from authoritative persisted attempt sequencing, not incidental slice/map ordering.

## Database & PgBouncer Standards

The authoritative inventory of production persistence families is `internal/testkit/dbparity.DefaultCatalog()`. Steering must not duplicate its component count or package list.

For every component registered there:

- maintain logical contract and migration/schema parity across supported database engines;
- keep domain interfaces independent of SQL-driver details;
- use the canonical repository parity commands rather than ad-hoc per-package substitutes when certifying cross-engine behavior;
- fail closed when a mandatory external database topology is requested but unavailable;
- keep credentials/DSNs secret-safe in errors and logs.

For transaction-pooled PostgreSQL paths:

- do not depend on session-pinned state such as `SET search_path`, temporary tables, SQL prepared-session state, or session/advisory locks unless the documented topology explicitly guarantees them;
- migrations/admin work uses an appropriate direct/admin connection rather than assuming a transaction pooler can provide session semantics;
- runtime pools are bounded and owned at composition/lifecycle boundaries, not opened opportunistically per request.

Specialized distributed/pooler/migration gates may exist in addition to the repository-wide parity gate. Their exact target names and topology details are authoritative in the `Makefile` and persistence/release documentation.

## Canonical Verification Commands

The `Makefile` is authoritative for exact target composition. Use these stable intents:

- `make quality-checks` — repository formatting/static/architecture hygiene.
- `make test-unit` — default in-memory/composed unit suite.
- `make test` — normal comprehensive local verification.
- `make parity-checks` — protocol/contract parity and bounded cross-surface certification.
- `make test-db-parity` — canonical dual-dialect persistence certification derived from `dbparity.DefaultCatalog()`.
- `make test-db-parity-sqlite` — SQLite side of the canonical persistence contracts.
- `make test-db-parity-postgres-direct` — fail-closed direct PostgreSQL side of the canonical persistence contracts.
- `make qa` — wide/release-grade static and tagged verification.
- `make test-cost` — explicit Windows-authoritative test/QA cost ratchet when changing test infrastructure/performance policy.

Do not copy the internal prerequisite graph, CI job names, container versions, or workflow condition expressions into steering; inspect the `Makefile`/workflows when those implementation details matter.
