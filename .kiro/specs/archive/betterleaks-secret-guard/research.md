# Research and Brownfield Gap Analysis

## Summary

- **Feature**: betterleaks-secret-guard
- **Source tracker**: GitHub issue #714, `feat(security): Integrate BetterLeaks SDK as primary secret detector; retain exact matching as defense-in-depth`
- **Repository baseline**: `main` at `29064c0dedb13e76c8d04d04a642b4bd8573abd4`
- **Discovery type**: full brownfield analysis
- **Complexity**: L
- **Risk**: High
- **Selected direction**: preserve the existing secret-guard enforcement envelope and exact matcher, add a private generation-scoped BetterLeaks v2 detector, scan whole logical fragments for heuristic context, project raw findings immediately into secret-safe metadata, and route discovered literal values back through AIProxer's existing exact redaction machinery.

The user-requested configurability is feasible without weakening the current shared-proxy isolation boundary. BetterLeaks should default enabled inside an enabled `secrets-guard` registration because it is the only detector that can discover unknown client-side credentials on a shared server. Local environment auto-discovery should retain its single-user default but be structurally impossible to enable in multi-user mode.

The top-level feature itself should remain opt-in in this tranche. Today `secrets-guard` requires an explicit action and an enabled feature registration. Automatically activating it would require inventing a default enforcement action and would be a separate behavior-breaking product decision unrelated to detector selection.

## Source Set

### Project sources reviewed

- GitHub issue #714 and its implementation/research comments
- `AGENTS.md`
- `.kiro/AGENTS.md`
- `.kiro/steering/product.md`
- `.kiro/steering/tech.md`
- `.kiro/steering/structure.md`
- `.kiro/steering/testing.md`
- `.kiro/rules/ears-format.md`
- `.kiro/rules/gap-analysis.md`
- `.kiro/rules/design-principles.md`
- `.kiro/rules/design-review.md`
- `.kiro/rules/tasks-generation.md`
- `docs/secrets-guard.md`
- `internal/plugins/features/secretguard/config.go`
- `internal/plugins/features/secretguard/runtime_compose.go`
- `internal/plugins/features/secretguard/guard.go`
- `internal/plugins/features/secretguard/engine/source.go`
- `internal/plugins/features/secretguard/engine/catalog.go`
- `internal/plugins/features/secretguard/engine/matcher.go`
- `internal/standardplugins/featurehost/secretguard/compose.go`
- `internal/stdhttp/auth/credential_matcher.go`
- `pkg/lipsdk/secretguard/types.go`
- `pkg/lipsdk/secretguard/execution_config.go`
- `pkg/lipsdk/secretguardhost/binding.go`
- archived pre-OSS secret-guard characterization/spec material

### Upstream BetterLeaks sources reviewed

- BetterLeaks releases: stable `v1.9.0`; v2 release candidate `v2.0.0-rc.1` dated 2026-09-30
- v2 `scan/options.go` and `scan/doc.go`
- v2 migration/config/scanning documentation
- v2 examples for scanner reuse and analysis-free scanning
- v2 configuration hashing support

Relevant current v2 facts confirmed from upstream:

- `scan.WithAllowSignatures()` with zero arguments disables the default `betterleaks:allow` and `gitleaks:allow` suppression markers.
- `scan.WithMinimumConfidence(...)` accepts confidence filtering.
- `scan.WithMaxDecodeDepth(0)` disables recursive decoding; negative values are rejected.
- `scan.WithWorkers(0)` resolves to `4 * GOMAXPROCS` at scanner construction, so AIProxer must not pass its own zero/auto value through unchanged.
- one `scan.Scanner` is designed for concurrent reuse and shares its detection worker limit across concurrent scans.
- `config.Config.Hash()` returns a stable SHA-256 hex digest of the resolved config and is explicitly suitable as one cache/policy identity input.
- v2 separates detection (`scan`) from provider evaluation (`analyze`) and composition (`pipeline`); detection does not require validation/analysis.
- BetterLeaks scanners are silent by default unless a logger is injected.

## Brownfield Current-State Findings

### 1. The existing feature is already an enforcement system, not merely a matcher

`internal/plugins/features/secretguard/guard.go` owns `block`, `redact`, and `log`, call cloning for mutation, request-wide scan limits, fail-closed handling for unsafe JSON tokens, and decision construction. Runtime execution owns pre-dispatch ordering, audit, quarantine, and denial.

**Implication:** replacing the entire feature with BetterLeaks would discard proxy-specific security semantics. #714 should add a detector seam and leave enforcement ownership with AIProxer.

### 2. Exact matching has two non-substitutable roles

The feature-local engine builds an immutable Aho-Corasick catalog from exact known secret values. In single-user mode, those values come from environment/proxy credential inventory. `internal/stdhttp/auth/credential_matcher.go` separately holds the accepted inbound request credential privately and exposes only matcher behavior.

BetterLeaks cannot guarantee detection of arbitrary operator-defined opaque strings. Exact matching therefore remains defense-in-depth even after BetterLeaks is added.

**Requirement repair:** “replace our matcher” is too broad. The correct architecture is hybrid.

### 3. Multi-user zero-environment-read is a structural security boundary

`engine.NewMultiUserSource` intentionally ignores the injected environment and returns a request-context matcher resolver. Existing tests use panic/spy environments to prove no reads occur.

The user's requested local-auto-discovery switch must not become a runtime branch that first loads a catalog and later decides not to use it. Validation and composition must reject explicit `true` in multi-user without touching `Environment.Lookup` or `Snapshot`.

**Selected rule:** local auto-discovery is tri-state at decode time because its default depends on access mode: absent -> true for single-user, absent -> false for multi-user, explicit true -> invalid for multi-user.

### 4. Current YAML bool decoding cannot represent access-mode-dependent absence safely

`SingleUserConfig.IncludePopularEnv` currently works by pre-seeding a bool before YAML decode. The new `auto_discovered_local_keys.enabled` needs to distinguish absent from explicit false.

**Design implication:** use presence-aware decoding (`*bool` or equivalent raw-node presence), resolve the effective value only after access mode is known, and keep the resolved runtime config concrete/immutable.

### 5. Current matcher contract is scalar-oriented and insufficient for BetterLeaks discovery

`pkg/lipsdk/secretguard.Matcher` exposes `ScanBytes/String` and `RedactBytes/String`. Existing JSON traversal decomposes valid JSON into keys/scalars because exact matching does not need surrounding syntax.

BetterLeaks generic rules deliberately use nearby context such as `api_key`, authentication fields, URI structure, and multipart components. Passing only a scalar value can convert a real positive into a false negative.

**Design repair:** BetterLeaks is not implemented as another public `Matcher`. It gets a private logical-fragment detector interface within the feature tree. Existing exact matcher remains on the old safe redaction path.

### 6. Raw BetterLeaks findings are unsafe as public DTOs

Upstream findings can carry full match text, primary secret, captures, components, context/line, and a fingerprint. Those values are useful transiently for discovery and redaction mapping but are not safe observability payloads.

**Selected boundary:** the BetterLeaks adapter owns raw findings and returns a feature-private result containing safe projected metadata plus transient literal rewrite candidates. No raw upstream type or value crosses into generic runtime/SDK contracts.

### 7. BetterLeaks redaction is not the right mutation authority

AIProxer already has byte-length-preserving exact masking, known-prefix behavior, parsed-JSON string mutation, fail-closed handling for unsafe token classes, and post-mutation canonical validation. BetterLeaks' concern is discovery, not preserving AIProxer canonical semantics.

**Selected flow:** discover whole-fragment -> extract literal candidate values privately -> transient exact matcher/catalog -> existing AIProxer rewriter. If a decoded finding cannot be mapped back to a literal representation, `redact` blocks rather than falsely claiming sanitization.

### 8. BetterLeaks default allow markers are an LLM-proxy bypass

Upstream v2 defaults to suppressing findings on lines containing `betterleaks:allow` or `gitleaks:allow`. That is useful in source repositories but unsafe when the scanned line is client-controlled prompt/tool content.

**Security blocker:** always construct the scanner with `WithAllowSignatures()` and no arguments. Do not expose a config knob to re-enable allow signatures in V1.

### 9. Detection-only is technically enforceable

Upstream v2 has clean package separation between `scan` and `analyze`. AIProxer does not need `pipeline`, provider validators, revocation, or source packages for request scanning.

**Architecture ratchet:** the BetterLeaks adapter may import the minimal `config`, `scan`, report/finding types needed by detection, plus a tiny in-memory source if the error-returning scanner API requires it. It must not import `analyze`, provider HTTP evaluation, CLI command packages, or source orchestration packages.

### 10. BetterLeaks worker zero is unsafe as AIProxer's “auto” sentinel

Upstream interprets zero as `4 * GOMAXPROCS`. In a server already running many concurrent requests, inheriting that generic CLI/repository-scanner default could create avoidable CPU contention.

**Selected default:** AIProxer treats configured `0`/absence as its own auto policy and passes an explicit positive count `max(1, min(4, GOMAXPROCS))`. Operators can set `1..64` explicitly.

### 11. Confidence `medium` is the right enforcement default

BetterLeaks generic API-key/password rules can start at low confidence and promote based on context. A live proxy false positive can block or mutate an interactive request, which is more disruptive than a CI finding.

Defaulting to `medium` keeps provider-specific/high-confidence and context-strengthened generic findings while reducing low-confidence generic noise. Operators who prefer maximum sensitivity can choose `low`; high-sensitivity production environments can choose `high` if they prefer fewer false positives.

### 12. Decode depth should be useful but tightly bounded

Encoded credentials are a real leak path. Depth 1 adds meaningful coverage while bounding CPU and avoiding deep recursive transformations. Depth 0 is available for latency-sensitive users. A hard maximum of 3 prevents a configuration from turning request scanning into an unbounded decoding workload.

Decoded findings create a redaction asymmetry: block/log are straightforward, but redact may not know the original encoded span. The fail-closed `unrewritable_detected_secret` outcome resolves this without pretending that redaction succeeded.

### 13. Rule selection is useful operator tuning, but arbitrary BetterLeaks config loading is not

Operators need a way to disable a noisy rule or isolate a small reviewed set. BetterLeaks already has rule-selection concepts. Loading arbitrary target-local config files, baselines, ignores, or source metadata would import repository-scanner semantics and create deployment-dependent policy drift.

**Selected surface:** `disable_rules` and `isolate_rules`, mutually exclusive, validated against the pinned default configuration. Unknown IDs fail generation. Custom config paths and allow/ignore/baseline semantics are not exposed.

### 14. Rule upgrades are behavior changes

A BetterLeaks dependency bump can add/remove rules, change confidence, or change filters without any AIProxer code diff. For a proxy that blocks/redacts live traffic, that is a security-policy change.

**Selected ratchet:** expose version/config hash/rule count and pin an expected default-policy hash (or equivalent golden inventory) in tests. A dependency upgrade that changes the resolved default must require an intentional test update and review.

### 15. Existing SDK Finding needs safe detector provenance

Current `pkg/lipsdk/secretguard.Finding` has `SecretRefName`, aliases, source category, location, and occurrence count. BetterLeaks needs bounded detector/rule/confidence metadata if audit consumers are to explain why a request was blocked.

Adding those safe strings is a small SDK contract extension and does not expose BetterLeaks itself. Values must be bounded/validated and BetterLeaks types remain private.

### 16. The existing request-wide scan limit remains the principal content-size bound

The current default `scan_max_bytes` is 2 MiB with a 64 MiB configuration maximum. BetterLeaks must share that budget rather than independently scanning every fragment with another full cap.

A second metadata-volume bound is needed because many rules can report the same/adversarial input. This spec sets 256 projected findings per request, after which enforcement treats the detector as failed/bounded rather than allocating unbounded decision metadata.

## Requirement-to-Asset Gap Map

| Requirement | Existing assets | Gap / constraint | Disposition |
| --- | --- | --- | --- |
| 1 detector defaults | feature registration, access mode, runtime config | no independent detector switches; local bool needs presence | add presence-aware detector config and resolved policy |
| 2 exact protection | env catalog matcher, request credential matcher | composition currently selects one resolver path | preserve exact paths; hybrid private composition |
| 3 embedded SDK | Go module architecture | no BetterLeaks dependency/adapter | private v2 adapter; no CLI/analyze/source orchestration |
| 4 logical fragments | canonical scanner/token-safe JSON traversal | current traversal destroys heuristic context | add parallel logical-fragment discovery view |
| 5 safe projection | safe SDK Finding | no detector/rule/confidence; raw BL object unsafe | extend safe metadata; private raw result |
| 6 enforcement | guard block/redact/log, quarantine, exact rewriter | BL findings not mapped to rewriter | transient exact rewrite candidates |
| 7 tuning | scan bytes, redaction, env include/exclude | no BL confidence/decode/workers/rules | add bounded BL subtree |
| 8 lifecycle/errors | immutable generations, fail-closed Guard | no generation scanner/candidate cap | precompiled shared scanner + 256 finding cap |
| 9 diagnostics | inventory extras, audit observer, metrics | no BL policy identity/ratchet | version/hash/rule count + golden policy test |
| 10 certification | frontend matrix, leak tests, race/QA conventions | no BL corpus/perf/arch tests | add synthetic corpus, fuzz/race/bench/arch ratchets |

## Implementation Approach Options

### Option A: Replace the exact matcher with BetterLeaks

**Advantages**
- superficially simpler detector stack;
- one scanner library.

**Blocking problems**
- loses deterministic opaque-secret protection;
- shared request credential may not have a provider format;
- BetterLeaks heuristic/regex changes can alter behavior;
- does not solve AIProxer-specific redaction semantics.

**Verdict:** rejected.

### Option B: Implement BetterLeaks behind the public `secretguard.Matcher`

**Advantages**
- fewer apparent interface changes;
- reuses current scanner call sites.

**Blocking problems**
- JSON scalar decomposition loses generic/multipart context;
- `Matcher.Redact*` encourages BetterLeaks to own mutation;
- public SDK seam would become coupled to a discovery model it was not designed for.

**Verdict:** rejected.

### Option C: Hybrid private discovery detector plus existing exact matcher and rewriter

**Advantages**
- preserves all strong brownfield invariants;
- gives BetterLeaks full logical-fragment context;
- keeps raw findings private;
- supports shared-proxy unknown secrets;
- isolates prerelease v2 API churn;
- permits detector-specific configuration and diagnostics.

**Costs**
- requires a second internal traversal/view over canonical logical fragments;
- requires private dedup/mapping before public safe findings;
- needs careful scan-budget accounting and redaction mapping.

**Verdict:** selected.

## Complexity and Risk

- **Effort: L (1–2 weeks)** — new dependency, config, generation lifecycle, logical-fragment discovery, redaction bridge, diagnostics, and broad certification across a security-sensitive feature.
- **Risk: High** — live false positives/negatives, accidental secret observability, multi-user isolation, prerelease SDK churn, CPU amplification, and redaction correctness are all security-impacting.

Risk is manageable because the design does not alter core routing or protocol translation and retains existing fail-closed enforcement.

## Decisions Repaired During Gap Analysis

1. **Top-level feature activation remains explicit.** The user asked for detector defaults, not a new default enforcement action. BetterLeaks is secure-by-default *inside an enabled guard*.
2. **Local discovery needs tri-state decode semantics.** A plain bool is insufficient because absence resolves differently by access mode.
3. **BetterLeaks cannot implement the current public Matcher seam directly.** Whole-fragment context is required.
4. **BetterLeaks findings cannot become SDK objects.** Only safe projected metadata may cross the adapter.
5. **Worker auto does not mean upstream zero.** AIProxer resolves a conservative explicit count.
6. **Allow markers are always disabled.** No operator/user bypass knob is provided.
7. **Rule tuning is list-based, not arbitrary config loading.** This retains deterministic policy and reviewability.
8. **Decoded unrewritable secrets block in redact mode.** Passing them through would violate the action contract.
