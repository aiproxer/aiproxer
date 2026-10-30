# backendplugin/v1 wire contract

Protobuf/gRPC ABI for executable backend connectors.

## Pinned tool versions

Pinned in the repository root `go.mod` via Go 1.26 `tool` directives:

| Tool | Version |
|---|---|
| buf CLI | 1.66.0 |
| protoc-gen-go | v1.36.12 |
| protoc-gen-go-grpc | v1.5.1 |

## Generate

From repository root:

```bash
go install tool
cd api
buf generate --template buf.gen.yaml
```

Confirm generated headers report `protoc-gen-go v1.36.12` and `protoc-gen-go-grpc v1.5.1`. Do not hand-edit `*.pb.go`.

Verify the contract gate (buf lint, breaking-change detection, generation
freshness) from repository root with `make proto-check` (POSIX:
`bash scripts/proto-check.sh`, Windows:
`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/proto-check.ps1`).

`Invocation.proxy_owned_session_id` is field 20 and is additive. It is usable
only after protocol minor 4 negotiation with the `proxy_owned_session_id`
feature. Older hosts must reject calls that carry proxy-owned session authority;
silently dropping the authority would make native-context partitioning unsafe.

`ExecuteServerFrame.accounting_evidence` is a host-only, additive sideband
introduced at protocol minor 5 and gated by `accounting_evidence_sideband`.
It carries bounded provider billing evidence with explicit counter presence,
source, authority, plane, and dedupe key. It is not a canonical event and must
be consumed by the host exactly once; older peers must disable native compaction
rather than synthesize a native usage lifecycle.

`ExecuteServerFrame.accounting_evidence_v2` and
`FinalizeBillingResponse.accounting_evidence_v2` are additive protocol-minor-9
payloads gated by `accounting_evidence_v2`. They carry the canonical typed
`metering.Observation` envelope, including directional/native media units,
exact decimal values, source revisions, charge coverage, subject/correlation,
and allowlisted safe evidence fields. The payload is host-only: it must never
be projected as a canonical client event. A V1 connector can use the explicit
partial token-only bridge, but must not claim complete V2 coverage. Strict
offers require the negotiated feature; unsupported V2 evidence fails closed.

`FactoryDescriptor.supports_accounting_evidence_v2` and
`ResolvedProfile.supports_accounting_evidence_v2` are explicit capability
metadata. The feature/minor negotiation remains authoritative for a live
session, and all V1 field numbers and meanings remain unchanged.

`Invocation.semantic_extensions` is an additive minor-6 carrier gated by
`semantic_extensions_v1`. It is optional and hosts must not advertise or emit it
for a peer that cannot negotiate minor 6. The carrier preserves one bounded
presence-bearing residual with closed identity syntax and direction; it is not a
request/response envelope tunnel. `Invocation.prompt_cache_key` remains the
legacy minor-3 compatibility field, but bridge code emits only one authority and
rejects conflicting alias/carrier values.

`ResolvedProfile.reasoning_effort_values` is an additive protocol-minor-11
payload gated by `reasoning_effort_values_v1`. It carries the provider-owned
graded reasoning-effort declaration for the exact requested model
(`model_id`, `supported`, `exhaustive`). Absent means the provider published
no fact (Unknown). Exact case-sensitive membership decides Supported;
an exhaustive omission decides Unsupported; anything else stays Unknown.
Model-agnostic Resolve calls leave the field absent, and unnegotiated peers
never send or trust it.
