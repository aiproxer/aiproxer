# Research & Design Decisions

## Summary

- **Feature**: `atomic-owned-resource-lifecycle`
- **Discovery Scope**: Brownfield architectural hardening / focused refactor
- **Key Findings**:
  - Cordis v4's useful idea for Go-LIP is resource/effect ownership locality: a successful acquisition should carry and immediately register its inverse.
  - Go-LIP already implements the more difficult runtime guarantees through immutable generations, leases, `ResourceLedger`, and manager-owned retirement; replacing those is unnecessary.
  - The highest-ROI brownfield targets are process builder closer propagation and the manually coupled cancel+join lifecycle of long-lived generation-owned loops.
  - Backend cleanup transfer is already sufficiently close to the desired pattern and should remain unchanged.

## Research Log

### Cordis v4: what is transferable

- **Context**: The source paper proposes temporal composability through revertible effects and spatial composability through reactive coeffects/dependency activation.
- **Sources Consulted**:
  - User-supplied paper: *A Programming Paradigm for Spatiotemporal Composability*, Yifan Shi, Wei Zhang, Tianyi Cui.
  - Cordis package/repository (`cordis` 4.x release candidate / `cordiverse/cordis`) for current implementation context.
- **Findings**:
  - Revertible effects pair mutation/acquisition with an inverse and unwind inverses in reverse order.
  - Cordis does not prove an inverse is semantically correct; the component author still owns that obligation.
  - Reactive dependency/coeffect machinery is valuable for independently changing component graphs, but it adds fibers/provider identity/reconciliation complexity.
- **Implications**:
  - Borrow only the acquire-with-inverse discipline.
  - Do not import the reactive dependency graph, generic context, HMR, or fine-grained component runtime into Go-LIP.

### Existing Go-LIP generation ownership

- **Context**: Determine whether Cordis solves a missing lifecycle problem.
- **Sources Consulted**:
  - `docs/architecture.md`
  - `docs/runtime-config-reload.md`
  - `internal/infra/runtimehost/generation.go`
  - `internal/infra/runtimehost/generation_refcount.go`
  - `internal/infra/runtimehost/retire.go`
  - `internal/infra/runtimebundle/generation_bundle.go`
  - `internal/infra/runtimebundle/resource_ledger.go`
- **Findings**:
  - New admissions pin one immutable active generation; old generations retire and drain without mutating in-flight work.
  - `ResourceLedger` already records generation-owned cleanup and unwinds in reverse order across rollback/quiesce/close phases.
  - Reload is already last-good transactional publication rather than in-place mutation.
- **Implications**:
  - A new lifecycle/effect engine would be duplication.
  - `ResourceLedger` must remain the generation-resource authority.

### ProcessServices ownership locality

- **Context**: Find remaining manual resource lifetime seams.
- **Sources Consulted**:
  - `internal/infra/runtimebundle/process_services.go`
  - `internal/infra/runtimebundle/process_services_types.go`
  - process builder helpers used by `NewProcessServices`
- **Findings**:
  - `ProcessServices` has correct reverse-order teardown and idempotent shutdown.
  - Constructor-local `register`, `regStep`, and builder-returned closer slices mean ownership sometimes crosses a builder boundary before the caller registers it.
  - The pattern is safe today but relies on maintenance discipline when new fallible steps are added.
- **Implications**:
  - Introduce one private append-only ownership facade that writes directly into the existing `ProcessServices.closers` set; do not create a second release stack or success-time handoff.
  - Selected builders should register non-nil releases themselves and return only their runtime value/error; value-only construction bypasses the owned helper.

### Generation-owned background loops

- **Context**: Evaluate goroutine lifecycle as a resource acquisition problem.
- **Sources Consulted**:
  - `internal/infra/runtimebundle/build_model.go`
  - repository `golang-concurrency` and `golang-context` skills
- **Findings**:
  - The model-registry refresh loop uses a derived context, cancel function, wait group, and `PhaseQuiesce` cleanup.
  - Repository guidance requires every goroutine to have a clear owner, stop signal, and join path.
  - A general async framework is unnecessary; the useful abstraction is only the exact cancel+join lifetime pattern.
- **Implications**:
  - Add one private helper that blocks application work until cleanup ownership has been registered.
  - Keep the helper generation-composition-specific and ledger-backed.

### Backend lifecycle

- **Context**: Check whether backend construction needs the same refactor.
- **Sources Consulted**:
  - `internal/pluginreg/lifecycle.go`
  - `internal/infra/runtimebundle/build_model.go`
  - `pkg/lipsdk/backendplugin/interfaces.go`
- **Findings**:
  - `BackendBuildResult` already returns a backend plus cleanup.
  - `buildBackends` transfers cleanup and lifecycle ownership to `ResourceLedger` before the backend enters the executor map.
  - External connectors also have independent process supervision and ABI contracts.
- **Implications**:
  - Do not change backend factory or SDK/ABI contracts in this spec.
  - Characterize existing transfer behavior and leave it alone.

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
|---|---|---|---|---|
| Full Cordis runtime | Generic context, reactive dependencies, component fibers/effects | maximal composability | duplicates current runtime; high complexity; weak ROI | Reject |
| Third-party DI/lifecycle framework | Introduce fx/dig/do-style lifecycle/container | ready-made lifecycle features | conflicts with explicit construction/no-container steering; new dependency | Reject |
| No change | Keep closer propagation/manual worker lifetime | no churn | caller-mediated ownership remains easy to regress | Reject |
| Private ownership hardening | Append-only facade over the existing process closer set + owned-only acquire seam + narrow ledger-backed loop helper | stronger invariants, local change, deletes plumbing | moderate migration/test churn | **Select** |

## Design Decisions

### Decision: Process ownership is an append-only facade, not a second stack or container

- **Context**: `ProcessServices` already has the authoritative closer slice, reverse rollback, and idempotent `Close`; only cleanup registration locality needs improvement.
- **Alternatives Considered**:
  1. Generic service context with keyed lookup.
  2. DI container with lifecycle.
  3. Separate private release stack plus success-time handoff.
  4. Private append-only facade over the existing `ProcessServices` closer set.
- **Selected Approach**: bind a private construction-only owner to the existing closer append operation. `Own` writes directly to `ps.closers`; rollback and normal shutdown continue to consume that same set.
- **Rationale**: directly solves forgotten/delayed cleanup registration without introducing transfer state, a second release collection, or a competing shutdown path.
- **Trade-offs**: does not automate dependency ordering; ordering remains explicit construction order, which is desirable here.
- **Follow-up**: architecture-test the absence of lookup/service-locator methods and regression-test exactly-once use of the shared close set.

### Decision: Move closer ownership inward, not the whole builder graph outward

- **Context**: Several process builders currently return closer slices.
- **Selected Approach**: pass the private process owner only to the selected composition builders and register releases at acquisition time; return runtime value/error.
- **Rationale**: makes the inverse local to acquisition and deletes caller plumbing.
- **Trade-offs**: touches several builder signatures/tests, but remains within one composition package.
- **Follow-up**: preserve exact close order with characterization tests.

### Decision: Preserve special explicit teardown

- **Context**: plugin staging/artifact teardown and database pool claim/prune have special ordering semantics.
- **Selected Approach**: leave explicit paths explicit where abstraction would hide why ordering exists.
- **Rationale**: maintainability is clarity, not uniformity.
- **Follow-up**: only migrate a special case if the new code is demonstrably simpler.

### Decision: Structured loop helper uses ResourceLedger, not a new worker runtime

- **Context**: generation-owned refresh loops are resources with cancel+join inverses.
- **Selected Approach**: one private helper starts a cancellation-aware gated goroutine, registers a close-only cancel/join action with the caller-selected ledger phase, then opens the gate.
- **Rationale**: prevents an unowned live goroutine without changing generation lifecycle and remains safe when `ResourceLedger.AddClose` performs synchronous immediate cleanup.
- **Trade-offs**: applies only to exact cancel+join loops; other async patterns remain explicit.
- **Follow-up**: race and goleak tests for quiesce/rollback/late-close paths, plus model-registry `PhaseQuiesce` refresh-before-`PhaseClose` catalog ordering.

### Decision: Backend lifecycle remains unchanged

- **Context**: backend creation already returns value + cleanup and transfers it immediately.
- **Selected Approach**: no pluginreg/SDK changes.
- **Rationale**: refactoring an already-good seam would add churn without stronger guarantees.
- **Trade-offs**: process and backend acquisition syntax will not be artificially uniform.
- **Follow-up**: retain characterization coverage.

## Risks & Mitigations

- **Abstraction creep** — keep new types package-private; architecture tests reject service lookup/provision APIs.
- **Close-order regression** — baseline and migration tests assert exact reverse ordering for partial construction and shutdown.
- **Double cleanup** — write owned releases directly into the existing `ProcessServices` close set and delete caller registration; no separate owner stack or success-time handoff exists.
- **Worker leak/deadlock** — the start gate observes cancellation, cleanup uses close-only ledger registration, and race/goleak tests cover synchronous immediate cleanup plus already-closing ownership.
- **Hidden behavior change from error wrapping** — preserve existing aggregate/error semantics; do not add user-visible error categories.
- **Refactor grows instead of shrinks** — final simplification gate reverts migrations that add more lifecycle concepts than they delete.

## References

- User-supplied paper: *A Programming Paradigm for Spatiotemporal Composability* — Cordis v4 effect/coeffect model and implementation rationale.
- `https://github.com/cordiverse/cordis` — Cordis implementation repository.
- `https://www.npmjs.com/package/cordis` — current Cordis package/release context.
- `docs/architecture.md` — current converged process/generation ownership model.
- `docs/runtime-config-reload.md` — immutable generation reload and retirement contract.
- `internal/infra/runtimebundle/resource_ledger.go` — generation resource lifecycle authority.
- `internal/infra/runtimebundle/process_services.go` — process acquisition/cleanup composition.
- `.kiro/skills/golang-design-patterns/SKILL.md` — smallest-pattern / explicit resource management guidance.
- `.kiro/skills/golang-concurrency/SKILL.md` — explicit goroutine ownership/exit/join guidance.
- `.kiro/skills/golang-context/SKILL.md` — cancellation ownership guidance.
