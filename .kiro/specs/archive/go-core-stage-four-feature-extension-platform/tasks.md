# Implementation Plan

## Task List

Spec directory: `go-core-stage-four-feature-extension-platform`

**Implementation strategy:** deliver the extension platform as a contract-first brownfield migration. Each task should land with RED tests first, preserve the existing hook bus until the richer seams are proven, and keep request execution bound to stable per-request runtime snapshots.

**Implementation graph note:** later phases depend on the SDK contracts, stage descriptors, and runtime-snapshot model established in early phases. Proof plugins and docs are stage-exit gates, not optional polish.

---

### Phase 0 - seam map, guardrails, and RED scaffolding

- [x] **1.** Capture the stage-four seam map, migration guardrails, and proof criteria in project docs before code changes expand the runtime surface
  - Record the stage-owned extension seams, privileged-contract boundaries, brownfield migration rules, and reload-friendly non-goals for this stage.
  - Update or add ADR/design-note material describing how stage four preserves the hook bus while moving to the richer extension model.
  - _Requirements: **R14**, **R15**, **R16**, **Q1**, **Q5**, **Q6**, **Q7**_
  - _Design: **§14**, **§15**, **§15B**, **§18**, **§19**_
  - _Boundary: `docs`, `.kiro/specs/archive/go-core-stage-four-feature-extension-platform`_
  - _Depends: (none)_
  - _Validation: doc review plus any touched doc checks_

- [x] **1.1** Add RED regression scaffolding for stage ordering, privilege visibility, proof-plugin exit criteria, and runtime-snapshot stability
  - Create failing tests or harnesses that assert deterministic stage ordering, fail-open/fail-closed behavior, privileged capability visibility, and stable per-request execution snapshots.
  - _Requirements: **R2**, **R10**, **R14**, **R16**, **Q4**, **Q7**_
  - _Design: **§1**, **§10**, **§17**, **§19**_
  - _Boundary: `internal/testkit`, `internal/core`, `internal/stdhttp`_
  - _Depends: **1**_
  - _Validation: `go test ./internal/testkit/... ./internal/core/... ./internal/stdhttp/...`_

---

### Phase 1 - feature bundle contracts and hook-bus compatibility

- [x] **2.** Replace hook-only feature assembly with a typed, versionable `FeatureBundle` contract in the stable SDK
  - Define the bundle type, version/schema metadata, and empty-stage semantics.
  - Preserve current submit/request-part/response-part/tool-reactor hooks as first-class bundle members.
  - _Requirements: **R1**, **R15**, **Q2**, **Q6**, **Q7**_
  - _Design: **§1**, **§15**, **§15B**_
  - _Boundary: `pkg/lipsdk/feature`, `pkg/lipsdk/hooks`_
  - _Depends: **1.1**_
  - _Validation: `go test ./pkg/lipsdk/...`_

- [x] **2.1** Implement brownfield compatibility adapters so current hook-only feature plugins can register through the new bundle composition path
  - Add mechanical adapters or shims for existing noop/reference plugins and prove that missing stage handlers are treated as absent, not as runtime special cases.
  - _Requirements: **R1**, **R15**, **Q6**_
  - _Design: **§1**, **§15**_
  - _Boundary: `internal/pluginreg`, `internal/plugins/features`, `pkg/lipsdk/feature`_
  - _Depends: **2**_
  - _Validation: `go test ./internal/pluginreg/... ./internal/plugins/features/...`_

---

### Phase 2 - explicit stage model, ordering, and failure policy

- [x] **3.** Define the stage registry and typed stage descriptors for the extension pipeline
  - Model the legal stage list, allowed mutation modes, and stage-owned data visibility without allowing plugins to invent ad hoc runtime stages.
  - _Requirements: **R2**, **R12**, **Q1**, **Q3**_
  - _Design: **§1**, **§5**, **§6**, **§17**_
  - _Boundary: `pkg/lipsdk/feature`, `internal/core/extensions`_
  - _Depends: **2**_
  - _Validation: `go test ./internal/core/extensions/... ./pkg/lipsdk/...`_

- [x] **3.1** Implement deterministic intra-stage ordering and the per-stage failure matrix
  - Encode the canonical ordering rule (`priority/order -> plugin or bundle ID -> registration tie-break`) and explicit fail-open/fail-closed behavior across stage runners.
  - _Requirements: **R2**, **R14**, **Q3**, **Q4**_
  - _Design: **§17**_
  - _Boundary: `internal/core/extensions`, `internal/core/hooks`, `internal/testkit`_
  - _Depends: **3**_
  - _Validation: `go test ./internal/core/extensions/... ./internal/core/hooks/...`_

- [x] **3.2** Expose stage occupancy, ordering, failure mode, and privilege metadata through diagnostics/inventory surfaces
  - Ensure operators can inspect enabled feature bundles, active stage classes, and privileged capabilities.
  - _Requirements: **R14**, **Q1**_
  - _Design: **§14**, **§17**_
  - _Boundary: `internal/stdhttp/inventory`, `internal/core/extensions`, `internal/core/diag`_
  - _Depends: **3.1**_
  - _Validation: `go test ./internal/stdhttp/... ./internal/core/...`_

---

### Phase 3 - typed context views, service facades, and runtime snapshots

- [x] **4.** Add stable plugin-facing context views for principal, session, attempt, workspace, and annotations
  - Keep provider SDK types, transport globals, core configs, and mutable internals out of plugin contracts.
  - _Requirements: **R3**, **Q2**_
  - _Design: **§2**_
  - _Boundary: `pkg/lipsdk/feature`, `pkg/lipsdk/session`, `pkg/lipsdk/workspace`, `internal/core/execctx`_
  - _Depends: **3**_
  - _Validation: `go test ./pkg/lipsdk/... ./internal/core/execctx/...`_

- [x] **4.1** Define narrow service facades for plugin state, auxiliary requests, workspace access, and traffic observation/capture
  - Ensure plugins receive capability-specific interfaces rather than a general-purpose service locator.
  - _Requirements: **R3**, **R6**, **R7**, **R10**, **Q2**_
  - _Design: **§2**, **§7**, **§8**, **§10**_
  - _Boundary: `pkg/lipsdk/state`, `pkg/lipsdk/auxiliary`, `pkg/lipsdk/workspace`, `pkg/lipsdk/traffic`_
  - _Depends: **4**_
  - _Validation: `go test ./pkg/lipsdk/...`_

- [x] **4.2** Implement immutable runtime execution snapshots and bind each request to one stable snapshot for its lifetime
  - Build composition so active bundles, stage runners, and service bindings can be published as immutable runtime snapshots without relying on process-wide mutable globals.
  - _Requirements: **Q6**, **Q7**_
  - _Design: **§15**, **§15B**_
  - _Boundary: `internal/core/extensions`, `internal/infra/runtimebundle`, `internal/stdhttp`_
  - _Depends: **4.1**_
  - _Validation: `go test ./internal/core/extensions/... ./internal/infra/runtimebundle/... ./internal/stdhttp/...`_

- [x] **4.3** Add deterministic fakes/builders for context views, service facades, and runtime-snapshot publication
  - Support TDD for stage ordering, stateful plugins, and future runtime-reload compatibility assumptions.
  - _Requirements: **R3**, **Q4**, **Q7**_
  - _Design: **§2**, **§15B**_
  - _Boundary: `internal/testkit`, `pkg/lipsdk`_
  - _Depends: **4.2**_
  - _Validation: `go test ./internal/testkit/... ./pkg/lipsdk/...`_

---

### Phase 4 - transport auth, principal propagation, session open, and workspace resolution

- [x] **5.** Add the transport-auth seam to `stdhttp` and propagate a canonical principal view into the execution pipeline
  - Integrate auth providers before frontend decode, support reject/challenge/annotate flows, and keep HTTP-specific auth types out of core runtime contracts.
  - _Requirements: **R4**, **Q2**_
  - _Design: **§3**, **§13**_
  - _Boundary: `pkg/lipsdk/transport/httpauth`, `internal/stdhttp/auth`, `internal/core/execctx`_
  - _Depends: **4.2**_
  - _Validation: `go test ./pkg/lipsdk/transport/... ./internal/stdhttp/... ./internal/core/execctx/...`_

- [x] **5.1** Implement the session-open stage and workspace-resolution seam
  - Allow session-scoped initialization, workspace discovery, cached reuse, and safe propagation of workspace metadata into later stages.
  - _Requirements: **R5**, **R12**, **Q4**_
  - _Design: **§3**, **§9**_
  - _Boundary: `pkg/lipsdk/session`, `pkg/lipsdk/workspace`, `internal/core/extensions`, `internal/core/workspace`_
  - _Depends: **5**_
  - _Validation: `go test ./pkg/lipsdk/session/... ./pkg/lipsdk/workspace/... ./internal/core/workspace/... ./internal/core/extensions/...`_

---

### Phase 5 - plugin state service and expiry-aware semantics

- [x] **6.** Implement the plugin-scoped state service with namespace, scope, TTL, and deterministic test seams
  - Support request/session/principal/global scopes without adding feature-specific core fields or tables.
  - _Requirements: **R6**, **Q4**, **Q7**_
  - _Design: **§8**, **§15B**_
  - _Boundary: `pkg/lipsdk/state`, `internal/core/state`, `internal/testkit`_
  - _Depends: **4.1**, **5.1**_
  - _Validation: `go test ./pkg/lipsdk/state/... ./internal/core/state/... ./internal/testkit/...`_

- [x] **6.1** Add expiry-aware inspection semantics and prove state-backed behavior with deterministic tests
  - Cover read, write, delete, TTL inspection, and shared state use from session/workspace/tool-policy flows.
  - _Requirements: **R6**, **Q4**_
  - _Design: **§8**_
  - _Boundary: `internal/core/state`, `internal/testkit`_
  - _Depends: **6**_
  - _Validation: `go test ./internal/core/state/... ./internal/testkit/...`_

---

### Phase 6 - request shaping, tool catalog filtering, and provider-agnostic tool reaction

- [x] **7.** Implement the request-transform stage and integrate it with existing submit/request-part hooks
  - Preserve explicit layering between submit-time mutation, request-wide shaping, and low-level part hooks.
  - _Requirements: **R2**, **R12**, **R15**, **Q3**_
  - _Design: **§5**, **§17**_
  - _Boundary: `pkg/lipsdk/request`, `internal/core/extensions`, `internal/core/hooks`_
  - _Depends: **3.1**, **5.1**_
  - _Validation: `go test ./pkg/lipsdk/request/... ./internal/core/extensions/... ./internal/core/hooks/...`_

- [x] **7.1** Add the tool catalog filter stage before backend translation and tool-choice reconciliation
  - Ensure downstream adapters consume the post-policy tool set and that filtering remains separate from tool-event enforcement.
  - _Requirements: **R9**, **R12**_
  - _Design: **§4**, **§5**_
  - _Boundary: `pkg/lipsdk/toolcatalog`, `internal/core/extensions`, `internal/core/runtime`_
  - _Depends: **7**_
  - _Validation: `go test ./pkg/lipsdk/toolcatalog/... ./internal/core/extensions/... ./internal/core/runtime/...`_

- [x] **7.2** Extend tool-event reaction contracts without leaking provider SDK types into core or SDK packages
  - Keep existing tool reactors valid while making policy contracts explicitly provider-agnostic.
  - _Requirements: **R9**, **R15**, **Q2**_
  - _Design: **§4**, **§15**_
  - _Boundary: `pkg/lipsdk/hooks`, `internal/core/hooks`, `internal/plugins/features`_
  - _Depends: **7.1**_
  - _Validation: `go test ./pkg/lipsdk/hooks/... ./internal/core/hooks/... ./internal/plugins/features/...`_

---

### Phase 7 - auxiliary requests, route roles, and advisory route hints

- [x] **8.** Implement the auxiliary-request service with lineage, role support, loop guards, and request-lifetime safety rules
  - Support private internal subrequests without giving plugins direct backend or executor access.
  - _Requirements: **R7**, **R8**, **R13**, **Q4**_
  - _Design: **§6**, **§7**, **§17**_
  - _Boundary: `pkg/lipsdk/auxiliary`, `internal/core/auxreq`, `internal/core/runtime`_
  - _Depends: **4.1**, **6**_
  - _Validation: `go test ./pkg/lipsdk/auxiliary/... ./internal/core/auxreq/... ./internal/core/runtime/...`_

- [x] **8.1** Add route-role and route-hint plumbing while keeping routing authoritative in core
  - Prove that hints are advisory only and do not bypass capability, health, eligibility, or recovery rules.
  - _Requirements: **R7**, **R13**, **R14**_
  - _Design: **§12**, **§14**_
  - _Boundary: `pkg/lipsdk/routehint`, `internal/core/routing`, `internal/core/extensions`, `internal/stdhttp/inventory`_
  - _Depends: **8**_
  - _Validation: `go test ./pkg/lipsdk/routehint/... ./internal/core/routing/... ./internal/stdhttp/...`_

---

### Phase 8 - completion gates and whole-response control

- [x] **9.** Implement the completion-gate seam, typed decision model, and bounded buffering behavior
  - Support pass-through, buffered decision, replacement, rejection, replay, and fail-open overflow behavior without violating streaming-first semantics.
  - _Requirements: **R8**, **R12**, **R14**, **Q4**_
  - _Design: **§6**, **§17**_
  - _Boundary: `pkg/lipsdk/completion`, `internal/core/extensions`, `internal/core/runtime`_
  - _Depends: **8**_
  - _Validation: `go test ./pkg/lipsdk/completion/... ./internal/core/extensions/... ./internal/core/runtime/...`_

- [x] **9.1** Add completion-gate regression tests for overflow, no-post-output replacement, and auxiliary-influenced final decisions
  - Cover gate failure policy, replacement, replay, pass-through, and bounded buffering limits.
  - _Requirements: **R8**, **Q4**_
  - _Design: **§6**, **§17**, **§19**_
  - _Boundary: `internal/core/extensions`, `internal/testkit`_
  - _Depends: **9**_
  - _Validation: `go test ./internal/core/extensions/... ./internal/testkit/...`_

---

### Phase 9 - four-leg traffic observation, privileged capture, and redaction boundaries

- [x] **10.** Implement traffic observers, privileged raw capture sinks, and redaction-stage plumbing across all four traffic legs
  - Keep general observers non-mutating and raw capture explicitly privileged.
  - _Requirements: **R10**, **R11**, **R14**, **Q2**_
  - _Design: **§10**, **§11**_
  - _Boundary: `pkg/lipsdk/traffic`, `internal/core/traffic`, `internal/stdhttp`, `internal/core/runtime`_
  - _Depends: **4.1**, **8.1**, **9**_
  - _Validation: `go test ./pkg/lipsdk/traffic/... ./internal/core/traffic/... ./internal/stdhttp/... ./internal/core/runtime/...`_

- [x] **10.1** Prove the per-leg observation pipeline contract with explicit raw-capture, redaction, and structured-observer tests
  - Assert verbatim-vs-mutated comparisons, privilege separation, and correlation metadata for CTP, PTB, BTP, and PTC legs.
  - _Requirements: **R10**, **R11**, **Q4**_
  - _Design: **§10**, **§11**, **§17**_
  - _Boundary: `internal/core/traffic`, `internal/testkit`_
  - _Depends: **10**_
  - _Validation: `go test ./internal/core/traffic/... ./internal/testkit/...`_

---

### Phase 10 - reference proof plugins and stage-exit gates

- [x] **11.** Implement the minimum proof-plugin set required to show the new seams are extension-complete
  - Deliver narrow proof plugins rather than production-grade Python feature ports.
  - _Requirements: **R16**, **Q4**, **Q5**, **Q6**_
  - _Design: **§19**_
  - _Boundary: `internal/plugins/features`, `internal/testkit`, `config`_
  - _Depends: **5.1**, **6.1**, **7.2**, **8.1**, **9.1**, **10.1**_
  - _Validation: `go test ./internal/plugins/features/... ./internal/testkit/...` plus focused composed integration tests_

- [x] **11.1** Add `ref-autoappend-file` (or equivalent) to prove session-open plus submit/request shaping behavior
  - _Requirements: **R5**, **R12**, **R16**_
  - _Design: **§3**, **§5**, **§19**_
  - _Boundary: `internal/plugins/features`, `internal/testkit`_
  - _Depends: **5.1**, **7**_

- [x] **11.2** Add `ref-tool-policy` (or equivalent) to prove tool catalog filtering plus tool-event enforcement
  - _Requirements: **R9**, **R16**_
  - _Design: **§4**, **§19**_
  - _Boundary: `internal/plugins/features`, `internal/testkit`_
  - _Depends: **7.2**_

- [x] **11.3** Add `ref-workspace-guard` (or equivalent) to prove workspace resolution plus state-backed safety behavior
  - _Requirements: **R5**, **R6**, **R16**_
  - _Design: **§8**, **§9**, **§19**_
  - _Boundary: `internal/plugins/features`, `internal/testkit`_
  - _Depends: **5.1**, **6.1**_

- [x] **11.4** Add `ref-traffic-transcript` (or equivalent) to prove four-leg observation and redacted export behavior
  - _Requirements: **R10**, **R11**, **R16**_
  - _Design: **§10**, **§11**, **§19**_
  - _Boundary: `internal/plugins/features`, `internal/testkit`_
  - _Depends: **10.1**_

- [x] **11.5** Add `ref-verifier-stub` (or equivalent) to prove completion gating plus auxiliary-request replacement flow
  - _Requirements: **R7**, **R8**, **R13**, **R16**_
  - _Design: **§6**, **§7**, **§12**, **§19**_
  - _Boundary: `internal/plugins/features`, `internal/testkit`_
  - _Depends: **8.1**, **9.1**_

---

### Phase 11 - docs, authoring guidance, and architecture verification gates

- [x] **12.** Publish extension-platform authoring guidance, migration notes, and seam-selection rules for future feature work
  - Document stage ordering, service-facade usage, privileged observer rules, hook-to-bundle migration, and how to choose the right seam.
  - _Requirements: **R14**, **R15**, **Q5**, **Q6**, **Q7**_
  - _Design: **§14**, **§15**, **§15B**, **§18**, **§19**_
  - _Boundary: `docs`, `.kiro/specs/archive/go-core-stage-four-feature-extension-platform`_
  - _Depends: **11**_
  - _Validation: doc review plus any touched doc checks_

- [x] **12.1** Add architecture tests and regression gates that prevent backsliding into core/plugin coupling
  - Assert no provider SDK leakage into stable feature SDK contracts, no concrete feature imports in core, no transport-auth leakage into core, and no hidden bypass of the extension seams.
  - _Requirements: **Q1**, **Q2**, **Q3**, **Q4**_
  - _Design: **§18**_
  - _Boundary: `internal/archtest`, `internal/qa`, `internal/testkit`_
  - _Depends: **12**_
  - _Validation: `go test ./internal/archtest/... ./internal/qa/...`_

- [x] **12.2** Add runtime-snapshot stability and inventory regression checks as final stage gates
  - Prove that requests execute against stable snapshots and that diagnostics continue to expose stage occupancy, failure mode, and privileged capabilities.
  - _Requirements: **R14**, **Q7**_
  - _Design: **§14**, **§15B**, **§17**_
  - _Boundary: `internal/core/extensions`, `internal/stdhttp/inventory`, `internal/testkit`_
  - _Depends: **12.1**_
  - _Validation: `go test ./internal/core/extensions/... ./internal/stdhttp/... ./internal/testkit/...`_

---

## Suggested execution order

1. seam map and RED scaffolding
2. feature bundle contracts and hook compatibility
3. stage registry, ordering, and failure policy
4. context views, service facades, and runtime snapshots
5. transport auth plus session/workspace seams
6. plugin state service
7. request shaping and tool policy stages
8. auxiliary requests and route hints
9. completion gates
10. traffic observation, capture, and redaction
11. reference proof plugins
12. docs and architecture verification gates

This order keeps the work contract-first, preserves the brownfield migration path, and makes the proof plugins the explicit exit gate for stage completion.
