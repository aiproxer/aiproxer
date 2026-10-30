# Brownfield Design Validation

## Verdict

**GO after scope and lifecycle-contract hardening.** The design fits the current Go-LIP runtime and turns a Cordis-inspired idea into a small composition-root safety refactor rather than a new runtime paradigm. Initial validation removed abstraction duplication, worker-helper overreach, and cleanup-semantic merging; subsequent review tightened the authoritative process closer-set contract, owned-vs-value-only acquisition, synchronous immediate cleanup, and model-registry phase ordering. This GO is a design-validation verdict, not approval to begin implementation.

## Critical Issues Found and Applied Corrections

### 1. Parallel lifecycle abstraction risk — RESOLVED

**Concern:** A generic `Owner`/effect framework could overlap `ResourceLedger` and `ProcessServices`, recreating the runtime duplication the project recently removed.
**Impact:** Two cleanup authorities would make shutdown/reload reasoning harder and undermine the one-process/one-generation ownership model.
**Correction:** The final design makes the process owner a private construction-time append facade over the existing `ProcessServices` closer set; generation resources continue to use `ResourceLedger` unchanged.
**Traceability:** 1.1-1.6, 2.5-2.7, 5.1-5.4.
**Evidence:** Design sections “Architecture Pattern & Boundary Map” and “Process Resource Owner”.

### 2. Generic worker framework risk — RESOLVED

**Concern:** Turning goroutine ownership into a general worker/scheduler API would add lifecycle concepts unrelated to the demonstrated gap.
**Impact:** Higher cognitive load, unclear error/supervision semantics, and pressure to migrate unrelated goroutines.
**Correction:** The helper is limited to existing generation-owned loops whose entire shutdown contract is cancel + join; model-registry refresh is the mandatory initial consumer.
**Traceability:** 4.1-4.7, 6.2-6.3, 6.7.
**Evidence:** Design section “Generation Loop Helper”.

### 3. Process/generation cleanup semantic collapse — RESOLVED

**Concern:** Sharing one generalized owner between process and generation resources could erase intentional differences: generation phases/retries versus host-owned process idempotency.
**Impact:** Close-order regressions or a second shutdown state machine.
**Correction:** Separate lifetime-specific mechanisms remain: the existing `ProcessServices` closer set for process ownership, accessed through a private construction-only append facade; existing `ResourceLedger` for generation phases. No cross-lifetime registration API is introduced.
**Traceability:** 2.7, 3.6, 5.1-5.4.
**Evidence:** Design sections “State and Ownership Model” and “Error Handling”.

## Validation Checklist

### Existing architecture alignment — PASS

The design stays under `internal/infra/runtimebundle`, preserves `ProcessServices`, immutable generation runtime, private host, reload contract, and manager-owned retirement. No domain/core package needs a new dependency.

### Core vs plugin ownership — PASS

This is composition-root infrastructure. Backend plugin contracts and external connector process supervision are explicitly unchanged.

### Canonical model neutrality — PASS

No `lipapi` request/event contract changes and no provider DTOs are introduced.

### Streaming and retry invariants — PASS

No request/stream execution code or retry decision path is changed. No-retry-after-output remains untouched.

### Lifecycle singularity — PASS

Generation resource phases remain exclusively `ResourceLedger`. Process shutdown remains exclusively host/`ProcessServices` owned. The private process owner is only an append facade over `ps.closers`; it has no independent release set, rollback method, success-time handoff, or close state.

### Type/interface scope — PASS

The ownership types are package-private, one-way release registration only. The owned acquisition helper requires a non-nil release; value-only/non-owning construction bypasses it. The design explicitly rejects `Get`, `Resolve`, `Provide`, keyed lookup, reflection, and container behavior.

### Concurrency — PASS with mandatory tests

The generation loop helper uses a cancellation-aware start gate, registers close-only cancel+join before opening the gate, and remains safe when `ResourceLedger.AddClose` executes cleanup synchronously for sealed/quiesced state. Model-registry refresh remains `PhaseQuiesce`, catalog cleanup remains `PhaseClose`, and refresh must terminate first. Race/goleak coverage is mandatory.

### Error behavior — PASS

Existing aggregate cleanup and shutdown semantics are preserved. Constructor rollback and normal shutdown consume the same authoritative process closer set, preventing double cleanup through a parallel owner path. No public error category or wire mapping changes.

### Maintainability / simplification — PASS

The selected process builders stop returning closer lists to `NewProcessServices`; old caller-side plumbing must be deleted. Special explicit teardown remains explicit where abstraction would reduce clarity.

## Design Strengths

1. **The design is intentionally asymmetric.** Process ownership is improved where it is manual; `ResourceLedger` and backend cleanup are left alone because they already solve the problem well.
2. **There is an explicit kill switch for abstraction creep.** A migrated path must delete plumbing or establish a stronger invariant; otherwise it is reverted.

## Design-to-Requirement Trace

| Requirement | Validation result |
|---|---|
| 1 Preserve runtime model | PASS |
| 2 Atomic process ownership | PASS |
| 3 Eliminate closer propagation | PASS |
| 4 Structured generation loops | PASS with race/leak gate |
| 5 Preserve ledger/backend semantics | PASS |
| 6 TDD/simplification gates | PASS |

## Final Assessment

**GO for design validation.** The design is sufficiently specified for approval and subsequent implementation as a focused brownfield refactor. It is **not yet authorized for implementation**: `spec.json` intentionally remains `phase: tasks-generated`, with requirements/design/tasks approvals false and `ready_for_implementation: false`. Implementation must not begin until the repository's Kiro approval state is updated according to the normal workflow. Once approved, implementation must remain inside the frozen scope: private ownership primitives, selected `ProcessServices` closer-propagation removal, model-registry refresh loop ownership, and architecture/test ratchets. Any proposal for reactive dependencies, public lifecycle APIs, a generalized effect/container runtime, or `ResourceLedger` redesign requires a new spec.
