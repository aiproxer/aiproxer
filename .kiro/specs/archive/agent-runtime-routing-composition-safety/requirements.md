# Requirements Document

## Introduction

Go-LIP intentionally presents many different backend implementations through one routing language. For ordinary inference endpoints this is a strength: ordered failover, weighted selection, parallel first-valid-output races, affinity, TTFT controls, and `[thinker]` composition can select among model-like request/response targets while core retains B2BUA ownership.

Issue #323 identifies a different execution class now present in the backend population. ACP-connected coding agents, Codex App Server, Cursor SDK, and similar whole-agent runtimes are not merely interchangeable inference endpoints. They may own hidden session state, workspace state, SDK-native tools, MCP execution, subprocesses, or other orchestration behavior. Routing them as if they were ordinary models—especially inside weighted, parallel, thinker, or failover expressions—can move a logical session across independent agent states or duplicate external side effects.

The critical safety distinction is therefore not “local vs remote”, “connector vs builtin”, or “supports tools vs does not support tools”. It is explicit **backend execution semantics**. Go-LIP needs a generation-bound execution class that is declared at the backend factory/export boundary, projected to configured backend instance IDs, and consumed by a core-owned semantic selector validator.

The default behavior shall remain permissive for direct use: an agent runtime is still a valid directly selected backend. The default safety rule instead constrains **composition**. A selector is composition-safe when it is one direct primary, or—if it is weighted, parallel, thinker-aware, or ordered failover—every reachable configured backend is explicitly classified as ordinary inference. Operators who intentionally accept the legacy risk can select an `unrestricted` policy.

## Boundary Context

- **In scope:** explicit backend execution classification; `inference`, `agent_runtime`, and compatibility `unknown` semantics; per-export connector manifest metadata; focused factory-registration metadata; projection from factory kind to configured backend instance ID; generation-bound `safe`/`unrestricted` policy; semantic validation of compiled selectors; direct, weighted, parallel, thinker, and failover behavior; alias-expanded and model-only selectors; configured/default selector validation; A-leg admin route-override preflight; runtime reload behavior; typed errors; bounded diagnostics; official connector classification; regression/architecture tests.
- **Out of scope:** changing selector grammar; changing how inference-only weighted/parallel/failover/thinker routing works; automatically sandboxing/rolling back agent workspaces; transactional tool/MCP execution; inventing a new agent protocol; changing canonical request/event semantics; changing model capability negotiation; changing ordinary B2BUA post-output commitment rules; provider-specific exception lists; per-selector/client bypasses; a generalized idempotency protocol; proving that a specific agent runtime is side-effect-free before output.
- **Primary ownership:** core routing owns the composition safety policy and semantic selector validation; `pkg/lipsdk`/plugin registration and backend-plugin manifest metadata declare execution semantics; runtimebundle/composition projects factory metadata to configured instance IDs; concrete connectors declare their own class but do not implement routing policy.
- **Canonical boundary:** execution class is backend topology/registration metadata. It shall not be added to `pkg/lipapi.Call`, canonical request requirements, canonical event capabilities, or frontend protocol payloads.
- **Streaming boundary:** streaming and output commitment remain unchanged. This feature adds a stricter pre-dispatch guard for agent-runtime composition because “no client-visible output” does not prove “no external side effect”.
- **Compatibility boundary:** direct routes to known backends with missing legacy execution metadata remain usable; safe composition treats such known-but-unclassified backends conservatively. `unrestricted` restores pre-feature composition behavior.
- **Revalidation triggers:** selector semantic validation, runtime route planning, route-override validation, backend registration contracts, executable connector manifest schema/parsing, generation assembly/reload, standard contribution metadata, frontend execution-error mapping, and architecture/TCK gates.

## Requirements

### Requirement 1: Explicit Backend Execution Classification

**Objective:** As a routing maintainer, I want every backend factory/export to declare execution semantics independently of provider identity, so that core can apply stable policy as the backend population grows.

#### Acceptance Criteria

1.1. The system shall support a backend execution class representing at least `inference` and `agent_runtime`.

1.2. When a known backend registration or executable export does not declare an execution class because it predates this feature, the system shall represent its effective class as `unknown` rather than silently assuming `inference`.

1.3. The system shall treat `unknown` as a compatibility state for missing metadata, not as a positive authored substitute for `inference` or `agent_runtime`.

1.4. An executable connector that exports more than one factory kind shall be able to declare a different execution class for each export.

1.5. The system shall not infer execution class from backend/provider names, route prefixes, registration source, local-only access scope, credential mode, process-sharing mode, executable-plugin provenance, model capability flags, or the presence of tool support.

1.6. The direct `openai-codex` inference export and the `openai-codex-app-server` export shall be representable with different execution classes even though they are delivered by the same Codex connector artifact.

1.7. Ordinary local/discovered inference runtimes shall be representable as `inference`, proving that process execution and local-only deployment do not imply `agent_runtime`.

1.8. Invalid non-empty execution-class metadata shall fail closed during registration/manifest validation with a bounded configuration error.

### Requirement 2: Default-Safe Selector Composition Policy

**Objective:** As a proxy operator, I want risky agent-runtime composition blocked by default while direct agent-runtime use remains available, so that normal routing cannot silently combine incompatible execution models.

#### Acceptance Criteria

2.1. The system shall expose a generation-scoped routing execution-composition policy with supported values `safe` and `unrestricted`.

2.2. When the policy is omitted, the system shall use `safe`.

2.3. Under `safe`, when a compiled selector contains exactly one failover alternative and that alternative is one direct primary, the system shall allow the selector regardless of whether that known backend's execution class is `inference`, `agent_runtime`, or compatibility `unknown`, subject to all existing routing/admission rules.

2.4. Under `safe`, when a selector is composed rather than one direct primary, the system shall allow it only when every reachable known backend is explicitly classified `inference`.

2.5. Under `safe`, a weighted selector that can select an `agent_runtime` or compatibility-`unknown` backend shall be rejected before route planning chooses a branch.

2.6. Under `safe`, a parallel selector that can execute an `agent_runtime` or compatibility-`unknown` backend shall be rejected before any parallel leg starts or handicap timer affects execution.

2.7. Under `safe`, a thinker-aware selector shall be rejected if either the thinker side, a normal executor side, or an embedded parallel executor side can reach an `agent_runtime` or compatibility-`unknown` backend.

2.8. Under `safe`, an ordered failover chain shall be rejected if any alternative can reach an `agent_runtime` or compatibility-`unknown` backend, regardless of alternative order.

2.9. Under `safe`, compositions containing only explicitly classified `inference` backends shall retain existing weighted, parallel, thinker, failover, affinity, TTFT, `[first]`, health, exclusion, and capability behavior.

2.10. Global selector parameters such as affinity/TTFT and per-primary query parameters shall not by themselves turn a single direct primary into a composed selector.

2.11. If a selector references a backend identity that is not part of the active generation, execution-composition validation shall not disguise that condition as an execution-class rejection; existing unknown/unresolvable-backend handling shall remain authoritative.

### Requirement 3: Agent-Runtime Failover and Side-Effect Safety

**Objective:** As an operator, I want Go-LIP not to rely on output commitment as a side-effect boundary for agent runtimes, so that a failed hidden agent execution is not transparently repeated elsewhere.

#### Acceptance Criteria

3.1. Under `safe`, ordered failover involving an `agent_runtime` shall be rejected even though normal inference failover is permitted before client-visible output.

3.2. Under `safe`, parallel racing involving an `agent_runtime` shall be rejected even when the proxy could otherwise cancel losing legs before downstream output commitment.

3.3. The system shall not assume that an agent-runtime attempt is side-effect-free merely because no client-visible canonical content event has been emitted.

3.4. This feature shall not weaken the existing rule that no transparent retry or failover occurs after the first client-visible output event.

3.5. This feature shall not modify ordinary inference retry/failover semantics or introduce a second output-commitment definition.

3.6. A future relaxation based on a stronger property such as pre-output side-effect freedom or transactional idempotency shall require a separately reviewed contract and shall not be implied by `agent_runtime`, transport cancellation, or streaming support.

### Requirement 4: Generation-Bound Instance Classification

**Objective:** As a maintainer, I want execution metadata resolved once into the active generation's configured backend identities, so that routing consumes immutable core-facing data rather than plugin registries.

#### Acceptance Criteria

4.1. Backend factories/exports shall declare execution metadata at their existing registration/discovery boundary.

4.2. During generation construction, the system shall project each enabled configured backend row's factory execution class onto that row's configured backend instance ID.

4.3. When multiple configured backend instances use the same factory kind, each instance shall receive that factory/export execution class without requiring duplicate provider-name configuration.

4.4. The active runtime generation shall expose routing with an immutable backend-instance execution-class view sufficient to classify selector leaves without importing concrete plugins or provider SDKs into core.

4.5. The execution-class view shall distinguish “configured backend exists but class metadata is missing/unknown” from “backend identity is not configured in this generation”.

4.6. Runtime configuration reload shall construct a new immutable execution-class view and policy for the candidate generation; publication shall not mutate the view held by an in-flight request.

4.7. A request admitted against a published generation shall not change execution class or composition policy because a later generation is built or published.

4.8. The implementation shall not introduce a process-global mutable execution-class registry, reflection lookup, provider switch in core, or per-request scan of connector manifests.

4.9. If an internal/alternate executor is constructed with configured backends but without an explicit execution-class view, the system shall not interpret that omission as `unrestricted` or as implicit `inference`; configured backends shall be conservatively classifiable as unknown until the composition root/test harness supplies explicit metadata.

### Requirement 5: Pure Semantic Validation Before Routing Side Effects

**Objective:** As a runtime maintainer, I want unsafe selectors rejected at the pure semantic-preflight boundary, so that rejection itself cannot consume model work or mutate dynamic routing state.

#### Acceptance Criteria

5.1. Execution-composition validation shall operate on the compiled selector AST after alias resolution, parsing, and model-only default-backend application.

5.2. Execution-composition validation shall not be implemented by substring/regex inspection of the raw selector.

5.3. The selector parser shall remain responsible for syntax/AST shape and shall not contain backend execution-class/provider classification logic.

5.4. For normal request execution, `safe` validation shall complete before weighted RNG selection, `[first]` session-state consumption, affinity mutation/selection side effects, interleaved thinker-cycle mutation or store access required solely for planning, B-leg allocation, backend/connector `Open`, provider process execution attributable to the request, or billing authorization.

5.5. Rejecting an unsafe selector shall not start a backend, send an upstream request, consume model usage, open an agent run, or launch parallel legs.

5.6. The validator shall be deterministic and side-effect-free for the same selector, execution-class view, and policy.

5.7. The validator shall recursively account for every primary reachable through failover alternatives, weighted branches, parallel branches, and the existing thinker-with-embedded-parallel AST shape.

5.8. Selector affinity/stickiness, health state, `[first]`, or a currently preferred candidate shall not make an otherwise unsafe possible execution graph acceptable.

### Requirement 6: Consistent Validation Across Selector Entry Points

**Objective:** As an operator, I want the same routing safety semantics no matter where a selector originates, so that aliases, defaults, and admin overrides cannot bypass the guard.

#### Acceptance Criteria

6.1. Normal client/request selectors shall use the execution-composition validator before routing execution.

6.2. When a configured/default route is resolvable during generation build/reload, the generation shall validate it with the candidate generation's execution-class view and policy and fail publication/build before request-time backend work if it is unsafe.

6.3. When an alias expands a request to an unsafe composition, the system shall reject the expanded compiled selector even if the raw client string does not visibly contain composition operators.

6.4. The existing A-leg routing-override admin write path shall validate a proposed override with the same generation-bound composition semantics before persisting the mutation.

6.5. If an admin override is rejected as unsafe, the prior override state/revision shall remain unchanged and no backend work shall occur.

6.6. A persisted raw route override shall not be rewritten, cleared, or migrated merely because a later generation changes execution metadata or policy.

6.7. If a persisted selector becomes unsafe under a later published generation, a later turn using that selector shall fail through the new generation's normal semantic-preflight path rather than silently falling back to the client selector, another route, or an older generation.

6.8. Route-override administration disabled at HTTP level shall not create a bypass: already persisted overrides shall still be checked by request-time safe validation.

6.9. Wherever current selector preflight also rejects unknown configured backend identities, unknown-backend validation and execution-composition validation shall use one shared compile/semantic-preflight sequence so admin and runtime behavior cannot drift.

### Requirement 7: Explicit Operator Opt-Out and Legacy Compatibility

**Objective:** As an advanced operator, I want an explicit way to retain today's unrestricted routing semantics, while default behavior protects users who did not opt into the risk.

#### Acceptance Criteria

7.1. Under `unrestricted`, execution-class composition validation shall not reject selectors solely because they contain `agent_runtime` or compatibility-`unknown` backends.

7.2. Under `unrestricted`, weighted, parallel, thinker, and failover behavior shall remain semantically compatible with behavior before this feature, including all existing B2BUA/output-commitment restrictions.

7.3. The `unrestricted` setting shall be operator-owned configuration and shall not be settable by a client request, model string annotation, route alias, frontend header, provider response, or backend plugin at request time.

7.4. Invalid execution-composition policy values shall fail configuration validation rather than falling back to `unrestricted`.

7.5. Existing third-party/legacy backends with missing execution metadata shall remain directly routable under the default `safe` policy.

7.6. Existing third-party/legacy backends with missing execution metadata shall require either explicit `inference` metadata in a supported registration/manifest path or the operator's `unrestricted` policy before participating in composite routing.

7.7. The migration shall not require changing existing client routing strings that directly select one backend.

### Requirement 8: Connector and Registration Metadata Without Canonical or ABI Leakage

**Objective:** As a plugin/connector author, I want to declare execution topology through the proper registration seam, so that routing safety does not pollute canonical model semantics or require unnecessary protocol negotiation.

#### Acceptance Criteria

8.1. The public/backend registration contract shall expose focused execution metadata separately from credential/access security metadata.

8.2. The executable connector closed manifest shall support per-export execution-class metadata and preserve existing manifests that omit the field.

8.3. All project-owned official backend factories/exports shall explicitly declare their execution class so that the standard distribution does not depend on `unknown` for maintained integrations.

8.4. Project-owned whole-agent/ACP/App-Server/SDK runtime exports shall be classified `agent_runtime` according to actual execution semantics, while direct inference exports shall be classified `inference`.

8.5. The implementation shall not classify an entire connector artifact as `agent_runtime` when the artifact exports both inference and agent-runtime factory kinds.

8.6. The implementation shall not add execution class to the canonical `pkg/lipapi` request/event/capability model.

8.7. The first implementation shall not require a new backend-plugin gRPC protocol feature, minor, or major solely to carry execution class; the host shall derive the class from installation/registration metadata before execution.

8.8. Core routing/runtime packages shall not import concrete connector packages, provider SDKs, or a generated list of agent provider names.

8.9. Standard contribution/registration metadata may gain a focused execution facet/profile, but shall not become a runtime service locator or mix execution policy into backend adapter code.

### Requirement 9: Typed Errors and Bounded Explainability

**Objective:** As a client/operator, I want an unsafe composition failure to be actionable and deterministic without leaking route or workspace details.

#### Acceptance Criteria

9.1. When `safe` rejects a selector, core shall return a typed error distinguishable from selector syntax errors, missing backends, capability negotiation failures, and transport failures.

9.2. The typed failure shall identify bounded semantic facts sufficient to explain the rejection, including the composition category and offending configured backend identity/class where available.

9.3. The failure shall not require or expose the full raw selector, prompt content, tool arguments, MCP configuration, workspace path, credentials, agent IDs, or hidden runtime state.

9.4. Standard HTTP frontends shall map an unsafe-composition request failure to their existing invalid-request/client-error family; OpenAI-compatible HTTP behavior shall use a 400-class invalid request rather than a retryable upstream/server failure.

9.5. The error shall state that direct agent-runtime routing remains supported and that unrestricted behavior requires operator configuration, without claiming that a particular provider is broken.

9.6. Logs/traces/metrics may record bounded policy/class/composition outcome fields but shall not create high-cardinality labels from raw selectors or provider-private identifiers.

9.7. Rejection shall be observable as a pre-dispatch routing-policy outcome and shall not be recorded as an upstream backend health failure.

### Requirement 10: TDD, Architecture Guards, and Regression Proof

**Objective:** As a maintainer, I want executable contracts to prevent both safety regressions and over-broad classification as new connectors are added.

#### Acceptance Criteria

10.1. Implementation shall follow RED -> GREEN -> REFACTOR, with pure validator/config/metadata contracts written before production routing changes.

10.2. Table-driven routing tests shall cover direct `inference`, direct `agent_runtime`, direct `unknown`, inference-only weighted/parallel/failover/thinker, agent-runtime weighted/parallel/failover/thinker, agent-runtime-to-agent-runtime composition, unknown composite routing, and `unrestricted` legacy behavior.

10.3. Tests shall cover the existing thinker hybrid with an embedded parallel executor group so nested agent-runtime leaves cannot bypass validation.

10.4. Tests shall prove alias expansion and model-only defaulting occur before class validation.

10.5. Tests shall prove classification follows configured backend instance IDs even when the instance ID differs from the factory kind and when multiple instances share a factory.

10.6. Connector/manifest tests shall prove one Codex artifact can classify `openai-codex` as inference and `openai-codex-app-server` as agent runtime.

10.7. Tests shall prove at least one local/discovered ordinary inference connector remains safely composable, preventing future heuristics based on local-only/process/discovered status.

10.8. Runtime tests shall use counters/barriers/fakes to prove unsafe rejection performs zero backend opens/upstream attempts and occurs before mutable weighted/affinity/interleaved routing state changes.

10.9. Route-override tests shall prove an unsafe PUT fails before store mutation and that a policy/classification change on reload affects later turns without mutating an in-flight turn.

10.10. Architecture tests shall fail if core execution-class policy begins depending on concrete connector/provider names or provider SDK packages.

10.11. Manifest/config tests shall cover omitted execution class, invalid class, explicit official classes, omitted policy defaulting to `safe`, invalid policy, and `unrestricted`.

10.12. Existing inference-only routing/parser/planner suites shall remain green without changing selector syntax or expected inference routing behavior.

10.13. The implementation shall run focused routing/runtime/manifest/plugin-registry/route-override/architecture tests plus the repository's normal quality gates appropriate to a routing and plugin-contract change.

10.14. Test-only executor builders that intend ordinary fake inference backends shall set that execution metadata explicitly (or provide a dedicated test helper), so unrelated legacy routing tests remain readable without adding a production fail-open default.
