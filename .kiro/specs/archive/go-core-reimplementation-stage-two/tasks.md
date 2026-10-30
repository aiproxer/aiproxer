# Implementation Plan

## Task List

Spec directory: `go-core-reimplementation-stage-two`

**Assumption:** Items in [`v1_code_review.md`](v1_code_review.md) are either already fixed on `main` or are encoded as failing/regression tests in **task 1** before large refactors merge.

**Implementation graph note:** A few tasks are checked complete while predecessor rows stay open because slices landed incrementally (notably **10.4**, **12**, **11.2**, **13.x**). `_Depends:` lines keep the original phase ordering; tick predecessor tasks when those phases are closed end-to-end, rather than treating an open `_Depends:` as proof the `[x]` row is invalid.

---

### Phase 0 — Baseline, regressions, and decisions

- [x] **1.** Encode `v1_code_review.md` regression matrix as automated tests (RED first where fixes are not yet landed)
  - Cover orchestration: baseline vs attempt stickiness, hook metadata, `max_attempts`, model-only selectors, body limits, composition/mount/lifecycle/store truthfulness per the review’s recommended test matrix.
  - _Requirements: **11.3**, **3.1**, **3.2**, **3.3**, **3.4**, **4.1**, **4.2**, **4.3**, **4.4**, **4.5**, **5.1**, **5.5**, **6.1**, **6.2**, **10.1**, **10.2**, **10.3**, **1.1**, **1.4**, **2.1**, **2.2**, **9.1**_
  - _Boundary: `internal/core/runtime`, `internal/stdhttp`, `cmd/lipstd`, `internal/plugins/frontends`, `internal/testkit`, `tests`_
  - _Depends: (none)_

- [x] **1.1** (P) Add focused unit tests for hook metadata fields across B-leg switches (`TraceID`, `ALegID`, `BLegID`, `AttemptSeq`)
  - _Requirements: **4.2**, **4.3**, **4.4**, **4.5**, **11.3**_
  - _Boundary: `internal/core/runtime`, `pkg/lipsdk/hooks`, `internal/testkit`_
  - _Depends: **1**_

- [x] **2.** Author architecture decision records for registry-driven composition, immutable baseline/attempt derivation, SQLite-first durable store, and candidate-aware capabilities
  - _Requirements: **13.1**, **13.2**, **1.1**, **3.1**, **6.3**, **7.1**_
  - _Boundary: `docs`, `.kiro/specs/archive/go-core-reimplementation-stage-two`_
  - _Depends: **1**_

- [x] **2.1** (P) Document branch or feature-flag policy for large refactors (maintainer workflow only)
  - _Requirements: **11.3**_
  - _Boundary: `docs`, repo policy_
  - _Depends: **2**_

---

### Phase 1 — Registries and factory-driven composition

- [x] **3.** Define stable factory contracts (`FrontendFactory`, `BackendFactory`, `FeatureFactory`, `StoreFactory`, observer hooks) in `pkg/lipsdk` (or adjacent stable packages) with opaque config payloads
  - _Requirements: **1.1**, **1.3**, **9.1**, **12.3**, **15.1**_
  - _Boundary: `pkg/lipsdk`, `pkg/lipapi` (types only if needed)_
  - _Depends: **2**_

- [x] **3.1** (P) Add compile-time or unit tests proving core packages do not import bundled plugins after registry extraction (`go test` boundary guards)
  - _Requirements: **12.1**, **12.2**, **12.3**, **15.1**, **15.2**_
  - _Boundary: `internal/testkit`, `scripts`, `internal/core`_
  - _Depends: **3**_

- [x] **4.** Implement standard-bundle registry: register built-in factories, validate uniqueness and mandatory coverage at startup
  - _Requirements: **1.1**, **1.2**, **2.3**_
  - _Boundary: `internal/pluginreg`, `internal/bundle`, `cmd/lipstd`, `pkg/lipsdk`_
  - _Depends: **3**_

- [x] **5.** Refactor `cmd/lipstd` (and related composition roots) to construct frontends, backends, features, stores, and observers only through registries/factories
  - _Requirements: **1.1**, **1.2**, **1.3**, **1.4**, **12.2**, **12.3**_
  - _Boundary: `cmd/lipstd`, `internal/stdhttp`, `internal/core/config`_
  - _Depends: **4**_

- [x] **5.1** Remove switch-based plugin composition from `cmd/lipstd` and `internal/stdhttp` wiring paths; retain explicit static registration lists that register into the registry only
  - _Requirements: **1.1**, **1.2**_
  - _Boundary: `cmd/lipstd`, `internal/stdhttp`_
  - _Depends: **5**_

- [x] **6.** Implement registry-driven HTTP mounting: each enabled frontend supplies `MountSpec` (or handler registration) from its factory-built instance; disabled rows are not mounted
  - _Requirements: **1.2**, **1.4**_
  - _Boundary: `internal/stdhttp`, `internal/plugins/frontends`, `pkg/lipsdk`_
  - _Depends: **5**_

---

### Phase 2 — Immutable baseline, attempt engine, hook metadata

- [x] **7.** Introduce immutable baseline representation and `AttemptBuilder` (or equivalent) that derives per-attempt `lipapi.Call` copies; stop persisting mutated calls as retry baselines
  - _Requirements: **3.1**, **3.2**, **3.3**, **3.4**, **13.1**_
  - _Boundary: `internal/core/runtime`, `pkg/lipapi`_
  - _Depends: **5.1**_

- [x] **8.** Split runtime orchestration into cohesive units (submit pipeline, continuity resolution, policy/plan, stream runner) without growing a single god type
  - _Requirements: **13.1**, **13.2**, **3.2**, **4.3**_
  - _Boundary: `internal/core/runtime`, `internal/core/middleware` (or new subpackages)_
  - _Depends: **7**_

- [x] **9.** Populate submit, request-part, response-part, and tool-reactor metadata from executor/planner state (`TraceID`, `ALegID`, `BLegID`, `AttemptSeq`); document phase semantics in code comments adjacent to callsites
  - _Requirements: **4.1**, **4.2**, **4.3**, **4.4**, **4.5**_
  - _Boundary: `internal/core/runtime`, `pkg/lipsdk/hooks`_
  - _Depends: **8**_

- [x] **9.1** Ensure request-part hooks run only on attempt-local derived calls; add tests for downgrade and hook non-stickiness across retries
  - _Requirements: **3.3**, **3.4**, **9.3**, **11.3**_
  - _Boundary: `internal/core/runtime`, `internal/core/hooks`, `internal/testkit`_
  - _Depends: **9**_

---

### Phase 3 — Routing policy, health, caps

- [x] **10.** Implement routing policy planner: enforce `max_attempts`, separate selector parsing from attempt planning, injectable RNG for weighted branches
  - _Requirements: **5.1**, **5.2**, **5.3**, **13.1**, **14.1**_
  - _Boundary: `internal/core/routing`, `internal/core/policy` (or equivalent new package)_
  - _Depends: **8**_

- [x] **10.1** Persist and test first-request steering at A-leg/session scope; deterministic weighted routing tests
  - _Requirements: **5.3**, **5.4**, **14.1**_
  - _Boundary: `internal/core/routing`, `internal/core/policy`, `internal/testkit`_
  - _Depends: **10**_

- [x] **10.2** Implement explicit model-only selector policy (reject early **or** resolve via documented default); remove empty-backend surprises
  - _Requirements: **5.5**, **11.3**_
  - _Boundary: `internal/core/routing`, `internal/core/runtime`, `internal/testkit`_
  - _Depends: **10**_

- [x] **10.3** Add health/circuit abstractions feeding the planner; unhealthy candidates excluded without executor-local ad hoc branching as the only path
  - _Requirements: **5.2**, **14.1**_
  - _Boundary: `internal/core/policy`, `internal/core/routing`_
  - _Depends: **10**_

- [x] **10.4** Make default-route/default-backend ownership registry- or policy-driven instead of handler-local
  - _Requirements: **1.5**, **5.5**_
  - _Boundary: `internal/stdhttp`, `internal/plugins/frontends`, `internal/core/routing`, `internal/core/policy`, `pkg/lipsdk`_
  - _Depends: **6**, **10.2**_
  - _Deliverables:_
    - one documented source of truth for fallback route selection when clients omit routing headers or fields
    - frontend factory metadata, routing policy config, or equivalent composition input that supplies the fallback without per-handler ad hoc wiring
    - removal of duplicated/default-routing constants from handler-local bundle wiring where that wiring bypasses registry or policy ownership
    - focused tests covering omitted-route requests across at least OpenAI Responses, OpenAI Chat, and one non-OpenAI frontend
  - _Acceptance criteria:_
    - omitted-route requests resolve through the documented default-routing source, not handler-local constants
    - changing the configured default route does not require editing frontend handler code
    - model-only selectors still resolve or fail according to the explicit policy from **10.2**
    - frontend mount registration and default-route ownership are separated cleanly: mounting decides exposure, policy decides fallback routing

---

### Phase 4 — Continuity stores (memory + SQLite)

- [x] **11.** Define stable store interfaces for continuity and attempt lineage in `pkg/lipsdk` (or dedicated stable package); wire store factories through the registry
  - _Requirements: **6.1**, **15.1**, **1.3**_
  - _Boundary: `pkg/lipsdk`, `internal/pluginreg`_
  - _Depends: **4**_

- [x] **11.1** Honor in-memory store options from configuration (TTL, max legs, documented limits)
  - _Requirements: **6.2**, **14.2**_
  - _Boundary: `internal/core/config`, `internal/core/b2bua` or `internal/core/continuity`, `internal/plugins/stores`_
  - _Depends: **11**_

- [x] **11.2** Implement SQLite-backed store plugin; persistence across restart; diagnostics read path uses configured store
  - _Requirements: **6.3**, **6.4**, **14.2**_
  - _Boundary: `internal/plugins/stores`, `internal/core/continuity`, `internal/core/diag`_
  - _Depends: **11**_
  - _Deliverables:_
    - SQLite-backed continuity/attempt-lineage store implementation behind the stage-two store seam
    - schema creation and migration/bootstrap logic suitable for local durable deployments
    - runtime selection path that opens SQLite when configuration requests it
    - diagnostics path that reads attempt lineage from the selected SQLite store
    - restart-survival test coverage using temporary on-disk SQLite state
  - _Acceptance criteria:_
    - when SQLite is configured, A-leg continuity and attempt lineage survive a normal process restart
    - in-memory and SQLite store selection both flow through the same documented factory/composition path
    - diagnostics do not special-case memory-only behavior once SQLite is selected
    - hermetic tests prove no shared global state is required for store-backed continuity behavior

---

### Phase 5 — Candidate-aware capabilities

- [x] **12.** Implement `CapabilityResolver` (or equivalent) that resolves per candidate/model; integrate with attempt planning; deterministic reject before upstream for unsupported required capabilities
  - _Requirements: **7.1**, **7.2**, **7.3**, **12.1**_
  - _Boundary: `internal/core/runtime`, `internal/core/routing`, `pkg/lipapi`, `internal/plugins/backends` (descriptor tables only, no core SDK imports)_
  - _Depends: **10**, **9.1**_
  - _Deliverables:_
    - stable candidate/model-aware capability resolution seam used by planning/runtime before upstream open
    - provider/model capability catalogs or descriptor tables for bundled backends where capability varies materially by target model or route flavor
    - deterministic negotiation tests for accept, reject, and downgrade paths across representative providers
    - documentation of which providers intentionally remain static and why
  - _Acceptance criteria:_
    - required-capability mismatches reject deterministically before upstream invocation
    - soft downgrades are test-observable and tied to the resolved candidate/model
    - capability resolution is not limited to one static blob per backend where provider variance exists
    - bundled capability catalogs are maintainable artifacts, not scattered inline conditionals without tests

- [x] **12.1** Expand bundled capability catalogs beyond minimal heuristics and document maintenance rules
  - _Requirements: **7.1**, **7.2**, **7.3**, **13.2**_
  - _Boundary: `internal/plugins/backends`, `docs`, `testdata`_
  - _Depends: **12**_
  - _Deliverables:_
    - documented capability-catalog ownership rules for bundled providers
    - representative catalog coverage for bundled providers with known model variance
    - regression fixtures proving catalog lookups for representative modern and legacy model families
  - _Acceptance criteria:_
    - adding a new bundled model family has one obvious capability-catalog update point
    - catalog drift on representative legacy/current model families fails tests
    - provider-specific capability exceptions are discoverable in one place per provider

---

### Phase 6 — Real feature plugins

- [x] **13.** Implement factory-driven feature plugins: decode opaque config in factories, return hook chains and optional lifecycle handles; deterministic ordering **9.6**
  - _Requirements: **9.1**, **9.6**, **12.2**_
  - _Boundary: `internal/plugins/features`, `pkg/lipsdk`, `cmd/lipstd`_
  - _Depends: **5**, **9**_
  - _Deliverables:_
    - factory-driven feature instantiation for submit, request-part, response-part, and tool-reactor families
    - opaque config decoding performed inside the feature boundary, not in core config structs
    - deterministic ordering behavior with tests across multiple plugins in the same family
    - lifecycle return path for resource-owning features
  - _Acceptance criteria:_
    - configured feature plugins are not placeholder-only; each family has at least one real non-noop implementation path in the standard distribution or documented example bundle
    - invalid feature config fails composition deterministically and early
    - disabled feature rows contribute no hooks or lifecycles
    - hook family ordering is stable under repeated runs and does not depend on map iteration

- [x] **13.1** Add explicit tool-reactor failure policy per **9.5** (contract and/or configuration); tests for pass/rewrite/replace/swallow and failure modes
  - _Requirements: **9.5**, **9.4**, **9.2**, **9.3**_
  - _Boundary: `pkg/lipsdk/hooks`, `internal/core/hooks`, `internal/plugins/features`_
  - _Depends: **13**_
  - _Deliverables:_
    - explicit tool-reactor failure-policy surface in stable contract, feature config, or both
    - tests for pass-through, rewrite, replace, swallow, and error behavior under each supported failure mode
    - runtime documentation near the callsite describing how failures propagate or are suppressed
  - _Acceptance criteria:_
    - tool-reactor failures are never governed by undocumented implicit behavior
    - supported decisions are exercised in tests with deterministic outcomes
    - failure-policy choice is visible to maintainers without tracing runtime internals

- [x] **13.2** Ship at least one reference plugin per family (submit annotator/rejector, request/response rewriter, tool reactor rewrite/swallow) as examples
  - _Requirements: **9.2**, **9.3**, **9.4**, **9.5**_
  - _Boundary: `internal/plugins/features`_
  - _Depends: **13.1**_
  - _Deliverables:_
    - one submit-family reference plugin with observable non-noop behavior
    - one request/response part plugin with observable canonical mutation behavior
    - one tool-reactor plugin that demonstrates rewrite or swallow behavior
    - tests and minimal operator/developer docs showing how each reference plugin is configured
  - _Acceptance criteria:_
    - each hook family has at least one runnable, test-backed, non-noop example in-tree
    - examples demonstrate behavior that matters to real usage, not only no-op plumbing
    - examples can be enabled through configuration without code edits

---

### Phase 7 — Tool-use history fidelity

- [x] **14.** Extend canonical history if required so **8.3** holds for the supported subset; document the subset in spec notes or package docs next to adapters
  - _Requirements: **8.3**, **8.5**, **13.2**_
  - _Boundary: `pkg/lipapi`, `internal/plugins/frontends`_
  - _Depends: **12**_

- [x] **14.1** Update OpenAI Chat frontend decode/encode for assistant tool-call history subset **8.1**
  - _Requirements: **8.1**, **8.4**, **11.1**_
  - _Boundary: `internal/plugins/frontends/openailegacy`, `testdata`_
  - _Depends: **14**_

- [x] **14.2** Update OpenAI Responses frontend for non-message input items subset **8.2**
  - _Requirements: **8.2**, **8.4**, **11.1**_
  - _Boundary: `internal/plugins/frontends/openairesponses`, `testdata`_
  - _Depends: **14**_

- [x] **14.3** (P) Add cross-protocol goldens Chat ↔ Responses ↔ Anthropic subset where already supported
  - _Requirements: **8.4**, **8.5**, **11.1**_
  - _Boundary: `testdata`, `internal/testkit`, `tests`_
  - _Depends: **14.1**, **14.2**_

- [x] **14.4** Review Anthropic/Gemini/Bedrock adapters for equivalent subset handling; adjust within **8.4**/**8.5**
  - _Requirements: **8.4**, **8.5**, **15.2**_
  - _Boundary: `internal/plugins/frontends`, `internal/plugins/backends`_
  - _Depends: **14.3**_

---

### Phase 8 — HTTP hardening (may be scheduled earlier if regressions demand)

- [x] **15.** Centralize max body size configuration; apply `http.MaxBytesReader` (or equivalent) in all bundled HTTP frontends; deterministic 413-class mapping **10.1**–**10.3**
  - _Requirements: **10.1**, **10.2**, **10.3**, **11.3**_
  - _Boundary: `internal/plugins/frontends`, `internal/stdhttp`, `internal/core/config`_
  - _Depends: **6**_

---

### Phase 9 — Plugin lifecycle orchestration

- [x] **16.** Wire `Lifecycle` start/stop from composition root: deterministic order, reverse on shutdown, startup failure aborts before traffic **2.1**–**2.4**
  - _Requirements: **2.1**, **2.2**, **2.3**, **2.4**, **14.3**_
  - _Boundary: `pkg/lipsdk/plugin`, `internal/core/runtime`, `cmd/lipstd`, `internal/stdhttp`_
  - _Depends: **5**, **11.2**_

---

### Phase 10 — Observability and diagnostics

- [x] **17.** Add structured route-planning traces, health/circuit diagnostics, plugin inventory (ids, mounts, enabled), optional observer seam
  - _Requirements: **5.2**, **6.4**, **1.2**_
  - _Boundary: `internal/core/diag`, `internal/stdhttp`, `internal/plugins`_
  - _Depends: **10.3**, **6**_

---

### Phase 11 — Conformance, replay, migration

- [x] **18.** Expand replay/conformance suite: failover lineage cases (requirement **11.2**); import or adapt Python LIP fixtures where feasible (requirement **11.1**)
  - _Requirements: **11.1**, **11.2**_
  - _Boundary: `tests`, `testdata`, `internal/testkit`_
  - _Depends: **10**, **14.3**, **11.2**_

- [x] **19.** Add routing/stream performance baselines (benchmarks) and operator migration notes (Python → Go routing and capability semantics)
  - _Requirements: **11.1**, **13.2**_
  - _Boundary: `docs`, `tests`_
  - _Depends: **18**_

---

## Requirement coverage checklist

Each acceptance criterion **`N.M`** in [`requirements.md`](requirements.md) appears in at least one task’s `_Requirements:` line below.

| Criterion | Task IDs |
|-----------|----------|
| **1.1** | **1**, **3**, **4**, **5**, **5.1** |
| **1.2** | **1**, **3**, **4**, **5**, **6**, **17** |
| **1.3** | **1**, **3**, **5**, **11** |
| **1.4** | **1**, **5**, **6** |
| **1.5** | **10.4** |
| **2.1** | **1**, **16** |
| **2.2** | **1**, **16** |
| **2.3** | **4**, **16** |
| **2.4** | **1**, **16** |
| **3.1** | **1**, **7** |
| **3.2** | **1**, **7**, **8** |
| **3.3** | **1**, **7**, **9.1** |
| **3.4** | **1**, **7**, **9.1** |
| **4.1** | **1**, **9** |
| **4.2** | **1**, **1.1**, **9** |
| **4.3** | **1**, **8**, **9** |
| **4.4** | **1**, **9** |
| **4.5** | **1**, **1.1**, **9** |
| **5.1** | **1**, **10** |
| **5.2** | **10**, **10.3**, **17** |
| **5.3** | **10**, **10.1** |
| **5.4** | **10.1** |
| **5.5** | **1**, **10.2** |
| **6.1** | **1**, **11** |
| **6.2** | **1**, **11.1** |
| **6.3** | **2**, **11.2** |
| **6.4** | **11.2**, **17** |
| **7.1** | **2**, **12** |
| **7.2** | **12**, **12.1** |
| **7.3** | **12**, **12.1** |
| **8.1** | **14.1** |
| **8.2** | **14.2** |
| **8.3** | **14** |
| **8.4** | **14.1**, **14.2**, **14.3**, **14.4** |
| **8.5** | **14**, **14.3**, **14.4** |
| **9.1** | **1**, **3**, **13** |
| **9.2** | **13.1**, **13.2** |
| **9.3** | **9.1**, **13.1** |
| **9.4** | **13.1**, **13.2** |
| **9.5** | **13.1**, **13.2** |
| **9.6** | **13** |
| **10.1** | **1**, **15** |
| **10.2** | **1**, **15** |
| **10.3** | **1**, **15** |
| **11.1** | **14.1**, **14.2**, **14.3**, **18**, **19** |
| **11.2** | **18** |
| **11.3** | **1**, **1.1**, **2.1**, **9.1**, **10.2**, **15** |
| **12.1** | **3.1**, **12** |
| **12.2** | **5**, **13** |
| **12.3** | **3**, **5**, **3.1** |
| **13.1** | **2**, **7**, **8**, **10**, **14** |
| **13.2** | **2**, **8**, **12.1**, **14**, **19** |
| **14.1** | **10**, **10.1**, **10.3** |
| **14.2** | **11.1**, **11.2** |
| **14.3** | **16** |
| **15.1** | **3**, **11** |
| **15.2** | **3.1**, **14.4** |

---

## Stage-two done checklist

- [x] All `v1_code_review.md` regressions covered by tests (**11.3**)
- [x] Registry-driven bundle composition replaces switchboards (**1.x**)
- [x] Frontend enablement is real (**1.4**)
- [x] Default-route ownership is registry- or policy-driven rather than handler-local (**1.5**)
- [x] Plugin lifecycle is real (**2.x**)
- [x] Immutable baseline + per-attempt derivation is in place (**3.x**)
- [x] Hook metadata is correct (**4.x**)
- [x] `max_attempts` and model-only policy are truthful (**5.1**, **5.5**)
- [x] Durable store option exists and works (**6.x**)
- [x] Default-route/default-backend fallback is configurable without handler edits (**1.5**, **5.5**)
- [x] Real feature plugins exist for submit/request-response/tool seams (**9.x**)
- [x] Tool-use history coverage expanded for supported subset (**8.x**)
- [x] Routing diagnostics and health policy are operational (**5.2**, **17**)

---

## Suggested workflow within each task

1. Align with ADRs from **task 2** where relevant.
2. Write failing tests first (TDD).
3. Implement the smallest slice that makes tests pass.
4. Run `make test` (or scoped `go test`) before claiming the task done.
5. Update config examples only when behavior or schema changes.
