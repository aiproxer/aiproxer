# Brownfield Design Validation

## Verdict

**GO after contract/lifecycle hardening.** The final design fits Go-LIP's converged provider boundary: core consumes protocol-neutral backend facts and owns orchestration, while concrete backends own effective cache identity and provider control. Initial validation found three material risks—generic prompt retention in core, an observation transport shape that diverged from the existing host-only sideband pattern, and ambiguous maintenance accounting—and the design was corrected before task generation.

This document records the completed implementation and design-validation result. The contract is archived as completed in `spec.json`; implementation evidence is recorded there, and the follow-on orchestration remains a separate dependent feature.

## Critical Issues Found and Applied Corrections

### 1. Generic core retention of provider-ready requests — RESOLVED

**Concern:** An earlier `WarmTarget`/opaque-payload sketch could be implemented as a full provider-ready request stored in core so that a scheduler could replay it later.
**Impact:** That would turn prompt/request retention into a generic core responsibility, leak provider shape across the adapter boundary, complicate privacy/auditing, and scale poorly for large prompts.
**Correction:** The final contract stores only a bounded backend-instance-scoped handle in host/core. Provider-ready renewal material remains bounded, volatile adapter/connector-local state and is explicitly forgotten through an idempotent release operation. Backend/generation close invalidates all handles.
**Traceability:** 1.1-1.7, 5.1-5.8, 7.1-7.5.
**Evidence:** Design sections “Residency Observation”, “Prompt Cache Controller”, and “Backend Target Store”.

### 2. Cache observations risked overloading terminal/canonical framing — RESOLVED

**Concern:** A first design draft considered attaching repeated cache observations directly to the connector terminal frame.
**Impact:** This diverged from the existing exclusive `ServerFrame` shape and from the proven `ServerFrameAccountingEvidence` host-only pattern; it would complicate validation and could accidentally make a provider sideband part of terminal semantics.
**Correction:** The final design adds a dedicated negotiated `prompt_cache_observation` sideband frame mirroring accounting evidence. The host wrapper buffers sidebands and exposes them only after a successful terminal; failure/cancellation discards them. No canonical event changes.
**Traceability:** 1.2-1.6, 3.1-3.9, 8.1-8.5, 9.5-9.7.
**Evidence:** Design sections “Foreground Observation”, “In-Process Sideband Contract”, and “Executable Connector Bridge”.

### 3. Maintenance accounting authority was underspecified — RESOLVED

**Concern:** The initial design left open whether cache-control provider usage required a new accounting plane.
**Impact:** Leaving this to implementation could create a third billing seam, merge maintenance cost into foreground usage, or invent inconsistent accounting semantics across essential and executable backends.
**Correction:** The final design reuses the existing `AccountingPlaneProviderBillable` plus existing source/authority semantics. Maintenance uses an operation-specific dedupe/correlation identity and remains separate from foreground B-leg usage. No new customer billing admission or accounting plane is introduced by this foundation.
**Traceability:** 8.1-8.6, 9.5-9.7.
**Evidence:** Design sections “Existing Architecture Analysis”, “Accounting Projection”, and “Error and Degradation Model”.

## Validation Checklist

### Existing architecture alignment — PASS

- Uses `internal/core/execbackend.Backend` as the existing executor-facing provider-neutral capability envelope.
- Uses `pkg/lipsdk` for stable plugin contracts and `pkg/lipsdk/backendplugin` for executable connector ABI.
- Leaves frontends, route grammar, B2BUA commitment, and immutable generation ownership unchanged.

### Core vs plugin ownership — PASS

Core receives only lifecycle/profile/observation/control results. Provider TTL interpretation, final cache identity, request replay shapes, account/region/downstream affinity, provider headers, and SDK types remain in concrete backend adapters/connectors.

### Canonical model neutrality — PASS

No new `pkg/lipapi.Operation`, cache-residency event, or client-visible maintenance field is required. Existing `PromptCacheKey` remains a foreground request hint and is explicitly non-authoritative for residency.

### Streaming and retry invariants — PASS

Normal streaming remains canonical. Cache observations are host-only sidebands. Cache control does not invoke normal `Open`/`Execute`, selector parsing, failover, racing, continuation mutation, or retry-after-output behavior.

### Identity and continuity isolation — PASS

The design separates A-leg/session identity, backend target identity, cacheable-content generation, and provider turn/continuation state. Core performs no compaction/subagent lineage heuristics. Ambiguity degrades to isolation/no active target.

### Generation/lifecycle ownership — PASS

Opaque handles are scoped to one configured backend instance/generation. Reload/retirement invalidates them and cache optimization never pins a retired generation.

### Privacy/security — PASS with mandatory bounds tests

Core does not receive provider-ready request bodies or raw credentials through the maintenance handle. Adapter-local state is volatile/bounded, release is local-forget only, and handles/cache IDs are prohibited from metrics/log labels.

### Executable ABI evolution — PASS

The change is additive: minor 7 plus negotiated feature, dedicated bounded sideband frame, optional control RPC/interface, legacy peers unchanged. Contract tests must verify old-minor and no-feature behavior.

### Accounting — PASS

Existing `provider_billable` evidence remains the sole provider-charge plane. Maintenance attribution is separate from the foreground B-leg and does not create a new customer billing path.

### Extensibility — PASS

The lifecycle taxonomy represents direct TTL caches, fixed cache resources, minimum-residency guarantees, best-effort caches, and unknown lifetimes without vendor names. Renewal support is optional and can remain disabled for providers such as Codex where safe refresh semantics are unproven.

## Design Strengths

1. **The abstraction follows epistemic ownership.** The backend reports only facts it can actually know after provider preparation; core does not guess cache identity or TTL.
2. **Unsupported/unknown behavior is first-class.** A provider can still expose cache affinity/evidence without being forced into a fake active-refresh contract.

## Design-to-Requirement Trace

| Requirement | Validation result |
|---|---|
| 1 Preserve proxy/canonical boundaries | PASS |
| 2 Backend residency capability | PASS |
| 3 Effective B-leg observation | PASS |
| 4 Identity separation | PASS |
| 5 Bounded opaque handles | PASS |
| 6 Explicit cache-control seam | PASS |
| 7 Route/account/generation affinity | PASS |
| 8 Separate accounting | PASS |
| 9 Additive connector ABI | PASS |
| 10 TDD/conformance/extensibility | PASS |

## Final Assessment

**GO for design validation.** The contract is sufficiently precise to implement without inventing provider behavior in core and sufficiently small to support both essential in-process backends and optional executable connectors. Implementation must remain foundation-only: SDK/ABI contracts, sideband/control plumbing, bounded local target state patterns, reference/TCK coverage, and architecture gates. Session scheduling, tool-trigger arming, keep-warm budgets, provider rollout decisions, and autonomous renewal belong exclusively to the chronologically subsequent `prompt-cache-keepwarm-orchestration` spec.
