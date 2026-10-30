# Final Spec Review

## Verdict

**GO as a specification after CodeRabbit hardening; not approved for implementation yet.**

The full Kiro SDD workflow has completed through tasks generation, brownfield correction, and post-PR review hardening. `spec.json` approvals intentionally remain false and `ready_for_implementation` remains false pending maintainer review.

## Supersession Decision

This spec is a **follow-up**, not a superseding specification.

It does not supersede or reopen:

- `reasoning-output-preservation`;
- `reasoning-preservation-e2e-validation`;
- `openai-responses-reasoning-preservation`;
- `openai-codex-native-compaction`;
- `compaction-continuity-preservation`.

Those completed specs still describe required behavior. This spec extends reasoning preservation with one optional semantic-surrogate lane and reuses generic auxiliary infrastructure produced by later work.

There was no previous dedicated Kiro implementation spec for issue #369, so there is no #369 spec to supersede.

## Workflow Audit

Completed:

1. spec initialization;
2. EARS-style requirements generation;
3. brownfield requirements/code gap analysis;
4. requirements reconciliation;
5. implementation research and architecture decisions;
6. initial design generation;
7. brownfield design validation — **NO-GO** on unnecessary semantic ABI, route inheritance, and optional-state breadth;
8. requirements/research/design correction;
9. corrected design validation — **GO**;
10. TDD-first task generation;
11. initial final scope/traceability review — **GO for maintainer review**;
12. CodeRabbit cross-check of five major findings;
13. requirements/design/tasks/reviews hardening for all five validated findings;
14. final post-review consistency assessment — **GO**.

## CodeRabbit Findings Assessment

All five unresolved CodeRabbit findings were verified as real.

### 1. Missing raw compressor-result byte limit — accepted

The previous spec bounded requested output tokens and decoded surrogate bytes but did not explicitly bound the complete raw collected response before JSON decode.

The corrected spec now requires:

- `max_output_bytes` distinct from `max_output_tokens` and `max_surrogate_bytes`;
- fragment-by-fragment local byte counting before full string construction/JSON decode;
- rejection as `raw_oversize` before decoding once the limit is exceeded;
- tests where a syntactically valid JSON tail exists only beyond the configured raw limit.

This closes a local allocation/parser DoS gap.

### 2. `BackgroundClient` source compatibility — accepted

The previous design proposed `Poll` as though it could simply be added to exported `auxiliary.BackgroundClient`. That would break external implementations satisfying the historical three-method interface.

The corrected design keeps `BackgroundClient` unchanged and adds a separate optional `BackgroundPoller` capability. Standard scheduler composition used by compression must implement both, while historical external implementations remain source-compatible.

### 3. Missing aggregate optional-state budget — accepted

Per-session limits alone do not bound a process/feature instance if many sessions are created.

The corrected spec adds:

- feature-instance `MaxPendingTotal`;
- feature-instance `MaxSurrogateBytesTotal`;
- reservation before provider submission;
- atomic counter maintenance through attach/delete/expiry/eviction;
- multi-session aggregate-exhaustion and race tests.

Optional budget exhaustion skips compression and never evicts authoritative originals.

### 4. Model-visible vs control-plane metadata wording — accepted

The corrected privacy boundary is explicit:

- `Role`, `Visibility`, detached session mode, parent lineage, and cloned trusted principal/scope remain **control-plane metadata** for authorization, routing, correlation, generation ownership, and billing;
- they are excluded from child canonical `Call.Messages`, compressor JSON payload, and content-bearing telemetry;
- documentation no longer claims session/account identity is absent from the entire auxiliary request/execution context.

### 5. Ordinary semantic text still needs data-processing policy — accepted

`SemanticText` is a representation property, not a sensitivity classification. Preserved reasoning may contain secrets, personal data, proprietary code, or residency/retention-constrained content.

The corrected design therefore requires a narrow trusted compression-egress decision before any out-of-trust-boundary submission:

- `allow`;
- `redact_then_allow`;
- `deny`.

The decision covers applicable operator retention, residency, consent/legal-basis, and provider-processing constraints. Existing trusted secret/redaction policy is reused where available. If policy requires redaction but no trusted sanitizer can satisfy it, compression is denied and the original remains authoritative.

This is deliberately feature-scoped; it does not introduce a generic compliance platform.

## Final Architecture Assessment

### Original reasoning authority

**PASS.** Original `TurnArtifact.Reasoning` is always authoritative. Compression state is optional and cannot become the only retained copy.

### Exact/native continuity

**PASS.** OpenAI Responses exact items, Codex encrypted/native state, Anthropic signed/redacted/opaque thinking, unknown/malformed forms, and exact-bearing structures never enter semantic compression.

### Capture ordering

**PASS.** Compression cannot start until the surfaced winner reaches `success_released` and the original artifact append succeeds.

### Optional-state memory safety

**PASS after hardening.** Pending and surrogate state are bounded separately from authoritative reasoning at both per-session and feature-instance aggregate levels.

### Background execution

**PASS after hardening.** Existing bounded process scheduler is reused. Non-blocking adoption uses an additive optional poll capability rather than breaking exported `BackgroundClient`.

### Parser/allocation safety

**PASS after hardening.** Raw response bytes are bounded before JSON decode, then decoded surrogate bytes and savings are validated separately.

### Privacy/security

**PASS after hardening.** Ordinary text requires explicit data-egress approval/redaction-or-denial. Model-visible content is separated from trusted auxiliary control-plane metadata. Content-bearing telemetry remains content-free.

### Billing/accounting

**PASS.** Compressor inference follows ordinary auxiliary admission/routing/B2BUA/usage/provider-cost/settlement and originating-principal attribution. Primary protocol usage remains separate.

### Replay safety

**PASS.** Shadow mode always restores originals. Active mode is explicit and only substitutes independently semantic-text placements after reclassifying the original and verifying destination `ReasoningReplaySupport`.

### Tool/order/native integrity

**PASS.** Surrogate selection can change only eligible textual reasoning payloads. Placement, tool IDs/order, ordinary assistant text, signatures, opaque/native fields, files/images, and reasoning/action/observation ordering remain unchanged.

### Retry/failover

**PASS.** Compression is optimization-local and cannot become primary retry/failover authority after downstream output commitment.

### Backend scalability

**PASS.** Compatibility is driven by canonical semantic/exact fixtures plus existing replay-support contracts, not backend Cartesian matrices.

### Architectural minimality

**PASS.** The design still avoids:

- a second provider client;
- a second generic worker/task runtime;
- another transcript database;
- another billing ledger/pricing engine;
- provider-specific compressor branches in generic core;
- a new backend semantic-permission ABI in v1;
- route inheritance machinery;
- synchronous compressor waits;
- callback polling machinery;
- a generic privacy/compliance subsystem.

## Implementation Order Review

The final task order remains intentionally conservative:

```text
Phase 1  RED exact/disabled/privacy/raw-bound/aggregate/source-compat contracts
Phase 2  canonical classifier + config + optional Poll + bounded store/composition
Phase 3  egress/sanitizer + compressor builder + pre-decode raw bound + strict validator
Phase 4  original-first shadow submission only
Phase 5  non-blocking shadow adoption only
Phase 6  explicit destination-gated active replay
Phase 7  billing/privacy/concurrency/parser/performance/repository certification
```

Backend-visible semantic substitution is impossible before Phase 6.

## Residual Risks

The remaining risks are implementation risks rather than unresolved design gaps:

- semantic compression can lose useful reasoning even when structurally safe; active mode is therefore explicit and should follow shadow evidence;
- provider data-processing guarantees are only as trustworthy as the operator's configured/connected policy authority;
- process-local v1 state is lost on restart, consistent with current reasoning-preservation durability;
- current active request/terminal-pipeline simplification specs may move integration seams before implementation, requiring revalidation against merged `main`;
- additional canonical text dialects should not be enabled without measured evidence.

None requires reopening completed exact/native continuity specs.

## Final Traceability and Scope Gate

The normative `requirements.md`, `design.md`, and `tasks.md` now consistently cover the five review-hardening additions:

1. raw compressor `max_output_bytes` before decode;
2. source-compatible optional `BackgroundPoller`;
3. feature-instance aggregate optional-state budgets;
4. model-visible/control-plane metadata separation;
5. ordinary-text data-egress policy and redaction-or-denial.

The PR remains specification-only. No production code, runtime behavior, configuration defaults, provider adapters, or billing implementation are changed by this branch.

## Final Verdict

**GO for maintainer review.**

The review comments identified genuine omissions, and the spec has been corrected rather than defended. The resulting architecture is safer without materially expanding the core design: original-first, asynchronous, bounded at every layer, source-compatible, privacy-gated, shadow-first, and exact/native continuity preserving.