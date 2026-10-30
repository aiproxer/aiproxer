# Requirements Document

## Go core reimplementation — stage two

Spec directory: `go-core-reimplementation-stage-two`

**Baseline assumption:** Material issues documented in [`v1_code_review.md`](v1_code_review.md) are addressed before or during the earliest implementation tasks of this stage, either as shipped fixes or as explicit regression-locked work in Phase 0 of the implementation plan.

## Project description (input)

Turn the v1 Go reference implementation into an **architecturally honest, extensible, production-credible** platform for LIP. The primary outcome is not “more features” alone: the runtime must become **registry-driven**, the execution model **attempt-local** with an **immutable client baseline**, and continuity, routing, and feature behavior must flow through **stable plugin and policy seams** instead of bundle-specific switchboards.

## Scope and boundaries

**Primary target**

Evolve the existing greenfield Go core so that configuration, SDK contracts, runtime behavior, and standard-bundle composition agree, while preserving distinctive LIP strengths (B2BUA-like pre-output recovery, routing/failover, cross-API translation through canonical models, streaming-first execution).

**In scope for this spec**

- Registry- and factory-driven bundle composition for the standard distribution
- Plugin lifecycle orchestration for resource-owning components
- Immutable baseline canonical request plus per-attempt derived calls
- Accurate hook and tool-reactor metadata across phases and failover
- Routing policy layer: health, circuit behavior, `max_attempts`, deterministic weighted routing, first-request steering
- Pluggable continuity / attempt lineage stores (memory and SQLite)
- Model- or candidate-aware capability negotiation where provider variance requires it
- Frontend mount configuration driven by config and registries
- Expanded protocol fidelity for tool-use history on the supported subset (OpenAI Chat, OpenAI Responses, cross-API goldens)
- Real feature plugins for submit, request-part, response-part, and tool-reactor families with explicit failure policy
- Shared HTTP request body limits and deterministic oversized-body errors
- Expanded conformance, replay, and regression coverage (including encoding of review findings as tests)

**Explicitly out of scope for this stage**

- Out-of-process plugin execution
- Dynamic loading via Go’s native `plugin` package
- Audio/video realtime protocols
- Full operator UI / admin console beyond diagnostics endpoints
- Full parity with every vendor-only exotic parameter

## Requirements

### Requirement ID convention

Each acceptance criterion is labeled **`N.M`** (requirement **N**, criterion **M**). These IDs are the stable handles used in [`tasks.md`](tasks.md), [`design.md`](design.md), and traceability. To find a criterion, search this file for `**N.M**` (for example `**3.2**`).

**Disambiguation:** Task numbers in `tasks.md` (for example task **5**) are unrelated to requirement **5** in this file. Always use the filename to distinguish task IDs from requirement criterion IDs.

---

### Requirement 1: Bundle composition shall be registry-driven

**Objective:** As a maintainer, I want bundled plugins constructed only through registries and factories, so adding a plugin does not require editing switchboard code.

#### Acceptance criteria

**1.1.** When a maintainer adds a bundled protocol plugin (frontend, backend, feature, store, or observer) to the standard distribution, the system shall register and construct that plugin only through registry-backed factories, without requiring edits to switchboard-style wiring such as manual `switch` on plugin ID in composition roots.

**1.2.** When the standard distribution exposes HTTP surfaces for a bundled frontend, the system shall obtain mount paths and handlers through the frontend factory contract (or equivalent registry-supplied registration), not through a hardcoded mount table in runtime-adjacent packages that bypasses registration.

**1.3.** When configuration is loaded for a bundled plugin, the system shall pass plugin-private configuration as an opaque payload to that plugin’s factory, and the core shall not embed plugin-private schema decoding inside core configuration structs.

**1.4.** When a frontend plugin configuration row sets `enabled` (or equivalent) to false, the system shall not mount that frontend’s HTTP handlers.

**1.5.** When the standard distribution needs a default route selector, default backend, or equivalent default-routing input for requests that omit explicit routing headers or fields, the system shall source that default from registry-backed frontend metadata, routing policy configuration, or another documented composition input, rather than from ad hoc per-handler wiring logic that bypasses the registry/policy model.

---

### Requirement 2: Plugin lifecycle shall be first-class

**Objective:** As an operator, I want plugins that hold resources to start and stop in a predictable way, so leaks and ordering bugs do not appear under load.

#### Acceptance criteria

**2.1.** When the standard distribution completes bootstrap before serving traffic, the system shall invoke start semantics for every lifecycle-aware plugin in a deterministic documented order.

**2.2.** When the process performs graceful shutdown, the system shall invoke stop semantics for every lifecycle-aware plugin in reverse order relative to the documented start order.

**2.3.** If any lifecycle-aware plugin fails during startup, the system shall abort startup and shall not begin serving traffic until the failure is resolved or the configuration is corrected.

**2.4.** When determining lifecycle transition order among plugins, the system shall use explicit deterministic ordering rules (for example stable sort by registration id), not nondeterministic map iteration order alone.

---

### Requirement 3: Execution shall use an immutable baseline and attempt-local derived calls

**Objective:** As an architect, I want every backend attempt to start from the same logical client request plus explicit per-attempt work, so retries do not leak downgrades or hook mutations across attempts.

#### Acceptance criteria

**3.1.** When a client request has been accepted and decoded into the canonical call model, the system shall treat that logical client payload as immutable baseline state for the lifetime of that client request’s orchestration.

**3.2.** When the runtime begins a new backend attempt, the system shall derive a fresh attempt-local canonical call copy from the baseline for that attempt before applying attempt-local negotiation, merges, or request-part hooks.

**3.3.** When capability negotiation applies downgrades for a given attempt, the system shall apply those downgrades only to the attempt-local call copy, not to the baseline.

**3.4.** When a recoverable pre-output failure causes a replacement attempt, the system shall derive the next attempt’s call from the baseline (plus explicit policy-driven inputs), such that capability downgrades and request-part hook mutations from a prior failed attempt do not persist implicitly into the next attempt.

---

### Requirement 4: Hook and reactor APIs shall receive accurate metadata

**Objective:** As a feature author, I want trace, session, and attempt context on every hook invocation, so policies and reactors can correlate behavior without global state.

#### Acceptance criteria

**4.1.** When submit hooks execute, the system shall supply trace metadata required by the stable submit-hook contract (including trace identifier).

**4.2.** When request-part hooks execute for an attempt, the system shall supply trace identifier, A-leg identifier, and when applicable B-leg identifier and attempt sequence consistent with the active attempt.

**4.3.** When response-part hooks process canonical stream events, the system shall supply full attempt metadata defined by the stable contract for that phase.

**4.4.** When tool reactors process canonical tool events, the system shall supply full attempt metadata defined by the stable contract.

**4.5.** When execution switches from one B-leg to another during recoverable failover within the same client request, the system shall update hook metadata so B-leg and attempt sequence fields reflect the new attempt context.

---

### Requirement 5: Routing policy shall be explicit, health-aware, and cap-bounded

**Objective:** As an operator, I want routing limits and health to behave as documented, so configuration is not decorative.

#### Acceptance criteria

**5.1.** When route planning and retry logic execute for a client request, the system shall enforce the configured maximum attempt count so the runtime does not open B-legs beyond that cap (including failures encountered at open time and recv-time replacement).

**5.2.** When a route candidate is temporarily excluded by health or circuit-breaker policy, the exclusion shall be applied through the routing policy layer (planner or dedicated policy unit), without requiring ad hoc health checks scattered through stream execution branches as the sole mechanism.

**5.3.** When weighted routing is used, the system shall produce deterministic outcomes in tests when a controlled randomness or shuffle source is injected.

**5.4.** When first-request steering is configured, the system shall persist steering consumption at A-leg or session scope per the routing contract so retries and failover respect documented semantics.

**5.5.** When a selector resolves to a model-only primary (no backend key) or equivalent ambiguous form, the system shall either reject the selector deterministically before upstream work begins, or resolve it through explicit documented policy (for example default backend or named route policy); the system shall not fail with an undocumented empty-backend lookup surprise.

---

### Requirement 6: Continuity and attempt lineage shall support configurable and durable stores

**Objective:** As an operator, I want to choose memory or SQLite continuity stores and have options honored, so continuity is operationally real.

#### Acceptance criteria

**6.1.** When configuration selects a continuity store implementation, the system shall construct that store through the registry/factory model rather than hardcoding a single store implementation in the composition root.

**6.2.** When the in-memory store is selected, the system shall honor documented memory-store options from configuration (for example TTL, maximum legs) rather than silently using zero-valued defaults that ignore configuration.

**6.3.** When the SQLite store is selected, the system shall persist A-leg continuity and attempt lineage such that the data survives normal process restart.

**6.4.** When diagnostics expose attempt lineage for an A-leg, the system shall read that data from the configured store implementation.

---

### Requirement 7: Capability negotiation shall be candidate-aware

**Objective:** As an architect, I want capabilities resolved per route candidate and model where needed, so negotiation does not false-accept or false-reject based on coarse static labels.

#### Acceptance criteria

**7.1.** When capability negotiation runs for a planned backend attempt, the system shall resolve effective capabilities with respect to the route candidate identity (including model where the provider matrix is model-dependent), not solely from a single static capability blob attached only to the backend plugin type when variance exists.

**7.2.** When negotiation yields an explicit soft downgrade relative to the client request, the system shall represent that downgrade in a test-observable way.

**7.3.** When a required capability cannot be satisfied for the resolved candidate before upstream invocation begins, the system shall reject the attempt with a deterministic capability error.

---

### Requirement 8: Protocol fidelity for tool-use history shall expand on the supported subset

**Objective:** As a user of mixed clients, I want tool-call history to round-trip through the canonical model for documented subsets, so multi-turn tool flows work across APIs.

#### Acceptance criteria

**8.1.** When the OpenAI Chat-compatible frontend receives conversation history that includes assistant tool-call representation within the documented supported subset, the system shall decode that history into the canonical model without rejecting the request solely due to presence of that history shape.

**8.2.** When the OpenAI Responses-compatible frontend receives non-message input items required for tool-result continuation within the documented supported subset, the system shall decode those items into the canonical representation.

**8.3.** The canonical history representation shall be sufficient to represent the supported subsets in **8.1** and **8.2** without undocumented semantic loss for those subsets.

**8.4.** When multi-turn tool-use conversations remain within the supported subset, the system shall pass round-trip tests through the canonical form for the documented cross-API golden matrix.

**8.5.** When extending protocol fidelity, the system shall preserve the architecture rule that cross-API translation flows through protocol-to-canonical and canonical-to-protocol adapters only, not through new undocumented pairwise protocol translators.

---

### Requirement 9: Feature plugins shall provide real submit, part, and tool-reactor behavior

**Objective:** As a feature author, I want factories, config, and ordering for real hook implementations, so features are not no-op placeholders wired by switch statements.

#### Acceptance criteria

**9.1.** When a submit-hook, request-part, response-part, or tool-reactor feature plugin is configured, the system shall instantiate it through its factory with an opaque configuration payload decoded by that factory.

**9.2.** When submit hook plugins run, the system shall allow them to reject or annotate the logical request per the stable submit-hook contract.

**9.3.** When request-part hook plugins run, the system shall apply them to the attempt-local derived call per attempt per the stable contract.

**9.4.** When response-part hook plugins run, the system shall apply them per canonical event per the stable contract.

**9.5.** When tool-reactor plugins run, the system shall allow pass-through, rewrite, replace, or swallow decisions per the stable contract, and shall implement an explicit, documented failure policy (contract field, configuration, or both) rather than an implicit undefined failure mode.

**9.6.** When multiple plugins of the same hook family are registered, the system shall invoke them in deterministic order (for example ascending priority then stable id tie-break as documented for the runtime).

---

### Requirement 10: Frontend HTTP request hardening shall be shared and explicit

**Objective:** As an operator, I want oversized uploads rejected cleanly and consistently across frontends.

#### Acceptance criteria

**10.1.** When any bundled HTTP frontend receives a request body larger than the configured maximum, the system shall return a deterministic oversized-body error mapping (for example 413-class semantics per that frontend’s documented error mapping).

**10.2.** The system shall enforce maximum body size using a shared, documented mechanism (for example `http.MaxBytesReader` or equivalent) suitable for `net/http` handlers across all bundled HTTP frontends.

**10.3.** When maximum body size is configured, all bundled HTTP frontends shall obtain that limit from the same central configuration value or other single documented source of truth.

---

### Requirement 11: Conformance, replay, and regression coverage shall expand

**Objective:** As a maintainer, I want regressions and cross-protocol behavior locked by tests and fixtures, so refactors do not silently break semantics.

#### Acceptance criteria

**11.1.** The conformance or golden suite shall include cross-protocol cases for tool-use history within the supported subset.

**11.2.** The integration or replay suite shall cover attempt lineage behavior under recoverable failover for documented scenarios.

**11.3.** The automated regression suite shall encode each material finding from [`v1_code_review.md`](v1_code_review.md) that remains relevant to stage-two goals (see traceability table below), such that recurrence fails CI.

---

### Requirement 12: Small-core discipline shall remain mandatory

**Objective:** As an architect, I want the core to stay provider-agnostic and free of concrete plugin imports.

#### Acceptance criteria

**12.1.** Core runtime packages shall not import official provider SDK modules.

**12.2.** Core runtime packages shall not import bundled protocol plugin packages (concrete `internal/plugins/...` implementations).

**12.3.** Standard-bundle composition shall assemble registries and factories from composition roots (for example `cmd/lipstd` and adjacent bundle packages), not from within narrow core orchestration packages that should remain import-clean.

---

### Requirement 13: Stage two shall not replace coupling with a larger god object

**Objective:** As a maintainer, I want responsibilities split across focused units, so the executor does not absorb every new concern.

#### Acceptance criteria

**13.1.** The implementation shall separate attempt derivation, routing policy planning, continuity store access, and plugin lifecycle orchestration into cohesive units rather than collapsing them into a single monolithic type responsible for all concerns.

**13.2.** The implementation shall avoid introducing a single new translation or orchestration monolith that becomes the default home for unrelated protocol or policy logic.

---

### Requirement 14: Deterministic testability shall remain first-class

**Objective:** As a tester, I want injectable time and randomness and hermetic stores, so policy and lineage are provable.

#### Acceptance criteria

**14.1.** The routing and policy components that depend on time or randomness shall accept injected clocks and randomness (or shuffle ports) suitable for deterministic unit tests.

**14.2.** Store implementations used for continuity and attempt lineage shall support hermetic tests (for example temporary SQLite paths or isolated in-memory instances) without requiring shared global state.

**14.3.** Lifecycle start and stop ordering shall be verifiable in unit tests using registered plugin fixtures.

---

### Requirement 15: Provider SDK usage shall remain adapter-scoped

**Objective:** As an SDK consumer, I want stable core and hook contracts free of provider types.

#### Acceptance criteria

**15.1.** Provider SDK types shall not appear in `pkg/lipapi` canonical surfaces consumed by the core for orchestration decisions, nor in public hook SDK types in ways that force importers to depend on a vendor SDK.

**15.2.** Protocol adapter tests shall be able to stub upstream behavior without reaching past published seams into private core runtime internals.

---

## Quality and success criteria (cross-cutting)

Stage two is successful when the repository can honestly claim:

- Plugin decoupling means construction, configuration, lifecycle, and enablement all flow through registries and factories (**1.x**, **2.x**, **9.1**).
- B2BUA failover semantics remain correct under retries without baseline contamination (**3.x**, **4.x**).
- Submit, request-part, response-part, and tool-reactor surfaces are real plugin paths with correct metadata (**4.x**, **9.x**).
- Continuity and attempt lineage can survive process restart via SQLite where selected (**6.3**).
- Route planning is policy-driven and health-aware rather than encoded only in executor branches (**5.x**).
- Tool-use history coverage improves for the supported subset without pairwise translator explosion (**8.x**, **8.5**).

---

## Traceability to v1 code review (informative)

This table maps review labels in [`v1_code_review.md`](v1_code_review.md) to primary requirement criteria in this document. It is **non-normative**; the normative statements are the **`N.M`** acceptance criteria above.

| Review ID | Primary criteria |
|-----------|------------------|
| C0.1 (sticky baseline / retries) | **3.1**–**3.4** |
| C0.2 (empty hook metadata) | **4.1**–**4.5** |
| C0.3 (`max_attempts` unused) | **5.1** |
| C0.4 (continuity config non-operational) | **6.1**, **6.2** |
| C0.5 (model-only selectors) | **5.5** |
| C0.6 (body limits / 413) | **10.1**–**10.3** |
| A1.1 (switchboard composition) | **1.1**–**1.3** |
| A1.2 (frontend enabled not honored) | **1.4** |
| A1.3 (feature hooks / config ignored) | **9.1**, **9.6** |
| A1.4 (lifecycle unused) | **2.1**–**2.4** |
| A1.5 (static capabilities) | **7.1** |
| P1.1 (Chat tool history rejected) | **8.1** |
| P1.2 (Responses non-message items) | **8.2** |
| P1.3 (adapter duplication) | Addressed under **8.5**, **13.2**, and implementation tasks (helpers scoped per protocol, not a monolith) |
| L2.3 (default-route/default-model ownership) | **1.5**, **5.5** |
