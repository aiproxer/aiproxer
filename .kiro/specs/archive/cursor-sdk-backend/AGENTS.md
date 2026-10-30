# Agent Rules — cursor-sdk-backend

## Scope

Active Kiro specification for an experimental Cursor SDK backend delivered as an
**external** connector artifact. Spec-only until a later implementation task
explicitly authorizes product code.

## Non-negotiables

- Implementation lives under `connectors/cursorsdk/` (Go module + Node bridge companion).
- Forbidden: `internal/plugins/backends/cursorsdk`, root `package.json` / Node deps, root `go.mod` require/replace of the connector, core/factory Cursor SDK types.
- Public ABI: `pkg/lipsdk/backendplugin` + closed `golip.backendplugin.manifest/v1`.
- Host contracts: trusted-directory discovery, digest-bound exact executable, approved secure local IPC, lazy activation.
- Process sharing: **per_instance** (justified in design.md).
- Preserve coexistence with external `cursorcliacp`; no silent failover after first content event.
- Validation: `make kiro-spec-check SPEC=cursor-sdk-backend`.

## Source alignment

Must stay consistent with `.kiro/specs/archive/backend-connector-plugin-architecture/`
(especially Requirements 1.6, 3.4, 4.3–4.4, 4.8, 7.2–7.4, 8.8, 10.1, 11.8, 12.10)
and current Go behavior in `pkg/lipsdk/backendplugin`,
`internal/infra/backendplugins/*`, and existing `connectors/*` release topology.
