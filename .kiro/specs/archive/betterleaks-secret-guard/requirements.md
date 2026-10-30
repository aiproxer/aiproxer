# Requirements Document

## Introduction

AIProxer's existing `secrets-guard` provides a strong live-proxy enforcement envelope but its discovery engine is intentionally narrow: it exactly matches secrets the proxy already knows. That is effective for a single-user process that can inventory its own environment, but it cannot discover arbitrary client-side credentials arriving at a shared proxy from remote machines.

This specification implements issue #714 by making secret detection hybrid. AIProxer retains its exact-known-secret detector where the proxy legitimately knows a value and embeds the BetterLeaks Go SDK as a second, expert-maintained discovery detector for unknown credentials. BetterLeaks performs detection only; AIProxer remains authoritative for canonical traversal, policy, mutation, audit, quarantine, fail-closed behavior, access-mode isolation, and observability.

The maintainer-approved provenance contract in 4.7 supersedes the issue's original broader request-field scope: only user prompts and tool execution output are eligible. Historical assistant responses and model-generated tool calls remain untouched when replayed. Provider response streams are outside SecretGuard ownership under 10.9.

The feature is brownfield. Existing stage ordering, `block`/`redact`/`log` actions, request-credential matching, single-user environment catalog, secure-session quarantine, scan-byte limit, audit policy, token-safe JSON rewriting, and runtime-generation composition must remain intact unless this specification explicitly changes them.

## Boundary Context

- **In scope**: configurable detector selection; default detector posture by access mode; embedded BetterLeaks v2 SDK adapter; context-preserving logical-fragment discovery; confidence/decode/worker/rule-selection controls; safe finding projection; deterministic hybrid merge; BetterLeaks-assisted redaction through AIProxer's existing safe rewriting semantics; scanner/config lifecycle; diagnostics; upgrade ratchets; performance, fuzz, race, leak, and frontend parity certification.
- **Out of scope**: BetterLeaks CLI/subprocess/sidecar integration; provider credential validation, analysis, or revocation; a general DLP product; malicious-obfuscation guarantees; filesystem/repository scanning; target-local `.betterleaks.toml` or ignore/baseline semantics; user-controlled suppression markers; a public BetterLeaks API in `pkg/lipsdk`; changing core routing/failover/B2BUA behavior; automatically enabling the top-level `secrets-guard` feature for installations that do not configure it.
- **Security posture**: once `secrets-guard` is enabled, BetterLeaks discovery is enabled by default in both `single_user` and `multi_user`. Local environment auto-discovery is enabled by default only in `single_user`, disabled in `multi_user`, and cannot be enabled in `multi_user`.
- **Version posture**: the implementation uses the BetterLeaks v2 Go SDK behind a private adapter. At specification time the newest v2 release is `v2.0.0-rc.1`; the dependency is pinned exactly until a separately reviewed upgrade changes it.

## Requirements

### Requirement 1: Detector Selection and Secure Defaults

**Objective:** As an operator, I want explicit detector controls with access-mode-safe defaults so that AIProxer is secure by default without making shared deployments inspect server-local credentials.

#### Acceptance Criteria

1.1. **Where** `secrets-guard` is not enabled through the existing feature-registration mechanism, AIProxer shall preserve current disabled behavior and shall construct neither an environment secret catalog nor a BetterLeaks scanner for request processing.

1.2. **When** `secrets-guard` is enabled and `betterleaks.enabled` is omitted, AIProxer shall resolve BetterLeaks discovery as enabled in both `single_user` and `multi_user` access modes.

1.3. **When** `secrets-guard` is enabled in `single_user` and `auto_discovered_local_keys.enabled` is omitted, AIProxer shall resolve local environment auto-discovery as enabled.

1.4. **When** `secrets-guard` is enabled in `multi_user` and `auto_discovered_local_keys.enabled` is omitted, AIProxer shall resolve local environment auto-discovery as disabled and shall perform zero environment reads for secret discovery.

1.5. **If** configuration explicitly enables `auto_discovered_local_keys.enabled` in `multi_user`, candidate generation shall fail before publication with a bounded configuration error and shall not read the process environment while validating that configuration.

1.6. **When** an operator explicitly disables `auto_discovered_local_keys.enabled` in `single_user`, AIProxer shall not enumerate or load environment secrets for the local exact catalog while leaving other enabled detector paths unaffected.

1.7. **When** an operator explicitly disables `betterleaks.enabled`, AIProxer shall not construct or invoke BetterLeaks while preserving enabled exact-known-secret protection applicable to the access mode.

1.8. **When** both configurable detector paths are disabled, AIProxer shall accept the configuration deliberately, expose a bounded diagnostic showing no discovery detector is active, and shall not silently re-enable either detector.

1.9. **The** existing top-level `secrets-guard` activation and required action contract shall remain explicit; this change shall not silently opt previously unconfigured deployments into block, redact, or log behavior.

### Requirement 2: Exact Known-Secret Protection by Access Mode

**Objective:** As a user, I want exact matching retained where AIProxer legitimately knows a credential so that BetterLeaks does not regress deterministic protection.

#### Acceptance Criteria

2.1. **While** local auto-discovery is enabled in `single_user`, AIProxer shall retain exact, case-sensitive matching of the composed local/proxy secret catalog, including current include/exclude, minimum-secret-length, known-prefix, and mask semantics.

2.2. **While** operating in `multi_user`, AIProxer shall retain exact matching of the current accepted request authentication credential made available by ingress authentication, even when BetterLeaks is disabled or has no matching rule.

2.3. **If** an arbitrary single-user environment secret has no BetterLeaks-recognizable format, the exact local detector shall still detect it when local auto-discovery is enabled.

2.4. **If** an arbitrary current request credential has no BetterLeaks-recognizable format, the request-scoped exact detector shall still detect it in `multi_user`.

2.5. **The** hybrid implementation shall not expose raw catalog values, current request credential bytes, or an API for retrieving them from the composed detector service.

### Requirement 3: Embedded BetterLeaks Detection-Only SDK

**Objective:** As an operator, I want mature secret discovery without introducing an external scanner process or new credential-egress path.

#### Acceptance Criteria

3.1. **Where** BetterLeaks discovery is enabled, AIProxer shall use BetterLeaks as an in-process Go library compiled into the AIProxer binary and shall not require a separately installed executable, subprocess, sidecar, container, or network service.

3.2. **The** BetterLeaks dependency shall be isolated behind a private secret-guard adapter and BetterLeaks types shall not appear in `pkg/lipsdk`, core runtime contracts, frontend contracts, backend contracts, or public host APIs.

3.3. **The** production request path shall not invoke `os/exec`, shell commands, BetterLeaks CLI commands, or IPC to perform BetterLeaks configuration, scanning, reporting, or redaction.

3.4. **The** BetterLeaks path shall perform detection only and shall not invoke credential validation, provider analysis, permission analysis, revocation, or provider-authenticated HTTP calls.

3.5. **The** BetterLeaks path shall not discover or load target-local BetterLeaks configuration, ignore files, baseline files, repository paths, Git history, filesystem targets, GitHub/GitLab/S3 sources, or other BetterLeaks source orchestration.

3.6. **When** the scanner is constructed, AIProxer shall disable BetterLeaks and Gitleaks allow signatures so that user-controlled `betterleaks:allow` and `gitleaks:allow` text cannot suppress a finding.

3.7. **If** either allow-marker string appears adjacent to an otherwise detectable credential, the credential shall remain detectable.

3.8. **The** implementation shall pin the BetterLeaks v2 dependency to an exact reviewed version and shall make a dependency upgrade an explicit security-policy review event rather than silently following upstream defaults.

### Requirement 4: Context-Preserving Logical Fragment Scanning

**Objective:** As a security feature, I want BetterLeaks to receive enough local context to use generic and multipart rules without losing canonical attribution or mutation safety.

#### Acceptance Criteria

4.1. **When** BetterLeaks scans textual canonical content, AIProxer shall provide a complete logical text fragment rather than decomposing it into unrelated scalar tokens first.

4.2. **When** BetterLeaks scans JSON-shaped user prompt or tool execution output content, AIProxer shall provide the original logical raw JSON fragment needed to preserve key/value and multipart context. Config snippets and schemas are eligible only when embedded in that content; model-generated tool-call arguments and tool definitions are excluded.

4.3. **If** a generic secret is detectable from contextual JSON such as an `api_key` key adjacent to an opaque value, the BetterLeaks path shall retain that context and shall not rely solely on scalar-value scanning.

4.4. **The** BetterLeaks path shall not flatten the entire request into one unstructured blob unless it can preserve deterministic mapping to canonical locations and existing scan-budget accounting; V1 shall use bounded per-logical-fragment scanning.

4.5. **When** the canonical scanner encounters repeated logical fragments or repeated findings, AIProxer shall preserve deterministic location attribution and shall not multiply enforcement decisions merely because more than one detector reports the same concrete secret occurrence.

4.6. **The** request-level `scan_max_bytes` limit shall bound unique canonical request bytes admitted to secret detection, not cumulative bytes inspected across detectors. AIProxer shall charge each logical fragment occurrence once, using its original text or raw JSON byte length, before either detector inspects it; identical content in different request fields shall count separately. Exact matching and BetterLeaks shall share the same admitted content and budget, with no separate detector allowance. A fragment that would exceed the remaining budget shall not be scanned by either detector and shall trigger the existing scan-limit behavior.

4.7. **The** SecretGuard feature shall inspect and mutate only user prompt content and tool execution output. For message-authoritative calls, only `Messages` with `RoleUser` or `RoleTool` are eligible. For item-authoritative calls, only message items with `RoleUser` or `RoleTool`, and `ItemKindToolResult` output/parts, are eligible. All `Instructions`, assistant/system/developer or unknown-role messages, model-generated tool calls, reasoning/refusal/reference items, and tool definitions (names, descriptions, schemas) shall remain byte-for-byte unchanged and shall not consume scan budget, produce findings, or trigger enforcement. The same provenance restriction applies to exact-only, BetterLeaks-only, and hybrid detection and every action, including replayed conversation history.

### Requirement 5: Safe Finding Projection and Hybrid Merge

**Objective:** As an operator, I want useful diagnostics without exposing the secrets BetterLeaks found.

#### Acceptance Criteria

5.1. **When** BetterLeaks reports a finding, AIProxer shall immediately project it inside the adapter to bounded AIProxer metadata and shall not expose the raw BetterLeaks finding object outside the adapter boundary.

5.2. **The** safe finding vocabulary may include detector identity, rule identifier, confidence band, canonical location, source category, safe reference name where available, and occurrence count, and shall not include the raw match, secret value, captures, components, line/context excerpts, original fragment, or BetterLeaks fingerprint.

5.3. **When** exact and BetterLeaks detectors identify the same concrete occurrence, AIProxer shall deduplicate privately before emitting audit/decision metadata and shall produce one coherent enforcement result.

5.4. **When** multiple findings remain after deduplication, AIProxer shall order safe findings deterministically independent of BetterLeaks worker scheduling or upstream finding iteration order.

5.5. **The** hybrid merge shall not introduce an unkeyed or reversible secret hash into SDK findings, logs, metrics, audit events, diagnostics, or client-visible errors solely for deduplication.

5.6. **When** findings are emitted to bounded metrics, rule IDs and arbitrary source text shall not become uncontrolled high-cardinality labels.

### Requirement 6: Enforcement and Redaction Ownership

**Objective:** As a user, I want discovered secrets blocked or redacted through AIProxer's proven enforcement semantics rather than through a scanner-specific mutation shortcut.

#### Acceptance Criteria

6.1. **When** action is `block` and any enabled detector reports an enforceable finding, AIProxer shall block before backend dispatch using the existing secret-guard denial and secure-session quarantine semantics.

6.2. **When** action is `log` and an enabled detector reports findings, AIProxer shall emit only safe decision/audit metadata and shall leave the canonical call unmodified.

6.3. **When** action is `redact` and BetterLeaks reports a credential representation that exists literally in the original logical fragment, AIProxer shall redact it through AIProxer's existing exact-value/text/JSON mutation machinery rather than delegating mutation to BetterLeaks reporting/redaction APIs.

6.4. **When** a BetterLeaks-discovered literal value is redacted in JSON string content, the resulting canonical JSON shall remain valid and existing token-safe traversal and post-mutation canonical validation shall remain authoritative.

6.5. **If** BetterLeaks detects a secret only after decoding or otherwise cannot map it safely to a literal rewritable representation in the original fragment, `block` shall block, `log` shall log safe metadata and continue, and `redact` shall fail closed by blocking with a bounded `unrewritable_detected_secret` failure kind.

6.6. **If** redaction encounters an existing unsupported JSON-key or non-string-token mutation condition, AIProxer shall retain the current fail-closed behavior and shall not claim successful sanitization.

6.7. **The** presence of BetterLeaks shall not weaken scan-limit failure handling, audit failure policy, quarantine persistence handling, or the no-backend-dispatch invariant for blocked requests.

### Requirement 7: BetterLeaks Configuration Surface

**Objective:** As an operator, I want bounded tuning controls for false positives, encoded secrets, CPU usage, and noisy rules without exposing unsafe BetterLeaks capabilities.

#### Acceptance Criteria

7.1. **When** `betterleaks.minimum_confidence` is omitted, AIProxer shall use `medium`; accepted values shall be `low`, `medium`, and `high`, and any other value shall fail candidate generation.

7.2. **When** `betterleaks.max_decode_depth` is omitted, AIProxer shall use `1`; an explicit `0` shall disable recursive decoding, negative values shall be rejected, and the configured maximum shall be bounded by an AIProxer-defined hard limit of `3`.

7.3. **When** `betterleaks.workers` is omitted or set to `0`, AIProxer shall resolve an explicit safe worker count of `max(1, min(4, GOMAXPROCS))` and shall not pass BetterLeaks' zero value through to its `4 * GOMAXPROCS` default.

7.4. **If** `betterleaks.workers` is configured outside `1..64`, candidate generation shall fail; accepted explicit values shall be used as the scanner-wide detection worker bound shared across concurrent scans of that generation.

7.5. **Where** `betterleaks.disable_rules` is configured, AIProxer shall validate every rule ID against the resolved pinned BetterLeaks configuration, reject unknown IDs, and remove exactly the selected known IDs from the active detector set. Candidate generation shall fail before publication with a bounded configuration error if any retained rule has a required component reference to a selected ID. A selected component may be removed when every rule that requires it is also explicitly selected for removal. Optional references from retained rules to selected IDs shall be pruned, but AIProxer shall never cascade-remove dependent rules, weaken or remove a retained rule's required reference, or substitute `SkipReport` or another suppression for removal.

7.6. **Where** `betterleaks.isolate_rules` is configured, AIProxer shall validate every rule ID and activate the selected roots together with their transitive required component closure, failing on unknown IDs. Optional references to components outside that closure shall be pruned while the retained required closure remains intact; an optional component rule selected explicitly shall remain in the configuration with its pinned matching and reporting behavior and shall bring along its own required closure.

7.7. **If** both `disable_rules` and `isolate_rules` are non-empty, candidate generation shall fail rather than guess precedence.

7.8. **The** operator configuration surface shall not expose BetterLeaks validation, analysis, revocation, allow signatures, ignore/baseline files, arbitrary config-file paths, source selection, report formatting, provider credentials, or network options.

7.9. **When** lists contain duplicate rule IDs or surrounding whitespace, AIProxer shall canonicalize them deterministically before scanner construction and configuration hashing.

7.10. **When** `scan_max_bytes`, existing redaction options, local auto-discovery options, or BetterLeaks options (including rule dependency validation) are invalid, candidate generation shall fail before publication with a bounded configuration error and the previously published generation shall remain serving according to existing reload semantics.

### Requirement 8: Scanner Lifecycle, Errors, and Bounded Resource Use

**Objective:** As an operator, I want BetterLeaks to be safe on the latency-sensitive hot path and to fail predictably under errors or adversarial input.

#### Acceptance Criteria

8.1. **When** a runtime generation is composed with BetterLeaks enabled, AIProxer shall construct and precompile one immutable reusable scanner for that generation rather than constructing a scanner per request, field, or fragment.

8.2. **The** scanner shall be safe for concurrent request use and request cancellation shall propagate through the error-returning scan path.

8.3. **If** BetterLeaks configuration, regex/filter compilation, or scanner construction fails, candidate generation shall fail before publication.

8.4. **If** BetterLeaks scanning fails during a request, `block` and `redact` shall fail closed before backend dispatch; `log` shall emit a bounded detector-failure audit outcome and shall preserve the user request without exposing raw scanner errors containing content.

8.5. **The** request path shall not use an SDK convenience API that discards scan errors when an error-returning scan API is available for the pinned SDK.

8.6. **When** finding volume exceeds an AIProxer hard cap of `256` projected findings per request, AIProxer shall stop accumulating finding metadata and shall treat the condition as a bounded scan failure; `block` and `redact` shall block and `log` shall record the bounded failure and continue.

8.7. **The** implementation shall not create an unbounded goroutine per fragment/request, shall not accept BetterLeaks' generic worker default implicitly, and shall preserve existing request-level memory/scan bounds.

8.8. **The** added scanner shall not materially regress no-secret request latency or allocation behavior without explicit benchmark evidence and maintainer review; certification shall record exact-only, BetterLeaks-only, and hybrid measurements.

### Requirement 9: Diagnostics, Audit, and Upgrade Ratchets

**Objective:** As an operator and maintainer, I want to know exactly which detection policy is active without exposing credential material.

#### Acceptance Criteria

9.1. **When** `secrets-guard` is enabled, diagnostics shall expose bounded detector posture including access mode, local-auto-discovery enabled state, BetterLeaks enabled state, BetterLeaks version, resolved rule count, minimum confidence, decode depth, worker bound, and a BetterLeaks configuration hash.

9.2. **The** BetterLeaks configuration hash shall be derived from the resolved in-memory detection configuration and shall not contain or depend on request secret values.

9.3. **When** an upgrade or configuration change alters the resolved BetterLeaks rules/configuration hash, repository tests or an explicit expected-policy ratchet shall make that change review-visible rather than silently accepting new live blocking behavior.

9.4. **When** audit events describe a BetterLeaks finding, they may include bounded detector/rule/confidence metadata but shall never include raw matches, captures, components, source excerpts, fingerprints, or raw fragments.

9.5. **When** logs, metrics, diagnostics, errors, or audit payloads are generated from requests containing synthetic canary secrets, automated anti-leak tests shall prove the canary values do not appear in any emitted observability sink.

9.6. **The** adapter shall keep BetterLeaks logging disabled unless a future reviewed sanitizer proves upstream logs cannot contain raw finding material; this specification does not authorize raw upstream scanner logging.

### Requirement 10: Brownfield Compatibility and Certification

**Objective:** As a maintainer, I want the hybrid detector to preserve all current secret-guard and protocol invariants across the standard distribution.

#### Acceptance Criteria

10.1. **When** the hybrid detector is integrated, existing secret-guard stage ordering before capture/routing/backend dispatch shall remain unchanged.

10.2. **While** running in `multi_user`, environment-spy/panic tests shall prove zero process-environment reads regardless of malformed single-user options, disabled BetterLeaks, enabled BetterLeaks, reload, or request activity.

10.3. **When** the standard frontend matrix supplies equivalent canonical secret-bearing content, the secret-guard decision shall remain semantically consistent across all bundled frontend protocol flavors.

10.4. **The** implementation shall include provider-specific positive fixtures, generic API-key/password/credential-URI fixtures, private-key and multipart fixtures, representative public/non-secret negatives, JSON-context positives, tool schema/result positives, repeated/overlapping findings, decoded findings, and allow-marker bypass attempts using synthetic values only.

10.5. **The** implementation shall include race/concurrency certification for one shared generation scanner and fuzz/adversarial tests for fragment mapping, redaction, malformed JSON, repeated matches, high finding counts, and observability non-leakage.

10.6. **The** implementation shall benchmark no-hit and positive-hit payloads at 1 KiB, 10 KiB, 100 KiB, 1 MiB, and the current 2 MiB default scan cap, including concurrent scans, adversarial candidate-heavy input, allocations, p50/p95/p99 latency, and binary/dependency-size delta.

10.7. **The** implementation shall add architecture ratchets that prevent BetterLeaks CLI/subprocess integration, provider validation/analysis/revocation imports on the secret-guard request path, BetterLeaks type leakage across the private adapter boundary, and user-controlled allow-signature reintroduction.

10.8. **When** the feature is disabled or both detector paths are deliberately off, unrelated routing, billing, continuity, frontend/backend translation, and response-stream semantics shall remain unchanged.

10.9. **The** SecretGuard feature shall operate exclusively before backend dispatch on the eligible client-to-provider canonical request content defined in 4.7, including while enabled and actively redacting. It MUST NOT inspect, scan, redact, transform, collect, buffer, delay for inspection, or otherwise interpose on provider response events or streams. A provider event containing a detectable secret shall reach downstream unchanged before stream completion; subsequent events and normal stream termination shall remain observable incrementally.
