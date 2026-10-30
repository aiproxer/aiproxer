# Design Document — 1569 Effort-Values ABI

## Overview

Extend the existing model-specific `ResolveProfile` with an optional `ReasoningEffortValuesV1` DTO gated by protocol minor 11 + feature `reasoning_effort_values_v1`. Host, plugin, adapter, and two real producers (OpenAI Responses in-process, Claude ACP executable) map the declaration to the existing `execbackend.EffortSupportState` tri-state consumed by the Proxy-Memory pre-open guard. Unknown stays default; no global vocabulary, no new RPC, no `pkg/aipapi` change.

### Goals

- Providers publish honest per-model graded effort facts over the versioned ABI.
- `EffectiveEffortSupport` returns Supported/Unsupported/Unknown to the actual guard.
- Legacy peers keep working with Unknown and normal inference.

### Non-Goals

- Global effort vocabulary, normalization/ceilings (#462); general capability routing (#495); operator UI; broad provider coverage; probing; persistence; new ratchets.

## Boundary Commitments

### This Spec Owns

- Proto field 15 + SDK DTO + validation + negotiation + host/plugin encode/decode gating.
- Adapter `ResolveEffortSupport` closure with exact-model mapping helper.
- OpenAI Responses table (official endpoint only) + Claude ACP read-only snapshot lookup.
- Guard-path regression tests proving Open counts and metric deltas.

### Out of Boundary

- Core policy changes (`EffectiveEffortSupport`, guard, counters), canonical contracts, manifest schema, routing/B2BUA, billing.

### Allowed Dependencies

- `pkg/aipsdk/backendplugin` (+host), `api/backendplugin/v1`, `internal/infra/backendplugins/adapter`, `internal/plugins/backends/openairesponses`, `connectors/claudeagentacp`, existing `runtimebundle`/`proxymemory` fixtures.

### Revalidation Triggers

- Plugin registration contract change: re-check negotiation/compat tests and `proto-check`.

## Architecture

### Seams Reused

- `ResolveProfile(model_id)` model-aware RPC; `Negotiation` minor/feature gate; `session.Resolve`; `execbackend.Backend.ResolveEffortSupport`; Proxy-Memory write/dispatch/pre-open guard fixtures.

### Project Boundary Questions (AIProxer)

- Core-owned or plugin-owned? Plugin-owned facts; core keeps tri-state query interface.
- New canonical concept, or adapter-specific behaviour? Adapter-specific; no `pkg/aipapi` change.
- Streaming-first path preserved? Yes; metadata-only Resolve, no inference.
- Provider SDK leakage avoided? Core never imports protobuf/connector SDK; adapter owns mapping.
- No retry/failover after first client-visible output? Yes; pre-open metadata only.
- Secure-session, diagnostics, startup-security posture affected? No; bounded safe tokens only.

## File Structure Plan

### New Files

- `pkg/aipsdk/backendplugin/reasoning_effort_values.go` — DTO, validation, pure mapping helper.
- `pkg/aipsdk/backendplugin/reasoning_effort_values_test.go` — validation + mapping table.
- `connectors/claudeagentacp/*_test.go` additions reuse existing service/product fixtures.

### Modified Files

- `api/backendplugin/v1/backend.proto` — message + field 15; regenerate `*.pb.go`.
- `api/backendplugin/v1/README.md`, `docs/backend-plugins/authoring.md` — feature paragraph.
- `pkg/aipsdk/backendplugin/bounds.go`, `protocol.go` — minor 11 + feature + helper.
- `pkg/aipsdk/backendplugin/convert_descriptor.go` — encode/decode with validation.
- `pkg/aipsdk/backendplugin/server.go` — validate/suppress unless negotiated.
- `pkg/aipsdk/backendplugin/host/session.go` — advertise minor 11 + feature; Resolve passthrough.
- `internal/infra/backendplugins/adapter/backend.go` — set `ResolveEffortSupport` closure.
- `internal/plugins/backends/openairesponses/plugin.go` (+ test) — exact-model table, official endpoint gate.
- `connectors/claudeagentacp/internal/product/spec.go` — read-only graded lookup.
- `connectors/claudeagentacp/internal/service/service.go` (+ tests) — Resolve facts, Describe minor/feature.
- `connectors/claudeagentacp/manifest/template.backendplugin.json` + `manifest_posture_parity_test.go` — packaged protocol range parity with the runtime descriptor.

## Components and Interfaces

| Component | Package | Intent | Requirements |
|-----------|---------|--------|--------------|
| EffortValues DTO | `backendplugin` | Validated declaration + exact-match mapper | 1.1, 1.3, 2.1–2.5 |
| Negotiation gate | `backendplugin` | Minor-11/feature helper | 1.1, 1.2 |
| Host/plugin codec | `backendplugin` | Presence-preserving encode/decode | 1.1–1.4 |
| Adapter closure | `adapter` | Session Resolve → core tri-state | 3.1–3.4 |
| OpenAI table | `openairesponses` | Official-endpoint exact-model facts | 4.1 |
| Claude lookup | `claudeagentacp` | Snapshot graded lookup | 4.2 |
| Guard proof | `runtimebundle`+`proxymemory` fixtures | End-to-end Supported/Unsupported/Unknown | 4.3–4.5 |

#### EffortValues mapper

```go
type ReasoningEffortValuesV1 struct {
  ModelID    string
  Supported  []string
  Exhaustive bool
}
func (v *ReasoningEffortValuesV1) Validate() error
func ResolveEffortSupportState(decl *ReasoningEffortValuesV1, model, effort string) EffortSupportState
func ReasoningEffortValuesNegotiated(neg Negotiation) bool
```

- `nil` declaration means no statement. Failed validation never yields Unsupported.
- Exact case-sensitive membership; mismatched model ID yields Unknown.

## Error Handling

Fail-open to Unknown on missing/invalid/unnegotiated/RPC failure/timeout/malformed peer; fail-closed only where the existing guard already refuses (Unsupported, strict Unknown). No new error taxonomy; no value echo in logs.

## Testing Strategy

- Table: validation (blank/control/dup/oversize/bad UTF-8/wrong model) + mapping (supported/exhaustive-negative/partial/mismatch).
- Integration: bufconn composed `ResolveProfile` → host session → adapter → `EffectiveEffortSupport` → production guard; cases A–H from handoff §6 with Open/Execute counts and metric deltas.
- Executor: real `Executor.Execute` with the production guard adapter, a bufconn host session, and an executable fake connector proves supported/unsupported/unknown pre-open behavior and the `model_policy_unverified_effort` delta.
- Fail-open: RPC error/timeout/unnegotiated peer keeps Unknown and ordinary traffic.
- Existing generic architecture rules extended: none; manifest schema unchanged.

## Configuration

None. Optional feature negotiated per session; no operator flags.
