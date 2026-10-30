# Validation Checklist — cursor-sdk-backend (Task 8.3)

Executable gate: `make kiro-spec-check SPEC=cursor-sdk-backend`.

## Required artifacts present

- [x] `AGENTS.md`, `requirements.md`, `design.md`, `tasks.md`, `research.md`, `spec.json`
- [x] `file-plan.md`, `packaging.md`
- [x] This checklist

## Required content needles (must appear in normative specs)

- [x] External module path `connectors/cursorsdk`
- [x] Public ABI `pkg/lipsdk/backendplugin`
- [x] Closed manifest `golip.backendplugin.manifest/v1`
- [x] `credential_mode: static` / static credentials
- [x] `access_scope: local_only`
- [x] `process_sharing: per_instance` with isolation justification
- [x] Trusted-directory discovery + digest-bound exact executable
- [x] Approved secure local IPC + lazy activation
- [x] `bridge-node` / `private_companions` packaging
- [x] Forbidden `internal/plugins/backends/cursorsdk`
- [x] No root `package.json` / `@cursor/sdk`
- [x] No root `go.mod` require/replace of the connector
- [x] Coexistence with `cursorcliacp`
- [x] No failover / restart after first content
- [x] Process-tree cleanup / cancel escalation
- [x] Canonical history / agent pool / divergence recreate
- [x] Generic discovery/catalog/resolution (no Cursor types in core factory)
- [x] `ready_for_implementation: false` until a later product wave

## Contradiction bans (normative files)

- [x] `design.md`, `requirements.md`, `tasks.md`, `AGENTS.md`, `file-plan.md`, `packaging.md` must not select root-tree `internal/plugins/backends/cursorsdk` as the delivery path
- [x] `research.md` Recommended Design Direction must prescribe `connectors/cursorsdk` and mark the old internal path superseded
