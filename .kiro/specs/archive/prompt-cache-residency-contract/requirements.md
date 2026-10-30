# Requirements Document

## Introduction

Go-LIP shall introduce a provider-neutral contract for observing and controlling prompt-cache residency without pretending that all inference caches share one TTL, one cache key, or one refresh operation. The proxy shall treat cache residency as a property of the **effective successful B-leg** after provider-specific request preparation. Core may consume provider-supplied residency facts and later make maintenance-policy decisions, while concrete backends remain the sole owners of provider wire semantics, effective cache identity, credential affinity, and any safe renewal primitive.

This is the foundation for issue #342. It deliberately does **not** implement the idle keep-warm scheduler, tool-trigger policy, budgets, or provider rollout policy; those depend on this contract and belong to the follow-on `prompt-cache-keepwarm-orchestration` spec.

## Boundary Context

- **In scope**: protocol-neutral prompt-cache residency SDK contracts; model/candidate-aware capability discovery; host-only residency observations from ordinary B-leg execution; opaque generation-scoped maintenance handles; explicit backend renew/release control seams; credential/route affinity invariants; cache usage evidence; connector ABI negotiation and mapping; essential-backend and executable-connector conformance tests; bounded memory/security rules.
- **Out of scope**: scheduler/timers; OS-command arming; per-session keep-warm budgets; global/per-session configuration UX; autonomous maintenance execution; a new client-facing canonical operation; changing routing/failover semantics; inventing provider TTLs; persisting prompts, auth tokens, opaque handles, or provider request bodies; adding provider SDK types to core or public canonical contracts.
- **Boundary ownership**: `internal/core` owns provider-neutral consumption and orchestration policy; `pkg/lipsdk` owns stable plugin-facing cache-residency contracts; `pkg/lipsdk/backendplugin` owns executable-connector ABI DTOs/negotiation; backend adapters/connectors own provider semantics and retained provider-local maintenance state.
- **Canonical neutrality**: `pkg/lipapi.PromptCacheKey` remains a request-side routing/cache hint and shall not become cache-residency authority. Residency observations and maintenance controls are host-only sidebands, not canonical stream events.
- **Dependency**: none. The follow-on orchestration spec depends on this contract.

## Requirement 1: Preserve Proxy and Canonical Boundaries

**Objective:** As a maintainer, I want prompt-cache maintenance capability to fit Go-LIP's existing ownership model, so that provider cache optimization does not contaminate canonical request semantics or execution policy.

### Acceptance Criteria

1.1. The system shall keep provider-specific cache TTLs, cache object identifiers, cache breakpoints, request replay shapes, routing headers, and provider SDK types outside `pkg/lipapi` and `internal/core`.

1.2. The system shall not introduce a client-facing `lipapi.Operation` or canonical request/event type for cache maintenance.

1.3. The system shall keep `PromptCacheKey` as request semantics/hinting only and shall not treat the client- or proxy-carried key as proof that an upstream cache exists, is resident, or is renewable.

1.4. The system shall transport cache residency and maintenance evidence outside client-visible canonical event streams, SSE/WebSocket output, tool lifecycle events, and normal completion history.

1.5. The system shall preserve streaming-first execution, existing B2BUA attempt commitment, and the rule that no retry/failover occurs after client-visible output.

1.6. The system shall not route a cache-control operation through ordinary `Executor.Execute`, frontend adapters, feature reactors, tool loops, continuation mutation, normal failover, or normal model routing.

1.7. The system shall add no provider-specific branching to core; provider differences shall enter core only through typed protocol-neutral capability, observation, and control contracts.

## Requirement 2: Model Cache Residency as a Backend Capability

**Objective:** As core orchestration, I want each concrete backend/model route to describe the cache lifecycle it can actually support, so that maintenance decisions do not rely on a global provider-name TTL table.

### Acceptance Criteria

2.1. When a backend can expose prompt-cache behavior, the backend shall provide a model/candidate-aware residency profile through the same capability-resolution layer used for effective backend properties.

2.2. The residency profile shall distinguish observation support from active renewal support so that affinity/usage telemetry can be supported without implying a safe heartbeat primitive.

2.3. The residency profile shall classify lifecycle semantics using protocol-neutral categories that distinguish at least known sliding expiry, fixed resource expiry, provider-stated minimum residency, best-effort/evictable residency, and unknown lifetime.

2.4. If a backend cannot establish a usable expiry or renewal schedule, the profile shall represent that state explicitly rather than substituting a default TTL.

2.5. If provider documentation states only a minimum retention/lifetime, the profile shall not reinterpret that minimum as a deterministic eviction deadline.

2.6. If cache semantics vary by effective model, route mode, region, provider product, or backend family, profile resolution shall use the effective candidate/model context rather than one backend-wide constant.

2.7. The core shall not contain a Cartesian provider-by-model cache behavior matrix; built-in/provider-specific facts required to implement a profile shall remain with the owning backend family or declarative provider profile where that family already supports data-driven specialization.

2.8. Unsupported or unknown active renewal shall degrade to observation-only/no-control capability without failing foreground inference.

## Requirement 3: Observe the Effective Successful B-Leg

**Objective:** As the proxy, I want residency identity to be captured after backend preparation and provider execution, so that later control operations target what the provider actually used rather than reconstructing identity from A-leg hints.

### Acceptance Criteria

3.1. When a foreground B-leg yields meaningful prompt-cache residency information, the backend shall emit a host-only residency observation associated with that exact B-leg and configured backend instance.

3.2. The observation shall be derived from the effective provider request/response state after backend rewrites, cache-key derivation, route/account selection, header synthesis, cache breakpoint placement, and other provider-specific preparation.

3.3. The observation shall expose a bounded opaque cache target identifier suitable for equality/correlation without requiring core to understand provider cache keys or object identifiers.

3.4. The observation shall expose a bounded cache-content generation identifier or fingerprint that changes when the backend determines the cacheable prefix/identity generation has materially changed.

3.5. The observation shall carry lifecycle timing/advice only when the backend can state it with the declared lifecycle semantics; absent/unknown timing shall remain absent/unknown.

3.6. The observation shall carry cache evidence with explicit presence, including provider-reported cache-read/cache-write usage when available, without fabricating a hit/miss when the provider omits evidence.

3.7. A cache-key hint, A-leg identifier, client session identifier, proxy authoritative session identifier, response continuation identifier, or transport connection identifier shall not by itself be used as the effective cache target identifier unless the backend explicitly establishes that equivalence.

3.8. If one foreground logical request opens multiple B-legs because of failover or a race, each backend shall report only its own observation; core shall never merge cache identities across attempts by model name alone.

3.9. Losing, cancelled, failed, or superseded attempts may report diagnostic cache evidence, but they shall not implicitly become an active renewable target; target eligibility shall be explicit in the observation/result contract.

## Requirement 4: Separate Session, Cache, Content, and Turn Identity

**Objective:** As a maintainer, I want cache identity to remain independent from proxy session and provider turn state, so that compaction, branching, subagents, and provider-specific continuation rules cannot accidentally alias maintenance targets.

### Acceptance Criteria

4.1. The contract shall model proxy/A-leg ownership separately from the backend-owned cache target identifier.

4.2. The contract shall model cacheable-content generation separately from physical client/provider session identifiers.

4.3. Where a backend intentionally preserves cache affinity across a continuity-preserving physical session rotation, the backend may emit the same cache target/generation after the rotation without requiring core to infer the lineage.

4.4. Where a branch, fork, delegate/subagent, tool child, or unrelated session requires cache isolation, the backend/harness observation shall preserve distinct target or generation identity; core shall not collapse them to a conversation root by heuristic.

4.5. Provider state documented or observed as turn-scoped shall not be retained in a reusable cross-turn maintenance handle unless the provider contract explicitly permits that lifetime.

4.6. Provider continuation identifiers that change normal transcript state shall not be consumed, replaced, or advanced by the residency-observation path.

4.7. If cache lineage is ambiguous, the system shall prefer isolation or no active target over speculative sharing.

## Requirement 5: Use Bounded Opaque Backend-Owned Maintenance Handles

**Objective:** As a security and privacy maintainer, I want core to hold only a small opaque reference to provider-local renewal state, so that prompt bodies and credentials are not copied into a generic scheduler/control plane.

### Acceptance Criteria

5.1. When active renewal is supported, the backend shall return a bounded opaque maintenance handle scoped to the configured backend instance/generation.

5.2. Core shall treat the maintenance handle as an uninterpreted value and shall not deserialize provider request bodies, provider cache object schemas, auth tokens, or provider routing headers from it.

5.3. Provider adapters may retain the minimum provider-local volatile state required to honor the handle, including an exact cacheable request representation when the provider requires replay, but that retained state shall be bounded by count and bytes and shall never include raw credentials that can instead be resolved at execution time.

5.4. The maintenance handle and provider-local retained request state shall not be written to continuity databases, billing journals, audit transcripts, traffic captures, configuration snapshots, logs, or metrics.

5.5. Backend instance close, generation retirement, connector process restart, explicit release, or provider-local eviction of retained control state shall invalidate affected handles.

5.6. The control contract shall provide an idempotent release/forget operation or equivalent lifecycle hook so core can promptly discard superseded/session-ended targets without waiting for provider TTL expiry.

5.7. A stale, unknown, expired, or released handle shall fail as a classified non-fatal cache-control result and shall never fail an unrelated foreground request.

5.8. Credential refresh may replace a token while preserving the same provider account; the backend shall validate provider-required account/tenant/credential affinity using provider-local binding state rather than exposing raw credentials to core.

## Requirement 6: Provide an Explicit Cache-Control Seam

**Objective:** As core orchestration, I want a dedicated cache-control operation that cannot masquerade as user inference, so that provider maintenance cannot mutate normal agent state or re-enter execution policy.

### Acceptance Criteria

6.1. Where active renewal is supported, the backend shall expose an explicit prompt-cache control capability separate from ordinary inference `Open`/`Execute`.

6.2. The cache-control operation shall accept only a previously issued valid backend-owned maintenance handle plus bounded host control metadata required for cancellation/idempotency/accounting; it shall not accept a newly routed model selector.

6.3. The backend shall own the concrete renewal action, including inference touch, zero-output prewarm, cache-resource TTL extension, provider-native cache control, or any future provider-specific primitive.

6.4. The control result shall report whether residency was renewed, remained resident without renewal, was cold/recreated, was unsupported/stale, or failed, with explicit evidence presence rather than assuming HTTP success equals cache renewal.

6.5. A control operation shall not emit client-visible assistant content, tool calls, reasoning, canonical stream events, or normal response-continuation state.

6.6. If the only available provider operation would advance or steal client-visible continuation state, reuse forbidden turn state, require speculative provider semantics, or otherwise risk changing the foreground conversation, the backend shall report active renewal unsupported.

6.7. Control failures shall be isolated from foreground inference and shall not trigger normal route fallback, provider failover, racing, model substitution, or retry-after-output behavior.

6.8. The control seam shall honor `context.Context` cancellation/deadlines and shall not create unowned goroutines or hidden retry loops.

## Requirement 7: Preserve Route, Account, and Generation Affinity

**Objective:** As a proxy operator, I want cache-control work to target the same upstream residency domain as the successful B-leg, so that a maintenance request cannot warm a different cache while charging another account.

### Acceptance Criteria

7.1. A maintenance handle shall be valid only for the configured backend instance/generation that issued it unless an explicit future contract defines safe transfer.

7.2. The backend shall preserve all provider-required cache-affinity dimensions captured during the successful B-leg, including effective native model, provider product/API family, account/tenant, region, endpoint, service tier, downstream provider choice, and cache-affinity identifiers where applicable.

7.3. Core shall not re-run selector parsing, weighted routing, `[first]`/`[thinker]`, race selection, health fallback, model aliases, account balancing, or backend failover when invoking the cache-control seam.

7.4. If required affinity can no longer be satisfied, the backend shall return stale/unavailable rather than silently selecting an alternative account, region, endpoint, downstream provider, or model.

7.5. A configuration-generation change shall not keep an old generation alive solely to preserve a cache-maintenance handle; retirement invalidates the old target and a later foreground observation may establish a replacement.

7.6. Aggregator backends shall not advertise deterministic cache renewal unless they can preserve or control the concrete downstream residency dimensions required by that aggregator/provider contract.

## Requirement 8: Keep Cache Control Accounting Separate and Explicit

**Objective:** As an operator, I want maintenance usage to be measurable without pretending it is a client turn, so that cost/quota effects remain observable and auditable.

### Acceptance Criteria

8.1. When a cache-control operation consumes provider tokens, requests, quota, or billable usage, the backend shall return host-only accounting evidence with explicit presence using the existing accounting authority model or a compatible maintenance-specific sideband.

8.2. Cache-control accounting shall be attributable to the backend instance, effective model, and maintenance operation without creating fake client messages or client-visible usage fields.

8.3. Maintenance accounting shall not be silently merged into the triggering foreground B-leg's usage counters.

8.4. The contract shall distinguish cache-read, cache-write, output, and total usage when the provider exposes those dimensions, while preserving unknown/absent values.

8.5. Metrics/log labels derived from residency/control shall use bounded-cardinality identifiers or coarse categories and shall not expose prompt cache keys, opaque handles, raw model strings of unbounded cardinality, prompts, headers, or credentials.

8.6. This foundation shall not add a new customer billing admission path; the follow-on orchestration design shall decide when maintenance may run under configured budgets/authorities using the accounting evidence defined here.

## Requirement 9: Evolve the Backend Plugin ABI Safely

**Objective:** As a connector author, I want cache-residency support to be optional and negotiated, so that existing executable connectors remain compatible while new connectors can implement the capability independently.

### Acceptance Criteria

9.1. The executable backend-plugin protocol shall advertise prompt-cache residency/control support through an additive feature/minor-version negotiation compatible with the existing fail-closed negotiation rules.

9.2. A connector that does not advertise the feature shall continue to execute ordinary inference unchanged and shall be treated as cache-residency unsupported.

9.3. The wire ABI shall carry only bounded protocol-neutral residency/control DTOs and shall not expose provider SDK types or raw credential material.

9.4. Host-to-plugin cache-control calls shall be instance-scoped and shall never be accepted before compatible negotiation/configuration.

9.5. Plugin-to-host residency observations and maintenance accounting shall remain host-only sidebands and shall not be converted into canonical model-output events.

9.6. ABI conversion shall preserve explicit presence for optional timing and usage evidence and shall reject malformed/oversized identifiers, handles, or sideband payloads before they reach core orchestration.

9.7. Contract tests shall prove round-trip parity for supported DTOs, optional-feature downgrade behavior, stale-handle classification, and absence of cache sidebands on legacy peers.

## Requirement 10: Prove Cross-Backend Extensibility With TDD and Conformance

**Objective:** As a maintainer, I want the contract proven without a frontend-by-backend Cartesian matrix, so that adding providers remains cheap and safe.

### Acceptance Criteria

10.1. Before implementation, RED tests shall pin lifecycle enum normalization, explicit unknown semantics, handle/identifier bounds, presence handling, and validation failure behavior.

10.2. Before implementation, RED tests shall pin that residency observations never become canonical stream events and that cache-control calls bypass normal inference routing/failover/continuation paths.

10.3. Before implementation, RED tests shall pin same-instance/generation affinity, stale-handle behavior, idempotent release, and absence of persisted/raw credential state.

10.4. Backend-family contract tests shall certify observation-only, renewable, unsupported, stale, and control-failure behaviors through reusable capability tests rather than pairwise frontend/backend tests.

10.5. Connector ABI contract tests shall certify negotiation, DTO bounds, sideband mapping, cancellation, and accounting evidence independently of any real external provider.

10.6. At least one essential backend test double and one executable connector test double shall prove the same residency/control contract through their respective composition paths.

10.7. Architecture tests shall fail if provider-specific cache code or SDK imports enter `internal/core`/`pkg/lipapi`, if a new cache-maintenance canonical operation appears, or if cache-control starts invoking normal route selection.

10.8. Default tests shall require no provider credentials or external network; provider live-validation tests, if added, shall be separately integration-gated.

10.9. The contract shall add no per-request goroutine and no request-hot-path database lookup; collecting a sideband observation shall be bounded work attached to the already selected B-leg.

10.10. If implementation reveals that a provider cannot satisfy the generic contract without exposing provider state to core or weakening foreground semantic isolation, that provider shall remain observation-only/unsupported rather than widening the core abstraction.
