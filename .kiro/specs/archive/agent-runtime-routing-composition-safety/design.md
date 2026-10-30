# Design Document

## 1. Overview

This design implements issue #323 as a core-owned **execution composition safety** rule rather than a list of prohibited backend names.

The design adds one new piece of backend metadata—execution class—and one generation-scoped routing policy. Backend/plugin edges declare whether a factory behaves like ordinary inference or a whole agent runtime. Composition projects that metadata onto configured backend instance IDs. Core routing validates the already-compiled selector AST before any dynamic routing work.

The first-version safe invariant is intentionally small:

> A selector is safe when it is exactly one direct primary, or every reachable configured backend in a composed selector is explicitly classified `inference`.

Therefore direct agent-runtime routes keep working, while weighted, parallel, thinker, and failover composition involving an agent runtime is blocked. Known legacy backends whose class is missing remain directly usable but cannot participate in safe composition until classified. Operators can explicitly choose `unrestricted` to retain legacy behavior.

No selector syntax, canonical `pkg/lipapi` contract, backend-plugin execution ABI, provider SDK boundary, or ordinary inference routing algorithm changes.

## 2. Goals and Non-Goals

### Goals

1. Model agent/orchestration execution semantics explicitly rather than heuristically.
2. Support different classes for multiple exports from one connector artifact.
3. Preserve direct routing to agent runtimes.
4. Make risky composition fail closed by default.
5. Include ordered failover in the restriction because pre-output does not imply side-effect-free agent execution.
6. Keep inference-only routing behavior unchanged.
7. Reuse one pure semantic validator for request execution, configured/default routes, and A-leg route-override preflight.
8. Reject before backend dispatch and before mutable routing/planning state.
9. Preserve third-party legacy direct routes through an `unknown` compatibility state.
10. Keep core independent of connector/provider identities.

### Non-goals

- adding/removing routing operators;
- providing workspace rollback or transactional tool execution;
- certifying any current agent runtime as safely retryable;
- creating provider-specific exception flags;
- allowing clients to disable policy;
- migrating execution class into `pkg/lipapi`;
- adding a backend-plugin gRPC feature/minor solely for classification;
- changing output commitment or inference failover rules.

## 3. Brownfield Architecture Fit

The existing architecture already has the correct ownership split:

```text
connector manifest / builtin contribution
              |
              | declares factory execution semantics
              v
       pluginreg factory metadata
              |
              | configured backend rows: factory ID -> instance ID
              v
     immutable generation instance view
              |
              | narrow resolver + policy
              v
       internal/core/routing
     CompileSelector -> ValidateExecutionComposition
              |
              | only if valid
              v
 native binding -> affinity/interleaved/planner -> billing -> backend Open
```

Core owns policy. Adapters own facts about themselves. Runtimebundle is the composition boundary that translates factory metadata into generation-local configured identities.

This follows existing hexagonal rules:

- **driven adapters/connectors:** declare metadata;
- **composition root:** assembles immutable metadata;
- **application/core policy:** decides whether a selector is legal;
- **driving frontends/admin HTTP:** map typed core errors but do not own policy.

## 4. Execution Metadata Contract

### 4.1 SDK registration type

Add a focused public SDK type because backend factory registration is a supported extension contract:

```go
type BackendExecutionClass string

const (
    BackendExecutionUnknown      BackendExecutionClass = ""
    BackendExecutionInference    BackendExecutionClass = "inference"
    BackendExecutionAgentRuntime BackendExecutionClass = "agent_runtime"
)

type BackendExecutionProfile struct {
    Class BackendExecutionClass
}

func (p BackendExecutionProfile) EffectiveClass() BackendExecutionClass
func (p BackendExecutionProfile) Validate() error
```

Design rules:

- zero/omitted means effective `unknown` for source compatibility;
- project-owned factories must explicitly use a non-zero known class;
- only `inference` and `agent_runtime` are valid authored non-empty values in v1;
- execution profile remains separate from `BackendSecurityProfile`.

Exact helper names may be adjusted idiomatically during implementation, but the type/ownership semantics are fixed.

### 4.2 Registry storage

`internal/pluginreg.Registry` gains a parallel factory-metadata map:

```go
backendExecutionProfiles map[string]lipsdk.BackendExecutionProfile
```

Registration APIs must have one explicit path to provide the execution profile without forcing callers to combine security and execution concerns into one struct.

Preferred migration shape:

- retain existing registration helpers as source-compatible wrappers using effective `unknown`;
- add focused `...WithProfiles`/registration descriptor support for standard distribution code;
- migrate project-owned builtins to explicit `inference`;
- discovered exports pass the manifest-derived profile.

Do not introduce a generic `map[string]any` metadata bag.

### 4.3 Standard contribution metadata

`internal/standardplugins` may add a focused execution facet/profile to backend contributions/registrations and derive registry input from it.

The contribution remains metadata only. It must not gain runtime services, routing callbacks, or provider-specific policy.

Essential built-in inference backends explicitly declare `inference`.

## 5. Executable Connector Manifest

### 5.1 Per-export field

Extend `golip.backendplugin.manifest/v1` export metadata:

```json
{
  "kind": "openai-codex-app-server",
  "credential_mode": "none",
  "access_scope": "local_only",
  "process_sharing": "per_instance",
  "execution_class": "agent_runtime"
}
```

`wireExport` and `sdkmanifest.Export` gain `ExecutionClass`.

Because the current parser is closed and uses `DisallowUnknownFields`, parser/type/validator changes are atomic.

### 5.2 Compatibility rules

- omitted field -> effective `unknown`;
- `inference` -> accepted;
- `agent_runtime` -> accepted;
- any other non-empty value -> manifest validation failure;
- existing manifests without the field remain readable;
- official project-owned manifest templates must be migrated to explicit classes.

No schema v2 and no backend-plugin runtime protocol feature are introduced. This is installation/registration metadata known before connector activation.

### 5.3 Per-export, not per-artifact

The Codex manifest demonstrates why this matters:

```text
io.golip.backend.codex
  ├── openai-codex             -> inference
  └── openai-codex-app-server  -> agent_runtime
```

Artifact-level classification is prohibited.

## 6. Official Classification Ownership

Concrete classifications live with their factory/export declarations, not in core.

Expected semantic categories include:

```text
agent_runtime
  - ACP-backed whole-agent harness exports
  - Cursor CLI ACP
  - Gemini CLI ACP
  - agent CLI ACP wrappers
  - openai-codex-app-server
  - cursorsdk

inference
  - essential hosted backends
  - compatible protocol-family/profile backends
  - openai-codex
  - opencode-go / opencode-zen inference endpoints
  - local inference runtimes (llama.cpp, Ollama, vLLM, LM Studio)
  - ordinary hosted/aggregator connectors
```

This list is explanatory only. Tests inspect owned metadata; no production central allowlist is created from this document.

## 7. Generation-Bound Instance Projection

### 7.1 Why instance projection is required

Selectors carry configured backend IDs. Registry metadata is keyed by factory kinds. Existing config supports:

```yaml
plugins:
  backends:
    - id: cursor-work
      kind: cursorsdk
      enabled: true
```

The validator must classify `cursor-work`, not assume selector leaves use `cursorsdk`.

### 7.2 Compile the immutable view before backend request execution

During generation build, for every enabled backend row:

1. resolve `FactoryID()`;
2. resolve `InstanceID()`;
3. read the factory execution profile from the plugin registry;
4. store effective class in a generation-local immutable view keyed by instance ID.

Conceptual value:

```go
type BackendExecutionView struct {
    // immutable after construction
    classes map[string]lipsdk.BackendExecutionClass
}

func (v BackendExecutionView) ResolveBackendExecution(
    backendID string,
) (lipsdk.BackendExecutionClass, bool)
```

`bool=false` means the backend instance is absent from the generation.
`bool=true, class=unknown` means it is configured but metadata is missing.

The concrete map type should remain unexported/defensively copied. Core consumes a narrow resolver interface defined near the routing validator, for example:

```go
type BackendExecutionResolver interface {
    ResolveBackendExecution(
        backendID string,
    ) (lipsdk.BackendExecutionClass, bool)
}
```

### 7.3 Build ordering

Factory/export metadata is available before a request uses a backend. Candidate generation assembly should construct the class view before validating configured/default selectors.

Where practical, configured/default selector rejection should happen before activating/building discovered backend instances. If current generation build ordering makes that disproportionately invasive, the hard correctness boundary is still “before generation publication and before request-attributable backend execution”; implementation tasks include a characterization test and choose the earliest existing side-effect-free composition point.

No per-request manifest scans are allowed.

### 7.4 Direct/internal executor construction

`runtime.NewExecutor` is internal but is also used by test helpers outside the standard runtimebundle path. A missing resolver must not mean “unrestricted” or “all inference”.

Construction rule:

- when an executor has configured `CoreRuntime.Backends` but no explicit execution-class resolver/view, those configured backend IDs are effectively `unknown` for safe composition;
- direct routes still work;
- composite routes fail safe unless the caller supplies explicit inference metadata or explicitly selects unrestricted;
- `internal/testkit.WithBackends` (or a dedicated test option introduced by the implementation) should mark ordinary fake test backends as inference so existing inference-routing tests remain concise;
- focused tests can override selected fake backends as agent runtime/unknown.

This fallback is about internal construction safety and test ergonomics; it is not a production heuristic based on backend implementation type.

## 8. Routing Policy Model

### 8.1 Configuration

Add typed routing config:

```yaml
routing:
  execution_composition_policy: safe
```

Supported values:

```go
type ExecutionCompositionPolicy string

const (
    ExecutionCompositionSafe         ExecutionCompositionPolicy = "safe"
    ExecutionCompositionUnrestricted ExecutionCompositionPolicy = "unrestricted"
)
```

Effective empty value is `safe`. Unknown values fail config validation.

The policy is operator-owned and generation-scoped.

### 8.2 Direct-vs-composed predicate

Define the semantic predicate structurally from the AST:

```go
func IsDirectPrimary(sel *Selector) bool {
    return sel != nil &&
        len(sel.Alternatives) == 1 &&
        sel.Alternatives[0].Primary != nil
}
```

A single-primary selector remains direct even when it has:

- global affinity;
- global/per-leaf TTFT;
- context constraints;
- query parameters.

Any weighted or parallel AST node is composition even if it happens to contain one eligible branch. More than one failover alternative is composition.

This keeps policy independent of dynamic health/exclusion/affinity state.

### 8.3 Safe rule

Pseudo-code:

```go
func ValidateExecutionComposition(
    sel *Selector,
    classes BackendExecutionResolver,
    policy ExecutionCompositionPolicy,
) error {
    if policy == ExecutionCompositionUnrestricted || IsDirectPrimary(sel) {
        return nil
    }

    for each primary reachable in sel {
        class, configured := classes.ResolveBackendExecution(primary.Backend)
        if !configured {
            continue // preserve existing unknown-backend authority
        }
        if class != lipsdk.BackendExecutionInference {
            return UnsafeExecutionCompositionError{...}
        }
    }
    return nil
}
```

Important behavior:

- configured `agent_runtime` in composition -> deny;
- configured `unknown` in composition -> deny;
- explicit inference only -> allow;
- absent backend -> do not manufacture “unsafe class”; existing unknown-backend validation/open path remains authoritative;
- no dynamic branch selection occurs before validation.

The implementation may aggregate all offending leaves for diagnostics if bounded, but only one is required to reject.

### 8.4 Nested traversal

The walker must include:

- top-level `Primary`;
- `WeightedBranch.Target`;
- every `ParallelBranch.Target`;
- `WeightedBranch.Parallel` nested executor groups used by thinker hybrids;
- every failover alternative.

Use one AST traversal helper so validation and tests do not grow separate shape switches.

## 9. Semantic Preflight Sequence

Create/reuse one semantic-preflight sequence rather than three subtly different paths.

Conceptual sequence:

```text
raw selector
   |
   v
aliases.Resolve
   |
   v
Parse
   |
   v
ApplyModelOnlyBackends
   |
   v
Reject unresolved model-only
   |
   v
(optional entry-point authority) RejectUnknownBackends
   |
   v
ValidateExecutionComposition  <-- new pure policy
   |
   v
request-only dynamic work
```

`RejectUnknownBackends` remains optional because current runtime request behavior may defer missing-backend handling differently from admin preflight. The shared abstraction should not change that brownfield semantic accidentally.

A suitable API shape is either:

```go
CompileSelector(...)
ValidateExecutionComposition(...)
```

used in a small shared preflight owner, or:

```go
CompileAndValidateSelector(raw, CompileOptions{...})
```

provided the latter remains pure and does not become a generic policy service bag.

The design favors keeping `CompileSelector` focused and adding a separate pure validator plus a small composition-owned preflight wrapper where reuse removes drift.

## 10. Request Execution Integration

Current `buildRoutePlan` order is:

```text
CompileSelector
BindNativeModelIDs
resolveAffinityKey
loadInterleavedState
initialize route plan
...
authorize billing
open attempts
```

New order:

```text
CompileSelector
ValidateExecutionComposition
BindNativeModelIDs
resolveAffinityKey
loadInterleavedState
initialize route plan
...
authorize billing
open attempts
```

The executor's `RoutingRuntime` receives:

- generation execution policy;
- immutable backend execution resolver/view.

Rejection therefore happens before:

- weighted RNG/planner selection;
- `[first]` consumption;
- affinity planner mutation/selection;
- interleaved state access used for route planning;
- attempt/B-leg allocation;
- billing authorization;
- backend `Open`;
- connector execution.

No stream has been created, so the normal post-output rule is untouched.

## 11. Configured Default Route and Alias Behavior

### 11.1 Default route

Generation assembly shall compile and semantically validate the effective configured/default selector using the candidate generation's:

- alias resolver;
- default backend;
- configured backend identities;
- execution-class view;
- execution-composition policy.

An unsafe static default fails candidate generation build/publication with an actionable config error.

### 11.2 Aliases

Aliases are regexp rewrites and cannot be exhaustively enumerated. Runtime/admin preflight therefore validates the **expanded** selector returned by the alias resolver.

Example:

```yaml
model_aliases:
  - pattern: "^cheap$"
    replacement: "cursor-work:auto|openai:gpt-x"
```

A client supplying `cheap` is rejected in safe mode if `cursor-work` is an agent runtime even though the raw input contains no `|`.

## 12. A-Leg Route Override Integration

The existing `generationSelectorValidator` already has:

- aliases;
- default backend;
- known backend set.

Extend its generation-bound state with:

- execution resolver/view;
- execution-composition policy.

Validation order:

```text
CompileSelector
RejectUnknownBackends
ValidateExecutionComposition
```

Only after this returns nil may the route-override service commit the mutation.

This preserves the existing store's atomicity and prevents unsafe override persistence under the generation that accepted the write.

Persisted state remains a raw selector. Reload does not rewrite it.

If generation N+1 changes a class or switches `unrestricted -> safe`, an override accepted under N can become illegal. A later turn under N+1 fails request preflight. It is not automatically cleared because mutation of operator state is not a side effect of config reload.

## 13. Failover Semantics

### 13.1 Existing inference rule remains

For inference backends, existing core rules remain authoritative:

- recoverable failover only before client-visible output;
- no transparent failover after output commitment;
- parallel losers cancel;
- lineage remains core-owned.

### 13.2 Agent runtime adds an earlier legality boundary

For agent runtimes, safe policy rejects the **selector composition before first dispatch**, because an agent runtime can mutate external state before visible output.

This is a selector legality rule, not a change to stream commitment.

No runtime attempt tries to detect whether an agent actually performed a side effect. Such detection would be incomplete and too late.

## 14. Unknown Metadata Compatibility

Three states must not be collapsed:

```text
backend absent from generation
    -> existing unknown/missing backend semantics

backend configured, class unknown
    -> direct allowed; safe composition denied

backend configured, class inference/agent_runtime
    -> normal explicit policy
```

This provides a conservative migration for third-party plugins:

- old plugin manifests keep working for direct routes;
- operators can use `unrestricted` temporarily;
- updating the plugin manifest/registration to `inference` enables safe composition;
- an old undeclared agent connector cannot silently be treated as inference.

Official project-owned backends must not rely on unknown after migration.

## 15. Error Contract and Frontend Mapping

Add a routing-owned sentinel/type, conceptually:

```go
var ErrUnsafeExecutionComposition = errors.New(
    "routing: unsafe backend execution composition",
)

type UnsafeExecutionCompositionError struct {
    Composition string
    BackendID   string
    Class       lipsdk.BackendExecutionClass
    Policy      ExecutionCompositionPolicy
}
```

Requirements:

- `errors.Is(err, ErrUnsafeExecutionComposition)` works;
- message is bounded;
- no full selector;
- no prompt/workspace/tool/MCP details;
- identifies direct routing as supported and operator opt-out where appropriate.

Example diagnostic:

```text
routing: unsafe backend execution composition:
failover references backend "cursor-work" with execution class "agent_runtime";
direct routing is supported; policy is "safe"
```

Frontend mapping should reuse the existing execution-error adapter and classify this as invalid client routing / HTTP 400 family, not retryable upstream failure.

No canonical event or `lipapi.Call` field is added.

## 16. Diagnostics and Observability

Add only bounded fields where existing route diagnostics support them:

- `execution_composition_policy`;
- `composition_rejected` boolean/category;
- offending execution class;
- bounded configured backend ID if existing route diagnostics already expose backend IDs.

Do not:

- emit raw selector as a metric label;
- emit workspace/agent/tool/MCP state;
- mark the backend unhealthy;
- penalize candidate health due to policy rejection.

Protected inspect/admin surfaces may expose configured execution class as part of backend inventory if that can be derived without a new public compatibility obligation. This is optional unless needed to make an operator rejection actionable; the typed error is the minimum required surface.

## 17. Reload and In-Flight Isolation

Execution class and policy are generation values.

Scenario:

```text
Generation N: unrestricted
Turn A starts and builds route plan
Reload builds N+1: safe
N+1 publishes
Turn A continues using N route plan
Turn B starts under N+1 and safe-validates
```

No in-flight route plan is reparsed or cancelled.

Likewise a manifest/factory classification change becomes visible only through the generation assembled from that metadata. The process does not mutate class maps behind a published executor.

## 18. Security and Trust Analysis

This change reduces accidental side-effect duplication but is not a sandbox.

Security properties:

- clients cannot opt out of safe policy;
- class metadata comes from trusted project registration or trusted/verified connector manifests;
- invalid class strings fail closed;
- old missing metadata cannot gain composite privileges;
- core does not trust provider-supplied runtime responses to self-classify;
- no raw selector/private agent state is needed for the decision;
- no dynamic code loading or new scripting language is introduced.

An operator choosing `unrestricted` explicitly accepts legacy behavior and its side-effect risks.

## 19. TDD and Validation Strategy

### 19.1 RED contracts first

Before production changes:

1. execution class/profile validation tests;
2. manifest parsing tests for explicit/omitted/invalid class;
3. pure routing validator table;
4. config policy default/invalid tests;
5. runtime zero-side-effect rejection tests;
6. route-override no-store-mutation test;
7. architecture guard for provider-neutral core.

### 19.2 Pure validator matrix

Named table cases must include:

| Selector shape | Classes | Safe result |
|---|---|---|
| direct | inference | allow |
| direct | agent_runtime | allow |
| direct | unknown | allow |
| weighted | inference + inference | allow |
| parallel | inference + inference | allow |
| failover | inference + inference | allow |
| thinker | inference + inference | allow |
| weighted | agent + inference | deny |
| parallel | agent + inference | deny |
| failover | agent + inference | deny |
| thinker | agent as thinker | deny |
| thinker | agent executor | deny |
| thinker+parallel executor | nested agent | deny |
| weighted/parallel | agent + agent | deny |
| any composite | configured unknown | deny |
| any legacy composite | any | allow under unrestricted |

### 19.3 Brownfield classification proofs

- Codex dual exports have different classes.
- configured instance ID different from factory kind still resolves.
- multiple instances of same factory resolve.
- at least one local/discovered inference connector remains composable.
- OpenCode inference exports are not classified agent-runtime by connector provenance.

### 19.4 Side-effect proofs

Use deterministic fakes/counters rather than sleeps:

- backend Open count remains zero on unsafe request;
- no parallel leg starts;
- billing authorization not invoked;
- weighted-first session state unchanged;
- affinity state unchanged;
- interleaved planning store is not consulted after unsafe compile;
- route-override store Replace not invoked on invalid PUT.

### 19.5 Reload proof

Hold a turn after generation binding, publish a generation with changed policy/class view, then prove:

- old turn is unaffected;
- next turn uses new policy;
- stored raw override is unchanged.

### 19.6 Architecture proof

Add guards that prevent:

- concrete connector imports in routing/runtime policy;
- provider-name switch/list used as class authority;
- execution class in canonical `pkg/lipapi`;
- new backend-plugin runtime protocol feature solely for execution class.

## 20. Compatibility and Migration

### Project-owned integrations

All official registrations/manifests migrate to explicit class in the same implementation. This avoids the standard distribution accidentally depending on unknown.

### Third-party integrations

Old registrations/manifests:

- continue to parse/register;
- effective class is unknown;
- direct routes work;
- safe composition requires metadata migration;
- `unrestricted` is a temporary compatibility path.

### Client behavior

- direct routing strings unchanged;
- inference-only composite strings unchanged;
- agent-runtime composite strings that previously ran are intentionally rejected by default;
- operator can explicitly restore legacy behavior.

This is a deliberate safety-default behavior change and must be documented in release/operator notes.

## 21. Alternatives Rejected

### Hard-code “heavy” backends in core

Rejected: violates dependency direction and scales as provider/product names change.

### Infer agent runtime from local/process/connector metadata

Rejected: llama.cpp/Ollama/local inference are counterexamples.

### Infer from tool capability

Rejected: normal inference models can use tools; agent runtimes can hide SDK-native tools.

### Put class in `lipapi`

Rejected: topology is not canonical model semantics.

### Ask connector at request time

Rejected: too late, adds I/O/ABI, and makes routing legality dependent on execution.

### Permit agent failover until first output

Rejected: output commitment is not an external side-effect boundary.

### Add granular allow flags immediately

Rejected: no evidence currently justifies the complexity. Start with one understandable invariant and evolve from real requirements.

## 22. Design Decisions

**D1 — Core owns execution-composition policy.**  
Connectors declare facts; core decides selector legality.

**D2 — Execution class is a dedicated registration semantic.**  
Do not overload security, process sharing, capabilities, or provider names.

**D3 — Classification is per export/factory and projected per configured instance.**  
This handles Codex dual exports and arbitrary instance IDs.

**D4 — Missing metadata becomes `unknown`.**  
Direct routes remain compatible; safe composition fails conservatively.

**D5 — `safe` means direct-any-class or composed-all-explicit-inference.**  
This one predicate covers mixed and same-class agent composition.

**D6 — Ordered failover is included in safe-mode denial for agent/unknown.**  
Pre-output does not prove side-effect-free execution.

**D7 — Validate the compiled AST, never raw selector text.**  
Aliases/defaulting occur first; parser syntax remains backend-neutral.

**D8 — Validation happens before dynamic planning and dispatch side effects.**  
No RNG/session/affinity/interleaved/billing/backend work for an unsafe graph.

**D9 — Reuse one generation-bound semantic preflight across runtime, defaults, and admin override writes.**  
Entry points cannot drift.

**D10 — No canonical `lipapi` or backend-plugin runtime ABI change.**  
Factory/install metadata is sufficient.

**D11 — Published generations hold immutable instance-class view and policy.**  
Reload never mutates in-flight route semantics.

**D12 — Typed bounded policy errors map to invalid-request behavior.**  
Do not expose raw selectors/private agent state or mark backends unhealthy.

**D13 — TDD plus architecture ratchets protect both safety and extensibility.**  
Tests must prove no provider-name authority and zero dispatch on rejection.

**D14 — Missing execution metadata never implies inference or unrestricted.**  
Alternate/internal executor construction treats configured unclassified backends as unknown; test-only builders opt ordinary fakes into inference explicitly.

## 23. Requirements Traceability

| Requirement | Primary design sections |
|---|---|
| R1 Explicit classification | 4, 5, 6 |
| R2 Safe policy | 8 |
| R3 Failover/side effects | 13 |
| R4 Instance classification | 7, 17 |
| R5 Pure validation | 8, 9, 10 |
| R6 Entry-point consistency | 9, 11, 12 |
| R7 Opt-out/compatibility | 8, 14, 20 |
| R8 Metadata boundaries | 4, 5, 6, 21 |
| R9 Errors/explainability | 15, 16 |
| R10 TDD/regression | 19 |

## 24. Implementation Boundary Summary

Expected production change zones:

- `pkg/lipsdk` — focused execution profile;
- `pkg/lipsdk/backendplugin/manifest` + strict parser — per-export metadata;
- `internal/pluginreg` / `internal/standardplugins` — registration metadata;
- official `connectors/*/manifest` templates/release-owned metadata — explicit classifications;
- `internal/infra/runtimebundle` — factory->instance projection and generation wiring;
- `internal/core/config` — policy;
- `internal/core/routing` — pure semantic validator/error;
- `internal/core/runtime` — early validation call;
- route-override generation validator — shared preflight;
- frontend execution-error mapping/diagnostics where required;
- tests/archtests/docs.

Explicitly unchanged boundaries:

- selector grammar;
- `pkg/lipapi`;
- provider SDK imports;
- backend-plugin execute/count/finalize gRPC wire protocol;
- B2BUA output commitment;
- ordinary inference routing algorithms.
