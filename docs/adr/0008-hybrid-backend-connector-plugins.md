# ADR 0008: Hybrid backend connector plugins (essential builtins + executable gRPC)

## Status

Accepted. Supersedes ADR 0001 for **optional backend connector composition**. ADR 0001 remains in force for essential/static frontend, feature, and essential-backend registry tables.

## Context

Optional provider/local/agent backends previously lived in the root module under `internal/plugins/backends/` and entered the binary through fixed registration tables. That forced every optional connector's dependency graph into the standard distribution, prevented independent release of connectors, and left steering/ADRs describing a static-only plugin model.

AIProxer still needs a small, race-detector-friendly essential set compiled into `cmd/aipstd`. Optional connectors must load as separate artifacts without Go native shared-object plugins, without open-ended manifests, and without core owning provider SDKs or B2BUA/orchestration.

## Decision

### Hybrid composition

- **Essential builtins** stay statically linked and registered through `internal/standardplugins` essential tables (`EssentialBackendBundle` / `EssentialBackendKinds`): the five hosted families plus approved dependency-free custom-compatible kinds.
- **Optional backends** are **executable connector plugins** under independent Go modules in `connectors/` (with shared support in `connector-support/` when needed). They are discovered from trusted roots via closed manifests and registered at composition time as discovered factories — not by editing a fixed optional table in the root module.
- Core continues to own orchestration, routing, failover, and B2BUA continuity. Connectors implement provider semantics behind the public backend-plugin ABI (`pkg/aipsdk/backendplugin`, `api/backendplugin/v1`).

### Rejected: Go native `plugin`

Do not use Go's native `plugin` package (shared objects). Optional connectors are separate OS processes speaking a versioned gRPC ABI over approved local IPC profiles.

### Manifest, launch, and trust

- Manifest schema is **closed**: unknown fields and undeclared capability claims fail closed.
- Launch binds an **exact executable artifact** verified by content digest (not pathname-only trust).
- Host uses **secure local IPC** profiles per platform (peer identity / protected channels as implemented by the process host); unauthorized local peers cannot configure or receive credential-bearing responses.
- Activation is **lazy**: discovery and inspect do not launch every plugin; processes start when a configured instance needs them.
- Each connector **declares its process model** (lifecycle, descendants, shutdown/kill expectations). The host supervises that declared model.

### Modules and registration

- Optional connector and support code live outside the root production import graph (`GOWORK=off` root builds without `connectors/` / `connector-support/`).
- Optional kinds register only through **manifest-driven discovery** into the composition-root registry (`BackendSourceDiscovered`). Maintainers must not add optional connectors to essential/`standard_table` fixed tables.
- Frontends and feature plugins remain explicit static registrations for the standard distribution unless a future ADR changes that surface.

## Consequences

- Authoritative docs (`AGENTS.md`, `.kiro/steering/`, architecture docs, EchoesVault) describe hybrid composition and link this ADR.
- Architecture tests forbid stale “static-only optional backends” and in-tree optional package paths in those sources.
- Operators gain optional kinds by installing manifests and digests against an unchanged `aipstd` binary.

## Related

- ADR 0001 — registry-driven composition (essential/static surfaces)
- `docs/backend-plugins/authoring.md` — connector authoring
- `EchoesVault/pages/backend-connector-plugins.md` — compiled concept page
