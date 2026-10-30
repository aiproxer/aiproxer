# Testing and TDD (Steering)

## Core Testing Invariants

- **TDD by default**: characterize or make the desired invariant fail first, implement the smallest correct change, then refactor.
- **Test behavior at ownership boundaries**: prefer canonical/domain contracts, adapter contracts, and lifecycle invariants over tests that mirror internal call graphs.
- **Every bug fix gets a focused regression**: reproduce the defect with the smallest stable fixture/test that would have caught it.

- **Default tests stay self-contained**: ordinary unit/composed tests use in-memory stores, `httptest`, stubs, fake clocks/IDs, and no external network/service requirement.
- **Concurrency owners prove cleanup**: packages that own goroutines, streams, processes, or async workers need cancellation/cleanup tests and leak evidence appropriate to the change; race coverage comes from scheduled remote scans, never local runs.
- **Real canonical types over mock graphs**: do not mock internal implementation chains merely to satisfy coverage.

For protocol terminal changes, reviewers require an observed terminal before
comparing its value; an expected-value default is not evidence of emission.
Demonstrate that removing the terminal or its discriminator breaks the focused
test. Use the pinned consumer SDK when compatibility is the claim, and account
for preserved content and usage as well as the terminal metadata.

## Test Architecture

Use layered evidence instead of a Cartesian product:

1. **Unit/domain tests** prove local policy and pure transformations.
2. **Adapter/family contracts** prove each frontend/backend/connector family against canonical semantics.
3. **Core contracts** prove orchestration, routing, commitment, continuity, and lifecycle independently of specific providers.
4. **Architecture/QA rules** prove dependency direction, generated-contract currency, and repository hygiene with generic, reusable checks (see Test Proportionality).
5. **Bounded real-stack sentinels** prove a small number of representative end-to-end paths.
6. **Environment/topology tests** prove database, process, or external-service behavior only when that topology matters.

Do not grow frontend×backend matrices simply because another provider/profile was added. A new implementation that passes the relevant family contract should not multiply unrelated test cells.

## Cognitive Complexity Ratchet

- The precommit-tagged check in `internal/qa` fails on complexity growth: a recorded function whose complexity increased, or a new function above the cognitive complexity threshold. Improvements and removals are reported but never fail.
- The threshold and the excluded paths are read from `.golangci.yml`, so the advisory report (`make lint-advisory`) and the ratchet always measure the same code. Change the policy in that file, never in the check.
- Growth fails and needs a reviewed baseline edit or a refactor below the threshold. An unrecorded improvement and a deleted function are reported, never fail, and the baseline is refreshed opportunistically.
- Re-record the baseline with `go test -tags=precommit ./internal/qa -run TestCognitiveComplexityRatchet -update-cognitive-baseline`.
- The ratchet is a growth guard, not a simplification target. A recorded function stays recorded until it is refactored below the threshold; the baseline is not a backlog to be cleared.

## Test Proportionality

Tests serve the slice (see `delivery.md`). They prove the behaviour the change ships, at the cheapest layer that can observe it.

- Each implementation task carries its own tests. Testing is never a requirement, a certification matrix, or a standalone task.
- A feature PR stays within about 1.5x test lines per production line (alpha budget). When a proof needs more, the slice or the design is too large.
- Architecture rules are generic and declarative: new code joins an existing rule (add the package, stage, or import to its list). A new `internal/archtest` or `internal/qa` check is warranted only for a rule that applies across features, and it lands in its own PR. Frozen until 2026-11-21: no new gates/ratchets/censuses/hooks/dev tooling, except a gate that is broken or blocks delivery (see `delivery.md` Alpha budget).
- Feature behaviour is proven by feature tests: table tests for decisions, one integration test per seam the feature touches, and a fail-open test when the feature can fail.
- Documentation, metric names, and Markdown content are reviewed by humans; tests assert code behaviour.
- Performance is proven by a benchmark when a requirement states a target; allocation, goroutine, or call-count ratchets belong to shared hot paths owned by core, not to individual features.
- Test helpers stay simple enough to be obviously correct; when a test needs its own tests, replace it with a simpler one.

## Test-Cost and Iteration-Speed Policy

Fast feedback is an architectural constraint.

- Keep the default suite narrow enough for normal development loops.
- Prefer focused package/contract execution over recursively spawning repository-wide test commands from tests.
- Reuse compiled/cached test helpers rather than rebuilding the same executable for each check.
- Prefer fake clocks and deterministic IDs over sleeps/polling.
- Prefer in-memory persistence unless reopen/durability/engine behavior is the subject of the test.
- When a test genuinely needs a file-backed SQLite database (reopen/durability/engine behavior), build the DSN with WAL journaling and `synchronous=NORMAL` (`?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)`, preserving any `_txlock`). WAL+`NORMAL` keeps close/reopen and process-crash durability for committed transactions while avoiding a full fsync per commit, which otherwise makes file-backed stores dominate the suite. Do not use it for tests that assert rollback-journal or power-loss durability semantics.
- Do not add expensive tagged/external work to the default unit path.
- Before intentionally increasing test/QA infrastructure cost, run the Windows-authoritative `make test-cost` ratchet and provide evidence. Budget changes require explicit maintainer authorization; they are not a normal escape hatch for regressions.

## Build Tag & Environment Gating Rules

- **Untagged/default tests** must be hermetic and must not require external databases, credentials, or network services.
- **Integration-tagged tests** may require real services/topologies and must state their prerequisites clearly.
- **Precommit/tagged matrices** are for broader certification that is too expensive or environment-sensitive for the inner loop.
- A mandatory gate that explicitly requests an external topology must **fail closed** when the prerequisite is unavailable; an optional ad-hoc integration run may skip when it is not configured.
- Do not infer CI behavior from steering. The `Makefile` and workflow files are authoritative for the current target/job graph.

### Database parity

`internal/testkit/dbparity.DefaultCatalog()` is the authoritative persistence inventory. Steering does not copy the number or names of registered components.

- `make test-db-parity` is the canonical repository-wide dual-engine certification.
- `make test-db-parity-sqlite` proves the SQLite side of the registered contracts.
- `make test-db-parity-postgres-direct` proves the direct PostgreSQL side and fails closed when invoked as a mandatory gate without a usable service.
- Specialized distributed/pooler/migration tests supplement the catalog when topology semantics differ; use the `Makefile` and persistence/release docs for their current names and prerequisites.

## Change-Surface Verification Procedure

Select evidence from the semantics changed, not from habit:

| Change surface | Minimum evidence direction |
| --- | --- |
| Pure domain/canonical logic | focused unit/regression tests |
| Parser/decoder/codec | focused tests + fuzzing where practical |
| Frontend/backend protocol behavior | family contract/conformance tests + focused wire tests |
| Routing/B2BUA/commitment | core runtime/routing tests; prove no post-commit recovery |
| Cancellation/goroutine/stream lifecycle | focused non-race tests locally; race coverage comes from scheduled remote scans and is not a merge or acceptance gate |
| Feature/extension-plane behavior | feature tests + SDK/architecture guards; generator check when plane metadata changes |
| Host/generation publication/reload | candidate rollback, publication isolation, retirement/cleanup tests |
| Persistence/schema/migrations | focused store tests + applicable `dbparity` gate/topology proof |
| Connector module | module-local tests + backend-plugin contract/release checks |
| Billing/accounting | domain invariants + persistence/convergence evidence; no stream-path shortcuts |
| Test/QA infrastructure | affected tests + `make test-cost` when cost could change |
| Wide/release-grade change | `make qa` plus domain-specific gates |

Run the smallest complete evidence set that proves the changed invariant. Add wider gates when the blast radius is genuinely wider.

## High-Value Semantic Targets

Prioritize tests for invariants whose failure would invalidate product behavior:

- canonical translation and explicit capability handling;
- protocol-legal streaming/error framing;
- routing planning and pre-output-only recovery;
- at-most-once attempt terminalization and cancellation ownership;
- immutable generation publication/rollback/retirement;
- secure-session and authorization boundaries;
- durable continuity and attempt ordering;
- feature/core ownership seams and no concrete feature leakage into generic runtime;
- secret-safe diagnostics/observability;
- billing exposure/usage/settlement separation;
- database logical parity across supported engines/topologies.

Concrete feature/provider inventories should not be copied into this list.

## Mocking and Fixture Rules

- Prefer small fakes/stubs and `httptest.Server` over general mock frameworks.
- Mock external boundaries, not internal call graphs.
- Use real `pkg/aipapi` canonical types in protocol/core tests.
- Keep fixtures bounded and human-auditable; golden files are appropriate when exact wire/canonical shape is the contract.
- Avoid sleeps as synchronization. Use channels, fake clocks, explicit barriers, or state observation.
- Tests must not depend on execution order or mutable process-global leftovers.

## Non-Vacuity for a Relaxed Assertion

A change that widens what an assertion accepts — two equivalent error causes, a looser predicate, a dropped expectation — must prove the assertion still rejects what it exists to reject. Widening is safe only when the newly accepted values are genuinely the same outcome AND the rejection path is still live. Fault-inject each rejection case; do not reason about it.

Two properties of Go's `testing` break the obvious probe, and both fail silently:

- `t.Fatalf` and `t.FailNow` end the calling goroutine via `runtime.Goexit`, not a panic. A probe wrapped in `recover()` observes nothing and passes vacuously.
- A failing subtest fails its parent regardless of what `t.Run` returns, so "this case should fail" cannot be asserted inside the same test binary.

Run each rejection case as its own process and assert on its output:

```sh
go test -count=1 -run '^TestProbeRejectsNil$' ./path/to/pkg 2>&1 | grep -q 'got success'
```

Keep probes out of the committed suite, or behind a build tag, so a probe that is meant to fail cannot redden a normal test run. When a relaxation is the fix, the PR body should show the injected cases and the message each produced.

## Failure Triage

When a broad gate fails during a scoped change:

1. reproduce the failure on the change branch;
2. determine whether the touched ownership surface can causally affect it;
3. reproduce on the relevant baseline/main SHA when attribution is uncertain;
4. fix branch-owned regressions before claiming completion;
5. record failures that reproduce on the baseline in the PR body and continue; their fixes go in a separate PR from `main` (see `delivery.md`).

Never claim success from a partial command when the requested completion gate is broader.

## What Steering Holds Here

Steering holds durable test policy: proportionality, layers of evidence, proportionality of cost, failure triage.
Volatile inventory — current target composition, CI job names, workflow predicates, container versions, connector
lists — lives in the `Makefile` and workflow files, which stay authoritative and are read on demand. Command-to-intent
mapping is listed once in the root `AGENTS.md`.
