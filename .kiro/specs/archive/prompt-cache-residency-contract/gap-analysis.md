# Brownfield Requirements Gap Analysis

## Scope

This analysis compares the `prompt-cache-residency-contract` requirements against current Go-LIP `main`. It is intentionally limited to the foundation contract. Scheduling, OS-command idle detection, renewal budgets, and operator policy belong to the follow-on orchestration spec.

## 1. Current-State Investigation

### Existing assets that should be reused

| Existing asset | Current role | Relevance |
|---|---|---|
| `pkg/lipapi.Call.PromptCacheKey` and semantic extension carrier | request-side protocol-neutral cache-affinity hint | Reuse as foreground request semantics only; do not promote to residency authority |
| `pkg/lipapi.SessionRef` | proxy/client session and continuity identifiers | Provides A-leg/session correlation, but must remain distinct from provider cache identity |
| `pkg/lipapi.ToolCategory` | name-derived tool classification | No foundation change required; consumed by follow-on orchestration only |
| `internal/core/execbackend.Backend` | executor-facing provider-neutral backend envelope | Natural core-side home for optional effective residency profile/control functions |
| `execbackend` model/candidate-aware resolvers | effective caps/transport/replay/dialect lookup | Reusable pattern for candidate/model-aware cache profile resolution |
| `pkg/lipsdk/backendplugin.ResolvedProfile` | executable connector model-aware capability profile | Natural additive ABI profile surface |
| `backendplugin.ConfiguredInstance` plus optional `TokenCounter` / `BillingFinalizer` | normal Execute plus explicit optional control operations | Strong precedent for an optional prompt-cache control interface instead of fake inference |
| `backendplugin.ServerFrame.Accounting` / accounting evidence | host-only non-canonical provider usage evidence | Strong precedent for host-only residency/accounting sidebands |
| backendplugin feature/minor negotiation | additive executable-connector compatibility | Reuse to gate new DTOs/control RPCs without breaking old connectors |
| backend instance/generation lifecycle | configured resources close with generation retirement | Natural lifetime boundary for opaque maintenance handles |
| provider adapters/connectors | final request rewrite, auth/account selection, headers and wire semantics | Only layer that can reliably know effective cache identity and safe renewal behavior |
| `internal/providerprofiles` | data-driven compatible-provider specialization | Can carry declarative cache facts only where the underlying compatible-family adapter can actually enforce/observe them |
| contract TCKs and archtests | backend/connector certification without FE×BE Cartesian matrix | Reuse for cross-backend residency conformance |

### Existing constraints

1. `pkg/lipapi` is deliberately provider-neutral and client-visible canonical semantics must remain small.
2. Core cannot import provider SDKs or concrete plugins.
3. Optional connectors are out-of-process gRPC peers and require versioned negotiated DTOs.
4. `execbackend.Backend.Open` returns the canonical managed stream, so provider-only observations cannot be represented as ordinary canonical events without violating the API boundary.
5. Immutable configuration generations own backend instances. A generic cache target must not prolong a retired generation.
6. Accounting/billing and canonical output are intentionally separated; maintenance evidence must preserve that separation.

## 2. Requirement-to-Asset Map

| Requirements | Technical need | Existing asset | Gap |
|---|---|---|---|
| 1.1-1.7 | non-canonical cache-residency/control plane | canonical/plugin boundary rules; accounting sidebands | **Missing:** cache-specific host-only contract; **Constraint:** no `lipapi` expansion |
| 2.1-2.8 | model/candidate-aware lifecycle profile | `execbackend` resolvers; backendplugin `ResolvedProfile` | **Missing:** residency profile/lifecycle taxonomy in both in-process and ABI projections |
| 3.1-3.9 | post-preparation effective residency observation | backend adapters know final request; usage evidence exists | **Missing:** observation sideband and drain/mapping path |
| 4.1-4.7 | distinct session/cache/content/turn identity | SessionRef, PromptCacheKey, provider-specific session logic | **Missing:** explicit cache target/generation concepts; **Constraint:** no core lineage heuristics |
| 5.1-5.8 | bounded opaque handle + plugin-local state + release | backend instance lifecycle | **Missing:** handle contract, bounds, release/forget seam and adapter-local retention policy |
| 6.1-6.8 | explicit non-inference renew operation | optional TokenCounter/BillingFinalizer pattern | **Missing:** prompt-cache controller interface/RPC and result taxonomy |
| 7.1-7.6 | same route/account/generation affinity | selected backend instance/candidate already known to executor | **Missing:** control invocation binding and fail-closed affinity contract |
| 8.1-8.6 | maintenance usage accounting | `AccountingEvidence` and host-only plane | **Partial:** reusable evidence model exists; **Missing:** cache-control operation attribution/separate consumption rules |
| 9.1-9.7 | additive connector ABI | negotiated features/minor + converter TCKs | **Missing:** feature/minor, DTOs, RPC/control mapping, legacy downgrade tests |
| 10.1-10.10 | TDD/TCK/architecture gates | testkit contract, backendplugin contracttest, archtest | **Partial:** frameworks exist; **Missing:** cache-residency conformance kit and guardrails |

## 3. Key Brownfield Gaps and Requirement Corrections

### Gap A — `PromptCacheKey` is already present, but it is the wrong authority

The current canonical call intentionally defines `PromptCacheKey` as a request hint, not trajectory control. Treating that value as the keep-warm target would lose backend-generated keys, downstream aggregator selection, account/region affinity, cache breakpoints, and harness-specific lineage behavior.

**Requirements correction applied:** 1.3, 3.2-3.7, and 4.1-4.7 explicitly require observation after final backend preparation and prohibit reconstruction from A-leg/cache-key hints.

### Gap B — a control operation cannot safely be modeled as `Execute`

The existing executor path owns route selection, failover/race semantics, canonical output, tool/reactor behavior, continuation state, and foreground usage. A synthetic inference-shaped heartbeat would couple maintenance to all of those semantics and recreates known OSS failure modes where keepalive calls re-enter agent loops.

**Requirements correction applied:** 1.6 and 6.1-6.8 require a separate optional backend control seam and semantic isolation.

### Gap C — host-owned opaque request clones would violate privacy and scaling goals

Some providers may require replaying a stable cacheable prefix, but copying provider-ready request bodies into core would expose prompts/provider fields and create a generic retention problem. Out-of-process connectors already provide a better ownership boundary: connector-local volatile state referenced by a small handle.

**Requirements correction applied:** 5.1-5.8 make the handle bounded and generation-scoped while retaining provider-ready state at the adapter edge with explicit release.

### Gap D — backend instance/generation lifetime is a hard cache-target boundary

The runtime already retires immutable generations. Keeping an old backend generation alive for a background optimization would complicate reload ownership and resource retirement.

**Requirements correction applied:** 5.5 and 7.5 require retirement to invalidate handles instead of extending generation lifetime.

### Gap E — existing accounting sidebands are reusable, but maintenance must not become a fake client turn

The backend plugin ABI already distinguishes host-only accounting from canonical events. That pattern should be reused for residency/control evidence rather than adding usage events to the client stream.

**Requirements correction applied:** 1.4, 8.1-8.6, and 9.5 make this separation explicit.

## 4. Implementation Approach Options

### Option A: Extend canonical `pkg/lipapi` with cache events and a maintenance operation

**Shape:** add cache lifecycle/caching event types and represent refresh as a canonical call.

**Advantages**
- One request/event vocabulary everywhere.
- Little new sideband plumbing.

**Disadvantages**
- Violates canonical/client semantics by making a provider optimization a cross-protocol trajectory concept.
- Invites provider cache IDs/TTL semantics into core.
- Risks routing, failover, continuation and hook re-entry.
- Makes frontend behavior and extension capability negotiation unnecessarily aware of maintenance.

**Assessment:** reject.

### Option B: Put all cache logic in a new core cache manager with provider tables

**Shape:** core maps backend/model to TTL and generates synthetic provider-neutral refresh calls.

**Advantages**
- Central implementation appears simple initially.
- Scheduler later has direct TTL data.

**Disadvantages**
- Core cannot know post-rewrite cache identity, account/region/downstream routing, provider request replay shape, or turn-state restrictions.
- Provider/model matrix grows combinatorially and becomes stale.
- Requires provider branching or lossy generic assumptions in core.

**Assessment:** reject.

### Option C: Hybrid provider-owned capability + core-owned policy

**Shape:** add a small protocol-neutral residency/control contract to plugin SDK and executable ABI; adapters emit effective observations and own opaque control state; `execbackend.Backend` exposes the effective capability to core; later orchestration consumes it.

**Advantages**
- Matches existing Go-LIP hexagonal boundary and optional-operation patterns.
- Provider semantics remain at the adapter edge.
- Works for direct providers, aggregators, explicit cache resources, subscription backends, and unknown/best-effort caches without pretending they are equivalent.
- Old connectors degrade cleanly to unsupported.
- Allows reusable contract TCKs rather than Cartesian integration tests.

**Disadvantages**
- Requires additive SDK + gRPC ABI work before active keep-warm behavior.
- Requires explicit sideband propagation through stream/connector wrappers.
- Provider implementations may need bounded local target stores.

**Assessment:** preferred.

## 5. Recommended Design Direction

Use **Option C** with the following boundaries:

1. Define a small public plugin-facing prompt-cache residency package/contract containing lifecycle profile, observation, bounded target/generation IDs, opaque handle, renew/release input/result, evidence presence, and validation rules.
2. Extend `execbackend.Backend` additively with model/candidate-aware cache profile resolution and optional cache-control functions. No core package imports a provider implementation.
3. Add a host-only observation drain/source seam to foreground backend streams. The stream still yields only canonical `lipapi.Event` values to the executor/client path.
4. Extend executable connector negotiation/DTO/RPC mapping additively. Legacy peers advertise no feature and remain unchanged.
5. Keep provider-ready renewal material inside the concrete adapter/connector instance behind a bounded opaque handle. Bind it to that instance/generation and explicitly release it.
6. Reuse accounting evidence semantics for maintenance usage, but keep maintenance operations separately attributable and non-canonical.
7. Build one reusable contract TCK with observation-only, renewable, unsupported, stale, affinity-failure, and accounting scenarios. Provider integrations satisfy the TCK independently.

## 6. Effort and Risk

- **Effort: L (1–2 weeks)** — new stable plugin contracts, executable ABI evolution, stream sideband plumbing, composition mapping, and reusable conformance coverage touch several architectural seams even though each change is additive.
- **Risk: High initially, reducible to Medium** — the main risk is accidental coupling of maintenance to canonical execution or generation lifetime. Additive optional interfaces, strict bounds, legacy-negotiation tests, and explicit no-`lipapi` architecture gates materially reduce the risk.

## 7. Research Items Carried Into Design

- Verify the minimum set of lifecycle categories that covers direct TTL caches, explicit cache resources, best-effort caches, and minimum-residency APIs without encoding vendor names.
- Verify the exact host-only sideband insertion point for essential backends and executable connector wrappers so observation capture cannot alter canonical stream ordering.
- Verify how maintenance accounting should reuse existing `AccountingEvidence` authority/plane values without creating a second financial seam.
- Specify ABI minor/feature evolution and bounds consistently with existing semantic-extension/accounting feature negotiation.
- Treat provider-specific active-renewal support as implementation evidence, not a requirement that every backend must support renewal.
