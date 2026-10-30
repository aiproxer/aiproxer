# ADR 0008: Versioned runtime configuration reload

## Status

Accepted (spec `versioned-runtime-reloadable-proxy-configuration`). Implementation through Phase 5 composition is in tree; Phase 6 certifies and documents the operator contract.

## Context

Stage four ([ADR 0006](0006-stage-four-extension-seam-map-and-migration.md)) kept reload as an explicit non-goal while requiring immutable request snapshots. Operators still need to change routing, plugin rows, and request-plane policy without restarting the process or dropping keep-alive/SSE connections.

## Decision

- Reload only on explicit `SIGHUP` (where available) or an authenticated management API call / `pkg/aipruntime.Reload`. No watcher, mtime poller, or automatic retry.
- Re-read only the startup-fixed absolute config path. Bound reads to 2 MiB; strict one-document YAML; require atomic path replacement for changed content.
- Compile a complete immutable request-plane generation beside the active one; publish by atomic active-pointer swap; pin in-flight work to the generation it acquired.
- Keep process services (stores, metrics/tracing providers, data-plane and management listeners, capacity limiters) outside generation bundles.
- Classify every config section as reloadable or restart-required; mixed candidates fail closed as one transaction.
- Management listener is **opt-in** via `AIP_RELOAD_MANAGEMENT_ADDRESS` (recommended `127.0.0.1:9090`). Strong dedicated bearer (`AIP_RELOAD_MANAGEMENT_TOKEN`, ≥16 Unicode code points) is required for multi-user or non-loopback; single-user loopback may use local trust. Cookie/data-plane keys never authorize reload.
- `check-config` shares the generation compiler in dry-run/rollback mode (no publish).
- Default retained-generation budget is 8; retention pressure rejects new publication without terminating pinned streams.
- `Runtime.Close` serializes close, honors caller deadlines, and is retryable after failed/deadline teardown (not `sync.Once`).

## Consequences

- Operator guide: [runtime-config-reload.md](../runtime-config-reload.md).
- Package map: `internal/core/configreload`, `internal/infra/configsource`, `internal/infra/runtimehost`, `internal/stdhttp/admin/configreload`, `runtimebundle.BuildHost` (binds the reload coordinator internally; superseded the two-step `AttachReloadHost` attachment at Task 5.5), `cmd/aipstd` signal/management adapters, `pkg/aipruntime` Reload/Status facade.
- ADR 0006 non-goals remain historically correct for stage four; this ADR owns the reload feature boundary.

## Related

- Spec requirements/design/tasks under `.kiro/specs/archive/versioned-runtime-reloadable-proxy-configuration/`
- [architecture.md](../architecture.md), [runtime-flow.md](../runtime-flow.md), [release-gates.md](../release-gates.md)
