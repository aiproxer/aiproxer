# Implementation Plan — 1569 Effort-Values ABI

- [x] 1. Public ABI substrate (minor 11 + DTO + codec + negotiation gates)
  - Add proto message + field 15, regenerate with pinned buf, SDK DTO/validation/mapper, bounds/protocol helper, convert encode/decode, server/host gating, docs paragraph.
  - RED: validation/mapping table, 10↔11 round-trips, unnegotiated suppression, malformed rejection.
  - _Requirements: 1.1–1.4, 2.1–2.5_
  - _Boundary: `pkg/aipsdk/backendplugin`, `api/backendplugin/v1`_
  - _Depends: none_
  - _Validation: `go test ./pkg/aipsdk/backendplugin/... -count=1`, `make proto-check`_

- [x] 2. Adapter wiring to existing core resolver
  - Set `ResolveEffortSupport` closure when negotiated; exact backend-facing model match; metadata-only Resolve with caller timeout; RPC failure → Unknown.
  - RED: supported/exhaustive-negative/partial/mismatch/timeout via fake connector bufconn.
  - _Requirements: 3.1–3.4_
  - _Boundary: `internal/infra/backendplugins/adapter` (SDK→core boundary)_
  - _Depends: 1_
  - _Validation: `go test ./internal/infra/backendplugins/adapter/... -count=1`_

- [x] 3. Real producers (OpenAI Responses + Claude ACP)
  - OpenAI: exact native model table on official endpoint only; Claude: read-only snapshot lookup + Describe minor/feature + packaged protocol-range parity; both with deterministic fixtures, no network/login.
  - RED: `high→Supported`, `none→Unsupported`, unknown model/compatible endpoint→Unknown; Claude proven/unproven/discovery-absent.
  - _Requirements: 4.1–4.2_
  - _Boundary: `internal/plugins/backends/openairesponses`, `connectors/claudeagentacp`_
  - _Depends: 1_
  - _Validation: `go test ./internal/plugins/backends/openairesponses/... -count=1`, `cd connectors/claudeagentacp && go test ./...`_

- [x] 4. Guard-path acceptance and delivery evidence
  - One composed `ResolveProfile`→adapter→`EffectiveEffortSupport`→production guard test (cases A–H abridged); `kirocheck`, `dev-test-changed`, CI-equivalents, `dev-delivery-plan`; PR body with head SHA + evidence.
  - _Requirements: 4.3–4.5_
  - _Boundary: `internal/infra/runtimebundle`, `internal/plugins/features/proxymemory` fixtures_
  - _Depends: 2, 3_
  - _Validation: `go test ./internal/infra/runtimebundle/... ./internal/plugins/features/proxymemory/... -count=1`_

## Completion Status

- [x] All four implementation tasks delivered in PR #1574, merged 2026-10-28 as `303bc48e082d76a8b9234fe55467d5163ec5d008` (issue #1569 closed).
- [x] Verified 2026-11-10 at the closeout-branch head based on `origin/main` `4c6f4c99b`; every listed validation was re-run in the clean closeout worktree and passed:
  - Task 1: `go test ./pkg/aipsdk/backendplugin/... -count=1` pass; `make proto-check` pass (buf lint, buf breaking, generation freshness).
  - Task 2: `go test ./internal/infra/backendplugins/adapter/... -count=1` pass.
  - Task 3: `go test ./internal/plugins/backends/openairesponses/... -count=1` pass; `GOWORK=off go test ./... -count=1` in `connectors/claudeagentacp` pass.
  - Task 4: `go test ./internal/infra/runtimebundle/... ./internal/plugins/features/proxymemory/... -count=1` pass; `TestProxyMemoryEffortValuesComposedPath` and `TestProxyMemoryExecutorEffortValuesPreOpenGuard` pass.
- [x] Deferred scope stays deferred: global effort vocabulary/normalization (#462), capability-aware routing (#495), operator UI, broad provider coverage, vendor API probing, cross-instance caching, new diagnostics/ratchets, and memory settings locks/quotes were not delivered by this spec.
