# Brownfield Requirements Gap Analysis

## Result

**PASS after scope corrections.** Go-LIP already implements the expensive parts of Cordis-style temporal composability at the correct proxy granularity: immutable generations, request pinning, transactional candidate publication, reverse-order generation rollback, quiesce/drain/close, and bounded retirement. A broad Cordis port would duplicate those mechanisms. The remaining high-ROI gap is narrower: some process builders still propagate closer lists to `NewProcessServices`, and at least one generation-owned refresh loop expresses `start goroutine -> later register cancel+join` manually.

The requirements were corrected to target only those gaps.

## Requirement-to-Asset Map

| Requirement area | Current asset | Gap | Classification |
|---|---|---|---|
| Existing runtime model | `runtimehost.Manager` / `Generation`, `runtimebundle.GenerationBundle`, `ResourceLedger` | none; preserve | Constraint |
| Process cleanup order | `ProcessServices.closers`, `disposeClosers`, constructor `register`/`regStep` | cleanup is correct but registration ownership is partly caller-mediated | Improvement |
| Process builder lifetime transfer | usage/concurrency authority, persistence, accounting, metering, terminal-work builders | these builders return closer slices across composition boundaries | Missing locality |
| Generation cleanup | `ResourceLedger` | already central, phased, reverse-order, retry-aware | Reuse |
| Backend lifecycle | `BackendBuildResult` + immediate `buildBackends` transfer | already close to value+inverse pattern | Reuse / no change |
| Generation-owned loops | model-registry refresh cancel + wait group + `PhaseQuiesce` cleanup | lifecycle coupling is manual | Improvement |
| Request/runtime semantics | executor, routing, stream, billing, sessions | unrelated to ownership hardening | Constraint |
| Public extension/API surfaces | `pkg/lipapi`, `pkg/lipsdk`, `pkg/lipruntime` | no change required | Constraint |

## Gaps and Requirements Corrections

### 1. A new effect runtime would duplicate `ResourceLedger`

The generation ledger already owns prepare/activate/publish/quiesce/rollback/close semantics, including reverse cleanup and retry behavior.

**Correction applied:** Requirements 1 and 5 forbid a second effect/cleanup runtime and make the existing `ResourceLedger` authoritative.

### 2. The actual process-side gap is ownership locality, not missing cleanup

`NewProcessServices` already disposes registered closers in reverse order on failure and shutdown. The maintainability cost is that several builders return `[]func() error`, after which the caller must remember to register them before later fallible work.

**Correction applied:** Requirements 2 and 3 require acquire-plus-release ownership before the resource escapes and remove closer propagation only from the selected high-value builders.

### 3. Some explicit teardown ordering should remain explicit

Plugin host/artifact/staging teardown has Windows-sensitive ordering, and database pool claim/prune has special construction semantics. Hiding these behind a generic helper would reduce clarity.

**Correction applied:** 3.4 permits explicit special cases. Uniformity is not a goal.

### 4. Backend construction already follows the useful Cordis pattern

`BackendBuildResult` carries `Backend` plus optional `Cleanup`, and `buildBackends` transfers that cleanup into the generation ledger immediately before the instance is exposed to the executor graph.

**Correction applied:** 5.2-5.3 explicitly preserve this contract instead of refactoring it for aesthetic consistency.

### 5. Worker ownership needs structure, but not a worker framework

The model-registry refresh path has the meaningful risk shape: long-lived goroutine + derived context + cancel + join + generation cleanup. A generic scheduler would be disproportionate.

**Correction applied:** Requirement 4 is restricted to existing composition-owned cancel-and-join loops and explicitly rejects worker pools/schedulers/error buses.

### 6. Process and generation cleanup have intentionally different semantics

Generation cleanup has phase state and retry behavior. Process shutdown is host-owned and idempotent through `ProcessServices`/host teardown.

**Correction applied:** 2.7 and 5.1 prohibit merging the two shutdown state machines. The new process ownership primitive is only a construction-time append facade over the existing `ProcessServices` closer set; it owns no second release stack and needs no success-time handoff.

### 7. The ROI must be observable in code shape

A small helper that merely wraps existing `register(...)` calls without deleting closer propagation would add abstraction without increasing safety.

**Correction applied:** 1.6, 3.5, and 6.6-6.7 require migrated paths to delete superseded plumbing or be left unchanged.

## Implementation Options

### Option A — Full Cordis-style context/effect runtime

Introduce generic context-managed resources, reactive dependencies, component fibers, and per-component teardown.

- **Pros:** maximum theoretical generality.
- **Cons:** duplicates generations/ledger, weak Go fit, broad state-machine interaction, no demonstrated product need.
- **Effort:** XL (2+ weeks).
- **Risk:** High.
- **Recommendation:** Reject.

### Option B — No change

Keep current manual registration/closer-return patterns.

- **Pros:** zero migration risk.
- **Cons:** preserves caller-mediated cleanup transfer and repeated cancel/join patterns.
- **Effort:** none.
- **Risk:** Low now; maintenance risk accumulates.
- **Recommendation:** Reject because a small safer alternative exists.

### Option C — Private scoped ownership hardening

Add a tiny append-only facade over the existing `ProcessServices` closer set plus an owned-only acquisition seam, migrate only the closer-propagating builders, add one narrow cancellation-safe structured loop helper over `ResourceLedger`, and add architecture gates.

- **Pros:** stronger invariant, local change, deletes plumbing, no runtime-model change.
- **Cons:** moderate constructor/test churn; requires disciplined scope.
- **Effort:** M (3-7 days for implementation plus review/race validation).
- **Risk:** Medium-Low.
- **Recommendation:** **Preferred.**

## Design-Phase Recommendations

1. Keep all new production types package-private under runtime composition.
2. Make the process owner an append-only facade over `ProcessServices.closers`, not a second closer stack or dependency container.
3. Preserve constructor rollback, `ProcessServices.Close`, and `ResourceLedger` as the actual shutdown owners.
4. Ensure the structured-loop helper uses a cancellation-aware start gate and preserves the model-registry `PhaseQuiesce` refresh-before-`PhaseClose` catalog split.
5. Do not alter backend lifecycle contracts.
6. Treat net simplification as a quality gate; revert individual migrations that become more complex.
