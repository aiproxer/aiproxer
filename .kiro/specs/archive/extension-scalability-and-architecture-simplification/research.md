# Architecture Research

## Research Question

How should Go-LIP change its architecture so that supporting hundreds or thousands of inference providers, additional protocol frontends, and future connector families reduces rather than multiplies maintenance cost, while preserving the project's existing canonical-middle, streaming-first, explicit-composition, fail-closed, and executable-connector architecture?

## Baseline

Repository baseline: `main` at `95089eb4b74d5cf8d062f238a1121124ce0da878`.

Relevant completed architecture work already provides:

- canonical frontend/backend translation through `pkg/lipapi`;
- explicit item/message projectors;
- semantic/dialect admission;
- immutable runtime generations and one stable host;
- executable backend connectors over versioned gRPC;
- explicit plugin registration;
- ordinary frontend pipeline reuse;
- architecture import/ownership tests;
- persistence and ACP mirror reduction.

The OpenResponses implementation therefore should not be interpreted as evidence that the whole repository is a monolith. It is evidence that **extension metadata, conformance proof, and some public/ABI fidelity contracts still amplify changes across otherwise clean layers**.

## OpenResponses Change-Surface Interpretation

PR #250 reported:

- 602 changed files;
- 93,200 additions;
- 941 deletions.

A large portion of that change was intentionally independent verification material:

- generated official compliance schemas;
- pinned protocol source fixtures;
- independent OpenResponses refclient/refbackend implementations;
- frontend/backend protocol tests;
- architecture tests;
- 45-cell conformance evidence;
- workflows and coverage tooling.

The high file count is therefore not an appropriate standalone architecture metric.

The more useful architectural signal is that one protocol addition required changes in:

- canonical request/event contracts;
- capability/dialect negotiation;
- runtime candidate admission;
- backend-plugin ABI;
- route ownership;
- diagnostics;
- standard compatible-backend registration;
- continuation storage/recording;
- conformance registries;
- connector-host evidence.

The target architecture should make **generated and protocol-owned evidence cheap to add**, while making **shared-boundary edits rare and explicitly justified**.

## Scale Model

Let:

- `F` = frontend protocol implementations;
- `B` = backend/provider identities;
- `D` = distinct backend protocol/behavior families;
- `P` = compatible provider profiles;
- `S_f` = frontend TCK scenarios;
- `S_b` = backend TCK scenarios;
- `K` = bounded end-to-end sentinel size.

The current complete-matrix model has a minimum pair count proportional to:

`F × B`

and feature-level evidence can grow toward:

`F × B × features`.

The proposed model has mandatory proof proportional to:

`F × S_f + D × S_b + P × profile_checks + connector_certifications + K`.

For providers that differ only by endpoint/auth/catalog details inside one compatible family, `P` grows while `D` stays constant.

The crucial invariant is not a theoretical Big-O label alone. The implementation must prove with a synthetic 1,000-profile catalog that no full frontend×provider pair collection or evidence registry is constructed.

## Existing Conformance Architecture

### Strengths

The current `internal/testkit/conformance` framework has several valuable properties:

- real mounted frontends;
- real canonical executor paths;
- real built-in backend adapters;
- actual executable connector-host paths for optional connectors;
- positive and negative feature evidence;
- no hidden unsupported-semantic downgrade;
- strong traceability to scenario IDs.

These assets should be harvested into the TCK architecture rather than discarded.

### Weakness

`BundledFrontendIDs()` and `BundledBackendIDs()` are treated as authoritative complete lists and `AllCells()` builds their Cartesian product.

OpenResponses then adds:

- frontend-row evidence;
- backend-column evidence;
- general 32-cell evidence;
- feature classification for all 45 cells;
- scenario-ID linkage for every cell.

This is excellent migration evidence for a canonical-model expansion, but it is not a viable permanent provider certification model.

## Contract Certification Model

### Frontend certification

A frontend adapter is correct if it proves:

1. legal wire input becomes the expected canonical `lipapi.Call`;
2. invalid/unsupported wire input fails with legal protocol errors;
3. canonical event streams become legal wire output;
4. auth, body/decode limits, cancellation, output commitment, and route ownership follow the frontend contract.

A backend is irrelevant to those properties.

The frontend TCK should inject:

- a capturing executor that records the canonical call;
- scripted canonical event streams;
- fake clocks/IDs where required;
- protocol-owned fixtures/refclients.

Stateful protocol behavior remains protocol-owned. The common TCK must not force OpenResponses WebSocket/continuation semantics onto ordinary frontends.

### Core certification

The core is correct if it proves:

- canonical requirement derivation;
- projection behavior;
- candidate capability/dialect admission;
- frozen failover requirements;
- no retry after commitment;
- cancellation/terminal semantics.

No frontend or provider SDK is required.

### Backend certification

A backend/family is correct if it proves:

1. declared canonical semantics are representable and correctly mapped;
2. required unsupported semantics fail before upstream work;
3. upstream responses map to legal canonical events;
4. cancellation/errors/usage/lifecycle are correct;
5. declared capability/dialect metadata is truthful.

The backend TCK selects scenarios from the backend's own effective capability declaration.

This gives an important inversion:

**capabilities determine tests; tests do not manually encode every frontend/backend pair.**

## Provider Family vs Provider Profile

### Why this distinction matters

Thousands of provider names do not imply thousands of unique protocol implementations.

Many providers expose:

- OpenAI Chat-compatible APIs;
- OpenAI Responses-compatible APIs;
- OpenResponses-compatible APIs;
- Anthropic-compatible APIs;
- a small number of native APIs.

A provider-specific Go package should be exceptional, not the default.

### Provider profile

A profile is declarative metadata applied to an already certified family adapter.

Candidate profile fields:

```yaml
api_version: lip.provider-profile/v1
id: example-provider
family: openai-responses-compatible

endpoint:
  base_url: https://api.example.com
  path_policy: family_default

auth:
  mode: bearer_env
  env_names:
    - EXAMPLE_API_KEY

headers:
  static:
    X-Provider-Client: go-lip

models:
  discovery: family_default
  namespace:
    strip_prefix: false

capabilities:
  enable: []
  disable: []

dialects:
  reasoning: []
  items: []
  compaction: []
  extensions: []

tokenizer:
  id: ""

quirks: []
```

The exact schema should reuse existing config/security primitives rather than duplicate them.

### Closed quirk vocabulary

Profiles must not become an embedded proxy-programming language.

A quirk is allowed only if:

- it is a closed enum understood by the family adapter;
- it is deterministic;
- it is covered by family/profile TCK cases;
- it does not bypass canonical requirement matching.

If a new provider needs arbitrary request rewriting or unique response interpretation, that is evidence for executable code: extend the family adapter if the semantics are shared, otherwise create a connector.

### Profile distribution

Project-shipped profiles should live in one profile catalog, preferably typed YAML/JSON data embedded into the standard binary through `go:embed` or generated typed values.

Benefits:

- adding a profile does not edit Go switch tables;
- profiles can be reviewed as data;
- validation is centralized;
- the binary remains self-contained;
- no runtime code loading is introduced.

The existing custom-compatible configuration remains available for unknown/private endpoints.

## Single-Source Contribution Metadata

Current explicit registration is a strength. It should remain explicit.

The problem is repeated metadata:

- standard frontend list;
- standard backend bundle;
- essential kind list;
- compatible kind list;
- route-claim map;
- protocol-specific diagnostics branches;
- conformance list/metadata.

A contribution model should derive these views from one extension-owned declaration.

### Avoiding a god descriptor

Do not create:

```go
type PluginEverything struct {
    // every runtime dependency and every possible feature
}
```

Instead compose focused metadata facets:

```go
type FrontendContribution struct {
    Registration FrontendRegistration
    Routes       RouteContribution
    Diagnostics  DiagnosticContribution
    Contract     FrontendContractContribution
}

type BackendContribution struct {
    Registration BackendRegistration
    Diagnostics  DiagnosticContribution
    Contract     BackendContractContribution
    Family       *CompatibleFamilyContribution
}
```

Each facet carries metadata/providers only. Runtime dependencies remain factory inputs owned by existing composition seams.

## Route Ownership

`RouteClaim` itself is a good abstraction: owner, method, path, kind.

The central list of protocol-specific `RouteKind` constants is not necessary for conflict detection. The HTTP layer only needs a non-empty bounded identifier for diagnostics/traceability.

Selected direction:

- retain `type RouteKind string` or equivalent;
- validate non-empty/size/safe characters;
- move concrete operation IDs next to frontend contributions/protocol packages;
- remove central additions as a requirement for new frontend protocols.

## Diagnostics

A dedicated diagnostic DTO per protocol does not scale.

Selected direction:

```go
type ExtensionInstanceRow struct {
    ID           string
    Kind         string
    Origin       string
    Enabled      bool
    Family       string
    Profile      string
    Capabilities []string
    RouteClaims  []string
    Inventory    string
    Conformance  string
    Health       string
    Details      []SafeField
}
```

`SafeField` is bounded key/value data with centralized redaction/size rules. It is not `map[string]any`.

Protocol-specific projection remains adapter-owned but returns the common bounded DTO.

## Canonical Contract Promotion

### Problem

A canonical middle must be expressive enough to preserve semantics, but trying to add every provider wire field as a first-class canonical struct field eventually produces a universal provider AST.

Current examples requiring review include:

- `Call.PromptCacheKey`, documented specifically as an OpenResponses remote hint;
- reasoning `Summary`, `Content`, and `EncryptedContent` exact-presence fields;
- compaction encrypted content.

Some exact reasoning state now participates in Codex continuity, so not every protocol-named field is merely accidental leakage.

### Selected rule

Promote a field to first-class `lipapi` when at least one is true:

1. core routing/admission/security/accounting/lifecycle policy interprets it;
2. multiple protocol families share the semantic meaning and need canonical projection;
3. a stable semantic value cannot be represented safely by an existing typed generic carrier.

Otherwise use a bounded dialect/extension carrier.

### Explicit non-goal

Do not perform a large canonical rewrite only to remove vendor words from identifiers.

The implementation should audit current candidates, migrate only clear adapter-only fidelity, and retain fields whose semantics are now genuinely shared/core-consumed.

This preserves ROI and avoids another OpenResponses-sized migration.

## Backend-Plugin ABI Evolution

### Current state

The ABI already made a good generic move in v1.2:

- ordered items;
- protocol requirements;
- dialect/extension support.

v1.3 then introduced `exact_openresponses_fields` and exact OpenResponses-shaped fields.

This must remain compatible with current connectors.

### Options

#### Option ABI-A: Immediate v2

Pros:

- clean schema;
- remove protocol-named history.

Cons:

- ecosystem churn;
- connector migration cost;
- large scope;
- little immediate user benefit.

**Rejected.**

#### Option ABI-B: Keep adding protocol-specific minor fields

Pros:

- easy short-term implementation.

Cons:

- connector DTO/converter growth;
- ecosystem-wide coordination for each rich protocol;
- repeats the problem this spec exists to solve.

**Rejected.**

#### Option ABI-C: Add generic semantic extension carriers and freeze protocol-named growth

Pros:

- backwards-compatible;
- new provider profiles need no ABI change;
- new protocol families using existing semantics need no ABI change;
- exact bounded residual fidelity can flow with explicit dialect requirements.

Cons:

- requires careful validation to avoid raw tunneling.

**Selected.**

v1.3 fields and feature name remain valid compatibility vocabulary. New minors use semantic names.

## Generic ABI Carrier Safety

A generic carrier must include enough identity to remain fail-closed:

```go
type SemanticExtension struct {
    Namespace   string
    Type        string
    Implementor string
    Direction   string
    Presence    Presence
    Data        RawJSON
}
```

Constraints:

- strict byte/depth/count bounds;
- normalized identity;
- exact `ProtocolRequirements`/`DialectSupport` match;
- no bypass of canonical validation;
- no unknown required extension silently dropped;
- no wholesale raw provider request/response envelope.

The carrier is a canonical residual, not a tunnel.

## Continuation Duplication

The current tree has near-identical:

- bounded `MemoryStore`;
- `StreamRecorder`;

under both `pkg/lipsdk/continuation` and `internal/core/continuation`.

This is precisely the mirror pattern removed elsewhere in the repository.

Selected ownership:

- `pkg/lipsdk/continuation`: protocol-neutral contracts, value types, policies, clone/materialization helpers, and intentionally reusable in-memory/reference utilities;
- `internal/infra/continuation`: durable filesystem/database implementations;
- `internal/core/continuation`: thin orchestration that composes SDK contracts with core call/session state.

Therefore the SDK implementation should be the single authority where an implementation is intentionally public/reusable; core copies should be deleted or reduced to delegation/orchestration.

If implementation review discovers an SDK utility should not remain public, a source-compatibility migration must be designed before moving it. The design does not permit two mutable authorities.

## End-to-End Sentinel

A small real-stack suite remains necessary because independent contract tests cannot prove composition wiring.

Sentinel categories:

1. representative built-in hosted backend;
2. representative compatible-family backend;
3. representative executable connector;
4. stateful frontend path where required;
5. one negative capability/projection path.

The sentinel is explicit and bounded.

Important rule:

**adding a provider profile within an existing family never adds a sentinel pair.**

A new protocol/implementation class may add one only when it protects a new composition boundary.

## Migration From the 45-Cell Matrix

### Stage 1: Freeze current matrix as characterization

Before refactoring, record:

- required feature IDs;
- currently executable scenarios;
- current negative pre-network assertions;
- independent wire suites.

### Stage 2: Build TCKs and traceability

Map each current required feature to:

- frontend TCK;
- core TCK;
- backend TCK;
- profile certification;
- protocol-specific suite;
- sentinel.

### Stage 3: Dual-run

Run current matrix and new certification model together until:

- all current release-critical feature obligations have a new owner;
- selected deliberate fault mutations fail the new owner suite;
- known compatibility regressions remain detectable.

### Stage 4: Delete Cartesian-only scaffolding

Delete:

- full-product completeness assertion;
- OpenResponses row/column feature registries used only by completeness;
- general-cell evidence builders used only by completeness;
- manual pairwise feature classification metadata.

Retain:

- reusable scenarios;
- independent emulators;
- official compliance;
- protocol state-machine tests;
- bounded sentinel.

## De-Bloat Metric

A broad raw repository line-count target can reward moving code around.

The more truthful metric is a **legacy Cartesian-only surface baseline**.

Implementation should identify all files/functions whose only purpose is:

- full cell generation;
- cell-by-cell feature evidence;
- row/column/general-cell completeness metadata.

At least 80% of those non-generated Go lines must disappear after migration, unless retained code has a documented reusable TCK role.

A second gate prevents replacement bloat: total non-generated Go lines across selected shared conformance/composition/continuation affected surfaces must not grow above the implementation baseline.

## Change-Surface Report

A changed-file count should be decomposed into categories:

- extension-owned production;
- shared composition;
- canonical `lipapi`;
- core routing/runtime;
- connector ABI;
- infrastructure;
- generated protocol assets;
- independent refs/tests/fixtures/docs.

The report is primarily review evidence, not a universal hard file-count gate.

Hard expectations:

- provider-profile-only addition: zero canonical/core/frontend/ABI/shared-registry edits;
- new backend family using existing semantics: no frontend edits and no Cartesian tables;
- new frontend: no backend package edits;
- ABI/canonical expansion: explicit SDD justification.

## Considered Architecture Options

### Option 1: Keep Cartesian coverage and optimize execution

Examples:

- parallelize;
- shard across CI;
- run subsets on changed code;
- cache providers.

Rejected because it reduces wall clock while preserving `F×B` metadata, maintenance, and conceptual coupling. At 1,000 providers the problem remains.

### Option 2: Test only protocol-family pairs, keep provider-specific Go backends

Better than full Cartesian tests, but provider code/registration still grows linearly with provider count in the most expensive way.

Rejected as incomplete.

### Option 3: TCK certification + provider profiles + contribution-derived metadata + bounded sentinel

Selected.

It attacks both major multipliers:

- testing/compliance multiplication;
- provider implementation/registration multiplication.

### Option 4: Generic raw HTTP proxy backend with arbitrary transforms

Rejected.

It would be superficially extensible but would bypass or weaken:

- canonical semantics;
- capability admission;
- audit/accounting;
- provider security review;
- deterministic tests.

### Option 5: Dynamic plugin/DI framework

Rejected.

The current explicit Go composition and executable connector model is already adequate. A dynamic framework would increase hidden coupling and security/operational complexity.

## SOLID/Hexagonal Assessment of Selected Direction

### Single Responsibility

- frontend TCK owns frontend contract proof;
- backend TCK owns backend contract proof;
- core TCK owns canonical policy proof;
- provider profile owns declarative provider variation;
- contribution metadata owns composition description.

### Open/Closed

- new provider profiles extend data, not central switches;
- new backend families certify against a fixed TCK;
- new frontends certify against a fixed TCK;
- central registries are derived from contributions.

### Liskov Substitution

Capability/dialect declarations become executable substitution contracts. A backend claiming a capability must pass that semantic TCK.

### Interface Segregation

Contribution metadata is split into focused facets; TCK harnesses expose narrow probes; stateful frontend behavior stays protocol-owned.

### Dependency Inversion

Core continues to depend on `lipapi`/SDK contracts, never provider implementations. Provider profiles bind at composition/adapter edges.

### Hexagonal Architecture

Driving adapters (frontends) and driven adapters (backends/connectors) are independently certified against the canonical/application boundary. The bounded sentinel verifies composition without turning every adapter pair into a separate contract.

## Expected ROI

High-ROI changes in this spec:

1. retire Cartesian completeness;
2. backend/frontend/core TCKs;
3. provider profiles;
4. contribution-derived registries;
5. semantic ABI evolution guard;
6. continuation mirror deletion.

Deferred because ROI is not yet proven:

- generalizing `frontendpipe` around stateful WebSocket/continuation lifecycle;
- a backend-plugin ABI v2;
- wholesale canonical DTO renaming/rewrite;
- release-manifest sharding already proposed by adjacent work;
- a generic workflow/DI/plugin framework.

## Recommendation

Proceed with the selected architecture as an incremental migration.

The highest-risk mistake would be to implement TCKs as an **additional** testing layer while leaving the Cartesian matrix authoritative. The implementation plan must therefore make deletion/cutover an explicit phase with traceability and line-removal gates.
