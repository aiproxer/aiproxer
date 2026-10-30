# Design Document
## Extension Scalability and Architecture Simplification
## Overview
This design changes Go-LIP's extension architecture from **pairwise compatibility proof and repeated central registration** to **independent contract certification and contribution-derived composition**.
The selected design has six coordinated parts:
1. **Frontend, canonical-core, and backend TCKs** certify each adapter/policy boundary independently.
2. **A bounded end-to-end sentinel** verifies real composition without enumerating every frontend×backend pair.
3. **Typed provider profiles** let thousands of compatible providers reuse a small number of certified backend-family implementations.
4. **Single-source contribution descriptors** derive standard registration, route claims, diagnostics, and conformance metadata.
5. **Canonical/ABI evolution rules** favor protocol-neutral semantics plus bounded negotiated dialect carriers instead of protocol-named field growth.
6. **Mirror deletion and architecture ratchets** remove confirmed continuation duplicates and prevent Cartesian/parallel-registry regression.
This is a brownfield migration. Existing wire behavior, routing/failover, output commitment, runtime generations, connector transport, authority, accounting, and security behavior remain authoritative.
## Goals
- Make provider/backend growth practical at 1,000+ provider profiles.
- Remove complete frontend×backend Cartesian coverage as a mandatory release invariant.
- Prove frontends/backends once against canonical semantic contracts.
- Keep a small real-stack integration signal.
- Make most compatible-provider additions data-only.
- Eliminate repeated extension metadata tables.
- Prevent future backend-plugin ABI minors from being named after protocols/providers.
- Prevent `pkg/lipapi` from becoming an unbounded copy of provider schemas.
- Delete existing Cartesian-only conformance scaffolding after safe migration.
- Delete confirmed continuation mirrors.
- Preserve explicit Go composition and current hexagonal boundaries.
## Non-Goals
- No client-facing protocol change.
- No provider-specific feature implementation.
- No selector/routing algorithm change.
- No accounting/authority/security semantics change.
- No backend-plugin ABI v2.
- No dynamic Go plugins.
- No reflection-based plugin registry or DI container.
- No arbitrary provider-profile transformation language.
- No raw HTTP/request-response tunneling around canonical semantics.
- No wholesale rewrite of `pkg/lipapi`.
- No generalized stateful frontend lifecycle framework until a second real frontend demonstrates the abstraction need.
- No duplicate implementation of release-manifest fragmentation already being handled by adjacent work.
## Baseline and Architectural Constraints
Baseline SHA:
`95089eb4b74d5cf8d062f238a1121124ce0da878`
Existing invariants retained:
- frontend → canonical → backend, never pairwise translators;
- streaming-first execution;
- hard capability mismatches reject before upstream work;
- no retry/failover after output commitment;
- core imports no concrete providers/protocol codecs;
- optional backends remain executable gRPC connectors;
- explicit registration, no runtime code loading;
- independent reference protocol implementations remain test-only;
- immutable generation/runtime ownership remains unchanged.
## Requirement Traceability
| Requirement | Main design area |
|---|---|
| 1 | Brownfield preservation, migration gates |
| 2 | Additive scale model, no-Cartesian architecture |
| 3 | Backend TCK |
| 4 | Frontend TCK |
| 5 | Core TCK and bounded sentinel |
| 6 | Provider family/profile model |
| 7 | Contribution-derived composition |
| 8 | Generic route/diagnostics contracts |
| 9 | Canonical promotion policy |
| 10 | Semantic backend-plugin ABI evolution |
| 11 | Continuation single ownership |
| 12 | Matrix retirement and deletion |
| 13 | Architecture/change-surface ratchets |
## Target Architecture
```text
                         ┌───────────────────────────┐
                         │       pkg/lipapi          │
                         │ canonical semantics       │
                         └────────────┬──────────────┘
                                      │
                    ┌─────────────────┼─────────────────┐
                    │                 │                 │
             Frontend TCK         Core TCK         Backend TCK
                    │                 │                 │
          ┌─────────▼────────┐        │        ┌───────▼────────┐
          │ Frontend adapter │────────┼────────│ Backend family │
          └──────────────────┘        │        └───────┬────────┘
                                      │                │
                                      │         Provider profiles
                                      │                │
                                      │        ┌───────▼────────┐
                                      │        │ Provider A..N  │
                                      │        └────────────────┘
                                      │
                             Bounded E2E sentinel
                                      │
                           real frontend→core→backend
```
Executable connectors remain:
```text
canonical executor
      │
      ▼
backendplugin host adapter
      │ semantic v1 ABI
      ▼
connector executable
      │
      ▼
provider/local runtime
```
The connector TCK drives the same backend semantic corpus through this real host path.
## Design Rules
**D1. Canonical middle remains the only semantic interoperability authority.**
**D2. Mandatory conformance shall never be the full registered frontend×backend Cartesian product.**
**D3. Capability/dialect declarations select executable TCK scenarios; they are not documentation-only metadata.**
**D4. Unsupported hard semantics must be proven to reject before upstream work.**
**D5. Provider variation is data when an existing family can safely express it; executable code is required when it cannot.**
**D6. Provider profiles are bounded typed configuration, never an arbitrary transformation language.**
**D7. Extension metadata is declared once and projected into registration/route/diagnostic/conformance views.**
**D8. Contribution descriptors compose narrow facets and never become runtime dependency bags.**
**D9. First-class canonical fields require shared/core semantics; adapter-only fidelity uses bounded negotiated dialect/extension carriers.**
**D10. Generic opaque carriers are residual semantic data, not raw protocol tunnels.**
**D11. New backend-plugin minor features are named for canonical semantics/transport capabilities, not protocols/providers.**
**D12. ABI v1.0–v1.3 compatibility is preserved; `exact_openresponses_fields` remains a legacy negotiated alias.**
**D13. One algorithm/state-machine authority per continuation responsibility; no mirrors.**
**D14. Independent wire emulators/compliance suites remain independent and survive matrix deletion.**
**D15. The real-stack sentinel protects composition only and is explicitly bounded.**
**D16. Migration dual-runs old/new evidence until traceability and mutation tests prove safe cutover.**
**D17. Shared architecture must shrink or stay flat after legacy matrix/mirror removal; no permanent parallel framework.**
**D18. Change-surface reports distinguish generated/test breadth from shared-boundary churn.**
## 1. Contract Certification Architecture
### 1.1 Semantic Scenario Vocabulary
Create one protocol-neutral scenario vocabulary shared by frontend/core/backend certification.
Conceptual types:
```go
type SemanticFeature string
const (
    FeatureText              SemanticFeature = "text"
    FeatureStreaming         SemanticFeature = "streaming"
    FeatureTools             SemanticFeature = "tools"
    FeatureVision            SemanticFeature = "vision"
    FeatureDocuments         SemanticFeature = "documents"
    FeatureStructuredOutput  SemanticFeature = "structured_output"
    FeatureReasoning         SemanticFeature = "reasoning"
    FeatureReasoningReplay   SemanticFeature = "reasoning_replay"
    FeatureParallelTools     SemanticFeature = "parallel_tools"
    FeatureOrderedItems      SemanticFeature = "ordered_items"
    FeatureItemReferences    SemanticFeature = "item_references"
    FeatureCompaction        SemanticFeature = "compaction"
    FeatureAssistantPhase    SemanticFeature = "assistant_phase"
    FeatureExtensions        SemanticFeature = "extensions"
    FeatureVideo             SemanticFeature = "video"
    FeatureAnnotations       SemanticFeature = "annotations"
    FeatureAssistantMedia    SemanticFeature = "assistant_media"
)
type ScenarioID string
type ScenarioDescriptor struct {
    ID        ScenarioID
    Feature   SemanticFeature
    Requires  lipapi.ProtocolRequirements
    Transport ScenarioTransport
}
```
The scenario descriptor is metadata. Executable builders/assertions remain in test packages so production packages do not depend on test functions.
### 1.2 Package Layout
Proposed root-module test architecture:
```text
internal/testkit/contract/
├── semantic/       # shared feature IDs, scenario metadata/fixtures
├── frontend/       # frontend TCK runner/harness
├── core/           # canonical core contracts
└── backend/        # built-in/family backend TCK runner/harness
```
Connector-facing supported package:
```text
pkg/lipsdk/backendplugin/contracttest/
```
`contracttest` either:
- delegates to a public-safe scenario corpus under `pkg/lipsdk`, or
- carries a small stable duplicate-free representation generated from the same source.
The final implementation must not maintain two independent semantic scenario catalogs. If `internal` visibility prevents direct sharing with third-party modules, place the dependency-neutral scenario descriptors in a supported `pkg/lipsdk/contract` package and keep runners in `internal/testkit`.
### 1.3 Certification Evidence
```go
type Certification struct {
    SubjectID     string
    SubjectKind   string // frontend, backend_family, provider_profile, connector
    Profile       string
    Capabilities  []lipapi.Capability
    Dialects      lipapi.DialectSupport
    Passed        []ScenarioID
    Negative      []ScenarioID
    Failed        []ScenarioFailure
}
```
CI may serialize this to JSON under test artifacts. Production runtime does not read certification artifacts.
The certification record replaces cell-by-cell feature evidence as the primary compatibility proof.
## 2. Backend TCK
### 2.1 Backend Harness
The root backend TCK consumes a narrow harness:
```go
type Harness interface {
    Subject() SubjectDescriptor
    Backend(ctx context.Context) (BackendView, error)
    Upstream() UpstreamProbe
    Reset(ctx context.Context) error
}
type BackendView interface {
    Open(ctx context.Context, call lipapi.Call) (lipapi.EventStream, error)
    EffectiveCapabilities(ctx context.Context, call lipapi.Call) BackendFacts
}
type UpstreamProbe interface {
    RequestCount() int
    LastRequest() CapturedRequest
}
```
This is a test seam, not a new production backend interface. Existing `execbackend.Backend` remains authoritative in production.
### 2.2 Scenario Selection
For each scenario:
1. build canonical call;
2. derive required protocol requirements;
3. inspect effective backend facts;
4. if supported:
   - execute positive scenario;
   - inspect upstream request;
   - inspect canonical events;
5. if hard requirements are not supported:
   - execute through real candidate admission/backend open boundary as appropriate;
   - assert stable rejection;
   - assert upstream count remains zero.
This prevents capability metadata from lying silently.
### 2.3 Mandatory Backend Contracts
Baseline scenarios include:
- text stream and non-stream collection equivalence;
- usage presence and zero values;
- typed recoverable pre-output errors;
- terminal provider errors;
- cancellation;
- tool call lifecycle;
- tool result replay;
- image/document input;
- reasoning output and replay dialects;
- structured output;
- ordered items;
- item references;
- compaction;
- extensions;
- output status/incomplete terminal;
- lifecycle/Close idempotency where applicable.
Not every backend passes every positive scenario. It passes the positive scenarios its declaration makes mandatory and the negative scenarios for unsupported hard semantics.
### 2.4 Connector Contract Test
`pkg/lipsdk/backendplugin/contracttest` drives:
- Negotiate;
- Describe;
- Configure;
- model/profile resolution where declared;
- Execute;
- cancellation;
- shutdown/close;
- semantic round-trips.
It must include semantic feature negotiation, not only transport/ABI shape validation.
Third-party connector authors should be able to write conceptually:
```go
func TestConnectorContract(t *testing.T) {
    contracttest.Run(t, contracttest.Config{
        Start: startMyConnector,
    })
}
```
Exact API may differ after interface-first implementation.
## 3. Frontend TCK
### 3.1 Capturing Executor
```go
type CapturingExecutor struct {
    Calls  []lipapi.Call
    Script EventScript
}
func (e *CapturingExecutor) Execute(
    ctx context.Context,
    call *lipapi.Call,
) (lipapi.EventStream, error)
```
Frontend tests send real wire requests through mounted handlers and inspect captured canonical calls.
Output tests script canonical events and inspect frontend wire output.
### 3.2 Common Contracts
Common frontend TCK covers:
- route match/ownership;
- authentication order;
- body limit;
- invalid JSON/wire syntax;
- canonical validation;
- route selector;
- text;
- tools;
- multimodal;
- reasoning/replay;
- structured output;
- usage;
- errors;
- cancellation;
- streaming/non-streaming.
Protocol-owned suites add behavior that has no common lifecycle:
- OpenResponses continuation;
- OpenResponses compaction resource rules;
- OpenResponses WebSocket session/queue/origin behavior;
- vendor-specific required presence and event state machines.
### 3.3 No Backend Construction
The frontend TCK may use refclients/fixtures but never constructs a provider backend. This guarantees adding provider profile #1001 cannot increase frontend certification work.
## 4. Core TCK
The core contract suite directly exercises:
- `RequiredCapabilities`;
- `DeriveProtocolRequirements`;
- `MatchRequirements`;
- `ProjectItemsToLegacyView`;
- `ProjectLegacyToOrderedItems`;
- `AdmitCandidate`;
- transport negotiation;
- frozen failover requirement union;
- output commitment/no-retry;
- canonical stream validation.
Where full runtime orchestration is required, use fake `execbackend.Backend` implementations rather than real provider adapters.
The core TCK is the proof that independently certified frontend/backend adapters compose semantically.
## 5. Bounded Integration Sentinel
### 5.1 Purpose
The sentinel catches:
- missing standard registration;
- broken composition-root wiring;
- route mounting mistakes;
- connector-host wiring failures;
- incorrect middleware ordering;
- generation/runtime assembly errors.
It is **not** the primary semantic compatibility proof.
### 5.2 Sentinel Registry
```go
type SentinelCase struct {
    ID       string
    Frontend string
    Backend  string
    Protects string
}
```
The registry is explicit.
Rules:
- no generated `AllCells()`;
- no automatic pair addition for provider profiles;
- one representative case per distinct composition boundary is preferred;
- `Protects` is mandatory;
- architecture test enforces an absolute reviewed upper bound or a formula based only on implementation classes, not provider count.
Initial migration may reuse existing reliable cells.
A provider-profile addition in an existing family cannot modify this registry.
## 6. Provider Family/Profile Architecture
### 6.1 Taxonomy
Every provider integration is exactly one of:
1. **Provider profile** — data applied to an existing compatible backend family.
2. **Backend family adapter** — Go implementation of a distinct compatible/native wire behavior.
3. **Executable connector** — out-of-process implementation for optional/unique provider/local runtime behavior.
Essential built-ins remain current essential families.
### 6.2 Profile Schema
Proposed source tree:
```text
provider-profiles/
├── schema-v1.md
├── openai-responses/
│   └── <provider>.yaml
├── openai-chat/
├── openresponses/
└── anthropic/
```
Runtime/compiler package:
```text
internal/providerprofiles/
├── schema.go
├── decode.go
├── validate.go
├── catalog.go
└── embedded.go
```
The catalog uses `go:embed` or deterministic generation so the standard binary remains self-contained.
### 6.3 Profile Types
Conceptual:
```go
type Profile struct {
    APIVersion string
    ID         string
    Family     FamilyID
    Endpoint EndpointProfile
    Auth     AuthProfile
    Headers  []Header
    Models   ModelProfile
    CapabilityOverride CapabilityOverride
    Dialects           lipapi.DialectSupport
    Tokenizer TokenizerProfile
    Quirks    []QuirkID
}
```
No `map[string]any`.
Unknown fields fail unless the profile schema explicitly allows forward-compatible extension metadata.
### 6.4 Approved Profile Variability
Allowed:
- HTTPS base URL / endpoint identity;
- family-defined path joining mode;
- credential mode and environment variable names;
- bounded allowlisted static headers;
- model catalog endpoint or static model inventory;
- namespace prefix rules already supported by family;
- tokenizer/accounting profile selection;
- capability disable/enable constrained by family maximums;
- exact dialect/extension support constrained by family;
- closed deterministic quirk IDs.
Not allowed:
- arbitrary Go/plugin code;
- JavaScript/Lua/templates;
- arbitrary regex substitution programs;
- arbitrary response remapping expressions;
- unbounded header passthrough;
- commands/process execution.
### 6.5 Effective Capability Rule
A profile cannot invent capabilities its family implementation cannot provide.
```text
effective(profile) =
    family_max
    - profile.disabled
    + profile.enabled_if_family_allows
```
Exact dialects/extensions must also be a subset of family-supported behavior or resolved by a family-owned model-aware profile resolver.
### 6.6 Graduation Rule
Use a dedicated implementation if the provider needs:
- non-family wire schema;
- unique stream event semantics;
- unique OAuth/browser/device auth workflow;
- local executable/process lifecycle;
- stateful provider session protocol;
- request/response interpretation requiring new code;
- provider-specific retry/continuation semantics.
This rule is documented and architecture-reviewed.
## 7. Contribution-Derived Composition
### 7.1 Focused Facets
```go
type RegistrationFacet struct {
    ID     string
    Source RegistrationSource
    // one of frontend/backend factory forms
}
type RouteFacet struct {
    Claims FrontendRouteClaims
}
type DiagnosticFacet struct {
    Project DiagnosticProjector
}
type ContractFacet struct {
    Subject ContractSubject
}
type CompatibleFamilyFacet struct {
    FamilyID FamilyID
    Profiles ProviderProfileSource
}
```
Composite:
```go
type FrontendContribution struct {
    Registration FrontendRegistrationFacet
    Routes       *RouteFacet
    Diagnostics  *DiagnosticFacet
    Contract     FrontendContractFacet
}
type BackendContribution struct {
    Registration BackendRegistrationFacet
    Diagnostics  *DiagnosticFacet
    Contract     BackendContractFacet
    Compatible   *CompatibleFamilyFacet
}
```
Exact type placement should avoid import cycles. Likely ownership:
- internal composition metadata in `internal/standardplugins` or a cycle-neutral `internal/pluginreg/contrib`;
- public connector registration types remain in `pkg/lipsdk`.
### 7.2 Derived Views
Functions derive:
- standard frontend registrations;
- standard backend registrations;
- essential backend IDs;
- compatible family IDs;
- route-claim providers;
- diagnostics projectors;
- contract subjects.
No second manually maintained list is authoritative.
### 7.3 Provider Profiles Do Not Become Contributions
A provider profile belongs to a family contribution.
If 500 profiles bind to `openai-responses-compatible`, there is still one family backend contribution, not 500 factory contributions.
Profiles become configured instances/inventory entries through the family profile catalog/compiler.
## 8. Generic Route Claims
Change central route kind ownership from closed protocol constants to validated string IDs.
Conceptual:
```go
type RouteKind string
func (k RouteKind) Validate() error {
    // non-empty, bounded, safe chars
}
```
Concrete IDs live with the frontend:
```go
const (
    RouteCreate  contract.RouteKind = "openresponses.create"
    RouteCompact contract.RouteKind = "openresponses.compact"
    RouteWS      contract.RouteKind = "openresponses.websocket"
)
```
`stdhttp/contract` does not need to know these constants.
Existing route conflict behavior remains method/path based.
## 9. Generic Diagnostics Projection
Replace protocol-specific central rows with one bounded common form:
```go
type InstanceDiagnostic struct {
    ID             string
    FactoryKind    string
    Origin         string
    Enabled        bool
    Family         string
    Profile        string
    Capabilities   []string
    RouteClaims    []string
    InventoryState string
    Conformance    string
    ConfigError    string
    Details        []SafeField
}
type SafeField struct {
    Key   string
    Value string
}
```
Limits:
- key count;
- key/value bytes;
- allowed key syntax;
- redaction/sanitization;
- deterministic ordering.
The projector is contribution-owned and side-effect free.
Existing HTTP JSON may need compatibility adapters if diagnostic response shapes are public/stable. The implementation must characterize current wire output before changing DTOs.
## 10. Canonical Promotion Policy and Targeted Audit
### 10.1 Promotion Checklist
Before adding first-class `lipapi` state, a future design must answer:
1. Which core policy consumes this semantic?
2. Which second protocol family shares it?
3. Why can a bounded dialect/extension carrier not represent exact residual fidelity?
4. What projection/admission behavior depends on the field?
5. What public API cost does promotion create?
No answer → keep adapter-owned or use negotiated residual carrier.
### 10.2 Current Audit
Required implementation audit:
| Candidate | Initial classification | Required action |
|---|---|---|
| `Call.PromptCacheKey` | likely adapter-only OpenResponses compatible hint | characterize uses; migrate to generic bounded call extension if no core/shared consumer |
| reasoning `Summary` | possible shared reasoning semantic | retain unless audit proves adapter-only |
| reasoning `Content` | possible shared exact reasoning semantic | retain unless audit proves adapter-only |
| reasoning `EncryptedContent` | shared OpenAI/Codex opaque continuity candidate | likely retain as generic reasoning opaque/presence semantic unless a cleaner carrier is demonstrably smaller |
| compaction encrypted content | compaction/replay semantic | retain or rename only if it reduces surface without migration churn |
This table deliberately avoids mandating a vendor-word cleanup.
### 10.3 Residual Carrier
Use existing `OpaqueExtension` / `ExtensionContentPart` where possible.
If a gap remains, add one generic bounded presence-bearing residual type rather than protocol-specific fields.
Example:
```go
type SemanticExtension struct {
    Namespace   string
    Type        string
    Implementor string
    Direction   ExtensionDirection
    Presence    JSONPresence
    Data        json.RawMessage
}
```
Core interprets identity/requirements, not provider payload schema.
## 11. Backend-Plugin ABI Semantic Extension
### 11.1 Compatibility
Keep:
- protocol major 1;
- v1.1 exact reasoning;
- v1.2 ordered items;
- v1.3 exact OpenResponses fields;
- current negotiation behavior.
No current connector is forced to update merely because this spec lands.
### 11.2 New Semantic Carrier Feature
If implementation proves existing extension DTOs are insufficient, add one additive semantic feature, e.g.:
`semantic_extensions_v1`
The exact name must be protocol-neutral.
The proto carrier mirrors the canonical residual:
```proto
message SemanticExtensionWire {
  string namespace = 1;
  string type = 2;
  string implementor = 3;
  string direction = 4;
  PresenceState presence = 5;
  bytes json = 6;
}
```
Attach only at canonical locations that genuinely support residual extension data.
Do not add a raw complete request/response envelope field.
### 11.3 Legacy Bridging
Host conversion rules:
- current v1.3 fields continue to round-trip exactly;
- where semantically equivalent and negotiated, host may construct the new generic residual representation internally;
- a connector negotiating only v1.3 receives legacy fields exactly as today;
- a connector negotiating a future generic feature receives validated generic carriers;
- never send both as independent authorities when that could duplicate semantics.
### 11.4 Architecture Gate
Maintain an explicit compatibility allowlist for existing protocol-named symbols:
- `FeatureExactOpenResponsesFields`;
- `ProtocolMinorExactOpenResponsesFields`;
- current v1.3 proto fields.
New protocol/provider names in backendplugin feature/proto schema fail an architecture test unless a separately approved exception updates the allowlist.
## 12. Continuation Ownership Convergence
### 12.1 Selected Authority
Default selected ownership:
- retain protocol-neutral `pkg/lipsdk/continuation` contracts/utilities;
- delete equivalent `internal/core/continuation.MemoryStore`;
- delete equivalent `internal/core/continuation.StreamRecorder`;
- keep `internal/core/continuation.MaterializeCall` or equivalent only where it applies core session/call policy around SDK materialization;
- retain durable `internal/infra/continuation` implementations.
Before deletion, characterization must prove exact behavior parity.
### 12.2 Compatibility Exception
If implementation discovers public SDK types cannot safely remain the implementation authority, design revalidation is required before moving them. The allowed outcomes are still one authority plus thin compatibility delegation, never two state machines.
### 12.3 Mirror Gate
Architecture tests check for:
- duplicate production `MemoryStore` continuation authorities;
- duplicate production `StreamRecorder` authorities;
- copied recorder/store state-field fingerprints where feasible;
- core wrappers that reimplement SDK algorithms.
Use semantic/go-types checks where practical, not fragile exact line-count equality.
## 13. Conformance Migration
### Phase A — Characterization
Freeze:
- current 45 cells;
- 17 required feature IDs;
- scenario IDs;
- zero-upstream negative assertions;
- official/refclient/refbackend coverage.
Capture legacy Cartesian-only file baseline.
### Phase B — RED TCKs
Add contract tests before changing production/conformance registration.
Use deliberate mutations/fixtures to prove:
- frontend TCK catches decode/encode drift;
- backend TCK catches false capability claims;
- core TCK catches projection/admission drift;
- connector TCK catches ABI semantic loss.
### Phase C — Contribution/Profile Migration
Introduce:
- provider-profile catalog;
- contribution-derived views;
- generic route/diagnostics projection.
Keep behavior characterized.
### Phase D — Dual Evidence
Run:
- legacy matrix;
- TCK certifications;
- bounded sentinel.
Generate traceability report from current feature IDs to new owners.
### Phase E — Cutover and Delete
After traceability passes:
- parity/release gate uses TCK + sentinel;
- full matrix becomes optional diagnostic/sampled only or is deleted;
- remove Cartesian-only evidence code;
- enforce ≥80% legacy-surface deletion.
## 14. Release/CI Topology
Proposed high-level gates:
### Default / quality
- architecture tests;
- core TCK;
- frontend TCK;
- backend TCK for cheap in-process built-ins/families;
- provider-profile schema/catalog tests.
### Precommit/integration
- connector-host backend TCK;
- bounded sentinel;
- protocol-specific richer suites.
### Nightly/optional
- sampled broader pair testing if useful;
- large provider-profile catalog stress;
- race/fuzz.
The exact tag placement follows steering and runtime cost measurements.
No mandatory job enumerates every profile against every frontend.
## 15. Synthetic Scale Contract
Build a test registry with:
- 5 frontend contributions;
- representative backend family contributions;
- 1,000 provider profiles bound across existing families.
Assertions:
- provider catalog size = 1,000;
- contribution/factory count grows by families, not profiles;
- sentinel count unchanged when profiles are added within represented families;
- no `AllCells()`-style pair list exists;
- profile validation performs no network/process start;
- memory allocations are bounded by catalog/profile data, not pair multiplication.
No wall-clock threshold is a hard correctness gate.
## 16. Change-Surface Report
Add a small repository tool or architecture helper that classifies changed paths.
Categories:
```text
extension-owned-production
provider-profile-data
shared-composition
canonical-contract
core-routing-runtime
backendplugin-abi
infrastructure
generated
tests-reference
docs-spec
```
Report example:
```text
provider-profile-data:       3
extension-owned-production:  0
shared-composition:           0
canonical-contract:           0
core-routing-runtime:        0
backendplugin-abi:            0
tests-reference:              4
```
It is intentionally not a blanket "max changed files" gate.
Hard policy checks can consume the categories for profile-only fixtures.
## 17. De-Bloat Measurement
At implementation start, create a baseline selector listing Cartesian-only files/functions.
Mandatory final result:
- ≥80% of legacy Cartesian-only non-generated Go lines removed;
- no net growth across reviewed shared conformance/composition/continuation surface;
- independent protocol evidence excluded from deletion target;
- generated official protocol sources excluded from line-count target.
This makes the goal deletion of accidental architecture, not deletion of tests for its own sake.
## 18. Failure Handling
### Invalid provider profile
Fail candidate/startup/reload validation before provider activation.
### False capability declaration
TCK fails; runtime admission remains fail-closed independently.
### Unknown semantic ABI carrier
Reject if required and unsupported; never silently omit.
### Missing certification
CI/release gate fails for the changed family/connector/profile subject according to contribution metadata.
### Sentinel failure
Treat as composition regression; TCK certification does not override it.
### Migration traceability gap
Do not delete the legacy matrix owner for that feature until the gap is resolved.
### Continuation parity mismatch
Do not delete either implementation until the mismatch is characterized and one contract is selected through design revalidation if necessary.
## 19. Security Considerations
- Profile schema rejects arbitrary transformations/execution.
- Credential values remain environment/secret owned; profiles carry names/modes, not secrets.
- Static headers are bounded and must exclude authorization/secret-reserved names unless the family auth contract owns them.
- Endpoint policy reuses existing HTTPS/loopback/insecure escape-hatch rules.
- Generic diagnostic details are bounded/redacted.
- Generic semantic ABI carriers are byte/depth/count bounded.
- Required opaque semantics participate in exact negotiation.
- Profile validation performs no provider network access.
- Refclients/refbackends remain test-only.
- No new dynamic code-loading surface.
## 20. Performance and Complexity
The primary performance requirement is architectural scaling.
Deterministic complexity gates:
- no mandatory Cartesian pair generation;
- profile additions do not add family factories;
- sentinel does not grow with profile count within a family;
- 1,000-profile validation has no per-profile goroutine/process/network work.
Runtime request hot paths should not materially change:
- TCK code is test-only;
- contribution derivation occurs at composition;
- profile validation occurs at startup/reload;
- capability admission remains existing core logic.
If profile lookup enters request routing, use immutable maps compiled per generation and benchmark lookup; do not scan the catalog per request.
## 21. Testing Strategy
### RED-first
Before implementation:
- characterization of central lists/diagnostics/route claims;
- matrix feature traceability freeze;
- continuation parity characterization;
- ABI v1.3 round-trip characterization;
- provider-profile expected zero-shared-footprint fixture.
### Unit
- profile decode/validation;
- contribution derivation;
- route kind validation;
- diagnostic sanitization;
- scenario selection;
- certification serialization;
- continuation authority.
### Integration
- built-in backend TCK;
- connector-host TCK;
- frontend mounted TCK;
- bounded sentinel;
- runtime reload of profile changes.
### Architecture
- no Cartesian completeness;
- no parallel authoritative registries;
- no protocol-named new ABI features;
- no provider profiles in core;
- no continuation mirrors;
- expected contribution derivation.
### Race/Fuzz
- connector/stream TCK paths under race;
- provider-profile decoder fuzz;
- generic semantic-extension decode/validation fuzz if new carrier added;
- continuation single implementation existing race/security tests.
## 22. Migration Compatibility
### Backend-plugin
No major bump. v1.3 remains supported.
### Configuration
Existing custom-compatible backend YAML remains valid.
Project-shipped provider profiles are additive; no existing configuration must switch to a profile.
### Diagnostics
Characterize current JSON. If replacing protocol-specific row structs changes external JSON, retain compatibility serialization or version the diagnostic view explicitly.
### Public SDK
Prefer aliases/thin compatibility for moved public continuation helpers where required. No silent removal.
### Test API
Internal conformance APIs may break because they are test infrastructure, but connector-facing `contracttest` becomes a supported SDK surface and should be versioned conservatively.
## 23. Implementation Boundaries
### Core
Allowed changes:
- architecture/core TCK tests;
- minimal generic requirement support only if needed;
- no provider-profile branching.
### `pkg/lipapi`
Allowed:
- targeted audit/removal/migration of adapter-only fidelity;
- generic bounded carrier only if proven necessary.
Not allowed:
- adding provider profile fields;
- provider-specific capability switches.
### Standard composition
Owns:
- contribution derivation;
- family/profile catalog binding;
- diagnostics/route contribution wiring.
### Backends/connectors
Own:
- family-specific profile interpretation;
- provider wire quirks;
- TCK harness adapters.
### Tests
Own:
- contract runners;
- certification evidence;
- sentinel selection;
- legacy matrix migration/deletion.
## 24. Design Validation Criteria
The implementation design is acceptable only if it can answer yes to all:
1. Can provider profile #1001 land without frontend/core/ABI/global-matrix edits?
2. Does a new backend family run one semantic backend TCK rather than F pair suites?
3. Does a new frontend run one frontend TCK rather than B pair suites?
4. Can a connector author certify semantics through a supported SDK package?
5. Does core remain provider/profile unaware?
6. Are route/diagnostic views derived without protocol-ID switches?
7. Are future ABI features semantic rather than provider/protocol named?
8. Are generic opaque carriers still capability/dialect gated?
9. Is there exactly one continuation store/recorder implementation authority?
10. Is there a bounded real-stack sentinel?
11. Is the old matrix actually deleted after dual-run, not merely supplemented?
12. Do affected shared surfaces shrink or remain flat?
