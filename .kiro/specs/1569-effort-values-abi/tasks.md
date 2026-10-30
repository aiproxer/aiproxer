# Implementation Plan — 1569 Effort-Values ABI

- [ ] 1. Public ABI substrate (minor 11 + DTO + codec + negotiation gates)
  - Add proto message + field 15, regenerate with pinned buf, SDK DTO/validation/mapper, bounds/protocol helper, convert encode/decode, server/host gating, docs paragraph.
  - RED: validation/mapping table, 10↔11 round-trips, unnegotiated suppression, malformed rejection.
  - _Requirements: 1.1–1.4, 2.1–2.5_
  - _Boundary: `pkg/aipsdk/backendplugin`, `api/backendplugin/v1`_
  - _Depends: none_
  - _Validation: `go test ./pkg/aipsdk/backendplugin/... -count=1`, `make proto-check`_

- [ ] 2. Adapter wiring to existing core resolver
  - Set `ResolveEffortSupport` closure when negotiated; exact backend-facing model match; metadata-only Resolve with caller timeout; RPC failure → Unknown.
  - RED: supported/exhaustive-negative/partial/mismatch/timeout via fake connector bufconn.
  - _Requirements: 3.1–3.4_
  - _Boundary: `internal/infra/backendplugins/adapter` (SDK→core boundary)_
  - _Depends: 1_
  - _Validation: `go test ./internal/infra/backendplugins/adapter/... -count=1`_

- [ ] 3. Real producers (OpenAI Responses + Claude ACP)
  - OpenAI: exact native model table on official endpoint only; Claude: read-only snapshot lookup + Describe minor/feature + packaged protocol-range parity; both with deterministic fixtures, no network/login.
  - RED: `high→Supported`, `none→Unsupported`, unknown model/compatible endpoint→Unknown; Claude proven/unproven/discovery-absent.
  - _Requirements: 4.1–4.2_
  - _Boundary: `internal/plugins/backends/openairesponses`, `connectors/claudeagentacp`_
  - _Depends: 1_
  - _Validation: `go test ./internal/plugins/backends/openairesponses/... -count=1`, `cd connectors/claudeagentacp && go test ./...`_

- [ ] 4. Guard-path acceptance and delivery evidence
  - One composed `ResolveProfile`→adapter→`EffectiveEffortSupport`→production guard test (cases A–H abridged); `kirocheck`, `dev-test-changed`, CI-equivalents, `dev-delivery-plan`; PR body with head SHA + evidence.
  - _Requirements: 4.3–4.5_
  - _Boundary: `internal/infra/runtimebundle`, `internal/plugins/features/proxymemory` fixtures_
  - _Depends: 2, 3_
  - _Validation: `go test ./internal/infra/runtimebundle/... ./internal/plugins/features/proxymemory/... -count=1`_
