# Brownfield Design Review

## Review Scope

Reviewed `requirements.md`, `gap-analysis.md`, `research.md`, `design.md`, and `tasks.md` against current `main`, focusing on minimum-change architecture, exact/native continuity safety, reasoning-preservation ownership, auxiliary/background lifecycle, exported SDK compatibility, billing/authority, ordinary-text privacy, parser allocation safety, aggregate memory bounds, reload/shutdown, and scalability across a rapidly growing backend set.

## Review Round 1

**Verdict: NO-GO pending correction.**

The initial architecture was directionally sound but widened brownfield surfaces more than necessary.

### Blocker 1: Replay semantic permission was over-designed as new candidate ABI metadata

Initial design proposed extending `lipapi.ReasoningReplaySupport` with semantic-text permission and carrying it through additional metadata.

**Correction:** use one conservative canonical artifact/dialect classifier. Plain canonical historical reasoning text may be semantic; OpenAI Responses exact items, Anthropic signed/redacted/opaque reasoning, native/unknown/malformed structures fail closed. Destination still requires existing `ReasoningReplaySupport` to represent the original dialect. Add a backend semantic ABI only if implementation evidence proves canonical semantics insufficient.

### Blocker 2: Compressor route inheritance was unnecessary and underspecified

Primary-route inheritance would require reconstructing selector semantics from backend/model or widening metadata solely for this feature.

**Correction:** v1 requires an explicit independent `compression.route`. Inherit-primary-route is out of scope.

### Blocker 3: Optional pending state needed the same safety posture as surrogates

A surrogate was prohibited from evicting an authoritative original, but pending references could also accumulate.

**Correction:** separately bound pending and surrogate state; optional-state exhaustion skips the optimization rather than evicting authoritative originals.

## Review Round 2

**Verdict: GO after correction.**

The corrected design was minimal enough to proceed to task generation:

- no new backend semantic ABI;
- no primary-route inheritance;
- original reasoning remains authoritative;
- background inference reuses generic auxiliary infrastructure;
- shadow mode precedes active replay;
- destination representability is revalidated at restore time;
- exact/native/signed/opaque reasoning is excluded structurally;
- no provider-by-provider Cartesian matrix.

## Review Round 3: CodeRabbit Cross-Check

CodeRabbit raised five unresolved findings after PR submission. Each was verified against the current spec and brownfield source. **All five were real and warranted correction.**

### Finding 1: Raw compressor response lacked a pre-decode byte limit

The prior design had `MaxOutputTokens` and `MaxSurrogateBytes`, but neither guarantees a local allocation bound over the complete raw response before JSON decoding. A provider can ignore token guidance, and decoded surrogate bytes do not bound JSON/schema overhead or a maliciously large raw response.

**Correction applied:**

- add `MaxOutputBytes` as a distinct raw collected-text limit;
- require fragment-by-fragment byte counting before full string materialization/JSON decoding;
- retain scheduler `MaxResultBytes` only as an outer defense-in-depth ceiling;
- add RED oversized-raw-response tests proving decode is never reached after the configured byte limit.

**Verdict:** valid major finding; fixed.

### Finding 2: Adding Poll directly to exported `BackgroundClient` would break source compatibility

Current `pkg/lipsdk/auxiliary.BackgroundClient` is exported and has three historical methods. Adding `Poll` as a required fourth method would break external implementations.

**Correction applied:**

- keep `BackgroundClient` method set unchanged;
- add a separate optional `BackgroundPoller` capability/result-state contract;
- require the standard scheduler used by this feature to implement both;
- add compile/source-compatibility tests using an external-style three-method implementation.

This is smaller and safer than an adapter that silently changes historical interface expectations.

**Verdict:** valid major finding; fixed.

### Finding 3: Per-session optional limits did not bound aggregate state across many sessions

Per-session pending/surrogate limits can still permit unbounded process/feature-instance growth if many sessions are created.

**Correction applied:**

- add `MaxPendingTotal` and `MaxSurrogateBytesTotal` at the reasoning-preservation feature-instance level;
- reserve pending capacity before provider submission;
- maintain aggregate counters atomically with attach/delete/expiry/eviction;
- reject optional state on aggregate exhaustion without evicting originals;
- add multi-session/race tests.

A feature-instance hard bound is the minimal memory-safety authority for v1. Account-specific optional-state quotas remain a possible product follow-up, not a prerequisite.

**Verdict:** valid major finding; fixed.

### Finding 4: Final review blurred model-visible data and trusted auxiliary metadata

The earlier privacy wording could be read as saying session/account identifiers are absent from the entire auxiliary request. That is inaccurate: auxiliary execution intentionally carries role, visibility, parent lineage, and cloned principal/scope as trusted control-plane metadata.

**Correction applied:**

- explicitly separate `auxiliary.Request` envelope/execution-context metadata from canonical child `Call.Messages`;
- preserve role/visibility/lineage/principal-scope for authorization, routing, correlation, generation ownership, and billing;
- prohibit copying those values into model-visible compressor payload or content-bearing telemetry;
- add prompt/envelope separation tests.

**Verdict:** valid security wording/contract finding; fixed.

### Finding 5: Semantic-text eligibility did not establish ordinary-text privacy

A structurally compressible reasoning string can still contain credentials, personal data, proprietary code, or residency/retention-constrained content. Detached/private execution is not equivalent to data-processing approval.

**Correction applied:**

- define a narrow feature-scoped trusted compression-egress decision with allow/redact/deny behavior;
- make explicit route selection insufficient by itself to establish consent/data-processing approval;
- require policy to cover applicable retention, residency, consent/legal-basis, and provider-processing constraints;
- reuse existing trusted secret/redaction authority when available instead of inventing another heuristic detector;
- deny/fail-open to original if required redaction cannot be performed;
- apply redaction before input budgeting and provider submission;
- add sensitive ordinary-text allow/redact/deny/missing-policy/mismatch tests.

This does **not** create a generic compliance platform; it adds only the narrow trusted egress seam necessary for this feature.

**Verdict:** valid major privacy finding; fixed.

## Corrected Architecture Review

### Ownership

**PASS.** `reasoning-output-preservation` remains sole owner of original capture/store/replay. Compression is an optional sub-lane. Generic core retains routing/B2BUA/billing; provider-native continuity stays provider-owned.

### Canonical neutrality

**PASS.** Compressibility uses canonical reasoning dialect/structure. No provider-name/model-name policy branch is introduced.

### Streaming lifecycle

**PASS.** Original capture remains final surfaced stream observation. Compressor work starts only after `success_released` original append and never delays response release.

### Exported SDK compatibility

**PASS after Round 3.** Historical `BackgroundClient` remains source-compatible; non-blocking polling is an optional additive capability.

### Resource bounds

**PASS after Round 3.** Bounds now exist at four relevant layers:

1. generic scheduler queue/result outer bounds;
2. compressor input token/byte bounds;
3. raw compressor `MaxOutputBytes` before decode and decoded `MaxSurrogateBytes` after decode;
4. optional-state per-session plus feature-instance aggregate pending/surrogate bounds.

### Storage safety

**PASS after Round 3.** Optional reservations/surrogates cannot evict an otherwise-retained authoritative original merely to make room. Aggregate exhaustion skips compression.

### Privacy/security

**PASS after Round 3.** Representation eligibility is separate from data-egress approval. Trusted control-plane metadata remains available to authorization/billing but is excluded from model prompt/content telemetry. Required sanitization happens locally before provider submission.

### Billing/accounting

**PASS.** Compressor work follows ordinary auxiliary admission, routing, metering, provider-cost, and terminal settlement, attributed to the originating principal by default.

### Retry/failover safety

**PASS.** Compression failures are optimization-local and cannot become primary retry/failover authority after downstream output commitment.

### Minimality

**PASS.** The design still does not add:

- another provider client;
- another worker runtime;
- another transcript DB;
- another billing ledger;
- provider-specific core branches;
- a backend semantic ABI in v1;
- a generic privacy platform;
- synchronous waits/callback polling machinery;
- a provider Cartesian compatibility matrix.

## Final Design Verdict

**GO after review-hardening corrections.**

The CodeRabbit findings exposed five missing safety/compatibility constraints rather than a fundamental architectural flaw. The corrected design preserves the original dependency order and remains a follow-up extension. Active semantic replay is still prohibited until the original-first shadow submission and non-blocking adoption path, including privacy, raw-bound, and aggregate-budget contracts, is green.