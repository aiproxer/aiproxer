# Requirements Document

## Introduction

Go-LIP now has a materially stronger architecture than the original implementation: canonical frontend/backend translation, explicit capability negotiation, immutable runtime generations, executable backend connectors, unified ordinary frontend plumbing, and architecture tests that enforce important dependency directions. The OpenResponses `2026-04-24` implementation nevertheless exposed a remaining scalability problem. PR #250 changed 602 files because the work combined a genuinely new wire protocol with canonical-model expansion, continuation, WebSocket transport, backend-plugin ABI changes, composition metadata, diagnostics, independent emulators, generated compliance artifacts, and an exhaustive 5×9 frontend×backend compatibility proof.

The raw 602-file count overstates production coupling because many files were generated compliance schemas, fixtures, independent reference implementations, and tests. The architectural concern remains valid: Go-LIP currently treats complete frontend×backend Cartesian coverage as authoritative conformance evidence and repeats extension metadata across several composition, diagnostics, route, compatibility, and test registries.

That model cannot remain authoritative when the project is expected to support hundreds or thousands of inference providers and aggregators. Most providers should not require a unique Go backend implementation: providers exposing an established compatible protocol should normally be represented as validated profiles of a small number of protocol-family adapters, while genuinely incompatible semantics, authentication, transport, or lifecycle behavior remain dedicated built-ins or executable connectors.

This specification changes the extension-cost model from approximately `frontends × backends × feature evidence` to:

`frontend contract certification + canonical-core contract certification + backend-family/connector contract certification + provider-profile certification + a bounded end-to-end integration sentinel`.

The objective is not a directory-taxonomy rewrite. It is a focused simplification of extension seams that reduces shotgun edits, removes confirmed mirrors, keeps provider growth out of core, and makes future protocol/provider additions primarily additive.

## Boundary Context

- **In scope:** frontend/backend contract test kits (TCKs); canonical-core contract suites; retirement of Cartesian completeness as a release invariant; a bounded integration sentinel; provider-family/provider-profile architecture for compatible inference providers; single-source frontend/backend contribution metadata; generic route-claim and diagnostics projections; capability-driven certification; backend-plugin ABI evolution policy and generic semantic carriers; canonical-contract promotion rules; targeted removal of adapter-only canonical leakage where justified; continuation mirror elimination; architecture/import/change-surface gates; conformance migration and deletion of obsolete matrix-specific evidence code; author documentation and release evidence.
- **Out of scope:** adding specific new commercial inference providers; changing existing client-facing protocol behavior; changing selector syntax, routing algorithms, accounting semantics, authority semantics, B2BUA continuity semantics, secure-session semantics, retry/failover policy, or no-retry-after-output behavior; replacing the canonical executor; replacing gRPC executable connectors; loading arbitrary runtime code or Go native plugins; introducing a DI container, reflection registry, service locator, generic workflow engine, or arbitrary user-supplied request/response transformation language.
- **Adjacent work:** the current OpenResponses implementation remains authoritative for OpenResponses wire semantics. Existing connector, runtime-convergence, reasoning-preservation, generic-compatible-backend, and ACP-deduplication work remains authoritative for its owned behavior. Release-manifest fragmentation proposed elsewhere is not duplicated by this specification.
- **Primary ownership:** `pkg/lipapi` remains the protocol-neutral canonical contract; `internal/core` remains policy/orchestration; frontend and backend adapters remain wire/provider edges; `internal/standardplugins` / `internal/pluginreg` remain explicit standard-distribution composition; executable connectors remain under `connectors/` behind `pkg/lipsdk/backendplugin`.
- **Scale assumption:** the backend/provider population is expected to grow by orders of magnitude. Architecture and release gates shall therefore be evaluated against synthetic large registries, including at least 1,000 provider profiles, rather than only the current dozen-or-so backend identities.
- **Revalidation triggers:** canonical `lipapi` request/event changes; backend-plugin ABI changes; frontend/backend registration contract changes; provider-profile schema changes; capability/dialect admission changes; route-claim ownership changes; diagnostics schema changes; TCK scenario-corpus changes; continuation ownership changes; or any reintroduction of full frontend×backend completeness as a mandatory release gate.

## Requirements

### Requirement 1: Preserve Product Behavior and Existing Safety Boundaries

**Objective:** As an operator and maintainer, I want extension-architecture simplification to preserve existing protocol, routing, security, and lifecycle behavior, so that maintainability improves without product regressions.

#### Acceptance Criteria

1.1. When an existing frontend decodes a request, the system shall preserve its current legal wire behavior, canonical call semantics, authentication ordering, request limits, route selection, and error mapping unless an independently approved protocol specification changes them.

1.2. When an existing backend or connector receives a representable canonical call, the system shall preserve request translation, transport behavior, event ordering, usage/cost semantics, cancellation, and terminal ownership.

1.3. If a backend candidate cannot represent required canonical semantics, the system shall continue to reject that candidate before upstream network or process work rather than silently downgrade hard requirements.

1.4. After the first client-visible output event, the system shall continue to prohibit transparent retry, failover, backend migration, or generation migration.

1.5. While runtime reload, generation pinning, connector lifecycle, or process shutdown occurs, the system shall preserve current ownership, last-good publication, bounded retention, and exactly-once cleanup guarantees.

1.6. The system shall preserve provider SDK isolation: `internal/core`, `pkg/lipapi`, and generic composition/conformance contracts shall not import provider SDKs or concrete provider adapters.

1.7. The system shall preserve streaming as the primary execution model; non-streaming frontends shall remain collectors over canonical streams rather than a separate backend execution path.

1.8. The system shall preserve explicit construction and registration and shall not introduce reflection registration, `init()` registration, a DI container, global mutable plugin registries, or Go native `plugin` loading.

1.9. If a public `pkg/lipapi` or `pkg/lipsdk` source-compatible contract must change, the implementation shall document the compatibility impact and provide an additive or deterministic migration path where practical.

1.10. Existing independent protocol refclients/refbackends and official compliance suites shall remain authoritative for their owned wire protocols and shall not be replaced by self-referential production-code tests.

### Requirement 2: Make Extension Cost Additive Rather Than Cartesian

**Objective:** As the project owner, I want frontend/backend/provider growth to have bounded additive integration cost, so that thousands of inference providers do not make implementation or CI complexity explode.

#### Acceptance Criteria

2.1. The mandatory conformance architecture shall not construct, require, or release-gate the complete Cartesian product of all registered frontends and all registered backends/providers.

2.2. Adding a provider profile to an already supported compatible protocol family shall not require changes to `pkg/lipapi`, `internal/core/runtime`, `internal/core/routing`, any frontend package, `api/backendplugin/v1/backend.proto`, or a global frontend×backend compatibility table.

2.3. Adding a provider profile to an existing family shall require at most the provider-profile data, profile-specific fixtures/tests where needed, and documentation/inventory updates generated or derived from the profile source of truth.

2.4. Adding a genuinely new backend family shall require certification against one backend contract suite rather than one new compatibility cell per frontend.

2.5. Adding a genuinely new frontend shall require certification against one frontend contract suite rather than one new compatibility cell per backend/provider.

2.6. A synthetic registry containing at least 5 frontends and 1,000 provider profiles shall not cause the mandatory end-to-end integration sentinel to generate 5,000 pairs or any equivalent `O(F×B)` work.

2.7. The required number of adapter-level contract executions shall scale with the number of adapters/profiles being certified, and the required end-to-end sentinel shall remain explicitly bounded and independent of provider count within an existing family.

2.8. No architecture or release gate shall require manual feature classification for every frontend×backend pair.

2.9. If cross-product testing is retained as an optional diagnostic or nightly sampling tool, it shall be non-authoritative, bounded/sampled, and shall not block adding an otherwise certified provider profile.

2.10. Architecture tests shall fail if a new authoritative conformance list or loop reintroduces mandatory complete frontend×backend Cartesian enumeration.

### Requirement 3: Establish a Capability-Driven Backend Contract Test Kit

**Objective:** As a backend or connector author, I want one reusable semantic contract suite, so that a backend proves canonical compatibility once regardless of how many frontends exist.

#### Acceptance Criteria

3.1. The repository shall provide one canonical backend TCK scenario corpus covering baseline text generation, streaming, tools, multimodal inputs, reasoning/replay, structured output, ordered items, item references, compaction, extensions, usage, errors, cancellation, and lifecycle where those semantics are declared.

3.2. When a backend declares a capability or exact dialect/extension support, the TCK shall automatically select the corresponding positive scenarios without requiring a frontend-specific test registration.

3.3. When a backend does not declare a hard capability required by a scenario, the TCK shall prove deterministic rejection before upstream work rather than treating the scenario as a silent skip.

3.4. The backend TCK shall provide an upstream probe/counter that can prove zero upstream requests or process execution for rejected semantic requirements.

3.5. The backend TCK shall verify canonical event ordering, bounded event envelopes, terminal ownership, error typing, cancellation propagation, and no backend-local automatic retry that conflicts with core policy.

3.6. The backend TCK shall verify that declared capability, transport, reasoning-replay, dialect, extension, and model-aware profile metadata are truthful relative to executable behavior.

3.7. Essential built-in backends, compatible-family adapters, and executable connector-hosted backends shall consume the same semantic scenario corpus, with transport-specific harness adapters rather than copied scenario definitions.

3.8. The executable-backend SDK shall expose a supported contract-test entry point or package that third-party connector authors can run without importing concrete root-module backend implementations.

3.9. A backend certification result shall identify the backend/family, effective capabilities/dialects, scenario IDs executed, negative scenarios executed, and failures in a machine-readable form suitable for CI evidence.

3.10. The TCK shall use real canonical `lipapi` types and shall not mock internal runtime call graphs.

### Requirement 4: Establish a Frontend Contract Test Kit

**Objective:** As a frontend author, I want to certify wire↔canonical behavior independently of backend population, so that frontend correctness does not depend on provider count.

#### Acceptance Criteria

4.1. The repository shall provide one frontend TCK that captures canonical calls through a fake/narrow executor seam and supplies canonical event streams back to the frontend under test.

4.2. For every frontend-declared feature, the TCK shall verify wire request decoding into the expected canonical call, including required capabilities/dialects/extensions.

4.3. For every frontend-declared output feature, the TCK shall verify legal wire encoding from canonical events, including streaming and non-streaming behavior where supported.

4.4. The frontend TCK shall cover authentication-before-sensitive-work ordering, body/decode limits, cancellation, protocol-legal errors, output commitment, and route ownership where applicable.

4.5. Stateful frontend-specific behavior such as OpenResponses continuation and WebSocket session semantics shall remain in protocol-owned suites and shall compose with, rather than fork, the common frontend TCK.

4.6. A frontend TCK shall not construct real provider backends and shall not import provider SDKs or backend implementation packages.

4.7. A new frontend shall register its contract capabilities and protocol-owned fixtures once; the test framework shall not require backend-specific row files.

4.8. Independent refclients or official client fixtures shall remain usable as black-box validation against the mounted frontend without becoming dependencies of production codecs.

4.9. Frontend certification evidence shall identify protocol/profile, declared features, scenario IDs, transport variants, and failures in a machine-readable form.

4.10. Frontend TCK scenarios shall use the same canonical semantic identifiers as the backend/core TCK so that coverage can be reasoned about by feature rather than by frontend×backend pair.

### Requirement 5: Certify the Canonical Core Independently and Retain Only a Bounded Integration Sentinel

**Objective:** As a core maintainer, I want routing/projection/execution semantics proven independently from wire adapters, while retaining a small set of real end-to-end paths to catch composition errors.

#### Acceptance Criteria

5.1. A canonical-core contract suite shall directly test requirement derivation, capability/dialect matching, projection feasibility, failover requirement freezing, output commitment, and terminal stream semantics without mounting concrete frontends or provider SDKs.

5.2. The core suite shall prove both item-authority→legacy-view and legacy-view→ordered-item projections for their documented portable intersections and stable rejection reasons.

5.3. The core suite shall prove that transformed/failover attempts cannot weaken the frozen semantic requirement set.

5.4. The core suite shall prove that routing excludes incompatible candidates before upstream work and preserves existing sticky-affinity cleanup behavior on admission rejection.

5.5. The repository shall retain a small explicit end-to-end sentinel set that exercises real frontend→core→backend composition across representative implementation classes.

5.6. The sentinel set shall contain representative built-in hosted, compatible-family, and executable-connector paths where those classes exist, but shall not contain one entry per provider profile.

5.7. Adding another provider profile within an already represented family shall not change the sentinel pair count.

5.8. Adding a new implementation class or protocol family may add a sentinel pair only with an explicit rationale describing the composition boundary it protects.

5.9. Sentinel selection shall be deterministic and bounded by checked-in policy; it shall not derive the full Cartesian product from registry contents.

5.10. The existing 5×9 OpenResponses matrix may remain temporarily as migration evidence, but it shall cease to be an authoritative release-completeness requirement after TCK equivalence is proven.

### Requirement 6: Represent Compatible Providers as Typed Provider Profiles of Protocol Families

**Objective:** As a provider integrator, I want most OpenAI/OpenResponses/Anthropic-compatible services to be declarative profiles of a proven family adapter, so that provider count can grow without proportional Go-code duplication.

#### Acceptance Criteria

6.1. The system shall distinguish a **backend family implementation** from a **provider profile** and from a **dedicated executable connector**.

6.2. A provider exposing an existing compatible family without materially unique semantics shall be represented by a provider profile rather than a new provider-specific Go backend package.

6.3. Provider profiles shall use a versioned, typed, bounded schema that can express only approved family-level configuration such as endpoint identity/path policy, authentication mode/environment names, safe static headers, model discovery policy, model namespace handling, tokenizer/accounting selection, capability overrides, dialect/extension declarations, and approved family quirks.

6.4. Provider profiles shall not embed executable code, templates capable of arbitrary request mutation, scripts, expressions, regex-driven rewrite programs, or arbitrary response transformation logic.

6.5. If a provider requires semantics, authentication handshakes, transport behavior, local process lifecycle, or response interpretation that cannot be represented safely by an existing family profile schema, the integration shall graduate to a dedicated family adapter or executable connector.

6.6. Project-shipped provider profiles shall be loadable from one profile catalog/source of truth and may be embedded into the standard binary without adding one Go registration statement per provider.

6.7. Each profile shall identify its family, security posture, capabilities/dialects, model-discovery policy, and provenance sufficiently for startup validation and diagnostics.

6.8. Profile validation shall fail closed on unknown schema versions, unknown family quirks, unsafe endpoint/auth/header combinations, inconsistent capability declarations, or unsupported family options.

6.9. Runtime reload shall treat provider-profile changes through the existing generation/candidate compilation model and shall not introduce a second mutable provider registry.

6.10. The provider-profile certification suite shall test schema validation, family binding, endpoint/auth/header/model behavior, effective capabilities, and any declared quirk without multiplying by frontend count.

6.11. A synthetic catalog containing at least 1,000 valid profiles shall load and validate through bounded deterministic work with no per-profile goroutine, process, or provider-network call during configuration validation.

6.12. Custom user-defined compatible backend instances shall remain supported; project-shipped profiles shall be a convenience/scalability layer, not a restriction that forces every endpoint into the built-in catalog.

### Requirement 7: Single-Source Frontend and Backend Contribution Metadata

**Objective:** As a composition maintainer, I want each extension to describe its contribution once, so that registration, diagnostics, route ownership, conformance, and security metadata cannot drift across parallel tables.

#### Acceptance Criteria

7.1. The standard distribution shall have one authoritative contribution descriptor per built-in frontend and one per built-in backend family.

7.2. A frontend contribution shall compose focused fields/providers for identity, mount/factory, route claims, diagnostics projection, security/auth posture, and frontend TCK declaration without becoming a generic service-locator bag.

7.3. A backend contribution shall compose focused fields/providers for identity, factory/lifecycle factory, registration source, security posture, compatible-family/profile binding where applicable, diagnostics projection, and backend TCK declaration.

7.4. `StandardBundle`, essential-kind discovery, compatible-family discovery, route-claim registration, diagnostics inventory, and conformance registration shall be derived from contribution sources rather than maintained as independent authoritative lists/switches.

7.5. No concrete provider profile shall require a new entry in `EssentialBackendKinds`, `CompatibleBackendKinds`, a frontend/backend matrix list, or a protocol-specific diagnostics switch.

7.6. Contribution descriptors shall remain explicit Go values or generated values from typed profile data; they shall not use reflection, package `init()`, runtime code loading, or unordered global mutation.

7.7. Architecture tests shall prove uniqueness of IDs, absence of duplicate route ownership, deterministic contribution ordering, and one registration source per extension.

7.8. A contribution shall expose only metadata required by composition; runtime policy and provider wire behavior shall remain in their current owning packages.

7.9. Tests shall prove that adding a synthetic contribution automatically appears in all derived registries relevant to its declared facets and nowhere else.

7.10. The implementation shall delete parallel lists and protocol-specific registration switches superseded by contribution-derived data.

### Requirement 8: Make Route Ownership and Diagnostics Protocol-Extensible

**Objective:** As an adapter maintainer, I want generic route and operator projection contracts, so that a new protocol does not require edits to central enums or protocol-named core diagnostic DTOs.

#### Acceptance Criteria

8.1. `RouteClaim` shall continue to use normalized owner/method/path validation, but the route kind/operation identifier shall be an opaque validated extension-owned identifier rather than a centrally closed list requiring edits for every frontend protocol.

8.2. Adding a frontend route kind shall not require adding a constant to a central standard-HTTP enumeration.

8.3. Common frontend/backend diagnostic fields shall use one bounded generic instance projection containing identity, origin/source, enabled state, profile/family, capabilities, route claims, inventory/conformance state, and configuration health where applicable.

8.4. Protocol-specific diagnostic details shall be emitted only through a bounded sanitized extension field/list owned by the adapter contribution and shall not require a new core diagnostic row type for each protocol.

8.5. Generic diagnostics shall never contain raw credentials, authorization headers, secret environment values, raw opaque payloads, unbounded YAML/JSON configuration, DSNs, or prohibited full local paths.

8.6. Existing diagnostics endpoints shall preserve equivalent operator-visible information for current frontends/backends after migration.

8.7. Route and diagnostics projection shall be side-effect free and shall not make provider network calls or activate provider processes.

8.8. Architecture tests shall fail when composition code switches on concrete protocol IDs solely to select route/diagnostic metadata that could be supplied by a contribution.

### Requirement 9: Keep Canonical Contracts Semantic and Use Bounded Dialect Carriers for Wire Fidelity

**Objective:** As a canonical-model maintainer, I want `pkg/lipapi` to represent shared proxy semantics rather than become a universal copy of every provider's schema, so that new protocols do not repeatedly expand the core public DTO surface.

#### Acceptance Criteria

9.1. A new field/type shall be promoted into `pkg/lipapi` only when core policy/orchestration consumes the semantic meaning or when the meaning is genuinely shared across multiple protocol families.

9.2. Exact adapter-to-adapter wire fidelity that core does not interpret shall preferentially use existing or improved bounded dialect/extension carriers with explicit namespace/type/implementor requirements.

9.3. Dialect/extension carriers shall preserve exact required presence/data only within explicit byte/depth/count limits and shall participate in capability/dialect admission.

9.4. Opaque carriers shall never permit raw request/response tunneling around canonical routing, safety, hooks, accounting, output commitment, or requirement matching.

9.5. Unknown or unsupported dialect/extension data shall fail closed or remain adapter-private according to an explicit contract; it shall never be silently dropped when required.

9.6. The implementation shall audit current protocol-named canonical fields added for OpenResponses/Codex fidelity, including `PromptCacheKey` and exact reasoning/compaction carriers, and shall classify each as shared semantic state, core-required state, or adapter-only wire fidelity.

9.7. Any audited field classified as adapter-only wire fidelity and safely representable by a bounded existing/new generic carrier shall migrate off the first-class canonical envelope with characterization tests and source-compatibility handling; fields with genuine shared/core semantics shall remain.

9.8. `pkg/lipapi` shall continue to import no protocol codec packages and no provider SDKs.

9.9. Projection utilities shall remain canonical semantic projectors, not frontend↔backend pair translators.

9.10. Architecture documentation shall record the promotion test and examples so future protocol SDDs must justify new first-class canonical fields.

### Requirement 10: Evolve the Executable Backend-Plugin ABI by Semantic Capability, Not Protocol Name

**Objective:** As a connector ecosystem maintainer, I want backend-plugin ABI evolution to be stable across provider/protocol proliferation, so that a new protocol does not force ecosystem-wide DTO churn.

#### Acceptance Criteria

10.1. Existing backend-plugin protocol v1.0–v1.3 negotiation and connectors that do not use newer fields shall remain wire-compatible.

10.2. The existing `exact_openresponses_fields` v1.3 feature shall remain accepted as a compatibility feature for deployed/current connectors until an explicit major-version migration is independently approved.

10.3. New minor-version feature names introduced by this architecture work shall describe protocol-neutral semantic carriers or transport capabilities and shall not embed a concrete provider/protocol product name.

10.4. The ABI shall provide or complete bounded generic carriers sufficient for canonical opaque extensions, exact presence-bearing structured payloads, ordered items, reasoning/compaction dialect material, and future semantic extension records without requiring one proto field per provider wire field.

10.5. Generic ABI carriers shall preserve namespace/type/implementor/direction or equivalent identity and shall be validated against negotiated canonical requirements before transmission.

10.6. A provider profile within an existing compatible family shall require no backend-plugin protocol version increment.

10.7. A new backend family that uses only existing canonical semantics shall require no backend-plugin protocol version increment.

10.8. If a future canonical semantic cannot be represented by the current ABI, a minor increment shall be justified by the new canonical semantic rather than by the provider wire field that exposed it.

10.9. Host and connector contract tests shall prove unknown/unsupported semantic carriers fail deterministically without silent field loss.

10.10. The public connector TCK shall verify feature negotiation and round-trip preservation across every negotiated semantic carrier used by a connector.

10.11. Architecture tests shall reject new protocol-named backend-plugin feature constants or protocol-specific proto fields unless an explicit allowlisted legacy compatibility exception is documented.

### Requirement 11: Remove Confirmed Continuation Mirrors and Enforce Single Ownership

**Objective:** As a maintainer, I want one implementation authority for continuation algorithms, so that the architecture does not recreate mirror drift while simplifying other extension paths.

#### Acceptance Criteria

11.1. The production tree shall contain one authoritative bounded in-memory continuation store implementation rather than separate `pkg/lipsdk/continuation` and `internal/core/continuation` copies.

11.2. The production tree shall contain one authoritative continuation stream-recorder implementation rather than separate copies with equivalent state and terminal-write logic.

11.3. `pkg/lipsdk/continuation` shall remain the protocol-neutral public contract/utility boundary for continuation types, policies, store/recorder interfaces, clone/materialization helpers, and any intentionally supported reusable implementation retained there.

11.4. `internal/core/continuation` shall contain only core/application orchestration not already owned by the SDK contract/utility layer and shall not fork SDK algorithms.

11.5. Durable filesystem/database continuation stores shall remain infrastructure adapters behind the continuation store contract.

11.6. Existing continuation security properties—scoped lookup, indistinguishable not-found behavior, chain-depth/size limits, proxy-owned IDs, terminal-only persistence, cleanup ownership, and cancellation—shall remain unchanged.

11.7. Characterization tests shall run against the selected single implementation before duplicate code is deleted.

11.8. Architecture tests shall detect reintroduced production mirrors of the selected continuation store/recorder authority.

11.9. Public compatibility wrappers may be retained only when they delegate to one implementation and do not carry independent mutable state machines.

### Requirement 12: Retire Cartesian Evidence and Delete Obsolete Extension Scaffolding

**Objective:** As a repository maintainer, I want the new TCK architecture to replace rather than coexist indefinitely with the current matrix bureaucracy, so that the codebase becomes smaller and easier to understand.

#### Acceptance Criteria

12.1. During migration, the current 45-cell OpenResponses matrix and new TCKs shall run in parallel long enough to demonstrate equivalent coverage of the canonical feature obligations currently treated as release-critical.

12.2. Before Cartesian completeness is removed, a traceability report shall map every currently required feature class to frontend TCK, core TCK, backend TCK, provider-profile certification, protocol-specific suite, or bounded sentinel evidence.

12.3. After equivalence is proven, the authoritative `BundledFrontendIDs × BundledBackendIDs` completeness invariant shall be removed from release gating.

12.4. OpenResponses-specific row/column evidence registries, 45-cell feature-classification scaffolding, and general-cell scenario metadata that exist only to satisfy Cartesian completeness shall be deleted or reduced to reusable TCK fixtures.

12.5. Independent refclient/refbackend emulators, protocol fixtures, official compliance suites, and protocol-specific state-machine tests shall remain where they provide independent evidence and shall not be deleted merely to reduce line count.

12.6. The implementation shall establish a checked-in baseline inventory of Cartesian-only files/lines and shall remove at least 80% of that legacy non-generated Go surface by completion unless a maintainer-approved exception identifies concrete reusable code retained under the TCK architecture.

12.7. The total non-generated Go lines across the selected shared conformance/composition/continuation affected surfaces shall not increase relative to the implementation baseline after the migration is complete.

12.8. A synthetic scale test shall prove that adding 1,000 provider profiles does not create 1,000×frontend scenario/evidence objects.

12.9. Current mandatory correctness, race, fuzz, architecture, protocol-compliance, and security evidence shall remain available through focused suites even after the matrix is retired.

12.10. The final release report shall record test-count and wall-clock comparisons as evidence; wall-clock timing shall be advisory, while structural non-Cartesian complexity and executable correctness remain the deterministic gates.

### Requirement 13: Add Architecture Ratchets and an Extension Change-Surface Report

**Objective:** As a reviewer, I want extension costs and boundary violations visible before merge, so that future features do not silently rebuild parallel registries or widen shared contracts unnecessarily.

#### Acceptance Criteria

13.1. Architecture tests shall enforce the expected dependency direction among canonical core, frontend adapters, backend adapters, provider profiles, contribution metadata, test/reference packages, and executable connectors.

13.2. Architecture tests shall prohibit pairwise protocol translator packages and protocol-specific branches in core routing/execution that bypass canonical capabilities/projectors.

13.3. Architecture tests shall prohibit authoritative full Cartesian conformance construction and duplicate contribution registries.

13.4. Architecture tests shall prohibit provider-profile imports from `internal/core` and provider SDK imports from provider-profile/conformance-generic packages.

13.5. The repository shall provide an extension change-surface report that classifies a diff into extension-owned files, shared composition metadata, canonical contracts, core runtime/routing, backend-plugin ABI, generated artifacts, and test/reference evidence.

13.6. The change-surface report shall not reject a feature merely because it touches many generated/test files; it shall separately flag high-cost shared-boundary changes such as `pkg/lipapi`, core runtime/routing, ABI proto, or multiple central registries.

13.7. Provider-profile-only changes shall have a strict expected shared-boundary footprint of zero; CI/architecture fixtures shall prove that profile registration is derived without central Go-table edits.

13.8. New protocol/backend-family SDDs shall explicitly justify any first-class canonical contract or ABI expansion identified by the change-surface report.

13.9. Author documentation shall define when to add a provider profile, extend an existing family adapter, create a new backend family, or create an executable connector.

13.10. Author documentation shall define the TCK certification workflow and the limited purpose of the end-to-end sentinel.

13.11. Final implementation evidence shall include baseline SHA, deleted legacy surfaces, certification coverage, synthetic 1,000-profile scale result, architecture report, and exact commands executed.

13.12. Implementation shall begin only after requirements, design, and tasks receive maintainer approval and `ready_for_implementation` is set to `true`.
