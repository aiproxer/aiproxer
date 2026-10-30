# Requirements Document

## Introduction

B-Leg Path Virtualization reduces repeated model-visible token usage caused by long absolute filesystem paths in agentic coding sessions. The proxy shall present a shorter, deterministic virtual path namespace to inference backends while preserving the client's real filesystem namespace. The feature is opt-in and cross-platform, operates only on path-bearing tool-call/tool-result surfaces, and must not alter ordinary user messages, model reasoning, or general assistant prose.

The feature is a reversible namespace translation, not lossy compression. A-leg/client truth remains expressed in real paths; B-leg/model truth may use deterministic aliases. Any model-emitted virtual path that is used in a path-bearing tool-call argument must be expanded back to the corresponding real path before client-facing tool execution and before downstream path/security policy evaluates the call.

## Boundary Context

- **In scope**: deterministic, workspace-bound virtualization of the authoritative workspace project root; POSIX, Windows drive, Windows UNC, and Windows extended absolute-path forms; canonical tool-call argument rewriting; conservative tool-result rewriting on explicitly path-bearing surfaces; reverse expansion of model-emitted path-bearing tool arguments; stateless stale-workspace alias rejection; audit mode; bounded metrics; retry/failover/continuation stability.
- **Out of scope**: arbitrary semantic compression; relative-path rewriting; filesystem canonicalization/symlink resolution; URI rewriting; path discovery from ordinary chat/reasoning; arbitrary substring replacement inside source/file contents; dynamic discovery of additional roots; persistence of a general path dictionary; operator-configurable alias namespace/version in V1; rewriting ordinary assistant prose in V1.
- **Adjacent expectations**: existing routing, B2BUA continuity, conversation-view store/persistence, persisted message-identity and anchor derivation, anchor-missing policy, secure-session, capability negotiation, billing/accounting, and provider adapters retain their current ownership.
- **Boundary ownership**: optional feature plugin plus narrowly additive generic SDK/runtime support required to make complete tool-call expansion safe and to preserve an already-resolved conversation-view placement across provably trajectory-preserving B-leg-only content rewrites.
- **Optional hexagonal lens**: pure path-virtualization policy in the feature; runtime orchestration only exposes/reuses generic extension seams, complete-tool-call buffering metadata, and request-local frozen conversation-projection evidence; adapters remain protocol translators.
- **Revalidation triggers**: tool-call finalization/assembly semantics, request hook ordering, conversation-view message identity/provenance/final reassertion, canonical tool result representation, workspace metadata, provider-side continuation behavior, token-accounting preflight ordering.

## Requirements

### Requirement 1: Deterministic Cross-Platform Virtual Namespace
**Objective:** As an operator, I want the proxy to replace long workspace-root paths with stable short aliases, so that repeated model-visible paths consume fewer tokens without changing client filesystem truth.

#### Acceptance Criteria
1. Where the feature is enabled in rewrite mode and `Workspace.ProjectRoot` is a supported absolute path, the proxy shall derive exactly one deterministic virtual root for that project root without filesystem I/O.
2. The proxy shall support POSIX absolute paths, Windows drive-absolute paths, UNC paths, and Windows extended drive/UNC paths independently of the operating system on which the proxy process itself runs.
3. The proxy shall use host-independent lexical path parsing and shall not depend on the proxy host's `filepath` semantics to interpret a client path from another operating system.
4. The virtual root shall be shorter than the real root before rewrite is applied; otherwise the proxy shall leave the path unchanged.
5. The virtual root shall preserve an OS-appropriate absolute-path shape, use the fixed V1 reserved proxy namespace, and contain a deterministic workspace identity tag derived from the authoritative project root.
6. When comparing a Windows root, the proxy shall use Windows-appropriate case-insensitive prefix comparison and separator-aware path boundaries while preserving the original suffix bytes used for reconstruction.
7. When comparing a POSIX root, the proxy shall use case-sensitive prefix comparison and `/` segment boundaries.
8. If the project root is relative, malformed, a Windows device path, unsupported, or collides with the reserved virtual namespace, the proxy shall disable rewriting for that mapping and record a bounded reason code rather than guessing.
9. The proxy shall not resolve symlinks, access the filesystem, normalize through the host OS, or change path semantics beyond reversible prefix substitution.
10. The workspace identity tag shall be derived from a versioned, path-flavor-aware canonical identity of `Workspace.ProjectRoot` using a collision-resistant digest of at least 96 bits; the tag shall not depend on B-leg ID, provider, model, retry ordinal, trace ID, or mutable process state.
11. When a selected path uses the fixed V1 reserved alias namespace, the proxy shall parse and validate its workspace identity tag before expansion; a malformed tag, a tag derived from another project root, or an incompatible path flavor shall be treated as an unresolved reserved alias rather than expanded against the current root.

### Requirement 2: B-Leg-Only Tool Surface Virtualization
**Objective:** As a coding-agent user, I want real paths preserved on the client side while the backend sees shorter equivalents only where filesystem paths are semantically expected.

#### Acceptance Criteria
1. When a backend-effective request contains a historical tool-call argument at a configured or safely inferred path-bearing JSON location under the real project root, the proxy shall replace only that path value's real-root prefix with the virtual root.
2. When a backend-effective request contains a tool result on an explicitly supported path-bearing structured surface, the proxy shall virtualize matching real-root prefixes on that surface.
3. The proxy shall not recursively replace arbitrary JSON strings merely because they contain the real root.
4. The proxy shall not rewrite `content`, `patch`, source-code, script-body, or other semantically opaque tool arguments unless an explicit tool profile marks a specific field as path-bearing.
5. The proxy shall not rewrite opaque textual tool output by default.
6. Where an operator or built-in conservative profile explicitly marks an opaque result as path-oriented, the proxy shall apply only the profile's bounded path-token/line rules and shall leave ambiguous content unchanged.
7. The proxy shall not inspect or rewrite ordinary user messages, system/developer instructions, model reasoning content, or ordinary assistant prose in V1.
8. The proxy shall preserve tool call IDs, tool names, item/message ordering, non-path argument values, tool-result status/metadata, and all unrelated canonical fields byte-for-byte where canonical representation permits.
9. Reapplying virtualization to an already virtualized backend-effective call shall be idempotent.

### Requirement 3: Safe Path-Bearing Surface Selection
**Objective:** As an operator, I want path rewriting limited to fields that are actually filesystem locators, so that source contents and command payloads are never silently modified.

#### Acceptance Criteria
1. The feature shall support explicit per-tool argument selectors expressed as JSON Pointer locations into completed tool-call argument JSON.
2. The feature shall support explicit per-tool result selectors for structured JSON result content where the canonical representation exposes structured JSON.
3. Where schema-assisted inference is enabled, the feature shall infer candidates only from declared tool-schema properties whose normalized names are in a bounded path-key vocabulary and whose schema shape is string or array-of-strings.
4. Schema-assisted inference shall not descend into properties named or described as content, patch, diff, script, command, query, expression, replacement, body, data, or equivalent denylisted payload concepts.
5. If selector resolution is ambiguous, invalid, references a non-string value, or cannot be proven path-bearing under the configured policy, the proxy shall skip that value and record a bounded reason code.
6. Built-in tool profiles shall be exact-name profiles, versioned with the feature, and shall never use substring/prefix tool-name matching as authority.
7. Operator-defined profiles shall override or extend built-in selectors only through typed feature configuration validated at generation compilation.
8. Unknown tools shall receive no opaque-result rewriting unless explicitly configured.

### Requirement 4: Mandatory Reverse Expansion Before Client Tool Execution
**Objective:** As a coding-agent user, I want model-emitted virtual paths converted back to real paths before my harness sees them, so that virtual aliases never become filesystem targets.

#### Acceptance Criteria
1. When the backend emits a completed tool call containing the virtual root at a path-bearing argument selector, the proxy shall expand that value to the real project root before releasing the corresponding client-facing tool-call argument event.
2. Reverse expansion shall operate on completed valid tool-call argument JSON, not independently on arbitrary streaming fragments.
3. Expanded tool calls shall continue through existing tool policies/reactors using the real path representation.
4. If a completed path-bearing tool argument contains the fixed V1 reserved virtual namespace but has a malformed workspace tag, a workspace tag that does not match the current authoritative project root, an incompatible path flavor, or otherwise cannot be mapped unambiguously, the proxy shall fail closed for that tool call and shall not release the virtual path to the client.
5. If complete argument reconstruction required for reverse expansion exceeds the configured mandatory expansion bound, the proxy shall fail closed for that tool call rather than bypass path expansion.
6. A failure or limit in an unrelated optional tool-call finalizer shall not cause path expansion to be silently skipped for a call to which path virtualization applies.
7. Tool calls containing no virtual root shall preserve existing finalization/pass-through behavior.
8. Reverse expansion shall preserve JSON validity and all non-selected argument fields.
9. The feature shall never expand reserved aliases inside arbitrary content/patch/script fields unless those exact fields are explicitly configured as path-bearing.

### Requirement 5: Runtime Ordering and Security Invariants
**Objective:** As a maintainer, I want virtualization placed at safe extension boundaries, so that routing, security, accounting, and conversation semantics remain correct.

#### Acceptance Criteria
1. The proxy shall perform path security/policy evaluation of model-emitted tool calls only after required virtual-path expansion has produced the real filesystem path.
2. The backend-effective request shall contain virtualized path-bearing tool history before final provider translation and `Backend.Open`.
3. Candidate sizing/context eligibility and token-accounting preflight shall be able to observe the virtualized backend-effective representation so that savings are not ignored by admission.
4. Late request shaping and conversation-view reassertion shall not restore real paths into path-bearing tool history after the final virtualization pass.
5. The feature shall not modify route identity, model/backend selection, B-leg sequencing, output commitment, retry/failover authority, billing authority, or secure-session authority.
6. Every retry, race participant, and failover candidate within one logical A-leg turn shall derive the same V1 workspace-root alias.
7. No path mapping shall be keyed to a B-leg ID, provider ID, model ID, retry ordinal, or trace ID.
8. Provider adapters shall remain unaware of path virtualization and continue to consume/produce canonical calls/events.
9. Once the early conversation-view projection has resolved an after-message anchor from A-leg/client truth, virtualization of selected payload bytes inside that same complete message shall not by itself invalidate the frozen placement at final backend-bound reassertion; the runtime shall carry that resolved placement through the rewrite only when a deterministic one-to-one trajectory lineage from the frozen filtered baseline to the backend-shaped call is proven.
10. This compatibility path shall not change persisted `MessageIdentityOf` semantics, stored anchor identity/occurrence, anchor-missing policy, overlay lifecycle, or fallback policy. If one-to-one lineage cannot be proven because messages/items were inserted, removed, reordered, structurally changed, or otherwise became ambiguous, final reassertion shall not guess by position and shall preserve the existing exact-resolution/fail-closed behavior.

### Requirement 6: Continuity, Replay, and Restart Safety
**Objective:** As an operator, I want aliases to remain reconstructable across turns and process lifecycle events, so that continuation cannot strand a model-visible namespace.

#### Acceptance Criteria
1. V1 shall derive the primary mapping exclusively from the current authoritative `Workspace.ProjectRoot` and deterministic alias rules; it shall not require a mutable per-session dictionary for the primary root.
2. Given the same supported project root, a later request shall derive the same workspace tag and virtual root after generation reload or process restart without reading prior mapping state; different supported project roots shall derive different workspace tags except with the explicitly bounded collision probability of the specified digest.
3. Where a client replays full history, the proxy shall re-virtualize historical path-bearing tool surfaces idempotently before backend submission.
4. Where provider-side continuation retains prior model-visible history, subsequent tool results/tool calls shall use the same deterministic workspace-bound V1 alias for the same workspace root.
5. If the authoritative workspace root changes for an existing logical session, the new root shall derive a different workspace tag, and any model-emitted alias carrying the prior root's tag shall be rejected as a stale-workspace alias rather than expanded against the new root.
6. Dynamic discovery and persistence of arbitrary secondary roots shall be out of scope for V1 and shall require a later design with explicit durable mapping semantics.

### Requirement 7: Configuration, Audit Mode, and Observability
**Objective:** As an operator, I want controlled rollout and measurable value, so that the feature can be validated before mutation is enabled.

#### Acceptance Criteria
1. The feature shall be disabled by default and enabled through feature-owned typed configuration under the standard feature plugin configuration surface.
2. The feature shall support at least `audit` and `rewrite` modes.
3. In audit mode, the proxy shall perform candidate detection and savings estimation without mutating canonical requests or response tool calls.
4. Configuration shall allow schema-assisted argument selection, explicit per-tool selectors, conservative result profiles, and a mandatory tool-call expansion byte bound; the V1 reserved alias namespace/version and workspace-tag encoding shall be fixed implementation contracts rather than operator-configurable values.
5. Invalid selectors, duplicate/conflicting tool profiles, unsupported bounds, or ambiguous configuration shall fail generation compilation before publication.
6. Metrics shall expose bounded counters/histograms for eligible occurrences, rewritten occurrences, skipped occurrences by bounded reason, bytes-before, bytes-after, bytes-saved, expansion failures, and mandatory-buffer overflows.
7. Logs/traces/metrics shall not include real paths, virtualized path suffixes, tool payload contents, source text, or high-cardinality path hashes.
8. The feature shall provide diagnostics/inventory visibility sufficient to show enablement, mode, and bounded configuration shape without exposing path values.
9. Audit/rewrite processing shall be bounded in CPU and memory by canonical payload limits and feature-specific selector/profile limits.

### Requirement 8: Compatibility and Failure Behavior
**Objective:** As a maintainer, I want the feature to compose safely with existing optional features, so that enabling it does not create silent semantic regressions.

#### Acceptance Criteria
1. If path virtualization is disabled, observable request/response behavior shall remain unchanged.
2. If outbound virtualization encounters an internal error before a virtual alias has become model-visible for the affected surface, the feature shall fail open by preserving the real path and record a bounded reason.
3. Once a virtual alias is model-visible in the current logical tool trajectory, reverse expansion failures for path-bearing model tool calls shall fail closed rather than releasing an unresolved alias.
4. The feature shall compose deterministically with tool-call repair; syntax repair may occur, but mandatory path expansion shall receive valid completed JSON and shall not be bypassed by tool-call-repair size policy.
5. The feature shall preserve canonical call/event validation after every mutation.
6. The feature shall not require frontend-by-backend Cartesian test coverage; family/canonical contracts plus bounded representative end-to-end sentinels shall certify protocol neutrality.
7. The feature shall remain compatible with streaming and non-streaming clients because non-streaming continues to collect the same canonical stream path.

### Requirement 9: Performance Value and Guardrails
**Objective:** As an operator, I want path virtualization to reduce backend-visible context without becoming a larger performance cost than the savings justify.

#### Acceptance Criteria
1. Rewrite mode shall apply virtualization only when the replacement is strictly shorter than the original matched prefix.
2. The implementation shall avoid filesystem I/O, regex backtracking, unbounded recursion, and whole-call JSON reserialization where a bounded targeted rewrite is sufficient.
3. Path matching shall use deterministic longest/specific match semantics even though V1 exposes only one primary project-root mapping.
4. The implementation shall include benchmarks for path parsing/prefix matching, selector-guided argument rewriting, representative path-oriented result rewriting, and completed-tool-call reverse expansion.
5. Audit metrics shall make it possible to calculate realized byte savings per request/turn without recording raw path content.
6. The implementation shall include at least one representative long Windows path and one representative long POSIX path in benchmark/test fixtures.
7. The feature shall not intentionally increase repository test/QA cost budgets without explicit maintainer authorization.
