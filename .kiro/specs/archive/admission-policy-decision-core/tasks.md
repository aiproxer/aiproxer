# Implementation Plan

- [x] 1. Establish shared policy decision contracts
- [x] 1.1 Add the public decision vocabulary and record model
  - Define protocol-neutral outcomes, effects, failure behavior, evidence visibility, provider identity, decision records, and decision context values.
  - Preserve unknown zero values and safe defaults so invalid or omitted decisions can be rejected deterministically.
  - Add clone and copy behavior for mutable fields so callers and observers cannot mutate shared record state.
  - Done when public SDK tests prove zero-value validation, cloning, JSON shape, and absence of provider or frontend imports.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.6, 8.5, 9.3_
  - _Boundary: SDK/public contract_
  - _Validation: go test -run TestPolicyDecision ./pkg/lipsdk/policydecision_

- [x] 1.2 Add readable SDK legality descriptors
  - Encode the approved stage/outcome/effect legality table against the existing legal extension stage IDs.
  - Expose the legal decision set for diagnostics and tests without creating a second stage taxonomy.
  - Keep the public descriptor surface read-only and provider-neutral.
  - Done when SDK tests cover every legal stage ID and prove replay and swallow appear only at their approved stages.
  - _Requirements: 1.5, 3.6, 4.4, 6.6_
  - _Boundary: SDK/public contract_
  - _Depends: 1.1_
  - _Validation: go test -run 'Test.*Decision.*Legal|Test.*Policy.*Legal' ./pkg/lipsdk/policydecision_

- [x] 1.3 Add core legality validation
  - Validate decision records emitted by core against the SDK legality descriptors.
  - Reject unknown stages, unknown outcomes, unknown effects, and illegal outcome/effect pairs as malformed policy decisions.
  - Preserve deterministic conflict handling by leaving runtime application order with the existing sorted stage runners.
  - Done when core tests reject malformed stage/outcome/effect combinations and preserve existing runner-selected outcomes.
  - _Requirements: 1.5, 3.6, 4.4, 6.6_
  - _Boundary: core/extensions_
  - _Depends: 1.2_
  - _Validation: go test -run 'Test.*Decision.*Legal|Test.*Malformed' ./internal/core/extensions_

- [x] 1.4 Add bounded evidence normalization and observer contracts
  - Normalize provider identifiers, trace identifiers, reason codes, client categories, client messages, annotations, and scope clones before records leave the core.
  - Add no-op and chain observer contracts with cloned record delivery semantics.
  - Keep raw prompts, payloads, headers, credentials, and unsafe claims out of default records.
  - Done when SDK tests prove normalized cloned records are delivered through observer contracts and observer mutation cannot change source records.
  - _Requirements: 7.1, 7.3, 7.6, 7.7_
  - _Boundary: SDK/public contract_
  - _Depends: 1.1_
  - _Validation: go test -run 'Test.*Normalize|Test.*Observer' ./pkg/lipsdk/policydecision_

- [x] 1.5 Add stable policy error roots
  - Add policy-denied, policy-failure, and malformed-policy error roots with stage, provider, reason, category, message, and cause information.
  - Support errors.Is and errors.As classification without exposing raw prompts, backend payloads, secrets, or unsafe claims.
  - Keep policy failures distinct from existing reject, session, capability, backend, auth, and internal errors.
  - Done when API tests prove each root wraps and classifies separately with only client-safe fields.
  - _Requirements: 1.3, 5.1, 5.4, 5.5, 5.6, 6.1, 6.5, 6.6, 7.2_
  - _Boundary: API/public contract_
  - _Depends: 1.1_
  - _Validation: go test -run TestPolicy ./pkg/lipapi_

- [x] 1.6 Add core policy error conversion helpers
  - Convert malformed decisions, provider failures, timeout failures, and fail-closed policy outcomes into stable policy errors.
  - Preserve context cancellation as cancellation instead of converting it into policy denial, failure, or malformed errors.
  - Done when core tests prove conversion helpers classify through the public roots and preserve safe messages.
  - _Requirements: 5.1, 5.4, 5.5, 5.6, 6.1, 6.4, 6.5, 6.6, 7.2_
  - _Boundary: core/extensions_
  - _Depends: 1.3, 1.5_
  - _Validation: go test -run 'Test.*Policy.*Error|Test.*Decision.*Error' ./internal/core/extensions_

- [x] 1.7 Add core evidence emission and diagnostics gating
  - Emit normalized policy decision records to the configured policy observer and bounded structured logs.
  - Enforce privileged-visibility gating so privileged records or details are withheld unless explicit diagnostics exposure posture is enabled.
  - Keep high-cardinality values out of metric labels and keep observer failures from changing request execution in this spec.
  - Done when core tests prove default evidence is bounded, privileged visibility is gated, and observer failures are isolated from runtime outcomes.
  - _Requirements: 7.1, 7.3, 7.4, 7.6, 7.7_
  - _Boundary: core/extensions_
  - _Depends: 1.4, 1.6_
  - _Validation: go test -run 'Test.*Decision.*Evidence|Test.*Visibility|Test.*Observer' ./internal/core/extensions_

- [x] 1.8 Add timeout budget enforcement for decision providers
  - Introduce a frozen timeout-budget source with a zero-timeout default that preserves legacy behavior.
  - Enforce non-zero provider budgets with child contexts and isolated mutable inputs so late provider results cannot mutate live call or stream state.
  - Preserve parent cancellation as cancellation, while provider timeouts produce fail-open skip or fail-closed policy failure evidence.
  - Done when tests distinguish provider timeout, parent cancellation, parent deadline, fail-open skip, and fail-closed failure behavior.
  - _Requirements: 6.1, 6.2, 6.3, 6.4_
  - _Boundary: core/extensions_
  - _Depends: 1.7_
  - _Validation: go test -run TestDecisionProviderTimeout ./internal/core/extensions_

- [x] 2. Carry safe scope and decision context through extension metadata
- [x] 2.1 Add authoritative scope to existing SDK metadata
  - Add safe scope fields to existing request, pre-request, tool policy, tool reactor, and completion metadata while preserving legacy principal/session/workspace fields.
  - Preserve source compatibility for existing plugin implementations and keep nil or zero values meaningful for local or anonymous operation.
  - Done when metadata tests prove scope values clone safely and legacy principal fields still project as before.
  - _Requirements: 2.1, 2.2, 2.3, 2.5, 2.6, 9.1, 9.6_
  - _Boundary: SDK/public contract_
  - _Depends: 1.1_
  - _Validation: go test -run 'Test.*Scope|Test.*Meta' ./pkg/lipsdk/...

- [x] 2.2 Build decision context from accepted request views
  - Assemble decision context from trusted execution views, secure-session lineage, workspace, session, annotations, provider identity, output-committed state, and timeout budget.
  - Preserve internal or auxiliary origin and parent trace attribution without using client payloads or unvetted claims as trusted scope.
  - Done when core tests prove contexts include safe scope, legacy principal projection, internal provenance, and no raw credentials or headers.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 7.5_
  - _Boundary: core/extensions_
  - _Depends: 1.1, 2.1_
  - _Validation: go test -run TestDecisionContext ./internal/core/extensions ./internal/core/execctx_

- [x] 2.3 Wire decision observation and timeout sources into request snapshots
  - Carry the policy decision observer and timeout budget source through frozen per-request runtime snapshots.
  - Provide disabled and no-op defaults so deployments without policy evidence observers keep current request outcomes.
  - Done when snapshot tests prove defensive copies, frozen access, and default no-op behavior.
  - _Requirements: 6.3, 7.6, 9.1, 10.5_
  - _Boundary: core/extensions_
  - _Depends: 1.7, 1.8_
  - _Validation: go test -run TestRequestRuntimeSnapshot ./internal/core/extensions_

- [x] 3. Project existing extension outcomes into shared evidence
- [x] 3.1 Project request-transform outcomes
  - Emit decision evidence for request transform pass-through, mutation, provider failure, timeout, and malformed validation outcomes.
  - Preserve existing transform order, fail-open/fail-closed behavior, isolated timeout commit behavior, and canonical call validation.
  - Done when tests show request mutation behavior is unchanged while ordered policy records are emitted.
  - _Requirements: 1.4, 1.7, 3.2, 4.1, 4.3, 4.6, 8.4, 9.1, 9.2, 9.5_
  - _Boundary: core/extensions_
  - _Depends: 1.3, 1.7, 1.8, 2.2, 2.3_
  - _Validation: go test -run 'TestRunRequestTransform|Test.*Decision' ./internal/core/extensions_

- [x] 3.2 Project pre-request admission outcomes
  - Emit decision evidence for pre-request allows, denials, annotations, skips, provider failures, timeouts, malformed outcomes, and no-backend-attempt denials.
  - Preserve existing handler order, denial return behavior, auxiliary-depth suppression, and fail-open/fail-closed behavior.
  - Done when tests show denial behavior is unchanged while policy records distinguish no-backend-attempt denials from backend failures.
  - _Requirements: 1.4, 1.7, 3.1, 4.1, 4.2, 4.3, 4.6, 8.1, 8.6, 9.1, 9.2, 9.5_
  - _Boundary: core/extensions_
  - _Depends: 3.1_
  - _Validation: go test -run 'TestRunPreRequest|Test.*Decision' ./internal/core/extensions_

- [x] 3.3 Project tool policy and tool reactor outcomes
  - Emit policy evidence for canonical tool allow, deny, rewrite, replace, swallow, provider failure, timeout, and malformed outcomes.
  - Preserve tool policy before tool reactor ordering and keep frontend-specific tool syntax out of evidence semantics.
  - Done when tests show denied tool calls stop correctly and tool reactor rewrites, replacements, and swallow behavior remain unchanged.
  - _Requirements: 1.7, 3.3, 4.1, 4.2, 4.3, 4.6, 6.1, 6.2, 6.6, 8.3, 9.1, 9.2, 9.5_
  - _Boundary: core/extensions, core/hooks_
  - _Depends: 1.3, 1.7, 1.8, 2.2, 2.3_
  - _Validation: go test -run 'TestRunToolPolicy|Test.*ToolReactor|Test.*Decision' ./internal/core/extensions ./internal/core/hooks_

- [x] 3.4 Project completion-gate and stream-stage outcomes
  - Emit policy evidence for pass, replace, replay, reject, ignored post-output replacement, provider failure, timeout, and malformed completion outcomes.
  - Preserve the streaming path, completion buffer behavior, deterministic event ordering, and no-failover-after-output invariant.
  - Done when tests show completion replace/reject evidence is emitted and post-output replacement still cannot trigger transparent retry.
  - _Requirements: 1.4, 1.7, 3.4, 4.1, 4.3, 4.4, 5.2, 8.2, 8.3, 9.1, 9.2, 9.5_
  - _Boundary: core/extensions, core/runtime stream path_
  - _Depends: 1.3, 1.7, 1.8, 2.2, 2.3_
  - _Validation: go test -run 'TestApplyCompletionGate|Test.*Completion.*Decision|Test.*NoFailover' ./internal/core/extensions ./internal/core/runtime_

- [x] 3.5 Project submit and tool-catalog stage outcomes
  - Emit compatible evidence for submit annotations and rejections through the existing submit hook behavior.
  - Emit compatible evidence for tool catalog shaping without changing advertised-tool mutation behavior.
  - Skip evidence for outcomes that cannot be represented without inventing new submit or tool-catalog semantics.
  - Done when tests show submit and tool catalog runtime effects remain unchanged while each stage family emits distinct policy records.
  - _Requirements: 1.7, 4.1, 4.3, 4.5, 4.6, 7.2, 7.5, 8.5, 9.1, 9.2, 9.3, 9.5_
  - _Boundary: core/extensions, core/hooks_
  - _Depends: 1.3, 1.7, 2.2, 2.3_
  - _Validation: go test -run 'Test.*Submit|Test.*ToolCatalog|Test.*Decision' ./internal/core/extensions ./internal/core/hooks_

- [x] 3.6 Project advisory route and observation outcomes
  - Emit compatible evidence for route hint decisions and attempt lifecycle observations while preserving advisory route semantics.
  - Keep usage and traffic observers independent so they do not need to interpret policy semantics, and prove policy evidence remains distinguishable from observer output.
  - Skip evidence for route, attempt, traffic, or usage outcomes that cannot be represented without changing observer or routing contracts.
  - Done when tests show route hints and observers retain their existing runtime effects while policy evidence remains distinguishable and independent.
  - _Requirements: 4.5, 7.2, 7.5, 7.6, 8.5, 9.1, 9.2, 9.3, 9.5, 10.5_
  - _Boundary: core/extensions, SDK observer seams_
  - _Depends: 1.3, 1.7, 2.2, 2.3_
  - _Validation: go test -run 'Test.*RouteHint|Test.*Observer|Test.*Decision' ./internal/core/extensions ./pkg/lipsdk/traffic ./pkg/lipsdk/usage_

- [x] 4. Integrate policy decisions into runtime and frontend behavior
- [x] 4.1 Integrate pre-backend decisions with secure-session and routing flow
  - Run decision projection only after secure-session authority is established and before route planning commits a backend attempt.
  - Record policy denials as no-backend-attempt outcomes distinct from backend failures.
  - Ensure capability negotiation observes the effective request after policy-controlled request shaping.
  - Done when runtime tests show pre-request denial returns before backend opening and transformed calls drive capability checks.
  - _Requirements: 3.1, 3.2, 3.5, 5.1, 8.1, 8.4, 8.6_
  - _Boundary: core/runtime_
  - _Depends: 3.2, 3.6_
  - _Validation: go test -run 'Test.*Prepare.*Policy|Test.*PreRequest.*NoBackend|Test.*Effective.*Capability' ./internal/core/runtime_

- [x] 4.2 Integrate stream-stage decisions with active stream semantics
  - Surface post-output policy denials and failures on the active stream without transparent backend replacement.
  - Preserve canonical event ordering around tool policy, tool reactors, stream mutation, completion gates, usage observation, and traffic observation.
  - Done when stream tests prove policy outcomes after committed output do not trigger failover and emitted events remain in deterministic order.
  - _Requirements: 3.3, 3.4, 5.2, 8.2, 8.3_
  - _Boundary: core/runtime stream path_
  - _Depends: 3.3, 3.4_
  - _Validation: go test -run 'Test.*Stream.*Policy|Test.*Tool.*Stream|Test.*Completion.*Stream' ./internal/core/runtime_

- [x] 4.3 (P) Classify policy errors at frontend boundaries
  - Map policy denial, policy failure, and malformed policy errors to distinct frontend execution classifications.
  - Keep protocol-specific rendering in frontend adapters and use only client-safe category/message fields on the wire.
  - Done when OpenAI Responses, OpenAI legacy, Anthropic, and Gemini frontend tests classify policy errors separately from capability, auth, session, backend, and internal errors.
  - _Requirements: 5.1, 5.3, 5.4, 5.5, 5.6, 9.4_
  - _Boundary: frontend plugin helper_
  - _Depends: 1.5_
  - _Validation: go test -run 'TestClassifyExecute|Test.*Policy.*Error' ./internal/plugins/frontends/...

- [x] 4.4 Wire standard runtime defaults without adding concrete policy rules
  - Wire default/no-op policy decision observation and zero timeout budgets through standard runtime construction.
  - Avoid adding concrete budget, rate-limit, PII, prompt-injection, dangerous-tool, admin GUI, persistence, or provider-forwarding behavior.
  - Done when standard bundle/runtime tests show the decision foundation is available but no request outcome changes when no concrete policy provider is configured.
  - _Requirements: 9.3, 10.1, 10.2, 10.3, 10.4, 10.5_
  - _Boundary: config/wiring_
  - _Depends: 2.3, 4.1, 4.2, 4.3_
  - _Validation: go test -run 'Test.*RuntimeBundle|Test.*Noop.*Policy|Test.*Noninterference' ./internal/infra/runtimebundle ./internal/core/runtime_

- [x] 5. Validate safety, compatibility, and architecture guardrails
- [x] 5.1 Add public SDK and architecture guardrail tests
  - Prove the new SDK package does not import internal packages, frontend plugins, backend plugins, provider SDKs, or transport wire models.
  - Prove core decision code does not import concrete plugins or provider SDKs.
  - Done when architecture tests fail on forbidden dependency direction and pass for the implemented design.
  - _Requirements: 1.1, 8.5, 9.3_
  - _Boundary: tests/architecture_
  - _Depends: 1.1, 1.2, 1.3, 1.4, 1.5, 1.7_
  - _Validation: go test ./internal/archtest ./pkg/lipsdk/...

- [x] 5.2 Add focused runtime integration coverage
  - Cover pre-request denial evidence, no-backend-attempt lineage, request-transform capability revalidation, fail-open skipped evidence, and fail-closed stable failures.
  - Cover timeout and malformed outcomes through the same runtime paths used by real requests.
  - Done when integration tests prove policy outcomes are distinguishable from routing, backend, auth, session, and capability failures.
  - _Requirements: 5.6, 6.1, 6.2, 6.3, 6.5, 6.6, 7.1, 7.2, 8.1, 8.4, 8.6_
  - _Boundary: tests/runtime integration_
  - _Depends: 4.1, 4.2_
  - _Validation: go test -run 'Test.*Policy.*Runtime|Test.*Decision.*Runtime' ./internal/core/runtime ./internal/core/extensions_

- [x] 5.3 (P) Add frontend/protocol compatibility coverage
  - Verify successful response shapes are unchanged when no policy changes request or response content.
  - Verify client-facing policy messages exclude provider IDs when unsafe, raw prompts, raw backend payloads, secrets, headers, and unsafe claims.
  - Done when frontend protocol tests pass across OpenAI Responses, OpenAI legacy, Anthropic, and Gemini surfaces.
  - _Requirements: 5.3, 5.4, 5.5, 9.4, 10.5_
  - _Boundary: tests/frontend protocols_
  - _Depends: 4.3, 4.4_
  - _Validation: go test -run 'Test.*Policy|Test.*Success.*Shape' ./internal/plugins/frontends/...

- [x] 5.4 Run repo quality gates for the completed feature
  - Run focused package tests for policydecision, extensions, runtime, frontend classification, runtimebundle, and architecture guardrails.
  - Run the default unit or quality target appropriate for the final diff and resolve regressions without broadening scope.
  - Done when verification evidence shows the focused packages and selected repo gate pass.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 5.1, 5.2, 5.3, 5.4, 5.5, 5.6, 6.1, 6.2, 6.3, 6.4, 6.5, 6.6, 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 7.7, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 10.1, 10.2, 10.3, 10.4, 10.5_
  - _Boundary: tests/quality gate_
  - _Depends: 5.1, 5.2, 5.3_
  - _Validation: make test-unit or make quality-checks
