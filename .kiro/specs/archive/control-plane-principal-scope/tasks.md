# Implementation Plan

- [x] 1. Establish the public principal/scope contract
- [x] 1.1 Add presence-aware safe attribution values and the authoritative scope snapshot
  - Represent unknown, known-empty, and known-populated attribution without overloading plain empty strings.
  - Include safe subject kind, principal identity, display label, auth method, credential identifier, roles, safe claims, tenant, organization, workspace, project, department, cost center, policy labels, and origin attribution.
  - Clone/copy behavior prevents callers from mutating roles, claims, labels, or nested attribution after receiving a view.
  - Done when package-local tests prove value semantics, clone isolation, field safety boundaries, and no raw credential/header fields exist in the public snapshot.
  - _Requirements: 1.2, 1.3, 2.5, 2.6, 3.1, 3.2, 3.3, 3.4, 3.6, 5.1, 5.2, 5.3, 5.5_
  - _Boundary: SDK/public contract_
  - _Validation: go test ./pkg/lipsdk/scope_

- [x] 1.2 Add the legacy principal projection from the authoritative scope
  - Derive the existing principal identity view from the authoritative scope instead of duplicating identity mapping rules.
  - Preserve existing principal identity, display label, roles, and claim compatibility for current feature consumers.
  - Done when projection tests show scope wins over any separate principal-only input and existing principal consumers receive the same compatible fields.
  - _Requirements: 1.1, 1.5, 4.6, 7.3_
  - _Boundary: SDK/public contract_
  - _Validation: go test ./pkg/lipsdk/scope ./pkg/lipsdk/execview_

- [x] 2. Normalize trusted authentication into request scope
- [x] 2.1 Add trusted scope carriage to authentication results and audit evidence
  - Allow authentication decisions and transport-auth principal results to carry an optional safe scope supplied by trusted auth code.
  - Keep raw bearer tokens, API keys, OAuth tokens, resume tokens, and transport headers outside the auth result and event scope data.
  - Done when auth contract tests compile with both legacy principal-only decisions and richer scope decisions, and auth evidence includes only safe attribution.
  - _Requirements: 2.1, 2.5, 2.6, 5.2, 6.1, 7.1_
  - _Boundary: SDK/public contract_
  - _Depends: 1.1, 1.2_
  - _Validation: go test ./pkg/lipsdk/auth ./pkg/lipsdk/transport/httpauth_

- [x] 2.2 Build precedence and denial tests for scope normalization
  - Cover precedence in this order: trusted scope, principal projection from trusted scope, legacy principal fallback, then allowed local synthetic fallback.
  - Cover denied or challenged decisions so they produce safe decision evidence without creating a successful request lifecycle snapshot.
  - Cover missing optional tenant, project, department, and cost-center values so they remain unknown and do not change allow/deny outcomes.
  - Done when the focused tests fail against current principal-only behavior and describe the intended normalizer behavior.
  - _Requirements: 1.1, 1.4, 1.6, 2.1, 2.2, 3.2, 3.5, 7.2, 8.5_
  - _Boundary: core/auth_
  - _Depends: 2.1_
  - _Validation: go test -run TestScope ./internal/core/auth_

- [x] 2.3 Implement trusted scope normalization and safety filtering
  - Normalize accepted auth decisions into one authoritative scope plus the derived legacy principal projection.
  - Preserve non-secret credential identifiers while omitting or rejecting unsafe attribution before execution begins.
  - Keep client-provided session, resume, or scope hints from elevating authority over trusted auth results.
  - Done when normalization tests pass and every returned principal view is derived from the returned scope snapshot.
  - _Requirements: 1.1, 1.4, 1.5, 2.1, 2.2, 2.5, 2.6, 3.2, 3.5, 5.4, 8.5_
  - _Boundary: core/auth_
  - _Depends: 2.2_
  - _Validation: go test ./internal/core/auth_

- [x] 2.4 Add operator-controlled local attribution configuration
  - Add optional local API key attribution for display label, auth method, credential id, tenant, organization, workspace, project, department, cost center, roles, safe claims, and policy labels.
  - Validate configured known values, roles, safe claim keys, and policy label keys at startup without changing raw key handling.
  - Done when config tests prove safe attribution is accepted, unsafe configured attribution is rejected, and missing optional fields remain unknown.
  - _Requirements: 2.5, 3.1, 3.2, 3.5, 5.4_
  - _Boundary: core/config_
  - _Depends: 2.3_
  - _Validation: go test ./internal/core/config_

- [x] 2.5 Map local API key and local no-auth requests to scope
  - Populate scope from validated local API key attribution while preserving non-secret credential identifiers and existing raw key behavior.
  - Mark allowed local no-auth requests as local single-user scope without inventing tenant, project, department, or cost-center values.
  - Done when local-auth tests prove configured API key attribution and local synthetic identity both produce safe scope snapshots.
  - _Requirements: 1.4, 2.4, 2.5, 3.1, 3.2, 3.5_
  - _Boundary: core/auth_
  - _Depends: 2.4_
  - _Validation: go test ./internal/core/auth_

- [x] 3. Attach scope at the HTTP trust boundary
- [x] 3.1 Bridge accepted HTTP auth decisions into request context
  - Attach both authoritative scope and the derived legacy principal projection for accepted requests before proxy execution begins.
  - Preserve current denial, challenge, and frontend response shapes for rejected requests.
  - Done when middleware tests prove accepted requests carry matching scope/principal views and rejected requests do not carry a successful lifecycle scope.
  - _Requirements: 1.1, 1.5, 1.6, 2.1, 2.3, 4.1, 6.1, 7.1, 7.3_
  - _Boundary: stdhttp/auth_
  - _Depends: 2.3_
  - _Validation: go test ./internal/stdhttp/auth_

- [x] 3.2 Emit audit-safe authentication evidence with scope attribution
  - Include trace correlation, outcome, reason, and safe principal/scope attribution in auth decision evidence where available.
  - Keep raw credentials, raw headers, unvetted claim values, and resume authority out of emitted evidence.
  - Done when auth adapter tests prove success and failure evidence contain safe scope identifiers only and preserve existing event compatibility fields.
  - _Requirements: 2.6, 5.2, 5.3, 6.1, 6.5, 7.1_
  - _Boundary: stdhttp/auth_
  - _Depends: 3.1_
  - _Validation: go test ./internal/stdhttp/auth_

- [x] 4. Carry immutable scope through execution
- [x] 4.1 Add scope to execution views with immutable copy semantics
  - Make the authoritative scope available alongside principal, session, attempt, workspace, and annotation views.
  - Keep lifecycle annotations separate from trusted attribution and copy maps/slices on insert and read.
  - Done when execution-view tests prove scope mutation after attach cannot affect stored views and annotations do not modify attribution.
  - _Requirements: 4.2, 4.3, 4.6, 5.1, 5.5_
  - _Boundary: core/execctx_
  - _Depends: 1.1, 1.2_
  - _Validation: go test ./internal/core/execctx_

- [x] 4.2 Resolve one scope before secure-session and backend execution
  - Read scope from trusted context when present, derive from legacy principal only when no scope exists, and create local synthetic scope only under existing local-mode conditions.
  - Pass only the principal and workspace fields secure-session needs, without making secure-session own the richer attribution model.
  - Done when runtime tests prove scope is present before backend work and secure-session receives a principal reference derived from the same scope.
  - _Requirements: 1.1, 1.4, 2.2, 2.4, 4.1, 4.6, 6.2, 7.2, 7.5_
  - _Boundary: core/runtime_
  - _Depends: 2.5, 3.1, 4.1_
  - _Validation: go test -run Test.*Scope ./internal/core/runtime ./internal/core/securesession/...

- [x] 4.3 Preserve scope across auxiliary requests and backend attempts
  - Preserve parent principal/scope attribution for internally derived requests and mark derived origin separately from trusted attribution.
  - Keep all backend attempts for one logical request associated with the same authoritative request scope.
  - Done when runtime lineage tests prove parent scope correlation survives internal requests, retries before output, and multi-attempt execution without changing recovery semantics.
  - _Requirements: 4.4, 4.5, 6.3, 6.5, 7.5_
  - _Boundary: core/runtime_
  - _Depends: 4.2_
  - _Validation: go test -run Test.*Scope ./internal/core/runtime_

- [x] 5. Propagate scope through observers
- [x] 5.1 Add safe scope attribution to usage and traffic observer contracts
  - Add scope as optional event metadata while preserving existing principal identifier fields for compatibility.
  - Keep observer implementations from needing to understand scope to keep working.
  - Done when usage and traffic contract tests compile for existing observer fixtures and prove principal identifiers match the authoritative scope.
  - _Requirements: 6.3, 6.4, 6.5, 7.3, 7.6_
  - _Boundary: SDK/public contract_
  - _Depends: 1.1, 1.2_
  - _Validation: go test ./pkg/lipsdk/usage ./pkg/lipsdk/traffic_

- [x] 5.2 Emit scope on runtime usage and traffic evidence
  - Include safe scope in usage and traffic observations emitted from runtime attempts without changing observer ordering or delivery.
  - Keep scope out of backend provider payloads, client-facing protocol responses, and high-cardinality metric labels.
  - Done when observer integration tests prove usage and traffic evidence contain safe scope, preserve legacy principal id, and leave backend calls unchanged.
  - _Requirements: 5.1, 5.2, 6.3, 6.4, 6.5, 7.4, 7.6_
  - _Boundary: core/runtime_
  - _Depends: 4.2, 5.1_
  - _Validation: go test -run Test.*Scope ./internal/core/runtime_

- [x] 6. Prove compatibility and explicit boundaries
- [x] 6.1 Verify client protocol compatibility and routing/session neutrality
  - Preserve current frontend request and response shapes while principal/scope attribution stays internal to the proxy.
  - Prove missing optional tenant, project, department, and cost-center attribution does not alter routing, secure-session eligibility, backend attempt selection, or non-streaming collection.
  - Done when focused compatibility tests pass for frontend shapes, missing optional scope, and streaming versus non-streaming scope consistency.
  - _Requirements: 7.1, 7.2, 7.4, 7.5, 7.6, 8.5_
  - _Boundary: tests/integration_
  - _Depends: 4.3, 5.2_
  - _Validation: go test ./internal/stdhttp/... ./internal/plugins/frontends/... ./internal/core/runtime/...

- [x] 6.2 Verify secret-safety across auth, session, usage, and traffic evidence
  - Prove bearer tokens, API keys, OAuth tokens, resume tokens, raw transport headers, and unsafe claim values never appear in safe scope, auth evidence, session evidence, usage events, or traffic observations.
  - Prove roles, safe claims, and policy labels are copied before exposure and cannot mutate authoritative request scope.
  - Done when security-focused tests pass across the edge, runtime, observer, and execution-view paths.
  - _Requirements: 2.6, 5.1, 5.2, 5.3, 5.4, 5.5, 6.1, 6.2, 6.4_
  - _Boundary: tests/security_
  - _Depends: 3.2, 4.1, 5.2_
  - _Validation: go test ./internal/stdhttp/auth ./internal/core/runtime ./internal/core/execctx ./pkg/lipsdk/scope

- [x] 6.3 Verify this remains attribution-only foundation work
  - Prove the feature does not add OAuth/SAML provisioning, billing, budgeting, rate limiting, allowance management, spend enforcement, redaction engines, dangerous-tool policy, policy decision engines, admin GUI flows, reporting charts, or cross-session search.
  - Prove principal/scope availability by itself does not change allow/deny outcomes when later enforcement features are absent.
  - Done when boundary tests and existing architecture checks pass without new provider-facing scope forwarding or new enforcement/admin surfaces.
  - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5_
  - _Boundary: tests/architecture_
  - _Depends: 6.1, 6.2_
  - _Validation: go test ./internal/archtest/... ./internal/qa/... ./internal/core/runtime/...

- [x] 7. Run final focused verification for the completed task graph
- [x] 7.1 Run the scope feature's focused verification commands
  - Run the package tests that cover scope contracts, auth normalization, HTTP auth bridging, execution views, runtime propagation, observers, and compatibility checks.
  - Repair only failures caused by this feature, preserving unrelated user changes.
  - Done when the focused command set passes and provides direct evidence for implementation readiness.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 5.1, 5.2, 5.3, 5.4, 5.5, 6.1, 6.2, 6.3, 6.4, 6.5, 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 8.1, 8.2, 8.3, 8.4, 8.5_
  - _Boundary: tests/verification_
  - _Depends: 6.3_
  - _Validation: go test ./pkg/lipsdk/scope ./pkg/lipsdk/auth ./pkg/lipsdk/transport/httpauth ./pkg/lipsdk/usage ./pkg/lipsdk/traffic ./internal/core/auth ./internal/core/config ./internal/stdhttp/auth ./internal/core/execctx ./internal/core/runtime ./internal/archtest/...

## Verification Evidence

Independent verification ran against the working tree on the maintained branch to record completion evidence for the boundary and final-verification task IDs that list multi-package command sets or external guardrails.

- Task **6.1** (compatibility, frontend/routing/session neutrality): covered by the overlap of task 7.1's selection with the broader `make test-unit` gate (`internal/stdhttp/...`, `internal/plugins/frontends/...`, `internal/core/runtime/...` all green).
- Task **6.2** (secret-safety across auth, session, usage, traffic): covered by task 7.1's selection — `internal/stdhttp/auth`, `internal/core/runtime`, `internal/core/execctx`, and `pkg/lipsdk/scope` all green; the 11 `_scope_test.go` files plus the in-package scope tests collectively assert no raw credential/header/resume values reach safe scope or evidence.
- Task **6.3** (boundary / attribution-only foundation): `internal/archtest` exited 0; guardrail tests enforce dependency direction and forbid provider-SDK, policy-engine, billing, or admin-GUI code on the affected paths.
- Task **7.1** (focused verification command set): the exact 11-package command set in this task's `_Validation` field passed (exit 0 on each package). The broader `make test-unit` gate (full-repo `go test -parallel=8 -timeout=10m ./...`) also returned 0 across all packages.
